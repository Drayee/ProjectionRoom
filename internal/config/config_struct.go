package config

import "time"

// Config 是 ProjectionRoom 的运行时配置。
// 默认值面向本机开发（SPEC §1.3：STUN/TURN/HTTPS 仅预留）。
type Config struct {
	Addr     string
	Room     RoomConfig
	Signal   SignalConfig
	ICE      ICEConfig
	Segment  SegmentConfig
	LogLevel string
}

// RoomConfig 控制房间规模与生命周期。
type RoomConfig struct {
	// MaxMembers 是房间成员上限的硬约束（含主播）。
	// P2P 分发的真实容量上限由 internal/usecase 依据实测上行计算（SPEC §6.2），
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

// SegmentConfig 控制「服务端视频切片」的配额、校验与生命周期。
//
// 它为什么**不违反不变量 I1（服务器不传输任何视频字节）**：
// 这条链路是**一次性预处理**，与直播链路完全分离。用户手上没有 ffmpeg 时，
// 把源视频上传到服务器切成放映室可用的分片，再一次性下载回本地；
// 开播之后分片仍然只在 peer 之间经 WebRTC DataChannel 流动，
// 服务器在直播期一个视频字节都不经手（SPEC §1.1 目标 7、ALGORITHM §0 I1）。
// 换句话说：上传/下载发生在"准备媒体"阶段，而不是"分发媒体"阶段。
type SegmentConfig struct {
	// Concurrency 是同时处于 running 状态的作业上限。
	Concurrency int
	// QueueLength 是排队队列长度（不含正在运行的作业）。
	// 队列已满时新请求直接被拒（HTTP 429），而不是无限堆积。
	QueueLength int
	// RatePerMinute 是令牌桶的匀速补充速率（作业/分钟）。
	RatePerMinute int
	// Burst 是令牌桶容量，即突发情况下最多能连续接几个作业。
	Burst int
	// TTL 是产物与作业记录的保留时间，过期后连同临时目录一起被后台清理。
	TTL time.Duration
	// CleanupInterval 是后台清理扫描的周期。
	CleanupInterval time.Duration
	// SingleResponseMaxBytes 是"单次返回"允许的产物总大小。
	// 产物超过它就必须走 manifest + 分批下载，且每一份都小于该值。
	SingleResponseMaxBytes int64
	// MaxDuration 是源视频时长上限（用 ffprobe 探测，超限返回 422）。
	MaxDuration time.Duration
	// MaxSourceBytes 是上传源文件的大小上限（超限返回 413）。
	MaxSourceBytes int64
	// SegmentSeconds 是分片目标时长，与 cmd/segmenter 的 -frag-sec 语义一致。
	SegmentSeconds float64
	// TempDir 是作业临时目录的根；为空时落在 os.TempDir()/projectionroom-segment。
	TempDir string
	// FFmpegPath 是 PR_FFMPEG 指定的 ffmpeg 路径（目录或可执行文件）。
	// 为空时按 exec.LookPath → Windows 常见安装目录 的顺序探测。
	FFmpegPath string
}

// ICEConfig 预留 STUN/TURN 配置，M1 只透传给前端，不参与信令逻辑。
type ICEConfig struct {
	STUNURLs []string
	TURNURLs []string
	TURNUser string
	TURNPass string
}
