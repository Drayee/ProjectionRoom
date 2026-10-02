// Command segmenter 把本地视频预处理成放映室可用的 fMP4 分片目录（SPEC §4.1、§4.5）。
//
// 三种用法：
//
//	segmenter -in out_frag.mp4 -out ./room-media              # 已经是 fragmented MP4，直接切
//	segmenter -in movie.mp4 -out ./room-media -fragment       # 无损重新封装后再切
//	segmenter -in movie.mp4 -out ./room-media -transcode 1200k # 低上行预设：转码降码率
//
// 为什么必须切在 moof 边界上：SourceBuffer.appendBuffer() 只接受完整的 fMP4 片段，
// 按固定字节数切割会产出"半个 moof"，浏览器直接抛错（SPEC §4.2）。
//
// 流水线本体（必要时 ffmpeg 重新封装/转码 → 按 moof 边界切分 → 写 index.json）在
// internal/service/segment，与服务端一次性切片共用同一份实现，
// 因此"本地切片"与"服务端切片"的产物格式不可能漂移（前端零改动）。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"ProjectionRoom/internal/service/segment"
	"ProjectionRoom/internal/usecase"
)

func main() {
	in := flag.String("in", "", "输入视频文件（必填）")
	out := flag.String("out", "./room-media", "输出目录")
	transcode := flag.String("transcode", "", "低上行预设：用 ffmpeg 转码到指定码率（如 1200k）后再切片")
	fragment := flag.Bool("fragment", false, "输入是普通 MP4 时，先无损重新封装成 fragmented MP4")
	fragSec := flag.Float64("frag-sec", 2, "分片目标时长（秒）")
	uplinkMbps := flag.Float64("uplink-mbps", 12, "主播上行估计（Mbps），仅用于打印容量提示")
	ffmpegPath := flag.String("ffmpeg", "",
		"ffmpeg 路径（可为目录或可执行文件）；默认按 PR_FFMPEG → PATH → 常见安装目录 查找")

	flag.Parse()

	if err := run(*in, *out, *transcode, *fragment, *fragSec, *uplinkMbps, *ffmpegPath); err != nil {
		fmt.Fprintf(os.Stderr, "segmenter 失败: %v\n", err)
		os.Exit(1)
	}
}

func run(in, out, transcode string, fragment bool, fragSec, uplinkMbps float64, ffmpegPath string) error {
	if strings.TrimSpace(in) == "" {
		return fmt.Errorf("必须指定 -in")
	}
	if _, err := os.Stat(in); err != nil {
		return fmt.Errorf("输入文件不可读: %w", err)
	}
	if fragSec <= 0 {
		return fmt.Errorf("-frag-sec 必须为正")
	}

	explicit := strings.TrimSpace(ffmpegPath)
	if explicit == "" {
		explicit = os.Getenv("PR_FFMPEG")
	}
	tools := segment.DiscoverTools(explicit)

	// 只有需要重新封装/转码时才要求 ffmpeg；直接切已经 fragmented 的 MP4 不需要它。
	if (transcode != "" || fragment) && tools.FFmpeg == "" {
		return fmt.Errorf("未找到 ffmpeg，请先安装并加入 PATH（或用 -in 直接传 fragmented MP4）")
	}

	logf := func(format string, args ...any) {
		fmt.Printf(format+"\n", args...)
	}

	artifacts, err := segment.Process(context.Background(), in, out, segment.ProcessOptions{
		Tools:          tools,
		SegmentSeconds: fragSec,
		Transcode:      transcode,
		ForceFragment:  fragment,
		Logf:           logf,
	})
	if err != nil {
		return err
	}

	index := artifacts.Index
	fmt.Printf("\n输出目录: %s\n", out)
	fmt.Printf("  编码格式: %s\n", index.MimeType)
	fmt.Printf("  时长:     %.2f 秒\n", index.TotalDuration)
	fmt.Printf("  分片:     %d 个（平均 %.2f 秒/片，目标 %.1f 秒）\n", len(index.Segments), index.SegmentSec, fragSec)
	fmt.Printf("  码率:     %.2f Mbps\n", float64(index.BitrateBps)/1_000_000)
	fmt.Printf("  体积:     %.2f MiB\n", float64(index.TotalBytes)/(1024*1024))

	printCapacityHint(index.BitrateBps, uplinkMbps)

	return nil
}

// printCapacityHint 把"这个码率下主播能带几个人"直接告诉主播（SPEC §6.1、§6.2）。
func printCapacityHint(streamBps int64, uplinkMbps float64) {
	uplinkBps := int64(uplinkMbps * 1_000_000)
	slots := usecase.HostChildSlots(uplinkBps, streamBps)

	fmt.Printf("\n容量提示（按主播上行 %.1f Mbps 估计）:\n", uplinkMbps)
	switch usecase.SelectMode(slots) {
	case usecase.ModeChain:
		if slots == 0 {
			fmt.Printf("  当前码率下连 1 个观众都带不动。请降低码率，例如：\n")
			fmt.Printf("    segmenter -in <视频> -out %s -transcode 1200k\n", "./room-media")
			return
		}
		fmt.Printf("  K0 = 1 → 单链分发模式：主播只服务 1 个分发节点，由它承担全房间分发。\n")
		fmt.Printf("  该节点必须真的比主播上行强，否则只是把瓶颈从主播搬到它身上。\n")
	default:
		fmt.Printf("  K0 = %d → 扇出模式：主播可直接服务 %d 个一级节点，其余成员挂到它们下面。\n", slots, slots)
	}
}
