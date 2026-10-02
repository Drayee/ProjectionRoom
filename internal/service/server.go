package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
)

// shutdownTimeout 是收到终止信号后留给在途请求的关闭时间。
const shutdownTimeout = 5 * time.Second

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

// Run 启动服务并阻塞到收到中断信号，随后优雅关闭。
//
// 这是 cmd 唯一需要调用的启动入口：收信号、超时关闭属于进程生命周期而非业务，
// 所以留在 service 层，让 main 保持"加载配置 → 组装 → 运行"三行。
func (s *Server) Run() error {
	s.Start()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)

	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := s.Shutdown(ctx); err != nil {
		return fmt.Errorf("service: 优雅关闭超时: %w", err)
	}
	log.Println("ProjectionRoom 已停止")

	return nil
}
