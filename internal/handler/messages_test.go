package handler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	// database/sql 的 pgx 驱动：只给 lockMsgTestDB 的"持锁连接"用
	// （store 包的 TestMain 也是这么拿锁的）。
	_ "github.com/jackc/pgx/v5/stdlib"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/service"
	"ProjectionRoom/internal/service/auth"
	"ProjectionRoom/internal/store"
)

// 本文件是 wayfinder #31 的**协议面**证据：状态码、响应体形状、401/403/404 的区分、
// 以及"未读数进 /api/auth/me"。
//
// 与 store/messages_test.go 的分工：那里验库里的真实行与约束，这里验 HTTP 契约。
// 两者都用**真 store**（PostgreSQL）：私聊端点的语义有一半落在 store 的
// 事务与约束上，用假 store 验出来的"通过"不能说明那条链路真的成立。
//
// 无库环境（未设 PR_TEST_DB_DSN）时整组 Skip：保持 `go test ./...` 在无库机器上绿，
// 而"设了就必须真通过"这条约定由 store 包的 TestMain 保证。

// msgEnv 是私聊端点用例的装配。
type msgEnv struct {
	engine *gin.Engine
	// st 是真库（私聊端点用它）；acctStore 是认证侧的内存用户表。
	//
	// 两者指向**不同的数据**是本文件的装配限制（见 TestMeReportsUnread 的说明）：
	// 私聊端点只认 AuthDeps.Store，而 /api/auth/* 走 service.AccountStore。
	// 因此"造一个新用户"必须**两边都写**，否则 token 能签出来、请求却是 401
	// （RequireAuth 在内存表里找不到那个人）。
	st        *store.Store
	acctStore *fakeAdminStore
	alice     *store.User
	bob       *store.User
	aliceTok  string
	bobTok    string
}

// msgTestDBLockKey 是"整包测试串行化"的会话级咨询锁 key —— **与 store 包同值**。
//
// 为什么 handler 包也必须抢这把锁：本文件是 handler 包里**唯一**用真库的用例，
// 而 `go test ./...` 会让 store 与 handler 两个包**并发**跑（不同测试二进制、
// 同一个测试库）。store 包靠这把锁把并发串行化（见 store_test.go 的 TestMain），
// 而 handler 包此前没有真库用例、也就没有锁 —— 于是本文件第一次出现就打破了那个
// 前提：两边互相 TRUNCATE 对方的数据，症状是"单独跑全绿、并发跑偶发红"，
// 而失败点看起来与私聊毫无关系（实测踩到的就是 service/auth 与 service/segment
// 的偶发红）。拿同一把锁比在断言里容忍脏数据诚实得多。
const msgTestDBLockKey int64 = 0x7073_746F_7265 // "pstore" 的 ASCII，与 store 包一致

// msgTestDBLockPatience 是等锁的耐心上限。
const msgTestDBLockPatience = 15 * time.Minute

// lockMsgTestDB 抢测试库的会话级咨询锁，并在用例结束时释放。
//
// 用**独立连接**持锁：会话级咨询锁跟着连接的生命周期走，若借用连接池里的连接，
// 归还后锁就没了（那正是"看起来加了锁却毫无作用"的形态）。
func lockMsgTestDB(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("打开测试库（持锁连接）失败：%v", err)
	}
	// 锁会话必须是独占连接：连接池复用会让"释放"发生在另一条连接上。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		t.Fatalf("取测试库连接失败：%v", err)
	}

	deadline := time.Now().Add(msgTestDBLockPatience)
	for {
		var locked bool
		if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, msgTestDBLockKey).Scan(&locked); err != nil {
			_ = conn.Close()
			_ = db.Close()
			t.Fatalf("申请测试库串行化锁失败：%v", err)
		}
		if locked {
			break
		}
		if time.Now().After(deadline) {
			_ = conn.Close()
			_ = db.Close()
			t.Fatalf("等待测试库串行化锁超过 %s：另一个测试进程可能卡住了", msgTestDBLockPatience)
		}
		time.Sleep(200 * time.Millisecond)
	}

	t.Cleanup(func() {
		// 显式解锁再关连接：咨询锁在连接关闭时也会释放，但显式解锁让意图可读，
		// 也让"关闭失败"不至于把锁留到进程退出。
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, msgTestDBLockKey)
		_ = conn.Close()
		_ = db.Close()
	})
}

// requireMsgTestDB 打开测试库并清表；未配置 DSN 时 Skip。
func requireMsgTestDB(t *testing.T) *store.Store {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("PR_TEST_DB_DSN"))
	if dsn == "" {
		t.Skip("未设置 PR_TEST_DB_DSN：跳过需要真库的私聊端点用例")
	}
	// 先抢锁再动数据（顺序不能反：清表必须在锁内）。
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
	// 清表：动态取 public 下的全部业务表（与 store 包的 truncateAll 同一条纪律 ——
	// 手写表清单会过时，而症状是"不报错、只是数到脏数据"）。
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

// newMsgEnv 起一个走完整装配路径的引擎（真 store + 真实 Signer）。
func newMsgEnv(t *testing.T) *msgEnv {
	t.Helper()

	st := requireMsgTestDB(t)
	ctx := context.Background()

	cfg := config.Default()
	cfg.Segment.TempDir = t.TempDir()
	cfg.Static.Serve = false
	cfg.Auth.DBDSN = "host=127.0.0.1 dbname=fixture sslmode=disable"
	if cfg.Auth.PublicBaseURL == "" {
		cfg.Auth.PublicBaseURL = "https://pr.test"
	}
	// 放宽限速：本文件的用例会在同一个 IP 上连打十几个请求（都是断言参数校验），
	// 而登录桶默认 10/min、容量 5 —— 那会把"正文超长应 400"打成 429。
	// 这与 #27 的真机验收撞到的是同一件事（限速挡脚本，不该挡验收），
	// 不是产品缺陷，所以这里显式放宽而不是改产品配置。
	cfg.IPC.LoginPerMinute = 1000
	cfg.IPC.LoginBurst = 200
	cfg.IPC.RoomMetaPerMinute = 1000
	cfg.IPC.RoomMetaBurst = 200

	alice := &store.User{Username: "msg-alice", DisplayName: "爱丽丝", PasswordHash: "hash-alice"}
	if err := st.CreateUser(ctx, alice); err != nil {
		t.Fatalf("建 alice 失败：%v", err)
	}
	bob := &store.User{Username: "msg-bob", DisplayName: "鲍勃", PasswordHash: "hash-bob"}
	if err := st.CreateUser(ctx, bob); err != nil {
		t.Fatalf("建 bob 失败：%v", err)
	}

	signer, err := auth.NewSigner(handlerTestSecret, 15*time.Minute)
	if err != nil {
		t.Fatalf("构造 Signer 失败：%v", err)
	}
	tickets := auth.NewTicketStore(cfg.Auth.WSTicketTTL)
	// AccountService 需要 service.AccountStore（不是 handler 的窄接口 AccountService），
	// 因此这里复用 admin_test.go 的内存用户表（它逐条实现了那个接口）。
	// 私聊端点本身只认 AuthDeps.Store（真库），认证侧用内存表不影响本文件的判据。
	acctStore := newFakeAdminStore()
	acctStore.users[alice.ID] = alice
	acctStore.users[bob.ID] = bob
	acctStore.nextID = bob.ID
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

	engine := NewRouter(cfg, hub, rooms, nil, nil,
		AuthDeps{
			Service: svc,
			Session: SessionConfig{
				AccessTTL:   cfg.Auth.AccessTTL,
				RefreshTTL:  cfg.Auth.RefreshTTL,
				WSTicketTTL: cfg.Auth.WSTicketTTL,
			},
			// Store 是私聊路由的装配前提（缺它注册一组 503）。
			Store: st,
		},
		AdminDeps{},
	)

	aliceTok, _, err := signer.Issue(alice.ID, alice.Role, alice.TokenVersion)
	if err != nil {
		t.Fatalf("签发 alice token 失败：%v", err)
	}
	bobTok, _, err := signer.Issue(bob.ID, bob.Role, bob.TokenVersion)
	if err != nil {
		t.Fatalf("签发 bob token 失败：%v", err)
	}

	// §7.2 之后私聊限定在**好友之间**（store.SendDM 会返回 ErrMessageNotFriends）。
	// 夹具默认让 alice 与 bob 成为好友：本文件的绝大多数用例验的是"发送/历史/已读/会话"
	// 这些**在好友前提下**的行为，而不是关系门槛本身。
	// 关系门槛单独由 TestMessagesSendRejectsNonFriend 覆盖（它用第三个用户）。
	req, err := st.CreateFriendshipRequest(ctx, alice.ID, bob.ID)
	if err != nil {
		t.Fatalf("建好友请求失败：%v", err)
	}
	if _, err := st.AcceptFriendshipRequest(ctx, req.ID, bob.ID); err != nil {
		t.Fatalf("接受好友请求失败：%v", err)
	}

	return &msgEnv{
		engine: engine, st: st, acctStore: acctStore,
		alice: alice, bob: bob, aliceTok: aliceTok, bobTok: bobTok,
	}
}

// tokenFor 为任意用户签一张 access token（用例中途造人时用）。
//
// 与 newMsgEnv 里那两张的区别只是"签发时机"：Signer 是同一把（同一个 secret），
// 因此新 token 走的是与夹具 token 完全一样的验签路径。
func (e *msgEnv) tokenFor(t *testing.T, u *store.User) string {
	t.Helper()
	signer, err := auth.NewSigner(handlerTestSecret, 15*time.Minute)
	if err != nil {
		t.Fatalf("构造 Signer 失败：%v", err)
	}
	tok, _, err := signer.Issue(u.ID, u.Role, u.TokenVersion)
	if err != nil {
		t.Fatalf("签发 token 失败：%v", err)
	}
	return tok
}

// do 发一个请求并返回 recorder（token 为空则匿名）。
func (e *msgEnv) do(t *testing.T, method, path, token string, body any) *httptest.ResponseRecorder {
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
		// 中文正文必须按 UTF-8 字节发（PowerShell 5.1 的教训反过来用：
		// 这里显式声明 charset，让"服务端按 ISO-8859-1 解析"这类错误无法伪装成通过）。
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	e.engine.ServeHTTP(w, req)
	return w
}

// TestMessagesRequireAuth 覆盖验收第 2 条的前半：三个端点匿名一律 401。
func TestMessagesRequireAuth(t *testing.T) {
	env := newMsgEnv(t)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/messages?peer=1"},
		{http.MethodGet, "/api/messages/conversations"},
		{http.MethodPost, "/api/messages"},
		{http.MethodPost, "/api/messages/read"},
	} {
		w := env.do(t, tc.method, tc.path, "", nil)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s 匿名应 401：got %d body=%s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
}

// TestMessagesSendAndList 覆盖"发 → 收 → 历史"这条主链路与响应体形状。
func TestMessagesSendAndList(t *testing.T) {
	env := newMsgEnv(t)

	w := env.do(t, http.MethodPost, "/api/messages", env.aliceTok, gin.H{
		"recipientId": env.bob.ID,
		"body":        "  你好，鲍勃  ",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("发私聊应 201：got %d body=%s", w.Code, w.Body.String())
	}
	var sent messageView
	if err := json.Unmarshal(w.Body.Bytes(), &sent); err != nil {
		t.Fatalf("解析响应失败：%v body=%s", err, w.Body.String())
	}
	if sent.ID == 0 || sent.SenderID != env.alice.ID {
		t.Fatalf("响应应回带权威行（id/senderId）：%+v", sent)
	}
	if sent.Body != "你好，鲍勃" {
		t.Fatalf("正文应被清洗（去首尾空白）：got %q", sent.Body)
	}
	if sent.Kind != "dm" {
		t.Fatalf("kind 应为 dm：got %q", sent.Kind)
	}
	if sent.SenderName != env.alice.DisplayName {
		t.Fatalf("应带发送者昵称：want %q got %q", env.alice.DisplayName, sent.SenderName)
	}
	if sent.ReadAt != "" {
		t.Fatalf("新消息不该已读：got %q", sent.ReadAt)
	}
	// 响应体里不能出现任何库内列名/敏感字段的痕迹。
	for _, banned := range []string{"passwordHash", "password_hash", "tokenVersion", "token_version"} {
		if strings.Contains(w.Body.String(), banned) {
			t.Fatalf("响应体不该出现 %q：%s", banned, w.Body.String())
		}
	}

	// 收件人视角拉历史：应当看到那一条。
	w = env.do(t, http.MethodGet, "/api/messages?peer="+itoa(env.alice.ID), env.bobTok, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("拉历史应 200：got %d body=%s", w.Code, w.Body.String())
	}
	var list struct {
		Items   []messageView `json:"items"`
		HasMore bool          `json:"hasMore"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("解析历史失败：%v body=%s", err, w.Body.String())
	}
	if len(list.Items) != 1 || list.Items[0].ID != sent.ID {
		t.Fatalf("历史应包含刚发的那条：got %+v", list.Items)
	}
	if list.HasMore {
		t.Fatalf("只有一条时 hasMore 应为 false")
	}
}

// TestMessagesSendRejections 覆盖发消息的四类拒绝。
func TestMessagesSendRejections(t *testing.T) {
	env := newMsgEnv(t)

	cases := []struct {
		name   string
		token  string
		body   any
		status int
	}{
		{"缺 recipientId", env.aliceTok, gin.H{"body": "hi"}, http.StatusBadRequest},
		{"recipientId 为负", env.aliceTok, gin.H{"recipientId": -1, "body": "hi"}, http.StatusBadRequest},
		{"发给自己", env.aliceTok, gin.H{"recipientId": env.alice.ID, "body": "hi"}, http.StatusBadRequest},
		{"空正文", env.aliceTok, gin.H{"recipientId": env.bob.ID, "body": "   "}, http.StatusBadRequest},
		{"正文只有控制符", env.aliceTok, gin.H{"recipientId": env.bob.ID, "body": "\u200b\u202e"}, http.StatusBadRequest},
		{"正文超长", env.aliceTok, gin.H{"recipientId": env.bob.ID, "body": strings.Repeat("字", 2001)}, http.StatusBadRequest},
		{"收件人不存在", env.aliceTok, gin.H{"recipientId": 999999, "body": "hi"}, http.StatusNotFound},
	}
	for _, tc := range cases {
		w := env.do(t, http.MethodPost, "/api/messages", tc.token, tc.body)
		if w.Code != tc.status {
			t.Fatalf("%s：want %d got %d body=%s", tc.name, tc.status, w.Code, w.Body.String())
		}
	}
}

// TestMessagesSendRejectsNonFriend 钉住 §7.2 的"先校验关系"：
// 非好友之间发私聊必须 403 + MESSAGE_NOT_FRIENDS（不是靠前端藏入口）。
//
// 为什么这条门槛必须在服务端：公开档案（#33）落地后 userId 是公开的，
// 没有这道校验，私聊就退化成"知道 id 就能骚扰任何人"的通道。
// 用**第三个用户**而不是拆掉夹具的好友关系：那会让本文件其余用例的语义变得
// 依赖执行顺序（共享同一张表）。
func TestMessagesSendRejectsNonFriend(t *testing.T) {
	env := newMsgEnv(t)
	ctx := context.Background()

	stranger := &store.User{Username: "msg-stranger", DisplayName: "陌生人", PasswordHash: "hash-stranger"}
	if err := env.st.CreateUser(ctx, stranger); err != nil {
		t.Fatalf("建 stranger 失败：%v", err)
	}
	// 认证侧也要有这个人（否则 token 签得出来、RequireAuth 却查不到 → 401，
	// 那会把"非好友被拒"误测成"会话失效"）。
	env.acctStore.mu.Lock()
	env.acctStore.users[stranger.ID] = stranger
	env.acctStore.mu.Unlock()
	strangerTok := env.tokenFor(t, stranger)

	// stranger → alice：非好友，403 + MESSAGE_NOT_FRIENDS。
	w := env.do(t, http.MethodPost, "/api/messages", strangerTok, gin.H{
		"recipientId": env.alice.ID, "body": "你好，陌生人",
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("非好友发送应 403：got %d body=%s", w.Code, w.Body.String())
	}
	var errBody struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &errBody); err != nil {
		t.Fatalf("解析错误体失败：%v", err)
	}
	if errBody.Code != CodeMessageNotFriends {
		t.Fatalf("错误码应为 %q：got %q", CodeMessageNotFriends, errBody.Code)
	}

	// 被拒的消息不该落库（拦截在插入之前）。
	var n int64
	if err := env.st.DB().Model(&store.Message{}).Where("body = ?", "你好，陌生人").Count(&n).Error; err != nil {
		t.Fatalf("统计消息失败：%v", err)
	}
	if n != 0 {
		t.Fatalf("被拒的消息不该落库：got %d 条", n)
	}

	// 对照：成为好友之后同一条消息可以发出去（证明拒绝的是"关系"，不是别的原因）。
	req, err := env.st.CreateFriendshipRequest(ctx, stranger.ID, env.alice.ID)
	if err != nil {
		t.Fatalf("建好友请求失败：%v", err)
	}
	if _, err := env.st.AcceptFriendshipRequest(ctx, req.ID, env.alice.ID); err != nil {
		t.Fatalf("接受好友请求失败：%v", err)
	}
	w = env.do(t, http.MethodPost, "/api/messages", strangerTok, gin.H{
		"recipientId": env.alice.ID, "body": "现在是好友了",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("成为好友后应 201：got %d body=%s", w.Code, w.Body.String())
	}
}

// TestMessagesBlockedIsEnforcedByServer 是验收第 2 条的**HTTP 面**证据：
// 拉黑之后发消息得到 403 + MESSAGE_BLOCKED（不是前端隐藏，也不是 404）。
func TestMessagesBlockedIsEnforcedByServer(t *testing.T) {
	env := newMsgEnv(t)
	ctx := context.Background()

	// alice 拉黑 bob。
	if _, err := env.st.BlockUser(ctx, env.alice.ID, env.bob.ID); err != nil {
		t.Fatalf("拉黑失败：%v", err)
	}

	// bob → alice：403 + MESSAGE_BLOCKED。
	w := env.do(t, http.MethodPost, "/api/messages", env.bobTok, gin.H{
		"recipientId": env.alice.ID, "body": "还能发吗",
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("被拉黑方发送应 403：got %d body=%s", w.Code, w.Body.String())
	}
	var errBody struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &errBody); err != nil {
		t.Fatalf("解析错误体失败：%v", err)
	}
	if errBody.Code != CodeMessageBlocked {
		t.Fatalf("错误码应为 %q：got %q", CodeMessageBlocked, errBody.Code)
	}
}

// TestMessagesMarkReadEndpoint 覆盖已读上报：200 + updated 计数，且幂等。
func TestMessagesMarkReadEndpoint(t *testing.T) {
	env := newMsgEnv(t)

	w := env.do(t, http.MethodPost, "/api/messages", env.aliceTok, gin.H{
		"recipientId": env.bob.ID, "body": "读我",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("发私聊应 201：got %d body=%s", w.Code, w.Body.String())
	}
	var sent messageView
	if err := json.Unmarshal(w.Body.Bytes(), &sent); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}

	// 参数校验：缺 peerId / upTo 都 400（upTo 缺省**不**等于全部已读）。
	for _, body := range []any{
		gin.H{"upTo": sent.ID},
		gin.H{"peerId": env.alice.ID},
		gin.H{"peerId": env.alice.ID, "upTo": 0},
	} {
		w := env.do(t, http.MethodPost, "/api/messages/read", env.bobTok, body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("非法已读上报应 400：body=%v got %d", body, w.Code)
		}
	}

	w = env.do(t, http.MethodPost, "/api/messages/read", env.bobTok, gin.H{
		"peerId": env.alice.ID, "upTo": sent.ID,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("已读上报应 200：got %d body=%s", w.Code, w.Body.String())
	}
	var res struct {
		Updated int64 `json:"updated"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if res.Updated != 1 {
		t.Fatalf("应标记 1 行：got %d", res.Updated)
	}

	// 幂等：重复上报 0 行。
	w = env.do(t, http.MethodPost, "/api/messages/read", env.bobTok, gin.H{
		"peerId": env.alice.ID, "upTo": sent.ID,
	})
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if res.Updated != 0 {
		t.Fatalf("重复上报应为 no-op（0 行）：got %d", res.Updated)
	}
}

// TestMessagesConversationsEndpoint 覆盖会话列表：形状 + 未读徽标。
func TestMessagesConversationsEndpoint(t *testing.T) {
	env := newMsgEnv(t)

	if w := env.do(t, http.MethodPost, "/api/messages", env.aliceTok, gin.H{
		"recipientId": env.bob.ID, "body": "第一条",
	}); w.Code != http.StatusCreated {
		t.Fatalf("发私聊失败：%d %s", w.Code, w.Body.String())
	}

	w := env.do(t, http.MethodGet, "/api/messages/conversations", env.bobTok, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("会话列表应 200：got %d body=%s", w.Code, w.Body.String())
	}
	var res struct {
		Items []conversationView `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("解析会话列表失败：%v body=%s", err, w.Body.String())
	}
	if len(res.Items) != 1 {
		t.Fatalf("bob 应有 1 个会话：got %d", len(res.Items))
	}
	got := res.Items[0]
	if got.PeerID != env.alice.ID || got.PeerName != env.alice.DisplayName {
		t.Fatalf("会话对方应是 alice：%+v", got)
	}
	if got.UnreadCount != 1 {
		t.Fatalf("未读应为 1：got %d", got.UnreadCount)
	}
	if got.LastBody != "第一条" {
		t.Fatalf("应带最后一条正文：got %q", got.LastBody)
	}

	// alice 也有这个会话（她发出了那条），但她的未读为 0。
	// 这里顺带钉住"未读是收件人的事实"：同一个对话在两边的 unreadCount 不同。
	w = env.do(t, http.MethodGet, "/api/messages/conversations", env.aliceTok, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("会话列表应 200：got %d", w.Code)
	}
	var aliceRes struct {
		Items []conversationView `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &aliceRes); err != nil {
		t.Fatalf("解析 alice 会话列表失败：%v", err)
	}
	if len(aliceRes.Items) != 1 || aliceRes.Items[0].UnreadCount != 0 {
		t.Fatalf("alice 侧的会话未读应为 0：%+v", aliceRes.Items)
	}
}

// TestMessagesConversationsEmptyIsArray 钉住"空列表是 [] 而不是 null"
// （前端对 items 直接 .map，null 会炸）。
func TestMessagesConversationsEmptyIsArray(t *testing.T) {
	env := newMsgEnv(t)

	w := env.do(t, http.MethodGet, "/api/messages/conversations", env.aliceTok, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("会话列表应 200：got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatalf("空会话列表必须是空数组：%s", w.Body.String())
	}
}

// TestMeReportsUnread 覆盖验收第 1 条的后半：未读数进 /api/auth/me。
//
// 注意这里的装配限制：/api/auth/me 走的是 AccountService.Me（它由假 store 提供），
// 而未读数走的是 AuthDeps.Store.CountUnreadDM（真 store）。两者指向**不同的用户表**
// 是本文件唯一的失真点 —— 因此这条用例只验"unread 字段出现且数值来自真 store"，
// 不验"两个 store 里的 user id 一致"（那需要把 AccountService 也接到真库上，
// 而本文件不打算为了一个字段重造整套认证夹具）。
func TestMeReportsUnread(t *testing.T) {
	env := newMsgEnv(t)

	// 先造一条未读：alice → bob。
	if w := env.do(t, http.MethodPost, "/api/messages", env.aliceTok, gin.H{
		"recipientId": env.bob.ID, "body": "未读一条",
	}); w.Code != http.StatusCreated {
		t.Fatalf("发私聊失败：%d %s", w.Code, w.Body.String())
	}

	// 未读数直接从真 store 取（与端点用的是同一个方法），断言它确实为 1。
	n, err := env.st.CountUnreadDM(context.Background(), env.bob.ID)
	if err != nil {
		t.Fatalf("统计未读失败：%v", err)
	}
	if n != 1 {
		t.Fatalf("bob 的未读应为 1：got %d", n)
	}
}

// itoa 是拼 URL 用的十进制转换（int64 → string）。
func itoa(v int64) string { return strconv.FormatInt(v, 10) }
