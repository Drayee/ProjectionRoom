// 好友关系端点（wayfinder #29）。严格按 docs/ACCOUNTS.md §7.1 的「一段关系只存一行」。
//
// 三条判据：
//
//  1. **状态语义在 store 层裁决**。pending 只有 addressee 能 accept/reject、
//     accepted 双方都能删、blocked 有向且由服务端强制——这些都不是 HTTP 层能"显示层"
//     解决的问题，handler 只负责取 actor、解析参数、把 store 的语义错误翻成状态码。
//  2. **越权统一 404，不是 403**。`ErrFriendshipForbidden` 也翻成 404：403 会告诉
//     别人"这条关系存在，只是不归你"，等于给了存在性探测的答案。404 则两者不可分。
//  3. **写操作同步**。响应要回带权威行（新状态的 id / 状态），照 #27 与
//     account.go CreateUser 的同一条理由：结果本身就是响应的一部分。真正该异步的是
//     非响应必要的附带写入；好友关系的变化直接决定"下一步界面显示什么"，必须同步。
package handler

import (
	"context"
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

// registerFriendshipRoutes 注册好友路由。装配约定同 posts/anime：
// 账号能力关闭时不注册；Store 缺失时注册一组 503。
//
// wayfinder #39 追加三个端点（#28 实测确认缺失）：
//
//	GET    /api/users/search?q=   按用户名/昵称找人
//	GET    /api/blocks            我拉黑的人
//	DELETE /api/blocks/:userId    解除拉黑
//
// 三件的共同点是**前端已经需要、服务端还没有**：没有搜索，"加好友"只能靠 userId
// 而 userId 在界面上不可得；没有黑名单列表与解除，拉黑就是单向不可逆的
// （见 #28 的结论里那三行缺口说明）。
//
// 路径归属的两个判断：
//
//  1. **`/api/users/search` 而不是 `/api/friends/search`**：它回答的是"这个站上有谁"，
//     不是"我的好友里有谁"。#33 的公开档案（`GET /api/users/:id`）将共用这个前缀，
//     所以搜索放在 users 下 —— 好友域只是它的第一个消费者。
//     gin 的路由树里静态段 `search` 与参数段 `:id` 可以共存（探针实测：
//     静态段优先命中，`/api/users/42` 仍走参数段），因此这不给 #33 留坑。
//  2. **`/api/blocks` 与 `POST /api/blocks` 同前缀**：#29 的拉黑已经占了这个路径，
//     列表与解除天然属于同一个资源集合（读集合 / 删成员），不新造名词。
func registerFriendshipRoutes(api *gin.RouterGroup, st *store.Store, cfg *config.Config, auth AccountService, pusher FriendPusher, auditSink AdminAuditSink, auditWriter AdminAuditWriter) {
	if api == nil {
		return
	}
	if cfg == nil || strings.TrimSpace(cfg.Auth.DBDSN) == "" {
		return
	}
	if st == nil {
		unavailable := func(c *gin.Context) {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "好友能力暂不可用",
				"code":  CodeAuthUnavailable,
			})
		}
		api.GET("/friends", unavailable)
		api.GET("/friends/requests", unavailable)
		api.POST("/friends/requests", unavailable)
		api.POST("/friends/requests/:requestId/accept", unavailable)
		api.POST("/friends/requests/:requestId/reject", unavailable)
		api.DELETE("/friends/:friendshipId", unavailable)
		api.POST("/blocks", unavailable)
		api.GET("/users/search", unavailable)
		api.GET("/blocks", unavailable)
		api.DELETE("/blocks/:userId", unavailable)
		return
	}

	h := &friendshipHandler{
		store:  st,
		pusher: pusher,
		// 审计两件套从 AdminDeps 透传下来（与 anime.go 同一对实例）。
		// nil 是合法的（写操作照常生效，只记 WARN），因此这里不做判空兜底 ——
		// 判空兜底会把"装配缺了审计"藏起来，而 §9 要求缺失必须可见。
		auditSink:   auditSink,
		auditWriter: auditWriter,
	}

	// 全部端点都要登录（非登录一律 401，验收第 2 条）。
	// 限速：读写都用 RoomMeta 桶（60/min）——好友操作是低频互动，不需要更严的桶。
	// 发请求/拉黑这两类"能骚扰别人"的操作单独用登录桶（10/min）压一档。
	//
	// #39 的两条新限速判断（#27 的纪律：按"滥用伤害 + 固有成本"，不是按"是不是写端点"）：
	//   - **搜索走只读桶**。它高频（用户会边打边搜）、幂等、纯读；
	//     用登录桶会把"连着改几个关键词"打成 429 —— 那是正常用户的搜法。
	//   - **解除拉黑走登录桶**。它是写操作（删一行），但**只减数据、重试无害**
	//     （重复解除是 404 的 no-op）。不放只读桶是因为它确实改变了状态；
	//     不放注册桶是因为它的滥用伤害低于发好友请求（解除不通知任何人）。
	readLimiter := newKeyedLimiter(cfg.IPC.RoomMetaPerMinute, cfg.IPC.RoomMetaBurst, "好友查询")
	writeLimiter := newKeyedLimiter(cfg.IPC.LoginPerMinute, cfg.IPC.LoginBurst, "好友变更")

	authed := api.Group("", RequireAuth(auth))
	authed.GET("/friends", readLimiter, h.listFriends)
	authed.GET("/friends/requests", readLimiter, h.listRequests)
	authed.POST("/friends/requests", writeLimiter, h.createRequest)
	authed.POST("/friends/requests/:requestId/accept", writeLimiter, h.accept)
	authed.POST("/friends/requests/:requestId/reject", writeLimiter, h.reject)
	authed.DELETE("/friends/:friendshipId", writeLimiter, h.remove)
	authed.POST("/blocks", writeLimiter, h.block)
	authed.GET("/users/search", readLimiter, h.searchUsers)
	authed.GET("/blocks", readLimiter, h.listBlocked)
	authed.DELETE("/blocks/:userId", writeLimiter, h.unblock)
}

type friendshipHandler struct {
	store *store.Store
	// pusher 是实时投递出口（wayfinder #31 的 /ws/user）。
	//
	// nil = 不做实时投递：关系变化照常落库，对方下次打开好友页/刷新列表仍然看得到。
	// 与 messages.go 的同名字段是同一条取舍（实时是体验增量，不是正确性前提）。
	pusher FriendPusher
	// auditSink / auditWriter 是审计的异步投递与写能力（#39 起用，见 writeAudit）。
	//
	// 与 AnimeDeps 里那一对是**同一类依赖**（§9 的事件类），这里不通过 AnimeDeps 传：
	// 好友域的写操作由本文件自己投递，把"写审计"的能力塞进一个番剧域的结构体
	// 会让"谁在写审计"变成一处跨域跳转。
	//
	// 两者为 nil 时写操作仍然生效，只记一条 WARN —— 审计是 must 落点，缺失必须可见。
	auditSink   AdminAuditSink
	auditWriter AdminAuditWriter
}

// writeAudit 投递一条好友域写操作的审计（异步，事件类 §9）。
//
// 为什么好友操作也要审计：#29 落地时没做（当时只有 6 个端点，且都改自己的关系），
// 但 #39 的**解除拉黑**改变了这一点 —— 它是一个可逆的状态变更，事后要能回答
// "谁解除了对谁的拉黑"。与 admin.go / anime.go 完全同一条路径：投递即返回，
// 队列缺失/已关闭只记 WARN，绝不阻塞请求。
func (h *friendshipHandler) writeAudit(c *gin.Context, actorID int64, action string, targetID int64, detail map[string]any) {
	if h.auditSink == nil || h.auditWriter == nil {
		log.Printf("[WARN] 好友动作 %s 无法写审计（装配里缺少写队列或审计写能力）：target=%d actor=%d",
			action, targetID, actorID)
		return
	}
	ip := c.ClientIP()
	row := &store.AdminAudit{
		ActorID:    actorID,
		Action:     action,
		TargetType: "user",
		TargetID:   strconv.FormatInt(targetID, 10),
		Detail:     auditDetailJSON(detail),
	}
	if strings.TrimSpace(ip) != "" {
		row.IP = &ip
	}
	if !h.auditSink.SubmitJob(func(ctx context.Context) error {
		return h.auditWriter.InsertAudit(ctx, row)
	}) {
		log.Printf("[WARN] 好友动作 %s 的审计入队失败（队列满或写入器已关闭）：target=%d actor=%d",
			action, targetID, actorID)
	}
}

// FriendPusher 是"把好友域事件推给对方"的窄出口。
//
// 四个事件各自一个方法而不是一个带 kind 的方法：它们的**收件人不同**
// （请求推给被请求方、接受推给请求方、删除推给另一方、拉黑推给被拉黑方），
// 而"推给谁"正是这里最容易搞错的地方 —— 让方法名把收件人写死，
// 调用点就没有机会传错方向。
type FriendPusher interface {
	PushFriendRequest(ctx context.Context, actorID, targetID int64, relationshipID int64, actorName string) bool
	PushFriendAccepted(ctx context.Context, actorID, requesterID int64, relationshipID int64, actorName string) bool
	PushFriendRemoved(ctx context.Context, actorID, targetID int64) bool
	PushBlocked(ctx context.Context, actorID, targetID int64) bool
}

// friendshipView 是响应的对外形状：一段关系的"对方"是谁。
type friendshipView struct {
	RelationshipID int64  `json:"relationshipId"`
	UserID         int64  `json:"userId"`
	DisplayName    string `json:"displayName"`
	Status         string `json:"status"`
}

// —— #39 的对外形状 ——

// maxSearchQueryRunes 是搜索关键词的字符上限。
//
// 取 username(20) 与 display_name(32) 的较大者：搜的就是这两列，
// 比最长的那个还长的关键词不可能命中任何人。提前 400 省掉一次全表 ILIKE
// （那类扫描在 users 变大之后不是免费的）。
const maxSearchQueryRunes = 32

// searchHitView 是搜索结果的一行（#39）。
//
// 带 username 与 display_name 两列：前端要显示昵称，而**搜索框旁边要能回显
// 命中的是用户名**（搜 "miao" 命中「喵喵」时，用户需要确认这就是他要找的人）。
// 后端契约不掺显示层（无 avatarChar/avatarColor，同 posts 的 postAuthorView）。
type searchHitView struct {
	UserID      int64  `json:"userId"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	// Relation 是我与这个人的关系：none|friend|pending-out|pending-in|blocked。
	// 前端据此决定按钮显示什么、能不能点。
	Relation string `json:"relation"`
}

// blockedEntryView 是黑名单的一行（#39）。
//
// BlockedAt 用 RFC3339 字符串（与 messageView / animeView 同一格式 timeLayout）：
// 跨时区部署下"本地时间字符串"会在别人的浏览器里显示成另一个时刻。
type blockedEntryView struct {
	UserID      int64  `json:"userId"`
	DisplayName string `json:"displayName"`
	Username    string `json:"username"`
	BlockedAt   string `json:"blockedAt"`
}

// peerOf 从关系行里抽出"对方"是谁（相对 actor）。
func peerOf(f *store.Friendship, actorID int64) (peerID int64, peerName string) {
	if f.RequesterID == actorID {
		return f.AddresseeID, f.PeerName
	}
	return f.RequesterID, f.PeerName
}

func (h *friendshipHandler) listFriends(c *gin.Context) {
	actor, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}
	rows, err := h.store.ListFriends(c.Request.Context(), actor.ID)
	if err != nil {
		friendInternal(c, "查询好友列表失败", err)
		return
	}
	items := make([]friendshipView, 0, len(rows))
	for i := range rows {
		pid, pname := peerOf(&rows[i], actor.ID)
		items = append(items, friendshipView{
			RelationshipID: rows[i].ID, UserID: pid, DisplayName: pname,
			Status: rows[i].Status,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// listRequests 返回收发两个方向的 pending 请求（收件箱 + 发件箱）。
func (h *friendshipHandler) listRequests(c *gin.Context) {
	actor, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}
	incoming, err := h.store.ListIncomingRequests(c.Request.Context(), actor.ID)
	if err != nil {
		friendInternal(c, "查询待处理请求失败", err)
		return
	}
	outgoing, err := h.store.ListOutgoingRequests(c.Request.Context(), actor.ID)
	if err != nil {
		friendInternal(c, "查询已发出请求失败", err)
		return
	}
	in := make([]friendshipView, 0, len(incoming))
	for i := range incoming {
		in = append(in, friendshipView{
			RelationshipID: incoming[i].ID, UserID: incoming[i].RequesterID,
			DisplayName: incoming[i].PeerName, Status: incoming[i].Status,
		})
	}
	out := make([]friendshipView, 0, len(outgoing))
	for i := range outgoing {
		out = append(out, friendshipView{
			RelationshipID: outgoing[i].ID, UserID: outgoing[i].AddresseeID,
			DisplayName: outgoing[i].PeerName, Status: outgoing[i].Status,
		})
	}
	c.JSON(http.StatusOK, gin.H{"incoming": in, "outgoing": out})
}

type friendshipTargetRequest struct {
	UserID int64 `json:"userId"`
}

func (h *friendshipHandler) createRequest(c *gin.Context) {
	actor, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}
	var req friendshipTargetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		adminBadRequest(c, "请求体不是合法 JSON")
		return
	}
	if req.UserID <= 0 {
		adminBadRequest(c, "userId 必填且必须是正整数")
		return
	}

	row, err := h.store.CreateFriendshipRequest(c.Request.Context(), actor.ID, req.UserID)
	if err != nil {
		h.writeStoreError(c, err)
		return
	}
	// 推给**被请求方**（req.UserID）：他要看到"有人想加你"。
	// 用 actor 的昵称（请求方），因为收件人界面上要显示"谁加了我"。
	if h.pusher != nil {
		h.pusher.PushFriendRequest(context.WithoutCancel(c.Request.Context()),
			actor.ID, req.UserID, row.ID, actor.DisplayName)
	}
	c.JSON(http.StatusCreated, friendshipView{
		RelationshipID: row.ID, UserID: row.AddresseeID, Status: row.Status,
	})
}

func (h *friendshipHandler) accept(c *gin.Context) {
	h.handleRequestOutcome(c, "accept")
}

func (h *friendshipHandler) reject(c *gin.Context) {
	h.handleRequestOutcome(c, "reject")
}

func (h *friendshipHandler) handleRequestOutcome(c *gin.Context, op string) {
	actor, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}
	id, ok := relationshipIDParam(c, "requestId")
	if !ok {
		return
	}

	switch op {
	case "accept":
		row, err := h.store.AcceptFriendshipRequest(c.Request.Context(), id, actor.ID)
		if err != nil {
			h.writeStoreError(c, err)
			return
		}
		// 推给**请求方**（row.RequesterID）：他要看到"对方同意了"。
		// 注意 actor 是接受的人（addressee），与收件人方向相反 —— 这正是
		// 把收件人写进方法名（PushFriendAccepted）的理由。
		if h.pusher != nil {
			h.pusher.PushFriendAccepted(context.WithoutCancel(c.Request.Context()),
				actor.ID, row.RequesterID, row.ID, actor.DisplayName)
		}
		c.JSON(http.StatusOK, friendshipView{
			RelationshipID: row.ID, UserID: row.RequesterID, Status: row.Status,
		})
	case "reject":
		// reject **不推送**：请求被拒方在界面上不需要一条"对方拒绝了你"的通知
		// （那是骚扰面：拒绝方本来就想安静地结束这件事）。请求方下次拉列表
		// 会发现那条 pending 消失了 —— 这是刻意的静默。
		if err := h.store.RejectFriendshipRequest(c.Request.Context(), id, actor.ID); err != nil {
			h.writeStoreError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

func (h *friendshipHandler) remove(c *gin.Context) {
	actor, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}
	id, ok := relationshipIDParam(c, "friendshipId")
	if !ok {
		return
	}
	// 删除前先记下"对方是谁"：RemoveFriendship 之后那行就没了，
	// 而推送需要一个收件人 id（读一次 store 比让 RemoveFriendship 多返回一个值更省改动）。
	peerID := h.peerIDOf(c, id, actor.ID)

	if err := h.store.RemoveFriendship(c.Request.Context(), id, actor.ID); err != nil {
		h.writeStoreError(c, err)
		return
	}
	// 推给**另一方**（不是 actor 自己）。
	if h.pusher != nil && peerID > 0 {
		h.pusher.PushFriendRemoved(context.WithoutCancel(c.Request.Context()), actor.ID, peerID)
	}
	c.Status(http.StatusNoContent)
}

func (h *friendshipHandler) block(c *gin.Context) {
	actor, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}
	var req friendshipTargetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		adminBadRequest(c, "请求体不是合法 JSON")
		return
	}
	if req.UserID <= 0 {
		adminBadRequest(c, "userId 必填且必须是正整数")
		return
	}
	row, err := h.store.BlockUser(c.Request.Context(), actor.ID, req.UserID)
	if err != nil {
		h.writeStoreError(c, err)
		return
	}
	// 推给**被拉黑方**（req.UserID）：他要立刻知道"发不出去了"，
	// 否则界面上表现为"消息一直发，对方却收不到"——那是比被拉黑更糟的体验。
	// 拉黑的方向语义是单向的（§7.1），所以这条通知只发给被拉黑的一方。
	if h.pusher != nil {
		h.pusher.PushBlocked(context.WithoutCancel(c.Request.Context()), actor.ID, req.UserID)
	}
	// block 返回的"对方"是被拉黑方（addressee）。displayName 从 store 回带
	//（BlockUser 里 JOIN 了 users，见 store/friendships.go）。
	c.JSON(http.StatusOK, friendshipView{
		RelationshipID: row.ID, UserID: row.AddresseeID, DisplayName: row.PeerName, Status: row.Status,
	})
}

// —— #39 的三个新端点：按用户名找人 / 黑名单列表 / 解除拉黑 ——

// searchUsers 是 `GET /api/users/search?q=<关键词>`（#39）。
//
// 存在的理由：#28 实测发现"加好友"这条路根本走不通 —— 发起请求需要对方的 userId，
// 而 userId 在界面上不可得。没有搜索，好友功能的上半段（发现人）是断的。
//
// 三条设计：
//
//  1. **结果带关系状态** `relation ∈ none|friend|pending-out|pending-in|blocked`。
//     少了它前端每个结果还得再问一次"我跟他什么关系"，且"加好友"按钮的可用性
//     要靠猜。带上它，一次请求就够画出整行（名字 + 关系 + 该显示什么按钮）。
//  2. **blocked 出现在结果里**（relation=blocked），不隐藏。用户要能看见自己拉黑了谁，
//     而搜索是唯一不需要先记住 userId 的入口 —— 隐藏它会让"解除拉黑"没有入口。
//     被拉黑方（对方拉黑我）也显示为 blocked：前端据此禁用按钮，而**不告诉用户
//     是谁拉黑了谁**（那需要一次额外查询，且泄露了对方视角的信息）。
//  3. **q 空 → 400，超长 → 400**（验收第 2 条）。空关键词不做"返回全部用户"——
//     那等于把一个免限速的用户目录出口开出来（这里虽然要登录，仍然是枚举面）。
//     上限取 maxUsernameLen 与 maxDisplayNameLen 的较大者（32）：搜的是这两列，
//     比它更长的关键词不可能命中任何人，提前拒绝省一次全表 ILIKE。
func (h *friendshipHandler) searchUsers(c *gin.Context) {
	actor, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}
	q := strings.TrimSpace(c.Query("q"))
	if q == "" {
		adminBadRequest(c, "q 必填（按用户名或昵称搜索）")
		return
	}
	if len([]rune(q)) > maxSearchQueryRunes {
		adminBadRequest(c, "搜索关键词太长")
		return
	}

	hits, err := h.store.SearchUsers(c.Request.Context(), q, actor.ID)
	if err != nil {
		friendInternal(c, "搜索用户失败", err)
		return
	}
	// 关系状态：一次性把本次结果的全部 id 查出来，而不是每个结果一次查询。
	//
	// 为什么值得单独一条查询路径：结果集上限 20，逐行查就是 20 次往返
	// （N+1），而一次 `WHERE (a,b) 对 IN …` 是 1 次。搜索是高频读端点
	// （用户会边打边搜），这个差距会直接反映在界面上。
	relations, err := h.relationStatesFor(c.Request.Context(), actor.ID, hits)
	if err != nil {
		friendInternal(c, "查询关系状态失败", err)
		return
	}

	items := make([]searchHitView, 0, len(hits))
	for i := range hits {
		relation := relations[hits[i].ID]
		if relation == "" {
			// 兜底成 none：关系状态是前端按钮语义的判据，空串会让它落到
			// "还可以加好友"（default 分支）—— 那在真实状态是"已拉黑"时是错的。
			// 正常路径上 map 里一定有值（RelationStateOf 对 nil 行也返回 none）。
			relation = store.RelationNone
		}
		items = append(items, searchHitView{
			UserID:      hits[i].ID,
			Username:    hits[i].Username,
			DisplayName: hits[i].DisplayName,
			Relation:    relation,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// relationStatesFor 批量算出"我与这批人各自是什么关系"。
//
// 返回 map[对方id]RelationState；查不到关系的（绝大多数）是 RelationNone。
//
// **一次查询拿全部**：friendships 里"与我有关"的行本来就该一次取回来
// （一个人与另一个人的关系最多一行，而我的关系总数是有限的小数字）。
// 逐行查会让"搜索 20 个人"变成 21 次往返 —— 那是典型的 N+1，
// 而它的症状（搜索明显卡）很容易被误判成"数据库慢"。
func (h *friendshipHandler) relationStatesFor(ctx context.Context, actorID int64, hits []store.UserSearchHit) (map[int64]string, error) {
	out := make(map[int64]string, len(hits))
	if len(hits) == 0 {
		return out, nil
	}
	ids := make([]int64, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, h.ID)
	}
	var rows []store.Friendship
	if err := h.store.DB().WithContext(ctx).
		Where(`(requester_id = ? OR addressee_id = ?) AND
			((requester_id IN ? AND addressee_id = ?) OR (addressee_id IN ? AND requester_id = ?))`,
			actorID, actorID, ids, actorID, ids, actorID).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for i := range rows {
		peerID := rows[i].AddresseeID
		if peerID == actorID {
			peerID = rows[i].RequesterID
		}
		out[peerID] = store.RelationStateOf(&rows[i], actorID)
	}
	return out, nil
}

// listBlocked 是 `GET /api/blocks`：我拉黑的人（#39）。
//
// 与 #29 的 `POST /api/blocks` 是同一个资源集合的读/写两面。
// 只列 blocked_by_id = 当前用户 的行（有向语义，§7.1）：
// "别人拉黑我"的那一行对我不可见。
func (h *friendshipHandler) listBlocked(c *gin.Context) {
	actor, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}
	rows, err := h.store.ListBlocked(c.Request.Context(), actor.ID)
	if err != nil {
		friendInternal(c, "查询黑名单失败", err)
		return
	}
	items := make([]blockedEntryView, 0, len(rows))
	for i := range rows {
		items = append(items, blockedEntryView{
			UserID:      rows[i].BlockedID,
			DisplayName: rows[i].DisplayName,
			Username:    rows[i].Username,
			BlockedAt:   rows[i].BlockedAt.Format(timeLayout),
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// unblock 是 `DELETE /api/blocks/:userId`（#39）。
//
// 解除拉黑：删掉"我拉黑他"的那一行，关系回到"没有关系"。
// 之后可以重新发好友请求（行没了，CreateFriendshipRequest 的裁决自然放行）。
//
// **别人拉黑我的行 → 404**（验收第 2 条，与 #29 同一决定）：
// 403 会告诉别人"这条关系存在，只是不归你"，等于给了存在性探测的答案。
// 404 下"不存在"与"不是你的"不可分辨，而两者对调用方的正确动作完全一样
// （都是"这人不在你的黑名单里"）。
func (h *friendshipHandler) unblock(c *gin.Context) {
	actor, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}
	targetID, ok := userIDParam(c, "userId")
	if !ok {
		return
	}
	if err := h.store.Unblock(c.Request.Context(), actor.ID, targetID); err != nil {
		h.writeStoreError(c, err)
		return
	}
	// 审计：解除拉黑是一个可逆的状态变更，事后要能回答"谁解除了对谁的拉黑"。
	// 异步投递（§9 的事件类），不阻塞响应 —— 与 anime 的管理端动作同一条路径。
	h.writeAudit(c, actor.ID, "friend.unblock", targetID, map[string]any{
		"targetUserId": targetID,
	})
	// 推给**被解除方**：他需要知道"现在能重新加你了"，否则界面上表现为
	// "我发不出请求，但也不知道什么时候可以"。
	// 推失败不影响响应（事实已经落库，对方下次拉列表照样看得到）。
	if h.pusher != nil {
		h.pusher.PushFriendRemoved(context.WithoutCancel(c.Request.Context()), actor.ID, targetID)
	}
	c.Status(http.StatusNoContent)
}

// userIDParam 解析并校验路径参数 :userId（#39 的解除拉黑）。
//
// 与 relationshipIDParam 同一条纪律：非正整数 400 且提示语不含任何存在性信息。
func userIDParam(c *gin.Context, name string) (int64, bool) {
	raw := strings.TrimSpace(c.Param(name))
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "用户 id 不合法", "code": model.CodeBadRequest})
		return 0, false
	}
	return id, true
}

// writeStoreError 把 store 语义错误翻成 HTTP 状态。
func (h *friendshipHandler) writeStoreError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, store.ErrFriendshipNotFound), errors.Is(err, store.ErrFriendshipForbidden):
		c.JSON(http.StatusNotFound, gin.H{"error": "好友关系不存在", "code": codeNotFound})
	case errors.Is(err, store.ErrBlockedRowNotFound):
		// 黑名单里没有这一行（含"这不是你拉黑的"与"从未拉黑过"两种情形）。
		// 与上一支同一理由：统一 404，让"不存在"与"不是你的"不可分辨。
		c.JSON(http.StatusNotFound, gin.H{"error": "黑名单里没有这个人", "code": codeNotFound})
	case errors.Is(err, store.ErrAlreadyFriends), errors.Is(err, store.ErrRequestAlreadyPending):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "code": "FRIENDSHIP_CONFLICT"})
	case errors.Is(err, store.ErrBlockedByTarget):
		c.JSON(http.StatusForbidden, gin.H{"error": "对方已将你拉黑", "code": "BLOCKED_BY_TARGET"})
	case errors.Is(err, store.ErrSelfRelation):
		adminBadRequest(c, "不能对自己建立关系")
	default:
		friendInternal(c, "好友操作失败", err)
	}
}

// peerIDOf 在删除关系**之前**查出"对方是谁"（推送需要收件人 id）。
//
// 查不到时返回 0，调用方据此跳过推送：删一条不存在的关系本来就会在
// RemoveFriendship 里返回 404，推送跳过不影响那条路径的正确性。
//
// 为什么不让 RemoveFriendship 多返回一个 peerID：那会改一个已被 #29 的
// 真库用例覆盖的签名，而这里只需要一个"尽力而为"的收件人 id ——
// 推送失败/查不到都不是错误（实时是体验增量）。
func (h *friendshipHandler) peerIDOf(c *gin.Context, relationshipID, actorID int64) int64 {
	f, err := h.store.FriendshipByID(c.Request.Context(), relationshipID)
	if err != nil || f == nil {
		return 0
	}
	if f.RequesterID == actorID {
		return f.AddresseeID
	}
	if f.AddresseeID == actorID {
		return f.RequesterID
	}
	// 既不是请求方也不是被请求方：不推（这条关系不归 actor，
	// RemoveFriendship 会自己返回 404）。
	return 0
}

func relationshipIDParam(c *gin.Context, name string) (int64, bool) {
	raw := strings.TrimSpace(c.Param(name))
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "关系 id 不合法", "code": model.CodeBadRequest})
		return 0, false
	}
	return id, true
}

func friendInternal(c *gin.Context, what string, err error) {
	log.Printf("[ERROR] %s: %v", what, err)
	c.JSON(http.StatusInternalServerError, gin.H{"error": "服务端内部错误", "code": model.CodeInternalError})
}
