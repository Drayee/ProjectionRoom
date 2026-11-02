package store

import (
	"context"
	"errors"
	"strconv"
	"testing"
)

// newFavoriteFixture 造两个用户 + 一篇帖子 + 一个番剧 + 一个放映室。
//
// 为什么一次造齐三种对象：收藏是**多态**的，只测帖子会让「target_type 分支写错」
// 这类缺陷漏网 —— 三个分支共用一张表，错的那个恰好是最少被点到的那一个。
func newFavoriteFixture(t *testing.T) (s *Store, me, other *User, postID, animeID int64, roomID string) {
	t.Helper()
	s = requireTestDB(t)
	ctx := context.Background()

	me = &User{Username: "favme", DisplayName: "收藏者", PasswordHash: "$2a$10$xx"}
	if err := s.CreateUser(ctx, me); err != nil {
		t.Fatalf("CreateUser(me): %v", err)
	}
	other = &User{Username: "favother", DisplayName: "旁观者", PasswordHash: "$2a$10$xx"}
	if err := s.CreateUser(ctx, other); err != nil {
		t.Fatalf("CreateUser(other): %v", err)
	}

	p := &Post{AuthorID: me.ID, Title: "可收藏的帖子", Content: "正文", Images: []string{}}
	if err := s.CreatePost(ctx, p); err != nil {
		t.Fatalf("CreatePost: %v", err)
	}
	postID = p.ID

	a := &Anime{Title: "可追的番", Aliases: []string{}, Summary: ""}
	if err := s.CreateAnime(ctx, a); err != nil {
		t.Fatalf("CreateAnime: %v", err)
	}
	animeID = a.ID

	roomID = "FAVROOM1"
	if err := s.UpsertRoomMeta(ctx, &RoomMeta{
		RoomID: roomID, OwnerUserID: me.ID, Title: "可收藏的放映室", IsPublic: true,
	}); err != nil {
		t.Fatalf("UpsertRoomMeta: %v", err)
	}
	return s, me, other, postID, animeID, roomID
}

// TestAddFavoriteIsIdempotent 钉住「同一个对象收藏两次只有一行」。
//
// 幂等不是"报个错"，而是**返回 created=false 且状态不变**：前端的乐观更新会在
// 网络重试时再发一次，把它变成 409 只会让用户看到一次莫名其妙的失败提示。
func TestAddFavoriteIsIdempotent(t *testing.T) {
	s, me, _, postID, _, _ := newFavoriteFixture(t)
	ctx := context.Background()

	created, err := s.AddFavorite(ctx, me.ID, FavoritePost, strconv.FormatInt(postID, 10))
	if err != nil || !created {
		t.Fatalf("首次收藏应 created=true: created=%v err=%v", created, err)
	}
	created, err = s.AddFavorite(ctx, me.ID, FavoritePost, strconv.FormatInt(postID, 10))
	if err != nil || created {
		t.Fatalf("重复收藏应 created=false 且无错: created=%v err=%v", created, err)
	}

	var cnt int64
	if err := s.db.WithContext(ctx).Table("favorites").
		Where("user_id = ? AND target_type = ? AND target_id = ?", me.ID, string(FavoritePost), strconv.FormatInt(postID, 10)).
		Count(&cnt).Error; err != nil {
		t.Fatalf("统计收藏行: %v", err)
	}
	if cnt != 1 {
		t.Fatalf("重复收藏后应只有 1 行，实际 %d 行", cnt)
	}
}

// TestAddFavoriteCoversAllThreeTypes 逐个类型验一遍，防止 target_type 分支写错。
//
// 放映室的主键是**字符串**房间码，帖子和番剧是自增 id —— 三种类型共用一个
// target_id 列，所以这一列必须是文本。这条用例就是那个决定的可执行形态。
func TestAddFavoriteCoversAllThreeTypes(t *testing.T) {
	s, me, _, postID, animeID, roomID := newFavoriteFixture(t)
	ctx := context.Background()

	for _, tc := range []struct {
		typ FavoriteType
		id  string
	}{
		{FavoritePost, strconv.FormatInt(postID, 10)},
		{FavoriteAnime, strconv.FormatInt(animeID, 10)},
		{FavoriteRoom, roomID},
	} {
		created, err := s.AddFavorite(ctx, me.ID, tc.typ, tc.id)
		if err != nil || !created {
			t.Fatalf("收藏 %s/%s 失败: created=%v err=%v", tc.typ, tc.id, created, err)
		}
	}
}

// TestAddFavoriteRejectsMissingTarget 钉住「不写孤儿行」：对象不存在就不落行。
//
// 没有外键就没人替我们挡这件事（见 favorites.go 的取舍说明），所以它是
// 应用层的硬约束，必须有可执行断言。
func TestAddFavoriteRejectsMissingTarget(t *testing.T) {
	s, me, _, _, _, _ := newFavoriteFixture(t)
	ctx := context.Background()

	cases := []struct {
		typ FavoriteType
		id  string
	}{
		{FavoritePost, "99999999"},
		{FavoriteAnime, "99999999"},
		{FavoriteRoom, "NOSUCHROOM"},
		// 数字型目标的 id 不是数字：同样按"不存在"处理，不能让 strconv 的错误
		// 变成一个 500（那会把"客户端传错"报成"服务端坏了"）。
		{FavoritePost, "not-a-number"},
	}
	for _, tc := range cases {
		if _, err := s.AddFavorite(ctx, me.ID, tc.typ, tc.id); !errors.Is(err, ErrFavoriteTargetNotFound) {
			t.Fatalf("收藏不存在的 %s/%s 应 ErrFavoriteTargetNotFound，实际 %v", tc.typ, tc.id, err)
		}
	}

	var cnt int64
	if err := s.db.WithContext(ctx).Table("favorites").Where("user_id = ?", me.ID).Count(&cnt).Error; err != nil {
		t.Fatalf("统计收藏行: %v", err)
	}
	if cnt != 0 {
		t.Fatalf("对象不存在时不应落任何行，实际 %d 行", cnt)
	}
}

// TestAddFavoriteRejectsUnknownType 钉住类型白名单：不认识的 target_type 直接拒。
//
// 为什么值得测：target_type 上没有外键，写错一个字符串会安静地落一行
// 「谁也读不出来」的收藏 —— 而用户看到的是"收藏成功了"。
func TestAddFavoriteRejectsUnknownType(t *testing.T) {
	s, me, _, postID, _, _ := newFavoriteFixture(t)
	ctx := context.Background()

	if _, err := s.AddFavorite(ctx, me.ID, FavoriteType("bogus"), strconv.FormatInt(postID, 10)); !errors.Is(err, ErrFavoriteBadType) {
		t.Fatalf("未知 target_type 应 ErrFavoriteBadType，实际 %v", err)
	}
}

// TestRemoveFavoriteIsIdempotent 取消收藏同样幂等（重试不发第二次效果，也不报错）。
func TestRemoveFavoriteIsIdempotent(t *testing.T) {
	s, me, _, postID, _, _ := newFavoriteFixture(t)
	ctx := context.Background()

	id := strconv.FormatInt(postID, 10)
	if _, err := s.AddFavorite(ctx, me.ID, FavoritePost, id); err != nil {
		t.Fatalf("AddFavorite: %v", err)
	}
	removed, err := s.RemoveFavorite(ctx, me.ID, FavoritePost, id)
	if err != nil || !removed {
		t.Fatalf("首次取消应 removed=true: removed=%v err=%v", removed, err)
	}
	removed, err = s.RemoveFavorite(ctx, me.ID, FavoritePost, id)
	if err != nil || removed {
		t.Fatalf("重复取消应 removed=false 且无错: removed=%v err=%v", removed, err)
	}
}

// TestFavoriteStatesForIsBatch 钉住批量回填：一次查询回答"N 个对象里哪些已收藏"。
//
// 这条是「禁 N+1」的可执行形态 —— 接口只有"一次调用返回整张表"这一种形状，
// 调用方就没有写循环的机会。
func TestFavoriteStatesForIsBatch(t *testing.T) {
	s, me, _, postID, _, _ := newFavoriteFixture(t)
	ctx := context.Background()

	second := &Post{AuthorID: me.ID, Title: "第二篇", Content: "正文", Images: []string{}}
	if err := s.CreatePost(ctx, second); err != nil {
		t.Fatalf("CreatePost(second): %v", err)
	}
	first, secondID := strconv.FormatInt(postID, 10), strconv.FormatInt(second.ID, 10)
	if _, err := s.AddFavorite(ctx, me.ID, FavoritePost, first); err != nil {
		t.Fatalf("AddFavorite: %v", err)
	}

	states, err := s.FavoriteStatesFor(ctx, me.ID, FavoritePost, []string{first, secondID})
	if err != nil {
		t.Fatalf("FavoriteStatesFor: %v", err)
	}
	if !states[first] {
		t.Fatalf("已收藏的帖子应回 true，实际 %v", states)
	}
	if states[secondID] {
		t.Fatalf("未收藏的帖子应回 false，实际 %v", states)
	}

	// 空输入不查库、不报错（列表为空时调用方不该为此写 if）。
	empty, err := s.FavoriteStatesFor(ctx, me.ID, FavoritePost, nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("空输入应回空 map 且无错: %v err=%v", empty, err)
	}
}

// TestListFavoritesOrdersDescAndPages 钉住「收藏时间倒序 + 游标翻页」。
//
// 为什么用游标而不是 offset：收藏夹是"边翻边加"的列表，offset 在插入后
// 会让第二页漏掉或重复一条 —— 而收藏重复一条比漏掉更容易被发现（也更烦）。
func TestListFavoritesOrdersDescAndPages(t *testing.T) {
	s, me, _, _, _, _ := newFavoriteFixture(t)
	ctx := context.Background()

	ids := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		p := &Post{AuthorID: me.ID, Title: "帖子" + string(rune('A'+i)), Content: "正文", Images: []string{}}
		if err := s.CreatePost(ctx, p); err != nil {
			t.Fatalf("CreatePost: %v", err)
		}
		id := strconv.FormatInt(p.ID, 10)
		ids = append(ids, id)
		if _, err := s.AddFavorite(ctx, me.ID, FavoritePost, id); err != nil {
			t.Fatalf("AddFavorite: %v", err)
		}
	}

	first, hasMore, err := s.ListFavorites(ctx, me.ID, FavoritePost, "", 2)
	if err != nil {
		t.Fatalf("ListFavorites(第一页): %v", err)
	}
	if !hasMore || len(first) != 2 {
		t.Fatalf("第一页应 2 条且 hasMore=true，实际 len=%d hasMore=%v", len(first), hasMore)
	}
	// 倒序：最后收藏的那篇在最前。
	if first[0].TargetID != ids[2] || first[1].TargetID != ids[1] {
		t.Fatalf("应按收藏时间倒序，实际 %s,%s（期望 %s,%s）", first[0].TargetID, first[1].TargetID, ids[2], ids[1])
	}
	if !first[0].Valid || first[0].Title == "" {
		t.Fatalf("有效条目应带标题，实际 %+v", first[0])
	}

	second, hasMore, err := s.ListFavorites(ctx, me.ID, FavoritePost, first[len(first)-1].Cursor, 2)
	if err != nil {
		t.Fatalf("ListFavorites(第二页): %v", err)
	}
	if hasMore {
		t.Fatalf("第二页不该还有更多")
	}
	if len(second) != 1 || second[0].TargetID != ids[0] {
		t.Fatalf("第二页应只剩最早那条（%s），实际 %+v", ids[0], second)
	}
}

// TestListFavoritesKeepsInvalidTargets 钉住失效语义：对象没了，条目**保留**并标失效。
//
// 这是决议 #48 §5 的原文："收藏莫名变少比看到「已失效」更让人困惑"。
// 没有外键正是为了让这件事成立 —— 有 ON DELETE CASCADE 的话这条会静默消失。
func TestListFavoritesKeepsInvalidTargets(t *testing.T) {
	s, me, _, postID, _, _ := newFavoriteFixture(t)
	ctx := context.Background()

	id := strconv.FormatInt(postID, 10)
	if _, err := s.AddFavorite(ctx, me.ID, FavoritePost, id); err != nil {
		t.Fatalf("AddFavorite: %v", err)
	}
	// 硬删帖子（模拟"帖子被删"）。favorites 没有外键，行应留下。
	if err := s.db.WithContext(ctx).Exec(`DELETE FROM post_comments WHERE post_id = ?`, postID).Error; err != nil {
		t.Fatalf("清评论: %v", err)
	}
	if err := s.db.WithContext(ctx).Exec(`DELETE FROM posts WHERE id = ?`, postID).Error; err != nil {
		t.Fatalf("删帖子: %v", err)
	}

	rows, _, err := s.ListFavorites(ctx, me.ID, FavoritePost, "", 10)
	if err != nil {
		t.Fatalf("ListFavorites: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("失效条目应保留，实际 %d 条", len(rows))
	}
	if rows[0].Valid {
		t.Fatalf("对象已删，Valid 应为 false，实际 %+v", rows[0])
	}
}

// TestFavoritesArePrivate 钉住「仅自己可见」：A 的收藏不会出现在 B 的列表里。
func TestFavoritesArePrivate(t *testing.T) {
	s, me, other, postID, _, _ := newFavoriteFixture(t)
	ctx := context.Background()

	id := strconv.FormatInt(postID, 10)
	if _, err := s.AddFavorite(ctx, me.ID, FavoritePost, id); err != nil {
		t.Fatalf("AddFavorite: %v", err)
	}
	rows, _, err := s.ListFavorites(ctx, other.ID, FavoritePost, "", 10)
	if err != nil {
		t.Fatalf("ListFavorites(other): %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("他人不该看到我的收藏，实际 %d 条", len(rows))
	}
	states, err := s.FavoriteStatesFor(ctx, other.ID, FavoritePost, []string{id})
	if err != nil {
		t.Fatalf("FavoriteStatesFor(other): %v", err)
	}
	if states[id] {
		t.Fatalf("他人视角 favorited 必须为 false")
	}
}

// TestFavoriteBadCursorIsRejected 坏游标报错而不是静默从头开始。
//
// 静默回落会让"翻到第二页突然回到第一页"变成一个没人能复现的现象；
// 报错至少能在日志里看见。
func TestFavoriteBadCursorIsRejected(t *testing.T) {
	s, me, _, _, _, _ := newFavoriteFixture(t)
	ctx := context.Background()

	if _, _, err := s.ListFavorites(ctx, me.ID, FavoritePost, "not-a-cursor", 10); !errors.Is(err, ErrFavoriteBadCursor) {
		t.Fatalf("坏游标应 ErrFavoriteBadCursor，实际 %v", err)
	}
}
