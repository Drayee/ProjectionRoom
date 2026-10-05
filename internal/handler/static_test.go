package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
)

// newTestStaticEngine 起一个只挂静态资源的引擎（不依赖 Hub/房间，因此单测很轻）。
// 返回的静态根目录是 dir 下的 dist/，其上一层放了一个"机密文件"，用于验证目录穿越被挡。
func newTestStaticEngine(t *testing.T) (*gin.Engine, string) {
	t.Helper()

	parent := t.TempDir()
	root := filepath.Join(parent, "dist")
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o755); err != nil {
		t.Fatalf("建静态目录失败: %v", err)
	}
	writeFile(t, filepath.Join(root, "index.html"), `<!doctype html><html><body><div id="app"></div></body></html>`)
	writeFile(t, filepath.Join(root, "assets", "index-D0KZYLfD.js"), "console.log('前端产物')")
	// 静态根目录之外的文件：任何穿越尝试都绝不能读到它。
	writeFile(t, filepath.Join(parent, "secret.txt"), "这是根目录外的机密内容")

	cfg := config.Default()
	cfg.Static.Serve = true
	cfg.Static.Dir = root

	r := gin.New()
	registerStatic(r, cfg)
	return r, root
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写文件 %s 失败: %v", path, err)
	}
}

func doGet(t *testing.T, r *gin.Engine, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestStaticServesIndexOnRoot 覆盖 GET /：页面入口必须能拿到，且不允许缓存。
func TestStaticServesIndexOnRoot(t *testing.T) {
	r, _ := newTestStaticEngine(t)

	w := doGet(t, r, "/")
	if w.Code != http.StatusOK {
		t.Fatalf("GET / 应当 200，实际 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `<div id="app">`) {
		t.Fatalf("GET / 正文里没有挂载点: %q", w.Body.String())
	}
	if cc := w.Header().Get("Cache-Control"); cc != noCacheControl {
		t.Fatalf("GET / 的 Cache-Control 应为 %q，实际 %q", noCacheControl, cc)
	}
}

// TestStaticSPAFallback 覆盖 /room/<房间码>：刷新与直接粘贴链接都要能进页面。
func TestStaticSPAFallback(t *testing.T) {
	r, _ := newTestStaticEngine(t)

	for _, p := range []string{"/room/ABCDEF", "/room/abcdef/anything", "/unknown/deep/path"} {
		w := doGet(t, r, p)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s 应当 200（SPA 回退），实际 %d", p, w.Code)
		}
		if !strings.Contains(w.Body.String(), `<div id="app">`) {
			t.Fatalf("GET %s 没有回退到 index.html: %q", p, w.Body.String())
		}
		if cc := w.Header().Get("Cache-Control"); cc != noCacheControl {
			t.Fatalf("GET %s 的 Cache-Control 应为 %q，实际 %q", p, noCacheControl, cc)
		}
	}
}

// TestStaticIndexHTMLCacheControl 覆盖显式 /index.html：同样 no-cache，避免卡在旧构建。
func TestStaticIndexHTMLCacheControl(t *testing.T) {
	r, _ := newTestStaticEngine(t)

	w := doGet(t, r, "/index.html")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /index.html 应当 200，实际 %d", w.Code)
	}
	if cc := w.Header().Get("Cache-Control"); cc != noCacheControl {
		t.Fatalf("GET /index.html 的 Cache-Control 应为 %q，实际 %q", noCacheControl, cc)
	}
}

// TestStaticAssetsImmutable 覆盖 Vite 产物：命中真实文件 → 200 + immutable 长缓存。
func TestStaticAssetsImmutable(t *testing.T) {
	r, _ := newTestStaticEngine(t)

	w := doGet(t, r, "/assets/index-D0KZYLfD.js")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /assets/index-D0KZYLfD.js 应当 200，实际 %d", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, "前端产物") {
		t.Fatalf("产物正文不对: %q", body)
	}
	if cc := w.Header().Get("Cache-Control"); cc != immutableCacheControl {
		t.Fatalf("/assets/* 的 Cache-Control 应为 %q；其中必须含 immutable，实际 %q", immutableCacheControl, cc)
	}
	if !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatal("Cache-Control 必须包含 immutable")
	}
}

// TestStaticMissingAssetIs404 锁定一个关键取舍：缺失的 /assets/* 必须 404，
// 绝不能回退 index.html（把 HTML 当 .js 返回只会让浏览器报 MIME 错，更难排查）。
func TestStaticMissingAssetIs404(t *testing.T) {
	r, _ := newTestStaticEngine(t)

	w := doGet(t, r, "/assets/index-GONE.js")
	if w.Code != http.StatusNotFound {
		t.Fatalf("缺失产物应当 404，实际 %d", w.Code)
	}
	if strings.Contains(w.Body.String(), `<div id="app">`) {
		t.Fatal("缺失产物不应回退到 index.html")
	}
}

// TestStaticBlocksPathTraversal 覆盖目录穿越：静态根目录之外的文件绝不能被读到。
func TestStaticBlocksPathTraversal(t *testing.T) {
	r, _ := newTestStaticEngine(t)

	for _, p := range []string{"/../secret.txt", "/assets/../../secret.txt", "/room/../../../secret.txt"} {
		w := doGet(t, r, p)
		if strings.Contains(w.Body.String(), "机密") {
			t.Fatalf("GET %s 泄露了静态根目录之外的文件: %q", p, w.Body.String())
		}
	}
}

// TestStaticResolveRejectsIllegalPaths 直接覆盖路径归一化：反斜杠与 NUL 一律拒绝。
func TestStaticResolveRejectsIllegalPaths(t *testing.T) {
	s := &staticServer{dir: "client/dist", indexPath: filepath.Join("client/dist", "index.html")}

	for _, p := range []string{`\..\secret.txt`, "/a\\b.js", "/a\x00b"} {
		if _, ok := s.resolve(p); ok {
			t.Fatalf("非法路径 %q 应当被拒绝", p)
		}
	}

	// 合法的 ".." 会被归一化，且结果必须仍在根目录内。
	got, ok := s.resolve("/../index.html")
	if !ok {
		t.Fatal("归一化后的路径应当合法")
	}
	if got != filepath.Join("client/dist", "index.html") {
		t.Fatalf("路径归一化结果不对: %q", got)
	}
}

// TestStaticMissingDirDegrades 覆盖"未构建前端"的降级：只降级，不 panic，
// 并且给出一条能指明下一步的中文提示。
func TestStaticMissingDirDegrades(t *testing.T) {
	cfg := config.Default()
	cfg.Static.Dir = filepath.Join(t.TempDir(), "not-built-yet")

	r := gin.New()
	registerStatic(r, cfg)

	w := doGet(t, r, "/")
	if w.Code != http.StatusNotFound {
		t.Fatalf("目录缺失时 GET / 应当 404（而不是 500），实际 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "npm --prefix client run build") {
		t.Fatalf("降级提示里应当给出构建命令，实际 %q", w.Body.String())
	}
}

// TestStaticDisabled 覆盖 PR_SERVE_STATIC=0：不注册静态路由，全部落到引擎默认 404。
func TestStaticDisabled(t *testing.T) {
	cfg := config.Default()
	cfg.Static.Serve = false

	r := gin.New()
	registerStatic(r, cfg)

	if w := doGet(t, r, "/"); w.Code != http.StatusNotFound {
		t.Fatalf("关闭静态托管后 GET / 应当 404，实际 %d", w.Code)
	}
}

// TestStaticNeverShadowsReservedRoutes 锁定路由优先级：NoRoute 不能顶掉 /api、/ws、/healthz 的前缀。
func TestStaticNeverShadowsReservedRoutes(t *testing.T) {
	r, _ := newTestStaticEngine(t)
	// 用 JSON 之外的哨兵值模拟"真实业务路由"。
	r.GET("/api/v1/ping", func(c *gin.Context) { c.String(http.StatusOK, "API_OK") })
	r.GET("/healthz", func(c *gin.Context) { c.String(http.StatusOK, "HEALTH_OK") })

	if body := doGet(t, r, "/api/v1/ping").Body.String(); body != "API_OK" {
		t.Fatalf("/api 路由被静态资源顶掉了: %q", body)
	}
	if body := doGet(t, r, "/healthz").Body.String(); body != "HEALTH_OK" {
		t.Fatalf("/healthz 路由被静态资源顶掉了: %q", body)
	}

	// 未注册的 /api/* 必须是 404 JSON 之外的明确错误，而不是被页面顶成 200。
	w := doGet(t, r, "/api/v1/not-registered")
	if w.Code != http.StatusNotFound {
		t.Fatalf("未注册的 /api/* 应当 404，实际 %d", w.Code)
	}
	if strings.Contains(w.Body.String(), `<div id="app">`) {
		t.Fatal("未注册的 /api/* 不应回退到 index.html")
	}
}

// TestStaticNonGetIsNotServedPage 非 GET/HEAD 不应拿到页面。
func TestStaticNonGetIsNotServedPage(t *testing.T) {
	r, _ := newTestStaticEngine(t)

	req := httptest.NewRequest(http.MethodPost, "/room/ABCDEF", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("POST /room/ABCDEF 应当 404，实际 %d", w.Code)
	}
}

// TestStaticHeadRequest 覆盖 HEAD：拿得到头，但不应有正文。
func TestStaticHeadRequest(t *testing.T) {
	r, _ := newTestStaticEngine(t)

	req := httptest.NewRequest(http.MethodHead, "/", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HEAD / 应当 200，实际 %d", w.Code)
	}
	if body, _ := io.ReadAll(w.Body); len(body) != 0 {
		t.Fatalf("HEAD / 不应有正文，实际 %q", string(body))
	}
}
