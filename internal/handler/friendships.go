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
func registerFriendshipRoutes(api *gin.RouterGroup, st *store.Store, cfg *config.Config, auth AccountService, pusher FriendPusher) {
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
		return
	}

	h := &friendshipHandler{store: st, pusher: pusher}

	// 全部端点都要登录（非登录一律 401，验收第 2 条）。
	// 限速：读写都用 RoomMeta 桶（60/min）——好友操作是低频互动，不需要更严的桶。
	// 发请求/拉黑这两类"能骚扰别人"的操作单独用登录桶（10/min）压一档。
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
}

type friendshipHandler struct {
	store *store.Store
	// pusher 是实时投递出口（wayfinder #31 的 /ws/user）。
	//
	// nil = 不做实时投递：关系变化照常落库，对方下次打开好友页/刷新列表仍然看得到。
	// 与 messages.go 的同名字段是同一条取舍（实时是体验增量，不是正确性前提）。
	pusher FriendPusher
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

// writeStoreError 把 store 语义错误翻成 HTTP 状态。
//
// 关键：ErrFriendshipForbidden 也翻成 404（不是 403）—— 避免存在性探测
// （403 会告诉别人"这条关系存在，只是不归你"）。
func (h *friendshipHandler) writeStoreError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, store.ErrFriendshipNotFound), errors.Is(err, store.ErrFriendshipForbidden):
		c.JSON(http.StatusNotFound, gin.H{"error": "好友关系不存在", "code": codeNotFound})
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
