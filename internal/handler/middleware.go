// 认证/授权中间件（ACCOUNTS §5 的"授权 100% 在服务端"）。
//
// 两条闸门：
//
//	RequireAuth   解析 Authorization: Bearer <access> → 校验 → 把用户放进 gin context
//	RequireAdmin  在 RequireAuth 之上再查 Role == admin
//
// 三条刻意的取舍（都在下面的实现里说明）：只从 Authorization 头取 token
// （URL/查询串一律不认）、userID 只能来自校验通过的 token、
// 授权判据只来自 service 查库的结果（不读 token 里的 role 快照）。
package handler

import (
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/model"
	"ProjectionRoom/internal/service/limiter"
	"ProjectionRoom/internal/store"
)

// logWarnf 是带 [WARN] 前缀的装配期提示（与 router.go 的日志风格一致）。
func logWarnf(format string, args ...any) {
	log.Printf("[WARN] "+format, args...)
}

// ginContextUserKey 是"当前登录用户"在 gin context 里的键。
//
// 用一个不可导出的常量而不是字符串字面量：拼错一个字母会让中间件写入的键
// 与处理器读取的键不一致，而那种 bug 的表现是"用户莫名其妙变成未登录"。
const ginContextUserKey = "pr.auth.user"

// bearerPrefix 是 access token 在 Authorization 头里的前缀（大小写不敏感）。
const bearerPrefix = "bearer "

// CurrentUser 从 gin context 取当前登录用户。
//
// 返回 ok=false 表示这条路由**没有**经过 RequireAuth，或者中间件被绕过 ——
// 两种都是编码错误，但处理器必须把它当成"未认证"处理（返回 401），
// 绝不能当成"匿名可用"（那会把越权变成默认行为）。
func CurrentUser(c *gin.Context) (*store.User, bool) {
	v, ok := c.Get(ginContextUserKey)
	if !ok {
		return nil, false
	}
	u, ok := v.(*store.User)
	return u, ok && u != nil
}

// RequireAuth 要求请求携带有效的 access token，校验通过后把用户放进 context。
//
// 为什么只认 Authorization 头：
//
//	把 token 放进查询串（?token=…）会立刻出现在反代 access log、浏览器历史与
//	Referer 里 —— 这正是 §5 为 WS 单独设计一次性票据的原因。REST 侧有请求头可用，
//	因此这里不提供任何降级入口（"方便调试"的代价是把长期凭据写进日志）。
func RequireAuth(svc AccountService) gin.HandlerFunc {
	return func(c *gin.Context) {
		if svc == nil {
			// 装配缺失（理论上 registerAccountRoutes 已经挡住了）：一律 503，
			// 不"放行"，也不假装是 401（那会把这归因到客户端凭据上）。
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"error": "账号能力暂时不可用（服务端未完成装配）",
				"code":  CodeAuthUnavailable,
			})
			return
		}

		token, ok := bearerToken(c)
		if !ok {
			abortUnauthorized(c, CodeUnauthorized, "缺少 Authorization: Bearer <access token>")
			return
		}

		u, err := svc.VerifyAccessToken(c.Request.Context(), token)
		if err != nil {
			// 过期与"无效/被撤销"分开：前端对前者静默刷新、对后者才弹登录框。
			status, code, message := accountErrorResponse(err)
			if status == http.StatusUnauthorized {
				abortUnauthorized(c, code, message)
				return
			}
			writeAccountError(c, err)
			return
		}

		c.Set(ginContextUserKey, u)
		c.Next()
	}
}

// RequireAdmin 要求当前用户是管理员（必须挂在 RequireAuth **之后**）。
//
// 授权判据是**数据库里的 role**（由 VerifyAccessToken 查库后写进 context），
// 而不是 token 载荷里的那个 role 快照 —— 后者在"刚被降级"的窗口里仍然是 admin
// （不变量 I3：token 里的 role 只决定显示什么）。
//
// 独立的 403 + FORBIDDEN 刻意不透露任何账号状态：能走到这里说明已经通过认证，
// 剩下的唯一结论就是"权限不足"。
func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		u, ok := CurrentUser(c)
		if !ok {
			// 没经过 RequireAuth：这是装配错误，按未认证处理（fail closed）。
			abortUnauthorized(c, CodeUnauthorized, "缺少身份信息，请先登录")
			return
		}
		if u.Role != store.RoleAdmin {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "需要管理员权限",
				"code":  CodeForbidden,
			})
			return
		}
		c.Next()
	}
}

// bearerToken 从 Authorization 头解析 access token。
//
// 刻意不做的事：读查询串 / URL 参数、读 Cookie、读自定义头（X-Access-Token）。
// 每多一条入口，长期凭据就多一个可能被写进日志或浏览器历史的地方。
func bearerToken(c *gin.Context) (string, bool) {
	raw := c.GetHeader("Authorization")
	if raw == "" {
		return "", false
	}
	// 前缀按大小写不敏感匹配：RFC 7235 的 scheme 是大小写不敏感的，
	// 而不少 HTTP 客户端会发 "Bearer"/"bearer" 两种形态。
	if len(raw) < len(bearerPrefix) || !strings.EqualFold(raw[:len(bearerPrefix)], bearerPrefix) {
		return "", false
	}
	token := strings.TrimSpace(raw[len(bearerPrefix):])
	if token == "" {
		return "", false
	}
	return token, true
}

// abortUnauthorized 写出 401 并终止链路。
func abortUnauthorized(c *gin.Context, code, message string) {
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": message, "code": code})
}

// newKeyedLimiter 构造"按 IP 的令牌桶"中间件（§5：注册/登录/刷新各自一条）。
//
// 三条与 router.go 里建房限速（roomCreateLimiter）完全一致的约定：
//   - 键用 c.ClientIP()，它的可信度由 NewRouter 里的 SetTrustedProxies 保证
//     （只信任本机代理的 XFF，否则用 TCP 对端）—— 换 key 口径会让伪造 XFF 生效；
//   - 超限返回 429 + RATE_LIMITED（与建房共用错误码：客户端对这两者的正确反应
//     一样，都是退避重试而不是改参数）；
//   - limiter.NewKeyed 在参数非法时返回 nil（表示该闸门关闭），此时放行 ——
//     这是仓库既有的约定，配置层已经拦住了非法值。
func newKeyedLimiter(perMinute float64, burst int, what string) gin.HandlerFunc {
	l := limiter.NewKeyed(perMinute, burst)
	if l == nil {
		// 只在装配期提示一次：这类配置错误的症状是"被撞库但没有任何限速"，
		// 属于需要人立刻知道的事，而不是每请求都刷一条日志。
		logWarnf("账号入口 %s 的限速闸门已关闭（PR_AUTH_* 配置为 0 或非法）", what)
	}
	return func(c *gin.Context) {
		if l != nil && !l.Allow(c.ClientIP()) {
			writeRateLimited(c, what)
			return
		}
		c.Next()
	}
}

// writeRateLimited 写出 429 并终止链路。
//
// 它是**唯一**的 429 产出点（中间件与处理器内联的每邮箱闸门都走它），
// 所以"不同闸门撞限速时的响应字节相同"是结构性成立的，而不是靠两边抄同一段代码。
// 这一点对找回密码尤其重要：如果"撞了每邮箱闸门"与"撞了每 IP 闸门"的文案不同，
// 就等于告诉对方"这个地址被单独计数过"。
func writeRateLimited(c *gin.Context, what string) {
	c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
		"error": what + "请求过于频繁，请稍后再试",
		"code":  model.CodeRateLimited,
	})
}

// 邮件链路的限速文案（429 的 message 由它们拼出来，两条闸门刻意用不同的词，
// 但同一类闸门（IP / 邮箱）在所有路径上完全一致）。
const (
	// forgotRateLimitWhat 用于 /api/auth/forgot 的两条闸门（每 IP、每邮箱）。
	//
	// 两条用**同一个词**是刻意的：这样攻击者无法从文案上区分自己撞的是哪一条 ——
	// 而"撞了每邮箱闸门"意味着"这个地址最近被请求过"，那是一条不需要存在性
	// 也能被观测到的状态。两条合一说不出任何多余的信息。
	forgotRateLimitWhat = "找回密码"
	// tokenRateLimitWhat 用于 /api/auth/reset 与 /api/auth/verify-email（两者共用一条桶）。
	tokenRateLimitWhat = "链接处理"
	// otpRateLimitWhat 用于验证码的每邮箱闸门。
	//
	// 与 forgotRateLimitWhat 分开：两者是不同的动作，而"撞了验证码闸门"
	// 与"撞了找回闸门"对用户来说意味着不同的等待对象。
	otpRateLimitWhat = "验证码"
)

// newHourlyEmailLimiter 构造"每小时 N 次"的**每 IP** 限速中间件。
//
// 它与 newKeyedLimiter 的唯一差别是单位换算 —— 而那个换算正是最容易写错的地方，
// 所以它被抽成一个有名字的函数（见 hourlyToPerMinute）。
func newHourlyEmailLimiter(perHour int, what string) gin.HandlerFunc {
	return newKeyedLimiter(hourlyToPerMinute(perHour), perHour, what)
}

// hourlyToPerMinute 把"每小时次数"换算成令牌桶的"每分钟补充速率"。
//
// ⚠ 这个 /60 是硬约束，不是可选的风格问题：
// limiter.NewKeyed 的第一个参数单位是**每分钟令牌数**，而 PR_MAIL_*_PER_HOUR
// 这一族配置的语义是**每小时次数**。少除这一次，闸门就宽了 60 倍，
// 而且**不会有任何报错** —— 唯一的现象是"限速好像没生效"，那是一种
// 只有在对账时才会被发现的静默失效。
//
// burst 取 perHour 本身（不是除以 60）：桶满表示"这一小时一次都还没用过"，
// 所以正常用户的第一批请求永远畅通，而持续滥用会被回填速率压死。
func hourlyToPerMinute(perHour int) float64 {
	if perHour <= 0 {
		// 0 表示"闸门关闭"（limiter.NewKeyed 的约定），config 层不允许配成 0。
		// 这里保持与 config 一致的语义，避免出现"除以 0 得到 +Inf"这种荒唐的补充速率。
		return 0
	}
	return float64(perHour) / 60
}
