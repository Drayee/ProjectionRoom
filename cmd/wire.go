//go:build wireinject
// +build wireinject

package main

import (
	"github.com/gin-gonic/gin"
	"github.com/google/wire"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/httpapi"
	"ProjectionRoom/internal/room"
	"ProjectionRoom/internal/signal"
)

// ProviderSet 是 M1 的依赖图。
// signal.Hub 实现 room.Broadcaster，用 wire.Bind 把具体类型绑定到接口上。
var ProviderSet = wire.NewSet(
	signal.NewHub,
	wire.Bind(new(room.Broadcaster), new(*signal.Hub)),
	room.NewManager,
	httpapi.NewRouter,
)

// Initialize 由 `go run github.com/google/wire/cmd/wire ./cmd` 生成；
// 生成结果 wire_gen.go 已入库，构建不依赖 wire CLI。
// 修改 ProviderSet 后需要重新生成，或同步修改 wire_gen.go。
func Initialize(cfg *config.Config) (*gin.Engine, func(), error) {
	wire.Build(ProviderSet)
	return nil, nil, nil
}
