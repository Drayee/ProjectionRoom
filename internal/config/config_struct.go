package config

import "time"

// Config 是 ProjectionRoom 的运行时配置。
// 默认值面向本机开发（SPEC §1.3：STUN/TURN/HTTPS 仅预留）。
type Config struct {
	Addr     string
	Room     RoomConfig
	Signal   SignalConfig
	ICE      ICEConfig
	LogLevel string
}

// RoomConfig 控制房间规模与生命周期。
type RoomConfig struct {
	// MaxMembers 是房间成员上限的硬约束（含主播）。
	// P2P 分发的真实容量上限由 internal/topology 依据实测上行计算（SPEC §6.2），
	// 这里是最后一道防线，避免房间被挂爆。
	MaxMembers int
	// DefaultStreamBps 是尚未拿到 mediaIndex 时的码率估计（bit/s），
	// 用于开播前的容量预判（SPEC §4.5、§6.2）。
	DefaultStreamBps int64
	// SafetyFactor 是容量计算时的上行安全系数（SPEC §6.1）。
	SafetyFactor float64
}

// SignalConfig 控制 WebSocket 信令层的超时与限额。
type SignalConfig struct {
	WriteTimeout    time.Duration
	PingInterval    time.Duration
	MaxMessageBytes int64
	MaxChatLen      int
	SendQueueSize   int
}

// ICEConfig 预留 STUN/TURN 配置，M1 只透传给前端，不参与信令逻辑。
type ICEConfig struct {
	STUNURLs []string
	TURNURLs []string
	TURNUser string
	TURNPass string
}
