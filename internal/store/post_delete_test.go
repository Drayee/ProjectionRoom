package store

import (
	"context"
	"errors"
	"testing"
)

// deletePostEnv 造两个用户 + 一篇 alice 的帖子，返回 store 与双方 id。
func deletePostEnv(t *testing.T) (*Store, *User, *User, int64) {
	t.Helper()
	s := requireTestDB(t)
	ctx := context.Background()

	alice := seedUser(t, s, "del-alice")
	bob := seedUser(t, s, "del-bob")

	p := &Post{AuthorID: alice.ID, Title: "要被删的帖", Content: "正文", Images: []string{}}
	if err := s.CreatePost(ctx, p); err != nil {
		t.Fatalf("CreatePost: %v", err)
	}
	return s, alice, bob, p.ID
}

// TestDeletePostOnlyAuthor 钉住「删帖只有作者能删」——这是 I3（授权 100% 在服务端）
// 在这条路径上的落点。前端隐藏按钮不算数：这里直接调 store。
func TestDeletePostOnlyAuthor(t *testing.T) {
	s, alice, bob, postID := deletePostEnv(t)
	ctx := context.Background()

	if err := s.DeletePost(ctx, postID, bob.ID); !errors.Is(err, ErrPostForbidden) {
		t.Fatalf("别人删帖应当 ErrPostForbidden，实得 %v", err)
	}
	// 别人的删除尝试不能产生任何副作用
	if _, err := s.PostByID(ctx, postID, 0); err != nil {
		t.Fatalf("被拒绝的删除不该动数据：%v", err)
	}

	if err := s.DeletePost(ctx, postID, alice.ID); err != nil {
		t.Fatalf("作者删自己的帖应当成功：%v", err)
	}
	if _, err := s.PostByID(ctx, postID, 0); !errors.Is(err, ErrPostNotFound) {
		t.Fatalf("删掉之后应当 ErrPostNotFound，实得 %v", err)
	}
}

// TestDeletePostMissingIsNotFound 钉住 404 与 403 的区分：
// 不存在的帖子对任何人是 404（不泄漏"这个 id 曾经存在"）。
func TestDeletePostMissingIsNotFound(t *testing.T) {
	s, alice, _, _ := deletePostEnv(t)
	ctx := context.Background()

	if err := s.DeletePost(ctx, 999999999, alice.ID); !errors.Is(err, ErrPostNotFound) {
		t.Fatalf("不存在的帖子应当 ErrPostNotFound，实得 %v", err)
	}
	if err := s.DeletePost(ctx, 0, alice.ID); !errors.Is(err, ErrPostNotFound) {
		t.Fatalf("id=0 应当 ErrPostNotFound，实得 %v", err)
	}
}

// TestDeletePostCascadesCommentsAndLikes 钉住子行清理。
//
// 评论与点赞靠数据库的 ON DELETE CASCADE（迁移 0010 的外键），**不需要**
// store 手写 DELETE —— 手写反而会漏掉将来新增的子表。这里验的是"删完之后
// 子表真的空了"，而不是"我们有没有写那条 DELETE"。
func TestDeletePostCascadesCommentsAndLikes(t *testing.T) {
	s, alice, bob, postID := deletePostEnv(t)
	ctx := context.Background()

	if _, err := s.TogglePostLike(ctx, postID, bob.ID); err != nil {
		t.Fatalf("TogglePostLike: %v", err)
	}
	if err := s.AddPostComment(ctx, &PostComment{PostID: postID, AuthorID: bob.ID, Content: "留个言"}); err != nil {
		t.Fatalf("AddPostComment: %v", err)
	}

	if err := s.DeletePost(ctx, postID, alice.ID); err != nil {
		t.Fatalf("DeletePost: %v", err)
	}

	var likes, comments int64
	if err := s.db.WithContext(ctx).Table("post_likes").Where("post_id = ?", postID).Count(&likes).Error; err != nil {
		t.Fatalf("统计点赞失败: %v", err)
	}
	if err := s.db.WithContext(ctx).Table("post_comments").Where("post_id = ?", postID).Count(&comments).Error; err != nil {
		t.Fatalf("统计评论失败: %v", err)
	}
	if likes != 0 || comments != 0 {
		t.Fatalf("删帖应当连带清掉子行，实得 likes=%d comments=%d", likes, comments)
	}
}

// TestDeletePostKeepsFavoritesEntry 钉住一条**刻意的例外**：
// 收藏行**不**随帖子删除而消失。
//
// 这与工单 #70 的验收里那句「FK CASCADE 连带 likes/comments/favorites」不一致，
// 但那条与决议 #48 §5 直接冲突，而决议是判据的真源：
//
//	对象失效（放映室关闭/未公开、番剧下架、**帖子被删**）→ 条目保留、
//	列表显示失效态，用户自行移除。
//
// 收藏表在迁移 0016 里**故意没有外键**，正是为了让这条语义成立：外键的
// ON DELETE CASCADE 会替用户悄悄删掉他的书签，而"我收藏过的东西后来没了"
// 本身是用户需要知道的信息。票 #65 已经按这个口径实现并验收（G5a/G5b），
// 这里把它钉在 store 层，防止以后有人"顺手补个外键"。
func TestDeletePostKeepsFavoritesEntry(t *testing.T) {
	s, alice, bob, postID := deletePostEnv(t)
	ctx := context.Background()

	target := itoaTest(postID)
	if _, err := s.AddFavorite(ctx, bob.ID, FavoritePost, target); err != nil {
		t.Fatalf("AddFavorite: %v", err)
	}

	if err := s.DeletePost(ctx, postID, alice.ID); err != nil {
		t.Fatalf("DeletePost: %v", err)
	}

	entries, _, err := s.ListFavorites(ctx, bob.ID, FavoritePost, "", 10)
	if err != nil {
		t.Fatalf("ListFavorites: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("收藏条目应当保留（失效态），实得 %d 条", len(entries))
	}
	if entries[0].Valid {
		t.Fatalf("帖子已删，收藏条目应当是失效态")
	}
}

// itoaTest 把 id 转成收藏表用的文本主键（本地小工具，避免引 strconv 只为一个调用）。
func itoaTest(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
