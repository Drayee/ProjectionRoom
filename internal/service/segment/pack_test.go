package segment

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ProjectionRoom/internal/model"
)

// 本文件覆盖「分片打包」：每 N 片合成一个 pack-XXXX.bin。
//
// 两条硬要求：
//  1. 打包只改变"字节放在哪个文件里"，逐片字节与逐片 sha256 必须与不打包时完全一致；
//  2. -pack 1（不打包）必须与打包功能出现之前逐字节等价。
//
// Process 在输入已经是 fragmented MP4 且不要求重新封装/转码时**不需要 ffmpeg**，
// 因此下面用一个合成 fixture 覆盖主路径（可离线跑、结果确定）；
// 真实 ffmpeg 的端到端在 reale2e_test.go。

// ---------- 合成 fixture（与 internal/service/mp4/split_test.go 同构）----------

func packU32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func packU64(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

func packConcat(parts ...[]byte) []byte {
	var buf bytes.Buffer
	for _, p := range parts {
		buf.Write(p)
	}
	return buf.Bytes()
}

func packBox(typ string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(8+len(payload)))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}

// packFixed 生成 size 字节的缓冲区，并按 offset 写入指定片段。
func packFixed(size int, patches map[int][]byte) []byte {
	buf := make([]byte, size)
	for off, data := range patches {
		copy(buf[off:], data)
	}
	return buf
}

func packBuildMovie(trackID, timescale, durationMs uint32) []byte {
	mvhd := packBox("mvhd", packFixed(100, map[int][]byte{
		0:  {0}, // version 0
		12: packU32(timescale),
		16: packU32(durationMs),
	}))
	tkhd := packBox("tkhd", packFixed(84, map[int][]byte{
		0:  {0},
		12: packU32(trackID),
	}))
	mdhd := packBox("mdhd", packFixed(24, map[int][]byte{
		0:  {0},
		12: packU32(timescale),
		16: packU32(durationMs),
	}))
	hdlr := packBox("hdlr", packFixed(24, map[int][]byte{8: []byte("vide")}))

	avcC := packBox("avcC", []byte{0x01, 0x64, 0x00, 0x1f, 0xff, 0xe1, 0x00, 0x00})
	entry := packBox("avc1", append(packFixed(78, nil), avcC...))

	stsd := packBox("stsd", packConcat(packU32(0), packU32(1), entry))
	stbl := packBox("stbl", stsd)
	minf := packBox("minf", stbl)
	mdia := packBox("mdia", packConcat(mdhd, hdlr, minf))
	trak := packBox("trak", packConcat(tkhd, mdia))

	return packBox("moov", packConcat(mvhd, trak))
}

func packBuildMoof(trackID uint32, baseTime uint64, ticks uint32, keyframe bool) []byte {
	mfhd := packBox("mfhd", packConcat(packU32(0), packU32(1)))
	tfhd := packBox("tfhd", packConcat([]byte{0, 0, 0, 0}, packU32(trackID)))
	tfdt := packBox("tfdt", packConcat([]byte{1, 0, 0, 0}, packU64(baseTime)))

	// sample_is_non_sync_sample (0x00010000) 为 0 表示关键帧。
	sampleFlags := uint32(0x01010000)
	if keyframe {
		sampleFlags = 0x02000000
	}
	// trun flags = 0x000100 (duration) | 0x000400 (sample flags)
	trun := packBox("trun", packConcat([]byte{0x00, 0x00, 0x05, 0x00}, packU32(1), packU32(ticks), packU32(sampleFlags)))

	traf := packBox("traf", packConcat(tfhd, tfdt, trun))
	return packBox("moof", packConcat(mfhd, traf))
}

// packBuildFragmentedMP4 造一个可被 mp4.SplitFile 切分的 fMP4：
// ftyp + moov + 每个分片一个 moof+mdat + 尾部 mfra。
func packBuildFragmentedMP4(keyframes []bool) []byte {
	ftyp := packBox("ftyp", packConcat([]byte("isom"), packU32(0x200), []byte("isom")))
	moov := packBuildMovie(1, 1000, uint32(len(keyframes))*1000)

	parts := [][]byte{ftyp, moov}
	for i, kf := range keyframes {
		parts = append(parts, packBuildMoof(1, uint64(i)*1000, 1000, kf))
		parts = append(parts, packBox("mdat", bytes.Repeat([]byte{byte(i + 1)}, 32)))
	}
	parts = append(parts, packBox("mfra", make([]byte, 16)))

	return packConcat(parts...)
}

func packWriteTemp(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "input.mp4")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("写入临时输入失败: %v", err)
	}
	return path
}

func readAllFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	return data
}

// packRun 跑一次完整流水线。packSize 为 0 表示"不设置"（用默认值）。
func packRun(t *testing.T, src string, packSize int) (*Artifacts, string) {
	t.Helper()

	outDir := t.TempDir()
	artifacts, err := Process(context.Background(), src, outDir, ProcessOptions{
		SegmentSeconds: 1,
		PackSize:       packSize,
	})
	if err != nil {
		t.Fatalf("流水线失败（pack=%d）: %v", packSize, err)
	}
	if err := artifacts.Index.Validate(); err != nil {
		t.Fatalf("产物索引不自洽（pack=%d）: %v", packSize, err)
	}
	return artifacts, outDir
}

// packSegmentBytes 取出一个分片的字节：打包时按包内 offset/size 切，未打包时就是整个文件。
func packSegmentBytes(t *testing.T, dir string, index *model.Index, seg model.Segment) []byte {
	t.Helper()

	data := readAllFile(t, filepath.Join(dir, seg.File))
	if !index.Packed() {
		return data
	}
	if seg.Offset+seg.Size > int64(len(data)) {
		t.Fatalf("分片 %d 越过 %s 的边界（offset=%d, size=%d, 文件=%d）",
			seg.Index, seg.File, seg.Offset, seg.Size, len(data))
	}
	return data[seg.Offset : seg.Offset+seg.Size]
}

// ---------- 测试 ----------

// TestProcessPackMatchesUnpacked 是打包功能的字节级断言：
// 同一份输入分别按 -pack 1 与 -pack 2 跑完整流水线，逐片字节必须完全一致，
// 且两种布局拼回去都等于原文件。
func TestProcessPackMatchesUnpacked(t *testing.T) {
	keyframes := []bool{true, false, true, true, false}
	original := packBuildFragmentedMP4(keyframes)
	src := packWriteTemp(t, original)

	plain, plainDir := packRun(t, src, 1)
	packed, packedDir := packRun(t, src, 2)

	// 分片数不变；只有"文件数"变了。
	if len(plain.Index.Segments) != len(keyframes) || len(packed.Index.Segments) != len(keyframes) {
		t.Fatalf("分片数应保持 %d，实际未打包 %d / 打包 %d",
			len(keyframes), len(plain.Index.Segments), len(packed.Index.Segments))
	}
	if plain.Index.Packed() {
		t.Fatalf("PackSize=1 不该产出 packs: %+v", plain.Index.Packs)
	}
	if !packed.Index.Packed() {
		t.Fatal("PackSize=2 必须产出 packs")
	}

	// 5 片、每包 2 片 → 3 个包；产物文件数 7 → 5。
	if len(packed.Index.Packs) != 3 {
		t.Fatalf("5 片 / 每包 2 片应产出 3 个包，实际 %d（%+v）",
			len(packed.Index.Packs), packed.Index.Packs)
	}
	if len(plain.Files) != len(plain.Index.Segments)+2 {
		t.Fatalf("未打包时产物应为 index.json + init.mp4 + %d 片，实际 %d 个文件",
			len(plain.Index.Segments), len(plain.Files))
	}
	if len(packed.Files) != len(packed.Index.Packs)+2 {
		t.Fatalf("打包后产物应为 index.json + init.mp4 + %d 个包，实际 %d 个文件（%v）",
			len(packed.Index.Packs), len(packed.Files), packFileNames(packed.Files))
	}
	// 包清单必须按序覆盖全部分片。
	for i, pack := range packed.Index.Packs {
		if pack.FirstSegment != i*2+1 {
			t.Fatalf("第 %d 个包的 firstSegment 应为 %d，实际 %d", i+1, i*2+1, pack.FirstSegment)
		}
		if pack.File != packFileName(i+1) {
			t.Fatalf("第 %d 个包的文件名应为 %s，实际 %s", i+1, packFileName(i+1), pack.File)
		}
		if pack.Bytes <= 0 {
			t.Fatalf("第 %d 个包 %s 的 bytes 必须为正，实际 %d", i+1, pack.File, pack.Bytes)
		}
	}
	if packed.Index.Packs[2].Count != 1 {
		t.Fatalf("第 3 个包应只剩 1 片，实际 %d", packed.Index.Packs[2].Count)
	}

	var plainConcat, packedConcat bytes.Buffer
	plainConcat.Write(readAllFile(t, filepath.Join(plainDir, InitFileName)))
	packedConcat.Write(readAllFile(t, filepath.Join(packedDir, InitFileName)))

	for i := range plain.Index.Segments {
		plainSeg := plain.Index.Segments[i]
		packedSeg := packed.Index.Segments[i]

		plainBytes := packSegmentBytes(t, plainDir, plain.Index, plainSeg)
		packedBytes := packSegmentBytes(t, packedDir, packed.Index, packedSeg)

		if !bytes.Equal(plainBytes, packedBytes) {
			t.Fatalf("第 %d 片在两种布局下的字节不一致（%d vs %d 字节）",
				plainSeg.Index, len(plainBytes), len(packedBytes))
		}
		if plainSeg.Size != packedSeg.Size {
			t.Fatalf("第 %d 片的 size 在两种布局下不一致（%d vs %d）",
				plainSeg.Index, plainSeg.Size, packedSeg.Size)
		}
		// sha256 始终是对分片求的：打包不该改变它。
		if plainSeg.SHA256 != packedSeg.SHA256 {
			t.Fatalf("第 %d 片的 sha256 在两种布局下不一致", plainSeg.Index)
		}
		if string(packedBytes[4:8]) != "moof" {
			t.Fatalf("第 %d 片必须以 moof 开头，实际 %q", packedSeg.Index, packedBytes[4:8])
		}

		plainConcat.Write(plainBytes)
		packedConcat.Write(packedBytes)
	}

	if !bytes.Equal(plainConcat.Bytes(), original) {
		t.Fatalf("未打包产物拼不回原文件：原 %d 字节，拼回 %d 字节",
			len(original), plainConcat.Len())
	}
	if !bytes.Equal(packedConcat.Bytes(), original) {
		t.Fatalf("打包产物拼不回原文件：原 %d 字节，拼回 %d 字节",
			len(original), packedConcat.Len())
	}
	if !bytes.Equal(plainConcat.Bytes(), packedConcat.Bytes()) {
		t.Fatal("两种布局拼出来的字节流必须完全相同")
	}
}

// TestProcessPackOneKeepsLegacyLayout 锁定 -pack 1 与打包功能出现之前的输出一致：
// 文件名仍是 c00001.m4s、offset 仍是原始视频里的字节偏移、index.json 里没有 packs 字段。
func TestProcessPackOneKeepsLegacyLayout(t *testing.T) {
	original := packBuildFragmentedMP4([]bool{true, true, false})
	src := packWriteTemp(t, original)

	artifacts, dir := packRun(t, src, 1)
	index := artifacts.Index

	if index.Packed() {
		t.Fatal("PackSize=1 不该产出 packs")
	}
	raw := readAllFile(t, filepath.Join(dir, IndexFileName))
	if bytes.Contains(raw, []byte(`"packs"`)) {
		t.Fatalf("PackSize=1 的 index.json 不该出现 packs 字段：%s", raw)
	}

	// offset 是分片在原始 fMP4 里的偏移：init 段之后按序连续排布，末尾正好到文件结尾。
	offset := int64(len(readAllFile(t, filepath.Join(dir, InitFileName))))
	for i, seg := range index.Segments {
		want := "c" + pad5(i+1) + ".m4s"
		if seg.File != want {
			t.Fatalf("第 %d 片的文件名应为 %s，实际 %s", i+1, want, seg.File)
		}
		if seg.Offset != offset {
			t.Fatalf("第 %d 片的 offset 应为 %d（原始视频内的偏移），实际 %d", i+1, offset, seg.Offset)
		}
		if int64(len(readAllFile(t, filepath.Join(dir, seg.File)))) != seg.Size {
			t.Fatalf("第 %d 片的大小与索引不一致", i+1)
		}
		offset += seg.Size
	}
	if offset != int64(len(original)) {
		t.Fatalf("init + 全部分片应正好等于原文件 %d 字节，实际 %d", len(original), offset)
	}
}

// TestProcessPackDefaultIs100 锁定默认粒度：不传 PackSize 时按每 100 片一包。
func TestProcessPackDefaultIs100(t *testing.T) {
	if DefaultPackSize != 100 {
		t.Fatalf("默认打包粒度应为 100，实际 %d", DefaultPackSize)
	}

	src := packWriteTemp(t, packBuildFragmentedMP4([]bool{true, true, true, true}))
	artifacts, _ := packRun(t, src, 0) // 0 = 不设置 → 用默认值

	if !artifacts.Index.Packed() {
		t.Fatal("默认必须打包（产物文件数从 ~1800 降到 ~18 靠的就是它）")
	}
	if len(artifacts.Index.Packs) != 1 || artifacts.Index.Packs[0].Count != 4 {
		t.Fatalf("4 片在默认粒度下应装进 1 个包，实际 %+v", artifacts.Index.Packs)
	}
	if artifacts.Index.Packs[0].FirstSegment != 1 {
		t.Fatalf("第一个包应从第 1 片开始，实际 %d", artifacts.Index.Packs[0].FirstSegment)
	}
}

// TestCollectArtifactsTreatsPacksAsArtifacts 验证产物清单按"文件"而不是按"分片"收集：
// 一个包出现一次（而不是被它包含的 100 片各列一次），总字节也不重复计数。
func TestCollectArtifactsTreatsPacksAsArtifacts(t *testing.T) {
	src := packWriteTemp(t, packBuildFragmentedMP4([]bool{true, true, true, true}))
	artifacts, dir := packRun(t, src, 2)

	again, err := CollectArtifacts(dir, artifacts.Index)
	if err != nil {
		t.Fatalf("收集产物失败: %v", err)
	}

	want := []string{IndexFileName, InitFileName, packFileName(1), packFileName(2)}
	if len(again.Files) != len(want) {
		t.Fatalf("产物应为 %v，实际 %v", want, packFileNames(again.Files))
	}
	for i, name := range want {
		if again.Files[i].Name != name {
			t.Fatalf("第 %d 个产物应为 %s，实际 %s", i+1, name, again.Files[i].Name)
		}
	}

	var sum int64
	for _, f := range again.Files {
		sum += f.Size
	}
	if again.TotalBytes != sum {
		t.Fatalf("总字节不该重复计数：%d vs %d", again.TotalBytes, sum)
	}
}

func packFileName(n int) string {
	return "pack-" + pad4(n) + ".bin"
}

func pad4(n int) string {
	digits := []byte{'0', '0', '0', '0'}
	for i, v := 3, n; i >= 0 && v > 0; i, v = i-1, v/10 {
		digits[i] = byte('0' + v%10)
	}
	return string(digits)
}

func pad5(n int) string {
	digits := []byte{'0', '0', '0', '0', '0'}
	for i, v := 4, n; i >= 0 && v > 0; i, v = i-1, v/10 {
		digits[i] = byte('0' + v%10)
	}
	return string(digits)
}

func packFileNames(files []ArtifactFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Name)
	}
	return out
}

// ---------- 队列侧：分批必须按包切分 ----------

// writeFakePackedArtifacts 造一份打包布局的假产物：4 片装在 2 个包里（不依赖 ffmpeg）。
func writeFakePackedArtifacts(outDir string) (*Artifacts, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}

	const packBytes = 900
	index := &model.Index{
		Version:       1,
		InitFile:      InitFileName,
		MimeType:      `video/mp4; codecs="avc1.64001f"`,
		TotalDuration: 8,
		SegmentSec:    2,
		BitrateBps:    8000,
		TotalBytes:    1800,
		Segments: []model.Segment{
			{Index: 1, File: "pack-0001.bin", Offset: 0, Size: 450, Duration: 2, StartPTS: 0, Keyframe: true},
			{Index: 2, File: "pack-0001.bin", Offset: 450, Size: 450, Duration: 2, StartPTS: 2, Keyframe: true},
			{Index: 3, File: "pack-0002.bin", Offset: 0, Size: 450, Duration: 2, StartPTS: 4, Keyframe: true},
			{Index: 4, File: "pack-0002.bin", Offset: 450, Size: 450, Duration: 2, StartPTS: 6, Keyframe: true},
		},
		Packs: []model.Pack{
			{File: "pack-0001.bin", FirstSegment: 1, Count: 2, Bytes: packBytes},
			{File: "pack-0002.bin", FirstSegment: 3, Count: 2, Bytes: packBytes},
		},
	}
	if err := index.Validate(); err != nil {
		return nil, err
	}

	files := map[string][]byte{
		IndexFileName:   []byte(`{"version":1,"initFile":"init.mp4"}`),
		InitFileName:    bytes.Repeat([]byte{0xA1}, 48),
		"pack-0001.bin": bytes.Repeat([]byte{0xB2}, packBytes),
		"pack-0002.bin": bytes.Repeat([]byte{0xC3}, packBytes),
	}
	for name, payload := range files {
		if err := os.WriteFile(filepath.Join(outDir, name), payload, 0o644); err != nil {
			return nil, err
		}
	}

	return CollectArtifacts(outDir, index)
}

// TestQueueServesPackedArtifactsByPack 是分批下载的打包形态：
// result.segments 仍是分片数（4），result.files 是文件数（2 个包 + index + init = 4）；
// 每一份 zip 里都是**完整的包文件**，绝不把一个包劈到两份里。
func TestQueueServesPackedArtifactsByPack(t *testing.T) {
	cfg := testConfig(t)
	// 假产物约 33 + 48 + 2×900 字节，上限 1500 → 必然分批。
	cfg.Segment.SingleResponseMaxBytes = 1500

	proc := processorFunc(func(_ context.Context, _ string, outDir string, _ ProcessOptions) (*Artifacts, error) {
		return writeFakePackedArtifacts(outDir)
	})
	q := newTestQueue(t, cfg, nil, nil, proc, nil)

	view, err := submitBytes(t, q, []byte("payload"))
	if err != nil {
		t.Fatalf("提交不应失败: %v", err)
	}
	waitFor(t, "作业完成", 3*time.Second, func() bool {
		current, ok := q.Get(view.JobID)
		return ok && current.State == StateDone
	})

	done, _ := q.Get(view.JobID)
	result := done.Result
	if result.SingleResponse {
		t.Fatalf("产物超过上限时不该走单次返回: %+v", result)
	}
	if result.Segments != 4 {
		t.Fatalf("result.segments 应保持「分片数」= 4，实际 %d", result.Segments)
	}
	if result.Files != 4 {
		t.Fatalf("result.files 应是文件数 4（index + init + 2 个包），实际 %d", result.Files)
	}
	if len(result.Parts) < 2 {
		t.Fatalf("应至少切成 2 份，实际 %d", len(result.Parts))
	}

	seen := make(map[string]int)
	for _, part := range result.Parts {
		_, partFiles, _, err := q.PartFiles(view.JobID, part.N)
		if err != nil {
			t.Fatalf("读取第 %d 份失败: %v", part.N, err)
		}
		for _, f := range partFiles {
			seen[f.Name]++
			// 包是分批的最小单位：一份里出现的包文件必须是完整的整包。
			if f.Name == "pack-0001.bin" || f.Name == "pack-0002.bin" {
				if f.Size != 900 {
					t.Fatalf("第 %d 份里的 %s 只有 %d 字节，包不能被劈开", part.N, f.Name, f.Size)
				}
			}
		}
	}

	if len(seen) != 4 {
		t.Fatalf("各份合起来应覆盖全部 4 个产物文件，实际 %v", seen)
	}
	for name, count := range seen {
		if count != 1 {
			t.Fatalf("产物 %s 出现在 %d 份里，应只出现一次", name, count)
		}
	}
}
