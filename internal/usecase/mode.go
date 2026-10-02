// 决定分发拓扑：主播能直接带几个子节点、用扇出还是单链分发。
//
// M2 只落地模式判定与容量模型（SPEC §6.1、§6.2）——
// 它是"开播前容量提示"的依据。父节点分配、选举、换防与抗慢节点机制在 M3 接入。
package usecase

// SafetyFactor 是容量计算的安全系数：把上行用到 80% 就该停手，
// 留出的余量既是重传/抖动的缓冲，也是避免把父节点压垮的关键（SPEC §6.1）。
const SafetyFactor = 0.8

// MaxChildren 是单个节点子节点数的硬上限。
const MaxChildren = 8

// Mode 是拓扑模式。
type Mode string

const (
	// ModeFanout：主播直连 K0 个子节点，各自向下分发。
	ModeFanout Mode = "fanout"
	// ModeChain：K0 ≤ 1 时，主播只服务 1 个分发节点，由它承担全房间分发。
	ModeChain Mode = "chain"
	// ModePending：还没有实测上行数据，容量未定。
	ModePending Mode = "pending"
)

// HostChildSlots 返回主播可直接服务的子节点数 K0。
// uploadBps 或 streamBps 未知（<=0）时返回保守默认 2，而不是假装容量很大。
func HostChildSlots(uploadBps, streamBps int64) int {
	if uploadBps <= 0 || streamBps <= 0 {
		return 2
	}

	slots := int(float64(uploadBps) * SafetyFactor / float64(streamBps))
	switch {
	case slots < 1:
		// 连 1 个都带不动：要么降低码率（SPEC §4.5 低码率预设），要么房间只剩主播自己。
		return 0
	case slots > MaxChildren:
		return MaxChildren
	default:
		return slots
	}
}

// NodeCapacity 返回一个普通节点可服务的子节点数。
func NodeCapacity(uploadBps, streamBps int64) int {
	return HostChildSlots(uploadBps, streamBps)
}

// SelectMode 依据主播容量选择拓扑模式。
func SelectMode(hostSlots int) Mode {
	if hostSlots <= 1 {
		return ModeChain
	}
	return ModeFanout
}

// CanDistribute 判断某个节点能否承担全房间分发（单链模式的选举判据，SPEC §6.3）。
// capacityBps 取 getStats 估算与实测吞吐的较小值，调用方负责交叉验证后的保守取值。
func CanDistribute(capacityBps int64, memberCount int, streamBps int64, stability float64) bool {
	if memberCount <= 1 || streamBps <= 0 {
		return false
	}

	need := int64(float64(memberCount-1) * float64(streamBps) / SafetyFactor)

	return capacityBps >= need && stability >= 0.7
}
