package room

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/media"
	"ProjectionRoom/internal/protocol"
	"ProjectionRoom/internal/topology"
)

// sampleMediaIndex 构造一个自洽的索引，供容量与索引相关用例复用。
func sampleMediaIndex(bitrateBps int64) media.Index {
	return media.Index{
		Version:       1,
		InitFile:      "init.mp4",
		MimeType:      `video/mp4; codecs="avc1.64001f"`,
		TotalDuration: 4,
		SegmentSec:    2,
		BitrateBps:    bitrateBps,
		TotalBytes:    bitrateBps / 2,
		Segments: []media.Segment{
			{Index: 1, File: "c00001.m4s", Size: 1000, Duration: 2, StartPTS: 0, Keyframe: true},
			{Index: 2, File: "c00002.m4s", Size: 1000, Duration: 2, StartPTS: 2, Keyframe: true},
		},
	}
}

// fakeBus 记录所有投递，用于断言"谁收到了什么"。
// room 包只依赖 Broadcaster 接口，因此可以完全脱离网络测试。
type fakeBus struct {
	mu       sync.Mutex
	direct   map[string][][]byte
	roomWide map[string][][]byte
	excepts  map[string][]string
	closed   []string
}

func newFakeBus() *fakeBus {
	return &fakeBus{
		direct:   make(map[string][][]byte),
		roomWide: make(map[string][][]byte),
		excepts:  make(map[string][]string),
	}
}

func (f *fakeBus) SendTo(id string, msg []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.direct[id] = append(f.direct[id], msg)
	return nil
}

func (f *fakeBus) BroadcastToRoom(roomID string, msg []byte, except string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.roomWide[roomID] = append(f.roomWide[roomID], msg)
	f.excepts[roomID] = append(f.excepts[roomID], except)
	return len(f.roomWide[roomID])
}

func (f *fakeBus) RoomSize(string) int { return 0 }

func (f *fakeBus) CloseRoom(roomID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, roomID)
}

// lastBroadcastOfType 返回房间内最后一条指定类型的广播。
func (f *fakeBus) lastBroadcastOfType(t *testing.T, roomID, msgType string) protocol.Envelope {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	for i := len(f.roomWide[roomID]) - 1; i >= 0; i-- {
		var env protocol.Envelope
		if json.Unmarshal(f.roomWide[roomID][i], &env) == nil && env.Type == msgType {
			return env
		}
	}
	t.Fatalf("房间 %s 没有类型为 %s 的广播", roomID, msgType)

	return protocol.Envelope{}
}

// lastDirectOfType 返回发往某连接的最后一条指定类型消息。
//
// 必须按类型取：拓扑分配与容量广播会插在 joined / member-joined 中间，
// "取最后一条"会读到别的消息上去。
func (f *fakeBus) lastDirectOfType(t *testing.T, id, msgType string) protocol.Envelope {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()

	list := f.direct[id]
	for i := len(list) - 1; i >= 0; i-- {
		var env protocol.Envelope
		if json.Unmarshal(list[i], &env) == nil && env.Type == msgType {
			return env
		}
	}
	t.Fatalf("连接 %s 没有收到类型为 %s 的定向消息", id, msgType)

	return protocol.Envelope{}
}

// broadcastCount 统计房间内某类型的广播条数。
func (f *fakeBus) broadcastCount(roomID, msgType string) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	count := 0
	for _, raw := range f.roomWide[roomID] {
		var env protocol.Envelope
		if json.Unmarshal(raw, &env) == nil && env.Type == msgType {
			count++
		}
	}
	return count
}

func decode(t *testing.T, raw []byte) protocol.Envelope {
	t.Helper()
	var env protocol.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("解析信封失败: %v (raw=%s)", err, raw)
	}
	return env
}

func newTestManager(t *testing.T, maxMembers int) (*Manager, *fakeBus) {
	t.Helper()
	cfg := config.Default()
	cfg.Room.MaxMembers = maxMembers
	bus := newFakeBus()
	return NewManager(cfg, bus), bus
}

func TestCreateAndJoinFlow(t *testing.T) {
	m, bus := newTestManager(t, 8)

	r, err := m.Create("", "pw", 0)
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	if len(r.ID) != roomCodeLen {
		t.Fatalf("房间码长度应为 %d，实际 %d（%q）", roomCodeLen, len(r.ID), r.ID)
	}
	if r.StreamBps <= 0 {
		t.Fatalf("码率估计应回退到配置默认值，实际 %d", r.StreamBps)
	}

	if err := m.Join(r.ID, "viewer1", "观众", protocol.RoleViewer, "pw"); !errors.Is(err, ErrNotReady) {
		t.Fatalf("主播未进房时应返回 ErrNotReady，实际 %v", err)
	}
	if err := m.Join(r.ID, "host", "主播", protocol.RoleHost, "bad"); !errors.Is(err, ErrBadPassword) {
		t.Fatalf("密码错误应返回 ErrBadPassword，实际 %v", err)
	}
	if err := m.Join(r.ID, "host", "主播", protocol.RoleHost, "pw"); err != nil {
		t.Fatalf("主播进房失败: %v", err)
	}

	joined := bus.lastDirectOfType(t, "host", protocol.TypeJoined)
	if joined.Type != protocol.TypeJoined || joined.HostID != "host" || joined.SelfID != "host" {
		t.Fatalf("主播入房快照不正确: %+v", joined)
	}
	if joined.Playback == nil || !joined.Playback.Paused {
		t.Fatalf("新房应处于暂停态: %+v", joined.Playback)
	}

	if err := m.Join(r.ID, "viewer1", "观众", protocol.RoleViewer, "pw"); err != nil {
		t.Fatalf("观众进房失败: %v", err)
	}

	vJoined := bus.lastDirectOfType(t, "viewer1", protocol.TypeJoined)
	if vJoined.Type != protocol.TypeJoined || len(vJoined.Members) != 2 {
		t.Fatalf("观众入房快照应含 2 名成员: %+v", vJoined)
	}

	broadcast := bus.lastBroadcastOfType(t, r.ID, protocol.TypeMemberJoined)
	if broadcast.Type != protocol.TypeMemberJoined || broadcast.Member == nil || broadcast.Member.ID != "viewer1" {
		t.Fatalf("应广播 member-joined: %+v", broadcast)
	}
	if except := bus.lastExceptFor(t, r.ID, protocol.TypeMemberJoined); except != "viewer1" {
		t.Fatalf("member-joined 应排除加入者本人，实际 except=%q", except)
	}
}

func TestJoinRejectsDuplicateAndBadName(t *testing.T) {
	m, _ := newTestManager(t, 8)
	r, err := m.Create("", "", 0)
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	if err := m.Join(r.ID, "host", "主播", protocol.RoleHost, ""); err != nil {
		t.Fatalf("主播进房失败: %v", err)
	}
	if err := m.Join(r.ID, "host", "主播", protocol.RoleHost, ""); !errors.Is(err, ErrAlreadyJoined) {
		t.Fatalf("重复加入应返回 ErrAlreadyJoined，实际 %v", err)
	}
	if err := m.Join(r.ID, "host2", "", protocol.RoleHost, ""); !errors.Is(err, ErrBadName) {
		t.Fatalf("空昵称应返回 ErrBadName，实际 %v", err)
	}
	if err := m.Join(r.ID, "host3", "x", protocol.RoleHost, ""); !errors.Is(err, ErrHostTaken) {
		t.Fatalf("第二主播应返回 ErrHostTaken，实际 %v", err)
	}
	if err := m.Join("NOPEXX", "v", "观众", protocol.RoleViewer, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的房间应返回 ErrNotFound，实际 %v", err)
	}
}

func TestJoinRespectsMaxMembers(t *testing.T) {
	m, _ := newTestManager(t, 2)
	r, err := m.Create("", "", 0)
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	if err := m.Join(r.ID, "host", "主播", protocol.RoleHost, ""); err != nil {
		t.Fatalf("主播进房失败: %v", err)
	}
	if err := m.Join(r.ID, "v1", "观众1", protocol.RoleViewer, ""); err != nil {
		t.Fatalf("第一个观众应能进房: %v", err)
	}
	if err := m.Join(r.ID, "v2", "观众2", protocol.RoleViewer, ""); !errors.Is(err, ErrFull) {
		t.Fatalf("超出上限应返回 ErrFull，实际 %v", err)
	}
}

func TestChatIsServerOrderedAndMemberOnly(t *testing.T) {
	m, bus := newTestManager(t, 8)
	r, err := m.Create("", "", 0)
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	if err := m.Join(r.ID, "host", "主播", protocol.RoleHost, ""); err != nil {
		t.Fatalf("主播进房失败: %v", err)
	}

	if err := m.HandleChat(r.ID, "stranger", "你好"); !errors.Is(err, ErrNotJoined) {
		t.Fatalf("非成员发言应返回 ErrNotJoined，实际 %v", err)
	}
	if err := m.HandleChat(r.ID, "host", "   "); !errors.Is(err, ErrBadInput) {
		t.Fatalf("空消息应返回 ErrBadInput，实际 %v", err)
	}
	if err := m.HandleChat(r.ID, "host", "一起看"); err != nil {
		t.Fatalf("聊天失败: %v", err)
	}

	chat := bus.lastBroadcastOfType(t, r.ID, protocol.TypeChat)
	if chat.Type != protocol.TypeChat || chat.Text != "一起看" || chat.From != "host" {
		t.Fatalf("聊天广播不正确: %+v", chat)
	}
	if bus.excepts[r.ID][len(bus.excepts[r.ID])-1] != "" {
		t.Fatal("聊天应广播给房间内所有人（含发送者），由服务端决定唯一次序")
	}
}

func TestOnlyHostCanControlAndSeqIsMonotonic(t *testing.T) {
	m, bus := newTestManager(t, 8)
	r, err := m.Create("", "", 0)
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	if err := m.Join(r.ID, "host", "主播", protocol.RoleHost, ""); err != nil {
		t.Fatalf("主播进房失败: %v", err)
	}
	if err := m.Join(r.ID, "v1", "观众", protocol.RoleViewer, ""); err != nil {
		t.Fatalf("观众进房失败: %v", err)
	}

	viewerIntent := protocol.Envelope{Action: protocol.ActionPlay, CurrentTime: 12.5}
	if err := m.HandleControl(r.ID, "v1", viewerIntent); !errors.Is(err, ErrNotHost) {
		t.Fatalf("观众下发控制应返回 ErrNotHost，实际 %v", err)
	}
	if err := m.HandleControl(r.ID, "host", protocol.Envelope{Action: "bogus"}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("未知动作应返回 ErrBadInput，实际 %v", err)
	}

	if err := m.HandleControl(r.ID, "host", viewerIntent); err != nil {
		t.Fatalf("主播下发控制失败: %v", err)
	}
	first := bus.lastBroadcastOfType(t, r.ID, protocol.TypeRoomControl)
	if first.Type != protocol.TypeRoomControl || first.Playback == nil {
		t.Fatalf("应广播 room-control: %+v", first)
	}
	if first.Playback.Seq != 1 || first.Playback.CurrentTime != 12.5 || first.Playback.Paused {
		t.Fatalf("播放状态不正确: %+v", first.Playback)
	}

	// 暂停由 action 单点决定：即使报文里漏了 paused 字段，也必须记录为暂停。
	if err := m.HandleControl(r.ID, "host", protocol.Envelope{Action: protocol.ActionPause, CurrentTime: 30}); err != nil {
		t.Fatalf("主播暂停失败: %v", err)
	}
	second := bus.lastBroadcastOfType(t, r.ID, protocol.TypeRoomControl)
	if second.Playback.Seq != 2 || !second.Playback.Paused {
		t.Fatalf("seq 应单调递增且记录暂停态: %+v", second.Playback)
	}
	if second.Playback.Rate != 1 {
		t.Fatalf("未指定速率时应归一为 1，实际 %v", second.Playback.Rate)
	}

	// seek 不改变播放状态：暂停中的跳转仍然是暂停。
	if err := m.HandleControl(r.ID, "host", protocol.Envelope{Action: protocol.ActionSeek, CurrentTime: 45, Paused: true}); err != nil {
		t.Fatalf("主播跳转失败: %v", err)
	}
	third := bus.lastBroadcastOfType(t, r.ID, protocol.TypeRoomControl)
	if third.Playback.Seq != 3 || !third.Playback.Paused || third.Playback.CurrentTime != 45 {
		t.Fatalf("seek 应保留暂停态并递增 seq: %+v", third.Playback)
	}

	// 后进房的人必须立刻拿到当前播放状态，否则会从 0 开始播（SPEC §7.1）。
	if err := m.Join(r.ID, "v2", "迟到观众", protocol.RoleViewer, ""); err != nil {
		t.Fatalf("迟到观众进房失败: %v", err)
	}
	late := bus.lastDirectOfType(t, "v2", protocol.TypeJoined)
	if late.Playback == nil || late.Playback.Seq != 3 || late.Playback.CurrentTime != 45 || !late.Playback.Paused {
		t.Fatalf("入房快照应携带最新播放状态: %+v", late.Playback)
	}
}

func TestLeaveLifecycle(t *testing.T) {
	m, bus := newTestManager(t, 8)
	r, err := m.Create("", "", 0)
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	if err := m.Join(r.ID, "host", "主播", protocol.RoleHost, ""); err != nil {
		t.Fatalf("主播进房失败: %v", err)
	}
	if err := m.Join(r.ID, "v1", "观众", protocol.RoleViewer, ""); err != nil {
		t.Fatalf("观众进房失败: %v", err)
	}

	m.Leave(r.ID, "v1")
	if left := bus.lastBroadcastOfType(t, r.ID, protocol.TypeMemberLeft); left.ClientID != "v1" {
		t.Fatalf("应广播 member-left: %+v", left)
	}
	if _, ok := m.Get(r.ID); !ok {
		t.Fatal("还有主播在房时，房间不应被销毁")
	}

	m.Leave(r.ID, "host")
	if closed := bus.lastBroadcastOfType(t, r.ID, protocol.TypeRoomClosed); closed.Type != protocol.TypeRoomClosed {
		t.Fatalf("主播离开应广播 room-closed: %+v", closed)
	}
	if len(bus.closed) != 1 || bus.closed[0] != r.ID {
		t.Fatalf("主播离开应关闭房间连接: %#v", bus.closed)
	}
	if _, ok := m.Get(r.ID); ok {
		t.Fatal("主播离开后房间必须销毁：没有主播就没有可播放的内容")
	}
}

func TestLeaveUnknownMemberIsNoop(t *testing.T) {
	m, _ := newTestManager(t, 8)
	r, err := m.Create("", "", 0)
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	m.Leave(r.ID, "ghost")
	m.Leave("NOSUCH", "ghost")
	if _, ok := m.Get(r.ID); !ok {
		t.Fatal("无关的 Leave 不应影响房间存在性")
	}
}

func TestCreateRejectsDuplicateRoomID(t *testing.T) {
	m, _ := newTestManager(t, 8)
	if _, err := m.Create("ROOM01", "", 0); err != nil {
		t.Fatalf("首次创建失败: %v", err)
	}
	if _, err := m.Create("ROOM01", "", 0); !errors.Is(err, ErrRoomExists) {
		t.Fatalf("重复房间码应返回 ErrRoomExists，实际 %v", err)
	}
}

// lastExceptFor 返回房间内最后一条指定类型广播的 except 参数。
// 广播与 except 两个切片同序追加，因此可以按下标对应。
func (f *fakeBus) lastExceptFor(t *testing.T, roomID, msgType string) string {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	for i := len(f.roomWide[roomID]) - 1; i >= 0; i-- {
		var env protocol.Envelope
		if json.Unmarshal(f.roomWide[roomID][i], &env) == nil && env.Type == msgType {
			return f.excepts[roomID][i]
		}
	}
	t.Fatalf("房间 %s 没有类型为 %s 的广播", roomID, msgType)

	return ""
}

func TestSetMediaIndexRequiresHostAndLocks(t *testing.T) {
	m, bus := newTestManager(t, 8)
	r, err := m.Create("", "", 0)
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	if err := m.Join(r.ID, "host", "主播", protocol.RoleHost, ""); err != nil {
		t.Fatalf("主播进房失败: %v", err)
	}
	if err := m.Join(r.ID, "v1", "观众", protocol.RoleViewer, ""); err != nil {
		t.Fatalf("观众进房失败: %v", err)
	}

	raw, err := json.Marshal(sampleMediaIndex(2_000_000))
	if err != nil {
		t.Fatalf("序列化索引失败: %v", err)
	}

	if err := m.SetMediaIndex(r.ID, "v1", raw); !errors.Is(err, ErrNotHost) {
		t.Fatalf("观众发布索引应返回 ErrNotHost，实际 %v", err)
	}
	if err := m.SetMediaIndex(r.ID, "ghost", raw); !errors.Is(err, ErrNotJoined) {
		t.Fatalf("未加入的连接应返回 ErrNotJoined，实际 %v", err)
	}
	if err := m.SetMediaIndex(r.ID, "host", json.RawMessage(`不是 JSON`)); !errors.Is(err, ErrBadMediaIndex) {
		t.Fatalf("非 JSON 索引应返回 ErrBadMediaIndex，实际 %v", err)
	}
	if err := m.SetMediaIndex(r.ID, "host", json.RawMessage(`{"version":1,"segments":[]}`)); !errors.Is(err, ErrBadMediaIndex) {
		t.Fatalf("不自洽的索引应返回 ErrBadMediaIndex，实际 %v", err)
	}

	if err := m.SetMediaIndex(r.ID, "host", raw); err != nil {
		t.Fatalf("主播发布索引失败: %v", err)
	}

	forwarded := bus.lastBroadcastOfType(t, r.ID, protocol.TypeMediaIndex)
	if len(forwarded.MediaIndex) == 0 || !json.Valid(forwarded.MediaIndex) {
		t.Fatalf("应把索引广播给其他人: %+v", forwarded)
	}
	if except := bus.lastExceptFor(t, r.ID, protocol.TypeMediaIndex); except != "host" {
		t.Fatalf("索引广播应排除发布者本人，实际 except=%q", except)
	}

	capacity := bus.lastBroadcastOfType(t, r.ID, protocol.TypeCapacity)
	if capacity.Capacity == nil || capacity.Capacity.StreamBps != 2_000_000 {
		t.Fatalf("发布索引后应用索引里的码率重算容量: %+v", capacity.Capacity)
	}

	// 同一份索引重发应当幂等；换一份则被锁定。
	if err := m.SetMediaIndex(r.ID, "host", raw); err != nil {
		t.Fatalf("重复发布同一索引应幂等，实际 %v", err)
	}
	other, err := json.Marshal(sampleMediaIndex(1_000_000))
	if err != nil {
		t.Fatalf("序列化索引失败: %v", err)
	}
	if err := m.SetMediaIndex(r.ID, "host", other); !errors.Is(err, ErrMediaLocked) {
		t.Fatalf("换片应返回 ErrMediaLocked，实际 %v", err)
	}

	// 后进房的人必须直接拿到索引，否则无从请求分片。
	if err := m.Join(r.ID, "v2", "迟到观众", protocol.RoleViewer, ""); err != nil {
		t.Fatalf("迟到观众进房失败: %v", err)
	}
	late := bus.lastDirectOfType(t, "v2", protocol.TypeJoined)
	if len(late.MediaIndex) == 0 {
		t.Fatal("入房快照必须携带分片索引")
	}
}

func TestCapacityGateFollowsMeasuredUplink(t *testing.T) {
	m, bus := newTestManager(t, 16)
	r, err := m.Create("", "", 0)
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	if err := m.Join(r.ID, "host", "主播", protocol.RoleHost, ""); err != nil {
		t.Fatalf("主播进房失败: %v", err)
	}
	if err := m.Join(r.ID, "v1", "观众1", protocol.RoleViewer, ""); err != nil {
		t.Fatalf("观众1 进房失败: %v", err)
	}

	raw, err := json.Marshal(sampleMediaIndex(2_000_000))
	if err != nil {
		t.Fatalf("序列化索引失败: %v", err)
	}
	if err := m.SetMediaIndex(r.ID, "host", raw); err != nil {
		t.Fatalf("主播发布索引失败: %v", err)
	}

	// 还没有实测上行：容量必须是 pending，而不是假装知道。
	before := r.Capacity(16)
	if before.Mode != protocol.ModePending || before.HostChildSlots != 0 || before.MaxMembers != 16 {
		t.Fatalf("未实测时容量应为 pending/16：%+v", before)
	}

	// 主播上报 4 Mbps 上行、码率 2 Mbps：K0 = floor(4*0.8/2) = 1 → 单链模式，上限 2 人。
	if err := m.UpdateMetrics(r.ID, "host", protocol.Metrics{UploadCapacityBps: 4_000_000, RTTMs: 20}); err != nil {
		t.Fatalf("上报度量失败: %v", err)
	}
	broadcast := bus.lastBroadcastOfType(t, r.ID, protocol.TypeCapacity)
	if broadcast.Capacity == nil {
		t.Fatal("实测上行变化后必须广播容量")
	}
	if broadcast.Capacity.HostChildSlots != 1 || broadcast.Capacity.Mode != string(topology.ModeChain) || broadcast.Capacity.MaxMembers != 2 {
		t.Fatalf("容量广播不正确: %+v", broadcast.Capacity)
	}

	// 房间已经满员（主播 + 1 个观众），再来人必须被拒，而不是一起卡。
	if err := m.Join(r.ID, "v2", "观众2", protocol.RoleViewer, ""); !errors.Is(err, ErrFull) {
		t.Fatalf("超出 1+K0 应返回 ErrFull，实际 %v", err)
	}

	// M3 起，观众的实测上行会变成"转发容量"：房间上限因此变大，
	// 但主播自己的 K0 不受影响 —— 瓶颈仍在主播的上行（SPEC §6.2）。
	beforeRelay := r.Capacity(16)
	if err := m.UpdateMetrics(r.ID, "v1", protocol.Metrics{UploadCapacityBps: 100_000_000}); err != nil {
		t.Fatalf("观众上报度量失败: %v", err)
	}
	afterRelay := r.Capacity(16)
	if afterRelay.HostChildSlots != 1 || afterRelay.Mode != string(topology.ModeChain) {
		t.Fatalf("主播的 K0 不应因观众的度量而改变: %+v", afterRelay)
	}
	if afterRelay.MaxMembers <= beforeRelay.MaxMembers {
		t.Fatalf("强上行观众应成为转发节点并扩大房间上限：before=%d after=%d",
			beforeRelay.MaxMembers, afterRelay.MaxMembers)
	}
}

func TestUpdateMetricsRejectsNonMember(t *testing.T) {
	m, _ := newTestManager(t, 8)
	r, err := m.Create("", "", 0)
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	if err := m.UpdateMetrics(r.ID, "ghost", protocol.Metrics{UploadCapacityBps: 1}); !errors.Is(err, ErrNotJoined) {
		t.Fatalf("非成员上报度量应返回 ErrNotJoined，实际 %v", err)
	}
	if err := m.UpdateMetrics("NOSUCH", "ghost", protocol.Metrics{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的房间应返回 ErrNotFound，实际 %v", err)
	}
}

// ---------- M3：拓扑分配与单链分发 ----------

func TestTopologyAssignmentsAreBroadcast(t *testing.T) {
	m, bus := newTestManager(t, 16)
	r, err := m.Create("", "", 0)
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}

	if err := m.Join(r.ID, "host", "主播", protocol.RoleHost, ""); err != nil {
		t.Fatalf("主播进房失败: %v", err)
	}
	// 16 Mbps 上行 / 2 Mbps 码率 → K0 = 6：三个观众应全部直连主播。
	if err := m.UpdateMetrics(r.ID, "host", protocol.Metrics{UploadCapacityBps: 16_000_000, RTTMs: 10}); err != nil {
		t.Fatalf("主播上报度量失败: %v", err)
	}
	raw, err := json.Marshal(sampleMediaIndex(2_000_000))
	if err != nil {
		t.Fatalf("序列化索引失败: %v", err)
	}
	if err := m.SetMediaIndex(r.ID, "host", raw); err != nil {
		t.Fatalf("发布索引失败: %v", err)
	}

	for _, id := range []string{"v1", "v2", "v3"} {
		if err := m.Join(r.ID, id, id, protocol.RoleViewer, ""); err != nil {
			t.Fatalf("%s 进房失败: %v", id, err)
		}
	}

	for _, id := range []string{"host", "v1", "v2", "v3"} {
		env := bus.lastDirectOfType(t, id, protocol.TypeParentAssignment)
		if env.Topology == nil {
			t.Fatalf("%s 没有收到拓扑分配", id)
		}
	}

	hostTopo := bus.lastDirectOfType(t, "host", protocol.TypeParentAssignment).Topology
	if hostTopo.Depth != 0 || len(hostTopo.Children) != 3 {
		t.Fatalf("主播应是深度 0 且带 3 个子节点，实际 %+v", hostTopo)
	}
	for _, id := range []string{"v1", "v2", "v3"} {
		topo := bus.lastDirectOfType(t, id, protocol.TypeParentAssignment).Topology
		if topo.PrimaryID != "host" || topo.Depth != 1 {
			t.Fatalf("%s 应直连主播，实际 %+v", id, topo)
		}
		if len(topo.BackupIDs) == 0 {
			t.Fatalf("%s 应至少有一个备用父（主父慢时才有退路）", id)
		}
		if topo.Mode != string(topology.ModeFanout) {
			t.Fatalf("模式应为 fanout，实际 %q", topo.Mode)
		}
	}

	capacity := bus.lastBroadcastOfType(t, r.ID, protocol.TypeCapacity)
	if capacity.Capacity == nil || capacity.Capacity.Mode != string(topology.ModeFanout) || capacity.Capacity.HostChildSlots != 6 {
		t.Fatalf("容量广播不正确: %+v", capacity.Capacity)
	}
}

func TestChainModeGivesHostExactlyOneChild(t *testing.T) {
	m, bus := newTestManager(t, 16)
	r, err := m.Create("", "", 0)
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	if err := m.Join(r.ID, "host", "主播", protocol.RoleHost, ""); err != nil {
		t.Fatalf("主播进房失败: %v", err)
	}
	raw, err := json.Marshal(sampleMediaIndex(2_000_000))
	if err != nil {
		t.Fatalf("序列化索引失败: %v", err)
	}
	if err := m.SetMediaIndex(r.ID, "host", raw); err != nil {
		t.Fatalf("发布索引失败: %v", err)
	}
	for _, id := range []string{"relay", "leaf1", "leaf2"} {
		if err := m.Join(r.ID, id, id, protocol.RoleViewer, ""); err != nil {
			t.Fatalf("%s 进房失败: %v", id, err)
		}
	}

	// 主播实测 4 Mbps → K0 = floor(4×0.8/2) = 1 → 单链模式；
	// relay 实测 20 Mbps → 它必须成为那个唯一的分发节点。
	if err := m.UpdateMetrics(r.ID, "host", protocol.Metrics{UploadCapacityBps: 4_000_000}); err != nil {
		t.Fatalf("主播上报失败: %v", err)
	}
	if err := m.UpdateMetrics(r.ID, "relay", protocol.Metrics{UploadCapacityBps: 20_000_000}); err != nil {
		t.Fatalf("relay 上报失败: %v", err)
	}

	hostTopo := bus.lastDirectOfType(t, "host", protocol.TypeParentAssignment).Topology
	if hostTopo.Mode != string(topology.ModeChain) {
		t.Fatalf("K0=1 时应为单链模式，实际 %q", hostTopo.Mode)
	}
	if len(hostTopo.Children) != 1 || hostTopo.Children[0] != "relay" {
		t.Fatalf("单链模式下主播只能有 1 个子节点（分发节点），实际 %v", hostTopo.Children)
	}
	if hostTopo.DistributorID != "relay" {
		t.Fatalf("分发节点应为上行最强的 relay，实际 %q", hostTopo.DistributorID)
	}

	relayTopo := bus.lastDirectOfType(t, "relay", protocol.TypeParentAssignment).Topology
	if relayTopo.PrimaryID != "host" || relayTopo.Depth != 1 {
		t.Fatalf("分发节点应直连主播，实际 %+v", relayTopo)
	}

	for _, id := range []string{"leaf1", "leaf2"} {
		topo := bus.lastDirectOfType(t, id, protocol.TypeParentAssignment).Topology
		if topo.PrimaryID != "relay" || topo.Depth != 2 {
			t.Fatalf("%s 应挂在分发节点下（depth=2），实际 %+v", id, topo)
		}
	}
}

func TestDistributorHandoffBroadcast(t *testing.T) {
	m, bus := newTestManager(t, 16)
	r, err := m.Create("", "", 0)
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	if err := m.Join(r.ID, "host", "主播", protocol.RoleHost, ""); err != nil {
		t.Fatalf("主播进房失败: %v", err)
	}
	raw, err := json.Marshal(sampleMediaIndex(2_000_000))
	if err != nil {
		t.Fatalf("序列化索引失败: %v", err)
	}
	if err := m.SetMediaIndex(r.ID, "host", raw); err != nil {
		t.Fatalf("发布索引失败: %v", err)
	}
	if err := m.Join(r.ID, "relay1", "转发1", protocol.RoleViewer, ""); err != nil {
		t.Fatalf("relay1 进房失败: %v", err)
	}

	if err := m.UpdateMetrics(r.ID, "host", protocol.Metrics{UploadCapacityBps: 4_000_000}); err != nil {
		t.Fatalf("主播上报失败: %v", err)
	}
	if err := m.UpdateMetrics(r.ID, "relay1", protocol.Metrics{UploadCapacityBps: 6_000_000}); err != nil {
		t.Fatalf("relay1 上报失败: %v", err)
	}
	if got := bus.lastDirectOfType(t, "host", protocol.TypeParentAssignment).Topology.DistributorID; got != "relay1" {
		t.Fatalf("分发节点应为 relay1，实际 %q", got)
	}

	// 再加入一个强得多的节点：换防必须发生，并且要广播出来。
	if err := m.Join(r.ID, "relay2", "转发2", protocol.RoleViewer, ""); err != nil {
		t.Fatalf("relay2 进房失败: %v", err)
	}
	before := bus.broadcastCount(r.ID, protocol.TypeDistributorChange)
	if err := m.UpdateMetrics(r.ID, "relay2", protocol.Metrics{UploadCapacityBps: 30_000_000}); err != nil {
		t.Fatalf("relay2 上报失败: %v", err)
	}

	if got := bus.lastDirectOfType(t, "host", protocol.TypeParentAssignment).Topology.DistributorID; got != "relay2" {
		t.Fatalf("应换防到强得多的 relay2，实际 %q", got)
	}
	if after := bus.broadcastCount(r.ID, protocol.TypeDistributorChange); after <= before {
		t.Fatalf("换防必须广播 distributor-change（before=%d after=%d）", before, after)
	}

	change := bus.lastBroadcastOfType(t, r.ID, protocol.TypeDistributorChange)
	if change.Distributor == nil || change.Distributor.FromID != "relay1" || change.Distributor.ToID != "relay2" {
		t.Fatalf("换防广播内容不正确: %+v", change.Distributor)
	}
}

func TestChunkReportIsRecorded(t *testing.T) {
	m, _ := newTestManager(t, 8)
	r, err := m.Create("", "", 0)
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	if err := m.Join(r.ID, "host", "主播", protocol.RoleHost, ""); err != nil {
		t.Fatalf("主播进房失败: %v", err)
	}

	if err := m.SetChunkReport(r.ID, "host", "AQID", true); err != nil {
		t.Fatalf("记录分片上报失败: %v", err)
	}

	r.mu.Lock()
	member := r.members["host"]
	r.mu.Unlock()
	if member == nil || member.HaveBits != "AQID" || !member.Complete {
		t.Fatalf("分片上报未被记录: %+v", member)
	}

	if err := m.SetChunkReport(r.ID, "ghost", "AQID", false); !errors.Is(err, ErrNotJoined) {
		t.Fatalf("非成员上报应返回 ErrNotJoined，实际 %v", err)
	}
}

func TestSendTopologyForUnknownMember(t *testing.T) {
	m, _ := newTestManager(t, 8)
	r, err := m.Create("", "", 0)
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	if err := m.Join(r.ID, "host", "主播", protocol.RoleHost, ""); err != nil {
		t.Fatalf("主播进房失败: %v", err)
	}
	if err := m.SendTopology(r.ID, "ghost"); !errors.Is(err, ErrNotJoined) {
		t.Fatalf("非成员请求拓扑应返回 ErrNotJoined，实际 %v", err)
	}
	if err := m.SendTopology(r.ID, "host"); err != nil {
		t.Fatalf("主播请求拓扑不应失败: %v", err)
	}
}
