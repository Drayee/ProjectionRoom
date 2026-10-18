package store

import (
	"context"
	"errors"
	"testing"
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
