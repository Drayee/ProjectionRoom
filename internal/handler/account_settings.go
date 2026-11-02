package handler

// 账号设置区（票 #71 / 决议 #51）。
//
// 本文件只有一件事：`PUT /api/me/password` —— 凭**当前密码**换新密码。
//
// 为什么它不是 `/api/auth/*` 下的端点：
//
//	`/api/auth/*` 是"进去 / 出来"那一族（登录、注册、刷新、登出、找回密码），
//	它们要解决的是"我还没有会话"或"我的会话该换了"。而这里是**已登录用户
//	在自己账号内部改一个字段**，与 `PATCH /api/me`（改昵称）是同一族 ——
//	前缀也相同。放进 /api/auth 会让 refresh Cookie 的 Path 规则（/api/auth）
//	与"哪些端点需要 refresh Cookie"之间多出一处不一致的可能。
//
// 两条改密门的分工（决议 #51，抄在这里因为改这一行的人一定想知道）：
//
//	记得密码 → 这里，只凭当前密码；
//	忘了密码 → `/api/auth/password/otp` + `/api/auth/password/otp/reset`（邮箱验证码）。
//
// 已接受的取舍：**只劫持到有效会话即可改密码**。挡住这一步需要再要一次邮箱码，
// 代价是每次改密都要等一封信；而拿到会话的攻击者本来就能删帖、读私聊 ——
// 改密只是让它更持久，且原主人还能走验证码门找回来。详见
// `service.AccountService.ChangeMyPassword` 的注释。
//
// 不做：改用户名（#51 Out of scope）、头像上传（#74）、注销（#73）。

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/model"
)

// changePasswordRequest 是 PUT /api/me/password 的请求体。
//
// 两个字段都用**非指针**：这里没有"不传即不改"的语义 —— 这个端点存在的唯一
// 目的就是改密码，两个都必填。缺任何一个都由下面的空串检查回 400，
// 用指针只会让"没传"与"传了空串"变成两条要分别处理的分支。
//
// 字段名是 `currentPassword` / `newPassword`（决议 #51 的契约，也是前端
// `MeSettingsBlock.vue` 提交的形状）。改名等于破坏跨票契约。
type changePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

// 本文件的两个入参哨兵（都在请求体层面就能判定，不必进 service）。
//
// 为什么"空口令"不进 service：它与"弱口令"是两条不同的文案 ——
// 前者是"你少填了一栏"（前端该把焦点移到那个输入框），后者是"长度/字符类
// 不够"（service 的文案带着具体门槛）。混成一条会让用户不知道该改什么。
var (
	errPasswordFieldsRequired = errors.New("请填写当前密码与新密码")
	errSettingsSessionInvalid = errors.New("登录状态无效，请重新登录")
)

// scopeChangePassword 是改密的幂等 scope。
//
// 与 `password.otp.reset` 分开：它们是**两条门**（记得密码 / 忘了密码），
// 共用 scope 会让"同一把幂等键"在两条路上互相命中，症状是"我改完密码
// 再去走找回密码，直接拿到了上一次的成功响应"。
const scopeChangePassword = "me.password"

// registerAccountSettingsRoutes 注册账号设置区的端点。
//
// 与其它二期路由同一套约定：账号能力关闭（`PR_DB_DSN` 为空）时整个域不注册；
// 服务未装配时注册 503（而不是让路由凭空消失 —— 那会让"页面 404"与
// "后端没上线"变成同一件事）。
//
// 为什么单独一个函数而不是塞进 registerUserProfileRoutes：
// 那条路径的依赖是 `UserProfileService`（一个只有两个方法的窄接口），
// 而改密要的是 `AccountService`（口令哈希、会话撤销、token_version 全在那边）。
// 把两个不同来源的依赖塞进一个注册函数，会让"这个域到底依赖了什么"变得
// 需要读完整个函数才能回答 —— 而它们其实是两个可以独立装配的能力。
func registerAccountSettingsRoutes(api *gin.RouterGroup, cfg *config.Config, auth AccountService) {
	if api == nil {
		return
	}
	if cfg == nil || strings.TrimSpace(cfg.Auth.DBDSN) == "" {
		return
	}
	if auth == nil {
		unavailable := func(c *gin.Context) {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"error": "账号设置暂不可用",
				"code":  CodeAuthUnavailable,
			})
		}
		api.PUT("/me/password", unavailable)
		return
	}

	h := &accountSettingsHandler{auth: auth, accessTTL: cfg.Auth.AccessTTL}

	// 改密挂**登录桶**（PR_AUTH_LOGIN_*，默认 10/min）。
	//
	// 为什么不是注册桶（5/min）：改密是登录用户在自己账号里的动作，
	// 试两次"新密码够不够长"是正常用法，5/min 会把它打成 429。
	//
	// 为什么不是只读桶：这个端点做两件昂贵的事 —— 一次 bcrypt 校验当前密码、
	// 一次 bcrypt 算新密码。它也是一个**口令猜测面**（拿着会话就能试当前密码），
	// 所以必须有闸门，且档位要与登录一致。
	passwordLimiter := newKeyedLimiter(cfg.IPC.LoginPerMinute, cfg.IPC.LoginBurst, "修改密码")
	api.PUT("/me/password", RequireAuth(auth), passwordLimiter, h.changePassword)
}

// accountSettingsHandler 持有依赖。
type accountSettingsHandler struct {
	auth AccountService
	// accessTTL 只用于响应体的 expiresIn 提示（权威值仍来自 Session.AccessExpiresAt）。
	accessTTL time.Duration
}

// changePassword 是 `PUT /api/me/password`。
//
// 状态码与判据：
//
//	401 SESSION_INVALID     没有/无效的 access token（RequireAuth 或这里的兜底）
//	400 PASSWORD_MISMATCH   当前密码不对
//	400 BAD_REQUEST         新口令不合策略（文案带具体原因）或请求体不是 JSON
//	403 FORBIDDEN           账号被封禁（本人应当知道自己不能改，而不是"密码错"）
//	429 RATE_LIMITED        撞上登录桶
//	200 + 新会话            改密成功，**当前设备换发一枚可用会话**，其余全部失效
//
// 为什么成功返回 200 + 完整会话而不是 204：
// 改密会把 `token_version` 加一并撤销全部会话 —— 如果只回 204，前端手里那枚
// access token 在下一个请求上就会 401，用户看到的是"改完密码就被踢出去了"。
// 返回新会话让前端能无缝续上（验收明确要求「当前会话保持、其余全蹬」）。
//
// 为什么走幂等壳：
// 改密是不可逆的（旧口令立刻失效），一次"网络超时后重发"如果真跑两遍，
// 第二遍会因为当前密码已变而失败 —— 用户看到的是"我明明改成功了却说密码不对"。
// 幂等键让重发直接拿到首次的成功响应。
func (h *accountSettingsHandler) changePassword(c *gin.Context) {
	runWithIdempotency(c, h.auth, scopeChangePassword, func() (int, any, error) {
		actor, ok := CurrentUser(c)
		if !ok {
			// 中间件被绕过时的兜底：fail closed。
			return 0, nil, errSettingsSessionInvalid
		}

		var req changePasswordRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			return 0, nil, errBadJSON
		}
		if strings.TrimSpace(req.CurrentPassword) == "" || strings.TrimSpace(req.NewPassword) == "" {
			return 0, nil, errPasswordFieldsRequired
		}

		sess, err := h.auth.ChangeMyPassword(c.Request.Context(),
			actor.ID, req.CurrentPassword, req.NewPassword, c.ClientIP(), c.Request.UserAgent())
		if err != nil {
			return 0, nil, err
		}

		// refresh Cookie 必须跟着换：旧 Cookie 指向的那一行已经被撤销，
		// 留着它会让下一次刷新命中重放检测（那是"撤销全部会话"的处置）。
		setRefreshCookie(c, sess.RefreshToken, sess.RefreshExpiresAt)
		return http.StatusOK, newSessionResponse(sess, h.accessTTL), nil
	})
}

// 保住 model 的引用：本文件的两个哨兵走 accountErrorResponse（它用 model.Code*），
// 但把这条 import 显式留下，是为了让"本域的 400 用哪个码"留在显眼处 ——
// 与 users.go 末尾同一条理由。
var _ = model.CodeBadRequest
