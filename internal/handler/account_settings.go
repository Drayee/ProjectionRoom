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
// 票 #72 加了换绑邮箱两个端点；票 #73 加了注销冷静期的三个端点
// （发起 / 撤销 / 读状态）。
//
// 不做：改用户名（#51 Out of scope）、头像上传（#74 / #75）。

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/model"
	"ProjectionRoom/internal/store"
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

// 换绑邮箱的两个 scope（票 #72）。
//
// 发码与确认**必须是两个 scope**：它们请求体不同（{email} vs {email,code}），
// 而幂等壳对"同键不同体"回 409 —— 共用 scope 会让前端复用一把键时
// 在"发完码去确认"这一步撞上 409，看起来像"服务端拒绝了我的确认"。
const (
	scopeEmailChangeOTP     = "me.email.otp"
	scopeEmailChangeConfirm = "me.email.confirm"
)

// sendEmailChangeOTPRequest 是 POST /api/me/email/otp 的请求体。
//
// 只有 `email`：调用方身份来自 token，**不**从请求体取 userID —— 能从请求体
// 指定"给谁换绑"的接口，第一步就要回答"能不能传别人的 id"。
type sendEmailChangeOTPRequest struct {
	Email string `json:"email"`
}

// confirmEmailChangeRequest 是 PUT /api/me/email 的请求体。
//
// `code` 是发到**新地址**的那 6 位数字。地址必须再传一次（而不是从服务端
// 会话里取"最近一次发码的地址"）：那会引入一份额外状态，且它一旦与用户
// 真正想绑的地址不一致，用户就永远确认不了自己填的地址。
type confirmEmailChangeRequest struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

// errEmailChangeFieldsRequired 是"少填了一栏"的入参哨兵。
//
// 与 errPasswordFieldsRequired 同族：这一层能判定，且它的文案与 service 的
// 策略/占用类错误是两件事 —— 前者是"你漏填了"，后者是"你填的不对"。
var errEmailChangeFieldsRequired = errors.New("请填写新邮箱与验证码")

// —— 注销冷静期（票 #73 / 决议 #51 §5）——

// scopeDeletionRequest / scopeDeletionCancel 是注销的两个幂等 scope。
//
// 必须分开：发起收 `{currentPassword}`、撤销收空体，共用 scope 会让
// "发起之后点撤销"撞 409 同键不同体（而这两件事本来就是连续动作）。
const (
	scopeDeletionRequest = "me.deletion.request"
	scopeDeletionCancel  = "me.deletion.cancel"
)

// requestDeletionRequest 是 POST /api/me/deletion 的请求体。
//
// 只有当前密码。**确认勾选不进请求体**：它是一个纯前端的前置条件
// （决议 #51 §5「勾选『我已知晓数据将在 30 天后永久删除』」），
// 传到服务端只会变成第二个可以伪造的字段 —— 服务端该判的是"是不是你"，
// 不是"你勾了没"。
type requestDeletionRequest struct {
	CurrentPassword string `json:"currentPassword"`
}

// errDeletionPasswordRequired 是"没填当前密码"的入参哨兵。
//
// 与 errPasswordFieldsRequired 分开：那条文案说"请填写当前密码与新密码"，
// 这里只有一个栏位，复用会让用户去找一个不存在的输入框。
var errDeletionPasswordRequired = errors.New("请输入当前密码以确认注销")

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
		api.POST("/me/email/otp", unavailable)
		api.PUT("/me/email", unavailable)
		api.GET("/me/deletion", unavailable)
		api.POST("/me/deletion", unavailable)
		api.DELETE("/me/deletion", unavailable)
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

	// 换绑邮箱两个端点（票 #72）挂**登录桶**：发码会真的花掉邮件配额，
	// 而确认是一个**验证码猜测面**（6 位数字，5 次即作废）。两者都按登录
	// 档位限制，理由与改密相同。
	//
	// 额外的每邮箱闸门（mailPerEmailLimiter）不在这里挂：它按**收件地址**
	// 计数，而这里的收件地址来自请求体、每个用户各填各的 —— 逐地址限流
	// 放在 service 的冷却期（60s）+ attempts（5 次）里已经足够，
	// 再叠一层按地址的桶只会让"我打错了地址想改一下"被拦。
	emailLimiter := newKeyedLimiter(cfg.IPC.LoginPerMinute, cfg.IPC.LoginBurst, "换绑邮箱")
	api.POST("/me/email/otp", RequireAuth(auth), emailLimiter, h.sendEmailChangeOTP)
	api.PUT("/me/email", RequireAuth(auth), emailLimiter, h.confirmEmailChange)

	// 注销（票 #73）：三个端点，两条桶。
	//
	// 发起挂**登录桶** —— 它要校验当前密码，是一个口令猜测面（与改密同一条理由）。
	// 撤销与读状态挂**只读桶** —— 撤销不需要凭据、只是把一列清空（幂等、无滥用面），
	// 读状态更是纯查询；拿登录桶去卡它们会让"我误点了想撤回"被限流挡住，
	// 而那正是最不能挡的时刻。
	deletionLimiter := newKeyedLimiter(cfg.IPC.LoginPerMinute, cfg.IPC.LoginBurst, "发起注销")
	readLimiter := newKeyedLimiter(cfg.IPC.RoomMetaPerMinute, cfg.IPC.RoomMetaBurst, "注销状态")
	api.GET("/me/deletion", RequireAuth(auth), readLimiter, h.getDeletion)
	api.POST("/me/deletion", RequireAuth(auth), deletionLimiter, h.requestDeletion)
	api.DELETE("/me/deletion", RequireAuth(auth), readLimiter, h.cancelDeletion)
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

// sendEmailChangeOTP 是 `POST /api/me/email/otp`（票 #72）。
//
// 状态码与判据：
//
//	401 SESSION_INVALID     没有/无效的 access token
//	400 BAD_REQUEST         邮箱格式不对（文案带原因）或请求体不是 JSON
//	409 EMAIL_TAKEN         新地址已被**别的**账号占用
//	400 EMAIL_UNCHANGED     新地址与当前邮箱相同
//	400 OTP_TOO_SOON        60 秒冷却期内重复发码
//	503 AUTH_UNAVAILABLE    实例没配邮件且没开 dev echo
//	429 RATE_LIMITED        撞上登录桶
//	200 {"status":"sent"}   已发码
//
// 为什么响应体与找回密码那条门**同形**（`{"status":"sent"}`）：
// 前端两条路可以共用同一段"已发送"提示与同一个倒计时组件。但**语义不同**：
// 找回密码对外恒定 200（防枚举），这里可以如实报错（已登录、目标地址就是
// 用户自己填的，报错不泄露任何他人的信息）。
//
// 为什么不回显验证码（除了 dev echo）：回显等于把"证明你拥有新邮箱"这件事
// 降级成"你能调这个接口"，换绑的全部意义就没了。dev echo 只在
// `PR_OTP_DEV_ECHO=1`（本机开发）时打开，且由 service 统一决定。
func (h *accountSettingsHandler) sendEmailChangeOTP(c *gin.Context) {
	runWithIdempotency(c, h.auth, scopeEmailChangeOTP, func() (int, any, error) {
		actor, ok := CurrentUser(c)
		if !ok {
			return 0, nil, errSettingsSessionInvalid
		}

		var req sendEmailChangeOTPRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			return 0, nil, errBadJSON
		}
		if strings.TrimSpace(req.Email) == "" {
			return 0, nil, errEmailChangeFieldsRequired
		}

		devCode, err := h.auth.SendEmailChangeOTP(c.Request.Context(),
			actor.ID, req.Email, c.ClientIP(), c.Request.UserAgent())
		if err != nil {
			return 0, nil, err
		}
		if devCode != "" {
			// 仅 PR_OTP_DEV_ECHO=1 时可达。字段名与注册/找回那条门一致，
			// 前端与真机脚本读的是同一个键。
			return http.StatusOK, gin.H{"status": "sent", "devCode": devCode}, nil
		}
		return http.StatusOK, gin.H{"status": "sent"}, nil
	})
}

// confirmEmailChange 是 `PUT /api/me/email`（票 #72）。
//
// 状态码与判据：
//
//	401 SESSION_INVALID        没有/无效的 access token
//	400 BAD_REQUEST            少填一栏、地址格式不对、验证码不对（OTP_INVALID）
//	409 EMAIL_TAKEN            发码与确认之间这个地址被别人抢走了
//	403 FORBIDDEN              账号被封禁
//	429 RATE_LIMITED           撞上登录桶（也是一个验证码猜测面）
//	200 + 更新后的 Profile     换绑成功，email 已是新地址、emailVerified 为 true
//
// 为什么回完整 Profile 而不是 204：前端要立刻把"未验证邮箱"的提示撤掉、
// 把邮箱显示改成新地址。让它再发一次 `GET /api/auth/me` 意味着中间那一帧
// 界面上还挂着旧地址（用户会以为没成功而再点一次确认，那第二次必然失败
// —— 码是单次使用的）。
func (h *accountSettingsHandler) confirmEmailChange(c *gin.Context) {
	runWithIdempotency(c, h.auth, scopeEmailChangeConfirm, func() (int, any, error) {
		actor, ok := CurrentUser(c)
		if !ok {
			return 0, nil, errSettingsSessionInvalid
		}

		var req confirmEmailChangeRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			return 0, nil, errBadJSON
		}
		if strings.TrimSpace(req.Email) == "" || strings.TrimSpace(req.Code) == "" {
			return 0, nil, errEmailChangeFieldsRequired
		}

		if err := h.auth.ConfirmEmailChange(c.Request.Context(), actor.ID, req.Email, req.Code); err != nil {
			return 0, nil, err
		}

		// 回读一次拿最新 Profile（换绑只改 users 一行，代价是一次主键查询）。
		//
		// 这里**不**复用 actor：那是中间件在请求开始时读到的快照，邮箱还是旧的。
		prof, err := h.auth.Me(c.Request.Context(), actor.ID)
		if err != nil {
			// 换绑已经落库了，回读失败不该让用户看到"失败"（他会再点一次，
			// 而码已经用掉了）。回 200 + 空 profile 更糟（前端会显示空邮箱），
			// 所以这里如实报错但把日志写清楚：用户刷新一次就能看到正确状态。
			return 0, nil, err
		}
		return http.StatusOK, prof, nil
	})
}

// getDeletion 是 `GET /api/me/deletion`（票 #73）。
//
// 给 `/me` 顶部的撤销横幅用：冷静期里的横幅要显示"还有 N 天"，而 N 需要
// 起点与冷静期两个值。返回 `{"pending":bool,"requestedAt":?,"deadline":?}`。
//
// 为什么不复用 `/api/auth/me` 里已有的 `deletionRequestedAt`：那个只有起点，
// 到期日需要前端自己知道冷静期是几天 —— 而"冷静期多久"是服务端配置
// （`PR_DELETION_GRACE`），把它复制到前端就是第二份真源。改配置时前端不改，
// 横幅就会显示一个永远不会到来的日期。
func (h *accountSettingsHandler) getDeletion(c *gin.Context) {
	actor, ok := CurrentUser(c)
	if !ok {
		writeAccountError(c, errSettingsSessionInvalid)
		return
	}
	st, err := h.auth.DeletionState(c.Request.Context(), actor.ID)
	if err != nil {
		writeAccountError(c, err)
		return
	}
	c.JSON(http.StatusOK, newDeletionStateView(st))
}

// requestDeletion 是 `POST /api/me/deletion`（票 #73）。
//
// 状态码与判据：
//
//	401 SESSION_INVALID     没有/无效的 access token
//	400 BAD_REQUEST         没填当前密码，或请求体不是 JSON
//	400 PASSWORD_MISMATCH   当前密码不对
//	403 FORBIDDEN           账号被封禁（封禁不该成为"删数据"的快捷方式）
//	429 RATE_LIMITED        撞上登录桶
//	200 + 状态              已进入冷静期（幂等：重复发起返回**原**起点，不延期）
//
// 为什么回 200 + 状态而不是 204：前端要立刻把横幅挂出来（含到期日）。
// 回 204 的话它得再发一次 GET，中间那一帧用户看不到"我到底成功了没"。
func (h *accountSettingsHandler) requestDeletion(c *gin.Context) {
	runWithIdempotency(c, h.auth, scopeDeletionRequest, func() (int, any, error) {
		actor, ok := CurrentUser(c)
		if !ok {
			return 0, nil, errSettingsSessionInvalid
		}

		var req requestDeletionRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			return 0, nil, errBadJSON
		}
		if strings.TrimSpace(req.CurrentPassword) == "" {
			return 0, nil, errDeletionPasswordRequired
		}

		if _, err := h.auth.RequestAccountDeletion(c.Request.Context(), actor.ID, req.CurrentPassword); err != nil {
			return 0, nil, err
		}

		// 回读状态：冷静期长度来自服务端配置，起点来自刚写的那一列。
		st, err := h.auth.DeletionState(c.Request.Context(), actor.ID)
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, newDeletionStateView(st), nil
	})
}

// cancelDeletion 是 `DELETE /api/me/deletion`（票 #73）。
//
// 状态码与判据：
//
//	401 SESSION_INVALID        没有/无效的 access token
//	409 DELETION_NOT_PENDING   没有待执行的注销（重复撤销）
//	200 + 状态                 已撤销
//
// **不要密码**（决议 #51 §5）：撤销是降低风险的方向。多一道门只会让
// "我误点了想撤回"更难，而那正是这道门存在的理由。
//
// 为什么走幂等壳：撤销本身是幂等的（清一列），但重发拿到同一个成功响应
// 比拿到 409 更符合用户预期 —— 而 409 只该出现在"你其实没申请过"这种
// 真正的前后不一致上，不该出现在"我刚才已经撤了"这种正常重试上。
// 因此这里**不**走幂等壳，让 409 如实反映状态。
func (h *accountSettingsHandler) cancelDeletion(c *gin.Context) {
	actor, ok := CurrentUser(c)
	if !ok {
		writeAccountError(c, errSettingsSessionInvalid)
		return
	}
	if err := h.auth.CancelAccountDeletion(c.Request.Context(), actor.ID); err != nil {
		writeAccountError(c, err)
		return
	}
	st, err := h.auth.DeletionState(c.Request.Context(), actor.ID)
	if err != nil {
		writeAccountError(c, err)
		return
	}
	c.JSON(http.StatusOK, newDeletionStateView(st))
}

// deletionStateView 是注销状态对外的形状。
//
// 时间用 RFC3339 字符串（与其它端点的 `createdAt` 一致），`null` 表示
// "不在冷静期" —— 与"零点值"区分开。前端据此决定挂不挂横幅。
type deletionStateView struct {
	Pending     bool    `json:"pending"`
	RequestedAt *string `json:"requestedAt"`
	Deadline    *string `json:"deadline"`
}

func newDeletionStateView(st store.DeletionState) deletionStateView {
	v := deletionStateView{Pending: st.Pending()}
	if st.RequestedAt != nil {
		s := st.RequestedAt.UTC().Format(time.RFC3339)
		v.RequestedAt = &s
	}
	if st.Deadline != nil {
		s := st.Deadline.UTC().Format(time.RFC3339)
		v.Deadline = &s
	}
	return v
}

// 保住 model 的引用：本文件的两个哨兵走 accountErrorResponse（它用 model.Code*），
// 但把这条 import 显式留下，是为了让"本域的 400 用哪个码"留在显眼处 ——
// 与 users.go 末尾同一条理由。
var _ = model.CodeBadRequest
