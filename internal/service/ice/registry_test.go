package ice

import (
	"context"
	"math"
	"testing"
	"time"

	"ProjectionRoom/internal/config"
)

// fakeProber 是实现 ice.Prober 的固定结果探测器：生产路径绝不使用它，
// 它存在的意义是让打分/选择/降级这些逻辑可以在不碰网络的前提下被断言。
type fakeProber struct {
	results map[string]Observation
	// calls 记录每次被要求探测的 URL 列表，用于断言"配置变化会传到探测器"。
	calls [][]string
}

func (f *fakeProber) Probe(_ context.Context, urls []string) []Observation {
	f.calls = append(f.calls, append([]string(nil), urls...))

	out := make([]Observation, 0, len(urls))
	for _, u := range urls {
		o, ok := f.results[u]
		if !ok {
			o = Observation{} // 未指定 = 本轮失败
		}
		o.URL = u
		out = append(out, o)
	}
	return out
}

// newTestRegistry 用假探测器构造一个**未启动后台循环**的 Registry。
func newTestRegistry(t *testing.T, urls []string, maxSTUN int, results map[string]Observation) (*Registry, *fakeProber) {
	t.Helper()

	cfg := config.Default()
	cfg.ICE.STUNURLs = urls
	cfg.ICE.MaxSTUN = maxSTUN

	fake := &fakeProber{results: results}
	return NewRegistryWithProber(cfg, fake), fake
}

// okObservation 造一条成功观测。
func okObservation(url string, rttMs int) Observation {
	return Observation{URL: url, OK: true, RTT: time.Duration(rttMs) * time.Millisecond}
}

// defaultListResults 按默认列表造一份"只有 qq 挂掉"的观测，与真机实测结论一致。
func defaultListResults() map[string]Observation {
	urls := config.DefaultSTUNURLs()
	results := map[string]Observation{
		urls[0]: okObservation(urls[0], 11),  // douyucdn
		urls[1]: okObservation(urls[1], 32),  // hitv
		urls[2]: okObservation(urls[2], 46),  // bilibili
		urls[3]: okObservation(urls[3], 119), // miwifi
		urls[4]: okObservation(urls[4], 203), // cloudflare（粘性）
		urls[5]: okObservation(urls[5], 236), // google（粘性）
		// urls[6] = qq：刻意不放进 map = 本轮失败
	}
	return results
}

// selectedURLs 从载荷里取出真正下发的 URL 列表。
func selectedURLs(p Payload) []string {
	out := make([]string, 0, len(p.IceServers))
	for _, s := range p.IceServers {
		out = append(out, s["urls"].(string))
	}
	return out
}

// scoreOf 取某个 URL 在本轮 probe.scores 里的条目。
func scoreOf(t *testing.T, p Payload, url string) ScoreEntry {
	t.Helper()

	for _, s := range p.Probe.Scores {
		if s.URL == url {
			return s
		}
	}
	t.Fatalf("probe.scores 里缺少 %q：%+v", url, p.Probe.Scores)
	return ScoreEntry{}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// TestPayloadBeforeFirstWindowFallsBackToFullList 锁定"首个窗口没跑完不许下发空列表"。
func TestPayloadBeforeFirstWindowFallsBackToFullList(t *testing.T) {
	reg, _ := newTestRegistry(t, config.DefaultSTUNURLs(), 4, defaultListResults())

	p := reg.Payload()

	if got := selectedURLs(p); urlsOf(got) != urlsOf(config.DefaultSTUNURLs()) {
		t.Fatalf("首窗口之前应下发完整默认列表，实际 %v", got)
	}
	if p.Probe.ProbedAt != 0 {
		t.Fatalf("还没探测过时 probedAt 应为 0，实际 %d", p.Probe.ProbedAt)
	}
	if p.Probe.Scores == nil || len(p.Probe.Scores) != 0 {
		t.Fatalf("还没探测过时 scores 应为空数组（不是 nil），实际 %#v", p.Probe.Scores)
	}
	if p.TTLSeconds != 300 {
		t.Fatalf("默认 ttlSeconds 应为 300，实际 %d", p.TTLSeconds)
	}
	if p.Probe.IntervalSeconds != 60 {
		t.Fatalf("默认 intervalSeconds 应为 60，实际 %d", p.Probe.IntervalSeconds)
	}
}

// TestProbeOnceScoresAndSelects 是核心用例：真机实测的分数序列下，
// 粘性条目必选、失败条目落选、总数不超过 MaxSTUN、顺序按分数从高到低。
func TestProbeOnceScoresAndSelects(t *testing.T) {
	urls := config.DefaultSTUNURLs()
	reg, _ := newTestRegistry(t, urls, 4, defaultListResults())

	reg.ProbeOnce(context.Background())
	p := reg.Payload()

	selected := selectedURLs(p)
	if len(selected) != 4 {
		t.Fatalf("默认 MaxSTUN=4，实际下发 %d 条：%v", len(selected), selected)
	}
	if !contains(selected, urls[4]) || !contains(selected, urls[5]) {
		t.Fatalf("粘性条目（cloudflare/google）应始终占位，实际 %v", selected)
	}
	if contains(selected, urls[6]) {
		t.Fatalf("本轮无响应的 qq 不应出现在下发列表里，实际 %v", selected)
	}

	// 探测失败的条目如实上报，且沉到 scores 末尾。
	qq := scoreOf(t, p, urls[6])
	if qq.OK || qq.Score != 0 || qq.RTTMs != 0 {
		t.Fatalf("qq 应如实上报为失败且 0 分，实际 %+v", qq)
	}
	if got := p.Probe.Scores[len(p.Probe.Scores)-1].URL; got != urls[6] {
		t.Fatalf("0 分条目应排在 scores 末尾，实际末尾是 %q", got)
	}

	// 全量上报：7 条都要能看到（不只下发的那 4 条），否则排障时无从判断落选原因。
	if len(p.Probe.Scores) != len(urls) {
		t.Fatalf("scores 应覆盖全部 %d 条配置，实际 %d", len(urls), len(p.Probe.Scores))
	}
	// 顺序必须按分数从高到低。
	for i := 1; i < len(p.Probe.Scores); i++ {
		if p.Probe.Scores[i-1].Score < p.Probe.Scores[i].Score {
			t.Fatalf("scores 未按分数降序：%+v", p.Probe.Scores)
		}
	}
	// 下发顺序 = 分数从高到低。
	prev := math.Inf(1)
	for _, u := range selected {
		s := scoreOf(t, p, u)
		if s.Score > prev {
			t.Fatalf("下发列表未按分数降序：%v", selected)
		}
		prev = s.Score
	}
	// selected 标记与下发列表一致。
	for _, s := range p.Probe.Scores {
		if want := contains(selected, s.URL); s.Selected != want {
			t.Fatalf("%s 的 selected 标记应为 %v，实际 %v", s.URL, want, s.Selected)
		}
	}
	// 最快的那条（douyucdn 11ms）分数最高。
	if p.Probe.Scores[0].URL != urls[0] {
		t.Fatalf("11ms 的 douyucdn 应排第一，实际 %q", p.Probe.Scores[0].URL)
	}
}

// TestStickyKeptWhenItsScoreIsLow 锁定粘性条目的"能力优先"：即使它们是全场最慢的，
// 只要还能应答就必须留在下发列表里（IPv6 srflx 候选只能来自它们）。
func TestStickyKeptWhenItsScoreIsLow(t *testing.T) {
	urls := []string{
		"stun:fast1.example:3478",
		"stun:fast2.example:3478",
		"stun:fast3.example:3478",
		"stun:stun.cloudflare.com:3478",
		"stun:stun.l.google.com:19302",
	}
	results := map[string]Observation{
		urls[0]: okObservation(urls[0], 10),
		urls[1]: okObservation(urls[1], 20),
		urls[2]: okObservation(urls[2], 30),
		// 粘性两条慢到接近封顶：只靠成功率拿到 0.2~0.3 分。
		urls[3]: okObservation(urls[3], 460),
		urls[4]: okObservation(urls[4], 480),
	}
	reg, _ := newTestRegistry(t, urls, 3, results)

	reg.ProbeOnce(context.Background())
	selected := selectedURLs(reg.Payload())

	if !contains(selected, urls[3]) || !contains(selected, urls[4]) {
		t.Fatalf("粘性条目即使分数最低也必须入选，实际 %v", selected)
	}
	// 只剩 1 个名额给最快的非粘性条目。
	if len(selected) != 3 || !contains(selected, urls[0]) {
		t.Fatalf("剩余名额应给分数最高的非粘性条目，实际 %v", selected)
	}
	// 分数上它们确实是最低的——"必选"来自规则，不是来自分数。
	lowest := reg.Payload().Probe.Scores[len(reg.Payload().Probe.Scores)-1]
	if !IsSticky(lowest.URL) {
		t.Fatalf("本用例里分数最低的应是粘性条目，实际 %q", lowest.URL)
	}
}

// TestStickyNotSelectedWhenUnreachable 粘性条目本轮不应答时如实不选入（不硬留）。
func TestStickyNotSelectedWhenUnreachable(t *testing.T) {
	urls := []string{
		"stun:fast.example:3478",
		"stun:stun.cloudflare.com:3478",
		"stun:stun.l.google.com:19302",
	}
	results := map[string]Observation{urls[0]: okObservation(urls[0], 15)}
	reg, _ := newTestRegistry(t, urls, 3, results)

	reg.ProbeOnce(context.Background())
	p := reg.Payload()

	if got := selectedURLs(p); urlsOf(got) != urls[0] {
		t.Fatalf("粘性条目不可达时只应下发可达的那条，实际 %v", got)
	}
	if s := scoreOf(t, p, urls[1]); s.Selected {
		t.Fatalf("不可达的粘性条目不该被标记为 selected：%+v", s)
	}
}

// TestMaxSTUNLimitsPayload 锁定 PR_ICE_MAX_STUN 真的限制了下发条数。
func TestMaxSTUNLimitsPayload(t *testing.T) {
	urls := config.DefaultSTUNURLs()
	allOK := map[string]Observation{}
	for i, u := range urls {
		allOK[u] = okObservation(u, 10*(i+1))
	}

	for _, max := range []int{1, 2, 4, 7} {
		reg, _ := newTestRegistry(t, urls, max, allOK)
		reg.ProbeOnce(context.Background())

		selected := selectedURLs(reg.Payload())
		want := max
		if want > len(urls) {
			want = len(urls)
		}
		if len(selected) != want {
			t.Fatalf("MaxSTUN=%d 时应下发 %d 条，实际 %d：%v", max, want, len(selected), selected)
		}
	}

	// MaxSTUN 比候选还多时不会凭空造出条目，也不会报错。
	reg, _ := newTestRegistry(t, urls, 32, allOK)
	reg.ProbeOnce(context.Background())
	if got := len(selectedURLs(reg.Payload())); got != len(urls) {
		t.Fatalf("MaxSTUN 超过候选数时应下发全部 %d 条，实际 %d", len(urls), got)
	}
}

// TestMaxSTUNGivesRoomToStickyFirst 名额被粘性条目占满时，剩下的名额为 0，
// 且总数仍然不超过上限。
func TestMaxSTUNGivesRoomToStickyFirst(t *testing.T) {
	urls := config.DefaultSTUNURLs()
	results := defaultListResults()

	reg, _ := newTestRegistry(t, urls, 2, results)
	reg.ProbeOnce(context.Background())

	selected := selectedURLs(reg.Payload())
	if len(selected) != 2 {
		t.Fatalf("MaxSTUN=2 时应下发 2 条，实际 %v", selected)
	}
	for _, u := range selected {
		if !IsSticky(u) {
			t.Fatalf("两个名额应被粘性条目占满，实际 %v", selected)
		}
	}
}

// TestAllFailDegradesToFullList 锁定降级决定：整轮全失败时下发**完整配置列表**，
// 而不是空列表；但打分结果如实上报，不做粉饰。
//
// 理由写在 Registry.Payload 的注释里：探测失败说明"服务端此刻看不见这些 STUN"，
// 不代表观众看不见；空列表会直接掐掉 srflx 候选，比给一份未筛选的列表更糟。
func TestAllFailDegradesToFullList(t *testing.T) {
	urls := config.DefaultSTUNURLs()
	reg, _ := newTestRegistry(t, urls, 4, nil) // 空 results = 全部失败

	reg.ProbeOnce(context.Background())
	p := reg.Payload()

	if got := selectedURLs(p); urlsOf(got) != urlsOf(urls) {
		t.Fatalf("全失败时应降级为完整列表，实际 %v", got)
	}
	if p.Probe.ProbedAt == 0 {
		t.Fatal("全失败也是一次完成过的探测，probedAt 不应为 0")
	}
	if len(p.Probe.Scores) != len(urls) {
		t.Fatalf("scores 应覆盖全部配置项，实际 %d", len(p.Probe.Scores))
	}
	for _, s := range p.Probe.Scores {
		if s.OK || s.Score != 0 || s.Selected {
			t.Fatalf("降级只影响 iceServers，打分必须如实为失败：%+v", s)
		}
	}
}

// TestSuccessRateCarriesAcrossWindows 成功率跨窗口累计：同一条 URL 连续两轮
// 一成一败后，分数里那 0.2 的份额应该显示 0.5。
func TestSuccessRateCarriesAcrossWindows(t *testing.T) {
	urls := []string{"stun:a.example:3478"}
	reg, _ := newTestRegistry(t, urls, 1, map[string]Observation{urls[0]: okObservation(urls[0], 0)})

	reg.ProbeOnce(context.Background())
	if s := scoreOf(t, reg.Payload(), urls[0]); !s.OK || s.Score != 1 {
		t.Fatalf("首轮成功应为满分，实际 %+v", s)
	}

	// 第二轮把它改成失败。
	reg.prober = &fakeProber{results: nil}
	reg.ProbeOnce(context.Background())
	p := reg.Payload()

	s := scoreOf(t, p, urls[0])
	if s.OK || s.Score != 0 {
		t.Fatalf("本轮失败应为 0 分（不被历史成功率救回），实际 %+v", s)
	}

	// 第三轮恢复成功：成功率 EWMA = 0.5*1 + 0.5*0.5 = 0.75 → 分数 = 0.8 + 0.2*0.75。
	reg.prober = &fakeProber{results: map[string]Observation{urls[0]: okObservation(urls[0], 0)}}
	reg.ProbeOnce(context.Background())
	s = scoreOf(t, reg.Payload(), urls[0])
	if want := 0.8 + 0.2*0.75; math.Abs(s.Score-want) > 1e-9 {
		t.Fatalf("恢复一轮后分数应为 %v，实际 %v", want, s.Score)
	}
}

// TestExpiresAtIsRecomputedPerCall 锁定 expiresAt 每次响应现算，而不是探测窗口的时间。
func TestExpiresAtIsRecomputedPerCall(t *testing.T) {
	urls := []string{"stun:a.example:3478"}
	reg, _ := newTestRegistry(t, urls, 1, map[string]Observation{urls[0]: okObservation(urls[0], 10)})

	frozen := time.Unix(1_760_000_000, 0)
	reg.now = func() time.Time { return frozen }
	reg.ProbeOnce(context.Background())

	first := reg.Payload()
	if first.ExpiresAt != frozen.Add(300*time.Second).Unix() {
		t.Fatalf("expiresAt 应为 now+300s，实际 %d", first.ExpiresAt)
	}
	if first.Probe.ProbedAt != frozen.Unix() {
		t.Fatalf("probedAt 应来自探测窗口，实际 %d", first.Probe.ProbedAt)
	}

	// 时间前进 100s：expiresAt 跟着走，probedAt 不动（窗口没重跑）。
	frozen = frozen.Add(100 * time.Second)
	second := reg.Payload()
	if second.ExpiresAt != first.ExpiresAt+100 {
		t.Fatalf("expiresAt 应随响应时间前移 100s，实际 %d → %d", first.ExpiresAt, second.ExpiresAt)
	}
	if second.Probe.ProbedAt != first.Probe.ProbedAt {
		t.Fatalf("probedAt 不该被响应时间改写：%d → %d", first.Probe.ProbedAt, second.Probe.ProbedAt)
	}
}

// TestTTLFromConfigFlowsIntoPayload 配置里的 TTL 必须真的出现在载荷里。
func TestTTLFromConfigFlowsIntoPayload(t *testing.T) {
	urls := []string{"stun:a.example:3478"}
	reg, _ := newTestRegistry(t, urls, 1, map[string]Observation{urls[0]: okObservation(urls[0], 10)})

	reg.cfg.ICE.TTL = 90 * time.Second
	frozen := time.Unix(1_760_000_000, 0)
	reg.now = func() time.Time { return frozen }

	p := reg.Payload()
	if p.TTLSeconds != 90 {
		t.Fatalf("ttlSeconds 应为 90，实际 %d", p.TTLSeconds)
	}
	if p.ExpiresAt != frozen.Add(90*time.Second).Unix() {
		t.Fatalf("expiresAt 应为 now+90s，实际 %d", p.ExpiresAt)
	}
}

// TestProbeOnceAsksProberForConfiguredURLs 探测对象必须正好是配置列表（配置变了就跟着变）。
func TestProbeOnceAsksProberForConfiguredURLs(t *testing.T) {
	urls := []string{"stun:a.example:3478", "stun:b.example:3478"}
	reg, fake := newTestRegistry(t, urls, 4, nil)

	reg.ProbeOnce(context.Background())

	if len(fake.calls) != 1 || urlsOf(fake.calls[0]) != urlsOf(urls) {
		t.Fatalf("探测器应收到配置里的 URL 列表，实际 %v", fake.calls)
	}
}

// TestStartProbesInBackgroundAndClose 后台循环能自己跑一轮，Close 后能干净退出。
func TestStartProbesInBackgroundAndClose(t *testing.T) {
	urls := []string{"stun:a.example:3478"}
	cfg := config.Default()
	cfg.ICE.STUNURLs = urls
	cfg.ICE.MaxSTUN = 1
	cfg.ICE.ProbeInterval = 10 * time.Millisecond

	fake := &fakeProber{results: map[string]Observation{urls[0]: okObservation(urls[0], 10)}}
	reg := NewRegistryWithProber(cfg, fake)

	reg.Start()
	defer reg.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if reg.Payload().Probe.ProbedAt != 0 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if reg.Payload().Probe.ProbedAt == 0 {
		t.Fatal("Start 之后应至少完成一轮探测")
	}

	// 再等一会儿，周期循环应该探了不止一轮。
	time.Sleep(50 * time.Millisecond)
	if len(fake.calls) < 2 {
		t.Fatalf("后台循环应周期性重探，实际只探了 %d 轮", len(fake.calls))
	}

	// Close 幂等。
	reg.Close()
	reg.Close()
}

// TestIsSticky 只认主机名，与端口、scheme 大小写无关。
func TestIsSticky(t *testing.T) {
	cases := map[string]bool{
		"stun:stun.l.google.com:19302":     true,
		"stun:stun.cloudflare.com:3478":    true,
		"stuns:stun.cloudflare.com":        true,
		"STUN:STUN.L.GOOGLE.COM:19302":     true,
		"stun:stun.douyucdn.cn:18000":      false,
		"stun:stun.l.google.com.evil.test": false,
		"stun:":                            false,
		"":                                 false,
	}
	for url, want := range cases {
		if got := IsSticky(url); got != want {
			t.Fatalf("IsSticky(%q) = %v，期望 %v", url, got, want)
		}
	}
}
