package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
)

// segmenterDownloadBody 是每个平台文件的"假二进制"内容。
// 内容各不相同，这样 sha256 断言才真能发现"张冠李戴"（把 A 的摘要配到 B 上）。
var segmenterDownloadBody = map[string]string{
	"segmenter-windows-amd64.exe": "windows-amd64 的假二进制",
	"segmenter-linux-amd64":       "linux-amd64 的假二进制",
	"segmenter-linux-arm64":       "linux-arm64 的假二进制",
	"segmenter-darwin-amd64":      "darwin-amd64 的假二进制",
	"segmenter-darwin-arm64":      "darwin-arm64 的假二进制",
}

// newTestDownloadsEngine 起一个只挂下载清单的引擎（不依赖 Hub/房间，因此单测很轻）。
// 它**不**创建目录：由调用方决定目录是不存在、还是空目录、还是放好文件。
func newTestDownloadsEngine(t *testing.T, dir string) *gin.Engine {
	t.Helper()

	cfg := config.Default()
	cfg.Downloads.Dir = dir

	r := gin.New()
	registerDownloadRoutes(r.Group("/api"), cfg)
	return r
}

// prepareDownloadsDir 建好下载目录并写入指定文件（files 为空即只建空目录），返回该目录。
func prepareDownloadsDir(t *testing.T, dir string, files ...string) string {
	t.Helper()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建下载目录失败: %v", err)
	}
	for _, name := range files {
		writeFile(t, filepath.Join(dir, name), segmenterDownloadBody[name])
	}
	return dir
}

// fetchSegmenterDownloads 请求清单端点并解出结构体（顺带断言 HTTP 200）。
func fetchSegmenterDownloads(t *testing.T, r *gin.Engine) (*httptest.ResponseRecorder, SegmenterDownloads) {
	t.Helper()

	w := doGet(t, r, "/api/downloads/segmenter")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/downloads/segmenter 应当 200，实际 %d（正文 %q）", w.Code, w.Body.String())
	}

	var got SegmenterDownloads
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("清单不是合法 JSON: %v（正文 %q）", err, w.Body.String())
	}
	return w, got
}

// TestSegmenterDownloadsListsExistingFiles 覆盖正常态：5 个都在时，
// 条数、顺序、bytes、sha256、url 全部要与磁盘事实一致。
func TestSegmenterDownloadsListsExistingFiles(t *testing.T) {
	names := make([]string, 0, len(segmenterDownloadTargets))
	for _, target := range segmenterDownloadTargets {
		names = append(names, target.file)
	}
	dir := prepareDownloadsDir(t, t.TempDir(), names...)
	r := newTestDownloadsEngine(t, dir)

	w, got := fetchSegmenterDownloads(t, r)

	if got.Version != "1" {
		t.Fatalf("清单版本应为 \"1\"，实际 %q", got.Version)
	}
	if len(got.Platforms) != len(segmenterDownloadTargets) {
		t.Fatalf("平台条数应为 %d，实际 %d（正文 %q）", len(segmenterDownloadTargets), len(got.Platforms), w.Body.String())
	}

	for i, target := range segmenterDownloadTargets {
		p := got.Platforms[i]

		// 顺序固定：windows/amd64、linux/amd64、linux/arm64、darwin/amd64、darwin/arm64。
		if p.OS != target.os || p.Arch != target.arch {
			t.Fatalf("第 %d 条平台顺序不对：期望 %s/%s，实际 %s/%s", i, target.os, target.arch, p.OS, p.Arch)
		}
		if p.File != target.file {
			t.Fatalf("第 %d 条文件名不对：期望 %q，实际 %q", i, target.file, p.File)
		}
		if want := "/downloads/" + target.file; p.URL != want {
			t.Fatalf("%s 的 url 应为 %q，实际 %q", target.file, want, p.URL)
		}

		// bytes 必须与 os.Stat 一致。
		st, err := os.Stat(filepath.Join(dir, target.file))
		if err != nil {
			t.Fatalf("读 %s 的状态失败: %v", target.file, err)
		}
		if p.Bytes != st.Size() {
			t.Fatalf("%s 的 bytes 应为 %d，实际 %d", target.file, st.Size(), p.Bytes)
		}

		// sha256 必须与 sha256.Sum256 一致，且是小写十六进制。
		sum := sha256.Sum256([]byte(segmenterDownloadBody[target.file]))
		if want := hex.EncodeToString(sum[:]); p.SHA256 != want {
			t.Fatalf("%s 的 sha256 应为 %s，实际 %s", target.file, want, p.SHA256)
		}
		if p.SHA256 != strings.ToLower(p.SHA256) {
			t.Fatalf("%s 的 sha256 必须是小写十六进制，实际 %q", target.file, p.SHA256)
		}
	}
}

// TestSegmenterDownloadsEmptyWhenDirMissing 锁定关键取舍：目录不存在是 200 + 空数组，
// 绝不是 500；空数组必须序列化成 []，null 会让客户端多一条分支。
func TestSegmenterDownloadsEmptyWhenDirMissing(t *testing.T) {
	// 刻意不建这个目录（没跑过 scripts/build-segmenter.ps1 的形态）。
	dir := filepath.Join(t.TempDir(), "not-built-yet")
	if _, err := os.Stat(dir); err == nil {
		t.Fatalf("前置条件不成立：%s 不应存在", dir)
	}
	r := newTestDownloadsEngine(t, dir)

	w, got := fetchSegmenterDownloads(t, r)

	if got.Version != "1" {
		t.Fatalf("目录缺失时版本仍应为 \"1\"，实际 %q", got.Version)
	}
	if got.Platforms == nil {
		t.Fatal("platforms 不能是 nil（会被序列化成 null），必须是空数组")
	}
	if len(got.Platforms) != 0 {
		t.Fatalf("目录缺失时平台列表应为空，实际 %d 条", len(got.Platforms))
	}
	if !strings.Contains(w.Body.String(), `"platforms":[]`) {
		t.Fatalf("正文里应当是 \"platforms\":[]，实际 %q", w.Body.String())
	}
}

// TestSegmenterDownloadsEmptyDir 覆盖"目录存在但一个文件都没有"。
func TestSegmenterDownloadsEmptyDir(t *testing.T) {
	r := newTestDownloadsEngine(t, prepareDownloadsDir(t, t.TempDir()))

	w, got := fetchSegmenterDownloads(t, r)

	if got.Platforms == nil || len(got.Platforms) != 0 {
		t.Fatalf("空目录时平台列表应为空数组，实际 %#v", got.Platforms)
	}
	if !strings.Contains(w.Body.String(), `"platforms":[]`) {
		t.Fatalf("正文里应当是 \"platforms\":[]，实际 %q", w.Body.String())
	}
}

// TestSegmenterDownloadsPartialFiles 覆盖部分缺失：只列出存在的那些，不报错、不留空洞。
func TestSegmenterDownloadsPartialFiles(t *testing.T) {
	// 故意跳过 linux/arm64 与 darwin/arm64，并打乱写入顺序。
	present := []string{
		"segmenter-darwin-amd64",
		"segmenter-windows-amd64.exe",
		"segmenter-linux-amd64",
	}
	r := newTestDownloadsEngine(t, prepareDownloadsDir(t, t.TempDir(), present...))

	_, got := fetchSegmenterDownloads(t, r)

	if len(got.Platforms) != len(present) {
		t.Fatalf("平台条数应为 %d，实际 %d", len(present), len(got.Platforms))
	}
	// 顺序仍然按固定平台表，而不是目录里的写入顺序。
	wantOSArch := []string{"windows/amd64", "linux/amd64", "darwin/amd64"}
	for i, want := range wantOSArch {
		if actual := got.Platforms[i].OS + "/" + got.Platforms[i].Arch; actual != want {
			t.Fatalf("第 %d 条平台应为 %s，实际 %s", i, want, actual)
		}
	}
}

// TestSegmenterDownloadsRejectsNonGet 非 GET/HEAD 必须被拒，且给中文提示。
func TestSegmenterDownloadsRejectsNonGet(t *testing.T) {
	r := newTestDownloadsEngine(t, prepareDownloadsDir(t, t.TempDir(), "segmenter-linux-amd64"))

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/api/downloads/segmenter", nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusNotFound {
				t.Fatalf("%s /api/downloads/segmenter 应当 404，实际 %d", method, w.Code)
			}
			if !strings.Contains(w.Body.String(), "只支持 GET/HEAD") {
				t.Fatalf("%s 的拒绝提示应为中文说明，实际 %q", method, w.Body.String())
			}
		})
	}
}

// TestSegmenterDownloadsHead 覆盖 HEAD：拿得到 200，但不生成正文。
func TestSegmenterDownloadsHead(t *testing.T) {
	r := newTestDownloadsEngine(t, prepareDownloadsDir(t, t.TempDir(), "segmenter-linux-arm64"))

	req := httptest.NewRequest(http.MethodHead, "/api/downloads/segmenter", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HEAD /api/downloads/segmenter 应当 200，实际 %d", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("HEAD 不应有正文，实际 %q", w.Body.String())
	}
	if cc := w.Header().Get("Cache-Control"); cc != noCacheControl {
		t.Fatalf("HEAD 的 Cache-Control 应为 %q，实际 %q", noCacheControl, cc)
	}
}

// TestSegmenterDownloadsIgnoresNonRegularFile 同名目录不算数：
// 只有普通文件才进清单（否则客户端会拿到一个下不动的 url）。
func TestSegmenterDownloadsIgnoresNonRegularFile(t *testing.T) {
	dir := prepareDownloadsDir(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(dir, "segmenter-linux-amd64"), 0o755); err != nil {
		t.Fatalf("建同名目录失败: %v", err)
	}
	r := newTestDownloadsEngine(t, dir)

	_, got := fetchSegmenterDownloads(t, r)
	if len(got.Platforms) != 0 {
		t.Fatalf("同名目录不应进清单，实际 %#v", got.Platforms)
	}
}
