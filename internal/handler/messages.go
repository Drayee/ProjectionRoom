// 私聊消息端点（wayfinder #31）。docs/ACCOUNTS.md §7.2 / §10。
//
// 四条判据：
//
//  1. **全部登录**。私聊没有"公开读"形态：任何一条消息的可见性都取决于
//     收件人是谁，因此三个端点全挂 RequireAuth（401 而不是 403 —— 未登录与
//     没权限是两件事，前端据此决定"去登录"还是"就停在这"）。
//  2. **拉黑由服务端强制**。`POST /api/messages` 在 store 的事务里判关系，
//     命中就 403 + MESSAGE_BLOCKED。这不是前端隐藏按钮能替代的性质：
//     被拉黑方自己拼请求也必须发不进来（§7.1）。
//  3. **未读数进 `GET /api/auth/me`**。会话列表要显示未读徽标，而登录后的第一次
//     渲染走的是 me —— 让它多一个 `unread` 字段，比让前端再打一发专门计数更省，
//     也让"登录后立刻能看到有几条没读"不需要两段式加载。
//  4. **写操作同步返回权威行**。发消息的响应就是那条消息（含服务端分配的 id 与
//     created_at），照 #27 / #29 的同一条理由：结果本身就是响应的一部分，
//     客户端要拿它去 append 到列表里，异步化会逼前端自己编一个临时 id。
//
// 限速分档（#27 的纪律：按"滥用伤害 + 固有成本"，不是按"是不是写端点"）：
//   - 发消息走登录桶：单对 20 条/分钟（§7.2）在 service 层是产品规则，
//     而每 IP 的登录桶（10/min）在这里是**防脚本**的那道闸。两者不重复：
//     前者限"同一个对话别刷屏"，后者限"一个来源别狂发"。
//   - 拉历史走只读桶（60/min）：幂等、只读、往上翻是正常的高频动作。
//   - 上报已读走只读桶：幂等（read_at IS NULL 让重试变 no-op），且只减未读。
//     用登录桶会把"翻一屏就上报一次"打成 429 —— 那是正常用户的读法。
package handler

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/model"
	"ProjectionRoom/internal/service"
	"ProjectionRoom/internal/store"
)

// 私聊域在 HTTP 层的边界。与 store 的同名常量**必须同值**（见 store/messages.go）。
const (
	maxMessageBodyHTTP = store.MaxMessageBodyLen
	// messagePageHTTP 是协议定死的页大小（§7.2：50）。前端不传 limit，
	// 服务端也不读它 —— 多一个可调参数就多一条"翻页边界不一致"的路径。
	messagePageHTTP = 50
)

// CodeMessageBlocked 是"存在拉黑关系，拒绝投递"的错误码（与 403 配对）。
//
// 与 CodeForbidden（管理端权限不足）分开：两者都是 403，但对用户的含义完全不同 ——
// 前者是"你们之间有一道墙"，后者是"你不是管理员"。前端据此显示不同文案。
const CodeMessageBlocked = "MESSAGE_BLOCKED"

// CodeMessageNotFriends 是"不是好友，拒绝投递"的错误码（与 403 配对）。
//
// 与 CodeMessageBlocked 分开：两者都是 403，但一个要引导"加好友"、另一个要说明
// "对方拉黑了你"。前端据此给不同的下一步（前者给加好友按钮，后者没有出口）。
const CodeMessageNotFriends = "MESSAGE_NOT_FRIENDS"

// registerMessageRoutes 注册私聊路由。装配约定同 posts/friendships：
// 账号能力关闭时不注册；Store 缺失时注册一组 503。
func registerMessageRoutes(api *gin.RouterGroup, st *store.Store, cfg *config.Config, auth AccountService, pusher MessagePusher) {
	if api == nil {
		return
	}
	if cfg == nil || strings.TrimSpace(cfg.Auth.DBDSN) == "" {
		return
	}
	if st == nil {
		unavailable := func(c *gin.Context) {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "私聊能力暂不可用",
				"code":  CodeAuthUnavailable,
			})
		}
		api.GET("/messages", unavailable)
		api.GET("/messages/conversations", unavailable)
		api.POST("/messages", unavailable)
		api.POST("/messages/read", unavailable)
		return
	}

	h := &messageHandler{store: st, pusher: pusher}

	readLimiter := newKeyedLimiter(cfg.IPC.RoomMetaPerMinute, cfg.IPC.RoomMetaBurst, "私聊历史")
	writeLimiter := newKeyedLimiter(cfg.IPC.LoginPerMinute, cfg.IPC.LoginBurst, "发送私聊")

	authed := api.Group("", RequireAuth(auth))
	// 会话列表**必须注册在 /messages 之前**？不 —— 两者的路径段数不同
	//（/messages/conversations 是两段，/messages 是一段），gin 的路由树不会互相遮蔽。
	// 但读与写按"读先于写"排列，与 router.go 里公开房那段的排列理由一致（可读性）。
	authed.GET("/messages", readLimiter, h.list)
	authed.GET("/messages/conversations", readLimiter, h.conversations)
	authed.POST("/messages", writeLimiter, h.send)
	authed.POST("/messages/read", readLimiter, h.markRead)
}

type messageHandler struct {
	store *store.Store
	// pusher 是实时投递出口（wayfinder #31 的 /ws/user）。
	//
	// nil = 不做实时投递（装配缺失）：消息照常落库，对方下次拉历史/未读仍然看得到。
	// 这正是 §7.2 的"否则只落库"那条分支 —— 所以 nil 不是错误状态。
	pusher MessagePusher
}

// MessagePusher 是"把一条私聊/已读回执推给对方"的窄出口（*service.UserHub 的组合实现）。
//
// 为什么是接口：handler 不该知道 UserHub 与 store 如何组合，也不该为了"对方在不在线"
// 去 import 传输层。测试可以注入一个捕获型假实现来断言"推送了什么"，
// 而真实现只在装配层出现一次。
type MessagePusher interface {
	// PushDM 把一条新私聊推给收件人；返回是否至少有一条连接收到。
	PushDM(ctx context.Context, msg *store.Message) bool
	// PushDMRead 把"读到哪一条"推给发送方。
	PushDMRead(ctx context.Context, readerID, senderID, msgID int64, readAt time.Time) bool
}

// messageView 是单条消息的对外形状（camelCase，与前端 types 一致）。
//
// **没有 avatarChar / avatarColor**：那是纯 UI 派生（首字母 + 名字哈希取色），
// 客户端已有 utils/avatar.ts，后端契约不掺显示层（同 posts 的 postAuthorView）。
type messageView struct {
	ID         int64  `json:"id"`
	Kind       string `json:"kind"`
	SenderID   int64  `json:"senderId"`
	SenderName string `json:"senderName"`
	// RecipientID 对私聊是必填；用指针是因为 `kind=room` 时它必须缺席
	// （omitempty 对 0 值有效，而 0 是"没有收件人"的合法表示）。
	RecipientID *int64 `json:"recipientId,omitempty"`
	Body        string `json:"body"`
	CreatedAt   string `json:"createdAt"`
	// ReadAt 为空表示未读。它只对**收件人**有意义（发送方看到的一直是"未读"，
	// 因为已读回执还没有推送通道 —— 见 #31 的 out 段：/ws/user 属后续图）。
	ReadAt string `json:"readAt"`
}

func toMessageView(m *store.Message) messageView {
	readAt := ""
	if m.ReadAt != nil {
		readAt = m.ReadAt.Format(timeLayout)
	}
	return messageView{
		ID:          m.ID,
		Kind:        m.Kind,
		SenderID:    m.SenderID,
		SenderName:  m.SenderName,
		RecipientID: m.RecipientID,
		Body:        m.Body,
		CreatedAt:   m.CreatedAt.Format(timeLayout),
		ReadAt:      readAt,
	}
}

// conversationView 是会话列表的一行。
type conversationView struct {
	PeerID      int64  `json:"peerId"`
	PeerName    string `json:"peerName"`
	LastID      int64  `json:"lastId"`
	LastSender  int64  `json:"lastSenderId"`
	LastBody    string `json:"lastBody"`
	LastSentAt  string `json:"lastSentAt"`
	UnreadCount int64  `json:"unreadCount"`
}

// list 是 `GET /api/messages?peer=<userId>&before=<id>`（§7.2 的冻结契约）。
//
// 两个参数的分工：
//   - `peer` **必填**：没有它就没有会话（私聊的可见性由"对方是谁"决定，
//     缺它只能是 400，而不是"返回全部"）；
//   - `before` 可选：缺省 = 第一页（最新 50 条）。它是 **id 游标**，不是 offset
//     （理由见 store.ListDM 的注释）。
func (h *messageHandler) list(c *gin.Context) {
	actor, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}
	peerID, ok := parsePositiveQuery(c, "peer", true)
	if !ok {
		return
	}
	before, ok := parsePositiveQuery(c, "before", false)
	if !ok {
		return
	}

	rows, hasMore, err := h.store.ListDM(c.Request.Context(), actor.ID, peerID, before, messagePageHTTP)
	if err != nil {
		messageInternal(c, "查询私聊历史失败", err)
		return
	}
	items := make([]messageView, 0, len(rows))
	for i := range rows {
		items = append(items, toMessageView(&rows[i]))
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "hasMore": hasMore})
}

// conversations 是 `GET /api/messages/conversations`（会话列表 + 每对话未读数）。
//
// 它不在 §10 的冻结清单里，但 #30（消息页）需要它：会话列表是消息页的**首屏**，
// 而"列出我参与过哪些对话"没有任何既有端点能表达（`/api/messages` 必须给 peer）。
// 契约形状是 `{items: [...]}`，与好友列表同构。
func (h *messageHandler) conversations(c *gin.Context) {
	actor, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}
	rows, err := h.store.ListDMConversations(c.Request.Context(), actor.ID)
	if err != nil {
		messageInternal(c, "查询会话列表失败", err)
		return
	}
	items := make([]conversationView, 0, len(rows))
	for _, r := range rows {
		items = append(items, conversationView{
			PeerID:      r.PeerID,
			PeerName:    r.PeerName,
			LastID:      r.LastID,
			LastSender:  r.LastSender,
			LastBody:    r.LastBody,
			LastSentAt:  r.LastSentAt.Format(timeLayout),
			UnreadCount: r.UnreadCount,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

type sendMessageRequest struct {
	RecipientID int64  `json:"recipientId"`
	Body        string `json:"body"`
}

// send 是 `POST /api/messages`（发一条私聊）。
//
// 清洗顺序与 #27 一致：**先 StripControl 再 TrimSpace**（反过来会留下
// 去控制符后新出现的首尾空白）。清洗后为空 → 400（§7.2：不允许空消息）。
func (h *messageHandler) send(c *gin.Context) {
	actor, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}
	var req sendMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		adminBadRequest(c, "请求体不是合法 JSON")
		return
	}
	if req.RecipientID <= 0 {
		adminBadRequest(c, "recipientId 必填且必须是正整数")
		return
	}
	if req.RecipientID == actor.ID {
		adminBadRequest(c, "不能给自己发私聊")
		return
	}

	body := strings.TrimSpace(service.StripControl(req.Body))
	if body == "" {
		adminBadRequest(c, "消息内容不能为空")
		return
	}
	if len([]rune(body)) > maxMessageBodyHTTP {
		adminBadRequest(c, "消息内容不能超过 "+strconv.Itoa(maxMessageBodyHTTP)+" 个字符")
		return
	}

	msg, err := h.store.SendDM(c.Request.Context(), actor.ID, req.RecipientID, body)
	if err != nil {
		h.writeStoreError(c, err)
		return
	}

	// 实时投递（§7.2「对方在线直投，否则只落库」）。
	//
	// **在响应之前**推：客户端拿到 201 时，对方若在线就应该已经收到了。
	// 反过来（先回 201 再异步推）会留一个"我以为发出去了、对方却还没收到"的
	// 窗口，而那个窗口在真机上表现为"消息延迟一两秒才出现"。
	// 推失败**不影响**响应：消息已经落库，对方下次拉历史仍然看得到 ——
	// 这正是"离线只落库"那条分支，不是错误。
	//
	// 用 context.WithoutCancel：请求 ctx 在响应写完后就被取消，而推送要读完
	// 连接表并逐条入队；用请求 ctx 会让"刚发完就断开"的请求推不出去。
	if h.pusher != nil {
		h.pusher.PushDM(context.WithoutCancel(c.Request.Context()), msg)
	}

	c.JSON(http.StatusCreated, toMessageView(msg))
}

type markReadRequest struct {
	PeerID int64 `json:"peerId"`
	UpTo   int64 `json:"upTo"`
}

// markRead 是 `POST /api/messages/read`（上报"读到哪一条"）。
//
// 两个字段都必填：`upTo` 缺省**不能**当作"全部已读"（见 store.MarkDMRead 的说明）。
// 响应回带本次真正被标记的行数 —— 前端据此判断要不要刷新未读徽标，
// 而不是靠"我以为我标记了"。
func (h *messageHandler) markRead(c *gin.Context) {
	actor, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}
	var req markReadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		adminBadRequest(c, "请求体不是合法 JSON")
		return
	}
	if req.PeerID <= 0 {
		adminBadRequest(c, "peerId 必填且必须是正整数")
		return
	}
	if req.UpTo <= 0 {
		adminBadRequest(c, "upTo 必填且必须是正整数（缺省不代表全部已读）")
		return
	}

	updated, err := h.store.MarkDMRead(c.Request.Context(), actor.ID, req.PeerID, req.UpTo)
	if err != nil {
		h.writeStoreError(c, err)
		return
	}

	// 已读回执推给**发送方**（actor 是读的人，peer 是发的人）。
	//
	// 只在真的标了行时才推：`updated == 0` 意味着这条上报是重复的（幂等 no-op），
	// 再推一次回执只会让对方的界面上"已读"闪一下又不变。
	// 与 PushDM 同一条理由用 WithoutCancel。
	if h.pusher != nil && updated > 0 {
		h.pusher.PushDMRead(context.WithoutCancel(c.Request.Context()), actor.ID, req.PeerID, req.UpTo, time.Now())
	}

	c.JSON(http.StatusOK, gin.H{"updated": updated})
}

// writeStoreError 把私聊域的 store 语义错误翻成 HTTP 状态。
//
// 三个映射各自的理由：
//   - ErrMessageSelfTarget → 400：参数错了（不该发给自己的这一对），用户可以改。
//   - ErrMessageBlocked → 403 + MESSAGE_BLOCKED：**服务端强制**的结果。
//     用 403 而不是 404：这里不存在"存在性探测"问题（用户知道自己在给谁发），
//     而 403 能让前端说清"你们之间存在拉黑关系"。
//   - ErrNotFound（收件人不存在）→ 404：与 #33 的"统一 404"口径一致。
func (h *messageHandler) writeStoreError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, store.ErrMessageSelfTarget):
		adminBadRequest(c, "不能给自己发私聊")
	case errors.Is(err, store.ErrMessageBlocked):
		c.JSON(http.StatusForbidden, gin.H{"error": "你们之间存在拉黑关系，无法发送私聊", "code": CodeMessageBlocked})
	case errors.Is(err, store.ErrMessageNotFriends):
		c.JSON(http.StatusForbidden, gin.H{"error": "只能给好友发私聊", "code": CodeMessageNotFriends})
	case errors.Is(err, store.ErrMessageNotFound), errors.Is(err, store.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "目标用户不存在", "code": codeNotFound})
	default:
		messageInternal(c, "私聊操作失败", err)
	}
}

// messageInternal 写出 500，底层原因只进服务端日志（可能带 SQL 片段）。
func messageInternal(c *gin.Context, what string, err error) {
	log.Printf("[ERROR] %s: %v", what, err)
	c.JSON(http.StatusInternalServerError, gin.H{"error": "服务端内部错误", "code": model.CodeInternalError})
}

// parsePositiveQuery 解析查询串里的正整数参数。
//
// required=true 时空值 → 400（"缺这个参数就没法继续"）；false 时空值 → 0
// （表示"没有这个约束"，见 before 的语义）。非整数或 <=0 一律 400 ——
// 静默当成 0 会让"传错了参数"看起来像"没有数据"。
func parsePositiveQuery(c *gin.Context, name string, required bool) (int64, bool) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		if required {
			adminBadRequest(c, name+" 必填")
			return 0, false
		}
		return 0, true
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v <= 0 {
		adminBadRequest(c, name+" 必须是正整数")
		return 0, false
	}
	return v, true
}

// 断言：本文件依赖的 store 方法在编译期真的存在（防止将来重命名后静默失配）。
var _ = func(ctx context.Context) {
	var s *store.Store
	_, _, _ = s.ListDM(ctx, 0, 0, 0, 0)
	_, _ = s.ListDMConversations(ctx, 0)
	_, _ = s.CountUnreadDM(ctx, 0)
	_, _ = s.MarkDMRead(ctx, 0, 0, 0)
	_, _ = s.SendDM(ctx, 0, 0, "")
}
