package handler

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/model"
	"ProjectionRoom/internal/service"
	"ProjectionRoom/internal/service/auth"
	"ProjectionRoom/internal/service/limiter"
	"ProjectionRoom/internal/store"
)

// —— 假 service（handler 只看得见 AccountService 这个窄接口）。
//
// 为什么 handler 的用例要自己造一个假实现而不是起真 store：
// 本文件要验的是**协议面**（状态码、Cookie 属性、响应体里有没有不该有的字段），
// 那与 SQL 无关；而 401/403/429 这些判据必须能在没有 PostgreSQL 的机器上跑。
// access token 用的是**真实** auth.Signer：它把"401 是因为签名/过期"这条链路
// 保持在真实实现上（唯一被替换掉的是"去数据库查用户"这一步）。

const handlerTestSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type fakeAccountService struct {
	mu        sync.Mutex
	signer    *auth.Signer
	users     map[int64]*store.User
	byName    map[string]int64
	sessions  map[string]*store.Session // token_hash → 行
	nextID    int64
	audits    int
	refreshOK bool // 为 false 时 Refresh 返回固定错误（用例注入）
	refreshEr error
}

func newFakeAccountService(t *testing.T) *fakeAccountService {
	t.Helper()
	signer, err := auth.NewSigner(handlerTestSecret, 15*time.Minute)
	if err != nil {
		t.Fatalf("构造 Signer 失败：%v", err)
	}
	return &fakeAccountService{
		signer: signer,
		users:  map[int64]*store.User{},
		byName: map[string]int64{},
		// 用一个显式标志而不是"map 是否为空"：Refresh 的正常路径与注入路径要能区分。
		sessions: map[string]*store.Session{},
	}
}

// issueAccess 用真实 Signer 签发 access token（附带一条会话行）。
func (f *fakeAccountService) issueAccess(u *store.User) (token string, hash string, err error) {
	tok, _, err := f.signer.Issue(u.ID, u.Role, u.TokenVersion)
	if err != nil {
		return "", "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	h := service.HashRefreshToken(base64.RawURLEncoding.EncodeToString(raw))
	f.sessions[h] = &store.Session{
		ID:        int64(len(f.sessions) + 1),
		UserID:    u.ID,
		TokenHash: h,
		IssuedAt:  time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
	}
	return tok, h, nil
}

// newSession 为主播…… 不：为**已有的**用户签发一对凭据，并写入一条新的会话行。
//
// inheritToken 为空表示"新建登录会话"（自己生成 refresh 原文）；
// 非空表示"轮换"——沿用调用方已经写进 sessions 的那一行（旧行由调用方撤销），
// 这样 Refresh 的语义才是"用旧 Cookie 换新 Cookie"，而不是"凭空多发一条会话"。
func (f *fakeAccountService) newSession(u *store.User, inheritToken string) (*service.Session, error) {
	access, _, err := f.issueAccess(u)
	if err != nil {
		return nil, err
	}
	token := inheritToken
	if token == "" {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		token = base64.RawURLEncoding.EncodeToString(raw)
		f.sessions[service.HashRefreshToken(token)] = &store.Session{
			ID:        int64(len(f.sessions) + 1),
			UserID:    u.ID,
			TokenHash: service.HashRefreshToken(token),
			IssuedAt:  time.Now(),
			ExpiresAt: time.Now().Add(time.Hour),
		}
	}
	return &service.Session{
		User:             service.ProfileOf(u),
		AccessToken:      access,
		AccessExpiresAt:  time.Now().Add(15 * time.Minute),
		TokenVersion:     u.TokenVersion,
		RefreshToken:     token,
		RefreshExpiresAt: time.Now().Add(time.Hour),
	}, nil
}

func (f *fakeAccountService) Register(ctx context.Context, username, displayName, password, ip, ua string) (*service.Session, error) {
	if err := auth.ValidatePasswordPolicy(password); err != nil {
		return nil, err
	}
	name, err := service.CanonicalUsername(username)
	if err != nil {
		return nil, err
	}
	display, err := service.SanitizeDisplayName(displayName, name)
	if err != nil {
		return nil, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.byName[name]; ok {
		return nil, service.ErrUsernameTaken
	}
	f.nextID++
	u := &store.User{
		ID: f.nextID, Username: name, DisplayName: display,
		// PasswordHash 故意放进内存：它绝不能出现在任何响应体里。
		PasswordHash: password,
		Role:         store.RoleUser, Status: store.StatusActive, TokenVersion: 1,
		CreatedAt: time.Now(),
	}
	f.users[u.ID] = u
	f.byName[name] = u.ID
	return f.newSession(u, "")
}

func (f *fakeAccountService) Login(ctx context.Context, username, password, ip, ua string) (*service.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.byName[strings.ToLower(strings.TrimSpace(username))]
	if !ok {
		return nil, service.ErrInvalidCredentials
	}
	u := f.users[id]
	if u.PasswordHash != password {
		return nil, service.ErrInvalidCredentials
	}
	if u.Status != store.StatusActive {
		return nil, service.ErrUserBanned
	}
	return f.newSession(u, "")
}

func (f *fakeAccountService) Refresh(ctx context.Context, refreshToken, ip, ua string) (*service.Session, error) {
	if f.refreshEr != nil {
		return nil, f.refreshEr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	sess, ok := f.sessions[service.HashRefreshToken(refreshToken)]
	if !ok || sess.RevokedAt != nil {
		// 已撤销 → 重放（与 service 的语义一致）。
		if ok && sess.RevokedAt != nil {
			return nil, service.ErrRefreshReplay
		}
		return nil, service.ErrSessionInvalid
	}
	u, ok := f.users[sess.UserID]
	if !ok {
		return nil, service.ErrSessionInvalid
	}

	// 轮换：旧行置撤销 + 新行（新 token）继承同一个用户。
	now := time.Now()
	sess.RevokedAt = &now
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	newToken := base64.RawURLEncoding.EncodeToString(raw)
	f.sessions[service.HashRefreshToken(newToken)] = &store.Session{
		ID:        int64(len(f.sessions) + 1),
		UserID:    u.ID,
		TokenHash: service.HashRefreshToken(newToken),
		IssuedAt:  now,
		ExpiresAt: now.Add(time.Hour),
	}
	return f.newSession(u, newToken)
}

func (f *fakeAccountService) Logout(ctx context.Context, refreshToken string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if sess, ok := f.sessions[service.HashRefreshToken(refreshToken)]; ok {
		now := time.Now()
		sess.RevokedAt = &now
	}
	return nil
}

func (f *fakeAccountService) LogoutAll(ctx context.Context, userID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.users[userID]; ok {
		u.TokenVersion++
	}
	return nil
}

func (f *fakeAccountService) Me(ctx context.Context, userID int64) (service.Profile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[userID]
	if !ok {
		return service.Profile{}, store.ErrNotFound
	}
	return service.ProfileOf(u), nil
}

func (f *fakeAccountService) IssueWSTicket(ctx context.Context, userID int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.users[userID]; !ok {
		return "", store.ErrNotFound
	}
	return "ticket-" + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprint(userID, time.Now().UnixNano()))), nil
}

func (f *fakeAccountService) VerifyAccessToken(ctx context.Context, token string) (*store.User, error) {
	claims, err := f.signer.Verify(token)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[claims.UserID]
	if !ok {
		return nil, service.ErrSessionInvalid
	}
	if claims.TokenVersion != u.TokenVersion {
		return nil, service.ErrSessionInvalid
	}
	if u.Status != store.StatusActive {
		return nil, service.ErrUserBanned
	}
	cp := *u
	return &cp, nil
}

// —— 组装。

// accountTestRouter 起一个只挂认证路由的引擎（走真实的注册路径）。
//
// 为什么可以直接调 registerAccountRoutes：它就是生产路径（NewRouter 内部调用的同一个函数，
// 收口前那个 accountRouteBinding 包级函数值已被参数化取代），
// 而它需要的两个外部条件（已配置 DBDSN 的 cfg、带 /api 分组的 gin 引擎）在测试里都能直接造。
// 这样 401/403/429/404 与 Cookie 属性这些判据都是**真实协议面**，不是对桩的断言。
func accountTestRouter(t *testing.T, svc AccountService, mutate func(*config.Config)) (*gin.Engine, *config.Config) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	cfg := config.Default()
	// 只要非空即代表"账号能力已开启"（真实装配下它是 PostgreSQL 连接串）。
	cfg.Auth.DBDSN = "host=127.0.0.1 dbname=projectionroom_test sslmode=disable"
	// 把三条限速闸门开到很大：本文件里只有专门的限速用例才关心它们，
	// 其余用例不应该因为"同一来源 IP 发了几次请求"而随机拿到 429。
	cfg.IPC.LoginPerMinute, cfg.IPC.LoginBurst = 1e6, 1e6
	cfg.IPC.RegisterPerMinute, cfg.IPC.RegisterBurst = 1e6, 1e6
	cfg.IPC.RefreshPerMinute, cfg.IPC.RefreshBurst = 1e6, 1e6
	if mutate != nil {
		mutate(cfg)
	}

	r := gin.New()
	api := r.Group("/api")
	registerAccountRoutes(api, AuthDeps{
		Service: svc,
		Session: SessionConfig{
			AccessTTL:   cfg.Auth.AccessTTL,
			RefreshTTL:  cfg.Auth.RefreshTTL,
			WSTicketTTL: cfg.Auth.WSTicketTTL,
		},
	}, cfg)
	return r, cfg
}

// doJSON 发一个 JSON 请求并返回响应。
func doJSON(t *testing.T, r *gin.Engine, method, path, body, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *strings.Reader
	if body == "" {
		rdr = strings.NewReader("")
	} else {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	req.RemoteAddr = "203.0.113.7:12345"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// seeded 预置一个账号，返回它的 id 与 password_hash。
func (f *fakeAccountService) seeded(username, passwordHash, role, status string) *store.User {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	u := &store.User{
		ID: f.nextID, Username: strings.ToLower(username), DisplayName: username,
		PasswordHash: passwordHash, Role: role, Status: status, TokenVersion: 1,
		CreatedAt: time.Now(),
	}
	f.users[u.ID] = u
	f.byName[u.Username] = u.ID
	return u
}

// ban 把账号改成 banned（管理端封禁路径的桩：只改状态，version 自增与本文件无关）。
//
// 它被 T8 的"已封禁账号的票据"用例使用：那条用例要的是"取票之后账号被封禁"
// 这个顺序，而不是封禁本身的实现（封禁实现由 service 的用例覆盖）。
func (f *fakeAccountService) ban(id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.users[id]; ok {
		u.Status = store.StatusBanned
	}
}

// —— 用例：注册 / 登录 / 刷新 ——

func TestAuthRegisterSucceedsWithoutLeakingHash(t *testing.T) {
	svc := newFakeAccountService(t)
	r, _ := accountTestRouter(t, svc, nil)

	w := doJSON(t, r, http.MethodPost, "/api/auth/register",
		`{"username":"Alice","displayName":"爱丽丝","password":"abcd1234"}`, "")

	if w.Code != http.StatusCreated {
		t.Fatalf("注册应当 201，实际 %d（body=%s）", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, "PasswordHash") || strings.Contains(body, "passwordHash") ||
		strings.Contains(body, "abcd1234") {
		t.Fatalf("响应体里不得出现口令哈希或明文口令：%s", body)
	}
	if !strings.Contains(body, "accessToken") {
		t.Fatalf("响应体应当带 accessToken：%s", body)
	}
	if strings.Contains(body, "pr_refresh=") || strings.Contains(body, "RefreshToken") {
		t.Fatalf("refresh token 只能走 Cookie，不得进响应体：%s", body)
	}

	// 刷新凭据必须在 Cookie 上，且属性齐全。
	cookie := findCookie(t, w, refreshCookieName)
	if cookie.Value == "" {
		t.Fatal("注册必须下发 refresh Cookie")
	}
	assertRefreshCookieAttrs(t, cookie)
}

func TestAuthLoginUnauthorizedAndBanned(t *testing.T) {
	svc := newFakeAccountService(t)
	svc.seeded("alice", "stored-passw0rd", store.RoleUser, store.StatusActive)
	svc.seeded("banned", "stored-passw0rd", store.RoleUser, store.StatusBanned)
	r, _ := accountTestRouter(t, svc, nil)

	t.Run("口令错误", func(t *testing.T) {
		w := doJSON(t, r, http.MethodPost, "/api/auth/login",
			`{"username":"alice","password":"wrongpass"}`, "")
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("应当 401，实际 %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), CodeLoginFailed) {
			t.Fatalf("错误码应当是 %s：%s", CodeLoginFailed, w.Body.String())
		}
	})

	t.Run("账号不存在文案与口令错一致", func(t *testing.T) {
		w := doJSON(t, r, http.MethodPost, "/api/auth/login",
			`{"username":"nobody","password":"wrongpass"}`, "")
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("应当 401，实际 %d", w.Code)
		}
		// 两条路径的响应体必须**逐字相同**（否则就是一条账号枚举通道）。
		other := doJSON(t, r, http.MethodPost, "/api/auth/login",
			`{"username":"alice","password":"wrongpass"}`, "")
		if w.Body.String() != other.Body.String() {
			t.Fatalf("两种失败的响应体必须一致：\n missing=%s\n wrong=%s", w.Body.String(), other.Body.String())
		}
	})

	t.Run("封禁账号", func(t *testing.T) {
		w := doJSON(t, r, http.MethodPost, "/api/auth/login",
			`{"username":"banned","password":"stored-passw0rd"}`, "")
		if w.Code != http.StatusForbidden {
			t.Fatalf("封禁应当 403，实际 %d（%s）", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), CodeUserBanned) {
			t.Fatalf("错误码应当是 %s：%s", CodeUserBanned, w.Body.String())
		}
	})

	t.Run("成功", func(t *testing.T) {
		w := doJSON(t, r, http.MethodPost, "/api/auth/login",
			`{"username":"alice","password":"stored-passw0rd"}`, "")
		if w.Code != http.StatusOK {
			t.Fatalf("应当 200，实际 %d（%s）", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "stored-passw0rd") {
			t.Fatalf("响应体泄漏了口令哈希：%s", w.Body.String())
		}
		assertRefreshCookieAttrs(t, findCookie(t, w, refreshCookieName))
	})

	t.Run("非法请求体", func(t *testing.T) {
		w := doJSON(t, r, http.MethodPost, "/api/auth/login", `not json`, "")
		if w.Code != http.StatusBadRequest {
			t.Fatalf("应当 400，实际 %d", w.Code)
		}
	})
}

// TestAuthRefreshRotatesCookieAndDetectsReplay 覆盖 T7 最关键的协议面：
// 刷新下发新 Cookie、旧 Cookie 重放返回 401 + REFRESH_REPLAY 且清掉浏览器上的凭据。
func TestAuthRefreshRotatesCookieAndDetectsReplay(t *testing.T) {
	svc := newFakeAccountService(t)
	svc.seeded("alice", "stored-passw0rd", store.RoleUser, store.StatusActive)
	r, _ := accountTestRouter(t, svc, nil)

	login := doJSON(t, r, http.MethodPost, "/api/auth/login",
		`{"username":"alice","password":"stored-passw0rd"}`, "")
	old := findCookie(t, login, refreshCookieName)
	if old.Value == "" {
		t.Fatal("登录必须下发 refresh Cookie")
	}

	// 第一次刷新：拿到新 Cookie。
	req := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", nil)
	req.AddCookie(&http.Cookie{Name: refreshCookieName, Value: old.Value})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("刷新应当 200，实际 %d（%s）", w.Code, w.Body.String())
	}
	rotated := findCookie(t, w, refreshCookieName)
	assertRefreshCookieAttrs(t, rotated)
	if rotated.Value == old.Value {
		t.Fatal("刷新必须轮换 Cookie 值")
	}

	// 再用旧 Cookie：重放 → 401 + REFRESH_REPLAY + 清 Cookie。
	req2 := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", nil)
	req2.AddCookie(&http.Cookie{Name: refreshCookieName, Value: old.Value})
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("重放应当 401，实际 %d", w2.Code)
	}
	if !strings.Contains(w2.Body.String(), CodeRefreshReplay) {
		t.Fatalf("错误码应当是 %s：%s", CodeRefreshReplay, w2.Body.String())
	}
	if cleared := findCookie(t, w2, refreshCookieName); cleared.Value != "" || cleared.MaxAge >= 0 {
		t.Fatalf("重放处置必须清掉浏览器上的 refresh Cookie，实际 value=%q maxAge=%d",
			cleared.Value, cleared.MaxAge)
	}

	// 没有 Cookie 的刷新：401 + 清 Cookie（不泄漏任何细节）。
	w3 := doJSON(t, r, http.MethodPost, "/api/auth/refresh", "", "")
	if w3.Code != http.StatusUnauthorized {
		t.Fatalf("缺少 Cookie 应当 401，实际 %d", w3.Code)
	}
}

// TestAuthRefreshQueryParamIsNotAccepted：refresh **只走 Cookie**。
// 查询串/请求体里的凭据一律不认（否则它会被写进反代 access log）。
func TestAuthRefreshQueryParamIsNotAccepted(t *testing.T) {
	svc := newFakeAccountService(t)
	svc.seeded("alice", "stored-passw0rd", store.RoleUser, store.StatusActive)
	r, _ := accountTestRouter(t, svc, nil)

	login := doJSON(t, r, http.MethodPost, "/api/auth/login",
		`{"username":"alice","password":"stored-passw0rd"}`, "")
	token := findCookie(t, login, refreshCookieName).Value

	w := doJSON(t, r, http.MethodPost, "/api/auth/refresh?refresh="+token, "", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("查询串里的 refresh 不得被接受，实际 %d", w.Code)
	}
}

func TestAuthLogoutIsIdempotent(t *testing.T) {
	svc := newFakeAccountService(t)
	svc.seeded("alice", "stored-passw0rd", store.RoleUser, store.StatusActive)
	r, _ := accountTestRouter(t, svc, nil)

	login := doJSON(t, r, http.MethodPost, "/api/auth/login",
		`{"username":"alice","password":"stored-passw0rd"}`, "")
	token := findCookie(t, login, refreshCookieName).Value

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
		req.AddCookie(&http.Cookie{Name: refreshCookieName, Value: token})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusNoContent {
			t.Fatalf("第 %d 次登出应当 204，实际 %d", i+1, w.Code)
		}
		if c := findCookie(t, w, refreshCookieName); c.Value != "" || c.MaxAge >= 0 {
			t.Fatalf("登出必须清 Cookie，实际 value=%q maxAge=%d", c.Value, c.MaxAge)
		}
	}

	// 没有 Cookie 的登出也必须 204（幂等）。
	w := doJSON(t, r, http.MethodPost, "/api/auth/logout", "", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("无凭据登出应当 204，实际 %d", w.Code)
	}
}

// —— 用例：中间件 ——

func TestRequireAuthRejectsMissingOrBadCredentials(t *testing.T) {
	svc := newFakeAccountService(t)
	alice := svc.seeded("alice", "stored-passw0rd", store.RoleUser, store.StatusActive)
	access, _, err := svc.issueAccess(alice)
	if err != nil {
		t.Fatalf("签发 access 失败：%v", err)
	}
	r, _ := accountTestRouter(t, svc, nil)

	t.Run("无凭据 → 401", func(t *testing.T) {
		w := doJSON(t, r, http.MethodGet, "/api/auth/me", "", "")
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("应当 401，实际 %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), CodeUnauthorized) {
			t.Fatalf("错误码应当是 %s：%s", CodeUnauthorized, w.Body.String())
		}
	})

	t.Run("查询串里的 token 不被接受 → 401", func(t *testing.T) {
		w := doJSON(t, r, http.MethodGet, "/api/auth/me?token="+access, "", "")
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("URL 传 token 必须被拒（§5：会进 access log），实际 %d", w.Code)
		}
	})

	t.Run("伪造 token → 401 SESSION_INVALID", func(t *testing.T) {
		w := doJSON(t, r, http.MethodGet, "/api/auth/me", "", "not-a-jwt")
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("应当 401，实际 %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), CodeSessionInvalid) {
			t.Fatalf("错误码应当是 %s：%s", CodeSessionInvalid, w.Body.String())
		}
	})

	t.Run("有效 token → 200 且不带哈希", func(t *testing.T) {
		w := doJSON(t, r, http.MethodGet, "/api/auth/me", "", access)
		if w.Code != http.StatusOK {
			t.Fatalf("应当 200，实际 %d（%s）", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "stored-passw0rd") || strings.Contains(w.Body.String(), "Hash") {
			t.Fatalf("me 响应不得包含口令哈希：%s", w.Body.String())
		}
	})

	t.Run("过期 token → 401 SESSION_EXPIRED", func(t *testing.T) {
		// 用一个 TTL 极短的 Signer 签一枚已经过期的 token（真实 auth 包的过期判定）。
		short, err := auth.NewSigner(handlerTestSecret, time.Second)
		if err != nil {
			t.Fatalf("构造 Signer 失败：%v", err)
		}
		expired, _, err := short.Issue(alice.ID, alice.Role, alice.TokenVersion)
		if err != nil {
			t.Fatalf("签发失败：%v", err)
		}
		time.Sleep(1100 * time.Millisecond)

		w := doJSON(t, r, http.MethodGet, "/api/auth/me", "", expired)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("过期 token 应当 401，实际 %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), CodeSessionExpired) {
			t.Fatalf("错误码应当是 %s（前端据此静默刷新）：%s", CodeSessionExpired, w.Body.String())
		}
	})

	t.Run("token 版本落后 → 401 SESSION_INVALID", func(t *testing.T) {
		svc.mu.Lock()
		alice.TokenVersion++
		svc.mu.Unlock()
		w := doJSON(t, r, http.MethodGet, "/api/auth/me", "", access)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("版本号变化后必须 401，实际 %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), CodeSessionInvalid) {
			t.Fatalf("错误码应当是 %s：%s", CodeSessionInvalid, w.Body.String())
		}
	})
}

// TestRequireAdminEnforcesRole 覆盖"管理员 200 / 普通用户 403"这条 I3 的边界。
func TestRequireAdminEnforcesRole(t *testing.T) {
	svc := newFakeAccountService(t)
	admin := svc.seeded("root", "stored-passw0rd", store.RoleAdmin, store.StatusActive)
	user := svc.seeded("bob", "stored-passw0rd", store.RoleUser, store.StatusActive)

	adminToken, _, err := svc.issueAccess(admin)
	if err != nil {
		t.Fatalf("签发失败：%v", err)
	}
	userToken, _, err := svc.issueAccess(user)
	if err != nil {
		t.Fatalf("签发失败：%v", err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := r.Group("/api")
	api.GET("/admin/ping", RequireAuth(svc), RequireAdmin(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	if w := doJSON(t, r, http.MethodGet, "/api/admin/ping", "", adminToken); w.Code != http.StatusOK {
		t.Fatalf("管理员应当 200，实际 %d（%s）", w.Code, w.Body.String())
	}
	w := doJSON(t, r, http.MethodGet, "/api/admin/ping", "", userToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("普通用户应当 403，实际 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), CodeForbidden) {
		t.Fatalf("错误码应当是 %s：%s", CodeForbidden, w.Body.String())
	}
	if w := doJSON(t, r, http.MethodGet, "/api/admin/ping", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("无凭据应当 401，实际 %d", w.Code)
	}
}

// TestRequireAdminWithoutRequireAuthIsFailClosed：漏挂 RequireAuth 时必须是 401，
// 绝不能"没有用户信息就当成有权限"。
func TestRequireAdminWithoutRequireAuthIsFailClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/x", RequireAdmin(), func(c *gin.Context) { c.Status(http.StatusOK) })

	if w := doJSON(t, r, http.MethodGet, "/x", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("应当 fail closed（401），实际 %d", w.Code)
	}
}

// —— 用例：限速 ——

// TestAuthRateLimitReturns429 用真实的令牌桶中间件撞限速：
// 容量 2 → 前两次放行、第三次 429 + RATE_LIMITED；换一个来源 IP 应当有独立的桶。
func TestAuthRateLimitReturns429(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/x", newKeyedLimiter(0.0001, 2, "登录"), func(c *gin.Context) { c.Status(http.StatusNoContent) })

	call := func(remote string) int {
		req := httptest.NewRequest(http.MethodPost, "/x", nil)
		req.RemoteAddr = remote
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}

	// 同一个来源 IP：桶容量 2。
	if got := call("198.51.100.9:1111"); got != http.StatusNoContent {
		t.Fatalf("第 1 次应当放行，实际 %d", got)
	}
	if got := call("198.51.100.9:2222"); got != http.StatusNoContent {
		t.Fatalf("第 2 次应当放行，实际 %d", got)
	}
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.RemoteAddr = "198.51.100.9:3333"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("第 3 次应当 429，实际 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), model.CodeRateLimited) {
		t.Fatalf("错误码应当是 %s：%s", model.CodeRateLimited, w.Body.String())
	}

	// 换一个真实来源 IP：独立令牌桶（这正是"按 IP"而不是"全局"的意义）。
	if got := call("198.51.100.10:1111"); got != http.StatusNoContent {
		t.Fatalf("另一个来源 IP 应当有独立桶，实际 %d", got)
	}
}

// TestAuthLoginRouteIsRateLimited 在生产注册路径上确认 /api/auth/login 挂了限速。
func TestAuthLoginRouteIsRateLimited(t *testing.T) {
	svc := newFakeAccountService(t)
	svc.seeded("alice", "stored-passw0rd", store.RoleUser, store.StatusActive)
	r, _ := accountTestRouter(t, svc, func(cfg *config.Config) {
		cfg.IPC.LoginPerMinute = 0.001
		cfg.IPC.LoginBurst = 2
	})

	codes := make([]int, 0, 3)
	for i := 0; i < 3; i++ {
		w := doJSON(t, r, http.MethodPost, "/api/auth/login",
			`{"username":"alice","password":"stored-passw0rd"}`, "")
		codes = append(codes, w.Code)
	}
	if codes[0] != http.StatusOK || codes[1] != http.StatusOK || codes[2] != http.StatusTooManyRequests {
		t.Fatalf("期望 [200 200 429]，实际 %v", codes)
	}
}

// —— 用例：账号能力关闭 / 装配缺失 ——

// TestAuthRoutesNotRegisteredWithoutDSN：PR_DB_DSN 为空时不注册认证路由。
// 这里走的是**真实**的 NewRouter，因此验的是生产行为而不是桩。
func TestAuthRoutesNotRegisteredWithoutDSN(t *testing.T) {
	cfg := config.Default() // Auth.DBDSN 默认为空 = 账号能力关闭
	cfg.Segment.TempDir = t.TempDir()
	cfg.Static.Serve = false

	hub, cleanup, err := service.NewHub(cfg)
	if err != nil {
		t.Fatalf("构造 Hub 失败：%v", err)
	}
	rooms := service.NewManager(cfg, hub)
	t.Cleanup(func() {
		rooms.Stop()
		cleanup()
	})

	srv := httptest.NewServer(NewRouter(cfg, hub, rooms, nil, AuthDeps{}, AdminDeps{}))
	t.Cleanup(srv.Close)

	for _, path := range []string{"/api/auth/login", "/api/auth/register", "/api/auth/refresh", "/api/auth/me"} {
		resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatalf("请求 %s 失败：%v", path, err)
		}
		// 路由未注册；NoRoute 的静态回退在没有 dist 时返回 404。
		if resp.StatusCode != http.StatusNotFound {
			resp.Body.Close()
			t.Fatalf("%s 在账号能力关闭时不该存在，实际 %d", path, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// TestAuthRoutesUnavailableWhenServiceMissing：DSN 配了但服务没装配出来 → 503（而不是 500 或消失）。
func TestAuthRoutesUnavailableWhenServiceMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Default()
	cfg.Auth.DBDSN = "host=127.0.0.1 dbname=x sslmode=disable"

	r := gin.New()
	api := r.Group("/api")
	registerAccountRoutes(api, AuthDeps{}, cfg) // Service 为 nil

	w := doJSON(t, r, http.MethodPost, "/api/auth/login", `{}`, "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("应当 503，实际 %d（%s）", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), CodeAuthUnavailable) {
		t.Fatalf("错误码应当是 %s：%s", CodeAuthUnavailable, w.Body.String())
	}
}

// —— 用例：票据 ——

func TestWSTicketRequiresAuthAndReturnsTicket(t *testing.T) {
	svc := newFakeAccountService(t)
	alice := svc.seeded("alice", "stored-passw0rd", store.RoleUser, store.StatusActive)
	access, _, err := svc.issueAccess(alice)
	if err != nil {
		t.Fatalf("签发失败：%v", err)
	}
	r, _ := accountTestRouter(t, svc, nil)

	if w := doJSON(t, r, http.MethodPost, "/api/auth/ws-ticket", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("无凭据取票应当 401，实际 %d", w.Code)
	}
	w := doJSON(t, r, http.MethodPost, "/api/auth/ws-ticket", "", access)
	if w.Code != http.StatusOK {
		t.Fatalf("取票应当 200，实际 %d（%s）", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "ticket") {
		t.Fatalf("响应应当带 ticket：%s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "expiresIn") {
		t.Fatalf("响应应当带 expiresIn：%s", w.Body.String())
	}
}

func TestLogoutAllRequiresAuth(t *testing.T) {
	svc := newFakeAccountService(t)
	alice := svc.seeded("alice", "stored-passw0rd", store.RoleUser, store.StatusActive)
	access, _, err := svc.issueAccess(alice)
	if err != nil {
		t.Fatalf("签发失败：%v", err)
	}
	r, _ := accountTestRouter(t, svc, nil)

	if w := doJSON(t, r, http.MethodPost, "/api/auth/logout-all", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("无凭据应当 401，实际 %d", w.Code)
	}
	if w := doJSON(t, r, http.MethodPost, "/api/auth/logout-all", "", access); w.Code != http.StatusNoContent {
		t.Fatalf("应当 204，实际 %d", w.Code)
	}
	// 版本号已被 +1 → 旧 access token 立刻失效。
	if w := doJSON(t, r, http.MethodGet, "/api/auth/me", "", access); w.Code != http.StatusUnauthorized {
		t.Fatalf("登出全部设备后旧 token 必须失效，实际 %d", w.Code)
	}
}

// —— 小工具 ——

// findCookie 从响应的 Set-Cookie 里取指定名字的那个。
func findCookie(t *testing.T, w *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, raw := range w.Result().Cookies() {
		if raw.Name == name {
			return raw
		}
	}
	return &http.Cookie{Name: name}
}

// assertRefreshCookieAttrs 钉住 §5 要求的三个属性**并且钉住"暂无 Secure"**。
//
// 为什么连"没有 Secure"也要断言：它是一个**已知缺口**（部署无 TLS），
// 而不是可以随意的实现细节。用一条断言把它显式化之后：
//   - 有人手滑加上 Secure，测试会红（提醒他这会直接弄坏刷新功能，得同时上 TLS）；
//   - 将来上 TLS 时，这条断言会强迫改动者正视 auth.go 里的 TODO(TLS)。
func assertRefreshCookieAttrs(t *testing.T, c *http.Cookie) {
	t.Helper()
	if !c.HttpOnly {
		t.Fatal("refresh Cookie 必须是 HttpOnly（JS 不得读取）")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("SameSite 必须是 Lax（挡跨站 POST），实际 %v", c.SameSite)
	}
	if c.Path != refreshCookiePath {
		t.Fatalf("Path 必须是 %s，实际 %q", refreshCookiePath, c.Path)
	}
	if c.MaxAge <= 0 {
		t.Fatalf("MaxAge 必须为正（由会话到期时刻推导），实际 %d", c.MaxAge)
	}
	if c.Secure {
		t.Fatal("当前部署无 TLS：加了 Secure 浏览器就不会回传它（会直接弄坏刷新）。" +
			"上 TLS 之后请一并更新 auth.go 的 TODO(TLS) 与本断言")
	}
}

// 断言 limiter 包的 keyed 语义在本包被正确使用（编译期保证，避免 import 漂移）。
var _ = limiter.NewKeyed
