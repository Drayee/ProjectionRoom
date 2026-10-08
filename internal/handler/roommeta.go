// 房主的标题 / 公开性端点（ACCOUNTS §6 / §10 的 PATCH /api/rooms/:id/meta，二期 T3）。
//
// 三条判据，逐条说明为什么这样选：
//
//  1. **授权是"房主"，不是"主播"**：房间标题与公开性是房主的资产。
//     主播位由 hostToken 决定（S-7），两者可以是不同人（§6）；
//     拿"是不是主播"当判据会让"临时替朋友推流"的人顺手把房间改成公开。
//  2. **标题在服务端清洗**：去零宽/Bidi/控制符（复用一期的 usecase.StripControl）、
//     去首尾空白、长度 ≤60（与 rooms_meta.title 的列上限一致）。
//     前端一律文本插值，服务端的责任是保证**存进去的东西本身**没有欺骗字符 ——
//     一个带 Bidi 覆盖的标题能让列表页显示成完全不同的文字。
//  3. **改完立刻在内存侧可见、异步落库**：内存房间是实时事实的 owner（I2），
//     rooms_meta 是它的持久化副本（§9 的事件类，投递即返回）。
//
// 为什么**不给**房主操作写 admin_audit：
//
//	admin_audit 的语义是"管理员以管理身份对系统做了什么"（§8 的 must 落点）。
//	把房主改自己房间的标题混进去，那张表就再也回答不了"最近有没有管理员动过手脚"——
//	提问者会被一堆用户自助操作淹没。房主操作的痕迹另有去处（服务端日志 +
//	rooms_meta 本身的状态变化），而"审计表只记管理动作"这条语义比"什么都记一笔"值钱得多。
package handler

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/model"
	"ProjectionRoom/internal/store"
	"ProjectionRoom/internal/usecase"
)

// maxRoomTitleLen 是房间标题的字符上限，与 rooms_meta.title 的列上限保持一致
// （store.maxTitleLen = 60）。两处必须同值：不一致时 store 会拒绝写入，
// 而症状是"保存按钮点了报 500"，排查要从 HTTP 层一路追到 SQL 层。
const maxRoomTitleLen = 60

// RoomMetaRoomSource 是房主改元数据需要的**内存**能力（*usecase.Manager 满足它）。
//
// Get 只是为了"先判存在、再判房主"这两步能给出精确的 404/403；
// SetRoomMeta 负责在同一次调用里复核"房间还在、而且调用者是房主"。
// 两步都保留不是重复：Get 回答"该返回 404 还是 403"，
// SetRoomMeta 回答"这次修改真的发生了吗"（两者之间的时间窗里房间可能已被关掉）。
type RoomMetaRoomSource interface {
	Get(roomID string) (*usecase.Room, bool)
	SetRoomMeta(roomID string, actorUserID int64, title *string, isPublic *bool) error
}

// RoomMetaReader 是房主改元数据时**读当前行**的能力（*store.Store 满足它）。
//
// 为什么只有读、没有写：写走一期的 RoomMetaSink 异步投递（§9 的事件类），
// 而投递的作业体自己会调 UpsertRoomMeta（见 router.go 的 roomMetaSink）。
// 在本层再持有一个写接口，会让"这次修改到底写了几次库"变成要看两处才知道的事。
//
// 读是必需的：UpsertRoomMeta 是**整体覆盖**语义，只有拿到当前行才能只改该改的字段
// （否则一次改标题会把 has_password / owner_user_id 覆盖成零值）。
type RoomMetaReader interface {
	RoomMetaByID(ctx context.Context, roomID string) (*store.RoomMeta, error)
}

// RoomMetaDeps 是房主元数据端点的全部依赖。
//
// 三个依赖缺一不可（Rooms / Meta / Sink）：缺 Sink 时"改完的东西落不了库"，
// 那比"端点不存在"更容易让人困惑（界面显示成功、刷新就变回去）。
// 因此 registerRoomMetaRoutes 在装配残缺时注册一组 503，而不是让它半可用。
type RoomMetaDeps struct {
	Rooms RoomMetaRoomSource
	Meta  RoomMetaReader
	Sink  RoomMetaSink
}

// registerRoomMetaRoutes 注册 PATCH /api/rooms/:id/meta（RequireAuth）。
//
// 与认证/管理端同一条开关约定：
//   - cfg.Auth.DBDSN 为空 = 账号能力关闭 → 本路由**不注册**（房间标题属于账号层的房间元数据）；
//   - DSN 配了但依赖没装配出来 → 一组一律 503（比"路由凭空消失"好定位）。
//
// 它必须挂在 RequireAuth 之下：房主身份只能来自校验过的 access token，
// 绝不能从请求体或查询串取（那等于让任何人改任何房间的标题）。
func registerRoomMetaRoutes(api *gin.RouterGroup, deps RoomMetaDeps, cfg *config.Config, auth AccountService) {
	if api == nil {
		return
	}
	if cfg == nil || strings.TrimSpace(cfg.Auth.DBDSN) == "" {
		return
	}

	if auth == nil || deps.Rooms == nil || deps.Meta == nil || deps.Sink == nil {
		log.Printf("[WARN] 已配置 PR_DB_DSN 但房主元数据依赖未装配：PATCH /api/rooms/:id/meta 一律返回 503")
		unavailable := func(c *gin.Context) {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"error": "房间元数据暂时不可用（服务端未完成装配）",
				"code":  CodeAuthUnavailable,
			})
		}
		api.PATCH("/rooms/:id/meta", unavailable)
		return
	}

	h := &roomMetaHandler{deps: deps}
	api.PATCH("/rooms/:id/meta", RequireAuth(auth), h.patch)
}

// roomMetaHandler 持有依赖，把端点做成方法。
type roomMetaHandler struct {
	deps RoomMetaDeps
}

// roomMetaPatchRequest 是 PATCH /api/rooms/:id/meta 的请求体。
//
// 用**指针**字段区分"没传"与"传了零值"：
//
//	{"isPublic": false}  是"把房间改成不公开"（有意义）；
//	{}                   是长度为零的 patch（400）；
//	{"title": null}      等同于没传（不覆盖已有标题）。
//
// 没有这个区分就只能靠"零值即忽略"，那会让"想把标题清空"永远做不到。
type roomMetaPatchRequest struct {
	Title    *string `json:"title"`
	IsPublic *bool   `json:"isPublic"`
}

// patch 改房间的标题 / 公开性（成功 200，返回改动后的**期望状态**）。
//
// 状态码（与 §10 的口径一致）：
//
//	401  没有有效 access token（由 RequireAuth 给出）
//	400  两项都没传 / 不是合法 JSON / 标题超长
//	403  房间存在但不是你的（ErrNotRoomOwner）
//	404  房间不存在或已被销毁（含"关房后又被并发改元数据"）
func (h *roomMetaHandler) patch(c *gin.Context) {
	actor, ok := CurrentUser(c)
	if !ok {
		// 中间件被绕过时的兜底：按未认证处理（fail closed），绝不按匿名放行。
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}

	roomID := strings.ToUpper(strings.TrimSpace(c.Param("id")))
	if roomID == "" {
		adminBadRequest(c, "路径参数 id 不能为空")
		return
	}

	var req roomMetaPatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		adminBadRequest(c, "请求体不是合法 JSON")
		return
	}
	if req.Title == nil && req.IsPublic == nil {
		adminBadRequest(c, "至少要指定 title 或 isPublic 之一")
		return
	}

	// 标题清洗：与 store 侧的上限一致（见 maxRoomTitleLen 的说明）。
	// 长度判定在**清洗之后**：先判长度会让"60 个可见字符 + 一堆零宽字符"
	// 因为超长被拒，而用户看到的明明只有 60 个字符。
	title := (*string)(nil)
	if req.Title != nil {
		cleaned := strings.TrimSpace(usecase.StripControl(*req.Title))
		if len([]rune(cleaned)) > maxRoomTitleLen {
			adminBadRequest(c, "标题不能超过 60 个字符")
			return
		}
		title = &cleaned
	}

	// 存在性 → 404；房主校验 → 403。两步都在内存里（不查库），
	// 因此"非房主探测某个房间码是否存在"不会额外制造数据库往返。
	r, exists := h.deps.Rooms.Get(roomID)
	if !exists {
		h.writeMetaRoomError(c, usecase.ErrNotFound, "房间不存在")
		return
	}
	if r.OwnerUserID() != actor.ID {
		h.writeMetaRoomError(c, usecase.ErrNotRoomOwner, "只有房主可以修改房间信息")
		return
	}

	// 内存侧：复核"房间还在、调用者仍是房主"，房间在两步之间被关掉时返回 404。
	if err := h.deps.Rooms.SetRoomMeta(roomID, actor.ID, title, req.IsPublic); err != nil {
		h.writeMetaRoomError(c, err, "修改房间信息失败")
		return
	}

	// 期望状态：以**当前库里的那一行为基底**覆盖本次要改的字段。
	//
	// 为什么先读再写，而不是直接写一个只有本次改动的行：
	// UpsertRoomMeta 是**整体覆盖**语义（owner/title/isPublic/hasPassword 一起写，
	// created_at 除外）。只带本次字段会把它余下的字段覆盖成零值 ——
	// 只改标题就会把 has_password 写成 false、把 owner_user_id 写成 0。
	// 读一次换来"只改该改的"，而这一读发生在异步投递**之前**，不进入 WS/REST 的
	// 等待路径（投递本身仍然是"投递即返回"，见 §9 / I4）。
	expected := h.expectedMeta(c.Request.Context(), roomID, actor.ID, r)
	if title != nil {
		expected.Title = *title
	}
	if req.IsPublic != nil {
		expected.IsPublic = *req.IsPublic
	}
	// 异步落库。失败不回滚内存侧的判定、也不报给用户：内存是实时事实的 owner（I2），
	// 而投递失败（队列满/写入器已关闭）只影响"重启后这份状态还在不在"，
	// 回滚反而会让"保存成功但刷新就变回去"这种最让人困惑的形态真的出现。
	if !h.deps.Sink.SubmitRoomMeta(expected) {
		log.Printf("[WARN] 房间 %s 的元数据投递失败（队列满或写入器已关闭）：内存已更新，重启后可能丢失", roomID)
	}

	c.JSON(http.StatusOK, gin.H{
		"roomId":   expected.RoomID,
		"title":    expected.Title,
		"isPublic": expected.IsPublic,
	})
}

// expectedMeta 组装"这次改完之后 rooms_meta 应该是什么样"。
//
// 库里已有行时以它为准（保留 created_at / has_password / closed_at 等本次不动的字段）；
// 缺行时（房主改一个元数据从未落库的房间）用内存房间的事实兜底，
// 这样这次修改同时也把这个缺行补上了 —— 与公开列表的"自愈投递"是同一条修复。
//
// 读失败（非"不存在"）时不报错而是用内存事实重建：一次查询抖动不该让房主改不了标题。
// 代价（created_at 会被写成"现在"）不会真的落库 —— upsert 的冲突分支不覆盖 created_at。
func (h *roomMetaHandler) expectedMeta(ctx context.Context, roomID string, ownerUserID int64, r *usecase.Room) *store.RoomMeta {
	if current, err := h.deps.Meta.RoomMetaByID(ctx, roomID); err == nil && current != nil {
		out := *current
		return &out
	} else if err != nil && !errors.Is(err, usecase.StoreNotFound) {
		log.Printf("[WARN] 房间 %s 改元数据前读库失败，按内存事实重建期望状态：%v", roomID, err)
	}
	hasPassword := r != nil && r.HasPassword()
	return &store.RoomMeta{
		RoomID:      roomID,
		OwnerUserID: ownerUserID,
		// 缺行时按"未公开"起步：与建房默认值一致（见 submitRoomMeta）。
		IsPublic:    false,
		HasPassword: hasPassword,
	}
}

// writeMetaRoomError 把房主元数据操作的领域错误翻译成响应。
//
// 与 roomErrorResponse 分开的理由：房主改元数据的两条新判据是
// ErrNotRoomOwner（403）与 ErrRoomChanged（403），它们只出现在这条路经上，
// 而 roomErrorResponse 是 REST 建房与 WS 两条入口共用的映射表 ——
// 往那张表里塞只属于本端点的条目，会让"WS 上也可能返回这个码"变成一种误读。
//
// 顺序是刻意的：ErrNotRoomOwner / ErrRoomChanged 都**不是** ErrNotFound 的包装，
// 但这里的判断顺序仍然是"具体在前、ErrNotFound 在后"，避免将来有人给它们加上包装时
// 静默变成 404（403 与 404 的语义差别是"你的房间"与"没有这个房间"）。
func (h *roomMetaHandler) writeMetaRoomError(c *gin.Context, err error, fallback string) {
	switch {
	case errors.Is(err, usecase.ErrNotRoomOwner):
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error": usecase.ErrNotRoomOwner.Error(),
			"code":  CodeForbidden,
		})
	case errors.Is(err, usecase.ErrRoomChanged):
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error": usecase.ErrRoomChanged.Error(),
			"code":  CodeForbidden,
		})
	case errors.Is(err, usecase.ErrBadRoomMeta):
		adminBadRequest(c, usecase.ErrBadRoomMeta.Error())
	case errors.Is(err, usecase.ErrNotFound):
		// 借用既有房间错误码：前端对"房间不存在"只有一套处理（退回列表页），
		// 多造一个码会让它多一条分支而没有任何行为差异。
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{
			"error": "房间不存在",
			"code":  model.CodeRoomNotFound,
		})
	default:
		log.Printf("[ERROR] 房主改房间元数据失败：%v", err)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
			"error": fallback,
			"code":  model.CodeInternalError,
		})
	}
}
