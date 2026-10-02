package handler

import (
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"mime/multipart"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/model"
	"ProjectionRoom/internal/service/segment"
)

// 切片服务专有的错误码。命名风格与 internal/model 的 CodeXxx 一致；
// 它们只出现在 REST 端点上，因此不往 WebSocket 的消息契约（internal/model）里塞。
const (
	codeSegmentQueueFull    = "SEGMENT_QUEUE_FULL"
	codeSegmentRateLimited  = "SEGMENT_RATE_LIMITED"
	codeSegmentJobNotFound  = "SEGMENT_JOB_NOT_FOUND"
	codeSegmentJobNotReady  = "SEGMENT_JOB_NOT_READY"
	codeSegmentSourceTooBig = "SEGMENT_SOURCE_TOO_LARGE"
	codeSegmentSourceEmpty  = "SEGMENT_SOURCE_EMPTY"
	codeSegmentTooLong      = "SEGMENT_DURATION_TOO_LONG"
	codeSegmentProbeFailed  = "SEGMENT_PROBE_FAILED"
	codeSegmentNoParts      = "SEGMENT_NO_PARTS"
	codeSegmentPartNotFound = "SEGMENT_PART_NOT_FOUND"
)

// segmentAPIPrefix 是切片端点的公共前缀，分批下载 URL 由它拼出来。
// 它与下面注册的路由必须一致（这是 manifest 里 url 字段的唯一来源）。
const segmentAPIPrefix = "/api/v1/segment/"

// registerSegmentRoutes 注册服务端切片端点。
//
// seg 为 nil 时整体跳过注册：单测可以只装配自己关心的那几条路由。
// 正常装配（cmd/wire_gen.go）必须传入真实 Queue，否则线上就没有这组端点。
func registerSegmentRoutes(api *gin.RouterGroup, seg *segment.Queue) {
	if seg == nil {
		return
	}

	v1 := api.Group("/v1/segment")
	v1.POST("/jobs", segmentSubmitHandler(seg))
	v1.GET("/jobs/:id", segmentJobHandler(seg))
	v1.GET("/jobs/:id/result", segmentResultHandler(seg))
	v1.GET("/jobs/:id/parts/:n", segmentPartHandler(seg))
}

// segmentError 写出与 errors.go 同一形状的统一错误体：{error, code}。
func segmentError(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, gin.H{"error": message, "code": code})
}

// segmentSubmitHandler 处理 multipart/form-data 上传（字段名 file）。
// 成功返回 202 + {jobId, state, queuePosition}。
func segmentSubmitHandler(seg *segment.Queue) gin.HandlerFunc {
	return func(c *gin.Context) {
		part, err := openFilePart(c.Request)
		if err != nil {
			segmentError(c, http.StatusBadRequest, model.CodeBadRequest, err.Error())
			return
		}
		defer func() { _ = part.Close() }()

		view, err := seg.Submit(part.FileName(), part, c.Request.ContentLength)
		if err != nil {
			writeSegmentSubmitError(c, err)
			return
		}

		body := gin.H{
			"jobId":         view.JobID,
			"state":         view.State,
			"progress":      view.Progress,
			"queuePosition": view.QueuePosition,
		}
		// 服务器没有 ffmpeg 时作业会立即失败，原因必须原样回给用户。
		if view.Error != "" {
			body["error"] = view.Error
		}
		c.JSON(http.StatusAccepted, body)
	}
}

// openFilePart 用流式方式取出 multipart 里名为 file 的那一段。
// 刻意不用 c.FormFile：那会把整个上传落到磁盘（或内存）之后才轮到我们检查配额与大小，
// 16GiB 的请求会先被写满一次盘。
func openFilePart(r *http.Request) (*multipart.Part, error) {
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, fmt.Errorf("请求不是合法的 multipart/form-data: %v", err)
	}

	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("multipart 表单缺少 file 字段")
		}
		if err != nil {
			return nil, fmt.Errorf("读取 multipart 失败: %v", err)
		}
		if part.FormName() == "file" {
			return part, nil
		}
		_ = part.Close()
	}
}

// writeSegmentSubmitError 把提交阶段的领域错误映射成 HTTP 状态码与错误码。
func writeSegmentSubmitError(c *gin.Context, err error) {
	var quota *segment.QuotaError
	switch {
	case errors.As(err, &quota):
		seconds := int(math.Ceil(quota.RetryAfter.Seconds()))
		if seconds < 1 {
			seconds = 1
		}
		c.Header("Retry-After", strconv.Itoa(seconds))
		code := codeSegmentQueueFull
		if errors.Is(quota.Err, segment.ErrRateLimited) {
			code = codeSegmentRateLimited
		}
		segmentError(c, http.StatusTooManyRequests, code, quota.Error())

	case errors.Is(err, segment.ErrSourceTooLarge):
		segmentError(c, http.StatusRequestEntityTooLarge, codeSegmentSourceTooBig, err.Error())

	case errors.Is(err, segment.ErrSourceEmpty):
		segmentError(c, http.StatusUnprocessableEntity, codeSegmentSourceEmpty, err.Error())

	case errors.Is(err, segment.ErrDurationTooLong):
		segmentError(c, http.StatusUnprocessableEntity, codeSegmentTooLong, err.Error())

	case errors.Is(err, segment.ErrProbeFailed):
		segmentError(c, http.StatusUnprocessableEntity, codeSegmentProbeFailed, err.Error())

	default:
		log.Printf("segment: 提交作业失败: %v", err)
		segmentError(c, http.StatusInternalServerError, model.CodeInternalError, "服务端内部错误")
	}
}

// segmentJobHandler 返回作业状态：{jobId, state, progress, error?, result?}。
func segmentJobHandler(seg *segment.Queue) gin.HandlerFunc {
	return func(c *gin.Context) {
		view, ok := seg.Get(c.Param("id"))
		if !ok {
			segmentError(c, http.StatusNotFound, codeSegmentJobNotFound, "作业不存在")
			return
		}
		fillPartURLs(view)
		c.JSON(http.StatusOK, view)
	}
}

// segmentResultHandler 交付产物：
//   - 总大小 ≤ 单次上限 → 200 + 流式 zip（index.json、init.mp4、全部分片）；
//   - 总大小 > 单次上限 → 200 + application/json 的 manifest（含每份 url/bytes/sha256）。
func segmentResultHandler(seg *segment.Queue) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")

		view, ok := seg.Get(id)
		if !ok {
			segmentError(c, http.StatusNotFound, codeSegmentJobNotFound, "作业不存在")
			return
		}
		if view.State != segment.StateDone {
			segmentError(c, http.StatusConflict, codeSegmentJobNotReady, notReadyMessage(view))
			return
		}

		if view.Result != nil && !view.Result.SingleResponse {
			fillPartURLs(view)
			c.JSON(http.StatusOK, gin.H{
				"jobId":          id,
				"bytes":          view.Result.Bytes,
				"segments":       view.Result.Segments,
				"singleResponse": false,
				"parts":          view.Result.Parts,
			})
			return
		}

		dir, files, err := seg.Artifacts(id)
		if err != nil {
			writeSegmentArtifactError(c, err)
			return
		}
		writeZipStream(c, id, dir, files, "room-media-"+id+".zip", "", 0)
	}
}

// segmentPartHandler 下载分批结果中的第 n 份（同样是 zip，内含这一份覆盖的产物文件）。
func segmentPartHandler(seg *segment.Queue) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		n, err := strconv.Atoi(c.Param("n"))
		if err != nil || n < 1 {
			segmentError(c, http.StatusNotFound, codeSegmentPartNotFound, "分批编号不存在")
			return
		}

		dir, files, part, err := seg.PartFiles(id, n)
		if err != nil {
			writeSegmentArtifactError(c, err)
			return
		}
		writeZipStream(c, id, dir, files, fmt.Sprintf("room-media-%s-part%03d.zip", id, n), part.SHA256, part.Bytes)
	}
}

// writeSegmentArtifactError 映射产物读取阶段的错误。
func writeSegmentArtifactError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, segment.ErrJobNotFound):
		segmentError(c, http.StatusNotFound, codeSegmentJobNotFound, "作业不存在")
	case errors.Is(err, segment.ErrNoParts):
		segmentError(c, http.StatusNotFound, codeSegmentNoParts, err.Error())
	case errors.Is(err, segment.ErrPartNotFound):
		segmentError(c, http.StatusNotFound, codeSegmentPartNotFound, err.Error())
	case errors.Is(err, segment.ErrJobNotReady):
		segmentError(c, http.StatusConflict, codeSegmentJobNotReady, err.Error())
	default:
		log.Printf("segment: 读取产物失败: %v", err)
		segmentError(c, http.StatusInternalServerError, model.CodeInternalError, "服务端内部错误")
	}
}

// writeZipStream 把产物以 zip 流的形式写出去。
// length > 0 时预先声明 Content-Length（分批下载知道确切字节数），便于前端显示进度。
func writeZipStream(c *gin.Context, jobID, dir string, files []segment.ArtifactFile,
	filename, sha256 string, length int64) {

	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	if length > 0 {
		c.Header("Content-Length", strconv.FormatInt(length, 10))
	}
	if sha256 != "" {
		c.Header("X-Segment-Sha256", sha256)
	}
	c.Status(http.StatusOK)

	if _, err := segment.WriteZip(c.Writer, dir, files); err != nil {
		// 响应头已经发出，改不了状态码：记录并断开，让客户端看到截断的 zip。
		log.Printf("segment: 打包作业 %s 失败: %v", jobID, err)
		c.Abort()
	}
}

// fillPartURLs 给 manifest 里的每一份补上下载地址。
// Result 是与作业共享的只读快照，因此先复制再改，绝不能就地改坏队列里的记录。
func fillPartURLs(view *segment.View) {
	if view == nil || view.Result == nil || len(view.Result.Parts) == 0 {
		return
	}

	copied := *view.Result
	copied.Parts = make([]segment.Part, len(view.Result.Parts))
	copy(copied.Parts, view.Result.Parts)
	for i := range copied.Parts {
		copied.Parts[i].URL = fmt.Sprintf("%sjobs/%s/parts/%d", segmentAPIPrefix, view.JobID, copied.Parts[i].N)
	}
	view.Result = &copied
}

func notReadyMessage(view *segment.View) string {
	if view.State == segment.StateFailed {
		return "作业失败：" + view.Error
	}
	return fmt.Sprintf("作业尚未完成（当前状态 %s，进度 %.0f%%）", view.State, view.Progress*100)
}
