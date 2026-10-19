package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 本文件是 wayfinder #38 的 store 层证据：板块（= 番剧条目）下的主题串
// （= 带 anime_id 的帖子）的查询路径、排序口径、反范式排序键的维护，
// 以及"不允许删有主题串的番剧"（验收第 2 条）。
//
// 与 posts_test.go 的分工：那里钉计数（like_count / comment_count）的不变量，
// 这里钉 last_reply_at 的不变量与 anime_id 的归属判据。两者共用的前提是
// "反范式列必须与纪录写入同事务"，所以这里也用"来回操作"而不是单次。

// newAnimeThreadFixture 造一个作者 + 一个番剧，返回 store 与两者。
func newAnimeThreadFixture(t *testing.T) (*Store, *User, *Anime) {
	t.Helper()
	s := requireTestDB(t)
	ctx := context.Background()

	author := &User{Username: "threadauthor", DisplayName: "串作者", PasswordHash: "$2a$10$xx"}
	if err := s.CreateUser(ctx, author); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	a := &Anime{Title: "板块测试番剧"}
	if err := s.CreateAnime(ctx, a); err != nil {
		t.Fatalf("CreateAnime: %v", err)
	}
	return s, author, a
}

// TestAnimeThreadCreateSetsLastReplyAt 钉住"发帖时 last_reply_at = created_at"。
//
// 为什么值得单独测：这个初值决定"没人回复的新帖"在 latest-reply 下排在哪。
// 若它是 NULL，排序行为取决于数据库对 NULLS 的处理（DESC 下默认 NULLS FIRST），
// 症状是"刚发的帖看不见，要翻到最后一页"—— 而那时人会以为发帖失败。
func TestAnimeThreadCreateSetsLastReplyAt(t *testing.T) {
	s, author, a := newAnimeThreadFixture(t)
	ctx := context.Background()

	animeID := a.ID
	p := &Post{AuthorID: author.ID, Title: "首个主题串", Content: "正文", AnimeID: &animeID}
	if err := s.CreatePost(ctx, p); err != nil {
		t.Fatalf("CreatePost(主题串): %v", err)
	}
	if p.LastReplyAt == nil {
		t.Fatalf("主题串的 last_reply_at 必须非 NULL")
	}
	ensureTimeClose(t, "last_reply_at 应等于 created_at", p.CreatedAt, *p.LastReplyAt)

	// 回读一次：确认它真的落进了列（而不是只在内存里）。
	got, err := s.PostByID(ctx, p.ID, 0)
	if err != nil {
		t.Fatalf("PostByID: %v", err)
	}
	if got.LastReplyAt == nil || got.AnimeID == nil || *got.AnimeID != animeID {
		t.Fatalf("anime_id / last_reply_at 未落库: animeId=%v lastReplyAt=%v", got.AnimeID, got.LastReplyAt)
	}

	// 普通帖子（没有板块）：anime_id 必须是 NULL —— 否则它会出现在某个板块里。
	plain := &Post{AuthorID: author.ID, Title: "普通帖", Content: "正文"}
	if err := s.CreatePost(ctx, plain); err != nil {
		t.Fatalf("CreatePost(普通帖): %v", err)
	}
	if plain.AnimeID != nil {
		t.Fatalf("普通帖子不该有 anime_id: %v", *plain.AnimeID)
	}
}

// TestAnimeThreadRejectsMissingAnime 钉住"挂到不存在的番剧"必须失败。
//
// 这是"板块即番剧条目"的准入条件：放行会让主题串永久不可见
// （没有任何板块页会列出它，而它又占了全站列表的一行）。
func TestAnimeThreadRejectsMissingAnime(t *testing.T) {
	s, author, _ := newAnimeThreadFixture(t)
	ctx := context.Background()

	missing := int64(999999999)
	p := &Post{AuthorID: author.ID, Title: "孤儿串", Content: "正文", AnimeID: &missing}
	if err := s.CreatePost(ctx, p); !errors.Is(err, ErrAnimeNotFound) {
		t.Fatalf("挂到不存在的番剧期望 ErrAnimeNotFound，得到 %v", err)
	}

	// 非法 id（0 / 负数）单独一条：它不是"番剧不存在"，而是输入本身不合法。
	for _, bad := range []int64{0, -1} {
		badID := bad
		p := &Post{AuthorID: author.ID, Title: "坏 id", Content: "正文", AnimeID: &badID}
		if err := s.CreatePost(ctx, p); err == nil {
			t.Fatalf("animeId=%d 应被拒绝", bad)
		}
	}
}

// TestAnimeThreadsSortByLatestReply 钉住两种排序口径（验收第 1 条）。
//
// 做法：造两个主题串，先发 A 再发 B（A 更老），然后给 A 加评论 ——
// 于是 latest-reply 下 A 应在前，latest 下 B 应在前。
// 这条用例同时钉住"加评论会推进 last_reply_at"（反范式列的核心维护点）。
func TestAnimeThreadsSortByLatestReply(t *testing.T) {
	s, author, a := newAnimeThreadFixture(t)
	ctx := context.Background()
	animeID := a.ID

	older := &Post{AuthorID: author.ID, Title: "先发的", Content: "正文", AnimeID: &animeID}
	if err := s.CreatePost(ctx, older); err != nil {
		t.Fatalf("CreatePost(older): %v", err)
	}
	// 造出可分辨的时间差：created_at 与 last_reply_at 都靠 now()，
	// 同一毫秒内发的两帖在两种排序下都无法区分（那会让断言变成掷骰子）。
	time.Sleep(10 * time.Millisecond)

	newer := &Post{AuthorID: author.ID, Title: "后发的", Content: "正文", AnimeID: &animeID}
	if err := s.CreatePost(ctx, newer); err != nil {
		t.Fatalf("CreatePost(newer): %v", err)
	}
	time.Sleep(10 * time.Millisecond)

	// 给更老的那篇加一条评论 → 它成为"最近有人来过"的那篇。
	if err := s.AddPostComment(ctx, &PostComment{PostID: older.ID, AuthorID: author.ID, Content: "顶一下"}); err != nil {
		t.Fatalf("AddPostComment: %v", err)
	}

	byReply, hasMore, err := s.ListAnimeThreads(ctx, animeID, AnimeThreadsSortLatestReply, 10, 0, 0)
	if err != nil || hasMore {
		t.Fatalf("ListAnimeThreads(latest-reply): hasMore=%v err=%v", hasMore, err)
	}
	if len(byReply) != 2 {
		t.Fatalf("应有 2 个主题串，得到 %d", len(byReply))
	}
	if byReply[0].ID != older.ID {
		t.Fatalf("latest-reply 应把刚被回复的排前面: first=%d want=%d", byReply[0].ID, older.ID)
	}

	byCreated, _, err := s.ListAnimeThreads(ctx, animeID, AnimeThreadsSortLatest, 10, 0, 0)
	if err != nil {
		t.Fatalf("ListAnimeThreads(latest): %v", err)
	}
	if len(byCreated) != 2 || byCreated[0].ID != newer.ID {
		t.Fatalf("latest 应按发帖时间倒序: first=%d want=%d", byCreated[0].ID, newer.ID)
	}

	// 评论推进了 last_reply_at：回读 older 确认它比 created_at 新。
	row, err := s.PostByID(ctx, older.ID, 0)
	if err != nil {
		t.Fatalf("PostByID(older): %v", err)
	}
	if row.LastReplyAt == nil || !row.LastReplyAt.After(row.CreatedAt) {
		t.Fatalf("加评论应推进 last_reply_at: created=%v lastReply=%v", row.CreatedAt, row.LastReplyAt)
	}
}

// TestAnimeThreadsIsolatedPerBoard 钉住"板块之间互不串台"。
//
// 这是子资源路径的全部意义：两个番剧各有一个主题串，各自的列表只能看到自己的。
// 若漏了 WHERE anime_id，症状是"每个板块都显示全站所有主题串" —— 而那在
// 只有一个板块的开发环境里**看不出来**。
func TestAnimeThreadsIsolatedPerBoard(t *testing.T) {
	s, author, a := newAnimeThreadFixture(t)
	ctx := context.Background()

	b := &Anime{Title: "第二个番剧"}
	if err := s.CreateAnime(ctx, b); err != nil {
		t.Fatalf("CreateAnime(b): %v", err)
	}

	aID, bID := a.ID, b.ID
	pa := &Post{AuthorID: author.ID, Title: "A 板块的串", Content: "正文", AnimeID: &aID}
	pb := &Post{AuthorID: author.ID, Title: "B 板块的串", Content: "正文", AnimeID: &bID}
	for _, p := range []*Post{pa, pb} {
		if err := s.CreatePost(ctx, p); err != nil {
			t.Fatalf("CreatePost(%s): %v", p.Title, err)
		}
	}

	rowsA, _, err := s.ListAnimeThreads(ctx, aID, AnimeThreadsSortLatestReply, 10, 0, 0)
	if err != nil || len(rowsA) != 1 || rowsA[0].ID != pa.ID {
		t.Fatalf("A 板块应只有自己的串: len=%d err=%v", len(rowsA), err)
	}
	rowsB, _, err := s.ListAnimeThreads(ctx, bID, AnimeThreadsSortLatestReply, 10, 0, 0)
	if err != nil || len(rowsB) != 1 || rowsB[0].ID != pb.ID {
		t.Fatalf("B 板块应只有自己的串: len=%d err=%v", len(rowsB), err)
	}

	// 不存在的番剧 → ErrAnimeNotFound（不是空列表：界面要区分"板块没了"与"还没帖"）。
	if _, _, err := s.ListAnimeThreads(ctx, 999999999, AnimeThreadsSortLatestReply, 10, 0, 0); !errors.Is(err, ErrAnimeNotFound) {
		t.Fatalf("不存在的番剧期望 ErrAnimeNotFound，得到 %v", err)
	}

	// CountAnimeThreads 与列表口径一致（删番剧的准入判据靠它）。
	n, err := s.CountAnimeThreads(ctx, aID)
	if err != nil || n != 1 {
		t.Fatalf("CountAnimeThreads(A) 应为 1: n=%d err=%v", n, err)
	}
}

// TestAnimeThreadsHasMoreAndLiked 钉住板块列表的分页与批量 liked
// （与全站列表同一形状：无限滚动靠 hasMore，前端因此可以复用同一个适配层）。
func TestAnimeThreadsHasMoreAndLiked(t *testing.T) {
	s, author, a := newAnimeThreadFixture(t)
	ctx := context.Background()
	animeID := a.ID

	ids := make([]int64, 0, 3)
	for _, title := range []string{"串一", "串二", "串三"} {
		p := &Post{AuthorID: author.ID, Title: title, Content: "正文", AnimeID: &animeID}
		if err := s.CreatePost(ctx, p); err != nil {
			t.Fatalf("CreatePost(%s): %v", title, err)
		}
		ids = append(ids, p.ID)
		time.Sleep(5 * time.Millisecond) // 让 latest-reply 的顺序确定
	}
	if _, err := s.TogglePostLike(ctx, ids[0], author.ID); err != nil {
		t.Fatalf("TogglePostLike: %v", err)
	}

	page1, hasMore, err := s.ListAnimeThreads(ctx, animeID, AnimeThreadsSortLatestReply, 2, 0, author.ID)
	if err != nil || len(page1) != 2 || !hasMore {
		t.Fatalf("第一页: len=%d hasMore=%v err=%v", len(page1), hasMore, err)
	}
	// latest-reply 且时间递增：最新的串三应在第一页第一位。
	if page1[0].ID != ids[2] {
		t.Fatalf("第一页首位应是最新的串三: got=%d want=%d", page1[0].ID, ids[2])
	}

	page2, hasMore2, err := s.ListAnimeThreads(ctx, animeID, AnimeThreadsSortLatestReply, 2, 2, author.ID)
	if err != nil || len(page2) != 1 || hasMore2 {
		t.Fatalf("第二页: len=%d hasMore=%v err=%v", len(page2), hasMore2, err)
	}

	// liked 只在被赞的那一行上为 true（且它落在第二页 —— 最老的那篇）。
	if page2[0].ID != ids[0] || !page2[0].Liked {
		t.Fatalf("第二页应是已赞的最老串: id=%d liked=%v", page2[0].ID, page2[0].Liked)
	}
	for i := range page1 {
		if page1[i].Liked {
			t.Fatalf("第一页不该有 liked=true 的行: id=%d", page1[i].ID)
		}
	}
	// 匿名视角：一律 false（viewerID=0 时不做点赞查询）。
	anon, _, err := s.ListAnimeThreads(ctx, animeID, AnimeThreadsSortLatestReply, 10, 0, 0)
	if err != nil {
		t.Fatalf("匿名列表: %v", err)
	}
	for i := range anon {
		if anon[i].Liked {
			t.Fatalf("匿名视角不该有 liked=true: id=%d", anon[i].ID)
		}
	}
}

// TestDeleteAnimeBlockedByThreads 是验收第 2 条的证据：
// 有主题串的番剧删不掉（ErrAnimeHasThreads），删掉主题串之后才删得掉。
//
// 为什么"删完主题串再删番剧"这一半也要测：若只测拒绝，一个"永远删不掉"的
// 实现也能通过。而"删掉帖子之后能删番剧"才是这条规则真正的形状。
func TestDeleteAnimeBlockedByThreads(t *testing.T) {
	s, author, a := newAnimeThreadFixture(t)
	ctx := context.Background()
	animeID := a.ID

	p := &Post{AuthorID: author.ID, Title: "挡路的主题串", Content: "正文", AnimeID: &animeID}
	if err := s.CreatePost(ctx, p); err != nil {
		t.Fatalf("CreatePost: %v", err)
	}

	if err := s.DeleteAnime(ctx, animeID); !errors.Is(err, ErrAnimeHasThreads) {
		t.Fatalf("有主题串时期望 ErrAnimeHasThreads，得到 %v", err)
	}
	// 番剧必须还在（被拒绝的删除不能产生任何写入）。
	if _, err := s.AnimeByID(ctx, animeID); err != nil {
		t.Fatalf("被拒绝的删除不该真的删掉番剧: %v", err)
	}

	// 删掉主题串。这里直接删行：帖子本身没有"删帖"端点（那是另一张票的范围），
	// 而本用例要验的是"番剧的准入判据随主题串清空而解除"。
	if err := s.DB().WithContext(ctx).Exec(`DELETE FROM posts WHERE id = ?`, p.ID).Error; err != nil {
		t.Fatalf("删主题串: %v", err)
	}

	if err := s.DeleteAnime(ctx, animeID); err != nil {
		t.Fatalf("主题串清空后应能删番剧: %v", err)
	}
	if _, err := s.AnimeByID(ctx, animeID); !errors.Is(err, ErrAnimeNotFound) {
		t.Fatalf("删除后应查不到番剧: %v", err)
	}
}

// TestDeleteAnimeThreadRaceBlockedByForeignKey 钉住数据库外键这一层防线。
//
// 它模拟的是"COUNT 检查通过 → 另一个请求刚发了一个主题串 → DELETE 执行"这个竞态。
// 做法是绕过 store 的检查、用原生 SQL 删番剧 —— 若没有 ON DELETE RESTRICT，
// 这一步会成功并留下一批挂在已删番剧上的帖子（永久不可见、也删不掉）。
//
// 为什么这条比上面那条更重要：应用层检查会被下一个调用点忘掉，外键不会。
func TestDeleteAnimeThreadRaceBlockedByForeignKey(t *testing.T) {
	s, author, a := newAnimeThreadFixture(t)
	ctx := context.Background()
	animeID := a.ID

	p := &Post{AuthorID: author.ID, Title: "竞态串", Content: "正文", AnimeID: &animeID}
	if err := s.CreatePost(ctx, p); err != nil {
		t.Fatalf("CreatePost: %v", err)
	}

	// 绕过 store 的 COUNT 检查，直接删 —— 外键必须拦住。
	err := s.DB().WithContext(ctx).Exec(`DELETE FROM anime WHERE id = ?`, animeID).Error
	if err == nil {
		t.Fatalf("外键 RESTRICT 必须拦住：有主题串时不能删番剧")
	}
	if !isForeignKeyViolation(err) {
		t.Fatalf("期望外键冲突（23503），得到 %v", err)
	}
}

// TestAnimeThreadsNullLastReplySortsLast 钉住 `NULLS LAST` 这条容易被删掉的细节。
//
// 为什么需要它：迁移 0013 **不回填**老行的 last_reply_at（见 DDL 注释），
// 所以库里确实存在 last_reply_at IS NULL 的帖子。而 PostgreSQL 在 `DESC` 下
// 默认是 **NULLS FIRST** —— 少写 NULLS LAST，任何一篇"后来被挂上板块的老帖"
// 都会立刻跳到板块第一位。症状是"板块顶部永远是那几篇没人回过的老帖"，
// 而它看起来完全像是排序逻辑写反了，实际是一个 NULL 默认值。
//
// 用例直接构造一行 last_reply_at = NULL 的主题串（模拟"老帖补挂板块"），
// 然后断言它排在正常主题串之后。
func TestAnimeThreadsNullLastReplySortsLast(t *testing.T) {
	s, author, a := newAnimeThreadFixture(t)
	ctx := context.Background()
	animeID := a.ID

	normal := &Post{AuthorID: author.ID, Title: "正常主题串", Content: "正文", AnimeID: &animeID}
	if err := s.CreatePost(ctx, normal); err != nil {
		t.Fatalf("CreatePost(normal): %v", err)
	}

	// 模拟"老帖补挂板块"：直接插一行 last_reply_at = NULL 的主题串。
	// 走原生 SQL 是刻意的 —— CreatePost 一定会设值，构造不出这个形态。
	var nullID int64
	if err := s.DB().WithContext(ctx).Raw(`INSERT INTO posts (author_id, title, content, anime_id, last_reply_at)
		VALUES (?, '老帖补挂', '正文', ?, NULL) RETURNING id`, author.ID, animeID).Scan(&nullID).Error; err != nil {
		t.Fatalf("插入 last_reply_at=NULL 的主题串: %v", err)
	}
	if nullID <= 0 {
		t.Fatalf("未回填 id")
	}

	rows, _, err := s.ListAnimeThreads(ctx, animeID, AnimeThreadsSortLatestReply, 10, 0, 0)
	if err != nil {
		t.Fatalf("ListAnimeThreads: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("应有 2 个主题串，得到 %d", len(rows))
	}
	if rows[0].ID != normal.ID {
		t.Fatalf("last_reply_at 非 NULL 的应排在前面：first=%d want=%d（NULL 排到了最前 = 少了 NULLS LAST）",
			rows[0].ID, normal.ID)
	}
	if rows[1].ID != nullID || rows[1].LastReplyAt != nil {
		t.Fatalf("NULL 行应排在末尾且保持 NULL：%+v", rows[1])
	}
}
