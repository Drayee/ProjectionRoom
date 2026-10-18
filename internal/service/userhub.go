package service

import (
	"context"
	"errors"
	"log"
	"strconv"
	"sync"

	"github.com/coder/websocket"

	"ProjectionRoom/internal/config"
)

// UserHub 是用户级实时通道（/ws/user）的连接注册表：userId → 该账号的若干连接。
//
// # 为什么单独一个 Hub，而不是复用房间的 Hub
//
// 房间 Hub 的索引是 `clientId → Client` 与 `roomID → 连接集合`，它的语义是
// "这条连接在哪个房间里"；而 /ws/user 的连接**不属于任何房间**（roomID 恒为空）。
// 塞进同一个 Hub 会让 RoomSize/BroadcastToRoom 这些按房间聚合的方法把用户通道
// 的连接算进去（比如"房间在线人数"多出来一个不存在的房间），或者要处处加
// `if roomID != ""` 的分支。两个 Hub 各自只回答一个问题，比一个 Hub 回答两个
// 更省事 —— 代价只是多一个 map。
//
// # 为什么允许一个账号多条连接
//
// 同一个账号可以同时开着手机和电脑（多标签页更常见）。投递语义因此是
// "**投递到该账号的全部连接**"，而不是"最后一条连接赢"。踢掉旧连接会让
// "在手机上打开消息页"把"电脑上的消息页"变成死的 —— 那不是一个用户能理解的
// 行为。代价是一条消息可能被同一账号的两个标签页各收一次，而这对客户端
// 是幂等的（按消息 id 去重即可）。
//
// # 并发模型
//
// 一把 RWMutex 保护两张表：写路径（注册/注销/上下线）都是低频的，
// 而读路径（投递）只需要 RLock 复制一份目标切片就释放 —— 绝不在持锁时做 IO。
// 这与房间 Hub 的做法一致（见 hub.go 的 SendTo）。
type UserHub struct {
	cfg config.SignalConfig

	mu sync.RWMutex
	// byUser 是 userId → (clientId → *Client)。第二层用 map 而不是 slice：
	// 注销要按 clientId 精确删除，而 slice 需要线性找 + 搬移。
	byUser map[int64]map[string]*Client
	// byClient 是 clientId → *Client，供"按连接注销"用（注销时只知道 clientId）。
	byClient map[string]*Client

	ctx    context.Context
	cancel context.CancelFunc
}

// ErrUserOffline 表示该账号当前没有在线连接。
//
// 调用方（发消息路径）**必须**把它当成正常情况：§7.2 的投递语义是
// "对方在线直投，否则只落库"。返回错误只是为了让调用方能区分两种结果，
// 不是失败。
var ErrUserOffline = errors.New("userhub: 该账号不在线")

// ErrUserClientTaken 表示同一个 clientId 已有一条活跃的用户通道连接。
//
// 与房间 Hub 的 ErrDuplicateID 同义：clientId 是连接的身份，复用会让
// 注销时删错连接。客户端收到后应换一个 clientId 重连。
var ErrUserClientTaken = errors.New("userhub: 该 clientId 已存在活跃的用户通道连接")

// NewUserHub 构造用户通道 Hub，并返回清理函数供装配层聚合。
func NewUserHub(cfg *config.Config) (*UserHub, func(), error) {
	ctx, cancel := context.WithCancel(context.Background())
	h := &UserHub{
		cfg:      cfg.Signal,
		byUser:   make(map[int64]map[string]*Client),
		byClient: make(map[string]*Client),
		ctx:      ctx,
		cancel:   cancel,
	}
	return h, h.Close, nil
}

// Close 关闭所有用户通道连接与写协程。
func (h *UserHub) Close() {
	h.cancel()

	h.mu.Lock()
	clients := make([]*Client, 0, len(h.byClient))
	for _, c := range h.byClient {
		clients = append(clients, c)
	}
	h.byClient = make(map[string]*Client)
	h.byUser = make(map[int64]map[string]*Client)
	h.mu.Unlock()

	for _, c := range clients {
		c.Close()
	}
}

// Register 注册一条用户通道连接并启动其写协程。
//
// userID 必须 > 0：这条通道**只服务已登录账号**（握手时票据是必填的，见 wsuser.go），
// 所以"游客的用户通道"不是一个合法状态 —— 让它在这里就失败，比让一条 userID=0
// 的连接潜伏在表里（永远收不到投递、也永远不会被"按账号踢下线"扫到）要好。
func (h *UserHub) Register(clientID string, userID int64, conn *websocket.Conn) (*Client, error) {
	if userID <= 0 {
		return nil, errors.New("userhub: userID 必须为正（用户通道只服务已登录账号）")
	}

	h.mu.Lock()
	if _, exists := h.byClient[clientID]; exists {
		h.mu.Unlock()
		return nil, ErrUserClientTaken
	}
	// roomID 传空串：用户通道不属于任何房间（见类型注释）。
	c := newClient(clientID, "", conn, h.cfg)
	// 账号在注册时就确定（不像房间通道那样"握手后由票据绑定"）：
	// /ws/user 的票据是**必填**的，没有游客路径，因此这里可以直接写入。
	c.SetUserID(userID)
	h.byClient[clientID] = c
	if h.byUser[userID] == nil {
		h.byUser[userID] = make(map[string]*Client)
	}
	h.byUser[userID][clientID] = c
	firstConn := len(h.byUser[userID]) == 1
	h.mu.Unlock()

	go c.writePump(h.ctx)

	// 第一条连接 = 该账号上线。这个返回值让调用方决定要不要广播 presence，
	// 而不是让 Hub 自己去查好友表（那会把好友域的知识塞进传输层）。
	if firstConn {
		log.Printf("wsuser: user=%d 上线（client=%s）", userID, clientID)
	}
	return c, nil
}

// Unregister 移除一条连接并关闭它；返回"该账号是否已无在线连接"。
//
// 返回值语义与 Register 的 firstConn 对称：调用方据此广播 presence offline。
// 它必须是**注销之后**的事实（持锁算完再返回），否则两条连接同时断开时
// 两次注销都会看到"还有一条在"，于是永远不广播下线。
func (h *UserHub) Unregister(clientID string) (userID int64, wentOffline bool) {
	h.mu.Lock()
	c, ok := h.byClient[clientID]
	if !ok {
		h.mu.Unlock()
		return 0, false
	}
	delete(h.byClient, clientID)
	userID = c.UserID()
	if conns := h.byUser[userID]; conns != nil {
		delete(conns, clientID)
		if len(conns) == 0 {
			delete(h.byUser, userID)
			wentOffline = true
		}
	}
	h.mu.Unlock()

	c.Close()
	if wentOffline {
		log.Printf("wsuser: user=%d 下线（client=%s）", userID, clientID)
	}
	return userID, wentOffline
}

// SendToUser 把一帧投递给某账号的**全部**在线连接，返回成功入队的连接数。
//
// 返回 0 时同时返回 ErrUserOffline：调用方据此知道"只落库"这条分支走了。
// 注意"成功入队"不等于"已送达"—— 队列满会被丢（见 Client.Send），
// 但那种情况下消息已经落库，对方下次拉历史仍然看得到。
func (h *UserHub) SendToUser(userID int64, msg []byte) (int, error) {
	h.mu.RLock()
	conns := h.byUser[userID]
	targets := make([]*Client, 0, len(conns))
	for _, c := range conns {
		targets = append(targets, c)
	}
	h.mu.RUnlock()

	if len(targets) == 0 {
		return 0, ErrUserOffline
	}

	sent := 0
	for _, c := range targets {
		if err := c.Send(msg); err == nil {
			sent++
		}
	}
	return sent, nil
}

// Online 回答"该账号现在有没有在线连接"。
func (h *UserHub) Online(userID int64) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.byUser[userID]) > 0
}

// OnlineIDs 返回某个候选集合里**在线**的那些 id。
//
// 它服务于 presence 的"上线时告诉我的好友我来了"：调用方先取好友列表，
// 再一次问"谁在线"，而不是对每个好友逐个 Online()（那会是 N 次 RLock）。
func (h *UserHub) OnlineIDs(candidates []int64) []int64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]int64, 0, len(candidates))
	for _, id := range candidates {
		if len(h.byUser[id]) > 0 {
			out = append(out, id)
		}
	}
	return out
}

// CloseByUser 断开某账号的全部用户通道连接，返回断开数量（封禁/会话撤销用）。
//
// 与房间 Hub 的同名方法对应：封禁必须同时切断两条通道，否则"封禁即时生效"
// 只对房间信令成立，而用户通道还能继续收发私聊。
func (h *UserHub) CloseByUser(userID int64) int {
	h.mu.Lock()
	conns := h.byUser[userID]
	targets := make([]*Client, 0, len(conns))
	ids := make([]string, 0, len(conns))
	for id, c := range conns {
		targets = append(targets, c)
		ids = append(ids, id)
	}
	for _, id := range ids {
		delete(h.byClient, id)
	}
	delete(h.byUser, userID)
	h.mu.Unlock()

	for _, c := range targets {
		// 用 1008 + 可读原因：客户端据此提示"账号已被封禁，请勿重连"，
		// 而不是无限重连（同房间通道的 bannedCloseReason 口径）。
		c.CloseWith(websocket.StatusPolicyViolation, bannedCloseReason)
	}
	return len(targets)
}

// Stats 返回用户通道的可观测指标（在线账号数、连接数）。
func (h *UserHub) Stats() (users, conns int) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.byUser), len(h.byClient)
}

// bannedCloseReason 与房间通道共用同一句文案（ws.go 的同名常量）。
//
// 为什么在这里再声明一次而不是 import handler：service 不该反向依赖 handler。
// 两处文案必须一致（客户端按字符串判断"这是封禁，不要重连"），
// 因此 handler 那边的常量注释指向这里。
const bannedCloseReason = "账号已被封禁，请勿重连"

// userClientID 给用户通道的连接生成一个稳定的 clientId（当客户端没传时用）。
//
// 为什么允许客户端不传：/ws/user 的语义是"我这个账号的通道"，连接身份对业务
// 没有意义（不像房间通道的 clientId 要参与信令路由）。但 Hub 需要按 id 注销，
// 所以缺省时生成一个。用 userID + 递增序号保证唯一且可读（日志里一眼看出是谁）。
func userClientID(userID int64, seq uint64) string {
	return "u" + strconv.FormatInt(userID, 10) + "-" + strconv.FormatUint(seq, 10)
}
