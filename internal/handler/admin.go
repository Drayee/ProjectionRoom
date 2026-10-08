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
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

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

// AdminHub 是连接池里管理端要用的两个能力（*service.Hub 满足它）。
//
// CloseByUser 是封禁的第二件事（ACCOUNTS §5）：只改 status 的话，
// 已经建立的长连接仍然是"已授权"的，它还能继续进房、发信令、留在房间里。
type AdminHub interface {
	Stats() (connections, distinctUsers int)
	CloseByUser(userID int64) int
}

// AdminRoomStats 是房间实况的只读统计（*usecase.Manager 满足它）。
type AdminRoomStats interface {
	RoomCount() int
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
	admin.GET("/logs", h.logs)
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

// 断言：生产装配里的两个具体类型逐字满足管理端的窄接口。
// 放在这里是为了让"是否真的满足"在**编译期**暴露，而不是等到装配时才发现。
var (
	_ AdminAccountWriter = (*usecase.AccountService)(nil)
	_ AdminUserStore     = (*store.Store)(nil)
	_ AdminHub           = (*service.Hub)(nil)
	_ AdminRoomStats     = (*usecase.Manager)(nil)
	_ AdminWriterStats   = (*store.Writer)(nil)
	_ AdminLogSource     = (*service.LogRing)(nil)
)
