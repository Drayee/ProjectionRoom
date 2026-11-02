package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/service"
	"ProjectionRoom/internal/service/auth"
	"ProjectionRoom/internal/store"
)

// 本文件是 wayfinder #39 的**协议面**证据：三个新端点的状态码、响应体形状，
// 以及 401 / 400 / 404 三条边界（验收第 2 条）。
//
// 与 store/friendships_test.go 的分工：那里验库里的行与约束（解除后关系回到 none、
// 黑名单只列自己拉黑的），这里验 HTTP 契约（匿名 401、q 空 400、解除别人的行 404、
// 审计真的投递了）。
//
// 与 messages_test.go 同一套装配约束：用真 store（PostgreSQL，关系语义一半落在
// store 的事务上），认证侧用内存用户表。无库环境（未设 PR_TEST_DB_DSN）整组 Skip。

// frEnv 是好友域新端点的测试装配。
type frEnv struct {
	engine *gin.Engine
	st     *store.Store
	// auditWriter 是审计写入器（**持指针**，不持 rows 切片）：
	// 切片在装配时被复制过来，之后 append 不会反映到那份副本上 ——
	// 那会让"审计投递了没有"永远断言到空。
	auditWriter *recordingAuditWriter
	alice       *store.User
	bob         *store.User
	carol       *store.User
	tokA        string
	tokB        string
	tokC        string
}

// audits 返回已投递的审计行。
func (e *frEnv) audits() []store.AdminAudit { return e.auditWriter.rows }

// frTestDBLockKey 与 msgTestDBLockKey 同值（同一把测试库串行化锁）。
const frTestDBLockKey int64 = 0x7073_746F_7265

// requireFriendTestDB 打开测试库并清表；未配置 DSN 时 Skip。
func requireFriendTestDB(t *testing.T) *store.Store {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("PR_TEST_DB_DSN"))
	if dsn == "" {
		t.Skip("未设置 PR_TEST_DB_DSN：跳过需要真库的好友域端点用例")
	}
	lockMsgTestDB(t, dsn)
	st := openAndMigrate(t, dsn)
	truncateFriendTables(t, st)
	return st
}

// openAndMigrate 打开测试库并补齐迁移。
func openAndMigrate(t *testing.T, dsn string) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("连接测试库失败：%v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	return st
}

// truncateFriendTables 动态清空全部业务表（与 store 包 truncateAll 同一条纪律：
// 手写表清单会过时，而症状是"不报错、只是数到脏数据"）。
func truncateFriendTables(t *testing.T, st *store.Store) {
	t.Helper()
	var tables []string
	if err := st.DB().Raw(`SELECT tablename FROM pg_tables
		WHERE schemaname = 'public' AND tablename <> 'schema_migrations'`).Scan(&tables).Error; err != nil {
		t.Fatalf("列出业务表失败：%v", err)
	}
	if len(tables) == 0 {
		return
	}
	quoted := make([]string, 0, len(tables))
	for _, name := range tables {
		quoted = append(quoted, `"`+name+`"`)
	}
	if err := st.DB().Exec(`TRUNCATE TABLE ` + strings.Join(quoted, ", ") + ` RESTART IDENTITY CASCADE`).Error; err != nil {
		t.Fatalf("清空测试库失败：%v", err)
	}
}

// newFriendEnv 起一台"好友域可用"的引擎（真 store + 真实 Signer + 同步执行的审计假实现）。
func newFriendEnv(t *testing.T) *frEnv {
	t.Helper()

	st := requireFriendTestDB(t)

	cfg := config.Default()
	cfg.Segment.TempDir = t.TempDir()
	cfg.Static.Serve = false
	cfg.Auth.DBDSN = "host=127.0.0.1 dbname=fixture sslmode=disable"
	if cfg.Auth.PublicBaseURL == "" {
		cfg.Auth.PublicBaseURL = "https://pr.test"
	}
	// 放宽限速：本文件的用例会在同一个 IP 上连打十几个请求（多数是参数校验），
	// 而登录桶默认 10/min、容量 5 —— 那会把"q 空应 400"打成 429。
	// 与 messages_test.go 同一条理由：限速挡脚本，不该挡验收。
	cfg.IPC.LoginPerMinute = 1000
	cfg.IPC.LoginBurst = 200
	cfg.IPC.RoomMetaPerMinute = 1000
	cfg.IPC.RoomMetaBurst = 200

	alice := newFriendUser(t, st, "fr-alice", "爱丽丝")
	bob := newFriendUser(t, st, "fr-bob", "鲍勃")
	carol := newFriendUser(t, st, "fr-carol", "卡罗尔")

	signer, err := auth.NewSigner(handlerTestSecret, 15*time.Minute)
	if err != nil {
		t.Fatalf("构造 Signer 失败：%v", err)
	}
	tickets := auth.NewTicketStore(cfg.Auth.WSTicketTTL)
	acctStore := newFakeAdminStore()
	acctStore.users[alice.ID] = alice
	acctStore.users[bob.ID] = bob
	acctStore.users[carol.ID] = carol
	acctStore.nextID = carol.ID
	svc, err := service.NewAccountService(acctStore, signer, tickets, service.AccountOptions{
		RefreshTTL:     cfg.Auth.RefreshTTL,
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

	// 审计：投递即**同步执行**（与 admin_test.go 的 fakeAdminWriter 同一条取舍），
	// 于是"解除拉黑有没有投递审计"可以在同一个断言里看到。
	auditSink := &fakeAdminWriter{}
	auditWriter := &recordingAuditWriter{}
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
		AdminDeps{
			AuditSink:   auditSink,
			AuditWriter: auditWriter,
		},
	)

	return &frEnv{
		engine: engine, st: st, auditWriter: auditWriter,
		alice: alice, bob: bob, carol: carol,
		tokA: friendToken(t, signer, alice),
		tokB: friendToken(t, signer, bob),
		tokC: friendToken(t, signer, carol),
	}
}

// newFriendUser 造一个用户（displayName 用中文：验收里要确认中文昵称能被搜到）。
func newFriendUser(t *testing.T, st *store.Store, username, displayName string) *store.User {
	t.Helper()
	u := &store.User{Username: username, DisplayName: displayName, PasswordHash: "$2a$10$xx"}
	if err := st.CreateUser(context.Background(), u); err != nil {
		t.Fatalf("CreateUser(%s): %v", username, err)
	}
	return u
}

func friendToken(t *testing.T, signer *auth.Signer, u *store.User) string {
	t.Helper()
	tok, _, err := signer.Issue(u.ID, u.Role, u.TokenVersion)
	if err != nil {
		t.Fatalf("签发 token 失败：%v", err)
	}
	return tok
}

// do 发一个请求并返回 recorder（token 为空则匿名）。
func (e *frEnv) do(t *testing.T, method, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req, err := http.NewRequest(method, path, nil)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	e.engine.ServeHTTP(w, req)
	return w
}

// recordingAuditWriter 记下写进来的审计行（满足 AdminAuditWriter）。
type recordingAuditWriter struct {
	rows []store.AdminAudit
}

func (w *recordingAuditWriter) InsertAudit(ctx context.Context, a *store.AdminAudit) error {
	cp := *a
	w.rows = append(w.rows, cp)
	return nil
}

// auditsOf 返回指定 action 的审计行。
func (e *frEnv) auditsOf(action string) []store.AdminAudit {
	out := make([]store.AdminAudit, 0, len(e.audits()))
	for _, a := range e.audits() {
		if a.Action == action {
			out = append(out, a)
		}
	}
	return out
}

// TestSearchUsersRequireAuthAndValidation 覆盖验收第 2 条：匿名 401、q 空/超长 400。
func TestSearchUsersRequireAuthAndValidation(t *testing.T) {
	env := newFriendEnv(t)

	// 匿名一律 401（三个新端点都验，不只搜索）。
	if w := env.do(t, http.MethodGet, "/api/users/search?q=x", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("搜索匿名应 401：got %d body=%s", w.Code, w.Body.String())
	}
	if w := env.do(t, http.MethodGet, "/api/blocks", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("黑名单匿名应 401：got %d body=%s", w.Code, w.Body.String())
	}
	if w := env.do(t, http.MethodDelete, "/api/blocks/1", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("解除拉黑匿名应 401：got %d body=%s", w.Code, w.Body.String())
	}

	// q 缺失 / 空 / 只有空白 → 400（不做"返回全部用户"）。
	for _, q := range []string{"", " ", "%20"} {
		w := env.do(t, http.MethodGet, "/api/users/search?q="+q, env.tokA)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("q=%q 应 400：got %d body=%s", q, w.Code, w.Body.String())
		}
	}
	// 完全没有 q 参数 → 400。
	if w := env.do(t, http.MethodGet, "/api/users/search", env.tokA); w.Code != http.StatusBadRequest {
		t.Fatalf("缺 q 应 400：got %d", w.Code)
	}
	// 超长 → 400（上限 32，与 username/display_name 列长同口径）。
	long := strings.Repeat("a", maxSearchQueryRunes+1)
	if w := env.do(t, http.MethodGet, "/api/users/search?q="+long, env.tokA); w.Code != http.StatusBadRequest {
		t.Fatalf("超长 q 应 400：got %d body=%s", w.Code, w.Body.String())
	}
}

// TestSearchUsersReturnsFollowState 覆盖验收第 1 条的 HTTP 面：排除自己、
// 中文昵称可搜、结果带**关注域事实**、被拉黑的人不隐藏但标出来。
//
// 票 #59 之前这里断言的是 friendships 流水线的 `relation` 字符串
// （pending-out / pending-in / blocked）。请求流水线退役后，搜索结果的
// 关系列换成关注域的五个布尔 —— 前端拿它们直接决定给「关注 / 已关注 /
// 回关 / 发消息」哪一颗按钮，不必再自己拼。
func TestSearchUsersReturnsFollowState(t *testing.T) {
	env := newFriendEnv(t)
	ctx := context.Background()

	// 造三种关系：
	//   alice → bob：单向关注        → 搜索 bob 得 following=true
	//   carol → alice：单向关注       → 搜索 carol 得 followedBy=true
	//   alice 拉黑 carol：同时删掉 alice→carol 那条边 → 搜索 carol 得 blocked=true
	if _, _, err := env.st.CreateFollow(ctx, env.alice.ID, env.bob.ID); err != nil {
		t.Fatalf("alice→bob 关注: %v", err)
	}
	if _, _, err := env.st.CreateFollow(ctx, env.carol.ID, env.alice.ID); err != nil {
		t.Fatalf("carol→alice 关注: %v", err)
	}
	if _, err := env.st.BlockUser(ctx, env.alice.ID, env.carol.ID); err != nil {
		t.Fatalf("alice 拉黑 carol: %v", err)
	}

	// 先搜「鲍」：只命中 bob（鲍勃），不含 alice 自己。
	w := env.do(t, http.MethodGet, "/api/users/search?q="+"鲍", env.tokA)
	if w.Code != http.StatusOK {
		t.Fatalf("搜索应 200：got %d body=%s", w.Code, w.Body.String())
	}
	var res struct {
		Items []searchHitView `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("解析响应失败：%v body=%s", err, w.Body.String())
	}
	if len(res.Items) != 1 || res.Items[0].UserID != env.bob.ID {
		t.Fatalf("搜「鲍」应只命中 bob：%+v", res.Items)
	}
	bob := res.Items[0]
	if !bob.Following || bob.FollowedBy || bob.Mutual || bob.Blocked {
		t.Fatalf("alice 单向关注 bob，四个布尔应只有 following：%+v", bob)
	}
	if bob.Username != "fr-bob" || bob.DisplayName != "鲍勃" {
		t.Fatalf("结果必须带 username 与 displayName：%+v", bob)
	}

	// 搜用户名共同前缀 "fr-"：命中 bob 与 carol（alice 自己被排除），
	// 于是正好验「排除自己」与「username 参与匹配」两件事。
	w = env.do(t, http.MethodGet, "/api/users/search?q=fr-", env.tokA)
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("解析响应失败：%v body=%s", err, w.Body.String())
	}
	if len(res.Items) != 2 {
		t.Fatalf("搜「fr-」应命中 bob 与 carol（排除自己）: %+v", res.Items)
	}
	byID := map[int64]searchHitView{}
	for _, it := range res.Items {
		if it.UserID == env.alice.ID {
			t.Fatalf("搜索结果不该包含自己: %+v", res.Items)
		}
		byID[it.UserID] = it
	}
	// carol：她关注了 alice（反向边还在），而 alice 拉黑了她。
	// **方向不区分** —— 搜索只告诉前端「不能关注」，不告诉用户是谁拉黑了谁。
	carol := byID[env.carol.ID]
	if !carol.Blocked || !carol.FollowedBy || carol.Following || carol.Mutual || carol.CanDM {
		t.Fatalf("carol 应 blocked=true/followedBy=true 且其余为假：%+v", carol)
	}
	// 被拉黑的人**不隐藏**（否则「解除拉黑」就没有入口了）。
	if _, ok := byID[env.carol.ID]; !ok {
		t.Fatalf("被拉黑的人应仍出现在搜索结果里: %+v", res.Items)
	}

	// 空结果集是空数组而不是 null（前端对 items 直接 .map）。
	w = env.do(t, http.MethodGet, "/api/users/search?q=zzz-none", env.tokA)
	if !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatalf("空结果必须是空数组：%s", w.Body.String())
	}

	// 互关：alice 回关 bob 之后，bob 那一行四个布尔全真（canDm 也真）。
	if _, _, err := env.st.CreateFollow(ctx, env.bob.ID, env.alice.ID); err != nil {
		t.Fatalf("bob→alice 回关: %v", err)
	}
	w = env.do(t, http.MethodGet, "/api/users/search?q=bob", env.tokA)
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("搜 bob 应只命中一人: %+v", res.Items)
	}
	bob = res.Items[0]
	if !bob.Following || !bob.FollowedBy || !bob.Mutual || !bob.CanDM || bob.Blocked {
		t.Fatalf("互关后 bob 应 following/followedBy/mutual/canDm 全真：%+v", bob)
	}

	// 观察者换人：bob 搜 alice 也应看到互关（方向对称）。
	w = env.do(t, http.MethodGet, "/api/users/search?q=alice", env.tokB)
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if len(res.Items) != 1 || !res.Items[0].Mutual {
		t.Fatalf("bob 看 alice 也应是互关：%+v", res.Items)
	}
}

// TestBlocksListAndUnblock 覆盖验收第 1 条的后半 + 第 2 条的 404 边界：
// 黑名单只列自己拉黑的、解除后回到 none、解除别人的行 404、审计真的投递了。
func TestBlocksListAndUnblock(t *testing.T) {
	env := newFriendEnv(t)
	ctx := context.Background()

	// alice 拉黑 bob；carol 拉黑 alice（别人拉黑我）。
	if _, err := env.st.BlockUser(ctx, env.alice.ID, env.bob.ID); err != nil {
		t.Fatalf("alice 拉黑 bob: %v", err)
	}
	if _, err := env.st.BlockUser(ctx, env.carol.ID, env.alice.ID); err != nil {
		t.Fatalf("carol 拉黑 alice: %v", err)
	}

	// alice 的黑名单只有 bob（carol 拉黑她的那一行不可见）。
	w := env.do(t, http.MethodGet, "/api/blocks", env.tokA)
	if w.Code != http.StatusOK {
		t.Fatalf("黑名单应 200：got %d body=%s", w.Code, w.Body.String())
	}
	var res struct {
		Items []blockedEntryView `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("解析响应失败：%v body=%s", err, w.Body.String())
	}
	if len(res.Items) != 1 || res.Items[0].UserID != env.bob.ID {
		t.Fatalf("alice 的黑名单应只有 bob：%+v", res.Items)
	}
	if res.Items[0].DisplayName != "鲍勃" || res.Items[0].BlockedAt == "" {
		t.Fatalf("黑名单行要能认出是谁 + 带拉黑时刻：%+v", res.Items[0])
	}

	// 解除"别人拉黑我"的那一行 → 404（统一 404 避免存在性探测，与 #29 同一决定）。
	w = env.do(t, http.MethodDelete, "/api/blocks/"+strconv.FormatInt(env.carol.ID, 10), env.tokA)
	if w.Code != http.StatusNotFound {
		t.Fatalf("解除别人拉黑的行应 404：got %d body=%s", w.Code, w.Body.String())
	}
	// 那一行必须还在（服务端强制不能被绕开）。
	if blocked, by, err := env.st.BlockStatusBetween(ctx, env.carol.ID, env.alice.ID); err != nil || !blocked || by != env.carol.ID {
		t.Fatalf("carol 拉黑 alice 的行不该被改动：blocked=%v by=%d err=%v", blocked, by, err)
	}

	// 从未拉黑过的两个人之间解除 → 404（与上一支同码，"不存在"与"不是你的"不可分辨）。
	if w := env.do(t, http.MethodDelete, "/api/blocks/"+strconv.FormatInt(env.bob.ID, 10), env.tokC); w.Code != http.StatusNotFound {
		t.Fatalf("从未拉黑过的人解除应 404：got %d body=%s", w.Code, w.Body.String())
	}

	// 路径参数非正整数 → 400。
	if w := env.do(t, http.MethodDelete, "/api/blocks/abc", env.tokA); w.Code != http.StatusBadRequest {
		t.Fatalf("非法 userId 应 400：got %d", w.Code)
	}
	if w := env.do(t, http.MethodDelete, "/api/blocks/0", env.tokA); w.Code != http.StatusBadRequest {
		t.Fatalf("userId=0 应 400：got %d", w.Code)
	}

	// 正常解除：204 + 关系回到 none + 能重新关注 + 审计投递。
	w = env.do(t, http.MethodDelete, "/api/blocks/"+strconv.FormatInt(env.bob.ID, 10), env.tokA)
	if w.Code != http.StatusNoContent {
		t.Fatalf("解除拉黑应 204：got %d body=%s", w.Code, w.Body.String())
	}
	row := friendRelationBetween(t, env.st, env.alice.ID, env.bob.ID)
	if row != nil {
		t.Fatalf("解除后那行应被删除：%+v", row)
	}
	if _, _, err := env.st.CreateFollow(ctx, env.alice.ID, env.bob.ID); err != nil {
		t.Fatalf("解除后应能重新关注：%v", err)
	}

	// 审计：解除拉黑必须留下"谁解除了对谁的拉黑"。
	rows := env.auditsOf("friend.unblock")
	if len(rows) != 1 {
		t.Fatalf("应投递 1 条 friend.unblock 审计：%+v", env.audits())
	}
	if rows[0].ActorID != env.alice.ID || rows[0].TargetID != strconv.FormatInt(env.bob.ID, 10) {
		t.Fatalf("审计的 actor/target 不对：%+v", rows[0])
	}
	if rows[0].TargetType != "user" {
		t.Fatalf("审计 targetType 应为 user：%q", rows[0].TargetType)
	}

	// 幂等：重复解除 404，且**不**再投递第二条审计（没有发生状态变更）。
	if w := env.do(t, http.MethodDelete, "/api/blocks/"+strconv.FormatInt(env.bob.ID, 10), env.tokA); w.Code != http.StatusNotFound {
		t.Fatalf("重复解除应 404：got %d", w.Code)
	}
	if n := len(env.auditsOf("friend.unblock")); n != 1 {
		t.Fatalf("重复解除不该再投递审计：got %d 条", n)
	}
}

// TestRetiredFriendRequestEndpointsAreGone 钉住票 #59 的核心交付：
// 好友请求流水线的五条端点**真的不在了**，而且不是静默消失。
//
// 为什么"非静默"值得单独一条判据：这些端点若被改成返回 200 空体，
// 前端会把"操作成功"演给用户看（点了没反应但也没报错），
// 这正是本轮要消灭的那类缺陷。所以断言三件事：
//  1. 状态码是 404（不是 200/204/410/500）；
//  2. 响应体**非空**（有一个可读的错误说明）；
//  3. 带合法 token 也一样 404 —— 不是"未登录被拦"的伪装。
//
// 生产态（PR_SERVE_STATIC=1）下由 NoRoute → isReservedPath 给出
// 「未找到该资源」；本用例的装配里静态托管关着，落到 gin 自己的 404 兜底，
// 两者都是 404 + 非空体，判据对两种装配同时成立。
func TestRetiredFriendRequestEndpointsAreGone(t *testing.T) {
	env := newFriendEnv(t)

	cases := []struct {
		method string
		path   string
		why    string
	}{
		{http.MethodGet, "/api/friends", "好友列表（好友 = 互关，走 /api/follows?tab=mutual）"},
		{http.MethodGet, "/api/friends/requests", "请求列表"},
		{http.MethodPost, "/api/friends/requests", "发起请求（关注即请求）"},
		{http.MethodPost, "/api/friends/requests/1/accept", "接受请求"},
		{http.MethodPost, "/api/friends/requests/1/reject", "拒绝请求"},
		{http.MethodDelete, "/api/friends/1", "删好友（取关即可）"},
	}
	for _, c := range cases {
		w := env.do(t, c.method, c.path, env.tokA)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s %s（%s）应 404，实际 %d body=%s", c.method, c.path, c.why, w.Code, w.Body.String())
		}
		if strings.TrimSpace(w.Body.String()) == "" {
			t.Fatalf("%s %s（%s）404 时不该是空体：静默失败无法归因", c.method, c.path, c.why)
		}
	}

	// 对照：替代它们的关注域端点是活的（否则"下线"就成了"功能没了"）。
	if w := env.do(t, http.MethodGet, "/api/follows?tab=mutual", env.tokA); w.Code != http.StatusOK {
		t.Fatalf("互关名单端点应仍然可用：got %d body=%s", w.Code, w.Body.String())
	}
	if w := env.do(t, http.MethodGet, "/api/blocks", env.tokA); w.Code != http.StatusOK {
		t.Fatalf("黑名单端点应仍然可用：got %d body=%s", w.Code, w.Body.String())
	}
}

// friendRelationBetween 返回 a 与 b 之间那唯一的一行（任一方向），没有则 nil。
func friendRelationBetween(t *testing.T, st *store.Store, a, b int64) *store.Friendship {
	t.Helper()
	var f store.Friendship
	err := st.DB().WithContext(context.Background()).
		Where(`(requester_id = ? AND addressee_id = ?) OR (requester_id = ? AND addressee_id = ?)`, a, b, b, a).
		Take(&f).Error
	if err != nil {
		if strings.Contains(err.Error(), "record not found") {
			return nil
		}
		t.Fatalf("查关系失败：%v", err)
	}
	return &f
}
