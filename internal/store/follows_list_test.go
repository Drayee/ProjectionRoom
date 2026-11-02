package store

import (
	"context"
	"testing"
)

// 关注名单（票 #58）的真库证据。
//
// 三个视图共用一条**名单谓词**：可见（未封禁）∧ ¬拉黑（双向）——
// 所以这里重点验的不是"能不能查出来"，而是"该消失的有没有消失"：
// 被封禁的人不该出现在名单里、拉黑存续的一对不该互相出现在名单里。

// followListEnv 造 alice 与三个人：bob（互关）、carol（alice 单向关注）、dave（dave 单向关注 alice）。
func followListEnv(t *testing.T) (*Store, *User, *User, *User, *User) {
	t.Helper()
	s := requireTestDB(t)
	alice := seedUser(t, s, "fl-alice")
	bob := seedUser(t, s, "fl-bob")
	carol := seedUser(t, s, "fl-carol")
	dave := seedUser(t, s, "fl-dave")
	ctx := context.Background()

	// alice ⇄ bob：互关（好友）。
	if _, _, err := s.CreateFollow(ctx, alice.ID, bob.ID); err != nil {
		t.Fatalf("alice→bob 关注失败：%v", err)
	}
	if _, _, err := s.CreateFollow(ctx, bob.ID, alice.ID); err != nil {
		t.Fatalf("bob→alice 关注失败：%v", err)
	}
	// alice → carol：单向（我关注的）。
	if _, _, err := s.CreateFollow(ctx, alice.ID, carol.ID); err != nil {
		t.Fatalf("alice→carol 关注失败：%v", err)
	}
	// dave → alice：单向（我的粉丝）。
	if _, _, err := s.CreateFollow(ctx, dave.ID, alice.ID); err != nil {
		t.Fatalf("dave→alice 关注失败：%v", err)
	}
	return s, alice, bob, carol, dave
}

// idsOf 把名单压成 id 集合（顺序另有用例验）。
func idsOf(rows []FollowListEntry) map[int64]bool {
	out := make(map[int64]bool, len(rows))
	for _, r := range rows {
		out[r.UserID] = true
	}
	return out
}

// TestFollowListMutualOnlyHasBothDirections 覆盖互关名单的**定义**：
// 好友 ≡ 互关，所以名单里只能有"两个方向都有边"的人。
func TestFollowListMutualOnlyHasBothDirections(t *testing.T) {
	s, alice, bob, carol, dave := followListEnv(t)

	rows, err := s.ListFollowList(context.Background(), alice.ID, FollowListMutual)
	if err != nil {
		t.Fatalf("查互关名单失败：%v", err)
	}
	if len(rows) != 1 || rows[0].UserID != bob.ID {
		t.Fatalf("互关名单只该有 bob：%+v", rows)
	}
	if !rows[0].Following || !rows[0].FollowedBy {
		t.Fatalf("互关行的两个方向都该为真：%+v", rows[0])
	}
	if rows[0].Username != "fl-bob" || rows[0].DisplayName != "测试：fl-bob" {
		t.Fatalf("名单行必须带 username 与 displayName：%+v", rows[0])
	}
	if !rows[0].CanDM() {
		t.Fatalf("互关且无拉黑 → 可私聊：%+v", rows[0])
	}
	// carol 与 dave 是单向的，都不该在互关名单里。
	got := idsOf(rows)
	if got[carol.ID] || got[dave.ID] {
		t.Fatalf("单向关注不该进互关名单：%+v", rows)
	}
}

// TestFollowListFollowingAndFollowersAreMirror 覆盖另外两个视图的方向语义。
func TestFollowListFollowingAndFollowersAreMirror(t *testing.T) {
	s, alice, bob, carol, dave := followListEnv(t)
	ctx := context.Background()

	following, err := s.ListFollowList(ctx, alice.ID, FollowListFollowing)
	if err != nil {
		t.Fatalf("查关注名单失败：%v", err)
	}
	// alice 关注了 bob 与 carol。
	if len(following) != 2 {
		t.Fatalf("关注名单应有 bob 与 carol：%+v", following)
	}
	for _, r := range following {
		if !r.Following {
			t.Fatalf("关注名单里每行都该 following=true：%+v", r)
		}
		if r.UserID == bob.ID && !r.FollowedBy {
			t.Fatalf("bob 也关注了 alice，followedBy 应为真：%+v", r)
		}
		if r.UserID == carol.ID && r.FollowedBy {
			t.Fatalf("carol 没有回关，followedBy 应为假：%+v", r)
		}
	}

	followers, err := s.ListFollowList(ctx, alice.ID, FollowListFollowers)
	if err != nil {
		t.Fatalf("查粉丝名单失败：%v", err)
	}
	// 粉丝是 bob 与 dave。
	if len(followers) != 2 {
		t.Fatalf("粉丝名单应有 bob 与 dave：%+v", followers)
	}
	for _, r := range followers {
		if !r.FollowedBy {
			t.Fatalf("粉丝名单里每行都该 followedBy=true：%+v", r)
		}
		if r.UserID == dave.ID && r.Following {
			t.Fatalf("dave 没被回关，following 应为假：%+v", r)
		}
	}
}

// TestFollowListExcludesBlockedBothWays 覆盖名单谓词的拉黑维：
// 拉黑存续期**双向**都不出现在名单里（票 #42 §4）。
//
// 为什么连"我拉黑的人"也要从我的名单里消失：名单是"我的人"的视图，
// 而拉黑的意思就是"从我的世界里拿掉"。留着它只会在解除拉黑之前一直碍眼。
func TestFollowListExcludesBlockedBothWays(t *testing.T) {
	s, alice, bob, carol, _ := followListEnv(t)
	ctx := context.Background()

	// alice 拉黑 bob。名单的可见性过滤因此要把 bob 从 alice 的三个名单里全部拿掉，
	// 同时 bob 那边也看不到 alice（拉黑是双向静默）。
	//
	// ⚠ 「拉黑时删掉拉黑方那条 follow」是票 #42 的规则，落地在票 #59（blocked 形态收口）；
	// 本票只保证**名单**不泄漏 —— 两条边还在，但都不出现在任何一侧的名单里。
	if _, err := s.BlockUser(ctx, alice.ID, bob.ID); err != nil {
		t.Fatalf("拉黑失败：%v", err)
	}

	for _, tab := range []FollowListTab{FollowListMutual, FollowListFollowing, FollowListFollowers} {
		rows, err := s.ListFollowList(ctx, alice.ID, tab)
		if err != nil {
			t.Fatalf("查 %s 名单失败：%v", tab, err)
		}
		if idsOf(rows)[bob.ID] {
			t.Fatalf("%s 名单不该包含被拉黑的 bob：%+v", tab, rows)
		}
	}

	// 反方向也一样：bob 的名单里看不到 alice（拉黑是双向静默）。
	rows, err := s.ListFollowList(ctx, bob.ID, FollowListFollowers)
	if err != nil {
		t.Fatalf("查 bob 的粉丝名单失败：%v", err)
	}
	if idsOf(rows)[alice.ID] {
		t.Fatalf("被拉黑方的名单也不该包含拉黑方：%+v", rows)
	}

	// carol 与 dave 不受影响（证明上面的过滤只针对这一对）。
	if rows, err := s.ListFollowList(ctx, alice.ID, FollowListFollowing); err != nil {
		t.Fatalf("查关注名单失败：%v", err)
	} else if !idsOf(rows)[carol.ID] {
		t.Fatalf("无关的人不该被连带过滤掉：%+v", rows)
	}
}

// TestFollowListExcludesBannedUsers 覆盖"未封禁"那一维。
//
// 封禁的语义是"这个账号在界面上不存在了"（公开档案也回 404），
// 所以它不该出现在任何名单里 —— 否则点进去是一个 404 页。
func TestFollowListExcludesBannedUsers(t *testing.T) {
	s, alice, _, carol, _ := followListEnv(t)
	ctx := context.Background()

	if err := s.DB().WithContext(ctx).Model(&User{}).Where("id = ?", carol.ID).
		Update("status", StatusBanned).Error; err != nil {
		t.Fatalf("封禁 carol 失败：%v", err)
	}

	rows, err := s.ListFollowList(ctx, alice.ID, FollowListFollowing)
	if err != nil {
		t.Fatalf("查关注名单失败：%v", err)
	}
	if idsOf(rows)[carol.ID] {
		t.Fatalf("被封禁的账号不该出现在名单里：%+v", rows)
	}
}

// TestFollowListUnknownTabIsRejected 覆盖 tab 的输入校验：
// 未知视图**不猜**（既不默认互关也不返回全部），因为猜错的后果是
// "界面显示了另一份名单"，而用户会以为那是真的。
func TestFollowListUnknownTabIsRejected(t *testing.T) {
	s, alice, _, _, _ := followListEnv(t)
	if _, err := s.ListFollowList(context.Background(), alice.ID, FollowListTab("bogus")); err == nil {
		t.Fatalf("未知 tab 应当报错")
	}
}

// TestFollowListIsEmptyForGuestID 覆盖 userID <= 0 的兜底：
// 返回空名单而不是报错（调用方在游客态不该拿到 500）。
func TestFollowListIsEmptyForGuestID(t *testing.T) {
	s := requireTestDB(t)
	rows, err := s.ListFollowList(context.Background(), 0, FollowListFollowing)
	if err != nil {
		t.Fatalf("游客 id 不该报错：%v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("游客 id 应当返回空名单：%+v", rows)
	}
}

// TestFollowStatesForIsBatchAndCarriesBlocked 覆盖批量关系查询（搜索页用它）。
//
// 一条 SQL 拿全部（不是逐人一次）：搜索是边打边搜的高频读端点，
// 20 条结果逐行查就是 21 次往返。
func TestFollowStatesForIsBatchAndCarriesBlocked(t *testing.T) {
	s, alice, bob, carol, dave := followListEnv(t)
	ctx := context.Background()

	// alice 拉黑 dave：dave 与 alice 之间的状态该带"被拉黑"，
	// 于是前端不会给他"回关"按钮（拉黑存续期不给关注按钮）。
	if _, err := s.BlockUser(ctx, alice.ID, dave.ID); err != nil {
		t.Fatalf("拉黑 dave 失败：%v", err)
	}

	states, err := s.FollowStatesFor(ctx, alice.ID, []int64{bob.ID, carol.ID, dave.ID})
	if err != nil {
		t.Fatalf("批量查关系失败：%v", err)
	}
	if len(states) != 3 {
		t.Fatalf("三个人都该有一行：%+v", states)
	}
	if st := states[bob.ID]; !st.Following || !st.FollowedBy || !st.Mutual() {
		t.Fatalf("bob 应互关：%+v", st)
	}
	if st := states[carol.ID]; !st.Following || st.FollowedBy {
		t.Fatalf("carol 应被我单向关注：%+v", st)
	}
	if st := states[dave.ID]; !st.Blocked {
		t.Fatalf("dave 应标记为拉黑存续：%+v", st)
	}
	if states[dave.ID].CanDM() {
		t.Fatalf("拉黑存续期不可私聊：%+v", states[dave.ID])
	}

	// 空输入不查库、不报错。
	empty, err := s.FollowStatesFor(ctx, alice.ID, nil)
	if err != nil {
		t.Fatalf("空输入不该报错：%v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("空输入应返回空 map：%+v", empty)
	}
}
