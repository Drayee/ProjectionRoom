package store

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"gorm.io/gorm"
)

// 零值语义与内部安全阀（都在 NewWriter 的注释里说明）。
const (
	defaultQueueSize      = 1024
	defaultRetryBaseDelay = 100 * time.Millisecond
	// maxRetryDelay 是退避上限：基数很小(100ms)时 2^n 很快就会大到没意义，
	// 而"队列被一个坏作业占着"比"重试得不够勤"更糟。
	maxRetryDelay = time.Second
	// jobAttemptTimeout 是单次尝试的上限：一个卡死的作业（比如连接池假死）
	// 不能把后面所有事件一起拖住（队头阻塞），超时后当作一次失败进入重试。
	jobAttemptTimeout = 10 * time.Second
)

// WriterOptions 是事件写入器的构造参数（ACCOUNTS §9 的"事件类"）。
//
// 零值语义（刻意不藏默认值，全部写在字段上）：
//   - QueueSize  <= 0 → defaultQueueSize(1024)；
//   - MaxRetries <  0 → 0；MaxRetries == 0 就是"不重试"（这是有意义的取值，
//     所以不把它当"未设置"）；
//   - RetryBaseDelay <= 0 → defaultRetryBaseDelay(100ms)；
//   - OnError 可为 nil。
type WriterOptions struct {
	QueueSize      int
	MaxRetries     int
	RetryBaseDelay time.Duration
	OnError        func(error)
}

// WriterStats 是写入器的可观测指标（§9：队列长度/丢弃计数/最近写入耗时）。
//
// 字段语义（别把它们当成同一类量）：
//   - Queued / Dropped / Succeeded / Failed 都是**累计计数器**（进程生命周期内单调递增）；
//   - Queued 是"累计被接受的作业数"，不是当前队列长度；
//   - 当前队列长度用 Writer.QueueLen()（管理端指标要用它）；
//   - LastLatency 是最近一次**成功**落库的尝试耗时（不含退避等待）：
//     失败与超时不该污染这个指标，否则它反映的是"坏作业"而不是"DB 快慢"。
type WriterStats struct {
	Queued      int
	Dropped     int
	Succeeded   int
	Failed      int
	LastLatency time.Duration
}

// job 是一次事件写入：在写协程里、一个事务内执行。
//
// 用类型别名（=）而不是新类型：Submit 的签名要与冻结接口逐字一致，
// 别名让两边是同一个类型，不需要任何转换。
type job = func(ctx context.Context, tx *gorm.DB) error

// Writer 是"事件类"写库的单写协程 + 有界队列（§9 / 不变量 I4）。
//
// 三条硬约束：
//  1. Submit 绝不阻塞调用方：队列满立即返回 false 并计入 Dropped；
//  2. 所有 DB 写都发生在写协程里（调用方只投递闭包，不执行任何 DB 操作）；
//  3. Close 把已入队的作业跑完（最多等 timeout），此后 Submit 一律返回 false。
//
// 生命周期：NewWriter 起协程 → 装配层投递 → 退出前 w.Close(timeout) → s.Close()。
// 先 w.Close 再 s.Close 的顺序不能反：连接池关掉后写协程只会一直重试失败。
type Writer struct {
	db    *gorm.DB
	opts  WriterOptions
	queue chan job

	stop    chan struct{} // 关闭信号：写协程排空队列后退出
	drained chan struct{} // 写协程退出后关闭（Close 等它）

	// gate 让"关闸"与"入队"互斥。若只用 atomic 标志，会出现这样的窗口：
	// Submit 刚刚通过检查、还没塞进队列，写协程已经排空并退出 —— 那个作业
	// 就永远丢了，而且调用方拿到的还是 true。用同一把锁排除它。
	gate   sync.Mutex
	closed atomic.Bool

	statsMu sync.Mutex
	stats   WriterStats
}

// NewWriter 起写协程并返回写入器。它只借用 db 的连接池，不接管其生命周期。
//
// db 为 nil 直接 panic：写协程里再发现 nil 就是一次进程级崩溃（而且栈里看不出
// 是谁装配错了），在装配点立刻失败更好定位。
func NewWriter(db *gorm.DB, opts WriterOptions) *Writer {
	if db == nil {
		panic("store: NewWriter 需要非 nil 的 *gorm.DB")
	}
	if opts.QueueSize <= 0 {
		opts.QueueSize = defaultQueueSize
	}
	if opts.MaxRetries < 0 {
		opts.MaxRetries = 0
	}
	if opts.RetryBaseDelay <= 0 {
		opts.RetryBaseDelay = defaultRetryBaseDelay
	}

	w := &Writer{
		db:      db,
		opts:    opts,
		queue:   make(chan job, opts.QueueSize),
		stop:    make(chan struct{}),
		drained: make(chan struct{}),
	}
	go w.loop()
	return w
}

// Submit 投递一个作业：返回 true = 已入队，false = 被丢弃（队列满或写入器已关闭）。
//
// 不阻塞、不返回错误 —— 这是 I4 的直接体现：调用方在 WS/REST 处理路径上，
// 没时间等数据库。丢弃要计入 Dropped 并被管理端看见（§9 可观测）。
//
// 传 nil 直接返回 false，但不计入 Dropped：Dropped 专指"队列满/已关闭导致事件
// 丢失"，nil 是调用方 bug，混进同一个计数器会让指标失去意义。
//
// 注意作业的 ctx 由写入器提供（见 exec），不是调用方的请求 ctx ——
// 否则请求一结束 ctx 就被取消，异步写必然失败。
func (w *Writer) Submit(fn job) bool {
	if fn == nil {
		return false
	}
	w.gate.Lock()
	defer w.gate.Unlock()

	if w.closed.Load() {
		w.bump(func(s *WriterStats) { s.Dropped++ })
		return false
	}
	select {
	case w.queue <- fn:
		w.bump(func(s *WriterStats) { s.Queued++ })
		return true
	default:
		w.bump(func(s *WriterStats) { s.Dropped++ })
		return false
	}
}

// Close 停止接收新作业并等待写协程把已入队的作业跑完。
//
// timeout <= 0 表示"只发关闭信号、不等待"（已排空的写入器仍返回 nil）；
// 超时返回错误并保留未完成的事实，由调用方决定是继续退出还是报警。
// Close 可以重复调用：第二次只是再等一次（已经排空则立即返回 nil）。
func (w *Writer) Close(timeout time.Duration) error {
	w.gate.Lock()
	if !w.closed.Load() {
		// 先关闸（此后 Submit 一律 false），再通知写协程排空。
		// 两步都在 gate 内完成，保证"最后一个被接受的作业"一定在队列里。
		w.closed.Store(true)
		close(w.stop)
	}
	w.gate.Unlock()

	if timeout <= 0 {
		select {
		case <-w.drained:
			return nil
		default:
			return fmt.Errorf("store: 写入器关闭超时（timeout=%s）：仍有未落库作业", timeout)
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-w.drained:
		return nil
	case <-timer.C:
		return fmt.Errorf("store: 写入器关闭超时（%s）：仍有未落库作业", timeout)
	}
}

// Stats 返回指标快照（可在任意协程调用：管理端指标接口会这么用）。
func (w *Writer) Stats() WriterStats {
	w.statsMu.Lock()
	defer w.statsMu.Unlock()
	return w.stats
}

// QueueLen 返回当前排队中的作业数（瞬时值，不是计数器）。
//
// 单独给一个方法而不是塞进 WriterStats.Queued（那是累计计数器），
// 是为了让"队列积压"这个 gauge 有明确的来源：管理端用它判断是否该降级/告警。
func (w *Writer) QueueLen() int {
	return len(w.queue)
}

// loop 是唯一的写协程：正常取作业执行；收到 stop 后排空剩余作业再退出。
func (w *Writer) loop() {
	defer close(w.drained)
	for {
		select {
		case fn := <-w.queue:
			w.run(fn)
		case <-w.stop:
			// 排空：stop 关闭后不会再有新的作业进来（Submit 被 gate + closed 挡住），
			// 所以"取到空"就意味着可以退出了。
			for {
				select {
				case fn := <-w.queue:
					w.run(fn)
				default:
					return
				}
			}
		}
	}
}

// run 执行一个作业，失败按指数退避重试，重试耗尽后计入 Failed 并回调 OnError。
//
// 重试次数语义：MaxRetries 是**额外**尝试次数，总尝试数 = 1 + MaxRetries。
func (w *Writer) run(fn job) {
	var lastErr error
	for attempt := 0; attempt <= w.opts.MaxRetries; attempt++ {
		if attempt > 0 {
			// 退避等待不响应关闭信号：Close 的 timeout 就是它的上界
			// （要么在 timeout 内跑完，要么 Close 返回超时错误）。
			time.Sleep(backoff(w.opts.RetryBaseDelay, attempt))
		}
		start := time.Now()
		err := w.exec(fn)
		if err == nil {
			latency := time.Since(start)
			w.bump(func(s *WriterStats) {
				s.Succeeded++
				s.LastLatency = latency
			})
			return
		}
		lastErr = err
	}

	w.bump(func(s *WriterStats) { s.Failed++ })
	if w.opts.OnError != nil {
		// 回调在写协程里执行：它必须轻量（打日志/计数），不能在这里再做 DB 操作，
		// 否则会把整个队列堵住。
		w.opts.OnError(fmt.Errorf("store: 异步写库失败（已重试 %d 次后放弃）: %w", w.opts.MaxRetries, lastErr))
	}
}

// exec 跑一次尝试：一个作业 = 一个事务。
//
// 为什么一个作业包在事务里：一次事件往往是"多行一起成立"的写入（例如消息 + 未读），
// 部分成功比整体失败更难收拾。失败即整体回滚，重试时不会留下半截数据。
func (w *Writer) exec(fn job) error {
	ctx, cancel := context.WithTimeout(context.Background(), jobAttemptTimeout)
	defer cancel()
	return w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(ctx, tx)
	})
}

// bump 在锁内改计数，保证 Stats() 拿到的是一致快照。
func (w *Writer) bump(f func(*WriterStats)) {
	w.statsMu.Lock()
	f(&w.stats)
	w.statsMu.Unlock()
}

// backoff 是确定性的指数退避：base << (attempt-1)，上限 maxRetryDelay。
//
// 不加随机抖动：这是单进程内的单写协程，不存在"惊群"，可预期性对测试与
// 运维排查都更重要（"每 100ms 重试一次"比"100ms ± 随机"更好读）。
func backoff(base time.Duration, attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := base
	for i := 1; i < attempt; i++ {
		if d >= maxRetryDelay {
			break
		}
		d *= 2
	}
	if d > maxRetryDelay {
		d = maxRetryDelay
	}
	return d
}
