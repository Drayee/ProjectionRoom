// 公开房列表端点（ACCOUNTS §6 / §10 的 GET /api/public-rooms，二期 T2）。
//
// 本文件负责三条判据，每条都能在没有 PostgreSQL 的机器上判定：
//
//  1. **只列显式公开且当前存在的房间**：候选集合是内存实况（service.Manager.ListRooms）
//     与元数据 is_public 的**交集**。内存里没有的房间一律不出现（§6："实况取自内存"）——
//     否则列表会指向一个个按码进去只有 ROOM_NOT_FOUND 的死房间。
//  2. **响应字段白名单**：房间码 / 标题 / 房主昵称 / 在座人数 / 主播在线与否 /
//     是否密码房。**绝不**出现密码、成员明细、房主邮箱/角色/状态，也不暴露 ownerUserId
//     （内部自增 id 没有对外理由，而它是"从列表反查账号"的第一步）。
//  3. **免登录 + 按 IP 限速**：观众按码进房本来是免登录的，列表是它的入口，
//     所以它也必须免登录；而它又是唯一免登录且会查库的读端点，因此必须有限速。
//
// 元数据缺行时的**降级 + 自愈**（§2 的兼容性边界）在这里落地：照常列出
// （标题空、昵称空串），并投递一次 upsert 把行补上 —— 这补掉一期"投递失败只记日志"
// 留下的缺口：那种房间在库里永远缺行，若列表侧不自愈，它会一直缺下去。
package handler

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/model"
	"ProjectionRoom/internal/service"
	"ProjectionRoom/internal/store"
)

// 公开房列表的分页口径。
//
// 上限 50 与 store.clampPage 的 200 刻意不同：这个端点**免登录**，
// 每一次请求都对应一次内存全量快照遍历 + 一次元数据批量查询，
// 而列表页的可用形态是"一屏十几张卡片 + 加载更多"。
// 默认 20 与上限 50 之间留出翻页余量，同时把单次响应的规模钉死在几十条量级。
const (
	defaultPublicRoomPageLimit = 20
	maxPublicRoomPageLimit     = 50
)

// 官方区查询参数 official 的取值解析。
//
// 只接受 true / false / 空：与 adminPublicFilter 的 public 参数同一套口径
// （非整数/任意别的字符串一律 400，不静默当空处理）。
//
// 为什么 `official=1` 这种"看起来更自然"的写法**不**接受：#35 的票面写的是
// `?official=1`，但这里坚持 true/false 的理由与 public 参数逐字相同 ——
// 静默把非法值当"不筛选"，会让"前端把某个字符串直接拼进查询串"变成被接受
// 的行为，而它拿到的是全量公开列表，看起来像"官方区有内容"。
const officialParamTrue = "true"

// officialFilter 解析 official 查询参数。
func officialFilter(raw string) (officialOnly bool, err error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return false, nil
	case officialParamTrue:
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("official 只能是 true、false 或留空")
	}
}

// PublicRoomLister 是房间实时快照的来源（*service.Manager 满足它）。
//
// 只声明 ListRooms 一个方法：本端点**不允许**碰房间内部状态
// （*Room 上挂着密码、成员表与主播令牌），把依赖面钉在一个返回纯值类型的方法上，
// 是"响应里不会出现密码"这条判据在编译期的第一道保证。
type PublicRoomLister interface {
	ListRooms() []service.RoomSnapshot
}

// PublicRoomMetaStore 是本端点需要的元数据能力（*store.Store 满足它）。
//
// 为什么是窄接口而不是 *store.Store：本层不 import gorm（沿用一期的约定），
// 且单测要能在没有 PostgreSQL 的机器上覆盖"缺行降级 + 自愈投递"这条判据 ——
// 那需要一张可控的内存元数据表。
// RoomMetaByID 是房主改元数据（T3）与下架（T4）要的，放在同一个接口里
// 是因为它们描述的是同一件事："handler 能对 rooms_meta 做什么"。
type PublicRoomMetaStore interface {
	ListRoomMetas(ctx context.Context, ids []string) (map[string]store.RoomMeta, error)
	RoomMetaByID(ctx context.Context, roomID string) (*store.RoomMeta, error)
	UpsertRoomMeta(ctx context.Context, m *store.RoomMeta) error
	UpdateRoomMeta(ctx context.Context, roomID string, patch store.RoomMetaPatch) error
}

// PublicRoomProfileStore 是"取房主昵称"的专用出口（T2）。
//
// 为什么单独定义一个只回答昵称的接口（而不是 continue 用 service.Profile）：
// 列表页只需要一个字符串，而 Profile 里带 email / role / status。
// 把出口钉在"一个 int64 → 一个 string"上，就不存在"某天有人顺手把账号档案
// 塞进列表响应"的路径 —— 与 service.Profile 之于 PasswordHash 是同一条理由
// （类型比"记得别写"可靠）。实现见 service.ProfileNameLookup（它的存储侧查询
// 只 SELECT display_name，连 password_hash 都不会经过内存）。
type PublicRoomProfileStore interface {
	ProfileNameByID(ctx context.Context, id int64) (string, error)
}

// PublicRoomDeps 是公开房列表的全部依赖。
//
// 全部可选：缺装配时端点仍然存在（返回空列表）而不是 500 —— 列表是只读的派生视图，
// 它的降级不该变成页面上的一个错误页。
type PublicRoomDeps struct {
	Rooms PublicRoomLister
	Meta  PublicRoomMetaStore
	Names PublicRoomProfileStore
	// Sink 是元数据的异步落库出口（一期的 RoomMetaSink，§9 的事件类）。
	// 缺它时自愈投递只记一条日志（与建房路径同一条取舍）。
	Sink RoomMetaSink
}

// registerPublicRoomRoutes 注册公开房列表（**免登录**）。
//
// 为什么不像 registerAdminRoutes 那样"DSN 为空就不注册"：它不是管理端视图，
// 观众按码进房本来就免登录；账号能力关闭时它退化成"没有房主昵称的空列表"，
// 而不是 404（客户端的导航入口不该因为后端没开账号就消失）。
// 限速用 cfg.IPC.PublicRoomsPerMinute（PR_PUBLIC_ROOMS_PER_MINUTE）。
func registerPublicRoomRoutes(api *gin.RouterGroup, deps PublicRoomDeps, cfg *config.Config) {
	if api == nil {
		return
	}
	h := &publicRoomHandler{deps: deps}

	perMinute, burst := 0.0, 0
	if cfg != nil {
		perMinute, burst = cfg.IPC.PublicRoomsPerMinute, cfg.IPC.PublicRoomsBurst
	}
	api.GET("/public-rooms", newKeyedLimiter(perMinute, burst, "公开房列表"), h.list)
}

// publicRoomHandler 持有依赖，把端点做成方法。
type publicRoomHandler struct {
	deps PublicRoomDeps
}

// publicRoomItem 是响应里的**一个房间**（字段白名单，§6）。
//
// 这里的字段就是协议面：多一个字段就等于多一条对外承诺。
// 刻意**没有**的东西（逐条说明为什么）：
//
//	password / hasPasswordValue  密码与它的任何形态；
//	members / memberList         成员明细（昵称列表本身就是隐私面）；
//	email / role / status        房主的敏感档案（列表只有昵称）；
//	ownerUserId                  内部账号 id —— 它的唯一用途是从列表反查账号。
//
// IsOfficial 是**运营事实**（0014，wayfinder #35）：它由管理员设置，
// 与"这个房间现在有没有主播"这类实时状态无关。官方区的卡片据此加一个标识，
// 而不必让前端自己判断"出现在这个列表里 = 官方"（那在 `official=false`
// 的普通列表里就等于没有标识可用）。
//
// 为什么它不构成泄露：is_official 对所有人可见（官方区本身就是公开的），
// 而它**不**透露 owner、rank 位次或任何账号信息。rank 不进响应：
// 前端只需要知道"在官方区里"与"卡片顺序"（后者由响应顺序直接给出），
// 把 rank 数字发出去只会让客户端有机会按一个它无法解释的整数排序。
type publicRoomItem struct {
	RoomID      string `json:"roomId"`
	Title       string `json:"title"`
	OwnerName   string `json:"ownerName"`
	MemberCount int    `json:"memberCount"`
	HostOnline  bool   `json:"hostOnline"`
	// HostOffline 表示主播已断线、房间处于离线宽限期内（§6 要求标注"主播离线中"）。
	HostOffline bool `json:"hostOffline"`
	HasPassword bool `json:"hasPassword"`
	// IsOfficial 见类型说明。零值 false 会让普通列表的 JSON 里多一个
	// `isOfficial:false` —— 这是有意的（客户端不必判"字段在不在"）。
	IsOfficial bool `json:"isOfficial"`
}

// list 返回公开房的分页列表（成功 200，免登录）。
//
// 顺序（可判定）：
//
//	① 内存实况（ListRooms，不查库）
//	② 与元数据 is_public 求交（closed_at 非空的房间不算"公开"）
//	③ 缺行的房间降级 + 投递一次自愈 upsert
//	④ 稳定排序 → 分页切片
//
// `?official=true` 时**同一个端点**变成官方区（#35 的口径：不新建路径）：
//
//	② 的筛选换成 is_official，④ 的排序换成运营位次（rank DESC, room_id）。
//
// 两条路径共用"内存实况是候选集合"这一条不变式：官方区也**只列当前真的存在
// 的房间**。一个被标记为官方但主播早就下线、房间已被回收的房间，出现在官方区
// 里只会让人点进去看到 ROOM_NOT_FOUND —— 那比"官方区暂时空着"更糟
// （前者是坏链接，后者是可由运营立刻修复的状态）。
func (h *publicRoomHandler) list(c *gin.Context) {
	limit, offset, err := publicRoomPage(c.Query("limit"), c.Query("offset"))
	if err != nil {
		publicRoomBadRequest(c, err.Error())
		return
	}
	officialOnly, err := officialFilter(c.Query("official"))
	if err != nil {
		publicRoomBadRequest(c, err.Error())
		return
	}

	// ① 内存快照。装配缺失（没有 Manager）时按空列表处理：
	// 这个端点没有"不可用"的语义，只有"现在没有房间"的语义。
	snapshots := []service.RoomSnapshot{}
	if h.deps.Rooms != nil {
		snapshots = h.deps.Rooms.ListRooms()
	}

	// ② 元数据：只查内存里真的存在的房间码（批量，一次往返）。
	meta := map[string]store.RoomMeta{}
	var metaErr error
	if h.deps.Meta != nil && len(snapshots) > 0 {
		ids := make([]string, 0, len(snapshots))
		for _, s := range snapshots {
			ids = append(ids, s.ID)
		}
		meta, metaErr = h.deps.Meta.ListRoomMetas(c.Request.Context(), ids)
		if metaErr != nil {
			// 查库失败**不让端点整体失败**：内存实况是这份列表的主要价值
			//（哪些房间现在真的能进），元数据只贡献标题与昵称。
			// 全部房间因此按"缺行"降级（标题空、昵称空），与真的缺行同一条路径。
			log.Printf("[WARN] 公开房列表：批量查房间元数据失败，本轮按缺行降级展示：%v", metaErr)
			meta = map[string]store.RoomMeta{}
		}
	}

	public := make([]service.RoomSnapshot, 0, len(snapshots))
	for _, s := range snapshots {
		m, ok := meta[s.ID]
		if !ok {
			// 缺行的房间**照常列出**（降级：标题空、昵称空串），并自愈补行 ——
			// 这正是 §2"元数据缺行可降级 + 自愈"那条边界：内存实况是主要价值，
			// 缺一行元数据不该让一个真的能进的房间从列表里消失。
			//
			// **官方区是唯一的例外**：缺行意味着 is_official 取不到（默认 false），
			// 而把"运营从未标记过任何房间"降级成"官方区里有内容但标题是空的"
			// 比空列表更糟。因此这里按"不在官方区"处理，也不自愈
			// （自愈补出来的 is_official 必然是 false，投递了也不会改变结果）。
			if officialOnly {
				continue
			}
			h.healMissingMeta(s)
			public = append(public, s)
			continue
		}
		// 有元数据时，"公开且未关闭"（或"官方且未关闭"）才是进列表的必要条件。
		// closed_at 的判定放在这里而不是 SQL：本端点的候选集合本来就是内存实况，
		// "元数据说公开"只是第二个必要条件。
		if m.ClosedAt != nil {
			continue
		}
		// ⚠ **官方区也要 is_public**：官方标记与公开性是两个正交维度
		// （一个房间可以又官方又未公开，例如运营先标记、等开播前再放出来），
		// 但 `/api/public-rooms` 这个**免登录**端点面向的是公众 ——
		// 一个未公开的房间出现在公众可见的任何列表里，都是"下架/未公开"被绕过。
		// 未公开的官方房只在管理端（`/api/admin/rooms?official=true`）可见，
		// 那是带 RequireAdmin 的读路径。
		if !m.IsPublic {
			continue
		}
		if officialOnly && !m.IsOfficial {
			continue
		}
		public = append(public, s)
	}

	// ④ 稳定排序。
	//
	// 官方区走**运营位次**（official_rank DESC, room_id ASC），普通列表走实时性
	// （有主播优先 → 播放进度降序 → 房间码）。两套排序的分页都必须是全序，
	// 否则翻页会出现重复/漏项。
	//
	// 官方区三条为什么必须补全：
	//   1. rank 相同的行极常见（默认值全是 0），只按它排时同分组内顺序不保证稳定；
	//   2. hostOnline 依然是第一顺位 —— 官方区里"有主播的官方位"排在"还没开播的
	//      官方位"之前，与运营意图不冲突（运营把一排房间设成官方，用户先进能进的）；
	//   3. room_id 是主键级唯一键，做最后一级让整序确定。
	var meta2 map[string]store.RoomMeta
	if officialOnly {
		meta2 = meta
	}
	sort.Slice(public, func(i, j int) bool {
		a, b := public[i], public[j]
		if officialOnly {
			ma, _ := meta2[a.ID]
			mb, _ := meta2[b.ID]
			if ma.OfficialRank != mb.OfficialRank {
				return ma.OfficialRank > mb.OfficialRank
			}
		}
		if a.HostOnline != b.HostOnline {
			return a.HostOnline
		}
		if a.PlayingIndex != b.PlayingIndex {
			return a.PlayingIndex > b.PlayingIndex
		}
		return a.ID < b.ID
	})

	total := len(public)
	page := slicePage(public, offset, limit)

	// 房主昵称：每个 page 内的唯一账号只查一次（同一房主开多个房间是常见形态）。
	names := map[int64]string{}
	for _, s := range page {
		if s.OwnerUserID <= 0 {
			continue
		}
		if _, done := names[s.OwnerUserID]; done {
			continue
		}
		names[s.OwnerUserID] = h.ownerName(c.Request.Context(), s.OwnerUserID)
	}

	items := make([]publicRoomItem, 0, len(page))
	for _, s := range page {
		// 缺行时 m 是零值 → 标题为空串（这就是"降级"的全部含义）。
		m := meta[s.ID]
		item := publicRoomItem{
			RoomID:      s.ID,
			Title:       m.Title,
			OwnerName:   names[s.OwnerUserID],
			MemberCount: s.MemberCount,
			HostOnline:  s.HostOnline,
			HostOffline: s.HostInGrace,
			HasPassword: s.HasPassword,
			IsOfficial:  officialOnly || m.IsOfficial,
		}
		items = append(items, item)
	}

	c.JSON(http.StatusOK, gin.H{
		"items":  items,
		"total":  total,
		"limit":  limit,
		"offset": offset,
		// 回显生效的筛选值：客户端不必猜自己传的 official 被理解成了什么
		// （与 limit/offset 回显同一目的）。
		"official": officialOnly,
	})
}

// ownerName 取房主昵称；取不到时返回空串（**不是**错误）。
//
// 昵称是"锦上添花"的字段：配额用尽/账号已删除/查库抖动都不该让整个列表失败。
// 前端对空昵称显示"未知房主"即可 —— 那比让页面整块报错好得多。
func (h *publicRoomHandler) ownerName(ctx context.Context, userID int64) string {
	if h.deps.Names == nil {
		return ""
	}
	name, err := h.deps.Names.ProfileNameByID(ctx, userID)
	if err != nil {
		log.Printf("[WARN] 公开房列表：查房主 %d 的昵称失败（按空昵称展示）：%v", userID, err)
		return ""
	}
	return name
}

// healMissingMeta 在"内存有房、库里缺行"时投递一次 upsert（§2 的自愈）。
//
// 为什么放在 handler 而不是 service：投递需要 RoomMetaSink（§9 的写队列出口），
// 而 service 的职责边界是"只向接口投递事件、不持有写队列"（I4）。
// 这里同时是唯一一个"已经知道记忆与库不一致"的地方 —— 它刚做完两边求交。
//
// 为什么用 upsert（而不是 insert）：并发/重试下同一个房间码可能被投递多次，
// insert 会因主键冲突失败并触发重试；upsert 是幂等的。
//
// 无房主的房间（ownerUserID == 0，例如单测直接建的房间）**不投递**：
// UpsertRoomMeta 明确拒绝 owner_user_id <= 0（§6：建房必须绑定房主），
// 硬投只会把注定失败的作业塞进写队列。这类房间在列表里照常降级展示。
func (h *publicRoomHandler) healMissingMeta(s service.RoomSnapshot) {
	if s.OwnerUserID <= 0 {
		return
	}
	if h.deps.Sink == nil {
		log.Printf("[WARN] 房间 %s 缺元数据行但装配里没有事件写队列，无法自愈（列表已降级展示）", s.ID)
		return
	}

	meta := &store.RoomMeta{
		RoomID:      s.ID,
		OwnerUserID: s.OwnerUserID,
		// 期望状态：标题空、未公开。这与建房时写死的默认值一致（见 submitRoomMeta），
		// 因此自愈补出来的行**不会**把一个从没公开过的房间变成公开房 ——
		// 自愈只补"缺失"，不改变任何语义。
		Title:       "",
		IsPublic:    false,
		HasPassword: s.HasPassword,
		CreatedAt:   time.Now(),
		LastSeenAt:  time.Now(),
	}
	if !h.deps.Sink.SubmitRoomMeta(meta) {
		log.Printf("[WARN] 房间 %s 的元数据自愈投递失败（队列满或写入器已关闭）；本轮照常降级展示", s.ID)
		return
	}
	log.Printf("公开房列表：房间 %s 在库里缺元数据行，已投递一次自愈 upsert（owner=%d，hasPassword=%t）",
		s.ID, s.OwnerUserID, s.HasPassword)
}

// publicRoomBadRequest 写出 400（参数不合法）。
func publicRoomBadRequest(c *gin.Context, message string) {
	c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
		"error": message,
		"code":  model.CodeBadRequest,
	})
}

// publicRoomPage 解析 limit/offset，并回显**生效值**。
//
// 与管理端完全同一套口径（由下面的 boundedLimit / boundedOffset 承载，
// 只有缺省值与上限不同）：
//   - 非整数一律 400，不静默降级 —— 那会让"翻页翻不到东西"变成查不出来的 bug；
//   - 非正数取缺省、超上限截断、负 offset 归零 —— 这三种是**合法**的客户端宽松写法，
//     夹到边界比报错更友好，且响应里回显生效值让客户端不必猜。
func publicRoomPage(limitRaw, offsetRaw string) (int, int, error) {
	limit, err := boundedLimit(limitRaw, defaultPublicRoomPageLimit, maxPublicRoomPageLimit)
	if err != nil {
		return 0, 0, err
	}
	offset, err := boundedOffset(offsetRaw)
	if err != nil {
		return 0, 0, err
	}
	return limit, offset, nil
}

// boundedLimit 解析 limit：空/非正数 → 缺省；超上限 → 截断；非整数 → 错误。
//
// 非整数**不**降级成缺省值，而是报错（调用方一律回 400）：静默改变分页口径会让
// "界面翻页翻不到东西"变成一个没人查得出来的 bug。
//
// 管理端（admin.go，缺省 50 / 上限 200）与这里的公开房列表共用它，只有这两个
// 参数不同 —— 口径一致才能保证"响应里回显的生效值"在两个列表上是同一套语义。
func boundedLimit(raw string, fallback, max int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("limit 必须是整数")
	}
	switch {
	case n <= 0:
		return fallback, nil
	case n > max:
		return max, nil
	default:
		return n, nil
	}
}

// boundedOffset 解析 offset：空/负数 → 0；非整数 → 错误（管理端与公开房列表共用）。
func boundedOffset(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("offset 必须是整数")
	}
	if n < 0 {
		return 0, nil
	}
	return n, nil
}

// slicePage 对切片做 offset/limit 切片；越界返回空切片而不是 nil。
func slicePage[T any](in []T, offset, limit int) []T {
	if offset >= len(in) || limit <= 0 {
		return []T{}
	}
	end := min(offset+limit, len(in))
	// 复制一份而不是返回子切片：子切片会让调用方（或它启动的 goroutine）
	// 继续持有整个底层数组，而这里恰好可以顺手切掉。
	out := make([]T, end-offset)
	copy(out, in[offset:end])
	return out
}

// 断言：生产装配里的具体类型逐字满足本文件的两个窄接口。
// 放在这里是让"是否真的满足"在**编译期**暴露（与 admin.go 末尾的断言同一目的）。
var (
	_ PublicRoomLister    = (*service.Manager)(nil)
	_ PublicRoomMetaStore = (*store.Store)(nil)
)
