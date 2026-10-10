package bootstrap

import (
	"log"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/handler"
	"ProjectionRoom/internal/service"
)

// —— 管理端的装配（日志环形缓冲 + handler.AdminDeps）——
//
// 两条取舍与账号能力开关（cfg.Auth.DBDSN）一致：DSN 为空时管理端整体不注册，
// 因此这里既不需要装日志缓冲，也不需要报错。

// LogRingResources 是"环形日志缓冲 + 它的恢复函数"这一对。
//
// 为什么配对：service.InstallLogRing 把缓冲 tee 到 log 输出上，退出时必须**恢复**
// 原来的输出（否则测试/同进程的其它 Runner 会把日志写进一个已经被丢弃的缓冲）。
// 与 DBResources 同一条理由：把顺序写死在一个 return 里，比依赖 wire 生成代码里
// 隐式的 cleanup 顺序可靠。
type LogRingResources struct {
	Ring *service.LogRing
}

// NewLogRingResources 装配管理端的日志缓冲（ACCOUNTS §4/§8：日志不落库、只留最近 N 行）。
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
func NewLogRingResources(cfg *config.Config) (*LogRingResources, func(), error) {
	if cfg == nil {
		return nil, nil, errConfigMissing
	}
	if cfg.Auth.DBDSN == "" {
		log.Printf("[WARN] 未配置 PR_DB_DSN：管理端关闭（不装日志环形缓冲）")
		return &LogRingResources{}, func() {}, nil
	}

	ring := service.NewLogRing(0) // 0 → service.LogRingDefaultCapacity
	restore := service.InstallLogRing(ring)
	log.Printf("管理端已启用：日志环形缓冲容量 %d 行（/api/admin/logs）", service.LogRingDefaultCapacity)
	return &LogRingResources{Ring: ring}, func() { restore() }, nil
}

// NewAdminDeps 把管理端需要的依赖打包成 handler 的形状（ACCOUNTS §8 / T9）。
//
// 账号能力关闭时返回零值：管理端路由本就不会注册（registerAdminRoutes 会因为
// PR_DB_DSN 为空直接返回），因此这里不需要报错。
//
// **每一个接口字段都显式判空**再赋值。这不是洁癖：把一个 typed nil（例如
// (*store.Writer)(nil)）塞进接口字段会得到一个**非 nil 的接口**，此后
// `if deps.Writer != nil` 恒为真，而调用 w.Stats() 会解引用空指针 panic ——
// 症状是"管理端指标端点 500"，而根因在装配层，极难定位。
func NewAdminDeps(
	cfg *config.Config,
	res *DBResources,
	svc *service.AccountService,
	hub *service.Hub,
	rooms *service.Manager,
	logs *LogRingResources,
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
	// Users/Writer 来自 DBResources：账号能力开启时它们必然非 nil（见 NewDBResources）。
	if res.Store != nil {
		deps.Users = res.Store
		// 二次（T4）：管理端房间列表/强关/下架要写的元数据，以及审计读取。
		// 两者都由 *store.Store 满足（方法集逐字匹配 handler 的窄接口）。
		deps.RoomMeta = res.Store
		deps.Audit = res.Store
		deps.AuditWriter = res.Store
	}
	// 审计的异步投递出口：service.WriterJobs 同时满足 AdminWriterStats（指标）
	// 与 AdminAuditSink（无 gorm 参数的投递），装配处因此只需写一次。
	if jobs := service.NewWriterJobs(res.Writer); jobs != nil {
		deps.Writer = jobs
		deps.AuditSink = jobs
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
