package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/model"
	"ProjectionRoom/internal/service"
	"ProjectionRoom/internal/usecase"
)

// sampleIndex 是 M2 用的自洽索引，覆盖索引发布与容量计算两条链路。
func sampleIndex(bitrateBps int64) model.Index {
	return model.Index{
		Version:       1,
		InitFile:      "init.mp4",
		MimeType:      `video/mp4; codecs="avc1.64001f,mp4a.40.2"`,
		TotalDuration: 4,
		SegmentSec:    2,
		BitrateBps:    bitrateBps,
		TotalBytes:    bitrateBps / 2,
		Segments: []model.Segment{
			{Index: 1, File: "c00001.m4s", Size: 1000, Duration: 2, StartPTS: 0, Keyframe: true},
			{Index: 2, File: "c00002.m4s", Size: 1000, Duration: 2, StartPTS: 2, Keyframe: true},
		},
	}
}

const testTimeout = 5 * time.Second

// startTestServer 用给定配置起一个真实的 gin + WebSocket 服务。
func startTestServer(t *testing.T, cfg *config.Config) *httptest.Server {
	t.Helper()

	hub, cleanup, err := service.NewHub(cfg)
	if err != nil {
		t.Fatalf("构造 Hub 失败: %v", err)
	}
	rooms := usecase.NewManager(cfg, hub)

	// 信令用例不涉及切片端点，这里传 nil 跳过 /api/v1/segment/* 的注册。
	srv := httptest.NewServer(NewRouter(cfg, hub, rooms, nil))
	t.Cleanup(func() {
		srv.Close()
		cleanup()
	})

	return srv
}

// newTestServer 起一个真实的 gin + WebSocket 服务，走完整链路（REST → Hub → usecase.Manager）。
func newTestServer(t *testing.T) (*httptest.Server, *config.Config) {
	t.Helper()

	return newTestServerWithGrace(t, config.DefaultHostGrace)
}

// newTestServerWithGrace 用指定的主播断线宽限期起服务。
// 默认 60s 对"断言宽限期到期"的用例太慢，需要它的用例把宽限期压到几百毫秒。
func newTestServerWithGrace(t *testing.T, grace time.Duration) (*httptest.Server, *config.Config) {
	t.Helper()

	cfg := config.Default()
	cfg.Room.HostGrace = grace

	return startTestServer(t, cfg), cfg
}

// newTestServerFromEnv 走 config.Load()（含 PR_* 环境变量）起服务：
// 用于验证环境变量真的作用到了运行期行为，而不是只落在配置结构体里。
func newTestServerFromEnv(t *testing.T) (*httptest.Server, *config.Config) {
	t.Helper()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}

	return startTestServer(t, cfg), cfg
}

// roomInfo 是 GET /api/rooms/:roomId 的可判定字段。
type roomInfo struct {
	Status      int
	Exists      bool `json:"exists"`
	HasHost     bool `json:"hasHost"`
	MemberCount int  `json:"memberCount"`
}

// getRoomInfo 查询房间信息；房间不存在时 Status=404（Exists 为 false）。
func getRoomInfo(t *testing.T, baseURL, roomID string) roomInfo {
	t.Helper()

	resp, err := http.Get(baseURL + "/api/rooms/" + roomID)
	if err != nil {
		t.Fatalf("查询房间失败: %v", err)
	}
	defer resp.Body.Close()

	var info roomInfo
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
			t.Fatalf("解析房间信息失败: %v", err)
		}
	}
	info.Status = resp.StatusCode

	return info
}

// waitRoomHasNoHost 轮询到"房间存在但没有主播"。
// 主播断线后的 Leave 是连接协程的 defer 里跑的异步过程，断言一次就完事会偶发失败。
func waitRoomHasNoHost(t *testing.T, baseURL, roomID string) roomInfo {
	t.Helper()

	deadline := time.Now().Add(testTimeout)
	for time.Now().Before(deadline) {
		info := getRoomInfo(t, baseURL, roomID)
		if info.Exists && !info.HasHost {
			return info
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待房间进入「存在但无主播」状态超时（最后状态: %+v）", getRoomInfo(t, baseURL, roomID))

	return roomInfo{}
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

func (c *wsClient) send(env model.Envelope) {
	c.t.Helper()

	payload, err := model.Marshal(&env)
	if err != nil {
		c.t.Fatalf("编码 protobuf 消息失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	if err := c.conn.Write(ctx, websocket.MessageBinary, payload); err != nil {
		c.t.Fatalf("发送消息失败: %v", err)
	}
}

// readUntil 读出第一条指定类型的消息，跳过其它类型；超时或断连即失败。
func (c *wsClient) readUntil(types ...string) model.Envelope {
	c.t.Helper()

	deadline := time.Now().Add(testTimeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Until(deadline))
		typ, data, err := c.conn.Read(ctx)
		cancel()
		if err != nil {
			c.t.Fatalf("读取消息失败（等待 %v）: %v", types, err)
		}
		if typ != websocket.MessageBinary {
			continue
		}

		env, err := model.Unmarshal(data)
		if err != nil {
			c.t.Fatalf("解析 protobuf 消息失败: %v", err)
		}
		for _, want := range types {
			if env.Type == want {
				return *env
			}
		}
	}

	c.t.Fatalf("等待消息超时: %v", types)
	return model.Envelope{}
}

func (c *wsClient) join(displayName, role, password string) {
	c.t.Helper()
	c.send(model.Envelope{
		Type:        model.TypeJoin,
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
		RoomID     string           `json:"roomId"`
		Capacity   model.Capacity   `json:"capacity"`
		ICEServers []map[string]any `json:"iceServers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(body.RoomID) != 6 {
		t.Fatalf("房间码应为 6 位，实际 %q", body.RoomID)
	}
	if body.Capacity.Mode != model.ModePending {
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
// 两个客户端进同一房间能聊天、看到成员、房主控制能广播、断线后成员被移除、
// 主播断线进入宽限期、宽限期到期才关闭房间。
func TestRoomChatControlAndLeave(t *testing.T) {
	// 宽限期压到 300ms：这里要验证的是"到期才关房"，真等 60s 会拖死测试。
	srv, _ := newTestServerWithGrace(t, 300*time.Millisecond)
	roomID := createRoom(t, srv.URL, "pw")

	host := dial(t, srv, roomID, "host-1")
	host.join("主播", model.RoleHost, "pw")
	hostJoined := host.readUntil(model.TypeJoined)
	if hostJoined.SelfID != "host-1" || hostJoined.HostID != "host-1" {
		t.Fatalf("主播入房快照不正确: %+v", hostJoined)
	}
	if len(hostJoined.Members) != 1 {
		t.Fatalf("主播入房时应只有自己: %+v", hostJoined.Members)
	}

	viewer := dial(t, srv, roomID, "viewer-1")
	viewer.join("观众", model.RoleViewer, "wrong")
	if errEnv := viewer.readUntil(model.TypeError); errEnv.Code != model.CodeBadPassword {
		t.Fatalf("密码错误应返回 BAD_PASSWORD，实际 %q", errEnv.Code)
	}

	viewer.join("观众", model.RoleViewer, "pw")
	viewerJoined := viewer.readUntil(model.TypeJoined)
	if len(viewerJoined.Members) != 2 {
		t.Fatalf("观众入房时应看到 2 人: %+v", viewerJoined.Members)
	}

	memberJoined := host.readUntil(model.TypeMemberJoined)
	if memberJoined.Member == nil || memberJoined.Member.ID != "viewer-1" || memberJoined.Member.Depth != 1 {
		t.Fatalf("主播应收到 member-joined: %+v", memberJoined)
	}

	// 聊天由服务端定序：发送者自己也会收到这条广播。
	viewer.send(model.Envelope{Type: model.TypeChat, Text: "一起看"})
	if chat := host.readUntil(model.TypeChat); chat.Text != "一起看" || chat.From != "viewer-1" || chat.DisplayName != "观众" {
		t.Fatalf("主播收到的聊天不正确: %+v", chat)
	}
	if chat := viewer.readUntil(model.TypeChat); chat.From != "viewer-1" {
		t.Fatalf("发送者应收到服务端定序后的同一条消息: %+v", chat)
	}

	// 观众不能控制播放。
	viewer.send(model.Envelope{Type: model.TypeRoomControl, Action: model.ActionPlay, CurrentTime: 5})
	if errEnv := viewer.readUntil(model.TypeError); errEnv.Code != model.CodeNotHost {
		t.Fatalf("观众控制应返回 NOT_HOST，实际 %q", errEnv.Code)
	}

	// 主播可以控制，且服务端加盖单调 seq。
	host.send(model.Envelope{
		Type:        model.TypeRoomControl,
		Action:      model.ActionPlay,
		CurrentTime: 5,
		Rate:        1,
	})
	control := viewer.readUntil(model.TypeRoomControl)
	if control.Playback == nil || control.Playback.Seq != 1 || control.Playback.CurrentTime != 5 || control.Playback.Paused {
		t.Fatalf("room-control 播放状态不正确: %+v", control.Playback)
	}

	// 迟到的人必须立刻对齐到当前播放位置（SPEC §7.1）。
	late := dial(t, srv, roomID, "viewer-2")
	late.join("迟到观众", model.RoleViewer, "pw")
	lateJoined := late.readUntil(model.TypeJoined)
	if lateJoined.Playback == nil || lateJoined.Playback.Seq != 1 || lateJoined.Playback.CurrentTime != 5 {
		t.Fatalf("入房快照应携带最新播放状态: %+v", lateJoined.Playback)
	}

	// 跨房信令必须被拒（防止 A 房间连接给 B 房间的人发信令）。
	otherRoom := createRoom(t, srv.URL, "")
	otherHost := dial(t, srv, otherRoom, "other-host")
	otherHost.join("别的主播", model.RoleHost, "")
	otherHost.readUntil(model.TypeJoined)
	otherHost.send(model.Envelope{
		Type:    model.TypeSignal,
		To:      "viewer-1",
		Payload: json.RawMessage(`{"sdp":"x"}`),
	})
	if errEnv := otherHost.readUntil(model.TypeError); errEnv.Code != model.CodeCrossRoom {
		t.Fatalf("跨房信令应返回 CROSS_ROOM_SIGNAL，实际 %q", errEnv.Code)
	}

	// 同房信令可以透传，且带上来源。
	viewer.send(model.Envelope{
		Type:    model.TypeSignal,
		To:      "host-1",
		Payload: json.RawMessage(`{"type":"offer","sdp":"v=0"}`),
	})
	forwarded := host.readUntil(model.TypeSignal)
	if forwarded.From != "viewer-1" || !bytes.Contains(forwarded.Payload, []byte("v=0")) {
		t.Fatalf("信令透传不正确: %+v", forwarded)
	}

	// 观众主动离开：其他人收到 member-left。
	late.send(model.Envelope{Type: model.TypeLeave})
	if left := viewer.readUntil(model.TypeMemberLeft); left.ClientID != "viewer-2" {
		t.Fatalf("应广播 member-left: %+v", left)
	}

	// 主播断线（不是离开）：观众先收到 member-left，房间进入宽限期而不是立刻销毁。
	if err := host.conn.Close(websocket.StatusNormalClosure, "host left"); err != nil {
		t.Fatalf("关闭主播连接失败: %v", err)
	}
	if left := viewer.readUntil(model.TypeMemberLeft); left.ClientID != "host-1" {
		t.Fatalf("主播断线应广播 member-left: %+v", left)
	}

	// 宽限期（本用例配 300ms）到期仍无主播，才广播 room-closed 并关闭连接。
	if closed := viewer.readUntil(model.TypeRoomClosed); closed.Code != model.CodeRoomClosed {
		t.Fatalf("宽限期到期应广播 room-closed: %+v", closed)
	}

	// 房间至此真正作废：HTTP 侧 404。
	deadline := time.Now().Add(testTimeout)
	for time.Now().Before(deadline) {
		if info := getRoomInfo(t, srv.URL, roomID); !info.Exists {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("宽限期到期后房间码必须作废（GET /api/rooms 应 404）")
}

// TestHostDisconnectGraceAndReconnect 是这次修复的回归测试：
// 主播 WS 断一次（网络抖动/刷新/半开连接）后房间必须保留，主播凭同一 clientId 重连即可恢复，
// 且观众还在房里、播放 seq 与分片索引都没有丢。
func TestHostDisconnectGraceAndReconnect(t *testing.T) {
	srv, cfg := newTestServerWithGrace(t, 5*time.Second)
	if cfg.Room.HostGrace != 5*time.Second {
		t.Fatalf("前置条件不成立：宽限期应为 5s，实际 %v", cfg.Room.HostGrace)
	}
	roomID := createRoom(t, srv.URL, "pw")

	host := dial(t, srv, roomID, "host-1")
	host.join("主播", model.RoleHost, "pw")
	host.readUntil(model.TypeJoined)

	viewer := dial(t, srv, roomID, "viewer-1")
	viewer.join("观众", model.RoleViewer, "pw")
	viewer.readUntil(model.TypeJoined)

	index := sampleIndex(2_000_000)
	host.send(model.Envelope{Type: model.TypeMediaIndex, MediaIndex: &index})
	viewer.readUntil(model.TypeMediaIndex)

	host.send(model.Envelope{Type: model.TypeRoomControl, Action: model.ActionPlay, CurrentTime: 5, Rate: 1})
	if control := viewer.readUntil(model.TypeRoomControl); control.Playback == nil || control.Playback.Seq != 1 {
		t.Fatalf("主播控制应带上 seq=1: %+v", control.Playback)
	}

	// 模拟网络抖动 / 页面刷新：直接掐断 TCP，不发关闭帧。
	if err := host.conn.CloseNow(); err != nil {
		t.Fatalf("掐断主播连接失败: %v", err)
	}
	if left := viewer.readUntil(model.TypeMemberLeft); left.ClientID != "host-1" {
		t.Fatalf("主播断线应广播 member-left: %+v", left)
	}

	// 宽限期内房间仍然存在，只是"当前没有主播"（HTTP 可判定），观众仍留在房里。
	info := waitRoomHasNoHost(t, srv.URL, roomID)
	if info.MemberCount != 1 {
		t.Fatalf("宽限期内观众必须留在房里: %+v", info)
	}

	// 同一次抖动里掉线的**观众**也必须能回房：拿到保留的分片索引与断线前的播放状态，
	// hostId 为空 + members 里没有 host，客户端据此显示"等待主播重连"。
	late := dial(t, srv, roomID, "viewer-2")
	late.join("迟到观众", model.RoleViewer, "pw")
	lateJoined := late.readUntil(model.TypeJoined)
	if lateJoined.HostID != "" {
		t.Fatalf("主播离线期间入房快照的 hostId 必须为空: %+v", lateJoined)
	}
	if lateJoined.MediaIndex == nil || len(lateJoined.MediaIndex.Segments) != 2 {
		t.Fatalf("宽限期内进房的观众必须拿到保留的分片索引: %+v", lateJoined.MediaIndex)
	}
	if lateJoined.Playback == nil || lateJoined.Playback.Seq != 1 || lateJoined.Playback.CurrentTime != 5 {
		t.Fatalf("宽限期内进房的观众必须拿到断线前的播放状态: %+v", lateJoined.Playback)
	}
	if left := viewer.readUntil(model.TypeMemberJoined); left.Member == nil || left.Member.ID != "viewer-2" {
		t.Fatalf("房内观众应收到迟到观众的 member-joined: %+v", left)
	}

	// 主播用同一个 clientId 重连：必须拿回房间，而不是 ROOM_NOT_FOUND。
	rejoined := dial(t, srv, roomID, "host-1")
	rejoined.join("主播", model.RoleHost, "pw")
	joined := rejoined.readUntil(model.TypeJoined)
	if joined.SelfID != "host-1" || joined.HostID != "host-1" {
		t.Fatalf("主播重连后应恢复房主身份: %+v", joined)
	}
	if joined.Playback == nil || joined.Playback.Seq != 1 || joined.Playback.CurrentTime != 5 || joined.Playback.Paused {
		t.Fatalf("恢复后播放状态必须延续（seq 不得回退）: %+v", joined.Playback)
	}
	if joined.MediaIndex == nil || len(joined.MediaIndex.Segments) != 2 {
		t.Fatalf("恢复后分片索引必须仍在: %+v", joined.MediaIndex)
	}
	if len(joined.Members) != 3 {
		t.Fatalf("恢复后成员表应含主播与两名观众: %+v", joined.Members)
	}

	// 观众收到 member-joined，知道主播回来了。
	if mj := viewer.readUntil(model.TypeMemberJoined); mj.Member == nil || mj.Member.ID != "host-1" {
		t.Fatalf("观众应收到主播的 member-joined: %+v", mj)
	}
	if after := getRoomInfo(t, srv.URL, roomID); !after.Exists || !after.HasHost {
		t.Fatalf("恢复后房间应存在且有主播: %+v", after)
	}
}

// TestHostGraceFromEnvDrivesLifecycle 是"配置 → Load → Manager → 运行期行为"的端到端检查：
// 只把 PR_ROOM_HOST_GRACE 配成 300ms，主播断线后房间就应保留约 300ms 再销毁。
// 其余用例直接改 cfg 字段，这一条专门证明环境变量真的生效。
func TestHostGraceFromEnvDrivesLifecycle(t *testing.T) {
	t.Setenv("PR_ROOM_HOST_GRACE", "300ms")

	srv, cfg := newTestServerFromEnv(t)
	if cfg.Room.HostGrace != 300*time.Millisecond {
		t.Fatalf("PR_ROOM_HOST_GRACE=300ms 应落在配置里，实际 %v", cfg.Room.HostGrace)
	}

	roomID := createRoom(t, srv.URL, "")
	host := dial(t, srv, roomID, "host-1")
	host.join("主播", model.RoleHost, "")
	host.readUntil(model.TypeJoined)

	viewer := dial(t, srv, roomID, "viewer-1")
	viewer.join("观众", model.RoleViewer, "")
	viewer.readUntil(model.TypeJoined)

	// 主播断线：刚断开的一瞬间房间必须还在（这就是"断线不清房"）。
	if err := host.conn.CloseNow(); err != nil {
		t.Fatalf("掐断主播连接失败: %v", err)
	}
	if info := getRoomInfo(t, srv.URL, roomID); !info.Exists {
		t.Fatal("宽限期内房间不得被销毁")
	}

	// 300ms 到期后：观众收到 room-closed，房间码作废。
	if closed := viewer.readUntil(model.TypeRoomClosed); closed.Code != model.CodeRoomClosed {
		t.Fatalf("宽限期到期应广播 room-closed: %+v", closed)
	}
	deadline := time.Now().Add(testTimeout)
	for time.Now().Before(deadline) {
		if info := getRoomInfo(t, srv.URL, roomID); !info.Exists {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("宽限期到期后房间码必须作废（GET /api/rooms 应 404）")
}

func TestJoinViewerBeforeHostIsRejected(t *testing.T) {
	srv, _ := newTestServer(t)
	roomID := createRoom(t, srv.URL, "")

	viewer := dial(t, srv, roomID, "early-bird")
	viewer.join("抢跑观众", model.RoleViewer, "")
	if errEnv := viewer.readUntil(model.TypeError); errEnv.Code != model.CodeRoomNotReady {
		t.Fatalf("主播未到时观众进房应返回 ROOM_NOT_READY，实际 %q", errEnv.Code)
	}
}

func TestJoinUnknownRoomIsRejected(t *testing.T) {
	srv, _ := newTestServer(t)

	client := dial(t, srv, "NOSUCH", "lost")
	client.join("迷路观众", model.RoleViewer, "")
	if errEnv := client.readUntil(model.TypeError); errEnv.Code != model.CodeRoomNotFound {
		t.Fatalf("不存在的房间应返回 ROOM_NOT_FOUND，实际 %q", errEnv.Code)
	}
}

// TestDuplicateClientIDGetsErrorEnvelope 锁定"重复 clientId"的自我修复路径：
// 服务端必须**先**下发 CLIENT_ID_TAKEN 错误帧，再以策略违规关闭 ——
// 否则客户端只看到一次莫名的关闭，除了拿同一个 clientId 空转重试别无选择。
//
// 同时验证被拒的连接不会误伤原有连接：它从未注册进 Hub，也就不能走 Leave
// （否则会把仍然活着的主播当成"离开"，把整个房间推进宽限期）。
func TestDuplicateClientIDGetsErrorEnvelope(t *testing.T) {
	srv, _ := newTestServer(t)
	roomID := createRoom(t, srv.URL, "")

	first := dial(t, srv, roomID, "dup-id")
	first.join("第一个", model.RoleHost, "")
	first.readUntil(model.TypeJoined)

	second := dial(t, srv, roomID, "dup-id")

	// 错误帧必须先到：readUntil 在读不到时直接失败，因此这条断言也覆盖了"先写后关"的顺序。
	errEnv := second.readUntil(model.TypeError)
	if errEnv.Code != model.CodeClientIDTaken {
		t.Fatalf("重复 clientId 应收到 %s，实际 %q（%s）", model.CodeClientIDTaken, errEnv.Code, errEnv.Message)
	}
	if errEnv.Message == "" {
		t.Fatal("CLIENT_ID_TAKEN 必须带上可读的 message，客户端才知道该换 id")
	}

	// 随后连接被以策略违规关闭。
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	if _, _, err := second.conn.Read(ctx); err == nil {
		t.Fatal("下发错误帧后应关闭这条连接")
	} else if got := websocket.CloseStatus(err); got != websocket.StatusPolicyViolation {
		t.Fatalf("应以策略违规（%d）关闭，实际 %d（err=%v）", websocket.StatusPolicyViolation, got, err)
	}

	// 原有连接不受影响：它仍是房主、房间里只有一个人，房间也没进入宽限期。
	first.send(model.Envelope{Type: model.TypeChat, Text: "我还在"})
	if chat := first.readUntil(model.TypeChat); chat.Text != "我还在" {
		t.Fatalf("原连接不应被重复 clientId 的拒绝流程影响: %+v", chat)
	}
	info := getRoomInfo(t, srv.URL, roomID)
	if !info.Exists || !info.HasHost || info.MemberCount != 1 {
		t.Fatalf("被拒连接不得触发原有连接的 Leave: %+v", info)
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

// TestMediaIndexAndCapacityFlow 覆盖 M2 的服务端链路：
// 主播发布索引 → 全房可见；主播上报实测上行 → 容量重算并广播；超出 1+K0 的人被拒。
func TestMediaIndexAndCapacityFlow(t *testing.T) {
	srv, _ := newTestServer(t)
	roomID := createRoom(t, srv.URL, "")

	host := dial(t, srv, roomID, "host-1")
	host.join("主播", model.RoleHost, "")
	host.readUntil(model.TypeJoined)

	viewer := dial(t, srv, roomID, "viewer-1")
	viewer.join("观众", model.RoleViewer, "")
	viewer.readUntil(model.TypeJoined)

	index := sampleIndex(2_000_000)

	host.send(model.Envelope{Type: model.TypeMediaIndex, MediaIndex: &index})

	forwarded := viewer.readUntil(model.TypeMediaIndex)
	if forwarded.MediaIndex == nil || len(forwarded.MediaIndex.Segments) != 2 {
		t.Fatalf("观众必须拿到完整分片索引，实际 %+v", forwarded.MediaIndex)
	}
	if forwarded.MediaIndex.Segments[1].File != "c00002.m4s" {
		t.Fatalf("索引分片内容不对: %+v", forwarded.MediaIndex.Segments[1])
	}

	// 索引发布时还没有实测上行：容量必须报 pending，而不是编一个数字。
	pending := viewer.readUntil(model.TypeCapacity)
	if pending.Capacity == nil || pending.Capacity.Mode != model.ModePending {
		t.Fatalf("未实测上行的容量应为 pending: %+v", pending.Capacity)
	}
	if pending.Capacity.StreamBps != 2_000_000 {
		t.Fatalf("容量应使用索引里的码率，实际 %d", pending.Capacity.StreamBps)
	}

	// 观众无权发布索引。
	viewer.send(model.Envelope{Type: model.TypeMediaIndex, MediaIndex: &index})
	if errEnv := viewer.readUntil(model.TypeError); errEnv.Code != model.CodeNotHost {
		t.Fatalf("观众发布索引应返回 NOT_HOST，实际 %q", errEnv.Code)
	}

	// metrics 缺字段要明确报错，而不是静默忽略。
	viewer.send(model.Envelope{Type: model.TypeMetrics})
	if errEnv := viewer.readUntil(model.TypeError); errEnv.Code != model.CodeBadRequest {
		t.Fatalf("缺少 metrics 字段应返回 BAD_REQUEST，实际 %q", errEnv.Code)
	}

	// 主播上报 12 Mbps 上行、码率 2 Mbps → K0 = 4 → 扇出模式。
	host.send(model.Envelope{
		Type:    model.TypeMetrics,
		Metrics: &model.Metrics{UploadCapacityBps: 12_000_000, RTTMs: 15},
	})
	measured := viewer.readUntil(model.TypeCapacity)
	if measured.Capacity == nil {
		t.Fatal("实测上行变化后必须广播容量")
	}
	if measured.Capacity.HostChildSlots != 4 || measured.Capacity.Mode != "fanout" {
		t.Fatalf("容量应为 fanout/K0=4，实际 %+v", measured.Capacity)
	}
	if measured.Capacity.MaxMembers != 5 {
		t.Fatalf("容量里的成员上限应为 1+K0=5，实际 %d", measured.Capacity.MaxMembers)
	}

	// 上行降到 4 Mbps → K0 = 1 → 上限 2 人；房间已有 2 人，新观众必须被拒。
	host.send(model.Envelope{
		Type:    model.TypeMetrics,
		Metrics: &model.Metrics{UploadCapacityBps: 4_000_000, RTTMs: 18},
	})
	degraded := viewer.readUntil(model.TypeCapacity)
	if degraded.Capacity == nil || degraded.Capacity.HostChildSlots != 1 || degraded.Capacity.Mode != "chain" {
		t.Fatalf("容量应降为 chain/K0=1，实际 %+v", degraded.Capacity)
	}

	extra := dial(t, srv, roomID, "viewer-2")
	extra.join("挤不进来的人", model.RoleViewer, "")
	if errEnv := extra.readUntil(model.TypeError); errEnv.Code != model.CodeRoomFull {
		t.Fatalf("超出 1+K0 应返回 ROOM_FULL，实际 %q", errEnv.Code)
	}

	// 已经在房里的人不受影响：容量收缩只拦新加入，不踢人（用一次聊天往返证明连接还活着）。
	viewer.send(model.Envelope{Type: model.TypeChat, Text: "我还在"})
	if chat := viewer.readUntil(model.TypeChat); chat.Text != "我还在" {
		t.Fatalf("容量收缩不应影响既有成员: %+v", chat)
	}
}

// TestMaxDepthFromEnvDrivesAssignment 是 PR_MAX_DEPTH 的端到端检查（与 TestHostGraceFromEnvDrivesLifecycle 同型）：
// 环境变量 → config.Load → usecase.NewManager → Room.Create → ReassignTopology → Assign → 下发载荷。
//
// 为什么必须到这一层：usecase 的单测只能证明"值进了 Options 之后"的行为，
// 而"配置真的从环境变量走到了分配器与下发报文里"只有走一次真实 gin + WebSocket + protobuf 才能证明。
//
// 拓扑：主播 4 Mbps（K0=1 → 单链）、relay 5 Mbps（2 个子节点位）、三个无上行叶子。
// 于是树只能是 host → relay(1) → l1/l2(2) → l3(3)，深度上限成为唯一的裁决者。
func TestMaxDepthFromEnvDrivesAssignment(t *testing.T) {
	for _, tc := range []struct {
		maxDepth int
		// wantL3 是第三个叶子应落的深度；0 表示"安置不下"。
		wantL3 int
	}{
		{maxDepth: 2, wantL3: 0},
		{maxDepth: 3, wantL3: 3},
	} {
		t.Run(fmt.Sprintf("PR_MAX_DEPTH=%d", tc.maxDepth), func(t *testing.T) {
			t.Setenv("PR_MAX_DEPTH", strconv.Itoa(tc.maxDepth))

			srv, cfg := newTestServerFromEnv(t)
			if cfg.Room.MaxDepth != tc.maxDepth {
				t.Fatalf("PR_MAX_DEPTH=%d 应落到配置上，实际 %d", tc.maxDepth, cfg.Room.MaxDepth)
			}

			roomID := createRoom(t, srv.URL, "")

			host := dial(t, srv, roomID, "host-1")
			host.join("主播", model.RoleHost, "")
			host.readUntil(model.TypeJoined)

			relay := dial(t, srv, roomID, "relay-1")
			relay.join("转发", model.RoleViewer, "")
			relay.readUntil(model.TypeJoined)

			leaves := make([]*wsClient, 0, 3)
			for _, id := range []string{"l1", "l2", "l3"} {
				c := dial(t, srv, roomID, id)
				c.join(id, model.RoleViewer, "")
				c.readUntil(model.TypeJoined)
				leaves = append(leaves, c)
			}

			// 所有人先进房（未实测时准入闸门放行），再上报实测上行触发重算。
			index := sampleIndex(2_000_000)
			host.send(model.Envelope{Type: model.TypeMediaIndex, MediaIndex: &index})
			host.send(model.Envelope{Type: model.TypeMetrics, Metrics: &model.Metrics{UploadCapacityBps: 4_000_000, RTTMs: 10}})
			relay.send(model.Envelope{Type: model.TypeMetrics, Metrics: &model.Metrics{UploadCapacityBps: 5_000_000, RTTMs: 20}})

			// 请求-响应式读取当前拓扑：未安置的成员拿不到拓扑，服务端会回 NOT_JOINED ——
			// 这正是"安置不下"在客户端侧的可观察形态。
			requestTopology := func(c *wsClient) model.Envelope {
				c.send(model.Envelope{Type: model.TypeTopologyRequest})
				return c.readUntil(model.TypeTopology, model.TypeError)
			}

			// 轮询到 l1 落到第 2 层：这一步同时证明 relay 的 metrics 已经触发过重算。
			// 不直接读一次容量广播的原因：两条连接各有自己的读循环，队列里那条可能还是上一步的。
			var l1Topo *model.TopologyAssignment
			deadline := time.Now().Add(testTimeout)
			for time.Now().Before(deadline) && l1Topo == nil {
				if env := requestTopology(leaves[0]); env.Topology != nil && env.Topology.Depth == 2 {
					l1Topo = env.Topology
					continue
				}
				time.Sleep(20 * time.Millisecond)
			}
			if l1Topo == nil {
				t.Fatalf("等待 l1 落到第 2 层超时（PR_MAX_DEPTH=%d）", tc.maxDepth)
			}
			if l1Topo.MaxDepth != tc.maxDepth {
				t.Fatalf("下发载荷里的 maxDepth 应为配置值 %d，实际 %d", tc.maxDepth, l1Topo.MaxDepth)
			}

			if env := requestTopology(host); env.Topology == nil || env.Topology.Depth != 0 || env.Topology.MaxDepth != tc.maxDepth {
				t.Fatalf("主播应为深度 0 且 maxDepth=%d，实际 %+v", tc.maxDepth, env.Topology)
			}
			if env := requestTopology(relay); env.Topology == nil ||
				env.Topology.Depth != 1 || env.Topology.PrimaryID != "host-1" || env.Topology.MaxDepth != tc.maxDepth {
				t.Fatalf("relay 应为深度 1、挂在主播下，实际 %+v", env.Topology)
			}
			if env := requestTopology(leaves[1]); env.Topology == nil || env.Topology.Depth != 2 {
				t.Fatalf("l2 应落在第 2 层，实际 %+v", env.Topology)
			}

			l3 := requestTopology(leaves[2])
			if tc.wantL3 == 0 {
				// 需要深度 3 才能安置 → 在上限 2 之下必须"未安置"，而不是突破上限继续挂。
				if l3.Topology != nil {
					t.Fatalf("PR_MAX_DEPTH=2 时 l3 不应拿到拓扑（需要深度 3），实际 %+v", l3.Topology)
				}
				if l3.Type != model.TypeError || l3.Code != model.CodeNotJoined {
					t.Fatalf("未安置的成员请求拓扑应收到 NOT_JOINED，实际 %+v", l3)
				}
			} else {
				if l3.Topology == nil || l3.Topology.Depth != tc.wantL3 {
					t.Fatalf("PR_MAX_DEPTH=3 时 l3 应落在第 %d 层，实际 %+v", tc.wantL3, l3.Topology)
				}
				if l3.Topology.MaxDepth != tc.maxDepth {
					t.Fatalf("l3 载荷里的 maxDepth 应为 %d，实际 %d", tc.maxDepth, l3.Topology.MaxDepth)
				}
			}
		})
	}
}
