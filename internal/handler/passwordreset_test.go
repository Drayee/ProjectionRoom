package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/service"
	"ProjectionRoom/internal/service/auth"
	"ProjectionRoom/internal/service/mail"
	"ProjectionRoom/internal/store"
)

// —— 账号邮件链路的**协议面**验收 ——
//
// 这一组用例全部跑在**真实**的 service 之上（只把 store 换成内存实现、把发信出口
// 换成一个捕获型假实现），因此它们验的是真实的协议行为，不是对桩的断言：
//
//   - 发码接口对"命中 / 未命中 / 未验证"三条路径的**响应字节**是否逐字相同；
//   - 双维度限速（每邮箱 / 每 IP）与 503 降级；
//   - 发信是否真的在请求路径之外（用一个卡住的 sender 断言 handler 立刻返回）；
//   - 明文令牌/验证码是否泄漏到响应体或日志里。
//
// ⚠ 端点名是冻结的前端契约：
//   - `/api/auth/password/otp`（发码，**恒定 200**）
//   - `/api/auth/password/otp/reset`（凭码改密，204）
//   - **`/api/auth/verify-email`**（不是 `verify`）—— 邮件里的验证链接把用户带到
//     前端的 `/verify#token=…` 页，那一页再 POST 到这里。
//
// ⚠ `/api/auth/forgot` 与 `/api/auth/reset` 已删除（链接式改密下线）。
// 本文件保留一条回归断言钉住"它们确实不存在"，防止有人以为删漏了又加回去。

// captureSender 是 mail.Sender 的捕获实现（保留每一封发出去的邮件）。
type captureSender struct {
	mu   sync.Mutex
	msgs []mail.Outbound
	// block 非 nil 时 Send 会一直等到它被关闭（用来验"handler 不等发信"）。
	block chan struct{}
}

func (c *captureSender) Send(ctx context.Context, msg mail.Outbound) error {
	if c.block != nil {
		select {
		case <-c.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, msg)
	return nil
}

func (c *captureSender) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.msgs)
}

// lastBody 等到至少有一封邮件被投递，返回**最后一封**的正文。
//
// 等而不是直接读：投递是异步的（这正是被测的性质），刚入队就读必然读到 0 封。
func (c *captureSender) lastBody(t *testing.T) string {
	t.Helper()
	if !waitUntil(func() bool { return c.count() > 0 }) {
		t.Fatal("没有等到任何邮件")
	}
	// 再多等一拍：count 是"投递完成"的计数，读正文与它同锁，因此拿到的一定是已写好的。
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.msgs[len(c.msgs)-1].Body
}

// resetEnv 是这一组用例的完整装配。
type resetEnv struct {
	engine *gin.Engine
	cfg    *config.Config
	st     *fakeAdminStore
	sender *captureSender
	close  func()
}

// newResetEnv 组装"真实 service + 内存 store + 捕获型发信"。
//
// withMail=false 表示**邮件能力未配置**（Mail 接口为 nil）：此时发码必须 503。
// 注意这里刻意用一个 nil 接口而不是"类型化 nil 的指针"—— 后者会让 service 的
// `s.mail == nil` 判断失效（那正是 bootstrap 里那段注释在防的坑）。
func newResetEnv(t *testing.T, withMail bool, mutate func(*config.Config)) *resetEnv {
	t.Helper()

	st := newFakeAdminStore()
	signer, err := auth.NewSigner(handlerTestSecret, 15*time.Minute)
	if err != nil {
		t.Fatalf("构造 Signer 失败：%v", err)
	}
	tickets := auth.NewTicketStore(time.Minute)

	var (
		sink       service.MailSink
		sender     *captureSender
		closeQueue = func() {}
	)
	if withMail {
		sender = &captureSender{}
		mailer, cleanup := mail.NewMailer(sender, mail.Options{QueueSize: 16, SendTimeout: time.Minute})
		sink = mailer
		closeQueue = cleanup
	}

	svc, err := service.NewAccountService(st, signer, tickets, service.AccountOptions{
		BcryptCost:     auth.MinBcryptCost,
		RefreshTTL:     time.Hour,
		EmailVerifyTTL: 24 * time.Hour,
		OTPTTL:         5 * time.Minute,
		PublicBaseURL:  "https://pr.example.com",
		Mail:           sink,
	})
	if err != nil {
		t.Fatalf("构造 AccountService 失败：%v", err)
	}

	engine, cfg := accountTestRouter(t, svc, mutate)
	// 管理端路由也挂上（同一份 deps 形状）：本文件要验"管理员绑定邮箱"那条通路，
	// 而它是 PATCH /api/admin/users/:id。Hub/Rooms 等留空是安全的 ——
	// patchUser 只在封禁时才需要 Hub（且那里对 nil 有判空）。
	registerAdminRoutes(engine.Group("/api"), AdminDeps{Users: st, Accounts: svc}, cfg, svc)

	return &resetEnv{engine: engine, cfg: cfg, st: st, sender: sender, close: closeQueue}
}

// seedWithEmail 造一个账号并绑定邮箱（verified 决定它是否已验证）。
func (e *resetEnv) seedWithEmail(t *testing.T, username, email, password string, verified bool) *store.User {
	t.Helper()
	u := e.st.seed(username, store.RoleUser, store.StatusActive)
	hash, err := auth.HashPassword(password, auth.MinBcryptCost)
	if err != nil {
		t.Fatalf("哈希失败：%v", err)
	}
	e.st.mu.Lock()
	e.st.users[u.ID].PasswordHash = hash
	e.st.mu.Unlock()

	var verifiedAt *time.Time
	if verified {
		now := time.Now()
		verifiedAt = &now
	}
	if err := e.st.SetUserEmail(context.Background(), u.ID, email, verifiedAt); err != nil {
		t.Fatalf("设置邮箱失败：%v", err)
	}
	got, _ := e.st.user(u.ID)
	return &got
}

// seedVerified 是 seedWithEmail(verified=true) 的简写（旧用例的调用形态）。
func (e *resetEnv) seedVerified(t *testing.T, username, email, password string) *store.User {
	t.Helper()
	return e.seedWithEmail(t, username, email, password, true)
}

// resetRows 数一下内存 store 里某个用途的令牌行数（"内部有没有真的动手"的可观测判据）。
func (e *resetEnv) resetRows(purpose string) int {
	e.st.mu.Lock()
	defer e.st.mu.Unlock()
	n := 0
	for _, r := range e.st.resets {
		if r.Purpose == purpose {
			n++
		}
	}
	return n
}

// tokenHashSeen 检查内存库里有没有直接存着这串令牌（原文落库 = 凭据泄漏）。
func (e *resetEnv) tokenHashSeen(token string) bool {
	e.st.mu.Lock()
	defer e.st.mu.Unlock()
	for _, r := range e.st.resets {
		if r.TokenHash == token {
			return true
		}
	}
	return false
}

// —— 恒定响应（枚举防护） ——

// TestPasswordOTPResponseIsByteIdentical 是本轮**最重要**的一条判据：
// 不论邮箱是否注册、是否已验证，状态码与响应体必须逐字相同。
func TestPasswordOTPResponseIsByteIdentical(t *testing.T) {
	env := newResetEnv(t, true, nil)
	defer env.close()

	env.seedVerified(t, "alice", "alice@example.com", "abcd1234")
	env.seedWithEmail(t, "bob", "bob@example.com", "abcd1234", false)

	bodies := make([][]byte, 0, 5)
	for _, email := range []string{
		"alice@example.com",  // 已注册 + 已验证
		"bob@example.com",    // 已注册 + **未验证**（旧通道下这条路不发信）
		"nobody@example.com", // 账号不存在
		"ghost@example.com",  // 账号不存在（第二个样本，排除偶然等价）
		"drayee@qq.com",      // 账号不存在（真实域名形态）
	} {
		w := doJSON(t, env.engine, http.MethodPost, "/api/auth/password/otp",
			`{"email":"`+email+`"}`, "")
		if w.Code != http.StatusOK {
			t.Fatalf("邮箱 %s 应当恒定 200，实际 %d（body=%s）", email, w.Code, w.Body.String())
		}
		if ct := w.Header().Get("Content-Type"); ct != otpSentResponseContentType {
			t.Fatalf("Content-Type 也必须恒定：%q（路径 %s）", ct, email)
		}
		bodies = append(bodies, w.Body.Bytes())
	}
	for i := 1; i < len(bodies); i++ {
		if !bytes.Equal(bodies[0], bodies[i]) {
			t.Fatalf("响应体必须逐字相同：第 0 条=%q 第 %d 条=%q", bodies[0], i, bodies[i])
		}
	}
	if string(bodies[0]) != `{"status":"sent"}` {
		t.Fatalf("响应体形状不对：%q", bodies[0])
	}

	// 内部：**两个**存在的邮箱都发了信（未验证也发），不存在的那三个没发。
	if !waitUntil(func() bool { return env.sender.count() == 2 }) {
		t.Fatalf("应当只有 2 个存在的邮箱收到码，实际 %d 封", env.sender.count())
	}
}

// TestPasswordOTPReachesUnverifiedEmail 单独钉住本次改造的核心：
// 未验证邮箱**也**收得到码（旧 Forgot 在这里是 0 封）。
func TestPasswordOTPReachesUnverifiedEmail(t *testing.T) {
	env := newResetEnv(t, true, nil)
	defer env.close()

	env.seedWithEmail(t, "bob", "bob@example.com", "abcd1234", false)

	w := doJSON(t, env.engine, http.MethodPost, "/api/auth/password/otp", `{"email":"bob@example.com"}`, "")
	if w.Code != http.StatusOK || string(w.Body.Bytes()) != `{"status":"sent"}` {
		t.Fatalf("未验证邮箱的发码也必须恒定 200，实际 %d %q", w.Code, w.Body.String())
	}
	if !waitUntil(func() bool { return env.sender.count() == 1 }) {
		t.Fatalf("未验证邮箱必须真的收到码（这是本次改造的目的），实际 %d 封", env.sender.count())
	}
	if got := env.resetRows(store.PurposeEmailOTP); got != 1 {
		t.Fatalf("应当留下 1 行验证码，实际 %d 行", got)
	}
	// 发码**不**等于验证：email_verified_at 不该被动。
	if got := env.resetRows(store.PurposeEmailVerify); got != 0 {
		t.Fatalf("发码不该产生验证链接用途的行，实际 %d 行", got)
	}
}

// TestLegacyLinkEndpointsAreGone 钉住"链接式改密确实下线了"。
//
// 为什么要写：删端点与"忘了注册"在运行期长得一样（都 404），
// 而这条判据把"它不该存在"变成可执行的事实 —— 也防止有人日后"顺手补回去"。
func TestLegacyLinkEndpointsAreGone(t *testing.T) {
	env := newResetEnv(t, true, nil)
	defer env.close()

	for _, path := range []string{"/api/auth/forgot", "/api/auth/reset"} {
		w := doJSON(t, env.engine, http.MethodPost, path,
			`{"email":"a@b.com","token":"x","password":"abcd1234"}`, "")
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s 应当已下线（404），实际 %d（%s）", path, w.Code, w.Body.String())
		}
	}
}

// TestPasswordOTPBadEmailIs400 验参数非法的分支（与"存不存在"无关）。
func TestPasswordOTPBadEmailIs400(t *testing.T) {
	env := newResetEnv(t, true, nil)
	defer env.close()
	for _, body := range []string{
		`{"email":""}`, `{"email":"not-an-email"}`, `{"email":"a b@c.com"}`, `{`, `{"email":"a@b.com\nBcc: x@y.com"}`,
	} {
		w := doJSON(t, env.engine, http.MethodPost, "/api/auth/password/otp", body, "")
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body=%s 应当 400，实际 %d（%s）", body, w.Code, w.Body.String())
		}
	}
}

// TestPasswordOTPRateLimitedPerEmail 覆盖**每邮箱**闸门（5/邮箱/小时）。
//
// 与旧 forgot 的差别：这里只测每邮箱那条。每 IP 那条在 OTP 路径上挂的是
// otpSendLimiter（与注册共用），它已经由注册侧的用例覆盖。
func TestPasswordOTPRateLimitedPerEmail(t *testing.T) {
	env := newResetEnv(t, true, func(cfg *config.Config) {
		cfg.IPC.MailPerEmailPerHour = 5
		// 每 IP 那条开到很大，确保这里打到的只可能是每邮箱闸门。
		cfg.IPC.RegisterPerMinute = 1_000_000
		cfg.IPC.RegisterBurst = 1_000_000
	})
	defer env.close()
	env.seedVerified(t, "alice", "alice@example.com", "abcd1234")

	for i := 0; i < 5; i++ {
		w := doJSON(t, env.engine, http.MethodPost, "/api/auth/password/otp", `{"email":"alice@example.com"}`, "")
		if w.Code != http.StatusOK {
			t.Fatalf("第 %d 次应当 200，实际 %d（%s）", i+1, w.Code, w.Body.String())
		}
	}
	w := doJSON(t, env.engine, http.MethodPost, "/api/auth/password/otp", `{"email":"alice@example.com"}`, "")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("第 6 次应当 429，实际 %d", w.Code)
	}
	assertRateLimitedBody(t, w.Body.Bytes())

	// 关键：限速键是**归一化后**的邮箱，所以大小写不同也共享同一个桶
	//（否则 5 封/小时会变成 10 封/小时）。
	w = doJSON(t, env.engine, http.MethodPost, "/api/auth/password/otp", `{"email":"ALICE@Example.COM"}`, "")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("大小写不同的同一邮箱应当共享限速桶，实际 %d", w.Code)
	}
}

// assertRateLimitedBody 断言 429 的响应体形状。
func assertRateLimitedBody(t *testing.T, body []byte) {
	t.Helper()
	var got map[string]string
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("429 响应不是合法 JSON：%s", body)
	}
	if got["code"] != "RATE_LIMITED" {
		t.Fatalf("429 的错误码应当是 RATE_LIMITED，实际 %q", got["code"])
	}
	if !strings.Contains(got["error"], "验证码请求过于频繁") {
		t.Fatalf("429 的文案不对：%q", got["error"])
	}
}

// TestPasswordOTPReturns503WhenMailNotConfigured 验显式降级（与"账号能力关闭"同风格）。
func TestPasswordOTPReturns503WhenMailNotConfigured(t *testing.T) {
	env := newResetEnv(t, false, nil) // Mail = nil：邮件能力未配置
	defer env.close()
	env.seedVerified(t, "alice", "alice@example.com", "abcd1234")

	w := doJSON(t, env.engine, http.MethodPost, "/api/auth/password/otp", `{"email":"alice@example.com"}`, "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("邮件能力未配置时应当 503，实际 %d", w.Code)
	}
	// 与"邮箱是否存在"无关：未注册的邮箱拿到**同一个** 503。
	w2 := doJSON(t, env.engine, http.MethodPost, "/api/auth/password/otp", `{"email":"nobody@example.com"}`, "")
	if w2.Code != http.StatusServiceUnavailable || w2.Body.String() != w.Body.String() {
		t.Fatalf("503 也必须与存在性无关：%d %q vs %d %q", w.Code, w.Body.String(), w2.Code, w2.Body.String())
	}
	if !strings.Contains(w.Body.String(), "MAIL_UNAVAILABLE") {
		t.Fatalf("503 应当带 MAIL_UNAVAILABLE 错误码：%s", w.Body.String())
	}
}

// —— 不变量 I4：发信必须在请求路径之外 ——

// TestPasswordOTPDoesNotWaitForDelivery 用一个**卡住的** SMTP 投递器断言：
// handler 立刻返回（它只入队），绝不等 SMTP。
func TestPasswordOTPDoesNotWaitForDelivery(t *testing.T) {
	st := newFakeAdminStore()
	signer, err := auth.NewSigner(handlerTestSecret, 15*time.Minute)
	if err != nil {
		t.Fatalf("构造 Signer 失败：%v", err)
	}
	blocked := make(chan struct{})
	sender := &captureSender{block: blocked}
	mailer, cleanup := mail.NewMailer(sender, mail.Options{QueueSize: 4, SendTimeout: time.Minute})
	defer cleanup()

	svc, err := service.NewAccountService(st, signer, auth.NewTicketStore(time.Minute), service.AccountOptions{
		BcryptCost: auth.MinBcryptCost, RefreshTTL: time.Hour,
		EmailVerifyTTL: 24 * time.Hour, OTPTTL: 5 * time.Minute,
		PublicBaseURL: "https://pr.example.com", Mail: mailer,
	})
	if err != nil {
		t.Fatalf("构造 AccountService 失败：%v", err)
	}
	engine, _ := accountTestRouter(t, svc, nil)

	u := st.seed("alice", store.RoleUser, store.StatusActive)
	now := time.Now()
	if err := st.SetUserEmail(context.Background(), u.ID, "alice@example.com", &now); err != nil {
		t.Fatalf("设置邮箱失败：%v", err)
	}

	start := time.Now()
	w := doJSON(t, engine, http.MethodPost, "/api/auth/password/otp", `{"email":"alice@example.com"}`, "")
	elapsed := time.Since(start)

	if w.Code != http.StatusOK {
		t.Fatalf("应当 200，实际 %d（%s）", w.Code, w.Body.String())
	}
	// 投递被卡死的情况下，请求也必须在很短的时间内返回。
	if elapsed > 2*time.Second {
		t.Fatalf("handler 等待了投递（耗时 %s）：发信必须在请求路径之外", elapsed)
	}
	// 但邮件确实已经入队了。
	//
	// 判据用 Enqueued（**在 Enqueue 里同步自增**）而不是 QueueLen+Sending：
	// 后两者是两个瞬时 gauge，在"工作协程刚从 channel 取走、还没把 Sending 加一"
	// 那一瞬间会同时为 0 —— 那会让这条断言偶发假红（已经踩过一次）。
	// 同时断言 sender 一封都没投出去：这才是"投递被卡住"的可观测形态。
	if got := mailer.Stats().Enqueued; got < 1 {
		t.Fatalf("邮件应当已经被入队，实际 Enqueued=%d", got)
	}
	if got := sender.count(); got != 0 {
		t.Fatalf("投递被卡住时不该有邮件被投出去，实际 %d 封", got)
	}
	close(blocked)
}

// —— 凭验证码改密 ——

// TestPasswordOTPResetFlowRevokesAllSessionsAndNeverEchoesCode 是端到端的服务端验收：
// 拿验证码改密 → 旧 access/refresh 全失效 → 新口令可登录；且码不出现在响应体里。
func TestPasswordOTPResetFlowRevokesAllSessionsAndNeverEchoesCode(t *testing.T) {
	env := newResetEnv(t, true, nil)
	defer env.close()
	env.seedVerified(t, "alice", "alice@example.com", "abcd1234")

	// 登录拿到一对凭据（旧 access + 旧 refresh Cookie）。
	w := doJSON(t, env.engine, http.MethodPost, "/api/auth/login",
		`{"username":"alice","password":"abcd1234"}`, "")
	if w.Code != http.StatusOK {
		t.Fatalf("登录应当 200，实际 %d（%s）", w.Code, w.Body.String())
	}
	var sess struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &sess); err != nil {
		t.Fatalf("解析登录响应失败：%v", err)
	}
	old := findCookie(t, w, refreshCookieName)
	if old.Value == "" {
		t.Fatal("登录应当下发 refresh Cookie")
	}

	// 申请验证码 → 从**发信内容**里取 6 位数字（接口不回显它，这是唯一合法的拿法）。
	if w := doJSON(t, env.engine, http.MethodPost, "/api/auth/password/otp", `{"email":"alice@example.com"}`, ""); w.Code != http.StatusOK {
		t.Fatalf("发码应当 200，实际 %d", w.Code)
	}
	code := otpFromMailBody(t, env.sender.lastBody(t))

	// 顺带钉住"库里是 sha256、不是原文"。
	if env.tokenHashSeen(code) {
		t.Fatal("库里出现了验证码原文")
	}

	// 捕获日志：整个改密过程中都不该出现明文验证码。
	logs := captureLogs(t, func() {
		wr := doJSON(t, env.engine, http.MethodPost, "/api/auth/password/otp/reset",
			`{"email":"alice@example.com","code":"`+code+`","password":"newpass123"}`, "")
		if wr.Code != http.StatusNoContent {
			t.Fatalf("改密应当 204，实际 %d（%s）", wr.Code, wr.Body.String())
		}
	})
	if strings.Contains(logs, code) {
		t.Fatalf("明文验证码出现在了服务端日志里：\n%s", logs)
	}

	// 旧 access token 必须失效。
	w = doJSON(t, env.engine, http.MethodGet, "/api/auth/me", "", sess.AccessToken)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("改密后旧 access token 必须 401，实际 %d", w.Code)
	}
	// 旧 refresh 必须失效。
	req := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", nil)
	req.AddCookie(&http.Cookie{Name: refreshCookieName, Value: old.Value})
	w2 := httptest.NewRecorder()
	env.engine.ServeHTTP(w2, req)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("改密后旧 refresh 必须 401，实际 %d（%s）", w2.Code, w2.Body.String())
	}
	// 新口令能登录。
	w3 := doJSON(t, env.engine, http.MethodPost, "/api/auth/login",
		`{"username":"alice","password":"newpass123"}`, "")
	if w3.Code != http.StatusOK {
		t.Fatalf("新口令应当能登录，实际 %d（%s）", w3.Code, w3.Body.String())
	}

	// 单次使用：同一个码再来一次必须是 400 + OTP_INVALID。
	w4 := doJSON(t, env.engine, http.MethodPost, "/api/auth/password/otp/reset",
		`{"email":"alice@example.com","code":"`+code+`","password":"another123"}`, "")
	if w4.Code != http.StatusBadRequest || !strings.Contains(w4.Body.String(), "OTP_INVALID") {
		t.Fatalf("第二次使用同一验证码应当 400 OTP_INVALID，实际 %d %s", w4.Code, w4.Body.String())
	}
}

// TestPasswordOTPResetRejectsWeakPassword 覆盖弱口令分支（沿用注册的策略与错误码）。
func TestPasswordOTPResetRejectsWeakPassword(t *testing.T) {
	env := newResetEnv(t, true, nil)
	defer env.close()
	env.seedVerified(t, "alice", "alice@example.com", "abcd1234")

	if w := doJSON(t, env.engine, http.MethodPost, "/api/auth/password/otp", `{"email":"alice@example.com"}`, ""); w.Code != http.StatusOK {
		t.Fatalf("发码应当 200，实际 %d", w.Code)
	}
	code := otpFromMailBody(t, env.sender.lastBody(t))

	w := doJSON(t, env.engine, http.MethodPost, "/api/auth/password/otp/reset",
		`{"email":"alice@example.com","code":"`+code+`","password":"short"}`, "")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"BAD_REQUEST"`) {
		t.Fatalf("弱口令应当 400 BAD_REQUEST，实际 %d %s", w.Code, w.Body.String())
	}
	// 弱口令**不该烧掉验证码**：同一条码仍可用强口令通过。
	w = doJSON(t, env.engine, http.MethodPost, "/api/auth/password/otp/reset",
		`{"email":"alice@example.com","code":"`+code+`","password":"newpass123"}`, "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("弱口令被拒后同一条码仍然可用，实际 %d（%s）", w.Code, w.Body.String())
	}
}

// TestVerifyEmailEndpointCompletesFlow 验验证端点（**契约名 `verify-email`**）：
// 204 + 之后该邮箱可找回。
func TestVerifyEmailEndpointCompletesFlow(t *testing.T) {
	env := newResetEnv(t, true, nil)
	defer env.close()

	w := doJSON(t, env.engine, http.MethodPost, "/api/auth/register",
		`{"username":"bob","displayName":"鲍勃","password":"abcd1234","email":"bob@example.com"}`, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("注册应当 201，实际 %d（%s）", w.Code, w.Body.String())
	}
	verifyToken := tokenFromMailBody(t, env.sender.lastBody(t))

	w = doJSON(t, env.engine, http.MethodPost, "/api/auth/verify-email", `{"token":"`+verifyToken+`"}`, "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("验证应当 204，实际 %d（%s）", w.Code, w.Body.String())
	}
	// 同一个令牌再来一次：400 + VERIFY_TOKEN_INVALID（单次使用）。
	w = doJSON(t, env.engine, http.MethodPost, "/api/auth/verify-email", `{"token":"`+verifyToken+`"}`, "")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "VERIFY_TOKEN_INVALID") {
		t.Fatalf("验证令牌必须单次使用，实际 %d %s", w.Code, w.Body.String())
	}
	// 验证之后该邮箱**从此可以找回**（发得出码 + 库里有 OTP 行）。
	before := env.sender.count()
	if w := doJSON(t, env.engine, http.MethodPost, "/api/auth/password/otp", `{"email":"bob@example.com"}`, ""); w.Code != http.StatusOK {
		t.Fatalf("验证后找回应当 200，实际 %d", w.Code)
	}
	if !waitUntil(func() bool { return env.sender.count() == before+1 }) {
		t.Fatalf("验证后应当能收到验证码，实际 %d 封（期望 %d）", env.sender.count(), before+1)
	}
	if got := env.resetRows(store.PurposeEmailOTP); got != 1 {
		t.Fatalf("应当留下 1 行验证码，实际 %d 行", got)
	}
}

// TestVerifyEmailRouteNameIsTheFrozenContract 是**跨线缺陷**的回归断言：
// 客户端要打的是 `/api/auth/verify-email`，而旧路径必须**不复存在**。
//
// 为什么要专门写一条：这个不一致的症状是"用户点邮件里的链接必 404"，
// 而它在服务端单测里完全看不出来（单测打的是自己注册的路径）。
func TestVerifyEmailRouteNameIsTheFrozenContract(t *testing.T) {
	env := newResetEnv(t, true, nil)
	defer env.close()

	// 契约路径存在（204/400 都算"存在"，404 才是缺陷）。
	w := doJSON(t, env.engine, http.MethodPost, "/api/auth/verify-email", `{"token":"x"}`, "")
	if w.Code == http.StatusNotFound {
		t.Fatal("契约路径 /api/auth/verify-email 不存在：客户端点验证链接会 404")
	}
	// 旧路径**不许**留下别名（两个路径 = 两个 owner = 会被消灭的熵）。
	w = doJSON(t, env.engine, http.MethodPost, "/api/auth/verify", `{"token":"x"}`, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("/api/auth/verify 不该存在（它会让两个路径各成一个 owner），实际 %d", w.Code)
	}
}

// TestVerifyEmailRateLimitedPerIP 验 verify-email 的每 IP 闸门。
//
// 它曾经与 /api/auth/reset 共用一条桶（"不能靠拆端点把猜 token 的预算翻倍"）。
// reset 下线后这条桶只剩 verify-email 一个使用者，判据本身仍然成立：
// 超过配额必须 429。
func TestVerifyEmailRateLimitedPerIP(t *testing.T) {
	env := newResetEnv(t, true, func(cfg *config.Config) {
		cfg.IPC.ResetPerIPPerHour = 3
	})
	defer env.close()

	for i := 0; i < 3; i++ {
		w := doJSON(t, env.engine, http.MethodPost, "/api/auth/verify-email", `{"token":"x"}`, "")
		if w.Code != http.StatusBadRequest {
			t.Fatalf("第 %d 次应当 400（令牌无效），实际 %d", i+1, w.Code)
		}
	}
	// 第 4 次撞闸门。
	w := doJSON(t, env.engine, http.MethodPost, "/api/auth/verify-email", `{"token":"x"}`, "")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("第 4 次 verify-email 应当 429，实际 %d", w.Code)
	}
}

// —— 注册侧：邮箱必填与唯一 ——

func TestRegisterRequiresEmailAndReportsEmailTaken(t *testing.T) {
	env := newResetEnv(t, true, nil)
	defer env.close()

	// 缺邮箱 → 400（必须填：账号要有邮箱才可能走找回）。
	w := doJSON(t, env.engine, http.MethodPost, "/api/auth/register",
		`{"username":"alice","displayName":"爱丽丝","password":"abcd1234"}`, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("缺邮箱应当 400，实际 %d（%s）", w.Code, w.Body.String())
	}
	// 正常注册。
	w = doJSON(t, env.engine, http.MethodPost, "/api/auth/register",
		`{"username":"alice","displayName":"爱丽丝","password":"abcd1234","email":"alice@example.com"}`, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("注册应当 201，实际 %d（%s）", w.Code, w.Body.String())
	}
	// 注册响应必须带上邮箱与验证状态（客户端据此提示"去验证邮箱"）。
	if !strings.Contains(w.Body.String(), `"email":"alice@example.com"`) ||
		!strings.Contains(w.Body.String(), `"emailVerified":false`) {
		t.Fatalf("注册响应应当带 email + emailVerified:false，实际 %s", w.Body.String())
	}
	// 邮箱撞车 → 409 + EMAIL_TAKEN（与 USERNAME_TAKEN 分开：用户要采取的动作不同）。
	w = doJSON(t, env.engine, http.MethodPost, "/api/auth/register",
		`{"username":"bob","displayName":"鲍勃","password":"abcd1234","email":"ALICE@example.com"}`, "")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "EMAIL_TAKEN") {
		t.Fatalf("重复邮箱应当 409 EMAIL_TAKEN，实际 %d（%s）", w.Code, w.Body.String())
	}
	// 响应体里不该出现口令哈希（既有纪律的回归断言）。
	if strings.Contains(w.Body.String(), "passwordHash") || strings.Contains(w.Body.String(), "$2a$") {
		t.Fatalf("响应体里出现了口令哈希：%s", w.Body.String())
	}
}

// —— 管理端：绑定邮箱 ——

func TestAdminPatchBindsEmailAndAuditsOnce(t *testing.T) {
	env := newResetEnv(t, true, nil)
	defer env.close()
	admin := env.st.seed("root", store.RoleAdmin, store.StatusActive)
	target := env.st.seed("alice", store.RoleUser, store.StatusActive)

	signer, err := auth.NewSigner(handlerTestSecret, 15*time.Minute)
	if err != nil {
		t.Fatalf("构造 Signer 失败：%v", err)
	}
	token, _, err := signer.Issue(admin.ID, admin.Role, admin.TokenVersion)
	if err != nil {
		t.Fatalf("签发管理员 token 失败：%v", err)
	}
	path := "/api/admin/users/" + strconv.FormatInt(target.ID, 10)

	w := doJSON(t, env.engine, http.MethodPatch, path, `{"email":"alice@example.com"}`, token)
	if w.Code != http.StatusOK {
		t.Fatalf("绑定邮箱应当 200，实际 %d（%s）", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"emailVerified":true`) {
		t.Fatalf("管理员绑定即已验证，响应里应当体现：%s", w.Body.String())
	}

	// 审计：等异步投递到达，且**只有一条**。
	if !waitUntil(func() bool { return len(env.st.auditsOf("user.email_bind")) == 1 }) {
		t.Fatalf("应当写入一条 user.email_bind 审计，实际 %d 条", len(env.st.auditsOf("user.email_bind")))
	}
	// 同一个值再 PATCH 一次：真正的 no-op，不写第二行审计。
	w = doJSON(t, env.engine, http.MethodPatch, path, `{"email":"alice@example.com"}`, token)
	if w.Code != http.StatusOK {
		t.Fatalf("重复绑定应当 200（幂等），实际 %d", w.Code)
	}
	time.Sleep(50 * time.Millisecond)
	if got := len(env.st.auditsOf("user.email_bind")); got != 1 {
		t.Fatalf("no-op 的绑定不该写审计，实际 %d 条", got)
	}

	// 绑定之后该邮箱**立刻**可用于找回。
	before := env.sender.count()
	if w := doJSON(t, env.engine, http.MethodPost, "/api/auth/password/otp", `{"email":"alice@example.com"}`, ""); w.Code != http.StatusOK {
		t.Fatalf("绑定后找回应当 200，实际 %d", w.Code)
	}
	if !waitUntil(func() bool { return env.sender.count() == before+1 }) {
		t.Fatalf("绑定后应当能收到验证码，实际 %d 封", env.sender.count())
	}

	// 邮箱格式非法 → 400；被别的账号占用 → 409 EMAIL_TAKEN。
	w = doJSON(t, env.engine, http.MethodPatch, path, `{"email":"bad"}`, token)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法邮箱应当 400，实际 %d（%s）", w.Code, w.Body.String())
	}
	other := env.st.seed("carol", store.RoleUser, store.StatusActive)
	now := time.Now()
	if err := env.st.SetUserEmail(context.Background(), other.ID, "taken@example.com", &now); err != nil {
		t.Fatalf("预置邮箱失败：%v", err)
	}
	w = doJSON(t, env.engine, http.MethodPatch, path, `{"email":"taken@example.com"}`, token)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "EMAIL_TAKEN") {
		t.Fatalf("被占用的邮箱应当 409 EMAIL_TAKEN，实际 %d（%s）", w.Code, w.Body.String())
	}
}

// —— 工具 ——

// captureLogs 把标准 logger 的输出抓下来，跑完 fn 再恢复。
//
// log 包是全局的，因此这里**不能**与 t.Parallel 一起用（本包所有用例都是串行的）。
func captureLogs(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)
	fn()
	log.SetOutput(prev)
	return buf.String()
}

// waitUntil 轮询等待一个条件成立（异步投递的断言用它，避免固定 sleep）。
func waitUntil(cond func() bool) bool {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}

// tokenFromMailBody 从邮件正文里取出 `#token=` 后面的串。
//
// 它同时钉住了链接形态：断言里带 `#token=`，所以把令牌挪到 query 里会让这些用例变红
// （而"令牌进 query"正是需求要避免的：那会进 nginx 访问日志与 Referer）。
func tokenFromMailBody(t *testing.T, body string) string {
	t.Helper()
	const marker = "#token="
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("邮件正文里找不到 %q：\n%s", marker, body)
	}
	rest := body[i+len(marker):]
	if j := strings.IndexAny(rest, " \r\n"); j >= 0 {
		rest = rest[:j]
	}
	if rest == "" {
		t.Fatal("令牌为空")
	}
	return rest
}

// otpFromMailBody 从验证码邮件正文里取出 6 位数字。
func otpFromMailBody(t *testing.T, body string) string {
	t.Helper()
	const marker = "验证码是："
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("邮件正文里找不到 %q：\n%s", marker, body)
	}
	rest := body[i+len(marker):]
	start := -1
	for j, r := range rest {
		if r >= '0' && r <= '9' {
			if start < 0 {
				start = j
			}
			if j-start+1 == 6 {
				return rest[start : j+1]
			}
			continue
		}
		start = -1
	}
	t.Fatalf("邮件正文里找不到 6 位验证码：\n%s", body)
	return ""
}
