package handler

import (
	"errors"
	"net/http"

	"ProjectionRoom/internal/model"
	"ProjectionRoom/internal/service"
	"ProjectionRoom/internal/service/auth"
)

// —— 账号层（ACCOUNTS §10）的错误码。
//
// 为什么定义在 handler 而不是 internal/model：model 的 Code* 是**WebSocket 信封**的
// 字段取值（Envelope.Code），而这一组只出现在 REST 响应体里，两者是不同的协议面。
// 命名与风格刻意与 model 保持一致（同一个单词、大写下划线），前端因此只有一套映射表。
//
// 关于 model.ErrorEnvelope：那是 WS 的错误**信封**（Envelope 的一种 type），
// REST 侧的既有约定是 gin.H{"error": …, "code": …}（见 router.go / errors.go），
// 新建一套信封会让前端多一条解析路径，所以这里沿用既有约定、只补错误码。
const (
	// CodeLoginFailed 表示用户名或密码错误（**不区分**账号是否存在）。
	CodeLoginFailed = "LOGIN_FAILED"
	// CodeUsernameTaken 表示注册时用户名已被占用。
	CodeUsernameTaken = "USERNAME_TAKEN"
	// CodeUserBanned 表示账号已被封禁。
	CodeUserBanned = "USER_BANNED"
	// CodeSessionInvalid 表示 access token 与账号当前状态不符（token 版本落后/账号已删），
	// 或 refresh 凭据无效。
	CodeSessionInvalid = "SESSION_INVALID"
	// CodeSessionExpired 表示 access token 已过期（与"无效"分开：前端可以静默刷新）。
	CodeSessionExpired = "SESSION_EXPIRED"
	// CodeRefreshReplay 表示 refresh 凭据重放，该账号的全部会话已被撤销。
	CodeRefreshReplay = "REFRESH_REPLAY"
	// CodeUnauthorized 表示请求缺少/带了无法解析的凭据（没有 Authorization: Bearer …）。
	CodeUnauthorized = "UNAUTHORIZED"
	// CodeForbidden 表示已认证但权限不足（非管理员访问 /api/admin/*）。
	CodeForbidden = "FORBIDDEN"
	// CodeAuthBusy 表示并发口令哈希闸门已满（服务端主动拒绝，客户端应退避重试）。
	CodeAuthBusy = "AUTH_BUSY"
	// CodeAuthUnavailable 表示账号能力未启用或装配未完成（认证路由一律 503）。
	CodeAuthUnavailable = "AUTH_UNAVAILABLE"
	// CodeWSTicketInvalid 表示 /ws 的一次性票据不可用（无效 / 已过期 / 已被使用过）。
	//
	// 它只出现在 **握手之前** 的拒绝上（HTTP 401）。客户端唯一正确的反应是重新取票，
	// 而不是拿同一张票重连 —— 票据是单次的，重连只会连续拿到 401。
	CodeWSTicketInvalid = "WS_TICKET_INVALID"
)

// accountErrorResponse 把账号层的领域错误映射为 HTTP 状态码、错误码与用户可读消息。
//
// 逐条取舍：
//   - ErrInvalidCredentials → 401 + **统一文案**：任何区分"用户不存在/口令错"的文案
//     都是一条账号枚举通道（§5）；
//   - ErrUserBanned → 403：能拿到它的前提是口令已通过，因此不构成枚举面，可以说明白；
//   - 过期的 access token → 401 + SESSION_EXPIRED：前端据此走静默刷新而不是弹登录框；
//   - ErrHashingBusy → 429：这是**限流**语义（稍后重试有意义），不是服务端内部错误；
//   - ErrRefreshReplay → 401 + REFRESH_REPLAY：客户端唯一正确的反应是清掉本地状态并重新登录。
func accountErrorResponse(err error) (status int, code string, message string) {
	switch {
	case err == nil:
		return http.StatusOK, "", ""

	case errors.Is(err, service.ErrInvalidCredentials):
		return http.StatusUnauthorized, CodeLoginFailed, "用户名或密码错误"
	case errors.Is(err, service.ErrUserBanned):
		return http.StatusForbidden, CodeUserBanned, "该账号已被封禁"
	case errors.Is(err, service.ErrSessionInvalid):
		return http.StatusUnauthorized, CodeSessionInvalid, "会话已失效，请重新登录"
	case errors.Is(err, service.ErrRefreshReplay):
		return http.StatusUnauthorized, CodeRefreshReplay, "刷新凭据已失效（检测到重放），请重新登录"
	case errors.Is(err, service.ErrHashingBusy):
		return http.StatusTooManyRequests, CodeAuthBusy, "服务繁忙，请稍后重试"

	case errors.Is(err, auth.ErrExpiredToken):
		return http.StatusUnauthorized, CodeSessionExpired, "登录状态已过期"
	case errors.Is(err, auth.ErrInvalidToken):
		return http.StatusUnauthorized, CodeSessionInvalid, "登录状态无效，请重新登录"

	case errors.Is(err, service.ErrUsernameTaken):
		return http.StatusConflict, CodeUsernameTaken, "该用户名已被占用"
	case errors.Is(err, service.ErrBadUsername), errors.Is(err, service.ErrBadDisplayName):
		return http.StatusBadRequest, model.CodeBadRequest, err.Error()

	// 口令策略的每一条都给出具体原因：注册不是枚举面（§5 的统一文案只针对登录），
	// 而"缺哪一条"是用户改一次就能过的关键信息。
	case errors.Is(err, auth.ErrPasswordEmpty),
		errors.Is(err, auth.ErrPasswordTooShort),
		errors.Is(err, auth.ErrPasswordTooLong),
		errors.Is(err, auth.ErrPasswordNoLetter),
		errors.Is(err, auth.ErrPasswordNoDigit):
		return http.StatusBadRequest, model.CodeBadRequest, err.Error()

	case errors.Is(err, service.ErrBadRole):
		return http.StatusBadRequest, model.CodeBadRequest, err.Error()

	// 目标账号不存在：对管理员来说是"操作对象错了"，用 404 比 500 准确。
	// 这条不构成枚举面 —— 只有管理员能走到这里。
	//
	// 判据用 service.StoreNotFound（它本身是 store.ErrNotFound 的别名）：
	// 为什么不让 handler 直接 import store —— handler 只认识 service 与 service/auth
	// 两个下层（见 router.go 的 import 面），为了一个哨兵错误把持久化包拉进 HTTP 层，
	// 会让"handler 依赖了什么"变得难以一眼看完。别名保持了判断能力，没有扩大依赖面。
	case errors.Is(err, service.StoreNotFound):
		return http.StatusNotFound, model.CodeBadRequest, "账号不存在"

	default:
		return http.StatusInternalServerError, model.CodeInternalError, "服务端内部错误"
	}
}

// roomErrorResponse 把 service 包的领域错误映射为 HTTP 状态码、协议错误码与用户可读消息。
// REST 与 WebSocket 两条入口共用它，保证同一错误在任何通道上的表述一致。
func roomErrorResponse(err error) (status int, code string, message string) {
	switch {
	case errors.Is(err, service.ErrNotFound):
		return http.StatusNotFound, model.CodeRoomNotFound, "房间不存在"
	case errors.Is(err, service.ErrBadPassword):
		return http.StatusForbidden, model.CodeBadPassword, "房间密码错误"
	case errors.Is(err, service.ErrFull):
		return http.StatusConflict, model.CodeRoomFull, "房间已满（受主播上行限制）"
	case errors.Is(err, service.ErrHostTaken):
		return http.StatusConflict, model.CodeHostTaken, "房间已有主播"
	case errors.Is(err, service.ErrNotReady):
		return http.StatusConflict, model.CodeRoomNotReady, "主播尚未进房"
	case errors.Is(err, service.ErrAlreadyJoined):
		return http.StatusConflict, model.CodeAlreadyJoin, "该连接已在房间中"
	case errors.Is(err, service.ErrNotJoined):
		return http.StatusBadRequest, model.CodeNotJoined, "尚未加入房间"
	case errors.Is(err, service.ErrNotHost):
		return http.StatusForbidden, model.CodeNotHost, "只有主播可以执行该操作"
	case errors.Is(err, service.ErrBadMediaIndex):
		return http.StatusBadRequest, model.CodeBadMediaIndex, "分片索引不合法"
	case errors.Is(err, service.ErrMediaLocked):
		return http.StatusConflict, model.CodeMediaLocked, "分片索引已锁定，换片需重开房间"
	case errors.Is(err, service.ErrBadName):
		return http.StatusBadRequest, model.CodeBadRequest, err.Error()
	case errors.Is(err, service.ErrBadInput):
		return http.StatusBadRequest, model.CodeBadRequest, "参数不合法"
	case errors.Is(err, service.ErrRoomExists):
		return http.StatusConflict, model.CodeBadRequest, "房间码已存在"
	// —— 以下三条是 S-3 / S-7 / S-11 新增闸门的对外表述。
	// 状态码选择：400（参数不合白名单）/ 503（服务端在册房间到顶，不是客户端的错）/ 403（令牌缺失或错误）。
	case errors.Is(err, service.ErrBadRoomCode):
		return http.StatusBadRequest, model.CodeBadRequest, err.Error()
	case errors.Is(err, service.ErrBadPasswordPolicy):
		return http.StatusBadRequest, model.CodeBadRequest, err.Error()
	case errors.Is(err, service.ErrTooManyRooms):
		return http.StatusServiceUnavailable, model.CodeTooManyRooms, err.Error()
	case errors.Is(err, service.ErrHostTokenRequired):
		return http.StatusForbidden, model.CodeHostTokenRequired, err.Error()
	case errors.Is(err, service.ErrJoinRateLimited):
		return http.StatusTooManyRequests, model.CodeRateLimited, err.Error()
	default:
		return http.StatusInternalServerError, model.CodeInternalError, "服务端内部错误"
	}
}
