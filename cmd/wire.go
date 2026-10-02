//go:build wireinject
// +build wireinject

package main

import (
	"github.com/google/wire"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/handler"
	"ProjectionRoom/internal/service"
	"ProjectionRoom/internal/usecase"
)

// InitializeApp 声明整条依赖链：cmd 里没有业务，只有装配与启动。
//
// 每个依赖都由它所在层的 NewXxx 提供，这里只列装配关系；
// 生成结果 cmd/wire_gen.go 由 `go tool wire ./cmd` 产出，不要手写。
func InitializeApp(cfg *config.Config) (*service.Server, func(), error) {
	wire.Build(
		service.NewHub,         // 信令连接池（自带清理函数）
		service.NewBroadcaster, // Hub → usecase.Broadcaster
		usecase.NewManager,     // 房间、成员与拓扑用例
		handler.NewRouter,      // gin 路由与 /ws
		service.NewServer,      // 可运行的服务（自带信号处理与优雅关闭）
	)
	return nil, nil, nil
}
