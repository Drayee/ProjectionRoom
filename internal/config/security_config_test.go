package config

import (
	"strings"
	"testing"
	"time"
)

// TestSecurityDefaults 锁定 S-1 / S-3 / S-9 / S-11 / F-9 的默认值与依据。
func TestSecurityDefaults(t *testing.T) {
	cfg := Default()

	// S-1 解码前预算。
	if cfg.Signal.MaxRepeatedElements != 16384 || DefaultSignalMaxRepeatedElements != 16384 {
		t.Fatalf("整帧元素预算默认应为 16384，实际 %d（常量 %d）",
			cfg.Signal.MaxRepeatedElements, DefaultSignalMaxRepeatedElements)
	}
	if cfg.Signal.MaxMembersPerMessage != 256 {
		t.Fatalf("members 单字段上限默认应为 256，实际 %d", cfg.Signal.MaxMembersPerMessage)
	}
	if cfg.Signal.MaxSegmentsPerIndex != 8192 {
		t.Fatalf("segments 上限默认应为 8192，实际 %d", cfg.Signal.MaxSegmentsPerIndex)
	}
	if cfg.Signal.MaxFieldBytes != 1<<20 {
		t.Fatalf("单字段字节上限默认应为 1 MiB，实际 %d", cfg.Signal.MaxFieldBytes)
	}
	if cfg.Signal.MaxRateBytesPerSec != 256<<10 {
		t.Fatalf("每连接速率配额默认应为 256 KiB/s，实际 %d", cfg.Signal.MaxRateBytesPerSec)
	}
	if cfg.Signal.RateBucketBytes != 512<<10 {
		t.Fatalf("速率桶容量默认应为 512 KiB，实际 %d", cfg.Signal.RateBucketBytes)
	}
	if cfg.MemoryLimitBytes != 512<<20 {
		t.Fatalf("GOMEMLIMIT 默认应为 512 MiB，实际 %d", cfg.MemoryLimitBytes)
	}

	// S-3 建房闸门与回收。
	if cfg.Room.MaxRooms != 256 {
		t.Fatalf("房间总数上限默认应为 256，实际 %d", cfg.Room.MaxRooms)
	}
	if cfg.Room.UnclaimedRoomTTL != 10*time.Minute {
		t.Fatalf("空置房间 TTL 默认应为 10m，实际 %v", cfg.Room.UnclaimedRoomTTL)
	}
	if cfg.Room.HostGraceZeroTTL != 30*time.Minute {
		t.Fatalf("兜底回收阈值默认应为 30m，实际 %v", cfg.Room.HostGraceZeroTTL)
	}
	if cfg.Room.SweepInterval != time.Minute {
		t.Fatalf("清扫周期默认应为 1m，实际 %v", cfg.Room.SweepInterval)
	}
	if cfg.IPC.CreatePerMinute != 20 || cfg.IPC.CreateBurst != 10 {
		t.Fatalf("建房限速默认应为 20/分钟、容量 10，实际 %v/%d", cfg.IPC.CreatePerMinute, cfg.IPC.CreateBurst)
	}
	if cfg.IPC.JoinFailPerMinute != 30 || cfg.IPC.JoinFailBurst != 30 {
		t.Fatalf("join 失败限速默认应为 30/分钟、容量 30，实际 %v/%d", cfg.IPC.JoinFailPerMinute, cfg.IPC.JoinFailBurst)
	}

	// S-9 metrics 节流。
	if cfg.Room.MetricsMinInterval != 1500*time.Millisecond {
		t.Fatalf("metrics 最小间隔默认应为 1500ms，实际 %v", cfg.Room.MetricsMinInterval)
	}
	if cfg.Room.MetricsSignificantRatio != 0.1 {
		t.Fatalf("显著变化阈值默认应为 0.1，实际 %v", cfg.Room.MetricsSignificantRatio)
	}

	// F-9 安全头。
	if !cfg.Security.Headers {
		t.Fatal("安全响应头默认应当开启")
	}
	for _, want := range []string{
		"style-src 'self' 'unsafe-inline'", // Vue 运行时注入内联 style
		"media-src 'self' blob:",           // MSE 用 blob URL
		"script-src 'self'",                // 现有产物没有内联脚本
		"frame-ancestors 'none'",
		"base-uri 'none'",
	} {
		if !strings.Contains(cfg.Security.CSP, want) {
			t.Fatalf("默认 CSP 缺少 %q：%q", want, cfg.Security.CSP)
		}
	}
}

// TestSignalBudgetEnvOverride 锁定 S-1 的预算都能被环境变量覆盖。
func TestSignalBudgetEnvOverride(t *testing.T) {
	t.Setenv(envSignalMaxRepeatedElements, "5000")
	t.Setenv(envSignalMaxMembers, "64")
	t.Setenv(envSignalMaxSegments, "1000")
	t.Setenv(envSignalMaxFieldBytes, "4096")
	t.Setenv(envSignalMaxRate, "1024")
	t.Setenv(envSignalRateBurst, "2048")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() 不应失败: %v", err)
	}
	if cfg.Signal.MaxRepeatedElements != 5000 {
		t.Fatalf("PR_SIGNAL_MAX_REPEATED_ELEMENTS 覆盖失败: %d", cfg.Signal.MaxRepeatedElements)
	}
	if cfg.Signal.MaxMembersPerMessage != 64 {
		t.Fatalf("PR_SIGNAL_MAX_MEMBERS 覆盖失败: %d", cfg.Signal.MaxMembersPerMessage)
	}
	if cfg.Signal.MaxSegmentsPerIndex != 1000 {
		t.Fatalf("PR_SIGNAL_MAX_SEGMENTS 覆盖失败: %d", cfg.Signal.MaxSegmentsPerIndex)
	}
	if cfg.Signal.MaxFieldBytes != 4096 {
		t.Fatalf("PR_SIGNAL_MAX_FIELD_BYTES 覆盖失败: %d", cfg.Signal.MaxFieldBytes)
	}
	if cfg.Signal.MaxRateBytesPerSec != 1024 || cfg.Signal.RateBucketBytes != 2048 {
		t.Fatalf("速率配额覆盖失败: %d/%d", cfg.Signal.MaxRateBytesPerSec, cfg.Signal.RateBucketBytes)
	}
}

// TestSignalBudgetEnvRejectsInvalid 锁定非法预算必须报错：
// 预算写错等于把审计实证的 OOM 路径重新打开，绝不能静默回退。
func TestSignalBudgetEnvRejectsInvalid(t *testing.T) {
	cases := []struct{ name, value string }{
		{envSignalMaxRepeatedElements, "0"},
		{envSignalMaxRepeatedElements, "-1"},
		{envSignalMaxRepeatedElements, "abc"},
		{envSignalMaxRepeatedElements, "5000000"}, // 超过红线 4,000,000
		{envSignalMaxMembers, "0"},
		{envSignalMaxMembers, "999999"},
		{envSignalMaxSegments, "0"},
		{envSignalMaxFieldBytes, "10"},        // 小于 1 KiB 下限
		{envSignalMaxFieldBytes, "999999999"}, // 超过 16 MiB
		{envSignalMaxRate, "-1"},
		{envSignalRateBurst, "10"}, // 小于 1 KiB
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

// TestRoomLifecycleEnvOverride 锁定 S-3 的房间上限与三个 TTL、清扫周期。
func TestRoomLifecycleEnvOverride(t *testing.T) {
	t.Setenv(envMaxRooms, "42")
	t.Setenv(envUnclaimedRoomTTL, "90s")
	t.Setenv(envEmptyRoomTTL, "45s")
	t.Setenv(envRoomSweepInterval, "500ms")
	t.Setenv(envRoomCreatePerMinute, "7.5")
	t.Setenv(envRoomCreateBurst, "3")
	t.Setenv(envJoinFailPerMinute, "12")
	t.Setenv(envJoinFailBurst, "4")
	t.Setenv(envMetricsMinInterval, "250ms")
	t.Setenv(envMetricsSignificantRatio, "0.25")
	t.Setenv(envMemoryLimit, "268435456")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() 不应失败: %v", err)
	}
	if cfg.Room.MaxRooms != 42 {
		t.Fatalf("PR_MAX_ROOMS 覆盖失败: %d", cfg.Room.MaxRooms)
	}
	if cfg.Room.UnclaimedRoomTTL != 90*time.Second || cfg.Room.HostGraceZeroTTL != 45*time.Second {
		t.Fatalf("回收 TTL 覆盖失败: %v / %v", cfg.Room.UnclaimedRoomTTL, cfg.Room.HostGraceZeroTTL)
	}
	if cfg.Room.SweepInterval != 500*time.Millisecond {
		t.Fatalf("清扫周期覆盖失败: %v", cfg.Room.SweepInterval)
	}
	if cfg.IPC.CreatePerMinute != 7.5 || cfg.IPC.CreateBurst != 3 {
		t.Fatalf("建房限速覆盖失败: %v/%d", cfg.IPC.CreatePerMinute, cfg.IPC.CreateBurst)
	}
	if cfg.IPC.JoinFailPerMinute != 12 || cfg.IPC.JoinFailBurst != 4 {
		t.Fatalf("join 失败限速覆盖失败: %v/%d", cfg.IPC.JoinFailPerMinute, cfg.IPC.JoinFailBurst)
	}
	if cfg.Room.MetricsMinInterval != 250*time.Millisecond || cfg.Room.MetricsSignificantRatio != 0.25 {
		t.Fatalf("metrics 节流覆盖失败: %v / %v", cfg.Room.MetricsMinInterval, cfg.Room.MetricsSignificantRatio)
	}
	if cfg.MemoryLimitBytes != 256<<20 {
		t.Fatalf("PR_MEMORY_LIMIT 覆盖失败: %d", cfg.MemoryLimitBytes)
	}
}

// TestRoomLifecycleEnvRejectsInvalid 锁定非法值必须报错。
func TestRoomLifecycleEnvRejectsInvalid(t *testing.T) {
	cases := []struct{ name, value string }{
		{envMaxRooms, "0"},
		{envMaxRooms, "-3"},
		{envMaxRooms, "abc"},
		{envMaxRooms, "99999999"}, // 超过 65536
		{envUnclaimedRoomTTL, "0"},
		{envUnclaimedRoomTTL, "0s"},
		{envUnclaimedRoomTTL, "-1m"},
		{envUnclaimedRoomTTL, "25h"}, // 超过 24h
		{envUnclaimedRoomTTL, "10"},  // 缺少单位
		{envEmptyRoomTTL, "0s"},
		{envRoomSweepInterval, "10ms"}, // 小于 100ms 下限
		{envRoomSweepInterval, "2h"},   // 超过 1h
		{envRoomCreatePerMinute, "0"},
		{envRoomCreatePerMinute, "-1"},
		{envRoomCreateBurst, "0"},
		{envJoinFailPerMinute, "0"},
		{envJoinFailBurst, "-1"},
		{envMetricsMinInterval, "-1s"},
		{envMetricsSignificantRatio, "0"},
		{envMetricsSignificantRatio, "2"},
		{envMemoryLimit, "0"},
		{envMemoryLimit, "1MiB"},   // 小于 16 MiB 下限
		{envMemoryLimit, "128GiB"}, // 超过 64 GiB
		{envMemoryLimit, "abc"},
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

// TestSecurityEnvOverride 锁定 F-9 的开关与 CSP 覆盖。
func TestSecurityEnvOverride(t *testing.T) {
	t.Setenv(envSecurityHeaders, "0")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() 不应失败: %v", err)
	}
	if cfg.Security.Headers {
		t.Fatal("PR_SECURITY_HEADERS=0 应当关闭安全头")
	}

	t.Setenv(envSecurityHeaders, "1")
	t.Setenv(envSecurityCSP, "default-src 'none'")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load() 不应失败: %v", err)
	}
	if !cfg.Security.Headers || cfg.Security.CSP != "default-src 'none'" {
		t.Fatalf("CSP 覆盖失败: %v / %q", cfg.Security.Headers, cfg.Security.CSP)
	}

	// 空 CSP 必须报错（要关就关整个开关，避免"静默没有 CSP"）。
	t.Setenv(envSecurityCSP, "   ")
	if _, err := Load(); err == nil {
		t.Fatal("空的 PR_SECURITY_CSP 应当报错")
	}
	t.Setenv(envSecurityCSP, "default-src 'none'")
	t.Setenv(envSecurityHeaders, "maybe")
	if _, err := Load(); err == nil {
		t.Fatal("PR_SECURITY_HEADERS=maybe 应当报错")
	}
}

// TestSignalRateZeroDisables 锁定"0 表示关闭速率闸"（排障用）。
func TestSignalRateZeroDisables(t *testing.T) {
	t.Setenv(envSignalMaxRate, "0")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("PR_SIGNAL_MAX_RATE_BYTES=0 应当合法（表示关闭）: %v", err)
	}
	if cfg.Signal.MaxRateBytesPerSec != 0 {
		t.Fatalf("速率闸应当被关闭，实际 %d", cfg.Signal.MaxRateBytesPerSec)
	}
}

// TestMaxMembersMustFitFrameBudget 锁定交叉校验：帧预算必须放得下配置允许的最大房间。
//
// 为什么这条校验值得单测：这两个变量单独看都合法，组合起来却会让
// **满员房间的成员表被自己的预算拒掉**（房间越大越容易突然断线，日志里只有
// 一条"帧预算超限"）。一个安静的配置组合错误比一个崩溃更难排查。
func TestMaxMembersMustFitFrameBudget(t *testing.T) {
	t.Run("默认组合合法", func(t *testing.T) {
		if _, err := Load(); err != nil {
			t.Fatalf("默认 16 <= 256 应当合法: %v", err)
		}
	})

	t.Run("MaxMembers 超过预算则报错", func(t *testing.T) {
		t.Setenv(envMaxMembers, "300") // 预算默认 256
		_, err := Load()
		if err == nil {
			t.Fatal("PR_MAX_MEMBERS=300 > PR_SIGNAL_MAX_MEMBERS=256 应当报错")
		}
		if !strings.Contains(err.Error(), "PR_SIGNAL_MAX_MEMBERS") {
			t.Fatalf("错误信息必须点名要一起改的变量，实际 %q", err.Error())
		}
	})

	t.Run("同时抬高两个变量则合法", func(t *testing.T) {
		t.Setenv(envMaxMembers, "300")
		t.Setenv(envSignalMaxMembers, "300")
		if _, err := Load(); err != nil {
			t.Fatalf("两个变量一起抬高应当合法: %v", err)
		}
	})
}
