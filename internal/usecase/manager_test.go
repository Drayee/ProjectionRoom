package usecase

import (
	"bytes"
	"errors"
	"sync"
	"testing"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/model"
)

// sampleMediaIndex 构造一个自洽的索引，供容量与索引相关用例复用。
func sampleMediaIndex(bitrateBps int64) model.Index {
	return model.Index{
		Version:       1,
		InitFile:      "init.mp4",
		MimeType:      `video/mp4; codecs="avc1.64001f"`,
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
func (f *fakeBus) lastBroadcastOfType(t *testing.T, roomID, msgType string) model.Envelope {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	for i := len(f.roomWide[roomID]) - 1; i >= 0; i-- {
		if env, err := model.Unmarshal(f.roomWide[roomID][i]); err == nil && env.Type == msgType {
			return *env
		}
	}
	t.Fatalf("房间 %s 没有类型为 %s 的广播", roomID, msgType)

	return model.Envelope{}
}

// lastDirectOfType 返回发往某连接的最后一条指定类型消息。
//
// 必须按类型取：拓扑分配与容量广播会插在 joined / member-joined 中间，
// "取最后一条"会读到别的消息上去。
func (f *fakeBus) lastDirectOfType(t *testing.T, id, msgType string) model.Envelope {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()

	list := f.direct[id]
	for i := len(list) - 1; i >= 0; i-- {
		if env, err := model.Unmarshal(list[i]); err == nil && env.Type == msgType {
			return *env
		}
	}
	t.Fatalf("连接 %s 没有收到类型为 %s 的定向消息", id, msgType)

	return model.Envelope{}
}

// broadcastCount 统计房间内某类型的广播条数。
func (f *fakeBus) broadcastCount(roomID, msgType string) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	count := 0
	for _, raw := range f.roomWide[roomID] {
		if env, err := model.Unmarshal(raw); err == nil && env.Type == msgType {
			count++
		}
	}
	return count
}

func decode(t *testing.T, raw []byte) model.Envelope {
	t.Helper()
	env, err := model.Unmarshal(raw)
	if err != nil {
		t.Fatalf("解析信封失败: %v", err)
	}
	return *env
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

	if err := m.Join(r.ID, "viewer1", "观众", model.RoleViewer, "pw"); !errors.Is(err, ErrNotReady) {
		t.Fatalf("主播未进房时应返回 ErrNotReady，实际 %v", err)
	}
	if err := m.Join(r.ID, "host", "主播", model.RoleHost, "bad"); !errors.Is(err, ErrBadPassword) {
		t.Fatalf("密码错误应返回 ErrBadPassword，实际 %v", err)
	}
	if err := m.Join(r.ID, "host", "主播", model.RoleHost, "pw"); err != nil {
		t.Fatalf("主播进房失败: %v", err)
	}

	joined := bus.lastDirectOfType(t, "host", model.TypeJoined)
	if joined.Type != model.TypeJoined || joined.HostID != "host" || joined.SelfID != "host" {
		t.Fatalf("主播入房快照不正确: %+v", joined)
	}
	if joined.Playback == nil || !joined.Playback.Paused {
		t.Fatalf("新房应处于暂停态: %+v", joined.Playback)
	}

	if err := m.Join(r.ID, "viewer1", "观众", model.RoleViewer, "pw"); err != nil {
		t.Fatalf("观众进房失败: %v", err)
	}

	vJoined := bus.lastDirectOfType(t, "viewer1", model.TypeJoined)
	if vJoined.Type != model.TypeJoined || len(vJoined.Members) != 2 {
		t.Fatalf("观众入房快照应含 2 名成员: %+v", vJoined)
	}

	broadcast := bus.lastBroadcastOfType(t, r.ID, model.TypeMemberJoined)
	if broadcast.Type != model.TypeMemberJoined || broadcast.Member == nil || broadcast.Member.ID != "viewer1" {
		t.Fatalf("应广播 member-joined: %+v", broadcast)
	}
	if except := bus.lastExceptFor(t, r.ID, model.TypeMemberJoined); except != "viewer1" {
		t.Fatalf("member-joined 应排除加入者本人，实际 except=%q", except)
	}
}

func TestJoinRejectsDuplicateAndBadName(t *testing.T) {
	m, _ := newTestManager(t, 8)
	r, err := m.Create("", "", 0)
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	if err := m.Join(r.ID, "host", "主播", model.RoleHost, ""); err != nil {
		t.Fatalf("主播进房失败: %v", err)
	}
	if err := m.Join(r.ID, "host", "主播", model.RoleHost, ""); !errors.Is(err, ErrAlreadyJoined) {
		t.Fatalf("重复加入应返回 ErrAlreadyJoined，实际 %v", err)
	}
	if err := m.Join(r.ID, "host2", "", model.RoleHost, ""); !errors.Is(err, ErrBadName) {
		t.Fatalf("空昵称应返回 ErrBadName，实际 %v", err)
	}
	if err := m.Join(r.ID, "host3", "x", model.RoleHost, ""); !errors.Is(err, ErrHostTaken) {
		t.Fatalf("第二主播应返回 ErrHostTaken，实际 %v", err)
	}
	if err := m.Join("NOPEXX", "v", "观众", model.RoleViewer, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的房间应返回 ErrNotFound，实际 %v", err)
	}
}

func TestJoinRespectsMaxMembers(t *testing.T) {
	m, _ := newTestManager(t, 2)
	r, err := m.Create("", "", 0)
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	if err := m.Join(r.ID, "host", "主播", model.RoleHost, ""); err != nil {
		t.Fatalf("主播进房失败: %v", err)
	}
	if err := m.Join(r.ID, "v1", "观众1", model.RoleViewer, ""); err != nil {
		t.Fatalf("第一个观众应能进房: %v", err)
	}
	if err := m.Join(r.ID, "v2", "观众2", model.RoleViewer, ""); !errors.Is(err, ErrFull) {
		t.Fatalf("超出上限应返回 ErrFull，实际 %v", err)
	}
}

func TestChatIsServerOrderedAndMemberOnly(t *testing.T) {
	m, bus := newTestManager(t, 8)
	r, err := m.Create("", "", 0)
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	if err := m.Join(r.ID, "host", "主播", model.RoleHost, ""); err != nil {
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

	chat := bus.lastBroadcastOfType(t, r.ID, model.TypeChat)
	if chat.Type != model.TypeChat || chat.Text != "一起看" || chat.From != "host" {
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
	if err := m.Join(r.ID, "host", "主播", model.RoleHost, ""); err != nil {
		t.Fatalf("主播进房失败: %v", err)
	}
	if err := m.Join(r.ID, "v1", "观众", model.RoleViewer, ""); err != nil {
		t.Fatalf("观众进房失败: %v", err)
	}

	viewerIntent := model.Envelope{Action: model.ActionPlay, CurrentTime: 12.5}
	if err := m.HandleControl(r.ID, "v1", viewerIntent); !errors.Is(err, ErrNotHost) {
		t.Fatalf("观众下发控制应返回 ErrNotHost，实际 %v", err)
	}
	if err := m.HandleControl(r.ID, "host", model.Envelope{Action: "bogus"}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("未知动作应返回 ErrBadInput，实际 %v", err)
	}

	if err := m.HandleControl(r.ID, "host", viewerIntent); err != nil {
		t.Fatalf("主播下发控制失败: %v", err)
	}
	first := bus.lastBroadcastOfType(t, r.ID, model.TypeRoomControl)
	if first.Type != model.TypeRoomControl || first.Playback == nil {
		t.Fatalf("应广播 room-control: %+v", first)
	}
	if first.Playback.Seq != 1 || first.Playback.CurrentTime != 12.5 || first.Playback.Paused {
		t.Fatalf("播放状态不正确: %+v", first.Playback)
	}

	// 暂停由 action 单点决定：即使报文里漏了 paused 字段，也必须记录为暂停。
	if err := m.HandleControl(r.ID, "host", model.Envelope{Action: model.ActionPause, CurrentTime: 30}); err != nil {
		t.Fatalf("主播暂停失败: %v", err)
	}
	second := bus.lastBroadcastOfType(t, r.ID, model.TypeRoomControl)
	if second.Playback.Seq != 2 || !second.Playback.Paused {
		t.Fatalf("seq 应单调递增且记录暂停态: %+v", second.Playback)
	}
	if second.Playback.Rate != 1 {
		t.Fatalf("未指定速率时应归一为 1，实际 %v", second.Playback.Rate)
	}

	// seek 不改变播放状态：暂停中的跳转仍然是暂停。
	if err := m.HandleControl(r.ID, "host", model.Envelope{Action: model.ActionSeek, CurrentTime: 45, Paused: true}); err != nil {
		t.Fatalf("主播跳转失败: %v", err)
	}
	third := bus.lastBroadcastOfType(t, r.ID, model.TypeRoomControl)
	if third.Playback.Seq != 3 || !third.Playback.Paused || third.Playback.CurrentTime != 45 {
		t.Fatalf("seek 应保留暂停态并递增 seq: %+v", third.Playback)
	}

	// 后进房的人必须立刻拿到当前播放状态，否则会从 0 开始播（SPEC §7.1）。
	if err := m.Join(r.ID, "v2", "迟到观众", model.RoleViewer, ""); err != nil {
		t.Fatalf("迟到观众进房失败: %v", err)
	}
	late := bus.lastDirectOfType(t, "v2", model.TypeJoined)
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
	if err := m.Join(r.ID, "host", "主播", model.RoleHost, ""); err != nil {
		t.Fatalf("主播进房失败: %v", err)
	}
	if err := m.Join(r.ID, "v1", "观众", model.RoleViewer, ""); err != nil {
		t.Fatalf("观众进房失败: %v", err)
	}

	m.Leave(r.ID, "v1")
	if left := bus.lastBroadcastOfType(t, r.ID, model.TypeMemberLeft); left.ClientID != "v1" {
		t.Fatalf("应广播 member-left: %+v", left)
	}
	if _, ok := m.Get(r.ID); !ok {
		t.Fatal("还有主播在房时，房间不应被销毁")
	}

	// 主播断线：进入宽限期。房间、成员、播放状态全部保留，只广播 member-left，
	// 不发 room-closed、不断开任何连接（详细断言见 host_grace_test.go）。
	m.Leave(r.ID, "host")
	if _, ok := m.Get(r.ID); !ok {
		t.Fatal("主播断线后房间应进入宽限期而不是立刻销毁")
	}
	if got := bus.broadcastCount(r.ID, model.TypeRoomClosed); got != 0 {
		t.Fatalf("宽限期内不得广播 room-closed，实际 %d 条", got)
	}
	if len(bus.closed) != 0 {
		t.Fatalf("宽限期内不得关闭房间连接: %#v", bus.closed)
	}
	if hostID, _, _, _, _ := r.Snapshot(8); hostID != "" {
		t.Fatalf("主播离线期间 hostId 必须为空（HTTP 侧据此判定「当前无主播」），实际 %q", hostID)
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
		if env, err := model.Unmarshal(f.roomWide[roomID][i]); err == nil && env.Type == msgType {
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
	if err := m.Join(r.ID, "host", "主播", model.RoleHost, ""); err != nil {
		t.Fatalf("主播进房失败: %v", err)
	}
	if err := m.Join(r.ID, "v1", "观众", model.RoleViewer, ""); err != nil {
		t.Fatalf("观众进房失败: %v", err)
	}

	index := sampleMediaIndex(2_000_000)

	if err := m.SetMediaIndex(r.ID, "v1", &index); !errors.Is(err, ErrNotHost) {
		t.Fatalf("观众发布索引应返回 ErrNotHost，实际 %v", err)
	}
	if err := m.SetMediaIndex(r.ID, "ghost", &index); !errors.Is(err, ErrNotJoined) {
		t.Fatalf("未加入的连接应返回 ErrNotJoined，实际 %v", err)
	}
	if err := m.SetMediaIndex(r.ID, "host", nil); !errors.Is(err, ErrBadMediaIndex) {
		t.Fatalf("空索引应返回 ErrBadMediaIndex，实际 %v", err)
	}
	if err := m.SetMediaIndex(r.ID, "host", &model.Index{Version: 1}); !errors.Is(err, ErrBadMediaIndex) {
		t.Fatalf("不自洽的索引应返回 ErrBadMediaIndex，实际 %v", err)
	}

	if err := m.SetMediaIndex(r.ID, "host", &index); err != nil {
		t.Fatalf("主播发布索引失败: %v", err)
	}

	forwarded := bus.lastBroadcastOfType(t, r.ID, model.TypeMediaIndex)
	if forwarded.MediaIndex == nil || forwarded.MediaIndex.MimeType == "" {
		t.Fatalf("应把索引广播给其他人: %+v", forwarded)
	}
	if except := bus.lastExceptFor(t, r.ID, model.TypeMediaIndex); except != "host" {
		t.Fatalf("索引广播应排除发布者本人，实际 except=%q", except)
	}

	capacity := bus.lastBroadcastOfType(t, r.ID, model.TypeCapacity)
	if capacity.Capacity == nil || capacity.Capacity.StreamBps != 2_000_000 {
		t.Fatalf("发布索引后应用索引里的码率重算容量: %+v", capacity.Capacity)
	}

	// 同一份索引重发应当幂等；换一份则被锁定。
	if err := m.SetMediaIndex(r.ID, "host", &index); err != nil {
		t.Fatalf("重复发布同一索引应幂等，实际 %v", err)
	}
	other := sampleMediaIndex(1_000_000)
	if err := m.SetMediaIndex(r.ID, "host", &other); !errors.Is(err, ErrMediaLocked) {
		t.Fatalf("换片应返回 ErrMediaLocked，实际 %v", err)
	}

	// 后进房的人必须直接拿到索引，否则无从请求分片。
	if err := m.Join(r.ID, "v2", "迟到观众", model.RoleViewer, ""); err != nil {
		t.Fatalf("迟到观众进房失败: %v", err)
	}
	late := bus.lastDirectOfType(t, "v2", model.TypeJoined)
	if late.MediaIndex == nil || len(late.MediaIndex.Segments) == 0 {
		t.Fatal("入房快照必须携带分片索引")
	}
}

func TestCapacityGateFollowsMeasuredUplink(t *testing.T) {
	m, bus := newTestManager(t, 16)
	r, err := m.Create("", "", 0)
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	if err := m.Join(r.ID, "host", "主播", model.RoleHost, ""); err != nil {
		t.Fatalf("主播进房失败: %v", err)
	}
	if err := m.Join(r.ID, "v1", "观众1", model.RoleViewer, ""); err != nil {
		t.Fatalf("观众1 进房失败: %v", err)
	}

	index := sampleMediaIndex(2_000_000)
	if err := m.SetMediaIndex(r.ID, "host", &index); err != nil {
		t.Fatalf("主播发布索引失败: %v", err)
	}

	// 还没有实测上行：容量必须是 pending，而不是假装知道。
	before := r.Capacity(16)
	if before.Mode != model.ModePending || before.HostChildSlots != 0 || before.MaxMembers != 16 {
		t.Fatalf("未实测时容量应为 pending/16：%+v", before)
	}

	// 主播上报 4 Mbps 上行、码率 2 Mbps：K0 = floor(4*0.8/2) = 1 → 单链模式，上限 2 人。
	if err := m.UpdateMetrics(r.ID, "host", model.Metrics{UploadCapacityBps: 4_000_000, RTTMs: 20}); err != nil {
		t.Fatalf("上报度量失败: %v", err)
	}
	broadcast := bus.lastBroadcastOfType(t, r.ID, model.TypeCapacity)
	if broadcast.Capacity == nil {
		t.Fatal("实测上行变化后必须广播容量")
	}
	if broadcast.Capacity.HostChildSlots != 1 || broadcast.Capacity.Mode != string(ModeChain) || broadcast.Capacity.MaxMembers != 2 {
		t.Fatalf("容量广播不正确: %+v", broadcast.Capacity)
	}

	// 房间已经满员（主播 + 1 个观众），再来人必须被拒，而不是一起卡。
	if err := m.Join(r.ID, "v2", "观众2", model.RoleViewer, ""); !errors.Is(err, ErrFull) {
		t.Fatalf("超出 1+K0 应返回 ErrFull，实际 %v", err)
	}

	// M3 起，观众的实测上行会变成"转发容量"：房间上限因此变大，
	// 但主播自己的 K0 不受影响 —— 瓶颈仍在主播的上行（SPEC §6.2）。
	beforeRelay := r.Capacity(16)
	if err := m.UpdateMetrics(r.ID, "v1", model.Metrics{UploadCapacityBps: 100_000_000}); err != nil {
		t.Fatalf("观众上报度量失败: %v", err)
	}
	afterRelay := r.Capacity(16)
	if afterRelay.HostChildSlots != 1 || afterRelay.Mode != string(ModeChain) {
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
	if err := m.UpdateMetrics(r.ID, "ghost", model.Metrics{UploadCapacityBps: 1}); !errors.Is(err, ErrNotJoined) {
		t.Fatalf("非成员上报度量应返回 ErrNotJoined，实际 %v", err)
	}
	if err := m.UpdateMetrics("NOSUCH", "ghost", model.Metrics{}); !errors.Is(err, ErrNotFound) {
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

	if err := m.Join(r.ID, "host", "主播", model.RoleHost, ""); err != nil {
		t.Fatalf("主播进房失败: %v", err)
	}
	// 16 Mbps 上行 / 2 Mbps 码率 → K0 = 6：三个观众应全部直连主播。
	if err := m.UpdateMetrics(r.ID, "host", model.Metrics{UploadCapacityBps: 16_000_000, RTTMs: 10}); err != nil {
		t.Fatalf("主播上报度量失败: %v", err)
	}
	index := sampleMediaIndex(2_000_000)
	if err := m.SetMediaIndex(r.ID, "host", &index); err != nil {
		t.Fatalf("发布索引失败: %v", err)
	}

	for _, id := range []string{"v1", "v2", "v3"} {
		if err := m.Join(r.ID, id, id, model.RoleViewer, ""); err != nil {
			t.Fatalf("%s 进房失败: %v", id, err)
		}
	}

	for _, id := range []string{"host", "v1", "v2", "v3"} {
		env := bus.lastDirectOfType(t, id, model.TypeParentAssignment)
		if env.Topology == nil {
			t.Fatalf("%s 没有收到拓扑分配", id)
		}
	}

	hostTopo := bus.lastDirectOfType(t, "host", model.TypeParentAssignment).Topology
	if hostTopo.Depth != 0 || len(hostTopo.Children) != 3 {
		t.Fatalf("主播应是深度 0 且带 3 个子节点，实际 %+v", hostTopo)
	}
	for _, id := range []string{"v1", "v2", "v3"} {
		topo := bus.lastDirectOfType(t, id, model.TypeParentAssignment).Topology
		if topo.PrimaryID != "host" || topo.Depth != 1 {
			t.Fatalf("%s 应直连主播，实际 %+v", id, topo)
		}
		if len(topo.BackupIDs) == 0 {
			t.Fatalf("%s 应至少有一个备用父（主父慢时才有退路）", id)
		}
		if topo.Mode != string(ModeFanout) {
			t.Fatalf("模式应为 fanout，实际 %q", topo.Mode)
		}
	}

	capacity := bus.lastBroadcastOfType(t, r.ID, model.TypeCapacity)
	if capacity.Capacity == nil || capacity.Capacity.Mode != string(ModeFanout) || capacity.Capacity.HostChildSlots != 6 {
		t.Fatalf("容量广播不正确: %+v", capacity.Capacity)
	}
}

func TestChainModeGivesHostExactlyOneChild(t *testing.T) {
	m, bus := newTestManager(t, 16)
	r, err := m.Create("", "", 0)
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	if err := m.Join(r.ID, "host", "主播", model.RoleHost, ""); err != nil {
		t.Fatalf("主播进房失败: %v", err)
	}
	index := sampleMediaIndex(2_000_000)
	if err := m.SetMediaIndex(r.ID, "host", &index); err != nil {
		t.Fatalf("发布索引失败: %v", err)
	}
	for _, id := range []string{"relay", "leaf1", "leaf2"} {
		if err := m.Join(r.ID, id, id, model.RoleViewer, ""); err != nil {
			t.Fatalf("%s 进房失败: %v", id, err)
		}
	}

	// 主播实测 4 Mbps → K0 = floor(4×0.8/2) = 1 → 单链模式；
	// relay 实测 20 Mbps → 它必须成为那个唯一的分发节点。
	if err := m.UpdateMetrics(r.ID, "host", model.Metrics{UploadCapacityBps: 4_000_000}); err != nil {
		t.Fatalf("主播上报失败: %v", err)
	}
	if err := m.UpdateMetrics(r.ID, "relay", model.Metrics{UploadCapacityBps: 20_000_000}); err != nil {
		t.Fatalf("relay 上报失败: %v", err)
	}

	hostTopo := bus.lastDirectOfType(t, "host", model.TypeParentAssignment).Topology
	if hostTopo.Mode != string(ModeChain) {
		t.Fatalf("K0=1 时应为单链模式，实际 %q", hostTopo.Mode)
	}
	if len(hostTopo.Children) != 1 || hostTopo.Children[0] != "relay" {
		t.Fatalf("单链模式下主播只能有 1 个子节点（分发节点），实际 %v", hostTopo.Children)
	}
	if hostTopo.DistributorID != "relay" {
		t.Fatalf("分发节点应为上行最强的 relay，实际 %q", hostTopo.DistributorID)
	}

	relayTopo := bus.lastDirectOfType(t, "relay", model.TypeParentAssignment).Topology
	if relayTopo.PrimaryID != "host" || relayTopo.Depth != 1 {
		t.Fatalf("分发节点应直连主播，实际 %+v", relayTopo)
	}

	for _, id := range []string{"leaf1", "leaf2"} {
		topo := bus.lastDirectOfType(t, id, model.TypeParentAssignment).Topology
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
	if err := m.Join(r.ID, "host", "主播", model.RoleHost, ""); err != nil {
		t.Fatalf("主播进房失败: %v", err)
	}
	index := sampleMediaIndex(2_000_000)
	if err := m.SetMediaIndex(r.ID, "host", &index); err != nil {
		t.Fatalf("发布索引失败: %v", err)
	}
	if err := m.Join(r.ID, "relay1", "转发1", model.RoleViewer, ""); err != nil {
		t.Fatalf("relay1 进房失败: %v", err)
	}

	if err := m.UpdateMetrics(r.ID, "host", model.Metrics{UploadCapacityBps: 4_000_000}); err != nil {
		t.Fatalf("主播上报失败: %v", err)
	}
	if err := m.UpdateMetrics(r.ID, "relay1", model.Metrics{UploadCapacityBps: 6_000_000}); err != nil {
		t.Fatalf("relay1 上报失败: %v", err)
	}
	if got := bus.lastDirectOfType(t, "host", model.TypeParentAssignment).Topology.DistributorID; got != "relay1" {
		t.Fatalf("分发节点应为 relay1，实际 %q", got)
	}

	// 再加入一个强得多的节点：换防必须发生，并且要广播出来。
	if err := m.Join(r.ID, "relay2", "转发2", model.RoleViewer, ""); err != nil {
		t.Fatalf("relay2 进房失败: %v", err)
	}
	before := bus.broadcastCount(r.ID, model.TypeDistributorChange)
	if err := m.UpdateMetrics(r.ID, "relay2", model.Metrics{UploadCapacityBps: 30_000_000}); err != nil {
		t.Fatalf("relay2 上报失败: %v", err)
	}

	if got := bus.lastDirectOfType(t, "host", model.TypeParentAssignment).Topology.DistributorID; got != "relay2" {
		t.Fatalf("应换防到强得多的 relay2，实际 %q", got)
	}
	if after := bus.broadcastCount(r.ID, model.TypeDistributorChange); after <= before {
		t.Fatalf("换防必须广播 distributor-change（before=%d after=%d）", before, after)
	}

	change := bus.lastBroadcastOfType(t, r.ID, model.TypeDistributorChange)
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
	if err := m.Join(r.ID, "host", "主播", model.RoleHost, ""); err != nil {
		t.Fatalf("主播进房失败: %v", err)
	}

	have := []byte{0x01, 0x02, 0x03}
	if err := m.SetChunkReport(r.ID, "host", have, true); err != nil {
		t.Fatalf("记录分片上报失败: %v", err)
	}

	r.mu.Lock()
	member := r.members["host"]
	r.mu.Unlock()
	if member == nil || !bytes.Equal(member.HaveBits, have) || !member.Complete {
		t.Fatalf("分片上报未被记录: %+v", member)
	}

	if err := m.SetChunkReport(r.ID, "ghost", have, false); !errors.Is(err, ErrNotJoined) {
		t.Fatalf("非成员上报应返回 ErrNotJoined，实际 %v", err)
	}
}

func TestSendTopologyForUnknownMember(t *testing.T) {
	m, _ := newTestManager(t, 8)
	r, err := m.Create("", "", 0)
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	if err := m.Join(r.ID, "host", "主播", model.RoleHost, ""); err != nil {
		t.Fatalf("主播进房失败: %v", err)
	}
	if err := m.SendTopology(r.ID, "ghost"); !errors.Is(err, ErrNotJoined) {
		t.Fatalf("非成员请求拓扑应返回 ErrNotJoined，实际 %v", err)
	}
	if err := m.SendTopology(r.ID, "host"); err != nil {
		t.Fatalf("主播请求拓扑不应失败: %v", err)
	}
}

// directCount 统计发往某连接的指定类型消息条数。
func (f *fakeBus) directCount(id, msgType string) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	count := 0
	for _, raw := range f.direct[id] {
		if env, err := model.Unmarshal(raw); err == nil && env.Type == msgType {
			count++
		}
	}
	return count
}

// setupFanoutRoom 搭一个"有第二条路可走"的房间：主播 5 Mbps（K0=2）配 2 Mbps 码率，
// 三个观众各 5 Mbps 上行 → v1/v2 直连主播，v3 只能挂在 v1 下面（深度 2）。
func setupFanoutRoom(t *testing.T, m *Manager, bus *fakeBus) *Room {
	t.Helper()

	r, err := m.Create("", "", 0)
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	if err := m.Join(r.ID, "host", "主播", model.RoleHost, ""); err != nil {
		t.Fatalf("主播进房失败: %v", err)
	}
	if err := m.UpdateMetrics(r.ID, "host", model.Metrics{UploadCapacityBps: 5_000_000, RTTMs: 10}); err != nil {
		t.Fatalf("主播上报度量失败: %v", err)
	}
	index := sampleMediaIndex(2_000_000)
	if err := m.SetMediaIndex(r.ID, "host", &index); err != nil {
		t.Fatalf("发布索引失败: %v", err)
	}
	for _, id := range []string{"v1", "v2", "v3"} {
		if err := m.Join(r.ID, id, id, model.RoleViewer, ""); err != nil {
			t.Fatalf("%s 进房失败: %v", id, err)
		}
		if err := m.UpdateMetrics(r.ID, id, model.Metrics{UploadCapacityBps: 5_000_000, RTTMs: 30}); err != nil {
			t.Fatalf("%s 上报度量失败: %v", id, err)
		}
	}
	_ = bus

	return r
}

// TestStallTriggersPathChange 覆盖"卡顿 → 服务器换路"这条链路（SPEC §7.5）：
// v3 原本挂在 v1 下面，上报一次新卡顿后必须换到另一条路上（v2），
// 且避开的应当是它**当前**的主父。
func TestStallTriggersPathChange(t *testing.T) {
	m, bus := newTestManager(t, 16)
	r := setupFanoutRoom(t, m, bus)

	before := bus.lastDirectOfType(t, "v3", model.TypeParentAssignment).Topology
	if before == nil || before.PrimaryID != "v1" || before.Depth != 2 {
		t.Fatalf("前置条件不成立：v3 应挂在 v1 下（深度 2），实际 %+v", before)
	}

	// 一次真实卡顿：stallCount 从 0 涨到 1，同时带着 degraded 与空缓冲。
	if err := m.UpdateMetrics(r.ID, "v3", model.Metrics{
		UploadCapacityBps: 5_000_000,
		RTTMs:             30,
		StallCount:        1,
		BufferHealth:      0,
		Degraded:          true,
		PrimaryID:         "v1",
	}); err != nil {
		t.Fatalf("上报卡顿失败: %v", err)
	}

	after := bus.lastDirectOfType(t, "v3", model.TypeParentAssignment).Topology
	if after == nil {
		t.Fatal("换路后 v3 没有收到新的拓扑分配")
	}
	if after.PrimaryID == "v1" {
		t.Fatalf("卡顿后仍挂在原来的主父上，路径没有改变: %+v", after)
	}
	// 唯一另一条"深度最小"的路是 v2（同为深度 1 的兄弟节点）。
	if after.PrimaryID != "v2" {
		t.Fatalf("卡顿后应换到 v2，实际 %+v", after)
	}
	if after.Depth != before.Depth {
		t.Fatalf("换路不应改变树的深度：原来 %d，现在 %d", before.Depth, after.Depth)
	}

	// 换路必须只影响当事节点：v1/v2 的父子关系不变。
	if got := bus.lastDirectOfType(t, "v1", model.TypeParentAssignment).Topology.PrimaryID; got != "host" {
		t.Fatalf("v1 不应被换路影响，实际主父 %q", got)
	}
}

// TestDegradedAloneDoesNotReplan 说明为什么触发条件是"新卡顿"而不是 degraded：
// 门控等待、刚起播的节点都会上报 degraded，按它换路会让整屋不停搬家。
func TestDegradedAloneDoesNotReplan(t *testing.T) {
	m, bus := newTestManager(t, 16)
	r := setupFanoutRoom(t, m, bus)

	countBefore := bus.directCount("v3", model.TypeParentAssignment)

	for i := 0; i < 3; i++ {
		if err := m.UpdateMetrics(r.ID, "v3", model.Metrics{
			UploadCapacityBps: 5_000_000,
			RTTMs:             30,
			StallCount:        0,
			BufferHealth:      1,
			Degraded:          true,
			PrimaryID:         "v1",
		}); err != nil {
			t.Fatalf("上报 degraded 失败: %v", err)
		}
	}

	if got := bus.directCount("v3", model.TypeParentAssignment); got != countBefore {
		t.Fatalf("只有 degraded 时不应重新下发拓扑，条数从 %d 变成 %d", countBefore, got)
	}
	if got := bus.lastDirectOfType(t, "v3", model.TypeParentAssignment).Topology.PrimaryID; got != "v1" {
		t.Fatalf("只有 degraded 时不应换路，实际主父 %q", got)
	}
}

// setupDeepChainRoom 搭一条"只能往下加深"的链：主播 4 Mbps（K0=1 → 单链模式），
// relay 5 Mbps（2 个位），三个叶子没有上行 —— 所以拓扑只能 host → relay → l1/l2 → l3。
// 深度上限因此成为唯一的裁决者：上限 3 时 l3 落在第 3 层，上限 2 时 l3 安置不下。
//
// 所有成员都在上报实测上行**之前**进房：准入闸门只按实测容量开闸（SPEC §6.2），
// 先测后进会让后面的人直接被 ErrFull 挡住，测的就不是深度了。
func setupDeepChainRoom(t *testing.T, maxDepth int) (*Manager, *fakeBus, *Room) {
	t.Helper()

	m, bus := newTestManager(t, 16)
	// Manager 持的是同一个 cfg 指针，Room 在 Create 里读取它 → 必须在 Create 之前设好。
	m.cfg.Room.MaxDepth = maxDepth

	r, err := m.Create("", "", 0)
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	if err := m.Join(r.ID, "host", "主播", model.RoleHost, ""); err != nil {
		t.Fatalf("主播进房失败: %v", err)
	}
	for _, id := range []string{"relay", "l1", "l2", "l3"} {
		if err := m.Join(r.ID, id, id, model.RoleViewer, ""); err != nil {
			t.Fatalf("%s 进房失败: %v", id, err)
		}
	}
	index := sampleMediaIndex(2_000_000)
	if err := m.SetMediaIndex(r.ID, "host", &index); err != nil {
		t.Fatalf("发布索引失败: %v", err)
	}

	// 主播实测 4 Mbps → K0=1 → 单链；relay 实测 5 Mbps → 它有 2 个位。
	if err := m.UpdateMetrics(r.ID, "host", model.Metrics{UploadCapacityBps: 4_000_000, RTTMs: 10}); err != nil {
		t.Fatalf("主播上报失败: %v", err)
	}
	if err := m.UpdateMetrics(r.ID, "relay", model.Metrics{UploadCapacityBps: 5_000_000, RTTMs: 20}); err != nil {
		t.Fatalf("relay 上报失败: %v", err)
	}

	return m, bus, r
}

// TestRoomHonorsConfiguredMaxDepth 锁定 PR_MAX_DEPTH 真的接到了**真实调用点**：
// 配置 2 时任何成员深度都不超过 2，放不下的进 Unassigned（而不是突破上限继续加深）；
// 配置 3（默认）时同一个成员正好落在第 3 层。
// 同时校验下发给客户端的 MaxDepth 字段用的是同一个值 —— 两边不一致会让客户端按另一套上限理解。
func TestRoomHonorsConfiguredMaxDepth(t *testing.T) {
	_, bus, r := setupDeepChainRoom(t, 2)

	r.mu.Lock()
	plan := r.plan
	r.mu.Unlock()

	deepest := 0
	for id, a := range plan.Assignments {
		if a.Depth > deepest {
			deepest = a.Depth
		}
		if a.Depth > 2 {
			t.Fatalf("MaxDepth=2 时节点 %s 深度 %d 超限", id, a.Depth)
		}
	}
	if deepest != 2 {
		t.Fatalf("链上应当铺满到第 2 层，实际最深 %d", deepest)
	}
	if len(plan.Unassigned) != 1 || plan.Unassigned[0] != "l3" {
		t.Fatalf("MaxDepth=2 时 l3 应进 Unassigned，实际 %v", plan.Unassigned)
	}
	if _, ok := plan.Assignments["l3"]; ok {
		t.Fatal("未安置的 l3 不应出现在分配表里")
	}
	if got := bus.lastDirectOfType(t, "l1", model.TypeParentAssignment).Topology.MaxDepth; got != 2 {
		t.Fatalf("下发的 maxDepth 应为配置值 2，实际 %d", got)
	}

	// 配置 3：同一个人应当被安置在第 3 层。
	_, bus3, r3 := setupDeepChainRoom(t, 3)

	r3.mu.Lock()
	plan3 := r3.plan
	r3.mu.Unlock()

	if len(plan3.Unassigned) != 0 {
		t.Fatalf("MaxDepth=3 时不应有人安置不下，实际 %v", plan3.Unassigned)
	}
	if got := plan3.Assignments["l3"].Depth; got != 3 {
		t.Fatalf("MaxDepth=3 时 l3 应落在第 3 层，实际深度 %d", got)
	}
	if got := bus3.lastDirectOfType(t, "l3", model.TypeParentAssignment).Topology.MaxDepth; got != 3 {
		t.Fatalf("下发的 maxDepth 应为配置值 3，实际 %d", got)
	}
}

// TestRoomMaxDepthFallsBackToDefault 锁定"没配"与"配成 0"都不会把上限变成 0：
// 0 会让除主播外一个人都放不下，那是配置事故而不是配置项。
func TestRoomMaxDepthFallsBackToDefault(t *testing.T) {
	m, _ := newTestManager(t, 16)
	m.cfg.Room.MaxDepth = 0

	r, err := m.Create("", "", 0)
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	if got := r.maxDepth(); got != DefaultMaxDepth {
		t.Fatalf("MaxDepth 未配置时应退回 %d，实际 %d", DefaultMaxDepth, got)
	}
}

// TestJoinGateStopsWhenTreeIsActuallyFull 是空位口径收紧的**端到端**效果：
// 树在深度上限处铺满后 GateSlots = 0，闸门按"现有成员数"收口，
// 后来者拿到 ROOM_FULL（明确被拒），而不是"进得来但没有父节点"。
//
// **这是一次行为变更**：旧口径把最深一层的虚位也算进 GateSlots（本用例里是 4 个），
// 于是闸门会一路放到 8 人 —— 多出来的 4 人永远拿不到 parent-assignment，
// 前端也没有任何信号。有效座位数没变（还是 4），变的是那 4 个"只能干等"的人不再被放进来。
func TestJoinGateStopsWhenTreeIsActuallyFull(t *testing.T) {
	m, _ := newTestManager(t, 16)
	m.cfg.Room.MaxDepth = 2 // 链只能铺到第 2 层

	r, err := m.Create("", "", 0)
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	if err := m.Join(r.ID, "host", "主播", model.RoleHost, ""); err != nil {
		t.Fatalf("主播进房失败: %v", err)
	}
	for _, id := range []string{"relay", "l1", "l2"} {
		if err := m.Join(r.ID, id, id, model.RoleViewer, ""); err != nil {
			t.Fatalf("%s 进房失败: %v", id, err)
		}
	}
	index := sampleMediaIndex(2_000_000)
	if err := m.SetMediaIndex(r.ID, "host", &index); err != nil {
		t.Fatalf("发布索引失败: %v", err)
	}
	// 主播 4 Mbps → K0=1 → 单链；relay 5 Mbps → 2 个位：host → relay → l1/l2 正好铺满 2 层。
	if err := m.UpdateMetrics(r.ID, "host", model.Metrics{UploadCapacityBps: 4_000_000, RTTMs: 10}); err != nil {
		t.Fatalf("主播上报失败: %v", err)
	}
	if err := m.UpdateMetrics(r.ID, "relay", model.Metrics{UploadCapacityBps: 5_000_000, RTTMs: 20}); err != nil {
		t.Fatalf("relay 上报失败: %v", err)
	}

	r.mu.Lock()
	plan := r.plan
	limit := r.joinLimitLocked(16)
	r.mu.Unlock()

	if len(plan.Assignments) != 4 || len(plan.Unassigned) != 0 {
		t.Fatalf("前置条件不成立：4 人应全部安置，实际 已安置=%d 未安置=%v", len(plan.Assignments), plan.Unassigned)
	}
	if plan.FreeSlots != 0 || plan.GateSlots != 0 {
		t.Fatalf("铺满第 2 层后空位应为 0，实际 FreeSlots=%d GateSlots=%d", plan.FreeSlots, plan.GateSlots)
	}
	if limit != 4 {
		t.Fatalf("闸门应按现有成员数收口（4），实际 %d", limit)
	}

	// 第 5 个人必须被明确拒绝，而不是进来干等。
	if err := m.Join(r.ID, "late", "迟到观众", model.RoleViewer, ""); !errors.Is(err, ErrFull) {
		t.Fatalf("树已满时应返回 ErrFull，实际 %v", err)
	}

	_, members, _, _, _ := r.Snapshot(16)
	if len(members) != 4 {
		t.Fatalf("房间人数不应变化：期望 4，实际 %d", len(members))
	}
	for _, mi := range members {
		if mi.Role == model.RoleHost {
			continue
		}
		if mi.PrimaryID == "" {
			t.Fatalf("%s 进了房却没有父节点 —— 这正是旧口径「准入虚高」的病症", mi.ID)
		}
	}
}

// TestStallReplanIsRateLimited 覆盖换路限流：连续卡顿上报不能变成"每几秒搬一次家"。
func TestStallReplanIsRateLimited(t *testing.T) {
	m, bus := newTestManager(t, 16)
	r := setupFanoutRoom(t, m, bus)

	if err := m.UpdateMetrics(r.ID, "v3", model.Metrics{StallCount: 1, Degraded: true, PrimaryID: "v1"}); err != nil {
		t.Fatalf("上报卡顿失败: %v", err)
	}
	afterFirst := bus.directCount("v3", model.TypeParentAssignment)

	// 立刻再报两次新卡顿：都在 3s 限流窗口内，不应再触发换路。
	for _, n := range []int{2, 3} {
		if err := m.UpdateMetrics(r.ID, "v3", model.Metrics{StallCount: n, Degraded: true, PrimaryID: "v2"}); err != nil {
			t.Fatalf("上报卡顿失败: %v", err)
		}
	}

	if got := bus.directCount("v3", model.TypeParentAssignment); got != afterFirst {
		t.Fatalf("3s 限流窗口内不应重复换路：条数从 %d 变成 %d", afterFirst, got)
	}
}
