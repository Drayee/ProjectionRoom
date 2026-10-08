package config

import "time"

// Config 是 ProjectionRoom 的运行时配置。
// 默认值面向本机开发（SPEC §1.3：HTTPS 仅预留；ICE 只用 STUN，TURN 已退役）。
type Config struct {
	Addr      string
	Room      RoomConfig
	Signal    SignalConfig
	ICE       ICEConfig
	Segment   SegmentConfig
	Static    StaticConfig
	Downloads DownloadsConfig
	Security  SecurityConfig
	Auth      AuthConfig
	IPC       IPCConfig
	LogLevel  string
	// MemoryLimitBytes 是进程软内存上限（PR_MEMORY_LIMIT，默认 512 MiB）。
	//
	// 为什么要有它：前面的"解码前预算"是**精确**的闸门（知道一帧会展开成什么），
	// 而这一条是**兜底**的闸门（不知道会从哪冒出来，只知道总量到顶了）。
	// 两者不可互相替代：预算挡的是已知形态的放大，debug.SetMemoryLimit 挡的是
	// "某个我们没想到的路径"。到顶时 Go 会提高 GC 频率而不是杀进程，
	// 因此它表现为"变慢/CPU 升高"，而不是 502。
	MemoryLimitBytes int64
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
	// MaxDepth 是分发树的深度上限（PR_MAX_DEPTH，默认 3，允许 [1,6]）。
	//
	// 为什么默认从 4 收到 3：产品目标已明确为「延迟与卡顿优先」。
	// 每跳中继实测给端到端多加 58–78ms（docs/ALGORITHM.md §2.1），4 跳最坏再叠 ~300ms，
	// 而多出来的那一层在十几人的房间里很少真的换来容量。深度与容量是对价关系，
	// 所以留成配置项：需要更大的房间时显式抬高，而不是默认让所有人替少数场景买单。
	// 分配侧的兜底常量是 usecase.DefaultMaxDepth，两者必须一致。
	MaxDepth int

	// —— 以下都是 S-3（建房无限速、房间永不回收）的闸门与回收参数。

	// MaxRooms 是**同时存在的房间总数**硬上限（PR_MAX_ROOMS，默认 256）。
	// 超出时建房返回 503（而不是 500 或静默成功）。
	//
	// 依据：一个房间的常驻成本是几 KB（成员表 + 播放状态 + 分配计划），
	// 但没有上限时"创建 400 个房间 RSS 只增不减"（审计实测），
	// 而每个房间还要额外配一个 60s 宽限定时器。256 对应几十 MB 量级，
	// 同时远高于任何真实放映室场景的并发房间数。真要开更多房间，
	// 应当同时抬高本值与 PR_MEMORY_LIMIT，而不是只抬一个。
	MaxRooms int
	// UnclaimedRoomTTL 是"创建后从未有人进房"的空房间保留时间（PR_ROOM_UNCLAIMED_TTL，默认 10m）。
	//
	// 为什么单独给它一个 TTL：僵尸房间的主要来源就是"POST /api/rooms 拿到链接但没人进房"
	// —— 主播分享链接给朋友、朋友没点，房间就一直占着。这类房间没有任何值得保留的状态
	//（成员表空、播放状态默认、索引为空），所以按创建时间回收是零风险的。
	// 10 分钟的依据：从"拿到房间码"到"主播点进房"这一段人机交互时间（复制链接、
	// 切换设备、登录）实测都在分钟内；10 分钟足够，又不会让房间码长期占位。
	UnclaimedRoomTTL time.Duration
	// HostGraceZeroTTL 是"主播断线宽限期内且房间已经没人"时的回收阈值
	//（PR_ROOM_EMPTY_TTL，默认 30m）。
	//
	// 关键约束：**hostOffline 期间一律不回收**（见 usecase.reclaimOnce）——
	// 宽限期内那几 KB 元数据正是"主播重连时房间码还能用"的全部依据，
	// 扫掉它等于把 60s 宽限期作废。这一项只覆盖一种极少见的状态：
	// 房间既没有成员、又**不在**宽限期里（正常路径下 Leave 已经销毁了它），
	// 属于"兜底回收"，所以给一个足够长的阈值，避免误杀任何活跃房间。
	HostGraceZeroTTL time.Duration
	// SweepInterval 是后台清扫周期（PR_ROOM_SWEEP_INTERVAL，默认 1m）。
	SweepInterval time.Duration

	// MetricsMinInterval 是同一成员两次 metrics 之间的最小处理间隔（PR_METRICS_MIN_INTERVAL，默认 1s）。
	//
	// 为什么需要它（S-9）：上传容量一旦"变化"就会触发 ReassignTopology(force=true)，
	// 而 force 绕过 reassignMinInterval 那道节流，并全量下发成员表。
	// 观众连发 metrics 就能把全房间拖进"每次上报都重算 + 全量广播"。
	// 默认 1s：客户端本来就是 5s 一次，1s 只是把"恶意连发"压到有意义的频率以下。
	MetricsMinInterval time.Duration
	// MetricsSignificantRatio 是"变化显著"的阈值（PR_METRICS_SIGNIFICANT_RATIO，默认 0.1）。
	//
	// 只有相对变化超过它的上行实测值才会触发重算（首次上报永远算显著，否则容量模型
	// 永远停在 pending）。依据：容量公式 gateSlots = U/stream - 1 对 U 是连续的，
	// 10% 以内的抖动最多让闸门挪 0~1 个席位，而一次重算 = 全房成员表广播，
	// 代价远大于那一个席位。允许范围 [0.01, 1.0]，配 1.0 等于"只有翻倍才重算"。
	MetricsSignificantRatio float64
}

// IPCConfig 是"按 IP（必要时再叠房间码）"的令牌桶限速参数。
//
// 为什么用令牌桶而不是计数窗口：这些入口的正常用量是"偶发一小簇"
// （建房一次、密码输错两三次），而攻击用量是"持续稳定"。令牌桶对前者零约束、
// 对后者有硬上限；固定窗口则会在窗口边缘被绕过，且对突发不友好。
type IPCConfig struct {
	// CreatePerMinute / CreateBurst 是 POST /api/rooms 的每 IP 限速（PR_ROOM_CREATE_PER_MINUTE、PR_ROOM_CREATE_BURST）。
	// 默认 20/分钟、容量 10：人建房是"点一下"，脚本建房是"每秒几百个"。
	// 容量 10 让"连续开几个房间做对比"仍然顺畅。
	CreatePerMinute float64
	CreateBurst     int
	// JoinFailPerMinute / JoinFailBurst 是 join **失败**的每 IP+房间码限速
	//（PR_JOIN_FAIL_PER_MINUTE、PR_JOIN_FAIL_BURST）。
	//
	// 只对失败计数（成功不消耗令牌）：正常重连/刷新会成功，不该被惩罚；
	// 而"猜房间密码"全是失败。默认 30/分钟、容量 30 的依据：
	// 一个 6 位房间码的密码猜测要做到有意义的概率需要 ≥10^4 次量级，
	// 30/分钟 把这条路压到需要几天；同时留给正常人"打错 3 次"的余量。
	JoinFailPerMinute float64
	JoinFailBurst     int

	// —— 账号层（ACCOUNTS §5）的三个入口，同样是每 IP 令牌桶。
	//
	// 为什么和建房共用这个结构体：它们的语义完全一样（按来源 IP 限速、突发友好、
	// 持续攻击有硬上限），而 IPCConfig 已经是这个语义的 owner。
	// 另建一套"AuthRateConfig"会立刻产生两个限速 owner，后续调参必然漏改一处。

	// LoginPerMinute / LoginBurst 是登录尝试的每 IP 限速
	//（PR_AUTH_LOGIN_PER_MINUTE、PR_AUTH_LOGIN_BURST，默认 10/分钟、容量 5）。
	// 依据：bcrypt cost=12 单次约 200ms，10/分钟意味着攻击者最多让服务端
	// 每秒多花 33ms 在校验上；而正常人打错密码三四次完全在容量内。
	// 登录失败**也计入**（与 join 失败不同）：这里要挡的正是撞库。
	LoginPerMinute float64
	LoginBurst     int
	// RegisterPerMinute / RegisterBurst 是注册的每 IP 限速
	//（PR_AUTH_REGISTER_PER_MINUTE、PR_AUTH_REGISTER_BURST，默认 5/分钟、容量 3）。
	// 注册同样要跑一次 bcrypt，且它会写入数据库，所以比登录更严。
	RegisterPerMinute float64
	RegisterBurst     int
	// RefreshPerMinute / RefreshBurst 是刷新会话的每 IP 限速
	//（PR_AUTH_REFRESH_PER_MINUTE、PR_AUTH_REFRESH_BURST，默认 30/分钟、容量 10）。
	// 它比登录宽松：客户端必然周期性刷新（多标签页会叠加），
	// 且刷新本身不跑 bcrypt，代价只是一次数据库查询。
	RefreshPerMinute float64
	RefreshBurst     int

	// PublicRoomsPerMinute / PublicRoomsBurst 是 GET /api/public-rooms 的每 IP 限速
	//（PR_PUBLIC_ROOMS_PER_MINUTE、PR_PUBLIC_ROOMS_BURST，默认 60/分钟、容量 20）。
	//
	// 为什么需要它（T2）：这是二期里**唯一免登录、且会触发数据库查询**的读取端点。
	// 没有它时，一个匿名 IP 就能持续拉列表，每次都换来一次 rooms_meta 批量查询
	// 与一次内存房间快照遍历；而列表页本身是"翻页看"的形态，60/分钟对任何正常
	// 浏览都过剩（那是每分钟 60 次翻页）。
	// 容量取 20：列表页首屏会有"进页面 + 立刻翻一页 + 改筛选重查"这种小簇，
	// 容量太小会让正常用户在头几秒就被 429。
	PublicRoomsPerMinute float64
	PublicRoomsBurst     int

	// AdminLogsPerMinute / AdminLogsBurst 是 GET /api/admin/logs 的每 IP 限速
	//（PR_ADMIN_LOGS_PER_MINUTE、PR_ADMIN_LOGS_BURST，默认 60/分钟、容量 10）。
	//
	// 这是补一期自报的缺口：日志端点把服务端环形缓冲整段交出去，是一条读取面，
	// 而它此前**没有任何限速**。口径取"管理员手点刷新"的量级（一分钟十几次），
	// 容量 10 让"连点几次 + 分页"仍然顺畅。
	// 注意它按 IP 计数而不是按管理员账号：管理端目前是一台机器上的一个运维入口，
	// 按账号计数需要在这里再引入一个维度，而收益（防单账号刷）在本期并不成立。
	AdminLogsPerMinute float64
	AdminLogsBurst     int
}

// AuthConfig 是账号层的数据库与凭据配置（ACCOUNTS §5）。
//
// 独立成一块而不是塞进 Room/Signal：账号是**新增的一条面**，
// 它的开关语义也与其它配置不同 —— DBDSN 为空表示"账号能力整体关闭"，
// 此时服务端退回纯游客模式（观众仍可按房间码进房，建房返回明确错误）。
// 这样做的目的是让"先部署代码、再逐步开启账号"成为可能。
type AuthConfig struct {
	// DBDSN 是 PostgreSQL 连接串（PR_DB_DSN，形如
	// `host=127.0.0.1 port=5432 user=pr_app password=… dbname=projectionroom sslmode=disable`）。
	// 为空即账号能力关闭：不连库、不注册认证路由，启动时打印一条 WARN。
	DBDSN string
	// JWTSecret 是 HS256 签名密钥（PR_JWT_SECRET，至少 32 字节，
	// 生成：`openssl rand -hex 32`）。DBDSN 非空时**必填**，否则启动失败 ——
	// 一个缺失/临时密钥会导致"所有人随机掉线"这种最难排查的症状，宁可拒绝启动。
	JWTSecret string
	// AccessTTL 是 access token 有效期（PR_ACCESS_TTL，默认 15m，范围 (0, 24h]）。
	AccessTTL time.Duration
	// RefreshTTL 是 refresh（会话）有效期（PR_REFRESH_TTL，默认 720h）。
	RefreshTTL time.Duration
	// BcryptCost 是口令哈希代价（PR_BCRYPT_COST，默认 12，范围 [10,14]）。
	BcryptCost int
	// HashingConcurrency 是并发口令哈希的闸门容量（PR_AUTH_HASH_CONCURRENCY，默认 4，范围 [1,64]）。
	//
	// 为什么需要它：bcrypt 是纯 CPU 的，本机实测 cost=12 单次约 0.57s，而服务器
	// 只有 2 vCPU。没有闸门时，少量 IP 并发打登录/注册就能把 CPU 占满，让正常
	// 用户从"0.6 秒登录"退化到"几秒登录"（甚至触发上游超时）。拿到闸门之外
	// 的请求**立刻**被拒（429），而不是排队 —— 排队会把响应时间变成无上界，
	// 且队列本身成为第二个被打爆的资源。
	// 默认 4 的依据：2 vCPU 上留一半余量给信令与静态托管。
	HashingConcurrency int
	// WSTicketTTL 是 WebSocket 一次性票据的有效期（PR_WS_TICKET_TTL，默认 30s）。
	WSTicketTTL time.Duration
}

// SignalConfig 控制 WebSocket 信令层的超时与限额。
type SignalConfig struct {
	WriteTimeout    time.Duration
	PingInterval    time.Duration
	MaxMessageBytes int64
	MaxChatLen      int
	SendQueueSize   int

	// —— 以下四项是 S-1 的"解码前预算"（见 internal/model/wire.go）。
	//
	// 它们与 MaxMessageBytes 是两把不同的尺子：MaxMessageBytes 卡"帧有多少字节"，
	// 这几项卡"帧会展开成多少个对象"。审计实证的攻击帧只有 4 MiB（在长度上限之内），
	// 却能展开成 1,398,100 个消息对象、把 RSS 从 22 MB 顶到 3,947 MB ——
	// 只看字节数的闸门对这种放大完全无效。

	// MaxRepeatedElements 是整帧允许展开的 repeated 元素总数（PR_SIGNAL_MAX_REPEATED_ELEMENTS）。
	// 默认 16384：实测最大的 index.json 是 14000 片，正好在上限内。
	MaxRepeatedElements int
	// MaxMembersPerMessage 是单帧 members 列表的元素上限（PR_SIGNAL_MAX_MEMBERS）。
	// 默认 256：房间硬上限默认 16，留 16 倍余量。
	MaxMembersPerMessage int
	// MaxSegmentsPerIndex 是 media_index.segments 的元素上限（PR_SIGNAL_MAX_SEGMENTS）。
	// 默认 8192：覆盖 2s/片 的 4.5 小时视频。
	MaxSegmentsPerIndex int
	// MaxFieldBytes 是单个 string/bytes 字段的字节上限（PR_SIGNAL_MAX_FIELD_BYTES）。
	// 默认 1 MiB：have 位图 1 bit/片 → 838 万片；SDP 实测几 KB。
	MaxFieldBytes int

	// MaxRateBytesPerSec 是**每连接的字节速率配额**（PR_SIGNAL_MAX_RATE_BYTES），第二道闸。
	//
	// 为什么需要第二道：上面的预算是"单帧"的，挡不住"每秒发几千条都合规的小帧"。
	// 正常客户端只在信令与度量时发消息（度量 5s 一次），几百字节每秒；
	// 默认 256 KiB/s 对任何正常连接都是零约束。
	// 0 表示关闭该闸门（仅用于排障）。
	MaxRateBytesPerSec int
	// RateBucketBytes 是速率配额的桶容量（PR_SIGNAL_RATE_BUCKET_BYTES）。
	// 取 512 KiB：必须 >= 单条消息上限（4 MiB 的那种索引帧走的是"桶里有存量"的路径，
	// 见 service.Client 的说明），同时限制突发。
	RateBucketBytes int

	// AllowedOrigins 是允许发起 WebSocket 的**额外**来源（host[:port]，支持 *.example.com）。
	//
	// 它现在是**唯一**的来源白名单：同源不再靠"请求自身的 Host"自动放行
	//（那条兜底就是 S-5 的 DNS rebinding 绕过：Origin: http://evil.com + Host: evil.com
	// 会被判为同源并升级成功）。单端口部署时页面与 /ws 同端口，浏览器发出的
	// Origin 就是页面自己的 host，因此必须把它显式列进 PR_ALLOWED_ORIGINS。
	AllowedOrigins []string
}

// SecurityConfig 是全站安全响应头（F-9）。
//
// 为什么放在服务端中间件而不是让前端/反代去加：反代可配但常常被漏配，
// 而这些头是"零成本的纵深防御"——不挡正常功能，只把一类攻击面直接关掉
// （MIME 嗅探、被 frame、referrer 外泄、设备权限）。
type SecurityConfig struct {
	// Headers 是总开关（PR_SECURITY_HEADERS，默认开）。
	// 关掉它主要是给"另一个反代已经加了这些头"的部署用，避免重复头。
	Headers bool
	// CSP 是 Content-Security-Policy 的内容（PR_SECURITY_CSP）。
	//
	// 默认值必须满足两条硬约束（否则页面直接坏）：
	//   - style-src 必须含 'unsafe-inline'：Vue 运行时按组件注入内联 <style>；
	//   - media-src 必须含 blob:：MSE 用 URL.createObjectURL(MediaSource) 播放。
	// 现有 client/dist 没有内联 <script>，所以 script-src 可以只留 'self'。
	CSP string
	// TrustedProxies 是**允许其转发头（X-Forwarded-For / X-Real-IP）被采信**的来源
	//（PR_TRUSTED_PROXIES，默认 127.0.0.1 与 ::1）。
	//
	// 为什么必须是显式白名单：gin 默认信任所有代理，于是 `c.ClientIP()` 会取
	// X-Forwarded-For 的最左值 —— 那是请求方可以随便写的。按 IP 的限速与
	// join 失败退避全部建立在 ClientIP 上，一旦被伪造就等于把这些闸门关掉
	//（审计实测：直连来源换 3 个伪造 XFF 就能连发 3 次建房）。
	// 本部署的形态是"nginx 在 127.0.0.1 上反代"，因此默认只信任回环；
	// 多层代理/容器 sidecar 需要显式把它们加进来。
	TrustedProxies []string
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

// ICEConfig 控制下发给浏览器的 ICE 配置。
//
// 这里**只有 STUN，没有 TURN**：TURN 已彻底退役，字段与 PR_TURN_* 环境变量一并删除
// （原因、以及"什么条件下才该重新引入"见 README「为什么不再有 TURN」）。
// 退役后列表里每一个条目都由服务端周期性实测打分，只把此刻真的能用的前 MaxSTUN 条下发，
// 且实测最快的两条不会因为轮询而落选（算法与打分公式见 internal/service/ice 与
// docs/ALGORITHM.md §1.6）。
type ICEConfig struct {
	// STUNURLs 是候选 STUN 列表，顺序即默认优先级（PR_STUN_URLS 可整体覆盖）。
	STUNURLs []string
	// MaxSTUN 是单次下发给浏览器的 STUN 条数上限（PR_ICE_MAX_STUN，默认 5）。
	//
	// 为什么要有上限：候选收集对每条 STUN 是串行的，一条 4 秒无响应的服务器
	// 就能把 srflx 收集拖慢一个数量级。宁可少给几条，也不给一条会卡住的。
	// 默认 5 的来历：恒选席位 = 最快 2 条 + 健康粘性 2 条，再留 1 个轮询席位
	// （详见 config.Load 里 DefaultICEMaxSTUN 的注释）。
	MaxSTUN int
	// ProbeInterval 是服务端重新探测一轮的周期（PR_ICE_PROBE_INTERVAL，默认 60s）。
	ProbeInterval time.Duration
	// TTL 是下发载荷的有效期（PR_ICE_TTL，默认 300s，允许范围 (0, 1h]）。
	// 响应里的 ttlSeconds 与 expiresAt 都由它推导，且 expiresAt 每次响应现算。
	TTL time.Duration
}
