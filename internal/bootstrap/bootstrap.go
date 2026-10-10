// Package bootstrap 是 cmd 的**装配层**：把各层的 provider 组成一个可运行的服务。
//
// 为什么它是独立包：cmd 根包只保留三个文件（main.go / wire.go / wire_gen.go），
// 而"装配知识"（provider 顺序、可选依赖、资源关闭配对）需要一个可读的落点。
//
// 依赖方向是**单向**的：
//
//	cmd → bootstrap → {service, handler, store, config, service/auth, service/segment}
//
// 没有反向边（service / handler / store 都不认识 bootstrap），因此不产生循环。
// 本包**只做装配**，不含业务逻辑：每个函数要么构造依赖，要么把依赖打包成
// 消费者（handler）需要的形状。
package bootstrap

import (
	"github.com/google/wire"

	"ProjectionRoom/internal/service"
	"ProjectionRoom/internal/service/segment"
)

// ProviderSet 声明整条依赖链：cmd 里没有业务，只有装配与启动。
//
// 每个依赖都由它所在层的 NewXxx 提供，这里只列装配关系；
// 生成结果 cmd/wire_gen.go 由 `go tool wire ./cmd` 产出，不要手写。
//
// **provider 的书写顺序即创建顺序，而清理按创建顺序的反序执行**，
// 所以这里从"最上层"往"最底层"排：
//
//	NewEngineDeps / NewAdminDeps（消费 DBResources → 让它最后创建、最先清理）
//	  NewEngine → service.NewServer      （服务先停，资源后关）
//	    service.NewManager → service.NewHub / NewBroadcaster
//	      segment.NewQueue
//	        NewAccountService → NewAccountStore / NewSigner / NewTicketStore
//	          NewDBResources（连接 + 迁移 + 事件写入器；最先创建、最后清理）
//
// 账号层（T1–T9）的装配边：
//
//	config → DBResources（连接 + 迁移 + 事件写入器）→ AccountStoreAdapter → AccountService
//	config → auth.Signer / auth.TicketStore ─┤
//	                        │                └→ handler.AuthDeps（含票据表与 rooms_meta 出口）
//	                        └→ /ws 的票据消费（同一个 TicketStore 实例）
//	config → NewLogRingResources（日志环形缓冲；退出时恢复标准输出）
//	AccountService + DBResources + Hub + Manager + LogRing → handler.AdminDeps
//	AuthDeps + AdminDeps → NewEngine → NewRouter（两条依赖都是**参数**，不是包级变量）
//
// 顺序不是风格问题：它决定了生成的 cleanup 链是
//
//	关服务（app.Run 返回）→ 恢复日志输出 → flush 事件写队列 + 关连接池 → 关切片队列 → 关连接池
//
// 里**正确的相对次序**（服务先停、写队列后 flush、连接池最后关）。改动这里的
// 顺序就等于改动退出语义，必须同步核对 cmd/wire_gen.go 里 cleanup 的调用序列。
var ProviderSet = wire.NewSet(
	NewEngineDeps,          // handler 认证/建房侧依赖（钉住 DBResources 的创建顺序）
	NewAdminDeps,           // handler 管理端依赖（同样消费 DBResources）
	NewEngine,              // 注入依赖后的 gin 引擎
	service.NewServer,      // 可运行的服务（自带信号处理与优雅关闭）
	service.NewManager,     // 房间、成员与拓扑用例
	service.NewHub,         // 信令连接池（自带清理函数）
	service.NewBroadcaster, // Hub → service.Broadcaster
	segment.NewQueue,       // 一次性的视频切片作业队列（自带清理函数）
	NewAccountService,      // 账号用例
	NewAccountStore,        // 账号存储适配器（唯一碰 gorm 的一层）
	NewSigner,              // access token 签发器
	NewTicketStore,         // WS 一次性票据表
	NewLogRingResources,    // 日志环形缓冲 + 退出时恢复标准输出
	NewDBResources,         // 数据库连接、迁移与事件写入器（最先创建、最后清理）
)
