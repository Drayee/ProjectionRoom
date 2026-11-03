package service

import (
	"context"
	"log"
	"sync"
	"time"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/store"
)

// 注销冷静期的后台清扫（决议 #51 §5 / 票 #73）。
//
// 这一层存在的唯一理由是铁律 I4：**任何 DB 写入都不在 WS/REST 处理路径上**。
// 到期账号的真删是一个批量 DELETE，它绝不能在用户请求里发生（一个 30 天没
// 上线的账号不该让某个正在刷帖子的人等一次全表扫描）。
//
// 三个设计取舍：
//
//  1. **不在 NewDeletionSweeper 里起协程**：wire provider 不该产生看不见的
//     后台生命周期（单测里会泄漏）。生产在 `handler.NewRouter` 里显式
//     `Start()`，与 `service.Manager` 同一套约定。
//  2. **分批删而不是一条大 DELETE**：`PurgeExpiredDeletions` 带 limit，
//     循环到某批返回 0 为止。一次删光会在账号堆积时锁住表；分批让每批
//     事务短、其它写入不被饿死。
//  3. **单轮内的批与批之间不 sleep**：正常情况下一批就删完（到期账号很少），
//     多余的等待只会让"到期后立刻删"变得不可预测。只有在真的删满一批时
//     才继续下一批，那是"确实还有"的信号。
//
// 与 Manager 的清扫的差别：那个遍历内存（房间数上限 256，成本可忽略），
// 这个要打数据库 —— 所以周期默认 1h 而不是 1m。

// deletionSweeperStore 是清扫器需要的最小能力。
//
// 刻意**不**复用 AccountStore：那是"账号用例"的能力集合（它会去算口令、
// 发邮件、签会话），而清扫器只需要"数一数、删一批"。依赖越窄，
// 越不容易被人顺手在清扫路径上加一次 bcrypt。
type deletionSweeperStore interface {
	PurgeExpiredDeletions(ctx context.Context, cutoff time.Time, limit int) (int64, error)
	CountExpiredDeletions(ctx context.Context, cutoff time.Time) (int64, error)
}

// DeletionSweeper 按周期把到期的注销账号真删。
type DeletionSweeper struct {
	store deletionSweeperStore
	grace time.Duration
	// interval 是扫描周期；<= 0 时回落到 config 默认值。
	interval time.Duration
	// batch 是单批删除上限；<= 0 时回落到 config 默认值。
	batch int

	now func() time.Time

	stopCh    chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once
}

// DefaultDeletionPurgeBatch 是单批删除上限。
//
// 200 的依据：到期账号在正常运营里是"每天个位数"，200 意味着**一轮就删完**；
// 而万一出现堆积（比如一次配置错误把冷静期设成 1 分钟），分批也不会
// 让单条语句锁表太久。
const DefaultDeletionPurgeBatch = 200

// NewDeletionSweeper 构造清扫器。**不起协程**（见文件头第 1 条）。
func NewDeletionSweeper(st deletionSweeperStore, grace, interval time.Duration) *DeletionSweeper {
	if interval <= 0 {
		interval = config.DefaultDeletionSweepInterval
	}
	return &DeletionSweeper{
		store:    st,
		grace:    grace,
		interval: interval,
		batch:    DefaultDeletionPurgeBatch,
		now:      time.Now,
		stopCh:   make(chan struct{}),
	}
}

// Start 启动后台清扫。可重复调用（第二次是 no-op）。
func (d *DeletionSweeper) Start() {
	if d == nil || d.store == nil {
		return
	}
	d.startOnce.Do(func() { go d.sweepLoop() })
}

// Stop 停止后台清扫。可重复调用。
func (d *DeletionSweeper) Stop() {
	if d == nil {
		return
	}
	d.stopOnce.Do(func() { close(d.stopCh) })
}

func (d *DeletionSweeper) sweepLoop() {
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	for {
		select {
		case <-d.stopCh:
			return
		case <-ticker.C:
			// 错误只记日志：清扫失败不该让进程退出（下一轮会重试），
			// 也不该冒泡到任何请求路径上（这里根本没有请求）。
			if _, err := d.SweepOnce(context.Background()); err != nil {
				log.Printf("[WARN] 注销清扫失败（下一轮重试）：%v", err)
			}
		}
	}
}

// SweepOnce 执行一轮清扫，返回本轮删掉的账号数。
//
// cutoff = now - grace：所有 `deletion_requested_at <= cutoff` 的账号都已到期。
// 分批循环直到某批返回 0 —— 那才是"确实没有更多了"的信号（用 hasMore 之类的
// 判据需要额外一次查询，而这里返回数本身就够）。
func (d *DeletionSweeper) SweepOnce(ctx context.Context) (int64, error) {
	if d == nil || d.store == nil {
		return 0, nil
	}
	cutoff := d.now().Add(-d.grace)

	var total int64
	for {
		n, err := d.store.PurgeExpiredDeletions(ctx, cutoff, d.batch)
		if err != nil {
			return total, err
		}
		total += n
		if n < int64(d.batch) {
			break
		}
		// 删满一批 = 很可能还有，继续；否则结束。
		select {
		case <-d.stopCh:
			// 被要求停止时不要卡在一个很长的循环里：返回已删的数量。
			return total, nil
		default:
		}
	}

	if total > 0 {
		log.Printf("[INFO] 注销清扫：已真删 %d 个到期账号（冷静期 %s）", total, d.grace)
	}
	return total, nil
}

// CountPending 返回当前"已到期但还没删"的账号数（监控与测试用）。
func (d *DeletionSweeper) CountPending(ctx context.Context) (int64, error) {
	if d == nil || d.store == nil {
		return 0, nil
	}
	return d.store.CountExpiredDeletions(ctx, d.now().Add(-d.grace))
}

// 编译期断言：真 store 满足清扫器的最小能力。
var _ deletionSweeperStore = (*store.Store)(nil)
