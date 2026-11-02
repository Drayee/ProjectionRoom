// 我的放映室端点（票 #70）：`GET /api/rooms/mine`。
//
// # 为什么需要它
//
// 在此之前「我的房间」只读本标签页的 `sessionStorage` —— 换个标签页、换台设备、
// 或者清一次缓存，房主就看不到自己开过哪些房了。而"把那间关掉的房重新打开"
// 恰恰只能由房主完成。
//
// # 与 `GET /api/users/:id/rooms` 的口径差异（这是本端点存在的全部理由）
//
//	他人档案页（票 #68）：**只要公开且未关闭的** —— 那是"这个人对外展示的橱窗"；
//	我的放映室（本票）  ：**owner 的全部房，含未公开与已关闭** —— 那是"我的资产清单"。
//
// 复用前者的过滤条件会让症状变成"我把房关掉了，它就凭空消失了"，而房主正是
// 唯一需要看到它来重新开放的人。两条口径刻意分开，各自有 store 方法与用例。
//
// # 为什么是 `/api/rooms/mine` 而不是 `?owner=me`
//
// 身份只能来自校验过的 access token（I3）。一个能传 owner 的查询串会立刻引出
// "那能不能传别人的 id"这个问题，而答案是"不能"—— 那就不要把它设计成可传的。
// 路径里出现 `mine` 这个词，本身就在说"这里没有参数"。
//
// 路径与既有的 `GET /api/rooms/:roomId` 并存：gin 的 radix 树静态段优先于通配符，
// 已用临时用例实测（`/api/rooms/mine` → mine 处理器，`/api/rooms/ABCD1234` → 房间探测）。
// 唯一的代价是**房间码不能是 `MINE`** —— 房间码是 `^[A-Z0-9]{4,12}$`，技术上
// `MINE` 合法；但它是 4 位、需要恰好被建成房间码，而那条路径会被这里吃掉。
// 这是一个已知的、可接受的边界（与"`/api/users/search` 吃掉 id=search"同族）。
package handler

import (
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/store"
)

// 我的放映室分页：与公开房列表同一套缺省/上限（同一类对象、同一类页面）。
const (
	myRoomsDefaultLimit = 20
	myRoomsMaxLimit     = 100
)

// myRoomItem 是「我的放映室」列表项。
//
// 比 `GET /api/users/:id/rooms` 的 item 多两个布尔（isPublic / isClosed），
// 因为房主需要据此画「私密」「已关闭」标记并给出"重新开放"的入口 ——
// 而那正是他人视角不该看到的信息。
type myRoomItem struct {
	RoomID      string `json:"roomId"`
	Title       string `json:"title"`
	IsPublic    bool   `json:"isPublic"`
	IsClosed    bool   `json:"isClosed"`
	HasPassword bool   `json:"hasPassword"`
	CreatedAt   string `json:"createdAt"`
	LastSeenAt  string `json:"lastSeenAt"`
}

type myRoomList struct {
	Items   []myRoomItem `json:"items"`
	HasMore bool         `json:"hasMore"`
}

// registerMyRoomRoutes 注册 `/api/rooms/mine`。
//
// 装配约定与帖子/关注域相同：DSN 为空 → 不注册（房间元数据属于账号层）；
// store 缺失 → 503（比"路由凭空消失"好定位）。
func registerMyRoomRoutes(api *gin.RouterGroup, st *store.Store, cfg *config.Config, auth AccountService) {
	if api == nil {
		return
	}
	if cfg == nil || strings.TrimSpace(cfg.Auth.DBDSN) == "" {
		return
	}
	if st == nil {
		log.Printf("[WARN] 已配置 PR_DB_DSN 但 Store 未装配：/api/rooms/mine 一律返回 503")
		api.GET("/rooms/mine", func(c *gin.Context) {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "我的放映室暂不可用（服务端未完成装配）",
				"code":  CodeAuthUnavailable,
			})
		})
		return
	}

	h := &myRoomHandler{store: st}
	// 只读桶：它是列表查询，与公开房列表同量级。
	limiter := newKeyedLimiter(cfg.IPC.RoomMetaPerMinute, cfg.IPC.RoomMetaBurst, "我的放映室")
	api.GET("/rooms/mine", RequireAuth(auth), limiter, h.list)
}

type myRoomHandler struct {
	store *store.Store
}

func (h *myRoomHandler) list(c *gin.Context) {
	actor, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}

	limit, err := boundedLimit(c.Query("limit"), myRoomsDefaultLimit, myRoomsMaxLimit)
	if err != nil {
		publicRoomBadRequest(c, "limit 必须是正整数")
		return
	}
	offset, err := boundedOffset(c.Query("offset"))
	if err != nil {
		publicRoomBadRequest(c, "offset 必须是非负整数")
		return
	}

	rows, hasMore, err := h.store.ListRoomsByOwner(c.Request.Context(), actor.ID, limit, offset)
	if err != nil {
		myRoomInternal(c, "查询我的放映室失败", err)
		return
	}

	items := make([]myRoomItem, 0, len(rows))
	for i := range rows {
		m := &rows[i]
		items = append(items, myRoomItem{
			RoomID:      m.RoomID,
			Title:       m.Title,
			IsPublic:    m.IsPublic,
			IsClosed:    m.ClosedAt != nil,
			HasPassword: m.HasPassword,
			CreatedAt:   m.CreatedAt.UTC().Format(time.RFC3339),
			LastSeenAt:  m.LastSeenAt.UTC().Format(time.RFC3339),
		})
	}
	c.JSON(http.StatusOK, myRoomList{Items: items, HasMore: hasMore})
}

func myRoomInternal(c *gin.Context, what string, err error) {
	log.Printf("[ERROR] %s: %v", what, err)
	c.JSON(http.StatusInternalServerError, gin.H{
		"error": "服务器开小差了，稍后再试",
		"code":  "INTERNAL",
	})
}
