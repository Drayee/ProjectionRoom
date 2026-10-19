// 番剧目录端点：公开读（`/api/animes`）+ 管理端 CRUD（`/api/admin/animes`）。
// wayfinder #37，`/forum` 的数据底座。
//
// 四条判据：
//
//  1. **读公开、写管理端**。番剧目录是"内容"，不是"用户数据"：未登录用户要能浏览
//     `/forum` 才知道这里有什么。因此读端点不挂 RequireAuth；写端点挂
//     RequireAuth + RequireAdmin（I3：授权 100% 在服务端，前端的 v-if 只决定显示）。
//  2. **每个写操作都留审计**。目录是人工维护的共享内容，出问题时要能回答
//     "谁在什么时候把这条加了/改了/删了"。审计走异步单写协程（事件类，§9）：
//     它不在响应路径上，投递失败只记日志，但**缺失必须可见**。
//  3. **清洗在服务端**。标题/别名/简介过 `service.StripControl`（去零宽/Bidi/控制符），
//     否则一个带 Bidi 覆盖的标题能让列表页显示成完全不同的文字。
//     `cover_url` 只接受 http(s)：`javascript:` / `data:` 一律拒 —— 这也是前端
//     `utils/safeLink.ts` 的口径，两边必须一致（前端只是第二道，不是唯一一道）。
//  4. **DELETE 是硬删，但带一条准入判据**（#38 落地后补）。软删除需要"已挂了这个
//     番剧的主题串怎么办"的答案，而当时主题串还没落地；现在有了 —— 票里把处置
//     定成"不允许删有主题串的番剧，409"（`ANIME_HAS_THREADS`）。于是仍然不需要
//     `deleted_at`：一个"删不掉"的状态比一个"删了但看不见"的状态诚实得多。
//     判据在 store（数据事实）+ 数据库外键 `ON DELETE RESTRICT`（兜住竞态）两层，
//     见 store/anime.go 的 DeleteAnime 与迁移 0013。
package handler

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/model"
	"ProjectionRoom/internal/service"
	"ProjectionRoom/internal/store"
)

// codeNotFound 是"资源不存在"的错误码。放在本文件而不是 errors.go：
// 它只被番剧端点用到，而 errors.go 里那批是**跨端点共享**的会话/凭证语义。
const codeNotFound = "NOT_FOUND"

// animePageDefault / animePageMax 是公开列表的分页边界。
//
// 比管理端默认小（20 vs 50）：公开列表是**浏览**入口，一次给太多没有意义；
// 上限 100 只是防失控（比如把 limit 传成 1e6）。
const (
	animePageDefault = 20
	animePageMax     = 100
)

// CodeAnimeTitleTaken 是标题重复的错误码（与 409 配对）。
const CodeAnimeTitleTaken = "ANIME_TITLE_TAKEN"

// CodeAnimeHasThreads 是"这个番剧下还有主题串，不能删"的错误码（与 409 配对）。
//
// wayfinder #38 验收第 2 条把处置定成"不允许删，409"。与 CodeAnimeTitleTaken
// 分成两个码是必要的：两者都是 409，但前端的动作完全不同 ——
// 标题重复是"换个标题再试"，有主题串是"去处理那些主题串（或干脆别删）"。
const CodeAnimeHasThreads = "ANIME_HAS_THREADS"

// AnimeStore 是番剧目录的持久化能力（*store.Store 满足它）。
type AnimeStore interface {
	ListAnimes(ctx context.Context, q string, limit, offset int) ([]store.Anime, int64, error)
	AnimeByID(ctx context.Context, id int64) (*store.Anime, error)
	CreateAnime(ctx context.Context, a *store.Anime) error
	UpdateAnime(ctx context.Context, id int64, title *string, aliases *[]string, coverURL **string, summary *string) (*store.Anime, error)
	DeleteAnime(ctx context.Context, id int64) error
}

// AnimeDeps 是番剧端点的全部依赖。
//
// AuditSink / AuditWriter 与 AdminDeps 里那两个字段是**同一对实例**：
// 这里单独持有是因为本文件不经过 adminHandler，而把审计能力塞进 AnimeStore
// 会让"读目录"与"写审计"变成同一个接口上的事（职责混淆）。
type AnimeDeps struct {
	Store AnimeStore
	// AuditSink 是审计的异步投递出口；AuditWriter 是审计行的写能力。
	// 两者为 nil 时写操作仍然生效，但会记一条 WARN —— 审计是 must 落点，缺失要可见。
	AuditSink   AdminAuditSink
	AuditWriter AdminAuditWriter
}

// registerAnimeRoutes 注册番剧目录路由。
//
// 与其它二期路由同一套约定：账号能力关闭（`PR_DB_DSN` 为空）时整个域**不注册**
// （路径不存在，而不是存在一个永远 500 的端点）；装配残缺时注册一组 503。
func registerAnimeRoutes(api *gin.RouterGroup, deps AnimeDeps, cfg *config.Config, auth AccountService) {
	if api == nil {
		return
	}
	if cfg == nil || strings.TrimSpace(cfg.Auth.DBDSN) == "" {
		return
	}

	if deps.Store == nil {
		unavailable := func(c *gin.Context) {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "番剧目录暂不可用",
				"code":  CodeAuthUnavailable,
			})
		}
		api.GET("/animes", unavailable)
		api.GET("/animes/:animeId", unavailable)
		api.GET("/admin/animes", unavailable)
		api.POST("/admin/animes", unavailable)
		api.PATCH("/admin/animes/:animeId", unavailable)
		api.DELETE("/admin/animes/:animeId", unavailable)
		return
	}

	h := &animeHandler{deps: deps}

	// 公开读：不挂 RequireAuth。限速用与公开房列表同一条"按 IP 令牌桶"，
	// 避免被当成免费的爬取入口。
	readLimiter := newKeyedLimiter(cfg.IPC.RoomMetaPerMinute, cfg.IPC.RoomMetaBurst, "番剧目录")
	api.GET("/animes", readLimiter, h.listPublic)
	api.GET("/animes/:animeId", readLimiter, h.get)

	// 管理端：RequireAuth（401）+ RequireAdmin（403）。两个中间件缺一不可：
	// 只用 RequireAdmin 时未登录会得到 403 而不是 401，前端就无从区分"该登录"与"没权限"。
	//
	// 管理端列表单独一条（而不是复用公开读）：管理端要看全量（将来会有草稿/下架态），
	// 而公开读要过滤；现在两者的差异只是限速与鉴权，但把语义分开后不会在加字段时打架。
	adminGroup := api.Group("/admin/animes", RequireAuth(auth), RequireAdmin())
	adminGroup.GET("", h.listAdmin)
	adminGroup.POST("", h.create)
	adminGroup.PATCH("/:animeId", h.update)
	adminGroup.DELETE("/:animeId", h.remove)
}

type animeHandler struct {
	deps AnimeDeps
}

// animeView 是对外的 JSON 形状（camelCase，与前端 types 一致）。
type animeView struct {
	ID        int64    `json:"id"`
	Title     string   `json:"title"`
	Aliases   []string `json:"aliases"`
	CoverURL  string   `json:"coverUrl"`
	Summary   string   `json:"summary"`
	CreatedAt string   `json:"createdAt"`
}

func toAnimeView(a *store.Anime) animeView {
	aliases := a.Aliases
	if aliases == nil {
		// 空数组而不是 null：前端对 `aliases` 直接 .map / .join，
		// null 会让它在渲染时抛错（那是"列表里有一条就白屏"这类事故的典型来源）。
		aliases = []string{}
	}
	cover := ""
	if a.CoverURL != nil {
		cover = *a.CoverURL
	}
	return animeView{
		ID:        a.ID,
		Title:     a.Title,
		Aliases:   aliases,
		CoverURL:  cover,
		Summary:   a.Summary,
		CreatedAt: a.CreatedAt.Format(timeLayout),
	}
}

// timeLayout 与账号/管理端其它端点保持一致的 RFC3339（前端统一按它解析）。
const timeLayout = "2006-01-02T15:04:05Z07:00"

// listPublic 是 `GET /api/animes?q&limit&offset`。
func (h *animeHandler) listPublic(c *gin.Context) {
	h.list(c, animePageDefault)
}

// listAdmin 是 `GET /api/admin/animes?q&limit&offset`。
func (h *animeHandler) listAdmin(c *gin.Context) {
	h.list(c, defaultAdminPageLimit)
}

func (h *animeHandler) list(c *gin.Context, fallbackLimit int) {
	limit, err := boundedLimit(c.Query("limit"), fallbackLimit, animePageMax)
	if err != nil {
		adminBadRequest(c, err.Error())
		return
	}
	offset, err := boundedOffset(c.Query("offset"))
	if err != nil {
		adminBadRequest(c, err.Error())
		return
	}

	rows, total, err := h.deps.Store.ListAnimes(c.Request.Context(), c.Query("q"), limit, offset)
	if err != nil {
		log.Printf("[ERROR] 番剧列表查询失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询番剧目录失败", "code": model.CodeInternalError})
		return
	}

	items := make([]animeView, 0, len(rows))
	for i := range rows {
		items = append(items, toAnimeView(&rows[i]))
	}
	c.JSON(http.StatusOK, gin.H{
		"items":  items,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// get 是 `GET /api/animes/:animeId`（公开）。
func (h *animeHandler) get(c *gin.Context) {
	id, ok := animeIDParam(c)
	if !ok {
		return
	}
	row, err := h.deps.Store.AnimeByID(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrAnimeNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "番剧不存在", "code": codeNotFound})
			return
		}
		log.Printf("[ERROR] 番剧详情查询失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询番剧失败", "code": model.CodeInternalError})
		return
	}
	c.JSON(http.StatusOK, toAnimeView(row))
}

// animeWriteRequest 是创建/更新的请求体。
//
// 指针字段区分"没传"与"传了空值"：PATCH 的语义是局部更新，
// 把"没传"当成"清空"会让一次改简介抹掉别名与封面。
type animeWriteRequest struct {
	Title    *string   `json:"title"`
	Aliases  *[]string `json:"aliases"`
	CoverURL *string   `json:"coverUrl"`
	Summary  *string   `json:"summary"`
}

// sanitizeAnimeInput 对请求里的文本字段做服务端清洗与校验。
//
// 顺序是刻意的：先 StripControl 再 TrimSpace —— 反过来会留下"去控制符后新出现的
// 首尾空白"（例如 "标题\u200b " 去零宽后变成 "标题 "）。
func sanitizeAnimeInput(req *animeWriteRequest) error {
	if req.Title != nil {
		t := strings.TrimSpace(service.StripControl(*req.Title))
		req.Title = &t
	}
	if req.Aliases != nil {
		cleaned := make([]string, 0, len(*req.Aliases))
		seen := make(map[string]struct{}, len(*req.Aliases))
		for _, raw := range *req.Aliases {
			a := strings.TrimSpace(service.StripControl(raw))
			if a == "" {
				continue // 空别名直接丢：它是用户手滑留下的空行，不是错误
			}
			if _, dup := seen[a]; dup {
				continue // 去重：同一别名写两遍没有意义，也不该因此报错
			}
			seen[a] = struct{}{}
			cleaned = append(cleaned, a)
		}
		req.Aliases = &cleaned
	}
	if req.Summary != nil {
		s := strings.TrimSpace(service.StripControl(*req.Summary))
		req.Summary = &s
	}
	if req.CoverURL != nil {
		u := strings.TrimSpace(*req.CoverURL)
		if u != "" {
			parsed, err := url.Parse(u)
			if err != nil {
				return errors.New("封面地址不是合法的 URL")
			}
			// 只放行 http(s)：`javascript:` / `data:` / `file:` 在 `<img src>` 里
			// 的后果从"不显示"到"执行脚本"不等，而目录的封面本来也只应该是外链图片。
			if parsed.Scheme != "http" && parsed.Scheme != "https" {
				return errors.New("封面地址必须是 http(s) 链接")
			}
			if parsed.Host == "" {
				return errors.New("封面地址缺少主机名")
			}
		}
		req.CoverURL = &u
	}
	return nil
}

// create 是 `POST /api/admin/animes`。
func (h *animeHandler) create(c *gin.Context) {
	actor, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}

	var req animeWriteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		adminBadRequest(c, "请求体不是合法 JSON")
		return
	}
	if req.Title == nil {
		adminBadRequest(c, "title 必填")
		return
	}
	if err := sanitizeAnimeInput(&req); err != nil {
		adminBadRequest(c, err.Error())
		return
	}

	row := &store.Anime{Title: *req.Title}
	if req.Aliases != nil {
		row.Aliases = *req.Aliases
	}
	if req.CoverURL != nil {
		row.CoverURL = req.CoverURL
	}
	if req.Summary != nil {
		row.Summary = *req.Summary
	}

	if err := h.deps.Store.CreateAnime(c.Request.Context(), row); err != nil {
		h.writeStoreError(c, err, "创建番剧失败")
		return
	}

	h.writeAudit(c, actor.ID, "anime.create", row.ID, map[string]any{
		"title": row.Title,
	})
	c.JSON(http.StatusCreated, toAnimeView(row))
}

// update 是 `PATCH /api/admin/animes/:animeId`（局部更新）。
func (h *animeHandler) update(c *gin.Context) {
	actor, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}
	id, ok := animeIDParam(c)
	if !ok {
		return
	}

	var req animeWriteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		adminBadRequest(c, "请求体不是合法 JSON")
		return
	}
	if req.Title == nil && req.Aliases == nil && req.CoverURL == nil && req.Summary == nil {
		adminBadRequest(c, "至少要指定 title / aliases / coverUrl / summary 之一")
		return
	}
	if err := sanitizeAnimeInput(&req); err != nil {
		adminBadRequest(c, err.Error())
		return
	}

	// coverUrl 的"有没有传"必须与别的字段用同一条判据。
	//
	// ⚠ 这里踩过一次：无条件传 `&req.CoverURL` 会让"客户端没传 coverUrl"变成
	// "显式清空封面" —— 因为 `req.CoverURL` 是 `*string`，取它的地址永远非 nil，
	// 而 store 的判断是"这个 **string 是不是 nil"。症状是"只改简介，封面消失"。
	// 因此只有真的传了才传地址。
	var coverArg **string
	if req.CoverURL != nil {
		coverArg = &req.CoverURL
	}

	row, err := h.deps.Store.UpdateAnime(c.Request.Context(), id, req.Title, req.Aliases, coverArg, req.Summary)
	if err != nil {
		h.writeStoreError(c, err, "更新番剧失败")
		return
	}

	// 审计里记**改了哪些字段**而不是完整新值：审计的价值在"谁动了什么"，
	// 把整份内容抄进去会让明细随简介长度膨胀，也让 diff 淹没在噪声里。
	fields := make([]string, 0, 4)
	if req.Title != nil {
		fields = append(fields, "title")
	}
	if req.Aliases != nil {
		fields = append(fields, "aliases")
	}
	if req.CoverURL != nil {
		fields = append(fields, "coverUrl")
	}
	if req.Summary != nil {
		fields = append(fields, "summary")
	}
	h.writeAudit(c, actor.ID, "anime.update", row.ID, map[string]any{
		"title":  row.Title,
		"fields": fields,
	})
	c.JSON(http.StatusOK, toAnimeView(row))
}

// remove 是 `DELETE /api/admin/animes/:animeId`。
func (h *animeHandler) remove(c *gin.Context) {
	actor, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}
	id, ok := animeIDParam(c)
	if !ok {
		return
	}

	// 先读一次标题：删完之后就查不到了，而审计行里"删的是哪个番剧"必须可读
	// （只留一个 id 的话，事后需要另一张表还活着才能解释这条审计）。
	row, err := h.deps.Store.AnimeByID(c.Request.Context(), id)
	if err != nil && !errors.Is(err, store.ErrAnimeNotFound) {
		log.Printf("[ERROR] 删除前查询番剧失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询番剧失败", "code": model.CodeInternalError})
		return
	}

	if err := h.deps.Store.DeleteAnime(c.Request.Context(), id); err != nil {
		h.writeStoreError(c, err, "删除番剧失败")
		return
	}

	detail := map[string]any{}
	if row != nil {
		detail["title"] = row.Title
	}
	h.writeAudit(c, actor.ID, "anime.delete", id, detail)
	c.Status(http.StatusNoContent)
}

// writeStoreError 把 store 的语义错误翻译成 HTTP 状态码。
//
// 只翻译**能确定**的两种：不存在 → 404、标题冲突 → 409。其余一律 500 并记日志，
// 因为"未知错误当 400 返回"会让真正的基础设施故障看起来像用户的错。
func (h *animeHandler) writeStoreError(c *gin.Context, err error, what string) {
	switch {
	case errors.Is(err, store.ErrAnimeNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "番剧不存在", "code": codeNotFound})
	case errors.Is(err, store.ErrAnimeTitleTaken):
		c.JSON(http.StatusConflict, gin.H{"error": "这个标题已经存在", "code": CodeAnimeTitleTaken})
	case errors.Is(err, store.ErrAnimeHasThreads):
		// wayfinder #38 验收第 2 条：不允许删有主题串的番剧。
		//
		// 409 而不是 400：请求本身完全合法，是**当前状态**不允许这个动作 ——
		// 而 400 会让调用方以为参数写错了（去改 id，然后照样失败）。
		// 文案要给出可执行的下一步（"先处理那些主题串"），因为这条拒绝没有
		// 客户端能自己解决的替代路径。
		c.JSON(http.StatusConflict, gin.H{
			"error": "这个番剧下还有主题串，先处理它们再删",
			"code":  CodeAnimeHasThreads,
		})
	default:
		log.Printf("[ERROR] %s: %v", what, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": what, "code": model.CodeInternalError})
	}
}

// writeAudit 投递一条番剧管理动作的审计（异步，事件类 §9）。
//
// 与 adminHandler.writeRoomAudit 同一形状：投递即返回；队列缺失/已关闭只记 WARN。
// 这里不抽成共用函数，是因为 targetType 与日志文案都不同，而抽出来的收益
// 只有几行 —— 共用函数的参数列表会退化成一个 switch。
func (h *animeHandler) writeAudit(c *gin.Context, actorID int64, action string, animeID int64, detail map[string]any) {
	if h.deps.AuditSink == nil || h.deps.AuditWriter == nil {
		log.Printf("[WARN] 番剧动作 %s 无法写审计（装配里缺少写队列或审计写能力）：anime=%d actor=%d",
			action, animeID, actorID)
		return
	}
	ip := c.ClientIP()
	row := &store.AdminAudit{
		ActorID:    actorID,
		Action:     action,
		TargetType: "anime",
		TargetID:   strconv.FormatInt(animeID, 10),
		Detail:     auditDetailJSON(detail),
	}
	if strings.TrimSpace(ip) != "" {
		row.IP = &ip
	}
	if !h.deps.AuditSink.SubmitJob(func(ctx context.Context) error {
		return h.deps.AuditWriter.InsertAudit(ctx, row)
	}) {
		log.Printf("[WARN] 番剧动作 %s 的审计入队失败（队列满或写入器已关闭）：anime=%d actor=%d",
			action, animeID, actorID)
	}
}

// animeIDParam 解析并校验路径参数 :animeId。
//
// 提示语一律含糊到"番剧不存在/参数不合法"而不区分 400 与 404 的原因：
// 这里两条都是"你给的 id 用不了"，而区分它们只会帮扫描器确认哪些 id 存在。
func animeIDParam(c *gin.Context) (int64, bool) {
	raw := strings.TrimSpace(c.Param("animeId"))
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "番剧 id 不合法", "code": model.CodeBadRequest})
		return 0, false
	}
	return id, true
}
