package store

import (
	"context"
	"errors"
	"testing"
)

// newPostFixture 造一个作者 + 一篇帖子，返回 store 与清理函数。
//
// 用户直接插 users（bcrypt 哈希在这里无所谓）：帖子域的用例测的是计数与归属，
// 不是注册链路 —— 那有自己的用例（otp_test.go / draft 测试）。
func newPostFixture(t *testing.T) (*Store, int64, int64) {
	t.Helper()
	s := requireTestDB(t)
	ctx := context.Background()

	author := &User{Username: "postauthor", DisplayName: "帖子作者", PasswordHash: "$2a$10$xx"}
	if err := s.CreateUser(ctx, author); err != nil {
		t.Fatalf("CreateUser(author): %v", err)
	}
	other := &User{Username: "postother", DisplayName: "别人", PasswordHash: "$2a$10$xx"}
	if err := s.CreateUser(ctx, other); err != nil {
		t.Fatalf("CreateUser(other): %v", err)
	}

	p := &Post{AuthorID: author.ID, Title: "计数不变量", Content: "正文", Images: []string{}}
	if err := s.CreatePost(ctx, p); err != nil {
		t.Fatalf("CreatePost: %v", err)
	}
	return s, p.ID, other.ID
}

// TestPostLikeTogglesCountAtomically 钉住反范式计数的核心不变量：
// like_count 永远等于 post_likes 的行数。
//
// 为什么测"来回"而不是单次：漂移恰恰发生在中间态 —— 计数更新与纪录增删
// 分成两个事务时，任何一次半途失败都会留下"行在、计数没动"或反之。
func TestPostLikeTogglesCountAtomically(t *testing.T) {
	s, postID, otherID := newPostFixture(t)
	ctx := context.Background()

	liked, err := s.TogglePostLike(ctx, postID, otherID)
	if err != nil || !liked {
		t.Fatalf("首次点赞应返回 true: liked=%v err=%v", liked, err)
	}
	assertLikeCountMatches(t, s, postID, 1)

	// 重复"点赞"（同一用户再 toggle）是取消：语义是 toggle，不是幂等 set。
	liked, err = s.TogglePostLike(ctx, postID, otherID)
	if err != nil || liked {
		t.Fatalf("再次 toggle 应取消点赞: liked=%v err=%v", liked, err)
	}
	assertLikeCountMatches(t, s, postID, 0)

	// 两个用户各赞一次：计数 2。
	if _, err := s.TogglePostLike(ctx, postID, otherID); err != nil {
		t.Fatalf("他人点赞: %v", err)
	}
	var authorID int64
	if err := s.db.Raw(`SELECT author_id FROM posts WHERE id = ?`, postID).Scan(&authorID).Error; err != nil {
		t.Fatalf("取作者 id: %v", err)
	}
	if _, err := s.TogglePostLike(ctx, postID, authorID); err != nil {
		t.Fatalf("作者自赞: %v", err)
	}
	assertLikeCountMatches(t, s, postID, 2)

	// viewer 的 liked 态：作者视角与匿名视角。
	row, err := s.PostByID(ctx, postID, authorID)
	if err != nil || !row.Liked {
		t.Fatalf("作者应显示已赞: %+v err=%v", row, err)
	}
	if row, err = s.PostByID(ctx, postID, 0); err != nil || row.Liked {
		t.Fatalf("匿名应显示未赞: liked=%v err=%v", row.Liked, err)
	}
}

func assertLikeCountMatches(t *testing.T, s *Store, postID int64, want int) {
	t.Helper()
	var cnt int
	if err := s.db.Raw(`SELECT count(*) FROM post_likes WHERE post_id = ?`, postID).Scan(&cnt).Error; err != nil {
		t.Fatalf("数点赞行: %v", err)
	}
	row, err := s.PostByID(context.Background(), postID, 0)
	if err != nil {
		t.Fatalf("PostByID: %v", err)
	}
	if row.LikeCount != cnt || cnt != int(want) {
		t.Fatalf("计数漂移: like_count=%d 行数=%d 期望=%d", row.LikeCount, cnt, want)
	}
}

// TestPostCommentLifecycle 覆盖评论的增/查/删与计数配对，以及"只能删自己的评论"。
func TestPostCommentLifecycle(t *testing.T) {
	s, postID, otherID := newPostFixture(t)
	ctx := context.Background()

	var authorID int64
	if err := s.db.Raw(`SELECT author_id FROM posts WHERE id = ?`, postID).Scan(&authorID).Error; err != nil {
		t.Fatalf("取作者 id: %v", err)
	}

	c1 := &PostComment{PostID: postID, AuthorID: authorID, Content: "第一条"}
	c2 := &PostComment{PostID: postID, AuthorID: otherID, Content: "第二条"}
	for _, c := range []*PostComment{c1, c2} {
		if err := s.AddPostComment(ctx, c); err != nil {
			t.Fatalf("AddPostComment: %v", err)
		}
	}

	row, err := s.PostByID(ctx, postID, 0)
	if err != nil || row.CommentCount != 2 {
		t.Fatalf("评论计数应为 2: count=%d err=%v", row.CommentCount, err)
	}

	rows, total, err := s.ListPostComments(ctx, postID, 10, 0)
	if err != nil || total != 2 || len(rows) != 2 {
		t.Fatalf("评论列表: total=%d len=%d err=%v", total, len(rows), err)
	}
	if rows[0].ID != c1.ID {
		t.Fatalf("评论应按时间升序（老→新）: first=%d want=%d", rows[0].ID, c1.ID)
	}
	if rows[0].AuthorName != "帖子作者" {
		t.Fatalf("评论应带出作者显示名: %q", rows[0].AuthorName)
	}

	// 删别人的评论 → ErrCommentForbidden，且**不能**产生任何写入。
	if err := s.DeletePostComment(ctx, c1.ID, otherID); !errors.Is(err, ErrCommentForbidden) {
		t.Fatalf("删他人评论期望 ErrCommentForbidden，得到 %v", err)
	}
	if row, _ = s.PostByID(ctx, postID, 0); row.CommentCount != 2 {
		t.Fatalf("被拒绝的删除不应动计数: %d", row.CommentCount)
	}

	// 作者删自己的 → 成功且计数 -1。
	if err := s.DeletePostComment(ctx, c1.ID, authorID); err != nil {
		t.Fatalf("删自己评论: %v", err)
	}
	if row, _ = s.PostByID(ctx, postID, 0); row.CommentCount != 1 {
		t.Fatalf("删除后计数应为 1: %d", row.CommentCount)
	}

	// 删不存在的 → ErrCommentNotFound。
	if err := s.DeletePostComment(ctx, 999999999, authorID); !errors.Is(err, ErrCommentNotFound) {
		t.Fatalf("期望 ErrCommentNotFound，得到 %v", err)
	}
}

// TestListPostsHasMoreAndLiked 验证列表的 hasMore 语义（多取一行）与批量 liked。
func TestListPostsHasMoreAndLiked(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()

	u := &User{Username: "listposter", DisplayName: "列表作者", PasswordHash: "$2a$10$xx"}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	ids := make([]int64, 0, 3)
	for i, title := range []string{"甲帖", "乙帖", "丙帖"} {
		p := &Post{AuthorID: u.ID, Title: title, Content: "正文" + string(rune('a'+i))}
		if err := s.CreatePost(ctx, p); err != nil {
			t.Fatalf("CreatePost(%s): %v", title, err)
		}
		ids = append(ids, p.ID)
	}
	if _, err := s.TogglePostLike(ctx, ids[1], u.ID); err != nil {
		t.Fatalf("点赞: %v", err)
	}

	// limit=2 时还有第 3 篇 → hasMore=true
	rows, hasMore, err := s.ListPosts(ctx, PostsSortLatest, 2, 0, u.ID)
	if err != nil || len(rows) != 2 || !hasMore {
		t.Fatalf("第一页: len=%d hasMore=%v err=%v", len(rows), hasMore, err)
	}
	// 第二页取完 → hasMore=false
	rows2, hasMore2, err := s.ListPosts(ctx, PostsSortLatest, 2, 2, u.ID)
	if err != nil || len(rows2) != 1 || hasMore2 {
		t.Fatalf("第二页: len=%d hasMore=%v err=%v", len(rows2), hasMore2, err)
	}

	// liked 集合：本页里恰好有被赞的那篇时为 true
	var likedCount int
	for i := range rows {
		if rows[i].Liked {
			likedCount++
			if rows[i].ID != ids[1] {
				t.Fatalf("liked 标错了行: %d", rows[i].ID)
			}
		}
	}
	for i := range rows2 {
		if rows2[i].Liked && rows2[i].ID == ids[1] {
			likedCount++
		}
	}
	if likedCount != 1 {
		t.Fatalf("两页里应恰好一处 liked=true（被赞的 %d）", ids[1])
	}
}
