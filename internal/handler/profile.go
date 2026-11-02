// 他人公开档案的内容块（票 #68）：某人的帖子、某人的公开放映室。
//
// 两个端点，都**免登录** —— 与 `GET /api/users/:id` 同一条理由：它们是公开档案
// 的一部分，而档案的入口（帖子列表的作者名、搜索结果、消息会话）大多本身免登录。
// 要求登录会让"点开作者名"变成一次登录跳转，而用户要看的只是"这个人发过什么"。
//
//	GET /api/users/:id/posts?limit&offset   某人的公开帖，时间倒序
//	GET /api/users/:id/rooms?limit&offset   某人的公开放映室，最近活跃优先
//
// # 为什么是子资源而不是 `GET /api/posts?authorId=`
//
// 决议 #47 §5 要求"二选一，不可两个都做"。选子资源的三条理由：
//
//  1. **准入不同**。`/api/posts` 是一个带 sort 枚举的流（latest/hot/following），
//     其中 following 需要登录；而档案页的列表永远按时间倒序、永远免登录。
//     把它们塞进同一个端点会让"为什么 sort 在这里被忽略"变成一个要读注释才知道的问题。
//  2. **前缀已存在**。`/api/users/:id` 已经在那里，子资源是它的自然延伸；
//     gin 的静态段优先保证 `/api/users/search` 不受影响。
//  3. **响应里带的是"这个人的东西"**，与"全站的东西按作者筛"是两种查询意图。
//     前者的可见性判据是"这个人可不可见"，后者是"这一批帖子可不可见"。
//
// # 三处一致的可见性口径
//
// 三个端点的 404 必须同形：档案 404 而子资源 200 是一条存在性旁路
// （"档案说没这个人，但 TA 的帖还能读"）。所以本文件在进入列表查询**之前**
// 先判一次账号可见性，用的是与 `service.PublicProfile` 相同的判据
// （不存在 / 非 active 都当 404）。
package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/store"
)

// profilePageLimit 是内容块每页条数的默认值与上限。
//
// 与帖子列表同一档位（20/100）：档案页的无限滚动与帖子列表是同一个交互，
// 两处用不同的页大小会让"翻到第几页"这件事在用户心里有两个答案。
const (
	profilePageDefaultLimit = 20
	profilePageMaxLimit     = 100
)

// registerAuthorContentRoutes 注册他人档案的两个只读内容块。
//
// 与其它二期路由同一套约定：账号能力关闭（`PR_DB_DSN` 为空）时整个域**不注册**；
// 装配残缺时注册一组 503。但这里多一条：`st == nil` 时**不注册**而不是 503 ——
// 这两个端点没有"降级成空列表"的语义可用，因为空列表的意思是"这个人没发过帖"，
// 而"装配缺失"是另一件事。返回 503 至少不会说谎。
//
// 限速走只读桶（与公开档案同一个档位）：它们都是按 id 递增的可枚举面。
func registerAuthorContentRoutes(api *gin.RouterGroup, st *store.Store, cfg *config.Config, auth AccountService) {
	if api == nil {
		return
	}
	if cfg == nil || strings.TrimSpace(cfg.Auth.DBDSN) == "" {
		return
	}
	if st == nil {
		unavailable := func(c *gin.Context) {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "用户资料暂不可用",
				"code":  CodeAuthUnavailable,
			})
		}
		api.GET("/users/:id/posts", unavailable)
		api.GET("/users/:id/rooms", unavailable)
		return
	}

	h := &authorContentHandler{store: st, auth: auth}
	readLimiter := newKeyedLimiter(cfg.IPC.RoomMetaPerMinute, cfg.IPC.RoomMetaBurst, "用户公开档案")
	api.GET("/users/:id/posts", readLimiter, h.posts)
	api.GET("/users/:id/rooms", readLimiter, h.rooms)
}

type authorContentHandler struct {
	store *store.Store
	// auth 用来识别**可选的**登录者（回填 liked / favorited），不用于准入。
	// 与 postHandler 的 auth 是同一个接口、同一条用法（见 optionalViewer）。
	auth AccountService
}

// authorPostItem 是作者帖列表里的一项。
//
// 刻意**复用** postView 的字段形状而不是另造一个：前端拿到的 Post 对象要能
// 直接喂给同一个 PostCard / ActionBar 组件，多一套形状就等于多一套适配层。
type authorPostList struct {
	Items   []postView `json:"items"`
	HasMore bool       `json:"hasMore"`
}

// authorRoomItem 是作者房列表里的一项。
//
// 字段白名单与 `publicRoomItem` 不同：这里**没有** memberCount / hostOnline /
// hostOffline —— 那些是内存实况（"现在房里几个人、主播在不在"），而本端点
// 只读元数据（"这个人开过哪些房"）。把实况混进来的代价是这个端点要依赖
// Manager，而它对"房间当前开没开"并不关心。
//
// 也没有 ownerName：调用方已经知道这是谁的档案（URL 里就是那个 id），
// 每行重复一遍昵称只是让响应变大。
type authorRoomItem struct {
	RoomID string `json:"roomId"`
	Title  string `json:"title"`
	// CreatedAt / LastSeenAt 是展示用的时间（卡片上"最近活跃"）。
	CreatedAt  string `json:"createdAt"`
	LastSeenAt string `json:"lastSeenAt"`
}

type authorRoomList struct {
	Items   []authorRoomItem `json:"items"`
	HasMore bool             `json:"hasMore"`
}

// visibleAuthor 判"这个 id 的账号对公众可见吗"。
//
// 判据与 `service.PublicProfile` 逐字一致：**不存在**与**非 active**都返回 false，
// 调用方一律翻成 404（区分它们等于开一条存在性探测通道）。
//
// 为什么不直接调 `UserProfileService.PublicProfile`：那个方法返回的是档案
// 结构（还带着字段白名单的语义），而这里只要一个布尔。更实际的理由是
// 依赖更窄 —— 本文件不需要账号服务，只需要一次 users 表的读。
func (h *authorContentHandler) visibleAuthor(c *gin.Context, id int64) bool {
	u, err := h.store.UserByID(c.Request.Context(), id)
	if err != nil {
		// 查不到（ErrNotFound）与查库失败都当"不可见"：
		// 前者是 404 的正确答案，后者至少不会把内部错误暴露成一个"有这个人"的信号。
		return false
	}
	return u != nil && u.Status == store.StatusActive
}

// posts 是 `GET /api/users/:id/posts`。
func (h *authorContentHandler) posts(c *gin.Context) {
	id, ok := adminPathID(c, "id")
	if !ok {
		return
	}
	limit, offset, ok := profilePage(c)
	if !ok {
		return
	}
	if !h.visibleAuthor(c, id) {
		notFound(c, "用户不存在")
		return
	}

	// viewer 是**可选**的：登录者的 liked / favorited 要回填，匿名一律 false。
	// 判据与 postHandler.optionalViewer 逐字相同（坏 token 当匿名，不 401）——
	// 读公开内容不需要凭据，把"帖子打不开"归因到登录状态上是错的。
	viewer := h.optionalViewer(c)
	rows, hasMore, err := h.store.ListPostsByAuthor(c.Request.Context(), id, limit, offset, viewer)
	if err != nil {
		profileInternal(c, "查询作者帖失败", err)
		return
	}

	items := make([]postView, 0, len(rows))
	for i := range rows {
		items = append(items, toPostView(&rows[i]))
	}
	c.JSON(http.StatusOK, authorPostList{Items: items, HasMore: hasMore})
}

// rooms 是 `GET /api/users/:id/rooms`。
func (h *authorContentHandler) rooms(c *gin.Context) {
	id, ok := adminPathID(c, "id")
	if !ok {
		return
	}
	limit, offset, ok := profilePage(c)
	if !ok {
		return
	}
	if !h.visibleAuthor(c, id) {
		notFound(c, "用户不存在")
		return
	}

	rows, hasMore, err := h.store.ListPublicRoomsByOwner(c.Request.Context(), id, limit, offset)
	if err != nil {
		profileInternal(c, "查询房主的公开房失败", err)
		return
	}

	items := make([]authorRoomItem, 0, len(rows))
	for i := range rows {
		items = append(items, authorRoomItem{
			RoomID:     rows[i].RoomID,
			Title:      rows[i].Title,
			CreatedAt:  rows[i].CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
			LastSeenAt: rows[i].LastSeenAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	c.JSON(http.StatusOK, authorRoomList{Items: items, HasMore: hasMore})
}

// profilePage 解析 limit / offset（沿用 publicroom 的 boundedLimit / boundedOffset 口径）。
//
// 解析失败时**已经**写好了 400 响应，调用方直接 return。
func profilePage(c *gin.Context) (limit, offset int, ok bool) {
	limit, err := boundedLimit(c.Query("limit"), profilePageDefaultLimit, profilePageMaxLimit)
	if err != nil {
		publicRoomBadRequest(c, err.Error())
		return 0, 0, false
	}
	offset, err = boundedOffset(c.Query("offset"))
	if err != nil {
		publicRoomBadRequest(c, err.Error())
		return 0, 0, false
	}
	return limit, offset, true
}

// optionalViewer 尝试从请求识别登录者；失败一律返回 0（匿名）。
//
// 与 postHandler.optionalViewer 逐字同构。**为什么不能直接用 CurrentUser**：
// 那两个 helper 依赖 `RequireAuth` 中间件往上下文里塞的用户，而本路由是免登录的，
// 中间件根本不会跑 —— 于是 viewer 恒为 0，拉黑谓词（只在 viewerID>0 时挂）
// 静默失效，症状是"被拉黑的人还能在对方档案页看到 TA 的帖"。
// 这个坑在 `TestAuthorPostsHiddenByBlock` 里被抓到。
func (h *authorContentHandler) optionalViewer(c *gin.Context) int64 {
	token, ok := bearerToken(c)
	if !ok || h.auth == nil {
		return 0
	}
	u, err := h.auth.VerifyAccessToken(c.Request.Context(), token)
	if err != nil {
		return 0
	}
	return u.ID
}

// profileInternal 是内容块查库失败时的统一出口（日志 + 500，不泄漏内部细节）。
func profileInternal(c *gin.Context, what string, err error) {
	// 复用好友域的 500 出口：它已经做了「日志写全、响应只给一句人话」这两件事，
	// 再写一份只会让两个域的错误文案漂移。
	friendInternal(c, what, err)
}
