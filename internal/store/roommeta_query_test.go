package store

import (
	"context"
	"testing"
	"time"
)

// 本文件是二期 T1 的**真库**验收面：ListRoomMetas（批量）与 QueryRoomMetas（管理端分页）。
//
// 为什么这两条必须在真库上验（而不是只靠 handler 侧的假表）：
//
//   - 「空 ids 返回空 map 不报错」与「IN 列表的参数绑定」是 SQL 层的行为，
//     假表验不到"GORM 会不会收到空 IN 而生成非法 SQL"；
//   - 分页的**稳定性**取决于 ORDER BY 的完整排序键（last_seen_at DESC, room_id ASC），
//     而"同一微秒的多行"只有真库上才会真的出现；
//   - title 的 ILIKE 通配符转义（% / _ / \ 按字面处理）走的是既有的 likePattern，
//     真库上才能确认 ESCAPE 生效。

func TestListRoomMetasBatch(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	owner := seedUser(t, s, "host")

	now := time.Now()
	mustUpsertRoom(t, s, &RoomMeta{RoomID: "BATCH001", OwnerUserID: owner.ID, Title: "客厅", IsPublic: true, LastSeenAt: now})
	mustUpsertRoom(t, s, &RoomMeta{RoomID: "BATCH002", OwnerUserID: owner.ID, Title: "书房", IsPublic: false, LastSeenAt: now})

	// 含一个不存在的房间码：它不该出现在结果里（调用方据此区分"缺行"）。
	got, err := s.ListRoomMetas(ctx, []string{"BATCH001", "BATCH002", "MISSING1", "BATCH001"})
	if err != nil {
		t.Fatalf("批量查询失败：%v", err)
	}
	if len(got) != 2 {
		t.Fatalf("应当只返回两行（重复的 id 去重、缺行的不补），实际 %d 行：%+v", len(got), got)
	}
	if got["BATCH001"].Title != "客厅" || !got["BATCH001"].IsPublic {
		t.Fatalf("BATCH001 的内容不对：%+v", got["BATCH001"])
	}
	if got["BATCH002"].Title != "书房" || got["BATCH002"].IsPublic {
		t.Fatalf("BATCH002 的内容不对：%+v", got["BATCH002"])
	}
	if _, exists := got["MISSING1"]; exists {
		t.Fatalf("缺行的房间码不该出现在 map 里：%+v", got)
	}

	// 空 ids：返回空 map 且**不报错**（列表端点经常会拿到空集合）。
	empty, err := s.ListRoomMetas(ctx, nil)
	if err != nil {
		t.Fatalf("空 ids 不该报错：%v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("空 ids 应返回空 map，实际 %#v", empty)
	}
	// 全是空白/空串的 ids 等价于空集合。
	if got2, err := s.ListRoomMetas(ctx, []string{"", "   "}); err != nil || len(got2) != 0 {
		t.Fatalf("全空 ids 应当等价于空集合，实际 err=%v len=%d", err, len(got2))
	}

	// 关掉的房间仍然能取到（批量取是"按 id 取"，不做公开性/关闭筛选）。
	closed := now.Add(-time.Minute)
	if err := s.UpdateRoomMeta(ctx, "BATCH002", RoomMetaPatch{ClosedAt: &closed}); err != nil {
		t.Fatalf("关闭房间失败：%v", err)
	}
	afterClose, err := s.ListRoomMetas(ctx, []string{"BATCH002"})
	if err != nil {
		t.Fatalf("批量查询失败：%v", err)
	}
	if _, ok := afterClose["BATCH002"]; !ok {
		t.Fatalf("已关闭的房间仍然应当能按 id 取到：%+v", afterClose)
	}
}

func TestQueryRoomMetasFiltersAndPaging(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	owner := seedUser(t, s, "host")
	base := time.Now().Add(-time.Hour)

	// 五行：两公开（标题含"客厅"）、两非公开、一个已关闭的公开房。
	mustUpsertRoom(t, s, &RoomMeta{RoomID: "QRY00001", OwnerUserID: owner.ID, Title: "客厅 A", IsPublic: true, LastSeenAt: base})
	mustUpsertRoom(t, s, &RoomMeta{RoomID: "QRY00002", OwnerUserID: owner.ID, Title: "客厅 B", IsPublic: true, LastSeenAt: base.Add(time.Minute)})
	mustUpsertRoom(t, s, &RoomMeta{RoomID: "QRY00003", OwnerUserID: owner.ID, Title: "书房", IsPublic: false, LastSeenAt: base.Add(2 * time.Minute)})
	mustUpsertRoom(t, s, &RoomMeta{RoomID: "QRY00004", OwnerUserID: owner.ID, Title: "卧室", IsPublic: false, LastSeenAt: base.Add(3 * time.Minute)})
	mustUpsertRoom(t, s, &RoomMeta{RoomID: "QRY00005", OwnerUserID: owner.ID, Title: "已关闭的客厅", IsPublic: true, LastSeenAt: base.Add(4 * time.Minute)})

	// 不过滤：五行，且 total 与行数一致。
	all, total, err := s.QueryRoomMetas(ctx, RoomMetaQuery{})
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	if total != 5 || len(all) != 5 {
		t.Fatalf("不过滤应当有 5 行，实际 total=%d len=%d", total, len(all))
	}
	// 排序：last_seen_at DESC。
	if all[0].RoomID != "QRY00005" || all[4].RoomID != "QRY00001" {
		t.Fatalf("应当按 last_seen_at DESC 排序，实际 %v", []string{all[0].RoomID, all[4].RoomID})
	}

	// 公开性筛选。
	publicOnly, total, err := s.QueryRoomMetas(ctx, RoomMetaQuery{PublicOnly: true})
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	if total != 3 || len(publicOnly) != 3 {
		t.Fatalf("PublicOnly 应当命中 3 行，实际 total=%d len=%d", total, len(publicOnly))
	}
	privateOnly, total, err := s.QueryRoomMetas(ctx, RoomMetaQuery{PrivateOnly: true})
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	if total != 2 || len(privateOnly) != 2 {
		t.Fatalf("PrivateOnly 应当命中 2 行，实际 total=%d len=%d", total, len(privateOnly))
	}

	// 两个互斥条件同时为 true → 空集（不是并集）。
	both, total, err := s.QueryRoomMetas(ctx, RoomMetaQuery{PublicOnly: true, PrivateOnly: true})
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	if total != 0 || len(both) != 0 {
		t.Fatalf("互斥条件同时成立应当返回空集，实际 total=%d len=%d", total, len(both))
	}

	// 搜索：匹配标题（子串）。
	matched, total, err := s.QueryRoomMetas(ctx, RoomMetaQuery{Search: "客厅"})
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	if total != 3 || len(matched) != 3 {
		t.Fatalf("search=客厅 应当命中 3 行，实际 total=%d len=%d", total, len(matched))
	}
	// 搜索：也匹配房间码。
	byCode, total, err := s.QueryRoomMetas(ctx, RoomMetaQuery{Search: "QRY00003"})
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	if total != 1 || len(byCode) != 1 || byCode[0].RoomID != "QRY00003" {
		t.Fatalf("search=房间码 应当命中 1 行，实际 total=%d %+v", total, byCode)
	}

	// 搜索里的通配符按**字面**处理（likePattern 会转义）：搜 "QRY_0001" 不应命中任何行
	//（下划线是 SQL 通配符，不转义的话它会匹配到 "QRY00001"）。
	wild, total, err := s.QueryRoomMetas(ctx, RoomMetaQuery{Search: "QRY_0001"})
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	if total != 0 || len(wild) != 0 {
		t.Fatalf("下划线应当按字面处理（不该命中），实际 total=%d %+v", total, wild)
	}

	// 分页：limit/offset 与 total 的口径（total 是过滤后的总数，与页大小无关）。
	page1, total, err := s.QueryRoomMetas(ctx, RoomMetaQuery{Limit: 2, Offset: 0})
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	if total != 5 || len(page1) != 2 {
		t.Fatalf("第一页应当 2 行 / total=5，实际 len=%d total=%d", len(page1), total)
	}
	page2, _, err := s.QueryRoomMetas(ctx, RoomMetaQuery{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	if len(page2) != 2 {
		t.Fatalf("第二页应当 2 行，实际 %d", len(page2))
	}
	// 两页之间**不得重叠**（这正是"补了 room_id 第二排序键"要保证的性质）。
	seen := map[string]bool{}
	for _, m := range page1 {
		seen[m.RoomID] = true
	}
	for _, m := range page2 {
		if seen[m.RoomID] {
			t.Fatalf("分页出现重复行：%s（第一页 %+v，第二页 %+v）", m.RoomID, page1, page2)
		}
	}
	// 越界 offset → 空切片（不是 nil、不报错）。
	out, total, err := s.QueryRoomMetas(ctx, RoomMetaQuery{Limit: 2, Offset: 99})
	if err != nil {
		t.Fatalf("越界 offset 不该报错：%v", err)
	}
	if total != 5 || len(out) != 0 || out == nil {
		t.Fatalf("越界 offset 应返回空切片 + 正确 total，实际 len=%d total=%d nil=%t", len(out), total, out == nil)
	}
	// 上限截断：limit=100000 被夹到 maxPageLimit（不报错）。
	if _, _, err := s.QueryRoomMetas(ctx, RoomMetaQuery{Limit: 100_000}); err != nil {
		t.Fatalf("超大 limit 不该报错：%v", err)
	}

	// 空库：total=0 且返回空切片。
	truncateAll(t, s)
	empty, total, err := s.QueryRoomMetas(ctx, RoomMetaQuery{})
	if err != nil {
		t.Fatalf("空库查询失败：%v", err)
	}
	if total != 0 || len(empty) != 0 || empty == nil {
		t.Fatalf("空库应返回空切片 + total=0，实际 len=%d total=%d nil=%t", len(empty), total, empty == nil)
	}
}

// TestQueryRoomMetasPagingIsStableOnIdenticalTimestamps 覆盖"补 room_id 第二排序键"的必要性：
// 同一批插入的行 last_seen_at 完全相同（同一个微秒），只按它排序时分页会抖动。
func TestQueryRoomMetasPagingIsStableOnIdenticalTimestamps(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	owner := seedUser(t, s, "host")

	same := time.Now()
	ids := []string{"STB00001", "STB00002", "STB00003", "STB00004"}
	for _, id := range ids {
		mustUpsertRoom(t, s, &RoomMeta{RoomID: id, OwnerUserID: owner.ID, LastSeenAt: same})
	}

	// 逐页取（每页 1 行），拼起来必须**恰好覆盖**全部四行、且无重复。
	seen := map[string]int{}
	for offset := 0; offset < len(ids); offset++ {
		page, _, err := s.QueryRoomMetas(ctx, RoomMetaQuery{Limit: 1, Offset: offset})
		if err != nil {
			t.Fatalf("查询失败：%v", err)
		}
		if len(page) != 1 {
			t.Fatalf("第 offset=%d 页应当 1 行，实际 %d", offset, len(page))
		}
		seen[page[0].RoomID]++
	}
	if len(seen) != len(ids) {
		t.Fatalf("逐页取应当覆盖全部 %d 行，实际覆盖 %d 行：%+v", len(ids), len(seen), seen)
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("房间 %s 在分页里出现了 %d 次（重复）", id, n)
		}
	}

	// 重复两次同样的分页查询，结果必须逐行一致（排序是全序）。
	first, _, err := s.QueryRoomMetas(ctx, RoomMetaQuery{})
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	second, _, err := s.QueryRoomMetas(ctx, RoomMetaQuery{})
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	for i := range first {
		if first[i].RoomID != second[i].RoomID {
			t.Fatalf("同一查询两次结果顺序不一致（第 %d 行：%s vs %s）", i, first[i].RoomID, second[i].RoomID)
		}
	}
}
