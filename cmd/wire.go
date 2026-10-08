//go:build wireinject
// +build wireinject

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/wire"

	"ProjectionRoom/internal/auth"
	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/handler"
	"ProjectionRoom/internal/service"
	"ProjectionRoom/internal/service/segment"
	"ProjectionRoom/internal/store"
	"ProjectionRoom/internal/usecase"
)

// 装配期的两类"硬错误"：都表示"配置说账号开启，但装配链断了"，
// 必须拒绝启动（症状若是"所有登录 500"，没人能从日志里看出是装配问题）。
var (
	errConfigMissing         = errors.New("cmd: 装配缺少配置")
	errAccountDepsIncomplete = errors.New("cmd: 账号能力已启用，但 store/signer/ticket 未能装配")
	errAccountStoreNil       = errors.New("cmd: 账号存储适配器为空")
)

// —— 账号层（ACCOUNTS §5/§9）的装配常量。
const (
	// accountOpenTimeout 是启动期连库 + 迁移的总预算。
	// 超时即拒绝启动：带着"连不上库"的账号层跑起来，症状会是"所有登录 500"，
	// 比启动失败难查得多（这也是 config 里 DBDSN 非空即硬校验密钥的同一条理由）。
	accountOpenTimeout = 30 * time.Second

	// writerGracefulClose 是退出时给写队列的 flush 时间（§9 的事件类不可丢）。
	writerGracefulClose = 5 * time.Second

	// writerQueueSize 是事件写队列长度。1024 的依据：账号层的事件是"登录心跳 +
	// 审计"量级（每人每分钟个位数），1024 能吸收一次数据库抖动而不丢事件。
	writerQueueSize = 1024
)

// dbResources 是"连接池 + 事件写入器"这一对必须一起、且按固定顺序关闭的资源。
//
// 为什么要显式配对：writer 必须先 flush 再关连接池（顺序反了写协程只会在一个
// 已关闭的池上重试），而 wires 的 cleanup 执行顺序是**生成代码里的隐式约定**，
// 读 wire.go 看不出来。把顺序写死在一个 return 里，比依赖那个隐式顺序可靠。
type dbResources struct {
	Store  *store.Store
	Writer *store.Writer
}

// newDBResources 打开连接、跑迁移、起事件写入器，并把它们的关闭顺序固定下来。
func newDBResources(cfg *config.Config) (*dbResources, func(), error) {
	if cfg == nil {
		return nil, nil, errConfigMissing
	}
	// PR_DB_DSN 为空 = 账号能力关闭（§5）。此时**不连库、不迁移**，
	// 服务照常以纯游客模式启动 —— 这是 T1 明确要求的渐进上线路径。
	if cfg.Auth.DBDSN == "" {
		log.Printf("[WARN] 未配置 PR_DB_DSN：账号能力已关闭（观众仍可按房间码进房；建房需要账号）")
		return &dbResources{}, func() {}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), accountOpenTimeout)
	defer cancel()

	st, err := store.Open(ctx, cfg.Auth.DBDSN)
	if err != nil {
		return nil, nil, err
	}
	if _, err := st.Migrate(ctx); err != nil {
		_ = st.Close()
		return nil, nil, err
	}
	log.Printf("账号能力已启用：数据库已连接、迁移已补齐")

	w := store.NewWriter(st.DB(), store.WriterOptions{
		QueueSize: writerQueueSize,
		// 事件类不可丢（§9）：给足重试次数，让一次数据库抖动不至于变成数据丢失。
		MaxRetries: 5,
		OnError:    func(err error) { log.Printf("[WARN] %v", err) },
	})

	cleanup := func() {
		if err := w.Close(writerGracefulClose); err != nil {
			log.Printf("[WARN] %v", err)
		}
		if err := st.Close(); err != nil {
			log.Printf("[WARN] 关闭数据库连接池失败：%v", err)
		}
	}
	return &dbResources{Store: st, Writer: w}, cleanup, nil
}

// newAccountStore 构造账号用例的存储适配器（handler 与 usecase 都不 import gorm，
// 翻译只发生在这一层）。
func newAccountStore(res *dbResources) (*usecase.AccountStoreAdapter, error) {
	if res == nil || res.Store == nil {
		// 账号能力关闭（没有库）时返回 nil：可选依赖，由 newAccountService 判空。
		return nil, nil
	}
	return usecase.NewAccountStoreAdapter(res.Store, res.Writer)
}

// newSigner 构造 access token 签发器。
//
// 账号能力关闭时返回 nil（不发 access token 也就没有密钥可信）。
// 密钥缺失/过短时 auth.NewSigner 自己会报错，且 config.Load 已经先拦了一道 ——
// 两道都保留：config 负责"配置层面明确"，这里负责"装配层面不可绕过"。
func newSigner(cfg *config.Config) (*auth.Signer, error) {
	if cfg == nil || cfg.Auth.DBDSN == "" {
		return nil, nil
	}
	return auth.NewSigner(cfg.Auth.JWTSecret, cfg.Auth.AccessTTL)
}

// newTicketStore 构造 WS 一次性票据表（账号能力关闭时返回 nil）。
func newTicketStore(cfg *config.Config) *auth.TicketStore {
	if cfg == nil || cfg.Auth.DBDSN == "" {
		return nil
	}
	return auth.NewTicketStore(cfg.Auth.WSTicketTTL)
}

// newAccountService 构造账号用例（账号能力关闭时返回 nil）。
func newAccountService(
	cfg *config.Config,
	st *usecase.AccountStoreAdapter,
	signer *auth.Signer,
	tickets *auth.TicketStore,
) (*usecase.AccountService, error) {
	if cfg == nil {
		return nil, errConfigMissing
	}
	if cfg.Auth.DBDSN == "" {
		return nil, nil
	}
	if st == nil || signer == nil || tickets == nil {
		// config 说"账号开启"但某条依赖没建出来 —— 装配 bug，必须吵。
		return nil, fmt.Errorf("%w（适配器=%v 签发器=%v 票据表=%v）",
			errAccountDepsIncomplete, st != nil, signer != nil, tickets != nil)
	}
	return usecase.NewAccountService(st, signer, tickets, usecase.AccountOptions{
		BcryptCost: cfg.Auth.BcryptCost,
		RefreshTTL: cfg.Auth.RefreshTTL,
		// 并发口令哈希闸门的容量：取舍见 usecase.DefaultHashingConcurrency 的说明。
		// 走配置（PR_AUTH_HASH_CONCURRENCY）而不是写死默认值：服务器是 2 vCPU，
		// 而单次 bcrypt(cost=12) 实测约 0.57s —— 容量该多大取决于机器能承受多少
		// 并发的 CPU 密集校验，这是部署事实而不是代码事实。
		HashingConcurrency: cfg.Auth.HashingConcurrency,
	})
}

// logRingResources 是"环形日志缓冲 + 它的恢复函数"这一对。
//
// 为什么配对：InstallLogRing 把缓冲 tee 到 log 输出上，退出时必须**恢复**原来的
// 输出（否则测试/同进程的其它 Runner 会把日志写进一个已经被丢弃的缓冲）。
// 与 dbResources 同一条理由：把顺序写死在一个 return 里，比依赖 wire 生成代码里
// 隐式的 cleanup 顺序可靠。
type logRingResources struct {
	Ring *service.LogRing
}

// newLogRingResources 装配管理端的日志缓冲（ACCOUNTS §4/§8：日志不落库、只留最近 N 行）。
//
// 两条取舍：
//
//  1. **容量走 LogRingDefaultCapacity(2000)** 而不是配置项：本期 config 不动
//     （T1 的范围已收口），而 2000 行约几百 KB，是"最近一次故障的现场"的量级；
//  2. **账号能力关闭时不装**（PR_DB_DSN 为空）：管理端整体不注册，
//     就没有任何路径能读到它，此时为每一行日志付一次清洗成本没有收益。
//
// 安装发生在**装配期**（在 engine/server 之前），恢复由 wire 生成的清理链执行。
// 它相对写队列 flush 的位置（cmd/wire_gen.go 里可核对：cleanup4 在 cleanup3 之前）
// 是无害的：flush 期的日志只进 stderr/journalctl，不进缓冲 —— 缓冲的读者是
// 管理端 HTTP，而此刻进程正在退出，没有任何读者。
func newLogRingResources(cfg *config.Config) (*logRingResources, func(), error) {
	if cfg == nil {
		return nil, nil, errConfigMissing
	}
	if cfg.Auth.DBDSN == "" {
		log.Printf("[WARN] 未配置 PR_DB_DSN：管理端关闭（不装日志环形缓冲）")
		return &logRingResources{}, func() {}, nil
	}

	ring := service.NewLogRing(0) // 0 → service.LogRingDefaultCapacity
	restore := service.InstallLogRing(ring)
	log.Printf("管理端已启用：日志环形缓冲容量 %d 行（/api/admin/logs）", service.LogRingDefaultCapacity)
	return &logRingResources{Ring: ring}, func() { restore() }, nil
}

// newAdminDeps 把管理端需要的依赖打包成 handler 的形状（ACCOUNTS §8 / T9）。
//
// 账号能力关闭时返回零值：管理端路由本就不会注册（registerAdminRoutes 会因为
// PR_DB_DSN 为空直接返回），因此这里不需要报错。
//
// **每一个接口字段都显式判空**再赋值。这不是洁癖：把一个 typed nil（例如
// (*store.Writer)(nil)）塞进接口字段会得到一个**非 nil 的接口**，此后
// `if deps.Writer != nil` 恒为真，而调用 w.Stats() 会解引用空指针 panic ——
// 症状是"管理端指标端点 500"，而根因在装配层，极难定位。
func newAdminDeps(
	cfg *config.Config,
	res *dbResources,
	svc *usecase.AccountService,
	hub *service.Hub,
	rooms *usecase.Manager,
	logs *logRingResources,
) (handler.AdminDeps, error) {
	if cfg == nil {
		return handler.AdminDeps{}, errConfigMissing
	}
	if res == nil {
		return handler.AdminDeps{}, errAccountStoreNil
	}
	if cfg.Auth.DBDSN == "" || svc == nil {
		return handler.AdminDeps{}, nil
	}

	deps := handler.AdminDeps{}
	// Users/Writer 来自 dbResources：账号能力开启时它们必然非 nil（见 newDBResources）。
	if res.Store != nil {
		deps.Users = res.Store
	}
	if res.Writer != nil {
		deps.Writer = res.Writer
	}
	deps.Accounts = svc
	if hub != nil {
		deps.Hub = hub
	}
	if rooms != nil {
		deps.Rooms = rooms
	}
	if logs != nil && logs.Ring != nil {
		deps.Logs = logs.Ring
	}
	return deps, nil
}

// newEngineDeps 把账号依赖打包成 handler 需要的形状（账号能力关闭时 Service 为 nil）。
//
// 它同时是**编译期存在性证明**：本函数消费 *dbResources，于是 dbResources 一定在
// 消费者链（authDeps / adminDeps → engine → server）之前被创建；而 wire 的 cleanup
// 按创建顺序的**反序**执行，且 main 是在 app.Run() 返回**之后**才调用聚合 cleanup。
// 两点合起来给出这里真正要的顺序：
//
//	服务先停（app.Run 返回）→ 再 flush 事件写队列 → 最后关连接池
//
// 反过来（连接池先关、写协程后关）会让退出时队列里的事件全部失败重试 ——
// 那正好违反 §9 的"事件类不可丢"。wire 的 cleanup 顺序在生成代码里是隐式的，
// 所以这里用一个显式的消费者把它钉住（并可在 cmd/wire_gen.go 里逐行核对），
// 而不是靠运气。
func newEngineDeps(
	cfg *config.Config,
	res *dbResources,
	svc *usecase.AccountService,
	tickets *auth.TicketStore,
) (handler.AuthDeps, error) {
	if cfg == nil {
		return handler.AuthDeps{}, errConfigMissing
	}
	if res == nil {
		return handler.AuthDeps{}, errAccountStoreNil
	}
	deps := handler.AuthDeps{
		Session: handler.SessionConfig{
			AccessTTL:   cfg.Auth.AccessTTL,
			RefreshTTL:  cfg.Auth.RefreshTTL,
			WSTicketTTL: cfg.Auth.WSTicketTTL,
		},
		Store:  res.Store,
		Writer: res.Writer,
	}
	if svc != nil {
		deps.Service = svc
	}
	// 票据表与 /api/auth/ws-ticket 用的必须是**同一个实例**（wire 会复用同一个
	// provider 的返回值），否则签发的票在 /ws 上永远查不到。
	if tickets != nil {
		deps.Tickets = tickets
	}
	// 建房时的 rooms_meta 异步投递（T8）。返回 nil 接口表示"没有写队列"，
	// 建房路径会据此只记一条日志（元数据缺失不影响开播）。
	deps.RoomMeta = handler.NewRoomMetaSink(res.Store, res.Writer)
	return deps, nil
}

// newEngine 是全图里唯一的"可选依赖"装配点。
//
// 它做一件事：把账号与管理端的依赖**作为参数**交给 handler.NewRouter
// （T8/T9 的收口：这些依赖以前经包级变量注入，无法在测试里复位）。
//
// 账号能力关闭时 Service 为 nil：路由本就不会注册（registerAccountRoutes 会因
// cfg.Auth.DBDSN 为空而直接返回），管理端同理，因此这里不需要判空也不该报错。
func newEngine(
	cfg *config.Config,
	hub *service.Hub,
	rooms *usecase.Manager,
	seg *segment.Queue,
	accountDeps handler.AuthDeps,
	adminDeps handler.AdminDeps,
) (*gin.Engine, error) {
	return handler.NewRouter(cfg, hub, rooms, seg, accountDeps, adminDeps), nil
}

// InitializeApp 声明整条依赖链：cmd 里没有业务，只有装配与启动。
//
// 每个依赖都由它所在层的 NewXxx 提供，这里只列装配关系；
// 生成结果 cmd/wire_gen.go 由 `go tool wire ./cmd` 产出，不要手写。
//
// **provider 的书写顺序即创建顺序，而清理按创建顺序的反序执行**，
// 所以这里从"最上层"往"最底层"排：
//
//	newEngineDeps / newAdminDeps（消费 dbResources → 让它最后创建、最先清理）
//	  newEngine → service.NewServer      （服务先停，资源后关）
//	    usecase.NewManager → service.NewHub / NewBroadcaster
//	      segment.NewQueue
//	        newAccountService → newAccountStore / newSigner / newTicketStore
//	          newDBResources（连接 + 迁移 + 事件写入器；最先创建、最后清理）
//
// 账号层（T1–T9）新增的装配边：
//
//	config → dbResources（连接 + 迁移 + 事件写入器）→ AccountStoreAdapter → AccountService
//	config → auth.Signer / auth.TicketStore ─┤
//	                        │                └→ handler.AuthDeps（含票据表与 rooms_meta 出口）
//	                        └→ /ws 的票据消费（同一个 TicketStore 实例）
//	config → newLogRingResources（日志环形缓冲；退出时恢复标准输出）
//	AccountService + dbResources + Hub + Manager + LogRing → handler.AdminDeps
//	AuthDeps + AdminDeps → newEngine → NewRouter（两条依赖都是**参数**，不是包级变量）
func InitializeApp(cfg *config.Config) (*service.Server, func(), error) {
	wire.Build(
		newEngineDeps,          // handler 认证/建房侧依赖（钉住 dbResources 的创建顺序）
		newAdminDeps,           // handler 管理端依赖（同样消费 dbResources）
		newEngine,              // 注入依赖后的 gin 引擎
		service.NewServer,      // 可运行的服务（自带信号处理与优雅关闭）
		usecase.NewManager,     // 房间、成员与拓扑用例
		service.NewHub,         // 信令连接池（自带清理函数）
		service.NewBroadcaster, // Hub → usecase.Broadcaster
		segment.NewQueue,       // 一次性的视频切片作业队列（自带清理函数）
		newAccountService,      // 账号用例
		newAccountStore,        // 账号存储适配器（唯一碰 gorm 的一层）
		newSigner,              // access token 签发器
		newTicketStore,         // WS 一次性票据表
		newLogRingResources,    // 日志环形缓冲 + 退出时恢复标准输出
		newDBResources,         // 数据库连接、迁移与事件写入器（最先创建、最后清理）
	)
	return nil, nil, nil
}
