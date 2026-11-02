package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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

// 关注边端点（wayfinder #56）。ADR 0001 的 HTTP 层：
// 关注是**单向、免同意**的边，互关即好友。
//
// 三个端点：
//
//	POST   /api/follows            {userId}   → 201 followView（关注）
//	DELETE /api/follows/:userId              → 204（取关）
//	GET    /api/follows/:userId              → 200 followView（我与 TA 的关系）
//
// # 为什么这三个端点都要登录
//
// "我有没有关注 TA"是**私有**事实：关注数与粉丝数是公开的（纯数字），
// 但"关注"这个动作本身不是公开信息（关注流是另一个票的事，它另有判据）。
// 免登录的关系查询等于给爬虫一个"谁在关注谁"的枚举面 —— 而那张表里
// 恰好含有"谁关注了谁"这种最有价值的社交图谱。
//
// # 为什么取关是 DELETE /api/follows/:userId 而不是 /api/follows/:id
//
// 关注边的主键是自增 id，但**调用方永远不该知道它**。
// "取消我对 TA 的关注"这句话里唯一需要的信息就是"TA 是谁"。
// 用 userId 做路径参数，接口就与 follows 表的自增主键**解耦** ——
// 将来表结构变了（分区、复合主键）而 URL 不变。

// 关注域的错误码。
const (
	// CodeFollowSelfTarget 表示"不能关注自己"。
	//
	// 单独一个码（而不是复用 BAD_REQUEST）：前端要给出**不同的文案**与不同的下一步
	//（"你关注的是你自己" vs "参数不合法"），而它们共用一个 400 但不是一件事。
	CodeFollowSelfTarget = "FOLLOW_SELF_TARGET"
	// CodeFollowBlocked 表示"你们之间存在拉黑关系，无法建立关注边"。
	//
	// 403 而不是 404：拉黑是**公开给对方**的规则（对方界面也会显示"已拉黑"），
	// 所以这里不必藏着。方向不区分（与私聊的 BLOCKED 同一条口径）。
	CodeFollowBlocked = "FOLLOW_BLOCKED"
)

// followView 是本域的响应形状。
//
// 三个关系布尔 + 互关 + 可私聊 + 两个计数，一次给全：
// 前端拿到就能画出整行（按钮显示什么、能不能点、数字是多少），
// 不必再打一次 `/api/users/:id` 或猜"我是不是已经关注了 TA"。
//
// 互关与可私聊**由服务端算好**（铁律 I3）：前端拿不到边的事实，
// 也就不该由前端推导"能不能发消息"。
type followView struct {
	// UserID 是**对方**（被关注的人）。
	UserID int64 `json:"userId"`
	// DisplayName 是对方的显示名（免去前端再查一次档案）。
	DisplayName string `json:"displayName"`
	// Following 是"我关注了对方"。
	Following bool `json:"following"`
	// FollowedBy 是"对方关注了我"（= 我是对方的粉丝）。
	FollowedBy bool `json:"followedBy"`
	// Mutual 是"互关"（= 好友，ADR 0001）。
	Mutual bool `json:"mutual"`
	// CanDM 是"我能给对方发私聊"（= mutual ∧ ¬blocked_either）。
	CanDM bool `json:"canDm"`
	// FollowerCount 是对方的粉丝数。
	FollowerCount int64 `json:"followerCount"`
	// FollowingCount 是对方的关注数。
	FollowingCount int64 `json:"followingCount"`
}

// followListEntryView 是名单里的一行（票 #58）。
//
// 与 followView 的区别：followView 说的是"我与某一个**已知**的人是什么关系"，
// 这里说的是"这批人分别与我是什么关系" —— 没有计数（名单页不显示计数，
// 那是档案页的事），但必须带 username：搜索结果与名单行都要能回显账号名，
// 否则两个同昵称的人在一列里无法区分。
//
// **刻意不含拉黑方向**：名单只列"可见的人"，被拉黑的人根本不出现在这里
// （store 层的可见性过滤已经滤掉），所以"是否拉黑"这个字段在名单里没有消费者。
// 多带一个字段就等于多一个能被子系统读错的信号（#39 的搜索结果才有 blocked，
// 因为搜索是解除拉黑的唯一入口）。
type followListEntryView struct {
	UserID      int64  `json:"userId"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	// Following 是"我关注了 TA"。
	Following bool `json:"following"`
	// FollowedBy 是"TA 关注了我"。
	FollowedBy bool `json:"followedBy"`
	// Mutual 是互关（= 好友）。
	Mutual bool `json:"mutual"`
	// CanDM 是"我能给 TA 发私聊"。
	CanDM bool `json:"canDm"`
}

// followTargetRequest 是 POST /api/follows 的请求体。
//
// 只收 userId：**不接受**方向参数（"我要关注 TA"的方向由登录身份决定，
// 让客户端传"我是谁"等于让客户端声明身份 —— 那是铁律 I3 的反面）。
type followTargetRequest struct {
	UserID int64 `json:"userId"`
}

// registerFollowRoutes 注册关注路由。
//
// 装配约定与其它域同形：账号能力关闭时不注册；Store 缺失时注册一组 503
// （而不是让路由凭空消失 —— 那会让"页面 404"与"后端没上线"变成同一件事）。
func registerFollowRoutes(api *gin.RouterGroup, st *store.Store, cfg *config.Config, auth AccountService, pusher FollowPusher, seen AsyncJobSink) {
	if api == nil {
		return
	}
	if cfg == nil || strings.TrimSpace(cfg.Auth.DBDSN) == "" {
		return
	}
	if st == nil {
		unavailable := func(c *gin.Context) {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "关注能力暂不可用",
				"code":  CodeAuthUnavailable,
			})
		}
		api.POST("/follows", unavailable)
		api.DELETE("/follows/:userId", unavailable)
		api.GET("/follows", unavailable)
		api.GET("/follows/:userId", unavailable)
		api.PUT("/me/followers-seen", unavailable)
		return
	}

	h := &followHandler{store: st, pusher: pusher, seen: seen}

	// 读端点（查关系）走只读桶（PR_ROOM_META_*，60/min）：
	// 它高频、幂等、纯读 —— 与好友搜索同一个档位（#39 的分档纪律）。
	//
	// 写端点（关注 / 取关）走登录桶（10/min）：它们改变状态，
	// 且"关注"能被用来骚扰（对着某人反复关注/取关）。
	readLimiter := newKeyedLimiter(cfg.IPC.RoomMetaPerMinute, cfg.IPC.RoomMetaBurst, "关注查询")
	writeLimiter := newKeyedLimiter(cfg.IPC.LoginPerMinute, cfg.IPC.LoginBurst, "关注变更")

	authed := api.Group("", RequireAuth(auth))
	authed.POST("/follows", writeLimiter, h.create)
	authed.DELETE("/follows/:userId", writeLimiter, h.remove)
	// 名单（票 #58）注册在 `/follows` 上，与 `/follows/:userId` 并存。
	//
	// gin 1.12 的路由树对"静态段 + 参数段"同层并存是允许的（静态优先匹配），
	// 所以 `/api/follows?tab=mutual` 与 `/api/follows/42` 不会互相吞掉。
	// 它同样走只读桶：名单是幂等纯读，与关系查询同一档位。
	authed.GET("/follows", readLimiter, h.list)
	authed.GET("/follows/:userId", readLimiter, h.state)
	// 粉丝水位线（票 #67）：`PUT /api/me/followers-seen`。
	//
	// 挂在 `/api/me` 前缀下而不是 `/api/follows/seen`：它写的是**我自己的**一列
	// （users.followers_seen_at），与 `PATCH /api/me`、`PUT /api/me/password` 同族 ——
	// "改我自己的账号内部字段"这一族不该散落在各个业务前缀里。
	//
	// 走只读桶（不是登录桶）：它幂等、只写一行、没有滥用面（谁也不会
	// 靠反复标记水位线骚扰别人）。登录桶是给"能被用来骚扰"的写动作留的。
	authed.PUT("/me/followers-seen", readLimiter, h.markSeen)
}

// followHandler 是关注域的处理器。
//
// 它只依赖 **store**（不依赖 service）：关注的事实全在 store 层
// （边、谓词、计数），而 HTTP 层只做"取 actor、解析参数、翻译错误"。
// 这与 friendships.go 同形（那边多一个 pusher 是因为好友请求要实时通知，
// 而关注的实时通知是 #53/#67 的活 —— 本票不做，也不预埋）。
type followHandler struct {
	store *store.Store
	// pusher 把关注事件推给对方（票 #67 / 决议 #53 §5）。
	//
	// nil = 不做实时投递（§7.2 的"否则只落库"），不是错误状态 ——
	// 与 FriendPusher/MessagePusher 同一条约定。
	pusher FollowPusher
	// seen 是"粉丝水位线"写入的**异步**出口（票 #67 / 铁律 I4）。
	//
	// 为什么水位线必须异步：它是"我打开了粉丝 tab"这个动作的副作用，
	// 而那个动作的主语是 GET 列表。在 GET 路径上同步写库，等于让一次
	// 纯读请求承担一次数据库往返 —— I4 明确禁止的形态。
	// 队列满/写入器缺失时只记日志：红点这次不清，下次进 tab 再清，
	// 没有任何数据因此不一致（水位线是幂等的"推到 now"）。
	seen AsyncJobSink
}

// AsyncJobSink 是"把一次写库投进事件单写协程"的窄出口（铁律 I4）。
//
// 与 handler.AdminAuditSink 的签名逐字相同，但**语义不同**：那个是审计，
// 这个是业务状态。之所以不直接复用那个名字：读代码的人看到"AuditSink"
// 会以为写进去的是审计行，而这里写的是 users.followers_seen_at。
//
// 生产装配里两者指向同一个 *service.WriterJobs —— 单写协程本来就只有一个。
type AsyncJobSink interface {
	SubmitJob(fn func(ctx context.Context) error) bool
}

// FollowPusher 是"把关注事件推给对方"的窄出口（票 #67）。
//
// 与 FriendPusher 分开而不是复用：两者服务不同端点（关注 vs 拉黑），
// 合并会让"建一条关注边"的路径也拿到"推拉黑"的能力。
//
// 两个方法各自的收件人是写死的（由名字表达），因为"推给谁"是这里
// 最容易搞错的地方：
//
//	PushFollowCreated  推给被关注方（有人关注了你）
//	PushMutualFollow   推给双方（你们互关了）
type FollowPusher interface {
	PushFollowCreated(ctx context.Context, followerID, followeeID int64) bool
	PushMutualFollow(ctx context.Context, userA, userB int64) bool
}

// create 是 POST /api/follows（关注）。
//
// 状态码用 **201**（不是 200）：关注确实**创建**了一条边，
// 而"取关"是 204（删掉一条边）。两者语义不同，客户端的缓存/重试策略也不同。
//
// 幂等：接受 Idempotency-Key 头（与注册域同一套）。
// 同一个键 + 同一份请求体 → 原样重放首次的响应；同键不同体 → 409。
//
// 两条守卫的顺序是有意的：
//
//  1. **先查对方是否存在且可见**（不存在 / 被封禁 → 404）：
//     这与公开档案同口径 —— 能区分"不存在"与"被封禁"就是一条存在性探测通道。
//  2. **再建边**（store 内部裁决自关注与拉黑）。
//
// 为什么顺序是"先查后建"：拉黑存续期与"对方不存在"是**两件不同的事**，
// 而对客户端来说前者是"暂时不行"、后者是"没有这个人"。
func (h *followHandler) create(c *gin.Context) {
	actor, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}

	h.withIdempotency(c, scopeFollowCreate, func() (int, any, error) {
		var req followTargetRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			return 0, nil, errBadJSON
		}
		if req.UserID <= 0 {
			return 0, nil, errFollowBadTarget
		}
		if req.UserID == actor.ID {
			return 0, nil, store.ErrFollowSelfTarget
		}

		// 对方必须存在且可见（不存在与被封禁同口径 → 404）。
		peer, err := h.store.UserByID(c.Request.Context(), req.UserID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return 0, nil, store.ErrFollowTargetNotFound
			}
			return 0, nil, err
		}
		if peer.Status != store.StatusActive {
			return 0, nil, store.ErrFollowTargetNotFound
		}

		// 建边（幂等；自关注与拉黑在 store 层裁决）。
		_, created, err := h.store.CreateFollow(c.Request.Context(), actor.ID, req.UserID)
		if err != nil {
			return 0, nil, err
		}

		view, err := h.viewOf(c, actor.ID, peer)
		if err != nil {
			return 0, nil, err
		}

		// 实时提示（票 #67 / 决议 #53 §5）：**只在真的新建了边时推**。
		//
		// 为什么必须判 created：这个端点是幂等的，用户双击「关注」会走两次
		// 这里 —— 第二次没有新边，却会再推一帧「有人关注了你」。
		// 那让提示变成"点得越多、对方被通知越多"，而这件事的语义是"发生了一次"。
		//
		// 互关时**只推 mutual-follow**（不叠加一条 follow-created）：
		// 对收件人来说这两件事是同一时刻发生的，推两帧会让界面上叠两条提示，
		// 而"你们互关了"已经完全包含了"有人关注了你"。
		if created && h.pusher != nil {
			ctx := c.Request.Context()
			if view.Mutual {
				h.pusher.PushMutualFollow(ctx, actor.ID, req.UserID)
			} else {
				h.pusher.PushFollowCreated(ctx, actor.ID, req.UserID)
			}
		}
		return http.StatusCreated, view, nil
	})
}

// remove 是 DELETE /api/follows/:userId（取关）。
//
// 204：取关没有"结果体"要说 —— 它就是"这条边没了"。
//
// 幂等：重复取关是 no-op（204），不是 404。理由与 store 层一致：
// 取关按钮可能被连点，而"我已经不关注 TA 了"不是错误。
func (h *followHandler) remove(c *gin.Context) {
	actor, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}
	targetID, ok := followTargetParam(c, "userId")
	if !ok {
		return
	}
	if targetID == actor.ID {
		// 自关注从来没成立过，所以"取消"也不成立 —— 但仍给 400 而不是 404：
		// 路径参数本身是合法的，非法的是**对象**。
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "不能关注自己",
			"code":  CodeFollowSelfTarget,
		})
		return
	}

	if _, err := h.store.RemoveFollow(c.Request.Context(), actor.ID, targetID); err != nil {
		h.writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// state 是 GET /api/follows/:userId（我与 TA 的关系 + TA 的两个计数）。
//
// 免登录**不行**（见文件头第 2 条）：关系是私有事实。
//
// 对方不存在或被封禁 → 404（与公开档案同口径）。
func (h *followHandler) state(c *gin.Context) {
	actor, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}
	targetID, ok := followTargetParam(c, "userId")
	if !ok {
		return
	}

	peer, err := h.store.UserByID(c.Request.Context(), targetID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在", "code": codeNotFound})
			return
		}
		friendInternal(c, "查询用户失败", err)
		return
	}
	if peer.Status != store.StatusActive {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在", "code": codeNotFound})
		return
	}

	view, err := h.viewOf(c, actor.ID, peer)
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

// list 是 `GET /api/follows?tab=mutual|following|followers`（票 #58）。
//
// 好友页的三张名单（好友=互关 / 关注 / 粉丝）都从这里取数。
//
// 三条设计：
//
//  1. **tab 必填且大小写敏感**：缺失 / 空 / 未知值一律 400，不做"默认返回互关"。
//     默认值属于**界面**（`utils/friendsRoute.ts` 的 `FRIENDS_DEFAULT_TAB`），
//     接口猜一个的后果是"界面显示了另一份名单"这种最难复现的错。
//  2. **匿名 401**：名单是私有事实（与 `GET /follows/:userId` 同一条理由）。
//  3. **空名单回 `[]` 不回 `null`**：`items` 是切片且用 `make(...,0,n)` 造，
//     所以"新用户没有任何关注"这条最常见的首次路径不会让前端 `v-for` 炸掉。
//
// 可见性（被拉黑的双向隐藏、被封禁的隐藏）**全部在 store 层**（`ListFollowList`）：
// 这里再写一份过滤就是给同一个规则造第二个 owner。
func (h *followHandler) list(c *gin.Context) {
	actor, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}

	tab := store.FollowListTab(strings.TrimSpace(c.Query("tab")))
	entries, err := h.store.ListFollowList(c.Request.Context(), actor.ID, tab)
	if err != nil {
		if errors.Is(err, store.ErrFollowListBadTab) {
			adminBadRequest(c, "tab 必须是 mutual / following / followers")
			return
		}
		friendInternal(c, "查询关注名单失败", err)
		return
	}

	items := make([]followListEntryView, 0, len(entries))
	for _, e := range entries {
		items = append(items, followListEntryView{
			UserID:      e.UserID,
			Username:    e.Username,
			DisplayName: e.DisplayName,
			Following:   e.Following,
			FollowedBy:  e.FollowedBy,
			Mutual:      e.Mutual(),
			CanDM:       e.CanDM(),
		})
	}

	// 粉丝红点（票 #67 / 决议 #53 §9）：**一个端点两用**。
	//
	// 为什么只有 `tab=followers` 带这个字段：红点的语义是"有人关注你而你没回关"，
	// 只有粉丝这份名单与它对应。互关/关注两份名单带上它会让前端拿到一个
	// 与当前视图无关的数字 —— 而"这个数字属于哪份名单"恰恰是最容易读错的地方。
	//
	// 为什么不另开一个 `GET /api/me/followers-count`：那会让"进粉丝 tab"
	// 变成两次往返（列表 + 计数），而它们必须一致（红点亮着、列表却看不到新人
	// 是最糟的形态）。一个端点一次查询天然一致。
	if tab == store.FollowListFollowers {
		n, err := h.store.CountNewFollowers(c.Request.Context(), actor.ID)
		if err != nil {
			friendInternal(c, "统计新粉丝失败", err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": items, "newCount": n})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// markSeen 是 PUT /api/me/followers-seen（票 #67）：把粉丝水位线推到"现在"。
//
// # 为什么是 PUT 而不是 POST
//
// PUT 的语义是"把资源置成这个状态"，而这里正是"把 followers_seen_at 置成 now"——
// 幂等，重复调用不产生额外效果。POST 会暗示"每次调用都创建一件事"，
// 而水位线只有一条。
//
// # 为什么时间由服务端取
//
// 客户端传时间会让"红点什么时候灭"取决于那台机器的时钟。跨时区/时钟漂移的
// 用户会看到红点永远不灭（客户端时间永远落后）或立刻灭掉（永远超前）。
// 水位线是一个**服务端事实**，与 created_at 同一个时钟源。
//
// # 为什么 DB 写走异步通道（铁律 I4）
//
// 这是"打开粉丝列表"的副作用，而那个动作的主语是一次 GET。在 GET 路径上
// 同步写库就是 I4 明令禁止的形态。投递失败只记日志：红点这次不清，
// 下次进 tab 再清 —— 水位线是"推到 now"，重放天然安全。
func (h *followHandler) markSeen(c *gin.Context) {
	actor, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}

	now := time.Now()
	if h.seen != nil {
		if !h.seen.SubmitJob(func(ctx context.Context) error {
			return h.store.MarkFollowersSeen(ctx, actor.ID, now)
		}) {
			log.Printf("[WARN] 粉丝水位线入队失败（user=%d）：队列满或写入器已关闭，下次进粉丝列表再清", actor.ID)
		}
	} else {
		// 没有写队列（窄实例/测试装配）：直接写。这里**不是**绕过 I4 ——
		// 装配里没有单写协程时，"异步"没有可去的地方，而这个端点本身
		// 不是高频热路径（一个人打开粉丝 tab 的频率）。
		if err := h.store.MarkFollowersSeen(c.Request.Context(), actor.ID, now); err != nil {
			friendInternal(c, "更新粉丝水位线失败", err)
			return
		}
	}

	// 回 204 而不是回新的计数：调用方刚刚"看过了"，它不需要知道
	// "清掉了几条" —— 那个数字只会让人以为还有事没做完。
	c.Status(http.StatusNoContent)
}

// viewOf 组装响应视图（关系 + 互关 + 可私聊 + 对方的两个计数）。
//
// 可私聊这一项**必须**用 store 的 can_dm 谓词（唯一实现，见 store.CanDM）：
// 它把"互关"与"未被拉黑"在同一条 SQL 里算掉。
// 在这里另写一份"互关且没拉黑"的判断，就是给同一个谓词造第二个 owner ——
// 而那种重复迟早漂移（票 42 决议的核心就是"互关是派生的，不是状态的"）。
func (h *followHandler) viewOf(c *gin.Context, actorID int64, peer *store.User) (followView, error) {
	st, err := h.store.FollowStateBetween(c.Request.Context(), actorID, peer.ID)
	if err != nil {
		return followView{}, err
	}
	canDM, err := h.store.CanDM(c.Request.Context(), actorID, peer.ID)
	if err != nil {
		return followView{}, err
	}
	return followView{
		UserID:         peer.ID,
		DisplayName:    peer.DisplayName,
		Following:      st.Following,
		FollowedBy:     st.FollowedBy,
		Mutual:         st.Mutual(),
		CanDM:          canDM,
		FollowerCount:  st.FollowerCount,
		FollowingCount: st.FollowingCount,
	}, nil
}

// —— 幂等键 ——

const (
	// scopeFollowCreate 是关注端点的幂等键 scope。
	//
	// scope 区分端点：同一串 UUID 撞到另一个端点不应该算命中
	// （与注册域的 register.start / register.email 同一条理由）。
	scopeFollowCreate = "follow.create"
)

// withIdempotency 是写操作的重放保护（与 auth.go 的同名壳同一条纪律）。
//
// 流程：有键 → Reserve（已完成则原样返回首次响应 / 进行中 409 / 冲突 409）
// → 执行 → 成功落键、失败释放。
//
// **只有成功才落键**：失败往往正是值得重试的那一类（网络抖、频控）。
func (h *followHandler) withIdempotency(c *gin.Context, scope string, run func() (int, any, error)) {
	key := idempotencyKeyFrom(c)
	raw, _ := io.ReadAll(c.Request.Body)
	c.Request.Body = io.NopCloser(strings.NewReader(string(raw)))

	if key == "" {
		status, body, err := run()
		if err != nil {
			h.writeError(c, err)
			return
		}
		if body == nil {
			c.Status(status)
			return
		}
		c.JSON(status, body)
		return
	}

	ctx := c.Request.Context()
	rec, err := h.store.ReserveIdempotencyKey(ctx, scope, key, hashRequestBody(raw), idempotencyTTL)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrIdempotencyKeyConflict):
			c.AbortWithStatusJSON(http.StatusConflict, gin.H{
				"error": "这个幂等键已用于另一个请求，请换一个键或检查请求内容",
				"code":  "IDEMPOTENCY_KEY_CONFLICT",
			})
			return
		case errors.Is(err, store.ErrIdempotencyKeyInFlight):
			c.AbortWithStatusJSON(http.StatusConflict, gin.H{
				"error": "同一请求正在处理中，请稍后重试",
				"code":  "IDEMPOTENCY_IN_FLIGHT",
			})
			return
		default:
			h.writeError(c, err)
			return
		}
	}
	if rec != nil {
		// 已完成过：原样返回首次的响应体与状态码。
		c.Data(rec.Status, "application/json; charset=utf-8", []byte(rec.Response))
		return
	}

	status, body, err := run()
	if err != nil {
		// 失败不落键：释放占位，让同一个键可以重试。
		_ = h.store.ReleaseIdempotencyKey(ctx, scope, key)
		h.writeError(c, err)
		return
	}

	payload := []byte("{}")
	if body != nil {
		if b, mErr := json.Marshal(body); mErr == nil {
			payload = b
		}
	}
	_ = h.store.CompleteIdempotencyKey(ctx, scope, key, status, payload)

	if body == nil {
		c.Status(status)
		return
	}
	c.JSON(status, body)
}

// followTargetParam 解析并校验路径参数 :userId。
//
// 非正整数 → 400，且提示语不含任何存在性信息（与 friendships.go 的 userIDParam 同口径）。
func followTargetParam(c *gin.Context, name string) (int64, bool) {
	raw := strings.TrimSpace(c.Param(name))
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "用户 id 不合法", "code": model.CodeBadRequest})
		return 0, false
	}
	return id, true
}

// errFollowBadTarget 是"userId 不是正整数"的哨兵。
var errFollowBadTarget = errors.New("userId 必填且必须是正整数")

// writeError 把 store 层的语义错误翻成 HTTP 状态。
//
// 逐条映射：
//
//   - ErrFollowSelfTarget → 400 + FOLLOW_SELF_TARGET（参数不合法：对象是自己）。
//   - ErrFollowTargetNotFound → 404 + NOT_FOUND（对象不存在或不可见，同口径）。
//   - ErrFollowBlocked → 403 + FOLLOW_BLOCKED（拉黑存续期，禁止新关注）。
//
// 其余落到 500。
func (h *followHandler) writeError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, store.ErrFollowSelfTarget):
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "不能关注自己",
			"code":  CodeFollowSelfTarget,
		})
	case errors.Is(err, store.ErrFollowTargetNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在", "code": codeNotFound})
	case errors.Is(err, store.ErrFollowBlocked):
		c.JSON(http.StatusForbidden, gin.H{
			"error": "你们之间存在拉黑关系，无法关注",
			"code":  CodeFollowBlocked,
		})
	case errors.Is(err, errFollowBadTarget):
		adminBadRequest(c, err.Error())
	case errors.Is(err, errBadJSON):
		adminBadRequest(c, "请求体不是合法 JSON")
	default:
		friendInternal(c, "关注操作失败", err)
	}
}

// 断言：本文件依赖的 store 方法在编译期真的存在（防止将来重命名后静默失配）。
//
// 与 messages.go 末尾那一段同一个用意：这些方法是**跨包契约**，
// 而 Go 只在接口赋值处检查它。写成一个包级变量让"签名漂移"变成编译错误，
// 而不是运行期 500。
var _ = func() any {
	var s *store.Store
	var _ = s.CreateFollow
	var _ = s.RemoveFollow
	var _ = s.FollowStateBetween
	var _ = s.CanDM
	return nil
}

// 保住 service 的引用：本文件目前不直接用 service 包（关注的事实全在 store），
// 但保留这条 import 让"将来把关注用例层加进来"的位置显眼。
var _ = service.ErrUserNotFound
