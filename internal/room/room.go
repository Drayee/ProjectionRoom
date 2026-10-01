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

	// 实测度量。M2 只用主播的上行做容量闸门；
	// M3 的父节点评分与分发节点选举会用到全部成员的这些字段（SPEC §6.4、§6.5）。
	RTTMs             float64
	ThroughputBps     float64
	UploadCapacityBps int64
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

	// hostUploadBps 是主播的实测上行；0 表示尚未测到（容量模式为 pending）。
	hostUploadBps int64

	Seq          int64
	lastPlayback protocol.PlaybackState

	members map[string]*Member
	order   []string
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
// 没有实测上行时模式是 pending —— 容量未知就要如实说未知，不能用一个保守默认值假装知道（SPEC §6.2）。
func (r *Room) capacityLocked(maxMembers int) protocol.Capacity {
	capacity := protocol.Capacity{
		Mode:       protocol.ModePending,
		MaxMembers: r.joinLimitLocked(maxMembers),
		StreamBps:  r.StreamBps,
	}

	if r.hostUploadBps > 0 {
		slots := topology.HostChildSlots(r.hostUploadBps, r.StreamBps)
		capacity.HostChildSlots = slots
		capacity.Mode = string(topology.SelectMode(slots))
	}

	return capacity
}

// joinLimitLocked 返回当前允许的成员总数。
//
// 拿到实测上行后，M2 的星形拓扑上限就是主播能直接服务的人数（1 + K0）：
// 再多的人挂上去也只会一起卡。实测之前只受配置里的硬上限约束（SPEC §6.2）。
func (r *Room) joinLimitLocked(maxMembers int) int {
	if r.hostUploadBps <= 0 {
		return maxMembers
	}

	limit := 1 + topology.HostChildSlots(r.hostUploadBps, r.StreamBps)
	if limit > maxMembers {
		return maxMembers
	}
	return limit
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
