package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 注销清扫器（票 #73）的单元证据。
//
// 这一层不该在真库上测"删得对不对"（那是 store 的 `TestPurgeExpiredDeletions`），
// 它要守的是**清扫器的形状**：
//
//   - cutoff 是 `now - grace`，不是 `now`（差一天就会把还没到期的账号删掉，
//     而那正是这个功能最不能出的错）；
//   - 分批循环：一批删满时继续下一批，直到某批没删满为止；
//   - 错误向上返回（不吞），但**不 panic**（清扫失败不该让进程退出）；
//   - Stop 之后循环退出（可重复调用）。

// fakePurgeStore 是清扫器的最小假实现。
type fakePurgeStore struct {
	// pending 是"当前到期的账号数"，每批删 min(batch, pending) 个。
	pending int64
	// calls 记录每次调用看到的 cutoff，用来判"传进来的是 now-grace"。
	cutoffs []time.Time
	// failAfter 在指定次数后开始报错（0 = 不报错）。
	failAfter int
	calls     int
}

func (f *fakePurgeStore) PurgeExpiredDeletions(_ context.Context, cutoff time.Time, limit int) (int64, error) {
	f.calls++
	f.cutoffs = append(f.cutoffs, cutoff)
	if f.failAfter > 0 && f.calls > f.failAfter {
		return 0, errors.New("boom")
	}
	n := int64(limit)
	if n > f.pending {
		n = f.pending
	}
	f.pending -= n
	return n, nil
}

func (f *fakePurgeStore) CountExpiredDeletions(_ context.Context, cutoff time.Time) (int64, error) {
	f.cutoffs = append(f.cutoffs, cutoff)
	return f.pending, nil
}

// cutoff 必须是 now - grace（不是 now）。
func TestDeletionSweeperUsesGraceAdjustedCutoff(t *testing.T) {
	grace := 30 * 24 * time.Hour
	fixedNow := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	fs := &fakePurgeStore{pending: 1}
	d := NewDeletionSweeper(fs, grace, time.Hour)
	d.now = func() time.Time { return fixedNow }

	if _, err := d.SweepOnce(context.Background()); err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}
	if len(fs.cutoffs) == 0 {
		t.Fatal("应当至少调用一次 PurgeExpiredDeletions")
	}
	want := fixedNow.Add(-grace)
	if !fs.cutoffs[0].Equal(want) {
		t.Fatalf("cutoff 应为 now-grace：got=%s want=%s", fs.cutoffs[0], want)
	}
}

// 分批循环：pending 超过一批时要继续删，直到清空。
func TestDeletionSweeperPurgesInBatches(t *testing.T) {
	fs := &fakePurgeStore{pending: 450}
	d := NewDeletionSweeper(fs, time.Hour, time.Hour)
	d.batch = 200

	n, err := d.SweepOnce(context.Background())
	if err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}
	if n != 450 {
		t.Fatalf("应删掉全部 450 个，实得 %d", n)
	}
	// 450 = 200 + 200 + 50 → 三次调用（第三次没删满，循环结束）。
	if fs.calls != 3 {
		t.Fatalf("应分三批删（200+200+50），实际调用 %d 次", fs.calls)
	}
}

// 没到期的一个都不删（pending=0 时只调一次就结束）。
func TestDeletionSweeperNoOpWhenNothingPending(t *testing.T) {
	fs := &fakePurgeStore{pending: 0}
	d := NewDeletionSweeper(fs, time.Hour, time.Hour)

	n, err := d.SweepOnce(context.Background())
	if err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}
	if n != 0 {
		t.Fatalf("没有到期账号时不该删任何东西，实得 %d", n)
	}
	if fs.calls != 1 {
		t.Fatalf("空跑应当只查一次，实际 %d 次", fs.calls)
	}
}

// 错误要向上返回（调用方是 sweepLoop，它记日志后下一轮重试），不 panic。
func TestDeletionSweeperPropagatesError(t *testing.T) {
	fs := &fakePurgeStore{pending: 10, failAfter: 0}
	// failAfter=0 表示不报错；这里用 failAfter 的另一种写法：直接让它第一次就报错。
	fs.failAfter = 0
	fs2 := &failFirstStore{}
	d := NewDeletionSweeper(fs2, time.Hour, time.Hour)

	if _, err := d.SweepOnce(context.Background()); err == nil {
		t.Fatal("store 报错时 SweepOnce 必须把错误返回")
	}
	_ = fs
}

type failFirstStore struct{}

func (f *failFirstStore) PurgeExpiredDeletions(context.Context, time.Time, int) (int64, error) {
	return 0, errors.New("boom")
}

func (f *failFirstStore) CountExpiredDeletions(context.Context, time.Time) (int64, error) {
	return 0, errors.New("boom")
}

// nil 接收者与 nil store 都要安全（测试装配与没有账号能力的窄实例都会传 nil）。
func TestDeletionSweeperNilSafety(t *testing.T) {
	var d *DeletionSweeper
	d.Start()
	d.Stop()
	if n, err := d.SweepOnce(context.Background()); err != nil || n != 0 {
		t.Fatalf("nil 接收者应安全，实得 n=%d err=%v", n, err)
	}

	empty := NewDeletionSweeper(nil, time.Hour, time.Hour)
	empty.Start()
	empty.Stop()
	if n, err := empty.SweepOnce(context.Background()); err != nil || n != 0 {
		t.Fatalf("nil store 应安全，实得 n=%d err=%v", n, err)
	}
}

// Start/Stop 可重复调用，且 Stop 之后循环真的会退出。
func TestDeletionSweeperStartStopIsIdempotent(t *testing.T) {
	fs := &fakePurgeStore{pending: 0}
	d := NewDeletionSweeper(fs, time.Hour, 10*time.Millisecond)

	d.Start()
	d.Start() // 第二次是 no-op
	d.Stop()
	d.Stop() // 第二次是 no-op

	// 给循环一点时间退出；退出后不该再有新的调用。
	before := fs.calls
	time.Sleep(50 * time.Millisecond)
	if fs.calls != before {
		t.Fatalf("Stop 之后不该再有清扫调用：before=%d after=%d", before, fs.calls)
	}
}

// CountPending 用同一个 cutoff（now - grace）。
func TestDeletionSweeperCountPending(t *testing.T) {
	grace := time.Hour
	fixedNow := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	fs := &fakePurgeStore{pending: 3}
	d := NewDeletionSweeper(fs, grace, time.Hour)
	d.now = func() time.Time { return fixedNow }

	n, err := d.CountPending(context.Background())
	if err != nil {
		t.Fatalf("CountPending: %v", err)
	}
	if n != 3 {
		t.Fatalf("CountPending 应回 3，实得 %d", n)
	}
	if len(fs.cutoffs) == 0 || !fs.cutoffs[0].Equal(fixedNow.Add(-grace)) {
		t.Fatalf("CountPending 的 cutoff 也必须是 now-grace")
	}
}
