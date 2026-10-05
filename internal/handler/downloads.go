package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
)

// 客户端切片器二进制的只读清单。
//
// 为什么只是一个「清单」：浏览器生成的一键切片脚本需要知道去哪儿下载 cmd/segmenter
// 的交叉编译产物，但它不该经 gin 转发几十 MB 的字节流。文件由静态托管
// （static.go 的 NoRoute → client/dist 下的真实文件）送出，这里只回答
// 「有哪些文件、多大、sha256 是多少」，两者指向同一个目录（config.DownloadsDir()）。

// downloadsVersion 是清单的协议版本。
// 形状一旦出现不兼容改动就把它 +1，而不是偷偷改字段含义（客户端按版本分流）。
const downloadsVersion = "1"

// downloadsURLPrefix 是二进制的静态托管前缀，与磁盘上的下载目录一一对应。
const downloadsURLPrefix = "/downloads/"

// downloadsBuildHint 是缺文件时给运维/开发者看的中文提示。
const downloadsBuildHint = "请先执行 powershell -File scripts/build-segmenter.ps1"

// segmenterDownloadTarget 描述一个要发布的交叉编译目标。
// 列表顺序即清单里的顺序：客户端按这个固定顺序挑选平台，因此不可随意重排。
type segmenterDownloadTarget struct {
	os   string
	arch string
	file string
}

// segmenterDownloadTargets 是固定的 5 个发布目标，顺序固定（windows/amd64 打头）。
var segmenterDownloadTargets = []segmenterDownloadTarget{
	{os: "windows", arch: "amd64", file: "segmenter-windows-amd64.exe"},
	{os: "linux", arch: "amd64", file: "segmenter-linux-amd64"},
	{os: "linux", arch: "arm64", file: "segmenter-linux-arm64"},
	{os: "darwin", arch: "amd64", file: "segmenter-darwin-amd64"},
	{os: "darwin", arch: "arm64", file: "segmenter-darwin-arm64"},
}

// SegmenterDownload 是清单里的一条平台记录。
// 字段名与 JSON tag 是客户端解析的契约，改名等于破坏兼容。
type SegmenterDownload struct {
	// OS 是目标操作系统（windows/linux/darwin）。
	OS string `json:"os"`
	// Arch 是目标架构（amd64/arm64）。
	Arch string `json:"arch"`
	// File 是磁盘上的文件名，同时也是 /downloads/ 下的文件名。
	File string `json:"file"`
	// URL 是静态托管的下载地址，恒为 /downloads/<File>（相对路径，同源）。
	URL string `json:"url"`
	// Bytes 是文件字节数，构造清单时扫描一次并缓存。
	Bytes int64 `json:"bytes"`
	// SHA256 是文件内容的小写十六进制摘要，供客户端校验下载完整性。
	SHA256 string `json:"sha256"`
}

// SegmenterDownloads 是 GET /api/downloads/segmenter 的响应体。
type SegmenterDownloads struct {
	// Version 是清单协议版本。
	Version string `json:"version"`
	// Platforms 是可用平台列表；一个文件都没有时为空数组（绝不是 null，也不是 404）。
	Platforms []SegmenterDownload `json:"platforms"`
}

// newSegmenterDownloads 扫描下载目录并构造清单（在注册路由时调用一次）。
//
// 这里刻意把 size 与 sha256 一次算完缓存起来：5 个二进制合计几十 MB，
// 每个请求都重算等于每次请求都把磁盘读一遍。
// 目录/文件缺失都只是「该平台不可用」，不是错误：没跑过构建脚本时端点照常返回空清单。
func newSegmenterDownloads(dir string) SegmenterDownloads {
	d := SegmenterDownloads{
		Version: downloadsVersion,
		// 非 nil 空切片：JSON 里必须序列化成 []，null 会让客户端解析逻辑多一条分支。
		Platforms: []SegmenterDownload{},
	}

	dir = strings.TrimSpace(dir)
	if dir == "" {
		log.Printf("[WARN] 切片器下载目录为空（PR_DOWNLOADS_DIR 与 PR_STATIC_DIR 都没配）：/api/downloads/segmenter 将返回空清单")
		return d
	}

	switch st, err := os.Stat(dir); {
	case err != nil:
		log.Printf("[WARN] 切片器下载目录不可用（%s）：%v；/api/downloads/segmenter 将返回空清单，%s", dir, err, downloadsBuildHint)
		return d
	case !st.IsDir():
		log.Printf("[WARN] 切片器下载路径不是目录（%s）：/api/downloads/segmenter 将返回空清单", dir)
		return d
	}

	var missing []string
	for _, t := range segmenterDownloadTargets {
		full := filepath.Join(dir, t.file)

		st, err := os.Stat(full)
		if err != nil || !st.Mode().IsRegular() {
			missing = append(missing, t.file)
			continue
		}

		sum, err := fileSHA256(full)
		if err != nil {
			log.Printf("[WARN] 计算切片器二进制的 sha256 失败（%s）：%v；该平台不进清单", full, err)
			missing = append(missing, t.file)
			continue
		}

		d.Platforms = append(d.Platforms, SegmenterDownload{
			OS:     t.os,
			Arch:   t.arch,
			File:   t.file,
			URL:    downloadsURLPrefix + t.file,
			Bytes:  st.Size(),
			SHA256: sum,
		})
	}

	// 只报「缺了哪些」一行，不逐个刷屏；服务照常可用（有就发布，没有就跳过）。
	if len(missing) > 0 {
		log.Printf("[WARN] 切片器下载目录 %s 缺少 %d 个二进制：%s；%s",
			dir, len(missing), strings.Join(missing, ", "), downloadsBuildHint)
	}
	log.Printf("切片器下载清单已就绪：%d/%d 个平台可用，目录 %s",
		len(d.Platforms), len(segmenterDownloadTargets), dir)

	return d
}

// fileSHA256 用流式方式算文件摘要（二进制几十 MB，不整个读进内存）。
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// downloadsHandler 返回已经算好的清单。
//
// 只接受 GET/HEAD：这是纯只读端点，其它方法一律 404 + 中文提示（与 static.go 同风格）。
// 缓存策略用 no-cache：重新交叉编译之后清单要立刻可见，不能让客户端拿到旧的 sha256。
func downloadsHandler(d SegmenterDownloads) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !isGetOrHead(c.Request.Method) {
			notFound(c, "切片器下载清单只支持 GET/HEAD")
			return
		}

		c.Header("Cache-Control", noCacheControl)
		if c.Request.Method == http.MethodHead {
			// HEAD 只要头：正文由 http 层丢弃，这里干脆不生成。
			c.Header("Content-Type", "application/json; charset=utf-8")
			c.Status(http.StatusOK)
			return
		}
		c.JSON(http.StatusOK, d)
	}
}

// registerDownloadRoutes 注册切片器二进制的只读清单端点。
//
// 注意边界：这条路由只回 JSON。二进制本身由静态托管（/downloads/<file>）送出，
// 所以它必须在 registerStatic 之前注册，保住「业务路由全部先注册」的既有约定。
func registerDownloadRoutes(api *gin.RouterGroup, cfg *config.Config) {
	// 用 Any 而不是 GET：非 GET/HEAD 才能走到我们的中文提示，而不是引擎默认的 404 页。
	api.Any("/downloads/segmenter", downloadsHandler(newSegmenterDownloads(cfg.DownloadsDir())))
}
