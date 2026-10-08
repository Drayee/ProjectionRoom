package handler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ProjectionRoom/internal/config"
)

// TestValidateStaticDirRejectsDangerousConfigs 覆盖 S-13 的启动期校验。
//
// 审计风险：把 PR_STATIC_DIR 配成 "/" 或进程工作目录，服务就会把整个仓库
// （含 .env 里的数据库密码、源码、.git）当成静态文件公开。
// 这类配置错误在启动期一定能判定，因此必须"拒绝启动"而不是记一条 WARN。
func TestValidateStaticDirRejectsDangerousConfigs(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("获取工作目录失败: %v", err)
	}

	t.Run("文件系统根目录", func(t *testing.T) {
		cfg := config.Default()
		cfg.Static.Serve = true
		cfg.Static.Dir = string(filepath.Separator)
		if err := ValidateStaticDir(cfg); err == nil {
			t.Fatal("静态根配成文件系统根必须被拒（整个盘会被公开）")
		}
	})

	t.Run("等于进程工作目录", func(t *testing.T) {
		cfg := config.Default()
		cfg.Static.Dir = wd
		if err := ValidateStaticDir(cfg); err == nil {
			t.Fatal("静态根等于工作目录必须被拒（.env 与源码会被公开）")
		}
	})

	t.Run("包含工作目录", func(t *testing.T) {
		cfg := config.Default()
		cfg.Static.Dir = filepath.Dir(wd) // 工作目录的父目录：工作目录在托管树里
		if err := ValidateStaticDir(cfg); err == nil {
			t.Fatal("静态根包含工作目录必须被拒")
		}
	})

	t.Run("含 .env", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, ".env"), "DB_PASSWORD=super-secret")
		writeFile(t, filepath.Join(dir, "index.html"), "<!doctype html>")

		cfg := config.Default()
		cfg.Static.Dir = dir
		err := ValidateStaticDir(cfg)
		if err == nil {
			t.Fatal("静态根里有 .env 必须被拒（它会被直接下载）")
		}
		if !strings.Contains(err.Error(), ".env") {
			t.Fatalf("错误信息必须点名 .env，实际 %q", err.Error())
		}
	})
}

// TestValidateStaticDirAcceptsSafeConfigs 锁定"合法配置不能被误拒"：
// 默认的 client/dist 正是"工作目录的子目录"，它必须通过。
func TestValidateStaticDirAcceptsSafeConfigs(t *testing.T) {
	t.Run("默认 client/dist", func(t *testing.T) {
		cfg := config.Default()
		if err := ValidateStaticDir(cfg); err != nil {
			t.Fatalf("默认配置必须通过校验: %v", err)
		}
	})

	t.Run("工作目录下的临时子目录", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "dist")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("建目录失败: %v", err)
		}
		cfg := config.Default()
		cfg.Static.Dir = dir
		if err := ValidateStaticDir(cfg); err != nil {
			t.Fatalf("普通子目录必须通过: %v", err)
		}
	})

	t.Run("目录不存在（未构建前端）", func(t *testing.T) {
		cfg := config.Default()
		cfg.Static.Dir = "client/dist-not-built-yet"
		if err := ValidateStaticDir(cfg); err != nil {
			t.Fatalf("目录不存在只是运行期降级，不该在校验阶段被拒: %v", err)
		}
	})

	t.Run("关闭静态托管", func(t *testing.T) {
		cfg := config.Default()
		cfg.Static.Serve = false
		cfg.Static.Dir = string(filepath.Separator)
		if err := ValidateStaticDir(cfg); err != nil {
			t.Fatalf("关闭托管时不必校验目录: %v", err)
		}
	})

	t.Run("空目录", func(t *testing.T) {
		cfg := config.Default()
		cfg.Static.Dir = ""
		if err := ValidateStaticDir(cfg); err != nil {
			t.Fatalf("空目录表示跳过托管，不该报错: %v", err)
		}
	})
}
