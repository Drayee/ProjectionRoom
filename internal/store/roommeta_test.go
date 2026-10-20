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

// TestOfficialRoomListOrdering 覆盖 0014（wayfinder #35）的公开读半边：
// 只列官方且未关闭的房间，按 official_rank DESC + room_id ASC 稳定排序，
// 非官方的房间一律不出现（哪怕它公开、哪怕 last_seen_at 更新）。
func TestOfficialRoomListOrdering(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	owner := seedUser(t, s, "host")
	now := time.Now()

	// 三个官方房：rank 5 / rank 5（同值）/ rank 9。同值的那两个用来验第二排序键。
	mustUpsertRoom(t, s, &RoomMeta{RoomID: "OFF00001", OwnerUserID: owner.ID, IsPublic: true, IsOfficial: true, OfficialRank: 5, LastSeenAt: now.Add(-time.Minute)})
	mustUpsertRoom(t, s, &RoomMeta{RoomID: "OFF00002", OwnerUserID: owner.ID, IsPublic: true, IsOfficial: true, OfficialRank: 5, LastSeenAt: now})
	mustUpsertRoom(t, s, &RoomMeta{RoomID: "OFF00003", OwnerUserID: owner.ID, IsPublic: true, IsOfficial: true, OfficialRank: 9, LastSeenAt: now})
	// 官方但已关闭：不进列表（与公开列表同口径）。
	closed := now.Add(-time.Second)
	mustUpsertRoom(t, s, &RoomMeta{RoomID: "OFF00009", OwnerUserID: owner.ID, IsPublic: true, IsOfficial: true, OfficialRank: 99, LastSeenAt: now, ClosedAt: &closed})
	// 公开但非官方：不进官方列表（这是与公开列表的关键差别）。
	mustUpsertRoom(t, s, &RoomMeta{RoomID: "PUB00007", OwnerUserID: owner.ID, IsPublic: true, IsOfficial: false, LastSeenAt: now.Add(time.Hour)})

	ids, err := s.ListOfficialRoomIDs(ctx, 0)
	if err != nil {
		t.Fatalf("查官方房间列表失败：%v", err)
	}
	want := []string{"OFF00003", "OFF00001", "OFF00002"}
	if len(ids) != len(want) {
		t.Fatalf("官方列表应为 %v，实际 %v", want, ids)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("官方列表顺序不对（rank DESC + room_id ASC）：want=%v got=%v", want, ids)
		}
	}

	// rank 9 的房间被撤掉官方后从列表消失，rank 5 的两个补上前两位。
	if err := s.UpdateRoomMeta(ctx, "OFF00003", RoomMetaPatch{IsOfficial: new(false), OfficialRank: new(0)}); err != nil {
		t.Fatalf("撤销官方失败：%v", err)
	}
	after, err := s.ListOfficialRoomIDs(ctx, 0)
	if err != nil {
		t.Fatalf("查官方房间列表失败：%v", err)
	}
	if len(after) != 2 || after[0] != "OFF00001" || after[1] != "OFF00002" {
		t.Fatalf("撤销后官方列表应只剩两个 rank=5 的房间，实际 %v", after)
	}

	// 空库返回空切片而不是 nil。
	truncateAll(t, s)
	empty, err := s.ListOfficialRoomIDs(ctx, 10)
	if err != nil {
		t.Fatalf("空库查询失败：%v", err)
	}
	if len(empty) != 0 || empty == nil {
		t.Fatalf("空库应返回空切片，实际 %v", empty)
	}
}

// TestUpdateRoomMetaOfficialPatch 覆盖运营两个字段的 patch 语义（0014）：
// nil = 不改、显式 false/0 = 改、负数 rank 被应用层挡住、缺行返回 ErrNotFound。
func TestUpdateRoomMetaOfficialPatch(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	owner := seedUser(t, s, "host")
	mustUpsertRoom(t, s, &RoomMeta{RoomID: "OFF00100", OwnerUserID: owner.ID, IsPublic: true, Title: "放映厅"})

	// 只设 rank 不动 is_official：常用来给一个还没公开的房间预排位。
	if err := s.UpdateRoomMeta(ctx, "OFF00100", RoomMetaPatch{OfficialRank: new(7)}); err != nil {
		t.Fatalf("只改 rank 失败：%v", err)
	}
	got, err := s.RoomMetaByID(ctx, "OFF00100")
	if err != nil {
		t.Fatalf("查房间失败：%v", err)
	}
	if got.IsOfficial {
		t.Fatalf("只改 rank 不该动 is_official：%+v", got)
	}
	if got.OfficialRank != 7 {
		t.Fatalf("rank 应改成 7，实际 %d", got.OfficialRank)
	}
	if !got.IsPublic || got.Title != "放映厅" {
		t.Fatalf("patch 不该动未指定的字段：%+v", got)
	}

	// 设官方 + 置顶。
	if err := s.UpdateRoomMeta(ctx, "OFF00100", RoomMetaPatch{IsOfficial: new(true), OfficialRank: new(3)}); err != nil {
		t.Fatalf("设官方失败：%v", err)
	}
	got, err = s.RoomMetaByID(ctx, "OFF00100")
	if err != nil {
		t.Fatalf("查房间失败：%v", err)
	}
	if !got.IsOfficial || got.OfficialRank != 3 {
		t.Fatalf("官方标记没写进去：%+v", got)
	}

	// 撤销官方时 rank 归零（RoomMetaPatch 的说明：留着老 rank 会让下次设官方时跳到老位置）。
	if err := s.UpdateRoomMeta(ctx, "OFF00100", RoomMetaPatch{IsOfficial: new(false), OfficialRank: new(0)}); err != nil {
		t.Fatalf("撤销官方失败：%v", err)
	}
	got, err = s.RoomMetaByID(ctx, "OFF00100")
	if err != nil {
		t.Fatalf("查房间失败：%v", err)
	}
	if got.IsOfficial || got.OfficialRank != 0 {
		t.Fatalf("撤销后应为 false/0，实际 %+v", got)
	}

	// 负数 rank 被应用层挡住（否则靠 CHECK 报错会把整次 patch 打回）。
	if err := s.UpdateRoomMeta(ctx, "OFF00100", RoomMetaPatch{OfficialRank: new(-1)}); err == nil {
		t.Fatalf("负 rank 应该报错")
	}
	// 目标不存在。
	if err := s.UpdateRoomMeta(ctx, "NOPE0009", RoomMetaPatch{OfficialRank: new(1)}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的房间应返回 ErrNotFound，实际 %v", err)
	}
}

// TestQueryRoomMetasOfficialOnlyFilter 覆盖管理端"只看官方"筛选（0014）：
// OfficialOnly 与公开性筛选是三个并列维度，官方房可以是未公开的。
func TestQueryRoomMetasOfficialOnlyFilter(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	owner := seedUser(t, s, "host")
	now := time.Now()

	mustUpsertRoom(t, s, &RoomMeta{RoomID: "OFF00200", OwnerUserID: owner.ID, IsPublic: false, IsOfficial: true, OfficialRank: 2, LastSeenAt: now})
	mustUpsertRoom(t, s, &RoomMeta{RoomID: "OFF00201", OwnerUserID: owner.ID, IsPublic: true, IsOfficial: true, OfficialRank: 1, LastSeenAt: now.Add(time.Hour)})
	mustUpsertRoom(t, s, &RoomMeta{RoomID: "PUB00202", OwnerUserID: owner.ID, IsPublic: true, IsOfficial: false, LastSeenAt: now})

	rows, total, err := s.QueryRoomMetas(ctx, RoomMetaQuery{OfficialOnly: true})
	if err != nil {
		t.Fatalf("按官方筛选失败：%v", err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("官方筛选应返回 2 行（含未公开的那个），实际 total=%d rows=%+v", total, rows)
	}
	for _, r := range rows {
		if !r.IsOfficial {
			t.Fatalf("官方筛选不该返回非官方行：%+v", r)
		}
	}

	// 官方 + 公开两个筛选同时生效（交集）。
	publicAndOfficial, _, err := s.QueryRoomMetas(ctx, RoomMetaQuery{OfficialOnly: true, PublicOnly: true})
	if err != nil {
		t.Fatalf("官方+公开筛选失败：%v", err)
	}
	if len(publicAndOfficial) != 1 || publicAndOfficial[0].RoomID != "OFF00201" {
		t.Fatalf("官方+公开应只剩 OFF00201，实际 %+v", publicAndOfficial)
	}
}

// TestUpsertRoomMetaKeepsOfficialColumns 覆盖 0014 的一条**静默失败**风险：
// UpsertRoomMeta 的 ON CONFLICT DO UPDATE 列清单是手工维护的，漏掉
// is_official / official_rank 时，同房间码的第二次登记会把它们写不回库——
// 于是"运营在管理端设了官方"在自愈投递（或任何一次建房重登记）之后
// 静默消失：不报错、不影响其它字段，症状只是"官方区某天突然空了"。
//
// 为什么值得单独一条而不是合进 TestUpsertRoomMetaInsertThenOverwrite：
// 那条验的是"created_at 不被覆盖"（一条**不**动的列），这条验的是
// "运营两列必须能被 upsert 写进去"（两条**要**动的列）。两个方向都会因
// 清单写错而失败，而失败症状完全不同（一个是"创建时间被刷新"，
// 一个是"官方标记莫名消失"）。
//
// 口径说明：UpsertRoomMeta 的语义是**整体登记**（调用方掌握完整期望状态），
// 因此它如实写入调用方传入的零值 —— "读行 → 改一个字段 → 整行写回"
// 这个纪律由调用方负责（见 roommeta.go 的 patch 与 admin.go 的 setOfficial）。
// 本用例验的是：调用方把 true/7 传进来时，upsert 必须把它们落库。
func TestUpsertRoomMetaKeepsOfficialColumns(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	owner := seedUser(t, s, "host")

	// 第一次登记一个**非**官方房（零值字段）。
	mustUpsertRoom(t, s, &RoomMeta{
		RoomID: "OFF00300", OwnerUserID: owner.ID,
		Title: "放映厅", IsPublic: true, HasPassword: true,
	})

	// 同房间码再来一次，这次带官方字段：模拟"运营在管理端设了官方"
	// 之后又发生一次建房重登记/自愈 upsert（调用方已先读了行）。
	mustUpsertRoom(t, s, &RoomMeta{
		RoomID: "OFF00300", OwnerUserID: owner.ID,
		Title: "放映厅", IsPublic: true, HasPassword: true,
		IsOfficial: true, OfficialRank: 7,
	})
	got, err := s.RoomMetaByID(ctx, "OFF00300")
	if err != nil {
		t.Fatalf("查房间失败：%v", err)
	}
	if !got.IsOfficial || got.OfficialRank != 7 {
		t.Fatalf("UpsertRoomMeta 必须把 is_official/official_rank 写进 ON CONFLICT 的赋值清单：%+v", got)
	}
	if got.Title != "放映厅" || !got.IsPublic || !got.HasPassword {
		t.Fatalf("其它字段不该被这次 upsert 改变：%+v", got)
	}

	// 反向：把 rank 改成新值也要照写（不是只读不写）。
	mustUpsertRoom(t, s, &RoomMeta{
		RoomID: "OFF00300", OwnerUserID: owner.ID,
		Title: "放映厅", IsPublic: true, HasPassword: true,
		IsOfficial: true, OfficialRank: 42,
	})
	got, err = s.RoomMetaByID(ctx, "OFF00300")
	if err != nil {
		t.Fatalf("查房间失败：%v", err)
	}
	if got.OfficialRank != 42 {
		t.Fatalf("显式修改 rank 后 upsert 应照写，实际 %+v", got)
	}
}

// TestUpsertRoomMetaSurvivesRoomMetaPatch 覆盖"读-改-写回"这条调用纪律本身：
// admin 的 setOfficial 与 roommeta 的 patch 都是"先读行、只改目标字段、
// 再把整行 upsert 回去"。这条用例模拟那条路径，确保官方标记在一次
// 不相干的标题修改之后**还在**（这正是真机验收抓到的那条缺陷）。
func TestUpsertRoomMetaSurvivesRoomMetaPatch(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	owner := seedUser(t, s, "host")

	mustUpsertRoom(t, s, &RoomMeta{
		RoomID: "OFF00301", OwnerUserID: owner.ID,
		Title: "旧标题", IsPublic: true,
		IsOfficial: true, OfficialRank: 5,
	})

	// 只改标题的 patch（官方两列不动）。
	newTitle := "新标题"
	if err := s.UpdateRoomMeta(ctx, "OFF00301", RoomMetaPatch{Title: &newTitle}); err != nil {
		t.Fatalf("局部更新失败：%v", err)
	}
	got, err := s.RoomMetaByID(ctx, "OFF00301")
	if err != nil {
		t.Fatalf("查房间失败：%v", err)
	}
	if got.Title != newTitle {
		t.Fatalf("标题没改成功：%q", got.Title)
	}
	if !got.IsOfficial || got.OfficialRank != 5 {
		t.Fatalf("只改标题的 patch 不该动官方标记（真机曾因 upsert 漏列在这里被抹掉）：%+v", got)
	}

	// 再将整行 upsert 回去（读-改-写回路径的第二步）。
	if err := s.UpsertRoomMeta(ctx, got); err != nil {
		t.Fatalf("再次登记失败：%v", err)
	}
	got, err = s.RoomMetaByID(ctx, "OFF00301")
	if err != nil {
		t.Fatalf("查房间失败：%v", err)
	}
	if !got.IsOfficial || got.OfficialRank != 5 {
		t.Fatalf("读-改-写回之后官方标记必须还在：%+v", got)
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
