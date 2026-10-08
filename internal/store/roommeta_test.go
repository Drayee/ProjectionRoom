package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func mustUpsertRoom(t *testing.T, s *Store, m *RoomMeta) {
	t.Helper()
	if err := s.UpsertRoomMeta(context.Background(), m); err != nil {
		t.Fatalf("登记房间元数据失败：%v", err)
	}
}

func TestUpsertRoomMetaInsertThenOverwrite(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	owner := seedUser(t, s, "host")
	firstSeen := time.Now().Add(-2 * time.Hour)

	meta := &RoomMeta{
		RoomID:      "ROOM0001",
		OwnerUserID: owner.ID,
		Title:       "客厅",
		IsPublic:    false,
		HasPassword: true,
		CreatedAt:   firstSeen,
		LastSeenAt:  firstSeen,
	}
	mustUpsertRoom(t, s, meta)

	got, err := s.RoomMetaByID(ctx, "ROOM0001")
	if err != nil {
		t.Fatalf("查房间元数据失败：%v", err)
	}
	if got.OwnerUserID != owner.ID || got.Title != "客厅" || got.IsPublic || !got.HasPassword {
		t.Fatalf("首次登记内容不对：%+v", got)
	}
	ensureTimeClose(t, "created_at", firstSeen, got.CreatedAt)
	if got.ClosedAt != nil {
		t.Fatalf("新房间不该是已关闭状态")
	}

	// 同房间码再登记：覆盖可变列，但 created_at 保持首次值。
	later := time.Now()
	mustUpsertRoom(t, s, &RoomMeta{
		RoomID:      "ROOM0001",
		OwnerUserID: owner.ID,
		Title:       "客厅（公开）",
		IsPublic:    true,
		HasPassword: false,
		CreatedAt:   later, // 故意传新时间：不允许覆盖
		LastSeenAt:  later,
	})

	updated, err := s.RoomMetaByID(ctx, "ROOM0001")
	if err != nil {
		t.Fatalf("查房间元数据失败：%v", err)
	}
	if updated.Title != "客厅（公开）" || !updated.IsPublic || updated.HasPassword {
		t.Fatalf("覆盖分支没生效：%+v", updated)
	}
	ensureTimeClose(t, "last_seen_at", later, updated.LastSeenAt)
	ensureTimeClose(t, "created_at（应保持首次值）", firstSeen, updated.CreatedAt)

	var count int64
	if err := s.DB().Model(&RoomMeta{}).Count(&count).Error; err != nil {
		t.Fatalf("统计房间失败：%v", err)
	}
	if count != 1 {
		t.Fatalf("Upsert 不该产生第二行，实际 %d 行", count)
	}
}

func TestUpsertRoomMetaValidation(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	owner := seedUser(t, s, "host")

	cases := []struct {
		name string
		meta *RoomMeta
		want string
	}{
		{"nil", nil, "nil"},
		{"缺 room_id", &RoomMeta{OwnerUserID: owner.ID}, "room_id"},
		{"缺 owner", &RoomMeta{RoomID: "ROOM0002"}, "owner_user_id"},
		{"标题超长", &RoomMeta{RoomID: "ROOM0003", OwnerUserID: owner.ID, Title: strings.Repeat("标", maxTitleLen+1)}, "超过"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := s.UpsertRoomMeta(ctx, c.meta)
			if err == nil {
				t.Fatalf("应该报错")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("错误信息应包含 %q，实际 %v", c.want, err)
			}
		})
	}

	// 房主不存在：由外键挡住（§4 的 REFERENCES users(id)）。
	err := s.UpsertRoomMeta(ctx, &RoomMeta{RoomID: "ROOM0004", OwnerUserID: 999999})
	if err == nil {
		t.Fatalf("不存在的房主应该被外键挡住")
	}
}

func TestRoomMetaByIDNotFound(t *testing.T) {
	s := requireTestDB(t)
	if _, err := s.RoomMetaByID(context.Background(), "NOPE0001"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的房间码应返回 ErrNotFound，实际 %v", err)
	}
}

func TestUpdateRoomMetaPatch(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	owner := seedUser(t, s, "host")
	mustUpsertRoom(t, s, &RoomMeta{RoomID: "ROOM0010", OwnerUserID: owner.ID, Title: "旧标题", HasPassword: true})

	// 只改标题：其它字段必须原样保留（patch 语义）。
	title := "新标题"
	if err := s.UpdateRoomMeta(ctx, "ROOM0010", RoomMetaPatch{Title: &title}); err != nil {
		t.Fatalf("局部更新失败：%v", err)
	}
	got, err := s.RoomMetaByID(ctx, "ROOM0010")
	if err != nil {
		t.Fatalf("查房间失败：%v", err)
	}
	if got.Title != title {
		t.Fatalf("标题没改成功：%q", got.Title)
	}
	if !got.HasPassword || got.IsPublic {
		t.Fatalf("patch 不该动未指定的字段：%+v", got)
	}

	// 一次改多项：公开性 + 关闭时间。
	isPublic := true
	closedAt := time.Now()
	if err := s.UpdateRoomMeta(ctx, "ROOM0010", RoomMetaPatch{IsPublic: &isPublic, ClosedAt: &closedAt}); err != nil {
		t.Fatalf("局部更新失败：%v", err)
	}
	got, err = s.RoomMetaByID(ctx, "ROOM0010")
	if err != nil {
		t.Fatalf("查房间失败：%v", err)
	}
	if !got.IsPublic || got.ClosedAt == nil {
		t.Fatalf("多字段 patch 没生效：%+v", got)
	}
	ensureTimeClose(t, "closed_at", closedAt, *got.ClosedAt)

	// 空 patch 必须报错：静默 no-op 会让"界面点了没反应"变成查不出来的 bug。
	if err := s.UpdateRoomMeta(ctx, "ROOM0010", RoomMetaPatch{}); err == nil {
		t.Fatalf("空 patch 应该报错")
	}
	// 标题超长先被应用层挡住。
	tooLong := strings.Repeat("标", maxTitleLen+1)
	if err := s.UpdateRoomMeta(ctx, "ROOM0010", RoomMetaPatch{Title: &tooLong}); err == nil {
		t.Fatalf("标题超长应该报错")
	}
	// 目标不存在。
	if err := s.UpdateRoomMeta(ctx, "NOPE0002", RoomMetaPatch{Title: &title}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的房间应返回 ErrNotFound，实际 %v", err)
	}
	// 空房间码。
	if err := s.UpdateRoomMeta(ctx, "   ", RoomMetaPatch{Title: &title}); err == nil {
		t.Fatalf("空房间码应该报错")
	}
}

func TestListPublicRoomIDs(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	owner := seedUser(t, s, "host")
	now := time.Now()

	// 公开且未关闭（两个，last_seen_at 不同）
	mustUpsertRoom(t, s, &RoomMeta{RoomID: "PUB00001", OwnerUserID: owner.ID, IsPublic: true, LastSeenAt: now.Add(-time.Minute)})
	mustUpsertRoom(t, s, &RoomMeta{RoomID: "PUB00002", OwnerUserID: owner.ID, IsPublic: true, LastSeenAt: now})
	// 非公开
	mustUpsertRoom(t, s, &RoomMeta{RoomID: "PRV00001", OwnerUserID: owner.ID, IsPublic: false, LastSeenAt: now})
	// 公开但已关闭
	closed := now.Add(-time.Second)
	mustUpsertRoom(t, s, &RoomMeta{RoomID: "CLS00001", OwnerUserID: owner.ID, IsPublic: true, LastSeenAt: now, ClosedAt: &closed})

	ids, err := s.ListPublicRoomIDs(ctx, 0) // 0 → 默认 limit
	if err != nil {
		t.Fatalf("查公开房列表失败：%v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("应只返回公开且未关闭的房间，实际 %v", ids)
	}
	// 排序 last_seen_at DESC：最近活跃的在前。
	if ids[0] != "PUB00002" || ids[1] != "PUB00001" {
		t.Fatalf("公开房应按 last_seen_at DESC 排序，实际 %v", ids)
	}

	limited, err := s.ListPublicRoomIDs(ctx, 1)
	if err != nil {
		t.Fatalf("查公开房列表失败：%v", err)
	}
	if len(limited) != 1 || limited[0] != "PUB00002" {
		t.Fatalf("limit=1 应返回最近活跃的那个，实际 %v", limited)
	}

	// 关闭后不该再出现。
	if err := s.UpdateRoomMeta(ctx, "PUB00002", RoomMetaPatch{ClosedAt: &closed}); err != nil {
		t.Fatalf("关闭房间失败：%v", err)
	}
	afterClose, err := s.ListPublicRoomIDs(ctx, 10)
	if err != nil {
		t.Fatalf("查公开房列表失败：%v", err)
	}
	if len(afterClose) != 1 || afterClose[0] != "PUB00001" {
		t.Fatalf("关闭的房间应从列表消失，实际 %v", afterClose)
	}

	// 超大 limit 被截断（不报错）。
	if _, err := s.ListPublicRoomIDs(ctx, maxPublicRoomLimit*10); err != nil {
		t.Fatalf("超大 limit 不该报错：%v", err)
	}

	// 空库返回空切片而不是 nil。
	truncateAll(t, s)
	empty, err := s.ListPublicRoomIDs(ctx, 10)
	if err != nil {
		t.Fatalf("空库查询失败：%v", err)
	}
	if len(empty) != 0 || empty == nil {
		t.Fatalf("空库应返回空切片，实际 %v", empty)
	}
}

// TestRoomMetaCascadesWithUserDelete 验证 §4 的 ON DELETE CASCADE：
// 删用户时其房间元数据一起走（实时状态本来就在内存里）。
func TestRoomMetaCascadesWithUserDelete(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	owner := seedUser(t, s, "host")
	mustUpsertRoom(t, s, &RoomMeta{RoomID: "ROOM0020", OwnerUserID: owner.ID})

	if err := s.DB().Exec(`DELETE FROM users WHERE id = ?`, owner.ID).Error; err != nil {
		t.Fatalf("删除用户失败：%v", err)
	}
	if _, err := s.RoomMetaByID(ctx, "ROOM0020"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删用户后房间元数据应被级联删除，实际 %v", err)
	}
}
