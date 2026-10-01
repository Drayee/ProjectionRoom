package room

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/media"
	"ProjectionRoom/internal/protocol"
)

const (
	// 去掉 I/O/0/1，避免口头传房间码时听错。
	roomCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	roomCodeLen      = 6
	maxDisplayName   = 24
)

// Broadcaster 由 signal.Hub 实现。
// 接口定义在 room 包内，使依赖方向保持 httpapi → room → Broadcaster ← signal，不产生环。
type Broadcaster interface {
	SendTo(id string, msg []byte) error
	BroadcastToRoom(roomID string, msg []byte, except string) int
	RoomSize(roomID string) int
	CloseRoom(roomID string)
}

// Manager 持有全部房间；房间码级别的操作与成员上限都在这里裁决。
type Manager struct {
	cfg *config.Config
	bus Broadcaster

	mu    sync.RWMutex
	rooms map[string]*Room
}

// NewManager 构造房间管理器。
func NewManager(cfg *config.Config, bus Broadcaster) *Manager {
	return &Manager{cfg: cfg, bus: bus, rooms: make(map[string]*Room)}
}

// Create 创建房间；roomID 留空时自动生成 6 位房间码。
// 房间在主播进房前就已存在，这样观众只会拿到 ROOM_NOT_READY 而不是 ROOM_NOT_FOUND。
func (m *Manager) Create(roomID, password string, streamBps int64) (*Room, error) {
	if streamBps <= 0 {
		streamBps = m.cfg.Room.DefaultStreamBps
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if roomID == "" {
		generated, err := m.newRoomIDLocked()
		if err != nil {
			return nil, err
		}
		roomID = generated
	}
	if _, exists := m.rooms[roomID]; exists {
		return nil, ErrRoomExists
	}

	r := &Room{
		ID:           roomID,
		Password:     password,
		StreamBps:    streamBps,
		CreatedAt:    time.Now(),
		members:      make(map[string]*Member),
		lastPlayback: protocol.PlaybackState{Paused: true, Rate: 1},
	}
	m.rooms[roomID] = r
	log.Printf("room %s: 已创建（密码保护=%t，码率估计=%d bps）", roomID, password != "", streamBps)

	return r, nil
}

// Get 按房间码取房间。
func (m *Manager) Get(roomID string) (*Room, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	r, ok := m.rooms[roomID]
	return r, ok
}

// Join 把连接加入房间：校验密码/容量，然后向本人发 joined、向其他人发 member-joined。
func (m *Manager) Join(roomID, clientID, displayName, role, password string) error {
	displayName = strings.TrimSpace(displayName)
	if !validDisplayName(displayName) {
		return ErrBadName
	}
	if role != protocol.RoleHost && role != protocol.RoleViewer {
		return ErrBadInput
	}

	r, ok := m.Get(roomID)
	if !ok {
		return ErrNotFound
	}
	if r.Password != "" && r.Password != password {
		return ErrBadPassword
	}

	r.mu.Lock()
	if _, exists := r.members[clientID]; exists {
		r.mu.Unlock()
		return ErrAlreadyJoined
	}
	if role == protocol.RoleHost && r.HostID != "" {
		r.mu.Unlock()
		return ErrHostTaken
	}
	if role == protocol.RoleViewer {
		if r.HostID == "" {
			r.mu.Unlock()
			return ErrNotReady
		}
		// 容量闸门：拿到实测上行后，超过 1+K0 的人会被直接拒绝，
		// 而不是全部挂上去一起卡（SPEC §6.2）。
		if len(r.members) >= r.joinLimitLocked(m.cfg.Room.MaxMembers) {
			r.mu.Unlock()
			return ErrFull
		}
	}

	// M2 是星形占位拓扑：主播深度 0，观众深度 1 直连主播。
	// 多父/多层树的真实分配由 M3 的 internal/topology 接管（SPEC §6.1、§6.4）。
	member := &Member{
		ID:          clientID,
		DisplayName: displayName,
		Role:        role,
		JoinedAt:    time.Now(),
	}
	if role == protocol.RoleHost {
		member.Depth = 0
		r.HostID = clientID
	} else {
		member.Depth = 1
		member.PrimaryID = r.HostID
	}
	r.members[clientID] = member
	r.order = append(r.order, clientID)

	hostID := r.HostID
	infos := r.memberInfosLocked()
	playback := r.lastPlayback
	capacity := r.capacityLocked(m.cfg.Room.MaxMembers)
	mediaIndex := r.MediaIndex
	info := member.info()
	r.mu.Unlock()

	// joined 只发给本人：带完整成员表、容量判断、当前播放状态与分片索引。
	_ = m.bus.SendTo(clientID, protocol.MustEnvelope(protocol.Envelope{
		Type:       protocol.TypeJoined,
		RoomID:     roomID,
		SelfID:     clientID,
		HostID:     hostID,
		Member:     &info,
		Members:    infos,
		Playback:   &playback,
		Capacity:   &capacity,
		MediaIndex: mediaIndex,
	}))

	// member-joined 广播给其他人，让他们的成员列表刷新。
	m.bus.BroadcastToRoom(roomID, protocol.MustEnvelope(protocol.Envelope{
		Type:    protocol.TypeMemberJoined,
		RoomID:  roomID,
		Member:  &info,
		Members: infos,
	}), clientID)

	log.Printf("room %s: %s(%s) 加入，当前 %d 人", roomID, displayName, role, len(infos))

	return nil
}

// Leave 让连接离开房间。
// 主播离开会销毁整个房间：主播是唯一时间权威，没有主播就没有可播放的内容（SPEC §7.1）。
func (m *Manager) Leave(roomID, clientID string) {
	r, ok := m.Get(roomID)
	if !ok {
		return
	}

	r.mu.Lock()
	member, exists := r.members[clientID]
	if !exists {
		r.mu.Unlock()
		return
	}
	delete(r.members, clientID)
	r.order = removeString(r.order, clientID)

	wasHost := member.Role == protocol.RoleHost || r.HostID == clientID
	if wasHost {
		r.HostID = ""
	}
	infos := r.memberInfosLocked()
	remaining := len(infos)
	r.mu.Unlock()

	m.bus.BroadcastToRoom(roomID, protocol.MustEnvelope(protocol.Envelope{
		Type:     protocol.TypeMemberLeft,
		RoomID:   roomID,
		ClientID: clientID,
		Members:  infos,
	}), clientID)

	log.Printf("room %s: %s(%s) 离开，剩余 %d 人", roomID, member.DisplayName, member.Role, remaining)

	if wasHost {
		m.closeRoom(roomID, "主播已离开，房间关闭")
		return
	}
	if remaining == 0 {
		m.mu.Lock()
		delete(m.rooms, roomID)
		m.mu.Unlock()
		log.Printf("room %s: 已空，房间销毁", roomID)
	}
}

// HandleChat 校验成员身份与长度后广播聊天。
// 广播给房间内所有人（含发送者），由服务端给出唯一次序 —— 前端不本地追加，避免两份真相。
func (m *Manager) HandleChat(roomID, clientID, text string) error {
	r, ok := m.Get(roomID)
	if !ok {
		return ErrNotFound
	}

	text = strings.TrimSpace(text)
	if text == "" || len([]rune(text)) > m.cfg.Signal.MaxChatLen {
		return ErrBadInput
	}

	member, ok := r.Info(clientID)
	if !ok {
		return ErrNotJoined
	}

	m.bus.BroadcastToRoom(roomID, protocol.MustEnvelope(protocol.Envelope{
		Type:        protocol.TypeChat,
		RoomID:      roomID,
		From:        member.ID,
		DisplayName: member.DisplayName,
		Text:        text,
		TS:          time.Now().UnixMilli(),
	}), "")

	return nil
}

// SetMediaIndex 由主播发布分片索引：校验、锁定、广播（SPEC §4.3、§5.1）。
//
// 服务端必须校验：一个畸形索引会让整个房间算错容量，或者让播放器拿到 appendBuffer 一定失败的 mimeType。
func (m *Manager) SetMediaIndex(roomID, clientID string, raw json.RawMessage) error {
	r, ok := m.Get(roomID)
	if !ok {
		return ErrNotFound
	}
	if len(raw) == 0 {
		return ErrBadMediaIndex
	}

	var index media.Index
	if err := json.Unmarshal(raw, &index); err != nil {
		return ErrBadMediaIndex
	}
	if err := index.Validate(); err != nil {
		log.Printf("room %s: 主播发布的分片索引被拒绝: %v", roomID, err)
		return ErrBadMediaIndex
	}

	r.mu.Lock()
	member, exists := r.members[clientID]
	if !exists {
		r.mu.Unlock()
		return ErrNotJoined
	}
	if member.Role != protocol.RoleHost {
		r.mu.Unlock()
		return ErrNotHost
	}
	if len(r.MediaIndex) > 0 && !bytes.Equal(r.MediaIndex, raw) {
		r.mu.Unlock()
		return ErrMediaLocked
	}

	r.MediaIndex = append(json.RawMessage(nil), raw...)
	// 用索引里的实测码率替换配置估计值，容量模型从此有真实输入。
	r.StreamBps = index.BitrateBps
	capacity := r.capacityLocked(m.cfg.Room.MaxMembers)
	r.mu.Unlock()

	m.bus.BroadcastToRoom(roomID, protocol.MustEnvelope(protocol.Envelope{
		Type:       protocol.TypeMediaIndex,
		RoomID:     roomID,
		From:       clientID,
		MediaIndex: raw,
	}), clientID)
	m.broadcastCapacity(roomID, capacity)

	log.Printf("room %s: 主播发布分片索引（%d 段，%.2f Mbps，mime=%s）",
		roomID, len(index.Segments), float64(index.BitrateBps)/1_000_000, index.MimeType)

	return nil
}

// UpdateMetrics 记录成员上报的实测度量。
// 主播的上行会直接改变房间容量，所以这里要立刻重算并广播（SPEC §6.2）。
func (m *Manager) UpdateMetrics(roomID, clientID string, metrics protocol.Metrics) error {
	r, ok := m.Get(roomID)
	if !ok {
		return ErrNotFound
	}

	r.mu.Lock()
	member, exists := r.members[clientID]
	if !exists {
		r.mu.Unlock()
		return ErrNotJoined
	}
	member.RTTMs = metrics.RTTMs
	member.ThroughputBps = metrics.ThroughputBps
	member.UploadCapacityBps = metrics.UploadCapacityBps

	changed := false
	if member.Role == protocol.RoleHost && metrics.UploadCapacityBps > 0 && metrics.UploadCapacityBps != r.hostUploadBps {
		r.hostUploadBps = metrics.UploadCapacityBps
		changed = true
	}
	capacity := r.capacityLocked(m.cfg.Room.MaxMembers)
	r.mu.Unlock()

	if changed {
		m.broadcastCapacity(roomID, capacity)
		log.Printf("room %s: 主播实测上行 %.2f Mbps → K0=%d（模式 %s，成员上限 %d）",
			roomID, float64(metrics.UploadCapacityBps)/1_000_000, capacity.HostChildSlots, capacity.Mode,
			1+capacity.HostChildSlots)
	}

	return nil
}

// HandleControl 处理房主控制。
// 只有主播可以下发；服务端负责分配单调递增的 seq 并记录最新播放状态，
// 供后进房的人立即对齐（SPEC §5.3、§7.1）。
func (m *Manager) HandleControl(roomID, clientID string, in protocol.Envelope) error {
	r, ok := m.Get(roomID)
	if !ok {
		return ErrNotFound
	}

	switch in.Action {
	case protocol.ActionPlay, protocol.ActionPause, protocol.ActionSeek, protocol.ActionRate:
	default:
		return ErrBadInput
	}

	r.mu.Lock()
	member, exists := r.members[clientID]
	if !exists {
		r.mu.Unlock()
		return ErrNotJoined
	}
	if member.Role != protocol.RoleHost {
		r.mu.Unlock()
		return ErrNotHost
	}

	rate := in.Rate
	if rate <= 0 {
		rate = 1
	}

	// 播放/暂停由 action 单点决定，不信任冗余字段：
	// 否则 "action=pause" + "paused=false" 这种自相矛盾的报文会让房间状态摇摆。
	// seek / rate 不改变播放状态，沿用主播显式给出的值。
	paused := in.Paused
	switch in.Action {
	case protocol.ActionPlay:
		paused = false
	case protocol.ActionPause:
		paused = true
	case protocol.ActionSeek, protocol.ActionRate:
	}

	r.Seq++
	r.lastPlayback = protocol.PlaybackState{
		Paused:      paused,
		CurrentTime: in.CurrentTime,
		HostClockMs: in.HostClockMs,
		Rate:        rate,
		Seq:         r.Seq,
		ClockEpoch:  in.ClockEpoch,
	}
	playback := r.lastPlayback
	r.mu.Unlock()

	// 广播给除主播外的所有人：主播本地已应用，回显只会造成重复处理。
	m.bus.BroadcastToRoom(roomID, protocol.MustEnvelope(protocol.Envelope{
		Type:     protocol.TypeRoomControl,
		RoomID:   roomID,
		From:     clientID,
		Action:   in.Action,
		Playback: &playback,
		TS:       time.Now().UnixMilli(),
	}), clientID)

	return nil
}

// CloseRoomIfEmpty 用于连接断开后的兜底清理（正常情况下 Leave 已处理）。
func (m *Manager) CloseRoomIfEmpty(roomID string) {
	m.mu.Lock()
	r, ok := m.rooms[roomID]
	m.mu.Unlock()
	if !ok {
		return
	}

	r.mu.Lock()
	empty := len(r.members) == 0
	r.mu.Unlock()
	if empty {
		m.mu.Lock()
		delete(m.rooms, roomID)
		m.mu.Unlock()
	}
}

func (m *Manager) broadcastCapacity(roomID string, capacity protocol.Capacity) {
	m.bus.BroadcastToRoom(roomID, protocol.MustEnvelope(protocol.Envelope{
		Type:     protocol.TypeCapacity,
		RoomID:   roomID,
		Capacity: &capacity,
	}), "")
}

// closeRoom 销毁房间：先广播原因，再关闭连接（关闭留出投递时间，见 signal.closeGrace）。
func (m *Manager) closeRoom(roomID, reason string) {
	m.mu.Lock()
	if _, ok := m.rooms[roomID]; !ok {
		m.mu.Unlock()
		return
	}
	delete(m.rooms, roomID)
	m.mu.Unlock()

	m.bus.BroadcastToRoom(roomID, protocol.MustEnvelope(protocol.Envelope{
		Type:    protocol.TypeRoomClosed,
		RoomID:  roomID,
		Code:    protocol.CodeRoomClosed,
		Message: reason,
	}), "")
	m.bus.CloseRoom(roomID)

	log.Printf("room %s: 已关闭（%s）", roomID, reason)
}

func (m *Manager) newRoomIDLocked() (string, error) {
	// 字母表长度 32 整除 256，因此取模不引入偏差。
	buf := make([]byte, roomCodeLen)
	for attempt := 0; attempt < 16; attempt++ {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for i := range buf {
			buf[i] = roomCodeAlphabet[int(buf[i])%len(roomCodeAlphabet)]
		}
		id := string(buf)
		if _, exists := m.rooms[id]; !exists {
			return id, nil
		}
	}

	return "", ErrRoomExists
}

func validDisplayName(name string) bool {
	if name == "" {
		return false
	}
	return len([]rune(name)) <= maxDisplayName
}

func removeString(list []string, target string) []string {
	out := make([]string, 0, len(list))
	for _, item := range list {
		if item != target {
			out = append(out, item)
		}
	}
	return out
}
