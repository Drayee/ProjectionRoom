package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/service/ice"
)

// 本文件是「ICE 载荷」这条端到端链路的验收：用**真实 router**（httptest）验证
// /api/ice 与 POST /api/rooms 这两个响应体的形状、TTL 计算与"绝不含 turn:"。
//
// 包级决定：handler 包的所有用例都换成假探测器。理由有两个——
//  1. 单测不该依赖外网：真探 STUN 会让 `go test ./...` 的耗时与结果随网络漂移；
//  2. 真实 UDP 路径有更合适的归属：internal/service/ice 的单测（本机假 STUN）
//     负责报文语义，README 的真机实测负责公网表现。
//
// 想验证真实探测时不要改这里，直接用真机实测流程（见报告）。
func TestMain(m *testing.M) {
	prev := newICERegistry
	newICERegistry = offlineICERegistry
	code := m.Run()
	newICERegistry = prev
	os.Exit(code)
}

// fixedProber 是对所有 URL 都给"成功、固定 20ms"的探测器：确定性来自它本身，
// 而不是来自网络。
type fixedProber struct {
	rtt    time.Duration
	failed map[string]bool
}

func (f fixedProber) Probe(_ context.Context, urls []string) []ice.Observation {
	out := make([]ice.Observation, 0, len(urls))
	for _, u := range urls {
		if f.failed[u] {
			out = append(out, ice.Observation{URL: u})
			continue
		}
		out = append(out, ice.Observation{URL: u, OK: true, RTT: f.rtt})
	}
	return out
}

// offlineICERegistry 造一个"已经探过一个窗口"的 Registry：不起后台循环，不碰外网。
func offlineICERegistry(cfg *config.Config) *ice.Registry {
	reg := ice.NewRegistryWithProber(cfg, fixedProber{rtt: 20 * time.Millisecond})
	reg.ProbeOnce(context.Background())
	return reg
}

// useICERegistry 在单个用例里临时替换 registry 工厂（想控制探测结果时用）。
func useICERegistry(t *testing.T, factory func(*config.Config) *ice.Registry) {
	t.Helper()

	prev := newICERegistry
	newICERegistry = factory
	t.Cleanup(func() { newICERegistry = prev })
}

// icePayload 复刻前端拿到的 ICE 载荷字段（iceServers 形状与 TURN 退役前一致）。
type icePayload struct {
	IceServers []map[string]any `json:"iceServers"`
	TTLSeconds int              `json:"ttlSeconds"`
	ExpiresAt  int64            `json:"expiresAt"`
	Probe      struct {
		ProbedAt        int64 `json:"probedAt"`
		IntervalSeconds int   `json:"intervalSeconds"`
		Scores          []struct {
			URL      string  `json:"url"`
			RTTMs    int64   `json:"rttMs"`
			OK       bool    `json:"ok"`
			Score    float64 `json:"score"`
			Selected bool    `json:"selected"`
		} `json:"scores"`
	} `json:"probe"`
}

// decodeICE 读一个响应体并同时保留原始字节（用于"整段 JSON 里不许出现 turn:"的断言）。
func decodeICE(t *testing.T, resp *http.Response) (icePayload, string) {
	t.Helper()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读响应体失败: %v", err)
	}
	resp.Body.Close()

	var p icePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("解析 ICE 载荷失败: %v（原文 %s）", err, raw)
	}
	return p, string(raw)
}

// urlsIn 取出 iceServers 里的 urls 字段。
func urlsIn(t *testing.T, servers []map[string]any) []string {
	t.Helper()

	out := make([]string, 0, len(servers))
	for _, s := range servers {
		u, ok := s["urls"].(string)
		if !ok {
			t.Fatalf("iceServers 条目缺少字符串 urls 字段: %#v", s)
		}
		out = append(out, u)
	}
	return out
}

// assertNoTURN 是本文件存在的理由：退役 TURN 之后，任何响应里出现 turn:
// 都说明退役没做干净（字段残留、env 残留或旧分支兜底）。
func assertNoTURN(t *testing.T, raw string, servers []map[string]any) {
	t.Helper()

	if strings.Contains(strings.ToLower(raw), "turn:") {
		t.Fatalf("响应体里出现了 turn: —— TURN 应已彻底退役：%s", raw)
	}
	for _, u := range urlsIn(t, servers) {
		if strings.HasPrefix(strings.ToLower(u), "turn:") || strings.HasPrefix(strings.ToLower(u), "turns:") {
			t.Fatalf("iceServers 里出现了 TURN 条目：%q", u)
		}
	}
	for _, s := range servers {
		if _, has := s["username"]; has {
			t.Fatalf("iceServers 条目不该带 TURN 凭据字段：%#v", s)
		}
		if _, has := s["credential"]; has {
			t.Fatalf("iceServers 条目不该带 TURN 凭据字段：%#v", s)
		}
	}
}

// TestICEAPIHasNoTURNAndCarriesTTL 真实 router 下的 /api/ice：形状、TTL、探测块、无 TURN。
func TestICEAPIHasNoTURNAndCarriesTTL(t *testing.T) {
	srv, cfg := newTestServer(t)

	resp, err := http.Get(srv.URL + "/api/ice")
	if err != nil {
		t.Fatalf("GET /api/ice 失败: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/api/ice 应返回 200，实际 %d", resp.StatusCode)
	}

	payload, raw := decodeICE(t, resp)
	assertNoTURN(t, raw, payload.IceServers)

	// iceServers 的形状保持不变（只有 urls），只做加法。
	if len(payload.IceServers) == 0 {
		t.Fatal("iceServers 不该为空：前端需要它才能发起 WebRTC")
	}
	for _, s := range payload.IceServers {
		if _, has := s["urls"]; !has {
			t.Fatalf("iceServers 条目必须只有 urls 形状：%#v", s)
		}
	}

	// TTL 与 expiresAt：默认 300s，且 expiresAt 应为"现在 + 300s"。
	if payload.TTLSeconds != 300 {
		t.Fatalf("默认 ttlSeconds 应为 300，实际 %d", payload.TTLSeconds)
	}
	now := time.Now().Unix()
	if delta := payload.ExpiresAt - now; delta < 295 || delta > 305 {
		t.Fatalf("expiresAt 应约等于 now+300s（±5s），实际偏移 %d 秒", delta)
	}

	// 探测块：真实窗口已跑过（offlineICERegistry 会先探一轮），分数覆盖完整配置列表。
	if payload.Probe.ProbedAt == 0 {
		t.Fatal("包级假探测器已经探过一轮，probedAt 不该是 0")
	}
	if payload.Probe.IntervalSeconds != 60 {
		t.Fatalf("默认 intervalSeconds 应为 60，实际 %d", payload.Probe.IntervalSeconds)
	}
	if len(payload.Probe.Scores) != len(cfg.ICE.STUNURLs) {
		t.Fatalf("probe.scores 应覆盖全部 %d 条配置，实际 %d",
			len(cfg.ICE.STUNURLs), len(payload.Probe.Scores))
	}
	selected := 0
	for _, s := range payload.Probe.Scores {
		if s.Selected {
			selected++
		}
	}
	if selected != len(payload.IceServers) {
		t.Fatalf("selected 标记数（%d）应与下发条数（%d）一致", selected, len(payload.IceServers))
	}
}

// TestCreateRoomResponseCarriesSameICEPayload 建房间响应与 /api/ice 是同源同形状，
// 同样不许出现 turn:。
func TestCreateRoomResponseCarriesSameICEPayload(t *testing.T) {
	srv, cfg := newTestServer(t)

	resp := postJSONAuth(t, srv.URL+"/api/rooms", map[string]any{"password": "pass"}, roomFixtureToken(t))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("建房间应返回 200，实际 %d", resp.StatusCode)
	}

	payload, raw := decodeICE(t, resp)
	assertNoTURN(t, raw, payload.IceServers)

	if len(payload.IceServers) == 0 {
		t.Fatal("建房间响应必须带 iceServers")
	}
	if payload.TTLSeconds != 300 || payload.Probe.IntervalSeconds != 60 {
		t.Fatalf("建房间响应应带与 /api/ice 相同的 TTL 信息：ttl=%d interval=%d",
			payload.TTLSeconds, payload.Probe.IntervalSeconds)
	}
	if payload.Probe.ProbedAt == 0 {
		t.Fatal("建房间响应里的 probe.probedAt 不该是 0")
	}
	if len(cfg.ICE.STUNURLs) == 0 {
		t.Fatal("默认配置不该是空的 STUN 列表")
	}
}

// TestICEAPIMaxSTUNFromConfig 锁定"最多下发 PR_ICE_MAX_STUN 条"在**真实 router** 上生效。
func TestICEAPIMaxSTUNFromConfig(t *testing.T) {
	for _, max := range []int{1, 2, 5} {
		t.Run("max="+strconv.Itoa(max), func(t *testing.T) {
			t.Setenv("PR_ICE_MAX_STUN", strconv.Itoa(max))

			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("加载配置失败: %v", err)
			}

			useICERegistry(t, offlineICERegistry)
			srv := startTestServer(t, cfg)

			resp, err := http.Get(srv.URL + "/api/ice")
			if err != nil {
				t.Fatalf("GET /api/ice 失败: %v", err)
			}
			payload, raw := decodeICE(t, resp)
			assertNoTURN(t, raw, payload.IceServers)

			if len(payload.IceServers) > max {
				t.Fatalf("PR_ICE_MAX_STUN=%d 却下发了 %d 条：%v",
					max, len(payload.IceServers), urlsIn(t, payload.IceServers))
			}
			if len(payload.IceServers) == 0 {
				t.Fatal("下发条数不该为 0（降级时应回退到完整列表）")
			}
			if len(cfg.ICE.STUNURLs) > max && len(payload.IceServers) != max {
				t.Fatalf("候选数 %d 多于上限 %d 时，应下发恰好 %d 条，实际 %d",
					len(cfg.ICE.STUNURLs), max, max, len(payload.IceServers))
			}
		})
	}
}

// TestICEAPIStunURLsEnvOverride 锁定 PR_STUN_URLS：整体覆盖生效，且覆盖后的列表
// 就是（首窗口前的）下发内容。
func TestICEAPIStunURLsEnvOverride(t *testing.T) {
	t.Setenv("PR_STUN_URLS", "stun:override-a.example:3478, stun:override-b.example:3478")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}

	// 覆盖成"整轮全失败"：走的是降级路径（下发完整覆盖列表），
	// 因此这条同时验证了"覆盖生效"与"降级不返回空列表"。
	useICERegistry(t, func(cfg *config.Config) *ice.Registry {
		reg := ice.NewRegistryWithProber(cfg, fixedProber{failed: map[string]bool{
			"stun:override-a.example:3478": true,
			"stun:override-b.example:3478": true,
		}})
		reg.ProbeOnce(context.Background())
		return reg
	})
	srv := startTestServer(t, cfg)

	resp, err := http.Get(srv.URL + "/api/ice")
	if err != nil {
		t.Fatalf("GET /api/ice 失败: %v", err)
	}
	payload, raw := decodeICE(t, resp)
	assertNoTURN(t, raw, payload.IceServers)

	got := urlsIn(t, payload.IceServers)
	want := []string{"stun:override-a.example:3478", "stun:override-b.example:3478"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("PR_STUN_URLS 覆盖未生效：期望 %v，实际 %v", want, got)
	}

	for _, s := range payload.Probe.Scores {
		if s.OK || s.Score != 0 || s.Selected {
			t.Fatalf("全失败时打分应如实为失败：%+v", s)
		}
	}
}

// TestICETTLOverridesFromEnv 锁定 PR_ICE_TTL 在真实 router 上的效果（含 90s 这种非默认值）。
func TestICETTLOverridesFromEnv(t *testing.T) {
	t.Setenv("PR_ICE_TTL", "90s")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}

	srv := startTestServer(t, cfg)
	resp, err := http.Get(srv.URL + "/api/ice")
	if err != nil {
		t.Fatalf("GET /api/ice 失败: %v", err)
	}
	payload, _ := decodeICE(t, resp)

	if payload.TTLSeconds != 90 {
		t.Fatalf("PR_ICE_TTL=90s 时 ttlSeconds 应为 90，实际 %d", payload.TTLSeconds)
	}
	if delta := payload.ExpiresAt - time.Now().Unix(); delta < 85 || delta > 95 {
		t.Fatalf("expiresAt 应约等于 now+90s，实际偏移 %d 秒", delta)
	}
}
