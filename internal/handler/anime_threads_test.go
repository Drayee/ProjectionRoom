package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/service"
	"ProjectionRoom/internal/service/auth"
	"ProjectionRoom/internal/store"
)

// 本文件是 wayfinder #38 的**协议面**证据：板块主题串列表的路径、排序参数、
// 404/400 边界，以及"不允许删有主题串的番剧 → 409"（验收第 2 条）。
//
// 与 store/anime_threads_test.go 的分工：那里验库里的行、排序与 last_reply_at
// 的维护；这里验 HTTP 契约（路由挂在哪、参数怎么校验、状态码与错误码是什么）。
//
// 装配约束与 messages_test.go / friendships_test.go 相同：用真 store（归属判据
// 一半落在 store 的查询与外键上），认证侧用内存用户表。无库环境整组 Skip。

// atEnv 是板块主题串端点的测试装配。
type atEnv struct {
	engine *gin.Engine
	st     *store.Store
	// author 是普通用户（发主题串），admin 是管理员（删番剧要 RequireAdmin）。
	author *store.User
	admin  *store.User
	// other 是第三方（验"板块之间互不串台"与匿名视角）。
	other *store.User
	tokAu string
	tokAd string
	tokOt string
}

// requireAnimeThreadTestDB 打开测试库、清表；未配置 DSN 时 Skip。
//
// 复用 messages_test.go 的 lockMsgTestDB（同一把测试库串行化锁）：
// handler 包里所有真库用例必须共用同一把锁，否则两个用例并发 TRUNCATE 会互相毁数据。
func requireAnimeThreadTestDB(t *testing.T) *store.Store {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("PR_TEST_DB_DSN"))
	if dsn == "" {
		t.Skip("未设置 PR_TEST_DB_DSN：跳过需要真库的板块主题串用例")
	}
	lockMsgTestDB(t, dsn)

	ctx := context.Background()
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("连接测试库失败：%v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	var tables []string
	if err := st.DB().Raw(`SELECT tablename FROM pg_tables
		WHERE schemaname = 'public' AND tablename <> 'schema_migrations'`).Scan(&tables).Error; err != nil {
		t.Fatalf("列出业务表失败：%v", err)
	}
	if len(tables) > 0 {
		quoted := make([]string, 0, len(tables))
		for _, name := range tables {
			quoted = append(quoted, `"`+name+`"`)
		}
		if err := st.DB().Exec(`TRUNCATE TABLE ` + strings.Join(quoted, ", ") + ` RESTART IDENTITY CASCADE`).Error; err != nil {
			t.Fatalf("清空测试库失败：%v", err)
		}
	}
	return st
}

// newAnimeThreadEnv 起一台"番剧目录 + 帖子域都可用"的引擎。
func newAnimeThreadEnv(t *testing.T) *atEnv {
	t.Helper()
	st := requireAnimeThreadTestDB(t)
	ctx := context.Background()

	cfg := config.Default()
	cfg.Segment.TempDir = t.TempDir()
	cfg.Static.Serve = false
	cfg.Auth.DBDSN = "host=127.0.0.1 dbname=fixture sslmode=disable"
	if cfg.Auth.PublicBaseURL == "" {
		cfg.Auth.PublicBaseURL = "https://pr.test"
	}
	// 放宽限速：本文件的用例会在同一 IP 上连打十几个请求（多数是参数校验），
	// 而发帖桶默认 5/min —— 那会把"sort 非法应 400"打成 429。
	// 与 messages_test.go 同一条理由：限速挡脚本，不该挡验收。
	cfg.IPC.RegisterPerMinute = 1000
	cfg.IPC.RegisterBurst = 200
	cfg.IPC.LoginPerMinute = 1000
	cfg.IPC.LoginBurst = 200
	cfg.IPC.RoomMetaPerMinute = 1000
	cfg.IPC.RoomMetaBurst = 200

	author := &store.User{Username: "at-author", DisplayName: "串作者", PasswordHash: "hash-a"}
	if err := st.CreateUser(ctx, author); err != nil {
		t.Fatalf("建 author 失败：%v", err)
	}
	admin := &store.User{Username: "at-admin", DisplayName: "管理员", PasswordHash: "hash-b", Role: store.RoleAdmin}
	if err := st.CreateUser(ctx, admin); err != nil {
		t.Fatalf("建 admin 失败：%v", err)
	}
	other := &store.User{Username: "at-other", DisplayName: "第三方", PasswordHash: "hash-c"}
	if err := st.CreateUser(ctx, other); err != nil {
		t.Fatalf("建 other 失败：%v", err)
	}

	signer, err := auth.NewSigner(handlerTestSecret, 15*time.Minute)
	if err != nil {
		t.Fatalf("构造 Signer 失败：%v", err)
	}
	tickets := auth.NewTicketStore(cfg.Auth.WSTicketTTL)
	// 认证侧用内存用户表（与 messages_test.go 同一取舍）：板块端点本身只认
	// AuthDeps.Store（真库），认证侧的内存表不影响本文件的判据。
	acctStore := newFakeAdminStore()
	acctStore.users[author.ID] = author
	acctStore.users[admin.ID] = admin
	acctStore.users[other.ID] = other
	acctStore.nextID = other.ID
	svc, err := service.NewAccountService(acctStore, signer, tickets, service.AccountOptions{
		RefreshTTL:     cfg.Auth.RefreshTTL,
		ResetTTL:       cfg.Auth.ResetTTL,
		EmailVerifyTTL: cfg.Auth.EmailVerifyTTL,
		OTPTTL:         cfg.Auth.OTPTTL,
		PublicBaseURL:  cfg.Auth.PublicBaseURL,
	})
	if err != nil {
		t.Fatalf("构造 AccountService 失败：%v", err)
	}

	hub, cleanup, err := service.NewHub(cfg)
	if err != nil {
		t.Fatalf("构造 Hub 失败：%v", err)
	}
	rooms := service.NewManager(cfg, hub)
	t.Cleanup(func() { rooms.Stop(); cleanup() })

	engine := NewRouter(cfg, hub, rooms, nil, nil,
		AuthDeps{
			Service: svc,
			Session: SessionConfig{
				AccessTTL:   cfg.Auth.AccessTTL,
				RefreshTTL:  cfg.Auth.RefreshTTL,
				WSTicketTTL: cfg.Auth.WSTicketTTL,
			},
			Store: st,
		},
		AdminDeps{},
	)

	issue := func(u *store.User) string {
		tok, _, err := signer.Issue(u.ID, u.Role, u.TokenVersion)
		if err != nil {
			t.Fatalf("签发 %s 的 token 失败：%v", u.Username, err)
		}
		return tok
	}
	return &atEnv{
		engine: engine, st: st,
		author: author, admin: admin, other: other,
		tokAu: issue(author), tokAd: issue(admin), tokOt: issue(other),
	}
}

// do 发一个请求（body 为 nil 时不带请求体）。
func (e *atEnv) do(t *testing.T, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求体失败：%v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, path, reader)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	e.engine.ServeHTTP(w, req)
	return w
}

// decodeJSON 解析响应体（失败即 Fatal，附带原始字节）。
func decodeJSON(t *testing.T, w *httptest.ResponseRecorder, into any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), into); err != nil {
		t.Fatalf("解析响应体失败：%v body=%s", err, w.Body.String())
	}
}

// createAnime 通过管理端端点建一个番剧（走完整装配路径，不直接插库：
// 本文件的判据里包含"番剧条目是板块的唯一来源"）。
func (e *atEnv) createAnime(t *testing.T, title string) int64 {
	t.Helper()
	w := e.do(t, http.MethodPost, "/api/admin/animes", e.tokAd, map[string]any{"title": title})
	if w.Code != http.StatusCreated {
		t.Fatalf("建番剧 %q 失败：%d %s", title, w.Code, w.Body.String())
	}
	var row struct {
		ID int64 `json:"id"`
	}
	decodeJSON(t, w, &row)
	if row.ID <= 0 {
		t.Fatalf("建番剧未回 id：%s", w.Body.String())
	}
	return row.ID
}

// createThread 在某个板块下发一个主题串（`POST /api/posts` + 请求体里的 animeId）。
func (e *atEnv) createThread(t *testing.T, animeID int64, title, token string) int64 {
	t.Helper()
	w := e.do(t, http.MethodPost, "/api/posts", token, map[string]any{
		"title": title, "content": "主题串正文", "animeId": animeID,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("发主题串 %q 失败：%d %s", title, w.Code, w.Body.String())
	}
	var row struct {
		ID      int64  `json:"id"`
		AnimeID *int64 `json:"animeId"`
	}
	decodeJSON(t, w, &row)
	if row.ID <= 0 {
		t.Fatalf("发主题串未回 id：%s", w.Body.String())
	}
	if row.AnimeID == nil || *row.AnimeID != animeID {
		t.Fatalf("响应里的 animeId 应是 %d：%s", animeID, w.Body.String())
	}
	return row.ID
}

// TestAnimeBoardThreadsListAndSort 覆盖验收第 1 条的前半：
// 板块下能发主题串、能列出来，且两种 sort 口径都能用、都与界面一致。
func TestAnimeBoardThreadsListAndSort(t *testing.T) {
	env := newAnimeThreadEnv(t)
	animeID := env.createAnime(t, "板块列表番剧")

	older := env.createThread(t, animeID, "先发的串", env.tokAu)
	time.Sleep(10 * time.Millisecond)
	newer := env.createThread(t, animeID, "后发的串", env.tokOt)
	time.Sleep(10 * time.Millisecond)

	// 给更老的那篇回复 → latest-reply 下它排前面（"排序口径与界面一致"的判据）。
	if w := env.do(t, http.MethodPost, "/api/posts/"+itoa(older)+"/comments", env.tokOt,
		map[string]any{"content": "顶一下"}); w.Code != http.StatusCreated {
		t.Fatalf("回复主题串失败：%d %s", w.Code, w.Body.String())
	}

	// 默认（不带 sort）= latest-reply。
	var byReply struct {
		Items   []postView `json:"items"`
		HasMore bool       `json:"hasMore"`
	}
	w := env.do(t, http.MethodGet, "/api/animes/"+itoa(animeID)+"/posts", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("板块列表应 200：%d %s", w.Code, w.Body.String())
	}
	decodeJSON(t, w, &byReply)
	if len(byReply.Items) != 2 || byReply.HasMore {
		t.Fatalf("应有 2 个主题串且无下一页：len=%d hasMore=%v", len(byReply.Items), byReply.HasMore)
	}
	if byReply.Items[0].ID != older {
		t.Fatalf("默认排序应把刚被回复的排前面：first=%d want=%d", byReply.Items[0].ID, older)
	}
	// 主题串的响应里必须带 animeId 与 lastReplyAt（前端据前者判断"这是主题串"）。
	if byReply.Items[0].AnimeID == nil || *byReply.Items[0].AnimeID != animeID {
		t.Fatalf("主题串响应应带 animeId=%d：%+v", animeID, byReply.Items[0])
	}
	if byReply.Items[0].LastReplyAt == "" {
		t.Fatalf("主题串响应应带 lastReplyAt：%+v", byReply.Items[0])
	}

	// sort=latest = 按发帖时间。
	var byCreated struct {
		Items []postView `json:"items"`
	}
	w = env.do(t, http.MethodGet, "/api/animes/"+itoa(animeID)+"/posts?sort=latest", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("sort=latest 应 200：%d %s", w.Code, w.Body.String())
	}
	decodeJSON(t, w, &byCreated)
	if len(byCreated.Items) != 2 || byCreated.Items[0].ID != newer {
		t.Fatalf("sort=latest 应按发帖时间倒序：first=%d want=%d", byCreated.Items[0].ID, newer)
	}

	// 空板块：200 + 空数组（不是 404 —— 板块存在、只是还没人发）。
	emptyID := env.createAnime(t, "空板块番剧")
	var empty struct {
		Items   []postView `json:"items"`
		HasMore bool       `json:"hasMore"`
	}
	w = env.do(t, http.MethodGet, "/api/animes/"+itoa(emptyID)+"/posts", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("空板块应 200：%d %s", w.Code, w.Body.String())
	}
	decodeJSON(t, w, &empty)
	if len(empty.Items) != 0 || empty.HasMore {
		t.Fatalf("空板块应回空数组：%s", w.Body.String())
	}
	// `items` 必须是 `[]` 而不是 `null`：前端直接 .map。
	if !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatalf("空板块的 items 必须是空数组而不是 null：%s", w.Body.String())
	}
}

// TestAnimeBoardThreadsValidationAndNotFound 覆盖边界：400（sort 非法 / id 非法）
// 与 404（番剧不存在）。
//
// 为什么"番剧不存在 → 404"值得单独钉：返回空列表会把"板块没了"伪装成
// "板块还没人发帖"，而用户会一直刷新一个永远不会出现内容的页面。
func TestAnimeBoardThreadsValidationAndNotFound(t *testing.T) {
	env := newAnimeThreadEnv(t)
	animeID := env.createAnime(t, "边界番剧")

	// sort 非法 → 400（不静默降级：分页口径被悄悄改掉是最难查的 bug）。
	for _, sort := range []string{"hot", "newest", "reply-asc"} {
		w := env.do(t, http.MethodGet, "/api/animes/"+itoa(animeID)+"/posts?sort="+sort, "", nil)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("sort=%q 应 400：got %d %s", sort, w.Code, w.Body.String())
		}
	}
	// limit 非整数 → 400（同一条纪律：不静默用缺省值）。
	w := env.do(t, http.MethodGet, "/api/animes/"+itoa(animeID)+"/posts?limit=abc", "", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("limit=abc 应 400：got %d %s", w.Code, w.Body.String())
	}

	// 番剧不存在 → 404。
	w = env.do(t, http.MethodGet, "/api/animes/999999999/posts", "", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("不存在的番剧应 404：got %d %s", w.Code, w.Body.String())
	}
	var notFound struct {
		Code string `json:"code"`
	}
	decodeJSON(t, w, &notFound)
	if notFound.Code != codeNotFound {
		t.Fatalf("404 的错误码应是 %s：%s", codeNotFound, w.Body.String())
	}

	// id 非法（非数字 / 0 / 负数）→ 400。
	for _, bad := range []string{"abc", "0", "-1"} {
		w := env.do(t, http.MethodGet, "/api/animes/"+bad+"/posts", "", nil)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("animeId=%q 应 400：got %d %s", bad, w.Code, w.Body.String())
		}
	}
}

// TestCreatePostWithAnimeID 覆盖"在板块里发主题串"的两条边界：
//   - animeId 指向不存在的番剧 → 404（不是 400：id 合法，是它指向的东西不在）；
//   - animeId = 0 / 负数 → 400（前端算错了 id，不该静默发到全站）；
//   - 不带 animeId → 普通帖子（响应里没有 animeId 键）。
func TestCreatePostWithAnimeID(t *testing.T) {
	env := newAnimeThreadEnv(t)
	animeID := env.createAnime(t, "发帖边界番剧")

	w := env.do(t, http.MethodPost, "/api/posts", env.tokAu, map[string]any{
		"title": "孤儿串", "content": "正文", "animeId": 999999999,
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("挂到不存在的番剧应 404：got %d %s", w.Code, w.Body.String())
	}

	for _, bad := range []int64{0, -1} {
		w := env.do(t, http.MethodPost, "/api/posts", env.tokAu, map[string]any{
			"title": "坏 id 串", "content": "正文", "animeId": bad,
		})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("animeId=%d 应 400：got %d %s", bad, w.Code, w.Body.String())
		}
	}

	// 普通帖子：响应里不该出现 animeId（omitempty + 指针）。
	w = env.do(t, http.MethodPost, "/api/posts", env.tokAu, map[string]any{
		"title": "普通帖", "content": "正文",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("发普通帖应 201：got %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"animeId"`) {
		t.Fatalf("普通帖不该带 animeId：%s", w.Body.String())
	}

	// 反向确认：主题串的响应**必须**带 animeId（否则前端区分不了两者）。
	w = env.do(t, http.MethodPost, "/api/posts", env.tokAu, map[string]any{
		"title": "正常主题串", "content": "正文", "animeId": animeID,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("发主题串应 201：got %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"animeId":`) {
		t.Fatalf("主题串必须带 animeId：%s", w.Body.String())
	}
}

// TestDeleteAnimeWithThreadsReturns409 是验收第 2 条的证据（HTTP 面）：
// 有主题串的番剧删不掉，返回 409 + ANIME_HAS_THREADS；且番剧必须还在。
func TestDeleteAnimeWithThreadsReturns409(t *testing.T) {
	env := newAnimeThreadEnv(t)
	animeID := env.createAnime(t, "有串的番剧")
	env.createThread(t, animeID, "挡路的串", env.tokAu)

	w := env.do(t, http.MethodDelete, "/api/admin/animes/"+itoa(animeID), env.tokAd, nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("有主题串时删番剧应 409：got %d %s", w.Code, w.Body.String())
	}
	var conflict struct {
		Code string `json:"code"`
	}
	decodeJSON(t, w, &conflict)
	if conflict.Code != CodeAnimeHasThreads {
		t.Fatalf("错误码应是 %s：%s", CodeAnimeHasThreads, w.Body.String())
	}
	// 被拒绝的删除不能产生任何写入：番剧必须还在。
	if w := env.do(t, http.MethodGet, "/api/animes/"+itoa(animeID), "", nil); w.Code != http.StatusOK {
		t.Fatalf("被拒绝后番剧应仍然存在：got %d %s", w.Code, w.Body.String())
	}

	// 空板块删得掉（204）—— "永远删不掉"的实现不能通过这条用例。
	emptyID := env.createAnime(t, "空板块可删")
	if w := env.do(t, http.MethodDelete, "/api/admin/animes/"+itoa(emptyID), env.tokAd, nil); w.Code != http.StatusNoContent {
		t.Fatalf("空板块应可删（204）：got %d %s", w.Code, w.Body.String())
	}
}

// TestAnimeBoardThreadsRepliesReusePostEndpoints 覆盖验收第 1 条的后半：
// 主题串详情与回复**复用** posts 的既有端点（不新建第二套）。
//
// 这条用例的形状就是"复用"本身：它只调 /api/posts/:id 与
// /api/posts/:id/comments，一个板块专属的详情/回复路径都不存在。
func TestAnimeBoardThreadsRepliesReusePostEndpoints(t *testing.T) {
	env := newAnimeThreadEnv(t)
	animeID := env.createAnime(t, "回复复用番剧")
	threadID := env.createThread(t, animeID, "被回复的串", env.tokAu)

	// 详情：走 /api/posts/:id，响应里带 animeId（说明它确实是主题串）。
	var detail postView
	w := env.do(t, http.MethodGet, "/api/posts/"+itoa(threadID), "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("主题串详情应 200：%d %s", w.Code, w.Body.String())
	}
	decodeJSON(t, w, &detail)
	if detail.AnimeID == nil || *detail.AnimeID != animeID {
		t.Fatalf("详情应带 animeId=%d：%+v", animeID, detail)
	}

	// ⚠ 这里必须真的跨过一秒：handler 的 timeLayout 是 RFC3339 到**秒**，
	// 而"发帖"与"回复"在同一次测试里相隔几十毫秒 —— 同一秒内两者格式化后完全
	// 相同，于是"lastReplyAt 有没有被推进"用字符串断言不出来（第一版就这么红了）。
	// 与其放宽断言（那会让它永远为真），不如让时间真的走过去。
	time.Sleep(1100 * time.Millisecond)

	// 回复：走 /api/posts/:id/comments。
	for _, content := range []string{"第一条回复", "第二条回复"} {
		w := env.do(t, http.MethodPost, "/api/posts/"+itoa(threadID)+"/comments", env.tokOt,
			map[string]any{"content": content})
		if w.Code != http.StatusCreated {
			t.Fatalf("回复应 201：%d %s", w.Code, w.Body.String())
		}
	}

	// 评论列表：走 /api/posts/:id/comments，两条都在。
	var comments struct {
		Items []commentView `json:"items"`
		Total int64         `json:"total"`
	}
	w = env.do(t, http.MethodGet, "/api/posts/"+itoa(threadID)+"/comments", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("评论列表应 200：%d %s", w.Code, w.Body.String())
	}
	decodeJSON(t, w, &comments)
	if comments.Total != 2 || len(comments.Items) != 2 {
		t.Fatalf("应有 2 条回复：total=%d len=%d", comments.Total, len(comments.Items))
	}

	// 回复推进了 last_reply_at：板块列表里 CommentCount=2 且 lastReplyAt != createdAt
	// —— 这是"最新回复优先"真正生效的证据（而不只是排序参数被接受）。
	var board struct {
		Items []postView `json:"items"`
	}
	w = env.do(t, http.MethodGet, "/api/animes/"+itoa(animeID)+"/posts", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("板块列表应 200：%d %s", w.Code, w.Body.String())
	}
	decodeJSON(t, w, &board)
	if len(board.Items) != 1 || board.Items[0].CommentCount != 2 {
		t.Fatalf("板块列表里应有 1 个串且回复数为 2：%+v", board.Items)
	}
	if board.Items[0].LastReplyAt == "" || board.Items[0].LastReplyAt == board.Items[0].CreatedAt {
		t.Fatalf("回复应推进 lastReplyAt：created=%s lastReply=%s",
			board.Items[0].CreatedAt, board.Items[0].LastReplyAt)
	}
}
