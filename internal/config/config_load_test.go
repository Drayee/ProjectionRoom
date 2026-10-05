package config

import (
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
