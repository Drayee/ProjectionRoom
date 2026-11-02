package store

import (
	"context"
	"errors"
	"testing"
)

// 关注边（follows）的真库证据。wayfinder #56。
//
// 与 store/friendships_test.go 的分工：那里验"一段关系只存一行"的状态机，
// 这里验 ADR 0001 的**有向边**：同一条边幂等、两个方向各自独立、
// 拉黑存续期双向禁止新关注、"互关"是两条边的合取而不是一个状态值。

// followEnv 造两个用户，返回 (store, alice, bob)。
func followEnv(t *testing.T) (*Store, *User, *User) {
	t.Helper()
	s := requireTestDB(t)
	alice := seedUser(t, s, "fol-alice")
	bob := seedUser(t, s, "fol-bob")
	return s, alice, bob
}

// TestFollowIsIdempotentAndUnique 覆盖验收第 1 条：唯一键 + 幂等。
//
// "重复关注"不是错误：用户双击「关注」或网络重发都只该得到**同一条边**。
// 幂等由唯一键 + ON CONFLICT DO NOTHING 保证（应用层是唯一 owner，DB 是最后一道）。
func TestFollowIsIdempotentAndUnique(t *testing.T) {
	s, alice, bob := followEnv(t)
	ctx := context.Background()

	row, created, err := s.CreateFollow(ctx, alice.ID, bob.ID)
	if err != nil {
		t.Fatalf("第一次关注失败：%v", err)
	}
	if !created {
		t.Fatalf("第一次关注应当新建一条边")
	}
	if row.FollowerID != alice.ID || row.FolloweeID != bob.ID {
		t.Fatalf("边的方向不对：%+v", row)
	}
	if row.CreatedAt.IsZero() {
		t.Fatalf("边应当带 created_at：%+v", row)
	}

	// 再来一次：幂等，不新建，也不报错。
	again, created2, err := s.CreateFollow(ctx, alice.ID, bob.ID)
	if err != nil {
		t.Fatalf("重复关注应当幂等：%v", err)
	}
	if created2 {
		t.Fatalf("重复关注不该新建边")
	}
	if again.ID != row.ID {
		t.Fatalf("重复关注应命中同一条边：want id=%d got id=%d", row.ID, again.ID)
	}

	// 库里仍然只有一条。
	var n int64
	if err := s.DB().WithContext(ctx).Model(&Follow{}).
		Where("follower_id = ? AND followee_id = ?", alice.ID, bob.ID).Count(&n).Error; err != nil {
		t.Fatalf("统计关注边失败：%v", err)
	}
	if n != 1 {
		t.Fatalf("同一条关注边只应有一行，实际 %d 行", n)
	}
}

// TestFollowUniqueConstraintIsEnforced 直接绕过应用层插一条重复边，
// 证明唯一键在数据库侧真的挡住了（应用层的 ON CONFLICT 只是少一条路径）。
func TestFollowUniqueConstraintIsEnforced(t *testing.T) {
	s, alice, bob := followEnv(t)
	ctx := context.Background()

	if _, _, err := s.CreateFollow(ctx, alice.ID, bob.ID); err != nil {
		t.Fatalf("建关注边失败：%v", err)
	}

	err := s.DB().WithContext(ctx).Exec(
		"INSERT INTO follows (follower_id, followee_id, created_at) VALUES (?, ?, now())",
		alice.ID, bob.ID).Error
	if err == nil {
		t.Fatalf("唯一键应当挡住重复的关注边")
	}
	if !isUniqueViolation(err) {
		t.Fatalf("重复插入应当是唯一约束冲突，实际：%v", err)
	}
}

// TestFollowRejectsSelfFollow 覆盖验收第 2 条：自关注 400（store 层先拒）。
func TestFollowRejectsSelfFollow(t *testing.T) {
	s, alice, _ := followEnv(t)
	ctx := context.Background()

	if _, _, err := s.CreateFollow(ctx, alice.ID, alice.ID); !errors.Is(err, ErrFollowSelfTarget) {
		t.Fatalf("自关注应当返回 ErrFollowSelfTarget，实际 %v", err)
	}
}

// TestFollowBlockedEitherDirection 覆盖验收第 4 条：
// 拉黑存续期**双向**禁止新关注（票 42 决议）。
//
// 两种方向都要挡住：拉黑方不能关注被拉黑方，被拉黑方也不能关注拉黑方。
func TestFollowBlockedEitherDirection(t *testing.T) {
	s, alice, bob := followEnv(t)
	ctx := context.Background()

	// alice 拉黑 bob。
	if _, err := s.BlockUser(ctx, alice.ID, bob.ID); err != nil {
		t.Fatalf("alice 拉黑 bob 失败：%v", err)
	}

	if _, _, err := s.CreateFollow(ctx, alice.ID, bob.ID); !errors.Is(err, ErrFollowBlocked) {
		t.Fatalf("拉黑方关注被拉黑方应当被拒，实际 %v", err)
	}
	if _, _, err := s.CreateFollow(ctx, bob.ID, alice.ID); !errors.Is(err, ErrFollowBlocked) {
		t.Fatalf("被拉黑方关注拉黑方应当被拒，实际 %v", err)
	}

	// 解除拉黑后恢复（票 42：解黑恢复）。
	if err := s.Unblock(ctx, alice.ID, bob.ID); err != nil {
		t.Fatalf("解除拉黑失败：%v", err)
	}
	if _, _, err := s.CreateFollow(ctx, bob.ID, alice.ID); err != nil {
		t.Fatalf("解黑后应当能关注：%v", err)
	}
}

// TestUnfollowRemovesOnlyOwnDirection 覆盖"取关只删自己这一条边"。
//
// 反向边**保留**：回关是对方的事，单方取关不该替对方做决定。
func TestUnfollowRemovesOnlyOwnDirection(t *testing.T) {
	s, alice, bob := followEnv(t)
	ctx := context.Background()

	if _, _, err := s.CreateFollow(ctx, alice.ID, bob.ID); err != nil {
		t.Fatalf("建关注边失败：%v", err)
	}
	if _, _, err := s.CreateFollow(ctx, bob.ID, alice.ID); err != nil {
		t.Fatalf("建反向关注边失败：%v", err)
	}

	removed, err := s.RemoveFollow(ctx, alice.ID, bob.ID)
	if err != nil {
		t.Fatalf("取关失败：%v", err)
	}
	if !removed {
		t.Fatalf("取关应当删掉一条边")
	}

	// alice→bob 没了，bob→alice 还在。
	if ok, err := s.IsFollowing(ctx, alice.ID, bob.ID); err != nil || ok {
		t.Fatalf("alice→bob 应当已删除：ok=%v err=%v", ok, err)
	}
	if ok, err := s.IsFollowing(ctx, bob.ID, alice.ID); err != nil || !ok {
		t.Fatalf("bob→alice 应当保留：ok=%v err=%v", ok, err)
	}

	// 再取关一次是 no-op（幂等，不报错）。
	removed2, err := s.RemoveFollow(ctx, alice.ID, bob.ID)
	if err != nil {
		t.Fatalf("重复取关应当幂等：%v", err)
	}
	if removed2 {
		t.Fatalf("重复取关不该再删一条边")
	}
}

// TestFollowStateBetweenIsDirectional 覆盖"关系状态是**派生**的"。
//
// 互关 = 两个方向都有边；单方只有一边；计数是对方的粉丝/关注数。
func TestFollowStateBetweenIsDirectional(t *testing.T) {
	s, alice, bob := followEnv(t)
	ctx := context.Background()

	// 一开始：什么都没发生。
	st, err := s.FollowStateBetween(ctx, alice.ID, bob.ID)
	if err != nil {
		t.Fatalf("查关系状态失败：%v", err)
	}
	if st.Following || st.FollowedBy || st.Mutual() || st.CanDM() {
		t.Fatalf("初始状态应当是『什么都没有』：%+v", st)
	}
	if st.FollowerCount != 0 || st.FollowingCount != 0 {
		t.Fatalf("初始计数应当都是 0：%+v", st)
	}

	// alice 关注 bob：单向。
	if _, _, err := s.CreateFollow(ctx, alice.ID, bob.ID); err != nil {
		t.Fatalf("建关注边失败：%v", err)
	}
	st, err = s.FollowStateBetween(ctx, alice.ID, bob.ID)
	if err != nil {
		t.Fatalf("查关系状态失败：%v", err)
	}
	if !st.Following || st.FollowedBy {
		t.Fatalf("应当是单向关注：%+v", st)
	}
	if st.Mutual() || st.CanDM() {
		t.Fatalf("单向关注不是互关，不能私聊：%+v", st)
	}
	// bob 的粉丝数 = 1、关注数 = 0（注意计数是**对方**的）。
	if st.FollowerCount != 1 || st.FollowingCount != 0 {
		t.Fatalf("计数应当是 bob 的：%+v", st)
	}

	// bob 回关：互关。
	if _, _, err := s.CreateFollow(ctx, bob.ID, alice.ID); err != nil {
		t.Fatalf("建反向边失败：%v", err)
	}
	st, err = s.FollowStateBetween(ctx, alice.ID, bob.ID)
	if err != nil {
		t.Fatalf("查关系状态失败：%v", err)
	}
	if !st.Following || !st.FollowedBy || !st.Mutual() {
		t.Fatalf("应当是互关：%+v", st)
	}
	if !st.CanDM() {
		t.Fatalf("互关且未拉黑应当可以私聊：%+v", st)
	}
	if st.FollowerCount != 1 || st.FollowingCount != 1 {
		t.Fatalf("互关后 bob 的计数应当各为 1：%+v", st)
	}
}

// TestCanDMPredicateFollowsADR0001 覆盖 can_dm 谓词（ADR 0001）。
//
// can_dm(A,B) = mutual_follow(A,B) ∧ ¬blocked_either(A,B)。
func TestCanDMPredicateFollowsADR0001(t *testing.T) {
	s, alice, bob := followEnv(t)
	ctx := context.Background()

	// 单向关注：不可私聊。
	if _, _, err := s.CreateFollow(ctx, alice.ID, bob.ID); err != nil {
		t.Fatalf("建关注边失败：%v", err)
	}
	if ok, err := s.CanDM(ctx, alice.ID, bob.ID); err != nil || ok {
		t.Fatalf("单向关注不可私聊：ok=%v err=%v", ok, err)
	}

	// 互关：可私聊。
	if _, _, err := s.CreateFollow(ctx, bob.ID, alice.ID); err != nil {
		t.Fatalf("建反向边失败：%v", err)
	}
	if ok, err := s.CanDM(ctx, alice.ID, bob.ID); err != nil || !ok {
		t.Fatalf("互关应当可私聊：ok=%v err=%v", ok, err)
	}

	// 拉黑（任一方向）：不可私聊。
	if _, err := s.BlockUser(ctx, alice.ID, bob.ID); err != nil {
		t.Fatalf("拉黑失败：%v", err)
	}
	if ok, err := s.CanDM(ctx, alice.ID, bob.ID); err != nil || ok {
		t.Fatalf("拉黑后不可私聊：ok=%v err=%v", ok, err)
	}
}

// TestFollowCountsCountEdges 覆盖两个计数（关注数 / 粉丝数）。
func TestFollowCountsCountEdges(t *testing.T) {
	s, alice, bob := followEnv(t)
	ctx := context.Background()

	carol := seedUser(t, s, "fol-carol")
	dave := seedUser(t, s, "fol-dave")

	for _, from := range []*User{alice, carol, dave} {
		if _, _, err := s.CreateFollow(ctx, from.ID, bob.ID); err != nil {
			t.Fatalf("建关注边失败：%v", err)
		}
	}
	if _, _, err := s.CreateFollow(ctx, alice.ID, carol.ID); err != nil {
		t.Fatalf("建关注边失败：%v", err)
	}

	followers, err := s.CountFollowers(ctx, bob.ID)
	if err != nil {
		t.Fatalf("统计粉丝数失败：%v", err)
	}
	if followers != 3 {
		t.Fatalf("bob 的粉丝数应为 3，实际 %d", followers)
	}
	following, err := s.CountFollowing(ctx, bob.ID)
	if err != nil {
		t.Fatalf("统计关注数失败：%v", err)
	}
	if following != 0 {
		t.Fatalf("bob 的关注数应为 0，实际 %d", following)
	}
}

// TestBackfillLegacyFollowsFollowsTicket42 覆盖票 42 决议的存量迁移映射。
//
// 四条判据：
//  1. accepted → **双向** follow（互关）；
//  2. pending  → requester→addressee **单向** follow，且**请求行被终结**；
//  3. blocked  → **不动**（拉黑不走关注边）；
//  4. **幂等**：重复回填不产生第二条边，也不重复终结。
func TestBackfillLegacyFollowsFollowsTicket42(t *testing.T) {
	s, alice, bob := followEnv(t)
	ctx := context.Background()

	// alice → bob：accepted（旧好友）。
	if _, err := s.CreateFriendshipRequest(ctx, alice.ID, bob.ID); err != nil {
		t.Fatalf("造请求失败：%v", err)
	}
	if _, err := s.AcceptFriendshipRequest(ctx, 1, bob.ID); err != nil {
		t.Fatalf("接受请求失败：%v", err)
	}
	// carol → alice：pending（旧请求）。
	carol := seedUser(t, s, "fol-carol")
	if _, err := s.CreateFriendshipRequest(ctx, carol.ID, alice.ID); err != nil {
		t.Fatalf("造请求失败：%v", err)
	}
	// dave → alice：blocked（拉黑，不动）。
	dave := seedUser(t, s, "fol-dave")
	if _, err := s.BlockUser(ctx, dave.ID, alice.ID); err != nil {
		t.Fatalf("造拉黑失败：%v", err)
	}

	out, err := s.BackfillLegacyFollows(ctx)
	if err != nil {
		t.Fatalf("回填失败：%v", err)
	}
	// accepted 贡献 2 条、pending 贡献 1 条。
	if out.FollowsCreated != 3 {
		t.Fatalf("应新建 3 条关注边，实际 %d", out.FollowsCreated)
	}
	if out.RequestsRetired != 1 {
		t.Fatalf("应终结 1 条 pending 请求，实际 %d", out.RequestsRetired)
	}

	// accepted → 双向：alice↔bob 都是互关。
	okAB, err := s.IsFollowing(ctx, alice.ID, bob.ID)
	if err != nil || !okAB {
		t.Fatalf("accepted 应补 alice→bob：ok=%v err=%v", okAB, err)
	}
	okBA, err := s.IsFollowing(ctx, bob.ID, alice.ID)
	if err != nil || !okBA {
		t.Fatalf("accepted 应补 bob→alice：ok=%v err=%v", okBA, err)
	}

	// pending → 单向：carol→alice 有，alice→carol 没有。
	okCA, err := s.IsFollowing(ctx, carol.ID, alice.ID)
	if err != nil || !okCA {
		t.Fatalf("pending 应补 carol→alice：ok=%v err=%v", okCA, err)
	}
	okAC, err := s.IsFollowing(ctx, alice.ID, carol.ID)
	if err != nil || okAC {
		t.Fatalf("pending 不该补反向边：ok=%v err=%v", okAC, err)
	}

	// blocked → 不动：dave 与 alice 之间没有任何关注边。
	okDA, err := s.IsFollowing(ctx, dave.ID, alice.ID)
	if err != nil || okDA {
		t.Fatalf("blocked 不该补关注边：ok=%v err=%v", okDA, err)
	}

	// pending 请求行已被终结。
	var pendingRows int64
	if err := s.DB().WithContext(ctx).Model(&Friendship{}).
		Where("status = ?", FriendshipPending).Count(&pendingRows).Error; err != nil {
		t.Fatalf("统计 pending 失败：%v", err)
	}
	if pendingRows != 0 {
		t.Fatalf("pending 请求行应被终结，实际还有 %d 行", pendingRows)
	}

	// 幂等：再来一次不新建边。
	out2, err := s.BackfillLegacyFollows(ctx)
	if err != nil {
		t.Fatalf("重复回填失败：%v", err)
	}
	if out2.FollowsCreated != 0 {
		t.Fatalf("重复回填不该新建边，实际新建 %d 条", out2.FollowsCreated)
	}
	var total int64
	if err := s.DB().WithContext(ctx).Model(&Follow{}).Count(&total).Error; err != nil {
		t.Fatalf("统计关注边失败：%v", err)
	}
	if total != 3 {
		t.Fatalf("库里应有 3 条关注边，实际 %d", total)
	}
}

// ===== 票 #57：can_dm 换门闩 + 存量迁移编排 + 取关后的门闩实效 =====
//
// 与上面的 #56 用例的分工：那边验 can_dm **谓词本身**，
// 这里验谓词**接进私聊发送路径之后**的行为（单向不可发、互关可发、拉黑双向不可发、
// 解黑恢复可发、单方取关后不可发新消息但历史仍可读），
// 以及一次性回填 BackfillLegacyFollowsOnce 的编排（登录时触发一次）。

// TestSendDMGatedByMutualFollow 是票 #57 验收第 1 条的私聊侧证据：
// 发送路径的门闩从 areFriends(accepted) 换成了 can_dm（互关 ∧ 非拉黑）。
//
// 四种状态逐一覆盖：单向 → 403；互关 → 201；任一方向拉黑 → 403；解黑 → 恢复可发。
// 每一条都从**发送端**验（SendDM 返回 ErrMessageNotFriends / ErrMessageBlocked），
// 而不是只看 CanDM —— 那才是"服务端强制"的落点。
func TestSendDMGatedByMutualFollow(t *testing.T) {
	s, alice, bob := followEnv(t)
	ctx := context.Background()

	// 1) 单向关注：不可发。
	if _, _, err := s.CreateFollow(ctx, alice.ID, bob.ID); err != nil {
		t.Fatalf("建关注边失败：%v", err)
	}
	if _, err := s.SendDM(ctx, alice.ID, bob.ID, "你好"); !errors.Is(err, ErrMessageNotFriends) {
		t.Fatalf("单向关注时发私聊应被拒（ErrMessageNotFriends），实际 %v", err)
	}

	// 2) 互关：可发。
	if _, _, err := s.CreateFollow(ctx, bob.ID, alice.ID); err != nil {
		t.Fatalf("建反向边失败：%v", err)
	}
	first, err := s.SendDM(ctx, alice.ID, bob.ID, "你好")
	if err != nil {
		t.Fatalf("互关后应当可以发私聊：%v", err)
	}
	if first.ID <= 0 || first.Body != "你好" {
		t.Fatalf("落库后的消息形状不对：%+v", first)
	}

	// 3) 拉黑（任一方向）：双向都不可发。
	if _, err := s.BlockUser(ctx, bob.ID, alice.ID); err != nil {
		t.Fatalf("拉黑失败：%v", err)
	}
	if _, err := s.SendDM(ctx, alice.ID, bob.ID, "还在吗"); !errors.Is(err, ErrMessageBlocked) {
		t.Fatalf("拉黑后发送方应收到 ErrMessageBlocked，实际 %v", err)
	}
	if _, err := s.SendDM(ctx, bob.ID, alice.ID, "我拉黑了你"); !errors.Is(err, ErrMessageBlocked) {
		t.Fatalf("拉黑方自己也不该发得出去，实际 %v", err)
	}

	// 4) 解黑：恢复可发。
	if err := s.Unblock(ctx, bob.ID, alice.ID); err != nil {
		t.Fatalf("解除拉黑失败：%v", err)
	}
	if _, err := s.SendDM(ctx, alice.ID, bob.ID, "解开了"); err != nil {
		t.Fatalf("解黑后应当恢复可发：%v", err)
	}
}

// TestUnfollowClosesGateButKeepsHistory 是票 #57 验收第 4 条：
// 取关事务 = 删边 → 失互关即不可发新私聊、**历史只读**；对端的边保留。
//
// "历史只读"落成两个断言：ListDM 仍返回旧的几条（读得回来），
// 而 SendDM 新增一条被拒（写不进去）。
func TestUnfollowClosesGateButKeepsHistory(t *testing.T) {
	s, alice, bob := followEnv(t)
	ctx := context.Background()

	if _, _, err := s.CreateFollow(ctx, alice.ID, bob.ID); err != nil {
		t.Fatalf("建关注边失败：%v", err)
	}
	if _, _, err := s.CreateFollow(ctx, bob.ID, alice.ID); err != nil {
		t.Fatalf("建反向边失败：%v", err)
	}
	if _, err := s.SendDM(ctx, alice.ID, bob.ID, "互关期间的一条"); err != nil {
		t.Fatalf("互关期间发消息失败：%v", err)
	}

	// alice 单方取关：只删她这一条边，bob→alice 那条保留。
	removed, err := s.RemoveFollow(ctx, alice.ID, bob.ID)
	if err != nil || !removed {
		t.Fatalf("取关失败：removed=%v err=%v", removed, err)
	}
	stillFollowing, err := s.IsFollowing(ctx, bob.ID, alice.ID)
	if err != nil || !stillFollowing {
		t.Fatalf("对端的边应当保留：ok=%v err=%v", stillFollowing, err)
	}

	// 失互关 → 不可发新私聊（两个方向都不可：can_dm 是对称的）。
	if _, err := s.SendDM(ctx, alice.ID, bob.ID, "取关之后"); !errors.Is(err, ErrMessageNotFriends) {
		t.Fatalf("取关后发送方应被拒，实际 %v", err)
	}
	if _, err := s.SendDM(ctx, bob.ID, alice.ID, "反向也要被拒"); !errors.Is(err, ErrMessageNotFriends) {
		t.Fatalf("取关后对端也不该发得进来，实际 %v", err)
	}

	// 历史只读：旧消息仍然读得到。
	rows, _, err := s.ListDM(ctx, alice.ID, bob.ID, 0, 50)
	if err != nil {
		t.Fatalf("读历史失败：%v", err)
	}
	if len(rows) != 1 || rows[0].Body != "互关期间的一条" {
		t.Fatalf("取关后历史应当仍可读，实际 %d 条：%+v", len(rows), rows)
	}

	// 会话列表也还在（"历史只读"的另一个面）。
	convs, err := s.ListDMConversations(ctx, alice.ID)
	if err != nil {
		t.Fatalf("读会话列表失败：%v", err)
	}
	if len(convs) != 1 || convs[0].PeerID != bob.ID {
		t.Fatalf("取关后会话列表应当仍有这一段，实际 %+v", convs)
	}
}

// TestBackfillLegacyFollowsOnceRunsOnlyOnce 覆盖一次性编排的半部分：
// 第二次调用不再碰库（版本键已落），因此不会重复删 pending 行、也不会重复建边。
//
// 为什么需要这个编排（而不是直接调 BackfillLegacyFollows）：本仓库的迁移规约是
// 「只做加法」，而回填要删 pending 行；它不能被夹在每次启动或每次登录里反复跑 ——
// 于是用 schema_migrations 里的一枚**数据版本键**把它钉成一次性动作。
//
// ⚠ 这条用例**必须**跑在独立的 schema 里（DSN 追加 `search_path=<临时> search_path=<临时>`）：
// 版本键是库级状态，而共享的 public schema 里一旦有任何一个更早的用例
// （或一次真实的启动回填）落过这枚键，"第一次应当执行"就永远不成立。
// 这也是"一次性动作"测试的通病：断言的是**全局副作用**，隔离是前提而不是选项。
func TestBackfillLegacyFollowsOnceRunsOnlyOnce(t *testing.T) {
	s, alice, _ := requireIsolatedSchemaDB(t)
	ctx := context.Background()

	carol := seedUser(t, s, "fol-carol")
	if _, err := s.CreateFriendshipRequest(ctx, carol.ID, alice.ID); err != nil {
		t.Fatalf("造 pending 请求失败：%v", err)
	}

	first, ran, err := s.BackfillLegacyFollowsOnce(ctx)
	if err != nil {
		t.Fatalf("第一次一次性回填失败：%v", err)
	}
	if !ran {
		t.Fatalf("第一次应当真的执行了回填")
	}
	if first.FollowsCreated != 1 || first.RequestsRetired != 1 {
		t.Fatalf("回填结果不对：%+v", first)
	}

	// 第二次：不再执行（版本键已落），返回 ran=false。
	second, ran2, err := s.BackfillLegacyFollowsOnce(ctx)
	if err != nil {
		t.Fatalf("第二次调用应当优雅跳过：%v", err)
	}
	if ran2 {
		t.Fatalf("第二次不该再执行回填")
	}
	if second != (LegacyFollowBackfill{}) {
		t.Fatalf("跳过的调用不该报告任何回填动作：%+v", second)
	}

	// 版本键在库里。
	var n int64
	if err := s.DB().WithContext(ctx).Table("schema_migrations").
		Where("version = ?", legacyFollowsDataVersion).Count(&n).Error; err != nil {
		t.Fatalf("查询版本键失败：%v", err)
	}
	if n != 1 {
		t.Fatalf("版本键应当只落一行，实际 %d 行", n)
	}

	// 再手动跑一遍裸回填确认幂等（即使版本键被谁删了也不会双写边）。
	if out, err := s.BackfillLegacyFollows(ctx); err != nil || out.FollowsCreated != 0 {
		t.Fatalf("裸回填应当幂等：out=%+v err=%v", out, err)
	}
}

// TestCanDMOverridesLegacyAcceptedFriendship 覆盖迁移的**语义**那一半：
// 存量 accepted 好友被回填成互关之后，私聊门闩才认他们 ——
// 而在此之前，单凭 friendships 里的 accepted 行已经发不出消息了。
//
// 这条用例是"换门闩"的回归锚：如果有人把 SendDM 改回 areFriends(accepted)，
// 第一段断言会失败（旧行还在，却发得出去）。
func TestCanDMOverridesLegacyAcceptedFriendship(t *testing.T) {
	s, alice, bob := followEnv(t)
	ctx := context.Background()

	// 造一段**旧**好友关系（accepted），但不补关注边。
	req, err := s.CreateFriendshipRequest(ctx, alice.ID, bob.ID)
	if err != nil {
		t.Fatalf("造旧好友请求失败：%v", err)
	}
	if _, err := s.AcceptFriendshipRequest(ctx, req.ID, bob.ID); err != nil {
		t.Fatalf("接受旧好友请求失败：%v", err)
	}

	// 门闩已经是 can_dm：旧 accepted 行不再构成发消息资格。
	if _, err := s.SendDM(ctx, alice.ID, bob.ID, "旧好友时代"); !errors.Is(err, ErrMessageNotFriends) {
		t.Fatalf("仅凭旧 accepted 行不该能发私聊，实际 %v", err)
	}

	// 回填之后才互关。
	if _, err := s.BackfillLegacyFollows(ctx); err != nil {
		t.Fatalf("回填失败：%v", err)
	}
	if _, err := s.SendDM(ctx, alice.ID, bob.ID, "迁移之后"); err != nil {
		t.Fatalf("回填成互关后应当可以发：%v", err)
	}
}
