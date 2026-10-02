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

// InitializeApp 组装整个应用。
//
// 约定：
//   - 每个依赖都由它所在层的 NewXxx 提供，这里只声明装配关系；
//   - 生成结果 cmd/wire_gen.go **由 `go tool wire ./cmd` 产出，不要手写**；
//   - 依赖方向固定为 handler → usecase → model，基础设施（service）经 wire.Bind 绑到用例的接口上。
func InitializeApp(cfg *config.Config) (*Init, func(), error) {
	wire.Build(
		// 基础设施：信令连接池（自带清理函数）与 HTTP 服务
		service.NewHub,
		wire.Bind(new(usecase.Broadcaster), new(*service.Hub)),
		service.NewServer,

		// 业务用例：房间生命周期、成员、房主控制与拓扑分配
		usecase.NewManager,

		// 接入层：gin 路由与 /ws 处理
		handler.NewRouter,

		// 应用句柄
		NewInit,
	)
	return &Init{}, nil, nil
}
