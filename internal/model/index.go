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
//
// File / Offset 的含义取决于 Index.Packs：
//   - 未打包：File 是分片自己的文件名（"c00001.m4s"），Offset 是它在原始
//     fragmented MP4 里的字节偏移；
//   - 打包：File 是包含该分片的包（"pack-0001.bin"），Offset 是它在包内的字节偏移。
//
// 两种形态下 Size 都恒等于"这一片的字节数"，SHA256 也始终是对**分片**求的摘要
// （不是对包），因此逐片校验在两种布局下完全一致。
type Segment struct {
	// Index 从 1 开始；0 是 init 段（SPEC §4.3）。
	Index int `json:"index"`
	// File 是包含该分片的文件名："c00001.m4s"（未打包）或 "pack-0001.bin"（打包）。
	File string `json:"file"`
	// Offset 是该分片在其所属文件中的字节偏移。
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

// Pack 描述一个分片包（把连续的若干个分片合成一个 ".bin"）。
//
// 为什么要有它：Windows 上"每个文件一次写盘"有固定开销（Chrome 先写同目录 swap 文件
// 再改名、杀软逐个扫描），1800 个小分片的写入要好几分钟。每 N 片合成一个包，
// 产物文件数降到十几个，写入/下载/解压都跟着变快。
type Pack struct {
	// File 是包文件名，例如 "pack-0001.bin"。
	File string `json:"file"`
	// FirstSegment 是包里第一个分片的序号（从 1 开始，与 Segment.Index 同源）。
	FirstSegment int `json:"firstSegment"`
	// Count 是包里包含的分片个数。
	Count int `json:"count"`
	// Bytes 是包的字节数（等于包内各分片 Size 之和）。
	Bytes int64 `json:"bytes"`
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
	// Packs 是分片打包清单，可选。为空（省略）表示"一片一个文件"的经典布局，
	// 此时 Segments[i].Offset 是分片在原始视频里的偏移；非空时
	// Segments[i].File/Offset 指向所属包与包内偏移（历史字段含义不变）。
	// 客户端与服务端都必须同时支持两种形态。
	Packs []Pack `json:"packs,omitempty"`
}

// Packed 报告索引是否使用打包布局。
func (i *Index) Packed() bool { return len(i.Packs) > 0 }

// FileCount 返回索引描述的产物文件个数（不含 index.json 与 init.mp4）：
// 打包时是包数，未打包时是分片数。注意它与"分片数"（len(Segments)）是两个概念。
func (i *Index) FileCount() int {
	if i.Packed() {
		return len(i.Packs)
	}
	return len(i.Segments)
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

	if i.Packed() {
		if err := i.validatePacks(); err != nil {
			return err
		}
	}

	return nil
}

// validatePacks 校验打包布局的自洽性。
//
// 为什么必须校验：包内偏移与分片大小是这个格式里唯一"能被写错而播放器看不出来"的地方 ——
// 偏移错了客户端会切出半片数据，MSE 只会在 append 时报一个看不懂的错。
// 判据：包按序覆盖全部分片（不重不漏）、分片落在自己所属的包里、
// 包内偏移递增且不重叠、且不越过 pack.Bytes。
func (i *Index) validatePacks() error {
	seen := make(map[string]struct{}, len(i.Packs))
	next := 1

	for n, pack := range i.Packs {
		if strings.TrimSpace(pack.File) == "" {
			return fmt.Errorf("media: 第 %d 个 pack 缺少文件名", n+1)
		}
		if _, dup := seen[pack.File]; dup {
			return fmt.Errorf("media: pack 文件名重复 (%q)", pack.File)
		}
		seen[pack.File] = struct{}{}

		if pack.FirstSegment != next {
			return fmt.Errorf("media: 第 %d 个 pack 的 firstSegment 应为 %d，实际 %d",
				n+1, next, pack.FirstSegment)
		}
		if pack.Count <= 0 {
			return fmt.Errorf("media: pack %s 的 count 非法 (%d)", pack.File, pack.Count)
		}
		if pack.Bytes <= 0 {
			return fmt.Errorf("media: pack %s 的 bytes 非法 (%d)", pack.File, pack.Bytes)
		}
		if end := pack.FirstSegment + pack.Count - 1; end > len(i.Segments) {
			return fmt.Errorf("media: pack %s 覆盖到第 %d 片，但索引只有 %d 片",
				pack.File, end, len(i.Segments))
		}

		// packEnd 是包内已被覆盖到的字节位置：下一片必须从它之后开始（偏移递增、不重叠）。
		var packEnd int64
		for k := pack.FirstSegment; k < pack.FirstSegment+pack.Count; k++ {
			seg := i.Segments[k-1]
			if seg.File != pack.File {
				return fmt.Errorf("media: 分片 %d 的 file 应为 %q，实际 %q", k, pack.File, seg.File)
			}
			if seg.Offset < packEnd {
				return fmt.Errorf("media: 分片 %d 在 %s 内的偏移 %d 与前一片重叠（已覆盖到 %d）",
					k, pack.File, seg.Offset, packEnd)
			}
			if seg.Offset+seg.Size > pack.Bytes {
				return fmt.Errorf("media: 分片 %d 越过 %s 的边界（offset=%d, size=%d, bytes=%d）",
					k, pack.File, seg.Offset, seg.Size, pack.Bytes)
			}
			packEnd = seg.Offset + seg.Size
		}

		next = pack.FirstSegment + pack.Count
	}

	if next != len(i.Segments)+1 {
		return fmt.Errorf("media: packs 只覆盖了 %d 片，索引里有 %d 片", next-1, len(i.Segments))
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
