package config

import "time"

// Config 是 ProjectionRoom 的运行时配置。
// 默认值面向本机开发（SPEC §1.3：STUN/TURN/HTTPS 仅预留）。
type Config struct {
	Addr      string
	Room      RoomConfig
	Signal    SignalConfig
	ICE       ICEConfig
	Segment   SegmentConfig
	Static    StaticConfig
	Downloads DownloadsConfig
	LogLevel  string
}

// StaticConfig 控制「单端口部署」：由 Go 服务端在同一个端口上托管前端构建产物。
//
// 为什么需要它：开发态是 Vite(5173) + Go(8080) 两个端口，用户要在内网/公网用就得暴露两个口。
// 前端只使用相对路径 /api 与 /ws，所以把 client/dist 交给 Go 托管之后，
// 一条隧道指向一个端口就能同时覆盖页面、API 与信令。
//
// 注意：这两个字段只描述"从哪个目录、要不要托管"，**不在这里校验目录是否存在**。
// 目录缺失/非法由 internal/handler 记 WARN 并降级（API 与 /ws 照常可用），不允许 fatal。
type StaticConfig struct {
	// Serve 是静态托管总开关，默认开（PR_SERVE_STATIC）。
	// 关掉之后服务端只提供 API 与 /ws，页面需要另找地方托管。
	Serve bool
	// Dir 是前端构建产物目录，默认 client/dist（PR_STATIC_DIR）。
	// 相对路径按进程工作目录解析，因此请在仓库根目录启动服务（go run ./cmd 即是）。
	Dir string
}

// DownloadsConfig 控制「客户端切片器二进制」的发布目录。
// 这些文件由 scripts/build-segmenter.ps1 交叉编译产出，端点只读地列出它们，
// 真正的下载由静态托管（/downloads/*）负责，走的是同一个目录。
type DownloadsConfig struct {
	// Dir 是下载目录，默认等于 <Static.Dir>/downloads（PR_DOWNLOADS_DIR 可覆盖）。
	// 为空即「跟随静态根目录」，这样 /downloads/<file> 的 URL 与磁盘布局天然一致。
	Dir string
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
	// HostGrace 是主播断线后房间的宽限期（PR_ROOM_HOST_GRACE，默认 60s）。
	//
	// 为什么需要它：WebSocket 断一次（网络抖动 / 刷新 / 服务端重启 / 半开连接）
	// 不代表主播离开。旧行为是"任何 WS 结束 → 立刻销毁房间"，于是主播自动重连
	// 只会拿到 ROOM_NOT_FOUND，房间码彻底作废、观众全掉。
	// 宽限期内房间只保留元数据（几 KB），主播凭同一房间码 + 密码重连即可恢复。
	HostGrace time.Duration
}

// SignalConfig 控制 WebSocket 信令层的超时与限额。
type SignalConfig struct {
	WriteTimeout    time.Duration
	PingInterval    time.Duration
	MaxMessageBytes int64
	MaxChatLen      int
	SendQueueSize   int
	// AllowedOrigins 是允许发起 WebSocket 的**额外**来源（host[:port]，支持 *.example.com）。
	//
	// 为什么需要它：单端口部署后页面与 /ws 同源，所以同源请求一律放行（见 ws.go 里把
	// 请求自身的 Host 也加进白名单）；而本机开发时页面在 Vite 5173、/ws 在 Go 8080，
	// 属于跨源，必须显式列出。默认值覆盖本机开发的两种情况。
	// 公网 http 隧道域名无需配置 —— 它天然同源。
	AllowedOrigins []string
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
	// PackSize 是每个 .bin 容纳的分片数，与 cmd/segmenter 的 -pack 语义一致。
	// 1 表示不打包（逐片一个 c*.m4s，与打包功能出现之前逐字节等价）。
	PackSize int
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
