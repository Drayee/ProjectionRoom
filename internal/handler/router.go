// Package handler 暴露 gin 路由：健康检查、房间管理与 WebSocket 信令端点。
package handler

import (
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

// NewRouter 组装 HTTP 路由。
// 返回 *gin.Engine 让 wire 能直接把它注入 main 的 http.Server。
//
// seg 是服务端切片队列；为 nil 时跳过 /api/v1/segment/* 的注册（单测可以只关心信令链路）。
// 正常装配必须传真实实例，见 cmd/wire.go。
func NewRouter(cfg *config.Config, hub *service.Hub, rooms *usecase.Manager, seg *segment.Queue) *gin.Engine {
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
	api.POST("/rooms", roomCreateLimiter(createLimiter), createRoomHandler(cfg, rooms, iceReg))
	api.GET("/rooms/:roomId", roomInfoHandler(cfg, rooms))
	api.GET("/ice", func(c *gin.Context) {
		c.JSON(http.StatusOK, iceReg.Payload())
	})

	// 服务端切片端点（一次性预处理，不参与直播链路，因此不违反不变量 I1）。
	registerSegmentRoutes(api, seg)

	// 客户端切片器二进制的只读清单：只回 JSON（平台/大小/sha256），
	// 二进制本身由静态托管 /downloads/<file> 送出，所以这里先于 registerStatic 注册。
	registerDownloadRoutes(api, cfg)

	r.GET("/ws", wsHandler(cfg, hub, rooms, joinLimiter))

	// 静态资源与 SPA 回退必须放在最后：NoRoute 只兜住业务路由之外的请求，
	// 这样 /api、/ws、/healthz 永远优先（详见 static.go）。
	registerStatic(r, cfg)

	return r
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

func createRoomHandler(cfg *config.Config, rooms *usecase.Manager, iceReg *ice.Registry) gin.HandlerFunc {
	return func(c *gin.Context) {
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

		created, hostToken, err := rooms.Create(roomID, req.Password, req.StreamBps)
		if err != nil {
			status, code, message := roomErrorResponse(err)
			c.JSON(status, gin.H{"error": message, "code": code})
			return
		}

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
func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if origin := c.GetHeader("Origin"); origin != "" {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
			c.Header("Access-Control-Allow-Headers", "Content-Type")
			c.Header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
