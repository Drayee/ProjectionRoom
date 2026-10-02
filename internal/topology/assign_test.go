package topology

import (
	"strings"
	"testing"
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
