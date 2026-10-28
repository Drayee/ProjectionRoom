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

// TestSearchUsersReturnsRelations 覆盖验收第 1 条的 HTTP 面：排除自己、
// 中文昵称可搜、结果带关系状态、blocked 不隐藏。
func TestSearchUsersReturnsRelations(t *testing.T) {
	env := newFriendEnv(t)
	ctx := context.Background()

	// 造三种关系：
	//   alice → bob：pending（alice 发出）→ 搜索 bob 得 pending-out
	//   carol → alice：pending → 搜索 carol 得 pending-in
	//   alice 拉黑 carol：那唯一一行原地转 blocked → 搜索 carol 得 blocked
	if _, err := env.st.CreateFriendshipRequest(ctx, env.alice.ID, env.bob.ID); err != nil {
		t.Fatalf("alice→bob 请求: %v", err)
	}
	if _, err := env.st.CreateFriendshipRequest(ctx, env.carol.ID, env.alice.ID); err != nil {
		t.Fatalf("carol→alice 请求: %v", err)
	}
	if _, err := env.st.BlockUser(ctx, env.alice.ID, env.carol.ID); err != nil {
		t.Fatalf("alice 拉黑 carol: %v", err)
	}

	// 按中文昵称搜：三个昵称都含"罗"以外的共同字？不 —— 用各自的前缀分别验。
	// 先搜"鲍"：只命中 bob（鲍勃），不含 alice 自己。
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
	if res.Items[0].Relation != store.RelationPendingOut {
		t.Fatalf("bob 的关系应为 pending-out：got %q", res.Items[0].Relation)
	}
	if res.Items[0].Username != "fr-bob" || res.Items[0].DisplayName != "鲍勃" {
		t.Fatalf("结果必须带 username 与 displayName：%+v", res.Items[0])
	}

	// 搜用户名共同前缀 "fr-"：命中 bob 与 carol（以及 alice 自己，但被排除），
	// 于是正好验"排除自己"与"username 参与匹配"两件事。
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
	// carol 那一行：alice 拉黑了 carol（blocked 覆盖 pending-in），
	// 而**方向不区分** —— 搜索结果只告诉前端"不能加好友"，不告诉用户是谁拉黑了谁。
	if got := byID[env.carol.ID].Relation; got != store.RelationBlocked {
		t.Fatalf("carol 的关系应为 blocked：got %q", got)
	}

	// 空结果集是空数组而不是 null（前端对 items 直接 .map）。
	w = env.do(t, http.MethodGet, "/api/users/search?q=zzz-none", env.tokA)
	if !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatalf("空结果必须是空数组：%s", w.Body.String())
	}

	// pending-in 方向：alice 收到了 carol 的请求（carol→alice 那行被 alice 拉黑覆盖了，
	// 所以这里用**第四个人**造一条干净的 pending-in）。
	//
	// 为什么必须换人：alice 拉黑 carol 之后那一行是 blocked，pending-in 已经被覆盖 ——
	// 这正是"一段关系只存一行"的代价（一个方向的状态会盖掉另一个方向的）。
	dave := newFriendUser(t, env.st, "fr-dave", "戴夫")
	if _, err := env.st.CreateFriendshipRequest(ctx, dave.ID, env.alice.ID); err != nil {
		t.Fatalf("dave→alice 请求: %v", err)
	}
	w = env.do(t, http.MethodGet, "/api/users/search?q=dave", env.tokA)
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if len(res.Items) != 1 || res.Items[0].Relation != store.RelationPendingIn {
		t.Fatalf("alice 看 dave 应为 pending-in（请求是 dave 发出的）：%+v", res.Items)
	}

	// 对照：bob 看 alice 是 **pending-in**（请求是 alice 发出的，收到的是 bob）。
	// 这一对与上一对正好互补，把"方向按 requester 判"两个方向都钉住：
	//   - dave 发出 → alice 看到 pending-in
	//   - alice 发出 → bob 看到 pending-in
	//   （两者的观察者都是收到请求的那一方）
	w = env.do(t, http.MethodGet, "/api/users/search?q=alice", env.tokB)
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if len(res.Items) != 1 || res.Items[0].Relation != store.RelationPendingIn {
		t.Fatalf("bob 看 alice 应为 pending-in：%+v", res.Items)
	}

	// 再验观察者是**发出方**的情形：alice 搜 bob 应是 pending-out
	// （alice 自己发出的那个请求）。这与文件末尾的"鲍"搜索互为对照。
	w = env.do(t, http.MethodGet, "/api/users/search?q=bob", env.tokA)
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if len(res.Items) != 1 || res.Items[0].Relation != store.RelationPendingOut {
		t.Fatalf("alice 搜 bob 应为 pending-out：%+v", res.Items)
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

	// 正常解除：204 + 关系回到 none + 能重新发请求 + 审计投递。
	w = env.do(t, http.MethodDelete, "/api/blocks/"+strconv.FormatInt(env.bob.ID, 10), env.tokA)
	if w.Code != http.StatusNoContent {
		t.Fatalf("解除拉黑应 204：got %d body=%s", w.Code, w.Body.String())
	}
	row := friendRelationBetween(t, env.st, env.alice.ID, env.bob.ID)
	if row != nil {
		t.Fatalf("解除后那行应被删除：%+v", row)
	}
	if _, err := env.st.CreateFriendshipRequest(ctx, env.alice.ID, env.bob.ID); err != nil {
		t.Fatalf("解除后应能重新发请求：%v", err)
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
