// 认证 REST 端点（ACCOUNTS §10）。
//
// 本文件只做四件事：解析请求 → 限速闸门（在 router.go 挂）→ 调 service →
// 把结果/错误翻译成 HTTP 与 Cookie。**任何授权判断都不在这里**：
// 身份事实由 service.VerifyAccessToken 给出（token_version 比对），
// 本层只负责把它的结论映射成状态码（见 errors.go）。
package handler

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/model"
	"ProjectionRoom/internal/service"
	"ProjectionRoom/internal/store"
)

// AccountService 是认证端点需要的**窄接口**。
//
// 为什么要接口而不是直接用 *service.AccountService：
//   - handler 的单测可以注入假 service，覆盖"响应体里绝不能出现 PasswordHash"、
//     "Cookie 属性"、"封禁/重放各返回什么状态码"这些**协议面**判据，
//     而不需要一台 PostgreSQL；
//   - 接口面本身就是一份"HTTP 层能对账号做的事"的清单，
//     比读一遍 service 的方法表更快看清边界。
type AccountService interface {
	Register(ctx context.Context, username, displayName, password, ip, ua string) (*service.Session, error)
	Login(ctx context.Context, username, password, ip, ua string) (*service.Session, error)
	Refresh(ctx context.Context, refreshToken, ip, ua string) (*service.Session, error)
	Logout(ctx context.Context, refreshToken string) error
	LogoutAll(ctx context.Context, userID int64) error

	Me(ctx context.Context, userID int64) (service.Profile, error)
	IssueWSTicket(ctx context.Context, userID int64) (string, error)
	VerifyAccessToken(ctx context.Context, token string) (*store.User, error)
}

// TicketConsumer 是 /ws 一次性票据的消费能力（ACCOUNTS §5）。
//
// 为什么是接口而不是直接用 *auth.TicketStore：ws.go 只需要"消费一次"这一个方法，
// 而单测要能注入"票据一定命中 / 一定不命中"的确定性实现（票据表是内存态 + 30 秒 TTL，
// 用真表写用例就得跟时钟赛跑）。*auth.TicketStore 的方法集逐字满足它。
type TicketConsumer interface {
	// Consume 取出票据对应的账号 id，并**立即作废**该票据（第二次调用必然 false）。
	Consume(ticket string) (userID int64, ok bool)
}

// AuthDeps 是认证路由与**建房路径**所需的全部依赖。
//
// Service 与 Session 由装配层（cmd/wire.go）构造；Store/Writer/RoomMeta/Tickets
// 是账号层在 handler 侧要用到的其余出口：
//   - Tickets 给 /ws 的握手（T8）；
//   - RoomMeta 给建房的 rooms_meta 异步入库（T8，§9 的事件类）；
//   - Store/Writer 给管理端指标（T9）。
//
// 它们全部作为**参数**经 NewRouter 传入（不再有装配期写一次的包级变量）。
type AuthDeps struct {
	Service  AccountService
	Session  SessionConfig
	Store    *store.Store
	Writer   *store.Writer
	Tickets  TicketConsumer
	RoomMeta RoomMetaSink
	// ProfileNames 是公开房列表取**房主昵称**的专用出口（T2）。
	//
	// 为什么它挂在 AuthDeps 而不是新开一个参数：硬约束是"不改 NewRouter 的参数个数与语义"，
	// 而这份依赖的来源（账号存储）与 Store 完全一致 —— 它只是同一个数据源上
	// 一个**只回答昵称**的更窄的出口（实现见 service.ProfileNameLookup）。
	// 它不放在 AdminDeps 里，是因为它服务的端点是免登录的公开列表，不是管理端。
	ProfileNames PublicRoomProfileStore
}

// SessionConfig 是会话在 HTTP 层的参数。
type SessionConfig struct {
	// AccessTTL 是 access token 有效期（仅用于响应体里的 expiresIn 提示）。
	AccessTTL time.Duration
	// RefreshTTL 是 refresh 的有效期（仅在没有具体会话时兜底推导 Cookie 的 Max-Age）。
	RefreshTTL time.Duration
	// WSTicketTTL 是一次性票据的有效期（仅用于响应体里的 expiresIn 提示；
	// 真实校验由 auth.TicketStore 按同一个值执行）。
	WSTicketTTL time.Duration
}

// registerAccountRoutes 是生产路径上的认证路由注册。
//
// 它由 NewRouter 直接调用（deps 是参数）。这里没有"可替换的函数值"了：
// 单测要假依赖，直接把假 AuthDeps 传给 NewRouter 即可 —— 那比"替换一个包级
// 函数值"更贴近生产路径（同一份注册代码、同一套中间件顺序）。
//
// 三条闸门逐条说明：
//  1. **账号能力开关**：PR_DB_DSN 为空 = 账号能力整体关闭（§5），此时**不注册**这些路由，
//     与 router.go 里"seg == nil 就跳过 /api/v1/segment/*"是同一条约定：
//     能力关掉时该路径不存在，而不是存在一个永远返回 500 的端点。
//  2. **装配缺失**：DSN 配了但 service 没装配出来（例如启动期连库失败被降级）——
//     注册一组一律 503 的处理器，比"路由凭空消失"更容易定位，
//     也符合 ACCOUNTS.md 里"清晰的 503 语义"这条要求。
//  3. 限速闸门为 nil 时**不拦截**（配置把该入口关掉了），但要打一条日志：
//     静默失去限速是一类很难发现的事故。
func registerAccountRoutes(api *gin.RouterGroup, deps AuthDeps, cfg *config.Config) {
	if api == nil {
		return
	}
	if cfg == nil || strings.TrimSpace(cfg.Auth.DBDSN) == "" {
		// 账号能力关闭：认证路由不存在（REST 侧表现为 404，pkg 侧不会命中这里）。
		return
	}

	if deps.Service == nil {
		log.Printf("[WARN] 已配置 PR_DB_DSN 但认证服务未装配（启动期连库/迁移失败？）：" +
			"/api/auth/* 一律返回 503（AUTH_UNAVAILABLE）")
		registerAuthUnavailable(api)
		return
	}

	loginLimiter := newKeyedLimiter(cfg.IPC.LoginPerMinute, cfg.IPC.LoginBurst, "登录")
	registerLimiter := newKeyedLimiter(cfg.IPC.RegisterPerMinute, cfg.IPC.RegisterBurst, "注册")
	refreshLimiter := newKeyedLimiter(cfg.IPC.RefreshPerMinute, cfg.IPC.RefreshBurst, "刷新")

	h := &authHandler{deps: deps}

	api.POST("/auth/register", registerLimiter, h.register)
	api.POST("/auth/login", loginLimiter, h.login)
	api.POST("/auth/refresh", refreshLimiter, h.refresh)
	api.POST("/auth/logout", h.logout)
	api.POST("/auth/logout-all", RequireAuth(deps.Service), h.logoutAll)
	api.GET("/auth/me", RequireAuth(deps.Service), h.me)
	api.POST("/auth/ws-ticket", RequireAuth(deps.Service), h.wsTicket)
}

// registerAuthUnavailable 注册一组"账号能力暂时不可用"的处理器（全部 503）。
func registerAuthUnavailable(api *gin.RouterGroup) {
	unavailable := func(c *gin.Context) {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
			"error": "账号能力暂时不可用（服务端未完成装配）",
			"code":  CodeAuthUnavailable,
		})
	}
	api.POST("/auth/register", unavailable)
	api.POST("/auth/login", unavailable)
	api.POST("/auth/refresh", unavailable)
	api.POST("/auth/logout", unavailable)
	api.POST("/auth/logout-all", unavailable)
	api.GET("/auth/me", unavailable)
	api.POST("/auth/ws-ticket", unavailable)
}

// authHandler 持有依赖，把每个端点的实现做成方法（避免闭包里重复传参）。
type authHandler struct {
	deps AuthDeps
}

// —— 请求体。

// credentialsRequest 是注册/登录的请求体（字段名与前端约定）。
type credentialsRequest struct {
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Password    string `json:"password"`
}

// —— 响应体。

// sessionResponse 是注册/登录/刷新的统一响应。
//
// **绝不包含 refresh token 与 PasswordHash**：
//   - refresh 只走 HttpOnly Cookie（进了响应体就等于给了 JS 一份可长期使用的凭据，
//     那正是 §5 要避免的形态）；
//   - PasswordHash 由 service.ProfileOf 在类型层面排除（它不是 Profile 的字段）。
type sessionResponse struct {
	User        service.Profile `json:"user"`
	AccessToken string          `json:"accessToken"`
	TokenType   string          `json:"tokenType"`
	ExpiresIn   int             `json:"expiresIn"`
}

// newSessionResponse 组装响应（access token 只在此处出现一次）。
func newSessionResponse(s *service.Session, accessTTL time.Duration) sessionResponse {
	// expiresIn 以 access token 的实际到期时刻为准（比 TTL 配置更准：
	// signer 会把时间戳截到秒，两者可能差一秒）。
	expiresIn := int(accessTTL.Seconds())
	if !s.AccessExpiresAt.IsZero() {
		if d := time.Until(s.AccessExpiresAt); d > 0 {
			expiresIn = int(d.Seconds())
		}
	}
	return sessionResponse{
		User:        s.User,
		AccessToken: s.AccessToken,
		TokenType:   "Bearer",
		ExpiresIn:   expiresIn,
	}
}

// —— Cookie（唯一的下发点）。

// refreshCookieName 是 refresh token 的 Cookie 名（§5 的协议面，前端与会话共用）。
const refreshCookieName = "pr_refresh"

// refreshCookiePath 把 Cookie 限制在认证路径上。
//
// 为什么不放 "/"：Cookie 会随**任何**路径的请求自动带上，包括静态资源与 /ws。
// 收窄到 /api/auth 之后，只有刷新与登出这两个真的需要它的端点会收到它。
const refreshCookiePath = "/api/auth"

// setRefreshCookie 下发 refresh token。
//
// 属性逐条说明（§5 + 审计 must）：
//   - HttpOnly：JS 读不到它。这是"refresh 不进 localStorage"的另一半
//     （审计把 access 与 refresh 都暴露给 JS 判为账号单点失守）；
//   - SameSite=Lax：跨站 POST 不携带它 → 挡掉一类 CSRF（刷新/登出都是 POST）；
//     用 Lax 而不是 Strict：从外部链接点回本站时（顶层导航），用户仍然是登录态，
//     Strict 会让"点链接回来"看起来像掉线；
//   - Path=/api/auth：见 refreshCookiePath；
//   - Max-Age：由会话实际的到期时刻推导（不是照抄配置），
//     保证"库里那一行的有效期"与"浏览器保留多久"始终一致。
//
// // TODO(TLS)：当前部署没有 TLS，所以**故意不加 Secure**（加了浏览器根本不会
// 在不安全的连接上回传它，等于把刷新功能关掉）。上 TLS 之后必须立刻：
//  1. 这里加 Secure；
//  2. SameSite 收紧（若要彻底挡 CSRF 可评估 Strict + 专门的刷新接口）；
//  3. 缩短 refresh 有效期（§13.1）。
func setRefreshCookie(c *gin.Context, token string, expiresAt time.Time) {
	maxAge := int(time.Until(expiresAt).Seconds())
	if maxAge <= 0 {
		maxAge = 1
	}
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(refreshCookieName, token, maxAge, refreshCookiePath, "", false, true)
}

// clearRefreshCookie 清掉浏览器上的 refresh Cookie（登出/重放处置）。
//
// 用 MaxAge=-1（立即删除）而不是"设空值"：空值会留下一个同名 Cookie，
// 服务端仍要处理"空 token"这条路径，而浏览器不会主动清掉它。
func clearRefreshCookie(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(refreshCookieName, "", -1, refreshCookiePath, "", false, true)
}

// refreshTokenFromRequest 取 request cookie 里的 refresh token。
// 缺失时返回空串（调用方按"无凭据"处理，不区分"没有 Cookie"与"Cookie 为空"）。
func refreshTokenFromRequest(c *gin.Context) string {
	v, err := c.Cookie(refreshCookieName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

// —— 端点。

// register 注册（成功 201）并直接登录。
func (h *authHandler) register(c *gin.Context) {
	var req credentialsRequest
	if !bindCredentials(c, &req) {
		return
	}

	sess, err := h.deps.Service.Register(c.Request.Context(),
		req.Username, req.DisplayName, req.Password, c.ClientIP(), c.Request.UserAgent())
	if err != nil {
		writeAccountError(c, err)
		return
	}

	setRefreshCookie(c, sess.RefreshToken, sess.RefreshExpiresAt)
	c.JSON(http.StatusCreated, newSessionResponse(sess, h.deps.Session.AccessTTL))
}

// login 登录（成功 200）。
func (h *authHandler) login(c *gin.Context) {
	var req credentialsRequest
	if !bindCredentials(c, &req) {
		return
	}

	sess, err := h.deps.Service.Login(c.Request.Context(),
		req.Username, req.Password, c.ClientIP(), c.Request.UserAgent())
	if err != nil {
		writeAccountError(c, err)
		return
	}

	setRefreshCookie(c, sess.RefreshToken, sess.RefreshExpiresAt)
	c.JSON(http.StatusOK, newSessionResponse(sess, h.deps.Session.AccessTTL))
}

// refresh 用 Cookie 里的 refresh 换新的一对凭据（成功 200）。
//
// 只从 Cookie 读，**不接受**请求体/查询串里的 refresh：多一条入口就多一处
// 凭据可能被写进日志或浏览器历史的地方（§5 对 WS 票据用的是同一套理由）。
func (h *authHandler) refresh(c *gin.Context) {
	token := refreshTokenFromRequest(c)
	if token == "" {
		// 没有 Cookie：这是"未登录"的常规形态，不需要区分成更细的错误。
		clearRefreshCookie(c)
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"error": "缺少刷新凭据，请重新登录",
			"code":  CodeSessionInvalid,
		})
		return
	}

	sess, err := h.deps.Service.Refresh(c.Request.Context(), token, c.ClientIP(), c.Request.UserAgent())
	if err != nil {
		// 重放与无效凭据都把浏览器上的那一个 Cookie 清掉：
		// 它与服务端的状态已经不可能再对上了，留着只会让客户端反复重试同一个死凭据。
		if errors.Is(err, service.ErrRefreshReplay) || errors.Is(err, service.ErrSessionInvalid) {
			clearRefreshCookie(c)
		}
		writeAccountError(c, err)
		return
	}

	setRefreshCookie(c, sess.RefreshToken, sess.RefreshExpiresAt)
	c.JSON(http.StatusOK, newSessionResponse(sess, h.deps.Session.AccessTTL))
}

// logout 撤销当前会话（幂等，成功 204）。
func (h *authHandler) logout(c *gin.Context) {
	token := refreshTokenFromRequest(c)
	clearRefreshCookie(c)

	if token != "" {
		if err := h.deps.Service.Logout(c.Request.Context(), token); err != nil {
			writeAccountError(c, err)
			return
		}
	}
	c.Status(http.StatusNoContent)
}

// logoutAll 撤销该账号的全部会话（成功 204）。
//
// 它必须经过 RequireAuth（router 里已挂）：userID 只能来自校验过的 access token，
// 绝不能来自请求体 —— 否则任何人都能凭一个 id 把别人踢下线。
func (h *authHandler) logoutAll(c *gin.Context) {
	u, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}
	if err := h.deps.Service.LogoutAll(c.Request.Context(), u.ID); err != nil {
		writeAccountError(c, err)
		return
	}
	clearRefreshCookie(c)
	c.Status(http.StatusNoContent)
}

// me 返回当前账号的对外档案（成功 200）。
func (h *authHandler) me(c *gin.Context) {
	u, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}
	p, err := h.deps.Service.Me(c.Request.Context(), u.ID)
	if err != nil {
		writeAccountError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"user": p})
}

// wsTicket 签发一张 WS 一次性票据（成功 200）。
func (h *authHandler) wsTicket(c *gin.Context) {
	u, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}
	ticket, err := h.deps.Service.IssueWSTicket(c.Request.Context(), u.ID)
	if err != nil {
		writeAccountError(c, err)
		return
	}
	resp := gin.H{"ticket": ticket}
	// 客户端据此决定"多久之内必须完成握手"，避免拿着一张过期票据去连。
	// 省略而不是填 0：前端不该看到"0 秒有效"这种会被误解成"立即过期"的值。
	if h.deps.Session.WSTicketTTL > 0 {
		resp["expiresIn"] = int(h.deps.Session.WSTicketTTL.Seconds())
	}
	c.JSON(http.StatusOK, resp)
}

// —— 请求解析与错误写出。

// maxCredentialsBodyBytes 是注册/登录请求体的上限。
//
// 64 KiB 的依据：合法的请求体是"用户名 + 昵称 + 口令"，几十字节到几百字节；
// 上限存在的意义是让"超大 body"在读到内存之前就被拒。
const maxCredentialsBodyBytes = 64 << 10

// bindCredentials 解析并规范化用户名/口令，同时压住请求体大小。
// 返回 false 表示已经写出了错误响应（调用方直接 return）。
func bindCredentials(c *gin.Context, req *credentialsRequest) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxCredentialsBodyBytes)
	if err := c.ShouldBindJSON(req); err != nil {
		// 不回显底层解析错误：它可能包含请求体片段（而请求体里有口令）。
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "请求体不是合法 JSON",
			"code":  model.CodeBadRequest,
		})
		return false
	}
	req.Username = strings.TrimSpace(req.Username)
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	if req.Username == "" || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "用户名与密码不能为空",
			"code":  model.CodeBadRequest,
		})
		return false
	}
	return true
}

// writeAccountError 把账号层的错误翻译成 JSON 响应（映射见 errors.go）。
func writeAccountError(c *gin.Context, err error) {
	status, code, message := accountErrorResponse(err)
	if status == http.StatusInternalServerError {
		// 500 的底层原因只进服务端日志：错误文本里可能带 SQL 片段或内部路径。
		log.Printf("[ERROR] 账号接口内部错误：%v", err)
	}
	c.AbortWithStatusJSON(status, gin.H{"error": message, "code": code})
}
