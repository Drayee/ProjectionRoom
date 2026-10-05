// 探测结果的用法：决定"直通（只重新封装）"还是"转码"。
//
// 判据来自一键脚本：视频 ∈ {h264, av1, vp9} 且音频 ∈ {aac, opus, 无}
// → 只做 `-c copy` 重新封装；否则自动转码成 H.264/AAC
// （libx264 veryfast crf 23 + aac 128k），除非用户显式给了 -transcode（优先于自动判定）。
//
// 为什么集合里会有 av1：它是浏览器**部分支持**的编码，直通能省掉一次重编码；
// 代价是只有在支持它的浏览器里能播（Safari、部分 Firefox 不行），所以判定之后会额外提示。
//
// vp9 也在直通集合里（对齐一键脚本的判据），但本机切片器
// （internal/service/mp4）只认 avc1/avc3/av01 + mp4a/Opus 的采样格式，
// 直通必然在切片那一步失败；因此判定之后就给出"必须加 -transcode"的明确提示，
// 而不是等切到一半再报错。见 compatWarning 与 explainProcessError。
package main

import (
	"fmt"
	"strings"

	"ProjectionRoom/internal/service/segment"
)

// passthroughVideo / passthroughAudio 是"可以直接 -c copy 交给浏览器"的编码集合。
// key 一律小写（ffprobe 的 codec_name 本身是小写，这里再兜一层）。
var (
	passthroughVideo = map[string]bool{"h264": true, "av1": true, "vp9": true}
	passthroughAudio = map[string]bool{"aac": true, "opus": true}
)

// processMode 是这次要走的处理路径。
type processMode int

const (
	// modeDirect：不过 ffmpeg，直接按 moof 边界切（输入已经是 fragmented MP4）。
	modeDirect processMode = iota
	// modeRemux：无损重新封装为 fMP4 再切（-c copy，不重新编码）。
	modeRemux
	// modeTranscode：重新编码成浏览器能播的格式再切。
	modeTranscode
)

// decision 是"怎么处理这个源文件"的结论，reason 是要打给用户的选择依据。
type decision struct {
	mode   processMode
	reason string
}

// decide 作出处理决定。优先级：-transcode → -fragment → 探测结论。
//
// info 为 nil 表示没有探测结果（没有 ffprobe）：此时只能直接切已经 fragmented 的 MP4。
func decide(info *segment.MediaInfo, transcode string, forceFragment bool) decision {
	if rate := strings.TrimSpace(transcode); rate != "" {
		return decision{
			mode:   modeTranscode,
			reason: fmt.Sprintf("你显式指定了 -transcode %s → 转码优先于自动判定", rate),
		}
	}
	if forceFragment {
		return decision{
			mode:   modeRemux,
			reason: "你显式指定了 -fragment → 强制无损重新封装（-c copy），不做自动判定",
		}
	}
	if info == nil {
		return decision{
			mode:   modeDirect,
			reason: "没有探测结果（ffprobe 不可用）→ 直接按 moof 边界切分（仅适用于已经是 fragmented MP4 的输入）",
		}
	}
	if ok, reason := isPassthrough(info); ok {
		return decision{mode: modeRemux, reason: reason}
	} else {
		return decision{mode: modeTranscode, reason: reason}
	}
}

// isPassthrough 判断这条媒体流能否直通，并给出可读依据。
func isPassthrough(info *segment.MediaInfo) (bool, string) {
	if info == nil || !info.HasVideo {
		return false, "探测不到视频轨 → 不能直通"
	}
	video := strings.ToLower(strings.TrimSpace(info.VideoCodec))
	audio := strings.ToLower(strings.TrimSpace(info.AudioCodec))

	if !passthroughVideo[video] {
		return false, fmt.Sprintf(
			"视频 %s 不在直通集合 {h264, av1, vp9} 内 → 自动转码为 H.264/AAC"+
				"（libx264 veryfast crf 23 + aac 128k）", displayCodec(info.VideoCodec, "未知"))
	}
	if info.HasAudio && !passthroughAudio[audio] {
		return false, fmt.Sprintf(
			"音频 %s 不在直通集合 {aac, opus, 无} 内 → 自动转码为 H.264/AAC"+
				"（libx264 veryfast crf 23 + aac 128k）", displayCodec(info.AudioCodec, "未知"))
	}

	audioText := "无音轨"
	if info.HasAudio {
		audioText = displayCodec(info.AudioCodec, "未知")
	}
	return true, fmt.Sprintf(
		"视频 %s 与音频 %s 都在直通集合内 → 直通：只做 -c copy 重新封装，不重新编码",
		displayCodec(info.VideoCodec, "未知"), audioText)
}

// compatWarning 在直通 AV1/VP9 时给出提示（空串表示不需要提示）。
//
// AV1 是"最大兼容 vs 省一次重编码"的取舍，必须明确告诉用户，而不是替他决定；
// VP9 不是取舍：切片器根本不认 vp09，直通这条路走不通，只能在决定路径时就说清必须转码。
func compatWarning(info *segment.MediaInfo) string {
	if info == nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(info.VideoCodec)) {
	case "vp9":
		return "VP9 直通不受支持：本机切片器只认 avc1/avc3/av01 + mp4a/Opus 的采样格式，" +
			"vp09 会在切片时报「暂不支持 vp09」；请改用 -transcode 1200k（转成 H.264/AAC）。"
	case "av1":
		return "AV1 直通后只有在支持它的浏览器里能播（Safari、部分 Firefox 不行）；" +
			"要在任何浏览器都能播，请加 -transcode 1200k 重新编码。"
	default:
		return ""
	}
}

// modeName 是给用户看的处理方式名字。
func modeName(m processMode) string {
	switch m {
	case modeRemux:
		return "无损重新封装（-c copy）"
	case modeTranscode:
		return "转码"
	default:
		return "直接切分"
	}
}

// explainProcessError 给共享流水线抛回来的错误补上"下一步怎么办"。
//
// 最典型的是 VP9：判定规则允许它直通（见本文件开头的集合），但本机切片器
// （internal/service/mp4）只认 avc1/avc3/av01 + mp4a/Opus 的采样格式，
// 于是会抛出"暂不支持 vp09"。这句话本身没错，却没告诉用户怎么绕过，
// 所以这里补一句明确的做法（而不是静默改成转码、也不是让用户自己猜）。
func explainProcessError(err error, dec decision, info *segment.MediaInfo) error {
	if err == nil {
		return nil
	}
	if dec.mode == modeRemux && info != nil && strings.EqualFold(strings.TrimSpace(info.VideoCodec), "vp9") {
		return fmt.Errorf("%w\n提示: 本机切片器只支持 avc1/av01 + mp4a/Opus 的采样格式，vp09 不在其中；"+
			"请加 -transcode 1200k 重跑（会自动转成 H.264/AAC，任何浏览器都能播）。", err)
	}
	return err
}

// displayCodec 只用于文案：空值给一个可读的兜底。
func displayCodec(codec, fallback string) string {
	c := strings.TrimSpace(codec)
	if c == "" {
		return fallback
	}
	return c
}
