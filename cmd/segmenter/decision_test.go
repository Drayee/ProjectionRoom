package main

import (
	"errors"
	"strings"
	"testing"

	"ProjectionRoom/internal/service/segment"
)

// TestDecide 锁定"直通还是转码"的判据（一键脚本里靠 ffprobe 的那一段）：
// 视频 ∈ {h264, av1, vp9} 且音频 ∈ {aac, opus, 无} → 直通；否则转码；
// 显式 -transcode / -fragment 优先于自动判定。
func TestDecide(t *testing.T) {
	cases := []struct {
		name       string
		info       *segment.MediaInfo
		transcode  string
		fragment   bool
		want       processMode
		wantReason string
	}{
		{
			name:       "av1+opus 直通",
			info:       &segment.MediaInfo{HasVideo: true, VideoCodec: "av1", HasAudio: true, AudioCodec: "opus"},
			want:       modeRemux,
			wantReason: "直通",
		},
		{
			name:       "h264+aac 直通",
			info:       &segment.MediaInfo{HasVideo: true, VideoCodec: "h264", HasAudio: true, AudioCodec: "aac"},
			want:       modeRemux,
			wantReason: "直通",
		},
		{
			name:       "h264 无音轨 直通",
			info:       &segment.MediaInfo{HasVideo: true, VideoCodec: "h264"},
			want:       modeRemux,
			wantReason: "无音轨",
		},
		{
			name:       "vp9+opus 直通",
			info:       &segment.MediaInfo{HasVideo: true, VideoCodec: "vp9", HasAudio: true, AudioCodec: "opus"},
			want:       modeRemux,
			wantReason: "直通",
		},
		{
			name:       "hevc+aac 转码",
			info:       &segment.MediaInfo{HasVideo: true, VideoCodec: "hevc", HasAudio: true, AudioCodec: "aac"},
			want:       modeTranscode,
			wantReason: "不在直通集合",
		},
		{
			name:       "h264+ac3 转码（音频不可直通）",
			info:       &segment.MediaInfo{HasVideo: true, VideoCodec: "h264", HasAudio: true, AudioCodec: "ac3"},
			want:       modeTranscode,
			wantReason: "音频 ac3",
		},
		{
			name:       "无视频轨 转码",
			info:       &segment.MediaInfo{HasAudio: true, AudioCodec: "aac"},
			want:       modeTranscode,
			wantReason: "视频轨",
		},
		{
			name:       "-transcode 优先于自动直通",
			info:       &segment.MediaInfo{HasVideo: true, VideoCodec: "h264", HasAudio: true, AudioCodec: "aac"},
			transcode:  "1200k",
			want:       modeTranscode,
			wantReason: "-transcode 1200k",
		},
		{
			name:       "-transcode 优先于需要转码的源",
			info:       &segment.MediaInfo{HasVideo: true, VideoCodec: "hevc", HasAudio: true, AudioCodec: "eac3"},
			transcode:  "800k",
			want:       modeTranscode,
			wantReason: "-transcode 800k",
		},
		{
			name:       "-fragment 优先于自动判定",
			info:       &segment.MediaInfo{HasVideo: true, VideoCodec: "hevc", HasAudio: true, AudioCodec: "aac"},
			fragment:   true,
			want:       modeRemux,
			wantReason: "-fragment",
		},
		{
			name:       "没有探测结果 → 直接切分",
			info:       nil,
			want:       modeDirect,
			wantReason: "ffprobe",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decide(tc.info, tc.transcode, tc.fragment)
			if got.mode != tc.want {
				t.Fatalf("处理方式 = %v（%s），应为 %v", got.mode, got.reason, tc.want)
			}
			if !strings.Contains(got.reason, tc.wantReason) {
				t.Fatalf("选择依据里应包含 %q，实际 %q", tc.wantReason, got.reason)
			}
		})
	}
}

// TestCompatWarningForAV1AndVP9 锁定两种直通提示的**不同口径**：
// AV1 是"能播但不通用"的兼容性取舍；VP9 是"切片器不认 vp09、必须转码"，
// 不能拿 Safari 那套说法糊过去——那会让人以为直通能顺利跑完。
// H.264 必须没有提示（否则提示会被当成噪音忽略掉）。
func TestCompatWarningForAV1AndVP9(t *testing.T) {
	for _, codec := range []string{"av1", "AV1"} {
		got := compatWarning(&segment.MediaInfo{VideoCodec: codec})
		for _, want := range []string{"Safari", "-transcode", "AV1"} {
			if !strings.Contains(got, want) {
				t.Fatalf("%s 的兼容性提示应包含 %q，实际 %q", codec, want, got)
			}
		}
	}
	for _, codec := range []string{"vp9", "VP9"} {
		got := compatWarning(&segment.MediaInfo{VideoCodec: codec})
		for _, want := range []string{"暂不支持 vp09", "-transcode", "VP9"} {
			if !strings.Contains(got, want) {
				t.Fatalf("%s 的提示应包含 %q，实际 %q", codec, want, got)
			}
		}
		if strings.Contains(got, "Safari") {
			t.Fatalf("VP9 的提示不该说成浏览器兼容性问题，实际 %q", got)
		}
	}
	for _, codec := range []string{"h264", ""} {
		if got := compatWarning(&segment.MediaInfo{VideoCodec: codec}); got != "" {
			t.Fatalf("%q 不该有兼容性提示，实际 %q", codec, got)
		}
	}
	if got := compatWarning(nil); got != "" {
		t.Fatalf("没有探测结果时不该有兼容性提示，实际 %q", got)
	}
}

// TestIsPassthroughReasonMentionsCodec 保证依据里点到具体的编码名（排障时才知道是谁挡住了）。
func TestIsPassthroughReasonMentionsCodec(t *testing.T) {
	ok, reason := isPassthrough(&segment.MediaInfo{
		HasVideo: true, VideoCodec: "vp9", HasAudio: true, AudioCodec: "vorbis"})
	if ok {
		t.Fatal("vp9+vorbis 不该直通")
	}
	if !strings.Contains(reason, "vorbis") {
		t.Fatalf("依据里应点到音频编码 vorbis，实际 %q", reason)
	}
}

// TestModeName 是文案开关，确保每种处理方式都有给用户看的名字。
func TestModeName(t *testing.T) {
	for mode, want := range map[processMode]string{
		modeDirect:    "直接切分",
		modeRemux:     "无损重新封装",
		modeTranscode: "转码",
	} {
		if got := modeName(mode); !strings.Contains(got, want) {
			t.Fatalf("modeName(%v) = %q，应包含 %q", mode, got, want)
		}
	}
}

// TestExplainProcessErrorAddsVP9Hint 锁定"VP9 直通被切片器拒绝时给出可执行的做法"：
// 判定规则本身不改（vp9 仍在直通集合里），但错误信息必须告诉用户下一步怎么走。
func TestExplainProcessErrorAddsVP9Hint(t *testing.T) {
	base := errors.New("mp4: 暂不支持 vp09（当前支持 avc1/avc3(H.264)、av01(AV1)、mp4a(AAC)、Opus）")

	vp9 := &segment.MediaInfo{HasVideo: true, VideoCodec: "vp9", HasAudio: true, AudioCodec: "opus"}
	got := explainProcessError(base, decision{mode: modeRemux}, vp9)
	if !errors.Is(got, base) {
		t.Fatal("应当用 %w 保留原始错误，否则用户看不到切片器的原话")
	}
	for _, want := range []string{"-transcode 1200k", "vp09"} {
		if !strings.Contains(got.Error(), want) {
			t.Fatalf("错误信息里应有 %q，实际 %q", want, got.Error())
		}
	}

	// 不是 VP9 的直通、以及转码分支：原样返回，不硬塞提示。
	h264 := &segment.MediaInfo{HasVideo: true, VideoCodec: "h264", HasAudio: true, AudioCodec: "aac"}
	if got := explainProcessError(base, decision{mode: modeRemux}, h264); got.Error() != base.Error() {
		t.Fatalf("h264 直通失败时不该加 vp09 提示，实际 %q", got)
	}
	if got := explainProcessError(base, decision{mode: modeTranscode}, vp9); got.Error() != base.Error() {
		t.Fatalf("转码分支不该加 vp09 提示，实际 %q", got)
	}
	if got := explainProcessError(nil, decision{mode: modeRemux}, vp9); got != nil {
		t.Fatalf("nil 错误应原样返回 nil，实际 %v", got)
	}
}
