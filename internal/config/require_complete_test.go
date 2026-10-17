package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 「必需配置不完整就拒绝启动」这道闸的用例。
//
// 它锁定的是一条**策略**：本实例要求功能完整，不做运行期降级。
// 早先相反的策略（缺 DB 就关账号、缺 SMTP 就让找回 503、缺前端产物就 404）
// 在这里被显式取代 —— 因此这组用例必须逐条断言"缺什么就报什么"，
// 而不是只断言"报错了"：拒绝启动的全部价值在于错误信息说清该补哪一项。

// completeEnv 把一份**完整**的必需配置灌进环境（用于"应当通过"的基线）。
func completeEnv(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html>"), 0o600); err != nil {
		t.Fatalf("造前端产物失败：%v", err)
	}
	t.Setenv(envRequireComplete, "1")
	t.Setenv(envDBDSN, "host=127.0.0.1 port=5432 user=u dbname=d sslmode=disable")
	t.Setenv(envJWTSecret, testJWTSecret)
	t.Setenv(envPublicBaseURL, "https://pr.example.com")
	t.Setenv(envSMTPHost, "smtp.example.com")
	t.Setenv(envSMTPUser, "bot@example.com")
	t.Setenv(envSMTPPass, "secret")
	t.Setenv(envStaticDir, dir)
	t.Setenv(envServeStatic, "true")
	// dev echo 与 SMTP 互斥（装配层会拒），这里显式关掉。
	t.Setenv(envOTPDevEcho, "0")
}

func TestRequireCompletePassesWithFullConfig(t *testing.T) {
	completeEnv(t)
	if _, err := Load(); err != nil {
		t.Fatalf("配置齐全时不应失败：%v", err)
	}
}

func TestRequireCompleteRejectsEachMissingItem(t *testing.T) {
	cases := []struct {
		name    string
		clear   string
		wantMsg string
	}{
		{"缺数据库", envDBDSN, "PR_DB_DSN"},
		{"缺密钥", envJWTSecret, "PR_JWT_SECRET"},
		{"缺对外基地址", envPublicBaseURL, "PR_PUBLIC_BASE_URL"},
		{"缺 SMTP 主机", envSMTPHost, "PR_SMTP_HOST"},
		{"缺 SMTP 账号", envSMTPUser, "PR_SMTP_HOST"},
		{"缺 SMTP 口令", envSMTPPass, "PR_SMTP_HOST"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			completeEnv(t)
			unsetEnv(t, c.clear)
			_, err := Load()
			if err == nil {
				t.Fatalf("清空 %s 后应当拒绝启动", c.clear)
			}
			if !strings.Contains(err.Error(), c.wantMsg) {
				t.Fatalf("错误信息应当点明 %s，实际：%v", c.wantMsg, err)
			}
		})
	}
}

// TestRequireCompleteRejectsMissingStaticArtifacts 单独一条：
// 前端产物缺失是"功能缺失"的典型形态（页面开不出来），必须在启动期拦住。
// 前提是**开启了托管** —— 关掉托管是正当配置（前端另找地方），不属于漏配。
func TestRequireCompleteRejectsMissingStaticArtifacts(t *testing.T) {
	completeEnv(t)
	t.Setenv(envServeStatic, "true")
	t.Setenv(envStaticDir, filepath.Join(t.TempDir(), "empty"))
	_, err := Load()
	if err == nil {
		t.Fatal("前端产物目录不存在时应当拒绝启动")
	}
	if !strings.Contains(err.Error(), "前端产物目录") {
		t.Fatalf("错误信息应当点明前端产物，实际：%v", err)
	}
}

// TestRequireCompleteAllowsServeStaticOff 关闭静态托管是**正当配置**：
// 前端另找地方托管（开发态 Vite 5173）或只跑信令，不该被当成"漏配"。
func TestRequireCompleteAllowsServeStaticOff(t *testing.T) {
	completeEnv(t)
	t.Setenv(envServeStatic, "false")
	t.Setenv(envStaticDir, filepath.Join(t.TempDir(), "not-built"))
	if _, err := Load(); err != nil {
		t.Fatalf("PR_SERVE_STATIC=false 时不该因产物缺失而拒绝启动：%v", err)
	}
}

// TestRequireCompleteRejectsDirWithoutIndex 目录在但缺 index.html：
// 多半是构建中断，症状与"目录不存在"不同（前者更隐蔽），因此单独断言。
func TestRequireCompleteRejectsDirWithoutIndex(t *testing.T) {
	completeEnv(t)
	dir := t.TempDir() // 空目录：存在但没有 index.html
	t.Setenv(envStaticDir, dir)
	_, err := Load()
	if err == nil {
		t.Fatal("目录里没有 index.html 时应当拒绝启动")
	}
	if !strings.Contains(err.Error(), "index.html") {
		t.Fatalf("错误信息应当点明 index.html，实际：%v", err)
	}
}

// TestRequireCompleteAllowsDevEchoInsteadOfSMTP 本机开发的正当形态：
// 没有 SMTP，但显式打开 PR_OTP_DEV_ECHO=1（验证码回显在响应里）。
func TestRequireCompleteAllowsDevEchoInsteadOfSMTP(t *testing.T) {
	completeEnv(t)
	unsetEnv(t, envSMTPHost)
	unsetEnv(t, envSMTPUser)
	unsetEnv(t, envSMTPPass)
	t.Setenv(envOTPDevEcho, "1")
	if _, err := Load(); err != nil {
		t.Fatalf("dev echo 模式下缺 SMTP 应当允许启动：%v", err)
	}
}

// TestRequireCompleteEscapeHatch 显式跳过：功能不全的实例仍可启动。
func TestRequireCompleteEscapeHatch(t *testing.T) {
	clearAuthEnv(t) // 里面已把 PR_REQUIRE_COMPLETE 设为 0
	if _, err := Load(); err != nil {
		t.Fatalf("PR_REQUIRE_COMPLETE=0 时应当允许功能不全地启动：%v", err)
	}
}

// TestRequireCompleteReportsAllMissingItems 一次报全：
// 逐条修配置的人不该被"修一个、重启一次、再发现下一个"来回折腾。
func TestRequireCompleteReportsAllMissingItems(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv(envRequireComplete, "1")
	_, err := Load()
	if err == nil {
		t.Fatal("全缺时应当拒绝启动")
	}
	msg := err.Error()
	for _, want := range []string{"PR_DB_DSN", "PR_JWT_SECRET", "PR_PUBLIC_BASE_URL", "PR_SMTP_HOST"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("错误信息应当一次列出 %s，实际：%v", want, msg)
		}
	}
}
