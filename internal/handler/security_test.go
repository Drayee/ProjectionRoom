package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/model"
	"ProjectionRoom/internal/usecase"
)

// —— S-5：WebSocket Origin 校验 ——

// TestWSOriginRejectsSelfHostedForgery 是 S-5 的核心回归测试。
//
// 修复前的实证：`Origin: http://evil.com` + `Host: evil.com` → 101（升级成功）。
// 原因是 wsHandler 把 c.Request.Host 追加进了白名单，而 Origin 与 Host
// 都是攻击者完全可控的请求头 —— 于是"禁止跨站页面连 WS"这条防线在任意域名上失效
// （DNS rebinding 的经典形态：攻击者让受害者的浏览器把 evil.com 解析到本机）。
//
// 修复后：白名单只来自显式配置，因此同一种请求必须 403。
func TestWSOriginRejectsSelfHostedForgery(t *testing.T) {
	cfg := config.Default()
	cfg.Signal.AllowedOrigins = []string{"allowed.example"}

	srv, _, _ := startTestServerWithManager(t, cfg)

	status, line := rawWSDial(t, srv, "/ws?roomId=ABCD12&clientId=c1", map[string]string{
		"Origin": "http://evil.com",
		"Host":   "evil.com",
	})
	if status == http.StatusSwitchingProtocols {
		t.Fatalf("Origin=evil.com + Host=evil.com 必须被拒（这就是 S-5 的绕过）：实际 %s", line)
	}
	if status != http.StatusForbidden {
		t.Fatalf("伪造同源应返回 403，实际 %d（%s）", status, line)
	}

	// 反向确认这条请求本来是"合法形态"：只把 Origin 换成白名单里的值，
	// 同一对 (Host, roomId, clientId) 必须能升级成功。
	status, line = rawWSDial(t, srv, "/ws?roomId=ABCD12&clientId=c2", map[string]string{
		"Origin": "http://allowed.example",
		"Host":   "allowed.example",
	})
	if status != http.StatusSwitchingProtocols {
		t.Fatalf("白名单内的 Origin 应升级成功（101），实际 %d（%s）", status, line)
	}
}

// TestWSOriginAllowsNonBrowserClient 锁定"无 Origin 仍放行"这条**有意保留**的现状。
//
// 为什么保留：本仓库的本地工具链（curl、verify-*.mjs）都不发 Origin，
// 而它们连的是本机端口。代价是"任何能连到端口的人都能发 join 指令"，
// 因此账号层上线后这条放行必须改成要求令牌（ws.go 的 joinedOriginTodo 与
// docs/SPEC.md 的安全待办各记了一份）。
func TestWSOriginAllowsNonBrowserClient(t *testing.T) {
	cfg := config.Default()
	cfg.Signal.AllowedOrigins = []string{"allowed.example"}

	srv, _, _ := startTestServerWithManager(t, cfg)

	status, line := rawWSDial(t, srv, "/ws?roomId=ABCD12&clientId=c3", nil)
	if status != http.StatusSwitchingProtocols {
		t.Fatalf("非浏览器客户端（无 Origin）应保持放行（101），实际 %d（%s）", status, line)
	}
}

// rawWSDial 发一次原始 WebSocket 握手，返回 HTTP 状态码与状态行。
// 用裸 socket 而不是 websocket.Dial：只有这样才能自由构造 Origin/Host 头
// （coder/websocket 会覆盖 Host）。
func rawWSDial(t *testing.T, srv *httptest.Server, path string, headers map[string]string) (int, string) {
	t.Helper()

	addr := strings.TrimPrefix(srv.URL, "http://")
	conn, err := net.DialTimeout("tcp", addr, testTimeout)
	if err != nil {
		t.Fatalf("连接测试服务失败: %v", err)
	}
	defer func() { _ = conn.Close() }()

	host := addr
	lines := []string{
		"GET " + path + " HTTP/1.1",
		"Upgrade: websocket",
		"Connection: Upgrade",
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==",
		"Sec-WebSocket-Version: 13",
	}
	for k, v := range headers {
		if strings.EqualFold(k, "Host") {
			host = v
			continue
		}
		lines = append(lines, k+": "+v)
	}
	lines = append(lines, "Host: "+host, "", "")

	if _, err := conn.Write([]byte(strings.Join(lines, "\r\n"))); err != nil {
		t.Fatalf("写握手请求失败: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(testTimeout))
	buf := make([]byte, 2048)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("读握手响应失败: %v", err)
	}
	statusLine := strings.SplitN(string(buf[:n]), "\r\n", 2)[0]
	var code int
	if _, err := fmt.Sscanf(statusLine, "HTTP/1.1 %d", &code); err != nil {
		t.Fatalf("无法解析状态行 %q: %v", statusLine, err)
	}
	return code, statusLine
}

// —— S-3：房间码白名单 ——

// TestCreateRoomRejectsIllegalRoomCode 覆盖建房校验：
// 审计实证 60 KB 的 roomId 曾经返回 200；修复后必须 400 且给明确文案。
func TestCreateRoomRejectsIllegalRoomCode(t *testing.T) {
	srv, _, _ := startTestServerWithManager(t, config.Default())

	cases := []struct{ name, roomID string }{
		{"超长（60 KB）", strings.Repeat("A", 60*1024)},
		{"过短", "ABC"},
		{"过长（13 位）", "ABCDEFGHIJKLM"},
		{"含连字符", "AB-CD"},
		{"含空格", "AB CD"},
		{"含中文", "房间码"},
		{"含路径穿越字符", "../../etc/passwd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := postJSON(t, srv.URL+"/api/rooms", map[string]any{"roomId": tc.roomID})
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("非法房间码（%s）应返回 400，实际 %d", tc.name, resp.StatusCode)
			}
			var body struct {
				Error string `json:"error"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&body)
			if !strings.Contains(body.Error, "4-12") {
				t.Fatalf("400 的文案应说明房间码规则，实际 %q", body.Error)
			}
		})
	}

	// 规范化仍然生效：小写会被折成大写后按白名单校验（合法）。
	resp := postJSON(t, srv.URL+"/api/rooms", map[string]any{"roomId": "room01"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("小写房间码应被规范化为大写后接受，实际 %d", resp.StatusCode)
	}
	var created struct {
		RoomID string `json:"roomId"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&created)
	if created.RoomID != "ROOM01" {
		t.Fatalf("房间码应规范化为大写 ROOM01，实际 %q", created.RoomID)
	}
}

// TestWSRejectsIllegalRoomCode 锁定"两条入口同一把尺子"：/ws 也必须校验房间码。
func TestWSRejectsIllegalRoomCode(t *testing.T) {
	srv, _, _ := startTestServerWithManager(t, config.Default())

	resp, err := http.Get(srv.URL + "/ws?roomId=" + strings.Repeat("A", 60*1024) + "&clientId=c1")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("/ws 上的非法房间码应返回 400，实际 %d", resp.StatusCode)
	}
}

// TestCreateRoomRejectsWeakPassword 覆盖 S-11 的密码长度策略。
func TestCreateRoomRejectsWeakPassword(t *testing.T) {
	srv, _, _ := startTestServerWithManager(t, config.Default())

	for _, pw := range []string{"a", "ab", "abc"} {
		resp := postJSON(t, srv.URL+"/api/rooms", map[string]any{"password": pw})
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%d 位密码应被拒（策略 0 或 4-64），实际 %d", len(pw), resp.StatusCode)
		}
	}
	for _, pw := range []string{"", "abcd", strings.Repeat("x", 64)} {
		resp := postJSON(t, srv.URL+"/api/rooms", map[string]any{"password": pw})
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%d 位密码应被接受，实际 %d", len(pw), resp.StatusCode)
		}
	}
}

// TestCreateRoomCountLimitReturns503 覆盖房间总数硬上限（S-3）。
func TestCreateRoomCountLimitReturns503(t *testing.T) {
	cfg := config.Default()
	cfg.Room.MaxRooms = 3
	cfg.IPC.CreateBurst = 100 // 关掉限速的干扰，本用例只验证"总数上限"

	srv, rooms, _ := startTestServerWithManager(t, cfg)

	for i := 0; i < 3; i++ {
		resp := postJSON(t, srv.URL+"/api/rooms", map[string]any{})
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("第 %d 个房间应创建成功，实际 %d", i+1, resp.StatusCode)
		}
	}
	if rooms.RoomCount() != 3 {
		t.Fatalf("在册房间数应为 3，实际 %d", rooms.RoomCount())
	}

	resp := postJSON(t, srv.URL+"/api/rooms", map[string]any{})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("超出房间总数上限应返回 503，实际 %d", resp.StatusCode)
	}
	var body struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Code != model.CodeTooManyRooms {
		t.Fatalf("应返回 %s，实际 %q", model.CodeTooManyRooms, body.Code)
	}
	if body.Error == "" {
		t.Fatal("503 必须带明确文案")
	}
}

// TestCreateRoomRateLimitReturns429 覆盖每 IP 建房限速（S-3）。
func TestCreateRoomRateLimitReturns429(t *testing.T) {
	cfg := config.Default()
	cfg.IPC.CreatePerMinute = 3 // 3 个/分钟的补充速率 + 容量 3：连续第 4 次必然被拒
	cfg.IPC.CreateBurst = 3
	cfg.Room.MaxRooms = 100 // 不触发总数上限

	srv, _, _ := startTestServerWithManager(t, cfg)

	codes := make([]int, 0, 4)
	for i := 0; i < 4; i++ {
		resp := postJSON(t, srv.URL+"/api/rooms", map[string]any{})
		codes = append(codes, resp.StatusCode)
		resp.Body.Close()
	}
	t.Logf("连续 4 次建房的响应码: %v", codes)
	for i := 0; i < 3; i++ {
		if codes[i] != http.StatusOK {
			t.Fatalf("第 %d 次建房应在令牌桶容量内（200），实际 %d", i+1, codes[i])
		}
	}
	if codes[3] != http.StatusTooManyRequests {
		t.Fatalf("第 4 次建房应被限速（429），实际 %d", codes[3])
	}
}

// —— S-3：房间回收（含宽限期交互，本条最重要）——

// TestSweepReclaimsUnclaimedRoom 覆盖"创建后从未有人进房"的房间被回收。
func TestSweepReclaimsUnclaimedRoom(t *testing.T) {
	cfg := config.Default()
	cfg.Room.UnclaimedRoomTTL = 20 * time.Millisecond
	cfg.Room.SweepInterval = config.MinRoomSweepInterval
	cfg.IPC.CreateBurst = 100

	srv, rooms, _ := startTestServerWithManager(t, cfg)
	roomID := createRoom(t, srv.URL, "")

	if !getRoomInfo(t, srv.URL, roomID).Exists {
		t.Fatal("刚创建的房间必须存在")
	}

	waitRoomGone(t, srv.URL, rooms, roomID, "创建后无人进房的房间应当被回收")
}

// TestSweepKeepsRoomWithinHostGrace 是 S-3 里最重要的那条：
// **宽限期内的房间绝不能被回收**，否则 60s 宽限期就形同虚设
// （主播断线后重连会拿到 ROOM_NOT_FOUND，房间码作废、观众全掉）。
func TestSweepKeepsRoomWithinHostGrace(t *testing.T) {
	cfg := config.Default()
	cfg.Room.HostGrace = 30 * time.Second // 宽限期远长于清扫阈值
	cfg.Room.UnclaimedRoomTTL = 20 * time.Millisecond
	cfg.Room.HostGraceZeroTTL = 20 * time.Millisecond
	cfg.Room.SweepInterval = config.MinRoomSweepInterval

	srv, rooms, _ := startTestServerWithManager(t, cfg)
	roomID := createRoom(t, srv.URL, "")

	host := dial(t, srv, roomID, "host-1")
	host.join("主播", model.RoleHost, "")
	host.readUntil(model.TypeJoined)

	// 主播断线 → 进入宽限期（房内一个人都没有）。
	if err := host.conn.CloseNow(); err != nil {
		t.Fatalf("掐断主播连接失败: %v", err)
	}
	waitRoomHasNoHost(t, srv.URL, roomID)

	// 让清扫跑过很多轮（阈值 20ms，跑满 600ms）。
	deadline := time.Now().Add(600 * time.Millisecond)
	for time.Now().Before(deadline) {
		rooms.SweepOnce()
		time.Sleep(5 * time.Millisecond)
	}

	if info := getRoomInfo(t, srv.URL, roomID); !info.Exists {
		t.Fatal("宽限期内的房间被清扫误杀了：主播重连会拿到 ROOM_NOT_FOUND")
	}

	// 反向确认：只有宽限期结束才允许回收。用配置里的 HostGrace 语义走正规路径 ——
	// 直接把宽限期配成极小值再起一个房间，主播断线后它必须被回收。
	tinyCfg := config.Default()
	tinyCfg.Room.HostGrace = 30 * time.Millisecond
	tinyCfg.Room.SweepInterval = config.MinRoomSweepInterval
	tinyCfg.Room.UnclaimedRoomTTL = 10 * time.Second // 排除"从未进房"这条判据的干扰
	tinyCfg.Room.HostGraceZeroTTL = 10 * time.Second

	tinySrv, tinyRooms, _ := startTestServerWithManager(t, tinyCfg)
	tinyRoom := createRoom(t, tinySrv.URL, "")
	tinyHost := dial(t, tinySrv, tinyRoom, "host-1")
	tinyHost.join("主播", model.RoleHost, "")
	tinyHost.readUntil(model.TypeJoined)
	if err := tinyHost.conn.CloseNow(); err != nil {
		t.Fatalf("掐断连接失败: %v", err)
	}
	waitRoomGone(t, tinySrv.URL, tinyRooms, tinyRoom, "宽限期到期后房间必须被回收（对照组）")
}

// waitRoomGone 轮询到房间消失（HTTP 404 且 Manager 里查不到），并顺带推进清扫。
func waitRoomGone(t *testing.T, baseURL string, rooms *usecase.Manager, roomID, desc string) {
	t.Helper()

	deadline := time.Now().Add(testTimeout)
	for time.Now().Before(deadline) {
		_, inMap := rooms.Get(roomID)
		if !inMap && !getRoomInfo(t, baseURL, roomID).Exists {
			return
		}
		rooms.SweepOnce()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待房间被回收超时：%s", desc)
}

// —— S-7：主播复位令牌 ——

// TestHostRejoinRequiresToken 覆盖 S-7 的四种情形。
func TestHostRejoinRequiresToken(t *testing.T) {
	cfg := config.Default()
	cfg.Room.HostGrace = 30 * time.Second

	srv, _, _ := startTestServerWithManager(t, cfg)
	roomID, hostToken := createRoomFull(t, srv.URL, "")

	if len(hostToken) < 32 {
		t.Fatalf("hostToken 应当足够长（≥32 hex），实际 %q", hostToken)
	}

	// 情形 4：首次 join（房内无主播且非宽限）**不需要**令牌。
	host := dial(t, srv, roomID, "host-1")
	host.hostToken = hostToken
	host.join("主播", model.RoleHost, "")
	host.readUntil(model.TypeJoined)

	// 主播断线 → 宽限期。
	if err := host.conn.CloseNow(); err != nil {
		t.Fatalf("掐断主播连接失败: %v", err)
	}
	waitRoomHasNoHost(t, srv.URL, roomID)

	// 情形 1：宽限期内无令牌抢主播位 → 必须失败，且错误码明确指出原因。
	attacker := dial(t, srv, roomID, "attacker")
	attacker.join("抢主播位的人", model.RoleHost, "")
	if errEnv := attacker.readUntil(model.TypeError); errEnv.Code != model.CodeHostTokenRequired {
		t.Fatalf("无令牌抢主播位应返回 %s，实际 %q（%s）",
			model.CodeHostTokenRequired, errEnv.Code, errEnv.Message)
	}

	// 情形 2：令牌错误 → 必须失败。
	wrong := dial(t, srv, roomID, "attacker2")
	wrong.hostToken = strings.Repeat("f", len(hostToken))
	wrong.join("令牌错误的人", model.RoleHost, "")
	if errEnv := wrong.readUntil(model.TypeError); errEnv.Code != model.CodeBadPassword {
		t.Fatalf("错误令牌应被拒（BAD_PASSWORD），实际 %q", errEnv.Code)
	}

	// 情形 3：正确令牌 → 必须成功接回主播位。
	rejoined := dial(t, srv, roomID, "host-1")
	rejoined.hostToken = hostToken
	rejoined.join("主播", model.RoleHost, "")
	joined := rejoined.readUntil(model.TypeJoined)
	if joined.HostID != "host-1" || joined.SelfID != "host-1" {
		t.Fatalf("带正确令牌应接回主播位: %+v", joined)
	}
}

// TestHostTokenNotRequiredWhenRoomHasHost 锁定不变式：
// 房间已有主播时"抢占"仍然被 HOST_TAKEN 拒绝（与修复前一致）。
func TestHostTokenNotRequiredWhenRoomHasHost(t *testing.T) {
	srv, _, _ := startTestServerWithManager(t, config.Default())
	roomID, hostToken := createRoomFull(t, srv.URL, "")

	host := dial(t, srv, roomID, "host-1")
	host.hostToken = hostToken
	host.join("主播", model.RoleHost, "")
	host.readUntil(model.TypeJoined)

	other := dial(t, srv, roomID, "host-2")
	other.hostToken = hostToken
	other.join("第二主播", model.RoleHost, "")
	if errEnv := other.readUntil(model.TypeError); errEnv.Code != model.CodeHostTaken {
		t.Fatalf("房间已有主播时应返回 HOST_TAKEN，实际 %q", envCode(errEnv))
	}
}

func envCode(e model.Envelope) string { return e.Code }

// —— F-9：安全响应头 ——

// TestSecurityHeaders 锁定全站安全响应头。
func TestSecurityHeaders(t *testing.T) {
	srv, _, _ := startTestServerWithManager(t, config.Default())

	for _, path := range []string{"/healthz", "/api/ice"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s 失败: %v", path, err)
		}
		resp.Body.Close()

		checks := map[string]string{
			"X-Content-Type-Options": "nosniff",
			"X-Frame-Options":        "DENY",
			"Referrer-Policy":        "same-origin",
		}
		for name, want := range checks {
			if got := resp.Header.Get(name); got != want {
				t.Fatalf("GET %s 的 %s 应为 %q，实际 %q", path, name, want, got)
			}
		}

		csp := resp.Header.Get("Content-Security-Policy")
		if csp == "" {
			t.Fatalf("GET %s 缺少 Content-Security-Policy", path)
		}
		// 两条硬约束：Vue 运行时会注入内联 style；MSE 用 blob: 播放。
		for _, want := range []string{
			"style-src 'self' 'unsafe-inline'",
			"media-src 'self' blob:",
			"frame-ancestors 'none'",
			"script-src 'self'",
		} {
			if !strings.Contains(csp, want) {
				t.Fatalf("CSP 缺少 %q，实际 %q", want, csp)
			}
		}
		perms := resp.Header.Get("Permissions-Policy")
		for _, want := range []string{"camera=()", "microphone=()", "geolocation=()"} {
			if !strings.Contains(perms, want) {
				t.Fatalf("Permissions-Policy 缺少 %q，实际 %q", want, perms)
			}
		}
	}
}

// TestSecurityHeadersCanBeDisabled 锁定 PR_SECURITY_HEADERS=0 的语义（给"反代已经加了"的部署用）。
func TestSecurityHeadersCanBeDisabled(t *testing.T) {
	cfg := config.Default()
	cfg.Security.Headers = false

	srv, _, _ := startTestServerWithManager(t, cfg)
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz 失败: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Security-Policy"); got != "" {
		t.Fatalf("关闭开关后不应再输出 CSP，实际 %q", got)
	}
}

// —— S-1：端到端 1009 ——

// TestWSRejectsOversizedFrameWith1009 端到端复现 S-1：
// 用放大帧打真实实例，断言连接被以 1009（报文过大）关闭，而不是被解码。
func TestWSRejectsOversizedFrameWith1009(t *testing.T) {
	cfg := config.Default()
	cfg.Signal.MaxMembersPerMessage = 64

	srv, _, _ := startTestServerWithManager(t, cfg)
	roomID := createRoom(t, srv.URL, "")

	client := dial(t, srv, roomID, "attacker")
	client.sendRaw(amplificationFrame(2000))

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	_, _, err := client.conn.Read(ctx)
	if err == nil {
		t.Fatal("放大帧之后连接应当被关闭")
	}
	if got := websocket.CloseStatus(err); got != websocket.StatusMessageTooBig {
		t.Fatalf("应以 1009（StatusMessageTooBig）关闭，实际 %d（err=%v）", got, err)
	}
}

// amplificationFrame 构造一个"members 重复 n 次"的放大帧（S-1 的实证形态）。
func amplificationFrame(n int) []byte {
	member := appendPBField(nil, 1, []byte("x")) // MemberInfo.id
	frame := appendPBField(nil, 1, []byte("join"))
	for i := 0; i < n; i++ {
		frame = appendPBField(frame, 24, member)
	}
	return frame
}

func appendPBField(dst []byte, num int, raw []byte) []byte {
	dst = appendPBVarint(dst, uint64(num)<<3|2)
	dst = appendPBVarint(dst, uint64(len(raw)))
	return append(dst, raw...)
}

func appendPBVarint(dst []byte, v uint64) []byte {
	for v >= 0x80 {
		dst = append(dst, byte(v)|0x80)
		v >>= 7
	}
	return append(dst, byte(v))
}
