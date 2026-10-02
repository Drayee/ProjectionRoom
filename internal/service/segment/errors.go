package segment

import (
	"errors"
	"fmt"
	"time"
)

// 作业生命周期里会用到的哨兵错误。
// handler 用 errors.Is 把它们映射成 HTTP 状态码，因此这里只保留"语义"，不掺 HTTP。
var (
	// ErrQueueFull 表示排队队列已满（第 QueueLength+1 个请求）。
	ErrQueueFull = errors.New("segment: 排队队列已满")
	// ErrRateLimited 表示令牌桶已空（提交过于频繁）。
	ErrRateLimited = errors.New("segment: 提交过于频繁")
	// ErrSourceEmpty 表示上传的文件是 0 字节。
	ErrSourceEmpty = errors.New("segment: 上传的文件为空")
	// ErrSourceTooLarge 表示源文件超过 MaxSourceBytes。
	ErrSourceTooLarge = errors.New("segment: 源文件超过大小上限")
	// ErrDurationTooLong 表示源视频时长超过 MaxDuration。
	ErrDurationTooLong = errors.New("segment: 源视频超过时长上限")
	// ErrProbeFailed 表示 ffprobe 无法解析上传的文件。
	ErrProbeFailed = errors.New("segment: 无法解析上传的视频文件")
	// ErrJobNotFound 表示作业不存在（或已被 TTL 清理）。
	ErrJobNotFound = errors.New("segment: 作业不存在")
	// ErrJobNotReady 表示作业尚未产出可用结果。
	ErrJobNotReady = errors.New("segment: 作业尚未完成")
	// ErrNoParts 表示该作业走单次返回，没有分批下载。
	ErrNoParts = errors.New("segment: 该作业走单次返回，没有分批下载")
	// ErrPartNotFound 表示分批编号不存在。
	ErrPartNotFound = errors.New("segment: 分批编号不存在")
	// ErrTooBigForParts 表示单个产物文件本身就超过单份上限，无法分批下载。
	ErrTooBigForParts = errors.New("segment: 单个产物文件超过单份上限")
)

// ErrFFmpegMissing 是"服务器没有 ffmpeg"时给用户的明确指引。
// 它不重试、不降级：本地照样能用 cmd/segmenter 或 ffmpeg 自行切片。
var ErrFFmpegMissing = errors.New(
	"服务器未安装 ffmpeg，你也可以在本地用 segmenter/ffmpeg 自行切片，见 docs/SEGMENT.md")

// QuotaError 是配额拒绝（队列满 / 令牌桶空）的统一表示。
// RetryAfter 是建议的重试间隔，handler 会把它写进 Retry-After 响应头（整数秒）。
type QuotaError struct {
	Err        error
	RetryAfter time.Duration
}

func (e *QuotaError) Error() string {
	return fmt.Sprintf("%v（建议 %s 后重试）", e.Err, e.RetryAfter.Round(time.Second))
}

func (e *QuotaError) Unwrap() error { return e.Err }
