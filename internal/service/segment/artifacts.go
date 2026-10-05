package segment

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ProjectionRoom/internal/model"
)

// 产物文件名。与 mp4.SplitOptions 的默认值一致，
// 也就是与 cmd/segmenter 的产出一致（前端零改动）。
const (
	InitFileName  = "init.mp4"
	IndexFileName = "index.json"
)

// zipEpoch 是写进 zip 文件头的固定时间戳。
//
// 为什么必须固定：manifest 里的 sha256 是"这一份下载内容"的摘要。
// 如果 zip 头里带上真实修改时间，同一份产物每次打包的字节流都不同，
// 摘要就没法在作业完成时预先算好 —— 要么每次下载都重算（客户端无法校验），
// 要么把第二份产物落到磁盘上（作业最大 16GiB，代价太高）。
var zipEpoch = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// zipPerFileOverhead 是每个 zip 成员的头/中央目录开销估计值。
// 分批时按"文件大小 + 该开销"判断，保证打包出来的整份**严格小于**单份上限。
const zipPerFileOverhead = 512

// ArtifactFile 是一个已落盘的产物文件。
type ArtifactFile struct {
	// Name 是相对产物目录的文件名，例如 index.json / init.mp4 / c00001.m4s / pack-0001.bin。
	Name string `json:"name"`
	// Size 是字节数。
	Size int64 `json:"size"`
	// SHA256 是文件内容的十六进制摘要。
	SHA256 string `json:"sha256"`
}

// Artifacts 是一次切片的全部产物。
type Artifacts struct {
	// Dir 是产物目录。
	Dir string `json:"-"`
	// Index 是切分产生的分片索引（同时已写进 index.json）。
	Index *model.Index `json:"-"`
	// Files 是产物清单，顺序即打包/下载顺序：index.json → init.mp4 → 分片文件。
	//
	// 打包（每 N 片一个 .bin）时这里**按包去重**：一个 pack-0001.bin 只出现一次，
	// 而不是被它包含的 100 个分片重复列 100 次。分批下载与 zip 打包因此天然按包切分，
	// 文件数也从 ~1800 降到 ~18。
	Files []ArtifactFile `json:"files"`
	// TotalBytes 是产物总大小（不含 zip 头开销）。去重后不会重复计入同一个包。
	TotalBytes int64 `json:"totalBytes"`
}

// Part 是分批下载中的一份。
type Part struct {
	// N 是 1-based 的分批编号。
	N int `json:"n"`
	// Bytes 是这一份 zip 的实际字节数。
	Bytes int64 `json:"bytes"`
	// SHA256 是这一份 zip 的十六进制摘要；同一个作业重复下载字节流完全一致。
	SHA256 string `json:"sha256"`
	// URL 是这一份的下载地址，由 handler 填（segment 包不感知 HTTP）。
	URL string `json:"url,omitempty"`
}

// Result 是作业产物在 API 上的描述（GET /jobs/{id} 的 result 字段）。
type Result struct {
	// Bytes 是产物总大小；SingleResponse 就是拿它和单次返回上限比的。
	Bytes int64 `json:"bytes"`
	// Segments 是**媒体分片个数**（不含 init 段），与是否打包无关。
	Segments int `json:"segments"`
	// Files 是**产物文件个数**（index.json + init.mp4 + pack-*.bin 或 c*.m4s）。
	// 它与 Segments 是两个概念，刻意分开命名：打包后文件数远小于分片数，
	// 客户端若要显示"还要写几个文件"，必须用这个值。
	Files int `json:"files"`
	// SingleResponse 为真表示走 GET /result 一次拿完。
	SingleResponse bool `json:"singleResponse"`
	// Parts 只在 SingleResponse 为假时非空。
	Parts []Part `json:"parts,omitempty"`
}

// CollectArtifacts 扫描产物目录，收集产物清单与总大小。
//
// 清单按"首次出现"去重：打包布局下 index.Segments 里同一个 pack-*.bin 会出现很多次
// （100 片一包就是 100 次），去重后它才是一个真实存在的产物文件。未打包布局下
// 每个分片本来就是独立文件，去重是恒等操作，行为与打包功能出现之前完全一致。
func CollectArtifacts(dir string, index *model.Index) (*Artifacts, error) {
	names := make([]string, 0, len(index.Segments)+2)
	seen := make(map[string]struct{}, len(index.Segments)+2)
	appendName := func(name string) {
		if _, dup := seen[name]; dup {
			return
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}

	appendName(IndexFileName)
	appendName(InitFileName)
	for _, seg := range index.Segments {
		appendName(seg.File)
	}

	out := &Artifacts{Dir: dir, Index: index}
	for _, name := range names {
		full := filepath.Join(dir, name)
		info, err := os.Stat(full)
		if err != nil {
			return nil, fmt.Errorf("segment: 产物缺失 %s: %w", name, err)
		}
		sum, err := fileSHA256(full)
		if err != nil {
			return nil, err
		}
		out.Files = append(out.Files, ArtifactFile{Name: name, Size: info.Size(), SHA256: sum})
		out.TotalBytes += info.Size()
	}
	return out, nil
}

// PlanParts 把产物按单份上限切分成若干份，每份打包后严格小于 maxBytes。
// 顺序被保留：客户端按 parts[n].url 逐份下载，拼起来就是完整产物。
//
// 它按**产物文件**切分：打包布局下每一个 pack-*.bin 就是一个不可再分的单位
// （绝不把一个包劈到两份里，否则客户端要额外做 Range 请求才能拼回完整文件）。
func PlanParts(files []ArtifactFile, maxBytes int64) ([][]ArtifactFile, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("segment: 单份上限必须为正（%d）", maxBytes)
	}

	var groups [][]ArtifactFile
	var current []ArtifactFile
	var currentBytes int64

	for _, f := range files {
		need := f.Size + zipPerFileOverhead
		if need >= maxBytes {
			return nil, fmt.Errorf("%w: %s 为 %s，单份上限 %s",
				ErrTooBigForParts, f.Name, HumanBytes(f.Size), HumanBytes(maxBytes))
		}
		if len(current) > 0 && currentBytes+need >= maxBytes {
			groups = append(groups, current)
			current, currentBytes = nil, 0
		}
		current = append(current, f)
		currentBytes += need
	}
	if len(current) > 0 {
		groups = append(groups, current)
	}
	return groups, nil
}

// WriteZip 把产物按给定顺序写成一个 zip，返回写入的字节数。
//
// 压缩方式：index.json 用 Deflate（文本，压得动），分片用 Store
// （H.264/AAC 已经压过，再 Deflate 只会白烧 CPU）。配合固定的 zipEpoch，
// 同一个作业每次生成的 zip 逐字节一致（有单测锁定这一点）。
func WriteZip(w io.Writer, dir string, files []ArtifactFile) (int64, error) {
	counter := &countingWriter{w: w}
	zw := zip.NewWriter(counter)

	for _, f := range files {
		method := zip.Store
		if strings.HasSuffix(f.Name, ".json") {
			method = zip.Deflate
		}
		header := &zip.FileHeader{Name: f.Name, Method: method, Modified: zipEpoch}
		header.SetMode(0o644)

		dst, err := zw.CreateHeader(header)
		if err != nil {
			return counter.n, fmt.Errorf("segment: 创建 zip 条目 %s 失败: %w", f.Name, err)
		}
		src, err := os.Open(filepath.Join(dir, f.Name))
		if err != nil {
			return counter.n, fmt.Errorf("segment: 读取产物 %s 失败: %w", f.Name, err)
		}
		_, copyErr := io.Copy(dst, src)
		closeErr := src.Close()
		if copyErr != nil {
			return counter.n, fmt.Errorf("segment: 写入 zip 条目 %s 失败: %w", f.Name, copyErr)
		}
		if closeErr != nil {
			return counter.n, fmt.Errorf("segment: 关闭产物 %s 失败: %w", f.Name, closeErr)
		}
	}

	if err := zw.Close(); err != nil {
		return counter.n, fmt.Errorf("segment: 关闭 zip 失败: %w", err)
	}
	return counter.n, nil
}

// hashZip 在不落盘的前提下算出某一份的 zip 大小与 sha256。
func hashZip(dir string, files []ArtifactFile) (int64, string, error) {
	hasher := sha256.New()
	size, err := WriteZip(hasher, dir, files)
	if err != nil {
		return 0, "", err
	}
	return size, hex.EncodeToString(hasher.Sum(nil)), nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("segment: 打开 %s 失败: %w", filepath.Base(path), err)
	}
	defer f.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return "", fmt.Errorf("segment: 读取 %s 失败: %w", filepath.Base(path), err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// HumanBytes 把字节数格式化成便于阅读的形式（错误信息与日志用）。
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(n)/float64(div), "KMGT"[exp])
}
