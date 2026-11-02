package store

import (
	"context"
	"testing"
	"time"
)

// 粉丝红点（票 #67；决议 #53）的真库证据。
//
// 核心语义一句话：`new_count = 我未回关的粉丝中，关注时间晚于 followers_seen_at 的人数`。
// 这个定义里每一段都不是装饰：
//
//   - "我未回关的"：回关即互关，那条关系已经从"待处理"变成"已处理"，
//     留在红点里等于让用户去处理一件他刚处理完的事；
//   - "晚于 followers_seen_at"：进过粉丝 tab 就把水位线推到当下，
//     之后的红点只数**真正新的**；
//   - "粉丝"（而不是互关/关注）：红点回答的是"有人想跟你建立关系"，
//     我关注了谁不需要我处理。
//
// 还有一条**不是**在 SQL 里但同等重要的：拉黑与封禁的人在三个方向上都要消失。
// 那是 #52/#53 的流谓词，本文件用同一组断言把它钉在红点这条路径上。

// seenEnv 造 alice（我）与三个粉丝，返回 (store, alice, bob, carol, dave)。
func seenEnv(t *testing.T) (*Store, *User, *User, *User, *User) {
	t.Helper()
	s := requireTestDB(t)
	alice := seedUser(t, s, "seen-alice")
	bob := seedUser(t, s, "seen-bob")
	carol := seedUser(t, s, "seen-carol")
	dave := seedUser(t, s, "seen-dave")
	return s, alice, bob, carol, dave
}

// followAt 建一条关注边并把它的 created_at 钉到指定时刻。
//
// 为什么要改 created_at：真库的 now() 在同一毫秒内会给出相同时间戳，
// 而本文件的全部判据都建立在"这条边比水位线新/旧"上 ——
// 靠 sleep 拉开时间会让用例变慢且仍可能抖动。
func followAt(t *testing.T, s *Store, followerID, followeeID int64, at time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, _, err := s.CreateFollow(ctx, followerID, followeeID); err != nil {
		t.Fatalf("建关注边失败：%v", err)
	}
	if err := s.DB().WithContext(ctx).
		Exec("UPDATE follows SET created_at = ? WHERE follower_id = ? AND followee_id = ?",
			at, followerID, followeeID).Error; err != nil {
		t.Fatalf("调整关注时间失败：%v", err)
	}
}

// TestCountNewFollowersIsZeroBeforeAnySeen 覆盖"从没进过粉丝 tab"的初态。
//
// followers_seen_at 为 NULL 时，水位线等价于"很久以前"——所有未回关的粉丝都算新。
// 这条不能写成"NULL 时返回 0"：新注册用户被第一个人关注，正是红点最该亮的时刻。
func TestCountNewFollowersIsZeroBeforeAnySeen(t *testing.T) {
	s, alice, bob, carol, _ := seenEnv(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)

	followAt(t, s, bob.ID, alice.ID, base)
	followAt(t, s, carol.ID, alice.ID, base.Add(time.Minute))

	n, err := s.CountNewFollowers(ctx, alice.ID)
	if err != nil {
		t.Fatalf("统计新粉丝失败：%v", err)
	}
	if n != 2 {
		t.Fatalf("从没看过粉丝列表时，两个粉丝都算新：want 2 got %d", n)
	}

	// 没被关注的人不该有红点。
	if n, err := s.CountNewFollowers(ctx, bob.ID); err != nil || n != 0 {
		t.Fatalf("没人关注 bob，红点应为 0：n=%d err=%v", n, err)
	}
}

// TestCountNewFollowersExcludesFollowedBack 覆盖"回关即灭"。
//
// 这是红点语义的核心：红点数的是**待处理**的粉丝，不是粉丝总数。
// 回关之后那条关系变成互关（好友），红点不该再提醒"有人关注了你"。
func TestCountNewFollowersExcludesFollowedBack(t *testing.T) {
	s, alice, bob, carol, _ := seenEnv(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)

	followAt(t, s, bob.ID, alice.ID, base)
	followAt(t, s, carol.ID, alice.ID, base.Add(time.Minute))

	if n, _ := s.CountNewFollowers(ctx, alice.ID); n != 2 {
		t.Fatalf("前置：两个粉丝都未回关，红点应为 2，实得 %d", n)
	}

	// 回关 bob。
	if _, _, err := s.CreateFollow(ctx, alice.ID, bob.ID); err != nil {
		t.Fatalf("回关失败：%v", err)
	}
	n, err := s.CountNewFollowers(ctx, alice.ID)
	if err != nil {
		t.Fatalf("统计新粉丝失败：%v", err)
	}
	if n != 1 {
		t.Fatalf("回关后 bob 不该再算新粉丝：want 1 got %d", n)
	}
}

// TestMarkFollowersSeenClearsCount 覆盖"进 tab 即清"与水位线的单向性。
//
// 两个判据：
//  1. 标记之后红点归零；
//  2. 之后**再来**新粉丝，红点重新变 1 —— 水位线是"上次看到哪儿"，
//     不是一个"已读开关"。写成布尔位会让红点从此再也不亮。
func TestMarkFollowersSeenClearsCount(t *testing.T) {
	s, alice, bob, carol, dave := seenEnv(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)

	followAt(t, s, bob.ID, alice.ID, base)
	if n, _ := s.CountNewFollowers(ctx, alice.ID); n != 1 {
		t.Fatalf("前置：红点应为 1，实得 %d", n)
	}

	seenAt := time.Now()
	if err := s.MarkFollowersSeen(ctx, alice.ID, seenAt); err != nil {
		t.Fatalf("标记已看失败：%v", err)
	}
	if n, err := s.CountNewFollowers(ctx, alice.ID); err != nil || n != 0 {
		t.Fatalf("标记后红点应归零：n=%d err=%v", n, err)
	}

	// 水位线真的落库了。
	got, ok, err := s.FollowersSeenAt(ctx, alice.ID)
	if err != nil {
		t.Fatalf("读水位线失败：%v", err)
	}
	if !ok {
		t.Fatalf("标记之后 followers_seen_at 不该还是 NULL")
	}
	if d := got.Sub(seenAt); d > time.Second || d < -time.Second {
		t.Fatalf("水位线落库时刻偏差过大：want≈%v got %v", seenAt, got)
	}

	// 之后来的新粉丝重新点亮红点（水位线之后）。
	followAt(t, s, carol.ID, alice.ID, seenAt.Add(time.Second))
	if n, err := s.CountNewFollowers(ctx, alice.ID); err != nil || n != 1 {
		t.Fatalf("水位线之后的新粉丝应重新点亮：n=%d err=%v", n, err)
	}

	// 而水位线之前的老粉丝仍然不亮。
	followAt(t, s, dave.ID, alice.ID, base.Add(time.Minute))
	if n, err := s.CountNewFollowers(ctx, alice.ID); err != nil || n != 1 {
		t.Fatalf("水位线之前的旧粉丝不该被计入：n=%d err=%v", n, err)
	}
}

// TestMarkFollowersSeenIsIdempotent 覆盖验收里的"幂等"。
//
// 重复标记不是错误，且**后一次生效**（水位线只会前进）。
// 这一点在真机上很重要：进粉丝 tab 会触发一次标记，页面刷新再触发一次，
// 两次之间可能夹进一个新粉丝 —— 让后一次覆盖前一次才是"我最后一次看是什么时候"。
func TestMarkFollowersSeenIsIdempotent(t *testing.T) {
	s, alice, _, _, _ := seenEnv(t)
	ctx := context.Background()

	first := time.Now().Add(-time.Minute)
	if err := s.MarkFollowersSeen(ctx, alice.ID, first); err != nil {
		t.Fatalf("第一次标记失败：%v", err)
	}
	second := time.Now()
	if err := s.MarkFollowersSeen(ctx, alice.ID, second); err != nil {
		t.Fatalf("重复标记应当幂等：%v", err)
	}

	got, ok, err := s.FollowersSeenAt(ctx, alice.ID)
	if err != nil || !ok {
		t.Fatalf("读水位线失败：ok=%v err=%v", ok, err)
	}
	if d := got.Sub(second); d > time.Second || d < -time.Second {
		t.Fatalf("后一次标记应当生效：want≈%v got %v", second, got)
	}
}

// TestCountNewFollowersIgnoresBlockedAndBanned 覆盖验收的"打扰豁免"。
//
// 拉黑（任一方向）与封禁（注销同口径，都表现为 status != active）的人
// 在红点里必须消失 —— 这是 #52 流谓词的第三处引用，不能各写一遍。
func TestCountNewFollowersIgnoresBlockedAndBanned(t *testing.T) {
	s, alice, bob, carol, dave := seenEnv(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)

	followAt(t, s, bob.ID, alice.ID, base)
	followAt(t, s, carol.ID, alice.ID, base.Add(time.Minute))
	followAt(t, s, dave.ID, alice.ID, base.Add(2*time.Minute))

	if n, _ := s.CountNewFollowers(ctx, alice.ID); n != 3 {
		t.Fatalf("前置：三个粉丝都该算新，实得 %d", n)
	}

	// alice 拉黑 bob：bob 的关注边被删（#59 的事务），红点自然少一个。
	if _, err := s.BlockUser(ctx, alice.ID, bob.ID); err != nil {
		t.Fatalf("拉黑失败：%v", err)
	}
	if n, err := s.CountNewFollowers(ctx, alice.ID); err != nil || n != 2 {
		t.Fatalf("拉黑后 bob 不该计入：n=%d err=%v", n, err)
	}

	// carol 反向拉黑 alice：边还在（拉黑只删拉黑方自己那条），但红点也不该亮。
	if _, err := s.BlockUser(ctx, carol.ID, alice.ID); err != nil {
		t.Fatalf("反向拉黑失败：%v", err)
	}
	if n, err := s.CountNewFollowers(ctx, alice.ID); err != nil || n != 1 {
		t.Fatalf("被 carol 拉黑后也不该计入：n=%d err=%v", n, err)
	}

	// dave 被封禁。
	if err := s.DB().WithContext(ctx).Exec("UPDATE users SET status = ? WHERE id = ?",
		StatusBanned, dave.ID).Error; err != nil {
		t.Fatalf("封禁失败：%v", err)
	}
	if n, err := s.CountNewFollowers(ctx, alice.ID); err != nil || n != 0 {
		t.Fatalf("被封禁的粉丝不该计入：n=%d err=%v", n, err)
	}
}

// TestCountNewFollowersIgnoresFollowedBackEvenIfNew 是一条**回归哨兵**：
// 它守的不是业务规则，而是一个曾经真实发生过的 SQL 拼接缺陷。
//
// 曾经的写法把最后一个 `Where` 的括号省掉了（`(SELECT ...) IS NULL OR created_at > ...`），
// 而 GORM **不会**替你补括号 —— 于是那个 `OR` 把前面四个条件全部短路：
//
//	(A AND B AND C AND D) OR f.created_at > seen
//
// 症状不是"多算一两个"，而是**全站任何人在水位线之后新关注了谁都算进我的红点**。
// 真机验收里它表现为"互关之后红点又亮 1"：互关那条边比水位线新，
// 于是"我已经回关了"这个条件被 OR 绕过，那条边又被数了一次。
//
// 所以这里的场景必须**同时**满足"已回关"与"比水位线新"：
// 只满足前者，旧写法也会给出正确的 0（AND 链自然排除了它）。
func TestCountNewFollowersIgnoresFollowedBackEvenIfNew(t *testing.T) {
	s, alice, bob, _, _ := seenEnv(t)
	ctx := context.Background()

	base := time.Now().Add(-time.Hour)
	followAt(t, s, bob.ID, alice.ID, base)

	// 先标记一次：此刻红点 1，水位线落在"现在"。
	if err := s.MarkFollowersSeen(ctx, alice.ID, time.Now()); err != nil {
		t.Fatalf("标记水位线失败：%v", err)
	}

	// alice 回关 bob —— 这条边的 created_at 比水位线**新**。
	followAt(t, s, alice.ID, bob.ID, time.Now().Add(time.Minute))

	if n, err := s.CountNewFollowers(ctx, alice.ID); err != nil || n != 0 {
		t.Fatalf("已回关的人不该计入红点（哪怕这条边比水位线新）：n=%d err=%v", n, err)
	}
}

// TestFollowersSeenAtIsPerUser 钉住"这是每个账号自己的水位线"。
func TestFollowersSeenAtIsPerUser(t *testing.T) {
	s, alice, bob, _, _ := seenEnv(t)
	ctx := context.Background()

	if err := s.MarkFollowersSeen(ctx, alice.ID, time.Now()); err != nil {
		t.Fatalf("标记失败：%v", err)
	}
	if _, ok, err := s.FollowersSeenAt(ctx, bob.ID); err != nil || ok {
		t.Fatalf("bob 从没看过粉丝列表，水位线应当是 NULL：ok=%v err=%v", ok, err)
	}
}

// TestFollowersSeenRejectsUnknownUser 钉住边界：不存在/非法 id 不该写进去。
func TestFollowersSeenRejectsUnknownUser(t *testing.T) {
	s, _, _, _, _ := seenEnv(t)
	ctx := context.Background()

	if err := s.MarkFollowersSeen(ctx, 0, time.Now()); err == nil {
		t.Fatalf("userID=0 应当被拒绝")
	}
	if err := s.MarkFollowersSeen(ctx, 999999999, time.Now()); err == nil {
		t.Fatalf("不存在的用户应当被拒绝")
	}
	if n, err := s.CountNewFollowers(ctx, 0); err != nil || n != 0 {
		t.Fatalf("userID=0 的红点应当是 0 且不报错：n=%d err=%v", n, err)
	}
}
