// 给用户看的结论：探测结果、处理依据、产物摘要、容量提示、下一步。
//
// 文案与一键脚本的最后一段对齐（分片数 / 文件数 / 体积 / 码率 / K0 / 下一步），
// 并补上一句必须说清楚的话：K0 是"能带几个直连子节点"，不是"能带几个人"。
package main

import (
	"ProjectionRoom/internal/service"
	"ProjectionRoom/internal/service/segment"
)

// printProbe 打印探测结果（一键脚本里"源: Xs (Y 分钟) 视频=… 音频=…"那一段）。
func (a *app) printProbe(info *segment.MediaInfo) {
	if info == nil {
		return
	}
	audio := "无"
	if info.HasAudio {
		audio = displayCodec(info.AudioCodec, "未知")
	}
	a.sayf("探测: 时长 %.1f 秒（%.1f 分钟）  视频=%s  音频=%s",
		info.DurationSec, info.DurationSec/60, displayCodec(info.VideoCodec, "未知"), audio)

	if info.DurationSec > 3600 {
		a.warnf("警告: 视频超过 60 分钟：本地切片没问题，但服务端切片会拒绝这么长的视频。")
	}
}

// printDecision 打印处理方式与选择依据（"为什么直通/为什么转码"）。
// info 用于在直通 AV1/VP9 时补一句浏览器兼容性提示。
func (a *app) printDecision(dec decision, info *segment.MediaInfo) {
	a.sayf("判定: %s", dec.reason)
	if dec.mode == modeRemux {
		if warn := compatWarning(info); warn != "" {
			a.warnf("警告: %s", warn)
		}
	}
}

// printSummary 打印产物摘要 → 容量提示 → 下一步。
func (a *app) printSummary(out string, artifacts *segment.Artifacts) {
	if artifacts == nil || artifacts.Index == nil {
		return
	}
	index := artifacts.Index

	a.sayf("")
	a.sayf("完成。产物目录: %s", out)
	a.sayf("  分片: %d 片（平均 %.2f 秒/片，目标 %.1f 秒）",
		len(index.Segments), index.SegmentSec, a.opts.fragSec)
	if index.Packed() {
		a.sayf("  打包: %d 个 .bin（每包 %d 片）", len(index.Packs), a.opts.pack)
	} else {
		a.sayf("  打包: 关闭（每片一个 c*.m4s）")
	}
	a.sayf("  文件: %d 个（index.json + init.mp4 + 分片/包）", len(artifacts.Files))
	a.sayf("  体积: %s", segment.HumanBytes(artifacts.TotalBytes))
	a.sayf("  码率: %.2f Mbps", float64(index.BitrateBps)/1_000_000)
	a.sayf("  时长: %.2f 秒", index.TotalDuration)
	a.sayf("  编码: %s", index.MimeType)

	a.printCapacityHint(index.BitrateBps)

	a.sayf("")
	a.sayf("接下来：在主播页点「选择分片目录」选中 %s 即可开播。", out)
}

// printCapacityHint 把"这个码率下主播能带几个直连子节点"直接告诉主播（SPEC §6.1、§6.2）。
//
// K0 = floor(上行 × 0.8 ÷ 码率)，上限 8（service.MaxChildren）。
func (a *app) printCapacityHint(streamBps int64) {
	uplinkMbps := a.opts.uplinkMbps
	slots := service.HostChildSlots(int64(uplinkMbps*1_000_000), streamBps)

	a.sayf("")
	a.sayf("容量提示（按主播上行 %.1f Mbps 估算，K0 = floor(上行 × %.1f ÷ 码率)，上限 %d）:",
		uplinkMbps, service.SafetyFactor, service.MaxChildren)
	a.sayf("  K0 = %d", slots)
	a.sayf("  注意：K0 是主播能直接带几个子节点，不是能带几个人；其余成员挂在这些直连节点下面。")

	switch {
	case slots <= 0:
		a.sayf("  当前码率下连 1 个观众都带不动。请降低码率，例如：")
		a.sayf("    segmenter -in <视频> -out %s -transcode 1200k", a.opts.out)
	case slots == 1:
		a.sayf("  单链分发模式：主播只服务 1 个分发节点，由它承担全房间分发。")
		a.sayf("  该节点必须真的比主播上行强，否则只是把瓶颈从主播搬到它身上。")
	default:
		a.sayf("  扇出模式：主播可直接服务 %d 个一级节点，其余成员挂到它们下面。", slots)
	}
}
