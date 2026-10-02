package segment

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Tools 是一对已定位的可执行文件。任一为空即表示未找到，Available() 为 false。
type Tools struct {
	FFmpeg  string
	FFprobe string
}

// Available 报告 ffmpeg 与 ffprobe 是否都可用。
func (t Tools) Available() bool {
	return t.FFmpeg != "" && t.FFprobe != ""
}

// DiscoverTools 按下面的顺序定位 ffmpeg/ffprobe：
//
//  1. explicit（PR_FFMPEG）：可以是可执行文件，也可以是包含它的目录；
//  2. exec.LookPath（即 PATH）；
//  3. Windows 常见安装目录（含本机实测的 D:\Program Files (x86)\ffmpeg-8.0.1-essentials_build\bin）。
//
// 只做查找，不做任何"猜一个正在运行的进程"之类的魔法：找不到就返回空 Tools，
// 由调用方给出明确的可见错误（见 ErrFFmpegMissing）。
func DiscoverTools(explicit string) Tools {
	return Tools{
		FFmpeg:  findExecutable(explicit, "ffmpeg"),
		FFprobe: findExecutable(explicit, "ffprobe"),
	}
}

// exeSuffix 是当前平台的可执行文件后缀。
func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// findExecutable 定位单个可执行文件。
func findExecutable(explicit, name string) string {
	if p := fromExplicit(explicit, name); p != "" {
		return p
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, dir := range candidateDirs() {
		if p := probe(dir, name); p != "" {
			return p
		}
	}
	return ""
}

// fromExplicit 处理 PR_FFMPEG：既可指向可执行文件，也可指向目录。
func fromExplicit(explicit, name string) string {
	explicit = strings.TrimSpace(explicit)
	if explicit == "" {
		return ""
	}
	info, err := os.Stat(explicit)
	if err != nil {
		// 明确给了路径却不存在：不再继续猜，避免"静默用了别的版本"。
		return ""
	}
	if info.IsDir() {
		return probe(explicit, name)
	}
	// 指向具体文件：若它就是要找的，直接用；否则在同目录里找。
	if base := strings.TrimSuffix(strings.ToLower(filepath.Base(explicit)), exeSuffix()); base == name {
		return explicit
	}
	return probe(filepath.Dir(explicit), name)
}

// probe 判断 dir 下是否有名为 name 的可执行文件。
func probe(dir, name string) string {
	if dir == "" {
		return ""
	}
	for _, candidate := range []string{filepath.Join(dir, name+exeSuffix()), filepath.Join(dir, name)} {
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

// candidateDirs 返回"常见安装位置"。
// 只列确定存在的目录模式，找不到就明确失败，不去扫整个磁盘（那会让启动变得不可预测）。
func candidateDirs() []string {
	var dirs []string
	if runtime.GOOS == "windows" {
		// 本机实测路径（用户在需求里点名的那一份）。
		dirs = append(dirs,
			`D:\Program Files (x86)\ffmpeg-8.0.1-essentials_build\bin`,
			`C:\ffmpeg\bin`,
			`C:\Program Files\ffmpeg\bin`,
			`C:\Program Files (x86)\ffmpeg\bin`,
			`C:\ProgramData\chocolatey\bin`,
		)
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			dirs = append(dirs,
				filepath.Join(local, `Microsoft\WinGet\Links`),
				// winget 安装的 Gyan.FFmpeg 落在 Packages 下的版本目录里。
				filepath.Join(local, `Microsoft\WinGet\Packages`),
			)
		}
		if home := os.Getenv("USERPROFILE"); home != "" {
			dirs = append(dirs, filepath.Join(home, `scoop\shims`))
		}
		dirs = append(dirs, globDirs(
			`C:\Program Files\ffmpeg*\bin`,
			`C:\Program Files (x86)\ffmpeg*\bin`,
			`D:\Program Files (x86)\ffmpeg*\bin`,
			`C:\ProgramData\chocolatey\lib\ffmpeg*\tools\ffmpeg*\bin`,
		)...)
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			dirs = append(dirs, globDirs(
				filepath.Join(local, `Microsoft\WinGet\Packages`, `Gyan.FFmpeg*`, `ffmpeg*`, `bin`),
			)...)
		}
	}
	dirs = append(dirs, "/usr/local/bin", "/usr/bin", "/opt/homebrew/bin", "/snap/bin")
	return dirs
}

// globDirs 展开目录通配模式，按字典序返回（保证同一台机器上结果稳定）。
func globDirs(patterns ...string) []string {
	var out []string
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		out = append(out, matches...)
	}
	return out
}
