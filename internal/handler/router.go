// Package handler 暴露 gin 路由：健康检查、房间管理与 WebSocket 信令端点。
package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
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

// NewRouter 组装 HTTP 路由。
// 返回 *gin.Engine 让 wire 能直接把它注入 main 的 http.Server。
//
// seg 是服务端切片队列；为 nil 时跳过 /api/v1/segment/* 的注册（单测可以只关心信令链路）。
// 正常装配必须传真实实例，见 cmd/wire.go。
func NewRouter(cfg *config.Config, hub *service.Hub, rooms *usecase.Manager, seg *segment.Queue) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(corsMiddleware())

	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// ICE 载荷由 ice.Registry 单点负责：探测结果 + 加权轮询选出的列表 + TTL。
	// 两条端点（/api/ice 与 POST /api/rooms）用同一个 payload 形状，前端只需一套解析。
	iceReg := newICERegistry(cfg)

	api := r.Group("/api")
	api.POST("/rooms", createRoomHandler(cfg, rooms, iceReg))
	api.GET("/rooms/:roomId", roomInfoHandler(cfg, rooms))
	api.GET("/ice", func(c *gin.Context) {
		c.JSON(http.StatusOK, iceReg.Payload())
	})

	// 服务端切片端点（一次性预处理，不参与直播链路，因此不违反不变量 I1）。
	registerSegmentRoutes(api, seg)

	// 客户端切片器二进制的只读清单：只回 JSON（平台/大小/sha256），
	// 二进制本身由静态托管 /downloads/<file> 送出，所以这里先于 registerStatic 注册。
	registerDownloadRoutes(api, cfg)

	r.GET("/ws", wsHandler(cfg, hub, rooms))

	// 静态资源与 SPA 回退必须放在最后：NoRoute 只兜住业务路由之外的请求，
	// 这样 /api、/ws、/healthz 永远优先（详见 static.go）。
	registerStatic(r, cfg)

	return r
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

		created, err := rooms.Create(strings.ToUpper(strings.TrimSpace(req.RoomID)), req.Password, req.StreamBps)
		if err != nil {
			status, code, message := roomErrorResponse(err)
			c.JSON(status, gin.H{"error": message, "code": code})
			return
		}

		resp := gin.H{
			"roomId":   created.ID,
			"capacity": created.Capacity(cfg.Room.MaxMembers),
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
