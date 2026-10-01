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
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"ProjectionRoom/internal/mp4"
	"ProjectionRoom/internal/topology"
)

func main() {
	in := flag.String("in", "", "输入视频文件（必填）")
	out := flag.String("out", "./room-media", "输出目录")
	transcode := flag.String("transcode", "", "低上行预设：用 ffmpeg 转码到指定码率（如 1200k）后再切片")
	fragment := flag.Bool("fragment", false, "输入是普通 MP4 时，先无损重新封装成 fragmented MP4")
	fragSec := flag.Float64("frag-sec", 2, "分片目标时长（秒）")
	uplinkMbps := flag.Float64("uplink-mbps", 12, "主播上行估计（Mbps），仅用于打印容量提示")

	flag.Parse()

	if err := run(*in, *out, *transcode, *fragment, *fragSec, *uplinkMbps); err != nil {
		fmt.Fprintf(os.Stderr, "segmenter 失败: %v\n", err)
		os.Exit(1)
	}
}

func run(in, out, transcode string, fragment bool, fragSec, uplinkMbps float64) error {
	if strings.TrimSpace(in) == "" {
		return fmt.Errorf("必须指定 -in")
	}
	if _, err := os.Stat(in); err != nil {
		return fmt.Errorf("输入文件不可读: %w", err)
	}
	if fragSec <= 0 {
		return fmt.Errorf("-frag-sec 必须为正")
	}

	source := in
	var cleanup func()

	if transcode != "" {
		tmp, err := os.CreateTemp("", "pr-transcode-*.mp4")
		if err != nil {
			return fmt.Errorf("创建临时文件失败: %w", err)
		}
		tmpPath := tmp.Name()
		tmp.Close()

		args := []string{
			"-y", "-hide_banner", "-loglevel", "error",
			"-i", in,
			"-c:v", "libx264", "-preset", "veryfast", "-b:v", transcode,
			"-c:a", "aac", "-b:a", "96k",
			"-movflags", "+frag_keyframe+empty_moov+default_base_moof",
			"-frag_duration", fmt.Sprintf("%d", int64(fragSec*1_000_000)),
			tmpPath,
		}
		fmt.Printf("低码率预设：转码到 %s（这一步可能耗时数分钟）…\n", transcode)
		if err := runFFmpeg(args); err != nil {
			os.Remove(tmpPath)
			return err
		}
		source, cleanup = tmpPath, func() { os.Remove(tmpPath) }
	} else if fragment {
		tmp, err := os.CreateTemp("", "pr-fragment-*.mp4")
		if err != nil {
			return fmt.Errorf("创建临时文件失败: %w", err)
		}
		tmpPath := tmp.Name()
		tmp.Close()

		args := []string{
			"-y", "-hide_banner", "-loglevel", "error",
			"-i", in,
			"-c", "copy",
			"-movflags", "+frag_keyframe+empty_moov+default_base_moof",
			"-frag_duration", fmt.Sprintf("%d", int64(fragSec*1_000_000)),
			tmpPath,
		}
		fmt.Println("无损重新封装为 fragmented MP4…")
		if err := runFFmpeg(args); err != nil {
			os.Remove(tmpPath)
			return err
		}
		source, cleanup = tmpPath, func() { os.Remove(tmpPath) }
	}
	if cleanup != nil {
		defer cleanup()
	}

	index, err := mp4.SplitFile(source, mp4.SplitOptions{OutDir: out})
	if err != nil {
		return err
	}

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
	slots := topology.HostChildSlots(uplinkBps, streamBps)

	fmt.Printf("\n容量提示（按主播上行 %.1f Mbps 估计）:\n", uplinkMbps)
	switch topology.SelectMode(slots) {
	case topology.ModeChain:
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

func runFFmpeg(args []string) error {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fmt.Errorf("未找到 ffmpeg，请先安装并加入 PATH（或用 -in 直接传 fragmented MP4）")
	}

	cmd := exec.Command("ffmpeg", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg 执行失败: %w\n%s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
