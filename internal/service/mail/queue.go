package mail

import (
	"context"
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// 队列的默认参数。
const (
	// DefaultQueueSize 是队列容量。
	//
	// 128 的依据：发信是"每次找回/验证请求一封"的量级，正常峰值是个位数；
	// 128 足以吸收一次 SMTP 抖动（每次投递最多 ~10s），而内存占用是
	// 128 × 一封纯文本邮件（几百字节）。更大的队列没有收益 —— 队列越长，
	// "排到的信已经过期"的概率越高（令牌 TTL 是 30 分钟）。
	DefaultQueueSize = 128

	// DefaultSendTimeout 是单封邮件的投递预算（建连 + AUTH + DATA 全包）。
	DefaultSendTimeout = 10 * time.Second
)

// Stats 是投递计数（排障与验收用）。
//
// Dropped 是**验收判据之一**：需求要求"队列满时记日志 + 仍然返回 200"，
// 而"有没有真的丢"只能从计数上看出来。
type Stats struct {
	Enqueued int64
	Dropped  int64
	Sending  int64
	Sent     int64
	Failed   int64
	Rejected int64
}

// Options 是队列参数。
type Options struct {
	// QueueSize <= 0 取 DefaultQueueSize。
	QueueSize int
	// SendTimeout 是单封邮件的投递预算；<= 0 取 DefaultSendTimeout。
	SendTimeout time.Duration
	// OnError 是投递失败的通知（nil 时只打日志）。
	OnError func(tag string, err error)
}

// Mailer 是**有界队列 + 单写协程**的发信出口（不变量 I4 的落点）。
//
// 它存在的全部理由：把"发信"从请求路径上摘下来。SMTP 的一次完整投递包含
// TCP 建连、TLS 握手、AUTH、DATA —— 在公网上的实测耗时是几百毫秒到数秒，
// 而且会失败/重试。任何一处发生在 handler 里，都等于让一个免登录端点
// 拥有了"被外部依赖拖住"的能力。
//
// 为什么是**单写协程**而不是 worker 池：
//
//	SMTP 服务器对同一账号的并发连接通常有限制（QQ 尤其），而这里的量级是每分钟个位数；
//	一个协程足以跑满，多开只会把"撞上服务端并发限制"变成一个偶发故障。
//	需要吞吐时应该先换成本地 MTA（把信交给本机的 postfix，由它排队），
//	而不是在这里加协程 —— 那属于部署形态的变化，不是参数调整。
type Mailer struct {
	sender Sender
	opts   Options

	ch chan Outbound

	// mu 保护 closed 与 ch 的关闭：Enqueue 持读锁、Close 持写锁。
	//
	// 为什么不用"关闭 channel + 检查标志位"那套无锁写法：那样会留下
	// "检查完 closed 但发到已关闭 channel"的竞态（后者直接 panic）。
	// 读写锁把"发送"与"关闭"互斥掉，代价是每次 Enqueue 一把读锁 ——
	// 在没有争用的场景下这个代价可以忽略，而它换来的是显而易见的正确性。
	mu     sync.RWMutex
	closed bool

	wg    sync.WaitGroup
	stats Stats // 只由工作协程与 Close 写；对外读走 Stats() 的原子快照
}

// NewMailer 构造发信队列并启动单写协程。
//
// 返回的 cleanup 必须由装配层在**服务停止之后**调用（ProviderSet 的顺序保证这一点）：
// 它先关队列入口，再给工作协程一段 flush 时间。
//
// sender 为 nil 时返回 nil：调用方（装配层）据此表示"邮件能力未配置"，
// 与"账号能力关闭"是同一种显式降级（见 handler 的 503）。
func NewMailer(sender Sender, opts Options) (*Mailer, func()) {
	if sender == nil {
		return nil, func() {}
	}
	if opts.QueueSize <= 0 {
		opts.QueueSize = DefaultQueueSize
	}
	if opts.SendTimeout <= 0 {
		opts.SendTimeout = DefaultSendTimeout
	}

	m := &Mailer{
		sender: sender,
		opts:   opts,
		ch:     make(chan Outbound, opts.QueueSize),
	}
	m.wg.Add(1)
	go m.run()

	cleanup := func() { m.Close() }
	return m, cleanup
}

// Enqueue 把一封邮件放进队列；**永不阻塞**。
//
// 返回 false 表示这一封没有进队列（队列已满、或已关闭）。调用方**必须**照常
// 返回成功响应 —— 这个返回值只用于日志与指标，绝不是"要不要告诉用户"的依据：
// 让"队列满"变成对外可见的差异，就等于给攻击者一个探测服务端状态的反馈通道。
func (m *Mailer) Enqueue(msg Outbound) bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		atomic.AddInt64(&m.stats.Rejected, 1)
		return false
	}
	select {
	case m.ch <- msg:
		atomic.AddInt64(&m.stats.Enqueued, 1)
		return true
	default:
		// 队列满：丢弃 + 记账。日志在调用方打（它知道 user/purpose，本包不该知道）。
		atomic.AddInt64(&m.stats.Dropped, 1)
		return false
	}
}

// Stats 返回计数快照。
func (m *Mailer) Stats() Stats {
	if m == nil {
		return Stats{}
	}
	return Stats{
		Enqueued: atomic.LoadInt64(&m.stats.Enqueued),
		Dropped:  atomic.LoadInt64(&m.stats.Dropped),
		Sending:  atomic.LoadInt64(&m.stats.Sending),
		Sent:     atomic.LoadInt64(&m.stats.Sent),
		Failed:   atomic.LoadInt64(&m.stats.Failed),
		Rejected: atomic.LoadInt64(&m.stats.Rejected),
	}
}

// QueueLen 返回当前排队长度（瞬时 gauge）。
func (m *Mailer) QueueLen() int {
	if m == nil {
		return 0
	}
	return len(m.ch)
}

// drainTimeout 是 Close 给工作协程的 flush 预算。
const drainTimeout = 5 * time.Second

// Close 关闭队列并等待工作协程把已入队的邮件投完（最多 drainTimeout）。
//
// 幂等：重复调用只等一次。超时不是错误路径里的"失败"，而是"队列里还有没发完的信"，
// 如实记一条日志 —— 让退出时的丢弃变得可见，而不是静默消失。
func (m *Mailer) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	close(m.ch)
	m.mu.Unlock()

	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(drainTimeout):
		s := m.Stats()
		log.Printf("[WARN] 邮件队列退出超时（%s）：已投递 %d、失败 %d、仍未投递的仍在队列里",
			drainTimeout, s.Sent, s.Failed)
	}
}

// run 是唯一的写协程：range 到 channel 关闭为止（即把队列里的信发完再退）。
func (m *Mailer) run() {
	defer m.wg.Done()
	for msg := range m.ch {
		atomic.AddInt64(&m.stats.Sending, 1)
		ctx, cancel := context.WithTimeout(context.Background(), m.opts.SendTimeout)
		err := m.sender.Send(ctx, msg)
		cancel()
		atomic.AddInt64(&m.stats.Sending, -1)

		if err != nil {
			atomic.AddInt64(&m.stats.Failed, 1)
			if m.opts.OnError != nil {
				m.opts.OnError(msg.Tag, err)
			} else {
				// 只打 Tag：Body 里带一次性 token 明文。
				log.Printf("[WARN] 邮件投递失败（%s）：%v", msg.Tag, err)
			}
			continue
		}
		atomic.AddInt64(&m.stats.Sent, 1)
		// 成功也打一条：验收要的"服务端日志里的发信证据"就在这里。
		// 同样只打 Tag —— 收件地址是隐私，正文里是凭据。
		log.Printf("[INFO] 邮件已投递（%s）", msg.Tag)
	}
}

// ErrMailUnavailable 是"邮件能力未配置"的哨兵（本包与 service 共用一个语义）。
var ErrMailUnavailable = errors.New("mail: 邮件能力未配置")
