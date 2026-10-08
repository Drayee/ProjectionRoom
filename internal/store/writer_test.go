package store

import (
	"context"
	"errors"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
)

// noopJob 什么都不做（用于只关心队列行为的用例）。
func noopJob(ctx context.Context, tx *gorm.DB) error { return nil }

// goroutineID 从 runtime.Stack 的头部抠出当前协程号（只有测试用）。
//
// 为什么要这么干：T5 的硬约束是"DB 写在写协程里执行"。用"调用方协程号 ≠
// 作业体里的协程号"来证明它，比"看起来是异步"要硬。
func goroutineID() int64 {
	buf := make([]byte, 64)
	n := runtime.Stack(buf, false)
	fields := strings.Fields(string(buf[:n]))
	if len(fields) < 2 {
		return -1
	}
	id, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return -1
	}
	return id
}

func TestNewWriterPanicsOnNilDB(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatalf("nil *gorm.DB 应该 panic（装配点立刻失败，而不是在写协程里崩）")
		}
	}()
	NewWriter(nil, WriterOptions{})
}

func TestNewWriterNormalizesOptions(t *testing.T) {
	s := requireTestDB(t)
	w := NewWriter(s.DB(), WriterOptions{})
	t.Cleanup(func() { _ = w.Close(time.Second) })

	if cap(w.queue) != defaultQueueSize {
		t.Fatalf("QueueSize<=0 应取默认 %d，实际 %d", defaultQueueSize, cap(w.queue))
	}
	if w.opts.MaxRetries != 0 {
		t.Fatalf("MaxRetries=0 就是「不重试」，不该被改成别的（实际 %d）", w.opts.MaxRetries)
	}
	if w.opts.RetryBaseDelay != defaultRetryBaseDelay {
		t.Fatalf("RetryBaseDelay<=0 应取默认 %s，实际 %s", defaultRetryBaseDelay, w.opts.RetryBaseDelay)
	}

	w2 := NewWriter(s.DB(), WriterOptions{QueueSize: 4, MaxRetries: -3, RetryBaseDelay: -1})
	t.Cleanup(func() { _ = w2.Close(time.Second) })
	if cap(w2.queue) != 4 {
		t.Fatalf("QueueSize 应保持 4，实际 %d", cap(w2.queue))
	}
	if w2.opts.MaxRetries != 0 {
		t.Fatalf("负数 MaxRetries 应收敛到 0，实际 %d", w2.opts.MaxRetries)
	}
	if w2.opts.RetryBaseDelay != defaultRetryBaseDelay {
		t.Fatalf("负数退避基数应取默认值，实际 %s", w2.opts.RetryBaseDelay)
	}
}

func TestBackoffIsExponentialAndCapped(t *testing.T) {
	base := 10 * time.Millisecond
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, base}, // 防御性：attempt<1 按第一次算
		{1, base}, // 第一次重试 = 基数
		{2, 20 * time.Millisecond},
		{3, 40 * time.Millisecond},
		{4, 80 * time.Millisecond},
		{64, maxRetryDelay}, // 不溢出、不无限增长
	}
	for _, c := range cases {
		if got := backoff(base, c.attempt); got != c.want {
			t.Fatalf("backoff(base, %d) = %s，期望 %s", c.attempt, got, c.want)
		}
	}
	if got := backoff(maxRetryDelay*4, 1); got != maxRetryDelay {
		t.Fatalf("基数超过上限时应被截到 %s，实际 %s", maxRetryDelay, got)
	}
}

// TestWriterRunsJobsInWriterGoroutine 同时证明两件事：
//  1. 作业体不在调用方协程里执行（硬约束 2）；
//  2. 永远只有一个作业在跑（单写协程 → 串行，不需要额外加锁保护写路径）。
func TestWriterRunsJobsInWriterGoroutine(t *testing.T) {
	s := requireTestDB(t)
	w := NewWriter(s.DB(), WriterOptions{QueueSize: 8, MaxRetries: 0})
	t.Cleanup(func() { _ = w.Close(time.Second) })

	callerGID := goroutineID()
	const jobs = 5
	done := make(chan int64, jobs)

	var (
		mu          sync.Mutex
		inFlight    int
		maxInFlight int
		gids        []int64
	)
	for i := 0; i < jobs; i++ {
		if !w.Submit(func(ctx context.Context, tx *gorm.DB) error {
			gid := goroutineID()
			mu.Lock()
			gids = append(gids, gid)
			inFlight++
			if inFlight > maxInFlight {
				maxInFlight = inFlight
			}
			mu.Unlock()

			time.Sleep(3 * time.Millisecond) // 制造重叠机会：并发执行的话 maxInFlight 会 >1

			mu.Lock()
			inFlight--
			mu.Unlock()
			done <- gid
			return nil
		}) {
			t.Fatalf("第 %d 个作业被丢弃", i)
		}
	}
	for i := 0; i < jobs; i++ {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("第 %d 个作业没有完成", i)
		}
	}
	if err := w.Close(5 * time.Second); err != nil {
		t.Fatalf("Close 失败：%v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if maxInFlight != 1 {
		t.Fatalf("单写协程应该串行执行，实际最大并发 %d", maxInFlight)
	}
	for i, gid := range gids {
		if gid == callerGID {
			t.Fatalf("第 %d 个作业跑在调用方协程里（gid=%d）：DB 写必须在写协程执行", i, gid)
		}
	}
	if len(gids) != jobs || w.Stats().Succeeded != jobs {
		t.Fatalf("应有 %d 个作业成功，实际 gids=%d stats=%+v", jobs, len(gids), w.Stats())
	}
}

// TestWriterSubmitNeverBlocksWhenQueueFull 覆盖"队列满不阻塞 + 计入丢弃"。
func TestWriterSubmitNeverBlocksWhenQueueFull(t *testing.T) {
	s := requireTestDB(t)
	started := make(chan struct{})
	gate := make(chan struct{})
	// 注意：startedOnce 与 gateOnce 必须是两个 Once。
	// 共用一个 Once 的话，作业体里的 once.Do(close(started)) 会先把 Once 标记为
	// "已完成"，release() 里的 close(gate) 就永远不会执行 —— 作业永久卡住。
	var startedOnce, gateOnce sync.Once
	release := func() { gateOnce.Do(func() { close(gate) }) }
	t.Cleanup(release)

	w := NewWriter(s.DB(), WriterOptions{QueueSize: 1, MaxRetries: 0})
	t.Cleanup(func() { release(); _ = w.Close(5 * time.Second) })

	// 第一个作业占住写协程（确认它真的开始跑了，队列才是空的）。
	if !w.Submit(func(ctx context.Context, tx *gorm.DB) error {
		startedOnce.Do(func() { close(started) })
		<-gate
		return nil
	}) {
		t.Fatalf("第一个作业应该被接受")
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatalf("写协程没有开始执行第一个作业")
	}

	// 队列深度 1：还能再进一个。
	if !w.Submit(noopJob) {
		t.Fatalf("队列还有空位时不该拒绝")
	}
	if got := w.QueueLen(); got != 1 {
		t.Fatalf("队列长度应该是 1，实际 %d", got)
	}

	// 队列已满：必须立即返回 false。
	start := time.Now()
	accepted := w.Submit(noopJob)
	elapsed := time.Since(start)
	if accepted {
		t.Fatalf("队列满时 Submit 必须返回 false")
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("Submit 阻塞了 %s：它必须在处理路径上立即返回", elapsed)
	}

	stats := w.Stats()
	if stats.Queued != 2 || stats.Dropped != 1 {
		t.Fatalf("计数不对：%+v（期望 Queued=2 Dropped=1）", stats)
	}

	release()
	if err := w.Close(5 * time.Second); err != nil {
		t.Fatalf("Close 失败：%v", err)
	}
	stats = w.Stats()
	if stats.Succeeded != 2 || stats.Failed != 0 {
		t.Fatalf("门后那一个作业也该跑完：%+v", stats)
	}
	if stats.LastLatency <= 0 {
		t.Fatalf("LastLatency 应该是最近一次成功写入的耗时，实际 %s", stats.LastLatency)
	}
	// 丢弃的作业不计入 Succeeded（Queued = Succeeded + Failed + 仍在队列/在跑）。
	if stats.Queued != stats.Succeeded+stats.Failed {
		t.Fatalf("队列已排空，Queued 应等于 Succeeded+Failed：%+v", stats)
	}
}

// TestWriterCloseFlushesQueuedJobs 覆盖"Close 之后已入队可见"：
// 用真实的 INSERT 证明事务可用、flush 完整、之后再投递一律被拒。
func TestWriterCloseFlushesQueuedJobs(t *testing.T) {
	s := requireTestDB(t)
	w := NewWriter(s.DB(), WriterOptions{QueueSize: 64, MaxRetries: 2, RetryBaseDelay: time.Millisecond})
	t.Cleanup(func() { _ = w.Close(2 * time.Second) })

	const jobs = 20
	stopReading := make(chan struct{})
	var readerWG sync.WaitGroup
	readerWG.Add(1)
	go func() { // 并发读指标：race 检测器会用它验证 Stats/QueueLen 的线程安全
		defer readerWG.Done()
		for {
			select {
			case <-stopReading:
				return
			default:
				_ = w.Stats()
				_ = w.QueueLen()
			}
		}
	}()

	for i := 0; i < jobs; i++ {
		target := strconv.Itoa(i)
		if !w.Submit(func(ctx context.Context, tx *gorm.DB) error {
			return tx.Exec(
				`INSERT INTO admin_audit (actor_id, action, target_type, target_id) VALUES (?, ?, ?, ?)`,
				1, "flush.probe", "probe", target,
			).Error
		}) {
			t.Fatalf("第 %d 个作业被丢弃（队列容量 64，不该发生）", i)
		}
	}

	if err := w.Close(30 * time.Second); err != nil {
		t.Fatalf("Close 失败：%v", err)
	}
	close(stopReading)
	readerWG.Wait()

	var rows int64
	if err := s.DB().Model(&AdminAudit{}).Where("action = ?", "flush.probe").Count(&rows).Error; err != nil {
		t.Fatalf("统计 flush 结果失败：%v", err)
	}
	if rows != jobs {
		t.Fatalf("Close 之后应该有 %d 行落库，实际 %d", jobs, rows)
	}
	stats := w.Stats()
	if stats.Succeeded != jobs || stats.Failed != 0 {
		t.Fatalf("统计不对：%+v（期望 Succeeded=%d）", stats, jobs)
	}
	t.Logf("Close 排空证据：提交 %d 个作业 → 落库 %d 行；stats=%+v", jobs, rows, stats)

	// Close 之后 Submit 一律 false，并且计入 Dropped（事件确实丢了，指标要看得见）。
	if w.Submit(noopJob) {
		t.Fatalf("Close 之后 Submit 必须返回 false")
	}
	if got := w.Stats().Dropped; got != 1 {
		t.Fatalf("关闭后被拒的投递应计入 Dropped，实际 %d", got)
	}
	// Close 幂等。
	if err := w.Close(time.Second); err != nil {
		t.Fatalf("重复 Close 应返回 nil，实际 %v", err)
	}
}

// TestWriterCloseTimesOutWhileJobHangs：Close 的等待上限必须真实生效。
func TestWriterCloseTimesOutWhileJobHangs(t *testing.T) {
	s := requireTestDB(t)
	started := make(chan struct{})
	gate := make(chan struct{})
	// startedOnce / gateOnce 必须分开（理由见上一个用例）。
	var startedOnce, gateOnce sync.Once
	release := func() { gateOnce.Do(func() { close(gate) }) }
	t.Cleanup(release)

	w := NewWriter(s.DB(), WriterOptions{QueueSize: 4, MaxRetries: 0})
	t.Cleanup(func() { release(); _ = w.Close(5 * time.Second) })

	if !w.Submit(func(ctx context.Context, tx *gorm.DB) error {
		startedOnce.Do(func() { close(started) })
		<-gate
		return nil
	}) {
		t.Fatalf("作业应该被接受")
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatalf("写协程没有开始执行作业")
	}

	err := w.Close(200 * time.Millisecond)
	if err == nil {
		t.Fatalf("作业卡住时 Close 应该返回超时错误")
	}
	if !strings.Contains(err.Error(), "超时") {
		t.Fatalf("错误信息应说明超时，实际 %v", err)
	}
	t.Logf("Close 超时错误：%v", err)

	// timeout<=0 = 只发信号不等待：此时仍未排空 → 立即返回错误。
	if err := w.Close(0); err == nil {
		t.Fatalf("timeout<=0 且未排空时应该返回错误")
	}

	release()
	if err := w.Close(5 * time.Second); err != nil {
		t.Fatalf("排空之后重复 Close 应返回 nil，实际 %v", err)
	}
}

// TestWriterRetriesThenSucceeds：指数退避重试成功（含尝试次数断言）。
func TestWriterRetriesThenSucceeds(t *testing.T) {
	s := requireTestDB(t)
	var attempts int32
	w := NewWriter(s.DB(), WriterOptions{QueueSize: 4, MaxRetries: 3, RetryBaseDelay: time.Millisecond})
	t.Cleanup(func() { _ = w.Close(2 * time.Second) })

	done := make(chan struct{}, 1)
	if !w.Submit(func(ctx context.Context, tx *gorm.DB) error {
		if n := atomic.AddInt32(&attempts, 1); n < 3 {
			return errors.New("临时失败")
		}
		done <- struct{}{}
		return nil
	}) {
		t.Fatalf("作业应该被接受")
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("作业没有在重试后成功")
	}
	if err := w.Close(5 * time.Second); err != nil {
		t.Fatalf("Close 失败：%v", err)
	}

	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("前两次失败、第三次成功 → 应尝试 3 次，实际 %d", got)
	}
	stats := w.Stats()
	if stats.Succeeded != 1 || stats.Failed != 0 || stats.Dropped != 0 {
		t.Fatalf("重试后成功应只计一次 Succeeded：%+v", stats)
	}
}

// TestWriterExhaustsRetriesThenFails：重试耗尽 → 计入 Failed + 回调 OnError，
// 并且整个作业回滚（不留半截数据）。
func TestWriterExhaustsRetriesThenFails(t *testing.T) {
	s := requireTestDB(t)
	var attempts int32
	errs := make(chan error, 4)
	w := NewWriter(s.DB(), WriterOptions{
		QueueSize:      4,
		MaxRetries:     2,
		RetryBaseDelay: time.Millisecond,
		OnError:        func(err error) { errs <- err },
	})
	t.Cleanup(func() { _ = w.Close(2 * time.Second) })

	if !w.Submit(func(ctx context.Context, tx *gorm.DB) error {
		atomic.AddInt32(&attempts, 1)
		// 先写一行再失败：用来证明"一个作业 = 一个事务"，失败即整体回滚。
		if err := tx.Exec(
			`INSERT INTO admin_audit (actor_id, action, target_type, target_id) VALUES (?, ?, ?, ?)`,
			1, "retry.probe", "probe", "x",
		).Error; err != nil {
			return err
		}
		return errors.New("永久失败")
	}) {
		t.Fatalf("作业应该被接受")
	}

	var onErr error
	select {
	case onErr = <-errs:
	case <-time.After(10 * time.Second):
		t.Fatalf("重试耗尽后必须调用 OnError")
	}
	if !strings.Contains(onErr.Error(), "重试 2 次") {
		t.Fatalf("OnError 的错误信息应说明重试次数，实际 %v", onErr)
	}
	t.Logf("OnError 回调内容：%v", onErr)

	if err := w.Close(5 * time.Second); err != nil {
		t.Fatalf("Close 失败：%v", err)
	}

	select {
	case extra := <-errs:
		t.Fatalf("OnError 不该被调用两次：%v", extra)
	default:
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("总尝试数应是 1+MaxRetries=3，实际 %d", got)
	}
	stats := w.Stats()
	if stats.Failed != 1 || stats.Succeeded != 0 {
		t.Fatalf("失败的作业应只计一次 Failed：%+v", stats)
	}

	var rows int64
	if err := s.DB().Model(&AdminAudit{}).Where("action = ?", "retry.probe").Count(&rows).Error; err != nil {
		t.Fatalf("统计重试痕迹失败：%v", err)
	}
	if rows != 0 {
		t.Fatalf("失败的作业必须整体回滚，实际留下 %d 行", rows)
	}
	t.Logf("重试耗尽证据：尝试 %d 次、失败计数 %d、DB 残留 %d 行", atomic.LoadInt32(&attempts), stats.Failed, rows)
}

// TestWriterJobTimeoutBoundsAttempt：单次尝试的超时上限（防队头阻塞）由内部常量控制，
// 这里只验证"作业拿到的 ctx 有 deadline"，不真等 10 秒。
func TestWriterJobTimeoutBoundsAttempt(t *testing.T) {
	s := requireTestDB(t)
	w := NewWriter(s.DB(), WriterOptions{QueueSize: 2, MaxRetries: 0})
	t.Cleanup(func() { _ = w.Close(2 * time.Second) })

	got := make(chan bool, 1)
	if !w.Submit(func(ctx context.Context, tx *gorm.DB) error {
		_, hasDeadline := ctx.Deadline()
		got <- hasDeadline
		return nil
	}) {
		t.Fatalf("作业应该被接受")
	}
	select {
	case hasDeadline := <-got:
		if !hasDeadline {
			t.Fatalf("作业 ctx 应带超时上限（jobAttemptTimeout）")
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("作业没有执行")
	}
	if err := w.Close(5 * time.Second); err != nil {
		t.Fatalf("Close 失败：%v", err)
	}
}
