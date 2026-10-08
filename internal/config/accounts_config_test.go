package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

// unsetEnv 真正删除一个环境变量（并在用例结束时恢复）。
//
// 为什么不能只用 t.Setenv(name, "")：那样是"变量存在、值为空"，
// 而本包对 PR_TRUSTED_PROXIES 刻意区分了这两种语义 ——
// LookupEnv 命中且为空 = 显式不信任任何代理；变量不存在 = 用默认回环白名单。
// 用 t.Setenv 清空会静默把用例挪到"显式空"那一支，等于没测"未设置"。
func unsetEnv(t *testing.T, name string) {
	t.Helper()
	old, had := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("清空环境变量 %s 失败：%v", name, err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(name, old)
			return
		}
		_ = os.Unsetenv(name)
	})
}

// clearAuthEnv 把账号层相关的环境变量真正清空。
//
// 为什么要显式清空而不是"不管"：Load() 会读进程环境，而开发机/CI 上可能存在
// 全局设置的 PR_* 变量（例如把 PR_DB_DSN 写进了系统环境）。那样这些用例会
// 随环境漂移 —— 在别人机器上红、在自己机器上绿。显式清空把用例钉在确定状态上。
func clearAuthEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		envDBDSN, envJWTSecret, envAccessTTL, envRefreshTTL, envBcryptCost, envWSTicketTTL,
		envTrustedProxies,
		envAuthLoginPerMinute, envAuthLoginBurst,
		envAuthRegisterPerMinute, envAuthRegisterBurst,
		envAuthRefreshPerMinute, envAuthRefreshBurst,
	} {
		unsetEnv(t, name)
	}
}

const testJWTSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestAuthDefaultsDisabled(t *testing.T) {
	clearAuthEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() 报错：%v", err)
	}
	if cfg.Auth.DBDSN != "" || cfg.Auth.JWTSecret != "" {
		t.Fatalf("默认应当是「账号关闭」（DBDSN/密钥为空），实际 %q / %q", cfg.Auth.DBDSN, cfg.Auth.JWTSecret)
	}
	if cfg.Auth.AccessTTL != DefaultAccessTTL {
		t.Errorf("AccessTTL 默认应为 %s，实际 %s", DefaultAccessTTL, cfg.Auth.AccessTTL)
	}
	if cfg.Auth.RefreshTTL != DefaultRefreshTTL {
		t.Errorf("RefreshTTL 默认应为 %s，实际 %s", DefaultRefreshTTL, cfg.Auth.RefreshTTL)
	}
	if cfg.Auth.BcryptCost != DefaultBcryptCost {
		t.Errorf("BcryptCost 默认应为 %d，实际 %d", DefaultBcryptCost, cfg.Auth.BcryptCost)
	}
	if cfg.Auth.WSTicketTTL != DefaultWSTicketTTL {
		t.Errorf("WSTicketTTL 默认应为 %s，实际 %s", DefaultWSTicketTTL, cfg.Auth.WSTicketTTL)
	}
	if cfg.Auth.HashingConcurrency != DefaultHashingConcurrency {
		t.Errorf("HashingConcurrency 默认应为 %d，实际 %d", DefaultHashingConcurrency, cfg.Auth.HashingConcurrency)
	}
	if cfg.Security.TrustedProxies == nil || len(cfg.Security.TrustedProxies) != 2 {
		t.Fatalf("TrustedProxies 默认应为 [127.0.0.1 ::1]，实际 %#v", cfg.Security.TrustedProxies)
	}
	if cfg.IPC.LoginPerMinute != DefaultAuthLoginPerMinute || cfg.IPC.LoginBurst != DefaultAuthLoginBurst {
		t.Errorf("登录限速默认应为 %v/%d，实际 %v/%d",
			DefaultAuthLoginPerMinute, DefaultAuthLoginBurst, cfg.IPC.LoginPerMinute, cfg.IPC.LoginBurst)
	}
}

// 这是本组用例里最重要的一条：缺密钥必须**拒绝启动**，而不是带着空密钥跑起来。
func TestAuthDSNWithoutSecretFails(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv(envDBDSN, "host=127.0.0.1 user=pr_app dbname=projectionroom")
	_, err := Load()
	if err == nil {
		t.Fatal("配置了 PR_DB_DSN 但缺少 PR_JWT_SECRET，Load() 应当报错")
	}
	if !strings.Contains(err.Error(), envJWTSecret) {
		t.Errorf("错误信息里应当点名 %s，实际：%v", envJWTSecret, err)
	}
}

func TestAuthShortSecretFails(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv(envDBDSN, "host=127.0.0.1 user=pr_app dbname=projectionroom")
	t.Setenv(envJWTSecret, "too-short")
	_, err := Load()
	if err == nil {
		t.Fatalf("密钥只有 %d 字节（阈值 %d），Load() 应当报错", len("too-short"), MinJWTSecretBytes)
	}
}

func TestAuthEnabledWithValidSecret(t *testing.T) {
	clearAuthEnv(t)
	const dsn = "host=127.0.0.1 port=5432 user=pr_app password=x dbname=projectionroom sslmode=disable"
	t.Setenv(envDBDSN, dsn)
	t.Setenv(envJWTSecret, testJWTSecret)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() 报错：%v", err)
	}
	if cfg.Auth.DBDSN != dsn {
		t.Errorf("DBDSN 未被采用：%q", cfg.Auth.DBDSN)
	}
	if cfg.Auth.JWTSecret != testJWTSecret {
		t.Error("JWTSecret 未被采用")
	}
}

// 密钥设置了但没配数据库：允许启动（账号能力仍关闭）。
// 这条是**刻意的**：部署时先放密钥、后开数据库是正常的运维顺序，
// 在这里报错会把"分两步上线"这条路堵死。
func TestAuthSecretWithoutDSNIsAllowed(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv(envJWTSecret, testJWTSecret)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("只配密钥不配数据库应当允许启动，实际报错：%v", err)
	}
	if cfg.Auth.DBDSN != "" {
		t.Errorf("账号能力应当仍为关闭，实际 DBDSN=%q", cfg.Auth.DBDSN)
	}
}

func TestAuthDurationAndCostBounds(t *testing.T) {
	cases := []struct {
		name    string
		env     string
		value   string
		wantErr bool
	}{
		{envAccessTTL, "PR_ACCESS_TTL", "30m", false},
		{envAccessTTL, "PR_ACCESS_TTL", "25h", true},
		{envAccessTTL, "PR_ACCESS_TTL", "0s", true},
		{envAccessTTL, "PR_ACCESS_TTL", "abc", true},
		{envRefreshTTL, "PR_REFRESH_TTL", "720h", false},
		{envRefreshTTL, "PR_REFRESH_TTL", "10m", true},  // 低于 1h 下限
		{envRefreshTTL, "PR_REFRESH_TTL", "400d", true}, // 超过 1 年上限
		{envWSTicketTTL, "PR_WS_TICKET_TTL", "45s", false},
		{envWSTicketTTL, "PR_WS_TICKET_TTL", "30m", true},
		{envBcryptCost, "PR_BCRYPT_COST", "11", false},
		{envBcryptCost, "PR_BCRYPT_COST", "9", true},
		{envBcryptCost, "PR_BCRYPT_COST", "15", true},
		{envBcryptCost, "PR_BCRYPT_COST", "twelve", true},
		{envAuthHashConcurrency, "PR_AUTH_HASH_CONCURRENCY", "1", false},
		{envAuthHashConcurrency, "PR_AUTH_HASH_CONCURRENCY", "64", false},
		{envAuthHashConcurrency, "PR_AUTH_HASH_CONCURRENCY", "0", true},
		{envAuthHashConcurrency, "PR_AUTH_HASH_CONCURRENCY", "65", true},
		{envAuthHashConcurrency, "PR_AUTH_HASH_CONCURRENCY", "four", true},
	}
	for _, tc := range cases {
		t.Run(tc.env+"="+tc.value, func(t *testing.T) {
			clearAuthEnv(t)
			t.Setenv(tc.env, tc.value)
			_, err := Load()
			if tc.wantErr && err == nil {
				t.Fatalf("期望报错，实际通过")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("期望通过，实际报错：%v", err)
			}
		})
	}
}

func TestAuthRateLimitsAppliedAndBounded(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv(envAuthLoginPerMinute, "3.5")
	t.Setenv(envAuthLoginBurst, "7")
	t.Setenv(envAuthRegisterPerMinute, "1")
	t.Setenv(envAuthRefreshBurst, "20")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() 报错：%v", err)
	}
	if cfg.IPC.LoginPerMinute != 3.5 || cfg.IPC.LoginBurst != 7 {
		t.Errorf("登录限速未生效：%v/%d", cfg.IPC.LoginPerMinute, cfg.IPC.LoginBurst)
	}
	if cfg.IPC.RegisterPerMinute != 1 {
		t.Errorf("注册限速未生效：%v", cfg.IPC.RegisterPerMinute)
	}
	if cfg.IPC.RefreshBurst != 20 {
		t.Errorf("刷新容量未生效：%d", cfg.IPC.RefreshBurst)
	}

	clearAuthEnv(t)
	t.Setenv(envAuthLoginPerMinute, "0")
	if _, err := Load(); err == nil {
		t.Error("PR_AUTH_LOGIN_PER_MINUTE=0 应当报错（范围下限 0.001）")
	}
	clearAuthEnv(t)
	t.Setenv(envAuthRegisterBurst, "0")
	if _, err := Load(); err == nil {
		t.Error("PR_AUTH_REGISTER_BURST=0 应当报错（下限 1）")
	}
}

// PR_TRUSTED_PROXIES 的三种语义必须可区分，否则"直连部署"与"反代部署"会用错口径。
func TestTrustedProxiesSemantics(t *testing.T) {
	t.Run("未设置=默认回环", func(t *testing.T) {
		clearAuthEnv(t)
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.Security.TrustedProxies) != 2 {
			t.Fatalf("期望默认两条，实际 %#v", cfg.Security.TrustedProxies)
		}
	})
	t.Run("显式列表=去重后采用", func(t *testing.T) {
		clearAuthEnv(t)
		// os.LookupEnv 需要变量"存在"；t.Setenv 设为非空以模拟真实配置。
		t.Setenv(envTrustedProxies, "10.0.0.1, 10.0.0.2 ,10.0.0.1")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.Security.TrustedProxies) != 2 {
			t.Fatalf("期望去重后两条，实际 %#v", cfg.Security.TrustedProxies)
		}
		if cfg.Security.TrustedProxies[0] != "10.0.0.1" || cfg.Security.TrustedProxies[1] != "10.0.0.2" {
			t.Fatalf("顺序应为首次出现顺序，实际 %#v", cfg.Security.TrustedProxies)
		}
	})
}

func TestAuthDefaultsAreSane(t *testing.T) {
	// 这条是"防手滑"的元测试：默认值之间的关系必须自洽，
	// 否则调参时很容易把 access 活得比 refresh 还久（那样刷新就没有意义了）。
	if DefaultAccessTTL >= DefaultRefreshTTL {
		t.Fatalf("access TTL (%s) 必须远小于 refresh TTL (%s)", DefaultAccessTTL, DefaultRefreshTTL)
	}
	if DefaultWSTicketTTL > time.Minute {
		t.Fatalf("WS 票据 TTL (%s) 应当在一分钟以内", DefaultWSTicketTTL)
	}
	if MinBcryptCost < 10 || DefaultBcryptCost < MinBcryptCost || DefaultBcryptCost > MaxBcryptCost {
		t.Fatalf("bcrypt 代价默认值 %d 不在 [%d,%d] 区间", DefaultBcryptCost, MinBcryptCost, MaxBcryptCost)
	}
}
