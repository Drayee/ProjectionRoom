//go:build !wireinject
// +build !wireinject

package main

import (
	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/httpapi"
	"ProjectionRoom/internal/room"
	"ProjectionRoom/internal/signal"
)

// Initialize 组装依赖图，等价于 wire 的生成结果。
// 手写并入库的原因：构建与 CI 不应强依赖 wire CLI（它在受限 sandbox 下需要额外写权限）。
// 修改 wire.go 的 ProviderSet 时必须同步修改这里，或运行 wire 重新生成后覆盖本文件。
func Initialize(cfg *config.Config) (*gin.Engine, func(), error) {
	hub, cleanup, err := signal.NewHub(cfg)
	if err != nil {
		return nil, nil, err
	}

	rooms := room.NewManager(cfg, hub)
	engine := httpapi.NewRouter(cfg, hub, rooms)

	return engine, cleanup, nil
}
