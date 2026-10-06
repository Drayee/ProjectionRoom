package ice

import (
	"strings"
	"testing"
)

func urlsOf(ss []string) string { return strings.Join(ss, ",") }

// TestSelectorPickAllWhenQuotaCovers 名额够放下全部候选时全选，并把状态归零。
func TestSelectorPickAllWhenQuotaCovers(t *testing.T) {
	s := newSelector()
	cands := []weighted{{"a", 0.9}, {"b", 0.5}, {"c", 0.1}}

	got := s.pick(cands, 3)
	if urlsOf(got) != "a,b,c" {
		t.Fatalf("名额足够时应全选且保持顺序，实际 %v", got)
	}
	// 再选一次必须还是同样的结果（状态已归零，不会因为"上一轮"而漂移）。
	if again := s.pick(cands, 5); urlsOf(again) != "a,b,c" {
		t.Fatalf("全选后状态应归零且结果稳定，实际 %v", again)
	}
}

// TestSelectorPickIsWeighted 权重 10:1 时，11 轮单名额挑选应当恰好是 10:1。
// 这条同时证明"权重 ∝ 分数"真的落在了选择分布上，而不是只写在注释里。
func TestSelectorPickIsWeighted(t *testing.T) {
	s := newSelector()
	cands := []weighted{{"hi", 10}, {"lo", 1}}

	count := map[string]int{}
	for i := 0; i < 11; i++ {
		picked := s.pick(cands, 1)
		if len(picked) != 1 {
			t.Fatalf("第 %d 轮应选出 1 条，实际 %v", i, picked)
		}
		count[picked[0]]++
	}

	if count["hi"] != 10 || count["lo"] != 1 {
		t.Fatalf("权重 10:1 在 11 轮里应为 10:1，实际 %v", count)
	}
}

// TestSelectorRotationIsReproducible 锁定"同一分数序列 → 同一串选择"，并证明轮换真的发生。
//
// 断言刻意不写死"第几个窗口选了谁"：等权重下浮点残差会决定同分时的胜出者，
// 那是实现细节，不是契约。契约有两条：
//   - 状态跨窗口保留（否则每个窗口都会选出同一组，落选者永远回不来）；
//   - 同一序列可复现（排障时能复算、测试不会随机器漂移）。
func TestSelectorRotationIsReproducible(t *testing.T) {
	run := func() [][]string {
		s := newSelector()
		cands := []weighted{{"a", 1.05}, {"b", 1.05}, {"c", 1.05}}
		var out [][]string
		for i := 0; i < 3; i++ {
			out = append(out, s.pick(cands, 2))
		}
		return out
	}

	first := run()

	// 每个窗口都是 2 条互不相同的条目。
	union := map[string]bool{}
	for i, picked := range first {
		if len(picked) != 2 || picked[0] == picked[1] {
			t.Fatalf("第 %d 个窗口应是 2 条不同条目，实际 %v", i+1, picked)
		}
		for _, u := range picked {
			union[u] = true
		}
	}

	// 状态跨窗口保留：三个窗口不可能完全一样。
	if urlsOf(first[0]) == urlsOf(first[1]) && urlsOf(first[1]) == urlsOf(first[2]) {
		t.Fatalf("三个窗口选出了同一组（%v），说明轮询状态没有跨窗口保留", first)
	}

	// 轮换把每个候选都带回来过：并集必须覆盖全部三个。
	if len(union) != 3 {
		t.Fatalf("三个窗口的并集应覆盖全部候选，实际 %v（窗口序列 %v）", union, first)
	}

	// 同一序列重跑必须逐窗口一致。
	second := run()
	for i := range first {
		if urlsOf(first[i]) != urlsOf(second[i]) {
			t.Fatalf("第 %d 个窗口不可复现：%v vs %v", i+1, first[i], second[i])
		}
	}
}

// TestSelectorEqualSameURLLosesToConfigOrder 同权重时保留先出现的候选（确定性 tie-break）。
func TestSelectorEqualSameURLLosesToConfigOrder(t *testing.T) {
	s := newSelector()
	cands := []weighted{{"first", 1}, {"second", 1}}

	if got := s.pick(cands, 1); urlsOf(got) != "first" {
		t.Fatalf("同权重时应选配置顺序靠前的条目，实际 %v", got)
	}
}

// TestSelectorNeverReturnsDuplicatesWithinOneWindow 回归用例：
// 一个窗口内选出的条目必须互不相同，否则等于白白浪费一个下发名额
// （iceServers 里的重复条目对浏览器毫无意义）。
//
// 这个缺陷真实存在过：早期实现用"加权重→取最大→减总量"逐轮挑选，
// 近似相等权重产生的浮点残差会让同一个候选连续两轮胜出（实测 [c c]）。
func TestSelectorNeverReturnsDuplicatesWithinOneWindow(t *testing.T) {
	// 权重几乎相等：最容易暴露浮点残差导致的重复。
	cands := []weighted{{"a", 1.0500000000000005}, {"b", 1.0499999999999998}, {"c", 1.05}}

	s := newSelector()
	for window := 0; window < 12; window++ {
		picked := s.pick(cands, 2)
		if len(picked) != 2 {
			t.Fatalf("第 %d 个窗口应选出 2 条，实际 %v", window+1, picked)
		}
		if picked[0] == picked[1] {
			t.Fatalf("第 %d 个窗口出现了重复条目：%v", window+1, picked)
		}
	}
}

// TestSelectorIsProportionalOverManyWindows 权重相等时，多窗口的轮换应当均等；
// 三候选两名额跑 12 轮 → 每条恰好 8 次。
func TestSelectorIsProportionalOverManyWindows(t *testing.T) {
	s := newSelector()
	cands := []weighted{{"a", 1.05}, {"b", 1.05}, {"c", 1.05}}

	count := map[string]int{}
	for i := 0; i < 12; i++ {
		for _, u := range s.pick(cands, 2) {
			count[u]++
		}
	}

	if count["a"] != 8 || count["b"] != 8 || count["c"] != 8 {
		t.Fatalf("等权重应均等轮换（各 8 次），实际 %v", count)
	}
}

// TestSelectorSkipsZeroWeight 权重 <=0 的候选（本轮探测失败）完全不参与选择。
func TestSelectorSkipsZeroWeight(t *testing.T) {
	s := newSelector()
	cands := []weighted{{"dead", 0}, {"alive", 0.3}}

	for i := 0; i < 5; i++ {
		if got := s.pick(cands, 1); urlsOf(got) != "alive" {
			t.Fatalf("0 权重条目不该被选中，第 %d 轮实际 %v", i, got)
		}
	}
	if got := s.pick([]weighted{{"dead", 0}}, 1); got != nil {
		t.Fatalf("全部 0 权重时不应有选择结果，实际 %v", got)
	}
}

// TestSelectorEmptyAndZeroQuota 空候选与 0 名额都要安全返回 nil。
func TestSelectorEmptyAndZeroQuota(t *testing.T) {
	s := newSelector()
	if got := s.pick(nil, 2); got != nil {
		t.Fatalf("空候选应返回 nil，实际 %v", got)
	}
	if got := s.pick([]weighted{{"a", 1}}, 0); got != nil {
		t.Fatalf("0 名额应返回 nil，实际 %v", got)
	}
}
