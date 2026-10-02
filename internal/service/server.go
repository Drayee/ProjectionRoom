package service

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
)

// Server 是 HTTP 服务的生命周期封装。
// 它把"起服务 / 收信号 / 优雅关闭"从 main 里搬出来，
// main 因此只剩"加载配置 → 组装 → 运行"三行。
type Server struct {
	cfg  *config.Config
	http *http.Server
}

// NewServer 是 wire 的提供者：吃配置与路由，吐出可运行的服务。
func NewServer(cfg *config.Config, engine *gin.Engine) (*Server, error) {
	if cfg == nil || engine == nil {
		return nil, errors.New("service: NewServer 需要非空的配置与路由引擎")
	}

	return &Server{
		cfg: cfg,
		http: &http.Server{
			Addr:              cfg.Addr,
			Handler:           engine,
			ReadHeaderTimeout: 10 * time.Second,
		},
	}, nil
}

// Addr 返回监听地址（日志用）。
func (s *Server) Addr() string { return s.cfg.Addr }

// Start 在后台启动监听。
// 启动期错误没有恢复手段，直接记日志并退出进程 —— 掩盖它只会让问题更难查。
func (s *Server) Start() {
	go func() {
		log.Printf("ProjectionRoom 已启动: http://%s（健康检查 /healthz，信令 /ws）", s.cfg.Addr)
		if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP 服务异常退出: %v", err)
		}
	}()
}

// Shutdown 优雅关闭。
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}
