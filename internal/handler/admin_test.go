package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/auth"
	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/model"
	"ProjectionRoom/internal/service"
	"ProjectionRoom/internal/store"
	"ProjectionRoom/internal/usecase"
)

// 本文件是 T9（管理端最小骨架）的验收面：权限边界、字段白名单、封禁的两件事、审计投递。
//
// 装配策略：**真实**的 usecase.AccountService + **真实**的 auth.Signer，
// 只把持久化换成一张内存用户表（*store.Store 需要 PostgreSQL，而这里要验的是
// HTTP 面的判据）。这样"封禁"这条链路上除 SQL 以外的每一层都是生产代码：
// 状态变更、token_version 自增、审计投递（异步）全都会真的走到。

// fakeAdminStore 是**一张内存用户表**，同时满足两个端口：
//
//   - usecase.AccountStore：真实 AccountService 用它做封禁/改角色/写审计；
//   - handler.AdminUserStore：管理端列表与回读。
//
// 两者共用同一份数据是刻意的：若各用一份，"封禁成功但回读还是旧状态"这类假象
// 会让用例断言不到真正的东西。
type fakeAdminStore struct {
	mu       sync.Mutex
	users    map[int64]*store.User
	sessions map[string]*store.Session
	audits   []*store.AdminAudit
	nextID   int64
}

func newFakeAdminStore() *fakeAdminStore {
	return &fakeAdminStore{
		users:    map[int64]*store.User{},
		sessions: map[string]*store.Session{},
	}
}

// seed 造一个账号（id 自增、token_version=1，与真实创建路径一致）。
func (s *fakeAdminStore) seed(username, role, status string) *store.User {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	u := &store.User{
		ID:           s.nextID,
		Username:     strings.ToLower(username),
		DisplayName:  username,
		PasswordHash: "$2a$12$fixturefixturefixturefixturefixturefixturefixturefixture",
		Role:         role,
		Status:       status,
		TokenVersion: 1,
		CreatedAt:    time.Now(),
		LastSeenAt:   time.Now(),
	}
	s.users[u.ID] = u
	return u
}

// user 返回某个账号的副本（断言用，避免数据竞争）。
func (s *fakeAdminStore) user(id int64) (store.User, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[id]
	if !ok {
		return store.User{}, false
	}
	return *u, true
}

// auditsOf 返回指定 action 的审计行（副本）。
func (s *fakeAdminStore) auditsOf(action string) []store.AdminAudit {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]store.AdminAudit, 0, len(s.audits))
	for _, a := range s.audits {
		if a.Action == action {
			out = append(out, *a)
		}
	}
	return out
}

// —— usecase.AccountStore ——

func (s *fakeAdminStore) CreateUser(ctx context.Context, u *store.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	u.ID = s.nextID
	cp := *u
	s.users[u.ID] = &cp
	return nil
}

func (s *fakeAdminStore) UserByUsername(ctx context.Context, username string) (*store.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.users {
		if u.Username == strings.ToLower(strings.TrimSpace(username)) {
			cp := *u
			return &cp, nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *fakeAdminStore) UserByID(ctx context.Context, id int64) (*store.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *u
	return &cp, nil
}

func (s *fakeAdminStore) SetUserStatus(ctx context.Context, id int64, status, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[id]
	if !ok {
		return store.ErrNotFound
	}
	u.Status = status
	if status == store.StatusBanned && strings.TrimSpace(reason) != "" {
		r := strings.TrimSpace(reason)
		u.BannedReason = &r
	} else {
		u.BannedReason = nil
	}
	return nil
}

func (s *fakeAdminStore) SetUserRole(ctx context.Context, id int64, role string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[id]
	if !ok {
		return store.ErrNotFound
	}
	u.Role = role
	return nil
}

func (s *fakeAdminStore) BumpTokenVersion(ctx context.Context, id int64) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[id]
	if !ok {
		return 0, store.ErrNotFound
	}
	u.TokenVersion++
	return u.TokenVersion, nil
}

func (s *fakeAdminStore) TouchLastSeen(ctx context.Context, id int64, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[id]
	if !ok {
		return store.ErrNotFound
	}
	u.LastSeenAt = at
	return nil
}

func (s *fakeAdminStore) CreateSession(ctx context.Context, sess *store.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sess.TokenHash] = sess
	return nil
}

func (s *fakeAdminStore) SessionByTokenHash(ctx context.Context, hash string) (*store.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[hash]
	if !ok {
		return nil, store.ErrNotFound
	}
	return sess, nil
}

func (s *fakeAdminStore) RevokeSession(ctx context.Context, id int64) error { return nil }

func (s *fakeAdminStore) RevokeAllSessions(ctx context.Context, userID int64) (int64, error) {
	return 0, nil
}

func (s *fakeAdminStore) InsertAudit(ctx context.Context, a *store.AdminAudit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *a
	s.audits = append(s.audits, &cp)
	return nil
}

// EventWriter 返回 nil：AccountService.submit 因此回落到"后台协程直接执行"，
// 于是审计仍然是**异步**投递的（这正是要验的形态），只是不经过写队列。
func (s *fakeAdminStore) EventWriter() *store.Writer { return nil }

// —— handler.AdminUserStore ——

func (s *fakeAdminStore) ListUsers(ctx context.Context, q store.UserQuery) ([]store.User, int64, error) {
	limit, offset := q.Limit, q.Offset
	if limit <= 0 {
		limit = defaultAdminPageLimit
	}
	if limit > maxAdminPageLimit {
		limit = maxAdminPageLimit
	}
	if offset < 0 {
		offset = 0
	}

	pattern := strings.ToLower(strings.TrimSpace(q.Search))

	s.mu.Lock()
	defer s.mu.Unlock()
	all := make([]store.User, 0, len(s.users))
	for _, u := range s.users {
		if pattern != "" &&
			!strings.Contains(strings.ToLower(u.Username), pattern) &&
			!strings.Contains(strings.ToLower(u.DisplayName), pattern) {
			continue
		}
		all = append(all, *u)
	}
	// 与 store.ListUsers 的排序口径一致（id ASC，稳定分页）。
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })

	total := int64(len(all))
	if offset >= len(all) {
		return []store.User{}, total, nil
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	return all[offset:end], total, nil
}

// fakeAdminHub 记录封禁断连（T9 要验的"第二件事"）。
type fakeAdminHub struct {
	mu            sync.Mutex
	closedByUser  []int64
	connections   int
	distinctUsers int
}

func (h *fakeAdminHub) Stats() (connections, distinctUsers int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.connections, h.distinctUsers
}

func (h *fakeAdminHub) CloseByUser(userID int64) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closedByUser = append(h.closedByUser, userID)
	return 1
}

// closedUsers 返回被要求断连的账号列表。
func (h *fakeAdminHub) closedUsers() []int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]int64(nil), h.closedByUser...)
}

// fakeAdminWriter 提供写队列指标。
type fakeAdminWriter struct {
	queueLen int
	stats    store.WriterStats
}

func (w *fakeAdminWriter) QueueLen() int { return w.queueLen }

func (w *fakeAdminWriter) Stats() store.WriterStats { return w.stats }

// adminEnv 是一台"管理端可用"的测试服务。
//
// 用 gin.Engine 而不是 httptest.Server：管理端用例验的是**协议面**
// （状态码、响应字段、审计副作用），不需要真实监听端口；
// 与 auth_test.go 的 accountTestRouter 同一形态，而且更快。
type adminEnv struct {
	engine     *gin.Engine
	rooms      *usecase.Manager
	hub        *fakeAdminHub
	st         *fakeAdminStore
	svc        *usecase.AccountService
	logs       *service.LogRing
	writer     *fakeAdminWriter
	admin      store.User
	user       store.User
	adminToken string
	userToken  string
}

// newAdminEnv 起服务（mutate 可改配置）。
func newAdminEnv(t *testing.T, mutate func(*config.Config)) *adminEnv {
	t.Helper()

	cfg := config.Default()
	cfg.Segment.TempDir = t.TempDir()
	cfg.Static.Serve = false
	if mutate != nil {
		mutate(cfg)
	}
	cfg.Auth.DBDSN = "host=127.0.0.1 dbname=fixture sslmode=disable"

	signer, err := auth.NewSigner(handlerTestSecret, 15*time.Minute)
	if err != nil {
		t.Fatalf("构造 Signer 失败: %v", err)
	}
	st := newFakeAdminStore()
	admin := st.seed("root", store.RoleAdmin, store.StatusActive)
	user := st.seed("alice", store.RoleUser, store.StatusActive)

	tickets := auth.NewTicketStore(cfg.Auth.WSTicketTTL)
	svc, err := usecase.NewAccountService(st, signer, tickets, usecase.AccountOptions{
		RefreshTTL: cfg.Auth.RefreshTTL,
	})
	if err != nil {
		t.Fatalf("构造 AccountService 失败: %v", err)
	}

	hub, cleanup, err := service.NewHub(cfg)
	if err != nil {
		t.Fatalf("构造 Hub 失败: %v", err)
	}
	rooms := usecase.NewManager(cfg, hub)
	logs := service.NewLogRing(8)
	writer := &fakeAdminWriter{
		queueLen: 3,
		stats: store.WriterStats{
			Queued: 10, Dropped: 1, Succeeded: 8, Failed: 1, LastLatency: 7 * time.Millisecond,
		},
	}
	fakeHub := &fakeAdminHub{connections: 5, distinctUsers: 2}

	engine := NewRouter(cfg, hub, rooms, nil,
		AuthDeps{
			Service: svc,
			Session: SessionConfig{
				AccessTTL:   cfg.Auth.AccessTTL,
				RefreshTTL:  cfg.Auth.RefreshTTL,
				WSTicketTTL: cfg.Auth.WSTicketTTL,
			},
		},
		AdminDeps{Users: st, Accounts: svc, Hub: fakeHub, Rooms: rooms, Writer: writer, Logs: logs},
	)
	t.Cleanup(func() { rooms.Stop(); cleanup() })

	adminTok, _, err := signer.Issue(admin.ID, admin.Role, admin.TokenVersion)
	if err != nil {
		t.Fatalf("签发管理员 token 失败: %v", err)
	}
	userTok, _, err := signer.Issue(user.ID, user.Role, user.TokenVersion)
	if err != nil {
		t.Fatalf("签发普通用户 token 失败: %v", err)
	}

	return &adminEnv{
		engine: engine, rooms: rooms, hub: fakeHub, st: st, svc: svc, logs: logs, writer: writer,
		admin: *admin, user: *user, adminToken: adminTok, userToken: userTok,
	}
}

// —— 用例：权限边界 ——

// TestAdminRoutesEnforceRoleOnServer 覆盖 T9 的权限判据：
// 无 token → 401、普通 user → 403、admin → 200（不是靠前端 v-if）。
func TestAdminRoutesEnforceRoleOnServer(t *testing.T) {
	env := newAdminEnv(t, nil)

	requests := []struct {
		method, path, body string
	}{
		{http.MethodGet, "/api/admin/users", ""},
		{http.MethodGet, "/api/admin/metrics", ""},
		{http.MethodGet, "/api/admin/logs", ""},
		{http.MethodPatch, "/api/admin/users/" + strconv.FormatInt(env.user.ID, 10), `{"status":"banned"}`},
	}

	for _, req := range requests {
		// ① 匿名 → 401
		if w := doJSON(t, env.engine, req.method, req.path, req.body, ""); w.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s 匿名应当 401，实际 %d", req.method, req.path, w.Code)
		}
		// ② 普通用户 → 403
		w := doJSON(t, env.engine, req.method, req.path, req.body, env.userToken)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s %s 普通用户应当 403，实际 %d（%s）", req.method, req.path, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), CodeForbidden) {
			t.Fatalf("403 应当带 %s：%s", CodeForbidden, w.Body.String())
		}
		// ③ 管理员 → 200
		if w := doJSON(t, env.engine, req.method, req.path, req.body, env.adminToken); w.Code != http.StatusOK {
			t.Fatalf("%s %s 管理员应当 200，实际 %d（%s）", req.method, req.path, w.Code, w.Body.String())
		}
	}
}

// —— 用例：用户列表 ——

// TestAdminUsersListPaginatesAndNeverLeaksHash 覆盖 T9-①：
// 分页/搜索/边界，以及"绝不返回 password_hash"。
func TestAdminUsersListPaginatesAndNeverLeaksHash(t *testing.T) {
	env := newAdminEnv(t, nil)
	// 再种两个账号，凑出可搜的规模。
	env.st.seed("alice2", store.RoleUser, store.StatusActive)
	env.st.seed("bob", store.RoleUser, store.StatusBanned)

	w := doJSON(t, env.engine, http.MethodGet, "/api/admin/users", "", env.adminToken)
	if w.Code != http.StatusOK {
		t.Fatalf("应当 200，实际 %d（%s）", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, forbidden := range []string{"passwordHash", "password_hash", "PasswordHash", "$2a$"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("用户列表响应里不得出现 %q：%s", forbidden, body)
		}
	}

	var page struct {
		Items  []usecase.Profile `json:"items"`
		Total  int64             `json:"total"`
		Limit  int               `json:"limit"`
		Offset int               `json:"offset"`
	}
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatalf("解析用户列表失败: %v", err)
	}
	if page.Total != 4 || len(page.Items) != 4 {
		t.Fatalf("应当有 4 个账号，实际 total=%d items=%d", page.Total, len(page.Items))
	}
	if page.Limit != defaultAdminPageLimit || page.Offset != 0 {
		t.Fatalf("缺省分页应当是 limit=%d offset=0，实际 limit=%d offset=%d",
			defaultAdminPageLimit, page.Limit, page.Offset)
	}
	// 分页必须稳定：id 升序。
	for i := 1; i < len(page.Items); i++ {
		if page.Items[i-1].ID >= page.Items[i].ID {
			t.Fatalf("用户列表必须按 id 升序（稳定分页），实际 %d → %d", page.Items[i-1].ID, page.Items[i].ID)
		}
	}

	// 搜索：只留匹配的，total 跟着变。
	w = doJSON(t, env.engine, http.MethodGet, "/api/admin/users?search=alice", "", env.adminToken)
	page.Items, page.Total = nil, 0
	_ = json.Unmarshal(w.Body.Bytes(), &page)
	if page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("search=alice 应命中 2 个（alice/alice2），实际 total=%d items=%d", page.Total, len(page.Items))
	}

	// 上限：limit=1000 被夹到 200（并把生效值回显）。
	w = doJSON(t, env.engine, http.MethodGet, "/api/admin/users?limit=1000&offset=-5", "", env.adminToken)
	page.Limit, page.Offset = 0, -1
	_ = json.Unmarshal(w.Body.Bytes(), &page)
	if page.Limit != maxAdminPageLimit || page.Offset != 0 {
		t.Fatalf("limit=1000 应被夹到 %d、offset=-5 应归零，实际 limit=%d offset=%d",
			maxAdminPageLimit, page.Limit, page.Offset)
	}

	// 非整数分页参数：400（静默降级会让"翻页翻不到东西"变成查不出的 bug）。
	for _, bad := range []string{"limit=abc", "offset=x"} {
		w = doJSON(t, env.engine, http.MethodGet, "/api/admin/users?"+bad, "", env.adminToken)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s 应当 400，实际 %d（%s）", bad, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), model.CodeBadRequest) {
			t.Fatalf("400 应当带 %s：%s", model.CodeBadRequest, w.Body.String())
		}
	}
}

// —— 用例：写操作（封禁 / 改角色） ——

// TestAdminPatchBanClosesConnectionsAndAudits 覆盖 T9-②的核心：
// 封禁必须**同时**做两件事（改状态 + token_version 自增，见 usecase；
// 以及断开在跑的长连接，见 handler），并且审计必须被投递。
func TestAdminPatchBanClosesConnectionsAndAudits(t *testing.T) {
	env := newAdminEnv(t, nil)
	path := "/api/admin/users/" + strconv.FormatInt(env.user.ID, 10)

	w := doJSON(t, env.engine, http.MethodPatch, path, `{"status":"banned","reason":"违规直播"}`, env.adminToken)
	if w.Code != http.StatusOK {
		t.Fatalf("封禁应当 200，实际 %d（%s）", w.Code, w.Body.String())
	}
	var resp struct {
		User usecase.Profile `json:"user"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析封禁响应失败: %v", err)
	}
	if resp.User.Status != store.StatusBanned || resp.User.ID != env.user.ID {
		t.Fatalf("响应应当回读改动后的事实（banned/%d），实际 %+v", env.user.ID, resp.User)
	}

	// 第一件事：状态 + token_version（旧 access token 立刻失效的依据）。
	got, ok := env.st.user(env.user.ID)
	if !ok {
		t.Fatal("账号不见了")
	}
	if got.Status != store.StatusBanned {
		t.Fatalf("账号状态应当是 banned，实际 %q", got.Status)
	}
	if got.TokenVersion != env.user.TokenVersion+1 {
		t.Fatalf("封禁必须自增 token_version（%d → %d），实际 %d",
			env.user.TokenVersion, env.user.TokenVersion+1, got.TokenVersion)
	}
	if got.BannedReason == nil || *got.BannedReason != "违规直播" {
		t.Fatalf("封禁理由应当落库，实际 %v", got.BannedReason)
	}

	// 第二件事：断开该账号的全部长连接（否则已授权的长连接仍然能发信令）。
	closed := env.hub.closedUsers()
	if len(closed) != 1 || closed[0] != env.user.ID {
		t.Fatalf("封禁必须调用 CloseByUser(%d)，实际 %v", env.user.ID, closed)
	}

	// 审计：异步投递（EventWriter 为 nil 时走后台协程），所以要等到它落下来。
	waitFor(t, "封禁审计入库", func() bool { return len(env.st.auditsOf("user.ban")) == 1 })
	audit := env.st.auditsOf("user.ban")[0]
	if audit.ActorID != env.admin.ID {
		t.Fatalf("审计的操作者应当是管理员 %d，实际 %d", env.admin.ID, audit.ActorID)
	}
	if audit.TargetType != "user" || audit.TargetID != strconv.FormatInt(env.user.ID, 10) {
		t.Fatalf("审计的目标应当是 user/%d，实际 %s/%s", env.user.ID, audit.TargetType, audit.TargetID)
	}
	if !strings.Contains(audit.Detail, "违规直播") || !strings.Contains(audit.Detail, store.StatusBanned) {
		t.Fatalf("审计明细应含状态与理由，实际 %s", audit.Detail)
	}
	if audit.IP == nil || *audit.IP != "203.0.113.7" {
		t.Fatalf("审计应记录来源 IP，实际 %v", audit.IP)
	}
}

// TestAdminPatchRoleAudits 覆盖改角色：走 usecase.SetRole（含审计），并回读新角色。
func TestAdminPatchRoleAudits(t *testing.T) {
	env := newAdminEnv(t, nil)
	path := "/api/admin/users/" + strconv.FormatInt(env.user.ID, 10)

	w := doJSON(t, env.engine, http.MethodPatch, path, `{"role":"admin"}`, env.adminToken)
	if w.Code != http.StatusOK {
		t.Fatalf("改角色应当 200，实际 %d（%s）", w.Code, w.Body.String())
	}
	var resp struct {
		User usecase.Profile `json:"user"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.User.Role != store.RoleAdmin {
		t.Fatalf("角色应当已改成 admin，实际 %q", resp.User.Role)
	}
	// 改角色**不该**断连（只有封禁要断）：它靠 token_version 让旧 token 失效。
	if closed := env.hub.closedUsers(); len(closed) != 0 {
		t.Fatalf("改角色不该断开连接，实际 %v", closed)
	}

	waitFor(t, "改角色审计入库", func() bool { return len(env.st.auditsOf("user.role")) == 1 })
	audit := env.st.auditsOf("user.role")[0]
	if !strings.Contains(audit.Detail, store.RoleAdmin) {
		t.Fatalf("审计明细应含新角色，实际 %s", audit.Detail)
	}
}

// TestAdminPatchRejectsBadInput 覆盖输入校验与"把自己锁在门外"的拒绝。
func TestAdminPatchRejectsBadInput(t *testing.T) {
	env := newAdminEnv(t, nil)
	target := "/api/admin/users/" + strconv.FormatInt(env.user.ID, 10)
	self := "/api/admin/users/" + strconv.FormatInt(env.admin.ID, 10)

	cases := []struct {
		name, path, body string
		want             int
	}{
		{"非法角色", target, `{"role":"root"}`, http.StatusBadRequest},
		{"非法状态", target, `{"status":"deleted"}`, http.StatusBadRequest},
		{"空 patch", target, `{}`, http.StatusBadRequest},
		{"非法 id", "/api/admin/users/abc", `{"role":"admin"}`, http.StatusBadRequest},
		{"不存在的 id", "/api/admin/users/99999", `{"status":"banned"}`, http.StatusNotFound},
		{"请求体不是 JSON", target, `not-json`, http.StatusBadRequest},
		{"自己封自己", self, `{"status":"banned"}`, http.StatusForbidden},
		{"自己降级", self, `{"role":"user"}`, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := doJSON(t, env.engine, http.MethodPatch, tc.path, tc.body, env.adminToken)
			if w.Code != tc.want {
				t.Fatalf("应当 %d，实际 %d（%s）", tc.want, w.Code, w.Body.String())
			}
			if w.Code == http.StatusForbidden && !strings.Contains(w.Body.String(), CodeForbidden) {
				t.Fatalf("403 应当带 %s：%s", CodeForbidden, w.Body.String())
			}
		})
	}

	// 自封禁被拒之后，管理员的账号必须原样（不能被"拒绝"顺手改掉）。
	got, _ := env.st.user(env.admin.ID)
	if got.Status != store.StatusActive || got.Role != store.RoleAdmin {
		t.Fatalf("被拒的自操作不得改动账号，实际 %+v", got)
	}
	// 被拒的写操作不得产生审计（"审计只记真正发生的事"）。
	if n := len(env.st.auditsOf("user.ban")) + len(env.st.auditsOf("user.role")); n != 0 {
		t.Fatalf("被拒的写操作不该留下审计，实际 %d 条", n)
	}
}

// —— 用例：指标 ——

// TestAdminMetricsAggregatesAllSources 覆盖 T9-③：四个来源各一组指标。
func TestAdminMetricsAggregatesAllSources(t *testing.T) {
	env := newAdminEnv(t, nil)
	if _, err := env.logs.Write([]byte("line-1\nline-2\n")); err != nil {
		t.Fatalf("写日志缓冲失败: %v", err)
	}

	w := doJSON(t, env.engine, http.MethodGet, "/api/admin/metrics", "", env.adminToken)
	if w.Code != http.StatusOK {
		t.Fatalf("应当 200，实际 %d（%s）", w.Code, w.Body.String())
	}
	var m struct {
		Connections int `json:"connections"`
		OnlineUsers int `json:"onlineUsers"`
		Rooms       int `json:"rooms"`
		WriteQueue  struct {
			Pending       int   `json:"pending"`
			Queued        int   `json:"queued"`
			Dropped       int   `json:"dropped"`
			Succeeded     int   `json:"succeeded"`
			Failed        int   `json:"failed"`
			LastLatencyMs int64 `json:"lastLatencyMs"`
		} `json:"writeQueue"`
		Logs struct {
			Kept    int   `json:"kept"`
			Total   int64 `json:"total"`
			Dropped int64 `json:"dropped"`
		} `json:"logs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("解析指标失败: %v（body=%s）", err, w.Body.String())
	}
	if m.Connections != 5 || m.OnlineUsers != 2 {
		t.Fatalf("连接/在线用户应来自 hub.Stats（5/2），实际 %d/%d", m.Connections, m.OnlineUsers)
	}
	if m.Rooms != env.rooms.RoomCount() {
		t.Fatalf("房间数应来自 manager，实际 %d（期望 %d）", m.Rooms, env.rooms.RoomCount())
	}
	if m.WriteQueue.Pending != 3 || m.WriteQueue.Queued != 10 || m.WriteQueue.Dropped != 1 ||
		m.WriteQueue.Succeeded != 8 || m.WriteQueue.Failed != 1 || m.WriteQueue.LastLatencyMs != 7 {
		t.Fatalf("写队列指标不对: %+v", m.WriteQueue)
	}
	if m.Logs.Kept != 2 || m.Logs.Total != 2 {
		t.Fatalf("日志指标不对: %+v", m.Logs)
	}
}

// —— 用例：日志 ——

// TestAdminLogsPageIsTextOnlyAndNewestFirst 覆盖 T9-④：
// 环形缓冲分页（新 → 旧）、total，以及**字段白名单**（只给文本行数组）。
func TestAdminLogsPageIsTextOnlyAndNewestFirst(t *testing.T) {
	env := newAdminEnv(t, nil)
	// 第二行故意是"一段 JSON 文本"：它必须作为**字符串**出现，
	// 而不是让前端拿到一个可以再去解析的结构（§8 的字段白名单要求）。
	if _, err := env.logs.Write([]byte("first\n{\"password_hash\":\"secret\",\"actor\":1}\nthird\n")); err != nil {
		t.Fatalf("写日志缓冲失败: %v", err)
	}

	w := doJSON(t, env.engine, http.MethodGet, "/api/admin/logs?limit=2", "", env.adminToken)
	if w.Code != http.StatusOK {
		t.Fatalf("应当 200，实际 %d（%s）", w.Code, w.Body.String())
	}

	// 顶层只允许这四个字段：多出任何一个都是"把原始结构抛给前端"的信号。
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("解析日志响应失败: %v", err)
	}
	allowed := map[string]bool{"lines": true, "total": true, "limit": true, "offset": true}
	for k := range raw {
		if !allowed[k] {
			t.Fatalf("日志响应的字段超出白名单：%q（body=%s）", k, w.Body.String())
		}
	}

	var page struct {
		Lines  []string `json:"lines"`
		Total  int64    `json:"total"`
		Limit  int      `json:"limit"`
		Offset int      `json:"offset"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("lines 必须是字符串数组: %v（body=%s）", err, w.Body.String())
	}
	if page.Total != 3 || page.Limit != 2 || page.Offset != 0 {
		t.Fatalf("分页元数据不对: total=%d limit=%d offset=%d", page.Total, page.Limit, page.Offset)
	}
	if len(page.Lines) != 2 {
		t.Fatalf("应当返回 2 行，实际 %d（%v）", len(page.Lines), page.Lines)
	}
	// 新 → 旧：最新一行是 third。
	if !strings.HasPrefix(page.Lines[0], "third") {
		t.Fatalf("日志分页必须是新 → 旧（第一行应为最新），实际 %q", page.Lines[0])
	}
	if !strings.Contains(page.Lines[1], "password_hash") {
		t.Fatalf("第二行应当是那段 JSON 文本，实际 %q", page.Lines[1])
	}
	// 那段 JSON 必须以**转义后的字符串**出现（而不是被解析成结构）。
	if !strings.Contains(w.Body.String(), `{\"password_hash\"`) {
		t.Fatalf("JSON 文本必须以转义字符串的形式出现：%s", w.Body.String())
	}

	// offset 越界返回空数组而不是报错（日志在持续写入，过期页码是正常现象）。
	w = doJSON(t, env.engine, http.MethodGet, "/api/admin/logs?offset=99", "", env.adminToken)
	if w.Code != http.StatusOK {
		t.Fatalf("越界 offset 应当 200，实际 %d", w.Code)
	}
	page.Lines = nil
	_ = json.Unmarshal(w.Body.Bytes(), &page)
	if len(page.Lines) != 0 || page.Total != 3 {
		t.Fatalf("越界 offset 应当返回空页 + 正确的 total，实际 lines=%v total=%d", page.Lines, page.Total)
	}
}

// —— 用例：能力开关与装配缺失 ——

// TestAdminRoutesNotRegisteredWithoutDSN：PR_DB_DSN 为空 → 管理端整体不注册。
// 走**真实**的 NewRouter，因此验的是生产行为。
func TestAdminRoutesNotRegisteredWithoutDSN(t *testing.T) {
	cfg := config.Default() // Auth.DBDSN 默认为空 = 账号能力关闭
	cfg.Segment.TempDir = t.TempDir()
	cfg.Static.Serve = false // 关掉 SPA 回退，让"路由不存在"表现为 404

	hub, cleanup, err := service.NewHub(cfg)
	if err != nil {
		t.Fatalf("构造 Hub 失败: %v", err)
	}
	rooms := usecase.NewManager(cfg, hub)
	t.Cleanup(func() { rooms.Stop(); cleanup() })

	// 依赖故意给全：只要 DSN 为空，它们就不该被用上。
	deps := AdminDeps{Users: newFakeAdminStore(), Hub: &fakeAdminHub{}}
	engine := NewRouter(cfg, hub, rooms, nil, AuthDeps{}, deps)

	for _, path := range []string{"/api/admin/users", "/api/admin/metrics", "/api/admin/logs"} {
		w := doJSON(t, engine, http.MethodGet, path, "", "")
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s 在账号能力关闭时不该存在，实际 %d（%s）", path, w.Code, w.Body.String())
		}
	}
}

// TestAdminRoutesUnavailableWhenDepsMissing：DSN 配了但依赖没装配 → 503（不是 500、也不是消失）。
func TestAdminRoutesUnavailableWhenDepsMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Default()
	cfg.Auth.DBDSN = "host=127.0.0.1 dbname=x sslmode=disable"

	r := gin.New()
	api := r.Group("/api")
	registerAdminRoutes(api, AdminDeps{}, cfg, nil) // Accounts/Users 都是 nil

	w := doJSON(t, r, http.MethodGet, "/api/admin/users", "", "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("应当 503，实际 %d（%s）", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), CodeAuthUnavailable) {
		t.Fatalf("错误码应当是 %s：%s", CodeAuthUnavailable, w.Body.String())
	}
}
