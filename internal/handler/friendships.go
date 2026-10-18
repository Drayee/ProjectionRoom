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
func registerFriendshipRoutes(api *gin.RouterGroup, st *store.Store, cfg *config.Config, auth AccountService) {
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

	h := &friendshipHandler{store: st}

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
		c.JSON(http.StatusOK, friendshipView{
			RelationshipID: row.ID, UserID: row.RequesterID, Status: row.Status,
		})
	case "reject":
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
	if err := h.store.RemoveFriendship(c.Request.Context(), id, actor.ID); err != nil {
		h.writeStoreError(c, err)
		return
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
