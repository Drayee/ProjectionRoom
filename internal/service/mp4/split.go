package mp4

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"ProjectionRoom/internal/model"
)

// IndexVersion 是 index.json 的格式版本。
const IndexVersion = 1

// SplitOptions 控制切片输出。
type SplitOptions struct {
	// OutDir 是输出目录，不存在时创建。
	OutDir string
	// InitName 是初始化段文件名，默认 init.mp4。
	InitName string
	// SegmentPrefix 是分片文件名前缀，默认 c（产出 c00001.m4s…）。
	SegmentPrefix string
	// IndexName 是索引文件名，默认 index.json。
	IndexName string
}

func (o *SplitOptions) applyDefaults() {
	if o.InitName == "" {
		o.InitName = "init.mp4"
	}
	if o.SegmentPrefix == "" {
		o.SegmentPrefix = "c"
	}
	if o.IndexName == "" {
		o.IndexName = "index.json"
	}
}

type byteRange struct {
	start int64
	end   int64 // 不含
}

// SplitFile 把一个 fragmented MP4 按 moof 边界切分为
// init.mp4 + c00001.m4s… + index.json，并返回索引。
//
// 切分规则：init 段 = 第一个 moof 之前的全部字节（ftyp+moov）；
// 每个分片 = 一个 moof 到下一个 moof 之前；最后一段延伸到文件末尾（含 mfra 等尾部 box）。
// 这条规则保证 init + 全部分片按序拼接后与原文件逐字节相同。
func SplitFile(inPath string, opts SplitOptions) (*model.Index, error) {
	opts.applyDefaults()

	f, err := os.Open(inPath)
	if err != nil {
		return nil, fmt.Errorf("mp4: 打开输入失败: %w", err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("mp4: 读取输入信息失败: %w", err)
	}
	totalSize := stat.Size()

	tops, err := scanTopLevel(f, totalSize)
	if err != nil {
		return nil, err
	}

	moov := findTop(tops, "moov")
	if moov == nil {
		return nil, fmt.Errorf("mp4: %s 不是有效的 MP4（缺少 moov）", inPath)
	}
	moovPayload, err := readRange(f, moov.Offset+moov.HeaderLen, moov.Size-moov.HeaderLen)
	if err != nil {
		return nil, err
	}
	movie, err := ParseMoov(moovPayload)
	if err != nil {
		return nil, err
	}

	moofs := filterTop(tops, "moof")
	if len(moofs) == 0 {
		return nil, fmt.Errorf(
			"mp4: %s 不是 fragmented MP4（没有 moof box），无法按分片边界切分。\n"+
				"请先处理：\n"+
				"  ffmpeg -i %s -c copy -movflags +frag_keyframe+empty_moov+default_base_moof -frag_duration 2000000 out_frag.mp4\n"+
				"或直接用 segmenter 的低码率预设：\n"+
				"  segmenter -in %s -out ./room-media -transcode 1200k",
			inPath, inPath, inPath)
	}

	initEnd := moofs[0].Offset
	ranges := make([]byteRange, len(moofs))
	for i, m := range moofs {
		end := totalSize
		if i+1 < len(moofs) {
			end = moofs[i+1].Offset
		}
		ranges[i] = byteRange{start: m.Offset, end: end}
	}

	if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
		return nil, fmt.Errorf("mp4: 创建输出目录失败: %w", err)
	}

	index := &model.Index{
		Version:    IndexVersion,
		InitFile:   opts.InitName,
		MimeType:   movie.MimeType,
		TotalBytes: totalSize,
	}

	if _, err := copyRange(f, 0, initEnd, filepath.Join(opts.OutDir, opts.InitName), nil); err != nil {
		return nil, err
	}

	for i, r := range ranges {
		moofPayload, err := readRange(f, moofs[i].Offset+moofs[i].HeaderLen, moofs[i].Size-moofs[i].HeaderLen)
		if err != nil {
			return nil, err
		}
		info, err := parseMoof(moofPayload, movie)
		if err != nil {
			return nil, fmt.Errorf("mp4: 解析第 %d 个 moof 失败: %w", i+1, err)
		}

		name := fmt.Sprintf("%s%05d.m4s", opts.SegmentPrefix, i+1)
		hasher := sha256.New()
		size, err := copyRange(f, r.start, r.end, filepath.Join(opts.OutDir, name), hasher)
		if err != nil {
			return nil, err
		}

		index.Segments = append(index.Segments, model.Segment{
			Index:    i + 1,
			File:     name,
			Offset:   r.start,
			Size:     size,
			Duration: info.Duration,
			StartPTS: info.StartPTS,
			Keyframe: info.Keyframe,
			SHA256:   hex.EncodeToString(hasher.Sum(nil)),
		})
	}

	// trun 未带 sample duration 时，用相邻分片的 PTS 差补齐（最后一段用影片时长）。
	fillDurations(index, movie.Duration)

	index.TotalDuration = 0
	for _, seg := range index.Segments {
		index.TotalDuration += seg.Duration
	}
	if index.TotalDuration <= 0 {
		index.TotalDuration = movie.Duration
	}
	index.SegmentSec = index.MeanSegmentSeconds()
	if index.TotalDuration > 0 {
		index.BitrateBps = int64(float64(totalSize) * 8 / index.TotalDuration)
	}

	if err := index.Validate(); err != nil {
		return nil, fmt.Errorf("mp4: 生成的索引不自洽: %w", err)
	}

	payload, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("mp4: 序列化索引失败: %w", err)
	}
	if err := os.WriteFile(filepath.Join(opts.OutDir, opts.IndexName), payload, 0o644); err != nil {
		return nil, fmt.Errorf("mp4: 写入索引失败: %w", err)
	}

	return index, nil
}

// moofInfo 是一个 moof 的关键时间信息。
type moofInfo struct {
	StartPTS float64
	Duration float64
	Keyframe bool
}

// trafInfo 是一个 traf 的解析结果。
type trafInfo struct {
	TrackID       uint32
	BaseTime      uint64
	BaseTimeSet   bool
	SampleTicks   uint64
	Keyframe      bool
	FlagsResolved bool
}

// parseMoof 解析 moof，取视频轨的时间与关键帧信息。
// 音频轨与视频轨共用一个 moof 时，以视频轨为准（同步与 seek 都落在视频时间轴上）。
func parseMoof(payload []byte, movie *MovieInfo) (moofInfo, error) {
	children, err := parseChildren(payload)
	if err != nil {
		return moofInfo{}, err
	}
	trafs := findBoxes(children, "traf")
	if len(trafs) == 0 {
		return moofInfo{}, fmt.Errorf("moof 缺少 traf")
	}

	var chosen *trafInfo
	for i := range trafs {
		info, err := parseTraf(trafs[i].Data)
		if err != nil {
			return moofInfo{}, err
		}
		if chosen == nil {
			chosen = &info
		}
		if info.TrackID == movie.VideoTrackID {
			chosen = &info
			break
		}
	}
	if chosen == nil || !chosen.BaseTimeSet {
		return moofInfo{}, fmt.Errorf("moof 中没有可用的 tfdt（无法确定分片起点）")
	}

	timescale := movie.VideoTimescale
	if timescale == 0 {
		timescale = movie.Timescale
	}
	if timescale == 0 {
		return moofInfo{}, fmt.Errorf("缺少 timescale，无法把 tfdt 换算成秒")
	}

	return moofInfo{
		StartPTS: float64(chosen.BaseTime) / float64(timescale),
		Duration: float64(chosen.SampleTicks) / float64(timescale),
		Keyframe: chosen.Keyframe,
	}, nil
}

func parseTraf(payload []byte) (trafInfo, error) {
	children, err := parseChildren(payload)
	if err != nil {
		return trafInfo{}, err
	}

	var info trafInfo

	tfhd := findBox(children, "tfhd")
	if tfhd == nil || len(tfhd.Data) < 8 {
		return info, fmt.Errorf("traf 缺少 tfhd")
	}
	tfhdFlags := be24(tfhd.Data[1:4])
	info.TrackID = binary.BigEndian.Uint32(tfhd.Data[4:8])

	p := 8
	if tfhdFlags&0x000001 != 0 { // base-data-offset-present
		p += 8
	}
	if tfhdFlags&0x000002 != 0 { // sample-description-index-present
		p += 4
	}
	var defaultDuration uint32
	if tfhdFlags&0x000008 != 0 { // default-sample-duration-present
		if p+4 > len(tfhd.Data) {
			return info, errTruncated
		}
		defaultDuration = binary.BigEndian.Uint32(tfhd.Data[p : p+4])
		p += 4
	}
	if tfhdFlags&0x000010 != 0 { // default-sample-size-present
		p += 4
	}
	var defaultFlags uint32
	defaultFlagsSet := false
	if tfhdFlags&0x000020 != 0 { // default-sample-flags-present
		if p+4 > len(tfhd.Data) {
			return info, errTruncated
		}
		defaultFlags = binary.BigEndian.Uint32(tfhd.Data[p : p+4])
		defaultFlagsSet = true
	}

	if tfdt := findBox(children, "tfdt"); tfdt != nil {
		if len(tfdt.Data) < 8 {
			return info, errTruncated
		}
		switch tfdt.Data[0] {
		case 0:
			info.BaseTime = uint64(binary.BigEndian.Uint32(tfdt.Data[4:8]))
		case 1:
			if len(tfdt.Data) < 12 {
				return info, errTruncated
			}
			info.BaseTime = binary.BigEndian.Uint64(tfdt.Data[4:12])
		default:
			return info, fmt.Errorf("tfdt 版本不支持 (%d)", tfdt.Data[0])
		}
		info.BaseTimeSet = true
	}

	truns := findBoxes(children, "trun")
	if len(truns) == 0 {
		return info, fmt.Errorf("traf 缺少 trun")
	}

	var firstFlags uint32
	firstFlagsSet := false

	for trunIndex, trun := range truns {
		if len(trun.Data) < 8 {
			return info, errTruncated
		}
		trunFlags := be24(trun.Data[1:4])
		sampleCount := binary.BigEndian.Uint32(trun.Data[4:8])

		p := 8
		if trunFlags&0x000001 != 0 { // data-offset-present
			p += 4
		}
		if trunFlags&0x000004 != 0 { // first-sample-flags-present
			if p+4 > len(trun.Data) {
				return info, errTruncated
			}
			if trunIndex == 0 {
				firstFlags = binary.BigEndian.Uint32(trun.Data[p : p+4])
				firstFlagsSet = true
			}
			p += 4
		}

		durationPresent := trunFlags&0x000100 != 0
		sizePresent := trunFlags&0x000200 != 0
		flagsPresent := trunFlags&0x000400 != 0
		ctoPresent := trunFlags&0x000800 != 0

		for s := uint32(0); s < sampleCount; s++ {
			if durationPresent {
				if p+4 > len(trun.Data) {
					return info, errTruncated
				}
				info.SampleTicks += uint64(binary.BigEndian.Uint32(trun.Data[p : p+4]))
				p += 4
			} else if defaultDuration > 0 {
				info.SampleTicks += uint64(defaultDuration)
			}
			if sizePresent {
				p += 4
			}
			if flagsPresent {
				if p+4 > len(trun.Data) {
					return info, errTruncated
				}
				if trunIndex == 0 && s == 0 && !firstFlagsSet {
					firstFlags = binary.BigEndian.Uint32(trun.Data[p : p+4])
					firstFlagsSet = true
				}
				p += 4
			}
			if ctoPresent {
				p += 4
			}
			if p > len(trun.Data) {
				return info, errTruncated
			}
		}
	}

	// sample_is_non_sync_sample 是 bit 16；为 0 表示首样本是关键帧（可作 seek 落点）。
	switch {
	case firstFlagsSet:
		info.Keyframe = firstFlags&0x00010000 == 0
		info.FlagsResolved = true
	case defaultFlagsSet:
		info.Keyframe = defaultFlags&0x00010000 == 0
		info.FlagsResolved = true
	}

	return info, nil
}

func fillDurations(index *model.Index, movieDuration float64) {
	n := len(index.Segments)
	for i := range index.Segments {
		if index.Segments[i].Duration > 0 {
			continue
		}
		switch {
		case i+1 < n:
			index.Segments[i].Duration = index.Segments[i+1].StartPTS - index.Segments[i].StartPTS
		case movieDuration > index.Segments[i].StartPTS:
			index.Segments[i].Duration = movieDuration - index.Segments[i].StartPTS
		}
	}
}

func scanTopLevel(r io.ReaderAt, size int64) ([]boxHeader, error) {
	var tops []boxHeader

	for off := int64(0); off < size; {
		header, err := readBoxHeaderAt(r, off)
		if err != nil {
			return nil, err
		}
		if header.Size == -1 {
			header.Size = size - off
		}
		if header.Size <= 0 || off+header.Size > size {
			return nil, fmt.Errorf("mp4: box %q 越界 (offset=%d, size=%d, 文件=%d)", header.Type, off, header.Size, size)
		}

		tops = append(tops, header)
		off += header.Size
	}

	return tops, nil
}

func findTop(tops []boxHeader, typ string) *boxHeader {
	for i := range tops {
		if tops[i].Type == typ {
			return &tops[i]
		}
	}
	return nil
}

func filterTop(tops []boxHeader, typ string) []boxHeader {
	var out []boxHeader
	for _, t := range tops {
		if t.Type == typ {
			out = append(out, t)
		}
	}
	return out
}

func readRange(r io.ReaderAt, start, length int64) ([]byte, error) {
	buf := make([]byte, length)
	if _, err := r.ReadAt(buf, start); err != nil && err != io.EOF {
		return nil, fmt.Errorf("mp4: 读取 %d@%d 失败: %w", length, start, err)
	}
	return buf, nil
}

// copyRange 把 [start,end) 复制到 dst；sink 非空时同时写入（用于边写边算哈希）。
func copyRange(r io.ReaderAt, start, end int64, dst string, sink io.Writer) (int64, error) {
	out, err := os.Create(dst)
	if err != nil {
		return 0, fmt.Errorf("mp4: 创建 %s 失败: %w", dst, err)
	}

	writer := io.Writer(out)
	if sink != nil {
		writer = io.MultiWriter(out, sink)
	}

	written, err := io.Copy(writer, io.NewSectionReader(r, start, end-start))
	closeErr := out.Close()
	if err != nil {
		return 0, fmt.Errorf("mp4: 写入 %s 失败: %w", filepath.Base(dst), err)
	}
	if closeErr != nil {
		return 0, fmt.Errorf("mp4: 关闭 %s 失败: %w", filepath.Base(dst), closeErr)
	}

	return written, nil
}

func be24(b []byte) uint32 {
	return uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2])
}
