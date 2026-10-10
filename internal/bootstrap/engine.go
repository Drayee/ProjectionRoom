package bootstrap

import (
	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/handler"
	"ProjectionRoom/internal/service"
	"ProjectionRoom/internal/service/auth"
	"ProjectionRoom/internal/service/segment"
)

// —— handler 依赖与 gin 引擎的装配 ——
//
// NewEngineDeps 是"账号能力关闭时 Service 为 nil"的那条可选依赖路径；
// NewEngine 是全图里唯一的可选依赖装配点。

// NewEngineDeps 把账号依赖打包成 handler 需要的形状（账号能力关闭时 Service 为 nil）。
//
// 它同时是**编译期存在性证明**：本函数消费 *DBResources，于是 DBResources 一定在
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
func NewEngineDeps(
	cfg *config.Config,
	res *DBResources,
	svc *service.AccountService,
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
	// 二次（T2）：公开房列表取房主昵称的**窄出口**（只返回一个 string）。
	// nil 表示"查不了昵称"，列表会把 ownerName 显示成空串 —— 与"账号能力关闭"
	// 时的降级路径是同一个方向，因此不需要在这里判错。
	if names := service.NewProfileNameLookup(res.Store); names != nil {
		deps.ProfileNames = names
	}
	return deps, nil
}

// NewEngine 是全图里唯一的"可选依赖"装配点。
//
// 它做一件事：把账号与管理端的依赖**作为参数**交给 handler.NewRouter
// （T8/T9 的收口：这些依赖以前经包级变量注入，无法在测试里复位）。
//
// 账号能力关闭时 Service 为 nil：路由本就不会注册（registerAccountRoutes 会因
// cfg.Auth.DBDSN 为空而直接返回），管理端同理，因此这里不需要判空也不该报错。
func NewEngine(
	cfg *config.Config,
	hub *service.Hub,
	rooms *service.Manager,
	seg *segment.Queue,
	accountDeps handler.AuthDeps,
	adminDeps handler.AdminDeps,
) (*gin.Engine, error) {
	return handler.NewRouter(cfg, hub, rooms, seg, accountDeps, adminDeps), nil
}
