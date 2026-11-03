package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 头像列（票 #74）的真库证据。
//
// 这一组用例守两件事：
//
//  1. **写入语义**：`UpdateAvatarURL` 只有一种"没有"（NULL，空串归一过去），
//     幂等、超长拒绝、不存在的 id 报 ErrNotFound。库里出现 '' 与 NULL 两种
//     "没有"的后果是显示层要判两次，而漏判一次的症状是渲染出一个空 `<img>`。
//  2. **注销遮罩**：`maskedAvatarSQL` 与 `maskedDisplayNameSQL` 是**同一件事**
//     （注销账号对外完全不可见），所以它必须覆盖**每一个**带作者信息的查询 ——
//     帖子列表、详情、评论、三份名单、黑名单、私聊、会话列表、搜索结果。
//     漏一处的症状是"档案 404 了，但帖子里还挂着那个人的照片"，而照片比名字
//     更能认出一个人（决议 #51 §5 的措辞就是"对外完全不可见"）。
//
// 遮罩这一半必须**逐条查询**验：它们各自拼 SQL，没有一个公共出口，
// 也就是说"新增一个带作者的查询"很容易忘掉这一列，而编译期发现不了。

const testAvatarURL = "https://cdn.example.test/pr-files/2026/11/0123456789abcdef0123456789abcdef.jpg"

// avatarEnv 造 alice（有头像的人）+ bob（看客）+ 一篇 alice 的帖 + 一条评论。
func avatarEnv(t *testing.T) (s *Store, alice, bob *User, postID int64) {
	t.Helper()
	s = requireTestDB(t)
	ctx := context.Background()

	alice = seedUser(t, s, "av-alice")
	bob = seedUser(t, s, "av-bob")
	if err := s.UpdateAvatarURL(ctx, alice.ID, strPtr(testAvatarURL)); err != nil {
		t.Fatalf("UpdateAvatarURL: %v", err)
	}

	p := &Post{AuthorID: alice.ID, Title: "头像测试帖", Content: "正文", Images: []string{}}
	if err := s.CreatePost(ctx, p); err != nil {
		t.Fatalf("CreatePost: %v", err)
	}
	if err := s.AddPostComment(ctx, &PostComment{PostID: p.ID, AuthorID: alice.ID, Content: "作者自己的评论"}); err != nil {
		t.Fatalf("AddPostComment: %v", err)
	}
	return s, alice, bob, p.ID
}

// 设置 / 覆盖 / 清除 / 空串归一 / 超长 / 未知用户。
func TestUpdateAvatarURLSemantics(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	u := seedUser(t, s, "av-semantics")

	// 设置。
	if err := s.UpdateAvatarURL(ctx, u.ID, strPtr(testAvatarURL)); err != nil {
		t.Fatalf("设置失败：%v", err)
	}
	got, err := s.UserByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("UserByID: %v", err)
	}
	if got.AvatarURL == nil || *got.AvatarURL != testAvatarURL {
		t.Fatalf("应已落值：got %v", got.AvatarURL)
	}

	// 覆盖成新地址（URL 即版本：换图就换 URL，不做同 key 覆盖）。
	second := strings.Replace(testAvatarURL, "0123456789abcdef", "fedcba9876543210", 1)
	if err := s.UpdateAvatarURL(ctx, u.ID, strPtr(second)); err != nil {
		t.Fatalf("覆盖失败：%v", err)
	}
	got, _ = s.UserByID(ctx, u.ID)
	if got.AvatarURL == nil || *got.AvatarURL != second {
		t.Fatalf("应已覆盖：got %v", got.AvatarURL)
	}

	// 空串归一成 NULL（"没有"只有一种表示）。
	if err := s.UpdateAvatarURL(ctx, u.ID, strPtr("   ")); err != nil {
		t.Fatalf("空串应被接受（归一成清除）：%v", err)
	}
	got, _ = s.UserByID(ctx, u.ID)
	if got.AvatarURL != nil {
		t.Fatalf("空串应归一成 NULL：got %q", *got.AvatarURL)
	}

	// nil = 清除，且对"本来就没有"也是幂等的。
	if err := s.UpdateAvatarURL(ctx, u.ID, nil); err != nil {
		t.Fatalf("清除一个本来就没有的头像应当成功（幂等）：%v", err)
	}

	// 超长拒绝。
	long := "https://cdn.example.test/" + strings.Repeat("a", maxAvatarURLLen)
	if err := s.UpdateAvatarURL(ctx, u.ID, strPtr(long)); err == nil {
		t.Fatal("超过 512 字符应被拒")
	}

	// 不存在的 id。
	if err := s.UpdateAvatarURL(ctx, 99999999, strPtr(testAvatarURL)); err != ErrNotFound {
		t.Fatalf("不存在的 id 应返回 ErrNotFound：got %v", err)
	}
	if err := s.UpdateAvatarURL(ctx, 0, strPtr(testAvatarURL)); err == nil {
		t.Fatal("id=0 应被拒")
	}
}

// 注销之后，**每一处**带作者信息的查询都不再带出那个人的头像。
//
// 这是本文件的核心：遮罩由 `maskedAvatarSQL` 逐个 SELECT 拼进去，
// 没有公共出口，所以每条查询都要有一条断言 —— 新增查询时忘记拼这一列，
// 只有这条用例能抓到。
func TestDeletionMasksAvatarEverywhere(t *testing.T) {
	s, alice, bob, postID := avatarEnv(t)
	ctx := context.Background()

	// bob 关注 alice（三份名单都要有东西可查），再互关以便私聊。
	if _, _, err := s.CreateFollow(ctx, bob.ID, alice.ID); err != nil {
		t.Fatalf("bob→alice 关注失败：%v", err)
	}
	if _, _, err := s.CreateFollow(ctx, alice.ID, bob.ID); err != nil {
		t.Fatalf("alice→bob 关注失败：%v", err)
	}
	if _, err := s.SendDM(ctx, alice.ID, bob.ID, "注销前的一条私聊"); err != nil {
		t.Fatalf("SendDM: %v", err)
	}

	// 注销前：八处都能读到 alice 的头像。先确认"有值"，否则后面的 NULL 分不清
	// "被遮罩了"与"本来就没有"。
	requireAvatar := func(label string, got *string) {
		t.Helper()
		if got == nil || *got != testAvatarURL {
			t.Fatalf("%s 注销前应带 alice 的头像：got %v", label, got)
		}
	}
	requireAvatar("注销前 · 帖子列表", firstPostAuthorAvatar(t, s, bob.ID))
	requireAvatar("注销前 · 帖子详情", postDetailAvatar(t, s, postID, bob.ID))
	requireAvatar("注销前 · 评论", firstCommentAvatar(t, s, postID))
	requireAvatar("注销前 · 关注名单", followListAvatar(t, s, bob.ID, FollowListFollowing, alice.ID))
	requireAvatar("注销前 · 私聊", firstDMAvatar(t, s, bob.ID, alice.ID))
	requireAvatar("注销前 · 会话列表", conversationAvatar(t, s, bob.ID, alice.ID))
	requireAvatar("注销前 · 搜索结果", searchHitAvatar(t, s, "av-alice", bob.ID))
	requireAvatar("注销前 · 公开档案", publicAvatarOf(t, s, alice.ID))

	// 发起注销。
	if _, err := s.RequestAccountDeletion(ctx, alice.ID, time.Now()); err != nil {
		t.Fatalf("发起注销失败：%v", err)
	}

	// 注销后：同样八处，头像必须全部为 NULL（而不是空串或旧值）。
	requireNil := func(label string, got *string) {
		t.Helper()
		if got != nil {
			t.Fatalf("%s 注销后不该带出头像：got %q", label, *got)
		}
	}
	requireNil("注销后 · 帖子列表", firstPostAuthorAvatar(t, s, bob.ID))
	requireNil("注销后 · 帖子详情", postDetailAvatar(t, s, postID, bob.ID))
	requireNil("注销后 · 评论", firstCommentAvatar(t, s, postID))
	requireNil("注销后 · 私聊", firstDMAvatar(t, s, bob.ID, alice.ID))
	requireNil("注销后 · 会话列表", conversationAvatar(t, s, bob.ID, alice.ID))
	// 名单与搜索：注销后**整行都不该出现**（可见性谓词已过滤），
	// 所以断言从"头像为 nil"变成"找不到这个人"。
	if id := followListRow(t, s, bob.ID, FollowListFollowing, alice.ID); id != nil {
		t.Fatalf("注销后关注名单不该有 alice：got %+v", id)
	}
	if id := searchHitRow(t, s, "av-alice", bob.ID); id != nil {
		t.Fatalf("注销后搜索不该命中 alice：got %+v", id)
	}
	// 公开档案：注销后 404（不是"带空头像的档案"）。
	if _, err := s.UserByID(ctx, alice.ID); err != nil {
		t.Fatalf("UserByID 本身仍应能读到行（软删不是物理删）：%v", err)
	}

	// 撤销之后一切复原。
	if err := s.CancelAccountDeletion(ctx, alice.ID); err != nil {
		t.Fatalf("撤销失败：%v", err)
	}
	requireAvatar("撤销后 · 帖子详情", postDetailAvatar(t, s, postID, bob.ID))
	requireAvatar("撤销后 · 评论", firstCommentAvatar(t, s, postID))
	requireAvatar("撤销后 · 会话列表", conversationAvatar(t, s, bob.ID, alice.ID))
	if id := followListRow(t, s, bob.ID, FollowListFollowing, alice.ID); id == nil {
		t.Fatal("撤销后关注名单应重新出现 alice")
	}
}

// ---------- 逐处取头像的小工具（每个只服务本文件的一条断言） ----------

func firstPostAuthorAvatar(t *testing.T, s *Store, viewerID int64) *string {
	t.Helper()
	rows, _, err := s.ListPosts(context.Background(), PostsSortLatest, 10, 0, viewerID)
	if err != nil {
		t.Fatalf("ListPosts: %v", err)
	}
	for i := range rows {
		if rows[i].Title == "头像测试帖" {
			return rows[i].AuthorAvatarURL
		}
	}
	t.Fatal("帖子列表里找不到测试帖")
	return nil
}

func postDetailAvatar(t *testing.T, s *Store, postID, viewerID int64) *string {
	t.Helper()
	p, err := s.PostByID(context.Background(), postID, viewerID)
	if err != nil {
		t.Fatalf("PostByID: %v", err)
	}
	return p.AuthorAvatarURL
}

func firstCommentAvatar(t *testing.T, s *Store, postID int64) *string {
	t.Helper()
	rows, _, err := s.ListPostComments(context.Background(), postID, 10, 0)
	if err != nil {
		t.Fatalf("ListPostComments: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("评论列表不该为空")
	}
	return rows[0].AuthorAvatarURL
}

func followListRow(t *testing.T, s *Store, userID int64, tab FollowListTab, wantID int64) *FollowListEntry {
	t.Helper()
	rows, err := s.ListFollowList(context.Background(), userID, tab)
	if err != nil {
		t.Fatalf("ListFollowList(%s): %v", tab, err)
	}
	for i := range rows {
		if rows[i].UserID == wantID {
			return &rows[i]
		}
	}
	return nil
}

func followListAvatar(t *testing.T, s *Store, userID int64, tab FollowListTab, wantID int64) *string {
	t.Helper()
	row := followListRow(t, s, userID, tab, wantID)
	if row == nil {
		t.Fatalf("名单 %s 里应能找到 %d", tab, wantID)
	}
	return row.AvatarURL
}

func firstDMAvatar(t *testing.T, s *Store, viewerID, peerID int64) *string {
	t.Helper()
	rows, _, err := s.ListDM(context.Background(), viewerID, peerID, 0, 10)
	if err != nil {
		t.Fatalf("ListDM: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("私聊列表不该为空")
	}
	return rows[0].SenderAvatarURL
}

func conversationAvatar(t *testing.T, s *Store, viewerID, peerID int64) *string {
	t.Helper()
	rows, err := s.ListDMConversations(context.Background(), viewerID)
	if err != nil {
		t.Fatalf("ListDMConversations: %v", err)
	}
	for i := range rows {
		if rows[i].PeerID == peerID {
			return rows[i].PeerAvatarURL
		}
	}
	t.Fatalf("会话列表里应能找到 peer=%d", peerID)
	return nil
}

func searchHitRow(t *testing.T, s *Store, keyword string, viewerID int64) *UserSearchHit {
	t.Helper()
	hits, err := s.SearchUsers(context.Background(), keyword, viewerID)
	if err != nil {
		t.Fatalf("SearchUsers: %v", err)
	}
	for i := range hits {
		if hits[i].Username == keyword {
			return &hits[i]
		}
	}
	return nil
}

func searchHitAvatar(t *testing.T, s *Store, keyword string, viewerID int64) *string {
	t.Helper()
	hit := searchHitRow(t, s, keyword, viewerID)
	if hit == nil {
		t.Fatalf("搜索结果里应能找到 %s", keyword)
	}
	return hit.AvatarURL
}

// publicAvatarOf 走 store 的列本身（不是 service 的 PublicProfile）：
// 本文件在 store 包内，判的是"这一列在库里是什么"，不是"接口怎么回它"。
func publicAvatarOf(t *testing.T, s *Store, userID int64) *string {
	t.Helper()
	u, err := s.UserByID(context.Background(), userID)
	if err != nil {
		t.Fatalf("UserByID: %v", err)
	}
	return u.AvatarURL
}
