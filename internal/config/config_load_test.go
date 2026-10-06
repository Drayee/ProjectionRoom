package config

import (
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() 不应失败: %v", err)
	}
	if cfg.Addr != "127.0.0.1:8080" {
		t.Fatalf("默认地址错误: %q", cfg.Addr)
	}
	if cfg.Room.SafetyFactor != 0.8 {
		t.Fatalf("默认安全系数错误: %v", cfg.Room.SafetyFactor)
	}
}

func TestLoadEnvOverride(t *testing.T) {
	t.Setenv(envAddr, "0.0.0.0:9000")
	t.Setenv(envMaxMembers, "8")
	t.Setenv(envSTUNURLs, "stun:a:1, stun:b:2")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() 不应失败: %v", err)
	}
	if cfg.Addr != "0.0.0.0:9000" {
		t.Fatalf("地址覆盖失败: %q", cfg.Addr)
	}
	if cfg.Room.MaxMembers != 8 {
		t.Fatalf("成员上限覆盖失败: %d", cfg.Room.MaxMembers)
	}
	if len(cfg.ICE.STUNURLs) != 2 || cfg.ICE.STUNURLs[1] != "stun:b:2" {
		t.Fatalf("STUN 列表解析失败: %#v", cfg.ICE.STUNURLs)
	}
}

// TestICEDefaults 锁定 ICE 下发的默认形态：7 条默认 STUN、上限 4、每 60s 重探、TTL 300s。
//
// 默认列表的**顺序**也在断言里：它既是探测列表，也是"首窗口还没跑完"时的下发顺序，
// 因此顺序本身就是契约（实测最快的排前面）。
func TestICEDefaults(t *testing.T) {
	cfg := Default()
	ic := cfg.ICE

	want := []string{
		"stun:stun.douyucdn.cn:18000",
		"stun:stun.hitv.com:3478",
		"stun:stun.chat.bilibili.com:3478",
		"stun:stun.miwifi.com:3478",
		"stun:stun.cloudflare.com:3478",
		"stun:stun.l.google.com:19302",
		"stun:stun.qq.com:3478",
	}
	if len(ic.STUNURLs) != len(want) {
		t.Fatalf("默认 STUN 应为 %d 条，实际 %d：%#v", len(want), len(ic.STUNURLs), ic.STUNURLs)
	}
	for i, u := range want {
		if ic.STUNURLs[i] != u {
			t.Fatalf("默认 STUN 第 %d 条应为 %q，实际 %q", i+1, u, ic.STUNURLs[i])
		}
	}
	if ic.MaxSTUN != 4 {
		t.Fatalf("默认 MaxSTUN 应为 4，实际 %d", ic.MaxSTUN)
	}
	if ic.ProbeInterval != 60*time.Second {
		t.Fatalf("默认探测周期应为 60s，实际 %v", ic.ProbeInterval)
	}
	if ic.TTL != 300*time.Second {
		t.Fatalf("默认 TTL 应为 300s，实际 %v", ic.TTL)
	}

	// Load() 走同一条默认路径。
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load() 不应失败: %v", err)
	}
	if !reflect.DeepEqual(loaded.ICE, ic) {
		t.Fatalf("Load() 的 ICE 默认值应与 Default() 一致：%+v vs %+v", loaded.ICE, ic)
	}

	// DefaultSTUNURLs 必须返回新切片：调用方会持有它，共享一份全局切片迟早被改坏。
	a := DefaultSTUNURLs()
	a[0] = "stun:mutated"
	if b := DefaultSTUNURLs(); b[0] != want[0] {
		t.Fatalf("DefaultSTUNURLs 返回了共享切片，已被改坏：%q", b[0])
	}
}

// TestICEEnvOverride 锁定 PR_STUN_URLS / PR_ICE_MAX_STUN / PR_ICE_PROBE_INTERVAL / PR_ICE_TTL。
func TestICEEnvOverride(t *testing.T) {
	t.Setenv(envSTUNURLs, "stun:x:1, stun:y:2 , stun:x:1") // 顺带验证去重
	t.Setenv(envICEMaxSTUN, "2")
	t.Setenv(envICEProbeInterval, "30s")
	t.Setenv(envICETTL, "5m")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() 不应失败: %v", err)
	}
	if len(cfg.ICE.STUNURLs) != 2 || cfg.ICE.STUNURLs[1] != "stun:y:2" {
		t.Fatalf("PR_STUN_URLS 覆盖/去重失败: %#v", cfg.ICE.STUNURLs)
	}
	if cfg.ICE.MaxSTUN != 2 {
		t.Fatalf("PR_ICE_MAX_STUN 覆盖失败: %d", cfg.ICE.MaxSTUN)
	}
	if cfg.ICE.ProbeInterval != 30*time.Second {
		t.Fatalf("PR_ICE_PROBE_INTERVAL 覆盖失败: %v", cfg.ICE.ProbeInterval)
	}
	if cfg.ICE.TTL != 5*time.Minute {
		t.Fatalf("PR_ICE_TTL=5m 应解析为 300s，实际 %v", cfg.ICE.TTL)
	}
}

// TestICETTLBoundaries TTL 的合法范围是 (0, 3600s]：两端都要验收，越界必须报错。
func TestICETTLBoundaries(t *testing.T) {
	okCases := []struct {
		value string
		want  time.Duration
	}{
		{"1s", time.Second},
		{"300s", 300 * time.Second},
		{"5m", 5 * time.Minute},
		{"3600s", time.Hour}, // 上界本身合法
		{"1h", time.Hour},
	}
	for _, tc := range okCases {
		t.Run("ok/"+tc.value, func(t *testing.T) {
			t.Setenv(envICETTL, tc.value)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("PR_ICE_TTL=%q 不应报错: %v", tc.value, err)
			}
			if cfg.ICE.TTL != tc.want {
				t.Fatalf("PR_ICE_TTL=%q 应为 %v，实际 %v", tc.value, tc.want, cfg.ICE.TTL)
			}
		})
	}

	for _, v := range []string{"0", "0s", "-1m", "30", "nope", "3601s", "2h", "1h1s"} {
		t.Run("bad/"+v, func(t *testing.T) {
			t.Setenv(envICETTL, v)
			if _, err := Load(); err == nil {
				t.Fatalf("PR_ICE_TTL=%q 应当报错（允许范围 (0, 1h]）", v)
			}
		})
	}
}

// TestICEEnvRejectsInvalid 其余三个 ICE 变量的非法值同样必须明确报错。
func TestICEEnvRejectsInvalid(t *testing.T) {
	cases := []struct{ name, value string }{
		{envICEMaxSTUN, "0"},   // 0 条等于不下发任何 STUN
		{envICEMaxSTUN, "-1"},  // 负数
		{envICEMaxSTUN, "abc"}, // 非整数
		{envICEProbeInterval, "0"},
		{envICEProbeInterval, "-30s"},
		{envICEProbeInterval, "60"}, // 漏了单位
		{envICEProbeInterval, "2h"}, // 超过 1h 上限
		{envICEProbeInterval, "abc"},
		{envSTUNURLs, ", ,"}, // 解析不出任何 URL
	}
	for _, tc := range cases {
		t.Run(tc.name+"="+tc.value, func(t *testing.T) {
			t.Setenv(tc.name, tc.value)
			if _, err := Load(); err == nil {
				t.Fatalf("%s=%q 应当报错", tc.name, tc.value)
			}
		})
	}
}

func TestLoadRejectsInvalidEnv(t *testing.T) {
	t.Setenv(envMaxMembers, "1")
	if _, err := Load(); err == nil {
		t.Fatal("MaxMembers=1 应当报错")
	}
	t.Setenv(envMaxMembers, "abc")
	if _, err := Load(); err == nil {
		t.Fatal("MaxMembers=abc 应当报错")
	}
	t.Setenv(envMaxMembers, "8")
	t.Setenv(envDefaultStreamBps, "-5")
	if _, err := Load(); err == nil {
		t.Fatal("DefaultStreamBps=-5 应当报错")
	}
}

// TestSegmentDefaults 锁定切片服务的默认配额：并发 2 / 队列 8 / 3 作业每分钟 / 30 分钟 TTL。
func TestSegmentDefaults(t *testing.T) {
	cfg := Default()
	sc := cfg.Segment

	if sc.Concurrency != 2 {
		t.Fatalf("默认并发应为 2，实际 %d", sc.Concurrency)
	}
	if sc.QueueLength != 8 {
		t.Fatalf("默认排队队列长度应为 8，实际 %d", sc.QueueLength)
	}
	if sc.RatePerMinute != 3 || sc.Burst != 3 {
		t.Fatalf("默认令牌桶应为 3/分钟、容量 3，实际 %d/%d", sc.RatePerMinute, sc.Burst)
	}
	if sc.TTL != 30*time.Minute {
		t.Fatalf("默认 TTL 应为 30 分钟，实际 %v", sc.TTL)
	}
	if sc.SingleResponseMaxBytes != 1<<30 {
		t.Fatalf("默认单次返回上限应为 1GiB，实际 %d", sc.SingleResponseMaxBytes)
	}
	if sc.MaxDuration != 60*time.Minute {
		t.Fatalf("默认时长上限应为 60 分钟，实际 %v", sc.MaxDuration)
	}
	if sc.MaxSourceBytes != 16<<30 {
		t.Fatalf("默认源文件上限应为 16GiB，实际 %d", sc.MaxSourceBytes)
	}
	if sc.SegmentSeconds != 2 {
		t.Fatalf("默认分片时长应为 2s，实际 %v", sc.SegmentSeconds)
	}
	// 默认打包（每 100 片一个 .bin）：这是把产物文件数从 ~1800 降到 ~18 的关键。
	if sc.PackSize != 100 {
		t.Fatalf("默认打包粒度应为 100，实际 %d", sc.PackSize)
	}
	if sc.PackSize != DefaultPackSize {
		t.Fatalf("默认打包粒度应与 DefaultPackSize 一致，实际 %d", sc.PackSize)
	}
}

func TestSegmentEnvOverride(t *testing.T) {
	t.Setenv(envSegmentConcurrency, "4")
	t.Setenv(envSegmentQueueLength, "0")
	t.Setenv(envSegmentRatePerMinute, "6")
	t.Setenv(envSegmentBurst, "1")
	t.Setenv(envSegmentTTL, "90s")
	t.Setenv(envSegmentCleanup, "5s")
	t.Setenv(envSegmentSingleMax, "1024")
	t.Setenv(envSegmentMaxDuration, "20m")
	t.Setenv(envSegmentMaxSource, "2048")
	t.Setenv(envSegmentSeconds, "1.5")
	t.Setenv(envSegmentPackSize, "1")
	t.Setenv(envSegmentTempDir, `D:\tmp\pr-seg`)
	t.Setenv(envFFmpeg, `D:\ffmpeg\bin\ffmpeg.exe`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() 不应失败: %v", err)
	}
	sc := cfg.Segment

	if sc.Concurrency != 4 || sc.RatePerMinute != 6 || sc.Burst != 1 {
		t.Fatalf("并发/速率/容量覆盖失败: %+v", sc)
	}
	// QueueLength=0 是合法值：表示不排队，超出并发的请求立刻 429。
	if sc.QueueLength != 0 {
		t.Fatalf("队列长度应允许覆盖为 0，实际 %d", sc.QueueLength)
	}
	if sc.TTL != 90*time.Second || sc.CleanupInterval != 5*time.Second {
		t.Fatalf("TTL/清理周期覆盖失败: %+v", sc)
	}
	if sc.SingleResponseMaxBytes != 1024 || sc.MaxSourceBytes != 2048 {
		t.Fatalf("大小上限覆盖失败: %+v", sc)
	}
	if sc.MaxDuration != 20*time.Minute || sc.SegmentSeconds != 1.5 {
		t.Fatalf("时长/分片覆盖失败: %+v", sc)
	}
	// 1 是合法值：表示关掉打包（逐片一个 c*.m4s，与打包功能出现之前一致）。
	if sc.PackSize != 1 {
		t.Fatalf("打包粒度应允许覆盖为 1（不打包），实际 %d", sc.PackSize)
	}
	if sc.TempDir != `D:\tmp\pr-seg` || sc.FFmpegPath != `D:\ffmpeg\bin\ffmpeg.exe` {
		t.Fatalf("目录/ffmpeg 覆盖失败: %+v", sc)
	}
}

func TestSegmentEnvRejectsInvalid(t *testing.T) {
	cases := []struct{ name, value string }{
		{envSegmentConcurrency, "0"},
		{envSegmentConcurrency, "abc"},
		{envSegmentQueueLength, "-1"},
		{envSegmentRatePerMinute, "0"},
		{envSegmentBurst, "-3"},
		{envSegmentTTL, "30"},
		{envSegmentTTL, "-1m"},
		{envSegmentCleanup, "nope"},
		{envSegmentSingleMax, "0"},
		{envSegmentMaxDuration, "0s"},
		{envSegmentMaxSource, "x"},
		{envSegmentSeconds, "0"},
		{envSegmentPackSize, "0"},
		{envSegmentPackSize, "-7"},
		{envSegmentPackSize, "many"},
	}
	for _, tc := range cases {
		t.Run(tc.name+"="+tc.value, func(t *testing.T) {
			t.Setenv(tc.name, tc.value)
			if _, err := Load(); err == nil {
				t.Fatalf("%s=%q 应当报错", tc.name, tc.value)
			}
		})
	}
}

// TestHostGraceDefaults 锁定主播断线宽限期的默认值：60s。
// 它必须覆盖"半开连接被 ping/写超时收尸"的最坏情况（20s ping + 10s 写超时 ≈ 30s），
// 否则主播重连时会撞上"房间已销毁"。
func TestHostGraceDefaults(t *testing.T) {
	if DefaultHostGrace != 60*time.Second {
		t.Fatalf("默认宽限期应为 60s，实际 %v", DefaultHostGrace)
	}
	if MaxHostGrace != 10*time.Minute {
		t.Fatalf("宽限期上限应为 10m，实际 %v", MaxHostGrace)
	}
	if cfg := Default(); cfg.Room.HostGrace != DefaultHostGrace {
		t.Fatalf("Default() 的宽限期应为 %v，实际 %v", DefaultHostGrace, cfg.Room.HostGrace)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() 不应失败: %v", err)
	}
	if cfg.Room.HostGrace != 60*time.Second {
		t.Fatalf("未配置时宽限期应为 60s，实际 %v", cfg.Room.HostGrace)
	}
}

// TestHostGraceEnvOverride 锁定 PR_ROOM_HOST_GRACE 接受 Go 时长字符串（45s / 2m）。
func TestHostGraceEnvOverride(t *testing.T) {
	cases := []struct {
		value string
		want  time.Duration
	}{
		{"45s", 45 * time.Second},
		{"2m", 2 * time.Minute},
		{"600s", 10 * time.Minute},
		{"500ms", 500 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv(envRoomHostGrace, tc.value)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("PR_ROOM_HOST_GRACE=%q 不应报错: %v", tc.value, err)
			}
			if cfg.Room.HostGrace != tc.want {
				t.Fatalf("PR_ROOM_HOST_GRACE=%q 应解析为 %v，实际 %v", tc.value, tc.want, cfg.Room.HostGrace)
			}
		})
	}
}

// TestHostGraceEnvRejectsInvalid 锁定非法宽限期必须明确报错，而不是静默回退：
// 静默回退会让"配错了"和"配对了"在运行期完全无法区分。
func TestHostGraceEnvRejectsInvalid(t *testing.T) {
	for _, v := range []string{
		"0",       // 0 会让宽限期失效（等于立刻销毁房间）
		"0s",      // 同上，显式写法也不能接受
		"-1m",     // 负数
		"30",      // 缺少单位
		"nope",    // 无法解析
		"601s",    // 超过 10 分钟上限
		"11m",     // 同上
		"1h",      // 同上
		"60 秒",    // 中文单位不是 Go 时长
		"60s 30s", // 拼接
	} {
		t.Run(v, func(t *testing.T) {
			t.Setenv(envRoomHostGrace, v)
			if _, err := Load(); err == nil {
				t.Fatalf("PR_ROOM_HOST_GRACE=%q 应当报错", v)
			}
		})
	}
}

// TestRoomMaxDepthDefaults 锁定分发树深度上限的默认值：3（PR_MAX_DEPTH 未设置时）。
//
// 从 4 收到 3 的依据：产品目标为「延迟与卡顿优先」。每跳中继实测给端到端多加
// 58–78ms（docs/ALGORITHM.md §2.1 的实测表），4 跳最坏再叠约 300ms；
// 而多出来的那一层在十几人的房间里很少真的换来容量（对价见 SPEC §6.2）。
func TestRoomMaxDepthDefaults(t *testing.T) {
	if DefaultRoomMaxDepth != 3 {
		t.Fatalf("默认深度上限应为 3，实际 %d", DefaultRoomMaxDepth)
	}
	if MinRoomMaxDepth != 1 || MaxRoomMaxDepth != 6 {
		t.Fatalf("允许范围应为 [1,6]，实际 [%d,%d]", MinRoomMaxDepth, MaxRoomMaxDepth)
	}
	if got := Default().Room.MaxDepth; got != DefaultRoomMaxDepth {
		t.Fatalf("Default() 的深度上限应为 %d，实际 %d", DefaultRoomMaxDepth, got)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() 不应失败: %v", err)
	}
	if cfg.Room.MaxDepth != 3 {
		t.Fatalf("未配置 PR_MAX_DEPTH 时深度上限应为 3，实际 %d", cfg.Room.MaxDepth)
	}
}

// TestRoomMaxDepthEnvOverride 锁定 PR_MAX_DEPTH：范围 [1,6] 的两端都必须认。
func TestRoomMaxDepthEnvOverride(t *testing.T) {
	for _, want := range []int{1, 2, 3, 4, 5, 6} {
		t.Run(strconv.Itoa(want), func(t *testing.T) {
			t.Setenv(envRoomMaxDepth, strconv.Itoa(want))

			cfg, err := Load()
			if err != nil {
				t.Fatalf("PR_MAX_DEPTH=%d 不应报错: %v", want, err)
			}
			if cfg.Room.MaxDepth != want {
				t.Fatalf("PR_MAX_DEPTH=%d 覆盖失败，实际 %d", want, cfg.Room.MaxDepth)
			}
		})
	}
}

// TestRoomMaxDepthEnvRejectsInvalid 锁定越界与非法值必须明确报错，不静默回退：
// 深度写小会把观众挡在房外（Unassigned 变多），写大会把延迟预算翻一倍，
// 两者都必须立刻可见，而不是"配错了和配对了在运行期看起来一样"。
func TestRoomMaxDepthEnvRejectsInvalid(t *testing.T) {
	for _, v := range []string{
		"0",   // 0 等于除主播外一个人都放不下
		"-1",  // 负数
		"7",   // 超过上限
		"12",  // 同上
		"abc", // 非整数
		"2.5", // 非整数
		"3层",  // 带单位
	} {
		t.Run(v, func(t *testing.T) {
			t.Setenv(envRoomMaxDepth, v)
			if _, err := Load(); err == nil {
				t.Fatalf("PR_MAX_DEPTH=%q 应当报错（允许范围 [1,6]）", v)
			}
		})
	}
}

// TestStaticDefaults 锁定单端口部署的默认形态：托管 client/dist，且默认开启。
func TestStaticDefaults(t *testing.T) {
	cfg := Default()
	if !cfg.Static.Serve {
		t.Fatal("静态资源托管默认应开启")
	}
	if cfg.Static.Dir != "client/dist" {
		t.Fatalf("默认静态目录应为 client/dist，实际 %q", cfg.Static.Dir)
	}
}

func TestStaticEnvOverride(t *testing.T) {
	t.Setenv(envStaticDir, `D:\other\dist`)
	t.Setenv(envServeStatic, "0")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() 不应失败: %v", err)
	}
	if cfg.Static.Dir != `D:\other\dist` {
		t.Fatalf("静态目录覆盖失败: %q", cfg.Static.Dir)
	}
	if cfg.Static.Serve {
		t.Fatal("PR_SERVE_STATIC=0 应当关闭静态托管")
	}

	// 开关的常见写法都要认，且大小写不敏感。
	for _, v := range []string{"1", "true", "TRUE", "on", "yes"} {
		t.Setenv(envServeStatic, v)
		on, err := Load()
		if err != nil {
			t.Fatalf("PR_SERVE_STATIC=%q 不应报错: %v", v, err)
		}
		if !on.Static.Serve {
			t.Fatalf("PR_SERVE_STATIC=%q 应当开启静态托管", v)
		}
	}
}

// TestStaticDirMissingIsNotFatal 锁定一条边界：目录不存在/路径古怪都只是运行期降级，
// 不可以在 Load 阶段报错——未构建前端时 go run ./cmd 仍要能起 API 与 /ws。
func TestStaticDirMissingIsNotFatal(t *testing.T) {
	for _, dir := range []string{
		`D:\definitely\not\here\dist`,
		"relative/not/here",
		"...",
	} {
		t.Setenv(envStaticDir, dir)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("PR_STATIC_DIR=%q 不应让 Load 失败: %v", dir, err)
		}
		if cfg.Static.Dir != dir {
			t.Fatalf("静态目录应当原样保留 %q，实际 %q", dir, cfg.Static.Dir)
		}
	}
}

func TestStaticEnvRejectsInvalidSwitch(t *testing.T) {
	for _, v := range []string{"maybe", "2", "开", "-1"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv(envServeStatic, v)
			if _, err := Load(); err == nil {
				t.Fatalf("PR_SERVE_STATIC=%q 应当报错", v)
			}
		})
	}
}

// TestDownloadsDirFollowsStaticRoot 锁定默认形态：不配 PR_DOWNLOADS_DIR 时，
// 切片器二进制的目录就是 <Static.Dir>/downloads —— URL /downloads/<file> 与磁盘布局一致。
func TestDownloadsDirFollowsStaticRoot(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() 不应失败: %v", err)
	}
	if cfg.Downloads.Dir != "" {
		t.Fatalf("默认 Downloads.Dir 应留空（跟随静态根），实际 %q", cfg.Downloads.Dir)
	}
	if got, want := cfg.DownloadsDir(), filepath.Join("client/dist", "downloads"); got != want {
		t.Fatalf("默认下载目录应为 %q，实际 %q", want, got)
	}

	// 跟随的含义是"静态根变了，下载目录跟着变"。
	t.Setenv(envStaticDir, `D:\other\dist`)
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load() 不应失败: %v", err)
	}
	if got, want := cfg.DownloadsDir(), filepath.Join(`D:\other\dist`, "downloads"); got != want {
		t.Fatalf("静态根覆盖后下载目录应为 %q，实际 %q", want, got)
	}
}

// TestDownloadsEnvOverride 锁定 PR_DOWNLOADS_DIR：非空即以它为准，不再跟随静态根。
func TestDownloadsEnvOverride(t *testing.T) {
	t.Setenv(envDownloadsDir, `D:\pr-downloads`)
	t.Setenv(envStaticDir, `D:\other\dist`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() 不应失败: %v", err)
	}
	if cfg.Downloads.Dir != `D:\pr-downloads` {
		t.Fatalf("下载目录覆盖失败: %q", cfg.Downloads.Dir)
	}
	if got := cfg.DownloadsDir(); got != `D:\pr-downloads` {
		t.Fatalf("DownloadsDir() 应返回覆盖值 %q，实际 %q", `D:\pr-downloads`, got)
	}
}
