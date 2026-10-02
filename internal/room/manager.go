package room

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/media"
	"ProjectionRoom/internal/protocol"
	"ProjectionRoom/internal/topology"
)

const (
	// 去掉 I/O/0/1，避免口头传房间码时听错。
	roomCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	roomCodeLen      = 6
	maxDisplayName   = 24

	// reassignMinInterval 限制拓扑重算频率：度量每 5s 上报一次，
	// 没有节流的话一次网络抖动就会引发一串无谓的重挂载（SPEC §6.3 要求换防平滑）。
	reassignMinInterval = 1500 * time.Millisecond
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
	topologyInfo := r.topologyLocked(clientID)
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
		Topology:   topologyInfo,
	}))

	// member-joined 广播给其他人，让他们的成员列表刷新。
	m.bus.BroadcastToRoom(roomID, protocol.MustEnvelope(protocol.Envelope{
		Type:    protocol.TypeMemberJoined,
		RoomID:  roomID,
		Member:  &info,
		Members: infos,
	}), clientID)

	log.Printf("room %s: %s(%s) 加入，当前 %d 人", roomID, displayName, role, len(infos))

	// 入房快照先发，拓扑分配后发（客户端据此再建 P2P 连接）。
	m.ReassignTopology(roomID, true)

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

	// 有人离开会腾出（或收走）容量，父子关系需要跟着变。
	m.ReassignTopology(roomID, true)

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

	// 码率变了：容量模型与顶层拓扑都要按新码率重算。
	m.ReassignTopology(roomID, true)

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
	uploadChanged := metrics.UploadCapacityBps > 0 && metrics.UploadCapacityBps != member.UploadCapacityBps
	if metrics.UploadCapacityBps > 0 {
		member.UploadCapacityBps = metrics.UploadCapacityBps
	}
	r.mu.Unlock()

	// 上行实测值变了就必须立刻重算（容量与准入都跟着变）；
	// 只有 RTT 之类的抖动交给节流，避免无谓的重挂载。
	m.ReassignTopology(roomID, uploadChanged)

	return nil
}

// ReassignTopology 重算分发拓扑，并把发生变化的那部分下发给相关成员。
//
// 只在分配真的变了才发：这正是 SPEC §6.3 要求的"换防期间不强断连接"的前提 ——
// 客户端据此复用已有连接，只是改变"谁服务谁"。
func (m *Manager) ReassignTopology(roomID string, force bool) {
	r, ok := m.Get(roomID)
	if !ok {
		return
	}

	r.mu.Lock()
	if !force && time.Since(r.lastAssignAt) < reassignMinInterval {
		r.mu.Unlock()
		return
	}

	previous := r.plan
	plan := topology.Assign(r.HostID, r.participantsLocked(), topology.Options{
		StreamBps:           r.StreamBps,
		PreviousDistributor: previous.DistributorID,
	})
	r.plan = plan
	r.lastAssignAt = time.Now()

	var outbound []protocol.Envelope
	for _, a := range plan.Assignments {
		fingerprint := assignmentFingerprint(a, plan.Mode, plan.DistributorID)
		if r.lastSent[a.PeerID] == fingerprint {
			continue
		}
		if r.lastSent == nil {
			r.lastSent = make(map[string]string)
		}
		r.lastSent[a.PeerID] = fingerprint

		topologyInfo := protocol.TopologyAssignment{
			PeerID:        a.PeerID,
			PrimaryID:     a.PrimaryID,
			BackupIDs:     a.BackupIDs,
			Children:      a.Children,
			Depth:         a.Depth,
			Mode:          string(plan.Mode),
			DistributorID: plan.DistributorID,
			Reason:        plan.Reason,
			MaxDepth:      topology.DefaultMaxDepth,
		}
		outbound = append(outbound, protocol.Envelope{
			Type:     protocol.TypeParentAssignment,
			RoomID:   roomID,
			Topology: &topologyInfo,
		})
	}

	// 把拓扑写回成员信息：成员列表与监控面板显示的应当就是真实的父子关系。
	var updatedMembers []protocol.MemberInfo
	for id, a := range plan.Assignments {
		member, ok := r.members[id]
		if !ok {
			continue
		}
		if member.PrimaryID == a.PrimaryID && member.Depth == a.Depth && sameStrings(member.BackupIDs, a.BackupIDs) {
			continue
		}
		member.PrimaryID = a.PrimaryID
		member.Depth = a.Depth
		member.BackupIDs = append([]string(nil), a.BackupIDs...)
	}
	if len(plan.Assignments) > 0 {
		updatedMembers = r.memberInfosLocked()
	}

	capacity := r.capacityLocked(m.cfg.Room.MaxMembers)
	distributorChanged := previous.Mode == topology.ModeChain && plan.DistributorID != previous.DistributorID
	fromID := previous.DistributorID
	toID := plan.DistributorID
	reason := plan.Reason
	unassigned := len(plan.Unassigned)
	summary := plan.Describe()
	r.mu.Unlock()

	for _, env := range outbound {
		if env.Topology == nil {
			continue
		}
		if err := m.bus.SendTo(env.Topology.PeerID, protocol.MustEnvelope(env)); err != nil {
			log.Printf("room %s: 下发拓扑给 %s 失败: %v", roomID, env.Topology.PeerID, err)
		}
	}

	if distributorChanged {
		m.bus.BroadcastToRoom(roomID, protocol.MustEnvelope(protocol.Envelope{
			Type:        protocol.TypeDistributorChange,
			RoomID:      roomID,
			Distributor: &protocol.DistributorChange{FromID: fromID, ToID: toID, Reason: reason},
		}), "")
		log.Printf("room %s: 分发节点换防 %s → %s", roomID, fromID, toID)
	}

	m.broadcastCapacity(roomID, capacity)

	if updatedMembers != nil {
		m.bus.BroadcastToRoom(roomID, protocol.MustEnvelope(protocol.Envelope{
			Type:    protocol.TypeMemberList,
			RoomID:  roomID,
			Members: updatedMembers,
		}), "")
	}

	if len(outbound) > 0 || distributorChanged {
		log.Printf("room %s: %s（下发 %d 条分配）", roomID, summary, len(outbound))
	}
	if unassigned > 0 {
		log.Printf("room %s: 有 %d 个成员当前安置不下（容量或深度不足）", roomID, unassigned)
	}
}

// assignmentFingerprint 用一个短字符串概括下发内容，用于判断"要不要重新下发"。
//
// 必须把 mode 与 distributorId 也算进去：单链模式下换防时，
// 各节点的父子关系可能完全没变（主播仍然只连一个子节点），但分发节点换了 ——
// 漏掉它们就会出现"换防了却没通知任何人"。
func assignmentFingerprint(a topology.Assignment, mode topology.Mode, distributorID string) string {
	return string(mode) + "|" + distributorID + "|" + a.PrimaryID + "|" +
		strings.Join(a.BackupIDs, ",") + "|" + strings.Join(a.Children, ",") + "|" + itoa(a.Depth)
}

func itoa(v int) string {
	return strconv.Itoa(v)
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// SetChunkReport 记录成员上报的分片拥有情况。
// M3 的逐分片父节点选择在客户端用同一张位图完成；服务端这里只做记录，供 M4 监控面板与诊断使用。
func (m *Manager) SetChunkReport(roomID, clientID, have string, complete bool) error {
	r, ok := m.Get(roomID)
	if !ok {
		return ErrNotFound
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	member, exists := r.members[clientID]
	if !exists {
		return ErrNotJoined
	}
	member.HaveBits = have
	member.Complete = complete

	return nil
}

// SendTopology 把当前拓扑单独发给某个成员（客户端可随时请求刷新）。
func (m *Manager) SendTopology(roomID, clientID string) error {
	r, ok := m.Get(roomID)
	if !ok {
		return ErrNotFound
	}

	topologyInfo := r.TopologyFor(clientID)
	if topologyInfo == nil {
		return ErrNotJoined
	}

	return m.bus.SendTo(clientID, protocol.MustEnvelope(protocol.Envelope{
		Type:     protocol.TypeTopology,
		RoomID:   roomID,
		Topology: topologyInfo,
	}))
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
