package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	envAddr             = "PR_ADDR"
	envMaxMembers       = "PR_MAX_MEMBERS"
	envDefaultStreamBps = "PR_DEFAULT_STREAM_BPS"
	// envRoomHostGrace 是主播断线宽限期，接受 45s / 2m 这类 Go 时长字符串。
	envRoomHostGrace = "PR_ROOM_HOST_GRACE"
	// envRoomMaxDepth 是分发树深度上限。
	envRoomMaxDepth   = "PR_MAX_DEPTH"
	envSTUNURLs       = "PR_STUN_URLS"
	envAllowedOrigins = "PR_ALLOWED_ORIGINS"

	// ICE 下发的三件套：最多给几条、多久重探一次、载荷能用多久。
	envICEMaxSTUN       = "PR_ICE_MAX_STUN"
	envICEProbeInterval = "PR_ICE_PROBE_INTERVAL"
	envICETTL           = "PR_ICE_TTL"

	// 服务端切片服务的环境变量（前缀 PR_SEGMENT_，ffmpeg 路径单独用 PR_FFMPEG）。
	envSegmentConcurrency   = "PR_SEGMENT_CONCURRENCY"
	envSegmentQueueLength   = "PR_SEGMENT_QUEUE_LENGTH"
	envSegmentRatePerMinute = "PR_SEGMENT_RATE_PER_MINUTE"
	envSegmentBurst         = "PR_SEGMENT_BURST"
	envSegmentTTL           = "PR_SEGMENT_TTL"
	envSegmentCleanup       = "PR_SEGMENT_CLEANUP_INTERVAL"
	envSegmentSingleMax     = "PR_SEGMENT_SINGLE_MAX_BYTES"
	envSegmentMaxDuration   = "PR_SEGMENT_MAX_DURATION"
	envSegmentMaxSource     = "PR_SEGMENT_MAX_SOURCE_BYTES"
	envSegmentSeconds       = "PR_SEGMENT_SECONDS"
	envSegmentPackSize      = "PR_SEGMENT_PACK_SIZE"
	envSegmentTempDir       = "PR_SEGMENT_TEMP_DIR"
	envFFmpeg               = "PR_FFMPEG"

	// 单端口部署：静态资源托管（前端构建产物）。
	envStaticDir   = "PR_STATIC_DIR"
	envServeStatic = "PR_SERVE_STATIC"

	// 客户端切片器二进制的发布目录；留空即跟随 <PR_STATIC_DIR>/downloads。
	envDownloadsDir = "PR_DOWNLOADS_DIR"

	// —— S-1：解码前的 proto 预算（见 internal/model/wire.go）。
	// 每个都是"元素/字节"量级的上限，非法值直接报错（预算写错等于闸门失效）。
	envSignalMaxRepeatedElements = "PR_SIGNAL_MAX_REPEATED_ELEMENTS"
	envSignalMaxMembers          = "PR_SIGNAL_MAX_MEMBERS"
	envSignalMaxSegments         = "PR_SIGNAL_MAX_SEGMENTS"
	envSignalMaxFieldBytes       = "PR_SIGNAL_MAX_FIELD_BYTES"
	// 每连接字节速率配额（第二道闸）。0 = 关闭。
	envSignalMaxRate   = "PR_SIGNAL_MAX_RATE_BYTES"
	envSignalRateBurst = "PR_SIGNAL_RATE_BUCKET_BYTES"

	// —— S-3：建房闸门与房间回收。
	envMaxRooms          = "PR_MAX_ROOMS"
	envUnclaimedRoomTTL  = "PR_ROOM_UNCLAIMED_TTL"
	envEmptyRoomTTL      = "PR_ROOM_EMPTY_TTL"
	envRoomSweepInterval = "PR_ROOM_SWEEP_INTERVAL"

	// —— S-3/S-11：每 IP 令牌桶。
	envRoomCreatePerMinute = "PR_ROOM_CREATE_PER_MINUTE"
	envRoomCreateBurst     = "PR_ROOM_CREATE_BURST"
	envJoinFailPerMinute   = "PR_JOIN_FAIL_PER_MINUTE"
	envJoinFailBurst       = "PR_JOIN_FAIL_BURST"

	// —— S-9：metrics 节流。
	envMetricsMinInterval      = "PR_METRICS_MIN_INTERVAL"
	envMetricsSignificantRatio = "PR_METRICS_SIGNIFICANT_RATIO"

	// —— F-9：安全响应头。
	envSecurityHeaders = "PR_SECURITY_HEADERS"
	envSecurityCSP     = "PR_SECURITY_CSP"

	// —— S-1 兜底：进程软内存上限。
	envMemoryLimit = "PR_MEMORY_LIMIT"

	// —— 账号层（ACCOUNTS §5）：数据库与凭据。
	envDBDSN       = "PR_DB_DSN"
	envJWTSecret   = "PR_JWT_SECRET"
	envAccessTTL   = "PR_ACCESS_TTL"
	envRefreshTTL  = "PR_REFRESH_TTL"
	envBcryptCost  = "PR_BCRYPT_COST"
	envWSTicketTTL = "PR_WS_TICKET_TTL"
	// envAuthHashConcurrency 是并发口令哈希闸门容量（见 AuthConfig.HashingConcurrency）。
	envAuthHashConcurrency = "PR_AUTH_HASH_CONCURRENCY"

	// —— 审计残留项 S2：只信任本机反代传来的转发头，避免伪造 XFF 绕过按 IP 限速。
	envTrustedProxies = "PR_TRUSTED_PROXIES"

	// —— 账号层的每 IP 令牌桶（沿用 IPCConfig 这个 owner，不再另建一套）。
	envAuthLoginPerMinute    = "PR_AUTH_LOGIN_PER_MINUTE"
	envAuthLoginBurst        = "PR_AUTH_LOGIN_BURST"
	envAuthRegisterPerMinute = "PR_AUTH_REGISTER_PER_MINUTE"
	envAuthRegisterBurst     = "PR_AUTH_REGISTER_BURST"
	envAuthRefreshPerMinute  = "PR_AUTH_REFRESH_PER_MINUTE"
	envAuthRefreshBurst      = "PR_AUTH_REFRESH_BURST"

	// —— 二期（T2）的公开房列表限速：同样并入 IPCConfig，理由见上面的说明。
	envPublicRoomsPerMinute = "PR_PUBLIC_ROOMS_PER_MINUTE"
	envPublicRoomsBurst     = "PR_PUBLIC_ROOMS_BURST"

	// —— 一期缺口的补齐：管理端日志端点的每 IP 限速。
	envAdminLogsPerMinute = "PR_ADMIN_LOGS_PER_MINUTE"
	envAdminLogsBurst     = "PR_ADMIN_LOGS_BURST"

	// —— 房主回读房间元数据的每 IP 限速（GET /api/rooms/:id/meta）。
	envRoomMetaPerMinute = "PR_ROOM_META_PER_MINUTE"
	envRoomMetaBurst     = "PR_ROOM_META_BURST"
)

// —— 账号层（ACCOUNTS §5）的默认值与允许范围。
const (
	// DefaultAccessTTL = 15m：access token 只存内存 + sessionStorage，
	// 泄漏窗口越短越好；15 分钟配合"刷新即轮换 + 重放检测"是常见折中。
	DefaultAccessTTL = 15 * time.Minute
	// DefaultRefreshTTL = 720h（30 天）：refresh 是不透明随机串、入库可撤销，
	// 因此可以长；它决定"多久不登录还能免密回来"。
	DefaultRefreshTTL = 30 * 24 * time.Hour
	// MinRefreshTTL / MaxRefreshTTL 是允许范围（1 小时 ~ 1 年）。
	MinRefreshTTL = time.Hour
	MaxRefreshTTL = 365 * 24 * time.Hour
	// DefaultBcryptCost = 12：低配 2 vCPU 上单次校验约 200ms，配合登录限速
	//（默认 10/分钟）仍余量充足；11 是更弱机器的退路，10 是下限。
	DefaultBcryptCost = 12
	MinBcryptCost     = 10
	MaxBcryptCost     = 14
	// DefaultWSTicketTTL = 30s：票据是"从取票到建立 WS"的时间预算，
	// 单次使用 + 30 秒过期，即便它出现在反代 access log 里也无法复用。
	DefaultWSTicketTTL = 30 * time.Second
	// DefaultHashingConcurrency = 4 是并发 bcrypt 的闸门容量（见 AuthConfig 的说明）。
	// 与 usecase.DefaultHashingConcurrency 同值：两处都要改的耦合由测试钉住。
	DefaultHashingConcurrency = 4
	MinHashingConcurrency     = 1
	MaxHashingConcurrency     = 64
	// MinJWTSecretBytes = 32：HS256 密钥必须有足够熵（openssl rand -hex 32）。
	MinJWTSecretBytes = 32
	// 登录/注册/刷新的每 IP 令牌桶默认值（依据见 IPCConfig 注释）。
	DefaultAuthLoginPerMinute    = 10
	DefaultAuthLoginBurst        = 5
	DefaultAuthRegisterPerMinute = 5
	DefaultAuthRegisterBurst     = 3
	DefaultAuthRefreshPerMinute  = 30
	DefaultAuthRefreshBurst      = 10
	// 公开房列表的每 IP 令牌桶默认值（依据见 IPCConfig.PublicRoomsPerMinute）。
	DefaultPublicRoomsPerMinute = 60
	DefaultPublicRoomsBurst     = 20
	// 管理端日志端点的每 IP 令牌桶默认值（依据见 IPCConfig.AdminLogsPerMinute）。
	DefaultAdminLogsPerMinute = 60
	DefaultAdminLogsBurst     = 10
	// 房主回读元数据的每 IP 令牌桶默认值（依据见 IPCConfig.RoomMetaPerMinute）。
	DefaultRoomMetaPerMinute = 60
	DefaultRoomMetaBurst     = 20
)

// DefaultPackSize 是分片打包的默认粒度（每个 .bin 容纳多少片）。
//
// 它在这里而不是在 segment 包里，是因为"每包多少片"是一个配置项，而 config 不能反向
// import segment（queue.go 依赖 config，会形成 import 循环）。segment 与 cmd/segmenter
// 都引用这个常量，因此默认值只有一处定义。
const DefaultPackSize = 100

// —— S-1 解码前预算的默认值与允许范围。
//
// 默认值逐条依据见 internal/model/wire.go 的 WireBudget 注释；这里只固定"数值本身"
// 与允许范围。范围上限（Max*）不是推荐值，而是"再往上就等于关掉这条闸门"的红线：
// 超限的配置必须显式写出来，避免有人在不知情的情况下把 DoS 面放大几个数量级。
const (
	// DefaultSignalMaxRepeatedElements = 16384：实测最大的 index.json 是 14000 片。
	DefaultSignalMaxRepeatedElements = 16384
	// MaxSignalMaxRepeatedElements = 4,000,000：再往上就等于让"4 MiB 帧 → 几十 GB 堆"
	// 成为可能（审计实证是 1,398,100 个元素），所以必须显式配置才允许。
	MaxSignalMaxRepeatedElements = 4_000_000
	// DefaultSignalMaxMembers = 256：房间硬上限默认 16，留 16 倍余量。
	DefaultSignalMaxMembers = 256
	// MaxSignalMaxMembers = 65536。
	MaxSignalMaxMembers = 65536
	// DefaultSignalMaxSegments = 8192：覆盖 2s/片 的 4.5 小时视频。
	DefaultSignalMaxSegments = 8192
	// MaxSignalMaxSegments = 1,000,000：24 天视频的索引，显式配置才允许。
	MaxSignalMaxSegments = 1_000_000
	// DefaultSignalMaxFieldBytes = 1 MiB：have 位图 838 万片 / SDP 几 KB。
	DefaultSignalMaxFieldBytes = 1 << 20
	// MaxSignalMaxFieldBytes = 16 MiB：单字段再大就等于没有单字段闸门。
	MaxSignalMaxFieldBytes = 16 << 20
	// DefaultSignalMaxRateBytes = 256 KiB/s：正常连接是几百字节每秒。
	DefaultSignalMaxRateBytes = 256 << 10
	// DefaultSignalRateBucketBytes = 512 KiB：突发上限，必须能容纳一条索引帧的常见大小。
	DefaultSignalRateBucketBytes = 512 << 10
	// MaxSignalRateBytes = 64 MiB/s：再高就等于关掉速率闸。
	MaxSignalRateBytes = 64 << 20
)

// —— S-3 / S-11 建房与 join 的默认闸门。
const (
	// DefaultMaxRooms = 256：每个房间常驻几 KB + 一个宽限定时器；
	// 审计实测"创建 400 个房间 RSS 只增不减"，这里给它一个上限。
	DefaultMaxRooms = 256
	// MaxMaxRooms = 65536：再往上等于放弃"总数上限"这条约束。
	MaxMaxRooms = 65536
	// DefaultUnclaimedRoomTTL = 10m：从"拿到房间码"到"主播点进房"的人机交互时间。
	DefaultUnclaimedRoomTTL = 10 * time.Minute
	// MaxUnclaimedRoomTTL = 24h。
	MaxUnclaimedRoomTTL = 24 * time.Hour
	// DefaultEmptyRoomTTL = 30m：兜底回收（无成员且不在宽限期），给足够长的误杀余量。
	DefaultEmptyRoomTTL = 30 * time.Minute
	// MaxEmptyRoomTTL = 24h。
	MaxEmptyRoomTTL = 24 * time.Hour
	// DefaultRoomSweepInterval = 1m：清扫是纯内存遍历（房间数上限 256），成本可忽略。
	DefaultRoomSweepInterval = time.Minute
	// MinRoomSweepInterval = 100ms（测试需要把周期压到很短）。
	MinRoomSweepInterval = 100 * time.Millisecond
	// MaxRoomSweepInterval = 1h。
	MaxRoomSweepInterval = time.Hour

	// DefaultRoomCreatePerMinute / DefaultRoomCreateBurst：每 IP 建房令牌桶。
	DefaultRoomCreatePerMinute = 20
	DefaultRoomCreateBurst     = 10
	// MaxRoomCreatePerMinute = 100000：再高等于关掉限速。
	MaxRoomCreatePerMinute = 100_000
	// DefaultJoinFailPerMinute / DefaultJoinFailBurst：join 失败的每 IP+房间码令牌桶。
	//
	// 容量 30 而不是 5，是因为建错房间/打错密码的正常用户会在同一分钟里连续试几次，
	// 而 6 位房间码的暴力猜测需要 10^4 量级次；30/分钟 已经让那条路不可行。
	DefaultJoinFailPerMinute = 30
	DefaultJoinFailBurst     = 30

	// DefaultMetricsMinInterval = 1500ms，与 usecase.reassignMinInterval（拓扑重算节流）
	// 取同一个量级：客户端本来 5s 上报一次，这个闸门只针对"连发"；
	// 而"秒级连续两次显著变化"里被丢掉的中间态本身没有观测价值
	//（下一次显著变化会立刻把容量与拓扑修正到最终值）。
	DefaultMetricsMinInterval = 1500 * time.Millisecond
	// DefaultMetricsSignificantRatio = 0.1（10%）。
	DefaultMetricsSignificantRatio = 0.1
	// MinMetricsSignificantRatio / MaxMetricsSignificantRatio。
	MinMetricsSignificantRatio = 0.01
	MaxMetricsSignificantRatio = 1.0

	// DefaultMemoryLimitBytes = 512 MiB：进程软内存上限（debug.SetMemoryLimit）。
	//
	// 这是**兜底**，不是替代前面的预算：预算挡的是"已知形态的一帧放大"，
	// 这一条挡的是"某个没想到的路径一直在长"。512 MiB 的依据：
	// 正常态 RSS 约 20–40 MB（审计实测 22 MB），切片作业会额外占 I/O 缓冲
	// （走的是文件流，不是堆），512 MiB 给了 >10 倍余量，同时把
	// "一帧把机器打死"变成"GC 变忙 + 拒绝（如果还超）"。
	DefaultMemoryLimitBytes = 512 << 20
	// MaxMemoryLimitBytes = 64 GiB。
	MaxMemoryLimitBytes = 64 << 30
)

// DefaultSecurityCSP 是全站 CSP 的默认内容（F-9）。
//
// 逐条说明为什么这么写（改错会直接弄坏页面）：
//
//	default-src 'self'        兜底：一切未显式列出的资源类型只允许同源
//	script-src 'self'          现有 client/dist 没有内联脚本（index.html 只有 <script type=module src=…>）
//	style-src 'self' 'unsafe-inline'  **必须**留 unsafe-inline：Vue 运行时按组件把 <style> 注入文档
//	connect-src 'self' ws: wss: 同源 fetch + WebSocket（ws: 保留给"页面 http、隧道 https"的混用）
//	img-src 'self' data:       SVG/小图标走 data URI
//	media-src 'self' blob:     **必须**留 blob:：MSE 用 URL.createObjectURL(MediaSource) 播放
//	worker-src 'self' blob:    保险：打包器将来引入 worker 时不至于被 CSP 掐掉
//	frame-ancestors 'none'     禁止被任何页面 frame（配合 X-Frame-Options: DENY）
//	base-uri 'none'            禁止 <base> 改写相对 URL 解析
//	form-action 'self'         表单只能提交到同源
const DefaultSecurityCSP = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"connect-src 'self' ws: wss:; " +
	"img-src 'self' data:; " +
	"media-src 'self' blob:; " +
	"worker-src 'self' blob:; " +
	"frame-ancestors 'none'; " +
	"base-uri 'none'; " +
	"form-action 'self'"

// ICE 下发的默认值。
//
// 为什么 TTL 是 300s 而探测周期是 60s：TTL 不是"数据保鲜期"，而是"客户端该多久
// 重新问一次服务端"。它比探测周期大几倍，是为了让客户端不必踩着探测窗口刷新；
// 同时又足够短，使一次网络路径变化（换网关、切运营商）能在几分钟内反映到新房间里。
const (
	// DefaultICEMaxSTUN 是单次下发的 STUN 条数上限。
	//
	// 为什么是 5 而不是 4：恒选席位是「实测最快的 2 条 + 本轮健康的粘性 2 条
	// （stun.l.google.com / stun.cloudflare.com）」，上限 4 会让恒选席位吃满全部名额、
	// 轮询只剩 0 席 —— 等于把"低分项/暂时抖动的条目靠轮换回升"这条设计抹掉。
	// 要同时容下最快两条与粘性两条、再留出轮询席位，下限就是 5。
	// （要改回 4 只需改这一个常量：改完轮询席位最多 1 个、常见情况下是 0 个。）
	DefaultICEMaxSTUN = 5
	// DefaultICEProbeInterval 是服务端重新探测一轮的周期。
	DefaultICEProbeInterval = 60 * time.Second
	// DefaultICETTL 是下发载荷的默认有效期。
	DefaultICETTL = 300 * time.Second
	// MaxICETTL 是 TTL 的上限：超过 1h 就等于"配了之后再也不用刷新"，
	// 一旦某台 STUN 长期劣化，房间里的浏览器会一直拿着它。
	MaxICETTL = time.Hour
	// MaxICEProbeInterval 是探测周期的上限：探测本身很便宜（7 个 UDP 包），
	// 周期长到超过 1h 就失去了"跟着网络路径变化走"的意义。
	MaxICEProbeInterval = time.Hour
)

// DefaultSTUNURLs 返回默认的 STUN 列表，**顺序即默认优先级**。
//
// 返回新切片而不是共享变量：调用方（registry）会持有并可能改动它，
// 共享一份全局切片迟早会被某个调用方就地改坏。
//
// 列表构成（2026-02 本机实测 UDP Binding Request 往返）：
//
//	stun.douyucdn.cn:18000      11ms    ✓
//	stun.hitv.com:3478          32ms    ✓
//	stun.chat.bilibili.com:3478 46ms    ✓
//	stun.miwifi.com:3478        119ms   ✓
//	stun.cloudflare.com:3478    203ms   ✓  （有 AAAA，粘性）
//	stun.l.google.com:19302     236ms ↔ 4s 超时（时好时坏，粘性）
//	stun.qq.com:3478            4s 无响应
//
// 最后一条刻意留着：它由服务端探测打分自然沉底/落选，而不是靠人工"把它删掉"。
// 这样它对所有人都保留一个可观察的样本（probe.scores 里能看到它 ok=false），
// 一旦哪天它恢复了，权重轮询会自动把它带回下发列表。
func DefaultSTUNURLs() []string {
	return []string{
		"stun:stun.douyucdn.cn:18000",
		"stun:stun.hitv.com:3478",
		"stun:stun.chat.bilibili.com:3478",
		"stun:stun.miwifi.com:3478",
		"stun:stun.cloudflare.com:3478",
		"stun:stun.l.google.com:19302",
		"stun:stun.qq.com:3478",
	}
}

// 主播断线宽限期（PR_ROOM_HOST_GRACE）。
//
// DefaultHostGrace 取 60s：客户端 WS 重连计划通常是"指数退避、上限 30s 内"，
// 60s 足够覆盖一次网络抖动、页面刷新，也覆盖半开连接被 ping/写超时收尸的最坏情况
// （service 层 20s ping + 10s 写超时 ≈ 最长 30s 才判定断线）。
//
// MaxHostGrace 取 10m：宽限期越长，"主播其实已经走了但房间（含房间码）还占着"的窗口越久。
// 10 分钟是这个取舍的上限，不是推荐值。
const (
	DefaultHostGrace = 60 * time.Second
	MaxHostGrace     = 10 * time.Minute
)

// 分发树深度上限（PR_MAX_DEPTH）。
//
// DefaultRoomMaxDepth = 3：产品目标为「延迟与卡顿优先」。每跳中继实测增加 58–78ms
// 端到端延迟（docs/ALGORITHM.md §2.1），4 跳最坏再叠约 300ms。
//
// MinRoomMaxDepth = 1 与 MaxRoomMaxDepth = 6 是**允许范围**，不是推荐值：
// 1 表示所有观众直连主播（只允许 K0 ≥ 人数时才成立，超出的人会进 Unassigned）；
// 6 之上单链退化（SPEC §6.2 硬约束 3）的延迟已经比"降码率 / 减少人数"更贵。
const (
	DefaultRoomMaxDepth = 3
	MinRoomMaxDepth     = 1
	MaxRoomMaxDepth     = 6
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
			HostGrace:        DefaultHostGrace,
			MaxDepth:         DefaultRoomMaxDepth,

			MaxRooms:                DefaultMaxRooms,
			UnclaimedRoomTTL:        DefaultUnclaimedRoomTTL,
			HostGraceZeroTTL:        DefaultEmptyRoomTTL,
			SweepInterval:           DefaultRoomSweepInterval,
			MetricsMinInterval:      DefaultMetricsMinInterval,
			MetricsSignificantRatio: DefaultMetricsSignificantRatio,
		},
		IPC: IPCConfig{
			CreatePerMinute:   DefaultRoomCreatePerMinute,
			CreateBurst:       DefaultRoomCreateBurst,
			JoinFailPerMinute: DefaultJoinFailPerMinute,
			JoinFailBurst:     DefaultJoinFailBurst,

			LoginPerMinute:    DefaultAuthLoginPerMinute,
			LoginBurst:        DefaultAuthLoginBurst,
			RegisterPerMinute: DefaultAuthRegisterPerMinute,
			RegisterBurst:     DefaultAuthRegisterBurst,
			RefreshPerMinute:  DefaultAuthRefreshPerMinute,
			RefreshBurst:      DefaultAuthRefreshBurst,

			PublicRoomsPerMinute: DefaultPublicRoomsPerMinute,
			PublicRoomsBurst:     DefaultPublicRoomsBurst,

			AdminLogsPerMinute: DefaultAdminLogsPerMinute,
			AdminLogsBurst:     DefaultAdminLogsBurst,

			RoomMetaPerMinute: DefaultRoomMetaPerMinute,
			RoomMetaBurst:     DefaultRoomMetaBurst,
		},
		Security: SecurityConfig{
			Headers: true,
			CSP:     DefaultSecurityCSP,
			// 默认只信任回环：本部署的形态是"nginx 在 127.0.0.1 上反代"。
			// 多层代理需要显式把它加进 PR_TRUSTED_PROXIES。
			TrustedProxies: []string{"127.0.0.1", "::1"},
		},
		// 账号能力默认**关闭**（DBDSN 为空）：这样"先部署代码、再开启账号"是安全的，
		// 也让所有既有测试与脚本在未配置数据库时保持原行为。
		Auth: AuthConfig{
			AccessTTL:          DefaultAccessTTL,
			RefreshTTL:         DefaultRefreshTTL,
			BcryptCost:         DefaultBcryptCost,
			WSTicketTTL:        DefaultWSTicketTTL,
			HashingConcurrency: DefaultHashingConcurrency,
		},
		MemoryLimitBytes: DefaultMemoryLimitBytes,
		Signal: SignalConfig{
			WriteTimeout: 10 * time.Second,
			PingInterval: 20 * time.Second,
			// 单条信令上限：索引是**一条**消息，大小随分片数线性增长。
			// 实测：142 分钟视频按 2s 切片 = 5087 片 → index.json 1.39MB；
			// 原来的 256KiB 会让主播一发布索引就被 1009 掐断 → Leave → 房间销毁 →
			// 后来的人看到"房间不存在"（既不是房间满，也不是编码问题）。
			// 4MiB 覆盖约 14000 片（2s/片 ≈ 7.8 小时）；再长的片子应改成紧凑索引/分批下发，
			// 而不是继续抬高这个值（抬高等于放宽 DoS 面）。
			MaxMessageBytes: 4 * 1024 * 1024,
			MaxChatLen:      500,
			SendQueueSize:   32,
			// 本机开发时页面在 Vite 5173、/ws 在 Go 8080（跨源）；
			// 单端口部署与隧道域名不再"自动同源"（S-5 删掉了请求 Host 兜底），
			// 所以部署到隧道时要把那个域名显式加进 PR_ALLOWED_ORIGINS。
			AllowedOrigins: []string{"127.0.0.1:5173", "localhost:5173"},

			MaxRepeatedElements:  DefaultSignalMaxRepeatedElements,
			MaxMembersPerMessage: DefaultSignalMaxMembers,
			MaxSegmentsPerIndex:  DefaultSignalMaxSegments,
			MaxFieldBytes:        DefaultSignalMaxFieldBytes,
			MaxRateBytesPerSec:   DefaultSignalMaxRateBytes,
			RateBucketBytes:      DefaultSignalRateBucketBytes,
		},
		ICE: ICEConfig{
			STUNURLs:      DefaultSTUNURLs(),
			MaxSTUN:       DefaultICEMaxSTUN,
			ProbeInterval: DefaultICEProbeInterval,
			TTL:           DefaultICETTL,
		},
		Segment: SegmentConfig{
			Concurrency:            2,
			QueueLength:            8,
			RatePerMinute:          3,
			Burst:                  3,
			TTL:                    30 * time.Minute,
			CleanupInterval:        time.Minute,
			SingleResponseMaxBytes: 1 << 30, // 1 GiB
			MaxDuration:            60 * time.Minute,
			MaxSourceBytes:         16 << 30, // 16 GiB
			SegmentSeconds:         2,
			// 每 100 片合成一个 pack-*.bin：Windows 上写 1800 个小文件要好几分钟，
			// 打包后产物文件数降到 ~18（细节见 docs/SEGMENT.md §6）。
			PackSize: DefaultPackSize,
		},
		Static: StaticConfig{
			// 默认托管 client/dist：单端口部署是默认形态，开发态用的是 Vite dev server，不冲突。
			Serve: true,
			Dir:   "client/dist",
		},
		// 留空 = 跟随静态根目录：切片器二进制与前端产物放在同一棵树里，
		// 因此 /downloads/<file> 的 URL 与磁盘上的 client/dist/downloads/<file> 天然一致。
		Downloads: DownloadsConfig{},
		LogLevel:  "info",
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
	// 额外允许的 WebSocket 来源（逗号分隔，支持 *.example.com）。
	// 它现在是**唯一**的来源白名单（同源不再隐式放行，见 S-5）。
	if v := os.Getenv(envAllowedOrigins); v != "" {
		cfg.Signal.AllowedOrigins = splitList(v)
	}

	if err := applySignalEnv(&cfg.Signal); err != nil {
		return nil, err
	}

	if err := applyIPCEnv(&cfg.IPC); err != nil {
		return nil, err
	}

	if err := applySecurityEnv(&cfg.Security); err != nil {
		return nil, err
	}

	if err := applyAuthEnv(&cfg.Auth); err != nil {
		return nil, err
	}

	if v := os.Getenv(envMemoryLimit); v != "" {
		n, err := positiveInt64(envMemoryLimit, v)
		if err != nil {
			return nil, err
		}
		if n < 16<<20 || n > MaxMemoryLimitBytes {
			return nil, fmt.Errorf("config: %s 必须落在 [16MiB, %d] 区间（字节数）, got %q",
				envMemoryLimit, int64(MaxMemoryLimitBytes), v)
		}
		cfg.MemoryLimitBytes = n
	}

	// 交叉校验：帧预算必须放得下"配置允许的最大房间"。
	//
	// 为什么必须显式报错（而不是各自独立校验）：这两个变量分别属于"协议预算"与
	// "业务容量"，单独看都合法，但组合起来会让**满员房间的成员表被自己的预算拒掉** ——
	// 症状是"房间越大越容易突然断线"，而且日志里只有一条"帧预算超限"，极难归因。
	// 例：PR_MAX_MEMBERS=300 而 PR_SIGNAL_MAX_MEMBERS 保持默认 256 →
	// 第 257 个成员进房后的 member-list 广播会被服务端拒绝。
	if cfg.Room.MaxMembers > cfg.Signal.MaxMembersPerMessage {
		return nil, fmt.Errorf(
			"config: PR_MAX_MEMBERS=%d 大于 PR_SIGNAL_MAX_MEMBERS=%d："+
				"满员房间的成员表会被帧预算拒绝（表现为房间越大越容易断线）。"+
				"请把 PR_SIGNAL_MAX_MEMBERS 抬到 >= PR_MAX_MEMBERS",
			cfg.Room.MaxMembers, cfg.Signal.MaxMembersPerMessage)
	}

	if err := applyICEEnv(&cfg.ICE); err != nil {
		return nil, err
	}

	if err := applySegmentEnv(&cfg.Segment); err != nil {
		return nil, err
	}

	if err := applyRoomEnv(&cfg.Room); err != nil {
		return nil, err
	}

	if err := applyStaticEnv(&cfg.Static); err != nil {
		return nil, err
	}

	// 切片器二进制目录：与静态目录一样**不校验是否存在**，
	// 没跑过 scripts/build-segmenter.ps1 时端点返回空清单（200），不是启动失败。
	if v := os.Getenv(envDownloadsDir); v != "" {
		cfg.Downloads.Dir = v
	}

	return cfg, nil
}

// DownloadsDir 返回切片器二进制的实际发布目录。
//
// Downloads.Dir 为空即「跟随静态根目录」：默认态下磁盘布局是 client/dist/downloads/，
// 静态托管正好把它映射到 /downloads/*，因此清单里的 url 常量不需要任何额外配置。
func (c *Config) DownloadsDir() string {
	if c.Downloads.Dir != "" {
		return c.Downloads.Dir
	}
	return filepath.Join(c.Static.Dir, "downloads")
}

// applyStaticEnv 应用 PR_STATIC_DIR / PR_SERVE_STATIC 覆盖。
//
// 这里**故意不校验目录是否存在**：目录缺失是运行期可降级的情况
// （未构建前端时 go run ./cmd 仍要能起 API 与 /ws），
// 真正需要报错的是开关值本身写错（例如 PR_SERVE_STATIC=maybe）。
func applyStaticEnv(sc *StaticConfig) error {
	if v := os.Getenv(envServeStatic); v != "" {
		b, err := parseBool(envServeStatic, v)
		if err != nil {
			return err
		}
		sc.Serve = b
	}
	if v := os.Getenv(envStaticDir); v != "" {
		sc.Dir = v
	}
	return nil
}

// applyRoomEnv 应用房间生命周期的环境变量覆盖（PR_ROOM_HOST_GRACE、PR_MAX_DEPTH）。
// 与其它配置一致：非法取值直接报错，不做静默回退 —— 宽限期写错会让"主播掉线"
// 要么等于立刻销毁房间（0），要么等于房间几乎不回收（>10m），都必须立刻可见；
// 深度写错则会让"延迟预算"或"房间容量"悄悄换一个量级。
func applyRoomEnv(rc *RoomConfig) error {
	if v := os.Getenv(envRoomHostGrace); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("config: %s 必须是 Go 时长（如 45s、2m）, got %q", envRoomHostGrace, v)
		}
		if d <= 0 || d > MaxHostGrace {
			return fmt.Errorf("config: %s 必须落在 (0, %s] 区间, got %q", envRoomHostGrace, MaxHostGrace, v)
		}
		rc.HostGrace = d
	}
	if v := os.Getenv(envRoomMaxDepth); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < MinRoomMaxDepth || n > MaxRoomMaxDepth {
			return fmt.Errorf("config: %s 必须是 [%d,%d] 区间内的整数, got %q",
				envRoomMaxDepth, MinRoomMaxDepth, MaxRoomMaxDepth, v)
		}
		rc.MaxDepth = n
	}

	// —— S-3：房间总数上限与三个回收 TTL。
	if v := os.Getenv(envMaxRooms); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > MaxMaxRooms {
			return fmt.Errorf("config: %s 必须是 [1, %d] 区间内的整数, got %q", envMaxRooms, MaxMaxRooms, v)
		}
		rc.MaxRooms = n
	}
	if v := os.Getenv(envUnclaimedRoomTTL); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 || d > MaxUnclaimedRoomTTL {
			return fmt.Errorf("config: %s 必须是 (0, %s] 区间内的 Go 时长（如 10m）, got %q",
				envUnclaimedRoomTTL, MaxUnclaimedRoomTTL, v)
		}
		rc.UnclaimedRoomTTL = d
	}
	if v := os.Getenv(envEmptyRoomTTL); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 || d > MaxEmptyRoomTTL {
			return fmt.Errorf("config: %s 必须是 (0, %s] 区间内的 Go 时长（如 30m）, got %q",
				envEmptyRoomTTL, MaxEmptyRoomTTL, v)
		}
		rc.HostGraceZeroTTL = d
	}
	if v := os.Getenv(envRoomSweepInterval); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < MinRoomSweepInterval || d > MaxRoomSweepInterval {
			return fmt.Errorf("config: %s 必须落在 [%s, %s] 区间（如 1m）, got %q",
				envRoomSweepInterval, MinRoomSweepInterval, MaxRoomSweepInterval, v)
		}
		rc.SweepInterval = d
	}

	// —— S-9：metrics 节流。
	if v := os.Getenv(envMetricsMinInterval); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 0 {
			return fmt.Errorf("config: %s 必须是 >=0 的 Go 时长（0 表示不节流）, got %q", envMetricsMinInterval, v)
		}
		rc.MetricsMinInterval = d
	}
	if v := os.Getenv(envMetricsSignificantRatio); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < MinMetricsSignificantRatio || f > MaxMetricsSignificantRatio {
			return fmt.Errorf("config: %s 必须落在 [%v, %v] 区间, got %q",
				envMetricsSignificantRatio, MinMetricsSignificantRatio, MaxMetricsSignificantRatio, v)
		}
		rc.MetricsSignificantRatio = f
	}
	return nil
}

// parseBool 解析开关型环境变量，非法取值直接报错（与其它配置一致，不静默回退）。
func parseBool(name, v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("config: %s 必须是布尔值（1/0/true/false/yes/no/on/off）, got %q", name, v)
	}
}

// applySegmentEnv 应用 PR_SEGMENT_* / PR_FFMPEG 覆盖。
// 与其它配置一致：非法取值直接报错，不做静默回退。
func applySegmentEnv(sc *SegmentConfig) error {
	if v := os.Getenv(envSegmentConcurrency); v != "" {
		n, err := positiveInt(envSegmentConcurrency, v)
		if err != nil {
			return err
		}
		sc.Concurrency = n
	}
	if v := os.Getenv(envSegmentQueueLength); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return fmt.Errorf("config: %s 必须是 >=0 的整数, got %q", envSegmentQueueLength, v)
		}
		sc.QueueLength = n
	}
	if v := os.Getenv(envSegmentRatePerMinute); v != "" {
		n, err := positiveInt(envSegmentRatePerMinute, v)
		if err != nil {
			return err
		}
		sc.RatePerMinute = n
	}
	if v := os.Getenv(envSegmentBurst); v != "" {
		n, err := positiveInt(envSegmentBurst, v)
		if err != nil {
			return err
		}
		sc.Burst = n
	}
	if v := os.Getenv(envSegmentTTL); v != "" {
		d, err := positiveDuration(envSegmentTTL, v)
		if err != nil {
			return err
		}
		sc.TTL = d
	}
	if v := os.Getenv(envSegmentCleanup); v != "" {
		d, err := positiveDuration(envSegmentCleanup, v)
		if err != nil {
			return err
		}
		sc.CleanupInterval = d
	}
	if v := os.Getenv(envSegmentSingleMax); v != "" {
		n, err := positiveInt64(envSegmentSingleMax, v)
		if err != nil {
			return err
		}
		sc.SingleResponseMaxBytes = n
	}
	if v := os.Getenv(envSegmentMaxDuration); v != "" {
		d, err := positiveDuration(envSegmentMaxDuration, v)
		if err != nil {
			return err
		}
		sc.MaxDuration = d
	}
	if v := os.Getenv(envSegmentMaxSource); v != "" {
		n, err := positiveInt64(envSegmentMaxSource, v)
		if err != nil {
			return err
		}
		sc.MaxSourceBytes = n
	}
	if v := os.Getenv(envSegmentSeconds); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f <= 0 {
			return fmt.Errorf("config: %s 必须是正数, got %q", envSegmentSeconds, v)
		}
		sc.SegmentSeconds = f
	}
	if v := os.Getenv(envSegmentPackSize); v != "" {
		// 1 是"不打包"：逐片一个文件，与打包功能出现之前一致。
		n, err := positiveInt(envSegmentPackSize, v)
		if err != nil {
			return err
		}
		sc.PackSize = n
	}
	if v := os.Getenv(envSegmentTempDir); v != "" {
		sc.TempDir = v
	}
	if v := os.Getenv(envFFmpeg); v != "" {
		sc.FFmpegPath = v
	}
	return nil
}

func positiveInt(name, v string) (int, error) {
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("config: %s 必须是正整数, got %q", name, v)
	}
	return n, nil
}

func positiveInt64(name, v string) (int64, error) {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("config: %s 必须是正整数, got %q", name, v)
	}
	return n, nil
}

func positiveDuration(name, v string) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("config: %s 必须是正的时长（如 30m）, got %q", name, v)
	}
	return d, nil
}

// applyICEEnv 应用 ICE 下发的环境变量覆盖。
// 与其它配置一致：非法取值直接报错，不做静默回退 —— 把 MaxSTUN 写成 0、把 TTL 写成
// "5"（漏了单位）都会让下发内容悄悄退化，必须在启动时立刻可见。
//
// 注意这里**没有 TURN**：TURN 字段与 PR_TURN_* 已随退役一并删除
// （见 README「为什么不再有 TURN」）。设置 PR_TURN_* 现在是无声无息的空操作。
func applyICEEnv(ic *ICEConfig) error {
	if v := os.Getenv(envSTUNURLs); v != "" {
		list := splitList(v)
		if len(list) == 0 {
			return fmt.Errorf("config: %s 不能解析出任何 URL, got %q", envSTUNURLs, v)
		}
		ic.STUNURLs = uniqueList(list)
	}
	if v := os.Getenv(envICEMaxSTUN); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return fmt.Errorf("config: %s 必须是 >=1 的整数, got %q", envICEMaxSTUN, v)
		}
		ic.MaxSTUN = n
	}
	if v := os.Getenv(envICEProbeInterval); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("config: %s 必须是 Go 时长（如 60s、2m）, got %q", envICEProbeInterval, v)
		}
		if d <= 0 || d > MaxICEProbeInterval {
			return fmt.Errorf("config: %s 必须落在 (0, %s] 区间, got %q", envICEProbeInterval, MaxICEProbeInterval, v)
		}
		ic.ProbeInterval = d
	}
	if v := os.Getenv(envICETTL); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("config: %s 必须是 Go 时长（如 300s、5m）, got %q", envICETTL, v)
		}
		if d <= 0 || d > MaxICETTL {
			return fmt.Errorf("config: %s 必须落在 (0, %s] 区间, got %q", envICETTL, MaxICETTL, v)
		}
		ic.TTL = d
	}
	return nil
}

// applySignalEnv 应用 S-1 的解码前预算与速率配额覆盖。
//
// 与其它配置一致：非法值直接报错。这里尤其不能静默回退 ——
// 预算写错（例如把 MaxMembers 写成 0 或负数）等于把审计实证的 OOM 路径重新打开，
// 而"配错了"和"配对了"在运行期完全看不出来。
func applySignalEnv(sc *SignalConfig) error {
	intCases := []struct {
		name  string
		value *int
		// min 是允许下限（含），max 是允许上限（含）。
		min, max int
	}{
		{envSignalMaxRepeatedElements, &sc.MaxRepeatedElements, 1, MaxSignalMaxRepeatedElements},
		{envSignalMaxMembers, &sc.MaxMembersPerMessage, 1, MaxSignalMaxMembers},
		{envSignalMaxSegments, &sc.MaxSegmentsPerIndex, 1, MaxSignalMaxSegments},
		{envSignalMaxFieldBytes, &sc.MaxFieldBytes, 1024, MaxSignalMaxFieldBytes},
		// 0 表示关闭速率闸（排障用），所以下限是 0。
		{envSignalMaxRate, &sc.MaxRateBytesPerSec, 0, MaxSignalRateBytes},
		{envSignalRateBurst, &sc.RateBucketBytes, 1024, MaxSignalRateBytes},
	}
	for _, tc := range intCases {
		v := os.Getenv(tc.name)
		if v == "" {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("config: %s 必须是整数, got %q", tc.name, v)
		}
		if n < tc.min || n > tc.max {
			return fmt.Errorf("config: %s 必须落在 [%d, %d] 区间, got %q", tc.name, tc.min, tc.max, v)
		}
		*tc.value = n
	}
	return nil
}

// applyIPCEnv 应用 S-3/S-11 的每 IP 令牌桶覆盖。
func applyIPCEnv(ic *IPCConfig) error {
	cases := []struct {
		name  string
		value *float64
		min   float64
		max   float64
	}{
		{envRoomCreatePerMinute, &ic.CreatePerMinute, 0.001, MaxRoomCreatePerMinute},
		{envJoinFailPerMinute, &ic.JoinFailPerMinute, 0.001, MaxRoomCreatePerMinute},
		{envAuthLoginPerMinute, &ic.LoginPerMinute, 0.001, MaxRoomCreatePerMinute},
		{envAuthRegisterPerMinute, &ic.RegisterPerMinute, 0.001, MaxRoomCreatePerMinute},
		{envAuthRefreshPerMinute, &ic.RefreshPerMinute, 0.001, MaxRoomCreatePerMinute},
		{envPublicRoomsPerMinute, &ic.PublicRoomsPerMinute, 0.001, MaxRoomCreatePerMinute},
		{envAdminLogsPerMinute, &ic.AdminLogsPerMinute, 0.001, MaxRoomCreatePerMinute},
		{envRoomMetaPerMinute, &ic.RoomMetaPerMinute, 0.001, MaxRoomCreatePerMinute},
	}
	for _, tc := range cases {
		v := os.Getenv(tc.name)
		if v == "" {
			continue
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("config: %s 必须是数字, got %q", tc.name, v)
		}
		if f < tc.min || f > tc.max {
			return fmt.Errorf("config: %s 必须落在 [%v, %v] 区间, got %q", tc.name, tc.min, tc.max, v)
		}
		*tc.value = f
	}

	bursts := []struct {
		name  string
		value *int
	}{
		{envRoomCreateBurst, &ic.CreateBurst},
		{envJoinFailBurst, &ic.JoinFailBurst},
		{envAuthLoginBurst, &ic.LoginBurst},
		{envAuthRegisterBurst, &ic.RegisterBurst},
		{envAuthRefreshBurst, &ic.RefreshBurst},
		{envPublicRoomsBurst, &ic.PublicRoomsBurst},
		{envAdminLogsBurst, &ic.AdminLogsBurst},
		{envRoomMetaBurst, &ic.RoomMetaBurst},
	}
	for _, tc := range bursts {
		v := os.Getenv(tc.name)
		if v == "" {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1_000_000 {
			return fmt.Errorf("config: %s 必须是 [1, 1000000] 区间内的整数, got %q", tc.name, v)
		}
		*tc.value = n
	}
	return nil
}

// applySecurityEnv 应用 F-9 的安全响应头覆盖。
func applySecurityEnv(sc *SecurityConfig) error {
	if v := os.Getenv(envSecurityHeaders); v != "" {
		b, err := parseBool(envSecurityHeaders, v)
		if err != nil {
			return err
		}
		sc.Headers = b
	}
	if v := os.Getenv(envSecurityCSP); v != "" {
		csp := strings.TrimSpace(v)
		if csp == "" {
			return fmt.Errorf("config: %s 不能为空（要关闭 CSP 请用 PR_SECURITY_HEADERS=0）, got %q", envSecurityCSP, v)
		}
		sc.CSP = csp
	}
	// 审计残留项 S2：可配置的受信代理（默认回环）。
	//
	// 显式传空串（PR_TRUSTED_PROXIES=""，即环境变量存在但值为空）表示"谁的转发头都不信"，
	// 这在"服务端直接对外"时是正确选择；不设置则保持默认的回环白名单。
	// 因此这里区分"未设置"与"设置为空"，与其它配置项一致地不做静默回退。
	if v, ok := os.LookupEnv(envTrustedProxies); ok {
		list := splitList(v)
		if len(list) == 0 {
			sc.TrustedProxies = []string{}
			// gin 的约定：空列表表示"不信任任何代理"，转发头一律忽略。
		} else {
			sc.TrustedProxies = uniqueList(list)
		}
	}
	return nil
}

// applyAuthEnv 应用账号层的数据库与凭据覆盖（ACCOUNTS §5）。
//
// 这里有一条**故意**的启动期硬校验：DBDSN 非空而 JWTSecret 缺失/过短时直接报错。
// 原因是这两种配置错误的症状都极其难以归因：
//   - 缺密钥 → 所有已登录用户随时掉线（token 校验失败），看起来像网络问题；
//   - 密钥过短 → HS256 可被暴力破解，而线上不会有任何异常表现。
//
// 另外，密钥支持 `PR_JWT_SECRET_FILE`（见下）以外的两种写法都不做：只读环境变量，
// 避免"从文件读密钥"这条额外路径被误配成世界可读。
func applyAuthEnv(ac *AuthConfig) error {
	if v := strings.TrimSpace(os.Getenv(envDBDSN)); v != "" {
		ac.DBDSN = v
	}
	if v := os.Getenv(envJWTSecret); v != "" {
		ac.JWTSecret = v
	}
	if ac.DBDSN != "" && len(ac.JWTSecret) < MinJWTSecretBytes {
		return fmt.Errorf(
			"config: 已配置 %s 但 %s 缺失或过短（当前 %d 字节，至少需要 %d）："+
				"请生成一个强密钥，例如 `openssl rand -hex 32`。"+
				"（缺少密钥会让所有已登录用户随机掉线，且症状像网络故障，所以这里拒绝启动。）",
			envDBDSN, envJWTSecret, len(ac.JWTSecret), MinJWTSecretBytes)
	}
	if v := os.Getenv(envAccessTTL); v != "" {
		d, err := positiveDuration(envAccessTTL, v)
		if err != nil {
			return err
		}
		if d > 24*time.Hour {
			return fmt.Errorf("config: %s 不得超过 24h, got %q", envAccessTTL, v)
		}
		ac.AccessTTL = d
	}
	if v := os.Getenv(envRefreshTTL); v != "" {
		d, err := positiveDuration(envRefreshTTL, v)
		if err != nil {
			return err
		}
		if d < MinRefreshTTL || d > MaxRefreshTTL {
			return fmt.Errorf("config: %s 必须落在 [%s, %s] 区间, got %q",
				envRefreshTTL, MinRefreshTTL, MaxRefreshTTL, v)
		}
		ac.RefreshTTL = d
	}
	if v := os.Getenv(envBcryptCost); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < MinBcryptCost || n > MaxBcryptCost {
			return fmt.Errorf("config: %s 必须是 [%d, %d] 区间内的整数, got %q",
				envBcryptCost, MinBcryptCost, MaxBcryptCost, v)
		}
		ac.BcryptCost = n
	}
	if v := os.Getenv(envAuthHashConcurrency); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < MinHashingConcurrency || n > MaxHashingConcurrency {
			return fmt.Errorf("config: %s 必须是 [%d, %d] 区间内的整数, got %q",
				envAuthHashConcurrency, MinHashingConcurrency, MaxHashingConcurrency, v)
		}
		ac.HashingConcurrency = n
	}
	if v := os.Getenv(envWSTicketTTL); v != "" {
		d, err := positiveDuration(envWSTicketTTL, v)
		if err != nil {
			return err
		}
		if d > 10*time.Minute {
			return fmt.Errorf("config: %s 不得超过 10m（票据只是「取票到建连」的窗口）, got %q", envWSTicketTTL, v)
		}
		ac.WSTicketTTL = d
	}
	return nil
}

// uniqueList 去掉重复项并保持首次出现的顺序。
// 为什么要去重：重复的 STUN 会在 iceServers 里出现两条一模一样的条目，
// 浏览器会白跑一轮重复的候选收集。
func uniqueList(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
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
