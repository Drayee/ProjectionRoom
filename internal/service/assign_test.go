package service

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"ProjectionRoom/internal/config"
)

func parts(items ...Participant) []Participant {
	for i := range items {
		if items[i].Order == 0 {
			items[i].Order = i + 1
		}
	}
	return items
}

func host(uploadBps int64) Participant {
	return Participant{ID: "host", IsHost: true, UploadBps: uploadBps, Order: 1}
}

func viewer(id string, order int, uploadBps int64) Participant {
	return Participant{ID: id, UploadBps: uploadBps, Order: order}
}

// viewerRTT 是带实测 RTT 的观众：延迟优先的评分里 RTT 是第一序参量，
// 因此需要它才能构造"同余量、不同 RTT"的用例。
func viewerRTT(id string, order int, uploadBps int64, rttMs float64) Participant {
	p := viewer(id, order, uploadBps)
	p.RTTMs = rttMs
	return p
}

// assertTreeSane 检查分配结果满足所有硬约束。
func assertTreeSane(t *testing.T, plan Plan, participants []Participant, opts Options) {
	t.Helper()
	opts = opts.withDefaults()

	byID := map[string]Participant{}
	for _, p := range participants {
		byID[p.ID] = p
	}

	for id, a := range plan.Assignments {
		if a.Depth > opts.MaxDepth {
			t.Fatalf("节点 %s 深度 %d 超过上限 %d", id, a.Depth, opts.MaxDepth)
		}
		if a.PrimaryID == "" {
			continue
		}
		if a.PrimaryID == id {
			t.Fatalf("节点 %s 的主父是自己", id)
		}
		parent, ok := plan.Assignments[a.PrimaryID]
		if !ok {
			t.Fatalf("节点 %s 的主父 %s 不在分配表里", id, a.PrimaryID)
		}
		if parent.Depth+1 != a.Depth {
			t.Fatalf("节点 %s 的深度 %d 与父节点 %s 的深度 %d 不一致", id, a.Depth, a.PrimaryID, parent.Depth)
		}
		for _, b := range a.BackupIDs {
			if b == id || b == a.PrimaryID {
				t.Fatalf("节点 %s 的备用父包含自己或主父: %v", id, a.BackupIDs)
			}
			if _, ok := byID[b]; !ok {
				t.Fatalf("节点 %s 的备用父 %s 不在参与者列表里", id, b)
			}
		}
	}

	// 每个节点的子节点数不能超过它的上行容量。
	used := map[string]int{}
	for _, a := range plan.Assignments {
		if a.PrimaryID != "" {
			used[a.PrimaryID]++
		}
	}
	for id, count := range used {
		var slots int
		switch {
		case byID[id].IsHost && plan.DistributorID != "":
			slots = 1 // 单链模式：主播只有 1 个上行位，已经给了分发节点
		case byID[id].IsHost:
			slots = plan.HostSlots
		case id == plan.DistributorID:
			slots = plan.RootSlots
		default:
			slots = NodeCapacity(byID[id].UploadBps, opts.StreamBps)
		}
		if count > slots {
			t.Fatalf("节点 %s 的子节点数 %d 超过容量 %d", id, count, slots)
		}
	}

	// 沿着主父一定能走到根（无环）。
	for id := range plan.Assignments {
		seen := map[string]bool{}
		cur := id
		for {
			if seen[cur] {
				t.Fatalf("节点 %s 的主父链出现环", id)
			}
			seen[cur] = true
			a, ok := plan.Assignments[cur]
			if !ok || a.PrimaryID == "" {
				break
			}
			cur = a.PrimaryID
		}
	}
}

func TestAssignFanoutFillsHostFirst(t *testing.T) {
	// 12 Mbps 上行 / 2 Mbps 码率 → K0 = 4
	participants := parts(
		host(12_000_000),
		viewer("v1", 2, 6_000_000),
		viewer("v2", 3, 6_000_000),
		viewer("v3", 4, 6_000_000),
		viewer("v4", 5, 6_000_000),
	)
	opts := Options{StreamBps: 2_000_000}

	plan := Assign("host", participants, opts)

	if plan.Mode != ModeFanout {
		t.Fatalf("K0=4 应为扇出模式，实际 %s", plan.Mode)
	}
	if plan.RootSlots != 4 {
		t.Fatalf("K0 应为 4，实际 %d", plan.RootSlots)
	}
	for _, id := range []string{"v1", "v2", "v3", "v4"} {
		a := plan.Assignments[id]
		if a.PrimaryID != "host" || a.Depth != 1 {
			t.Fatalf("%s 应直连主播（depth=1），实际 %+v", id, a)
		}
	}
	if len(plan.Assignments["host"].Children) != 4 {
		t.Fatalf("主播应有 4 个子节点，实际 %v", plan.Assignments["host"].Children)
	}
	assertTreeSane(t, plan, participants, opts)
}

func TestAssignOverflowGoesToSecondLayer(t *testing.T) {
	// 6 Mbps / 2 Mbps → K0 = 2：主播只能带 2 个，第 3、4 个必须挂到第一层的转发节点下面。
	participants := parts(
		host(6_000_000),
		viewer("relay", 2, 6_000_000),
		viewer("v2", 3, 0),
		viewer("v3", 4, 0),
	)
	opts := Options{StreamBps: 2_000_000}

	plan := Assign("host", participants, opts)

	if plan.Mode != ModeFanout {
		t.Fatalf("K0=2 应为扇出模式，实际 %s", plan.Mode)
	}
	if a := plan.Assignments["relay"]; a.PrimaryID != "host" || a.Depth != 1 {
		t.Fatalf("relay 应直连主播（depth=1），实际 %+v", a)
	}
	if len(plan.Assignments["host"].Children) != 2 {
		t.Fatalf("主播应有 2 个子节点（K0=2），实际 %v", plan.Assignments["host"].Children)
	}
	// 先浅后优：主播的 2 个位被 relay 与 v2 占满，v3 只能下沉到第二层。
	if a := plan.Assignments["v2"]; a.PrimaryID != "host" || a.Depth != 1 {
		t.Fatalf("v2 应先占用主播剩余的子位（depth=1），实际 %+v", a)
	}
	if a := plan.Assignments["v3"]; a.PrimaryID != "relay" || a.Depth != 2 {
		t.Fatalf("v3 应在主播位用完后挂到 relay 下（depth=2），实际 %+v", a)
	}
	assertTreeSane(t, plan, participants, opts)
}

func TestAssignFanoutSpreadsAcrossLayer1(t *testing.T) {
	// 16 Mbps / 2 Mbps → K0 = 6；再给两个强上行观众当转发节点。
	participants := parts(
		host(16_000_000),
		viewer("r1", 2, 8_000_000),
		viewer("r2", 3, 8_000_000),
		viewer("v1", 4, 0),
		viewer("v2", 5, 0),
		viewer("v3", 6, 0),
		viewer("v4", 7, 0),
		viewer("v5", 8, 0),
		viewer("v6", 9, 0),
		viewer("v7", 10, 0),
	)
	opts := Options{StreamBps: 2_000_000}

	plan := Assign("host", participants, opts)

	if plan.Mode != ModeFanout {
		t.Fatalf("应为扇出模式，实际 %s", plan.Mode)
	}
	if len(plan.Assignments) != 10 {
		t.Fatalf("9 个观众应全部安置，实际安置 %d", len(plan.Assignments))
	}
	if len(plan.Unassigned) != 0 {
		t.Fatalf("不应有未安置节点: %v", plan.Unassigned)
	}
	if len(plan.Assignments["host"].Children) > 6 {
		t.Fatalf("主播子节点数不能超过 K0=6，实际 %v", plan.Assignments["host"].Children)
	}
	assertTreeSane(t, plan, participants, opts)
}

func TestAssignChainPicksStrongestDistributorNotFirstComer(t *testing.T) {
	// 3 Mbps / 2 Mbps → K0=1 → 单链模式。
	// 先到的是弱上行节点，后到的上行很强：分发位必须给强的那个（SPEC §6.3）。
	participants := parts(
		host(3_000_000),
		viewer("weak", 2, 4_000_000),
		viewer("strong", 3, 20_000_000),
		viewer("v3", 4, 0),
	)
	opts := Options{StreamBps: 2_000_000}

	plan := Assign("host", participants, opts)

	if plan.Mode != ModeChain {
		t.Fatalf("K0=1 应为单链模式，实际 %s", plan.Mode)
	}
	if plan.DistributorID != "strong" {
		t.Fatalf("分发节点应为上行最强的 strong，实际 %q", plan.DistributorID)
	}
	if a := plan.Assignments["host"]; len(a.Children) != 1 || a.Children[0] != "strong" {
		t.Fatalf("主播的唯一子节点应是分发节点，实际 %v", a.Children)
	}
	if a := plan.Assignments["weak"]; a.PrimaryID != "strong" {
		t.Fatalf("弱上行节点应挂在分发节点下，实际 %+v", a)
	}
	assertTreeSane(t, plan, participants, opts)
}

func TestAssignChainWarmupFallsBackToFirstComer(t *testing.T) {
	// 主播上行只够带 1 个人（K0=1 → 单链），但所有观众都还没上报实测上行：
	// 预热期必须先把分发位给最早加入的人，让房间立刻可用（SPEC §6.3）。
	participants := parts(
		host(3_000_000),
		viewer("first", 2, 0),
		viewer("second", 3, 0),
	)
	opts := Options{StreamBps: 2_000_000}

	plan := Assign("host", participants, opts)

	if plan.Mode != ModeChain {
		t.Fatalf("K0=1 应为单链模式，实际 %s", plan.Mode)
	}
	if plan.DistributorID != "first" {
		t.Fatalf("预热期分发节点应为最早加入者，实际 %q", plan.DistributorID)
	}
	if a := plan.Assignments["second"]; a.PrimaryID != "first" {
		t.Fatalf("其余成员应挂在分发节点下，实际 %+v", a)
	}
	assertTreeSane(t, plan, participants, opts)
}

func TestAssignUnknownUploadUsesConservativeDefault(t *testing.T) {
	// 上行未知 → HostChildSlots 返回保守默认 2（不是"容量很大"）。
	participants := parts(host(0), viewer("v1", 2, 0), viewer("v2", 3, 0), viewer("v3", 4, 0))
	opts := Options{StreamBps: 2_000_000}

	plan := Assign("host", participants, opts)

	if plan.RootSlots != 2 {
		t.Fatalf("未实测时 K0 应为保守默认 2，实际 %d", plan.RootSlots)
	}
	if plan.Mode != ModeFanout {
		t.Fatalf("K0=2 应为扇出模式，实际 %s", plan.Mode)
	}
	assertTreeSane(t, plan, participants, opts)
}

func TestAssignK0ZeroAssignsNobody(t *testing.T) {
	// 2 Mbps 上行 / 2 Mbps 码率 → K0=0：物理上一个人都带不动。
	participants := parts(host(2_000_000), viewer("v1", 2, 50_000_000))
	opts := Options{StreamBps: 2_000_000}

	plan := Assign("host", participants, opts)

	if len(plan.Assignments) != 1 {
		t.Fatalf("K0=0 时只应有主播自身，实际分配了 %d 个节点", len(plan.Assignments))
	}
	if len(plan.Unassigned) != 1 || plan.Unassigned[0] != "v1" {
		t.Fatalf("观众应被标记为未安置，实际 %v", plan.Unassigned)
	}
	if !strings.Contains(plan.Reason, "码率") {
		t.Fatalf("原因里应给出可执行的出路（降码率），实际 %q", plan.Reason)
	}
}

func TestAssignDepthLimitLeavesNodesUnassigned(t *testing.T) {
	// 只有 root 一个位、每个转发节点也只有 1 个位，深度上限 2 → 第 3 层放不下。
	participants := parts(
		host(16_000_000),
		viewer("a", 2, 2_500_000), // NodeCapacity = floor(2.5*0.8/2) = 1
		viewer("b", 3, 0),         // 未实测 → 保守默认 2
		viewer("c", 4, 0),
	)
	opts := Options{StreamBps: 2_000_000, MaxDepth: 2}

	plan := Assign("host", participants, opts)

	assertTreeSane(t, plan, participants, opts)
	for id, a := range plan.Assignments {
		if a.Depth > 2 {
			t.Fatalf("节点 %s 深度 %d 超过 MaxDepth=2", id, a.Depth)
		}
	}
}

func TestAssignKeepsCurrentParentToAvoidChurn(t *testing.T) {
	participants := parts(
		host(12_000_000),
		viewer("big", 2, 20_000_000),
		viewer("v1", 3, 0),
	)
	participants[2].CurrentPrimary = "host"
	opts := Options{StreamBps: 2_000_000}

	plan := Assign("host", participants, opts)

	// 主播还有余量，v1 又已经挂在它下面 → 不应该因为 big 更"强"就搬家。
	if a := plan.Assignments["v1"]; a.PrimaryID != "host" {
		t.Fatalf("当前父节点仍有余量时不应重挂，实际 %+v", a)
	}
}

func TestAssignBackupsExcludeSelfPrimaryAndDescendants(t *testing.T) {
	participants := parts(
		host(4_000_000),
		viewer("relay", 2, 8_000_000),
		viewer("leaf", 3, 0),
		viewer("other", 4, 0),
	)
	opts := Options{StreamBps: 2_000_000}

	plan := Assign("host", participants, opts)

	relay := plan.Assignments["relay"]
	for _, b := range relay.BackupIDs {
		if b == "relay" || b == relay.PrimaryID {
			t.Fatalf("备用父不应包含自己或主父: %v", relay.BackupIDs)
		}
		if b == "leaf" {
			t.Fatalf("备用父不应包含自己的下游节点: %v", relay.BackupIDs)
		}
	}

	leaf := plan.Assignments["leaf"]
	if leaf.PrimaryID != "relay" {
		t.Fatalf("leaf 应挂在 relay 下，实际 %+v", leaf)
	}
	if len(leaf.BackupIDs) == 0 {
		t.Fatal("叶子节点也应至少有一个备用父，否则主父一慢就无从补救")
	}
	assertTreeSane(t, plan, participants, opts)
}

func TestAssignFreeSlotsReflectRemainingCapacity(t *testing.T) {
	participants := parts(host(12_000_000), viewer("v1", 2, 0))
	opts := Options{StreamBps: 2_000_000}

	plan := Assign("host", participants, opts)

	// 主播 4 位用掉 1 位；v1 未实测 → 2 位。
	if plan.FreeSlots != 3+2 {
		t.Fatalf("空位应为 3+2=5，实际 %d", plan.FreeSlots)
	}
}

func TestShouldHandOff(t *testing.T) {
	stream := int64(2_000_000)

	current := []Participant{
		{ID: "d1", UploadBps: 6_000_000, Order: 2},
		{ID: "d2", UploadBps: 20_000_000, Order: 3},
		{ID: "d3", UploadBps: 7_000_000, Order: 4},
	}

	// d2 是 3 倍以上强，且能承担 4 人分发 → 应换防。
	if id, ok := ShouldHandOff("d1", current, stream, 4, 1.5); !ok || id != "d2" {
		t.Fatalf("应换防到 d2，实际 id=%q ok=%v", id, ok)
	}

	// 只强一点点（d3 是 7/6 ≈ 1.17 倍）→ 不换防，避免来回抖。
	only := []Participant{
		{ID: "d1", UploadBps: 6_000_000, Order: 2},
		{ID: "d3", UploadBps: 7_000_000, Order: 4},
	}
	if id, ok := ShouldHandOff("d1", only, stream, 4, 1.5); ok {
		t.Fatalf("差距不足 1.5 倍时不应换防，实际换到 %q", id)
	}

	// 候选虽然上行高但稳定性不达标（0.5 < 0.7）→ 不能换。
	unstable := []Participant{
		{ID: "d1", UploadBps: 6_000_000, Order: 2},
		{ID: "flaky", UploadBps: 50_000_000, Order: 3, Stability: 0.5},
	}
	if id, ok := ShouldHandOff("d1", unstable, stream, 4, 1.5); ok {
		t.Fatalf("稳定性不达标的候选不应被选为分发节点，实际 %q", id)
	}
}

func TestAssignKeepsDistributorWithoutSignificantUpgrade(t *testing.T) {
	// 现任分发节点 6 Mbps，新候选 7 Mbps（1.17 倍）：不到 1.5 倍就不换，
	// 否则每次度量刷新都会重挂整棵子树。
	participants := parts(
		host(3_000_000),
		viewer("incumbent", 2, 6_000_000),
		viewer("challenger", 3, 7_000_000),
	)
	opts := Options{StreamBps: 2_000_000, PreviousDistributor: "incumbent"}

	plan := Assign("host", participants, opts)

	if plan.DistributorID != "incumbent" {
		t.Fatalf("差距不足 1.5 倍时不应换防，实际换成 %q", plan.DistributorID)
	}
	if a := plan.Assignments["host"]; len(a.Children) != 1 || a.Children[0] != "incumbent" {
		t.Fatalf("主播的唯一子节点应保持为现任分发节点，实际 %v", a.Children)
	}
	assertTreeSane(t, plan, participants, opts)
}

func TestAssignHandsOffToSignificantlyStrongerNode(t *testing.T) {
	participants := parts(
		host(3_000_000),
		viewer("incumbent", 2, 6_000_000),
		viewer("challenger", 3, 20_000_000),
	)
	opts := Options{StreamBps: 2_000_000, PreviousDistributor: "incumbent"}

	plan := Assign("host", participants, opts)

	if plan.DistributorID != "challenger" {
		t.Fatalf("候选强 3 倍以上时应换防，实际 %q", plan.DistributorID)
	}
}

func TestAssignReplacesDepartedDistributor(t *testing.T) {
	// 现任已经离开房间：无论强弱都必须重新选，否则整棵树会挂在一个不存在的节点上。
	participants := parts(
		host(3_000_000),
		viewer("newcomer", 2, 6_000_000),
	)
	opts := Options{StreamBps: 2_000_000, PreviousDistributor: "gone"}

	plan := Assign("host", participants, opts)

	if plan.DistributorID != "newcomer" {
		t.Fatalf("现任离开后应重新选举，实际 %q", plan.DistributorID)
	}
	if _, ok := plan.Assignments["gone"]; ok {
		t.Fatal("已离开的节点不应出现在分配表里")
	}
}

// TestAssignAvoidPrimaryMovesToSibling 覆盖"卡顿换路"的正面效果（SPEC §7.5）：
// 被标为避开的父节点这一轮不再被选中，连"继续用现任"的捷径都取消。
func TestAssignAvoidPrimaryMovesToSibling(t *testing.T) {
	opts := Options{StreamBps: 2_000_000}
	// 主播 K0 = floor(5*0.8/2) = 2：v1、v2 直连主播，v3 只能挂到 v1 下面。
	participants := parts(
		host(5_000_000),
		viewer("v1", 2, 5_000_000),
		viewer("v2", 3, 5_000_000),
		viewer("v3", 4, 5_000_000),
	)

	first := Assign("host", participants, opts)
	if got := first.Assignments["v3"].PrimaryID; got != "v1" {
		t.Fatalf("前置条件不成立：v3 应挂在 v1 下，实际 %q", got)
	}

	// 模拟服务端：把每个成员的当前主父写回，并把 v3 的当前主父标成"避开"。
	for i := range participants {
		if a, ok := first.Assignments[participants[i].ID]; ok {
			participants[i].CurrentPrimary = a.PrimaryID
		}
	}
	for i := range participants {
		if participants[i].ID == "v3" {
			participants[i].AvoidPrimary = "v1"
		}
	}

	second := Assign("host", participants, opts)
	if got := second.Assignments["v3"].PrimaryID; got != "v2" {
		t.Fatalf("避开 v1 后 v3 应换到 v2，实际 %q", got)
	}
	if second.Assignments["v3"].Depth != first.Assignments["v3"].Depth {
		t.Fatalf("换路不应改变树的深度：原来 %d，现在 %d",
			first.Assignments["v3"].Depth, second.Assignments["v3"].Depth)
	}
	assertTreeSane(t, second, participants, opts)
}

// TestAssignAvoidPrimaryFallsBackWhenOnlyOption 覆盖软排除语义：
// 被避开的父节点是唯一可行选择时仍然要用它 ——
// 硬性排除会把节点从"卡顿"直接变成"没有数据"，那比卡顿更糟。
func TestAssignAvoidPrimaryFallsBackWhenOnlyOption(t *testing.T) {
	opts := Options{StreamBps: 2_000_000}
	participants := parts(host(5_000_000), viewer("v1", 2, 5_000_000))
	participants[1].CurrentPrimary = "host"
	participants[1].AvoidPrimary = "host"

	plan := Assign("host", participants, opts)

	a, ok := plan.Assignments["v1"]
	if !ok || a.PrimaryID != "host" {
		t.Fatalf("避开的父节点是唯一选择时应退回选它，实际 %+v", a)
	}
	if len(plan.Unassigned) != 0 {
		t.Fatalf("不应有未安置节点，实际 %v", plan.Unassigned)
	}
}

// ---------- 延迟优先：权重重排（行为变更） ----------
//
// 这一组用例锁定的是**一次刻意的行为变更**：产品目标已明确为「延迟与卡顿优先、观众跨运营商」，
// 因此父节点评分从"余量 0.5 / RTT 0.3 / 稳定性 0.2"改为"RTT 0.5 / 稳定性 0.25 / 余量 0.25"。
// 原来那些"余量优先"的期望值不是被放宽了，而是被**反向**锁死：
// 同样的候选集合，旧权重要选高 RTT 的父节点，新权重必须选低 RTT 的那个。

// scoreLegacySpareFirst 是本次改动**之前**的评分公式（余量优先）。
//
// 它只活在测试里，用于打印前后对照表、证明"选择结果确实按目标翻转了"；
// 生产代码里已经不存在这个公式（见 allocNode.score）。
func scoreLegacySpareFirst(n *allocNode) float64 {
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

// bestByScoreFn 返回按给定评分最高的节点 id 与分数，并列时取加入顺序靠前者
// （与 bestParentByScore 的遍历口径一致）。
func bestByScoreFn(nodes map[string]*allocNode, score func(*allocNode) float64) (string, float64) {
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

	best, bestScore := "", 0.0
	for _, id := range ids {
		if s := score(nodes[id]); best == "" || s > bestScore {
			best, bestScore = id, s
		}
	}
	return best, bestScore
}

// TestScoreWeightsLatencyFirstFlipsChoice 是本次行为变更的核心证据：
// 同一组候选（A 余量 0.9 / RTT 180ms、B 余量 0.5 / RTT 30ms、C 余量 0.6 / RTT 60ms）
// 在旧权重下选 A（余量最大但最慢），在新权重下必须选 B（最快）。
func TestScoreWeightsLatencyFirstFlipsChoice(t *testing.T) {
	nodes := map[string]*allocNode{
		"A": {id: "A", order: 1, depth: 1, slots: 10, used: 1, rtt: 180, stability: 1}, // 余量 0.9
		"B": {id: "B", order: 2, depth: 1, slots: 10, used: 5, rtt: 30, stability: 1},  // 余量 0.5
		"C": {id: "C", order: 3, depth: 1, slots: 10, used: 4, rtt: 60, stability: 1},  // 余量 0.6
	}

	t.Log("候选人 | 余量占比 | RTT(ms) | 旧公式（余量0.5/RTT0.3/稳定性0.2） | 新公式（RTT0.5/稳定性0.25/余量0.25）")
	for _, id := range []string{"A", "B", "C"} {
		n := nodes[id]
		t.Logf("%s | %.2f | %3.0f | %.4f | %.4f",
			id, float64(n.spare())/float64(n.slots), n.rtt, scoreLegacySpareFirst(n), n.score())
	}

	legacyBest, _ := bestByScoreFn(nodes, scoreLegacySpareFirst)
	newBest, _ := bestByScoreFn(nodes, func(n *allocNode) float64 { return n.score() })
	t.Logf("旧权重选择 = %s；新权重选择 = %s", legacyBest, newBest)

	// 这是一次行为变更：旧权重下这里必然选 A（余量 0.9*0.5 直接压过 RTT 劣势）。
	if legacyBest != "A" {
		t.Fatalf("对照前提不成立：旧公式应当选 A（余量最大），实际 %q", legacyBest)
	}
	// 新权重必须选低 RTT 的 B：RTT 0.5 的权重让 30ms 对 180ms 的差距（0.206）压过余量差距（0.10）。
	if newBest != "B" {
		t.Fatalf("新权重应选低 RTT 的 B，实际 %q", newBest)
	}

	// 生产侧的真实选择函数也必须给出 B（上面只是公式对照，这里走 bestParentByScore）。
	candidate := Participant{ID: "x", Order: 9}
	opts := Options{StreamBps: 1, MaxDepth: 3}
	if got := bestParentByScore(nodes, candidate, opts, ""); got != "B" {
		t.Fatalf("bestParentByScore 应选低 RTT 的 B，实际 %q", got)
	}
}

// TestAssignLatencyFirstPicksLowerRTTEveryStep 是同一条行为变更的端到端版本：
// 走真实 Assign（真实容量模型、真实广度优先），用三个父节点的**余量差异**复现同一个取舍。
//
// 房间：主播 8 Mbps（K0=3）→ A/B/C 占满主播的三个位，x1/x2/x3 逐层下挂：
//
//	A 20Mbps → 8 位，RTT 180ms（余量最多但最慢）
//	B  5Mbps → 2 位，RTT  30ms（最快，但很快就只剩一半余量）
//	C  8Mbps → 3 位，RTT  60ms
//
// 旧权重：x1→B、x2→C，然后 A 凭"余量 1.0 × 0.5"把 x3 抢走（A 的 RTT 劣势被余量盖过）。
// 新权重：x1→B、x2→C，x3 仍然挂 B —— 快的那条路即使余量只剩 0.5 也优先。
func TestAssignLatencyFirstPicksLowerRTTEveryStep(t *testing.T) {
	participants := parts(
		host(8_000_000), // K0 = floor(8*0.8/2) = 3
		viewerRTT("A", 2, 20_000_000, 180),
		viewerRTT("B", 3, 5_000_000, 30),
		viewerRTT("C", 4, 8_000_000, 60),
		viewerRTT("x1", 5, 0, 0),
		viewerRTT("x2", 6, 0, 0),
		viewerRTT("x3", 7, 0, 0),
	)
	opts := Options{StreamBps: 2_000_000}

	plan := Assign("host", participants, opts)

	for _, id := range []string{"A", "B", "C"} {
		if a := plan.Assignments[id]; a.PrimaryID != "host" || a.Depth != 1 {
			t.Fatalf("%s 应先占满主播的直连位（depth=1），实际 %+v", id, a)
		}
	}
	if got := plan.Assignments["x1"].PrimaryID; got != "B" {
		t.Fatalf("x1 应在同余量下选低 RTT 的 B，实际 %q", got)
	}
	if got := plan.Assignments["x2"].PrimaryID; got != "C" {
		t.Fatalf("x2 应在 B 的余量降到 0.5 后选 C（RTT 60ms，余量 1.0），实际 %q", got)
	}
	// 关键断言（旧权重在这里给出 A，余量 1.0 压过 150ms 的 RTT 劣势）。
	if got := plan.Assignments["x3"].PrimaryID; got != "B" {
		t.Fatalf("x3 应继续选最快的 B（新权重：RTT 优先），实际 %q", got)
	}
	if len(plan.Unassigned) != 0 {
		t.Fatalf("不应有未安置节点，实际 %v", plan.Unassigned)
	}
	assertTreeSane(t, plan, participants, opts)
}

// TestAssignStillHardExcludesFullParent 锁定那条不能被权重绕过的硬约束：
// spare() <= 0 的父节点直接出局，无论它的 RTT 多好。权重只表达偏好，
// 越界由硬排除负责 —— 否则"最快"的父节点会被挂到过载，反过来变成卡顿源头。
func TestAssignStillHardExcludesFullParent(t *testing.T) {
	participants := parts(
		host(5_000_000),                      // K0 = 2
		viewerRTT("fast", 2, 5_000_000, 5),   // 2 位，RTT 5ms（会被填满）
		viewerRTT("slow", 3, 5_000_000, 500), // 2 位，RTT 500ms
		viewerRTT("f1", 4, 0, 10),
		viewerRTT("f2", 5, 0, 10),
		viewerRTT("x", 6, 0, 10),
	)
	opts := Options{StreamBps: 2_000_000}

	plan := Assign("host", participants, opts)

	// 前置条件：fast 的两个位被 f1/f2 占满（它 RTT 最低，评分也最高）。
	if got := plan.Assignments["f1"].PrimaryID; got != "fast" {
		t.Fatalf("前置条件不成立：f1 应挂在最快的 fast 下，实际 %q", got)
	}
	if got := plan.Assignments["f2"].PrimaryID; got != "fast" {
		t.Fatalf("前置条件不成立：f2 应挂在最快的 fast 下，实际 %q", got)
	}
	if spare := NodeCapacity(5_000_000, 2_000_000) - 2; spare != 0 {
		t.Fatalf("前置条件不成立：fast 的余量应为 0，实际 %d", spare)
	}

	if got := plan.Assignments["x"].PrimaryID; got != "slow" {
		t.Fatalf("fast 的余量已为 0，x 必须落到 slow（哪怕它 RTT 500ms），实际 %q", got)
	}
	if len(plan.Unassigned) != 0 {
		t.Fatalf("余量犹在的 slow 足以安置 x，不应有未安置节点：%v", plan.Unassigned)
	}
	assertTreeSane(t, plan, participants, opts)
}

// TestAssignStabilityBreaksRTTTie 锁定稳定性权重仍然参与：
// 两个候选 RTT 与余量都一样时，稳定性高的那个胜出（0.25 的权重足以分胜负）。
func TestAssignStabilityBreaksRTTTie(t *testing.T) {
	participants := parts(
		host(5_000_000), // K0 = 2
		viewerRTT("steady", 2, 5_000_000, 40),
		viewerRTT("flaky", 3, 5_000_000, 40),
		viewerRTT("x", 4, 0, 40),
	)
	// Stability 为 0 视为"未知、不惩罚"，所以这里给两个**都已知**的值。
	participants[1].Stability = 0.95
	participants[2].Stability = 0.30
	opts := Options{StreamBps: 2_000_000}

	plan := Assign("host", participants, opts)

	if got := plan.Assignments["x"].PrimaryID; got != "steady" {
		t.Fatalf("RTT 与余量相同时应选稳定性更高的 steady，实际 %q", got)
	}
	assertTreeSane(t, plan, participants, opts)
}

// TestAssignMaxDepthOverflowGoesUnassigned 锁定深度上限的两半语义：
// 放得下的继续加深到上限为止；放不下的进 Unassigned，而不是突破上限继续挂。
//
// 场景：K0=1 → 单链模式；每个转发节点只有 1 个位（2.5 Mbps 上行 / 2 Mbps 码率）。
// 于是拓扑只能是一条链：host → R1(1) → R2(2) → R3(3) → R4(4)。
func TestAssignMaxDepthOverflowGoesUnassigned(t *testing.T) {
	newParticipants := func() []Participant {
		return parts(
			host(4_000_000),
			viewer("R1", 2, 2_500_000),
			viewer("R2", 3, 2_500_000),
			viewer("R3", 4, 2_500_000),
			viewer("R4", 5, 2_500_000),
		)
	}
	stream := int64(2_000_000)

	// 默认上限是 3：R4 需要深度 4，必须进 Unassigned（旧默认 4 会把它塞进第 4 层）。
	def := Assign("host", newParticipants(), Options{StreamBps: stream})
	for id, a := range def.Assignments {
		if a.Depth > DefaultMaxDepth {
			t.Fatalf("节点 %s 深度 %d 超过默认上限 %d", id, a.Depth, DefaultMaxDepth)
		}
	}
	if got := def.Assignments["R3"].Depth; got != 3 {
		t.Fatalf("R3 应正好落在第 3 层，实际深度 %d", got)
	}
	if len(def.Unassigned) != 1 || def.Unassigned[0] != "R4" {
		t.Fatalf("默认上限 3 时 R4 应进 Unassigned（而不是继续加深），实际 %v", def.Unassigned)
	}

	// 显式放宽到 4：同一个人应当被安置在第 4 层，Unassigned 清空。
	wide := Assign("host", newParticipants(), Options{StreamBps: stream, MaxDepth: 4})
	if got := wide.Assignments["R4"].Depth; got != 4 {
		t.Fatalf("MaxDepth=4 时 R4 应落在第 4 层，实际深度 %d", got)
	}
	if len(wide.Unassigned) != 0 {
		t.Fatalf("MaxDepth=4 时不应有未安置节点，实际 %v", wide.Unassigned)
	}

	// 收到 2：R3 就已经放不下了，超限成员同样是 Unassigned，而不是被塞进第 3 层。
	narrow := Assign("host", newParticipants(), Options{StreamBps: stream, MaxDepth: 2})
	for id, a := range narrow.Assignments {
		if a.Depth > 2 {
			t.Fatalf("MaxDepth=2 时节点 %s 深度 %d 超限", id, a.Depth)
		}
	}
	if len(narrow.Unassigned) != 2 {
		t.Fatalf("MaxDepth=2 时 R3/R4 都应进 Unassigned，实际 %v", narrow.Unassigned)
	}
	for _, id := range narrow.Unassigned {
		if _, placed := narrow.Assignments[id]; placed {
			t.Fatalf("未安置的 %s 不应同时出现在分配表里", id)
		}
	}
}

// TestDefaultMaxDepthMatchesConfigDefault 防漂移：算法侧兜底默认值（Options.MaxDepth<=0 时用）
// 必须与配置侧默认值（PR_MAX_DEPTH 未设置时用）一致 ——
// 否则"直接调用 Assign"与"经过服务器的真实路径"会给出两棵不同的树，而且只有生产环境能看出来。
func TestDefaultMaxDepthMatchesConfigDefault(t *testing.T) {
	if DefaultMaxDepth != 3 {
		t.Fatalf("默认深度上限应为 3（延迟优先，4 跳最坏多 ~300ms），实际 %d", DefaultMaxDepth)
	}
	if DefaultMaxDepth != config.DefaultRoomMaxDepth {
		t.Fatalf("service.DefaultMaxDepth=%d 与 config.DefaultRoomMaxDepth=%d 必须一致",
			DefaultMaxDepth, config.DefaultRoomMaxDepth)
	}
	if got := (Options{}).withDefaults().MaxDepth; got != DefaultMaxDepth {
		t.Fatalf("Options.MaxDepth<=0 应退回默认值 %d，实际 %d", DefaultMaxDepth, got)
	}
	// 显式配置必须原样生效（含 1 与 6 这两个边界值）。
	for _, want := range []int{1, 2, 5, 6} {
		if got := (Options{MaxDepth: want}).withDefaults().MaxDepth; got != want {
			t.Fatalf("Options.MaxDepth=%d 应原样生效，实际 %d", want, got)
		}
	}
}

// TestDepthCapTradesCapacityForLatency 把"深度从 4 收到 3"的对价量化出来：
// 链式退化场景下少一层就是少一层容量。这不是缺陷，是本次取舍的**代价**，
// 所以要有一条断言把它钉住（免得有人以为收深度是纯赚）。
func TestDepthCapTradesCapacityForLatency(t *testing.T) {
	// 1 个主播 + 1 个分发节点 + 每个转发节点 2 个位，够铺满 4 层。
	build := func() []Participant {
		items := []Participant{host(4_000_000), viewer("R1", 2, 5_000_000)}
		for i := 0; i < 14; i++ {
			items = append(items, viewer("v"+string(rune('a'+i)), i+3, 5_000_000))
		}
		return parts(items...)
	}
	opts3 := Options{StreamBps: 2_000_000, MaxDepth: 3}
	opts4 := Options{StreamBps: 2_000_000, MaxDepth: 4}

	p3 := Assign("host", build(), opts3)
	p4 := Assign("host", build(), opts4)

	t.Logf("MaxDepth=3: 已安置=%d 未安置=%d FreeSlots=%d GateSlots=%d",
		len(p3.Assignments), len(p3.Unassigned), p3.FreeSlots, p3.GateSlots)
	t.Logf("MaxDepth=4: 已安置=%d 未安置=%d FreeSlots=%d GateSlots=%d",
		len(p4.Assignments), len(p4.Unassigned), p4.FreeSlots, p4.GateSlots)

	if len(p4.Assignments) <= len(p3.Assignments) {
		t.Fatalf("第 4 层本应换到更多安置名额（这正是代价所在）：depth3=%d depth4=%d",
			len(p3.Assignments), len(p4.Assignments))
	}

	// 空位口径收紧后（只算能收子节点的节点），这两种拓扑都已经是"真满"：
	// 最深一层塞满了转发节点，它们的余量一个也用不出去。
	// **这是一次行为变更**：旧口径在这两种拓扑上分别虚报 8 / 16 个空位（第 3 层 / 第 4 层的虚位），
	// 准入闸门据此会放进安置不下的人。
	for _, tc := range []struct {
		name          string
		plan          Plan
		opts          Options
		members       []Participant
		wantDeepest   int
		wantPlaceable int
	}{
		{"MaxDepth=3", p3, opts3, build(), 3, 8},
		{"MaxDepth=4", p4, opts4, build(), 4, 16},
	} {
		deepest := 0
		for _, a := range tc.plan.Assignments {
			if a.Depth > deepest {
				deepest = a.Depth
			}
		}
		if deepest != tc.wantDeepest {
			t.Fatalf("%s：前置条件不成立，最深应为 %d，实际 %d", tc.name, tc.wantDeepest, deepest)
		}
		if len(tc.plan.Assignments) != tc.wantPlaceable {
			t.Fatalf("%s：应安置 %d 人，实际 %d", tc.name, tc.wantPlaceable, len(tc.plan.Assignments))
		}
		if tc.plan.FreeSlots != 0 || tc.plan.GateSlots != 0 {
			t.Fatalf("%s：最深一层不贡献名额时，空位应为 0，实际 FreeSlots=%d GateSlots=%d",
				tc.name, tc.plan.FreeSlots, tc.plan.GateSlots)
		}
		// 功能验证：再挂一个人必须安置不下（否则"0"才是过度收紧）。
		if placed, _ := probePlacement(tc.members, tc.opts, 1); placed != 0 {
			t.Fatalf("%s：FreeSlots=0 却有 %d 个探针被安置，说明空位被低估", tc.name, placed)
		}
	}
}

// probePlacement 追加 count 个无上行探针再算一遍，返回真正被安置的探针数与未安置的探针数。
//
// 这是"实际可安置人数"的功能化度量：探针不改变房间本身，只是问树"现在还能放下几个"。
// 用它来验证 FreeSlots / GateSlots 不是虚位 —— 空位是"账"，探针安置是"实"。
func probePlacement(participants []Participant, opts Options, count int) (placed, unassigned int) {
	if count <= 0 {
		return 0, 0
	}

	merged := make([]Participant, 0, len(participants)+count)
	merged = append(merged, participants...)
	for i := 0; i < count; i++ {
		merged = append(merged, viewer(fmt.Sprintf("probe%02d", i), 1000+i, 0))
	}

	plan := Assign("host", merged, opts)
	for i := 0; i < count; i++ {
		if _, ok := plan.Assignments[fmt.Sprintf("probe%02d", i)]; ok {
			placed++
			continue
		}
		unassigned++
	}
	return placed, unassigned
}

// TestSlotsExcludeDeepestLayer 直接锁定新口径本身：
// 处在最深一层（depth == MaxDepth）的节点一个子节点也收不了（chooseParent 要求 depth+1 <= MaxDepth），
// 因此它们的余量**既不算 FreeSlots 也不算 GateSlots**。
//
// 链式 4 人（K0=1 → 单链，relay 只有 2 个位）在 MaxDepth=2 下正好铺满 1+1+2：
//
//	host(0) → relay(1) → l1/l2(2)，而 l1/l2 各自还有余量 —— 那是虚位。
//	旧口径报 FreeSlots=GateSlots=4（全部虚高），新口径必须报 0。
func TestSlotsExcludeDeepestLayer(t *testing.T) {
	stream := int64(2_000_000)
	chain4 := func() []Participant {
		return parts(
			host(4_000_000),
			viewer("relay", 2, 5_000_000),
			viewer("l1", 3, 5_000_000),
			viewer("l2", 4, 5_000_000),
		)
	}

	// 深度上限 2：l1/l2 处在最深一层，空位必须是 0。
	tight := Options{StreamBps: stream, MaxDepth: 2}
	plan := Assign("host", chain4(), tight)

	if len(plan.Assignments) != 4 || len(plan.Unassigned) != 0 {
		t.Fatalf("前置条件不成立：4 人应全部安置，实际 已安置=%d 未安置=%v",
			len(plan.Assignments), plan.Unassigned)
	}
	if plan.Assignments["l1"].Depth != 2 {
		t.Fatalf("前置条件不成立：l1 应落在最深一层（2），实际 %d", plan.Assignments["l1"].Depth)
	}
	if plan.FreeSlots != 0 || plan.GateSlots != 0 {
		t.Fatalf("最深一层的余量是虚位，应为 0，实际 FreeSlots=%d GateSlots=%d", plan.FreeSlots, plan.GateSlots)
	}
	if placed, unassigned := probePlacement(chain4(), tight, 1); placed != 0 || unassigned != 1 {
		t.Fatalf("FreeSlots=0 时应无人可安置，实际 安置=%d 未安置=%d", placed, unassigned)
	}

	// 对照组（防止"一律收紧"）：同样的 4 人、上限 3 时，l1/l2 在第 2 层还有子位，
	// 那些位是真能用的 —— 4 个探针必须全部被安置。
	roomy := Options{StreamBps: stream, MaxDepth: 3}
	plan3 := Assign("host", chain4(), roomy)
	if plan3.FreeSlots != 4 || plan3.GateSlots != 4 {
		t.Fatalf("上限 3 时 l1/l2 各余 2 位，应为 4，实际 FreeSlots=%d GateSlots=%d", plan3.FreeSlots, plan3.GateSlots)
	}
	if placed, unassigned := probePlacement(chain4(), roomy, plan3.GateSlots); placed != plan3.GateSlots || unassigned != 0 {
		t.Fatalf("GateSlots=%d 声称的空位必须真的能放人，实际 安置=%d 未安置=%d",
			plan3.GateSlots, placed, unassigned)
	}

	// 边界值 MaxDepth=1（配置允许范围的下界）：所有观众都必须挂在深度 1，
	// 于是除了主播自己的 K0 个位以外不可能再有空位 —— 深度 1 的节点同样不贡献名额。
	tiny := Options{StreamBps: stream, MaxDepth: 1}
	plan1 := Assign("host", chain4(), tiny)
	if len(plan1.Assignments) != 2 || len(plan1.Unassigned) != 2 {
		t.Fatalf("MaxDepth=1 时只应安置主播与唯一直连节点，实际 已安置=%d 未安置=%v",
			len(plan1.Assignments), plan1.Unassigned)
	}
	if plan1.FreeSlots != 0 || plan1.GateSlots != 0 {
		t.Fatalf("MaxDepth=1 时不应有任何空位，实际 FreeSlots=%d GateSlots=%d", plan1.FreeSlots, plan1.GateSlots)
	}
	if placed, _ := probePlacement(chain4(), tiny, 1); placed != 0 {
		t.Fatalf("MaxDepth=1 时多进一个人应安置不下，实际安置了 %d 人", placed)
	}
}

// TestGateSlotsAreAlwaysPlaceable 是空位口径的**契约测试**，覆盖三种拓扑：
//
//	① 链式 4 人（MaxDepth=2 / 3）
//	② 弱上行链（10 人，默认深度 3）—— 每个转发节点只有 1 个位，拓扑被深度截断
//	③ SPEC §6.2 的扇出例子：主播 12 Mbps（K0=4）+ 8 个 6 Mbps relay + 20 个 6 Mbps 叶子
//
// 两个方向都要成立，缺一不可：
//   - 不虚高：声称多少空位，就真能安置多少人（探针实测）；
//   - 不过度收紧：声称 0 空位时，多进一个人必须真的安置不下。
func TestGateSlotsAreAlwaysPlaceable(t *testing.T) {
	stream := int64(2_000_000)

	weakChain := func(n int) []Participant {
		items := make([]Participant, 0, n+1)
		items = append(items, host(4_000_000))
		for i := 1; i <= n; i++ {
			// 2.5 Mbps / 2 Mbps → 每个转发节点只有 1 个子位：链只能逐跳向下。
			items = append(items, viewer(fmt.Sprintf("R%d", i), i+1, 2_500_000))
		}
		return parts(items...)
	}
	fanout := func() []Participant {
		items := []Participant{host(12_000_000)}
		for i := 0; i < 8; i++ {
			items = append(items, viewer(fmt.Sprintf("r%d", i), i+2, 6_000_000))
		}
		for i := 0; i < 20; i++ {
			items = append(items, viewer(fmt.Sprintf("v%d", i), i+20, 6_000_000))
		}
		return parts(items...)
	}
	chain4 := func() []Participant {
		return parts(
			host(4_000_000),
			viewer("relay", 2, 5_000_000),
			viewer("l1", 3, 5_000_000),
			viewer("l2", 4, 5_000_000),
		)
	}

	cases := []struct {
		name         string
		participants []Participant
		opts         Options
	}{
		{"①链式4人/上限2", chain4(), Options{StreamBps: stream, MaxDepth: 2}},
		{"①链式4人/上限3", chain4(), Options{StreamBps: stream, MaxDepth: 3}},
		{"②弱上行链10人/默认3", weakChain(9), Options{StreamBps: stream}},
		{"③扇出例子/上限3", fanout(), Options{StreamBps: stream, MaxDepth: 3}},
		{"③扇出例子/上限4", fanout(), Options{StreamBps: stream, MaxDepth: 4}},
	}

	t.Log("拓扑 | 已安置 | 未安置 | FreeSlots(实测可安置) | GateSlots(实测可安置)")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := Assign("host", tc.participants, tc.opts)

			freePlaced, _ := probePlacement(tc.participants, tc.opts, plan.FreeSlots)
			gatePlaced, _ := probePlacement(tc.participants, tc.opts, plan.GateSlots)
			t.Logf("%s | %d | %d | %d(%d) | %d(%d)", tc.name,
				len(plan.Assignments), len(plan.Unassigned), plan.FreeSlots, freePlaced, plan.GateSlots, gatePlaced)

			if plan.GateSlots > plan.FreeSlots {
				t.Fatalf("准入口径（只认实测）不应比排布口径更宽松：GateSlots=%d FreeSlots=%d",
					plan.GateSlots, plan.FreeSlots)
			}
			if freePlaced != plan.FreeSlots {
				t.Fatalf("FreeSlots=%d 是虚位：实际只能再安置 %d 人", plan.FreeSlots, freePlaced)
			}
			if gatePlaced != plan.GateSlots {
				t.Fatalf("GateSlots=%d 是虚位：闸门放进来的人里有 %d 个安置不下",
					plan.GateSlots, plan.GateSlots-gatePlaced)
			}
			if plan.GateSlots == 0 {
				if placed, _ := probePlacement(tc.participants, tc.opts, 1); placed != 0 {
					t.Fatalf("GateSlots=0 却还能安置 %d 人：空位被过度收紧", placed)
				}
			}
		})
	}
}

// TestFanoutExampleSlotAccounting 给 SPEC §6.2 的扇出例子钉一组具体数字：
// 29 人（主播 + 8 relay + 20 叶子）在默认深度 3 下**正好铺满**（最深一层 16 个节点全是虚位），
// 因此 FreeSlots/GateSlots 必须为 0；把上限放到 4，第 3 层的 32 个位才真的能用。
//
// 这组数字同时回答"闸门是否被过度收紧"：上限 3 时 0 空位对应"真的一个人也放不下"（探针实测）。
func TestFanoutExampleSlotAccounting(t *testing.T) {
	stream := int64(2_000_000)
	build := func() []Participant {
		items := []Participant{host(12_000_000)}
		for i := 0; i < 8; i++ {
			items = append(items, viewer(fmt.Sprintf("r%d", i), i+2, 6_000_000)) // K0=4，Ki=2
		}
		for i := 0; i < 20; i++ {
			items = append(items, viewer(fmt.Sprintf("v%d", i), i+20, 6_000_000))
		}
		return parts(items...)
	}

	at3 := Options{StreamBps: stream, MaxDepth: 3}
	p3 := Assign("host", build(), at3)
	if len(p3.Assignments) != 29 || len(p3.Unassigned) != 0 {
		t.Fatalf("29 人应全部安置在第 3 层以内，实际 已安置=%d 未安置=%v", len(p3.Assignments), p3.Unassigned)
	}
	if p3.FreeSlots != 0 || p3.GateSlots != 0 {
		t.Fatalf("铺满后空位应为 0（第 3 层 16 个节点全是虚位），实际 FreeSlots=%d GateSlots=%d",
			p3.FreeSlots, p3.GateSlots)
	}
	if placed, _ := probePlacement(build(), at3, 1); placed != 0 {
		t.Fatalf("上限 3 且铺满时，多进一个人应安置不下，实际安置了 %d 人", placed)
	}

	at4 := Options{StreamBps: stream, MaxDepth: 4}
	p4 := Assign("host", build(), at4)
	if p4.FreeSlots != 32 || p4.GateSlots != 32 {
		t.Fatalf("上限 4 时第 3 层的 16 个节点各余 2 位（真能用），应为 32，实际 FreeSlots=%d GateSlots=%d",
			p4.FreeSlots, p4.GateSlots)
	}
	if placed, unassigned := probePlacement(build(), at4, p4.GateSlots); placed != 32 || unassigned != 0 {
		t.Fatalf("上限 4 的 32 个空位必须真的能放人，实际 安置=%d 未安置=%d", placed, unassigned)
	}

	t.Logf("扇出例子：上限3 → 已安置=%d FreeSlots=%d GateSlots=%d；上限4 → 已安置=%d FreeSlots=%d GateSlots=%d",
		len(p3.Assignments), p3.FreeSlots, p3.GateSlots, len(p4.Assignments), p4.FreeSlots, p4.GateSlots)
}
