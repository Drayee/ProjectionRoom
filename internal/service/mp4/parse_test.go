package mp4

import (
	"strings"
	"testing"
)

// ---------- 合成 fixture：与 split_test.go 同一套手法（boxBytes/fixed/concat/u32） ----------

// av1ConfigBox 造一个 av1C（AV1CodecConfigurationRecord）。
//
// 位布局与 parse.go 的 av1CodecString 一一对应：
//
//	第 0 字节 = marker(1) + version(7) → 0x81
//	第 1 字节 = seq_profile(高 3 位) | seq_level_idx_0(低 5 位)
//	第 2 字节 = seq_tier_0(bit7) | high_bitdepth(bit6) | twelve_bit(bit5) | 4:2:0(bit3-2)
func av1ConfigBox(profile, levelIdx byte, highTier, highBitdepth, twelveBit bool) []byte {
	b1 := profile<<5 | (levelIdx & 0x1f)

	var b2 byte
	if highTier {
		b2 |= 0x80
	}
	if highBitdepth {
		b2 |= 0x40
	}
	if twelveBit {
		b2 |= 0x20
	}
	b2 |= 0x0c // chroma_subsampling_x = chroma_subsampling_y = 1（4:2:0）

	return boxBytes("av1C", []byte{0x81, b1, b2, 0x00})
}

// visualSampleEntry 造一个视频采样条目：78 字节固定字段 + 子 box（无 8 字节头）。
func visualSampleEntry(typ string, children []byte) []byte {
	return boxBytes(typ, append(fixed(78, nil), children...))
}

// audioSampleEntry 造一个音频采样条目：28 字节固定字段 + 子 box（无 8 字节头）。
func audioSampleEntry(typ string, children []byte) []byte {
	return boxBytes(typ, append(fixed(28, nil), children...))
}

// stsdPayload 造一个 stsd 负载：version/flags(0) + entryCount + 各采样条目。
// parseStsd 收的是**负载**而不是整个 stsd box，这里刻意对齐它的入参。
func stsdPayload(entries ...[]byte) []byte {
	return concat(u32(0), u32(uint32(len(entries))), concat(entries...))
}

// trakWithEntry 造一条 trak：handler 决定 vide / soun。
func trakWithEntry(trackID uint32, handler string, entry []byte) []byte {
	tkhd := boxBytes("tkhd", fixed(84, map[int][]byte{0: {0}, 12: u32(trackID)}))
	mdhd := boxBytes("mdhd", fixed(24, map[int][]byte{0: {0}, 12: u32(1000), 16: u32(1000)}))
	hdlr := boxBytes("hdlr", fixed(24, map[int][]byte{8: []byte(handler)}))
	stsd := boxBytes("stsd", stsdPayload(entry))
	stbl := boxBytes("stbl", stsd)
	minf := boxBytes("minf", stbl)
	mdia := boxBytes("mdia", concat(mdhd, hdlr, minf))
	return boxBytes("trak", concat(tkhd, mdia))
}

// moovPayload 把若干 trak 装进 moov，返回 moov 的负载（ParseMoov 的入参）。
func moovPayload(durationMs uint32, traks ...[]byte) []byte {
	mvhd := boxBytes("mvhd", fixed(100, map[int][]byte{0: {0}, 12: u32(1000), 16: u32(durationMs)}))
	return concat(append([][]byte{mvhd}, traks...)...)
}

// ---------- av01：av1C → RFC 6381 ----------

func TestParseStsdAV1CodecString(t *testing.T) {
	cases := []struct {
		name         string
		profile      byte
		levelIdx     byte
		highTier     bool
		highBitdepth bool
		twelveBit    bool
		want         string
	}{
		// 本机素材 test/resource/big_mp4_video.mp4 的真实 av1C 负载：
		// 81 0c 4d 00 → seq_profile=0、seq_level_idx_0=12、tier=M、10bit。
		{"素材实测（profile 0 / level 12 / 10bit）", 0, 12, false, true, false, "av01.0.12M.10"},
		{"8bit 且 level 只有一位", 0, 5, false, false, false, "av01.0.05M.08"},
		{"High tier / High profile / 12bit", 1, 13, true, true, true, "av01.1.13H.12"},
		{"level 0 也要补零", 2, 0, false, false, false, "av01.2.00M.08"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := visualSampleEntry("av01", av1ConfigBox(tc.profile, tc.levelIdx, tc.highTier, tc.highBitdepth, tc.twelveBit))

			codecs, err := parseStsd(stsdPayload(entry))
			if err != nil {
				t.Fatalf("parseStsd 失败: %v", err)
			}
			if len(codecs) != 1 || codecs[0] != tc.want {
				t.Fatalf("av01 编码串不正确：得到 %v，应为 %q", codecs, tc.want)
			}
		})
	}
}

// TestParseMoovAV1OpusMimeType 锁定"142 分钟 AV1+Opus 大文件直通"要用的 mimeType：
// 视频轨 av01 与音频轨 Opus 必须都出现在 codecs 里，顺序是视频在前。
func TestParseMoovAV1OpusMimeType(t *testing.T) {
	video := trakWithEntry(1, "vide", visualSampleEntry("av01", av1ConfigBox(0, 12, false, true, false)))
	// dOps 只是解码器初始化数据，不参与编码串拼接（少一个也不能改变结果）。
	dOps := boxBytes("dOps", []byte{0x00, 0x02, 0x01, 0x38, 0x00, 0x00, 0xbb, 0x80, 0x00, 0x00, 0x01, 0x04})
	audio := trakWithEntry(2, "soun", audioSampleEntry("Opus", dOps))

	info, err := ParseMoov(moovPayload(8523000, video, audio))
	if err != nil {
		t.Fatalf("ParseMoov 失败: %v", err)
	}

	if len(info.Codecs) != 2 || info.Codecs[0] != "av01.0.12M.10" || info.Codecs[1] != "opus" {
		t.Fatalf("codecs 不正确：%v", info.Codecs)
	}
	const want = `video/mp4; codecs="av01.0.12M.10,opus"`
	if info.MimeType != want {
		t.Fatalf("mimeType 不正确：\n得到 %q\n应为 %q", info.MimeType, want)
	}
	if info.VideoTrackID != 1 {
		t.Fatalf("视频轨 ID 不正确：%d", info.VideoTrackID)
	}
}

// Opus 的 sample entry 类型在 RFC 6381 里写作 "Opus"（首字母大写），
// 但也见过小写 "opus" 的封装；两种都要能解析，且编码串一律是全小写 "opus"。
func TestParseStsdOpusCaseInsensitive(t *testing.T) {
	for _, typ := range []string{"Opus", "opus"} {
		entry := audioSampleEntry(typ, boxBytes("dOps", make([]byte, 11)))

		codecs, err := parseStsd(stsdPayload(entry))
		if err != nil {
			t.Fatalf("%s: parseStsd 失败: %v", typ, err)
		}
		if len(codecs) != 1 || codecs[0] != "opus" {
			t.Fatalf("%s: 编码串应为 %q，实际 %v", typ, "opus", codecs)
		}
	}
}

// ---------- 负例：不能给出一个 play 不起来的 mimeType ----------

func TestParseStsdAV1MissingAv1C(t *testing.T) {
	// 只有 78 字节固定字段，没有 av1C。
	_, err := parseStsd(stsdPayload(visualSampleEntry("av01", nil)))
	if err == nil {
		t.Fatal("av01 缺少 av1C 时应当报错")
	}
	if !strings.Contains(err.Error(), "av1C") {
		t.Fatalf("错误信息应点名 av1C，实际: %v", err)
	}
}

func TestParseStsdUnknownSampleEntryStillFails(t *testing.T) {
	_, err := parseStsd(stsdPayload(visualSampleEntry("zzzz", nil)))
	if err == nil {
		t.Fatal("未知采样格式应当明确报错")
	}
	if !strings.Contains(err.Error(), "zzzz") {
		t.Fatalf("错误信息应点名未知格式，实际: %v", err)
	}
	// 错误信息要列出当前支持的编码，用户才知道下一步怎么办。
	for _, want := range []string{"avc1", "av01", "Opus"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误信息应列出支持的编码 %q，实际: %v", want, err)
		}
	}
}

func TestParseStsdUnsupportedKnownSampleEntryStillFails(t *testing.T) {
	// VP9 / HEVC 仍然不支持：既不能给出编码串，也不能给出一个能过 isTypeSupported 的假串。
	for _, typ := range []string{"vp09", "hvc1"} {
		_, err := parseStsd(stsdPayload(visualSampleEntry(typ, nil)))
		if err == nil {
			t.Fatalf("%s 应当报错", typ)
		}
		if !strings.Contains(err.Error(), typ) {
			t.Fatalf("错误信息应点名 %s，实际: %v", typ, err)
		}
	}
}
