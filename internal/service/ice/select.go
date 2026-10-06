package ice

// minWeight 是"探测成功但分数很低"条目的兜底权重。
//
// 为什么需要它：纯 ∝ 分数 的权重会让一条 500ms 的 STUN 拿到 0 权重、永远不再被下发，
// 于是它再也没机会被观察到"已经变快了"。加一个 0.05 的底，让低分项以很小的份额参与
// 轮换，既挤不掉好节点，也不会把回升的机会彻底掐死。
// 本次探测**失败**的条目不在此列（weight=0，不参与轮询）——那是"一票否决"，
// 不是"低分"。
const minWeight = 0.05

// weightOf 把分数折算成轮询权重。只对"本轮探测成功"的条目调用。
func weightOf(score float64) float64 { return score + minWeight }

// weighted 是一个参与轮询的候选。
type weighted struct {
	url string
	w   float64
}

// selector 是跨窗口保留状态的平滑加权轮询（nginx SWRR 同款）。
//
// 为什么不是"按分数排序取前 N"：分数在窗口之间会抖（同一条可能 200ms ↔ 超时），
// 纯排序会让整份下发列表整体翻转，客户端每次拿 ICE 都换一批服务器，反而更容易撞上
// 正在抖的那几条。SWRR 把权重摊到多轮上，相邻窗口通常只换一两条；
// 而且给定同一段分数序列时输出完全可复现（见 select_test.go），排障时能直接复算。
type selector struct {
	// current 是每个 URL 的累计权重，**跨窗口保留**；这正是"轮换"的来源。
	current map[string]float64
}

func newSelector() *selector { return &selector{current: make(map[string]float64)} }

// pick 从 cands 里按权重选出 n 条**互不相同**的条目。
//
// 返回值的第一条是第一个被选中的；顺序对下发本身无用（下发按分数排序），
// 保留它只为让测试能断言轮换过程。
// cands 的顺序即同分时的次序（调用方按配置顺序传入），因此结果是确定性的。
func (s *selector) pick(cands []weighted, n int) []string {
	if n <= 0 {
		return nil
	}

	usable := make([]weighted, 0, len(cands))
	total := 0.0
	for _, c := range cands {
		if c.w <= 0 {
			continue
		}
		usable = append(usable, c)
		total += c.w
	}
	if len(usable) == 0 {
		return nil
	}

	// 名额够放下所有候选时直接全选：此时轮询没有意义，
	// 顺便把状态归零，保证"名额变化"这件事不会留下漂移的累计权重。
	if n >= len(usable) {
		out := make([]string, 0, len(usable))
		for _, c := range usable {
			out = append(out, c.url)
			s.current[c.url] = 0
		}
		return out
	}

	out := make([]string, 0, n)
	picked := make(map[string]bool, n)
	for k := 0; k < n; k++ {
		// 每轮给**所有**候选加一次权重（这才是"按比例分配"的来源），
		// 但只在还没被选中的候选里决胜。
		for _, c := range usable {
			s.current[c.url] += c.w
		}

		best := ""
		for _, c := range usable {
			if picked[c.url] {
				continue
			}
			// 严格大于：同分时保留先出现的那个，保证确定性。
			if best == "" || s.current[c.url] > s.current[best] {
				best = c.url
			}
		}
		if best == "" {
			break
		}

		s.current[best] -= total
		picked[best] = true
		out = append(out, best)
	}
	return out
}
