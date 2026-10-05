package main

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCountdownSkippedByYes 覆盖"-yes 跳过 5 秒倒计时"：一次都不等，但仍要打印"跳过了"。
func TestCountdownSkippedByYes(t *testing.T) {
	var buf bytes.Buffer
	waits := 0
	runCountdown(&buf, true, countdownSeconds, func(time.Duration) { waits++ })

	if waits != 0 {
		t.Fatalf("-yes 一次都不该等待，实际等了 %d 次", waits)
	}
	if !strings.Contains(buf.String(), "跳过 5 秒倒计时") {
		t.Fatalf("应当说明跳过了倒计时，实际 %q", buf.String())
	}
}

// TestCountdownWaitsFiveSeconds 覆盖默认行为：5、4、3、2、1，每秒一次。
func TestCountdownWaitsFiveSeconds(t *testing.T) {
	var buf bytes.Buffer
	var waits []time.Duration
	runCountdown(&buf, false, countdownSeconds, func(d time.Duration) { waits = append(waits, d) })

	if len(waits) != countdownSeconds {
		t.Fatalf("应当等 %d 次，实际 %d 次", countdownSeconds, len(waits))
	}
	for _, d := range waits {
		if d != time.Second {
			t.Fatalf("每次等待应为 1 秒，实际 %v", d)
		}
	}
	for _, want := range []string{"  5 ...", "  4 ...", "  1 ..."} {
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("倒计时输出里应有 %q，实际 %q", want, buf.String())
		}
	}
}

// TestDownloadExtractZipAndFindTools 是"没装 ffmpeg → 自动下载 → 解压 → 找到工具"的核心用例：
// 用 httptest 提供一个**小 zip**（内含假的 ffmpeg/ffprobe），不真下 100 MB。
func TestDownloadExtractZipAndFindTools(t *testing.T) {
	env := newTestEnv(t, nil)
	suffix := env.app.exeSuffix()

	// 压缩包里的层级刻意做成官方包的样子（ffmpeg-<版本>-essentials_build/bin/...），
	// 这样"递归找工具"这一步才是真的被测到。
	stub := []byte("fake ffmpeg binary")
	zipBytes := zipOf(t, map[string][]byte{
		"ffmpeg-8.0.1-essentials_build/bin/ffmpeg" + suffix:  stub,
		"ffmpeg-8.0.1-essentials_build/bin/ffprobe" + suffix: stub,
		"ffmpeg-8.0.1-essentials_build/README.txt":           []byte("readme"),
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", itoa(len(zipBytes)))
		_, _ = w.Write(zipBytes)
	}))
	defer srv.Close()

	// 下载之前：什么都没找到。
	if tools, _ := env.app.findTools(); tools.Available() {
		t.Fatalf("下载前不该找到工具: %+v", tools)
	}

	dir := env.app.ffmpegPkgDir()
	if err := env.app.downloadAndExtract(context.Background(), srv.URL+"/ffmpeg.zip", dir); err != nil {
		t.Fatalf("下载并解压失败: %v", err)
	}

	tools, sources := env.app.findTools()
	if !tools.Available() {
		t.Fatalf("解压后应当能找到 ffmpeg/ffprobe: %+v", tools)
	}
	for i, tool := range []string{tools.FFmpeg, tools.FFprobe} {
		if !strings.HasPrefix(tool, dir) {
			t.Fatalf("工具应落在下载目录 %s 里，实际 %q", dir, tool)
		}
		if !strings.Contains(sources[i], "自动下载目录") {
			t.Fatalf("来源说明应指出是自动下载目录，实际 %q", sources[i])
		}
		if info, err := os.Stat(tool); err != nil || info.Size() != int64(len(stub)) {
			t.Fatalf("解压出来的文件不对: %v / %d 字节", err, info.Size())
		}
	}
	// 压缩包里其它文件也要解出来（不是只挑工具）。
	if _, err := os.Stat(filepath.Join(dir, "ffmpeg-8.0.1-essentials_build", "README.txt")); err != nil {
		t.Fatalf("压缩包里的其它文件也应当解出来: %v", err)
	}
	if !strings.Contains(env.out(), "已下载: ") {
		t.Fatalf("应当打印下载结果，实际 %q", env.out())
	}
}

// TestDownloadRejectsZipSlip 覆盖目录穿越：压缩包里的 ../ 必须被拒绝，且不能真的写出去。
func TestDownloadRejectsZipSlip(t *testing.T) {
	env := newTestEnv(t, nil)
	zipBytes := zipOf(t, map[string][]byte{"../escaped.exe": []byte("evil")})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(zipBytes)
	}))
	defer srv.Close()

	dir := env.app.ffmpegPkgDir()
	err := env.app.downloadAndExtract(context.Background(), srv.URL+"/ffmpeg.zip", dir)
	if err == nil {
		t.Fatal("含 ../ 的压缩包必须被拒绝")
	}
	if !strings.Contains(err.Error(), "目标目录之外") {
		t.Fatalf("错误信息应说明拒绝的原因，实际 %q", err)
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(dir), "escaped.exe")); statErr == nil {
		t.Fatal("危险的条目绝不能被写到目标目录之外")
	}
}

// TestDownloadErrorsAreExplicit 覆盖下载失败的各种形状：都要给出明确错误，不能静默。
func TestDownloadErrorsAreExplicit(t *testing.T) {
	env := newTestEnv(t, nil)

	t.Run("HTTP 500", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		defer srv.Close()

		err := env.app.downloadAndExtract(context.Background(), srv.URL+"/ffmpeg.zip", env.app.ffmpegPkgDir())
		if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
			t.Fatalf("应当报出 HTTP 状态码，实际 %v", err)
		}
	})

	t.Run("连接不上", func(t *testing.T) {
		err := env.app.downloadAndExtract(context.Background(), "http://127.0.0.1:1/ffmpeg.zip", env.app.ffmpegPkgDir())
		if err == nil || !strings.Contains(err.Error(), "下载") {
			t.Fatalf("应当报出下载失败，实际 %v", err)
		}
	})

	t.Run("不认识的压缩格式", func(t *testing.T) {
		err := env.app.downloadAndExtract(context.Background(), "https://example.com/ffmpeg.7z", env.app.ffmpegPkgDir())
		if err == nil || !strings.Contains(err.Error(), "不认识的压缩格式") {
			t.Fatalf("应当明确说不支持这种格式，实际 %v", err)
		}
	})

	t.Run("0 字节", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		defer srv.Close()

		err := env.app.downloadAndExtract(context.Background(), srv.URL+"/ffmpeg.zip", env.app.ffmpegPkgDir())
		if err == nil || !strings.Contains(err.Error(), "0 字节") {
			t.Fatalf("0 字节应当报错，实际 %v", err)
		}
	})
}

// TestUntarXZOnWindowsIsExplicit：Windows 上没有 .tar.xz 分支，必须明确说清楚。
func TestUntarXZOnWindowsIsExplicit(t *testing.T) {
	err := untarXZInto("ffmpeg.tar.xz", t.TempDir(), "windows")
	if err == nil || !strings.Contains(err.Error(), ".tar.xz") {
		t.Fatalf("Windows 上应当明确拒绝 .tar.xz，实际 %v", err)
	}
}

// TestFindToolsExeSiblingDirs 覆盖"exe 同级目录"这一步（./ffmpeg/bin/ 与 ./bin/），
// 这是"用户手动把 ffmpeg 解压到 exe 旁边"的用法。
func TestFindToolsExeSiblingDirs(t *testing.T) {
	env := newTestEnv(t, nil)
	suffix := env.app.exeSuffix()

	ffmpegDir := filepath.Join(env.app.exeDir, "ffmpeg", "bin")
	binDir := filepath.Join(env.app.exeDir, "bin")
	for _, dir := range []string{ffmpegDir, binDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ffmpeg := filepath.Join(ffmpegDir, "ffmpeg"+suffix)
	ffprobe := filepath.Join(binDir, "ffprobe"+suffix)
	for _, f := range []string{ffmpeg, ffprobe} {
		if err := os.WriteFile(f, []byte("stub"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	tools, sources := env.app.findTools()
	if tools.FFmpeg != ffmpeg || tools.FFprobe != ffprobe {
		t.Fatalf("应当在 exe 同级目录里找到工具：%+v", tools)
	}
	for _, s := range sources {
		if !strings.Contains(s, "exe 同级目录") {
			t.Fatalf("来源说明应指出 exe 同级目录，实际 %q", s)
		}
	}
}

// TestFindToolsExplicitDirWins：-ffmpeg-dir 优先于 exe 同级目录与 PATH。
func TestFindToolsExplicitDirWins(t *testing.T) {
	env := newTestEnv(t, nil)
	suffix := env.app.exeSuffix()

	explicitDir := t.TempDir()
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		if err := os.WriteFile(filepath.Join(explicitDir, name+suffix), []byte("stub"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// 同时也在 exe 同级目录里放一份，验证优先级。
	sibling := filepath.Join(env.app.exeDir, "bin")
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sibling, "ffmpeg"+suffix), []byte("sibling"), 0o755); err != nil {
		t.Fatal(err)
	}

	env.app.opts.ffmpegDir = explicitDir
	tools, sources := env.app.findTools()
	if filepath.Dir(tools.FFmpeg) != explicitDir || filepath.Dir(tools.FFprobe) != explicitDir {
		t.Fatalf("-ffmpeg-dir 应当优先，实际 %+v", tools)
	}
	for _, s := range sources {
		if s != "-ffmpeg-dir" {
			t.Fatalf("来源说明应为 -ffmpeg-dir，实际 %q", s)
		}
	}
}

// TestFindToolsExplicitFileAccepted：-ffmpeg-dir 也可以直接给可执行文件。
func TestFindToolsExplicitFileAccepted(t *testing.T) {
	env := newTestEnv(t, nil)
	suffix := env.app.exeSuffix()

	dir := t.TempDir()
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		if err := os.WriteFile(filepath.Join(dir, name+suffix), []byte("stub"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	env.app.opts.ffmpegDir = filepath.Join(dir, "ffmpeg"+suffix)
	tools, _ := env.app.findTools()
	if !tools.Available() {
		t.Fatalf("给可执行文件时也应当定位到同目录的 ffprobe: %+v", tools)
	}
}

// TestFindToolsRecursiveInPackageDir：下载到的包里层级不固定，必须递归找。
func TestFindToolsRecursiveInPackageDir(t *testing.T) {
	env := newTestEnv(t, nil)
	suffix := env.app.exeSuffix()

	deep := filepath.Join(env.app.ffmpegPkgDir(), "ffmpeg-7.1-static", "nested")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	ffmpeg := filepath.Join(deep, "ffmpeg"+suffix)
	ffprobe := filepath.Join(deep, "ffprobe"+suffix)
	for _, f := range []string{ffmpeg, ffprobe} {
		if err := os.WriteFile(f, []byte("stub"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	tools, _ := env.app.findTools()
	if tools.FFmpeg != ffmpeg || tools.FFprobe != ffprobe {
		t.Fatalf("应当递归找到工具：%+v", tools)
	}
}

// TestFFmpegURLPrecedence 锁定下载地址的优先级：-ffmpeg-url → PR_FFMPEG_URL → 平台默认值。
func TestFFmpegURLPrecedence(t *testing.T) {
	t.Setenv("PR_FFMPEG_URL", "")

	env := newTestEnv(t, nil)
	env.app.goos = "windows"
	if got := env.app.ffmpegURL(); got != ffmpegURLWindows {
		t.Fatalf("windows 默认地址不对: %q", got)
	}
	env.app.goos = "darwin"
	if got := env.app.ffmpegURL(); got != ffmpegURLDarwin {
		t.Fatalf("darwin 默认地址不对: %q", got)
	}
	env.app.goos = "linux"
	if got := env.app.ffmpegURL(); got != ffmpegURLLinux {
		t.Fatalf("linux 默认地址不对: %q", got)
	}

	t.Setenv("PR_FFMPEG_URL", "https://mirror.example.com/ffmpeg.zip")
	if got := env.app.ffmpegURL(); got != "https://mirror.example.com/ffmpeg.zip" {
		t.Fatalf("环境变量应当生效，实际 %q", got)
	}

	env.app.opts.ffmpegURL = "https://local.example.com/ffmpeg.zip"
	if got := env.app.ffmpegURL(); got != "https://local.example.com/ffmpeg.zip" {
		t.Fatalf("-ffmpeg-url 应当优先于环境变量，实际 %q", got)
	}
}

// TestSafeJoin 锁定解压路径的防护。
func TestSafeJoin(t *testing.T) {
	dir := t.TempDir()

	ok := map[string]string{
		"ffmpeg-8.0.1-essentials_build/bin/ffmpeg.exe": filepath.Join(dir, "ffmpeg-8.0.1-essentials_build", "bin", "ffmpeg.exe"),
		"./ffmpeg": filepath.Join(dir, "ffmpeg"),
	}
	for name, want := range ok {
		got, err := safeJoin(dir, name)
		if err != nil {
			t.Fatalf("%q 应当被接受: %v", name, err)
		}
		if got != want {
			t.Fatalf("safeJoin(%q) = %q，应为 %q", name, got, want)
		}
	}

	for _, name := range []string{"../evil", "..", "/etc/passwd", `\\server\share\x`, "a/../../evil"} {
		if _, err := safeJoin(dir, name); err == nil {
			t.Fatalf("%q 应当被拒绝", name)
		}
	}
}

// itoa 只为了避免在测试里引 strconv。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// zipOf 造一个内存 zip（条目名 → 内容）。
func zipOf(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
