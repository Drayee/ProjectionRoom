package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	envAddr             = "PR_ADDR"
	envMaxMembers       = "PR_MAX_MEMBERS"
	envDefaultStreamBps = "PR_DEFAULT_STREAM_BPS"
	envSTUNURLs         = "PR_STUN_URLS"
	envTURNURLs         = "PR_TURN_URLS"
	envTURNUser         = "PR_TURN_USER"
	envTURNPass         = "PR_TURN_PASS"

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
)

// DefaultPackSize 是分片打包的默认粒度（每个 .bin 容纳多少片）。
//
// 它在这里而不是在 segment 包里，是因为"每包多少片"是一个配置项，而 config 不能反向
// import segment（queue.go 依赖 config，会形成 import 循环）。segment 与 cmd/segmenter
// 都引用这个常量，因此默认值只有一处定义。
const DefaultPackSize = 100

// Default 返回面向本机开发的默认配置。
// 注意：服务端不传输任何视频字节，这里的 DefaultStreamBps 只用于容量预判。
func Default() *Config {
	return &Config{
		Addr: "127.0.0.1:8080",
		Room: RoomConfig{
			MaxMembers:       16,
			DefaultStreamBps: 2_000_000,
			SafetyFactor:     0.8,
		},
		Signal: SignalConfig{
			WriteTimeout:    10 * time.Second,
			PingInterval:    20 * time.Second,
			MaxMessageBytes: 256 * 1024,
			MaxChatLen:      500,
			SendQueueSize:   32,
		},
		ICE: ICEConfig{
			STUNURLs: []string{"stun:stun.l.google.com:19302"},
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
		LogLevel: "info",
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
	if v := os.Getenv(envSTUNURLs); v != "" {
		cfg.ICE.STUNURLs = splitList(v)
	}
	if v := os.Getenv(envTURNURLs); v != "" {
		cfg.ICE.TURNURLs = splitList(v)
	}
	if v := os.Getenv(envTURNUser); v != "" {
		cfg.ICE.TURNUser = v
	}
	if v := os.Getenv(envTURNPass); v != "" {
		cfg.ICE.TURNPass = v
	}

	if err := applySegmentEnv(&cfg.Segment); err != nil {
		return nil, err
	}

	if err := applyStaticEnv(&cfg.Static); err != nil {
		return nil, err
	}

	return cfg, nil
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

// ICEServers 把配置转换成 WebRTC 的 iceServers 结构（供前端直接使用）。
func (c *Config) ICEServers() []map[string]any {
	out := make([]map[string]any, 0, len(c.ICE.STUNURLs)+len(c.ICE.TURNURLs))
	for _, u := range c.ICE.STUNURLs {
		out = append(out, map[string]any{"urls": u})
	}
	for _, u := range c.ICE.TURNURLs {
		entry := map[string]any{"urls": u}
		if c.ICE.TURNUser != "" {
			entry["username"] = c.ICE.TURNUser
			entry["credential"] = c.ICE.TURNPass
		}
		out = append(out, entry)
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
