package store

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"gorm.io/gorm"
)

// newFriendFixture 造三个用户（a 发起、b 接收、c 旁观），返回 store 与 id。
func newFriendFixture(t *testing.T) (*Store, int64, int64, int64) {
	t.Helper()
	s := requireTestDB(t)
	ctx := context.Background()
	ids := make([]int64, 0, 3)
	for _, name := range []string{"frienda", "friendb", "friendc"} {
		u := &User{Username: name, DisplayName: name, PasswordHash: "$2a$10$xx"}
		if err := s.CreateUser(ctx, u); err != nil {
			t.Fatalf("CreateUser(%s): %v", name, err)
		}
		ids = append(ids, u.ID)
	}
	return s, ids[0], ids[1], ids[2]
}

// TestFriendshipLifecycle 覆盖 pending → accepted → remove 的完整生命周期，
// 以及"一段关系只存一行"与"只有 addressee 能 accept/reject"。
func TestFriendshipLifecycle(t *testing.T) {
	s, a, b, c := newFriendFixture(t)
	ctx := context.Background()

	// a 向 b 发请求
	req, err := s.CreateFriendshipRequest(ctx, a, b)
	if err != nil || req.Status != FriendshipPending {
		t.Fatalf("发请求应 pending: %+v err=%v", req, err)
	}
	if req.RequesterID != a || req.AddresseeID != b {
		t.Fatalf("登记角色错误: req=%d addr=%d", req.RequesterID, req.AddresseeID)
	}

	// 重复发（a 再发一次）→ 已 pending，不新建
	if _, err := s.CreateFriendshipRequest(ctx, a, b); !errors.Is(err, ErrRequestAlreadyPending) {
		t.Fatalf("重复请求期望 ErrRequestAlreadyPending，得到 %v", err)
	}
	// 反方向（b 向 a）也发 → 同样已 pending（一段关系只存一行，锁两个方向）
	if _, err := s.CreateFriendshipRequest(ctx, b, a); !errors.Is(err, ErrRequestAlreadyPending) {
		t.Fatalf("反方向请求期望 ErrRequestAlreadyPending，得到 %v", err)
	}

	// 只有 b（addressee）能 accept；c 无权 → ErrFriendshipForbidden
	if _, err := s.AcceptFriendshipRequest(ctx, req.ID, c); !errors.Is(err, ErrFriendshipForbidden) {
		t.Fatalf("第三人 accept 期望 Forbidden，得到 %v", err)
	}
	// a（requester）也无权 accept 自己的请求（有向语义）
	if _, err := s.AcceptFriendshipRequest(ctx, req.ID, a); !errors.Is(err, ErrFriendshipForbidden) {
		t.Fatalf("请求方 accept 期望 Forbidden，得到 %v", err)
	}

	// b 接受
	acc, err := s.AcceptFriendshipRequest(ctx, req.ID, b)
	if err != nil || acc.Status != FriendshipAccepted {
		t.Fatalf("accept 应 accepted: %+v err=%v", acc, err)
	}

	// 好友列表对称：a 与 b 都能看到对方，c 看不到
	friendsA, err := s.ListFriends(ctx, a)
	if err != nil || len(friendsA) != 1 || friendsA[0].PeerName != "friendb" {
		t.Fatalf("a 的好友应只有 b: %+v err=%v", friendsA, err)
	}
	friendsB, err := s.ListFriends(ctx, b)
	if err != nil || len(friendsB) != 1 || friendsB[0].PeerName != "frienda" {
		t.Fatalf("b 的好友应只有 a: %+v err=%v", friendsB, err)
	}
	friendsC, _ := s.ListFriends(ctx, c)
	if len(friendsC) != 0 {
		t.Fatalf("c 不应有好友: %+v", friendsC)
	}

	// 删除好友：双方都能删；删后列表空
	if err := s.RemoveFriendship(ctx, acc.ID, a); err != nil {
		t.Fatalf("a 删好友: %v", err)
	}
	if fa, _ := s.ListFriends(ctx, a); len(fa) != 0 {
		t.Fatalf("删除后 a 不应有好友: %+v", fa)
	}
}

// TestFriendshipBlock 覆盖拉黑语义：覆盖已有关系、双向不可见、被拉黑方发不出请求。
func TestFriendshipBlock(t *testing.T) {
	s, a, b, _ := newFriendFixture(t)
	ctx := context.Background()

	// a 拉黑 b（无既有关系 → 新建 blocked）
	blk, err := s.BlockUser(ctx, a, b)
	if err != nil || blk.Status != FriendshipBlocked || derefInt64(blk.BlockedByID) != a {
		t.Fatalf("拉黑应 blocked 且 blocked_by_id=a: %+v err=%v", blk, err)
	}

	// 被拉黑方（b）发请求 → ErrBlockedByTarget（服务端强制）
	if _, err := s.CreateFriendshipRequest(ctx, b, a); !errors.Is(err, ErrBlockedByTarget) {
		t.Fatalf("被拉黑方发请求期望 ErrBlockedByTarget，得到 %v", err)
	}

	// BlockStatusBetween：双向都能查到 blocked，且 blocked_by 是 a
	blocked, by, err := s.BlockStatusBetween(ctx, a, b)
	if err != nil || !blocked || by != a {
		t.Fatalf("BlockStatusBetween(a,b) 应 blocked by a: %v,%d err=%v", blocked, by, err)
	}
	blocked, by, err = s.BlockStatusBetween(ctx, b, a)
	if err != nil || !blocked || by != a {
		t.Fatalf("BlockStatusBetween(b,a) 应 blocked by a: %v,%d err=%v", blocked, by, err)
	}

	// 拉黑后列表互不可见（都不是 accepted，list 里没有对方）
	fa, _ := s.ListFriends(ctx, a)
	fb, _ := s.ListFriends(ctx, b)
	if len(fa) != 0 || len(fb) != 0 {
		t.Fatalf("拉黑后应互不可见: a=%+v b=%+v", fa, fb)
	}
}

// TestFriendshipBlockOverridesAccepted 拉黑覆盖已 accept 的关系（§7.1 服务端强制）。
func TestFriendshipBlockOverridesAccepted(t *testing.T) {
	s, a, b, _ := newFriendFixture(t)
	ctx := context.Background()

	req, err := s.CreateFriendshipRequest(ctx, a, b)
	if err != nil {
		t.Fatalf("发请求: %v", err)
	}
	if _, err := s.AcceptFriendshipRequest(ctx, req.ID, b); err != nil {
		t.Fatalf("accept: %v", err)
	}

	// b 拉黑 a：关系行原地转 blocked，blocked_by_id=b（addressee 拉黑 requester，
	// 这正是 blocked_by_id 必须独立成列的原因）
	blk, err := s.BlockUser(ctx, b, a)
	if err != nil || blk.Status != FriendshipBlocked || derefInt64(blk.BlockedByID) != b {
		t.Fatalf("拉黑覆盖好友关系失败: %+v err=%v", blk, err)
	}

	// 关系行只有一条（一段关系只存一行）
	var count int64
	if err := s.db.Raw(`SELECT count(*) FROM friendships WHERE requester_id=? AND addressee_id=?`,
		a, b).Scan(&count).Error; err != nil {
		t.Fatalf("数关系行: %v", err)
	}
	if count != 1 {
		t.Fatalf("拉黑覆盖后应仍只有一条关系行，实际 %d", count)
	}
}

// TestFriendshipSelfRelation 不能对自己建立关系。
func TestFriendshipSelfRelation(t *testing.T) {
	s, a, _, _ := newFriendFixture(t)
	ctx := context.Background()
	if _, err := s.CreateFriendshipRequest(ctx, a, a); !errors.Is(err, ErrSelfRelation) {
		t.Fatalf("自己发请求期望 ErrSelfRelation，得到 %v", err)
	}
	if _, err := s.BlockUser(ctx, a, a); !errors.Is(err, ErrSelfRelation) {
		t.Fatalf("自己拉黑期望 ErrSelfRelation，得到 %v", err)
	}
}

// TestSearchUsersExcludesSelfAndCarriesRelations 覆盖 #39 的搜索语义：
// 排除自己、按 username 与 display_name 两列匹配、结果带关系状态所需的原始行。
func TestSearchUsersExcludesSelfAndCarriesRelations(t *testing.T) {
	s, a, b, c := newFriendFixture(t)
	ctx := context.Background()

	// 造几种关系，让"关系状态"有东西可判：
	//   a → b：pending（a 发出，pending-out）
	//   c → a：pending（a 收到，pending-in）
	//   a 拉黑 c：那唯一一行原地转 blocked，blocked_by=a
	// 于是 a 搜索 c 应得到 blocked（不是隐藏），a 搜索 b 应得到 pending 行。
	if _, err := s.CreateFriendshipRequest(ctx, a, b); err != nil {
		t.Fatalf("a→b 请求: %v", err)
	}
	if _, err := s.CreateFriendshipRequest(ctx, c, a); err != nil {
		t.Fatalf("c→a 请求: %v", err)
	}
	if _, err := s.BlockUser(ctx, a, c); err != nil {
		t.Fatalf("a 拉黑 c: %v", err)
	}

	// 空关键词：不去查库，直接空结果。
	if hits, err := s.SearchUsers(ctx, "   ", a); err != nil || len(hits) != 0 {
		t.Fatalf("空关键词应返回空: %+v err=%v", hits, err)
	}

	// 按 display_name 前缀搜：三个用户里排除自己，只剩 b 与 c。
	hits, err := s.SearchUsers(ctx, "friend", a)
	if err != nil {
		t.Fatalf("搜索失败: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("应命中 b 与 c（排除 a 自己）: %+v", hits)
	}
	for _, h := range hits {
		if h.ID == a {
			t.Fatalf("搜索结果不该包含自己: %+v", hits)
		}
	}

	// 按 username 精确搜（username 全小写存储，查询侧同一套归一化）。
	hits, err = s.SearchUsers(ctx, "FRIENDB", a)
	if err != nil || len(hits) != 1 || hits[0].ID != b {
		t.Fatalf("按 username 搜应命中 b（大小写不敏感）: %+v err=%v", hits, err)
	}

	// blocked 行仍作为结果出现（要显示"已拉黑"，不是隐藏）。
	blockedRow, err := relationBetweenIn(ctx, s.db, a, c)
	if err != nil {
		t.Fatalf("查 a-c 关系: %v", err)
	}
	if blockedRow == nil || blockedRow.Status != FriendshipBlocked || derefInt64(blockedRow.BlockedByID) != a {
		t.Fatalf("a-c 应是被 a 拉黑的 blocked 行: %+v", blockedRow)
	}

	// 通配符按字面处理：搜 "friend_" 不该命中 "friendb"（_ 被转义）。
	hits, err = s.SearchUsers(ctx, "friend_", a)
	if err != nil || len(hits) != 0 {
		t.Fatalf("下划线应被当字面量（无人匹配）: %+v err=%v", hits, err)
	}

	// 结果集上限：再造 25 个人，搜共同前缀，最多 searchLimit 条。
	for i := 0; i < 25; i++ {
		u := &User{Username: "crowd" + string(rune('a'+i)), DisplayName: "人群" + string(rune('a'+i)), PasswordHash: "$2a$10$xx"}
		if err := s.CreateUser(ctx, u); err != nil {
			t.Fatalf("CreateUser(crowd%c): %v", rune('a'+i), err)
		}
	}
	hits, err = s.SearchUsers(ctx, "人群", a)
	if err != nil {
		t.Fatalf("搜索人群失败: %v", err)
	}
	if len(hits) != searchLimit {
		t.Fatalf("应截到 %d 条: got %d", searchLimit, len(hits))
	}
}

// TestListBlockedOnlyMine 覆盖 #39 的黑名单语义：只列我拉黑的人，
// 别人拉黑我的那一行不可见。
func TestListBlockedOnlyMine(t *testing.T) {
	s, a, b, c := newFriendFixture(t)
	ctx := context.Background()

	// 初始为空（不是 nil 也不是报错）。
	rows, err := s.ListBlocked(ctx, a)
	if err != nil || len(rows) != 0 {
		t.Fatalf("初始黑名单应为空: %+v err=%v", rows, err)
	}

	// a 拉黑 b 与 c。
	if _, err := s.BlockUser(ctx, a, b); err != nil {
		t.Fatalf("a 拉黑 b: %v", err)
	}
	if _, err := s.BlockUser(ctx, a, c); err != nil {
		t.Fatalf("a 拉黑 c: %v", err)
	}

	// b 拉黑 a：**不会新建第二行** —— BlockUser 命中已有的 (a,b) 那一行并原地翻转
	// （#29 的"一段关系只存一行"）。翻转后 blocked_by 从 a 变成 b，
	// 于是 a 的黑名单从 {b,c} 缩成 {c}。这正是单向语义在单行模型下的样子：
	// "谁拉黑谁"是那一行上的一个字段，不是两行各自的记录。
	if _, err := s.BlockUser(ctx, b, a); err != nil {
		t.Fatalf("b 拉黑 a: %v", err)
	}

	// a 的黑名单 = {c}：b 那一行已经不归 a 了（它现在是 b 拉黑 a）。
	// ⚠ 这条同时钉住一个容易搞错的方向：ListBlocked 必须按 blocked_by_id 判归属，
	// 而不是按 requester/addressee —— 那一行 requester 仍是 a（登记角色不变）。
	rows, err = s.ListBlocked(ctx, a)
	if err != nil {
		t.Fatalf("列黑名单失败: %v", err)
	}
	if len(rows) != 1 || rows[0].BlockedID != c {
		t.Fatalf("a 的黑名单应只剩 c: %+v", rows)
	}
	if rows[0].DisplayName != "friendc" || rows[0].Username != "friendc" {
		t.Fatalf("c 那行应带显示名与用户名: %+v", rows[0])
	}

	// b 的黑名单 = {a}：那一行现在归 b，且被拉黑方（peer）要按登记角色反推出来。
	bRows, err := s.ListBlocked(ctx, b)
	if err != nil || len(bRows) != 1 || bRows[0].BlockedID != a {
		t.Fatalf("b 的黑名单应只有 a: %+v err=%v", bRows, err)
	}
	if bRows[0].DisplayName != "frienda" {
		t.Fatalf("b 的黑名单行应带 a 的显示名: %+v", bRows[0])
	}
}

// TestUnblockReturnsToNone 覆盖 #39 的解除拉黑：删掉那一行、关系回到 none、
// 之后能重新发请求；且动不了别人拉黑的行（统一 404 的 store 侧依据）。
func TestUnblockReturnsToNone(t *testing.T) {
	s, a, b, c := newFriendFixture(t)
	ctx := context.Background()

	// a 拉黑 b，再让 c 拉黑 a（c→a 那行是别人拉黑我）。
	if _, err := s.BlockUser(ctx, a, b); err != nil {
		t.Fatalf("a 拉黑 b: %v", err)
	}
	if _, err := s.BlockUser(ctx, c, a); err != nil {
		t.Fatalf("c 拉黑 a: %v", err)
	}

	// a 解除对 b 的拉黑。
	if err := s.Unblock(ctx, a, b); err != nil {
		t.Fatalf("解除拉黑: %v", err)
	}

	// 关系回到"没有关系"（不是回到好友/pending）。
	row, err := relationBetweenIn(ctx, s.db, a, b)
	if err != nil {
		t.Fatalf("查 a-b 关系: %v", err)
	}
	if row != nil {
		t.Fatalf("解除后那行应被删除: %+v", row)
	}

	// 解除后能重新发请求（验收第 1 条）。
	req, err := s.CreateFriendshipRequest(ctx, a, b)
	if err != nil || req.Status != FriendshipPending {
		t.Fatalf("解除后应能重新发请求: %+v err=%v", req, err)
	}

	// 重复解除 → ErrBlockedRowNotFound（幂等且不泄露存在性）。
	if err := s.Unblock(ctx, a, b); !errors.Is(err, ErrBlockedRowNotFound) {
		t.Fatalf("重复解除期望 ErrBlockedRowNotFound，得到 %v", err)
	}

	// a 试图解除"c 拉黑 a"的那一行：无权，同一错误。
	if err := s.Unblock(ctx, a, c); !errors.Is(err, ErrBlockedRowNotFound) {
		t.Fatalf("解除别人拉黑的行期望 ErrBlockedRowNotFound，得到 %v", err)
	}
	// 那一行必须还在（服务端强制不能被绕开）。
	cRow, err := relationBetweenIn(ctx, s.db, c, a)
	if err != nil {
		t.Fatalf("查 c-a 关系: %v", err)
	}
	if cRow == nil || cRow.Status != FriendshipBlocked || derefInt64(cRow.BlockedByID) != c {
		t.Fatalf("别人拉黑我的行不该被改动: %+v", cRow)
	}

	// 未拉黑过的两个人之间解除 → 同一错误（不区分"没有"与"不是你的"）。
	if err := s.Unblock(ctx, a, c); !errors.Is(err, ErrBlockedRowNotFound) {
		t.Fatalf("从未拉黑过的人解除期望 ErrBlockedRowNotFound，得到 %v", err)
	}
	if _, err := s.BlockUser(ctx, a, a); !errors.Is(err, ErrSelfRelation) {
		t.Fatalf("自己解除期望 ErrSelfRelation，得到 %v", err)
	}
}

// relationBetweenIn 返回 a 与 b 之间那唯一的一行（任一方向），没有则 nil。
//
// 与 areFriendsIn / blockStatusBetweenIn 同一条纪律：吃 *gorm.DB 而不是连接池，
// 让用例能在同一把连接上验证"事务里看到的与事务外看到的一致"。
func relationBetweenIn(ctx context.Context, db *gorm.DB, a, b int64) (*Friendship, error) {
	var f Friendship
	err := db.WithContext(ctx).
		Where(`(requester_id = ? AND addressee_id = ?) OR (requester_id = ? AND addressee_id = ?)`, a, b, b, a).
		Take(&f).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: 查询好友关系失败: %w", err)
	}
	return &f, nil
}
