package service

import (
	"runtime/debug"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
)

// mustEngine 返回一个最小可用的 gin 引擎（本文件只验证 Server 的参数装配，
// 不涉及任何路由行为）。
func mustEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	return gin.New()
}

// TestNewServerAppliesMemoryLimit 覆盖 S-1 的兜底闸：GOMEMLIMIT 必须真的被设上。
//
// 为什么单测只断言"设上了"而不是"真的限制了"：debug.SetMemoryLimit 的语义是
// "接近上限时提高 GC 频率"，不会有可观测的报错；而断言"内存不会超"需要一个
// 会打到上限的负载（那正是它要防的东西）。所以这里锁定的是配置→运行时的那一跳。
func TestNewServerAppliesMemoryLimit(t *testing.T) {
	before := debug.SetMemoryLimit(-1) // 读取当前值（-1 表示"只读不设"）
	t.Cleanup(func() { debug.SetMemoryLimit(before) })

	cfg := config.Default()
	cfg.MemoryLimitBytes = 96 << 20 // 96 MiB，明显不同于默认 512 MiB

	if _, err := NewServer(cfg, mustEngine()); err != nil {
		t.Fatalf("构造服务失败: %v", err)
	}

	got := debug.SetMemoryLimit(-1) // 读回当前值
	if got != cfg.MemoryLimitBytes {
		t.Fatalf("GOMEMLIMIT 应为 %d，实际 %d", cfg.MemoryLimitBytes, got)
	}
}

// TestNewServerMemoryLimitCanBeDisabled 锁定"配 0 表示不限制"这条语义：
// 不改动进程已有的 GOMEMLIMIT（例如由 GOMEMLIMIT 环境变量设的）。
func TestNewServerMemoryLimitCanBeDisabled(t *testing.T) {
	baseline := debug.SetMemoryLimit(-1)
	t.Cleanup(func() { debug.SetMemoryLimit(baseline) })

	cfg := config.Default()
	cfg.MemoryLimitBytes = 0

	if _, err := NewServer(cfg, mustEngine()); err != nil {
		t.Fatalf("构造服务失败: %v", err)
	}

	if got := debug.SetMemoryLimit(-1); got != baseline {
		t.Fatalf("配置为 0 时不应当改动 GOMEMLIMIT（期望保持 %d，实际 %d）", baseline, got)
	}
}

// TestHTTPServerTimeouts 覆盖 S-15：超时参数必须都设上，且写超时有明确取舍。
func TestHTTPServerTimeouts(t *testing.T) {
	cfg := config.Default()
	srv, err := NewServer(cfg, mustEngine())
	if err != nil {
		t.Fatalf("构造服务失败: %v", err)
	}

	h := srv.http
	if h.ReadHeaderTimeout != 10*time.Second {
		t.Fatalf("ReadHeaderTimeout 应为 10s，实际 %v", h.ReadHeaderTimeout)
	}
	if h.IdleTimeout != httpIdleTimeout {
		t.Fatalf("IdleTimeout 应为 %v，实际 %v", httpIdleTimeout, h.IdleTimeout)
	}
	if h.MaxHeaderBytes != httpMaxHeaderBytes {
		t.Fatalf("MaxHeaderBytes 应为 %d，实际 %d", httpMaxHeaderBytes, h.MaxHeaderBytes)
	}
	// WriteTimeout 有意保持 0：流式产物下载（单产物上限 1 GiB）在慢链路上
	// 几分钟是正常的，统一写超时只有"大到没保护"或"小到误杀长下载"两种结果。
	if h.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout 应当保持 0（逐路由收紧的取舍），实际 %v", h.WriteTimeout)
	}
}

// TestHumanBytes 锁定日志用的字节数格式化（它出现在启动日志里，别写错量级）。
func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{512 << 20, "512 MiB"},
		{1 << 30, "1.0 GiB"},
		{2 << 30, "2.0 GiB"},
		{1024, "1024 字节"},
	}
	for _, tc := range cases {
		if got := humanBytes(tc.in); got != tc.want {
			t.Fatalf("humanBytes(%d) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}
