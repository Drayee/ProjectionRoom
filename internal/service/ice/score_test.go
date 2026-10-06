package ice

import (
	"math"
	"testing"
	"time"
)

// TestRTTScoreBoundaries 锁定 RTT 折算分数的边界：0ms → 1，500ms → 0，越界都夹住。
func TestRTTScoreBoundaries(t *testing.T) {
	cases := []struct {
		name string
		rtt  time.Duration
		want float64
	}{
		{"0ms 满分", 0, 1},
		{"负延迟按 0 处理", -5 * time.Millisecond, 1},
		{"11ms 实测最好那条", 11 * time.Millisecond, 1 - 11.0/500},
		{"250ms 恰好一半", 250 * time.Millisecond, 0.5},
		{"500ms 触底", 500 * time.Millisecond, 0},
		{"600ms 仍是 0", 600 * time.Millisecond, 0},
		{"4s 超时量级", 4 * time.Second, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RTTScore(tc.rtt); math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("RTTScore(%v) = %v，期望 %v", tc.rtt, got, tc.want)
			}
		})
	}
}

// TestScoreFormula 锁定打分公式：0.8×RTT 分 + 0.2×成功率，失败一律 0。
func TestScoreFormula(t *testing.T) {
	cases := []struct {
		name        string
		ok          bool
		rtt         time.Duration
		successRate float64
		want        float64
	}{
		{"超时 → 0 分（成功率再漂亮也没用）", false, 0, 1, 0},
		{"超时且延迟有值 → 仍是 0", false, 20 * time.Millisecond, 0.9, 0},
		{"0ms 且从未失败 → 满分", true, 0, 1, 1},
		{"0ms 且成功率 0.5 → 0.9", true, 0, 0.5, 0.9},
		{"250ms 且成功率 0.5 → 0.5", true, 250 * time.Millisecond, 0.5, 0.5},
		{"500ms 且从不失败 → 0.2（只剩成功率那 2 成）", true, 500 * time.Millisecond, 1, 0.2},
		{"203ms 实测 cloudflare 量级", true, 203 * time.Millisecond, 1, 0.8*(1-203.0/500) + 0.2},
		// 半成功：一次成一次败的 EWMA 结果是 0.5，这里直接喂 0.5 验算公式本身。
		{"半成功条目", true, 100 * time.Millisecond, 0.5, 0.8*0.8 + 0.1},
		{"成功率越界要夹住（>1）", true, 0, 2, 1},
		{"成功率越界要夹住（<0）", true, 0, -1, 0.8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Score(tc.ok, tc.rtt, tc.successRate); math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("Score(%v, %v, %v) = %v，期望 %v", tc.ok, tc.rtt, tc.successRate, got, tc.want)
			}
		})
	}
}

// TestUpdateSuccessRate 锁定 EWMA：首个样本不衰减，之后每次拉走一半。
func TestUpdateSuccessRate(t *testing.T) {
	// prev<0 = 没有历史：首个样本直接等于本窗口结果。
	if got := UpdateSuccessRate(-1, true); got != 1 {
		t.Fatalf("首个成功样本应为 1，实际 %v", got)
	}
	if got := UpdateSuccessRate(-1, false); got != 0 {
		t.Fatalf("首个失败样本应为 0，实际 %v", got)
	}

	// 半成功：连续一次成功一次失败 → 0.5。
	rate := UpdateSuccessRate(-1, true)
	rate = UpdateSuccessRate(rate, false)
	if math.Abs(rate-0.5) > 1e-9 {
		t.Fatalf("一成一败后成功率应为 0.5，实际 %v", rate)
	}

	// 从 1 掉一次 → 0.5；再成功一次 → 0.75。两个窗口内就能把"刚开始挂掉"的服务器扣到 0.5。
	rate = UpdateSuccessRate(1, false)
	if math.Abs(rate-0.5) > 1e-9 {
		t.Fatalf("从 1 失败一次应为 0.5，实际 %v", rate)
	}
	if got := UpdateSuccessRate(rate, true); math.Abs(got-0.75) > 1e-9 {
		t.Fatalf("再成功一次应为 0.75，实际 %v", got)
	}

	// 长期失败必定收敛到 0。
	rate = 1
	for i := 0; i < 20; i++ {
		rate = UpdateSuccessRate(rate, false)
	}
	if rate > 1e-6 {
		t.Fatalf("连续 20 次失败后成功率应趋近 0，实际 %v", rate)
	}
}

// TestScoreOrderingMatchesIntent 把"打分到底想要什么排序"写成断言：
// 稳定的低延迟 > 抖动的高延迟 > 高延迟但稳定。
//
// 这是文档里那句"RTT 决定速度，成功率决定会不会卡住"的可执行版本。
func TestScoreOrderingMatchesIntent(t *testing.T) {
	fastSteady := Score(true, 30*time.Millisecond, 1)
	fastFlaky := Score(true, 30*time.Millisecond, 0.667)
	slowSteady := Score(true, 400*time.Millisecond, 1)

	if !(fastSteady > fastFlaky) {
		t.Fatalf("稳定的 30ms 应优于抖动的 30ms：%v vs %v", fastSteady, fastFlaky)
	}
	if !(fastFlaky > slowSteady) {
		t.Fatalf("抖动的 30ms 应优于稳定的 400ms：%v vs %v", fastFlaky, slowSteady)
	}
}
