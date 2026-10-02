package usecase

import (
	"sort"
	"strconv"
	"strings"
)

// DefaultMaxDepth 是拓扑深度上限（SPEC §6.1）：限制端到端延迟与故障半径。
const DefaultMaxDepth = 4

// DefaultMaxBackups 是每个节点的备用父数量上限（SPEC §6.1）。
const DefaultMaxBackups = 2

// HandOffFactor 是换防阈值：候选分发节点要比现任强这么多倍才换（SPEC §6.3）。
// 没有滞回的话，两个差不多的节点会来回抢位，每次都要重挂整棵子树。
const HandOffFactor = 1.5

// Participant 是分配算法的一个输入节点。
type Participant struct {
	ID     string
	IsHost bool
	// UploadBps 是实测上行；0 表示尚未测到（此时用保守默认值，而不是假装容量很大）。
	UploadBps int64
	// RTTMs 是到该节点的实测往返延迟。
	RTTMs float64
	// Stability 是 0-1 的稳定性；0 视为"未知"，不惩罚。
	Stability float64
	// Order 是加入顺序，用于让预热期的选择保持确定。
	Order int
	// CurrentPrimary 是当前主父，用于抑制抖动：只要它还有余量就继续用。
	CurrentPrimary string
}

// Options 是分配参数。
type Options struct {
	// StreamBps 是视频码率，容量模型的唯一输入。
	StreamBps int64
	// MaxDepth 为 0 时取 DefaultMaxDepth。
	MaxDepth int
	// MaxBackups 为 0 时取 DefaultMaxBackups。
	MaxBackups int
	// PreviousDistributor 是上一轮的分发节点，用于换防滞回（SPEC §6.3）。
	PreviousDistributor string
}

func (o Options) withDefaults() Options {
	if o.MaxDepth <= 0 {
		o.MaxDepth = DefaultMaxDepth
	}
	if o.MaxBackups <= 0 {
		o.MaxBackups = DefaultMaxBackups
	}
	if o.StreamBps <= 0 {
		o.StreamBps = 1
	}
	return o
}

// Assignment 是分配给一个节点的拓扑位置。
type Assignment struct {
	PeerID    string
	PrimaryID string
	BackupIDs []string
	Children  []string
	Depth     int
}

// Plan 是一次完整的拓扑计算。
type Plan struct {
	Mode Mode
	// HostSlots 是主播能直接服务的子节点数 K0（无论当前是谁在当根）。
	HostSlots int
	// RootSlots 是当前根节点的子节点位数：扇出模式等于 HostSlots，单链模式等于分发节点的容量。
	RootSlots     int
	DistributorID string
	Assignments   map[string]Assignment
	// Unassigned 是当前安置不下的节点（容量或深度不足）。
	Unassigned []string
	// Measured 表示主播的上行是否已实测。未实测时容量口径必须是 pending，
	// 准入也只能退回配置里的硬上限 —— 猜出来的容量不能拿来开闸（SPEC §6.2、C15）。
	Measured bool
	// FreeSlots 是整棵树还空着的子节点位（含主播），按排布口径算（未实测节点按保守默认）。
	FreeSlots int
	// GateSlots 是**只按实测容量**算出的空位，准入闸门用它。
	GateSlots int
	// Reason 说明模式与分发节点的选择依据，便于日志与前端展示。
	Reason string
}

type allocNode struct {
	id    string
	order int
	depth int
	// slots 是用于**排布**的子节点位数：未实测的节点用保守默认值，让预热期也能建树。
	slots int
	// slotsMeasured 是用于**准入**的位数：未实测一律记 0。
	// 两个数必须分开：拿猜测值开闸会让房间容量自我膨胀（每个新节点凭"默认 2 个位"再引入容量）。
	slotsMeasured int
	used          int
	rtt           float64
	stability     float64
}

func (n *allocNode) spare() int { return n.slots - n.used }

// score 在同深度候选之间比较"谁更适合当父节点"。
//
// 权重向余量倾斜（SPEC §6.5 把 SpareCapacityRatio 放到最高权重）：
// 只看速度的评分会把新节点持续挂到同一个最快的父节点上，直到把它压垮 ——
// 那正是"一个节点拖慢所有节点"的成因。
func (n *allocNode) score() float64 {
	if n.slots <= 0 {
		return 0
	}
	spareRatio := float64(n.spare()) / float64(n.slots)
	rttScore := 1.0 / (1.0 + n.rtt/100.0)
	stability := n.stability
	if stability <= 0 {
		stability = 1
	}
	return spareRatio*0.5 + rttScore*0.3 + stability*0.2
}

// Assign 计算整棵分发树。
//
// 规则（SPEC §6.1–§6.3）：
//   - 主播的上行决定 K0；K0 ≥ 2 用扇出模式，K0 ≤ 1 用单链分发模式；
//   - 单链模式先选出分发节点 D（按实测上行），主播只连 D 一个，其余人挂在 D 的子树下；
//   - 分配是广度优先 + 最大余量优先，深度不超过 MaxDepth；
//   - 已经挂在某个父节点下、且该父节点仍有余量时保持不变（避免抖动）。
func Assign(hostID string, participants []Participant, opts Options) Plan {
	opts = opts.withDefaults()

	plan := Plan{
		Assignments: make(map[string]Assignment, len(participants)),
	}

	byID := make(map[string]Participant, len(participants))
	var host *Participant
	ordered := make([]Participant, 0, len(participants))
	for i := range participants {
		p := participants[i]
		byID[p.ID] = p
		ordered = append(ordered, p)
		if p.IsHost || p.ID == hostID {
			host = &participants[i]
		}
	}
	if host == nil {
		plan.Mode = ModePending
		plan.Reason = "房间还没有主播"
		return plan
	}

	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Order != ordered[j].Order {
			return ordered[i].Order < ordered[j].Order
		}
		return ordered[i].ID < ordered[j].ID
	})

	viewers := make([]Participant, 0, len(ordered))
	for _, p := range ordered {
		if p.ID != host.ID {
			viewers = append(viewers, p)
		}
	}

	k0 := HostChildSlots(host.UploadBps, opts.StreamBps)
	plan.Mode = SelectMode(k0)
	plan.HostSlots = k0
	plan.RootSlots = k0
	plan.Measured = host.UploadBps > 0

	// K0 = 0 是物理下限：主播实测上行连一个节点都带不动。
	// 此时任何拓扑都救不了，只能降低码率（SPEC §4.5）或减少人数 —— 必须如实报告，不能硬塞。
	if k0 == 0 {
		plan.Mode = ModeChain
		plan.Reason = "主播实测上行连 1 个节点都带不动（K0=0）：需要降低码率或减少人数"
		plan.Assignments[host.ID] = Assignment{PeerID: host.ID, Depth: 0}
		for _, v := range viewers {
			plan.Unassigned = append(plan.Unassigned, v.ID)
		}
		return plan
	}

	// 主播自己一定在分配表里（depth=0，无父）。
	// 少了它，"子是主播的子节点"这类关系就无处落账，前端也拿不到自己的 children。
	plan.Assignments[host.ID] = Assignment{PeerID: host.ID, Depth: 0}

	// 根节点：扇出模式是主播；单链模式是选出来的分发节点。
	rootID := host.ID
	rootSlots := k0
	if plan.Mode == ModeChain {
		d := electDistributor(viewers, opts.StreamBps, len(participants))
		// 换防滞回：现任仍在房里、且新候选没有显著更强时保持现状。
		if prev := opts.PreviousDistributor; prev != "" && prev != d && hasViewer(viewers, prev) {
			if better, ok := ShouldHandOff(prev, participants, opts.StreamBps, len(participants), HandOffFactor); !ok || better != d {
				d = prev
			}
		}
		plan.DistributorID = d
		plan.Reason = "主播上行不足以直连多人（K0≤1），改由分发节点承担分发"
		if d != "" {
			rootID = d
			rootSlots = NodeCapacity(byID[d].UploadBps, opts.StreamBps)
			plan.RootSlots = rootSlots
		}
	} else {
		plan.Reason = "主播上行足够，直接扇出"
	}

	// 根节点的实测位：主播用 K0（未实测则 0）；单链模式下的分发节点用它自己的实测容量。
	rootMeasured := 0
	if rootID == host.ID {
		if plan.Measured {
			rootMeasured = k0
		}
	} else if byID[rootID].UploadBps > 0 {
		rootMeasured = NodeCapacity(byID[rootID].UploadBps, opts.StreamBps)
	}

	nodes := map[string]*allocNode{
		rootID: {
			id:            rootID,
			order:         participantOrder(byID[rootID]),
			depth:         0,
			slots:         rootSlots,
			slotsMeasured: rootMeasured,
			rtt:           byID[rootID].RTTMs,
			stability:     byID[rootID].Stability,
		},
	}

	// 单链模式下，主播唯一的子位固定给分发节点 D。
	if plan.Mode == ModeChain && rootID != host.ID {
		nodes[host.ID] = &allocNode{
			id:    host.ID,
			order: participantOrder(*host),
			depth: 0,
			slots: 1,
			// 主播唯一的位已经给了分发节点，准入口径上是满的。
			slotsMeasured: 1,
			used:          1,
			rtt:           host.RTTMs,
			stability:     host.Stability,
		}
		nodes[rootID].depth = 1
		plan.Assignments[rootID] = Assignment{PeerID: rootID, PrimaryID: host.ID, Depth: 1}
		plan.Assignments[host.ID] = Assignment{PeerID: host.ID, Depth: 0}
	}

	// 广度优先分配其余节点。
	pending := make([]Participant, 0, len(viewers))
	for _, v := range viewers {
		if v.ID == rootID {
			continue
		}
		pending = append(pending, v)
	}

	for len(pending) > 0 {
		progressed := false
		next := make([]Participant, 0, len(pending))

		for _, candidate := range pending {
			parent := chooseParent(nodes, candidate, opts)
			if parent == "" {
				next = append(next, candidate)
				continue
			}

			nodes[parent].used++
			child := &allocNode{
				id:        candidate.ID,
				order:     candidate.Order,
				depth:     nodes[parent].depth + 1,
				slots:     NodeCapacity(candidate.UploadBps, opts.StreamBps),
				rtt:       candidate.RTTMs,
				stability: candidate.Stability,
			}
			if candidate.UploadBps > 0 {
				child.slotsMeasured = NodeCapacity(candidate.UploadBps, opts.StreamBps)
			}
			nodes[candidate.ID] = child
			plan.Assignments[candidate.ID] = Assignment{
				PeerID:    candidate.ID,
				PrimaryID: parent,
				Depth:     child.depth,
			}
			progressed = true
		}

		if !progressed {
			break
		}
		pending = next
	}

	for _, leftover := range pending {
		plan.Unassigned = append(plan.Unassigned, leftover.ID)
	}

	// 主父的 children 列表 + 备用父。
	children := make(map[string][]string, len(plan.Assignments))
	for _, a := range plan.Assignments {
		if a.PrimaryID != "" {
			children[a.PrimaryID] = append(children[a.PrimaryID], a.PeerID)
		}
	}
	for id, a := range plan.Assignments {
		if list, ok := children[id]; ok {
			sort.SliceStable(list, func(i, j int) bool {
				return participantOrder(byID[list[i]]) < participantOrder(byID[list[j]])
			})
			a.Children = list
			plan.Assignments[id] = a
		}
	}

	// 备用父：只用于"主父慢/缺片"时的补取，因此按"离根近 + 有余量"挑。
	for id, a := range plan.Assignments {
		a.BackupIDs = chooseBackups(plan, nodes, byID, id, a, opts)
		plan.Assignments[id] = a
	}

	// 剩余可用子节点位：FreeSlots 按排布口径（含未实测节点的保守默认），
	// GateSlots 只认真实测容量 —— 后者才是准入闸门的依据。
	free, gate := 0, 0
	for _, n := range nodes {
		if n.spare() > 0 {
			free += n.spare()
		}
		if spare := n.slotsMeasured - n.used; spare > 0 {
			gate += spare
		}
	}
	plan.FreeSlots = free
	plan.GateSlots = gate

	return plan
}

// chooseParent 挑选父节点，规则是"先浅后优"：
//  1. 在所有还有余量、且深度允许的节点里取**深度最小**的一层 —— 树越浅，跳数与故障半径越小；
//  2. 同一层内再按 score 选（余量占比优先，其次 RTT、稳定性）；
//  3. 已经挂着的父节点只要还有余量就继续用 —— 重挂载的代价远高于收益。
func chooseParent(nodes map[string]*allocNode, candidate Participant, opts Options) string {
	if current, ok := nodes[candidate.CurrentPrimary]; ok && current.spare() > 0 && current.depth+1 <= opts.MaxDepth {
		return candidate.CurrentPrimary
	}

	// 按加入顺序遍历，而不是遍历 map：并列时必须给出一致的结果，
	// 否则同样的输入会得到不同的树，调试与验收都无从复现。
	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.SliceStable(ids, func(i, j int) bool {
		if nodes[ids[i]].order != nodes[ids[j]].order {
			return nodes[ids[i]].order < nodes[ids[j]].order
		}
		return ids[i] < ids[j]
	})

	best := ""
	bestDepth := 0
	var bestScore float64
	for _, id := range ids {
		n := nodes[id]
		if n.id == candidate.ID || n.spare() <= 0 || n.depth+1 > opts.MaxDepth {
			continue
		}
		if best == "" || n.depth < bestDepth || (n.depth == bestDepth && n.score() > bestScore) {
			best = n.id
			bestDepth = n.depth
			bestScore = n.score()
		}
	}
	return best
}

// chooseBackups 为节点挑选备用父：离根近的优先（跳数少、上行通常更宽），并且排除自己的子树。
func chooseBackups(plan Plan, nodes map[string]*allocNode, byID map[string]Participant, selfID string, self Assignment, opts Options) []string {
	descendants := map[string]bool{}
	collectDescendants(plan, selfID, descendants)

	type candidate struct {
		id    string
		depth int
		spare int
	}
	var pool []candidate
	for id, n := range nodes {
		if id == selfID || id == self.PrimaryID || descendants[id] {
			continue
		}
		pool = append(pool, candidate{id: id, depth: n.depth, spare: n.spare()})
	}
	sort.SliceStable(pool, func(i, j int) bool {
		if (pool[i].spare > 0) != (pool[j].spare > 0) {
			return pool[i].spare > 0
		}
		if pool[i].depth != pool[j].depth {
			return pool[i].depth < pool[j].depth
		}
		return participantOrder(byID[pool[i].id]) < participantOrder(byID[pool[j].id])
	})

	out := make([]string, 0, opts.MaxBackups)
	for _, c := range pool {
		if len(out) >= opts.MaxBackups {
			break
		}
		out = append(out, c.id)
	}
	return out
}

func collectDescendants(plan Plan, root string, out map[string]bool) {
	for _, a := range plan.Assignments {
		if a.PrimaryID == root {
			out[a.PeerID] = true
			collectDescendants(plan, a.PeerID, out)
		}
	}
}

// electDistributor 选出单链模式下的分发节点（SPEC §6.3）。
//
// 先按实测上行挑最强的；完全测不到数据时退回"最早加入者"，
// 让房间在预热期也能立刻跑起来，等实测数据到位后由 Reassign 纠正。
func electDistributor(viewers []Participant, streamBps int64, memberCount int) string {
	if len(viewers) == 0 {
		return ""
	}

	measured := false
	for _, v := range viewers {
		if v.UploadBps > 0 {
			measured = true
			break
		}
	}
	if !measured {
		best := viewers[0]
		for _, v := range viewers[1:] {
			if participantOrder(v) < participantOrder(best) {
				best = v
			}
		}
		return best.ID
	}

	type candidate struct {
		id        string
		capacity  int64
		stability float64
		order     int
	}
	pool := make([]candidate, 0, len(viewers))
	for _, v := range viewers {
		stability := v.Stability
		if stability <= 0 {
			stability = 1
		}
		pool = append(pool, candidate{id: v.ID, capacity: v.UploadBps, stability: stability, order: participantOrder(v)})
	}
	sort.SliceStable(pool, func(i, j int) bool {
		if pool[i].capacity != pool[j].capacity {
			return pool[i].capacity > pool[j].capacity
		}
		if pool[i].stability != pool[j].stability {
			return pool[i].stability > pool[j].stability
		}
		return pool[i].order < pool[j].order
	})

	// 优先挑"能覆盖全房间"的；一个都没有时，挑最强的那个（它就是当前最好的选择）。
	for _, c := range pool {
		if CanDistribute(c.capacity, memberCount, streamBps, c.stability) {
			return c.id
		}
	}
	return pool[0].id
}

// ShouldHandOff 判断是否需要换防到更强的分发节点（SPEC §6.3）。
// 只有候选显著更强（默认 1.5 倍）且能承担分发时才换，避免在两个差不多的节点之间来回抖。
func ShouldHandOff(currentID string, participants []Participant, streamBps int64, memberCount int, factor float64) (string, bool) {
	if factor <= 1 {
		factor = 1.5
	}

	var currentBps int64
	for _, p := range participants {
		if p.ID == currentID {
			currentBps = p.UploadBps
		}
	}

	bestID := ""
	var bestBps int64
	for _, p := range participants {
		if p.IsHost || p.ID == currentID || p.UploadBps <= 0 {
			continue
		}
		stability := p.Stability
		if stability <= 0 {
			stability = 1
		}
		if !CanDistribute(p.UploadBps, memberCount, streamBps, stability) {
			continue
		}
		if p.UploadBps > bestBps {
			bestBps = p.UploadBps
			bestID = p.ID
		}
	}
	if bestID == "" {
		return "", false
	}
	if currentBps > 0 && float64(bestBps) < float64(currentBps)*factor {
		return "", false
	}
	return bestID, true
}

func participantOrder(p Participant) int { return p.Order }

func hasViewer(viewers []Participant, id string) bool {
	for _, v := range viewers {
		if v.ID == id {
			return true
		}
	}
	return false
}

// Describe 生成一行人类可读的拓扑摘要，用于日志与监控面板。
func (p Plan) Describe() string {
	if p.Mode == ModePending {
		return "拓扑：未就绪（" + p.Reason + "）"
	}

	var b strings.Builder
	b.WriteString("拓扑：")
	b.WriteString(string(p.Mode))
	if p.DistributorID != "" {
		b.WriteString(" 分发节点=")
		b.WriteString(p.DistributorID)
	}
	b.WriteString(" 已安置=")
	b.WriteString(strconv.Itoa(len(p.Assignments)))
	b.WriteString(" 空位=")
	b.WriteString(strconv.Itoa(p.FreeSlots))
	if len(p.Unassigned) > 0 {
		b.WriteString(" 未安置=")
		b.WriteString(strings.Join(p.Unassigned, ","))
	}
	return b.String()
}
