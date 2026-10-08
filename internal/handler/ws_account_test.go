package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"ProjectionRoom/internal/auth"
	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/model"
	"ProjectionRoom/internal/service"
	"ProjectionRoom/internal/store"
	"ProjectionRoom/internal/usecase"
)

// 本文件是 T8 的验收面：**建房绑定房主 + WS 一次性票据 + 封禁拦截**。
//
// 装配形态与生产一致（同一个 NewRouter、同一组中间件），只把两处外部世界换成桩：
//   - 账号服务用 auth_test.go 里的假实现（不需要 PostgreSQL）；
//   - rooms_meta 的异步落库用假 sink（不需要写队列），断言"投递了什么"。
//
// 票据表用**真实**的 auth.TicketStore：单次使用与过期语义是本任务的一部分，
// 用一个"总是命中"的桩会把最该验的性质验掉。

// recordingRoomMetaSink 记录建房路径投递的房间元数据（并可以模拟"队列满"）。
type recordingRoomMetaSink struct {
	mu    sync.Mutex
	metas []store.RoomMeta
	// reject 为 true 时模拟"队列满/写入器已关闭"：SubmitRoomMeta 返回 false。
	reject bool
}

func (s *recordingRoomMetaSink) SubmitRoomMeta(m *store.RoomMeta) bool {
	if m == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reject {
		return false
	}
	s.metas = append(s.metas, *m)
	return true
}

// recorded 返回已记录的元数据快照。
func (s *recordingRoomMetaSink) recorded() []store.RoomMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.RoomMeta(nil), s.metas...)
}

// wsAccountEnv 是一台"账号能力已开启"的测试服务及其可观察的依赖。
type wsAccountEnv struct {
	srv     *httptest.Server
	hub     *service.Hub
	rooms   *usecase.Manager
	svc     *fakeAccountService
	tickets *auth.TicketStore
	meta    *recordingRoomMetaSink
	cfg     *config.Config
}

// newWSAccountEnv 起服务。mutate 可以改配置（例如把票据 TTL 压到 1ms）。
func newWSAccountEnv(t *testing.T, mutate func(*config.Config)) *wsAccountEnv {
	t.Helper()

	cfg := config.Default()
	if mutate != nil {
		mutate(cfg)
	}
	// 非空的 DSN = "账号能力已开启"（认证路由按这个开关注册）。
	cfg.Auth.DBDSN = "host=127.0.0.1 dbname=fixture sslmode=disable"

	hub, cleanup, err := service.NewHub(cfg)
	if err != nil {
		t.Fatalf("构造 Hub 失败: %v", err)
	}
	rooms := usecase.NewManager(cfg, hub)
	tickets := auth.NewTicketStore(cfg.Auth.WSTicketTTL)
	svc := newFakeAccountService(t)
	meta := &recordingRoomMetaSink{}

	deps := AuthDeps{
		Service: svc,
		Session: SessionConfig{
			AccessTTL:   cfg.Auth.AccessTTL,
			RefreshTTL:  cfg.Auth.RefreshTTL,
			WSTicketTTL: cfg.Auth.WSTicketTTL,
		},
		Tickets:  tickets,
		RoomMeta: meta,
	}
	srv := httptest.NewServer(NewRouter(cfg, hub, rooms, nil, deps, AdminDeps{}))
	t.Cleanup(func() {
		srv.Close()
		rooms.Stop()
		cleanup()
	})

	return &wsAccountEnv{srv: srv, hub: hub, rooms: rooms, svc: svc, tickets: tickets, meta: meta, cfg: cfg}
}

// wsURL 拼一条 /ws 地址（ticket 为空时不带该参数 —— 那正是"游客"与"带票"的分界）。
func (e *wsAccountEnv) wsURL(roomID, clientID, ticket string) string {
	url := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/ws?roomId=" + roomID + "&clientId=" + clientID
	if ticket != "" {
		url += "&ticket=" + ticket
	}
	return url
}

// dialWS 连接 /ws；成功时返回连接，失败时返回 HTTP 响应（握手被拒时它是唯一的证据）。
func dialWS(t *testing.T, url string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	return websocket.Dial(ctx, url, nil)
}

// closeInfo 读出直到连接关闭，返回关闭码与关闭原因（以及收到的最后一条错误信封码）。
func closeInfo(t *testing.T, conn *websocket.Conn) (websocket.StatusCode, string, string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	lastCode := ""
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			var ce websocket.CloseError
			if errors.As(err, &ce) {
				return ce.Code, ce.Reason, lastCode
			}
			t.Fatalf("等待关闭帧时出错: %v", err)
		}
		if typ != websocket.MessageBinary {
			continue
		}
		if env, err := model.Unmarshal(data); err == nil && env.Type == model.TypeError {
			lastCode = env.Code
		}
	}
}

// waitFor 轮询到条件成立（用于"服务端异步完成的动作"：注册连接、异步入审计）。
func waitFor(t *testing.T, desc string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(testTimeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", desc)
}

// —— T8-①：建房必须登录 ——

// TestCreateRoomRequiresLoginAndBindsOwner 覆盖 T8 的核心两半：
// 匿名建房必须 401（且**不留下房间**），带有效 access token 建房必须 200
// 并把房主写进内存房间与（异步）rooms_meta。
func TestCreateRoomRequiresLoginAndBindsOwner(t *testing.T) {
	env := newWSAccountEnv(t, nil)
	owner := env.svc.seeded("room-owner", "stored-passw0rd", store.RoleUser, store.StatusActive)
	access, _, err := env.svc.issueAccess(owner)
	if err != nil {
		t.Fatalf("签发 access token 失败: %v", err)
	}

	// ① 匿名：401，且不能留下任何房间。
	anon := postJSON(t, env.srv.URL+"/api/rooms", map[string]any{"password": "pass"})
	defer anon.Body.Close()
	if anon.StatusCode != http.StatusUnauthorized {
		t.Fatalf("匿名建房应 401（建房必须登录），实际 %d（body=%s）", anon.StatusCode, readBody(t, anon))
	}
	if n := env.rooms.RoomCount(); n != 0 {
		t.Fatalf("被拒的匿名建房不得留下房间，实际在册 %d 间", n)
	}
	if got := env.meta.recorded(); len(got) != 0 {
		t.Fatalf("被拒的匿名建房不得投递元数据，实际 %d 条", len(got))
	}

	// ② 带有效 token：200 + 房主绑定 + 元数据投递。
	resp := postJSONAuth(t, env.srv.URL+"/api/rooms", map[string]any{"password": "pass"}, access)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("已登录建房应 200，实际 %d（body=%s）", resp.StatusCode, readBody(t, resp))
	}
	var created struct {
		RoomID    string `json:"roomId"`
		HostToken string `json:"hostToken"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("解析建房响应失败: %v", err)
	}

	r, ok := env.rooms.Get(created.RoomID)
	if !ok {
		t.Fatalf("建房响应给了房间码 %q，但 Manager 里查不到", created.RoomID)
	}
	if got := r.OwnerUserID(); got != owner.ID {
		t.Fatalf("内存房间的房主应当是 %d（建房者），实际 %d", owner.ID, got)
	}

	metas := env.meta.recorded()
	if len(metas) != 1 {
		t.Fatalf("建房应当投递 1 条 rooms_meta，实际 %d 条", len(metas))
	}
	m := metas[0]
	if m.RoomID != created.RoomID || m.OwnerUserID != owner.ID {
		t.Fatalf("元数据的房间码/房主不对: %+v（期望 room=%s owner=%d）", m, created.RoomID, owner.ID)
	}
	if !m.HasPassword {
		t.Fatal("设了密码的房间，has_password 必须是 true（公开房列表要靠它）")
	}
	if m.Title != "" || m.IsPublic {
		t.Fatalf("建房时的默认元数据应当是 title=\"\"、is_public=false，实际 %+v", m)
	}
}

// TestCreateRoomSucceedsEvenIfMetaWriteFails 钉住 T8 的回滚语义：
// 元数据投递失败（队列满/写入器已关闭）**不能**让建房失败 —— 真相在内存房间，
// 元数据只是派生视图；反过来回滚也收不回已经下发的房间码。
func TestCreateRoomSucceedsEvenIfMetaWriteFails(t *testing.T) {
	env := newWSAccountEnv(t, nil)
	env.meta.reject = true // 模拟写队列满
	owner := env.svc.seeded("room-owner", "stored-passw0rd", store.RoleUser, store.StatusActive)
	access, _, err := env.svc.issueAccess(owner)
	if err != nil {
		t.Fatalf("签发 access token 失败: %v", err)
	}

	resp := postJSONAuth(t, env.srv.URL+"/api/rooms", map[string]any{}, access)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("元数据投递失败不该影响开播：建房应当 200，实际 %d", resp.StatusCode)
	}
	if env.rooms.RoomCount() != 1 {
		t.Fatalf("房间应当已经建好（在册 1 间），实际 %d", env.rooms.RoomCount())
	}
	if got := env.meta.recorded(); len(got) != 0 {
		t.Fatalf("sink 明确拒绝时不该有记录，实际 %d 条", len(got))
	}
}

// TestCreateRoomWithoutMetaSinkStillWorks 覆盖"装配里没有写队列"这条降级路径。
func TestCreateRoomWithoutMetaSinkStillWorks(t *testing.T) {
	cfg := config.Default()
	cfg.Auth.DBDSN = "host=127.0.0.1 dbname=fixture sslmode=disable"
	hub, cleanup, err := service.NewHub(cfg)
	if err != nil {
		t.Fatalf("构造 Hub 失败: %v", err)
	}
	rooms := usecase.NewManager(cfg, hub)
	svc := newFakeAccountService(t)
	owner := svc.seeded("room-owner", "stored-passw0rd", store.RoleUser, store.StatusActive)
	access, _, err := svc.issueAccess(owner)
	if err != nil {
		t.Fatalf("签发 access token 失败: %v", err)
	}
	// RoomMeta 故意留 nil。
	srv := httptest.NewServer(NewRouter(cfg, hub, rooms, nil, AuthDeps{Service: svc}, AdminDeps{}))
	t.Cleanup(func() { srv.Close(); rooms.Stop(); cleanup() })

	resp := postJSONAuth(t, srv.URL+"/api/rooms", map[string]any{}, access)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("没有写队列时建房仍应成功（只记日志），实际 %d", resp.StatusCode)
	}
}

// —— T8-②：WS 一次性票据 ——

// TestWSTicketBindsAccount 覆盖"带有效票据的 WS → 连接绑定 userID"。
//
// 断言方式刻意选了**可观测的后果**而不是"看起来绑上了"：
// hub.Stats() 的在线用户数 + hub.CloseByUser 能精确找到这条连接。
// 后者正是封禁路径要依赖的能力：绑不上就等于封禁拦不住已在线的连接。
func TestWSTicketBindsAccount(t *testing.T) {
	env := newWSAccountEnv(t, nil)
	u := env.svc.seeded("alice", "stored-passw0rd", store.RoleUser, store.StatusActive)
	room, _, err := env.rooms.Create("", "", 0)
	if err != nil {
		t.Fatalf("建房失败: %v", err)
	}
	ticket := env.tickets.Issue(u.ID)

	conn, resp, err := dialWS(t, env.wsURL(room.ID, "c1", ticket))
	if err != nil {
		t.Fatalf("带有效票据的握手应当成功: %v（resp=%v）", err, resp)
	}
	defer conn.CloseNow()

	// 服务端在 101 之后才注册连接与绑定账号：轮询到它稳定。
	waitFor(t, "连接注册并绑定账号", func() bool {
		_, users := env.hub.Stats()
		return users == 1
	})
	if n := env.hub.RoomSize(room.ID); n != 1 {
		t.Fatalf("连接应当已注册进房间，实际 %d", n)
	}
	// 绑定是"真的"：封禁路径能凭 userID 精确定位到它，并以 1008 断开。
	if n := env.hub.CloseByUser(u.ID); n != 1 {
		t.Fatalf("封禁断连应当命中 1 条连接，实际 %d（绑定没生效？）", n)
	}
	code, _, _ := closeInfo(t, conn)
	if code != websocket.StatusPolicyViolation {
		t.Fatalf("封禁断连的关闭码应当是 1008，实际 %d", code)
	}
}

// TestWSBannedTicketClosedWith1008 覆盖 T8-③：票据有效但账号已被封禁。
//
// 场景顺序是刻意的：**先取票、后封禁**。封禁发生在取票之后是常态
// （管理端封禁时，客户端正好拿着刚取到的票在重连）。
//
// 判据有两条，缺一不可：
//   - 升级必须成功（否则浏览器读不到任何原因，只能无限重连）；
//   - 关闭码必须是 1008 且带可读原因（客户端据此提示"账号已被封禁"并停止重连）。
func TestWSBannedTicketClosedWith1008(t *testing.T) {
	env := newWSAccountEnv(t, nil)
	u := env.svc.seeded("bad-guy", "stored-passw0rd", store.RoleUser, store.StatusActive)
	room, _, err := env.rooms.Create("", "", 0)
	if err != nil {
		t.Fatalf("建房失败: %v", err)
	}
	ticket := env.tickets.Issue(u.ID)
	env.svc.ban(u.ID) // 取票之后被封禁

	conn, resp, err := dialWS(t, env.wsURL(room.ID, "c1", ticket))
	if err != nil {
		t.Fatalf("被封禁的账号也应当先完成升级再用 1008 关闭（HTTP 401 在浏览器里读不到原因）: %v（resp=%v）", err, resp)
	}
	defer conn.CloseNow()

	code, reason, envCode := closeInfo(t, conn)
	if code != websocket.StatusPolicyViolation {
		t.Fatalf("封禁账号的连接必须以 1008 关闭，实际 %d（reason=%q）", code, reason)
	}
	if !strings.Contains(reason, "封禁") {
		t.Fatalf("关闭原因必须让客户端能提示「账号已被封禁」，实际 %q", reason)
	}
	if envCode != CodeUserBanned {
		t.Fatalf("关闭前应当先下发 %s 错误信封，实际 %q", CodeUserBanned, envCode)
	}
	// 被拒的连接绝不能注册进 Hub（否则它会成为一条可用的信令连接）。
	if n := env.hub.RoomSize(room.ID); n != 0 {
		t.Fatalf("被拒的连接不得注册进 Hub，实际房内 %d 条", n)
	}
}

// TestWSTicketIsSingleUse 覆盖 T8-④：同一张票据用第二次必须被拒。
//
// 票据是**单次**凭据（ACCOUNTS §5）：第二次使用要么是客户端 bug，要么是票据泄漏。
// 无论哪种，正确反应都是拒绝并让客户端重新取票 —— 复用会把它退化成一个
// 长期放在浏览器历史里的可重放凭据。
func TestWSTicketIsSingleUse(t *testing.T) {
	env := newWSAccountEnv(t, nil)
	u := env.svc.seeded("alice", "stored-passw0rd", store.RoleUser, store.StatusActive)
	room, _, err := env.rooms.Create("", "", 0)
	if err != nil {
		t.Fatalf("建房失败: %v", err)
	}
	ticket := env.tickets.Issue(u.ID)

	first, _, err := dialWS(t, env.wsURL(room.ID, "c1", ticket))
	if err != nil {
		t.Fatalf("第一次使用票据应当成功: %v", err)
	}
	defer first.CloseNow()

	_, resp, err := dialWS(t, env.wsURL(room.ID, "c2", ticket))
	if err == nil {
		t.Fatal("同一张票据第二次使用必须被拒")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("复用票据应当 401，实际 %v", resp)
	}
	defer resp.Body.Close()
	body := readBody(t, resp)
	if !strings.Contains(body, CodeWSTicketInvalid) {
		t.Fatalf("响应应当带 %s，实际 %s", CodeWSTicketInvalid, body)
	}
}

// TestWSTicketRejectedWhenInvalidOrExpired 覆盖三种"票据不可用"的形态。
func TestWSTicketRejectedWhenInvalidOrExpired(t *testing.T) {
	// TTL 压到 1ms：过期是"时间"而不是"内容"决定的，用真表才能验到。
	env := newWSAccountEnv(t, func(cfg *config.Config) { cfg.Auth.WSTicketTTL = time.Millisecond })
	u := env.svc.seeded("alice", "stored-passw0rd", store.RoleUser, store.StatusActive)
	room, _, err := env.rooms.Create("", "", 0)
	if err != nil {
		t.Fatalf("建房失败: %v", err)
	}

	expired := env.tickets.Issue(u.ID)
	time.Sleep(10 * time.Millisecond) // 超过 1ms TTL

	cases := []struct{ name, ticket string }{
		{"乱造的票据", "not-a-real-ticket"},
		{"已过期", expired},
		{"超长（>128 字节）", strings.Repeat("a", 200)},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// clientId 用 ASCII 编号：用例名里有空格/全角字符，直接拼进 URL 会让
			// 请求行本身非法（服务端回 400），那验的就不是票据路径了。
			_, resp, err := dialWS(t, env.wsURL(room.ID, fmt.Sprintf("c%d", i), tc.ticket))
			if err == nil {
				t.Fatal("坏票据必须被拒（握手阶段）")
			}
			if resp == nil || resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("坏票据应当 401，实际 %v", resp)
			}
			defer resp.Body.Close()
			if body := readBody(t, resp); !strings.Contains(body, CodeWSTicketInvalid) {
				t.Fatalf("响应应当带 %s，实际 %s", CodeWSTicketInvalid, body)
			}
		})
	}

	// 空票据（`?ticket=`）走**严格**路径而不是降级成游客：一次失败的鉴权
	// 静默变成匿名连接，会让客户端以为自己在用账号，而服务端按游客对待。
	emptyTicketURL := "ws" + strings.TrimPrefix(env.srv.URL, "http") +
		"/ws?roomId=" + room.ID + "&clientId=empty-ticket&ticket="
	_, resp, err := dialWS(t, emptyTicketURL)
	if err == nil {
		t.Fatal("空票据必须被拒（不得静默降级成游客）")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("空票据应当 401，实际 %v", resp)
	}
	resp.Body.Close()
}

// TestWSTicketRequiresAccountCapability 覆盖"带了票据但账号能力没装配"：
// 必须是 503（装配问题）而不是 401（客户端凭据问题）——
// 报错归因错了，排障会一路查向客户端。
func TestWSTicketRequiresAccountCapability(t *testing.T) {
	cfg := config.Default()
	cfg.Auth.DBDSN = "host=127.0.0.1 dbname=fixture sslmode=disable"
	hub, cleanup, err := service.NewHub(cfg)
	if err != nil {
		t.Fatalf("构造 Hub 失败: %v", err)
	}
	rooms := usecase.NewManager(cfg, hub)
	// 空的 AuthDeps：Tickets/Service 都是 nil。
	srv := httptest.NewServer(NewRouter(cfg, hub, rooms, nil, AuthDeps{}, AdminDeps{}))
	t.Cleanup(func() { srv.Close(); rooms.Stop(); cleanup() })

	room, _, err := rooms.Create("", "", 0)
	if err != nil {
		t.Fatalf("建房失败: %v", err)
	}
	_, resp, err := dialWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws?roomId="+room.ID+"&clientId=c1&ticket=whatever")
	if err == nil {
		t.Fatal("账号能力未装配时带票握手必须被拒")
	}
	if resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("应当 503（装配问题），实际 %v", resp)
	}
	resp.Body.Close()
}

// TestWSWithoutTicketIsStillGuest 是 T8 的**游客回归**：
// 没有 ticket 参数时 /ws 与账号层上线前完全一致（观众免登录进房是产品决定）。
//
// 断言到"进房"这一层：连接建立 + 房内可见 + 在线用户数为 0（游客不参与该指标）
// + join 拿到的是 ROOM_NOT_READY（主播未进房）而不是任何鉴权错误。
func TestWSWithoutTicketIsStillGuest(t *testing.T) {
	env := newWSAccountEnv(t, nil)
	room, _, err := env.rooms.Create("", "", 0)
	if err != nil {
		t.Fatalf("建房失败: %v", err)
	}

	// 注意：这里连的是**不带 ticket** 的 URL。
	conn, resp, err := dialWS(t, env.wsURL(room.ID, "guest", ""))
	if err != nil {
		t.Fatalf("游客连接必须仍然可用: %v（resp=%v）", err, resp)
	}
	defer conn.CloseNow()

	waitFor(t, "游客连接注册进房间", func() bool { return env.hub.RoomSize(room.ID) == 1 })
	if _, users := env.hub.Stats(); users != 0 {
		t.Fatalf("游客连接不得被算进在线用户（实际 %d）", users)
	}

	// 游客路径的端到端证据：join 能走到房间逻辑（得到"主播尚未进房"而不是鉴权错误）。
	client := &wsClient{t: t, conn: conn}
	client.join("游客", model.RoleViewer, "")
	if env2 := client.readUntil(model.TypeError); env2.Code != model.CodeRoomNotReady {
		t.Fatalf("游客 join 应当得到 %s（主播未进房），实际 %q", model.CodeRoomNotReady, env2.Code)
	}
}

// TestWSGuestWorksWhenAccountsDisabled 是"账号能力关闭"（默认部署形态：PR_DB_DSN 为空）
// 下最重要的一条兼容性保证：**没有 ticket 的 /ws 必须照旧可用**。
//
// 这条路径上 deps.Tickets 与 deps.Service 都是 nil，而 resolveWSTicket 在
// "请求里没有 ticket 参数"时就返回了 —— 因此空的 AuthDeps 完全不影响游客。
// 这条用例是"账号层引入了鉴权"之后最容易回归的地方（早期实现随手加一个
// `if deps.Service == nil { 503 }` 就会把游客一起关掉）。
func TestWSGuestWorksWhenAccountsDisabled(t *testing.T) {
	cfg := config.Default() // Auth.DBDSN 为空 = 账号能力关闭
	hub, cleanup, err := service.NewHub(cfg)
	if err != nil {
		t.Fatalf("构造 Hub 失败: %v", err)
	}
	rooms := usecase.NewManager(cfg, hub)
	srv := httptest.NewServer(NewRouter(cfg, hub, rooms, nil, AuthDeps{}, AdminDeps{}))
	t.Cleanup(func() { srv.Close(); rooms.Stop(); cleanup() })

	room, _, err := rooms.Create("", "", 0)
	if err != nil {
		t.Fatalf("建房失败: %v", err)
	}

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws?roomId=" + room.ID + "&clientId=guest"
	conn, resp, err := dialWS(t, url)
	if err != nil {
		t.Fatalf("账号能力关闭时游客必须仍能连上 /ws: %v（resp=%v）", err, resp)
	}
	defer conn.CloseNow()

	client := &wsClient{t: t, conn: conn}
	client.join("游客", model.RoleViewer, "")
	if env := client.readUntil(model.TypeError); env.Code != model.CodeRoomNotReady {
		t.Fatalf("游客 join 应当得到 %s（主播未进房），实际 %q", model.CodeRoomNotReady, env.Code)
	}
}

// readBody 读取响应体文本（错误报告里要用到它）。
func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Sprintf("（读取响应体失败: %v）", err)
	}
	return string(b)
}
