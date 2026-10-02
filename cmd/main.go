// Command ProjectionRoom 是放映室的信令与房间服务端。
//
// 它只负责信令交换、房间状态与拓扑管理，不传输任何视频字节（SPEC §1.1 目标 7）。
//
// main 只有三步：加载配置 → 组装依赖（wire 生成）→ 运行。
// cmd 里没有任何业务：构造逻辑都在各层的 NewXxx，装配关系在 cmd/wire.go。
package main

import (
	"log"

	"ProjectionRoom/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	app, cleanup, err := InitializeApp(cfg)
	if err != nil {
		log.Fatalf("初始化依赖失败: %v", err)
	}
	defer cleanup()

	if err := app.Run(); err != nil {
		log.Fatalf("运行失败: %v", err)
	}
}
