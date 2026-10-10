//go:build wireinject
// +build wireinject

package main

import (
	"github.com/google/wire"

	"ProjectionRoom/internal/bootstrap"
	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/service"
)

// InitializeApp 组装整条依赖链并返回可运行的服务（cmd 里没有业务，只有装配与启动）。
//
// 依赖链的**声明**在 internal/bootstrap.ProviderSet：**provider 的书写顺序即创建
// 顺序，而清理按创建顺序的反序执行**，因此"服务先停 → flush 事件写队列 → 关连接池"
// 这段顺序语义与 ProviderSet 的注释放在一起（本文件只负责把它接进来）。
//
// 生成结果 cmd/wire_gen.go 由 `go tool wire ./cmd` 产出，不要手写；
// 改动 ProviderSet 的顺序之后必须重新生成，并逐行核对 wire_gen.go 里 cleanup 的序列。
func InitializeApp(cfg *config.Config) (*service.Server, func(), error) {
	wire.Build(bootstrap.ProviderSet)
	return nil, nil, nil
}
