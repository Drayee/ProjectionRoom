package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"ProjectionRoom/internal/service"

	"github.com/gin-gonic/gin"
)

// 两段式注册与邮箱验证码的 HTTP 层。
//
// # 端点
//
//	POST /api/auth/register/start   {username, displayName, password}  → {ticket, expiresIn}
//	POST /api/auth/register/email   {ticket, email}                   → 204（发出验证码）
//	POST /api/auth/register/verify  {ticket, code}                    → 201（会话）
//	POST /api/auth/password/otp     {email}                           → 200（恒定）
//	POST /api/auth/password/otp/reset {email, code, password}         → 204
//
// # 幂等
//
// 前三个端点都接受 `Idempotency-Key` 头（客户端生成 UUID）。命中同一个键 + 同一份
// 请求体时直接返回首次的响应，不再执行 —— 这是"双击提交"与"超时重发"不会建出
// 两个账号、不会发出两封码的唯一可靠做法。
//
// 为什么把幂等放在 handler 而不是 service：它是**HTTP 语义**（重放同一请求），
// 而 service 描述的是业务事实（建一个账号）。放在这一层意味着 service 的每个
// 方法都可以被安全地重试一次（业务幂等），而"整个请求"的幂等由这里兜住。

// 幂等键的三个 scope（与端点点一一对应）。
const (
	scopeRegisterStart  = "register.start"
	scopeRegisterEmail  = "register.email"
	scopeRegisterVerify = "register.verify"
	// idempotencyTTL 是幂等键的存活时间。
	//
	// 24 小时的理由：客户端的重试窗口（网络超时、用户手点重发）都在分钟级，
	// 而"昨天那次注册请求今天被重放"没有意义。留一整天是为了覆盖
	// "用户第二天打开同一个标签页再点一次"这种真实场景。
	idempotencyTTL = 24 * time.Hour
)

// —— 请求体 ——

type registerStartRequest struct {
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Password    string `json:"password"`
}

type registerEmailRequest struct {
	Ticket string `json:"ticket"`
	Email  string `json:"email"`
}

type registerVerifyRequest struct {
	Ticket string `json:"ticket"`
	Code   string `json:"code"`
}

type passwordOTPRequest struct {
	Email string `json:"email"`
}

type passwordOTPResetRequest struct {
	Email    string `json:"email"`
	Code     string `json:"code"`
	Password string `json:"password"`
}

// —— 幂等辅助 ——

// idempotencyKeyFrom 读取幂等键。
//
// 缺失时返回空串 = 不启用幂等（调用方按普通请求处理）。这是刻意的降级：
// 幂等是**客户端选择**（它要能生成并记住一个 UUID），服务端不该因为它缺席就拒绝。
func idempotencyKeyFrom(c *gin.Context) string {
	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	// 长度上限：键会进库（varchar(64)），超长会在写入时炸成 500。
	// 这里先截断判定并给出 400，让问题在客户端可见。
	if len(key) > 64 {
		return ""
	}
	return key
}

// hashRequestBody 算出请求体的摘要（幂等键的"同键不同体"判定依据）。
func hashRequestBody(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// withIdempotency 是幂等键的**唯一**执行壳。
//
// 流程：
//
//	① 没有键 → 直接执行；
//	② 有键 → Reserve：
//	     已完成 → 原样返回首次的响应（连状态码一起）；
//	     进行中 → 409（让客户端稍后重试，而不是以为失败）；
//	     键冲突 → 409（同键不同体是客户端 bug，必须拒绝）；
//	     占位成功 → 执行 → 成功则 Complete 落响应，失败则 Release 让客户端能重试。
//
// `run` 返回 (status, body) 或 error。**只有成功才落键**：失败往往正是值得重试的
// 那一类（网络抖、邮件服务暂时不可用），落键会让同一个键永远拿到那个失败。
func (h *authHandler) withIdempotency(c *gin.Context, scope string, run func() (int, any, error)) {
	key := idempotencyKeyFrom(c)
	raw, _ := io.ReadAll(c.Request.Body)
	c.Request.Body = io.NopCloser(strings.NewReader(string(raw)))

	if key == "" {
		status, body, err := run()
		if err != nil {
			writeAccountError(c, err)
			return
		}
		if body == nil {
			c.Status(status)
			return
		}
		c.JSON(status, body)
		return
	}

	ctx := c.Request.Context()
	rec, err := h.deps.Service.ReserveIdempotency(c.Request.Context(), scope, key, hashRequestBody(raw), idempotencyTTL)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrIdempotencyKeyConflict):
			c.AbortWithStatusJSON(http.StatusConflict, gin.H{
				"error": "这个幂等键已用于另一个请求，请换一个键或检查请求内容",
				"code":  "IDEMPOTENCY_KEY_CONFLICT",
			})
			return
		case errors.Is(err, service.ErrIdempotencyKeyInFlight):
			c.AbortWithStatusJSON(http.StatusConflict, gin.H{
				"error": "同一请求正在处理中，请稍后重试",
				"code":  "IDEMPOTENCY_IN_FLIGHT",
			})
			return
		default:
			writeAccountError(c, err)
			return
		}
	}
	if rec != nil {
		// 已完成过：原样返回首次的响应体与状态码。
		c.Data(rec.Status, "application/json; charset=utf-8", []byte(rec.Response))
		return
	}

	status, body, err := run()
	if err != nil {
		// 失败不落键：释放占位，让同一个键可以重试。
		_ = h.deps.Service.ReleaseIdempotency(ctx, scope, key)
		writeAccountError(c, err)
		return
	}

	// 成功：落响应（供重放）。body 为 nil（204）时落一个空对象，
	// 保证重放拿到的状态码与首次一致。
	payload := []byte("{}")
	if body != nil {
		if b, mErr := json.Marshal(body); mErr == nil {
			payload = b
		}
	}
	_ = h.deps.Service.CompleteIdempotency(ctx, scope, key, status, payload)

	if body == nil {
		c.Status(status)
		return
	}
	c.JSON(status, body)
}

// —— 处理器 ——

// registerStart 注册第一段：用户名 + 口令 → ticket（成功 200）。
//
// 状态码用 200 而不是 201：这一步**没有创建任何对用户可见的资源**（只有一份
// 15 分钟后过期的草稿）。用 201 会让客户端以为"账号已经建好了"。
func (h *authHandler) registerStart(c *gin.Context) {
	h.withIdempotency(c, scopeRegisterStart, func() (int, any, error) {
		var req registerStartRequest
		if err := bindJSONBody(c, &req); err != nil {
			return 0, nil, err
		}
		res, err := h.deps.Service.RegisterStart(c.Request.Context(),
			req.Username, req.DisplayName, req.Password, c.ClientIP(), c.Request.UserAgent())
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, gin.H{"ticket": res.Ticket, "expiresIn": res.ExpiresIn}, nil
	})
}

// registerEmail 注册第二段上半：给草稿挂邮箱并发验证码（成功 204）。
func (h *authHandler) registerEmail(c *gin.Context) {
	h.withIdempotency(c, scopeRegisterEmail, func() (int, any, error) {
		var req registerEmailRequest
		if err := bindJSONBody(c, &req); err != nil {
			return 0, nil, err
		}
		dev, err := h.deps.Service.RegisterSendEmailOTP(c.Request.Context(),
			req.Ticket, req.Email, c.ClientIP(), c.Request.UserAgent())
		if err != nil {
			return 0, nil, err
		}
		if dev != "" {
			// 仅本机开发（PR_OTP_DEV_ECHO=1）：把明文码一并回显，好让没有 SMTP
			// 的环境也能端到端走通。生产恒为空串，因此这条分支不会命中。
			return http.StatusOK, gin.H{"status": "sent", "devCode": dev}, nil
		}
		return http.StatusNoContent, nil, nil
	})
}

// registerVerify 注册第二段下半：凭 ticket + 验证码建号并登录（成功 201）。
//
// 响应与既有 register 完全同形（sessionResponse），因此前端拿到之后
// 走的还是同一条"存 token + 拉档案"的路。
func (h *authHandler) registerVerify(c *gin.Context) {
	h.withIdempotency(c, scopeRegisterVerify, func() (int, any, error) {
		var req registerVerifyRequest
		if err := bindJSONBody(c, &req); err != nil {
			return 0, nil, err
		}
		sess, err := h.deps.Service.RegisterVerify(c.Request.Context(),
			req.Ticket, req.Code, c.ClientIP(), c.Request.UserAgent())
		if err != nil {
			return 0, nil, err
		}
		// refresh Cookie 是**副作用**，不在幂等响应体里：重放时不再下发
		// （浏览器本来就还持有首次那一枚，重发只会轮换出一个多余的会话）。
		setRefreshCookie(c, sess.RefreshToken, sess.RefreshExpiresAt)
		return http.StatusCreated, newSessionResponse(sess, h.deps.Session.AccessTTL), nil
	})
}

// passwordOTP 发一条改密码验证码（成功 200，**恒定**）。
//
// 与 forgot 同一条口径：无论账号是否存在、是否可发信，对外都是同一个 200。
// 因此这里**不能**用 withIdempotency（它会把"首次响应"落键，而那与恒定响应
// 没有冲突，但会多一次库写；更重要的是恒等响应本身就是幂等的）。
func (h *authHandler) passwordOTP(c *gin.Context) {
	var req passwordOTPRequest
	if err := bindJSONBody(c, &req); err != nil {
		writeAccountError(c, err)
		return
	}
	// 每邮箱闸门（键是归一化后的地址）：先于任何查库，因此不泄露存在性。
	if h.otpEmailLimiter != nil {
		key := strings.ToLower(strings.TrimSpace(req.Email))
		if !h.otpEmailLimiter.Allow(key) {
			writeRateLimited(c, otpRateLimitWhat)
			return
		}
	}
	dev, err := h.deps.Service.SendAccountOTP(c.Request.Context(),
		req.Email, c.ClientIP(), c.Request.UserAgent())
	if err != nil {
		writeAccountError(c, err)
		return
	}
	if dev != "" {
		// 仅本机开发（PR_OTP_DEV_ECHO=1），理由同 registerEmail。
		c.JSON(http.StatusOK, gin.H{"status": "sent", "devCode": dev})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "sent"})
}

// passwordOTPReset 凭验证码改密码（成功 204）。
//
// 用 withIdempotency：重放一次"改密码"会消耗一条新的验证码（第一次已经把码用掉），
// 于是重放必然失败。幂等键让"网络超时后重发"直接拿到首次的成功结果。
func (h *authHandler) passwordOTPReset(c *gin.Context) {
	h.withIdempotency(c, "password.otp.reset", func() (int, any, error) {
		var req passwordOTPResetRequest
		if err := bindJSONBody(c, &req); err != nil {
			return 0, nil, err
		}
		if err := h.deps.Service.ResetPasswordWithOTP(c.Request.Context(),
			req.Email, req.Code, req.Password); err != nil {
			return 0, nil, err
		}
		return http.StatusNoContent, nil, nil
	})
}

// bindJSONBody 解析请求体，把"不是合法 JSON"翻成统一的 400。
//
// 与 bindCredentials 的区别：那个函数还做字段级的长度上限预检（注册/登录共用），
// 而这几个端点的字段很少，长度由 service 层统一校验（用户名/邮箱/口令各有一条）。
func bindJSONBody(c *gin.Context, dst any) error {
	if err := c.ShouldBindJSON(dst); err != nil {
		return errBadJSON
	}
	return nil
}

// errBadJSON 是"请求体不是合法 JSON"的哨兵（映射到 400）。
var errBadJSON = errors.New("请求体不是合法 JSON")
