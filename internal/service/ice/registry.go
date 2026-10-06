package ice

import (
	"context"
	"sort"
	"sync"
	"time"

	"ProjectionRoom/internal/config"
)

// ProbeTimeout 是单次 UDP STUN Binding Request 的超时（契约值 1.5s）。
//
// 为什么是 1.5s：实测可用的服务器最慢 203ms，不可用的那条是 4s 无响应。
// 1.5s 给出 7 倍余量，同时把"一整轮探测"的最坏耗时钉在 1.5s 内
// （并发探测，不是 7 × 1.5s），后台循环不会被拖长。
const ProbeTimeout = 1500 * time.Millisecond

// Observation 是一次探测的结果。
type Observation struct {
	URL string
	OK  bool
	// RTT 仅在本轮成功时有效；失败时为 0。
	RTT time.Duration
}

// Prober 探测一组 STUN URL。
//
// 契约：必须并发、必须尊重 ctx；返回值按 URL 索引（顺序不重要），每个 URL 至多一条。
// 实现方可以返回缺失的条目，Registry 会把缺失的 URL 记为"本轮失败"。
type Prober interface {
	Probe(ctx context.Context, urls []string) []Observation
}

// ScoreEntry 是单个条目的本轮探测结果与得分，直接进 probe.scores。
type ScoreEntry struct {
	URL string `json:"url"`
	// RTTMs 是本轮往返延迟；ok=false 时为 0（表示"没有 RTT"，而不是"0ms 很快"）。
	RTTMs int64 `json:"rttMs"`
	OK    bool  `json:"ok"`
	// Score 综合 RTT 与跨窗口成功率；本轮失败为 0。
	Score float64 `json:"score"`
	// Selected 表示这条 URL 出现在本次下发列表里。
	Selected bool `json:"selected"`
}

// ProbeReport 是探测窗口的可观测部分。
type ProbeReport struct {
	// ProbedAt 是窗口完成时刻（Unix 秒）。0 表示"第一个窗口还没跑完"，
	// 此时 Scores 为空数组，下发的是完整配置列表。
	ProbedAt int64 `json:"probedAt"`
	// IntervalSeconds 是探测周期，客户端可以据此判断数据新不新鲜。
	IntervalSeconds int `json:"intervalSeconds"`
	// Scores 覆盖**配置列表里的每一条**（不只下发的那几条），按分数从高到低。
	Scores []ScoreEntry `json:"scores"`
}

// Payload 是下发给客户端的 ICE 载荷。
//
// iceServers 的形状与退役 TURN 之前完全一致（只做加法），前端解析逻辑不需要改。
type Payload struct {
	IceServers []map[string]any `json:"iceServers"`
	// TTLSeconds 是本载荷的建议有效期（向下取整的秒数）。
	TTLSeconds int `json:"ttlSeconds"`
	// ExpiresAt 是过期时刻（Unix 秒），每次响应现算，不缓存成探测窗口的时间。
	ExpiresAt int64       `json:"expiresAt"`
	Probe     ProbeReport `json:"probe"`
}

// MergeInto 把载荷摊进一个已有的响应体。
//
// /api/rooms 还要带 roomId 与 capacity，所以用"合并"而不是"整体替换"；
// 键名与 json tag 同源，保证 /api/ice 与建房间响应里的字段完全一致。
func (p Payload) MergeInto(dst map[string]any) {
	dst["iceServers"] = p.IceServers
	dst["ttlSeconds"] = p.TTLSeconds
	dst["expiresAt"] = p.ExpiresAt
	dst["probe"] = p.Probe
}

// ServersOf 把 URL 列表转成 iceServers 条目。
// 单一形状（只有 urls，没有 username/credential）：TURN 退役后不再有需要凭据的条目。
func ServersOf(urls []string) []map[string]any {
	out := make([]map[string]any, 0, len(urls))
	for _, u := range urls {
		out = append(out, map[string]any{"urls": u})
	}
	return out
}

// window 是一次完整探测窗口的快照。写入后不再修改，靠换指针发布，因此读侧无需加锁复制。
type window struct {
	probedAt time.Time
	scores   []ScoreEntry // 全量条目，按分数从高到低
	selected []string     // 本轮下发的 URL，顺序 = 分数从高到低
}

// Registry 持有"最近一次探测窗口"与跨窗口的轮询/成功率状态，并负责拼出下发载荷。
type Registry struct {
	cfg    *config.Config
	prober Prober
	// now 可注入，便于测试断言 TTL 与 expiresAt 的算法本身。
	now func() time.Time

	mu     sync.RWMutex
	latest *window
	// rates 是跨窗口的 EWMA 成功率（按 URL）。
	rates map[string]float64
	// rotate 是跨窗口保留的加权轮询状态。
	rotate *selector

	startOnce sync.Once
	closeOnce sync.Once
	stop      chan struct{}
	wg        sync.WaitGroup
}

// NewRegistry 用真实 UDP 探测器构造 Registry，但**不启动**后台探测（见 Start）。
func NewRegistry(cfg *config.Config) *Registry {
	return NewRegistryWithProber(cfg, NewUDPProber(ProbeTimeout))
}

// NewRegistryWithProber 同上，探测器由调用方注入（测试用假实现，避免依赖外网）。
func NewRegistryWithProber(cfg *config.Config, prober Prober) *Registry {
	return &Registry{
		cfg:    cfg,
		prober: prober,
		now:    time.Now,
		rates:  make(map[string]float64),
		rotate: newSelector(),
		stop:   make(chan struct{}),
	}
}

// Start 启动后台探测循环：立即探一轮（异步，不阻塞监听），之后每 ProbeInterval 一轮。
// 可重复调用，只有第一次生效。
func (r *Registry) Start() {
	r.startOnce.Do(func() {
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()

			r.ProbeOnce(context.Background())

			t := time.NewTicker(r.cfg.ICE.ProbeInterval)
			defer t.Stop()
			for {
				select {
				case <-r.stop:
					return
				case <-t.C:
					r.ProbeOnce(context.Background())
				}
			}
		}()
	})
}

// Close 停止后台探测并等待在途的一轮结束。幂等，也可以在 Start 之前调用。
func (r *Registry) Close() {
	r.closeOnce.Do(func() { close(r.stop) })
	r.wg.Wait()
}

// ProbeOnce 同步跑完一轮探测并更新内部状态（Start 的循环与测试都走这里）。
func (r *Registry) ProbeOnce(ctx context.Context) {
	urls := r.cfg.ICE.STUNURLs
	obs := r.prober.Probe(ctx, urls)

	byURL := make(map[string]Observation, len(obs))
	for _, o := range obs {
		if _, dup := byURL[o.URL]; !dup {
			byURL[o.URL] = o
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// 以**配置列表**为准展开：即使探测器少回了几条，也要有条目可观察（记为失败）。
	stats := make([]ScoreEntry, 0, len(urls))
	for _, u := range urls {
		o := byURL[u]
		prev, seen := r.rates[u]
		if !seen {
			prev = -1
		}
		rate := UpdateSuccessRate(prev, o.OK)
		r.rates[u] = rate

		stats = append(stats, ScoreEntry{
			URL:   u,
			RTTMs: o.RTT.Milliseconds(),
			OK:    o.OK,
			Score: Score(o.OK, o.RTT, rate),
		})
	}

	selected := r.selectURLs(stats)

	scores := sortedByScore(stats)
	chosen := make(map[string]bool, len(selected))
	for _, u := range selected {
		chosen[u] = true
	}
	for i := range scores {
		scores[i].Selected = chosen[scores[i].URL]
	}

	r.latest = &window{probedAt: r.now(), scores: scores, selected: selected}
}

// fastestQuota 是"恒选席位"里按分数取的条数。
//
// 为什么要有它（延迟优先）：纯加权轮询下，上一窗口最快的两条也可能因为"轮到自己让位"
// 而落选，客户端就少了一条实测最快的 STUN。实测最快的两条是**当轮已知最可靠的路径**，
// 它们的价值高于"让低分项轮换一圈"；因此把最快两条升级为恒选，
// 轮询只负责填剩下那些"次优但有希望回升"的名额。
const fastestQuota = 2

// selectURLs 决定本轮下发哪些 URL（在 r.mu 保护下调用，因为要改轮询状态）。
//
// 席位分两类：
//  1. 恒选（mandatory）
//     - 按 score 降序的**最快 2 条**（只数本轮健康的）—— 延迟优先。
//     - 本轮健康的粘性条目（stun.l.google.com / stun.cloudflare.com）—— 能力优先，
//     它们是列表里少数有 AAAA 的服务器，IPv6 srflx 候选只能来自它们（见 stickyHosts）。
//     不健康的条目既不占恒选席位，也不参与轮询。
//  2. 轮询（SWRR，权重 ∝ score + 兜底小权重）
//     名额 = MaxSTUN - len(恒选)，候选 = 本轮健康、且**没被恒选选中**的条目。
//     状态跨窗口保留 → 同一段分数序列下可复现，相邻窗口只换 1~2 条。
//
// 硬上限：恒选条数可能超过 MaxSTUN（例如 MaxSTUN=3 时 2 快 + 2 粘性 = 4）。
// 此时按 score 降序截断，即**最快的优先留下**；被截掉的粘性条目本轮不下发，
// 但它仍在候选列表里，下一轮用自己的健康度继续竞争。
func (r *Registry) selectURLs(stats []ScoreEntry) []string {
	ordered := sortedByScore(stats)
	limit := r.cfg.ICE.MaxSTUN

	// 先把恒选集合算成集合，再按 score 降序物化 —— 这样"截断"天然等价于
	// "淘汰分数最低的恒选条目"，不需要额外排序。
	mandatorySet := make(map[string]bool, limit)
	fast := 0
	for _, s := range ordered {
		if !s.OK {
			continue
		}
		if fast < fastestQuota {
			mandatorySet[s.URL] = true
			fast++
		}
		if IsSticky(s.URL) {
			mandatorySet[s.URL] = true
		}
	}

	mandatory := make([]string, 0, len(mandatorySet))
	for _, s := range ordered {
		if mandatorySet[s.URL] {
			mandatory = append(mandatory, s.URL)
		}
	}
	if len(mandatory) > limit {
		mandatory = mandatory[:limit]
	}

	chosen := make(map[string]bool, limit)
	for _, u := range mandatory {
		chosen[u] = true
	}

	if remaining := limit - len(chosen); remaining > 0 {
		cands := make([]weighted, 0, len(ordered))
		for _, s := range ordered {
			if s.OK && !chosen[s.URL] {
				cands = append(cands, weighted{url: s.URL, w: weightOf(s.Score)})
			}
		}
		for _, u := range r.rotate.pick(cands, remaining) {
			chosen[u] = true
		}
	}

	out := make([]string, 0, len(chosen))
	for _, s := range ordered {
		if chosen[s.URL] {
			out = append(out, s.URL)
		}
	}
	return out
}

// Payload 组装当前应该下发的载荷。expiresAt 每次调用现算（不是探测窗口的时间）。
//
// 降级行为（重要）：
//   - 第一个窗口还没跑完（latest == nil）→ 下发**完整**配置列表，而不是空列表。
//   - 整轮探测全失败（选中 0 条）→ 同样退回完整配置列表。
//     理由：探测失败说明"服务端此刻看不见这些 STUN"（断网、DNS 挂、隧道抖动），
//     但这不代表**观众**看不见它们。此时给浏览器一个空列表等于直接放弃 srflx 候选，
//     比给一份未经筛选的列表更糟；而浏览器拿到列表后自己失败一次是无害的。
//     注意 score 与 selected 仍然如实上报，降级只影响 iceServers，不粉饰探测结果。
func (r *Registry) Payload() Payload {
	ttl := r.cfg.ICE.TTL
	fallback := r.cfg.ICE.STUNURLs

	r.mu.RLock()
	latest := r.latest
	r.mu.RUnlock()

	report := ProbeReport{
		IntervalSeconds: int(r.cfg.ICE.ProbeInterval / time.Second),
		Scores:          []ScoreEntry{},
	}
	urls := fallback
	if latest != nil {
		report.ProbedAt = latest.probedAt.Unix()
		report.Scores = latest.scores
		if len(latest.selected) > 0 {
			urls = latest.selected
		}
	}

	return Payload{
		IceServers: ServersOf(urls),
		TTLSeconds: int(ttl / time.Second),
		ExpiresAt:  r.now().Add(ttl).Unix(),
		Probe:      report,
	}
}

// sortedByScore 按分数从高到低排序；同分保持入参顺序（调用方按配置顺序传入），
// 因此下发给客户端的顺序是确定的：高分在前，探测失败的沉底。
func sortedByScore(stats []ScoreEntry) []ScoreEntry {
	out := make([]ScoreEntry, len(stats))
	copy(out, stats)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})
	return out
}
