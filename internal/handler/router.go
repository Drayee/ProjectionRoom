// Package handler 暴露 gin 路由：健康检查、房间管理与 WebSocket 信令端点。
package handler

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/limiters"
	"ProjectionRoom/internal/model"
	"ProjectionRoom/internal/service"
	"ProjectionRoom/internal/service/ice"
	"ProjectionRoom/internal/service/segment"
	"ProjectionRoom/internal/store"
	"ProjectionRoom/internal/usecase"
)

// newICERegistry 是 ICE 探测器在**生产路径**上的构造方式：真实 UDP 探测 + 立即异步启动。
//
// 它被做成包级函数值，只为一件事：让"起一个真实 router"的测试可以换成假探测器，
// 从而不依赖外网（见 ice_test.go）。生产代码永远走默认实现。
//
// 生命周期说明：Registry 在 NewRouter 内创建并常驻进程，与 HTTP 服务同生共死。
// 它自带 Close()，但 NewRouter 拿不到关闭时机（gin.Engine 不暴露生命周期钩子），
// 所以没有把它接进 service.Server 的关闭路径 —— 进程退出时随进程结束，
// 在途的一轮探测最长 1.5s。若将来 cmd/ 可改，应把 Close 接到 Server.Shutdown。
var newICERegistry = func(cfg *config.Config) *ice.Registry {
	reg := ice.NewRegistry(cfg)
	reg.Start() // 异步探测：不阻塞监听
	return reg
}

// createRoomCodeHint 是房间码非法时的用户可读文案（REST 与 WS 共用，保证两条入口口径一致）。
const createRoomCodeHint = "房间码必须是 4-12 位大写字母或数字（A-Z、0-9）"

// RoomMetaSink 是"房间元数据异步落库"的能力（ACCOUNTS §9 的**事件类**：不可丢、
// 有界队列、投递即返回）。
//
// 为什么要一个接口而不是直接用 *store.Writer：
//   - 写作业的签名是 func(ctx, *gorm.DB) error —— 本层不 import gorm（同 errors.go
//     里 storeNotFound 别名的那条理由：让"handler 依赖了什么"一眼可读）；
//   - 建房路径的"元数据投递"因此可以在单测里断言（假 sink 记一次投递），
//     真库验证留给 store 自己的用例。
type RoomMetaSink interface {
	// SubmitRoomMeta 投递一次 rooms_meta 的 upsert；
	// 返回 false = 被丢弃（队列满 / 写入器已关闭）。
	SubmitRoomMeta(meta *store.RoomMeta) bool
}

// roomMetaSink 是 RoomMetaSink 的生产实现。
type roomMetaSink struct {
	store  *store.Store
	writer *store.Writer
}

// NewRoomMetaSink 构造生产实现；任一依赖为 nil 时返回 nil（调用方按"没有写队列"处理）。
//
// 返回 **nil 接口**而不是"一个方法会失败的值"：nil 接口可以被 `sink == nil` 直接判出来，
// 而"带 nil 指针的非 nil 接口"会让判空失效（那正是这类装配最常见的崩溃来源）。
func NewRoomMetaSink(st *store.Store, w *store.Writer) RoomMetaSink {
	if st == nil || w == nil {
		return nil
	}
	return &roomMetaSink{store: st, writer: w}
}

// SubmitRoomMeta 把一次 upsert 投进事件写队列。
//
// 借用 usecase.SubmitAccountJob 做签名翻译（它把 func(ctx) error 翻成写入器要的
// func(ctx, *gorm.DB) error）：房间元数据与账号事件同属 §9 的"事件类"，
// 投递语义完全一致（不阻塞调用方、队列满即丢弃、失败重试）。
func (s *roomMetaSink) SubmitRoomMeta(meta *store.RoomMeta) bool {
	if s == nil || meta == nil {
		return false
	}
	return usecase.SubmitAccountJob(s.writer, func(ctx context.Context) error {
		return s.store.UpsertRoomMeta(ctx, meta)
	})
}

// submitRoomMeta 在建房成功后投递房间元数据（T8 / ACCOUNTS §6、§9）。
//
// 三条取舍：
//
//  1. **异步**：元数据不是"能开播"的前提（真相在内存房间里，见不变量 I2），
//     把它同步写进建房路径等于给建房加一次数据库往返，而 §9 的硬约束是
//     "任何写库都不出现在 WS/REST 处理路径上"（I4）。
//  2. **失败不回滚**：投递失败（队列满 / 写入器已关闭 / 装配缺写队列）时**只记日志**。
//     回滚的代价是"数据库抖动 → 用户建不了房"，而缺一行元数据只影响派生视图
//     （我的房间、公开房列表的标题）；反过来，回滚也救不了什么：
//     房间已经在内存里、hostToken 也已下发给客户端，服务端无法把那个房间码收回来。
//  3. **默认值显式写死**（title=""、is_public=false）：标题与公开性由房主之后
//     通过 PATCH 修改，建房时不留"未设置"这种第三种状态。
func submitRoomMeta(sink RoomMetaSink, roomID string, ownerUserID int64, hasPassword bool) {
	if sink == nil {
		log.Printf("[WARN] 房间元数据未投递（room=%s owner=%d）：装配里没有事件写队列，房间照常可用",
			roomID, ownerUserID)
		return
	}
	meta := &store.RoomMeta{
		RoomID:      roomID,
		OwnerUserID: ownerUserID,
		Title:       "",
		IsPublic:    false,
		HasPassword: hasPassword,
	}
	if !sink.SubmitRoomMeta(meta) {
		log.Printf("[WARN] 房间元数据入队失败（room=%s owner=%d）：队列满或写入器已关闭，房间照常可用",
			roomID, ownerUserID)
	}
}

// NewRouter 组装 HTTP 路由。
// 返回 *gin.Engine 让 wire 能直接把它注入 main 的 http.Server。
//
// seg 是服务端切片队列；为 nil 时跳过 /api/v1/segment/* 的注册（单测可以只关心信令链路）。
// 正常装配必须传真实实例，见 cmd/wire.go。
//
// accountDeps / adminDeps 是**参数**（T8/T9 的收口改造：它们曾经是包级变量）：
//
//	为什么要参数化：包级变量是"装配期写一次、此后只读"的隐藏全局状态 ——
//	它无法在单测里复位（一个用例改了，后面的用例就跟着变），而 handler 恰恰需要
//	假依赖来覆盖 401/403/503 与建房绑定这些协议面判据。做成参数之后，
//	生产装配与测试装配走的是同一条注册路径，且没有任何跨用例的状态。
func NewRouter(
	cfg *config.Config,
	hub *service.Hub,
	rooms *usecase.Manager,
	seg *segment.Queue,
	accountDeps AuthDeps,
	adminDeps AdminDeps,
) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)

	// S-13：静态根目录的安全校验必须在**启动时**做，而且不能降级成 WARN。
	// 把 PR_STATIC_DIR 配成 "/" 或进程工作目录会让整个仓库（含 .env 里的数据库密码）
	// 变成可下载文件；这种配置错误必须在启动那一刻炸出来，而不是等有人来读它。
	if err := ValidateStaticDir(cfg); err != nil {
		panic(fmt.Sprintf("静态目录配置不安全，拒绝启动: %v", err))
	}

	// S-3：起后台房间清扫协程。它挂在 Manager 上（与 HTTP 服务同生共死），
	// 以保证"创建后从未进房的房间"与"兜底残留的空房间"最终会被回收。
	rooms.Start()

	r := gin.New()
	// 反代信任边界（S-3/S-11 的必要前提）：**只信任本机代理**。
	//
	// 为什么必须收紧：gin 默认 TrustedProxies 是"全信任"，此时 c.ClientIP() 取的是
	// X-Forwarded-For 的**最左值** —— 而 XFF 是请求方随便写的。于是任何能直连本端口的
	// 路径都能用 `X-Forwarded-For: <随机 IP>` 把"每 IP 建房限速"与"每 IP+房间码 join
	// 失败退避"逐次绕过（每次都算一个新 IP，令牌桶永远是满的）。
	//
	// 收紧后的语义：只有当**直连来源**是 127.0.0.1/::1（也就是本机 nginx）时才采信
	// XFF；其它来源一律用 TCP 对端地址。这样"公网 → nginx → 本服务"能拿到真实客户端 IP，
	// 而"直接连本服务"伪造 XFF 也拿不到好处（它被当作直连来源本身）。
	//
	// 注意：这不是"反对直连"，而是让直连失去绕过限速的好处。部署若真的直连公网
	//（PR_ADDR 不是回环），启动时会打印醒目警告（见下）。
	if err := r.SetTrustedProxies([]string{"127.0.0.1", "::1"}); err != nil {
		// 固定常量列表不可能解析失败；真失败了说明 gin 语义变了，必须立刻可见。
		panic(fmt.Sprintf("配置信任代理失败: %v", err))
	}
	logProxyTrustWarning(cfg)

	// S-5 的部署提示：同源不再隐式放行。
	//
	// 删掉 c.Request.Host 兜底之后，"页面与 /ws 同端口"不再自动通过来源校验 ——
	// 这是必须的（那条兜底本来就是可伪造的），代价是**单端口/隧道部署必须把
	// 对外访问的域名写进 PR_ALLOWED_ORIGINS**。这一行把要求写在启动日志里，
	// 否则故障表现是"页面能打开但一直连不上信令"，非常难定位。
	log.Printf("WebSocket 来源白名单（S-5：同源不隐式放行）：%v；"+
		"经域名/隧道访问时，请在 PR_ALLOWED_ORIGINS 里加上该域名（如 example.com），"+
		"否则页面与 /ws 之间会被拒 403", cfg.Signal.AllowedOrigins)

	r.Use(gin.Recovery())
	// F-9：安全响应头放在最前面，保证所有出口（业务路由、静态资源、NoRoute 回退）
	// 都带上这些头。
	r.Use(securityHeadersMiddleware(cfg))
	r.Use(corsMiddleware())

	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// ICE 载荷由 ice.Registry 单点负责：探测结果 + 加权轮询选出的列表 + TTL。
	// 两条端点（/api/ice 与 POST /api/rooms）用同一个 payload 形状，前端只需一套解析。
	iceReg := newICERegistry(cfg)

	// S-3：建房限速的每 IP 令牌桶（工厂只建一次，所有请求共享同一份状态）。
	createLimiter := limiters.NewKeyed(cfg.IPC.CreatePerMinute, cfg.IPC.CreateBurst)
	// S-11：join 失败的每 IP+房间码令牌桶。
	joinLimiter := limiters.NewKeyed(cfg.IPC.JoinFailPerMinute, cfg.IPC.JoinFailBurst)

	api := r.Group("/api")
	// T8 / ACCOUNTS §6：建房必须登录（房主要写进内存房间与 rooms_meta）。
	//
	// 注意别和"观众进房"搞混：观众按房间码进房走的是 /ws，**依然免登录**
	// （那是产品决定，见 ws.go 的游客路径）。
	//
	// 顺序（RequireAuth → 限速）：先认证再计入令牌桶，于是"匿名 401 洪水"不消耗
	// 建房配额，而"拿自己账号反复建房"仍然受每 IP 限速约束（S-3 的口径不变）。
	api.POST("/rooms",
		RequireAuth(accountDeps.Service),
		roomCreateLimiter(createLimiter),
		createRoomHandler(cfg, rooms, iceReg, accountDeps),
	)
	api.GET("/rooms/:roomId", roomInfoHandler(cfg, rooms))
	api.GET("/ice", func(c *gin.Context) {
		c.JSON(http.StatusOK, iceReg.Payload())
	})

	// 服务端切片端点（一次性预处理，不参与直播链路，因此不违反不变量 I1）。
	registerSegmentRoutes(api, seg)

	// 账号层的认证路由（ACCOUNTS §10）。
	//
	// 生产路径上它就是下面这个调用：DSN 为空 → 内部直接 return → 这些路由**不注册**
	// （与 seg == nil 跳过 /api/v1/segment/* 是同一条约定）。
	registerAccountRoutes(api, accountDeps, cfg)

	// 管理端（ACCOUNTS §8 / T9）。同一条"能力关闭即不注册"的约定；
	// RequireAuth 用的账号服务与认证路由是同一个实例（见 registerAdminRoutes 的说明）。
	registerAdminRoutes(api, adminDeps, cfg, accountDeps.Service)

	// 二期（T2/T3）：公开房列表（免登录）与房主改房间元数据（RequireAuth）。
	//
	// 依赖来自两条既有链路，**不新增 NewRouter 的参数位**（硬约束）：
	//   - rooms（usecase.Manager）提供内存实况（ListRooms / Get / SetRoomMeta / CloseRoomByAdmin）；
	//   - accountDeps.Store 提供 rooms_meta 的读写与房主昵称查询；
	//   - accountDeps.RoomMeta 是一期的异步落库出口（§9 的事件类）。
	//
	// 顺序说明：写端点必须在只读端点之后挂载，这样 GET /api/rooms/:roomId（既有）
	// 与 GET /api/public-rooms 的路径不会互相遮蔽（gin 的路由树按段匹配，两条不同路径本就不冲突，
	// 但把同一前缀下的读与写按"读在写之前"排列可以让注册表读起来更符合直觉）。
	registerPublicRoomRoutes(api, publicRoomDeps(rooms, accountDeps, adminDeps), cfg)
	registerRoomMetaRoutes(api, RoomMetaDeps{
		Rooms: rooms,
		Meta:  roomMetaReader(accountDeps, adminDeps),
		Sink:  accountDeps.RoomMeta,
	}, cfg, accountDeps.Service)

	// 客户端切片器二进制的只读清单：只回 JSON（平台/大小/sha256），
	// 二进制本身由静态托管 /downloads/<file> 送出，所以这里先于 registerStatic 注册。
	registerDownloadRoutes(api, cfg)

	// T8：/ws 支持一次性票据（绑定账号 + 封禁拦截）；无票据时是**逐字不变**的游客路径。
	r.GET("/ws", wsHandler(cfg, hub, rooms, joinLimiter, accountDeps))

	// 静态资源与 SPA 回退必须放在最后：NoRoute 只兜住业务路由之外的请求，
	// 这样 /api、/ws、/healthz 永远优先（详见 static.go）。
	registerStatic(r, cfg)

	return r
}

// publicRoomDeps 把既有依赖组装成公开房列表需要的形状（T2）。
//
// 元数据来源有两个候选，优先级是刻意的：
//
//	① adminDeps.RoomMeta（管理端的元数据端口，它的方法集是 PublicRoomMetaStore 的超集）
//	② accountDeps.Store （生产装配里的真 store）
//
// 为什么把①放前面：两个候选在生产里**指向同一个 *store.Store**，所以顺序不影响行为；
// 而它让"只有一套元数据端口"的测试装配（管理端用例的假内存表）也能驱动公开房列表 ——
// 否则同一条链路上会出现"管理端看得见房间、公开列表看不见"这种只存在于测试里的怪象。
//
// 缺装配时的行为写在这里，而不是留给读者猜：Rooms 为 nil → 端点返回空列表；
// Meta 为 nil → 全部按"缺元数据行"降级（标题空）；Names 为 nil → ownerName 为空串。
// 三种情况都不是错误：公开列表是只读派生视图，它只有"现在没有内容"这一种降级形态。
func publicRoomDeps(rooms *usecase.Manager, accountDeps AuthDeps, adminDeps AdminDeps) PublicRoomDeps {
	deps := PublicRoomDeps{}
	if rooms != nil {
		deps.Rooms = rooms
	}
	if adminDeps.RoomMeta != nil {
		deps.Meta = adminDeps.RoomMeta
	} else if accountDeps.Store != nil {
		deps.Meta = accountDeps.Store
	}
	if accountDeps.ProfileNames != nil {
		deps.Names = accountDeps.ProfileNames
	}
	deps.Sink = accountDeps.RoomMeta
	return deps
}

// roomMetaReader 选出房主改元数据时"读当前行"的实现（T3）。
//
// 两个候选的优先级与 publicRoomDeps 一致：生产里它们指向同一个 *store.Store，
// 所以顺序不影响行为；而它让**只有一套元数据端口**的测试装配也能驱动这条路由。
//
// 这里必须显式判空并回落到另一个候选，不能直接写 Meta: accountDeps.Store ——
// 把 nil 的 *store.Store 塞进接口会得到一个**非 nil 的接口**，
// 于是 registerRoomMetaRoutes 的装配判空失效，请求进来才会在方法内部崩（500 而不是 503）。
// 这类"typed nil"是 Go 里最容易漏的一种空指针。
func roomMetaReader(accountDeps AuthDeps, adminDeps AdminDeps) RoomMetaReader {
	if accountDeps.Store != nil {
		return accountDeps.Store
	}
	if adminDeps.RoomMeta != nil {
		return adminDeps.RoomMeta
	}
	return nil
}

// logProxyTrustWarning 在监听地址不是回环时打印一条醒目警告。
//
// 为什么这条警告重要（S-3/S-11 的限速边界）：本服务用"按 IP 的令牌桶"保护建房与
// join 失败。只有"经前置反代（本机）传入"的请求才能拿到真实客户端 IP；
// 如果直接暴露在公网（PR_ADDR=0.0.0.0:8080 或内网网卡地址），那么：
//   - 直连请求的 ClientIP() 就是 TCP 对端地址（伪造 XFF 无效，这一点没问题）；
//   - 但任何**前置到本服务的其它来源**（另一台机器上的反代、K8s ingress、
//     甚至是转发链）都不会被采信 XFF，于是所有请求会被算作同一个 IP，
//     限速退化成**全局限速**：一个滥用者能把所有正常用户一起挡在门外。
//
// 两种部署都合法，但必须知道自己在哪一种里，所以这里只在"非回环"时警告。
func logProxyTrustWarning(cfg *config.Config) {
	if cfg == nil || isLoopbackAddr(cfg.Addr) {
		return
	}
	log.Printf("[WARN] 监听地址 %q 不是回环地址：本服务的建房/join 限速按 IP 计数，"+
		"且只信任来自 127.0.0.1/::1 的 X-Forwarded-For。"+
		"公网直连时伪造 XFF 无效（按 TCP 对端计），但**任何非本机前置代理**都会让所有请求"+
		"被算作同一个 IP，使限速退化为全局限速并可能误伤正常用户。"+
		"建议：只监听回环（PR_ADDR=127.0.0.1:8080）并由本机反代转发，或确认上游代理在同一台机器上。",
		cfg.Addr)
}

// isLoopbackAddr 判断监听地址是否是回环（支持 ":8080"、"127.0.0.1:8080"、"[::1]:8080"）。
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// 没有端口（例如只写了 "127.0.0.1" 或 ":8080"）：按原样判断。
		host = strings.Trim(addr, "[]")
	}
	if host == "" {
		// ":8080" 等价于监听所有网卡 —— 不是回环。
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return strings.EqualFold(host, "localhost")
}

// roomCreateLimiter 是 POST /api/rooms 的每 IP 限速中间件（S-3）。
//
// 为什么按 IP 而不是按"全局"：建房是每个用户都会做的正常操作，
// 全局限速会让一个人被另一个人影响；按 IP 才能精确地把滥用者关掉。
// 真实 IP 的可信度由 NewRouter 里的 SetTrustedProxies 保证
// （只信任本机代理的 XFF，否则用 TCP 对端）。
func roomCreateLimiter(limiter *limiters.Keyed) gin.HandlerFunc {
	return func(c *gin.Context) {
		if limiter != nil && !limiter.Allow(c.ClientIP()) {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error": "建房请求过于频繁，请稍后再试",
				"code":  model.CodeRateLimited,
			})
			c.Abort()
			return
		}
		c.Next()
	}
}

// securityHeadersMiddleware 注入全站安全响应头（F-9）。
//
// 为什么用中间件而不是让反代去加：反代可配但常常被漏配，而这些头是零成本的纵深防御 ——
// 不挡正常功能，只把一类攻击面直接关掉（MIME 嗅探、被 frame、referrer 外泄、设备权限）。
//
// 两个"必须"（写错页面就坏，详见 config.DefaultSecurityCSP）：
//   - style-src 含 'unsafe-inline'：Vue 运行时按组件注入内联 <style>；
//   - media-src 含 blob:：MSE 用 URL.createObjectURL(MediaSource) 播放。
func securityHeadersMiddleware(cfg *config.Config) gin.HandlerFunc {
	csp := config.DefaultSecurityCSP
	enabled := true
	if cfg != nil {
		enabled = cfg.Security.Headers
		if strings.TrimSpace(cfg.Security.CSP) != "" {
			csp = cfg.Security.CSP
		}
	}

	return func(c *gin.Context) {
		if !enabled {
			c.Next()
			return
		}

		h := c.Writer.Header()
		h.Set("Content-Security-Policy", csp)
		// nosniff：禁止浏览器按内容猜类型（否则一个 .txt 能被当成 .js 执行）。
		h.Set("X-Content-Type-Options", "nosniff")
		// 老浏览器的 frame 防护（现代浏览器看 CSP 的 frame-ancestors）。
		h.Set("X-Frame-Options", "DENY")
		// 房间码在 URL 里：跨站跳转时不要把完整 URL（含房间码）带给第三方。
		h.Set("Referrer-Policy", "same-origin")
		// 放映室只需要：播放（自动）、可能的全屏。相机/麦克风/地理位置一律关掉。
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")

		c.Next()
	}
}

// createRoomRequest 是创建房间的请求体；字段全可选。
type createRoomRequest struct {
	RoomID    string `json:"roomId"`
	Password  string `json:"password"`
	StreamBps int64  `json:"streamBps"`
}

func createRoomHandler(cfg *config.Config, rooms *usecase.Manager, iceReg *ice.Registry, deps AuthDeps) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 房主身份只来自**校验过的 access token**（RequireAuth 已挡掉匿名请求）。
		// 绝不从请求体/查询串取 ownerUserId：那等于让任何人把房间挂到别人名下。
		// 这道判空是"中间件被绕过"的兜底（fail closed，返回 401 而不是当匿名处理）。
		owner, ok := CurrentUser(c)
		if !ok {
			abortUnauthorized(c, CodeUnauthorized, "建房需要登录")
			return
		}

		var req createRoomRequest
		// 允许空 body：等价于"自动生成房间码、无密码、用默认码率估计"。
		if c.Request.ContentLength > 0 {
			if err := c.ShouldBindJSON(&req); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "请求体不是合法 JSON"})
				return
			}
		}

		// S-3：规范化之后立刻按白名单校验。原先这里只做 ToUpper/Trim，
		// 实测 60 KB 的 roomId 也能建房成功（而每个这样的房间都会常驻内存）。
		roomID := strings.ToUpper(strings.TrimSpace(req.RoomID))
		if roomID != "" && !usecase.ValidRoomCode(roomID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": createRoomCodeHint, "code": model.CodeBadRequest})
			return
		}

		created, hostToken, err := rooms.CreateOwned(roomID, req.Password, req.StreamBps, owner.ID)
		if err != nil {
			status, code, message := roomErrorResponse(err)
			c.JSON(status, gin.H{"error": message, "code": code})
			return
		}

		// 房间元数据异步落库（§9 的事件类）。投递失败不回滚建房，理由见 submitRoomMeta。
		submitRoomMeta(deps.RoomMeta, created.ID, owner.ID, req.Password != "")

		resp := gin.H{
			"roomId":   created.ID,
			"capacity": created.Capacity(cfg.Room.MaxMembers),
			// S-7：主播复位令牌**只在创建响应里下发一次**，服务端只存哈希。
			// 主播断线进入宽限期后，用它才能接回主播位（否则任何知道房间码的人都能抢）。
			// 前端应把它连同"我创建了这个房间"一起存本地；丢了就只能等宽限期结束重开房间。
			"hostToken": hostToken,
		}
		// 与 /api/ice 同源同形状：iceServers + ttlSeconds + expiresAt + probe。
		iceReg.Payload().MergeInto(resp)
		c.JSON(http.StatusOK, resp)
	}
}

func roomInfoHandler(cfg *config.Config, rooms *usecase.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		r, ok := rooms.Get(strings.ToUpper(strings.TrimSpace(c.Param("roomId"))))
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"exists": false, "error": "房间不存在"})
			return
		}

		hostID, members, _, capacity, mediaIndex := r.Snapshot(cfg.Room.MaxMembers)
		c.JSON(http.StatusOK, gin.H{
			"exists":      true,
			"roomId":      r.ID,
			"hasHost":     hostID != "",
			"hasMedia":    mediaIndex != nil,
			"memberCount": len(members),
			"capacity":    capacity,
		})
	}
}

// corsMiddleware 允许本机前端开发源访问 REST 接口（前后端分离，端口不同）。
// WebSocket 的来源校验在 wsHandler 里单独处理。
//
// 二期的两处补齐（T4 顺手补一期缺口）：
//
//	Allow-Methods 补 PATCH      —— 新增的 PATCH /api/rooms/:id/meta、PATCH /api/admin/users/:id
//	                              在跨源预检时会因"方法不在列表里"直接被浏览器拦掉，
//	                              表现为"接口用 curl 通、页面打不通"；
//	Allow-Headers 补 Authorization —— 账号层的凭据走 Authorization 头（§5），
//	                              预检不声明它时浏览器不会把该头发出去，结果一律 401。
//
// **刻意不动 Origin 的处理方式**：这里仍然是"反射请求的 Origin"（开发态本机两个端口）。
// 来源白名单属于 WS 的一侧（S-5 的 PR_ALLOWED_ORIGINS），本中间件不参与授权判断 ——
// 它只影响浏览器愿不愿意发出请求，而真正的闸门在服务端每一条路由上。
// 把它改成"收紧到白名单"是另一件事（需要区分生产/开发），本期不做。
func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if origin := c.GetHeader("Origin"); origin != "" {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
			c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
			c.Header("Access-Control-Allow-Methods", "GET, POST, PATCH, OPTIONS")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
