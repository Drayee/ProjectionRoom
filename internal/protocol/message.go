// Package protocol 定义客户端与服务端之间的 WebSocket 文本消息契约（SPEC §5.1）。
//
// M1 阶段消息种类少、字段扁平，因此统一使用一个 Envelope 承载，
// 保证前后端字段名只有一处定义。二进制分片帧不在本包定义：
// 它走 WebRTC DataChannel（SPEC §5.2），服务端不经手。
//
// 方向约定：
//   - 客户端上行（join / room-control / metrics）使用 Envelope 的扁平字段；
//   - 服务端下行的播放权威信息统一放在 Playback 里，
//     避免"上行扁平、下行嵌套"两套字段名并存。
package protocol

import (
	"encoding/json"

	"ProjectionRoom/internal/media"
)

// 注意：本文件的 json tag 现在只作为"字段名说明"保留 ——
// 线上编码已经是 protobuf（见 pb.go 与 proto/projection_room.proto）。

// 客户端 → 服务端。
const (
	TypeJoin            = "join"
	TypeSignal          = "signal"
	TypeMediaIndex      = "media-index"
	TypeChunksReport    = "chunks-report"
	TypeMetrics         = "metrics"
	TypeTopologyRequest = "topology-request"
	TypeRoomControl     = "room-control"
	TypeChat            = "chat"
	TypeLeave           = "leave"
)

// 服务端 → 客户端。
const (
	TypeJoined            = "joined"
	TypeMemberJoined      = "member-joined"
	TypeMemberLeft        = "member-left"
	TypeMemberList        = "member-list"
	TypeCapacity          = "capacity"
	TypeTopology          = "topology"
	TypeParentAssignment  = "parent-assignment"
	TypeDistributorChange = "distributor-change"
	TypeRoomClosed        = "room-closed"
	TypeError             = "error"
)

// 错误码（SPEC §5.1）。
const (
	CodeBadRequest    = "BAD_REQUEST"
	CodeRoomNotFound  = "ROOM_NOT_FOUND"
	CodeBadPassword   = "BAD_PASSWORD"
	CodeRoomFull      = "ROOM_FULL"
	CodeNotHost       = "NOT_HOST"
	CodeNotJoined     = "NOT_JOINED"
	CodeAlreadyJoin   = "ALREADY_JOINED"
	CodeHostTaken     = "HOST_TAKEN"
	CodeRoomNotReady  = "ROOM_NOT_READY"
	CodeRoomClosed    = "ROOM_CLOSED"
	CodeCrossRoom     = "CROSS_ROOM_SIGNAL"
	CodeBadMediaIndex = "BAD_MEDIA_INDEX"
	CodeMediaLocked   = "MEDIA_LOCKED"
	CodeInternalError = "INTERNAL"
)

// 角色。
const (
	RoleHost   = "host"
	RoleViewer = "viewer"
)

// 房主控制动作。
const (
	ActionPlay  = "play"
	ActionPause = "pause"
	ActionSeek  = "seek"
	ActionRate  = "rate"
)

// ModePending 表示尚未拿到实测上行、容量未定（SPEC §6.1、§6.2）。
// M3 之后由 internal/topology 给出 fanout / chain。
const ModePending = "pending"

// Envelope 是所有 WebSocket 文本消息的统一封装。
// 字段按用途分组并全部 omitempty，保证线上报文可读、可审计。
type Envelope struct {
	Type string `json:"type"`

	// 路由
	RoomID   string `json:"roomId,omitempty"`
	ClientID string `json:"clientId,omitempty"`
	To       string `json:"to,omitempty"`
	From     string `json:"from,omitempty"`

	// join
	DisplayName string `json:"displayName,omitempty"`
	Role        string `json:"role,omitempty"`
	Password    string `json:"password,omitempty"`

	// 信令透传：服务端原样转发，不解析 payload
	Payload json.RawMessage `json:"payload,omitempty"`

	// 聊天
	Text string `json:"text,omitempty"`
	TS   int64  `json:"ts,omitempty"`

	// 客户端上行：房主控制意图（服务端校验权限后重新广播）
	Action      string  `json:"action,omitempty"`
	CurrentTime float64 `json:"currentTime,omitempty"`
	HostClockMs int64   `json:"hostClockMs,omitempty"`
	ClockEpoch  string  `json:"clockEpoch,omitempty"`
	Paused      bool    `json:"paused,omitempty"`
	Rate        float64 `json:"rate,omitempty"`

	// 服务端下行：播放权威信息与单调序号（SPEC §5.3、§7.1）
	Playback *PlaybackState `json:"playback,omitempty"`
	Seq      int64          `json:"seq,omitempty"`

	// 实测度量（SPEC §6.2、§6.3）
	Metrics *Metrics `json:"metrics,omitempty"`

	// 入房快照 / 成员变化
	SelfID  string       `json:"selfId,omitempty"`
	HostID  string       `json:"hostId,omitempty"`
	Member  *MemberInfo  `json:"member,omitempty"`
	Members []MemberInfo `json:"members,omitempty"`

	// 拓扑（SPEC §6.1–§6.3）
	Topology    *TopologyAssignment `json:"topology,omitempty"`
	Distributor *DistributorChange  `json:"distributor,omitempty"`

	// 分片拥有情况（base64 位图），服务端记录用于监控；父节点选择在客户端用位图直接完成
	// Have 是分片拥有位图（raw bytes，不再是 base64 字符串）。
	Have     []byte `json:"have,omitempty"`
	Complete bool   `json:"complete,omitempty"`

	// 房间元信息与错误
	MediaIndex *media.Index `json:"mediaIndex,omitempty"`
	Capacity   *Capacity    `json:"capacity,omitempty"`
	Code       string       `json:"code,omitempty"`
	Message    string       `json:"message,omitempty"`
	// Reason 说明请求或通知的缘由（如 topology-request 的 "stalled"）。
	Reason string `json:"reason,omitempty"`
}

// PlaybackState 是播放权威信息的服务端表示。
// 主播是唯一时间权威（SPEC §7.1）：这些值由主播给出，服务端只负责加盖单调 seq 并广播；
// 中继节点逐跳原样转发，不得改写 currentTime 或 hostClockMs。
type PlaybackState struct {
	Paused      bool    `json:"paused"`
	CurrentTime float64 `json:"currentTime"`
	HostClockMs int64   `json:"hostClockMs"`
	Rate        float64 `json:"rate"`
	Seq         int64   `json:"seq"`
	// ClockEpoch 是主播页面的时钟纪元。主播重新加载页面后 performance.now() 归零，
	// 观众侧的时钟偏移滤波必须整体重置，否则全房间会一起跳到错误位置（C13）。
	ClockEpoch string `json:"clockEpoch,omitempty"`
}

// Metrics 是成员上报的实测度量。
// M2 只用到主播的 UploadCapacityBps（容量闸门）；M3 的评分与选举会用到全部字段。
type Metrics struct {
	// RTTMs 是 DataChannel ping/pong 实测往返延迟。
	RTTMs float64 `json:"rttMs,omitempty"`
	// ThroughputBps 是实测交付速率。
	ThroughputBps float64 `json:"throughputBps,omitempty"`
	// UploadCapacityBps 取自 getStats().availableOutgoingBitrate，是容量计算的唯一可信来源（C11）。
	UploadCapacityBps int64 `json:"uploadCapacityBps,omitempty"`
	// Depth 是该节点在拓扑中的深度。
	Depth int `json:"depth,omitempty"`

	// 以下用于"卡顿 → 服务器重算路径"：客户端主动上报健康度，
	// 服务器据此把落后节点重挂到更合适的父节点上（而不是让它一直拖着自己）。
	BufferHealth  float64 `json:"bufferHealth,omitempty"`
	P95DeliveryMs float64 `json:"p95DeliveryMs,omitempty"`
	StallCount    int     `json:"stallCount,omitempty"`
	Degraded      bool    `json:"degraded,omitempty"`
	PrimaryID     string  `json:"primaryId,omitempty"`
}

// TopologyAssignment 是一个节点在分发树中的位置（SPEC §6.1）。
// Children 只包含"以本节点为主父"的成员：进度与时钟锚点沿这条路径逐跳转发（§7.5）。
type TopologyAssignment struct {
	PeerID        string   `json:"peerId"`
	PrimaryID     string   `json:"primaryId,omitempty"`
	BackupIDs     []string `json:"backupIds,omitempty"`
	Children      []string `json:"children,omitempty"`
	Depth         int      `json:"depth"`
	Mode          string   `json:"mode"`
	DistributorID string   `json:"distributorId,omitempty"`
	Reason        string   `json:"reason,omitempty"`
	MaxDepth      int      `json:"maxDepth"`
}

// DistributorChange 是单链模式下分发节点的换防通知（SPEC §6.3）。
type DistributorChange struct {
	FromID string `json:"fromId,omitempty"`
	ToID   string `json:"toId,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// MemberInfo 是成员列表中的一个条目。
type MemberInfo struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"displayName"`
	Role        string   `json:"role"`
	Depth       int      `json:"depth"`
	PrimaryID   string   `json:"primaryId,omitempty"`
	BackupIDs   []string `json:"backupIds,omitempty"`
	JoinedAt    int64    `json:"joinedAt"`
}

// Capacity 描述房间当前的容量判断（SPEC §6.2）。
// Mode 为 pending 时 HostChildSlots 无意义（固定 0）：还没有实测上行，不能假装容量已知。
type Capacity struct {
	Mode           string `json:"mode"`
	MaxMembers     int    `json:"maxMembers"`
	StreamBps      int64  `json:"streamBps"`
	HostChildSlots int    `json:"hostChildSlots"`
}

// ErrorEnvelope 构造错误消息。
func ErrorEnvelope(code, message string) []byte {
	return MustEnvelope(Envelope{Type: TypeError, Code: code, Message: message})
}
