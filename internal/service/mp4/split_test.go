package mp4

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ProjectionRoom/internal/model"
)

// ---------- 合成 fixture：不依赖 ffmpeg，保证解析逻辑有确定性覆盖 ----------

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func u64(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

func concat(parts ...[]byte) []byte {
	var buf bytes.Buffer
	for _, p := range parts {
		buf.Write(p)
	}
	return buf.Bytes()
}

// fixed 生成 size 字节的缓冲区，并按 offset 写入指定片段。
func fixed(size int, patches map[int][]byte) []byte {
	buf := make([]byte, size)
	for off, data := range patches {
		copy(buf[off:], data)
	}
	return buf
}

func buildSyntheticMovie(trackID, timescale, durationMs uint32, sampleEntry string) []byte {
	mvhd := boxBytes("mvhd", fixed(100, map[int][]byte{
		0:  {0}, // version 0
		12: u32(timescale),
		16: u32(durationMs),
	}))
	tkhd := boxBytes("tkhd", fixed(84, map[int][]byte{
		0:  {0},
		12: u32(trackID),
	}))
	mdhd := boxBytes("mdhd", fixed(24, map[int][]byte{
		0:  {0},
		12: u32(timescale),
		16: u32(durationMs),
	}))
	hdlr := boxBytes("hdlr", fixed(24, map[int][]byte{8: []byte("vide")}))

	var entry []byte
	switch sampleEntry {
	case "avc1":
		avcC := boxBytes("avcC", []byte{0x01, 0x64, 0x00, 0x1f, 0xff, 0xe1, 0x00, 0x00})
		entry = boxBytes("avc1", append(fixed(78, nil), avcC...))
	default:
		entry = boxBytes(sampleEntry, fixed(78, nil))
	}

	stsd := boxBytes("stsd", concat(u32(0), u32(1), entry))
	stbl := boxBytes("stbl", stsd)
	minf := boxBytes("minf", stbl)
	mdia := boxBytes("mdia", concat(mdhd, hdlr, minf))
	trak := boxBytes("trak", concat(tkhd, mdia))

	return boxBytes("moov", concat(mvhd, trak))
}

func buildMoof(trackID uint32, baseTime uint64, ticks uint32, keyframe bool) []byte {
	mfhd := boxBytes("mfhd", concat(u32(0), u32(1)))
	tfhd := boxBytes("tfhd", concat([]byte{0, 0, 0, 0}, u32(trackID)))
	tfdt := boxBytes("tfdt", concat([]byte{1, 0, 0, 0}, u64(baseTime)))

	// sample_is_non_sync_sample (0x00010000) 为 0 表示关键帧。
	sampleFlags := uint32(0x01010000)
	if keyframe {
		sampleFlags = 0x02000000
	}
	// trun flags = 0x000100 (duration) | 0x000400 (sample flags)
	trun := boxBytes("trun", concat([]byte{0x00, 0x00, 0x05, 0x00}, u32(1), u32(ticks), u32(sampleFlags)))

	traf := boxBytes("traf", concat(tfhd, tfdt, trun))
	return boxBytes("moof", concat(mfhd, traf))
}

func buildSyntheticFragmentedMP4(t *testing.T, keyframes []bool) []byte {
	t.Helper()

	ftyp := boxBytes("ftyp", concat([]byte("isom"), u32(0x200), []byte("isom")))
	moov := buildSyntheticMovie(1, 1000, uint32(len(keyframes))*1000, "avc1")

	parts := [][]byte{ftyp, moov}
	for i, kf := range keyframes {
		parts = append(parts, buildMoof(1, uint64(i)*1000, 1000, kf))
		parts = append(parts, boxBytes("mdat", bytes.Repeat([]byte{byte(i + 1)}, 32)))
	}
	parts = append(parts, boxBytes("mfra", make([]byte, 16)))

	return concat(parts...)
}

// ---------- 断言辅助 ----------

func readFileBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	return data
}

// assertRoundTrip 是切片正确性的核心断言：
// init + 全部分片按序拼接必须与原文件逐字节相同，且每个分片都以 moof 开头。
func assertRoundTrip(t *testing.T, original []byte, outDir string, index *model.Index) {
	t.Helper()

	initData := readFileBytes(t, filepath.Join(outDir, index.InitFile))
	if string(initData[4:8]) != "ftyp" {
		t.Fatalf("init 段应以 ftyp 开头，实际 %q", initData[4:8])
	}
	if !bytes.Contains(initData, []byte("moov")) {
		t.Fatal("init 段必须包含 moov")
	}

	var rebuilt bytes.Buffer
	rebuilt.Write(initData)

	for _, seg := range index.Segments {
		data := readFileBytes(t, filepath.Join(outDir, seg.File))
		if string(data[4:8]) != "moof" {
			t.Fatalf("分片 %s 必须以 moof 开头，实际 %q", seg.File, data[4:8])
		}
		if int64(len(data)) != seg.Size {
			t.Fatalf("分片 %s 实际大小 %d 与索引 %d 不一致", seg.File, len(data), seg.Size)
		}

		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != seg.SHA256 {
			t.Fatalf("分片 %s 的 SHA256 与索引不一致", seg.File)
		}

		rebuilt.Write(data)
	}

	if !bytes.Equal(rebuilt.Bytes(), original) {
		t.Fatalf("拼接结果与原文件不一致：原 %d 字节，拼回 %d 字节", len(original), rebuilt.Len())
	}
}

// ---------- 测试 ----------

func TestSplitSyntheticFixture(t *testing.T) {
	original := buildSyntheticFragmentedMP4(t, []bool{true, false, true})
	outDir := t.TempDir()

	index, err := SplitFile(writeTemp(t, original), SplitOptions{OutDir: outDir})
	if err != nil {
		t.Fatalf("SplitFile 失败: %v", err)
	}

	if err := index.Validate(); err != nil {
		t.Fatalf("生成的索引不自洽: %v", err)
	}
	if len(index.Segments) != 3 {
		t.Fatalf("应切出 3 个分片，实际 %d", len(index.Segments))
	}
	if index.MimeType != `video/mp4; codecs="avc1.64001f"` {
		t.Fatalf("mimeType 不正确: %q", index.MimeType)
	}
	if index.InitFile != "init.mp4" {
		t.Fatalf("init 文件名不正确: %q", index.InitFile)
	}

	for i, seg := range index.Segments {
		if seg.Index != i+1 {
			t.Fatalf("分片序号应从 1 开始递增，第 %d 个是 %d", i, seg.Index)
		}
		if seg.File != []string{"c00001.m4s", "c00002.m4s", "c00003.m4s"}[i] {
			t.Fatalf("第 %d 个分片文件名不正确: %q", i, seg.File)
		}
		if seg.StartPTS != float64(i) {
			t.Fatalf("第 %d 个分片 StartPTS 应为 %d，实际 %v", i, i, seg.StartPTS)
		}
		if seg.Duration != 1 {
			t.Fatalf("第 %d 个分片时长应为 1s，实际 %v", i, seg.Duration)
		}
	}

	// 关键帧标记直接决定 seek 能不能落在安全位置。
	wantKeyframes := []bool{true, false, true}
	for i, want := range wantKeyframes {
		if index.Segments[i].Keyframe != want {
			t.Fatalf("第 %d 个分片 Keyframe 应为 %v，实际 %v", i, want, index.Segments[i].Keyframe)
		}
	}

	if index.TotalDuration != 3 {
		t.Fatalf("总时长应为 3s，实际 %v", index.TotalDuration)
	}
	if index.BitrateBps <= 0 {
		t.Fatalf("码率必须为正，实际 %d", index.BitrateBps)
	}

	assertRoundTrip(t, original, outDir, index)

	if got := index.SegmentAt(0.5); got != 1 {
		t.Fatalf("SegmentAt(0.5) 应为 1，实际 %d", got)
	}
	if got := index.SegmentAt(2.5); got != 3 {
		t.Fatalf("SegmentAt(2.5) 应为 3，实际 %d", got)
	}
	if got := index.SegmentAt(999); got != 3 {
		t.Fatalf("SegmentAt(999) 应夹到最后一个分片，实际 %d", got)
	}
}

func TestSplitSyntheticFixtureIndexFileOnDisk(t *testing.T) {
	original := buildSyntheticFragmentedMP4(t, []bool{true})
	outDir := t.TempDir()

	index, err := SplitFile(writeTemp(t, original), SplitOptions{OutDir: outDir})
	if err != nil {
		t.Fatalf("SplitFile 失败: %v", err)
	}

	raw := readFileBytes(t, filepath.Join(outDir, "index.json"))
	if !bytes.Contains(raw, []byte(`"mimeType"`)) || !bytes.Contains(raw, []byte("c00001.m4s")) {
		t.Fatalf("index.json 内容不完整: %s", raw)
	}
	if index.Segments[0].Size != int64(len(readFileBytes(t, filepath.Join(outDir, index.Segments[0].File)))) {
		t.Fatal("索引里的 size 与磁盘上的分片大小不一致")
	}
}

// TestSplitPackedMatchesUnpacked 锁定打包格式的核心不变量：
// 打包只是"把连续的分片放进同一个文件"，逐片字节与逐片 sha256 必须与不打包时**完全一致**，
// 且 init + 按 offset/size 切出来的全部片段拼回去仍与原文件逐字节相同。
//
// 打包的动机是产物文件数（Windows 上每多一个文件就多一次固定写盘开销），
// 因此这里同时断言：3 个分片按每包 2 片打包后，目录里只有 2 个包文件。
func TestSplitPackedMatchesUnpacked(t *testing.T) {
	original := buildSyntheticFragmentedMP4(t, []bool{true, false, true})

	plainDir := t.TempDir()
	plain, err := SplitFile(writeTemp(t, original), SplitOptions{OutDir: plainDir})
	if err != nil {
		t.Fatalf("未打包切分失败: %v", err)
	}

	packedDir := t.TempDir()
	packed, err := SplitFile(writeTemp(t, original), SplitOptions{OutDir: packedDir, PackSize: 2})
	if err != nil {
		t.Fatalf("打包切分失败: %v", err)
	}

	if err := packed.Validate(); err != nil {
		t.Fatalf("打包后的索引不自洽: %v", err)
	}
	if len(packed.Segments) != len(plain.Segments) {
		t.Fatalf("打包不该改变分片数：%d → %d", len(plain.Segments), len(packed.Segments))
	}

	// 3 片 / 每包 2 片 → 2 个包；包清单必须按序覆盖全部分片。
	wantPacks := []model.Pack{
		{File: "pack-0001.bin", FirstSegment: 1, Count: 2},
		{File: "pack-0002.bin", FirstSegment: 3, Count: 1},
	}
	if len(packed.Packs) != len(wantPacks) {
		t.Fatalf("应产出 %d 个包，实际 %d（%+v）", len(wantPacks), len(packed.Packs), packed.Packs)
	}
	for i, want := range wantPacks {
		got := packed.Packs[i]
		if got.File != want.File || got.FirstSegment != want.FirstSegment || got.Count != want.Count {
			t.Fatalf("第 %d 个包不正确：%+v，应为 %+v", i+1, got, want)
		}
		if got.Bytes <= 0 {
			t.Fatalf("第 %d 个包的 bytes 必须为正，实际 %d", i+1, got.Bytes)
		}
	}

	// 目录里只有 index.json + init.mp4 + 2 个包：不再有逐片文件残留。
	entries, err := os.ReadDir(packedDir)
	if err != nil {
		t.Fatalf("读取打包目录失败: %v", err)
	}
	if len(entries) != 2+len(wantPacks) {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("打包目录应只有 index.json + init.mp4 + %d 个包，实际 %v", len(wantPacks), names)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".m4s") {
			t.Fatalf("打包后不该残留逐片文件：%s", e.Name())
		}
	}

	var rebuilt bytes.Buffer
	rebuilt.Write(readFileBytes(t, filepath.Join(packedDir, packed.InitFile)))

	for i, seg := range packed.Segments {
		want := readFileBytes(t, filepath.Join(plainDir, plain.Segments[i].File))

		packData := readFileBytes(t, filepath.Join(packedDir, seg.File))
		got := packData[seg.Offset : seg.Offset+seg.Size]

		if !bytes.Equal(got, want) {
			t.Fatalf("第 %d 片在包内的字节与未打包时不一致（%d vs %d 字节）",
				seg.Index, len(got), len(want))
		}
		// sha256 始终是对"分片"求的，与怎么打包无关。
		sum := sha256.Sum256(got)
		if hex.EncodeToString(sum[:]) != seg.SHA256 {
			t.Fatalf("第 %d 片的 sha256 与索引不一致", seg.Index)
		}
		if plain.Segments[i].SHA256 != seg.SHA256 {
			t.Fatalf("第 %d 片打包前后的 sha256 不一致", seg.Index)
		}
		if plain.Segments[i].Size != seg.Size {
			t.Fatalf("第 %d 片打包前后的 size 不一致", seg.Index)
		}
		rebuilt.Write(got)
	}

	if !bytes.Equal(rebuilt.Bytes(), original) {
		t.Fatalf("打包产物拼不回原文件：原 %d 字节，拼回 %d 字节", len(original), rebuilt.Len())
	}

	// 每个包都以 moof 开头（包里第一片就是从包首开始的完整分片）。
	for _, pack := range packed.Packs {
		data := readFileBytes(t, filepath.Join(packedDir, pack.File))
		if int64(len(data)) != pack.Bytes {
			t.Fatalf("%s 实际 %d 字节与索引 %d 不一致", pack.File, len(data), pack.Bytes)
		}
		if string(data[4:8]) != "moof" {
			t.Fatalf("%s 必须以 moof 开头，实际 %q", pack.File, data[4:8])
		}
	}
}

func TestSplitRejectsNonFragmentedMP4(t *testing.T) {
	ftyp := boxBytes("ftyp", concat([]byte("isom"), u32(0x200), []byte("isom")))
	moov := buildSyntheticMovie(1, 1000, 1000, "avc1")
	plain := concat(ftyp, moov, boxBytes("mdat", make([]byte, 64)))

	_, err := SplitFile(writeTemp(t, plain), SplitOptions{OutDir: t.TempDir()})
	if err == nil {
		t.Fatal("普通 MP4 应当被拒绝，并给出重新封装的指引")
	}
	if !strings.Contains(err.Error(), "fragmented") || !strings.Contains(err.Error(), "ffmpeg") {
		t.Fatalf("错误信息应说明原因与解决办法，实际: %v", err)
	}
}

func TestSplitRejectsUnsupportedSampleEntry(t *testing.T) {
	ftyp := boxBytes("ftyp", concat([]byte("isom"), u32(0x200), []byte("isom")))
	moov := buildSyntheticMovie(1, 1000, 1000, "vp09")
	file := concat(ftyp, moov, buildMoof(1, 0, 1000, true), boxBytes("mdat", make([]byte, 16)))

	_, err := SplitFile(writeTemp(t, file), SplitOptions{OutDir: t.TempDir()})
	if err == nil {
		t.Fatal("不支持的采样格式应当明确报错，而不是产出一个 play 不起来的 mimeType")
	}
	if !strings.Contains(err.Error(), "vp09") {
		t.Fatalf("错误信息应点名不支持的格式，实际: %v", err)
	}
}

// TestSplitRealFFmpegFile 用 ffmpeg 生成真实的 H.264+AAC 分片文件跑一遍。
// 这是唯一能覆盖"一个 moof 里有 video+audio 两个 traf"的测试。
func TestSplitRealFFmpegFile(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("未安装 ffmpeg，跳过真实文件测试")
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "src.mp4")
	cmd := exec.Command(ffmpeg,
		"-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=15:duration=4",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=4",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "15", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "64k",
		"-shortest",
		"-movflags", "+frag_keyframe+empty_moov+default_base_moof",
		"-frag_duration", "1000000",
		src,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg 生成测试视频失败: %v\n%s", err, output)
	}

	original := readFileBytes(t, src)
	outDir := filepath.Join(dir, "room-media")

	index, err := SplitFile(src, SplitOptions{OutDir: outDir})
	if err != nil {
		t.Fatalf("SplitFile 失败: %v", err)
	}

	if len(index.Segments) < 3 {
		t.Fatalf("4 秒 / 1 秒分片应至少切出 3 段，实际 %d", len(index.Segments))
	}
	if !strings.Contains(index.MimeType, "avc1.") || !strings.Contains(index.MimeType, "mp4a.40.") {
		t.Fatalf("mimeType 应同时包含视频与音频编码串，实际 %q", index.MimeType)
	}
	if index.TotalDuration < 3 || index.TotalDuration > 6 {
		t.Fatalf("总时长应在 3–6 秒之间，实际 %v", index.TotalDuration)
	}

	keyframes := 0
	for _, seg := range index.Segments {
		if seg.Keyframe {
			keyframes++
		}
	}
	if keyframes == 0 {
		t.Fatal("至少应有一个分片以关键帧开头，否则无法 seek")
	}
	if !index.Segments[0].Keyframe {
		t.Fatal("第一个分片必须以关键帧开头，否则无法起播")
	}

	assertRoundTrip(t, original, outDir, index)

	t.Logf("真实文件切片结果: %d 段 / 平均 %.2fs / 码率 %.2f Mbps / mime=%s",
		len(index.Segments), index.SegmentSec, float64(index.BitrateBps)/1_000_000, index.MimeType)
}

func writeTemp(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "input.mp4")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("写入临时输入失败: %v", err)
	}
	return path
}
