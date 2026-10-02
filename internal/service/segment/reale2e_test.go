package segment

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ProjectionRoom/internal/model"
)

// requireFFmpeg 定位真实 ffmpeg；找不到就跳过（并说明跳过原因）。
func requireFFmpeg(t *testing.T) Tools {
	t.Helper()

	tools := DiscoverTools("")
	if !tools.Available() {
		t.Skip("未找到 ffmpeg/ffprobe，跳过真实端到端测试（服务端一次性切片依赖它们）")
	}
	return tools
}

// makeTestVideo 用 testsrc 生成约 6 秒的真实视频。
// 刻意产出**普通 MP4**（非 fragmented）：服务端的自动流水线必须真的走一次
// "无损重新封装 → 按 moof 切分"，否则这个用例就覆盖不到主路径。
func makeTestVideo(t *testing.T, tools Tools, dir string) string {
	t.Helper()

	src := filepath.Join(dir, "plain.mp4")
	cmd := exec.Command(tools.FFmpeg,
		"-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=160x120:rate=10:duration=6",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=6",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "10", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "64k",
		"-shortest",
		src,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg 生成测试视频失败: %v\n%s", err, output)
	}
	return src
}

// TestProcessWithRealFFmpeg 覆盖抽取后的流水线本体：
// probe → 无损重新封装 → 按 moof 边界切分 → 写 index.json，产物必须自洽。
func TestProcessWithRealFFmpeg(t *testing.T) {
	tools := requireFFmpeg(t)
	dir := t.TempDir()
	src := makeTestVideo(t, tools, dir)

	info, err := Probe(context.Background(), tools, src)
	if err != nil {
		t.Fatalf("ffprobe 探测失败: %v", err)
	}
	if info.DurationSec < 4 || info.DurationSec > 8 {
		t.Fatalf("测试视频时长应在 4–8 秒之间，实际 %.2f", info.DurationSec)
	}
	if !info.BrowserPlayable() {
		t.Fatalf("testsrc 视频应当是可播放的 h264/aac，实际 %s/%s", info.VideoCodec, info.AudioCodec)
	}

	var progress []float64
	outDir := filepath.Join(dir, "room-media")
	artifacts, err := Process(context.Background(), src, outDir, ProcessOptions{
		Tools:          tools,
		SegmentSeconds: 2,
		Auto:           true,
		Info:           info,
		Progress:       func(p float64) { progress = append(progress, p) },
	})
	if err != nil {
		t.Fatalf("流水线失败: %v", err)
	}

	index := artifacts.Index
	if err := index.Validate(); err != nil {
		t.Fatalf("生成的索引不自洽: %v", err)
	}
	if len(index.Segments) < 2 {
		t.Fatalf("6 秒 / 2 秒分片应至少切出 2 段，实际 %d", len(index.Segments))
	}
	if !strings.Contains(index.MimeType, "avc1.") || !strings.Contains(index.MimeType, "mp4a.40.") {
		t.Fatalf("mimeType 应同时含视频与音频编码串，实际 %q", index.MimeType)
	}
	if len(artifacts.Files) != len(index.Segments)+2 {
		t.Fatalf("产物应为 index.json + init.mp4 + %d 个分片，实际 %d 个文件",
			len(index.Segments), len(artifacts.Files))
	}

	// 输出格式必须与 cmd/segmenter 一致：init.mp4 以 ftyp 开头且含 moov，
	// 每个分片以 moof 开头。
	initData := readAll(t, filepath.Join(outDir, InitFileName))
	if string(initData[4:8]) != "ftyp" || !bytes.Contains(initData, []byte("moov")) {
		t.Fatal("init.mp4 必须是 ftyp + moov（MSE 的首个 appendBuffer）")
	}
	for _, seg := range index.Segments {
		data := readAll(t, filepath.Join(outDir, seg.File))
		if string(data[4:8]) != "moof" {
			t.Fatalf("分片 %s 必须以 moof 开头，实际 %q", seg.File, data[4:8])
		}
		if int64(len(data)) != seg.Size {
			t.Fatalf("分片 %s 实际 %d 字节与索引 %d 不一致", seg.File, len(data), seg.Size)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != seg.SHA256 {
			t.Fatalf("分片 %s 的 sha256 与索引不一致", seg.File)
		}
	}

	// index.json 必须是能被 model.Index 读回来的合法索引（前端与服务器都按它解析）。
	var decoded model.Index
	if err := json.Unmarshal(readAll(t, filepath.Join(outDir, IndexFileName)), &decoded); err != nil {
		t.Fatalf("index.json 不是合法 JSON: %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("index.json 不自洽: %v", err)
	}

	if len(progress) == 0 {
		t.Fatal("流水线必须报告进度")
	}
	if progress[len(progress)-1] != 1 {
		t.Fatalf("最后一条进度应为 1，实际 %v", progress[len(progress)-1])
	}
	for i := 1; i < len(progress); i++ {
		if progress[i] < progress[i-1] {
			t.Fatalf("进度必须单调不减，第 %d 条出现回退: %v", i, progress)
		}
	}

	t.Logf("真实流水线: %d 段 / 平均 %.2fs / 码率 %.2f Mbps / 产物 %.2f KiB",
		len(index.Segments), index.SegmentSec,
		float64(index.BitrateBps)/1_000_000, float64(artifacts.TotalBytes)/1024)
}

// TestQueueEndToEndWithRealFFmpeg 是服务端的真实端到端：
// 提交上传 → 队列跑真实 ffmpeg → 单次返回 zip 校验 → 改用小上限验证 manifest 分批与 sha256。
func TestQueueEndToEndWithRealFFmpeg(t *testing.T) {
	tools := requireFFmpeg(t)

	workDir := t.TempDir()
	src := makeTestVideo(t, tools, workDir)
	payload, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("读取测试视频失败: %v", err)
	}

	// ---- 第一段：走单次返回（zip） ----
	cfg := testConfig(t)
	cfg.Segment.SingleResponseMaxBytes = 1 << 30
	q := newTestQueue(t, cfg, nil, Probe, processorFunc(Process), &tools)

	view, err := q.Submit("movie.mp4", bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatalf("提交作业失败: %v", err)
	}
	if view.State != StateQueued && view.State != StateRunning {
		t.Fatalf("刚提交的作业应处于 queued/running，实际 %q", view.State)
	}

	done := waitForJob(t, q, view.JobID, 120*time.Second)
	if done.State != StateDone {
		t.Fatalf("作业应完成，实际 %q（%s）", done.State, done.Error)
	}
	if done.Result == nil || !done.Result.SingleResponse {
		t.Fatalf("小产物应走单次返回，实际 %+v", done.Result)
	}

	artifactsDir, files, err := q.Artifacts(view.JobID)
	if err != nil {
		t.Fatalf("读取产物失败: %v", err)
	}

	var buf bytes.Buffer
	if _, err := WriteZip(&buf, artifactsDir, files); err != nil {
		t.Fatalf("打包 zip 失败: %v", err)
	}
	names := zipNames(t, buf.Bytes())
	for _, want := range []string{IndexFileName, InitFileName, "c00001.m4s"} {
		if _, ok := names[want]; !ok {
			t.Fatalf("zip 里缺少 %s，实际条目 %v", want, keys(names))
		}
	}
	if len(names) != len(files) {
		t.Fatalf("zip 条目数 %d 与产物清单 %d 不一致", len(names), len(files))
	}
	// zip 里的 index.json 必须是可用的分片索引。
	var decoded model.Index
	if err := json.Unmarshal(zipEntry(t, buf.Bytes(), IndexFileName), &decoded); err != nil {
		t.Fatalf("zip 里的 index.json 无法解析: %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("zip 里的 index.json 不自洽: %v", err)
	}
	if len(decoded.Segments) < 2 {
		t.Fatalf("真实视频应切出多个分片，实际 %d", len(decoded.Segments))
	}

	// 源文件必须在作业结束后被删除。
	if leftovers := globNames(t, filepath.Join(cfg.Segment.TempDir, "job-"+view.JobID), "source.*"); len(leftovers) != 0 {
		t.Fatalf("源文件应在作业完成后删除，实际残留 %v", leftovers)
	}

	// ---- 第二段：把上限压到"两个最大文件"以内，逼出 manifest 分批 ----
	var maxFile int64
	for _, f := range files {
		if f.Size > maxFile {
			maxFile = f.Size
		}
	}
	limit := maxFile*2 + zipPerFileOverhead

	cfg2 := testConfig(t)
	cfg2.Segment.SingleResponseMaxBytes = limit
	q2 := newTestQueue(t, cfg2, nil, Probe, processorFunc(Process), &tools)

	view2, err := q2.Submit("movie.mp4", bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatalf("提交作业失败: %v", err)
	}
	done2 := waitForJob(t, q2, view2.JobID, 120*time.Second)
	if done2.State != StateDone {
		t.Fatalf("作业应完成，实际 %q（%s）", done2.State, done2.Error)
	}
	if done2.Result == nil || done2.Result.SingleResponse {
		t.Fatalf("产物超过上限时应走 manifest，实际 %+v", done2.Result)
	}
	if len(done2.Result.Parts) < 2 {
		t.Fatalf("应至少切成 2 份，实际 %d", len(done2.Result.Parts))
	}

	var entries []string
	for i, part := range done2.Result.Parts {
		if part.Bytes >= limit {
			t.Fatalf("第 %d 份 %d 字节不应达到单份上限 %d", part.N, part.Bytes, limit)
		}

		dir, partFiles, got, err := q2.PartFiles(view2.JobID, part.N)
		if err != nil {
			t.Fatalf("读取第 %d 份失败: %v", part.N, err)
		}
		if got.SHA256 != part.SHA256 {
			t.Fatalf("第 %d 份的 sha256 与实际记录不一致", part.N)
		}

		// 客户端下载到的字节流必须与预先算好的 sha256 一致 ——
		// 这正是"zip 打包必须确定性"的原因。
		var partBuf bytes.Buffer
		size, err := WriteZip(&partBuf, dir, partFiles)
		if err != nil {
			t.Fatalf("打包第 %d 份失败: %v", part.N, err)
		}
		if size != part.Bytes {
			t.Fatalf("第 %d 份实际 %d 字节与 manifest 的 %d 不一致", part.N, size, part.Bytes)
		}
		sum := sha256.Sum256(partBuf.Bytes())
		if hex.EncodeToString(sum[:]) != part.SHA256 {
			t.Fatalf("第 %d 份的 sha256 校验失败：manifest 里的摘要与下载内容不符", part.N)
		}
		for name := range zipNames(t, partBuf.Bytes()) {
			entries = append(entries, name)
		}
		if part.N != i+1 {
			t.Fatalf("分批编号应连续，第 %d 份是 %d", i+1, part.N)
		}
	}

	// 所有份合起来必须覆盖全部产物，且不重复。
	if len(entries) != len(files) {
		t.Fatalf("各份条目合计 %d，应为产物的 %d 个文件", len(entries), len(files))
	}
	seen := map[string]bool{}
	for _, name := range entries {
		if seen[name] {
			t.Fatalf("文件 %s 出现在多个分批里", name)
		}
		seen[name] = true
	}

	t.Logf("真实端到端: 单次返回 %d 字节；分批上限 %d 字节 → %d 份",
		done.Result.Bytes, limit, len(done2.Result.Parts))
}

// ---------- 小工具 ----------

func waitForJob(t *testing.T, q *Queue, id string, timeout time.Duration) *View {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		view, ok := q.Get(id)
		if ok && (view.State == StateDone || view.State == StateFailed) {
			return view
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待作业 %s 结束超时", id)
	return nil
}

func readAll(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	return data
}

func zipNames(t *testing.T, data []byte) map[string]struct{} {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("生成的 zip 无法打开: %v", err)
	}
	names := make(map[string]struct{}, len(reader.File))
	for _, entry := range reader.File {
		names[entry.Name] = struct{}{}
	}
	return names
}

func zipEntry(t *testing.T, data []byte, name string) []byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("生成的 zip 无法打开: %v", err)
	}
	for _, entry := range reader.File {
		if entry.Name != name {
			continue
		}
		rc, err := entry.Open()
		if err != nil {
			t.Fatalf("打开 zip 条目 %s 失败: %v", name, err)
		}
		defer rc.Close()
		content, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("读取 zip 条目 %s 失败: %v", name, err)
		}
		return content
	}
	t.Fatalf("zip 里没有 %s", name)
	return nil
}

func keys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
