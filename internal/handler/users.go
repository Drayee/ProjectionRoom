// 用户公开档案与本人资料编辑（wayfinder #33）。docs/ACCOUNTS.md §7.3 / §10。
//
// 两个端点，各自的准入不同 —— 这个差别是本文件的核心：
//
//	GET   /api/users/:id   免登录（公开档案）
//	PATCH /api/me          必须登录（改自己的昵称）
//
// 为什么公开档案**免登录**：它是 `/posts` 的作者名、`/friends` 的搜索结果、
// `/messages` 的会话对方这些地方点进来的落点。那些入口本身大多免登录（帖子列表
// 就是），如果档案页要求登录，用户点一个作者名会被弹去登录页 —— 而他要看的只是
// "这个人是谁"。产品上这是"公开信息"的定义，不是"权限放宽"。
//
// 为什么改昵称**必须登录**：它改的是"我是谁"，而身份只能来自校验过的 access token
// （不变量 I3）。userID 绝不从请求体取 —— 那等于让任何人改别人的昵称。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/model"
	"ProjectionRoom/internal/service"
)

// maxDisplayNamePatchRunes 是 PATCH /api/me 的昵称入参上限。
//
// 取 4 × service.MaxDisplayNameLen（32）= 128，而不是直接 32：清洗（StripControl）
// 会先**去掉**零宽/Bidi 字符，所以"看起来 40 个字、清洗后 30 个字"是合法输入。
// 用 32 当闸门会把这类输入挡在清洗之前，而错误文案会变成"太长了"——
// 但用户数的是他看到的字符数，那会是一个无法解释的拒绝。
//
// 上限本身仍然必要：没有它，一个 10 MB 的 body 会先被读进内存再被清洗。
const maxDisplayNamePatchRunes = 4 * service.MaxDisplayNameLen

// CodeDisplayNameInvalid 是昵称不合规的错误码（与 400 配对）。
//
// 与 model.CodeBadRequest 分开：前者让前端能把错误定位到**那一个输入框**
// （设置页只有这一个可编辑字段），后者是"整个请求有问题"。
const CodeDisplayNameInvalid = "DISPLAY_NAME_INVALID"

// CodeAvatarURLInvalid 是"这个头像地址不是我们发的"（票 #74）。
//
// 与 DISPLAY_NAME_INVALID 分开：一个是名字内容不合规、一个是地址来源不可信，
// 前端要给的下一步不同（改名 vs 重新走上传）。
const CodeAvatarURLInvalid = "AVATAR_URL_INVALID"

// UserProfileService 是本文件需要的账号能力（wayfinder #33）。
//
// 为什么单独声明一个窄接口而不是直接用 AccountService：本文件只需要两个方法，
// 而 AccountService 有 20+ 个（注册/找回/OTP/幂等）。把整张表塞进依赖，
// 会让"公开档案端点能对账号做什么"变成一个要读完 20 行才知道的问题。
//
// *service.AccountService 的方法集满足它；测试可以只实现这两个。
type UserProfileService interface {
	PublicProfile(ctx context.Context, userID int64) (service.PublicProfile, error)
	UpdateMyDisplayName(ctx context.Context, userID int64, displayName string) (service.Profile, error)
	// UpdateMyAvatarURL 改像素头像（票 #74）；nil = 清除。
	UpdateMyAvatarURL(ctx context.Context, userID int64, avatarURL *string) (service.Profile, error)
}

// registerUserProfileRoutes 注册公开档案与本人资料路由。
//
// 与其它二期路由同一套约定：账号能力关闭（`PR_DB_DSN` 为空）时整个域**不注册**；
// 装配残缺时注册一组 503（而不是让路由凭空消失 —— 那会让"页面 404"与
// "后端没上线"变成同一件事，而它们该有不同的排查路径）。
//
// ⚠ **路由树的两条硬约束**（本票踩过的坑，写在这里防止下次改坏）：
//
//  1. `GET /api/users/search` 由 #39 注册在**同一个** `/api/users` 前缀下。
//     gin 的路由树里静态段优先于参数段，所以 `search` 与 `:id` 可以共存 ——
//     已用一次性探针实测（#39 的注释里也记了同一条）。但**顺序不影响**这一点：
//     gin 在注册时就把它们分成两个节点，谁先注册都一样。
//  2. `:id` 必须是**正整数**。gin 的 `:id` 会匹配任意单段（含 "search"），
//     所以本文件必须自己校验并回 400 —— 但那种情况其实到不了这里
//     （静态段先命中），这道校验是为了"将来有人加了新的静态段却忘了排序"时不至于
//     静默把它当成一个用户 id。
func registerUserProfileRoutes(api *gin.RouterGroup, svc UserProfileService, cfg *config.Config, auth AccountService) {
	if api == nil {
		return
	}
	if cfg == nil || strings.TrimSpace(cfg.Auth.DBDSN) == "" {
		return
	}
	if svc == nil {
		unavailable := func(c *gin.Context) {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "用户资料暂不可用",
				"code":  CodeAuthUnavailable,
			})
		}
		api.GET("/users/:id", unavailable)
		api.PATCH("/me", unavailable)
		return
	}

	h := &userProfileHandler{svc: svc}

	// 公开档案：**免登录**，但挂只读桶。
	//
	// 为什么免登录的端点也要限速：它是按 id 递增的可枚举面 —— 一个脚本可以
	// 1..100000 扫过去，把全站用户名与加入时间拉下来。限速不能替代"字段白名单"
	// （那是另一种保护），但能把批量扫的成本抬到不划算。
	// 桶的选择：只读桶（PR_ROOM_META_*，60/min）—— 与好友搜索同一个档位，
	// 理由也相同（高频、幂等、纯读）。
	readLimiter := newKeyedLimiter(cfg.IPC.RoomMetaPerMinute, cfg.IPC.RoomMetaBurst, "用户公开档案")
	api.GET("/users/:id", readLimiter, h.publicProfile)

	// 改昵称：必须登录 + 登录桶。
	//
	// 用登录桶（10/min）而不是注册桶：改昵称不污染信息流（它只改自己一行），
	// 但它是写操作且有 bcrypt 之外的清洗成本。注册桶（5/min）会把"改两次试试
	// 哪个名字好看"打成 429 —— 那是正常用户的用法。
	writeLimiter := newKeyedLimiter(cfg.IPC.LoginPerMinute, cfg.IPC.LoginBurst, "修改昵称")
	api.PATCH("/me", RequireAuth(auth), writeLimiter, h.patchMe)
}

type userProfileHandler struct {
	svc UserProfileService
}

// publicProfile 是 `GET /api/users/:id`（公开档案，免登录）。
//
// 三种输入，两种输出：
//
//	合法 id 且账号可见  → 200 + PublicProfile
//	id 不存在 / 被封禁  → 404 + NOT_FOUND（**同一个响应**，理由见 service.ErrUserNotFound）
//	id 不是正整数       → 400 + BAD_REQUEST
//
// 第三种的取舍：不把"id 写错了"也吞成 404。前者是**调用方**的问题（前端拼错了 URL、
// 或者路由参数没转成数字），后者是**资源**的问题，两者的排查方向完全不同 ——
// 而 400 的错误文案能让开发者一眼看出是哪一类。
func (h *userProfileHandler) publicProfile(c *gin.Context) {
	id, ok := adminPathID(c, "id")
	if !ok {
		return
	}

	p, err := h.svc.PublicProfile(c.Request.Context(), id)
	if err != nil {
		h.writeProfileError(c, err)
		return
	}
	c.JSON(http.StatusOK, p)
}

// patchMeRequest 是 PATCH /api/me 的请求体。
//
// DisplayName 用**指针**：`{}`（什么都没传）与 `{"displayName": ""}`（传了空串）
// 是两件不同的事 —— 前者是"这次不改"，后者是"清空昵称"（而清空会被清洗回落成
// 用户名）。用非指针的话两者都是零值 ""，服务端就分不出来了。
//
// 这是 #37 的 PATCH 踩过的同一类坑（无条件传 `&req.CoverURL` 使"没传"变成"清空封面"）：
// 指针的**nil 判据**才是"用户有没有传这个字段"的唯一答案。
type patchMeRequest struct {
	DisplayName *string `json:"displayName"`
	// AvatarURL 是像素头像（票 #74）。**指针同样是刻意的**，但这里有三态：
	//
	//	字段缺席      → nil，且 "avatarUrl" 不在原始 JSON 里 → 这次不改头像
	//	"avatarUrl": null → 指针为 nil，但键在 → **清除**（回落字母头）
	//	"avatarUrl": "..." → 设成这个 URL（必须属于本桶）
	//
	// 所以判"有没有传这个字段"不能只看指针 nil，必须看原始 JSON 里有没有这个键
	//（见 patchMe 里的 avatarURLPresent）。这是 #37 那个「无条件传 &req.CoverURL
	// 使『没传』变成『清空封面』」的坑的正面版本：那时是分不出，
	// 这里是**必须分开**——"没传"与"清空"在 PATCH 语义下是两件事。
	AvatarURL *string `json:"avatarUrl"`
}

// avatarURLPresent 判原始 JSON 里有没有 avatarUrl 这个键。
//
// 用 map 再解一次而不是给 AvatarURL 换一个包装类型（如 **string）：
// 双重指针在 JSON 上确实能区分三种情况，但它在 Go 侧读起来是 `**string`，
// 而本文件已经因为"指针的 nil 判据"被坑过一次 —— 让它显式一点更安全。
// 代价是多一次小对象解析（请求体只有几十字节）。
func avatarURLPresent(raw map[string]json.RawMessage) bool {
	_, ok := raw["avatarUrl"]
	return ok
}

// patchMe 是 `PATCH /api/me`（改自己的昵称，必须登录）。
//
// 四步：
//
//  1. **认证**：RequireAuth 已经挡掉匿名（401）。这里的 CurrentUser 判空是
//     "中间件被绕过"的兜底 —— fail closed（401）而不是当匿名处理。
//  2. **入参**：至少要传一个字段，否则 400（"什么都没改"不该是一次成功的写请求，
//     那会让审计与前端提示都失去意义）。
//  3. **清洗与落库**：交给 service（StripControl → TrimSpace → 限长 → 回落用户名）。
//  4. **回读**：返回改动后的完整档案（`Profile`，与 `/api/auth/me` 同形状）。
//
// 为什么返回完整档案而不是 204：前端改完昵称要立刻更新顶栏与档案页，
// 而清洗可能改过值（去零宽、回落成用户名）。返回权威行让它不必猜，也不必再打一发
// `/me` —— 这与 #27/#31 的"写操作同步返回权威行"是同一条取舍。
func (h *userProfileHandler) patchMe(c *gin.Context) {
	actor, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}

	// 先拿原始字节再解两次：一次解成 map 只为判"有没有 avatarUrl 这个键"
	//（三态需要，见 patchMeRequest 的说明），一次解成结构体。
	//
	// 顺序不能反：ShouldBindJSON 会把 body 读走，之后 GetRawData 拿不到东西。
	raw, err := c.GetRawData()
	if err != nil {
		adminBadRequest(c, "请求体不是合法 JSON")
		return
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		adminBadRequest(c, "请求体不是合法 JSON")
		return
	}
	var req patchMeRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		adminBadRequest(c, "请求体不是合法 JSON")
		return
	}

	wantsName := req.DisplayName != nil
	wantsAvatar := avatarURLPresent(keys)
	if !wantsName && !wantsAvatar {
		adminBadRequest(c, "至少要指定 displayName 或 avatarUrl")
		return
	}

	// 两个字段可以同时改（一次请求改两样），但**各自独立校验、独立落库**：
	// 头像校验失败时昵称也不该生效 —— 半成功比全失败更难解释。
	//
	// 所以昵称先只做**预检**（长度），真正的写入放到两个字段都校验完之后。
	if wantsName {
		// 长度预检（清洗**之前**，见 maxDisplayNamePatchRunes 的说明）。
		// 超长直接 400：清洗会先把零宽/Bidi 去掉，所以这里给的是"清洗后仍可能合法"
		// 的宽松上限，只挡住明显失控的 body。
		if len([]rune(*req.DisplayName)) > maxDisplayNamePatchRunes {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error": "昵称太长",
				"code":  CodeDisplayNameInvalid,
			})
			return
		}
	}

	// 头像先落（它有两个额外的领域闸门：本桶校验与"存储没配"）。
	// 先跑它是有意的：它是**更可能失败**的那一半，先失败就不会留下
	// "昵称改了、头像没改"的半成功。
	var p service.Profile
	if wantsAvatar {
		p, err = h.svc.UpdateMyAvatarURL(c.Request.Context(), actor.ID, req.AvatarURL)
		if err != nil {
			h.writeProfileError(c, err)
			return
		}
	}
	if wantsName {
		p, err = h.svc.UpdateMyDisplayName(c.Request.Context(), actor.ID, *req.DisplayName)
		if err != nil {
			h.writeProfileError(c, err)
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"user": p})
}

// writeProfileError 把本域的领域错误翻成 HTTP 状态。
//
// 六条映射各自的理由：
//
//   - ErrUserNotFound → 404 + NOT_FOUND：**不存在与被封禁共用它**（见
//     service.ErrUserNotFound 的说明）。文案只说"用户不存在"，不暗示"也可能被封了"。
//   - ErrUserForbidden → 403 + FORBIDDEN：本人对自己做不了这件事（当前只有"被封禁"）。
//     与上面分成两个码是必要的：前者是"你看的人不存在"，后者是"你自己不能改"，
//     前端要给的下一步完全不同（一个是"回到列表"，一个是"去问管理员"）。
//   - ErrBadDisplayName → 400 + DISPLAY_NAME_INVALID：昵称清洗后为空或超长。
//     文案用 service 的那一句（它带着"不超过 N 个字符"的具体数字）。
//   - ErrAvatarURLInvalid → 400 + AVATAR_URL_INVALID（票 #74）。
//   - ErrAvatarUnavailable → 503 + AUTH_UNAVAILABLE（票 #74，部署侧缺能力）。
//   - ErrAvatarQuotaExceeded → 429 + RATE_LIMITED（票 #75，每日五次用满）。
//
// 其余错误落到 writeAccountError：它已经处理了 ErrSessionInvalid / ErrUserBanned /
// 口令策略 / 默认 500 这一整套，重复一遍只会让两边漂移。
func (h *userProfileHandler) writeProfileError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrUserNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在", "code": codeNotFound})
	case errors.Is(err, service.ErrUserForbidden):
		c.JSON(http.StatusForbidden, gin.H{"error": "当前账号不允许执行该操作", "code": CodeForbidden})
	case errors.Is(err, service.ErrBadDisplayName):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "code": CodeDisplayNameInvalid})
	case errors.Is(err, service.ErrAvatarURLInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "code": CodeAvatarURLInvalid})
	case errors.Is(err, service.ErrAvatarUnavailable):
		// 503 而不是 400：请求本身没错，是这个部署没开对象存储
		//（PR_STORAGE_ENDPOINT 为空 → /api/uploads 也不注册）。前端据此显示
		// CapabilityNotice，而不是让用户反复重试一个永远失败的动作。
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error(), "code": CodeAuthUnavailable})
	case errors.Is(err, service.ErrAvatarQuotaExceeded):
		// 429 而不是 403（票 #75）：它不是"你没权限"，而是"今天用完了"。
		// 状态码的区别对前端是有意义的 —— 403 不该重试，429 明天可以再来，
		// 而且 429 是唯一一个能自然承载"稍后重试"语义的码。
		//
		// 用 model.CodeRateLimited（而不是新造一个 AVATAR_QUOTA_EXCEEDED）：
		// 前端已经有一处 429 的分诊（发码冷却），让它多认一个码没有收益，
		// 而文案里带了具体次数与"明天"，用户看到的信息是完整的。
		c.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error(), "code": model.CodeRateLimited})
	default:
		writeAccountError(c, err)
	}
}

// 断言：本文件依赖的 service 方法在编译期真的存在（防止将来重命名后静默失配）。
//
// 与 messages.go 末尾那一段同一个用意：这些方法是**跨包契约**，而 Go 只在
// 接口赋值处检查它。写成一个包级变量让"签名漂移"变成编译错误，而不是运行期 500。
var _ = func() any {
	var s *service.AccountService
	var _ UserProfileService = s
	return nil
}

// 保住 model 的引用：本文件目前的错误映射没有直接用 model.Code*（它走
// writeAccountError），但保留这条 import 会让"本域的 400 用哪个码"这件事
// 留在显眼处 —— 如果将来有人把 adminBadRequest 换成裸 c.JSON，这里会提醒他
// 还有一套既有错误码。
var _ = model.CodeBadRequest
