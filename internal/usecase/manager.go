package usecase

import (
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/model"
	"ProjectionRoom/internal/utils"
)

const (
	// roomCodeLen 房间码长度；字符表见 utils.RoomCodeAlphabet（已去掉易听错的字符）。
	roomCodeLen    = 6
	maxDisplayName = 24

	// reassignMinInterval 限制拓扑重算频率：度量每 5s 上报一次，
	// 没有节流的话一次网络抖动就会引发一串无谓的重挂载（SPEC §6.3 要求换防平滑）。
	reassignMinInterval = 1500 * time.Millisecond

	// degradedReplanInterval 限制"卡顿换路"的频率：卡顿会连续上报多轮，
	// 每轮都重算就退化成"每几秒搬一次家"，而搬家本身又会打断缓冲 —— 那是抖动的来源。
	degradedReplanInterval = 3 * time.Second
)

// Broadcaster 由 service.Hub 实现。
// 接口定义在 usecase 包内，使依赖方向保持 handler → usecase → Broadcaster ← service，不产生环。
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

// roomMaxDepth 返回新建房间应使用的分发树深度上限（PR_MAX_DEPTH → RoomConfig.MaxDepth）。
//
// 配置为 0（例如测试里手搓的 Config）时退回算法默认值 —— 与 Options.withDefaults 同语义：
// "没配"和"配成 0"都不会把树的深度上限变成 0（那会让除主播外一个人都放不下）。
func (m *Manager) roomMaxDepth() int {
	if m.cfg.Room.MaxDepth <= 0 {
		return DefaultMaxDepth
	}
	return m.cfg.Room.MaxDepth
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
		MaxDepth:     m.roomMaxDepth(),
		members:      make(map[string]*Member),
		avoidPrimary: make(map[string]string),
		lastPlayback: model.PlaybackState{Paused: true, Rate: 1},
	}
	m.rooms[roomID] = r
	log.Printf("room %s: 已创建（密码保护=%t，码率估计=%d bps，深度上限=%d）",
		roomID, password != "", streamBps, r.MaxDepth)

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
	if role != model.RoleHost && role != model.RoleViewer {
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
	// 房间可能在拿到指针之后、拿到锁之前被 CloseRoomIfEmpty / 宽限期到期销毁：
	// 没有这道检查，一次加入会"成功"却立即被断连（拿到 joined 也毫无意义）。
	if r.closed {
		r.mu.Unlock()
		return ErrNotFound
	}
	if _, exists := r.members[clientID]; exists {
		r.mu.Unlock()
		return ErrAlreadyJoined
	}
	if role == model.RoleHost && r.HostID != "" {
		r.mu.Unlock()
		return ErrHostTaken
	}
	if role == model.RoleViewer {
		// 闸门只拦"主播从未进房"的房间 —— 那种房间没有任何可播放内容。
		//
		// 主播离线宽限期内 HostID 同样是空的，但房间还在：members / MediaIndex /
		// lastPlayback 全部保留，新观众据此就能对齐播放。若这里一律返回 ErrNotReady，
		// 同一次网络抖动里掉线的观众在主播回来之前（最长 60s）就再也回不了房 ——
		// 那等于把"主播断线"的破坏面从主播一个人扩大到全房间的观众。
		if r.HostID == "" && !r.hostOffline {
			r.mu.Unlock()
			return ErrNotReady
		}
		// 容量闸门：拿到实测上行后，超过 1+K0 的人会被直接拒绝，
		// 而不是全部挂上去一起卡（SPEC §6.2）。
		// 宽限期内用的是主播断线前的那份实测值（plan 被刻意保留），
		// 口径与断线前一致，不会凭空放大房间容量。
		if len(r.members) >= r.joinLimitLocked(m.cfg.Room.MaxMembers) {
			r.mu.Unlock()
			return ErrFull
		}
	}

	// M2 是星形占位拓扑：主播深度 0，观众深度 1 直连主播。
	// 多父/多层树的真实分配由 M3 的 assign.go 接管（SPEC §6.1、§6.4）。
	member := &Member{
		ID:          clientID,
		DisplayName: displayName,
		Role:        role,
		JoinedAt:    time.Now(),
	}
	recovered := false
	offlineFor := time.Duration(0)
	if role == model.RoleHost {
		member.Depth = 0
		r.HostID = clientID
		// 主播重连：结束宽限期，但**不重置**任何播放状态 ——
		// Seq / lastPlayback / MediaIndex / StreamBps 都要原样延续，
		// 否则观众的 seq 过滤会把恢复后的进度全部当成乱序丢掉（SPEC §7.1、§5.3）。
		if r.hostOffline {
			r.hostOffline = false
			offlineFor = time.Since(r.hostOfflineSince)
			r.hostOfflineSince = time.Time{}
			if r.hostGraceTimer != nil {
				r.hostGraceTimer.Stop()
				r.hostGraceTimer = nil
			}
			recovered = true
		}
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
	_ = m.bus.SendTo(clientID, model.MustEnvelope(model.Envelope{
		Type:       model.TypeJoined,
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
	m.bus.BroadcastToRoom(roomID, model.MustEnvelope(model.Envelope{
		Type:    model.TypeMemberJoined,
		RoomID:  roomID,
		Member:  &info,
		Members: infos,
	}), clientID)

	if recovered {
		log.Printf("room %s: %s(%s) 重连成功（离线 %s），宽限期结束，房间恢复（保留 seq=%d、分片索引=%t、房内 %d 人）",
			roomID, displayName, role, offlineFor.Round(time.Millisecond), playback.Seq, mediaIndex != nil, len(infos))
	} else {
		log.Printf("room %s: %s(%s) 加入，当前 %d 人", roomID, displayName, role, len(infos))
	}

	// 入房快照先发，拓扑分配后发（客户端据此再建 P2P 连接）。
	m.ReassignTopology(roomID, true)

	return nil
}

// Leave 让连接离开房间。
//
// 主播离开**不再**立刻销毁房间：任何 WS 断开（网络抖动 / 刷新 / 服务端重启 / 半开连接）
// 都会走到这里，而断线不等于离开。主播离开时房间进入宽限期（PR_ROOM_HOST_GRACE）：
// 保留 members / MediaIndex / lastPlayback / Seq，只把 hostOffline 置位并起一个到期定时器；
// 到期仍无主播才 closeRoom。观众离开的语义完全不变。
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
	r.order = utils.RemoveString(r.order, clientID)

	wasHost := member.Role == model.RoleHost || r.HostID == clientID
	if wasHost {
		r.HostID = ""
		// 宽限期从这里开始：房间仍在，只是"当前没有主播"。
		r.hostOffline = true
		r.hostOfflineSince = time.Now()
	}
	infos := r.memberInfosLocked()
	remaining := len(infos)
	// 主播离线宽限中：房间只剩元数据（几 KB）也**不能**删 ——
	// 删掉就等于把房间码作废，主播重连只会拿到 ROOM_NOT_FOUND。
	graceActive := r.hostOffline
	r.mu.Unlock()

	m.bus.BroadcastToRoom(roomID, model.MustEnvelope(model.Envelope{
		Type:     model.TypeMemberLeft,
		RoomID:   roomID,
		ClientID: clientID,
		Members:  infos,
	}), clientID)

	log.Printf("room %s: %s(%s) 离开，剩余 %d 人", roomID, member.DisplayName, member.Role, remaining)

	if wasHost {
		// 只广播 member-left（上面那条），不发 room-closed：房间还活着。
		m.enterHostGrace(roomID, r)
		return
	}

	if graceActive {
		// 没有主播就没有树可算：保留上一次的 plan / lastSent，
		// 这样主播重连时可以按原来的父子关系平滑恢复（SPEC §6.3 换防不强断连接）。
		log.Printf("room %s: 主播离线宽限中，暂不重算拓扑（保留原分配）", roomID)
		return
	}

	// 有人离开会腾出（或收走）容量，父子关系需要跟着变。
	m.ReassignTopology(roomID, true)

	if remaining == 0 {
		// 摘出 m.rooms 与置 closed 必须成对发生，且都在 m.mu + r.mu 临界区内：
		// 否则一个已拿到 *Room 指针、还没拿到 r.mu 的并发 Join 会把自己加进
		// 一个查不到的孤儿房间（后续 Get / 广播全部失效）。
		// 另外按身份核对一次：别的清理路径可能已经摘掉这个房间，
		// 而房间码可能已被新房间复用 —— 那时绝不能误删新房间。
		m.mu.Lock()
		removed := false
		if cur, ok := m.rooms[roomID]; ok && cur == r {
			delete(m.rooms, roomID)
			r.closed = true
			removed = true
		}
		m.mu.Unlock()
		if removed {
			log.Printf("room %s: 已空，房间销毁", roomID)
		}
	}
}

// enterHostGrace 让房间进入主播离线宽限期；grace 被配置为 <=0 时退回"立即销毁"的旧语义。
func (m *Manager) enterHostGrace(roomID string, r *Room) {
	grace := m.cfg.Room.HostGrace
	if grace <= 0 {
		m.closeRoom(roomID, "主播已离开，房间关闭")
		return
	}

	r.mu.Lock()
	if r.hostGraceTimer != nil {
		r.hostGraceTimer.Stop()
	}
	// 到期时再确认一次"仍然没有主播"：这 60 秒内主播很可能已经回来了。
	r.hostGraceTimer = time.AfterFunc(grace, func() { m.expireHostGrace(roomID) })
	remaining := len(r.members)
	r.mu.Unlock()

	log.Printf("room %s: 主播离线，进入 %s 宽限期（房间保留，房内仍有 %d 人；到期仍无主播才关闭）",
		roomID, grace, remaining)
}

// expireHostGrace 是宽限期到期的处理：仍然没有主播才销毁房间。
//
// 判定与销毁在 m.mu + r.mu 下原子完成（见 closeRoomIf），因此"到期"与"主播恰好重连"
// 不会互相覆盖：重连成功会清掉 hostOffline，这里就不再关房。
func (m *Manager) expireHostGrace(roomID string) {
	m.closeRoomIf(roomID, func(r *Room) bool { return r.hostOffline }, func(r *Room) string {
		return "主播离线超过 " + describeDuration(m.cfg.Room.HostGrace) + "，房间已关闭"
	})
}

// describeDuration 把宽限期写成人能读懂的时长（60s → "60 秒"，2m → "2 分钟"）。
func describeDuration(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		return strconv.Itoa(int(d/time.Minute)) + " 分钟"
	}
	if d >= time.Second && d%time.Second == 0 {
		return strconv.Itoa(int(d/time.Second)) + " 秒"
	}
	return d.String()
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

	m.bus.BroadcastToRoom(roomID, model.MustEnvelope(model.Envelope{
		Type:        model.TypeChat,
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
func (m *Manager) SetMediaIndex(roomID, clientID string, index *model.Index) error {
	r, ok := m.Get(roomID)
	if !ok {
		return ErrNotFound
	}
	if index == nil {
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
	if member.Role != model.RoleHost {
		r.mu.Unlock()
		return ErrNotHost
	}
	// 索引一经设定即锁定；重复发布同一份是幂等的（SPEC §8.1）。
	if r.MediaIndex != nil && !model.SameIndex(r.MediaIndex, index) {
		r.mu.Unlock()
		return ErrMediaLocked
	}

	r.MediaIndex = index
	// 用索引里的实测码率替换配置估计值，容量模型从此有真实输入。
	r.StreamBps = index.BitrateBps
	capacity := r.capacityLocked(m.cfg.Room.MaxMembers)
	r.mu.Unlock()

	m.bus.BroadcastToRoom(roomID, model.MustEnvelope(model.Envelope{
		Type:       model.TypeMediaIndex,
		RoomID:     roomID,
		From:       clientID,
		MediaIndex: index,
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
func (m *Manager) UpdateMetrics(roomID, clientID string, metrics model.Metrics) error {
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
	// 卡顿（stall）是"当前这条路已经不行了"的直接信号：立刻换路，不等 5s 度量节流。
	// degraded 单独不足以触发 —— 门控等待、刚起播的节点都会上报 degraded，
	// 拿它当触发条件会让整屋每 3 秒搬一次家，而节流的存在正是为了防这个。
	stalled := metrics.StallCount > member.StallCount
	member.StallCount = metrics.StallCount
	avoidCurrentParent := false
	if stalled && member.PrimaryID != "" && time.Since(r.lastDegradedReplanAt) >= degradedReplanInterval {
		r.lastDegradedReplanAt = time.Now()
		r.avoidPrimary[clientID] = member.PrimaryID
		avoidCurrentParent = true
		log.Printf("room %s: %s 上报卡顿（累计 %d 次，缓冲 %.1fs，模式 %s），按避开主父 %s 重新规划路径",
			roomID, clientID, member.StallCount, metrics.BufferHealth, r.plan.Mode, member.PrimaryID)
	} else if metrics.Degraded && member.PrimaryID != "" {
		log.Printf("room %s: %s 上报 degraded（缓冲 %.1fs），暂不换路（未观测到新卡顿）",
			roomID, clientID, metrics.BufferHealth)
	}
	r.mu.Unlock()

	// 上行实测值变了就必须立刻重算（容量与准入都跟着变）；
	// 只有 RTT 之类的抖动交给节流，避免无谓的重挂载。
	m.ReassignTopology(roomID, uploadChanged || avoidCurrentParent)

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
	// 主播离线宽限期内不重算拓扑：没有根节点，Assign 只会给出一个空的 pending 计划，
	// 反而会把刻意保留的分配与容量口径抹掉 —— 而主播重连后正是靠它平滑恢复。
	// 主播重连时会先把 hostOffline 清掉再调用这里，所以恢复那一次不会被拦住。
	if r.hostOffline {
		r.mu.Unlock()
		return
	}
	if !force && time.Since(r.lastAssignAt) < reassignMinInterval {
		r.mu.Unlock()
		return
	}

	previous := r.plan
	plan := Assign(r.HostID, r.participantsLocked(), Options{
		StreamBps:           r.StreamBps,
		MaxDepth:            r.maxDepth(),
		PreviousDistributor: previous.DistributorID,
	})
	r.plan = plan
	r.lastAssignAt = time.Now()
	// 避开指令是一次性的：只影响刚算完的这一轮，否则会退化成对某个父节点的永久惩罚。
	if len(r.avoidPrimary) > 0 {
		r.avoidPrimary = make(map[string]string)
	}

	var outbound []model.Envelope
	for _, a := range plan.Assignments {
		fingerprint := assignmentFingerprint(a, plan.Mode, plan.DistributorID)
		if r.lastSent[a.PeerID] == fingerprint {
			continue
		}
		if r.lastSent == nil {
			r.lastSent = make(map[string]string)
		}
		r.lastSent[a.PeerID] = fingerprint

		topologyInfo := model.TopologyAssignment{
			PeerID:        a.PeerID,
			PrimaryID:     a.PrimaryID,
			BackupIDs:     a.BackupIDs,
			Children:      a.Children,
			Depth:         a.Depth,
			Mode:          string(plan.Mode),
			DistributorID: plan.DistributorID,
			Reason:        plan.Reason,
			MaxDepth:      r.maxDepth(),
		}
		outbound = append(outbound, model.Envelope{
			Type:     model.TypeParentAssignment,
			RoomID:   roomID,
			Topology: &topologyInfo,
		})
	}

	// 把拓扑写回成员信息：成员列表与监控面板显示的应当就是真实的父子关系。
	var updatedMembers []model.MemberInfo
	for id, a := range plan.Assignments {
		member, ok := r.members[id]
		if !ok {
			continue
		}
		if member.PrimaryID == a.PrimaryID && member.Depth == a.Depth && utils.SameStrings(member.BackupIDs, a.BackupIDs) {
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
	distributorChanged := previous.Mode == ModeChain && plan.DistributorID != previous.DistributorID
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
		if err := m.bus.SendTo(env.Topology.PeerID, model.MustEnvelope(env)); err != nil {
			log.Printf("room %s: 下发拓扑给 %s 失败: %v", roomID, env.Topology.PeerID, err)
		}
	}

	if distributorChanged {
		m.bus.BroadcastToRoom(roomID, model.MustEnvelope(model.Envelope{
			Type:        model.TypeDistributorChange,
			RoomID:      roomID,
			Distributor: &model.DistributorChange{FromID: fromID, ToID: toID, Reason: reason},
		}), "")
		log.Printf("room %s: 分发节点换防 %s → %s", roomID, fromID, toID)
	}

	m.broadcastCapacity(roomID, capacity)

	if updatedMembers != nil {
		m.bus.BroadcastToRoom(roomID, model.MustEnvelope(model.Envelope{
			Type:    model.TypeMemberList,
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
func assignmentFingerprint(a Assignment, mode Mode, distributorID string) string {
	return string(mode) + "|" + distributorID + "|" + a.PrimaryID + "|" +
		strings.Join(a.BackupIDs, ",") + "|" + strings.Join(a.Children, ",") + "|" + strconv.Itoa(a.Depth)
}

// SetChunkReport 记录成员上报的分片拥有情况。
// M3 的逐分片父节点选择在客户端用同一张位图完成；服务端这里只做记录，供 M4 监控面板与诊断使用。
func (m *Manager) SetChunkReport(roomID, clientID string, have []byte, complete bool) error {
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

	return m.bus.SendTo(clientID, model.MustEnvelope(model.Envelope{
		Type:     model.TypeTopology,
		RoomID:   roomID,
		Topology: topologyInfo,
	}))
}

// HandleControl 处理房主控制。
// 只有主播可以下发；服务端负责分配单调递增的 seq 并记录最新播放状态，
// 供后进房的人立即对齐（SPEC §5.3、§7.1）。
func (m *Manager) HandleControl(roomID, clientID string, in model.Envelope) error {
	r, ok := m.Get(roomID)
	if !ok {
		return ErrNotFound
	}

	switch in.Action {
	case model.ActionPlay, model.ActionPause, model.ActionSeek, model.ActionRate:
	default:
		return ErrBadInput
	}

	r.mu.Lock()
	member, exists := r.members[clientID]
	if !exists {
		r.mu.Unlock()
		return ErrNotJoined
	}
	if member.Role != model.RoleHost {
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
	case model.ActionPlay:
		paused = false
	case model.ActionPause:
		paused = true
	case model.ActionSeek, model.ActionRate:
	}

	r.Seq++
	r.lastPlayback = model.PlaybackState{
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
	m.bus.BroadcastToRoom(roomID, model.MustEnvelope(model.Envelope{
		Type:     model.TypeRoomControl,
		RoomID:   roomID,
		From:     clientID,
		Action:   in.Action,
		Playback: &playback,
		TS:       time.Now().UnixMilli(),
	}), clientID)

	return nil
}

// CloseRoomIfEmpty 用于连接断开后的兜底清理（正常情况下 Leave 已处理）。
//
// 主播离线宽限期内**不清理**：那时房间可能真的一个人都没有（全在重连），
// 但那几 KB 元数据正是"房间码还能用"的全部依据，清了就等于把房间作废。
func (m *Manager) CloseRoomIfEmpty(roomID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	r, ok := m.rooms[roomID]
	if !ok {
		return
	}

	r.mu.Lock()
	empty := len(r.members) == 0 && !r.hostOffline
	if empty {
		// 与 Leave 的同一条不变式：摘出 m.rooms 与置 closed 在同一个临界区里成对发生，
		// 这样并发 Join 要么在摘除前完整完成（房间里有成员，本函数就不会再删），
		// 要么在摘除后被 r.closed 挡住（ErrNotFound）。
		delete(m.rooms, roomID)
		r.closed = true
	}
	r.mu.Unlock()

	if empty {
		log.Printf("room %s: 已空，房间销毁", roomID)
	}
}

func (m *Manager) broadcastCapacity(roomID string, capacity model.Capacity) {
	m.bus.BroadcastToRoom(roomID, model.MustEnvelope(model.Envelope{
		Type:     model.TypeCapacity,
		RoomID:   roomID,
		Capacity: &capacity,
	}), "")
}

// closeRoom 销毁房间：先广播原因，再关闭连接（关闭留出投递时间，见 service.closeGrace）。
func (m *Manager) closeRoom(roomID, reason string) {
	m.closeRoomIf(roomID, nil, func(*Room) string { return reason })
}

// closeRoomIf 只在 cond 成立时销毁房间，理由由 reasonOf 依据当时状态生成。
//
// cond 必须在 m.mu 与 r.mu 双锁下求值：宽限期到期（定时器协程）与主播重连（handler 协程）
// 是两条并发路径，只有把"判定 + 摘除房间 + 置 closed"放进同一个临界区，才不会出现
// "主播刚恢复又被到期定时器关掉"或"房间已关还被 Join 加入"这两种撕裂状态。
// 锁序固定为 m.mu → r.mu：仓库里没有任何一处持 r.mu 后再去拿 m.mu，因此不会死锁。
func (m *Manager) closeRoomIf(roomID string, cond func(*Room) bool, reasonOf func(*Room) string) {
	m.mu.Lock()
	r, ok := m.rooms[roomID]
	if !ok {
		m.mu.Unlock()
		return
	}

	r.mu.Lock()
	if cond != nil && !cond(r) {
		r.mu.Unlock()
		m.mu.Unlock()
		return
	}
	delete(m.rooms, roomID)
	// 关房即终止宽限期：否则定时器会在房间已被销毁后再触发一次无意义的关房。
	r.closed = true
	r.hostOffline = false
	if r.hostGraceTimer != nil {
		r.hostGraceTimer.Stop()
		r.hostGraceTimer = nil
	}
	reason := reasonOf(r)
	r.mu.Unlock()
	m.mu.Unlock()

	m.bus.BroadcastToRoom(roomID, model.MustEnvelope(model.Envelope{
		Type:    model.TypeRoomClosed,
		RoomID:  roomID,
		Code:    model.CodeRoomClosed,
		Message: reason,
	}), "")
	m.bus.CloseRoom(roomID)

	log.Printf("room %s: 已关闭（%s）", roomID, reason)
}

// newRoomIDLocked 生成一个当前未被占用的房间码；调用方需持有 m.mu。
func (m *Manager) newRoomIDLocked() (string, error) {
	for attempt := 0; attempt < 16; attempt++ {
		id, err := utils.RandomCode(utils.RoomCodeAlphabet, roomCodeLen)
		if err != nil {
			return "", err
		}
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
