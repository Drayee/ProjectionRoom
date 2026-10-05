package segment

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/service/mp4"
)

// 进度里程碑（0–1）。粗粒度是刻意的：切片本身没有可靠的细粒度回调，
// 编一个假的百分比只会让前端显示得比实际更精确。
const (
	progressProbe      = 0.02
	progressPrepare    = 0.10 // 重新封装/转码：0.10 → 0.60
	progressPrepareEnd = 0.60
	progressSplit      = 0.65
	progressDone       = 1.0
)

// MediaInfo 是 ffprobe 的结论。
type MediaInfo struct {
	// DurationSec 是容器时长（秒）。
	DurationSec float64
	// SizeBytes 是文件字节数（以磁盘实际情况为准，ffprobe 的值只作为备份）。
	SizeBytes int64
	// VideoCodec / AudioCodec 是 ffprobe 的 codec_name，如 h264、aac、hevc。
	VideoCodec string
	AudioCodec string
	HasVideo   bool
	HasAudio   bool
}

// BrowserPlayable 报告这条媒体流能否被 MSE 直接播放。
// SPEC §4.3 的 mimeType 只由 H.264/AAC 解析得出（见 mp4.codecFromSampleEntry），
// 所以其它编码必须先转码，否则切出来的分片在浏览器上根本 append 不进去。
func (m *MediaInfo) BrowserPlayable() bool {
	if m == nil || !m.HasVideo {
		return false
	}
	if !strings.EqualFold(m.VideoCodec, "h264") {
		return false
	}
	if m.HasAudio && !strings.EqualFold(m.AudioCodec, "aac") {
		return false
	}
	return true
}

// Prober 探测媒体信息。抽成函数类型是为了单测能注入假数据
// （否则"时长超限返回 422"这类用例要造一个 60 分钟的真实视频）。
type Prober func(ctx context.Context, tools Tools, path string) (*MediaInfo, error)

// Probe 用 ffprobe 读取时长与编码。它是"不要等切完才发现超限"的前置校验。
func Probe(ctx context.Context, tools Tools, path string) (*MediaInfo, error) {
	if tools.FFprobe == "" {
		return nil, ErrFFmpegMissing
	}

	cmd := exec.CommandContext(ctx, tools.FFprobe,
		"-v", "error",
		"-print_format", "json",
		"-show_format", "-show_streams",
		path,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %v\n%s", ErrProbeFailed, err, strings.TrimSpace(stderr.String()))
	}

	var payload struct {
		Streams []struct {
			CodecName string `json:"codec_name"`
			CodecType string `json:"codec_type"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
			Size     string `json:"size"`
		} `json:"format"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		return nil, fmt.Errorf("%w: ffprobe 输出不是合法 JSON: %v", ErrProbeFailed, err)
	}

	info := &MediaInfo{}
	// duration 在 ffprobe 里是字符串，且流式输入时可能缺失；缺失当 0 处理，
	// 由调用方决定是否接受（服务端要求 >0，见 Queue 的校验）。
	if d, err := strconv.ParseFloat(strings.TrimSpace(payload.Format.Duration), 64); err == nil {
		info.DurationSec = d
	}
	if n, err := strconv.ParseInt(strings.TrimSpace(payload.Format.Size), 10, 64); err == nil {
		info.SizeBytes = n
	}
	for _, s := range payload.Streams {
		switch s.CodecType {
		case "video":
			if !info.HasVideo {
				info.HasVideo = true
				info.VideoCodec = s.CodecName
			}
		case "audio":
			if !info.HasAudio {
				info.HasAudio = true
				info.AudioCodec = s.CodecName
			}
		}
	}
	if st, err := os.Stat(path); err == nil {
		info.SizeBytes = st.Size()
	}

	return info, nil
}

// DefaultPackSize 是默认的打包粒度：每个 pack-XXXX.bin 容纳多少片。
//
// 为什么默认打包：Windows 上"每个文件一次写盘"有固定开销（Chrome 先写同目录 swap
// 文件再改名 + 杀软逐个扫描），1800 个小分片要好几分钟。100 片一包把产物文件数
// 从 ~1800 降到 ~18，写入/下载/解压同步变快，而单包仍在几百 MB 级以内。
//
// 它是 config.DefaultPackSize 的别名：默认值只有一处定义。
const DefaultPackSize = config.DefaultPackSize

// ProcessOptions 控制一次切片流水线。
type ProcessOptions struct {
	// Tools 是已定位的 ffmpeg/ffprobe。
	Tools Tools
	// SegmentSeconds 是分片目标时长（秒），默认 2。
	SegmentSeconds float64
	// Transcode 非空时强制转码到该视频码率（如 "1200k"），对应 CLI 的 -transcode。
	Transcode string
	// ForceFragment 为真时强制做一次无损重新封装，对应 CLI 的 -fragment。
	ForceFragment bool
	// Auto 为服务端模式：自动判断"无损重封装"还是"转码"。
	// 打开它必须同时给出 Info，否则无法判断编码是否可播。
	Auto bool
	// Info 是预先探测到的媒体信息（服务端已经为时长校验探测过一次，不重复探测）。
	Info *MediaInfo
	// WorkDir 是重新封装/转码的中间文件目录；为空时用系统临时目录。
	WorkDir string
	// PackSize 是每个 .bin 容纳的分片数；<=0 用 DefaultPackSize，1 表示不打包
	// （逐片一个 c*.m4s，与打包功能出现之前逐字节等价）。
	// 服务端取自 config.Segment.PackSize，CLI 取自 -pack。
	PackSize int
	// Progress 接收 0–1 的完成度；可空。
	Progress func(float64)
	// Logf 接收面向用户的阶段说明；可空。
	Logf func(format string, args ...any)
}

// Processor 是流水线的可注入形式，便于单测替换掉真实的 ffmpeg/切片。
type Processor interface {
	Process(ctx context.Context, in, outDir string, opts ProcessOptions) (*Artifacts, error)
}

// processorFunc 让普通函数满足 Processor。
type processorFunc func(ctx context.Context, in, outDir string, opts ProcessOptions) (*Artifacts, error)

func (f processorFunc) Process(ctx context.Context, in, outDir string, opts ProcessOptions) (*Artifacts, error) {
	return f(ctx, in, outDir, opts)
}

// Process 执行与 cmd/segmenter 完全相同的流水线：
//
//	probe（可选）→ 必要时 ffmpeg 无损重封装/转码 → 按 moof 边界切分 → 写 index.json
//
// 它是"本地 CLI"和"服务端切片"的唯一实现，产出格式因此不可能漂移。
func Process(ctx context.Context, in, outDir string, opts ProcessOptions) (*Artifacts, error) {
	opts.applyDefaults()

	if _, err := os.Stat(in); err != nil {
		return nil, fmt.Errorf("输入文件不可读: %w", err)
	}
	if opts.SegmentSeconds <= 0 {
		return nil, fmt.Errorf("分片时长必须为正（%v）", opts.SegmentSeconds)
	}
	if opts.PackSize > 1 {
		opts.logf("分片打包：每 %d 片合成一个 .bin（产物文件数因此大幅减少）", opts.PackSize)
	}

	emit := opts.progressFunc()
	emit(progressProbe)

	// 决定要不要先过一遍 ffmpeg。
	transcode := strings.TrimSpace(opts.Transcode) != ""
	fragment := opts.ForceFragment
	if opts.Auto && !transcode && !fragment {
		if opts.Info == nil {
			return nil, fmt.Errorf("segment: 自动模式需要先探测源文件")
		}
		if opts.Info.BrowserPlayable() {
			fragment = true
		} else {
			transcode = true
		}
	}

	var temporaries []string
	defer func() {
		for _, p := range temporaries {
			_ = os.Remove(p)
		}
	}()

	source := in
	fragDuration := strconv.FormatInt(int64(opts.SegmentSeconds*1_000_000), 10)

	emit(progressPrepare)

	switch {
	case transcode:
		tmp, err := opts.tempFile("pr-transcode-*.mp4")
		if err != nil {
			return nil, err
		}
		temporaries = append(temporaries, tmp)

		args := []string{
			"-y", "-hide_banner", "-loglevel", "error",
			"-i", in,
			"-c:v", "libx264", "-preset", "veryfast",
		}
		audioBitrate := "128k"
		if opts.Transcode != "" {
			// 低上行预设：与 cmd/segmenter -transcode 的行为逐字保持一致。
			args = append(args, "-b:v", opts.Transcode)
			audioBitrate = "96k"
			opts.logf("低码率预设：转码到 %s（这一步可能耗时数分钟）…", opts.Transcode)
		} else {
			// 服务端自动模式：源编码不是 H.264/AAC，转成浏览器能播的格式。
			args = append(args, "-crf", "23")
			opts.logf("源视频编码为 %s/%s，不是 H.264/AAC；转码为浏览器可播放的格式…",
				displayCodec(opts.Info, true), displayCodec(opts.Info, false))
		}
		args = append(args,
			"-c:a", "aac", "-b:a", audioBitrate,
			"-movflags", "+frag_keyframe+empty_moov+default_base_moof",
			"-frag_duration", fragDuration,
			tmp,
		)
		if err := runFFmpeg(ctx, opts.Tools.FFmpeg, args, opts.Info, progressPrepare, progressPrepareEnd, emit); err != nil {
			return nil, err
		}
		source = tmp

	case fragment:
		tmp, err := opts.tempFile("pr-fragment-*.mp4")
		if err != nil {
			return nil, err
		}
		temporaries = append(temporaries, tmp)

		opts.logf("无损重新封装为 fragmented MP4…")
		args := []string{
			"-y", "-hide_banner", "-loglevel", "error",
			"-i", in,
			"-c", "copy",
			"-movflags", "+frag_keyframe+empty_moov+default_base_moof",
			"-frag_duration", fragDuration,
			tmp,
		}
		if err := runFFmpeg(ctx, opts.Tools.FFmpeg, args, opts.Info, progressPrepare, progressPrepareEnd, emit); err != nil {
			return nil, err
		}
		source = tmp
	}

	emit(progressSplit)

	index, err := mp4.SplitFile(source, mp4.SplitOptions{
		OutDir:    outDir,
		InitName:  InitFileName,
		IndexName: IndexFileName,
		PackSize:  opts.PackSize,
	})
	if err != nil {
		return nil, err
	}

	artifacts, err := CollectArtifacts(outDir, index)
	if err != nil {
		return nil, err
	}

	emit(progressDone)
	return artifacts, nil
}

func (o *ProcessOptions) applyDefaults() {
	if o.SegmentSeconds <= 0 {
		o.SegmentSeconds = 2
	}
	if o.PackSize <= 0 {
		o.PackSize = DefaultPackSize
	}
}

func (o *ProcessOptions) progressFunc() func(float64) {
	var last float64
	var mu sync.Mutex
	return func(p float64) {
		mu.Lock()
		defer mu.Unlock()
		if p < 0 {
			p = 0
		}
		if p > 1 {
			p = 1
		}
		if p <= last {
			return
		}
		last = p
		if o.Progress != nil {
			o.Progress(p)
		}
	}
}

func (o *ProcessOptions) logf(format string, args ...any) {
	if o.Logf != nil {
		o.Logf(format, args...)
	}
}

// tempFile 在 WorkDir（默认系统临时目录）里创建一个占位文件并返回路径。
func (o *ProcessOptions) tempFile(pattern string) (string, error) {
	f, err := os.CreateTemp(o.WorkDir, pattern)
	if err != nil {
		return "", fmt.Errorf("创建临时文件失败: %w", err)
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("关闭临时文件失败: %w", err)
	}
	return name, nil
}

// displayCodec 只用于日志文案。
func displayCodec(info *MediaInfo, video bool) string {
	if info == nil {
		return "未知"
	}
	if video {
		if info.VideoCodec == "" {
			return "未知"
		}
		return info.VideoCodec
	}
	if info.AudioCodec == "" {
		return "无音轨"
	}
	return info.AudioCodec
}

// runFFmpeg 执行 ffmpeg，并把 out_time_us 换算成 [from, to] 区间内的进度。
//
// 进度用 -progress pipe:1 的 key=value 输出解析，不靠猜；
// 总时长未知（Info 为空或探测不到）时退化为一次性执行，不报错。
func runFFmpeg(ctx context.Context, bin string, args []string,
	info *MediaInfo, from, to float64, emit func(float64)) error {

	if bin == "" {
		return ErrFFmpegMissing
	}

	total := 0.0
	if info != nil {
		total = info.DurationSec
	}

	if total <= 0 {
		cmd := exec.CommandContext(ctx, bin, args...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("ffmpeg 执行失败: %w\n%s", err, strings.TrimSpace(string(output)))
		}
		return nil
	}

	cmd := exec.CommandContext(ctx, bin, append([]string{"-progress", "pipe:1", "-nostats"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("ffmpeg 输出管道创建失败: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ffmpeg 启动失败: %w", err)
	}

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		value, ok := strings.CutPrefix(scanner.Text(), "out_time_us=")
		if !ok {
			continue
		}
		us, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil || us <= 0 {
			continue
		}
		ratio := float64(us) / 1e6 / total
		emit(from + (to-from)*clamp01(ratio))
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("ffmpeg 执行失败: %w\n%s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func clamp01(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	default:
		return v
	}
}

// EnsureOutDir 创建产物目录（流水线与测试共用）。
func EnsureOutDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建输出目录失败: %w", err)
	}
	return nil
}

// SourceExt 猜测上传文件的扩展名，仅用于让作业目录里的源文件可读。
func SourceExt(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".mp4", ".mov", ".mkv", ".webm", ".avi", ".flv", ".m4v", ".ts", ".m2ts", ".wmv", ".mpg", ".mpeg":
		return ext
	default:
		return ".bin"
	}
}
