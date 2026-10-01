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

	return cfg, nil
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
