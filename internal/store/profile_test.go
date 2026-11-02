package store

import (
	"context"
	"testing"
	"time"
)

// 他人公开档案的内容块（票 #68）的真库证据。
//
// 两个只读列表：某人的帖子、某人的公开放映室。它们与其它列表的**唯一**区别是
// "按作者筛"，但那条筛选的可见性口径必须与关注流（#66）逐字一致 ——
// 这正是 `applyAuthorVisibility` 存在的理由，下面的用例逐条确认它真的被挂上了。
//
// 为什么这些用例值得写：档案页是**免登录**的。如果可见性谓词漏在这里，
// 症状是"点开一个被封禁的人的档案，404 了，但 TA 的帖子还能从档案页读到" ——
// 一条列表 404、子资源 200 的组合，界面上没有任何报错。

// profileEnv 造一个作者 + 一个看客 + 三篇帖。
//
//	author —— 档案的主人（三篇帖，时间递增）
//	viewer —— 看客（用来验证拉黑谓词）
func profileEnv(t *testing.T) (s *Store, author, viewer *User, postIDs []int64) {
	t.Helper()
	s = requireTestDB(t)
	ctx := context.Background()

	author = seedUser(t, s, "pf-author")
	viewer = seedUser(t, s, "pf-viewer")

	// 三篇帖，created_at 手动错开（默认全在同一微秒内，排序会退化到 id）。
	base := time.Now().Add(-3 * time.Hour)
	for i := 0; i < 3; i++ {
		p := &Post{
			AuthorID: author.ID,
			Title:    "作者帖 " + string(rune('A'+i)),
			Content:  "正文",
			Images:   []string{},
		}
		if err := s.CreatePost(ctx, p); err != nil {
			t.Fatalf("CreatePost: %v", err)
		}
		stamp := base.Add(time.Duration(i) * time.Hour)
		if err := s.db.WithContext(ctx).Exec("UPDATE posts SET created_at = ? WHERE id = ?", stamp, p.ID).Error; err != nil {
			t.Fatalf("调整 created_at: %v", err)
		}
		postIDs = append(postIDs, p.ID)
	}
	return s, author, viewer, postIDs
}

func titlesOf(rows []Post) []string {
	out := make([]string, 0, len(rows))
	for i := range rows {
		out = append(out, rows[i].Title)
	}
	return out
}

// 倒序 + 只含该作者的帖。
func TestListPostsByAuthorIsDescAndScoped(t *testing.T) {
	s, author, _, postIDs := profileEnv(t)
	ctx := context.Background()

	// 另一个人的帖不该混进来。
	other := seedUser(t, s, "pf-other")
	if err := s.CreatePost(ctx, &Post{AuthorID: other.ID, Title: "别人的帖", Content: "x", Images: []string{}}); err != nil {
		t.Fatalf("CreatePost(other): %v", err)
	}

	rows, hasMore, err := s.ListPostsByAuthor(ctx, author.ID, 20, 0, 0)
	if err != nil {
		t.Fatalf("ListPostsByAuthor: %v", err)
	}
	if hasMore {
		t.Fatalf("三篇帖不该有下一页")
	}
	if len(rows) != 3 {
		t.Fatalf("应当恰好 3 篇，实得 %d：%v", len(rows), titlesOf(rows))
	}
	// 时间递增建的，倒序后应当是 C B A。
	want := []string{"作者帖 C", "作者帖 B", "作者帖 A"}
	got := titlesOf(rows)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 篇应当是 %q，实得 %q（全量 %v）", i, want[i], got[i], got)
		}
	}
	// 作者显示名要带回来（列表渲染需要）。
	if rows[0].AuthorName != author.DisplayName {
		t.Fatalf("AuthorName 应当是 %q，实得 %q", author.DisplayName, rows[0].AuthorName)
	}
	_ = postIDs
}

// 分页：limit+1 判 hasMore，不重不漏。
func TestListPostsByAuthorPages(t *testing.T) {
	s, author, _, _ := profileEnv(t)
	ctx := context.Background()

	seen := map[int64]bool{}
	offset := 0
	for page := 0; page < 5; page++ {
		rows, hasMore, err := s.ListPostsByAuthor(ctx, author.ID, 2, offset, 0)
		if err != nil {
			t.Fatalf("第 %d 页: %v", page, err)
		}
		for i := range rows {
			if seen[rows[i].ID] {
				t.Fatalf("第 %d 页出现重复行 %d", page, rows[i].ID)
			}
			seen[rows[i].ID] = true
		}
		offset += len(rows)
		if !hasMore {
			break
		}
	}
	if len(seen) != 3 {
		t.Fatalf("翻完应当见到 3 篇，实得 %d", len(seen))
	}
}

// 被拉黑：作者从我的世界里消失 —— 档案页的帖列表同样要被挡住。
//
// 这是"列表 404、子资源 200"那条泄漏的防线。
func TestListPostsByAuthorHidesBlockedAuthor(t *testing.T) {
	s, author, viewer, _ := profileEnv(t)
	ctx := context.Background()

	// 先确认可见。
	rows, _, err := s.ListPostsByAuthor(ctx, author.ID, 20, 0, viewer.ID)
	if err != nil {
		t.Fatalf("拉黑前: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("拉黑前应当 3 篇，实得 %d", len(rows))
	}

	// author 拉黑 viewer：双向都看不见（票 #42 §4）。
	if _, err := s.BlockUser(ctx, author.ID, viewer.ID); err != nil {
		t.Fatalf("BlockUser: %v", err)
	}

	rows, _, err = s.ListPostsByAuthor(ctx, author.ID, 20, 0, viewer.ID)
	if err != nil {
		t.Fatalf("拉黑后: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("拉黑后不该再看到该作者的帖，实得 %d 篇：%v", len(rows), titlesOf(rows))
	}

	// 匿名视角仍然看得见：拉黑是"我与他的关系"，不影响公众（票 #42 §4）。
	rows, _, err = s.ListPostsByAuthor(ctx, author.ID, 20, 0, 0)
	if err != nil {
		t.Fatalf("匿名: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("匿名视角应当仍看得见 3 篇（公开内容不设限），实得 %d", len(rows))
	}
}

// 被封禁的作者：一条都不给（连匿名也不给）—— 与公开档案 404 同一口径。
func TestListPostsByAuthorHidesBannedAuthor(t *testing.T) {
	s, author, _, _ := profileEnv(t)
	ctx := context.Background()

	if err := s.SetUserStatus(ctx, author.ID, StatusBanned, "刷屏"); err != nil {
		t.Fatalf("SetUserStatus: %v", err)
	}
	rows, _, err := s.ListPostsByAuthor(ctx, author.ID, 20, 0, 0)
	if err != nil {
		t.Fatalf("ListPostsByAuthor: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("被封禁的作者不该有可见的帖，实得 %d 篇", len(rows))
	}
}

// 某人的公开放映室：只列已公开且未关闭的。
func TestListPublicRoomsByOwner(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	owner := seedUser(t, s, "pf-owner")
	other := seedUser(t, s, "pf-owner2")

	mk := func(roomID string, ownerID int64, public bool, closed bool) {
		t.Helper()
		m := &RoomMeta{RoomID: roomID, OwnerUserID: ownerID, Title: "房间 " + roomID, IsPublic: public}
		if closed {
			now := time.Now()
			m.ClosedAt = &now
		}
		if err := s.UpsertRoomMeta(ctx, m); err != nil {
			t.Fatalf("UpsertRoomMeta(%s): %v", roomID, err)
		}
	}
	mk("PFOPEN01", owner.ID, true, false)
	mk("PFOPEN02", owner.ID, true, false)
	mk("PFPRIV01", owner.ID, false, false) // 未公开
	mk("PFCLOS01", owner.ID, true, true)   // 已关闭
	mk("PFOTHER1", other.ID, true, false)  // 别人的

	rows, hasMore, err := s.ListPublicRoomsByOwner(ctx, owner.ID, 20, 0)
	if err != nil {
		t.Fatalf("ListPublicRoomsByOwner: %v", err)
	}
	if hasMore {
		t.Fatalf("两间房不该有下一页")
	}
	if len(rows) != 2 {
		ids := make([]string, 0, len(rows))
		for i := range rows {
			ids = append(ids, rows[i].RoomID)
		}
		t.Fatalf("应当只列 2 间已公开未关闭的房，实得 %d：%v", len(rows), ids)
	}
	for i := range rows {
		if rows[i].OwnerUserID != owner.ID {
			t.Fatalf("不该出现别人的房间：%s", rows[i].RoomID)
		}
	}
}

// 空名单返回空切片而不是 nil（调用方直接 JSON 序列化，nil 会变成 null）。
func TestListPublicRoomsByOwnerEmptyIsSlice(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	owner := seedUser(t, s, "pf-norooms")

	rows, hasMore, err := s.ListPublicRoomsByOwner(ctx, owner.ID, 20, 0)
	if err != nil {
		t.Fatalf("ListPublicRoomsByOwner: %v", err)
	}
	if rows == nil {
		t.Fatalf("空名单必须是空切片，不能是 nil")
	}
	if len(rows) != 0 || hasMore {
		t.Fatalf("应当空且无下一页，实得 %d / %v", len(rows), hasMore)
	}
}
