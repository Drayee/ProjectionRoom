package store

import (
	"context"
	"testing"
	"time"
)

// ownerRoomsEnv 造一个房主 + 三间房（公开未关 / 私密未关 / 公开已关），
// 外加另一个房主的一间房（用来验"只看自己的"）。
func ownerRoomsEnv(t *testing.T) (*Store, *User, *User) {
	t.Helper()
	s := requireTestDB(t)
	ctx := context.Background()

	owner := seedUser(t, s, "rooms-owner")
	other := seedUser(t, s, "rooms-other")

	now := time.Now()
	closed := now.Add(-time.Hour)
	metas := []*RoomMeta{
		{RoomID: "OWNOpen01", OwnerUserID: owner.ID, Title: "公开房", IsPublic: true},
		{RoomID: "OWNPriv01", OwnerUserID: owner.ID, Title: "私密房", IsPublic: false},
		{RoomID: "OWNClos01", OwnerUserID: owner.ID, Title: "已关房", IsPublic: true, ClosedAt: &closed},
		{RoomID: "OTHOpen01", OwnerUserID: other.ID, Title: "别人的房", IsPublic: true},
	}
	for _, m := range metas {
		if err := s.UpsertRoomMeta(ctx, m); err != nil {
			t.Fatalf("UpsertRoomMeta(%s): %v", m.RoomID, err)
		}
	}
	return s, owner, other
}

func roomIDsOf(rows []RoomMeta) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.RoomID)
	}
	return out
}

// TestListRoomsByOwnerIncludesAllStates 钉住「我的放映室」与「公开房列表」的**口径差异**：
// 前者要 owner 的全部房（含未公开、含已关闭），后者只要公开且未关闭。
//
// 为什么这个差异必须钉住：如果这里复用了 ListPublicRoomsByOwner 的过滤条件，
// 症状是"我把房关掉了，它就凭空消失了" —— 而房主恰恰是唯一需要看到那间房
// 来重新开放它的人。
func TestListRoomsByOwnerIncludesAllStates(t *testing.T) {
	s, owner, _ := ownerRoomsEnv(t)
	ctx := context.Background()

	rows, hasMore, err := s.ListRoomsByOwner(ctx, owner.ID, 10, 0)
	if err != nil {
		t.Fatalf("ListRoomsByOwner: %v", err)
	}
	if hasMore {
		t.Fatalf("三间房不该 hasMore")
	}
	got := roomIDsOf(rows)
	if len(got) != 3 {
		t.Fatalf("应当看到自己的全部三间房（含私密与已关），实得 %v", got)
	}
	seen := map[string]bool{}
	for _, id := range got {
		seen[id] = true
	}
	for _, want := range []string{"OWNOpen01", "OWNPriv01", "OWNClos01"} {
		if !seen[want] {
			t.Fatalf("缺少 %s，实得 %v", want, got)
		}
	}
	if seen["OTHOpen01"] {
		t.Fatalf("不该看到别人的房，实得 %v", got)
	}
}

// TestListRoomsByOwnerPages 钉住分页不重不漏。
func TestListRoomsByOwnerPages(t *testing.T) {
	s, owner, _ := ownerRoomsEnv(t)
	ctx := context.Background()

	seen := map[string]bool{}
	for offset := 0; offset < 3; offset += 2 {
		rows, _, err := s.ListRoomsByOwner(ctx, owner.ID, 2, offset)
		if err != nil {
			t.Fatalf("ListRoomsByOwner(offset=%d): %v", offset, err)
		}
		for _, r := range rows {
			if seen[r.RoomID] {
				t.Fatalf("翻页出现重复：%s", r.RoomID)
			}
			seen[r.RoomID] = true
		}
	}
	if len(seen) != 3 {
		t.Fatalf("翻页应当覆盖全部 3 间，实得 %d", len(seen))
	}
}

// TestListRoomsByOwnerEmptyIsSlice 钉住空结果是 []，不是 nil。
//
// JSON 序列化 nil slice 得到 `null`，而前端契约是 `items: []` ——
// 一个 `null` 会让"没有房间"在页面上变成一次解构异常。
func TestListRoomsByOwnerEmptyIsSlice(t *testing.T) {
	s, _, other := ownerRoomsEnv(t)
	ctx := context.Background()

	// 造一个没有任何房间的用户
	bare := seedUser(t, s, "rooms-bare")
	rows, hasMore, err := s.ListRoomsByOwner(ctx, bare.ID, 10, 0)
	if err != nil {
		t.Fatalf("ListRoomsByOwner: %v", err)
	}
	if rows == nil {
		t.Fatalf("空结果应当是 [] 而不是 nil")
	}
	if len(rows) != 0 || hasMore {
		t.Fatalf("没有房间的用户应当返回空且 hasMore=false，实得 %v hasMore=%v", roomIDsOf(rows), hasMore)
	}

	// guestID = 0 也不该报错（与关注名单同一条纪律）
	if _, _, err := s.ListRoomsByOwner(ctx, 0, 10, 0); err != nil {
		t.Fatalf("userID=0 不该报错：%v", err)
	}
	_ = other
}
