// 帖子域端点（wayfinder #27）：公开读 + 登录写。
//
// 形状对齐前端门面契约（client/src/types/posts.ts）：列表是 `{items, hasMore}`
// 的无限滚动，不是 `{items, total}` 的页码表 —— 服务端因此**不做全表 COUNT**
// （帖子会长得比 anime 快得多，每页一次 COUNT 是白付的）。
//
// 四条判据：
//
//  1. **读公开、写登录**。看帖子不需要账号（与游客进房同一条产品决定）；
//     发帖/点赞/评论/删评论需要。授权 100% 在服务端（I3）。
//  2. **读端点"可选识别登录者"**：登录时列表/详情要带 liked（前端形状是必填）。
//     实现是 optionalViewer —— 有合法 token 就认，没有/无效就当匿名。
//     这不是 RequireAuth 的降级：它只服务只读路径，写路径仍走完整 RequireAuth。
//     token 无效时**不报 401**：读公开内容不需要凭据，让坏 token 挡住浏览是本末倒置。
//  3. **只能删自己的评论**（验收第 1 条）：判据是 author_id == 请求者，
//     由 store 在事务里复核（ErrCommentForbidden → 403）。不是前端隐藏按钮。
//  4. **内容清洗在进 store 之前**：StripControl → TrimSpace（先 Strip 再 Trim，
//     反过来会留下去控制符后新出现的首尾空白）；images 只收 http(s) 外链
//     （与 anime 的 cover_url 同口径，前端 safeLink.ts 是第二道不是唯一一道）。
//
// 计数（like_count / comment_count）的维护为什么必须与纪录插入同事务，
// 见 store/posts.go 与迁移 0010 的注释（反范式列的代价）。
package handler

import (
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

// 帖子域的长度上限与 store 保持一致（两处同值的理由见 store/posts.go）。
const (
	maxPostTitleHTTP   = 60
	maxPostContentHTTP = 5000
	maxPostImagesHTTP  = 9
	maxCommentHTTP     = 1000
)

// registerPostRoutes 注册帖子路由。装配约定与 anime 相同：
// 账号能力关闭时不注册；Store 缺失时注册一组 503。
//
// 依赖直接用 *store.Store（与 AnimeStore 接口不同的取舍）：帖子域的方法多
// （7 个端点 × 各自的读写），抽一个只被一处实现的接口不会带来可测性收益
// （handler 层的测试本来就是起真 store 或整个 server），只会让"这个方法到底谁实现"
// 多一跳。#37 抽接口是因为那里要同时注入审计两件套；这里没有。
func registerPostRoutes(api *gin.RouterGroup, st *store.Store, cfg *config.Config, auth AccountService) {
	if api == nil {
		return
	}
	if cfg == nil || strings.TrimSpace(cfg.Auth.DBDSN) == "" {
		return
	}
	if st == nil {
		unavailable := func(c *gin.Context) {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "帖子能力暂不可用",
				"code":  CodeAuthUnavailable,
			})
		}
		for _, p := range []string{"/posts", "/posts/:postId", "/posts/:postId/like", "/posts/:postId/comments", "/comments/:commentId"} {
			api.GET(p, unavailable)
			api.POST(p, unavailable)
			api.DELETE(p, unavailable)
		}
		return
	}

	h := &postHandler{store: st, auth: auth}

	// 读：免登录（但识别登录者以带出 liked）。写：RequireAuth。
	//
	// 限速分三档，依据是"被滥用时的伤害不同"与"操作的固有成本"：
	//   - **发帖**走注册桶（5/min）：帖子是长内容、要人读，刷帖的伤害是污染信息流；
	//   - **点赞/评论**走登录桶（10/min）：轻量互动，但会触发写库与计数维护。
	//     真机验收时先试过"四个写端点共用注册桶"—— 发一帖 + 两条评论 + 一次点赞
	//     就撞 429，那是正常用户的行为量，不是滥用。限速挡的是脚本，不该挡人。
	//   - **删除自己的评论**走只读桶（60/min）：它是幂等的（重试不会产生第二份效果），
	//     且只会减少数据。用互动桶会挡住"连删几条自己的评论"这个正常动作 ——
	//     真机验收就撞到过：前面几步把令牌爬光了，删除无辜 429。
	//（与 #37 同一先例：复用既有几桶，不为新域发明配置。）
	readLimiter := newKeyedLimiter(cfg.IPC.RoomMetaPerMinute, cfg.IPC.RoomMetaBurst, "帖子列表")
	createLimiter := newKeyedLimiter(cfg.IPC.RegisterPerMinute, cfg.IPC.RegisterBurst, "发帖")
	interactLimiter := newKeyedLimiter(cfg.IPC.LoginPerMinute, cfg.IPC.LoginBurst, "帖子互动")

	api.GET("/posts", readLimiter, h.list)
	api.GET("/posts/:postId", readLimiter, h.detail)
	api.GET("/posts/:postId/comments", readLimiter, h.listComments)

	api.POST("/posts", RequireAuth(auth), createLimiter, h.create)
	api.POST("/posts/:postId/like", RequireAuth(auth), interactLimiter, h.like)
	api.POST("/posts/:postId/comments", RequireAuth(auth), interactLimiter, h.addComment)
	api.DELETE("/comments/:commentId", RequireAuth(auth), readLimiter, h.deleteComment)
}

type postHandler struct {
	store *store.Store
	auth  AccountService
}

// postAuthorView 是响应里的作者形状。**没有 avatarChar / avatarColor**：
// 那是纯 UI 派生（首字母 + 名字哈希取色，客户端已有逻辑），后端契约不掺显示层
// （映射放在 #26 的 real 适配层做）。
type postAuthorView struct {
	ID          int64  `json:"id"`
	DisplayName string `json:"displayName"`
}

type postView struct {
	ID           int64          `json:"id"`
	Title        string         `json:"title"`
	Content      string         `json:"content"`
	AuthorID     int64          `json:"authorId"`
	Author       postAuthorView `json:"author"`
	Images       []string       `json:"images"`
	LikeCount    int            `json:"likeCount"`
	CommentCount int            `json:"commentCount"`
	Liked        bool           `json:"liked"`
	CreatedAt    string         `json:"createdAt"`
}

type commentView struct {
	ID        int64          `json:"id"`
	PostID    int64          `json:"postId"`
	AuthorID  int64          `json:"authorId"`
	Author    postAuthorView `json:"author"`
	Content   string         `json:"content"`
	CreatedAt string         `json:"createdAt"`
}

func toPostView(p *store.Post) postView {
	images := p.Images
	if images == nil {
		images = []string{} // 空数组而不是 null：前端直接 .map（同 anime 的理由）
	}
	return postView{
		ID:        p.ID,
		Title:     p.Title,
		Content:   p.Content,
		AuthorID:  p.AuthorID,
		Author:    postAuthorView{ID: p.AuthorID, DisplayName: p.AuthorName},
		Images:    images,
		LikeCount: p.LikeCount, CommentCount: p.CommentCount,
		Liked:     p.Liked,
		CreatedAt: p.CreatedAt.Format(timeLayout),
	}
}

// optionalViewer 尝试从请求识别登录者；失败一律返回 0（匿名）。
//
// 与 RequireAuth 的差别是刻意的：读公开内容不需要凭据，坏 token 不该挡住浏览，
// 更不该返回 401（那会把"帖子打不开"归因到用户的登录状态上）。
// 写路径不使用它 —— 那里仍走完整的 RequireAuth。
func (h *postHandler) optionalViewer(c *gin.Context) int64 {
	token, ok := bearerToken(c)
	if !ok || h.auth == nil {
		return 0
	}
	u, err := h.auth.VerifyAccessToken(c.Request.Context(), token)
	if err != nil {
		return 0
	}
	return u.ID
}

// list 是 `GET /api/posts?sort&limit&offset`。
//
// 用 offset 而不是 page（前端契约是 `page`，但评论端点本来就用 offset）：
// 一个域里混两种分页风格，前端适配层就得写两套换算，而那种换算正是 off-by-one
// 的温床。统一成 offset 后，调用方传 0/20/40 就是第 1/2/3 页，无需再算页码。
// 前端 types/posts.ts 的 ListPostsInput.page 由 #26 的 real 适配层换算成 offset。
func (h *postHandler) list(c *gin.Context) {
	sort := store.PostsSortLatest
	switch strings.ToLower(strings.TrimSpace(c.Query("sort"))) {
	case "", "latest", "new":
		sort = store.PostsSortLatest
	case "hot":
		sort = store.PostsSortHot
	default:
		adminBadRequest(c, "sort 只支持 latest 或 hot")
		return
	}

	limit, err := boundedLimit(c.Query("limit"), defaultPostHTTPLimit, maxPostListHTTPLimit)
	if err != nil {
		adminBadRequest(c, err.Error())
		return
	}
	offset, err := boundedOffset(c.Query("offset"))
	if err != nil {
		adminBadRequest(c, err.Error())
		return
	}

	viewer := h.optionalViewer(c)
	rows, hasMore, err := h.store.ListPosts(c.Request.Context(), sort, limit, offset, viewer)
	if err != nil {
		postInternal(c, "帖子列表查询失败", err)
		return
	}
	items := make([]postView, 0, len(rows))
	for i := range rows {
		items = append(items, toPostView(&rows[i]))
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "hasMore": hasMore})
}

// detail 是 `GET /api/posts/:postId`。
func (h *postHandler) detail(c *gin.Context) {
	id, ok := postIDParam(c)
	if !ok {
		return
	}
	row, err := h.store.PostByID(c.Request.Context(), id, h.optionalViewer(c))
	if err != nil {
		h.writeStoreError(c, err, "帖子详情查询失败")
		return
	}
	c.JSON(http.StatusOK, toPostView(row))
}

type createPostRequest struct {
	Title   string   `json:"title"`
	Content string   `json:"content"`
	Images  []string `json:"images"`
}

// create 是 `POST /api/posts`（登录）。
func (h *postHandler) create(c *gin.Context) {
	actor, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}

	var req createPostRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		adminBadRequest(c, "请求体不是合法 JSON")
		return
	}
	req.Title = strings.TrimSpace(service.StripControl(req.Title))
	req.Content = strings.TrimSpace(service.StripControl(req.Content))
	if req.Title == "" {
		adminBadRequest(c, "标题不能为空")
		return
	}
	if len([]rune(req.Title)) > maxPostTitleHTTP {
		adminBadRequest(c, "标题超过 60 个字符")
		return
	}
	if req.Content == "" {
		adminBadRequest(c, "正文不能为空")
		return
	}
	if len([]rune(req.Content)) > maxPostContentHTTP {
		adminBadRequest(c, "正文超过 5000 个字符")
		return
	}
	images, err := sanitizeImageURLs(req.Images)
	if err != nil {
		adminBadRequest(c, err.Error())
		return
	}

	row := &store.Post{AuthorID: actor.ID, Title: req.Title, Content: req.Content, Images: images}
	if err := h.store.CreatePost(c.Request.Context(), row); err != nil {
		h.writeStoreError(c, err, "创建帖子失败")
		return
	}
	c.JSON(http.StatusCreated, toPostView(&store.Post{
		ID: row.ID, AuthorID: row.AuthorID, Title: row.Title, Content: row.Content,
		Images: row.Images, CreatedAt: row.CreatedAt, AuthorName: actor.DisplayName,
	}))
}

// like 是 `POST /api/posts/:postId/like`（登录，toggle 语义，回新状态）。
func (h *postHandler) like(c *gin.Context) {
	actor, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}
	id, ok := postIDParam(c)
	if !ok {
		return
	}
	liked, err := h.store.TogglePostLike(c.Request.Context(), id, actor.ID)
	if err != nil {
		h.writeStoreError(c, err, "点赞操作失败")
		return
	}
	// 回读权威行：响应里带最新计数（前端乐观更新要靠它对账，计数漂移会当场暴露）。
	row, err := h.store.PostByID(c.Request.Context(), id, actor.ID)
	if err != nil {
		h.writeStoreError(c, err, "点赞后回读帖子失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"liked": liked, "post": toPostView(row)})
}

type addCommentRequest struct {
	Content string `json:"content"`
}

// addComment 是 `POST /api/posts/:postId/comments`（登录）。
func (h *postHandler) addComment(c *gin.Context) {
	actor, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}
	id, ok := postIDParam(c)
	if !ok {
		return
	}
	var req addCommentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		adminBadRequest(c, "请求体不是合法 JSON")
		return
	}
	content := strings.TrimSpace(service.StripControl(req.Content))
	if content == "" {
		adminBadRequest(c, "评论内容不能为空")
		return
	}
	if len([]rune(content)) > maxCommentHTTP {
		adminBadRequest(c, "评论超过 1000 个字符")
		return
	}

	row := &store.PostComment{PostID: id, AuthorID: actor.ID, Content: content, AuthorName: actor.DisplayName}
	if err := h.store.AddPostComment(c.Request.Context(), row); err != nil {
		h.writeStoreError(c, err, "创建评论失败")
		return
	}
	c.JSON(http.StatusCreated, commentView{
		ID: row.ID, PostID: row.PostID, AuthorID: row.AuthorID,
		Author:  postAuthorView{ID: row.AuthorID, DisplayName: row.AuthorName},
		Content: row.Content, CreatedAt: row.CreatedAt.Format(timeLayout),
	})
}

// listComments 是 `GET /api/posts/:postId/comments?page`（公开，老→新）。
func (h *postHandler) listComments(c *gin.Context) {
	id, ok := postIDParam(c)
	if !ok {
		return
	}
	// 帖子不存在时评论列表直接 404：返回空列表会让"删掉的帖子"看起来还在。
	if _, err := h.store.PostByID(c.Request.Context(), id, 0); err != nil {
		h.writeStoreError(c, err, "查询评论的帖子失败")
		return
	}
	limit, err := boundedLimit(c.Query("limit"), defaultPostHTTPLimit, maxPostListHTTPLimit)
	if err != nil {
		adminBadRequest(c, err.Error())
		return
	}
	offset, err := boundedOffset(c.Query("offset"))
	if err != nil {
		adminBadRequest(c, err.Error())
		return
	}
	rows, total, err := h.store.ListPostComments(c.Request.Context(), id, limit, offset)
	if err != nil {
		postInternal(c, "评论列表查询失败", err)
		return
	}
	items := make([]commentView, 0, len(rows))
	for i := range rows {
		r := &rows[i]
		items = append(items, commentView{
			ID: r.ID, PostID: r.PostID, AuthorID: r.AuthorID,
			Author:    postAuthorView{ID: r.AuthorID, DisplayName: r.AuthorName},
			Content:   r.Content,
			CreatedAt: r.CreatedAt.Format(timeLayout),
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "limit": limit, "offset": offset})
}

// deleteComment 是 `DELETE /api/comments/:commentId`（登录 + 仅作者）。
func (h *postHandler) deleteComment(c *gin.Context) {
	actor, ok := CurrentUser(c)
	if !ok {
		abortUnauthorized(c, CodeSessionInvalid, "登录状态无效，请重新登录")
		return
	}
	raw := strings.TrimSpace(c.Param("commentId"))
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "评论 id 不合法", "code": model.CodeBadRequest})
		return
	}
	if err := h.store.DeletePostComment(c.Request.Context(), id, actor.ID); err != nil {
		switch {
		case errors.Is(err, store.ErrCommentNotFound), errors.Is(err, store.ErrPostNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "评论不存在", "code": codeNotFound})
		case errors.Is(err, store.ErrCommentForbidden):
			// 服务端判"只能删自己的评论"（验收第 1 条）。不透露那条评论是否存在之外的信息。
			c.JSON(http.StatusForbidden, gin.H{"error": "只能删除自己的评论", "code": CodeForbidden})
		default:
			postInternal(c, "删除评论失败", err)
		}
		return
	}
	c.Status(http.StatusNoContent)
}

// writeStoreError 把 store 的语义错误翻译成 HTTP 状态（同 anime 的纪律：
// 只翻译能确定的，未知一律 500 + 日志——"未知当 400"会把基础设施故障伪装成用户的错）。
func (h *postHandler) writeStoreError(c *gin.Context, err error, what string) {
	switch {
	case errors.Is(err, store.ErrPostNotFound), errors.Is(err, store.ErrCommentNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "内容不存在", "code": codeNotFound})
	default:
		postInternal(c, what, err)
	}
}

// sanitizeImageURLs 校验图片外链：只收 http(s)，去重，最多 9 条。
func sanitizeImageURLs(in []string) ([]string, error) {
	if len(in) > maxPostImagesHTTP {
		return nil, errors.New("图片最多 9 张")
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, raw := range in {
		u := strings.TrimSpace(raw)
		if u == "" {
			continue
		}
		parsed, err := url.Parse(u)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return nil, errors.New("图片地址必须是 http(s) 外链")
		}
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	return out, nil
}

// postIDParam 解析并校验路径参数 :postId（400 的措辞刻意含糊：不区分"格式错"与
// "不存在"，免得这条端点变成 id 枚举器。同 animeIDParam 的纪律）。
func postIDParam(c *gin.Context) (int64, bool) {
	raw := strings.TrimSpace(c.Param("postId"))
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "帖子 id 不合法", "code": model.CodeBadRequest})
		return 0, false
	}
	return id, true
}

func postInternal(c *gin.Context, what string, err error) {
	log.Printf("[ERROR] %s: %v", what, err)
	c.JSON(http.StatusInternalServerError, gin.H{"error": "服务端内部错误", "code": model.CodeInternalError})
}

const (
	defaultPostHTTPLimit = 20
	maxPostListHTTPLimit = 100
)
