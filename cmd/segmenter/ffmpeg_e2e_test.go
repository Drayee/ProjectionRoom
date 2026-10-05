package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ProjectionRoom/internal/model"
	"ProjectionRoom/internal/service/segment"
)

// TestAutoDownloadThenSliceEndToEnd 是本包最重要的一条端到端用例，覆盖 SPEC 里
// "用户只下载一个 exe、本机没有 ffmpeg"的主路径：
//
//	本机没有 ffmpeg（PATH 与 exe 同级目录都空）
//	→ 打印 5 秒倒计时（-yes 跳过）
//	→ 从本地 HTTP 下载一个小 zip（不真下 100 MB）
//	→ 解压 → 在解压目录里递归找到 ffmpeg/ffprobe
//	→ ffprobe 探测 → 判定直通 → ffmpeg -c copy 重新封装 → 按 moof 边界切分
//	→ 产出 init.mp4 + 分片 + index.json（并打印容量提示与下一步）
//
// 假工具会把它收到的参数写进 PR_STUB_LOG，这既是"工具真的被执行了"的证据，
// 也是"我们把找到的路径作为 ffmpeg 传下去"的证据。
func TestAutoDownloadThenSliceEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("没有 go 工具链：需要在测试内编译假的 ffmpeg/ffprobe，跳过")
	}

	outDir := filepath.Join(t.TempDir(), "cut")
	env := newTestEnv(t, &options{
		out:          outDir,
		fragSec:      2,
		pack:         1,
		uplinkMbps:   12,
		searchByName: false,
		yes:          true,
	})
	suffix := env.app.exeSuffix()

	// 1) 编译假工具，并打成一个"官方包形状"的 zip。
	stubDir := filepath.Join(env.app.exeDir, "stubs")
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatal(err)
	}
	entries := map[string][]byte{}
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		out := filepath.Join(stubDir, name+suffix)
		buildStub(t, out)
		data, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		entries["ffmpeg-8.0.1-essentials_build/bin/"+name+suffix] = data
	}
	zipBytes := zipOf(t, entries)

	// 2) 本地 HTTP 服务顶上"下载地址"（也可以在命令行用 -ffmpeg-url 指过来）。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(zipBytes)
	}))
	defer srv.Close()

	// 3) 一个普通 MP4 输入（假 ffmpeg 不读它的内容，只负责产出真正的分片 MP4）。
	input := filepath.Join(t.TempDir(), "plain_movie.mp4")
	if err := os.WriteFile(input, []byte("this is not really a video, the stub ffmpeg ignores it"), 0o644); err != nil {
		t.Fatal(err)
	}
	env.app.opts.in = input
	env.app.opts.ffmpegURL = srv.URL + "/ffmpeg.zip"

	logPath := filepath.Join(t.TempDir(), "stub.log")
	t.Setenv("PR_STUB_LOG", logPath)

	// 4) 跑完整流程。
	if err := env.app.run(context.Background()); err != nil {
		t.Fatalf("一键流程失败: %v\n--- stdout ---\n%s\n--- stderr ---\n%s", err, env.out(), env.errOut())
	}
	stdout := env.out()

	// 5) 倒计时被 -yes 跳过。
	if !strings.Contains(stdout, "跳过 5 秒倒计时") {
		t.Fatalf("-yes 应当跳过倒计时，实际输出:\n%s", stdout)
	}
	// 6) 下载 → 解压 → 在解压目录里找到工具（来源必须是"自动下载目录"）。
	pkgDir := env.app.ffmpegPkgDir()
	tools, sources := env.app.findTools()
	if !tools.Available() {
		t.Fatalf("解压后应当找到工具: %+v", tools)
	}
	for i, tool := range []string{tools.FFmpeg, tools.FFprobe} {
		if !strings.HasPrefix(tool, pkgDir) {
			t.Fatalf("工具应来自解压目录 %s，实际 %q", pkgDir, tool)
		}
		if !strings.Contains(sources[i], "自动下载目录") {
			t.Fatalf("来源应为自动下载目录，实际 %q", sources[i])
		}
	}
	if !strings.Contains(stdout, "已下载并解压到: "+pkgDir) {
		t.Fatalf("应当打印解压目录，实际:\n%s", stdout)
	}
	// 7) 探测与判定（假 ffprobe 报的是 h264+aac，应当走直通）。
	if !strings.Contains(stdout, "视频=h264") || !strings.Contains(stdout, "音频=aac") {
		t.Fatalf("应当打印探测结果，实际:\n%s", stdout)
	}
	if !strings.Contains(stdout, "直通") {
		t.Fatalf("h264+aac 应当判定为直通，实际:\n%s", stdout)
	}
	// 8) 假 ffmpeg 确实被当成 ffmpeg 执行了，并且收到的是"重新封装"那套参数。
	logText := readTextFile(t, logPath)
	if !strings.Contains(logText, tools.FFmpeg) {
		t.Fatalf("执行的应当是下载到的 ffmpeg（%s），实际日志:\n%s", tools.FFmpeg, logText)
	}
	for _, want := range []string{"-c copy", "-movflags", "+frag_keyframe+empty_moov+default_base_moof"} {
		if !strings.Contains(logText, want) {
			t.Fatalf("传给 ffmpeg 的参数里应有 %q，实际日志:\n%s", want, logText)
		}
	}

	// 9) 产物必须自洽：init.mp4 + 分片 + index.json，且分片以 moof 开头。
	indexData, err := os.ReadFile(filepath.Join(outDir, segment.IndexFileName))
	if err != nil {
		t.Fatalf("产物缺少 index.json: %v", err)
	}
	var index model.Index
	if err := json.Unmarshal(indexData, &index); err != nil {
		t.Fatalf("index.json 不是合法 JSON: %v", err)
	}
	if err := index.Validate(); err != nil {
		t.Fatalf("index.json 不自洽: %v", err)
	}
	if len(index.Segments) < 2 {
		t.Fatalf("6 秒 / 2 秒分片应至少切出 2 段，实际 %d", len(index.Segments))
	}
	if !strings.Contains(index.MimeType, "avc1.") || !strings.Contains(index.MimeType, "mp4a.40.") {
		t.Fatalf("mimeType 应同时含视频与音频编码串，实际 %q", index.MimeType)
	}
	for _, seg := range index.Segments {
		data, err := os.ReadFile(filepath.Join(outDir, seg.File))
		if err != nil {
			t.Fatalf("读取分片 %s 失败: %v", seg.File, err)
		}
		if len(data) < 8 || string(data[4:8]) != "moof" {
			t.Fatalf("分片 %s 必须以 moof 开头", seg.File)
		}
	}
	// 10) 最后的摘要与下一步也要在（用户看到的最后一段）。
	if !strings.Contains(stdout, "K0 = ") || !strings.Contains(stdout, "不是能带几个人") {
		t.Fatalf("应当打印容量提示与它的含义，实际:\n%s", stdout)
	}
	if !strings.Contains(stdout, "在主播页点「选择分片目录」") {
		t.Fatalf("应当打印下一步，实际:\n%s", stdout)
	}
}

// buildStub 在测试内编译 testdata/stubtools 成假 ffmpeg/ffprobe。
func buildStub(t *testing.T, out string) {
	t.Helper()

	cmd := exec.Command("go", "build", "-o", out, "./testdata/stubtools")
	cmd.Dir = "." // go test 的工作目录就是本包目录
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("编译假工具失败: %v\n%s", err, output)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("编译假工具没有产出文件: %v", err)
	}
}

func readTextFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	return string(data)
}
