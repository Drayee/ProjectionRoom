// Package usecase 管理房间生命周期、成员表、房主控制与聊天广播，
// 是"谁是成员、谁是主播、当前播放状态与容量"的唯一权威（SPEC §3 职责边界）。
//
// 本包不接触 WebSocket 连接，只通过 Broadcaster 接口投递消息，
// 因此可以脱离网络单独测试。
package usecase

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"regexp"
	"sync"
	"time"

	"ProjectionRoom/internal/model"
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
	// ErrBadRoomCode 表示房间码不符合白名单格式（S-3）。
	ErrBadRoomCode = errors.New("room: 房间码必须是 4-12 位大写字母或数字（A-Z 0-9）")
	// ErrTooManyRooms 表示同时存在的房间数已达配置上限（S-3）。
	ErrTooManyRooms = errors.New("room: 房间数已达上限，请稍后重试")
	// ErrBadPasswordPolicy 表示密码长度不符合策略（S-11）。
	ErrBadPasswordPolicy = errors.New("room: 密码长度必须为 4-64 个字符（留空表示不设密码）")
	// ErrHostTokenRequired 表示宽限期内接回主播位必须携带正确的复位令牌（S-7）。
	ErrHostTokenRequired = errors.New("room: 该房间处于主播重连宽限期，需要主播复位令牌")
	// ErrJoinRateLimited 表示该 IP+房间码的 join 失败次数过多（S-11）。
	ErrJoinRateLimited = errors.New("room: 尝试过于频繁，请稍后再试")
)

// RoomCodePattern 是房间码的白名单：4-12 位大写字母或数字。
//
// 为什么是白名单而不是"长度 + 黑名单"：房间码来自 URL 路径/查询串，会被写进日志、
// 广播给全房、拼进前端路由。放开字符集等于放开"控制字符注入日志"与"超长字符串"
// 两条路（审计实测 60 KB 的 roomId 也能建房成功，而每个这样的房间都会常驻内存）。
// 4-12 位覆盖两种合法来源：自动生成的 6 位码，以及用户自选的短码。
// 注意它与 utils.RoomCodeAlphabet 不同：字母表去掉了易听错的 I/O/0/1，
// 那是"生成侧"的约束；这里必须**接受**全部 A-Z0-9，否则手输的房间码会被误拒。
var RoomCodePattern = regexp.MustCompile(`^[A-Z0-9]{4,12}$`)

// ValidRoomCode 判断房间码是否符合白名单。
func ValidRoomCode(code string) bool { return RoomCodePattern.MatchString(code) }

// 密码策略（S-11）。留空 = 不设密码（默认形态，局域网/朋友之间用）。
//
// 为什么下限是 4：1-3 位密码在"每个人都能试"的入口上没有任何意义，
// 与其给一个假的保护感，不如拒绝并让用户要么留空要么设一个像样的。
// 上限 64：压住"用超长密码把 join 变成一次哈希/拷贝攻击"的形态；
// bcrypt 的 72 字节截断界限也在这个量级，将来接账号层不会撞上。
const (
	MinPasswordLen = 4
	MaxPasswordLen = 64
)

// ValidPassword 判断密码是否符合策略（留空合法）。
func ValidPassword(pw string) bool {
	n := len([]rune(pw))
	if n == 0 {
		return true
	}
	return n >= MinPasswordLen && n <= MaxPasswordLen
}

// hostTokenGrace 是主播复位令牌（S-7）在**无成员空窗期**内的有效期。
//
// 令牌本身不设过期：房间还在，令牌就有效（它随房间一起被回收）。
// 这一项只在一种情况下生效：房间里的主播位和令牌都被占着/刚用过，
// 而房间已经一个成员都没有（全在重连）。给 5 分钟是为了让
// "主播电脑崩了、10 分钟后换台机器拿回房间"仍然可用，
// 同时不给"房间码泄露 + 长期空窗"留下无限期的窗口。
const hostTokenGrace = 5 * time.Minute

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
	// StallCount 是客户端上报的累计卡顿次数。它是"这条路已经不行了"的直接信号：
	// 服务端据此立刻换路（degraded 单独不足以触发，见 UpdateMetrics）。
	StallCount int
	// lastMetricsAt 是上一次**被处理**的 metrics 时间（S-9 的最小间隔节流用）。
	lastMetricsAt time.Time

	// 分片拥有情况（base64 位图）。服务端只做记录，供 M4 监控面板展示；
	// 逐分片的父节点选择在客户端用同一张位图直接完成（SPEC §6.4）。
	HaveBits []byte
	Complete bool
}

func (m *Member) info() model.MemberInfo {
	backups := make([]string, len(m.BackupIDs))
	copy(backups, m.BackupIDs)

	return model.MemberInfo{
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
// 与实测度量、主播离线宽限期状态（hostOffline / hostGraceTimer / closed）的所有读写都必须持 mu；
// 只读字段（ID/CreatedAt/Password/MaxDepth）构造后不再变更。
type Room struct {
	mu sync.Mutex

	ID        string
	Password  string
	CreatedAt time.Time
	// ownerUserID 是建房者（0 = 无房主：游客路径或单测直接建房）。
	//
	// ACCOUNTS §6：REST 建房必须登录，房主在**内存房间**上就确定下来，
	// 同时异步落 rooms_meta。为什么不等于"主播"：主播位由 hostToken 决定（S-7），
	// 房主是"谁建的房"（用于"我的房间"与后续的房间管理），两者可以不同人。
	// 与 ID/Password 一样是构造后不再变更的只读字段。
	ownerUserID int64

	HostID string
	// StreamBps 是容量模型的码率输入：主播发布索引前用配置估计值，之后用索引里的实测码率。
	StreamBps int64
	// MaxDepth 是分发树的深度上限，来自 PR_MAX_DEPTH（config.RoomConfig.MaxDepth）。
	// 分配（Assign）与下发（parent-assignment.MaxDepth）都必须用同一个值，
	// 否则客户端按 A 理解、服务端按 B 建树。
	MaxDepth int
	// MediaIndex 是主播发布的分片索引，一经设定即锁定（SPEC §8.1）。
	MediaIndex *model.Index

	// hostOffline / hostOfflineSince 描述"主播已断线但房间仍在宽限期内"。
	//
	// 主播断线不等于离开：WebSocket 断一次（抖动 / 刷新 / 服务端重启 / 半开连接）
	// 就要保留房间，等主播凭同一房间码重连回来（PR_ROOM_HOST_GRACE）。
	// 宽限期内 HostID 为 ""、members/lastPlayback/Seq/MediaIndex 全部原样保留。
	hostOffline      bool
	hostOfflineSince time.Time
	// hostGraceTimer 是宽限期到期定时器；主播重连或房间关闭时取消。
	hostGraceTimer *time.Timer
	// neverJoined 表示自创建以来**从未有人成功进房**。
	//
	// 为什么需要它（S-3）：僵尸房间的主要来源就是"创建了但没人进房"
	// （拿到链接的人没点进来），这类房间没有任何值得保留的状态，
	// 所以可以按"创建后 TTL"直接回收；而"主播进过房又断开"的房间
	// 走的是 hostOffline 宽限期那条路，两者的回收判据必须分开。
	neverJoined bool
	// hostToken 是主播复位令牌的 SHA-256（S-7）。
	//
	// 为什么存哈希而不是原文：房间对象会被导出到诊断面板/日志上下文的机会很多，
	// 落一个明文令牌在里面等于把"主播位"变成"谁抓到日志谁能抢"。
	// 校验是常量时间比较（subtle.ConstantTimeCompare），先 sha256 再比。
	hostToken [32]byte
	// hostTokenUntil 是令牌的失效时刻。零值表示"当前无令牌"。
	hostTokenUntil time.Time
	// hostTokenPlain 是令牌**明文**，只在同包单测里用来证明"正确令牌能接回主播位"。
	//
	// 生产路径只存哈希（下面是 hostToken）；这个字段存在的唯一理由是
	// 单测需要"重放一次真实令牌"，而令牌只在下发响应里出现过一次、不可再导出。
	// 它不是秘密泄漏面：房间对象本来就在进程内存里，而这段内存里还有房间密码原文。
	hostTokenPlain string
	// closed 表示房间已被 closeRoom 销毁。
	// Join 在持 mu 后会先检查它：否则"关房瞬间挤进来"的连接会拿到 joined 却立即被断连。
	closed bool

	Seq          int64
	lastPlayback model.PlaybackState

	members map[string]*Member
	order   []string

	// plan 是最近一次拓扑计算；lastSent 记录已下发给每个成员的分配指纹，避免重复广播（SPEC §6.3）。
	plan         Plan
	lastSent     map[string]string
	lastAssignAt time.Time
	// avoidPrimary 是"下一轮重算时避开某个成员当前主父"的一次性指令（卡顿换路）。
	// 用完即清：它只影响紧跟着的那一次规划，否则会变成永久惩罚。
	avoidPrimary map[string]string
	// lastDegradedReplanAt 是上一次因卡顿触发换路的时间，用于给换路限流。
	lastDegradedReplanAt time.Time
}

// OwnerUserID 返回房主账号 id（0 = 没有房主）。见 ownerUserID 的说明。
func (r *Room) OwnerUserID() int64 { return r.ownerUserID }

// HasPassword 返回本房间是否设了密码（T2/T3 需要判断"这是不是一个密码房"）。
//
// 为什么给一个只回答布尔的出口，而不是让调用方读 r.Password != ""：
// 密码原文是房间对象上最敏感的那个字段，而调用方（handler）只需要知道"有没有"。
// 多一个只读方法，就让"不小心把密码带出这个包"从"需要自觉"变成"需要写出来"。
// Password 是构造后不再变更的只读字段，因此这里不需要加锁。
func (r *Room) HasPassword() bool { return r.Password != "" }

// Info 返回某个成员的信息。
func (r *Room) Info(memberID string) (model.MemberInfo, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	m, ok := r.members[memberID]
	if !ok {
		return model.MemberInfo{}, false
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
func (r *Room) MemberInfos() []model.MemberInfo {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.memberInfosLocked()
}

// Snapshot 返回入房快照所需的房间状态。
// 包含当前播放状态与分片索引，让后进房的人立刻与房主对齐（SPEC §7.1）。
func (r *Room) Snapshot(maxMembers int) (
	hostID string,
	members []model.MemberInfo,
	playback model.PlaybackState,
	capacity model.Capacity,
	mediaIndex *model.Index,
) {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.HostID, r.memberInfosLocked(), r.lastPlayback, r.capacityLocked(maxMembers), r.MediaIndex
}

// Capacity 返回当前容量判断。
func (r *Room) Capacity(maxMembers int) model.Capacity {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.capacityLocked(maxMembers)
}

// capacityLocked 计算容量判断。
// 口径全部来自最近一次拓扑计算：K0 是主播能直连的人数，空位数决定还能进几个人。
// 还没算过拓扑时模式是 pending —— 容量未知就如实说未知，不能拿一个默认值假装知道（SPEC §6.2）。
func (r *Room) capacityLocked(maxMembers int) model.Capacity {
	capacity := model.Capacity{
		Mode:       model.ModePending,
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
func (r *Room) participantsLocked() []Participant {
	out := make([]Participant, 0, len(r.order))
	for i, id := range r.order {
		member, ok := r.members[id]
		if !ok {
			continue
		}

		p := Participant{
			ID:        member.ID,
			IsHost:    member.Role == model.RoleHost,
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
		// 卡顿换路：这一轮把当前主父标成"避开"（软排除）。
		p.AvoidPrimary = r.avoidPrimary[member.ID]
		out = append(out, p)
	}
	return out
}

// maxDepth 返回本房间实际使用的深度上限。
// Room.MaxDepth 由 PR_MAX_DEPTH 决定；未设置（<=0，例如直接构造 Room 的测试）时
// 退回算法默认值，语义与 Options.withDefaults 一致。
func (r *Room) maxDepth() int {
	if r.MaxDepth <= 0 {
		return DefaultMaxDepth
	}
	return r.MaxDepth
}

// topologyLocked 生成某个成员的拓扑下发内容（调用方需持锁）。
func (r *Room) topologyLocked(peerID string) *model.TopologyAssignment {
	a, ok := r.plan.Assignments[peerID]
	if !ok {
		return nil
	}
	return &model.TopologyAssignment{
		PeerID:        a.PeerID,
		PrimaryID:     a.PrimaryID,
		BackupIDs:     a.BackupIDs,
		Children:      a.Children,
		Depth:         a.Depth,
		Mode:          string(r.plan.Mode),
		DistributorID: r.plan.DistributorID,
		Reason:        r.plan.Reason,
		MaxDepth:      r.maxDepth(),
	}
}

// TopologyFor 返回某个成员当前的拓扑位置。
func (r *Room) TopologyFor(peerID string) *model.TopologyAssignment {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.topologyLocked(peerID)
}

func (r *Room) memberInfosLocked() []model.MemberInfo {
	out := make([]model.MemberInfo, 0, len(r.order))
	for _, id := range r.order {
		if m, ok := r.members[id]; ok {
			out = append(out, m.info())
		}
	}
	return out
}

// setHostToken 记录主播复位令牌的哈希（S-7）。调用方需持 mu。
//
// 令牌只在创建响应里下发一次；重复创建同一房间码会被 Create 挡掉（ErrRoomExists），
// 所以正常路径下只会设一次。
func (r *Room) setHostToken(token string) {
	if token == "" {
		return
	}
	r.hostToken = sha256.Sum256([]byte(token))
	r.hostTokenUntil = time.Now().Add(hostTokenGrace)
	r.hostTokenPlain = token
}

// hostTokenUsableLocked 判断当前是否处于"必须携带令牌"的窗口。调用方需持 mu。
//
// 为什么令牌会失效：房间已空、且超过 hostTokenGrace（5 分钟）之后，
// 令牌不再被接受 —— 那正是"房间码泄露 + 房间长期没人"的组合，
// 给一个有限窗口比给一个无限窗口更保守；房间本身也会被后台清扫回收。
func (r *Room) hostTokenUsableLocked(now time.Time) bool {
	return !now.After(r.hostTokenUntil)
}

// consumeHostTokenLocked 校验令牌，成功即作废它（一次性）。调用方需持 mu。
//
// 为什么是一次性：令牌代表"主播位"，用完即弃可以缩小泄露窗口 ——
// 重连成功之后令牌就不再有用（房间已有主播，再来会被 ErrHostTaken 挡住）。
// 用常量时间比较是为了不泄露"前几个字节对不对"。
func (r *Room) consumeHostTokenLocked(token string) bool {
	if token == "" {
		return false
	}
	got := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(got[:], r.hostToken[:]) != 1 {
		return false
	}
	r.hostTokenUntil = time.Time{}
	return true
}

// MemberCount 返回当前成员数（后台清扫用，避免外部直接碰 members）。
func (r *Room) MemberCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.members)
}
