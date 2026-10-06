package ice

import "time"

// rttCap 是 RTT 折算分数时的封顶值：到这里就是 0 分。
//
// 取 500ms：跨洋 STUN 也在 200ms 量级，500ms 已经属于"能用但很差"。
// 再往上没有区分意义（都很难用），统一压到 0 可以避免长尾把轮询权重摊薄。
const rttCap = 500 * time.Millisecond

// 打分权重：RTT 占 8 成，跨窗口成功率占 2 成。
//
// 为什么成功率只占 2 成：RTT 决定打洞的**速度**，成功率决定它**会不会卡住**。
// 一条 30ms 但每三次里失败一次（成功率 0.667）的 STUN，得分 0.8+0.133=0.933；
// 一条 400ms 但从不失败（成功率 1）的 STUN，得分 0.8*0.2+0.2=0.36。
// 这个排序是我们想要的：稳定的低延迟优先，抖动只是扣分而不是一票否决
// （一票否决交给"本次探测失败"——那一项直接 0 分）。
const (
	weightRTT     = 0.8
	weightSuccess = 0.2
)

// RTTScore 把单次探测的往返延迟折算成 [0,1] 的分数：0ms → 1，≥500ms → 0，线性。
//
// 负数按 0 处理：测量误差（或注入的测试时钟）不该被当成"比光速还快"。
func RTTScore(rtt time.Duration) float64 {
	switch {
	case rtt <= 0:
		return 1
	case rtt >= rttCap:
		return 0
	default:
		return 1 - float64(rtt)/float64(rttCap)
	}
}

// Score 是单个 STUN 条目在某个窗口里的得分。
//
// ok=false（超时、报文不合法、域名解析失败）一律 0 分：一个"这次没有应答"的服务器，
// 不该因为历史成功率漂亮而继续占据下发名额。它下一轮恢复应答后会自动回升，
// 因为轮询权重里给它留了兜底份额（见 minWeight）。
func Score(ok bool, rtt time.Duration, successRate float64) float64 {
	if !ok {
		return 0
	}
	return weightRTT*RTTScore(rtt) + weightSuccess*clamp01(successRate)
}

// successAlpha 是成功率 EWMA 的平滑系数。
//
// 取 0.5：一个窗口就能把成功率拉走一半，因此"刚刚开始挂掉"的服务器会在两个窗口内
// 掉出下发列表（60s × 2），而偶尔一次抖动不会把它直接判死。
const successAlpha = 0.5

// UpdateSuccessRate 用本窗口结果更新跨窗口成功率（EWMA）。
//
// prev < 0 表示"还没有历史"，首个样本直接等于本窗口结果（1 或 0），
// 不做 0.5 衰减——否则一条从未失败过的服务器要等两个窗口才被认出来。
func UpdateSuccessRate(prev float64, ok bool) float64 {
	cur := 0.0
	if ok {
		cur = 1
	}
	if prev < 0 {
		return cur
	}
	return successAlpha*cur + (1-successAlpha)*prev
}

func clamp01(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	default:
		return v
	}
}
