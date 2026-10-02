package segment

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// knownWindowsFFmpegDir 是需求里点名的本机安装位置；
// 在这个目录不存在时相关用例会跳过，而不是假装通过。
const knownWindowsFFmpegDir = `D:\Program Files (x86)\ffmpeg-8.0.1-essentials_build\bin`

func TestFromExplicitHandlesFileAndDir(t *testing.T) {
	// 明确给了不存在的位置：不猜、不回退到 LookPath，直接当作"没指定"。
	if got := fromExplicit(`Z:\definitely-missing-path\ffmpeg.exe`, "ffmpeg"); got != "" {
		t.Fatalf("不存在的显式路径应返回空，实际 %q", got)
	}

	if runtime.GOOS != "windows" {
		t.Skip("Windows 专有的路径分支")
	}
	if _, err := os.Stat(knownWindowsFFmpegDir); err != nil {
		t.Skipf("本机没有 %s，跳过显式目录用例", knownWindowsFFmpegDir)
	}

	// 传目录：应能在里面找到 ffmpeg/ffprobe。
	if got := fromExplicit(knownWindowsFFmpegDir, "ffmpeg"); got == "" {
		t.Fatalf("目录 %s 下应能找到 ffmpeg", knownWindowsFFmpegDir)
	}
	// 传可执行文件：同目录的 ffprobe 也要能被找出来。
	ffmpegExe := filepath.Join(knownWindowsFFmpegDir, "ffmpeg.exe")
	tools := Tools{FFmpeg: fromExplicit(ffmpegExe, "ffmpeg"), FFprobe: fromExplicit(ffmpegExe, "ffprobe")}
	if !tools.Available() {
		t.Fatalf("显式传 ffmpeg.exe 时也应当定位到 ffprobe，实际 %+v", tools)
	}
	if filepath.Base(tools.FFmpeg) != "ffmpeg.exe" || filepath.Base(tools.FFprobe) != "ffprobe.exe" {
		t.Fatalf("定位结果不对: %+v", tools)
	}
}

// TestDiscoverToolsExplicitWins 验证 PR_FFMPEG 的优先级：显式给的目录即使不在 PATH 里也必须生效。
func TestDiscoverToolsExplicitWins(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows 专有的路径分支")
	}
	if _, err := os.Stat(knownWindowsFFmpegDir); err != nil {
		t.Skipf("本机没有 %s，跳过用例", knownWindowsFFmpegDir)
	}

	t.Setenv("PATH", "")
	tools := DiscoverTools(knownWindowsFFmpegDir)
	if !tools.Available() {
		t.Fatalf("PATH 为空时显式目录必须生效，实际 %+v", tools)
	}
	if filepath.Dir(tools.FFmpeg) != knownWindowsFFmpegDir {
		t.Fatalf("应使用显式目录里的 ffmpeg，实际 %q", tools.FFmpeg)
	}
}

// TestDiscoverToolsFallsBackToCommonDirs 验证第 3 条发现路径：
// PATH 里没有 ffmpeg 时，应当回退到 Windows 常见安装目录。
func TestDiscoverToolsFallsBackToCommonDirs(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows 专有的常见目录回退")
	}
	if _, err := os.Stat(knownWindowsFFmpegDir); err != nil {
		t.Skipf("本机没有 %s，无法验证常见目录回退", knownWindowsFFmpegDir)
	}

	t.Setenv("PATH", "")
	tools := DiscoverTools("")
	if !tools.Available() {
		t.Fatal("PATH 为空时应回退到 Windows 常见安装目录找到 ffmpeg/ffprobe")
	}
}

// TestDiscoverToolsMissingIsExplicit 验证"找不到就是找不到"：
// 只给一个不存在的显式路径、且 PATH 也是空的时候，必须返回空 Tools，
// 而不是编造一个可执行文件路径出去。
func TestDiscoverToolsMissingIsExplicit(t *testing.T) {
	t.Setenv("PATH", "")
	tools := DiscoverTools(`Z:\definitely-missing-path`)
	if tools.FFmpeg != "" || tools.FFprobe != "" {
		// 允许"常见目录里真的有"这一种情况（本机就属于这种情况），否则必须为空。
		if _, err := os.Stat(knownWindowsFFmpegDir); err != nil {
			t.Fatalf("找不到 ffmpeg 时应返回空 Tools，实际 %+v", tools)
		}
	}
	if tools.Available() {
		t.Logf("本机常见安装目录里确实有 ffmpeg，跳过空值断言: %+v", tools)
	}
}

func TestCandidateDirsNonEmpty(t *testing.T) {
	dirs := candidateDirs()
	if len(dirs) == 0 {
		t.Fatal("候选目录列表不应为空")
	}
	for _, dir := range dirs {
		if dir == "" {
			t.Fatal("候选目录里不应出现空字符串")
		}
	}
}

func TestSourceExt(t *testing.T) {
	cases := map[string]string{
		"movie.mp4":    ".mp4",
		"MOVIE.MP4":    ".mp4",
		"clip.mkv":     ".mkv",
		"archive.zip":  ".bin",
		"noext":        ".bin",
		"":             ".bin",
		"weird.tar.gz": ".bin",
	}
	for input, want := range cases {
		if got := SourceExt(input); got != want {
			t.Fatalf("SourceExt(%q) = %q，应为 %q", input, got, want)
		}
	}
}
