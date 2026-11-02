package handler

// 账号设置区（票 #71）的 HTTP 层判据。
//
// 为什么这些用例用**真的** AccountService（而不是 auth_test.go 里那个
// fakeAccountService）：本票的核心判据是"改密之后谁还能用、谁不能了" ——
// 撤销会话、token_version 自增、给当前设备补发新会话，这三件事全在 service 里。
// 用一个只回固定值的假实现，判据会退化成"我调用了那个方法"，而那正是
// 最该被钉住的语义。所以这里复用 admin_test.go 的 fakeAdminStore（它是
// AccountStore 的完整内存实现）+ 真的 Signer。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/service"
	"ProjectionRoom/internal/service/auth"
	"ProjectionRoom/internal/store"
)

// settingsEnv 是一台"账号域 + 设置域都可用"的引擎。
type settingsEnv struct {
	engine *gin.Engine
	svc    *service.AccountService
	store  *settingsStore
	signer *auth.Signer
	cfg    *config.Config
}

// settingsStore 在 fakeAdminStore 之上补一份**真的会记忆**的幂等键表。
//
// 为什么必须补：admin_test.go 里那三个幂等方法都是空实现（`return nil, nil`），
// 也就是"永远当作没见过这把键"。对管理端用例无所谓，但对改密是致命的 ——
// 空实现让同键重放真的又执行了一遍改密，于是第二次拿旧口令去比对、
// 得到 PASSWORD_MISMATCH，而不是复用首次的成功响应。
// 换句话说：用空实现测幂等，测的是"没有幂等"。
type settingsStore struct {
	*fakeAdminStore
	mu   sync.Mutex
	idem map[string]*store.IdempotencyRecord
}

func newSettingsStore() *settingsStore {
	return &settingsStore{fakeAdminStore: newFakeAdminStore(), idem: map[string]*store.IdempotencyRecord{}}
}

func idemKeyOf(scope, key string) string { return scope + "\x00" + key }

func (s *settingsStore) ReserveIdempotencyKey(
	_ context.Context, scope, key, requestHash string, ttl time.Duration,
) (*store.IdempotencyRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := idemKeyOf(scope, key)
	rec, ok := s.idem[k]
	if !ok {
		s.idem[k] = &store.IdempotencyRecord{
			Scope:       scope,
			Key:         key,
			RequestHash: requestHash,
			Status:      0,
			Response:    json.RawMessage(`{}`),
			CreatedAt:   time.Now(),
			ExpiresAt:   time.Now().Add(ttl),
		}
		// 刚占住它，调用方可以执行。
		return nil, nil
	}
	if rec.RequestHash != requestHash {
		return nil, store.ErrIdempotencyKeyConflict
	}
	if rec.Status == 0 {
		return nil, store.ErrIdempotencyKeyInFlight
	}
	cp := *rec
	return &cp, nil
}

func (s *settingsStore) CompleteIdempotencyKey(_ context.Context, scope, key string, status int, response []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.idem[idemKeyOf(scope, key)]
	if !ok || rec.Status != 0 {
		// 与真 store 同口径：键不存在或已完成都不该让业务失败。
		return nil
	}
	rec.Status = status
	if len(response) == 0 {
		response = []byte("{}")
	}
	rec.Response = append(json.RawMessage(nil), response...)
	return nil
}

func (s *settingsStore) ReleaseIdempotencyKey(_ context.Context, scope, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := idemKeyOf(scope, key)
	if rec, ok := s.idem[k]; ok && rec.Status == 0 {
		delete(s.idem, k)
	}
	return nil
}

func newSettingsEnv(t *testing.T) *settingsEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	cfg := config.Default()
	cfg.Auth.DBDSN = "host=127.0.0.1 dbname=projectionroom_test sslmode=disable"
	if cfg.Auth.PublicBaseURL == "" {
		cfg.Auth.PublicBaseURL = "https://pr.test"
	}
	// 限速开到很大：本文件的用例会在同一个 IP 上连打十几个请求，
	// 而登录桶默认 10/min —— 那会把"错密码应 400"打成 429。
	cfg.IPC.LoginPerMinute, cfg.IPC.LoginBurst = 1e6, 1e6
	cfg.IPC.RegisterPerMinute, cfg.IPC.RegisterBurst = 1e6, 1e6
	cfg.IPC.RefreshPerMinute, cfg.IPC.RefreshBurst = 1e6, 1e6
	cfg.IPC.MailPerEmailPerHour = 1_000_000
	cfg.IPC.MailPerIPPerHour = 1_000_000
	cfg.IPC.ResetPerIPPerHour = 1_000_000

	signer, err := auth.NewSigner(handlerTestSecret, 15*time.Minute)
	if err != nil {
		t.Fatalf("构造 Signer 失败：%v", err)
	}
	acctStore := newSettingsStore()
	svc, err := service.NewAccountService(acctStore, signer, auth.NewTicketStore(cfg.Auth.WSTicketTTL), service.AccountOptions{
		BcryptCost:     auth.MinBcryptCost,
		RefreshTTL:     cfg.Auth.RefreshTTL,
		EmailVerifyTTL: cfg.Auth.EmailVerifyTTL,
		OTPTTL:         cfg.Auth.OTPTTL,
		PublicBaseURL:  cfg.Auth.PublicBaseURL,
	})
	if err != nil {
		t.Fatalf("构造 AccountService 失败：%v", err)
	}

	r := gin.New()
	api := r.Group("/api")
	deps := AuthDeps{
		Service: svc,
		Session: SessionConfig{
			AccessTTL:   cfg.Auth.AccessTTL,
			RefreshTTL:  cfg.Auth.RefreshTTL,
			WSTicketTTL: cfg.Auth.WSTicketTTL,
		},
		Store: nil,
	}
	registerAccountRoutes(api, deps, cfg)
	registerAccountSettingsRoutes(api, cfg, svc)

	return &settingsEnv{engine: r, svc: svc, store: acctStore, signer: signer, cfg: cfg}
}

// register 走真实注册端点建号，返回会话响应里的 access token 与 refresh Cookie。
func (e *settingsEnv) register(t *testing.T, username, password string) (access string, refreshCookie string) {
	t.Helper()
	w := doJSON(t, e.engine, http.MethodPost, "/api/auth/register",
		`{"username":"`+username+`","displayName":"`+username+`","password":"`+password+`","email":"`+username+`@example.com"}`, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("注册应当 201，实际 %d（body=%s）", w.Code, w.Body.String())
	}
	return accessFrom(t, w), refreshCookieFrom(t, w)
}

// login 走真实登录端点，返回 access token 与 refresh Cookie。
func (e *settingsEnv) login(t *testing.T, username, password string) (access string, refreshCookie string) {
	t.Helper()
	w := doJSON(t, e.engine, http.MethodPost, "/api/auth/login",
		`{"username":"`+username+`","password":"`+password+`"}`, "")
	if w.Code != http.StatusOK {
		t.Fatalf("登录应当 200，实际 %d（body=%s）", w.Code, w.Body.String())
	}
	return accessFrom(t, w), refreshCookieFrom(t, w)
}

// changePassword 发一次改密请求（可带幂等键）。
func (e *settingsEnv) changePassword(t *testing.T, access, body, idemKey string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/me/password", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if access != "" {
		req.Header.Set("Authorization", "Bearer "+access)
	}
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	req.RemoteAddr = "203.0.113.7:12345"
	w := httptest.NewRecorder()
	e.engine.ServeHTTP(w, req)
	return w
}

// refreshWith 用给定的 refresh Cookie 打一次刷新端点。
func (e *settingsEnv) refreshWith(t *testing.T, cookie string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.Header.Set("Cookie", refreshCookieName+"="+cookie)
	}
	req.RemoteAddr = "203.0.113.7:12345"
	w := httptest.NewRecorder()
	e.engine.ServeHTTP(w, req)
	return w
}

// meWith 用给定的 access token 打一次 /api/auth/me。
func (e *settingsEnv) meWith(t *testing.T, access string) *httptest.ResponseRecorder {
	t.Helper()
	return doJSON(t, e.engine, http.MethodGet, "/api/auth/me", "", access)
}

// —— 小工具 ——

func accessFrom(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析会话响应失败：%v（body=%s）", err, w.Body.String())
	}
	if body.AccessToken == "" {
		t.Fatalf("会话响应缺少 accessToken：%s", w.Body.String())
	}
	return body.AccessToken
}

func refreshCookieFrom(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == refreshCookieName && c.Value != "" {
			return c.Value
		}
	}
	t.Fatalf("响应没有下发 %s Cookie：%v", refreshCookieName, w.Result().Cookies())
	return ""
}

// —— 用例 ——

func TestChangePasswordRequiresAuth(t *testing.T) {
	env := newSettingsEnv(t)

	w := env.changePassword(t, "", `{"currentPassword":"abcd1234","newPassword":"newpass123"}`, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("匿名改密应当 401，实际 %d（body=%s）", w.Code, w.Body.String())
	}
}

func TestChangePasswordRejectsWrongCurrent(t *testing.T) {
	env := newSettingsEnv(t)
	access, _ := env.register(t, "alice", "abcd1234")

	w := env.changePassword(t, access, `{"currentPassword":"wrongpass1","newPassword":"newpass123"}`, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("当前密码不对应当 400，实际 %d（body=%s）", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), CodePasswordMismatch) {
		t.Fatalf("错误码应当是 %s，实际：%s", CodePasswordMismatch, w.Body.String())
	}
	// 会话没被动：同一枚 access token 仍然可用。
	if got := env.meWith(t, access); got.Code != http.StatusOK {
		t.Fatalf("失败的改密不该影响当前会话，/me 实际 %d", got.Code)
	}
}

func TestChangePasswordRejectsWeakNewPassword(t *testing.T) {
	env := newSettingsEnv(t)
	access, _ := env.register(t, "alice", "abcd1234")

	w := env.changePassword(t, access, `{"currentPassword":"abcd1234","newPassword":"short"}`, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("弱口令应当 400，实际 %d（body=%s）", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "至少") {
		t.Fatalf("文案应当带上策略的具体原因（含「至少」），实际：%s", w.Body.String())
	}
}

func TestChangePasswordRejectsEmptyFields(t *testing.T) {
	env := newSettingsEnv(t)
	access, _ := env.register(t, "alice", "abcd1234")

	for name, body := range map[string]string{
		"两个都空":  `{}`,
		"缺新口令":  `{"currentPassword":"abcd1234"}`,
		"新口令空白": `{"currentPassword":"abcd1234","newPassword":"   "}`,
	} {
		t.Run(name, func(t *testing.T) {
			w := env.changePassword(t, access, body, "")
			if w.Code != http.StatusBadRequest {
				t.Fatalf("%s 应当 400，实际 %d（body=%s）", name, w.Code, w.Body.String())
			}
		})
	}
}

// TestChangePasswordKicksOtherSessionsAndKeepsCurrent 是本票最重要的一条：
// 改密之后**当前设备仍然在线**（响应里带新会话 + 新 Cookie），
// 而**其他设备全部下线**（旧 access token 401、旧 refresh Cookie 刷新失败）。
func TestChangePasswordKicksOtherSessionsAndKeepsCurrent(t *testing.T) {
	env := newSettingsEnv(t)
	curAccess, curRefresh := env.register(t, "alice", "abcd1234")
	// 第二台设备：同一账号再登录一次。
	otherAccess, otherRefresh := env.login(t, "alice", "abcd1234")

	w := env.changePassword(t, curAccess, `{"currentPassword":"abcd1234","newPassword":"newpass123"}`, "")
	if w.Code != http.StatusOK {
		t.Fatalf("改密应当 200，实际 %d（body=%s）", w.Code, w.Body.String())
	}

	// ① 响应带新会话：前端据此无缝续上，不必再登录一次。
	newAccess := accessFrom(t, w)
	newRefresh := refreshCookieFrom(t, w)
	if newAccess == curAccess {
		t.Fatal("改密后应当换发新的 access token（旧的已随 token_version 失效）")
	}
	if got := env.meWith(t, newAccess); got.Code != http.StatusOK {
		t.Fatalf("改密响应里的新 access token 必须立刻可用，/me 实际 %d（body=%s）", got.Code, got.Body.String())
	}

	// ② 新 refresh Cookie 可用。
	if got := env.refreshWith(t, newRefresh); got.Code != http.StatusOK {
		t.Fatalf("改密下发的新 refresh Cookie 必须能刷新，实际 %d（body=%s）", got.Code, got.Body.String())
	}

	// ③ 旧 access token 全失效（含当前设备原来那枚）。
	for name, tok := range map[string]string{"当前设备旧 token": curAccess, "另一台设备 token": otherAccess} {
		if got := env.meWith(t, tok); got.Code != http.StatusUnauthorized {
			t.Fatalf("改密后 %s 必须失效（401），实际 %d", name, got.Code)
		}
	}

	// ④ 旧 refresh Cookie 也失效。两种失败形态都算通过：
	//    行已撤销 → 重放检测（并撤销全部会话）；行已清理 → 无效。
	for name, ck := range map[string]string{"当前设备旧 Cookie": curRefresh, "另一台设备 Cookie": otherRefresh} {
		if got := env.refreshWith(t, ck); got.Code == http.StatusOK {
			t.Fatalf("改密后 %s 不该还能刷新，实际 200（body=%s）", name, got.Body.String())
		}
	}

	// ⑤ 口令真的换了：旧口令登录失败、新口令成功。
	if got := doJSON(t, env.engine, http.MethodPost, "/api/auth/login",
		`{"username":"alice","password":"abcd1234"}`, ""); got.Code == http.StatusOK {
		t.Fatal("旧口令不该还能登录")
	}
	if got := doJSON(t, env.engine, http.MethodPost, "/api/auth/login",
		`{"username":"alice","password":"newpass123"}`, ""); got.Code != http.StatusOK {
		t.Fatalf("新口令应当能登录，实际 %d（body=%s）", got.Code, got.Body.String())
	}
}

// TestChangePasswordIsIdempotent 钉住"重发拿到首次的成功响应"。
//
// 为什么这条重要：改密是不可逆的 —— 真的跑第二遍会因为"当前密码已不是那个"
// 而 400，用户看到的是"我明明改成功了却说密码不对"。幂等键让重发复用首次结果。
//
// ⚠ 重放必须带一枚**仍然有效**的 access token：改密会把 token_version 加一，
// 于是第一次请求用的那枚在第二次时已被 RequireAuth 判为失效 —— 那是正确行为
// （谁都不能拿旧凭据继续操作），但它属于中间件层判据、与幂等无关。
// 所以这里先用新口令登录拿一枚新 token，再拿它重放同一把键。
func TestChangePasswordIsIdempotent(t *testing.T) {
	env := newSettingsEnv(t)
	access, _ := env.register(t, "alice", "abcd1234")
	const key = "0b8f3f1e-6a1f-4b1e-9a1e-2c3d4e5f6a7b"

	first := env.changePassword(t, access, `{"currentPassword":"abcd1234","newPassword":"newpass123"}`, key)
	if first.Code != http.StatusOK {
		t.Fatalf("首次改密应当 200，实际 %d（body=%s）", first.Code, first.Body.String())
	}

	// 拿新口令登录，得到一枚仍然有效的 token。
	freshAccess, _ := env.login(t, "alice", "newpass123")

	// 用同一把键重放：必须原样拿到首次的 200 与同一份响应体
	//（而不是"当前密码不对"的 400）。
	second := env.changePassword(t, freshAccess, `{"currentPassword":"abcd1234","newPassword":"newpass123"}`, key)
	if second.Code != http.StatusOK {
		t.Fatalf("同键重放应当复用首次响应（200），实际 %d（body=%s）", second.Code, second.Body.String())
	}
	if accessFrom(t, second) != accessFrom(t, first) {
		t.Fatal("同键重放应当拿到**同一份**响应体（幂等的定义）")
	}
}

// TestChangePasswordDoesNotDisturbOtpDoor 钉住"两条门互不替代"。
//
// 改密之后，忘记密码那条门（邮箱验证码）仍然完整可用 —— 它是"改密只需当前
// 密码"这条取舍的第二道防线，不能因为改过一次密码就失效。
func TestChangePasswordDoesNotDisturbOtpDoor(t *testing.T) {
	env := newSettingsEnv(t)
	access, _ := env.register(t, "alice", "abcd1234")

	if w := env.changePassword(t, access, `{"currentPassword":"abcd1234","newPassword":"newpass123"}`, ""); w.Code != http.StatusOK {
		t.Fatalf("改密应当 200，实际 %d（body=%s）", w.Code, w.Body.String())
	}

	// 发码端点仍然存在且受理（邮件能力未配置 → 503 MAIL_UNAVAILABLE 也算"存在"，
	// 关键是它**不是 404**：那条路没有因为本票的改动被摘掉）。
	w := doJSON(t, env.engine, http.MethodPost, "/api/auth/password/otp", `{"email":"alice@example.com"}`, "")
	if w.Code == http.StatusNotFound {
		t.Fatalf("验证码改密端点不该消失（本票只新增了另一条门），实际 404")
	}
}

// 编译期断言：本文件依赖的 service 方法真的存在（签名漂移时变成编译错误）。
var _ = func() any {
	var s *service.AccountService
	var _ AccountService = s
	return nil
}

// 保住 store 引用（settingsEnv 的字段类型来自它）。
var _ = store.StatusActive
