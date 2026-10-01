package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/protocol"
	"ProjectionRoom/internal/room"
	"ProjectionRoom/internal/signal"
)

const testTimeout = 5 * time.Second

// newTestServer 起一个真实的 gin + WebSocket 服务，走完整链路（REST → Hub → room.Manager）。
func newTestServer(t *testing.T) (*httptest.Server, *config.Config) {
	t.Helper()

	cfg := config.Default()
	hub, cleanup, err := signal.NewHub(cfg)
	if err != nil {
		t.Fatalf("构造 Hub 失败: %v", err)
	}
	rooms := room.NewManager(cfg, hub)

	srv := httptest.NewServer(NewRouter(cfg, hub, rooms))
	t.Cleanup(func() {
		srv.Close()
		cleanup()
	})

	return srv, cfg
}

func postJSON(t *testing.T, url string, body any) *http.Response {
	t.Helper()

	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("序列化请求体失败: %v", err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("POST %s 失败: %v", url, err)
	}
	return resp
}

func createRoom(t *testing.T, baseURL, password string) string {
	t.Helper()

	resp := postJSON(t, baseURL+"/api/rooms", map[string]any{"password": password})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("创建房间应返回 200，实际 %d", resp.StatusCode)
	}

	var body struct {
		RoomID string `json:"roomId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("解析创建房间响应失败: %v", err)
	}
	if body.RoomID == "" {
		t.Fatal("创建房间响应缺少 roomId")
	}
	return body.RoomID
}

// wsClient 是测试用的 WebSocket 客户端。
type wsClient struct {
	t    *testing.T
	conn *websocket.Conn
}

func dial(t *testing.T, srv *httptest.Server, roomID, clientID string) *wsClient {
	t.Helper()

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws?roomId=" + roomID + "&clientId=" + clientID
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("连接 /ws 失败: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "test done") })

	return &wsClient{t: t, conn: conn}
}

func (c *wsClient) send(env protocol.Envelope) {
	c.t.Helper()

	payload, err := json.Marshal(env)
	if err != nil {
		c.t.Fatalf("序列化消息失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	if err := c.conn.Write(ctx, websocket.MessageText, payload); err != nil {
		c.t.Fatalf("发送消息失败: %v", err)
	}
}

// readUntil 读出第一条指定类型的消息，跳过其它类型；超时或断连即失败。
func (c *wsClient) readUntil(types ...string) protocol.Envelope {
	c.t.Helper()

	deadline := time.Now().Add(testTimeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Until(deadline))
		typ, data, err := c.conn.Read(ctx)
		cancel()
		if err != nil {
			c.t.Fatalf("读取消息失败（等待 %v）: %v", types, err)
		}
		if typ != websocket.MessageText {
			continue
		}

		var env protocol.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			c.t.Fatalf("解析消息失败: %v (raw=%s)", err, data)
		}
		for _, want := range types {
			if env.Type == want {
				return env
			}
		}
	}

	c.t.Fatalf("等待消息超时: %v", types)
	return protocol.Envelope{}
}

func (c *wsClient) join(displayName, role, password string) {
	c.t.Helper()
	c.send(protocol.Envelope{
		Type:        protocol.TypeJoin,
		DisplayName: displayName,
		Role:        role,
		Password:    password,
	})
}

func TestCreateRoomAPI(t *testing.T) {
	srv, _ := newTestServer(t)

	resp := postJSON(t, srv.URL+"/api/rooms", map[string]any{"password": "pw"})
	defer resp.Body.Close()

	var body struct {
		RoomID     string            `json:"roomId"`
		Capacity   protocol.Capacity `json:"capacity"`
		ICEServers []map[string]any  `json:"iceServers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(body.RoomID) != 6 {
		t.Fatalf("房间码应为 6 位，实际 %q", body.RoomID)
	}
	if body.Capacity.Mode != protocol.ModePending {
		t.Fatalf("M1 阶段容量模式应为 pending，实际 %q", body.Capacity.Mode)
	}
	if len(body.ICEServers) == 0 {
		t.Fatal("创建房间应返回 ICE 配置，前端才能发起 WebRTC")
	}

	info, err := http.Get(srv.URL + "/api/rooms/" + body.RoomID)
	if err != nil {
		t.Fatalf("查询房间失败: %v", err)
	}
	defer info.Body.Close()
	if info.StatusCode != http.StatusOK {
		t.Fatalf("已存在的房间应返回 200，实际 %d", info.StatusCode)
	}

	var infoBody struct {
		Exists    bool `json:"exists"`
		HasHost   bool `json:"hasHost"`
		MemberCnt int  `json:"memberCount"`
	}
	if err := json.NewDecoder(info.Body).Decode(&infoBody); err != nil {
		t.Fatalf("解析房间信息失败: %v", err)
	}
	if !infoBody.Exists || infoBody.HasHost || infoBody.MemberCnt != 0 {
		t.Fatalf("新建房间应存在且无主播: %+v", infoBody)
	}

	missing, err := http.Get(srv.URL + "/api/rooms/ZZZZZZ")
	if err != nil {
		t.Fatalf("查询不存在房间失败: %v", err)
	}
	defer missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("不存在的房间应返回 404，实际 %d", missing.StatusCode)
	}
}

// TestRoomChatControlAndLeave 覆盖 M1 的验收标准：
// 两个客户端进同一房间能聊天、看到成员、房主控制能广播、断线后成员被移除、主播离开房间关闭。
func TestRoomChatControlAndLeave(t *testing.T) {
	srv, _ := newTestServer(t)
	roomID := createRoom(t, srv.URL, "pw")

	host := dial(t, srv, roomID, "host-1")
	host.join("主播", protocol.RoleHost, "pw")
	hostJoined := host.readUntil(protocol.TypeJoined)
	if hostJoined.SelfID != "host-1" || hostJoined.HostID != "host-1" {
		t.Fatalf("主播入房快照不正确: %+v", hostJoined)
	}
	if len(hostJoined.Members) != 1 {
		t.Fatalf("主播入房时应只有自己: %+v", hostJoined.Members)
	}

	viewer := dial(t, srv, roomID, "viewer-1")
	viewer.join("观众", protocol.RoleViewer, "wrong")
	if errEnv := viewer.readUntil(protocol.TypeError); errEnv.Code != protocol.CodeBadPassword {
		t.Fatalf("密码错误应返回 BAD_PASSWORD，实际 %q", errEnv.Code)
	}

	viewer.join("观众", protocol.RoleViewer, "pw")
	viewerJoined := viewer.readUntil(protocol.TypeJoined)
	if len(viewerJoined.Members) != 2 {
		t.Fatalf("观众入房时应看到 2 人: %+v", viewerJoined.Members)
	}

	memberJoined := host.readUntil(protocol.TypeMemberJoined)
	if memberJoined.Member == nil || memberJoined.Member.ID != "viewer-1" || memberJoined.Member.Depth != 1 {
		t.Fatalf("主播应收到 member-joined: %+v", memberJoined)
	}

	// 聊天由服务端定序：发送者自己也会收到这条广播。
	viewer.send(protocol.Envelope{Type: protocol.TypeChat, Text: "一起看"})
	if chat := host.readUntil(protocol.TypeChat); chat.Text != "一起看" || chat.From != "viewer-1" || chat.DisplayName != "观众" {
		t.Fatalf("主播收到的聊天不正确: %+v", chat)
	}
	if chat := viewer.readUntil(protocol.TypeChat); chat.From != "viewer-1" {
		t.Fatalf("发送者应收到服务端定序后的同一条消息: %+v", chat)
	}

	// 观众不能控制播放。
	viewer.send(protocol.Envelope{Type: protocol.TypeRoomControl, Action: protocol.ActionPlay, CurrentTime: 5})
	if errEnv := viewer.readUntil(protocol.TypeError); errEnv.Code != protocol.CodeNotHost {
		t.Fatalf("观众控制应返回 NOT_HOST，实际 %q", errEnv.Code)
	}

	// 主播可以控制，且服务端加盖单调 seq。
	host.send(protocol.Envelope{
		Type:        protocol.TypeRoomControl,
		Action:      protocol.ActionPlay,
		CurrentTime: 5,
		Rate:        1,
	})
	control := viewer.readUntil(protocol.TypeRoomControl)
	if control.Playback == nil || control.Playback.Seq != 1 || control.Playback.CurrentTime != 5 || control.Playback.Paused {
		t.Fatalf("room-control 播放状态不正确: %+v", control.Playback)
	}

	// 迟到的人必须立刻对齐到当前播放位置（SPEC §7.1）。
	late := dial(t, srv, roomID, "viewer-2")
	late.join("迟到观众", protocol.RoleViewer, "pw")
	lateJoined := late.readUntil(protocol.TypeJoined)
	if lateJoined.Playback == nil || lateJoined.Playback.Seq != 1 || lateJoined.Playback.CurrentTime != 5 {
		t.Fatalf("入房快照应携带最新播放状态: %+v", lateJoined.Playback)
	}

	// 跨房信令必须被拒（防止 A 房间连接给 B 房间的人发信令）。
	otherRoom := createRoom(t, srv.URL, "")
	otherHost := dial(t, srv, otherRoom, "other-host")
	otherHost.join("别的主播", protocol.RoleHost, "")
	otherHost.readUntil(protocol.TypeJoined)
	otherHost.send(protocol.Envelope{
		Type:    protocol.TypeSignal,
		To:      "viewer-1",
		Payload: json.RawMessage(`{"sdp":"x"}`),
	})
	if errEnv := otherHost.readUntil(protocol.TypeError); errEnv.Code != protocol.CodeCrossRoom {
		t.Fatalf("跨房信令应返回 CROSS_ROOM_SIGNAL，实际 %q", errEnv.Code)
	}

	// 同房信令可以透传，且带上来源。
	viewer.send(protocol.Envelope{
		Type:    protocol.TypeSignal,
		To:      "host-1",
		Payload: json.RawMessage(`{"type":"offer","sdp":"v=0"}`),
	})
	forwarded := host.readUntil(protocol.TypeSignal)
	if forwarded.From != "viewer-1" || !bytes.Contains(forwarded.Payload, []byte("v=0")) {
		t.Fatalf("信令透传不正确: %+v", forwarded)
	}

	// 观众主动离开：其他人收到 member-left。
	late.send(protocol.Envelope{Type: protocol.TypeLeave})
	if left := viewer.readUntil(protocol.TypeMemberLeft); left.ClientID != "viewer-2" {
		t.Fatalf("应广播 member-left: %+v", left)
	}

	// 主播离开：房间关闭，所有连接收到 room-closed。
	if err := host.conn.Close(websocket.StatusNormalClosure, "host left"); err != nil {
		t.Fatalf("关闭主播连接失败: %v", err)
	}
	if closed := viewer.readUntil(protocol.TypeRoomClosed); closed.Code != protocol.CodeRoomClosed {
		t.Fatalf("主播离开应广播 room-closed: %+v", closed)
	}
}

func TestJoinViewerBeforeHostIsRejected(t *testing.T) {
	srv, _ := newTestServer(t)
	roomID := createRoom(t, srv.URL, "")

	viewer := dial(t, srv, roomID, "early-bird")
	viewer.join("抢跑观众", protocol.RoleViewer, "")
	if errEnv := viewer.readUntil(protocol.TypeError); errEnv.Code != protocol.CodeRoomNotReady {
		t.Fatalf("主播未到时观众进房应返回 ROOM_NOT_READY，实际 %q", errEnv.Code)
	}
}

func TestJoinUnknownRoomIsRejected(t *testing.T) {
	srv, _ := newTestServer(t)

	client := dial(t, srv, "NOSUCH", "lost")
	client.join("迷路观众", protocol.RoleViewer, "")
	if errEnv := client.readUntil(protocol.TypeError); errEnv.Code != protocol.CodeRoomNotFound {
		t.Fatalf("不存在的房间应返回 ROOM_NOT_FOUND，实际 %q", errEnv.Code)
	}
}

func TestDuplicateClientIDIsRejected(t *testing.T) {
	srv, _ := newTestServer(t)
	roomID := createRoom(t, srv.URL, "")

	first := dial(t, srv, roomID, "dup-id")
	first.join("第一个", protocol.RoleHost, "")
	first.readUntil(protocol.TypeJoined)

	second := dial(t, srv, roomID, "dup-id")

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	if _, _, err := second.conn.Read(ctx); err == nil {
		t.Fatal("重复 clientId 的连接应被服务端以策略违规关闭")
	}
}

func TestWebSocketRequiresQueryParams(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, err := http.Get(srv.URL + "/ws?roomId=ABC123")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("缺少 clientId 应返回 400，实际 %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "clientId") {
		t.Fatalf("错误信息应说明缺少的字段: %s", body)
	}
}

func TestHealthz(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("健康检查失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("健康检查应返回 200，实际 %d", resp.StatusCode)
	}
}
