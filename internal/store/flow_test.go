package store

import (
	"context"
	"errors"
	"testing"
)

// 关注流（票 #66）的真库证据。
//
// 这张票的核心不是"多一个排序值"，而是**把可见性谓词收敛到一处**：
// 「关注者 ∧ 双向无拉黑 ∧ 未封禁」这三条在关注流、他人帖列表（#68）、
// 我的帖管理（#70）三处都要成立。写成三份的后果是改口径时漏掉一处，
// 症状是"某个列表里还能看见被拉黑的人"—— 那种泄漏在界面上没有任何报错，
// 只会被细心的人偶然发现。
//
// 所以下面的用例不只测"关注流排得对"，还测**每一条过滤各自独立生效**
// （分别造一个只违反一条的数据，确认它真的被挡住）。

// flowEnv 造四个用户 + 每人一篇帖子，返回 (store, 各用户)。
//
//	me     —— 看流的人
//	alice  —— 我关注了（正常，应当出现）
//	bob    —— 我没关注（不应当出现）
//	carol  —— 我关注了，但她拉黑了我（应当被挡住；注意这条边**还在**）
//	dave   —— 我关注了，但他被封禁（应当被挡住）
func flowEnv(t *testing.T) (s *Store, me, alice, bob, carol, dave *User) {
	t.Helper()
	s = requireTestDB(t)
	ctx := context.Background()

	me = seedUser(t, s, "flow-me")
	alice = seedUser(t, s, "flow-alice")
	bob = seedUser(t, s, "flow-bob")
	carol = seedUser(t, s, "flow-carol")
	dave = seedUser(t, s, "flow-dave")

	for _, u := range []*User{alice, bob, carol, dave} {
		p := &Post{AuthorID: u.ID, Title: "帖子 by " + u.Username, Content: "正文", Images: []string{}}
		if err := s.CreatePost(ctx, p); err != nil {
			t.Fatalf("CreatePost(%s): %v", u.Username, err)
		}
	}

	// 我关注 alice / carol / dave。
	for _, u := range []*User{alice, carol, dave} {
		if _, _, err := s.CreateFollow(ctx, me.ID, u.ID); err != nil {
			t.Fatalf("CreateFollow(%s): %v", u.Username, err)
		}
	}

	// carol 拉黑我：**我→carol 的边保留**（拉黑删的是拉黑方自己那条边），
	// 所以"被拉黑的人还在我的关注列表里"是真实存在的数据形态 ——
	// 只看 follows 表挡不住她，必须靠 blocked 谓词。
	if _, err := s.BlockUser(ctx, carol.ID, me.ID); err != nil {
		t.Fatalf("BlockUser(carol→me): %v", err)
	}

	// dave 被封禁：边还在，但账号在界面上不存在了。
	if err := s.SetUserStatus(ctx, dave.ID, StatusBanned, "刷屏"); err != nil {
		t.Fatalf("SetUserStatus(dave): %v", err)
	}
	return s, me, alice, bob, carol, dave
}

// authorsOf 把一页帖子压成作者 id 集合，便于断言"谁在流里"。
func authorsOf(rows []Post) map[int64]bool {
	out := make(map[int64]bool, len(rows))
	for i := range rows {
		out[rows[i].AuthorID] = true
	}
	return out
}

// TestFollowingFlowOnlyHasFollowees 是这张票的主判据：
// 关注流里只有"我关注的人"，且被拉黑与被封禁的都不在。
func TestFollowingFlowOnlyHasFollowees(t *testing.T) {
	s, me, alice, bob, carol, dave := flowEnv(t)
	ctx := context.Background()

	rows, _, err := s.ListPosts(ctx, PostsSortFollowing, 20, 0, me.ID)
	if err != nil {
		t.Fatalf("关注流查询失败：%v", err)
	}

	got := authorsOf(rows)
	if !got[alice.ID] {
		t.Fatalf("我关注的人应当出现在关注流里：alice=%d rows=%v", alice.ID, got)
	}
	if got[bob.ID] {
		t.Fatalf("没关注的人不应当出现：bob=%d rows=%v", bob.ID, got)
	}
	if got[carol.ID] {
		t.Fatalf("拉黑我的人不应当出现在我的关注流里：carol=%d rows=%v", carol.ID, got)
	}
	if got[dave.ID] {
		t.Fatalf("被封禁的人不应当出现在关注流里：dave=%d rows=%v", dave.ID, got)
	}
	if len(rows) != 1 {
		t.Fatalf("关注流应当只有 alice 那一篇，实际 %d 篇：%v", len(rows), got)
	}
}

// TestFollowingFlowIsStrictlyTimeOrdered 钉住"严格时间倒序"（决议 #52 §2）。
//
// 不做热度加权：时间线永远可预测，而加权参数要调、空窗期会整天不更新。
func TestFollowingFlowIsStrictlyTimeOrdered(t *testing.T) {
	s, me, alice, bob, _, _ := flowEnv(t)
	ctx := context.Background()

	// bob 也关注进来（这样流里至少有两篇），并让他的帖子更新。
	if _, _, err := s.CreateFollow(ctx, me.ID, bob.ID); err != nil {
		t.Fatalf("CreateFollow(me→bob): %v", err)
	}
	// 把 alice 的帖子压到过去，bob 的保持最新 —— 谁最新谁在前。
	if err := s.db.Exec(`UPDATE posts SET created_at = now() - interval '2 hours' WHERE author_id = ?`, alice.ID).Error; err != nil {
		t.Fatalf("调 alice 帖子时间：%v", err)
	}

	rows, _, err := s.ListPosts(ctx, PostsSortFollowing, 20, 0, me.ID)
	if err != nil {
		t.Fatalf("关注流查询失败：%v", err)
	}
	if len(rows) < 2 {
		t.Fatalf("应当至少两篇（alice + bob），实际 %d", len(rows))
	}
	if rows[0].AuthorID != bob.ID {
		t.Fatalf("最新的一篇应当排第一：want bob=%d got author=%d", bob.ID, rows[0].AuthorID)
	}
}

// TestFollowingFlowDropsAuthorOnUnfollow 钉住"取关即出流"。
//
// 边一删，下一篇帖子就不再出现 —— 没有缓存、没有"下次刷新才生效"。
func TestFollowingFlowDropsAuthorOnUnfollow(t *testing.T) {
	s, me, alice, _, _, _ := flowEnv(t)
	ctx := context.Background()

	rows, _, err := s.ListPosts(ctx, PostsSortFollowing, 20, 0, me.ID)
	if err != nil {
		t.Fatalf("关注流查询失败：%v", err)
	}
	if !authorsOf(rows)[alice.ID] {
		t.Fatalf("取关前 alice 应当在流里")
	}

	if _, err := s.RemoveFollow(ctx, me.ID, alice.ID); err != nil {
		t.Fatalf("RemoveFollow: %v", err)
	}

	rows, _, err = s.ListPosts(ctx, PostsSortFollowing, 20, 0, me.ID)
	if err != nil {
		t.Fatalf("取关后查询失败：%v", err)
	}
	if authorsOf(rows)[alice.ID] {
		t.Fatalf("取关后 alice 不应当还在流里")
	}
}

// TestFollowingFlowDoesNotLeakIntoLatest 钉住"普通列表无泄漏"。
//
// 同一条查询函数被三种排序共用，谓词一旦挂错位置（比如加到公共 Where 里），
// 症状是"最新列表突然只剩关注的人"—— 那是最严重的一种回归，因为它是静默的。
//
// 这里**只**判"没关注的人也还在"，不判被封禁者的存量帖是否可见：
// 那是另一个产品问题（公开列表要不要隐藏封禁者的历史内容），本票没有决策，
// 顺手把它钉进用例等于替后续票做了决定。
func TestFollowingFlowDoesNotLeakIntoLatest(t *testing.T) {
	s, me, _, bob, _, _ := flowEnv(t)
	ctx := context.Background()

	rows, _, err := s.ListPosts(ctx, PostsSortLatest, 20, 0, me.ID)
	if err != nil {
		t.Fatalf("最新列表查询失败：%v", err)
	}
	if !authorsOf(rows)[bob.ID] {
		t.Fatalf("最新列表应当包含没关注的人（bob=%d）", bob.ID)
	}
}

// TestFollowingFlowRequiresViewer 钉住"匿名请求关注流是被拒绝的"。
//
// 匿名视角下"我关注的人"没有意义 —— 返回空列表会把"没登录"伪装成"你还没关注谁"，
// 于是界面会给出错误的引导（叫一个游客去关注别人）。
func TestFollowingFlowRequiresViewer(t *testing.T) {
	s, _, _, _, _, _ := flowEnv(t)
	ctx := context.Background()

	if _, _, err := s.ListPosts(ctx, PostsSortFollowing, 20, 0, 0); !errors.Is(err, ErrFlowNeedsViewer) {
		t.Fatalf("匿名请求关注流应当返回 ErrFlowNeedsViewer，实际 %v", err)
	}
}

// TestFollowingFlowPagesWithoutDuplicates 钉住分页边界不重不漏。
//
// 排序键是 `created_at DESC, id DESC`，与列表同一口径 —— 同一毫秒发的两篇
// 因此在翻页边界上不会重复或漏行。
func TestFollowingFlowPagesWithoutDuplicates(t *testing.T) {
	s, me, _, _, _, _ := flowEnv(t)
	ctx := context.Background()

	// 造 5 个我关注的人、每人一篇，时间全部相同（最容易出边界错的情形）。
	var ids []int64
	for i := 0; i < 5; i++ {
		u := seedUser(t, s, "flow-page-"+string(rune('a'+i)))
		p := &Post{AuthorID: u.ID, Title: "同秒帖", Content: "正文", Images: []string{}}
		if err := s.CreatePost(ctx, p); err != nil {
			t.Fatalf("CreatePost: %v", err)
		}
		ids = append(ids, p.ID)
		if _, _, err := s.CreateFollow(ctx, me.ID, u.ID); err != nil {
			t.Fatalf("CreateFollow: %v", err)
		}
	}
	if err := s.db.Exec(`UPDATE posts SET created_at = now() WHERE id IN ?`, ids).Error; err != nil {
		t.Fatalf("统一时间：%v", err)
	}

	seen := map[int64]bool{}
	for offset := 0; offset < 10; offset += 2 {
		rows, _, err := s.ListPosts(ctx, PostsSortFollowing, 2, offset, me.ID)
		if err != nil {
			t.Fatalf("分页查询失败：%v", err)
		}
		for i := range rows {
			if seen[rows[i].ID] {
				t.Fatalf("分页出现重复行：post=%d", rows[i].ID)
			}
			seen[rows[i].ID] = true
		}
	}
	// alice 那篇也在流里（flowEnv 造的），加上 5 篇同秒帖 = 6 篇。
	if len(seen) != 6 {
		t.Fatalf("翻完应当看到全部 6 篇，实际 %d 篇", len(seen))
	}
}
