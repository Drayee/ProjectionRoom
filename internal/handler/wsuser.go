// 用户级实时通道 `/ws/user`（wayfinder #31，ACCOUNTS §7.2 / §10）。
//
// # 它与 `/ws`（房间信令）的四点差别
//
//  1. **票据必填**。房间通道有游客路径（观众免登录进房），而"我这个账号的私聊
//     与好友事件"没有任何匿名形态。因此这里**不带 ticket 直接 401**，不复用
//     `/ws` 的"没带票据就是游客"分支。
//  2. **不属于任何房间**。连接只登记进 UserHub（userId → 连接），
//     不参与房间成员/信令/容量。roomID 恒为空。
//  3. **上行只有 ping**。这条通道的**写操作走 REST**（发消息、已读上报、
//     加好友都是 HTTP 端点）。为什么不在 WS 上开一条"发消息"的上行：
//     那会让同一件事有两条路径（REST 与 WS），而它们的校验/限速/幂等要各写一遍
//     且必须逐字一致。WS 在这里只承担**下行推送**，REST 是唯一的写入口。
//  4. **允许同一账号多条连接**（手机 + 电脑 + 多标签页），投递到全部连接。
//
// # 为什么它必须自己再走一遍 Origin 校验
//
// 与 `/ws` 同一把尺子（S-5）：Origin 白名单是唯一判据，绝不接受
// "Origin 的 host 等于请求 Host"（那是可伪造的）。两条通道各做一次而不是抽公共
// 中间件：中间件要读 query/写不同状态码，抽出来的收益小于它引入的间接层。
package handler

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/model"
	"ProjectionRoom/internal/service"
	"ProjectionRoom/internal/service/limiter"
)

// userWSSeq 给"客户端没传 clientId"的连接生成唯一 id。
//
// 用进程内自增而不是随机串：日志里 `u42-3` 一眼能看出是哪个账号的第几条连接，
// 而随机串要把日志和请求对起来才能定位。唯一性由 UserHub 的 byClient 兜底
// （撞了会返回 ErrUserClientTaken，客户端换一个重连）。
var userWSSeq atomic.Uint64

// registerUserWSRoute 注册 `/ws/user`。
//
// 装配约定与其它域一致：账号能力关闭（DSN 为空）时不注册；UserHub 缺失时
// 注册一组"握手即 503"的处理——**不是**不注册，因为"404"会让客户端以为
// 这条通道不存在（那是另一件事：能力未上线 vs 装配缺失）。
func registerUserWSRoute(r *gin.Engine, cfg *config.Config, hub *service.UserHub, accountDeps AuthDeps, presence PresenceBroadcaster) {
	if r == nil || cfg == nil {
		return
	}
	if strings.TrimSpace(cfg.Auth.DBDSN) == "" {
		return // 账号能力关闭：私聊/好友都不存在，这条通道也没有意义
	}
	h := &userWSHandler{cfg: cfg, hub: hub, deps: accountDeps, presence: presence}
	r.GET("/ws/user", h.handle)
}

// PresenceBroadcaster 是"告诉我的好友我上线/下线了"的出口。
//
// 为什么做成接口而不是直接在 handler 里查好友表：它需要 store（好友列表）
// 与 UserHub（谁在线）两样东西，而这两样在不同层。用窄接口让装配层决定
// 谁来组合它们，handler 只负责"连上了/断开了"这两个时刻。
//
// nil 表示不做 presence 广播（降级形态：私聊照常，只是看不到好友在不在线）。
type PresenceBroadcaster interface {
	// FriendIDs 返回某账号的好友 id 列表（accepted）。
	FriendIDs(ctx context.Context, userID int64) ([]int64, error)
	// PushPresence 把"userID 上线/下线"推给他的好友们。
	PushPresence(ctx context.Context, userID int64, online bool)
}

type userWSHandler struct {
	cfg      *config.Config
	hub      *service.UserHub
	deps     AuthDeps
	presence PresenceBroadcaster
}

// handle 是 /ws/user 的唯一入口：`/ws/user?ticket=..[&clientId=..]`。
func (h *userWSHandler) handle(c *gin.Context) {
	// ① 装配缺失：503 而不是 404（"服务端没装好"与"这个能力不存在"是两件事）。
	if h.hub == nil || h.deps.Service == nil || h.deps.Tickets == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "实时通道暂时不可用（服务端未完成装配）",
			"code":  CodeAuthUnavailable,
		})
		return
	}

	// ② 票据**必填**（与 /ws 的游客路径刻意不同，见文件头第 1 条）。
	// 用 Query().Has 区分"没带"与"带了空串"：两者都 401，但日志要能分辨
	// ——"忘了带"是客户端 bug，"带了空"通常是取票失败。
	if !c.Request.URL.Query().Has("ticket") {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "用户通道需要票据（请先调用 /api/auth/ws-ticket 取票）",
			"code":  CodeWSTicketInvalid,
		})
		return
	}

	// ③ Origin：与 /ws 同一把尺子（S-5）。
	if err := checkOrigin(c.Request, h.cfg.Signal.AllowedOrigins); err != nil {
		log.Printf("wsuser: 来源被拒 origin=%q host=%q: %v",
			c.GetHeader("Origin"), c.Request.Host, err)
		c.JSON(http.StatusForbidden, gin.H{
			"error": "来源不被允许（请把页面所在域名加入 PR_ALLOWED_ORIGINS）",
			"code":  model.CodeBadRequest,
		})
		return
	}

	// ④ 消费票据换身份。复用 /ws 的那套判据（不存在/过期/用过 → 401；
	// 账号已删 → 401；查库出错 → 503，不确定时一律不放行）。
	ident, ok := resolveWSTicket(c, h.deps)
	if !ok {
		return
	}
	if ident == nil {
		// 理论上不可达（上面已保证 ticket 存在），但 fail closed：
		// 一条没有身份的连接进了这张表就永远查不出是谁。
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "用户通道需要有效票据",
			"code":  CodeWSTicketInvalid,
		})
		return
	}

	conn, err := websocket.Accept(c.Writer, c.Request, &websocket.AcceptOptions{
		// 库的 Origin 校验已被上面的白名单取代（它的同源判据可被伪造 Host 满足）。
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		log.Printf("wsuser: 升级失败 user=%d: %v", ident.userID, err)
		return
	}
	conn.SetReadLimit(h.cfg.Signal.MaxMessageBytes)

	// ⑤ 封禁必须在**升级之后**用 1008 拒掉（同 /ws 的理由：浏览器读不到
	// 失败握手的响应体，只有 onclose(code, reason) 能表达"不要重连"）。
	if ident.bannedReason != "" {
		writePreAuthError(h.cfg, conn, model.ErrorEnvelope(CodeUserBanned, "该账号已被封禁"))
		_ = conn.Close(websocket.StatusPolicyViolation, ident.bannedReason)
		return
	}

	clientID := strings.TrimSpace(c.Query("clientId"))
	if clientID == "" {
		clientID = userClientIDFor(ident.userID)
	}

	client, err := h.hub.Register(clientID, ident.userID, conn)
	if err != nil {
		writePreAuthError(h.cfg, conn, model.ErrorEnvelope(model.CodeClientIDTaken, "该 clientId 已有活跃的用户通道连接，请换一个重试"))
		log.Printf("wsuser: client=%s user=%d 被拒绝: %v", clientID, ident.userID, err)
		_ = conn.Close(websocket.StatusPolicyViolation, "clientId 已存在活跃连接")
		return
	}

	// ⑥ 第一帧：通道就绪确认。客户端据此知道"可以开始依赖这条通道"，
	// 而不是靠"连上了但一直没帧"去猜（见 model.TypeUserReady 的说明）。
	_ = client.Send(model.MustEnvelope(model.Envelope{
		Type:             model.TypeUserReady,
		UserChannelReady: true,
	}))

	// ⑦ 告诉我的好友"我上线了"。失败不影响通道本身（presence 是体验增量）。
	if h.presence != nil {
		go h.presence.PushPresence(context.Background(), ident.userID, true)
	}

	log.Printf("wsuser: user=%d 已连接（client=%s）", ident.userID, clientID)

	defer func() {
		userID, wentOffline := h.hub.Unregister(clientID)
		log.Printf("wsuser: user=%d 已断开（client=%s）", userID, clientID)
		if wentOffline && h.presence != nil {
			// 用独立的 ctx：请求的 ctx 在断开时已经被取消，
			// 用它查好友表会立刻失败（同 store.Writer 的那条理由）。
			go h.presence.PushPresence(context.Background(), userID, false)
		}
	}()

	// ⑧ 读循环：这条通道**只接受 ping 帧**，其余一律忽略。
	//
	// 为什么仍然要读：不读的话 TCP 缓冲会积压，而且 ping/pong 与关闭握手
	// 都需要读循环在跑。但业务上行一律拒绝——写操作走 REST（见文件头第 3 条），
	// 在这里接受"发消息"会让同一件事有两条路径且校验要各写一遍。
	quota := newUserWSQuota(h.cfg)
	ctx := c.Request.Context()
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			log.Printf("wsuser: user=%d 读循环结束: %v（关闭码=%v）",
				ident.userID, err, websocket.CloseStatus(err))
			return
		}
		if typ != websocket.MessageBinary {
			continue
		}
		// 速率配额仍然要有：即便只收控制帧，一条连接也不该能无限灌字节
		//（否则它就是一个免费的 CPU 消耗点）。
		if !quota.Allow(len(data)) {
			log.Printf("wsuser: user=%d 超出用户通道速率配额，以 1008 关闭", ident.userID)
			_ = conn.Close(websocket.StatusPolicyViolation, "用户通道速率超限")
			return
		}
		// 业务上行**不处理**，但也不当成错误：客户端可能复用了同一个
		// "读一帧就回一帧"的循环。静默丢弃比断开更宽容，而这条通道的
		// 上行本来就没有语义（写操作走 REST）。
	}
}

// userClientIDFor 生成缺省 clientId（`u<userId>-<seq>`，日志可读）。
func userClientIDFor(userID int64) string {
	return "u" + strconv.FormatInt(userID, 10) + "-" + strconv.FormatUint(userWSSeq.Add(1), 10)
}

// newUserWSQuota 构造每连接的字节速率配额。
//
// 直接复用 `/ws` 用的 `limiter.NewBucket`，理由：那条闸门的语义（令牌桶 +
// 桶深兜住单帧大报文）已经被房间通道验证过，而"每条连接能灌多少字节"
// 在两条通道上没有理由不同。自造一个按秒窗口只会多一份要各自维护的实现。
func newUserWSQuota(cfg *config.Config) *limiter.Bucket {
	return limiter.NewBucket(cfg.Signal.MaxRateBytesPerSec, cfg.Signal.RateBucketBytes)
}
