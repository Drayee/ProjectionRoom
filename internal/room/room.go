// Package room 管理房间生命周期、成员表、房主控制与聊天广播，
// 是"谁是成员、谁是主播、当前播放状态与容量"的唯一权威（SPEC §3 职责边界）。
//
// 本包不接触 WebSocket 连接，只通过 Broadcaster 接口投递消息，
// 因此可以脱离网络单独测试。
package room

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"ProjectionRoom/internal/protocol"
	"ProjectionRoom/internal/topology"
)

var (
	// ErrRoomExists 表示房间码已被占用。
	ErrRoomExists = errors.New("room: 房间码已存在")
	// ErrNotFound 表示房间不存在（或已随主播离开而销毁）。
	ErrNotFound = errors.New("room: 房间不存在")
	// ErrBadPassword 表示房间密码错误。
	ErrBadPassword = errors.New("room: 密码错误")
	// ErrFull 表示房间已达当前容量上限。
	ErrFull = errors.New("room: 房间已满")
	// ErrHostTaken 表示房间已有一位主播。
	ErrHostTaken = errors.New("room: 房间已有主播")
	// ErrNotReady 表示主播尚未进房，观众无法加入。
	ErrNotReady = errors.New("room: 主播尚未进房")
	// ErrNotHost 表示该操作只有主播可以执行。
	ErrNotHost = errors.New("room: 只有主播可以执行该操作")
	// ErrNotJoined 表示连接尚未加入房间。
	ErrNotJoined = errors.New("room: 尚未加入房间")
	// ErrAlreadyJoined 表示该连接已在房间中。
	ErrAlreadyJoined = errors.New("room: 该连接已在房间中")
	// ErrBadName 表示昵称不合法。
	ErrBadName = errors.New("room: 昵称需为 1-24 个字符")
	// ErrBadInput 表示请求参数不合法。
	ErrBadInput = errors.New("room: 参数不合法")
	// ErrBadMediaIndex 表示主播发布的分片索引不自洽。
	ErrBadMediaIndex = errors.New("room: 分片索引不合法")
	// ErrMediaLocked 表示索引已锁定：中途换片需要重开房间（SPEC §8.1）。
	ErrMediaLocked = errors.New("room: 分片索引已锁定（换片需重开房间）")
)

// Member 是房间内的一个成员。
type Member struct {
	ID          string
	DisplayName string
	Role        string
	Depth       int
	PrimaryID   string
	BackupIDs   []string
	JoinedAt    time.Time

	// 实测度量：拓扑分配与容量计算全部来自这里（SPEC §6.2、§6.4）。
	RTTMs             float64
	ThroughputBps     float64
	UploadCapacityBps int64

	// 分片拥有情况（base64 位图）。服务端只做记录，供 M4 监控面板展示；
	// 逐分片的父节点选择在客户端用同一张位图直接完成（SPEC §6.4）。
	HaveBits string
	Complete bool
}

func (m *Member) info() protocol.MemberInfo {
	backups := make([]string, len(m.BackupIDs))
	copy(backups, m.BackupIDs)

	return protocol.MemberInfo{
		ID:          m.ID,
		DisplayName: m.DisplayName,
		Role:        m.Role,
		Depth:       m.Depth,
		PrimaryID:   m.PrimaryID,
		BackupIDs:   backups,
		JoinedAt:    m.JoinedAt.UnixMilli(),
	}
}

// Room 的并发保护：members / order / HostID / Seq / lastPlayback / MediaIndex
// 与实测度量的所有读写都必须持 mu；
// 只读字段（ID/CreatedAt/Password）构造后不再变更。
type Room struct {
	mu sync.Mutex

	ID        string
	Password  string
	CreatedAt time.Time

	HostID string
	// StreamBps 是容量模型的码率输入：主播发布索引前用配置估计值，之后用索引里的实测码率。
	StreamBps int64
	// MediaIndex 是主播发布的分片索引，一经设定即锁定（SPEC §8.1）。
	MediaIndex json.RawMessage

	Seq          int64
	lastPlayback protocol.PlaybackState

	members map[string]*Member
	order   []string

	// plan 是最近一次拓扑计算；lastSent 记录已下发给每个成员的分配指纹，避免重复广播（SPEC §6.3）。
	plan         topology.Plan
	lastSent     map[string]string
	lastAssignAt time.Time
}

// Info 返回某个成员的信息。
func (r *Room) Info(memberID string) (protocol.MemberInfo, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	m, ok := r.members[memberID]
	if !ok {
		return protocol.MemberInfo{}, false
	}
	return m.info(), true
}

// IsMember 判断连接是否为本房间成员。
// 用于信令定向转发前的跨房校验（防止 A 房间的连接给 B 房间的人发信令）。
func (r *Room) IsMember(memberID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, ok := r.members[memberID]
	return ok
}

// MemberInfos 返回按加入顺序排列的成员表。
func (r *Room) MemberInfos() []protocol.MemberInfo {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.memberInfosLocked()
}

// Snapshot 返回入房快照所需的房间状态。
// 包含当前播放状态与分片索引，让后进房的人立刻与房主对齐（SPEC §7.1）。
func (r *Room) Snapshot(maxMembers int) (
	hostID string,
	members []protocol.MemberInfo,
	playback protocol.PlaybackState,
	capacity protocol.Capacity,
	mediaIndex json.RawMessage,
) {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.HostID, r.memberInfosLocked(), r.lastPlayback, r.capacityLocked(maxMembers), r.MediaIndex
}

// Capacity 返回当前容量判断。
func (r *Room) Capacity(maxMembers int) protocol.Capacity {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.capacityLocked(maxMembers)
}

// capacityLocked 计算容量判断。
// 口径全部来自最近一次拓扑计算：K0 是主播能直连的人数，空位数决定还能进几个人。
// 还没算过拓扑时模式是 pending —— 容量未知就如实说未知，不能拿一个默认值假装知道（SPEC §6.2）。
func (r *Room) capacityLocked(maxMembers int) protocol.Capacity {
	capacity := protocol.Capacity{
		Mode:       protocol.ModePending,
		MaxMembers: r.joinLimitLocked(maxMembers),
		StreamBps:  r.StreamBps,
	}

	// 只有实测过主播上行才敢报出具体容量；否则保持 pending。
	if r.plan.Measured {
		capacity.Mode = string(r.plan.Mode)
		capacity.HostChildSlots = r.plan.HostSlots
	}

	return capacity
}

// joinLimitLocked 返回当前允许的成员总数。
//
// 上限 = 现有成员 + 树上仍然空着的子节点位。这是比"1+K0"更准的口径：
// 每个有上行余量的转发节点都在为房间贡献容量（SPEC §6.2）。
// 还没算过拓扑时只受配置里的硬上限约束。
func (r *Room) joinLimitLocked(maxMembers int) int {
	if len(r.plan.Assignments) == 0 || !r.plan.Measured {
		return maxMembers
	}

	// 只按实测容量开闸：未实测的转发节点一个位都不算。
	limit := len(r.members) + r.plan.GateSlots
	if limit > maxMembers {
		limit = maxMembers
	}
	if limit < 1 {
		limit = 1
	}
	return limit
}

// participantsLocked 把当前成员整理成拓扑算法的输入。
func (r *Room) participantsLocked() []topology.Participant {
	out := make([]topology.Participant, 0, len(r.order))
	for i, id := range r.order {
		member, ok := r.members[id]
		if !ok {
			continue
		}

		p := topology.Participant{
			ID:        member.ID,
			IsHost:    member.Role == protocol.RoleHost,
			UploadBps: member.UploadCapacityBps,
			RTTMs:     member.RTTMs,
			// 稳定性暂时按 1 处理：重连计数属于 M4 的监控指标。
			Stability: 1,
			Order:     i + 1,
		}
		// 已经挂着的父节点只要还有余量就保持不变，避免每次重算都搬家。
		if previous, ok := r.plan.Assignments[member.ID]; ok {
			p.CurrentPrimary = previous.PrimaryID
		}
		out = append(out, p)
	}
	return out
}

// topologyLocked 生成某个成员的拓扑下发内容（调用方需持锁）。
func (r *Room) topologyLocked(peerID string) *protocol.TopologyAssignment {
	a, ok := r.plan.Assignments[peerID]
	if !ok {
		return nil
	}
	return &protocol.TopologyAssignment{
		PeerID:        a.PeerID,
		PrimaryID:     a.PrimaryID,
		BackupIDs:     a.BackupIDs,
		Children:      a.Children,
		Depth:         a.Depth,
		Mode:          string(r.plan.Mode),
		DistributorID: r.plan.DistributorID,
		Reason:        r.plan.Reason,
		MaxDepth:      topology.DefaultMaxDepth,
	}
}

// TopologyFor 返回某个成员当前的拓扑位置。
func (r *Room) TopologyFor(peerID string) *protocol.TopologyAssignment {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.topologyLocked(peerID)
}

func (r *Room) memberInfosLocked() []protocol.MemberInfo {
	out := make([]protocol.MemberInfo, 0, len(r.order))
	for _, id := range r.order {
		if m, ok := r.members[id]; ok {
			out = append(out, m.info())
		}
	}
	return out
}
