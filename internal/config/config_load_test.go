package config

import "testing"

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
