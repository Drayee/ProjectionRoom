package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	envAddr             = "PR_ADDR"
	envMaxMembers       = "PR_MAX_MEMBERS"
	envDefaultStreamBps = "PR_DEFAULT_STREAM_BPS"
	// envRoomHostGrace 是主播断线宽限期，接受 45s / 2m 这类 Go 时长字符串。
	envRoomHostGrace = "PR_ROOM_HOST_GRACE"
	// envRoomMaxDepth 是分发树深度上限。
	envRoomMaxDepth   = "PR_MAX_DEPTH"
	envSTUNURLs       = "PR_STUN_URLS"
	envAllowedOrigins = "PR_ALLOWED_ORIGINS"

	// ICE 下发的三件套：最多给几条、多久重探一次、载荷能用多久。
	envICEMaxSTUN       = "PR_ICE_MAX_STUN"
	envICEProbeInterval = "PR_ICE_PROBE_INTERVAL"
	envICETTL           = "PR_ICE_TTL"

	// 服务端切片服务的环境变量（前缀 PR_SEGMENT_，ffmpeg 路径单独用 PR_FFMPEG）。
	envSegmentConcurrency   = "PR_SEGMENT_CONCURRENCY"
	envSegmentQueueLength   = "PR_SEGMENT_QUEUE_LENGTH"
	envSegmentRatePerMinute = "PR_SEGMENT_RATE_PER_MINUTE"
	envSegmentBurst         = "PR_SEGMENT_BURST"
	envSegmentTTL           = "PR_SEGMENT_TTL"
	envSegmentCleanup       = "PR_SEGMENT_CLEANUP_INTERVAL"
	envSegmentSingleMax     = "PR_SEGMENT_SINGLE_MAX_BYTES"
	envSegmentMaxDuration   = "PR_SEGMENT_MAX_DURATION"
	envSegmentMaxSource     = "PR_SEGMENT_MAX_SOURCE_BYTES"
	envSegmentSeconds       = "PR_SEGMENT_SECONDS"
	envSegmentPackSize      = "PR_SEGMENT_PACK_SIZE"
	envSegmentTempDir       = "PR_SEGMENT_TEMP_DIR"
	envFFmpeg               = "PR_FFMPEG"

	// 单端口部署：静态资源托管（前端构建产物）。
	envStaticDir   = "PR_STATIC_DIR"
	envServeStatic = "PR_SERVE_STATIC"

	// 客户端切片器二进制的发布目录；留空即跟随 <PR_STATIC_DIR>/downloads。
	envDownloadsDir = "PR_DOWNLOADS_DIR"
)

// DefaultPackSize 是分片打包的默认粒度（每个 .bin 容纳多少片）。
//
// 它在这里而不是在 segment 包里，是因为"每包多少片"是一个配置项，而 config 不能反向
// import segment（queue.go 依赖 config，会形成 import 循环）。segment 与 cmd/segmenter
// 都引用这个常量，因此默认值只有一处定义。
const DefaultPackSize = 100

// ICE 下发的默认值。
//
// 为什么 TTL 是 300s 而探测周期是 60s：TTL 不是"数据保鲜期"，而是"客户端该多久
// 重新问一次服务端"。它比探测周期大几倍，是为了让客户端不必踩着探测窗口刷新；
// 同时又足够短，使一次网络路径变化（换网关、切运营商）能在几分钟内反映到新房间里。
const (
	// DefaultICEMaxSTUN 是单次下发的 STUN 条数上限。
	DefaultICEMaxSTUN = 4
	// DefaultICEProbeInterval 是服务端重新探测一轮的周期。
	DefaultICEProbeInterval = 60 * time.Second
	// DefaultICETTL 是下发载荷的默认有效期。
	DefaultICETTL = 300 * time.Second
	// MaxICETTL 是 TTL 的上限：超过 1h 就等于"配了之后再也不用刷新"，
	// 一旦某台 STUN 长期劣化，房间里的浏览器会一直拿着它。
	MaxICETTL = time.Hour
	// MaxICEProbeInterval 是探测周期的上限：探测本身很便宜（7 个 UDP 包），
	// 周期长到超过 1h 就失去了"跟着网络路径变化走"的意义。
	MaxICEProbeInterval = time.Hour
)

// DefaultSTUNURLs 返回默认的 STUN 列表，**顺序即默认优先级**。
//
// 返回新切片而不是共享变量：调用方（registry）会持有并可能改动它，
// 共享一份全局切片迟早会被某个调用方就地改坏。
//
// 列表构成（2026-02 本机实测 UDP Binding Request 往返）：
//
//	stun.douyucdn.cn:18000      11ms    ✓
//	stun.hitv.com:3478          32ms    ✓
//	stun.chat.bilibili.com:3478 46ms    ✓
//	stun.miwifi.com:3478        119ms   ✓
//	stun.cloudflare.com:3478    203ms   ✓  （有 AAAA，粘性）
//	stun.l.google.com:19302     236ms ↔ 4s 超时（时好时坏，粘性）
//	stun.qq.com:3478            4s 无响应
//
// 最后一条刻意留着：它由服务端探测打分自然沉底/落选，而不是靠人工"把它删掉"。
// 这样它对所有人都保留一个可观察的样本（probe.scores 里能看到它 ok=false），
// 一旦哪天它恢复了，权重轮询会自动把它带回下发列表。
func DefaultSTUNURLs() []string {
	return []string{
		"stun:stun.douyucdn.cn:18000",
		"stun:stun.hitv.com:3478",
		"stun:stun.chat.bilibili.com:3478",
		"stun:stun.miwifi.com:3478",
		"stun:stun.cloudflare.com:3478",
		"stun:stun.l.google.com:19302",
		"stun:stun.qq.com:3478",
	}
}

// 主播断线宽限期（PR_ROOM_HOST_GRACE）。
//
// DefaultHostGrace 取 60s：客户端 WS 重连计划通常是"指数退避、上限 30s 内"，
// 60s 足够覆盖一次网络抖动、页面刷新，也覆盖半开连接被 ping/写超时收尸的最坏情况
// （service 层 20s ping + 10s 写超时 ≈ 最长 30s 才判定断线）。
//
// MaxHostGrace 取 10m：宽限期越长，"主播其实已经走了但房间（含房间码）还占着"的窗口越久。
// 10 分钟是这个取舍的上限，不是推荐值。
const (
	DefaultHostGrace = 60 * time.Second
	MaxHostGrace     = 10 * time.Minute
)

// 分发树深度上限（PR_MAX_DEPTH）。
//
// DefaultRoomMaxDepth = 3：产品目标为「延迟与卡顿优先」。每跳中继实测增加 58–78ms
// 端到端延迟（docs/ALGORITHM.md §2.1），4 跳最坏再叠约 300ms。
//
// MinRoomMaxDepth = 1 与 MaxRoomMaxDepth = 6 是**允许范围**，不是推荐值：
// 1 表示所有观众直连主播（只允许 K0 ≥ 人数时才成立，超出的人会进 Unassigned）；
// 6 之上单链退化（SPEC §6.2 硬约束 3）的延迟已经比"降码率 / 减少人数"更贵。
const (
	DefaultRoomMaxDepth = 3
	MinRoomMaxDepth     = 1
	MaxRoomMaxDepth     = 6
)

// Default 返回面向本机开发的默认配置。
// 注意：服务端不传输任何视频字节，这里的 DefaultStreamBps 只用于容量预判。
func Default() *Config {
	return &Config{
		Addr: "127.0.0.1:8080",
		Room: RoomConfig{
			MaxMembers:       16,
			DefaultStreamBps: 2_000_000,
			SafetyFactor:     0.8,
			HostGrace:        DefaultHostGrace,
			MaxDepth:         DefaultRoomMaxDepth,
		},
		Signal: SignalConfig{
			WriteTimeout: 10 * time.Second,
			PingInterval: 20 * time.Second,
			// 单条信令上限：索引是**一条**消息，大小随分片数线性增长。
			// 实测：142 分钟视频按 2s 切片 = 5087 片 → index.json 1.39MB；
			// 原来的 256KiB 会让主播一发布索引就被 1009 掐断 → Leave → 房间销毁 →
			// 后来的人看到"房间不存在"（既不是房间满，也不是编码问题）。
			// 4MiB 覆盖约 14000 片（2s/片 ≈ 7.8 小时）；再长的片子应改成紧凑索引/分批下发，
			// 而不是继续抬高这个值（抬高等于放宽 DoS 面）。
			MaxMessageBytes: 4 * 1024 * 1024,
			MaxChatLen:      500,
			SendQueueSize:   32,
			// 本机开发时页面在 Vite 5173、/ws 在 Go 8080（跨源）；
			// 单端口部署与隧道域名天然同源，不需要在这里列。
			AllowedOrigins: []string{"127.0.0.1:5173", "localhost:5173"},
		},
		ICE: ICEConfig{
			STUNURLs:      DefaultSTUNURLs(),
			MaxSTUN:       DefaultICEMaxSTUN,
			ProbeInterval: DefaultICEProbeInterval,
			TTL:           DefaultICETTL,
		},
		Segment: SegmentConfig{
			Concurrency:            2,
			QueueLength:            8,
			RatePerMinute:          3,
			Burst:                  3,
			TTL:                    30 * time.Minute,
			CleanupInterval:        time.Minute,
			SingleResponseMaxBytes: 1 << 30, // 1 GiB
			MaxDuration:            60 * time.Minute,
			MaxSourceBytes:         16 << 30, // 16 GiB
			SegmentSeconds:         2,
			// 每 100 片合成一个 pack-*.bin：Windows 上写 1800 个小文件要好几分钟，
			// 打包后产物文件数降到 ~18（细节见 docs/SEGMENT.md §6）。
			PackSize: DefaultPackSize,
		},
		Static: StaticConfig{
			// 默认托管 client/dist：单端口部署是默认形态，开发态用的是 Vite dev server，不冲突。
			Serve: true,
			Dir:   "client/dist",
		},
		// 留空 = 跟随静态根目录：切片器二进制与前端产物放在同一棵树里，
		// 因此 /downloads/<file> 的 URL 与磁盘上的 client/dist/downloads/<file> 天然一致。
		Downloads: DownloadsConfig{},
		LogLevel:  "info",
	}
}

// Load 在 Default 基础上应用环境变量覆盖。
// 非法取值直接返回错误，不做静默回退（配置错误必须立刻可见）。
func Load() (*Config, error) {
	cfg := Default()

	if v := os.Getenv(envAddr); v != "" {
		cfg.Addr = v
	}
	if v := os.Getenv(envMaxMembers); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 2 {
			return nil, fmt.Errorf("config: %s 必须是 >=2 的整数, got %q", envMaxMembers, v)
		}
		cfg.Room.MaxMembers = n
	}
	if v := os.Getenv(envDefaultStreamBps); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("config: %s 必须是正整数, got %q", envDefaultStreamBps, v)
		}
		cfg.Room.DefaultStreamBps = n
	}
	// 额外允许的 WebSocket 来源（逗号分隔，支持 *.example.com）。
	// 同源（页面与 /ws 同端口）始终放行，所以这一项主要是给"前后端分离开发"或
	// 需要从别的域名嵌页面进来的场景用。
	if v := os.Getenv(envAllowedOrigins); v != "" {
		cfg.Signal.AllowedOrigins = splitList(v)
	}

	if err := applyICEEnv(&cfg.ICE); err != nil {
		return nil, err
	}

	if err := applySegmentEnv(&cfg.Segment); err != nil {
		return nil, err
	}

	if err := applyRoomEnv(&cfg.Room); err != nil {
		return nil, err
	}

	if err := applyStaticEnv(&cfg.Static); err != nil {
		return nil, err
	}

	// 切片器二进制目录：与静态目录一样**不校验是否存在**，
	// 没跑过 scripts/build-segmenter.ps1 时端点返回空清单（200），不是启动失败。
	if v := os.Getenv(envDownloadsDir); v != "" {
		cfg.Downloads.Dir = v
	}

	return cfg, nil
}

// DownloadsDir 返回切片器二进制的实际发布目录。
//
// Downloads.Dir 为空即「跟随静态根目录」：默认态下磁盘布局是 client/dist/downloads/，
// 静态托管正好把它映射到 /downloads/*，因此清单里的 url 常量不需要任何额外配置。
func (c *Config) DownloadsDir() string {
	if c.Downloads.Dir != "" {
		return c.Downloads.Dir
	}
	return filepath.Join(c.Static.Dir, "downloads")
}

// applyStaticEnv 应用 PR_STATIC_DIR / PR_SERVE_STATIC 覆盖。
//
// 这里**故意不校验目录是否存在**：目录缺失是运行期可降级的情况
// （未构建前端时 go run ./cmd 仍要能起 API 与 /ws），
// 真正需要报错的是开关值本身写错（例如 PR_SERVE_STATIC=maybe）。
func applyStaticEnv(sc *StaticConfig) error {
	if v := os.Getenv(envServeStatic); v != "" {
		b, err := parseBool(envServeStatic, v)
		if err != nil {
			return err
		}
		sc.Serve = b
	}
	if v := os.Getenv(envStaticDir); v != "" {
		sc.Dir = v
	}
	return nil
}

// applyRoomEnv 应用房间生命周期的环境变量覆盖（PR_ROOM_HOST_GRACE、PR_MAX_DEPTH）。
// 与其它配置一致：非法取值直接报错，不做静默回退 —— 宽限期写错会让"主播掉线"
// 要么等于立刻销毁房间（0），要么等于房间几乎不回收（>10m），都必须立刻可见；
// 深度写错则会让"延迟预算"或"房间容量"悄悄换一个量级。
func applyRoomEnv(rc *RoomConfig) error {
	if v := os.Getenv(envRoomHostGrace); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("config: %s 必须是 Go 时长（如 45s、2m）, got %q", envRoomHostGrace, v)
		}
		if d <= 0 || d > MaxHostGrace {
			return fmt.Errorf("config: %s 必须落在 (0, %s] 区间, got %q", envRoomHostGrace, MaxHostGrace, v)
		}
		rc.HostGrace = d
	}
	if v := os.Getenv(envRoomMaxDepth); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < MinRoomMaxDepth || n > MaxRoomMaxDepth {
			return fmt.Errorf("config: %s 必须是 [%d,%d] 区间内的整数, got %q",
				envRoomMaxDepth, MinRoomMaxDepth, MaxRoomMaxDepth, v)
		}
		rc.MaxDepth = n
	}
	return nil
}

// parseBool 解析开关型环境变量，非法取值直接报错（与其它配置一致，不静默回退）。
func parseBool(name, v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("config: %s 必须是布尔值（1/0/true/false/yes/no/on/off）, got %q", name, v)
	}
}

// applySegmentEnv 应用 PR_SEGMENT_* / PR_FFMPEG 覆盖。
// 与其它配置一致：非法取值直接报错，不做静默回退。
func applySegmentEnv(sc *SegmentConfig) error {
	if v := os.Getenv(envSegmentConcurrency); v != "" {
		n, err := positiveInt(envSegmentConcurrency, v)
		if err != nil {
			return err
		}
		sc.Concurrency = n
	}
	if v := os.Getenv(envSegmentQueueLength); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return fmt.Errorf("config: %s 必须是 >=0 的整数, got %q", envSegmentQueueLength, v)
		}
		sc.QueueLength = n
	}
	if v := os.Getenv(envSegmentRatePerMinute); v != "" {
		n, err := positiveInt(envSegmentRatePerMinute, v)
		if err != nil {
			return err
		}
		sc.RatePerMinute = n
	}
	if v := os.Getenv(envSegmentBurst); v != "" {
		n, err := positiveInt(envSegmentBurst, v)
		if err != nil {
			return err
		}
		sc.Burst = n
	}
	if v := os.Getenv(envSegmentTTL); v != "" {
		d, err := positiveDuration(envSegmentTTL, v)
		if err != nil {
			return err
		}
		sc.TTL = d
	}
	if v := os.Getenv(envSegmentCleanup); v != "" {
		d, err := positiveDuration(envSegmentCleanup, v)
		if err != nil {
			return err
		}
		sc.CleanupInterval = d
	}
	if v := os.Getenv(envSegmentSingleMax); v != "" {
		n, err := positiveInt64(envSegmentSingleMax, v)
		if err != nil {
			return err
		}
		sc.SingleResponseMaxBytes = n
	}
	if v := os.Getenv(envSegmentMaxDuration); v != "" {
		d, err := positiveDuration(envSegmentMaxDuration, v)
		if err != nil {
			return err
		}
		sc.MaxDuration = d
	}
	if v := os.Getenv(envSegmentMaxSource); v != "" {
		n, err := positiveInt64(envSegmentMaxSource, v)
		if err != nil {
			return err
		}
		sc.MaxSourceBytes = n
	}
	if v := os.Getenv(envSegmentSeconds); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f <= 0 {
			return fmt.Errorf("config: %s 必须是正数, got %q", envSegmentSeconds, v)
		}
		sc.SegmentSeconds = f
	}
	if v := os.Getenv(envSegmentPackSize); v != "" {
		// 1 是"不打包"：逐片一个文件，与打包功能出现之前一致。
		n, err := positiveInt(envSegmentPackSize, v)
		if err != nil {
			return err
		}
		sc.PackSize = n
	}
	if v := os.Getenv(envSegmentTempDir); v != "" {
		sc.TempDir = v
	}
	if v := os.Getenv(envFFmpeg); v != "" {
		sc.FFmpegPath = v
	}
	return nil
}

func positiveInt(name, v string) (int, error) {
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("config: %s 必须是正整数, got %q", name, v)
	}
	return n, nil
}

func positiveInt64(name, v string) (int64, error) {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("config: %s 必须是正整数, got %q", name, v)
	}
	return n, nil
}

func positiveDuration(name, v string) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("config: %s 必须是正的时长（如 30m）, got %q", name, v)
	}
	return d, nil
}

// applyICEEnv 应用 ICE 下发的环境变量覆盖。
// 与其它配置一致：非法取值直接报错，不做静默回退 —— 把 MaxSTUN 写成 0、把 TTL 写成
// "5"（漏了单位）都会让下发内容悄悄退化，必须在启动时立刻可见。
//
// 注意这里**没有 TURN**：TURN 字段与 PR_TURN_* 已随退役一并删除
// （见 README「为什么不再有 TURN」）。设置 PR_TURN_* 现在是无声无息的空操作。
func applyICEEnv(ic *ICEConfig) error {
	if v := os.Getenv(envSTUNURLs); v != "" {
		list := splitList(v)
		if len(list) == 0 {
			return fmt.Errorf("config: %s 不能解析出任何 URL, got %q", envSTUNURLs, v)
		}
		ic.STUNURLs = uniqueList(list)
	}
	if v := os.Getenv(envICEMaxSTUN); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return fmt.Errorf("config: %s 必须是 >=1 的整数, got %q", envICEMaxSTUN, v)
		}
		ic.MaxSTUN = n
	}
	if v := os.Getenv(envICEProbeInterval); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("config: %s 必须是 Go 时长（如 60s、2m）, got %q", envICEProbeInterval, v)
		}
		if d <= 0 || d > MaxICEProbeInterval {
			return fmt.Errorf("config: %s 必须落在 (0, %s] 区间, got %q", envICEProbeInterval, MaxICEProbeInterval, v)
		}
		ic.ProbeInterval = d
	}
	if v := os.Getenv(envICETTL); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("config: %s 必须是 Go 时长（如 300s、5m）, got %q", envICETTL, v)
		}
		if d <= 0 || d > MaxICETTL {
			return fmt.Errorf("config: %s 必须落在 (0, %s] 区间, got %q", envICETTL, MaxICETTL, v)
		}
		ic.TTL = d
	}
	return nil
}

// uniqueList 去掉重复项并保持首次出现的顺序。
// 为什么要去重：重复的 STUN 会在 iceServers 里出现两条一模一样的条目，
// 浏览器会白跑一轮重复的候选收集。
func uniqueList(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func splitList(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
