package handler

import (
	"errors"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/storage"
)

// 上传相关的错误码（与既有 code* 常量的命名口径一致）。
const (
	codeUploadUnavailable = "upload_unavailable"
	codeUploadBadType     = "upload_bad_type"
	codeUploadTooLarge    = "upload_too_large"
	codeUploadEmpty       = "upload_empty"
	codeUploadFailed      = "upload_failed"
)

// registerUploadRoutes 注册图片上传端点。
//
// 与账号层"能力关闭即不注册"同一条约定：对象存储未配置时**不注册**这条路由，
// 请求落到 404（而不是 503）。理由：404 是"这个功能不存在"，
// 503 是"它现在坏了"—— 对未启用该能力的部署，前者才是事实。
//
// 需要登录：上传是**写操作**，且会占用存储配额。游客不能往桶里写东西。
func registerUploadRoutes(api *gin.RouterGroup, store *storage.Client, accountSvc AccountService) {
	if store == nil || !store.Enabled() {
		return
	}
	api.POST("/uploads", RequireAuth(accountSvc), uploadImageHandler(store))
}

// uploadImageHandler 接收一张图片并存入对象存储，返回公开 URL。
//
// # 为什么用流式读取而不是 c.FormFile
//
// c.FormFile 会把整个文件先落到磁盘（或内存）再交给我们，于是"限制 10 MiB"
// 变成"先让 10 GiB 的请求把盘写满，然后才拒绝"。这里用 MultipartReader
// 直接拿 part 的流，配合 http.MaxBytesReader 在**读的过程中**就截断。
// 同 internal/handler/segment.go 的 openFilePart 的理由。
//
// # 为什么只收一张
//
// 多图上传在客户端循环调用即可（并发 3~4 个）。服务端做批量会让"部分失败
// 怎么回滚"变成一个没有正确答案的问题 —— 而它换不到任何性能（瓶颈在带宽）。
func uploadImageHandler(store *storage.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 在解析前就套上硬上限：MaxBytesReader 会在读到超限时返回错误，
		// 而不是等到全部读完才发现。多留 1 MiB 给 multipart 的边界与头部开销。
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, storage.MaxImageBytes+1<<20)

		reader, err := c.Request.MultipartReader()
		if err != nil {
			uploadError(c, http.StatusBadRequest, codeUploadFailed,
				"请求必须是 multipart/form-data")
			return
		}

		part, err := nextFilePart(reader)
		if err != nil {
			if errors.Is(err, errNoFilePart) {
				uploadError(c, http.StatusBadRequest, codeUploadFailed, "缺少 file 字段")
				return
			}
			uploadError(c, http.StatusBadRequest, codeUploadFailed, "读取上传内容失败")
			return
		}
		defer func() { _ = part.Close() }()

		// part.Header.Get("Content-Type") 是**客户端声明**的，不可信。
		// 但在这里做完整的内容嗅探（读前 512 字节）会让流式上传退化成
		// "先缓冲再判断"。取舍：声明类型只用于**白名单初筛**（挡住明显的错误），
		// 真正的保障是"只存位图 + 匿名只读 + 不走同源执行"这三条。
		// 若将来要支持 SVG 或需要更强的保证，必须改成嗅探 + 转码。
		mime := part.Header.Get("Content-Type")

		// ⚠ 这里**不能**用 c.Request.ContentLength：那是整个 multipart 请求的长度
		// （含 boundary、各段头、其它表单字段），比文件本身大得多。
		// 用它当 PutObject 的 size 会让 SDK 期待 N 字节却只读到 M 字节，
		// 报 "ContentLength=N with Body length M" 并 500。
		// 传 -1 让 SDK 自己分块上传（它会边读边发，不需要预先知道长度）。
		size := int64(-1)

		url, err := store.PutImage(c.Request.Context(), part, size, mime)
		switch {
		case err == nil:
			c.JSON(http.StatusCreated, gin.H{"url": url})

		case errors.Is(err, storage.ErrUnsupportedType):
			uploadError(c, http.StatusUnsupportedMediaType, codeUploadBadType,
				"只支持 JPEG / PNG / GIF / WebP 图片")

		case errors.Is(err, storage.ErrTooLarge):
			uploadError(c, http.StatusRequestEntityTooLarge, codeUploadTooLarge,
				"图片不能超过 "+strconv.Itoa(storage.MaxImageBytes>>20)+" MiB")

		case errors.Is(err, storage.ErrUnavailable):
			uploadError(c, http.StatusServiceUnavailable, codeUploadUnavailable,
				"文件存储未启用")

		default:
			// 真实错误只进日志，不回给客户端：它可能含内网地址与凭据线索。
			log.Printf("[WARN] 图片上传失败：%v", err)
			uploadError(c, http.StatusInternalServerError, codeUploadFailed,
				"上传失败，请稍后重试")
		}
	}
}

// errNoFilePart 表示 multipart 里没有名为 file 的段。
var errNoFilePart = errors.New("multipart 缺少 file 字段")

// nextFilePart 从 multipart reader 里取出名为 file 的段。
//
// 跳过其它段（表单里可能还有 title 之类的字段），并关闭它们 ——
// 不关闭会让底层连接无法复用。
func nextFilePart(reader *multipart.Reader) (*multipart.Part, error) {
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return nil, errNoFilePart
		}
		if err != nil {
			return nil, err
		}
		if part.FormName() == "file" {
			// 空文件在 PutImage 里会因大小检查而失败，这里不额外判
			return part, nil
		}
		_ = part.Close()
	}
}

// uploadError 输出统一形状的错误响应。
func uploadError(c *gin.Context, status int, code, msg string) {
	c.JSON(status, gin.H{"error": msg, "code": code})
}
