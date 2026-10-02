// Package handler 暴露 gin 路由：健康检查、房间管理与 WebSocket 信令端点。
package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/service"
	"ProjectionRoom/internal/service/segment"
	"ProjectionRoom/internal/usecase"
)

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

	api := r.Group("/api")
	api.POST("/rooms", createRoomHandler(cfg, rooms))
	api.GET("/rooms/:roomId", roomInfoHandler(cfg, rooms))
	api.GET("/ice", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"iceServers": cfg.ICEServers()})
	})

	// 服务端切片端点（一次性预处理，不参与直播链路，因此不违反不变量 I1）。
	registerSegmentRoutes(api, seg)

	r.GET("/ws", wsHandler(cfg, hub, rooms))

	return r
}

// createRoomRequest 是创建房间的请求体；字段全可选。
type createRoomRequest struct {
	RoomID    string `json:"roomId"`
	Password  string `json:"password"`
	StreamBps int64  `json:"streamBps"`
}

func createRoomHandler(cfg *config.Config, rooms *usecase.Manager) gin.HandlerFunc {
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

		c.JSON(http.StatusOK, gin.H{
			"roomId":     created.ID,
			"iceServers": cfg.ICEServers(),
			"capacity":   created.Capacity(cfg.Room.MaxMembers),
		})
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
