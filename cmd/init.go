package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ProjectionRoom/internal/service"
)

// Init 是装配完成后的应用句柄。
//
// 这一层是 wire 图与 main 之间的唯一交点：main 只依赖 Init，
// 各层之间的依赖关系全部写在 cmd/wire.go 的 ProviderSet 里。
type Init struct {
	Server *service.Server
}

// NewInit 由 wire 调用（见 cmd/wire.go）。
func NewInit(server *service.Server) *Init {
	return &Init{Server: server}
}

// Run 启动服务并阻塞到收到终止信号。
func (i *Init) Run() error {
	if i.Server == nil {
		return fmt.Errorf("Init: 服务未装配")
	}

	i.Server.Start()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := i.Server.Shutdown(ctx); err != nil {
		return fmt.Errorf("优雅关闭超时: %w", err)
	}
	log.Println("ProjectionRoom 已停止")

	return nil
}
