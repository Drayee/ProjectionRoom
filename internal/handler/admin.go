// 管理端 REST 端点（ACCOUNTS §8 / T9）。
//
// 一期只做**API 与权限边界**：前端 /admin 视图属二期，本期验收口径是 curl 与
// httptest（见 docs/plans/2026-10-08-accounts-phase1.md 的 T9）。
//
// 三条本文件负责的不变量：
//
//  1. **授权 100% 在服务端**：整组路由挂在 RequireAuth + RequireAdmin 之下，
//     前端有没有把按钮画出来与本层无关（不变量 I3）。
//  2. **响应体里永不出现 PasswordHash**：所有用户数据都经 usecase.ProfileOf 出口，
//     而 Profile 类型里根本没有那个字段（类型系统比"记得别写"可靠）。
//  3. **写操作有审计**：封禁/解禁/改角色的审计由 usecase（T6）在同一个调用里投递
//     （actor/target/action/detail/ip 齐全）。本层**不再写第二份** ——
//     同一动作两行审计不是"更安全"，而是让审计表里出现两条互相矛盾的事实。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/model"
	"ProjectionRoom/internal/service"
	"ProjectionRoom/internal/store"
	"ProjectionRoom/internal/usecase"
)

// 分页口径与 store.clampPage 一致（默认 50、上限 200）。
//
// 为什么这一层也要夹一遍：store 的夹取是"最后一道"，而本层要先**回显**生效值
// （响应里带 limit/offset），两者口径不同就会出现"响应说 200、实际查了 500"。
const (
	defaultAdminPageLimit = 50
	maxAdminPageLimit     = 200
)

// —— 窄接口：每个依赖都能在单测里换成假实现（T9 的验收方式就是 httptest）。
//
// 为什么全部用接口而不是 *store.Store / *service.Hub：
// 管理端的一期验收里有一半是"权限与协议面"判据（401/403/200、字段白名单、
// 封禁是否真的断连），它们必须在**没有 PostgreSQL、没有真实 WebSocket** 的
// 机器上可判定。接口面同时是一份"管理端能对这个系统做什么"的清单。

// AdminUserStore 是管理端读用户的能力（*store.Store 的方法集满足它）。
type AdminUserStore interface {
	ListUsers(ctx context.Context, q store.UserQuery) ([]store.User, int64, error)
	UserByID(ctx context.Context, id int64) (*store.User, error)
}

// AdminAccountWriter 是管理端的写操作（*usecase.AccountService 的方法集满足它）。
//
// 三个方法都**自带审计投递**（T6）：本层调它们就等于"改状态 + 写审计"两件事都做了。
type AdminAccountWriter interface {
	BanUser(ctx context.Context, actorID, targetID int64, reason, ip string) error
	UnbanUser(ctx context.Context, actorID, targetID int64, ip string) error
	SetRole(ctx context.Context, actorID, targetID int64, role, ip string) error
}

// AdminHub 是连接池里管理端要用的能力（*service.Hub 满足它）。
//
// CloseByUser 是封禁的第二件事（ACCOUNTS §5）：只改 status 的话，
// 已经建立的长连接仍然是"已授权"的，它还能继续进房、发信令、留在房间里。
//
// CloseRoom 是**强制关闭房间**的第二件事（T4）：usecase 的关房路径已经通过
// Broadcaster 广播了 room-closed 并调用了 bus.CloseRoom（生产装配里 Broadcaster
// 就是本 Hub），这里再显式调一次是**装配期的防御**：只要 deps.Hub 与 Manager 的
// Broadcaster 指向同一个连接池（生产与测试都是），重复调用是幂等的空操作
// （房间索引已被清空）；若两者曾指向不同实例，这一行就是"房内连接真的被断开"
// 的唯一保证。Hub.CloseRoom 的幂等性见 service/hub.go。
type AdminHub interface {
	Stats() (connections, distinctUsers int)
	CloseByUser(userID int64) int
	CloseRoom(roomID string)
}

// AdminRoomStats 是房间实况的只读/受控写能力（*usecase.Manager 满足它）。
//
// 为什么把 CloseRoomByAdmin / SetRoomMeta 也放在这里，而不是新开一个"管理员房间写"接口：
// 它们与 RoomCount/ListRooms 的**owner 完全相同**（内存里的房间表），
// 拆成两个接口只能表达"同一个对象上的两组方法"，却会让装配处必须两次传同一个 Manager
// （那正是"两个接口指向不同实例"这类装配错的温床）。
type AdminRoomStats interface {
	RoomCount() int
	ListRooms() []usecase.RoomSnapshot
	// CloseRoomByAdmin 是 closeRoom 的受控导出（见 usecase 的说明）：强制关闭房间。
	CloseRoomByAdmin(roomID, reason string) error
	// SetRoomMeta 在内存侧复核"房间还在、调用者是房主"。管理端下架时会拿到
	// ErrNotRoomOwner（管理员通常不是房主），调用方按"库侧那一份才是权威"处理。
	SetRoomMeta(roomID string, actorUserID int64, title *string, isPublic *bool) error
}

// AdminRoomLister 是房间元数据的分页查询能力（*store.Store 满足它）。
//
// 管理端房间列表 = 内存快照 ∪ DB 元数据（T4 明确要求**含未公开**的房间），
// 所以这里要的是"能带着搜索/公开性筛选翻页查 rooms_meta"，
// 而不是公开列表那条"按 id 批量取"的路径。
type AdminRoomLister interface {
	QueryRoomMetas(ctx context.Context, q store.RoomMetaQuery) ([]store.RoomMeta, int64, error)
}

// AdminAuditSource 是审计行的分页读出口（*store.Store 满足它）。
type AdminAuditSource interface {
	ListAudit(ctx context.Context, limit, offset int) ([]store.AdminAudit, error)
}

// AdminAuditWriter 是审计的写入出口（*store.Store 满足它）。
//
// 只有 InsertAudit：读（ListAudit）在另一个接口里，这样"哪些端点会写审计"
// 能从 deps 的字段名上一眼看出。
type AdminAuditWriter interface {
	InsertAudit(ctx context.Context, a *store.AdminAudit) error
}

// AdminAuditSink 是审计的**异步投递**出口（*usecase.WriterJobs 满足它）。
//
// 为什么签名里没有 gorm 类型：本层从一期起就不 import gorm（见 errors.go 里
// storeNotFound 别名的说明），因此不能用 store.Writer.Submit 的原签名
// （它是 func(ctx, *gorm.DB) error）。这里收的是"只吃 context"的作业，
// 由 usecase.SubmitWriterJob 在**已经 import gorm 的那一层**做翻译 ——
// 与建房路径投递 rooms_meta 走的是同一条路（见 router.go 的 roomMetaSink）。
type AdminAuditSink interface {
	SubmitJob(fn func(ctx context.Context) error) bool
}

// AdminRoomMetaWriter 是房间管理动作需要的全部元数据能力（*store.Store 满足它）。
//
// 它**内嵌** PublicRoomMetaStore（公开房列表在同一个 store 上的那个端口，
// 见 publicroom.go），于是"handler 能对 rooms_meta 做什么"只有一处声明：
// 装配处不可能出现"公开列表与管理端指向两个不同的元数据源"这种装配错 ——
// 那会让两个视图对同一个房间给出不同的公开性。
//
// 额外加的三个方法的分工：
//   - QueryRoomMetas   管理端的分页/搜索/公开性筛选（公开列表用的是 ListRoomMetas 那条按 id 批量取的路径）；
//   - UpdateRoomMeta   局部改动（强关只补 closed_at）；
//   - UpsertRoomMeta 已经在内嵌接口里：下架要同时表达"is_public=false"与
//     "清空 closed_at"，而 patch 语义表达不了"置 NULL"，所以下架走 upsert。
type AdminRoomMetaWriter interface {
	PublicRoomMetaStore
	AdminRoomLister
	UpdateRoomMeta(ctx context.Context, roomID string, patch store.RoomMetaPatch) error
}

// AdminWriterStats 是事件写队列的指标（*store.Writer 满足它，§9 的可观测要求）。
type AdminWriterStats interface {
	QueueLen() int
	Stats() store.WriterStats
}

// AdminLogSource 是服务端日志的环形缓冲（*service.LogRing 满足它）。
//
// Snapshot 的返回顺序是**新 → 旧**（见 service.LogRing.Snapshot），
// 本层只做分页透传，不再排序 —— 顺序是数据源的事实，不是展示层的选择。
type AdminLogSource interface {
	Snapshot(limit, offset int) ([]string, int64)
	Len() int
	Stats() (total, dropped int64)
}

// AdminDeps 是管理端的全部依赖（全部可选：装配缺失时对应端点降级而不是崩）。
type AdminDeps struct {
	Users    AdminUserStore
	Accounts AdminAccountWriter
	Hub      AdminHub
	Rooms    AdminRoomStats
	Writer   AdminWriterStats
	Logs     AdminLogSource

	// —— T4（房间管理与审计）新增的出口。
	//
	// RoomMeta 一个字段同时承担"列表读"（内嵌的 AdminRoomLister）与"强关/下架写"：
	// 两者是同一个 *store.Store 上的两组方法，拆成两个字段只会让装配处多一次
	// 传同一个实例的机会（见 AdminRoomMetaWriter 的说明）。
	RoomMeta AdminRoomMetaWriter
	// Audit 是审计的**读**出口（GET /api/admin/audit）。
	Audit AdminAuditSource
	// AuditSink 是审计的**异步写**出口（强关/下架）。它与 Writer 指向同一个写入器，
	// 但两个字段各有各的类型：Writer 只承诺指标（metrics 端点），
	// AuditSink 只承诺投递（写路径）。这样"某个端点用了写队列的哪一面"是可读的。
	AuditSink AdminAuditSink
	// AuditWriter 是审计行的**写**能力（*store.Store）。
	AuditWriter AdminAuditWriter
}

// registerAdminRoutes 注册管理端路由（全部 RequireAdmin）。
//
// 与 registerAccountRoutes 同一套装配约定：
//   - cfg.Auth.DBDSN 为空 = 账号能力关闭 → 管理端**整体不注册**（路径不存在，
//     而不是存在一个永远 500 的端点）；
//   - DSN 配了但依赖没装配出来 → 一组一律 503 的处理器（比"路由凭空消失"好定位）；
//   - 权限闸门是 RequireAuth + RequireAdmin，**不是**前端 v-if。
//
// auth 是 RequireAuth 需要的账号服务（与认证路由用的是**同一个**实例，
// 由 NewRouter 从 accountDeps 传进来）：它必须与 deps.Accounts 指向同一个对象，
// 否则会出现"用 A 校验 token、用 B 写状态"这种看不出症状的装配错。
func registerAdminRoutes(api *gin.RouterGroup, deps AdminDeps, cfg *config.Config, auth AccountService) {
	if api == nil {
		return
	}
	if cfg == nil || strings.TrimSpace(cfg.Auth.DBDSN) == "" {
		return
	}

	if deps.Accounts == nil || deps.Users == nil {
		log.Printf("[WARN] 已配置 PR_DB_DSN 但管理端依赖未装配：/api/admin/* 一律返回 503")
		registerAdminUnavailable(api)
		return
	}

	h := &adminHandler{deps: deps}
	admin := api.Group("/admin", RequireAuth(auth), RequireAdmin())
	admin.GET("/users", h.listUsers)
	admin.PATCH("/users/:id", h.patchUser)
	admin.GET("/metrics", h.metrics)
	// 一期自报的缺口：日志端点是"把服务端日志整段交出去"的读取入口，
	// 没有限速时一个管理员 token 就能把它变成日志导出器。
	// 复用与登录/建房同一条令牌桶实现（internal/limiters），只是换了一组参数。
	admin.GET("/logs", newKeyedLimiter(cfg.IPC.AdminLogsPerMinute, cfg.IPC.AdminLogsBurst, "管理端日志"), h.logs)

	// T4：房间管理与审计。
	admin.GET("/rooms", h.listRooms)
	admin.POST("/rooms/:id/close", h.closeRoom)
	admin.POST("/rooms/:id/unpublish", h.unpublishRoom)
	admin.GET("/audit", h.listAudit)
}

// registerAdminUnavailable 注册一组"管理端暂时不可用"的处理器（全部 503）。
func registerAdminUnavailable(api *gin.RouterGroup) {
	unavailable := func(c *gin.Context) {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
			"error": "管理端暂时不可用（服务端未完成装配）",
			"code":  CodeAuthUnavailable,
		})
	}
	api.GET("/admin/users", unavailable)
	api.PATCH("/admin/users/:id", unavailable)
	api.GET("/admin/metrics", unavailable)
	api.GET("/admin/logs", unavailable)
	api.GET("/admin/rooms", unavailable)
	api.POST("/admin/rooms/:id/close", unavailable)
	api.POST("/admin/rooms/:id/unpublish", unavailable)
	api.GET("/admin/audit", unavailable)
}

// adminHandler 持有依赖，把每个端点做成方法。
type adminHandler struct {
	deps AdminDeps
}

// —— 请求/响应体。

// adminUserPatch 是 PATCH /api/admin/users/:id 的请求体。
//
// 用**指针**字段区分"没传"与"传了空串"：role="" 是客户端 bug（应当 400），
// 而不传 role 是"这次不改角色"。没有这个区分就得靠"零值即忽略"，那会让
// 一次笔误静默变成 no-op。
type adminUserPatch struct {
	Role   *string `json:"role"`
	Status *string `json:"status"`
	Reason string  `json:"reason"`
}

// listUsers 返回分页的用户列表（成功 200）。
//
// 响应形状 {items, total} 与 §10 对齐；额外的 limit/offset 是**生效值**回显
// （客户端不必再猜自己传的 1000 被夹到了多少）。
func (h *adminHandler) listUsers(c *gin.Context) {
	limit, err := adminPageLimit(c.Query("limit"))
	if err != nil {
		adminBadRequest(c, err.Error())
		return
	}
	offset, err := adminPageOffset(c.Query("offset"))
	if err != nil {
		adminBadRequest(c, err.Error())
		return
	}

	users, total, err := h.deps.Users.ListUsers(c.Request.Context(), store.UserQuery{
		Search: strings.TrimSpace(c.Query("search")),
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		adminInternalError(c, "查询用户列表失败", err)
		return
	}

	items := make([]usecase.Profile, 0, len(users))
	for i := range users {
		// ProfileOf 是**唯一出口**：PasswordHash 不在 Profile 的字段里，
		// 因此"忘了剔除"这件事在类型层面就不可能发生。
		items = append(items, usecase.ProfileOf(&users[i]))
	}
	c.JSON(http.StatusOK, gin.H{
		"items":  items,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// patchUser 改角色 / 封禁解禁（成功 200，返回改动后的档案）。
//
// 顺序是有意的：
//
//	先改角色（若指定）→ 再改状态（若指定）→ 最后回读返回。
//
// 每一步都是**独立的写操作**，各自写自己的审计行（user.role / user.ban / user.unban），
// 因此一次性传 role+status 会得到两行审计 —— 那正是"改了两件事"的如实记录。
//
// 封禁的**两件事**都在这里发生：usecase.BanUser（status=banned + token_version+1，
// 让已签发的 access token 立刻失效）**以及** hub.CloseByUser（断开在跑的长连接）。
// 只做前者会留下"被封禁的连接继续发信令"的窗口。
func (h *adminHandler) patchUser(c *gin.Context) {
	actor, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}
	targetID, ok := adminPathID(c, "id")
	if !ok {
		return
	}

	var req adminUserPatch
	if err := c.ShouldBindJSON(&req); err != nil {
		adminBadRequest(c, "请求体不是合法 JSON")
		return
	}
	if req.Role == nil && req.Status == nil {
		adminBadRequest(c, "至少要指定 role 或 status 之一")
		return
	}

	var role, status string
	if req.Role != nil {
		role = strings.ToLower(strings.TrimSpace(*req.Role))
		if role != store.RoleUser && role != store.RoleAdmin {
			adminBadRequest(c, "role 只能是 user 或 admin")
			return
		}
	}
	if req.Status != nil {
		status = strings.ToLower(strings.TrimSpace(*req.Status))
		if status != store.StatusActive && status != store.StatusBanned {
			adminBadRequest(c, "status 只能是 active 或 banned")
			return
		}
	}

	ctx := c.Request.Context()
	ip := c.ClientIP()

	if req.Role != nil {
		if err := h.deps.Accounts.SetRole(ctx, actor.ID, targetID, role, ip); err != nil {
			h.writeWriteError(c, err)
			return
		}
	}
	if req.Status != nil {
		if status == store.StatusBanned {
			// reason 只在封禁时有意义（解禁会清空 banned_reason，见 store.SetUserStatus），
			// 因此在其它分支里它被忽略而不是报 400 —— 它属于同一个 patch 信封，
			// 而不是一个"用错就整条请求失败"的独立参数。
			if err := h.deps.Accounts.BanUser(ctx, actor.ID, targetID, req.Reason, ip); err != nil {
				h.writeWriteError(c, err)
				return
			}
			h.closeUserConnections(targetID)
		} else {
			if err := h.deps.Accounts.UnbanUser(ctx, actor.ID, targetID, ip); err != nil {
				h.writeWriteError(c, err)
				return
			}
		}
	}

	// 回读：返回"改动后的事实"而不是"我们请求改成的样子"（两者不一致时，
	// 前者才是真的，而管理端面板显示的就是这个值）。
	u, err := h.deps.Users.UserByID(ctx, targetID)
	if err != nil {
		if errors.Is(err, usecase.StoreNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "账号不存在", "code": model.CodeBadRequest})
			return
		}
		adminInternalError(c, "回读账号失败", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"user": usecase.ProfileOf(u)})
}

// closeUserConnections 是封禁的第二件事（ACCOUNTS §5）：断开该账号的全部长连接。
//
// 为什么不等下一次请求：长连接**已经**通过了授权，服务端的 token_version 比对
// 只在新请求上发生。不主动断连的话，被封禁的账号可以一直留在房间里发信令。
//
// Hub 缺失（装配缺环）时只记日志：封禁本身（状态 + 版本号）已经落库，
// 而那才是"下次校验会拦住他"的依据；这里漏掉的只是"当前这条连接的即时性"。
func (h *adminHandler) closeUserConnections(userID int64) {
	if h.deps.Hub == nil {
		log.Printf("[WARN] 封禁 user=%d 完成，但装配里没有连接池：其在线连接不会被立刻断开", userID)
		return
	}
	n := h.deps.Hub.CloseByUser(userID)
	log.Printf("管理端封禁 user=%d：已以 1008 断开 %d 条在线连接", userID, n)
}

// metrics 返回运行指标（成功 200）。
//
// 四个来源各自独立：Hub（连接）、Manager（房间）、Writer（事件写队列）、LogRing（日志）。
// 任一依赖缺失时**只是少一个字段**，而不是整个端点 500 —— 管理端指标是排障入口，
// 它在装配残缺时更需要能用。
func (h *adminHandler) metrics(c *gin.Context) {
	resp := gin.H{}
	if h.deps.Hub != nil {
		connections, onlineUsers := h.deps.Hub.Stats()
		resp["connections"] = connections
		// onlineUsers 只统计带账号的连接（游客连接没有账号，不参与"在线用户"）。
		resp["onlineUsers"] = onlineUsers
	}
	if h.deps.Rooms != nil {
		resp["rooms"] = h.deps.Rooms.RoomCount()
	}
	if h.deps.Writer != nil {
		s := h.deps.Writer.Stats()
		resp["writeQueue"] = gin.H{
			// pending 是**瞬时**队列长度（gauge），其余四个是进程生命周期内的累计计数。
			// 混成一类会让"队列积压"这个最关键的信号被累计值掩盖。
			"pending":       h.deps.Writer.QueueLen(),
			"queued":        s.Queued,
			"dropped":       s.Dropped,
			"succeeded":     s.Succeeded,
			"failed":        s.Failed,
			"lastLatencyMs": s.LastLatency.Milliseconds(),
		}
	}
	if h.deps.Logs != nil {
		total, dropped := h.deps.Logs.Stats()
		resp["logs"] = gin.H{
			"kept":    h.deps.Logs.Len(),
			"total":   total,
			"dropped": dropped,
		}
	}
	c.JSON(http.StatusOK, resp)
}

// logs 返回服务端日志的环形缓冲分页（成功 200）。
//
// 字段白名单（§8 的要求）：响应里只有**文本行数组**。
// 这一条不是"少给点数据"，而是把"日志就是一段要给人看的文本"钉死在协议上：
// 客户端因此没有机会把日志里的用户可控文本当成结构去解析或渲染。
// 另外日志行已经由 LogRing 在写入侧去过控制符（含 \r —— 它能伪造整行日志），
// 前端只需文本插值。
//
// 顺序：**新 → 旧**（offset=0 是最新一行），与 service.LogRing.Snapshot 一致。
func (h *adminHandler) logs(c *gin.Context) {
	if h.deps.Logs == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "日志缓冲未启用",
			"code":  CodeAuthUnavailable,
		})
		return
	}
	limit, err := adminPageLimit(c.Query("limit"))
	if err != nil {
		adminBadRequest(c, err.Error())
		return
	}
	offset, err := adminPageOffset(c.Query("offset"))
	if err != nil {
		adminBadRequest(c, err.Error())
		return
	}

	lines, total := h.deps.Logs.Snapshot(limit, offset)
	c.JSON(http.StatusOK, gin.H{
		"lines":  lines,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// —— 房间管理（T4 / §8：房间列表含未公开 + 强制关闭 + 下架）——

// adminRoomItem 是管理端房间列表的**一行**。
//
// 与公开房列表（publicroom.publicRoomItem）的关键差别：这里**含未公开的房间**，
// 并且额外给出"可管理"所需的字段（ownerUserId、maxDepth、时间戳）。
// 管理端的读面本来就看得到这些（它要能回答"这个房间是谁的、什么时候建的"），
// 但**依旧不含**密码与成员明细 —— "管理端"不是"什么都给"的理由：
// 密码在库/内存里都没有可用的明文形态（服务端只存原文用于比对，不该有第二条出口），
// 而成员明细对排障没有价值。
type adminRoomItem struct {
	RoomID string `json:"roomId"`
	// OwnerUserID 是房主账号 id（0 = 无房主：游客路径或单测直接建的房间）。
	OwnerUserID int64  `json:"ownerUserId"`
	Title       string `json:"title"`
	MemberCount int    `json:"memberCount"`
	MaxDepth    int    `json:"maxDepth"`
	// IsPublic 在"缺元数据行"时为 false，且 MetadataMissing 会置 true ——
	// 两个字段一起看才能区分"未公开"与"元数据缺失"（后者需要人去查为什么缺）。
	IsPublic        bool  `json:"isPublic"`
	HasPassword     bool  `json:"hasPassword"`
	HostOnline      bool  `json:"hostOnline"`
	HostOffline     bool  `json:"hostOffline"`
	PlayingIndex    int64 `json:"playingIndex"`
	MetadataMissing bool  `json:"metadataMissing"`
	// InMemory 恒为 true（这一行至少来自内存）；保留它是为了将来"只存在于库里的房间"
	//（例如已关闭的历史行）能复用同一个类型而不需要客户端改解析。
	InMemory bool `json:"inMemory"`
	// ClosedAt 只在库里有值时出现（*string，RFC3339）。
	ClosedAt *time.Time `json:"closedAt,omitempty"`
	// LastSeenAt 是元数据侧的最近活跃时间；缺行时不出现。
	LastSeenAt *time.Time `json:"lastSeenAt,omitempty"`
}

// listRooms 返回管理端房间列表（成功 200）。
//
// 数据来源是**两边的并集**（T4 明确要求含未公开）：
//
//	内存快照（ListRooms，实时事实：谁在房里、主播在线与否）
//	  ∪ rooms_meta（持久事实：标题、公开性、创建/关闭时间）
//
// 并集而不是交集：只存在于库里的行（房间已销毁但元数据留着）也要能出现在管理端，
// 否则"下架"与"强关"之后管理员就再也看不到那条记录，也就无法核对审计。
//
// 筛选与分页口径：
//   - `search` 同时用于"内存侧的房间码匹配"和"库侧的 room_id/title 子串匹配"；
//   - `public` 只接受 true / false / 空，非法值 400（不静默当空处理 ——
//     那会让"筛了个非法值却被当成不筛"返回一屏看似正常的结果）；
//   - 分页在**并集之后的内存里**做：两个来源的排序规则不同，先各自分页再合并
//     会丢行/重复行（这正是"分页要在一处做"的经典理由）。
func (h *adminHandler) listRooms(c *gin.Context) {
	limit, err := adminPageLimit(c.Query("limit"))
	if err != nil {
		adminBadRequest(c, err.Error())
		return
	}
	offset, err := adminPageOffset(c.Query("offset"))
	if err != nil {
		adminBadRequest(c, err.Error())
		return
	}
	publicOnly, privateOnly, err := adminPublicFilter(c.Query("public"))
	if err != nil {
		adminBadRequest(c, err.Error())
		return
	}
	search := strings.TrimSpace(c.Query("search"))

	if h.deps.Rooms == nil && h.deps.RoomMeta == nil {
		adminInternalError(c, "查询房间列表", errors.New("装配里既没有内存房间源也没有元数据查询"))
		return
	}

	// ① 内存快照（含未公开的房间）。缺装配时按空处理：列表的其余部分仍然有价值。
	snapshots := []usecase.RoomSnapshot{}
	if h.deps.Rooms != nil {
		snapshots = h.deps.Rooms.ListRooms()
	}

	// ② 元数据：全量筛出来（不先分页），因为并集之后才排序分页。
	// limit 用内存房间数 + 分页上限，依据见下面的注释。
	metaRows := []store.RoomMeta{}
	metaTotal := int64(0)
	if h.deps.RoomMeta != nil {
		// 为什么要给一个"看起来很随意"的 limit：元数据侧只需要**内存里已经存在的那些行**
		//（房间码按定义是同一个键），因此"内存房间数 + 一页"足以覆盖所有可能与内存求交的行。
		// 多出来的那些（已关闭的历史行）排在 last_seen_at DESC 的尾部，
		// 它们除了让并集变大之外不提供实时信息，而管理端真正要管的正是"现在还在的房间"。
		limitRows := len(snapshots) + maxAdminPageLimit
		if limitRows < maxAdminPageLimit {
			limitRows = maxAdminPageLimit
		}
		rows, total, err := h.deps.RoomMeta.QueryRoomMetas(c.Request.Context(), store.RoomMetaQuery{
			Search:      search,
			PublicOnly:  publicOnly,
			PrivateOnly: privateOnly,
			Limit:       limitRows,
			Offset:      0,
		})
		if err != nil {
			adminInternalError(c, "查询房间元数据列表", err)
			return
		}
		metaRows, metaTotal = rows, total
	}

	// ③ 并集：先放内存快照（用元数据补齐标题/公开性/时间戳），再补只在库里的行。
	byID := make(map[string]store.RoomMeta, len(metaRows))
	for _, m := range metaRows {
		byID[m.RoomID] = m
	}

	items := make([]adminRoomItem, 0, len(snapshots)+len(metaRows))
	seen := make(map[string]struct{}, len(snapshots))
	pattern := strings.ToLower(search)
	for _, s := range snapshots {
		m, hasMeta := byID[s.ID]
		if !hasMeta {
			// 内存里有、库里没有：如果带了公开性筛选，它无法满足任何一个方向
			//（库里没有 is_public 可判），因此被筛掉；search 仍然可以对房间码生效。
			if publicOnly || privateOnly {
				continue
			}
		}
		if pattern != "" && !strings.Contains(strings.ToLower(s.ID), pattern) && !hasMeta {
			// 房间码不匹配、库里也没有标题可匹配 → 这个房间与搜索无关。
			continue
		}
		if pattern != "" && hasMeta &&
			!strings.Contains(strings.ToLower(s.ID), pattern) &&
			!strings.Contains(strings.ToLower(m.Title), pattern) {
			continue
		}

		item := adminRoomItem{
			RoomID:          s.ID,
			OwnerUserID:     s.OwnerUserID,
			Title:           m.Title,
			MemberCount:     s.MemberCount,
			MaxDepth:        s.MaxDepth,
			IsPublic:        m.IsPublic,
			HasPassword:     s.HasPassword,
			HostOnline:      s.HostOnline,
			HostOffline:     s.HostInGrace,
			PlayingIndex:    s.PlayingIndex,
			MetadataMissing: !hasMeta,
			InMemory:        true,
		}
		if hasMeta {
			item.ClosedAt = m.ClosedAt
			lastSeen := m.LastSeenAt
			item.LastSeenAt = &lastSeen
			// 库里的 owner 是权威（内存里可能因为房间码复用而与库不一致）。
			item.OwnerUserID = m.OwnerUserID
			item.HasPassword = m.HasPassword
		}
		seen[s.ID] = struct{}{}
		items = append(items, item)
	}
	for _, m := range metaRows {
		if _, dup := seen[m.RoomID]; dup {
			continue
		}
		// 只在库里（房间已销毁）：这些行不可能满足"有主播"这类实时判据，
		// 但它们是"刚才被关掉的房间"，管理端需要它们来做前后对照。
		item := adminRoomItem{
			RoomID:      m.RoomID,
			OwnerUserID: m.OwnerUserID,
			Title:       m.Title,
			IsPublic:    m.IsPublic,
			HasPassword: m.HasPassword,
			InMemory:    false,
		}
		item.ClosedAt = m.ClosedAt
		lastSeen := m.LastSeenAt
		item.LastSeenAt = &lastSeen
		items = append(items, item)
	}

	// ④ 稳定排序 + 分页（在合并后的集合上做一次）。
	// 排序键：有主播的优先 → 房间码升序。房间码是主键级唯一键，
	// 因此这个顺序是全序，分页不会出现重复/漏项。
	sort.Slice(items, func(i, j int) bool {
		if items[i].HostOnline != items[j].HostOnline {
			return items[i].HostOnline
		}
		return items[i].RoomID < items[j].RoomID
	})

	total := int64(len(items))
	if int64(len(metaRows)) > total {
		// 库侧总数大于可见并集时（历史行被上面的 limit 截断），以库侧总数为准，
		// 让"还有更多"这件事能被分页器感知到。
		total = metaTotal
	}
	page := slicePage(items, offset, limit)

	c.JSON(http.StatusOK, gin.H{
		"items":  page,
		"total":  total,
		"limit":  limit,
		"offset": offset,
		// 回显生效的筛选值：客户端不必猜自己传的 public 被理解成了什么。
		"search": search,
		"public": c.Query("public"),
	})
}

// adminPublicFilter 解析 public 查询参数。
//
// 只接受 true / false / 空三种形态（T4 的要求）：非法值 400 而不是当空处理。
// 用 strconv.ParseBool 会让 "1"/"t" 也通过 —— 那看起来更宽容，
// 实则让"客户端把某个字符串直接拼进查询串"变成一个被静默接受的行为，
// 而它的反面（传错参数却拿到全量结果）正是最难被发现的 bug 形态。
func adminPublicFilter(raw string) (publicOnly, privateOnly bool, err error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return false, false, nil
	case "true":
		return true, false, nil
	case "false":
		return false, true, nil
	default:
		return false, false, fmt.Errorf("public 只能是 true、false 或留空")
	}
}

// closeRoom 强制关闭一个房间（成功 200）。
//
// 三件事，缺一不可（T4）：
//
//  1. **内存侧关房**：Manager.CloseRoomByAdmin 走既有的 closeRoom 路径 ——
//     摘出房间 + 置 closed + 终止宽限定时器 + 作废 hostToken + 广播 room-closed。
//     "受控导出"的意义就在这：这三条不变式只有一处实现。
//  2. **断开房内连接**：Hub.CloseRoom（见 AdminHub 的说明；生产装配里
//     step 1 已经通过 Broadcaster 做到，这一行是装配期的防御）。
//  3. **审计一行** room.force_close：这是"管理员动了手"的唯一可靠痕迹。
//
// 数据库侧的 closed_at 是**尽力而为**：它只让"这条房间码在元数据侧已关闭"这件事
// 可查（并且让 ListPublicRoomIDs 之类的元数据筛选不再把它算进去），
// 而强关本身已经在内存与广播里完成了。写失败只记日志、不改返回值 ——
// 否则会退化成"库抖了一下，管理员就关不掉一个直播事故房间"。
//
// 404 的判定：房间在内存里不存在（已被回收/已关闭）→ 404。
// 管理端强关的是一个**正在发生的房间**，"关一个不存在的房间"必须能被区分出来。
func (h *adminHandler) closeRoom(c *gin.Context) {
	actor, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}
	roomID := strings.ToUpper(strings.TrimSpace(c.Param("id")))
	if roomID == "" {
		adminBadRequest(c, "路径参数 id 不能为空")
		return
	}
	if h.deps.Rooms == nil {
		adminInternalError(c, "强制关闭房间", errors.New("装配里没有房间管理器"))
		return
	}

	snapshot, exists := h.roomSnapshot(roomID)
	if !exists {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{
			"error": "房间不存在（可能已经被回收或已关闭）",
			"code":  model.CodeRoomNotFound,
		})
		return
	}

	const reason = "管理员强制关闭了该房间"
	if err := h.deps.Rooms.CloseRoomByAdmin(roomID, reason); err != nil {
		if errors.Is(err, usecase.ErrNotFound) {
			// 与上面同一条语义的竞态分支（两次查询之间房间被回收）。
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{
				"error": "房间不存在（可能已经被回收或已关闭）",
				"code":  model.CodeRoomNotFound,
			})
			return
		}
		adminInternalError(c, "强制关闭房间", err)
		return
	}
	if h.deps.Hub != nil {
		h.deps.Hub.CloseRoom(roomID)
	}

	// 审计：与封禁/改角色同一条异步路径（由本层投递一次，usecase 不再写第二份）。
	// 明细里不带标题：标题要另查一次库，而"谁在何时强制关闭了哪个房间"这个事实
	// 由 actor/action/target 三者已经完全确定（房间码是唯一键）。
	h.writeRoomAudit(c, actor.ID, "room.force_close", roomID, map[string]any{
		"reason":      reason,
		"memberCount": snapshot.MemberCount,
		"hostOnline":  snapshot.HostOnline,
	})

	// 元数据侧补 closed_at（best-effort，见上面的说明）。
	if h.deps.RoomMeta != nil {
		closedAt := time.Now()
		if err := h.deps.RoomMeta.UpdateRoomMeta(c.Request.Context(), roomID, store.RoomMetaPatch{ClosedAt: &closedAt}); err != nil {
			log.Printf("[WARN] 房间 %s 已强制关闭，但元数据的 closed_at 未能写上：%v", roomID, err)
		}
	}

	log.Printf("管理端强关 room=%s（管理员 %d，房内 %d 人，主播在线=%t）",
		roomID, actor.ID, snapshot.MemberCount, snapshot.HostOnline)

	c.JSON(http.StatusOK, gin.H{
		"roomId":  roomID,
		"closed":  true,
		"members": snapshot.MemberCount,
		"reason":  reason,
	})
}

// unpublishRoom 把房间从公开列表下架（成功 200，**幂等**）。
//
// 幂等语义（T4 明确要求）：
//
//	已经是未公开 → 仍然返回 200（调用方看到的是"它现在未公开"这个事实，
//	而不是"你这次操作有没有生效"），但**审计只在真的发生变更时写一行**。
//
// 为什么审计只记实际变更：审计表要能回答"管理员对公开房做过几次下架"。
// 把每次幂等调用都记一行，会让"有多少次是有效的"淹没在重试/双击产生的噪声里 ——
// 而管理员的鼠标双击是完全正常的行为。
//
// 两处一起改（与 T3 同一条约定）：
//
//	内存侧 usecase.Manager.SetRoomMeta（授权判据）；
//	库侧 UpsertRoomMeta（**整体覆盖**，所以先读当前行再覆盖 is_public）。
func (h *adminHandler) unpublishRoom(c *gin.Context) {
	actor, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}
	roomID := strings.ToUpper(strings.TrimSpace(c.Param("id")))
	if roomID == "" {
		adminBadRequest(c, "路径参数 id 不能为空")
		return
	}
	if h.deps.Rooms == nil {
		adminInternalError(c, "下架房间", errors.New("装配里没有房间管理器"))
		return
	}

	snapshot, exists := h.roomSnapshot(roomID)
	if !exists {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{
			"error": "房间不存在（可能已经被回收或已关闭）",
			"code":  model.CodeRoomNotFound,
		})
		return
	}

	// 当前公开性：库里有行时以库为准（库是 is_public 的权威）。缺行时按未公开看待 ——
	// 那正是自愈/建房路径的默认值，因此"下架一个缺行的房间"不会产生任何变更。
	wasPublic := false
	var current *store.RoomMeta
	if h.deps.RoomMeta != nil {
		row, err := h.deps.RoomMeta.RoomMetaByID(c.Request.Context(), roomID)
		switch {
		case err == nil && row != nil:
			current = row
			wasPublic = row.IsPublic
		case errors.Is(err, usecase.StoreNotFound):
			// 缺行：不是错误，下面按"需要补一行未公开的元数据"处理。
		default:
			adminInternalError(c, "读房间元数据", err)
			return
		}
	}

	// 内存侧：房主身份校验在 SetRoomMeta 里。管理员通常不是房主，
	// 因此这里**不把 ErrNotRoomOwner 当失败**：下架的权威后果是"is_public=false"，
	// 而这件事由库侧那一行表达；内存侧那一份只是同一次调用里的内存事实通知。
	if err := h.deps.Rooms.SetRoomMeta(roomID, actor.ID, nil, boolPtr(false)); err != nil {
		switch {
		case errors.Is(err, usecase.ErrNotFound):
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{
				"error": "房间不存在（可能已经被回收或已关闭）",
				"code":  model.CodeRoomNotFound,
			})
			return
		case errors.Is(err, usecase.ErrNotRoomOwner):
			log.Printf("管理端下架 room=%s：管理员 %d 不是房主，只降低库侧的公开性（内存侧无副本可改）",
				roomID, actor.ID)
		default:
			adminInternalError(c, "下架房间（内存侧）", err)
			return
		}
	}

	changed := wasPublic
	if h.deps.RoomMeta != nil {
		expected := &store.RoomMeta{
			RoomID:      roomID,
			OwnerUserID: snapshot.OwnerUserID,
			IsPublic:    false,
			HasPassword: snapshot.HasPassword,
		}
		if current != nil {
			// 整体覆盖前先以当前行为基底，避免把 title/created_at/closed_at 冲掉。
			out := *current
			expected = &out
			expected.IsPublic = false
		}
		if err := h.deps.RoomMeta.UpsertRoomMeta(c.Request.Context(), expected); err != nil {
			adminInternalError(c, "下架房间（元数据侧）", err)
			return
		}
	}

	if changed {
		h.writeRoomAudit(c, actor.ID, "room.unpublish", roomID, map[string]any{
			"wasPublic": true,
		})
	}

	log.Printf("管理端下架 room=%s（管理员 %d，原公开=%t，实际变更=%t）", roomID, actor.ID, wasPublic, changed)

	c.JSON(http.StatusOK, gin.H{
		"roomId":   roomID,
		"isPublic": false,
		// changed 让客户端能区分"这次真的下架了"与"本来就未公开（幂等重试）" ——
		// 它不影响状态码（两者都是 200），只影响提示文案。
		"changed": changed,
	})
}

// roomSnapshot 在内存里查一个房间的实时快照。
//
// 它刻意不复用 publicroom 的读法，而是走 Manager.ListRooms 之外的一条更窄的路径：
// 管理端只需要**一个**房间，遍历全部房间既浪费也掩盖了"这个房间在不在"这个判断。
// 房间已 closed 房间（刚被并发关闭）视为不存在。
func (h *adminHandler) roomSnapshot(roomID string) (usecase.RoomSnapshot, bool) {
	if h.deps.Rooms == nil {
		return usecase.RoomSnapshot{}, false
	}
	for _, s := range h.deps.Rooms.ListRooms() {
		if s.ID == roomID {
			return s, true
		}
	}
	return usecase.RoomSnapshot{}, false
}

// roomSnapshot 的补充视图：管理端房间行里要显示的标题（库里才有）。
// 单独一个小函数是为了让 closeRoom / unpublishRoom 的审计明细里能带上它，
// 而不必把"库里的标题"塞进内存快照类型（那会让内存类型带上持久化字段）。
//
// 注意：标题缺失时审计明细里是空串，这是可接受的（审计的价值在谁在何时对谁做了什么）。
// writeRoomAudit 投递一条房间管理动作的审计。
//
// 与 usecase 的封禁/改角色审计**同一条异步路径**（store.Writer 的事件类），
// 但方向相反：那两条由 usecase 在自己的调用里投递（"改状态 + 写审计"原子配对），
// 而房间管理动作由本层投递 —— 因为"关房"跨了两个 owner（usecase 的内存关房、
// service.Hub 的断连），把它们塞进 usecase 会让 usecase 去调用 Hub 的另一个方法。
//
// **只写一份**：本层投递之后没有任何地方再写第二条（§9 的要求）。
func (h *adminHandler) writeRoomAudit(c *gin.Context, actorID int64, action, roomID string, detail map[string]any) {
	if h.deps.AuditSink == nil || h.deps.AuditWriter == nil {
		// 装配里没有事件写队列（或没有审计写能力）时只记日志：
		// 审计是 must 落点，缺失必须可见。
		log.Printf("[WARN] 管理端动作 %s 无法写审计（装配里缺少写队列或审计写能力）：room=%s actor=%d",
			action, roomID, actorID)
		return
	}
	ip := c.ClientIP()
	row := &store.AdminAudit{
		ActorID:    actorID,
		Action:     action,
		TargetType: "room",
		TargetID:   roomID,
		Detail:     auditDetailJSON(detail),
	}
	if strings.TrimSpace(ip) != "" {
		row.IP = &ip
	}
	if !h.deps.AuditSink.SubmitJob(func(ctx context.Context) error {
		return h.deps.AuditWriter.InsertAudit(ctx, row)
	}) {
		log.Printf("[WARN] 管理端动作 %s 的审计入队失败（队列满或写入器已关闭）：room=%s actor=%d",
			action, roomID, actorID)
	}
}

// auditDetailJSON 把结构化的审计明细编成 JSON 文本。
//
// 与 usecase.auditDetail 是同一件事，但它是**未导出**的（跨包用不了），
// 而重新实现一份 8 行的函数比把 usecase 的内部工具导出更划算：
// 导出一个"给 handler 用的小工具"会让 usecase 的公开面多出一个与业务无关的符号。
// 编码失败退化成 {}：明细为空远好过整条审计写不进去（§8 的"宁可写进去也别丢"）。
func auditDetailJSON(fields map[string]any) string {
	if len(fields) == 0 {
		return "{}"
	}
	b, err := json.Marshal(fields)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// boolPtr 返回一个布尔量的指针（"置 false"这类 patch 用）。
func boolPtr(v bool) *bool { return &v }

// —— 审计读取（T4 / §10 的 GET /api/admin/audit）——

// adminAuditItem 是审计行的**字段白名单**（§8）。
//
// 白名单的字段就是 §10 列的那一组：id / actorId / action / targetType /
// targetId / detail / ip / createdAt。刻意**没有**的东西：
// 发起操作的账号档案（它要另查 users，而审计要能在用户被删后仍然可读）、
// 以及任何形式的库内列（那属于 schema 面，不是协议面）。
//
// detail 以**文本**形式返回：列类型是 jsonb，但把它解成结构再下发会让前端
// 有机会把审计明细里的用户可控文本当结构渲染（与日志文本插值同一条理由）。
type adminAuditItem struct {
	ID         int64     `json:"id"`
	ActorID    int64     `json:"actorId"`
	Action     string    `json:"action"`
	TargetType string    `json:"targetType"`
	TargetID   string    `json:"targetId"`
	Detail     string    `json:"detail"`
	IP         string    `json:"ip"`
	CreatedAt  time.Time `json:"createdAt"`
}

// listAudit 返回审计行的分页（成功 200）。
//
// 响应里**没有** total：store.ListAudit 的契约是"按页取行"，它不做 COUNT
// （审计表是只增的大表，一条 COUNT 在每次翻页时都全表扫一遍，代价与收益完全不成比例）。
// 前端据此用"本页是否满页"判断还有没有更多 —— 那正好是 LIMIT/OFFSET 分页
// 唯一可靠的判据，而 total 在分页里本来就不保证连续（审计还在持续写入）。
func (h *adminHandler) listAudit(c *gin.Context) {
	if h.deps.Audit == nil {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
			"error": "审计读取未启用（服务端未完成装配）",
			"code":  CodeAuthUnavailable,
		})
		return
	}
	limit, err := adminPageLimit(c.Query("limit"))
	if err != nil {
		adminBadRequest(c, err.Error())
		return
	}
	offset, err := adminPageOffset(c.Query("offset"))
	if err != nil {
		adminBadRequest(c, err.Error())
		return
	}

	rows, err := h.deps.Audit.ListAudit(c.Request.Context(), limit, offset)
	if err != nil {
		adminInternalError(c, "查询审计", err)
		return
	}

	items := make([]adminAuditItem, 0, len(rows))
	for i := range rows {
		r := rows[i]
		item := adminAuditItem{
			ID:         r.ID,
			ActorID:    r.ActorID,
			Action:     r.Action,
			TargetType: r.TargetType,
			TargetID:   r.TargetID,
			Detail:     r.Detail,
			CreatedAt:  r.CreatedAt,
		}
		if r.IP != nil {
			item.IP = *r.IP
		}
		items = append(items, item)
	}

	c.JSON(http.StatusOK, gin.H{
		"items":  items,
		"limit":  limit,
		"offset": offset,
	})
}

// —— 内部：错误写出与参数解析。
// writeWriteError 把写操作的领域错误翻译成响应。
//
// 为什么单独一个函数：ErrSelfTarget（对自己封禁/降级，会把最后一个管理员锁在门外）
// 不在 accountErrorResponse 的映射表里（那张表属 T6/T7，本任务不动它），
// 落到 default 分支会变成 500 "服务端内部错误" —— 那会把一次**预期内的拒绝**
// 说成服务端故障。这里补一条：403 + FORBIDDEN（已认证、但该操作被服务端禁止），
// 文案用 usecase 自己的说明。
func (h *adminHandler) writeWriteError(c *gin.Context, err error) {
	if errors.Is(err, usecase.ErrSelfTarget) {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error": err.Error(),
			"code":  CodeForbidden,
		})
		return
	}
	writeAccountError(c, err)
}

// adminBadRequest 写出 400（参数不合法）。
func adminBadRequest(c *gin.Context, message string) {
	c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
		"error": message,
		"code":  model.CodeBadRequest,
	})
}

// adminInternalError 写出 500，底层原因只进服务端日志（可能带 SQL 片段/内部路径）。
func adminInternalError(c *gin.Context, what string, err error) {
	log.Printf("[ERROR] 管理端 %s：%v", what, err)
	c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
		"error": "服务端内部错误",
		"code":  model.CodeInternalError,
	})
}

// adminPathID 解析路径参数 :id（正整数）；非法时写出 400 并返回 ok=false。
func adminPathID(c *gin.Context, name string) (int64, bool) {
	raw := strings.TrimSpace(c.Param(name))
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		adminBadRequest(c, "路径参数 "+name+" 必须是正整数")
		return 0, false
	}
	return id, true
}

// adminPageLimit 解析 limit：缺省 50、非正数取缺省、超上限截断（与 store.clampPage 同口径）。
//
// 非整数**不**降级成缺省值，而是 400：静默改变分页口径会让"界面翻页翻不到东西"
// 变成一个没人查得出来的 bug。
func adminPageLimit(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultAdminPageLimit, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("limit 必须是整数")
	}
	switch {
	case n <= 0:
		return defaultAdminPageLimit, nil
	case n > maxAdminPageLimit:
		return maxAdminPageLimit, nil
	default:
		return n, nil
	}
}

// adminPageOffset 解析 offset：缺省 0、负数归零（与 store.clampPage 同口径）。
func adminPageOffset(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("offset 必须是整数")
	}
	if n < 0 {
		return 0, nil
	}
	return n, nil
}

// 断言：生产装配里的具体类型逐字满足管理端的窄接口。
// 放在这里是为了让"是否真的满足"在**编译期**暴露，而不是等到装配时才发现。
var (
	_ AdminAccountWriter  = (*usecase.AccountService)(nil)
	_ AdminUserStore      = (*store.Store)(nil)
	_ AdminHub            = (*service.Hub)(nil)
	_ AdminRoomStats      = (*usecase.Manager)(nil)
	_ AdminWriterStats    = (*usecase.WriterJobs)(nil)
	_ AdminLogSource      = (*service.LogRing)(nil)
	_ AdminRoomLister     = (*store.Store)(nil)
	_ AdminRoomMetaWriter = (*store.Store)(nil)
	_ AdminAuditSource    = (*store.Store)(nil)
	_ AdminAuditWriter    = (*store.Store)(nil)
	_ AdminAuditSink      = (*usecase.WriterJobs)(nil)
)
