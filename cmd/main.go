// Command ProjectionRoom 是放映室的信令与房间服务端。
//
// 它只负责信令交换、房间状态与拓扑管理，不传输任何视频字节（SPEC §1.1 目标 7）：
// 视频由主播通过 WebRTC DataChannel 直接分发给其他节点。
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ProjectionRoom/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	engine, cleanup, err := Initialize(cfg)
	if err != nil {
		log.Fatalf("初始化依赖失败: %v", err)
	}
	defer cleanup()

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           engine,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("ProjectionRoom 已启动: http://%s （健康检查 /healthz，信令 /ws）", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP 服务异常退出: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("优雅关闭超时: %v", err)
	}
	log.Println("ProjectionRoom 已停止")
}
