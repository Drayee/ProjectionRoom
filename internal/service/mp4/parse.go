package mp4

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// TrackInfo 描述 moov 中的一条轨道。
type TrackInfo struct {
	TrackID   uint32
	Timescale uint32
	Handler   string // 'vide' / 'soun'
	Codecs    []string
}

// MovieInfo 是 moov 的解析结果。
type MovieInfo struct {
	Timescale      uint32
	Duration       float64 // 秒
	VideoTrackID   uint32
	VideoTimescale uint32
	MimeType       string
	Codecs         []string
}

// ParseMoov 解析 moov box 的负载，得到时长、视频轨标识与 MSE 需要的 mimeType。
func ParseMoov(payload []byte) (*MovieInfo, error) {
	children, err := parseChildren(payload)
	if err != nil {
		return nil, err
	}

	info := &MovieInfo{}
	if mvhd := findBox(children, "mvhd"); mvhd != nil {
		timescale, duration, err := parseMvhd(mvhd.Data)
		if err != nil {
			return nil, err
		}
		info.Timescale = timescale
		if timescale > 0 {
			info.Duration = float64(duration) / float64(timescale)
		}
	}

	traks := findBoxes(children, "trak")
	if len(traks) == 0 {
		return nil, fmt.Errorf("mp4: moov 不包含任何 trak")
	}

	var videoCodecs, audioCodecs []string
	for _, trak := range traks {
		track, err := parseTrak(trak.Data)
		if err != nil {
			return nil, err
		}
		switch track.Handler {
		case "vide":
			if info.VideoTrackID == 0 {
				info.VideoTrackID = track.TrackID
				info.VideoTimescale = track.Timescale
				videoCodecs = append(videoCodecs, track.Codecs...)
			}
		case "soun":
			audioCodecs = append(audioCodecs, track.Codecs...)
		}
	}

	if info.VideoTrackID == 0 {
		return nil, fmt.Errorf("mp4: 只找到音频轨；M2 的播放链路要求文件含视频轨")
	}

	info.Codecs = append(append([]string{}, videoCodecs...), audioCodecs...)
	if len(info.Codecs) == 0 {
		return nil, fmt.Errorf("mp4: 无法从 stsd 解析出编码串")
	}
	// A/V 复用在同一条分片里，因此用 video/mp4 承载（SPEC §4.3 的 C3 决策）。
	info.MimeType = "video/mp4; codecs=" + codecString(info.Codecs)

	return info, nil
}

func parseMvhd(data []byte) (timescale uint32, duration uint64, err error) {
	if len(data) < 4 {
		return 0, 0, errTruncated
	}
	switch data[0] {
	case 0:
		if len(data) < 20 {
			return 0, 0, errTruncated
		}
		return binary.BigEndian.Uint32(data[12:16]), uint64(binary.BigEndian.Uint32(data[16:20])), nil
	case 1:
		if len(data) < 32 {
			return 0, 0, errTruncated
		}
		return binary.BigEndian.Uint32(data[20:24]), binary.BigEndian.Uint64(data[24:32]), nil
	default:
		return 0, 0, fmt.Errorf("mp4: mvhd 版本不支持 (%d)", data[0])
	}
}

func parseTrak(data []byte) (TrackInfo, error) {
	children, err := parseChildren(data)
	if err != nil {
		return TrackInfo{}, err
	}

	var info TrackInfo
	if tkhd := findBox(children, "tkhd"); tkhd != nil {
		trackID, err := parseTkhdTrackID(tkhd.Data)
		if err != nil {
			return TrackInfo{}, err
		}
		info.TrackID = trackID
	}

	mdia := findBox(children, "mdia")
	if mdia == nil {
		return info, nil
	}
	mdiaChildren, err := parseChildren(mdia.Data)
	if err != nil {
		return TrackInfo{}, err
	}

	if mdhd := findBox(mdiaChildren, "mdhd"); mdhd != nil {
		timescale, err := parseMdhdTimescale(mdhd.Data)
		if err != nil {
			return TrackInfo{}, err
		}
		info.Timescale = timescale
	}
	if hdlr := findBox(mdiaChildren, "hdlr"); hdlr != nil && len(hdlr.Data) >= 12 {
		info.Handler = string(hdlr.Data[8:12])
	}

	minf := findBox(mdiaChildren, "minf")
	if minf == nil {
		return info, nil
	}
	minfChildren, err := parseChildren(minf.Data)
	if err != nil {
		return TrackInfo{}, err
	}
	stbl := findBox(minfChildren, "stbl")
	if stbl == nil {
		return info, nil
	}
	stblChildren, err := parseChildren(stbl.Data)
	if err != nil {
		return TrackInfo{}, err
	}
	stsd := findBox(stblChildren, "stsd")
	if stsd == nil {
		return info, nil
	}

	codecs, err := parseStsd(stsd.Data)
	if err != nil {
		return TrackInfo{}, err
	}
	info.Codecs = codecs

	return info, nil
}

func parseTkhdTrackID(data []byte) (uint32, error) {
	if len(data) < 4 {
		return 0, errTruncated
	}
	switch data[0] {
	case 0:
		if len(data) < 16 {
			return 0, errTruncated
		}
		return binary.BigEndian.Uint32(data[12:16]), nil
	case 1:
		if len(data) < 24 {
			return 0, errTruncated
		}
		return binary.BigEndian.Uint32(data[20:24]), nil
	default:
		return 0, fmt.Errorf("mp4: tkhd 版本不支持 (%d)", data[0])
	}
}

func parseMdhdTimescale(data []byte) (uint32, error) {
	if len(data) < 4 {
		return 0, errTruncated
	}
	switch data[0] {
	case 0:
		if len(data) < 16 {
			return 0, errTruncated
		}
		return binary.BigEndian.Uint32(data[12:16]), nil
	case 1:
		if len(data) < 24 {
			return 0, errTruncated
		}
		return binary.BigEndian.Uint32(data[20:24]), nil
	default:
		return 0, fmt.Errorf("mp4: mdhd 版本不支持 (%d)", data[0])
	}
}

// parseStsd 解析 sample description，返回 RFC 6381 形式的编码串（如 avc1.64001f、av01.0.12M.10、opus）。
func parseStsd(data []byte) ([]string, error) {
	if len(data) < 8 {
		return nil, errTruncated
	}
	entryCount := binary.BigEndian.Uint32(data[4:8])

	var codecs []string
	off := 8
	for range entryCount {
		if off+8 > len(data) {
			return nil, errTruncated
		}
		size := int(binary.BigEndian.Uint32(data[off : off+4]))
		typ := string(data[off+4 : off+8])
		if size < 8 || off+size > len(data) {
			return nil, fmt.Errorf("mp4: stsd 条目 %q 长度非法 (size=%d)", typ, size)
		}

		codec, err := codecFromSampleEntry(typ, data[off:off+size])
		if err != nil {
			return nil, err
		}
		codecs = append(codecs, codec)
		off += size
	}

	return codecs, nil
}

// supportedCodecsHint 是错误信息里列出的"当前支持的编码"。
// 集中一处，避免文案在多个分支里各写一份、改一处漏一处。
const supportedCodecsHint = "当前支持 avc1/avc3(H.264)、av01(AV1)、mp4a(AAC)、Opus"

// codecFromSampleEntry 把采样条目换算成 RFC 6381 编码串。
// 支持的格式：H.264(avc1/avc3)、AV1(av01)、AAC(mp4a) 与 Opus(Opus)。
// 其它格式（hvc1/hev1/vp09/…）明确报错，而不是给出一个 play 不起来的 mimeType。
func codecFromSampleEntry(typ string, entry []byte) (string, error) {
	switch typ {
	case "avc1", "avc3":
		// VisualSampleEntry 的固定字段共 78 字节，加 8 字节 box 头 = 86。
		if len(entry) < 86 {
			return "", errTruncated
		}
		children, err := parseChildren(entry[86:])
		if err != nil {
			return "", err
		}
		avcC := findBox(children, "avcC")
		if avcC == nil {
			return "", fmt.Errorf("mp4: %s 缺少 avcC 配置", typ)
		}
		if len(avcC.Data) < 4 {
			return "", errTruncated
		}
		// AVCDecoderConfigurationRecord: version, profile, compatibility, level
		return fmt.Sprintf("%s.%02x%02x%02x", typ, avcC.Data[1], avcC.Data[2], avcC.Data[3]), nil

	case "av01":
		// VisualSampleEntry 的固定字段共 78 字节，加 8 字节 box 头 = 86。
		if len(entry) < 86 {
			return "", errTruncated
		}
		children, err := parseChildren(entry[86:])
		if err != nil {
			return "", err
		}
		av1C := findBox(children, "av1C")
		if av1C == nil {
			return "", fmt.Errorf("mp4: av01 缺少 av1C 配置")
		}
		return av1CodecString(av1C.Data)

	case "mp4a":
		// AudioSampleEntry 的固定字段共 28 字节，加 8 字节 box 头 = 36。
		if len(entry) < 36 {
			return "", errTruncated
		}
		children, err := parseChildren(entry[36:])
		if err != nil {
			return "", err
		}
		esds := findBox(children, "esds")
		if esds == nil {
			return "", fmt.Errorf("mp4: mp4a 缺少 esds 配置")
		}
		audioObjectType, err := parseESDS(esds.Data)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("mp4a.40.%d", audioObjectType), nil

	case "Opus", "opus":
		// AudioSampleEntry 的固定字段共 28 字节，加 8 字节 box 头 = 36。
		// RFC 6381 里 Opus 的编码串就是 "opus"（全小写）；
		// dOps（OpusSpecificBox）只是解码器初始化数据，不参与编码串拼接。
		if len(entry) < 36 {
			return "", errTruncated
		}
		return "opus", nil

	case "hvc1", "hev1", "vp09", "vp08", "avc2":
		return "", fmt.Errorf("mp4: 暂不支持 %s（%s；其它编码可用 ffmpeg -c:v libx264 -c:a aac 转码）",
			typ, supportedCodecsHint)

	default:
		return "", fmt.Errorf("mp4: 未知采样格式 %q（%s）", typ, supportedCodecsHint)
	}
}

// av1CodecString 按 AV1-ISOBMFF 的 av1C（AV1CodecConfigurationRecord）生成 RFC 6381 编码串：
//
//	av01.<seq_profile>.<seq_level_idx><seq_tier>.<bitDepth>   例如 av01.0.12M.10
//
// av1C 负载的位布局：
//
//	第 0 字节 = marker(1) + version(7)
//	第 1 字节 = seq_profile(高 3 位) | seq_level_idx_0(低 5 位)
//	第 2 字节 = seq_tier_0(bit7) | high_bitdepth(bit6) | twelve_bit(bit5) |
//	           monochrome(bit4) | chroma_subsampling_x(bit3) |
//	           chroma_subsampling_y(bit2) | chroma_sample_position(bit1-0)
//
// level 写两位十进制（5 → "05"，12 → "12"），bitDepth 写两位（8 → "08"，10 → "10"）。
func av1CodecString(av1c []byte) (string, error) {
	if len(av1c) < 3 {
		return "", errTruncated
	}
	profile := (av1c[1] >> 5) & 0x07
	levelIdx := av1c[1] & 0x1f

	tier := "M"
	if av1c[2]&0x80 != 0 {
		tier = "H"
	}

	// high_bitdepth=0 → 8bit；=1 时由 twelve_bit 决定 10 还是 12 bit。
	bitDepth := 8
	if av1c[2]&0x40 != 0 {
		if av1c[2]&0x20 != 0 {
			bitDepth = 12
		} else {
			bitDepth = 10
		}
	}

	return fmt.Sprintf("av01.%d.%02d%s.%02d", profile, levelIdx, tier, bitDepth), nil
}

// parseESDS 从 esds 中取出 AAC 的 audioObjectType（AAC-LC = 2）。
func parseESDS(data []byte) (int, error) {
	const aacLC = 2
	if len(data) < 5 {
		return 0, errTruncated
	}
	// 跳过 ES_Descriptor 之前的 version/flags。
	tag, esBody, err := readDescriptor(data[4:])
	if err != nil {
		return 0, err
	}
	if tag != 0x03 {
		return 0, fmt.Errorf("mp4: esds 期望 ES_Descriptor(0x03)，实际 0x%02x", tag)
	}
	if len(esBody) < 3 {
		return 0, errTruncated
	}

	// ES_Descriptor: ES_ID(2) + flags(1) + 可选字段
	flags := esBody[2]
	body := esBody[3:]
	if flags&0x80 != 0 { // streamDependenceFlag
		if len(body) < 2 {
			return 0, errTruncated
		}
		body = body[2:]
	}
	if flags&0x40 != 0 { // URL_Flag
		if len(body) < 1 {
			return 0, errTruncated
		}
		urlLen := int(body[0])
		body = body[1:]
		if len(body) < urlLen {
			return 0, errTruncated
		}
		body = body[urlLen:]
	}
	if flags&0x20 != 0 { // OCRstreamFlag
		if len(body) < 2 {
			return 0, errTruncated
		}
		body = body[2:]
	}

	tag, decBody, err := readDescriptor(body)
	if err != nil {
		return 0, err
	}
	if tag != 0x04 {
		return 0, fmt.Errorf("mp4: esds 期望 DecoderConfigDescriptor(0x04)，实际 0x%02x", tag)
	}
	if len(decBody) < 13 {
		return 0, errTruncated
	}
	if objectType := decBody[0]; objectType != 0x40 {
		return 0, fmt.Errorf("mp4: 不支持的 objectTypeIndication 0x%02x（只处理 MPEG-4 Audio）", objectType)
	}

	// 没有 DecoderSpecificInfo 时按 AAC-LC 处理。
	tag, asc, err := readDescriptor(decBody[13:])
	if err != nil || tag != 0x05 || len(asc) == 0 {
		return aacLC, nil
	}
	audioObjectType := int(asc[0] >> 3)
	if audioObjectType == 0 {
		// 0 表示扩展的 audioObjectType，需要更多位；M2 直接按 AAC-LC 处理。
		return aacLC, nil
	}
	return audioObjectType, nil
}

// readDescriptor 读取一个 MPEG-4 描述符（tag + 可变长长度 + 负载）。
func readDescriptor(b []byte) (tag byte, body []byte, err error) {
	if len(b) < 2 {
		return 0, nil, errTruncated
	}
	tag = b[0]

	size := 0
	i := 1
	for {
		if i >= len(b) || i > 5 {
			return 0, nil, fmt.Errorf("mp4: 描述符长度非法")
		}
		c := b[i]
		i++
		size = size<<7 | int(c&0x7f)
		if c&0x80 == 0 {
			break
		}
	}

	if i+size > len(b) {
		return 0, nil, errTruncated
	}
	return tag, b[i : i+size], nil
}

// FormatCodecs 便于日志与错误信息展示。
func FormatCodecs(codecs []string) string {
	return strings.Join(codecs, ", ")
}
