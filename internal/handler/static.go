package handler

import (
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
)

// 单端口部署：同一个端口上同时提供 API、/ws 与前端静态资源（client/dist）。
//
// 为什么可以这样：前端刻意只使用相对路径 /api 与 /ws（client/vite.config.ts 的代理注释），
// 页面本身也由 Vite 构建成带 hash 的 /assets/*。因此把构建产物交给 Go 托管之后，
// 用户只需要暴露一个端口，挂一条内网穿透隧道指向它就能给外部使用：
//
//	浏览器 ──隧道──> PR_ADDR（同一个端口）──┬─ /api、/ws  → gin 业务路由
//	                                        └─ 其它 GET   → client/dist 里的真实文件
//	                                                        未命中则回退 index.html（SPA 路由）
//
// 注意边界：这只解决「页面 + 信令」的可达性。媒体分发仍然是 peer 之间的 WebRTC 直连，
// 隧道不参与媒体传输；打洞失败时没有 TURN（PR_TURN_*）就会连不上。

// immutableCacheControl 用于 Vite 产物：文件名里带内容 hash，内容永远不变，可以放心长缓存。
const immutableCacheControl = "public, max-age=31536000, immutable"

// noCacheControl 用于 index.html 与 SPA 回退：必须每次回源校验，
// 否则用户会卡在旧构建（旧的 index.html 指向已经被删掉的 hash 资源，页面直接白屏）。
const noCacheControl = "no-cache"

// staticDirMissingHint 是静态目录不可用时给浏览器看的中文提示。
const staticDirMissingHint = "前端静态资源不可用：请先执行 npm --prefix client run build（API 与 /ws 不受影响）"

// staticServer 托管前端构建产物并处理 SPA 回退。
//
// 它不缓存 index.html 的内容：每次请求都从磁盘读，配合 no-cache 保证「重新构建 = 刷新即生效」。
type staticServer struct {
	// dir 是静态资源根目录（已 Clean，通常是相对进程工作目录的 client/dist）。
	dir string
	// indexPath 是 SPA 回退的目标文件。
	indexPath string

	// warnOnce 保证「目录/入口缺失」的请求期 WARN 只打一次，避免刷屏。
	warnOnce sync.Once
}

// registerStatic 把静态资源与 SPA 回退挂到路由上。
//
// 调用位置必须在 /api、/ws、/healthz 全部注册之后（见 NewRouter），
// 这样 NoRoute 只兜住"业务路由之外"的请求，不会抢占已有路由。
// 目录不存在/不可读时只记 WARN，服务照常提供 API 与 /ws。
func registerStatic(r *gin.Engine, cfg *config.Config) {
	if !cfg.Static.Serve {
		log.Printf("静态资源托管已关闭（PR_SERVE_STATIC=0）：本进程只提供 API 与 /ws，页面请用别的服务器托管")
		return
	}

	dir := strings.TrimSpace(cfg.Static.Dir)
	if dir == "" {
		log.Printf("[WARN] PR_STATIC_DIR 为空，已跳过静态资源托管（API 与 /ws 不受影响）")
		return
	}

	s := newStaticServer(dir)

	// Vite 产物（/assets/*）走独立路由：命中给 immutable 长缓存；
	// 未命中直接 404，绝不回退 index.html —— 把一个 HTML 当成 .js 返回只会让浏览器报 MIME 错，更难排查。
	r.GET("/assets/*filepath", s.serveAsset)

	// 其余未命中路由（/、/room/<房间码>、/favicon.ico…）交给 SPA 回退。
	r.NoRoute(s.serveFallback)
}

// newStaticServer 构造静态处理器，并在启动时对目录做一次探测。
// 探测失败不是错误：开发态可能还没构建前端，此时 API/WS 必须照常可用。
func newStaticServer(dir string) *staticServer {
	clean := filepath.Clean(dir)
	s := &staticServer{
		dir:       clean,
		indexPath: filepath.Join(clean, "index.html"),
	}

	switch st, err := os.Stat(clean); {
	case err != nil:
		log.Printf("[WARN] 静态资源目录不可用（%s）：%v；页面将不可访问，请先执行 npm --prefix client run build（API 与 /ws 不受影响）", clean, err)
	case !st.IsDir():
		log.Printf("[WARN] 静态资源路径不是目录（%s）；页面将不可访问，请先执行 npm --prefix client run build（API 与 /ws 不受影响）", clean)
	default:
		if _, err := os.Stat(s.indexPath); err != nil {
			log.Printf("[WARN] 静态资源目录 %s 里没有 index.html：%v；页面将不可访问，请先执行 npm --prefix client run build（API 与 /ws 不受影响）", clean, err)
		} else {
			log.Printf("静态资源托管已启用：%s（API、/ws、页面同端口）", clean)
		}
	}

	return s
}

// serveAsset 处理 /assets/*：只服务真实文件，缓存 immutable，未命中直接 404。
func (s *staticServer) serveAsset(c *gin.Context) {
	if !isGetOrHead(c.Request.Method) {
		notFound(c, "静态资源只支持 GET/HEAD")
		return
	}
	if !s.serveFile(c, c.Request.URL.Path, immutableCacheControl) {
		notFound(c, "静态资源不存在（前端可能已重新构建，请刷新页面）")
	}
}

// serveFallback 是 SPA 回退：命中真实文件就返回，否则把 index.html 发出去。
//
// 这条路径让 /room/<房间码> 的刷新、直接粘贴链接、以及任何前端路由深链都能进页面。
func (s *staticServer) serveFallback(c *gin.Context) {
	// 非 GET/HEAD（例如有人往 /ws 发 POST）不应拿到页面。
	if !isGetOrHead(c.Request.Method) {
		notFound(c, "未找到该资源")
		return
	}

	urlPath := c.Request.URL.Path
	// 防御性排除：这些前缀正常不会走到 NoRoute，万一走到也不能被页面顶掉。
	if isReservedPath(urlPath) {
		notFound(c, "未找到该资源")
		return
	}

	// 真实文件（favicon、robots.txt、vite.svg…）：按路径决定缓存策略。
	if s.serveFile(c, urlPath, cacheControlFor(urlPath)) {
		return
	}

	// SPA 回退：交给 index.html（no-cache）。
	if s.serveFile(c, "/index.html", noCacheControl) {
		return
	}

	// 目录缺失 / 没有 index.html：降级为 404 + 中文提示，而不是让服务崩掉。
	s.warnIndexMissing()
	notFound(c, staticDirMissingHint)
}

// serveFile 打开静态根目录下的真实文件并写出。
// 返回 false 表示"没有这个文件"（不存在、不是普通文件、路径非法），由调用方决定回退策略。
func (s *staticServer) serveFile(c *gin.Context, urlPath, cacheControl string) bool {
	full, ok := s.resolve(urlPath)
	if !ok {
		return false
	}

	f, err := os.Open(full)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	st, err := f.Stat()
	if err != nil || st.IsDir() {
		return false
	}

	c.Header("Cache-Control", cacheControl)
	// ServeContent 负责 Content-Type（按扩展名）、Last-Modified、Range 与 304 协商。
	http.ServeContent(c.Writer, c.Request, st.Name(), st.ModTime(), f)
	return true
}

// resolve 把 URL 路径安全地映射到静态根目录下的绝对路径。
//
// 防目录穿越：拒绝反斜杠与 NUL（Windows 上 "\" 也是分隔符），
// 用 path.Clean 归一化掉 ".."，最后再用 filepath.Rel 复核结果仍在根目录内。
func (s *staticServer) resolve(urlPath string) (string, bool) {
	if urlPath == "" || urlPath == "/" {
		urlPath = "/index.html"
	}
	if strings.ContainsAny(urlPath, "\\\x00") {
		return "", false
	}

	clean := path.Clean("/" + urlPath)
	full := filepath.Join(s.dir, filepath.FromSlash(strings.TrimPrefix(clean, "/")))

	rel, err := filepath.Rel(s.dir, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return full, true
}

// warnIndexMissing 在请求期发现入口缺失时补一条 WARN（只打一次）。
func (s *staticServer) warnIndexMissing() {
	s.warnOnce.Do(func() {
		log.Printf("[WARN] 静态资源入口缺失（%s）：请先执行 npm --prefix client run build（API 与 /ws 不受影响）", s.indexPath)
	})
}

// cacheControlFor 返回路径对应的缓存策略。
//
// 只有 Vite 的 /assets/*（文件名带内容 hash）可以 immutable 长缓存；
// 其余一切（含 index.html）都用 no-cache，浏览器每次回源校验，避免用户卡在旧构建。
func cacheControlFor(urlPath string) string {
	if strings.HasPrefix(urlPath, "/assets/") {
		return immutableCacheControl
	}
	return noCacheControl
}

// isReservedPath 判断路径是否属于业务路由前缀。
// /api 与 /ws 由真实路由处理；这里只是"NoRoute 也绝不能顶掉它们"的最后一道保险。
func isReservedPath(urlPath string) bool {
	for _, prefix := range []string{"/api", "/ws", "/healthz"} {
		if urlPath == prefix || strings.HasPrefix(urlPath, prefix+"/") {
			return true
		}
	}
	return false
}

func isGetOrHead(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

// notFound 统一输出 404 与中文提示；错误响应不允许被缓存。
func notFound(c *gin.Context, message string) {
	c.Header("Cache-Control", "no-store")
	c.String(http.StatusNotFound, message)
}
