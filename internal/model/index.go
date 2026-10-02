// 分片索引的数据模型（SPEC §4.3）。
//
// 它是三个地方共用的契约：
//   - cmd/segmenter 生成 index.json；
//   - 服务端校验主播发布的索引并读取 BitrateBps 做容量判定；
//   - 前端 client/src/types/ts 是它的投影。
package model

import (
	"errors"
	"fmt"
	"strings"
)

// InitSegmentIndex 是初始化段保留的索引值。
// Segments 里只放媒体分片，它们的 Index 从 1 开始；0 永远代表 init 段。
const InitSegmentIndex = 0

// Segment 描述一个媒体分片。
type Segment struct {
	// Index 从 1 开始；0 是 init 段（SPEC §4.3）。
	Index int `json:"index"`
	// File 是分片文件名，例如 "c00001.m4s"。
	File string `json:"file"`
	// Offset 是该分片在原始 fragmented MP4 中的字节偏移。
	Offset int64 `json:"offset"`
	// Size 是分片字节数。
	Size int64 `json:"size"`
	// Duration 是分片时长（秒），来自 trun 的 sample duration，缺失时用相邻 PTS 差补齐。
	Duration float64 `json:"duration"`
	// StartPTS 是分片在播放时间轴上的起点（秒），来自 tfdt / 视频轨 timescale。
	StartPTS float64 `json:"startPts"`
	// Keyframe 表示这个分片以关键帧开头，可以安全地作为 seek 落点。
	Keyframe bool `json:"keyframe"`
	// SHA256 是分片内容的十六进制摘要，用于生成期校验。
	SHA256 string `json:"sha256"`
}

// Index 是完整的分片索引。
type Index struct {
	Version       int     `json:"version"`
	InitFile      string  `json:"initFile"`
	MimeType      string  `json:"mimeType"`
	TotalDuration float64 `json:"totalDuration"`
	SegmentSec    float64 `json:"segmentSec"`
	// BitrateBps 是整体平均码率，是容量模型与模式判定的唯一输入（SPEC §6.1、§6.2）。
	BitrateBps int64 `json:"bitrateBps"`
	// TotalBytes 是原始文件字节数。
	TotalBytes int64 `json:"totalBytes"`
	// Segments 只包含媒体分片（Index 从 1 开始）。
	Segments []Segment `json:"segments"`
}

// ErrEmptyIndex 表示索引没有任何媒体分片。
var ErrEmptyIndex = errors.New("media: 索引不包含任何媒体分片")

// Validate 检查索引是否自洽。
// 服务端必须校验主播发布的索引，否则一个畸形索引会让整个房间算错容量或播放器崩溃。
func (i *Index) Validate() error {
	if i.Version <= 0 {
		return fmt.Errorf("media: 索引版本非法 (%d)", i.Version)
	}
	if strings.TrimSpace(i.InitFile) == "" {
		return errors.New("media: 缺少 initFile")
	}
	if !strings.HasPrefix(i.MimeType, "video/") && !strings.HasPrefix(i.MimeType, "audio/") {
		return fmt.Errorf("media: mimeType 非法 (%q)", i.MimeType)
	}
	if i.TotalDuration <= 0 {
		return fmt.Errorf("media: totalDuration 必须为正 (%v)", i.TotalDuration)
	}
	if i.BitrateBps <= 0 {
		return fmt.Errorf("media: bitrateBps 必须为正 (%d)", i.BitrateBps)
	}
	if len(i.Segments) == 0 {
		return ErrEmptyIndex
	}

	for n, seg := range i.Segments {
		if seg.Index != n+1 {
			return fmt.Errorf("media: 第 %d 个分片的 index 应为 %d，实际 %d", n, n+1, seg.Index)
		}
		if seg.Size <= 0 {
			return fmt.Errorf("media: 分片 %d 的 size 非法 (%d)", seg.Index, seg.Size)
		}
		if strings.TrimSpace(seg.File) == "" {
			return fmt.Errorf("media: 分片 %d 缺少文件名", seg.Index)
		}
		if seg.StartPTS < 0 || seg.Duration < 0 {
			return fmt.Errorf("media: 分片 %d 的时间字段非法 (pts=%v, dur=%v)", seg.Index, seg.StartPTS, seg.Duration)
		}
	}

	return nil
}

// MeanSegmentSeconds 返回平均分片时长，用于校验切片目标是否达成。
func (i *Index) MeanSegmentSeconds() float64 {
	if len(i.Segments) == 0 {
		return 0
	}
	var total float64
	for _, seg := range i.Segments {
		total += seg.Duration
	}
	return total / float64(len(i.Segments))
}

// SegmentAt 返回覆盖播放位置 pts（秒）的分片序号（从 1 开始）。
// 播放位置早于第一个分片时返回第一个分片，超出末尾时返回最后一个分片。
func (i *Index) SegmentAt(pts float64) int {
	if len(i.Segments) == 0 {
		return 0
	}
	for _, seg := range i.Segments {
		if pts < seg.StartPTS+seg.Duration {
			return seg.Index
		}
	}
	return i.Segments[len(i.Segments)-1].Index
}
