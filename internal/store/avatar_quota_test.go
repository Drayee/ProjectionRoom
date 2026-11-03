package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// 头像变更每日频控（票 #75；决议 #54 §3 审核四件套之一）的真库证据。
//
// 语义一句话：**每天最多 N 次**，N 由调用方给（service 决定是 5），
// 跨日自动归零，不需要任何定时任务。
//
// 本文件的判据分三类，对应三种真实的失败方式：
//
//  1. 计数正确（用满之前每次都能过、第 N+1 次被拒）；
//  2. **跨日**归零（票 #75 验收第 3 条明确要求进测资）—— 这里能测是因为
//     `today` 是参数而不是 SQL 的 CURRENT_DATE；
//  3. **并发**不超额 —— 这是"读-判断-写"两次往返一定做不对、
//     只有单条 UPDATE 才做对的那一条。它也是最容易被将来的"优化"改坏的。

// quotaEnv 造一个用户，返回 (store, user)。
func quotaEnv(t *testing.T) (*Store, *User) {
	t.Helper()
	s := requireTestDB(t)
	u := seedUser(t, s, "quota-user")
	return s, u
}

// TestConsumeAvatarChangeAllowsExactlyLimitTimes 判"刚好用满 permit 次数"。
//
// 用满之后的那一次必须报 ErrAvatarQuotaExceeded，而不是 ErrNotFound ——
// 两者对用户意味着完全不同的下一步（"明天再来" vs "账号没了"），
// 而它们在 SQL 层是同一种现象（一行都没被 UPDATE 匹配上）。
func TestConsumeAvatarChangeAllowsExactlyLimitTimes(t *testing.T) {
	s, u := quotaEnv(t)
	ctx := context.Background()
	day := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

	for i := 1; i <= 5; i++ {
		if err := s.ConsumeAvatarChange(ctx, u.ID, day, 5); err != nil {
			t.Fatalf("第 %d 次（限额 5）应当通过，实际：%v", i, err)
		}
		got, err := s.AvatarChangeQuota(ctx, u.ID, day)
		if err != nil {
			t.Fatalf("读回计数失败：%v", err)
		}
		if got != i {
			t.Fatalf("第 %d 次之后计数应为 %d，实际 %d", i, i, got)
		}
	}

	err := s.ConsumeAvatarChange(ctx, u.ID, day, 5)
	if !errors.Is(err, ErrAvatarQuotaExceeded) {
		t.Fatalf("第 6 次应当被拒（ErrAvatarQuotaExceeded），实际：%v", err)
	}
	// 被拒的那次不能占用额度 —— 否则"再试一次"会自己把剩余次数吃光。
	got, err := s.AvatarChangeQuota(ctx, u.ID, day)
	if err != nil {
		t.Fatalf("读回计数失败：%v", err)
	}
	if got != 5 {
		t.Fatalf("被拒之后计数仍应是 5，实际 %d", got)
	}
}

// TestConsumeAvatarChangeResetsNextDay 判跨日口径。
//
// 这条是票 #75 验收第 3 条明确要求"跨日口径进测资"的那一项。
// 关键在于**不做任何清理**：只是把 today 换成明天，计数就该从 1 开始。
func TestConsumeAvatarChangeResetsNextDay(t *testing.T) {
	s, u := quotaEnv(t)
	ctx := context.Background()
	today := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	tomorrow := today.AddDate(0, 0, 1)

	for i := 0; i < 5; i++ {
		if err := s.ConsumeAvatarChange(ctx, u.ID, today, 5); err != nil {
			t.Fatalf("今天的第 %d 次应当通过：%v", i+1, err)
		}
	}
	if err := s.ConsumeAvatarChange(ctx, u.ID, today, 5); !errors.Is(err, ErrAvatarQuotaExceeded) {
		t.Fatalf("今天已满，应当被拒：%v", err)
	}

	// 换日：不需要任何清理动作。
	if err := s.ConsumeAvatarChange(ctx, u.ID, tomorrow, 5); err != nil {
		t.Fatalf("第二天应当重新可用，实际：%v", err)
	}
	got, err := s.AvatarChangeQuota(ctx, u.ID, tomorrow)
	if err != nil {
		t.Fatalf("读回明天计数失败：%v", err)
	}
	if got != 1 {
		t.Fatalf("第二天用掉一次之后计数应为 1（昨天的不算），实际 %d", got)
	}
	// 读**今天**的计数时应当看到"不是今天，归零"——同一个函数两种口径都在。
	gotToday, err := s.AvatarChangeQuota(ctx, u.ID, today)
	if err != nil {
		t.Fatalf("读回今天计数失败：%v", err)
	}
	if gotToday != 0 {
		t.Fatalf("昨天的日期已不是当前计数所属的日，应回 0，实际 %d", gotToday)
	}
}

// TestConsumeAvatarChangeFirstEverIsNotBlocked 判"从未改过"的那一行。
//
// 这是一个**真实存在过的缺陷形状**：`avatar_changes_on <> today` 在 NULL 上
// 求值为 NULL（不是 true），于是从没改过头像的账号会一次都用不了。
// 所以实现里用的是 `IS DISTINCT FROM`，这条用例就是它的哨兵。
func TestConsumeAvatarChangeFirstEverIsNotBlocked(t *testing.T) {
	s, u := quotaEnv(t)
	ctx := context.Background()

	// 前置：新账号的这一列确实是 NULL（不是空日期）。
	//
	// 用 `IS NULL` 的计数而不是把列扫进 *time.Time：后者在 NULL 上会
	// 报 "unsupported Scan, storing driver.Value type <nil> into type *time.Time"
	// —— 判"是 NULL"最直白的写法本来就是一个布尔查询。
	var nulls int64
	if err := s.DB().WithContext(ctx).Raw(
		`SELECT count(*) FROM users WHERE id = ? AND avatar_changes_on IS NULL`, u.ID).
		Scan(&nulls).Error; err != nil {
		t.Fatalf("读取初始状态失败：%v", err)
	}
	if nulls != 1 {
		t.Fatalf("夹具前提不成立：新账号的 avatar_changes_on 应为 NULL")
	}
	if got, err := s.AvatarChangeQuota(ctx, u.ID, time.Now()); err != nil || got != 0 {
		t.Fatalf("从未改过应当读回 0，实际 (%d, %v)", got, err)
	}

	if err := s.ConsumeAvatarChange(ctx, u.ID, time.Now(), 5); err != nil {
		t.Fatalf("新账号第一次改头像必须通过，实际：%v", err)
	}
}

// TestConsumeAvatarChangeConcurrentNeverOverspends 判并发不超额。
//
// 20 个 goroutine 同时抢 5 个额度，成功的必须**恰好** 5 个。
// 「读 → 判断 → 写回」的两次往返实现在这里会超额（两边都读到 4 就都放行）；
// 单条 UPDATE 把判断放进 WHERE 里，让行锁替我们做，才是对的。
//
// 用 -race 跑时这条同时是数据竞争的观察点（虽然是纯 DB 侧的竞争，
// 真正的冲突发生在数据库行锁上）。
func TestConsumeAvatarChangeConcurrentNeverOverspends(t *testing.T) {
	s, u := quotaEnv(t)
	ctx := context.Background()
	day := time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)

	const racers = 20
	const limit = 5

	var wg sync.WaitGroup
	var mu sync.Mutex
	granted := 0
	rejected := 0
	var unexpected []error

	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := s.ConsumeAvatarChange(ctx, u.ID, day, limit)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				granted++
			case errors.Is(err, ErrAvatarQuotaExceeded):
				rejected++
			default:
				unexpected = append(unexpected, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(unexpected) > 0 {
		t.Fatalf("%d 个 goroutine 拿到了意料之外的错误，第一个：%v", len(unexpected), unexpected[0])
	}
	if granted != limit {
		t.Fatalf("并发抢 %d 个额度，应当恰好放行 %d 个，实际放行 %d 个（被拒 %d）",
			limit, limit, granted, rejected)
	}
	if rejected != racers-limit {
		t.Fatalf("其余 %d 个应当全部被拒，实际被拒 %d 个", racers-limit, rejected)
	}
	got, err := s.AvatarChangeQuota(ctx, u.ID, day)
	if err != nil {
		t.Fatalf("读回计数失败：%v", err)
	}
	if got != limit {
		t.Fatalf("最终计数应当恰好是 %d，实际 %d", limit, got)
	}
}

// TestConsumeAvatarChangeUnknownUser 判不存在的用户报 ErrNotFound，不是"额度用满"。
func TestConsumeAvatarChangeUnknownUser(t *testing.T) {
	s, _ := quotaEnv(t)
	ctx := context.Background()

	if err := s.ConsumeAvatarChange(ctx, 0, time.Now(), 5); !errors.Is(err, ErrNotFound) {
		t.Fatalf("id=0 应当报 ErrNotFound，实际：%v", err)
	}
	if err := s.ConsumeAvatarChange(ctx, 999999, time.Now(), 5); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的 id 应当报 ErrNotFound，实际：%v", err)
	}
	if _, err := s.AvatarChangeQuota(ctx, 999999, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("读不存在的用户应当报 ErrNotFound，实际：%v", err)
	}
}

// TestConsumeAvatarChangeUnlimited 判 limit<=0 = 不限额。
//
// 这条路径的存在理由是**部署侧的取舍要能被表达**：某个实例决定先不限，
// 不该需要改调用方代码。它也必须仍然把计数记对（否则从"不限"切回
// "限 5"时，用户当天的额度会从 0 开始）。
func TestConsumeAvatarChangeUnlimited(t *testing.T) {
	s, u := quotaEnv(t)
	ctx := context.Background()
	day := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)

	for i := 0; i < 9; i++ {
		if err := s.ConsumeAvatarChange(ctx, u.ID, day, 0); err != nil {
			t.Fatalf("不限额时第 %d 次应当通过：%v", i+1, err)
		}
	}
	got, err := s.AvatarChangeQuota(ctx, u.ID, day)
	if err != nil {
		t.Fatalf("读回计数失败：%v", err)
	}
	if got != 9 {
		t.Fatalf("不限额也应当如实计数，期望 9 实际 %d", got)
	}
}

// TestConsumeAvatarChangeIgnoresTimeOfDay 判"同一天的不同时刻是同一个日子"。
//
// dateOnly 把时间截到日；如果实现里忘了截断（直接把 time.Time 塞进
// `date` 列），PostgreSQL 会在服务端按自己的时区取舍，而这里传的是 UTC ——
// 于是"同一天的早晨与深夜"可能落到两个不同的日期上，额度凭空翻倍。
func TestConsumeAvatarChangeIgnoresTimeOfDay(t *testing.T) {
	s, u := quotaEnv(t)
	ctx := context.Background()
	morning := time.Date(2026, 11, 3, 1, 0, 0, 0, time.UTC)
	evening := time.Date(2026, 11, 3, 23, 0, 0, 0, time.UTC)

	if err := s.ConsumeAvatarChange(ctx, u.ID, morning, 5); err != nil {
		t.Fatalf("早晨那次应当通过：%v", err)
	}
	got, err := s.AvatarChangeQuota(ctx, u.ID, evening)
	if err != nil {
		t.Fatalf("读回计数失败：%v", err)
	}
	if got != 1 {
		t.Fatalf("同一天的深夜应当看到早晨那次（计数 1），实际 %d —— 日截断没生效", got)
	}
}
