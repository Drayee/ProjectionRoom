package handler

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/model"
	"ProjectionRoom/internal/store"
)

// 收藏域端点（票 #64 / 决议 #48 §6）。
//
// 形状与既有写端点刻意不同：**PUT/DELETE 而不是 POST toggle**。
// 点赞是 toggle（再点一次反着来），收藏是幂等 set（再发一次结果不变）——
// 因为收藏按钮的乐观更新与网络重试都需要"重发没有副作用"。
// 若写成 toggle，一次超时重试会把刚收藏的对象取消掉。
//
// 三条路径各自对应一种对象，但**共用一个处理器族**：差别只在 target_type
// 与"id 从哪里取"（帖子/番剧是数字路径参数，放映室是房间码）。
// 路由分三组注册而不是写成 `/api/favorites/:type/:id`，是因为后者会把
// "类型"变成一个用户可传的自由字符串 —— 那是把白名单搬到 URL 里，
// 每个 handler 都要重新校验一遍。
func registerFavoriteRoutes(api *gin.RouterGroup, st *store.Store, cfg *config.Config, auth AccountService) {
	if api == nil {
		return
	}
	if cfg == nil || strings.TrimSpace(cfg.Auth.DBDSN) == "" {
		return
	}
	if st == nil {
		unavailable := func(c *gin.Context) {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "收藏能力暂不可用",
				"code":  CodeAuthUnavailable,
			})
		}
		for _, p := range []string{
			"/posts/:postId/favorite", "/rooms/:roomId/favorite",
			"/animes/:animeId/favorite", "/favorites",
		} {
			api.PUT(p, unavailable)
			api.DELETE(p, unavailable)
			api.GET(p, unavailable)
		}
		return
	}

	h := &favoriteHandler{store: st, auth: auth}

	// 读列表挂只读桶（60/min）：翻收藏夹是浏览行为。
	// 写挂互动桶（10/min）：轻量、会写库，与点赞评论同一档 —— 复用既有几桶，
	// 不为新域发明配置（#37 的先例）。
	readLimiter := newKeyedLimiter(cfg.IPC.RoomMetaPerMinute, cfg.IPC.RoomMetaBurst, "收藏列表")
	writeLimiter := newKeyedLimiter(cfg.IPC.LoginPerMinute, cfg.IPC.LoginBurst, "收藏变更")

	authed := api.Group("", RequireAuth(auth))
	authed.GET("/favorites", readLimiter, h.list)

	authed.PUT("/posts/:postId/favorite", writeLimiter, h.favoritePost)
	authed.DELETE("/posts/:postId/favorite", writeLimiter, h.unfavoritePost)
	authed.PUT("/animes/:animeId/favorite", writeLimiter, h.favoriteAnime)
	authed.DELETE("/animes/:animeId/favorite", writeLimiter, h.unfavoriteAnime)
	authed.PUT("/rooms/:roomId/favorite", writeLimiter, h.favoriteRoom)
	authed.DELETE("/rooms/:roomId/favorite", writeLimiter, h.unfavoriteRoom)
}

type favoriteHandler struct {
	store *store.Store
	auth  AccountService
}

// favoriteEntryView 是收藏列表的一行。
//
// `valid=false` 表示对象已失效（帖子被删 / 番剧下架 / 放映室关闭或未公开）。
// **失效条目照样返回**（决议 #48 §5）：前端灰显 + 「已失效」+ 不可点进，
// 由用户自己移除。静默删掉会让"收藏莫名变少"变成一个没人能解释的现象。
type favoriteEntryView struct {
	TargetID   string `json:"targetId"`
	TargetType string `json:"targetType"`
	Title      string `json:"title"`
	Valid      bool   `json:"valid"`
	CreatedAt  string `json:"createdAt"`
	Cursor     string `json:"cursor"`
}

// favoriteStateView 是 PUT/DELETE 的响应：**权威状态**而不是"成功/失败"。
//
// 回带 favorited 而不是 204：前端乐观更新后要用服务端的值收敛
// （与 TogglePostLike 回带 liked 同一条理由）。created/removed 只是诊断信息，
// 客户端不该依赖它们做界面判断 —— 幂等重试时它们是 false。
type favoriteStateView struct {
	TargetID   string `json:"targetId"`
	TargetType string `json:"targetType"`
	Favorited  bool   `json:"favorited"`
	Created    bool   `json:"created,omitempty"`
	Removed    bool   `json:"removed,omitempty"`
}

// list 是 `GET /api/favorites?type=post|room|anime&before=<cursor>&limit=`。
//
// type **必填**：缺省时返回什么都有歧义（"全部类型混排"没有定义好的排序），
// 而猜一个默认值会让客户端传错时安静地拿到别的东西。
func (h *favoriteHandler) list(c *gin.Context) {
	actor, ok := CurrentUser(c)
	if !ok {
		// RequireAuth 已经挡过，这里只是防御（测试里直接调 handler 时会走到）。
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}
	typ := store.FavoriteType(strings.TrimSpace(c.Query("type")))
	if typ == "" {
		adminBadRequest(c, "type 必填（post / room / anime）")
		return
	}
	if !typ.Valid() {
		adminBadRequest(c, "type 只支持 post / room / anime")
		return
	}
	limit, err := boundedLimit(c.Query("limit"), defaultFavoriteHTTPLimit, maxFavoriteListHTTPLimit)
	if err != nil {
		adminBadRequest(c, err.Error())
		return
	}

	rows, hasMore, err := h.store.ListFavorites(c.Request.Context(), actor.ID, typ, strings.TrimSpace(c.Query("before")), limit)
	if err != nil {
		if errors.Is(err, store.ErrFavoriteBadCursor) {
			adminBadRequest(c, "before 游标不合法")
			return
		}
		favoriteInternal(c, "收藏列表查询失败", err)
		return
	}
	items := make([]favoriteEntryView, 0, len(rows))
	for i := range rows {
		items = append(items, favoriteEntryView{
			TargetID:   rows[i].TargetID,
			TargetType: string(typ),
			Title:      rows[i].Title,
			Valid:      rows[i].Valid,
			CreatedAt:  rows[i].CreatedAt.Format(timeLayout),
			Cursor:     rows[i].Cursor,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "hasMore": hasMore})
}

// —— 三个对象类型的写端点 ——
//
// 每个都是"取 id → 调 store → 映射错误"，差异只在 id 的来源与错误文案。
// 不抽成一个高阶函数：那样会让三条路径的参数校验藏进闭包，
// 而"房间码不是数字"这类差异恰恰是读代码时最该一眼看见的东西。

func (h *favoriteHandler) favoritePost(c *gin.Context) {
	h.write(c, store.FavoritePost, numericIDParam(postIDParam))
}

func (h *favoriteHandler) unfavoritePost(c *gin.Context) {
	h.write(c, store.FavoritePost, numericIDParam(postIDParam))
}

func (h *favoriteHandler) favoriteAnime(c *gin.Context) {
	h.write(c, store.FavoriteAnime, numericIDParam(animeIDParam))
}

func (h *favoriteHandler) unfavoriteAnime(c *gin.Context) {
	h.write(c, store.FavoriteAnime, numericIDParam(animeIDParam))
}

func (h *favoriteHandler) favoriteRoom(c *gin.Context) {
	h.write(c, store.FavoriteRoom, roomIDParam)
}

func (h *favoriteHandler) unfavoriteRoom(c *gin.Context) {
	h.write(c, store.FavoriteRoom, roomIDParam)
}

// idParamFunc 是三种 id 取法的统一签名：返回 (id 文本, 是否通过校验)。
//
// 用文本而不是 int64：放映室的主键是房间码（字符串），而收藏表的 target_id
// 是 text —— 在 handler 里就把三种 id 归一成同一种形态，store 只认文本。
type idParamFunc func(c *gin.Context) (string, bool)

// numericIDParam 把既有的 (int64, bool) 型取参函数适配成文本形态。
//
// 为什么不改 postIDParam / animeIDParam 的签名：它们各自还有别的调用点，
// 为一个新域把公共函数的返回类型改掉，是在让"收藏"影响"发帖/删帖"。
// 适配器把这次影响收在一处。
func numericIDParam(fn func(*gin.Context) (int64, bool)) idParamFunc {
	return func(c *gin.Context) (string, bool) {
		id, ok := fn(c)
		if !ok {
			return "", false
		}
		return strconv.FormatInt(id, 10), true
	}
}

// write 是 PUT/DELETE 的共用实现。
//
// 用 HTTP 方法而不是两个函数名区分方向：端点的语义是「把状态设为收藏 / 未收藏」，
// 方法就是那个方向。这与 toggle 的取舍不同（那里 POST 一个路径就够了，
// 因为方向由"当前状态"决定）。
func (h *favoriteHandler) write(c *gin.Context, typ store.FavoriteType, param idParamFunc) {
	actor, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}
	targetID, ok := param(c)
	if !ok {
		return
	}

	var (
		changed bool
		err     error
	)
	if c.Request.Method == http.MethodDelete {
		changed, err = h.store.RemoveFavorite(c.Request.Context(), actor.ID, typ, targetID)
	} else {
		changed, err = h.store.AddFavorite(c.Request.Context(), actor.ID, typ, targetID)
	}
	if err != nil {
		switch {
		case errors.Is(err, store.ErrFavoriteTargetNotFound):
			// 404 而不是 400：与"帖子详情不存在"同一口径 —— 调用方不该从
			// 状态码差异里推断出"这个 id 曾经存在过"。
			c.JSON(http.StatusNotFound, gin.H{"error": "对象不存在", "code": codeNotFound})
		case errors.Is(err, store.ErrFavoriteBadType):
			adminBadRequest(c, "type 只支持 post / room / anime")
		default:
			favoriteInternal(c, "收藏操作失败", err)
		}
		return
	}

	view := favoriteStateView{TargetID: targetID, TargetType: string(typ)}
	if c.Request.Method == http.MethodDelete {
		view.Favorited = false
		view.Removed = changed
		c.JSON(http.StatusOK, view)
		return
	}
	view.Favorited = true
	view.Created = changed
	c.JSON(http.StatusOK, view)
}

// roomIDParam 校验并返回房间码。
//
// 与 postIDParam 的差别：房间码是**用户可见的业务主键**，格式判据在
// 创建房间那一侧（`^[A-Z0-9]{4,12}$`）。这里只做最小归一（去空白 + 转大写），
// 存在性交给 store 的 favoriteTargetExists —— 在 handler 里再写一遍正则
// 就是第三个真源（前两个是建房校验与 rooms_meta 主键）。
func roomIDParam(c *gin.Context) (string, bool) {
	raw := strings.ToUpper(strings.TrimSpace(c.Param("roomId")))
	if raw == "" || len(raw) > 12 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "房间码不合法", "code": model.CodeBadRequest})
		return "", false
	}
	return raw, true
}

func favoriteInternal(c *gin.Context, what string, err error) {
	log.Printf("[ERROR] %s: %v", what, err)
	c.JSON(http.StatusInternalServerError, gin.H{"error": "服务端内部错误", "code": model.CodeInternalError})
}

const (
	defaultFavoriteHTTPLimit = 20
	maxFavoriteListHTTPLimit = 100
)
