// 找 ffmpeg/ffprobe：本地找 → 找不到就自动下载一份。
//
// 顺序与一键脚本一致（用户机器上不许有"必须先自己装 ffmpeg"这个前提）：
//
//	-ffmpeg-dir → PATH → exe 同级目录（./ffmpeg/bin/、./ffmpeg/、./bin/）
//	→ 醒目警告 + 5 秒倒计时（-yes 跳过）→ net/http 下载到 exe 同级目录 → 再找一次
//
// 下载用标准库 net/http（不依赖 curl）；zip 用标准库 archive/zip 解，
// .tar.xz 交给系统 tar（Go 标准库没有 xz 解码器，而"不加第三方依赖"是硬约束）。
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"ProjectionRoom/internal/service/segment"
)

// resolveTools 定位 ffmpeg/ffprobe，必要时自动下载一份。
//
// 返回的 error 非空表示"没凑齐两个工具"；此时调用方打印人工安装指引并降级
// （能直接切 fragmented MP4 就不该被卡住），绝不静默失败。
func (a *app) resolveTools(ctx context.Context) (segment.Tools, [2]string, error) {
	tools, sources := a.findTools()
	if tools.Available() {
		return tools, sources, nil
	}

	url := a.ffmpegURL()
	dir := a.ffmpegPkgDir()

	a.warnf("警告: 没有找到 ffmpeg/ffprobe（两个都需要：ffprobe 用来探测编码，ffmpeg 用来重新封装/转码）。")
	a.warnf("  %d 秒后自动下载一份到 %s（约 100 MB）：", countdownSeconds, dir)
	a.warnf("    %s", url)
	a.warnf("  不想下载就按 Ctrl+C 取消；也可以自己装好 ffmpeg，再用 -ffmpeg-dir 指定它的位置。")
	runCountdown(a.stdout, a.opts.yes, countdownSeconds, a.sleep)

	if err := a.downloadAndExtract(ctx, url, dir); err != nil {
		return tools, sources, fmt.Errorf("自动下载 ffmpeg 失败: %w", err)
	}
	a.sayf("已下载并解压到: %s", dir)

	tools, sources = a.findTools()
	if tools.Available() {
		return tools, sources, nil
	}

	// macOS：evermeet 的 ffmpeg 包里没有 ffprobe，补一份（成败都明确打印）。
	if a.goos == "darwin" && tools.FFmpeg != "" && tools.FFprobe == "" {
		a.warnf("警告: 下载到的包里只有 ffmpeg、没有 ffprobe；再用 %s 补一份。", darwinFFprobeURL)
		if err := a.downloadAndExtract(ctx, darwinFFprobeURL, dir); err != nil {
			a.warnf("警告: 补下载 ffprobe 失败: %v", err)
		} else {
			tools, sources = a.findTools()
			if tools.Available() {
				return tools, sources, nil
			}
		}
	}

	return tools, sources, fmt.Errorf("下载并解压之后仍然没有找到 ffmpeg/ffprobe（解压目录：%s）", dir)
}

// findTools 按顺序查找两个工具，返回路径与"来源说明"（用于把选择依据打给用户）。
func (a *app) findTools() (segment.Tools, [2]string) {
	var tools segment.Tools
	var sources [2]string
	targets := [2]*string{&tools.FFmpeg, &tools.FFprobe}
	for i, name := range [2]string{"ffmpeg", "ffprobe"} {
		path, source := a.findOne(name)
		*targets[i] = path
		sources[i] = source
	}
	return tools, sources
}

// findOne 查找单个工具，返回路径与来源说明（找不到时两者都是空）。
func (a *app) findOne(name string) (string, string) {
	// 1. -ffmpeg-dir（也认环境变量 PR_FFMPEG）：可以是目录，也可以是可执行文件。
	explicit := strings.TrimSpace(a.opts.ffmpegDir)
	if explicit == "" {
		explicit = strings.TrimSpace(os.Getenv("PR_FFMPEG"))
	}
	if explicit != "" {
		if p := lookInExplicit(explicit, name, a.exeSuffix()); p != "" {
			return p, "-ffmpeg-dir"
		}
		a.warnf("警告: -ffmpeg-dir 给的位置里没有 %s：%s（继续按 PATH → exe 同级目录 查找）", name, explicit)
	}

	// 2. PATH。
	if a.lookPath != nil {
		if p, err := a.lookPath(name); err == nil && p != "" {
			return p, "PATH"
		}
	}

	// 3. exe 同级目录：./ffmpeg/bin/、./ffmpeg/、./bin/（与一键脚本同一份清单）。
	for _, dir := range a.siblingToolDirs() {
		if p := lookInDir(dir, name, a.exeSuffix()); p != "" {
			return p, "exe 同级目录 " + dir
		}
	}

	// 4. 自动下载/解压目录：压缩包内部是嵌套结构，要递归找。
	for _, dir := range a.downloadDirs() {
		if p := findToolRecursive(dir, name, a.exeSuffix()); p != "" {
			return p, "自动下载目录 " + dir
		}
	}
	return "", ""
}

// printTools 打印最终使用的两个工具与来源（"-ffmpeg-dir / PATH / exe 同级目录 / 自动下载目录"）。
func (a *app) printTools(tools segment.Tools, sources [2]string) {
	a.sayf("ffmpeg:  %s   （来源: %s）", tools.FFmpeg, sources[0])
	a.sayf("ffprobe: %s   （来源: %s）", tools.FFprobe, sources[1])
}

// exeSuffix 是当前平台上可执行文件的后缀（只影响查找，不影响运行）。
func (a *app) exeSuffix() string {
	if a.goos == "windows" {
		return ".exe"
	}
	return ""
}

// siblingToolDirs 是 exe 同级的候选目录（顺序即优先级）。
func (a *app) siblingToolDirs() []string {
	if a.exeDir == "" {
		return nil
	}
	return []string{
		filepath.Join(a.exeDir, "ffmpeg", "bin"),
		filepath.Join(a.exeDir, "ffmpeg"),
		filepath.Join(a.exeDir, "bin"),
	}
}

// downloadDirs 是"自动下载/解压"可能落地的目录。
// _ffmpeg 是旧版 PowerShell 一键脚本用的目录名，留着可以复用用户已经下好的那一份。
func (a *app) downloadDirs() []string {
	if a.exeDir == "" {
		return nil
	}
	return []string{
		filepath.Join(a.exeDir, ffmpegPkgDirName),
		filepath.Join(a.exeDir, "_ffmpeg"),
	}
}

// ffmpegPkgDir 是本次自动下载的落地目录（exe 同级）。
func (a *app) ffmpegPkgDir() string {
	return filepath.Join(a.exeDir, ffmpegPkgDirName)
}

// ffmpegURL 决定下载地址：-ffmpeg-url → PR_FFMPEG_URL → 按平台的默认值。
func (a *app) ffmpegURL() string {
	if u := strings.TrimSpace(a.opts.ffmpegURL); u != "" {
		return u
	}
	if u := strings.TrimSpace(os.Getenv("PR_FFMPEG_URL")); u != "" {
		return u
	}
	switch a.goos {
	case "darwin":
		return ffmpegURLDarwin
	case "windows":
		return ffmpegURLWindows
	default:
		return ffmpegURLLinux
	}
}

// lookInExplicit 解析显式给出的"目录或可执行文件"。
// 明确给了路径却不存在时直接放行（返回空），由调用方给出可见警告，不静默用别的版本。
func lookInExplicit(explicit, name, suffix string) string {
	explicit = strings.TrimSpace(explicit)
	if explicit == "" {
		return ""
	}
	info, err := os.Stat(explicit)
	if err != nil {
		return ""
	}
	if info.IsDir() {
		return lookInDir(explicit, name, suffix)
	}
	base := strings.TrimSuffix(strings.ToLower(filepath.Base(explicit)), strings.ToLower(suffix))
	if base == name {
		return explicit
	}
	return lookInDir(filepath.Dir(explicit), name, suffix)
}

// lookInDir 判断 dir 下是否有名为 name 的可执行文件（带后缀与不带后缀都认）。
func lookInDir(dir, name, suffix string) string {
	if dir == "" {
		return ""
	}
	for _, candidate := range []string{filepath.Join(dir, name+suffix), filepath.Join(dir, name)} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

// findToolRecursive 在目录树里递归找一个工具（下载到的包里层级不固定：
// 官方 zip 是 ffmpeg-<版本>-essentials_build/bin/ffmpeg.exe，静态 tar.xz 是 ffmpeg-<版本>-static/ffmpeg）。
func findToolRecursive(dir, name, suffix string) string {
	if dir == "" {
		return ""
	}
	if _, err := os.Stat(dir); err != nil {
		return ""
	}
	var found string
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || found != "" {
			return nil // 读不动的子树跳过，不当成失败
		}
		if entry.IsDir() {
			return nil
		}
		base := entry.Name()
		if base == name+suffix || (suffix != "" && base == name) {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// downloadAndExtract 把 url 下载到 dir 并解压（按扩展名选 zip 或 tar.xz）。
func (a *app) downloadAndExtract(ctx context.Context, url, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建下载目录 %s 失败: %w", dir, err)
	}
	// 扩展名判断要去掉 query（下载地址常带 ?v=1 之类）。
	clean := strings.ToLower(strings.SplitN(url, "?", 2)[0])
	switch {
	case strings.HasSuffix(clean, ".zip"):
		archive := filepath.Join(dir, "ffmpeg.zip")
		if err := a.download(ctx, url, archive); err != nil {
			return err
		}
		return unzipInto(archive, dir, a.goos)
	case strings.HasSuffix(clean, ".tar.xz"):
		archive := filepath.Join(dir, "ffmpeg.tar.xz")
		if err := a.download(ctx, url, archive); err != nil {
			return err
		}
		return untarXZInto(archive, dir, a.goos)
	default:
		return fmt.Errorf("不认识的压缩格式（只支持 .zip 与 .tar.xz）：%s", url)
	}
}

// download 用 net/http 下载到 dest（先写 .part 再改名，避免半截文件被当成完整包）。
func (a *app) download(ctx context.Context, url, dest string) error {
	client := a.httpClient
	if client == nil {
		client = http.DefaultClient
	}

	a.sayf("下载中: %s", url)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("下载地址不合法（%s）: %w", url, err)
	}
	// 有些镜像对空 UA 会直接拒绝，给一个明确的 UA。
	req.Header.Set("User-Agent", "ProjectionRoom-segmenter/1")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("下载 %s 失败: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载 %s 失败: HTTP %d", url, resp.StatusCode)
	}

	tmp := dest + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("创建 %s 失败: %w", tmp, err)
	}
	written, copyErr := io.Copy(f, &progressReader{r: resp.Body, w: a.stdout, total: resp.ContentLength})
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("下载 %s 中断: %w", url, copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("写入 %s 失败: %w", tmp, closeErr)
	}
	if written == 0 {
		_ = os.Remove(tmp)
		return fmt.Errorf("下载 %s 得到 0 字节（地址是不是需要登录/已经失效？）", url)
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("保存 %s 失败: %w", dest, err)
	}
	a.sayf("已下载: %s（%s）", dest, segment.HumanBytes(written))
	return nil
}

// progressReader 每 10%（未知总长时每 8 MiB）打一行进度。
// 只影响观感，不影响正确性；失败时不吞错误。
type progressReader struct {
	r        io.Reader
	w        io.Writer
	total    int64
	read     int64
	lastMark int
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.read += int64(n)

	step := int64(8 << 20)
	if p.total > 0 {
		step = p.total / 10
		if step <= 0 {
			step = 1
		}
	}
	if mark := int(p.read / step); mark > p.lastMark {
		p.lastMark = mark
		if p.total > 0 {
			fmt.Fprintf(p.w, "  … %d%%（%s）\n", int(p.read*100/p.total), segment.HumanBytes(p.read))
		} else {
			fmt.Fprintf(p.w, "  … 已下载 %s\n", segment.HumanBytes(p.read))
		}
	}
	return n, err
}

// unzipInto 用标准库解压 zip（不依赖外部 unzip）。
// 逐条目处理：拒绝目录穿越，按需保留可执行位（macOS 的静态包靠它跑起来）。
func unzipInto(archive, dir, goos string) error {
	reader, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("打开压缩包 %s 失败: %w", archive, err)
	}
	defer reader.Close()

	for _, entry := range reader.File {
		target, err := safeJoin(dir, entry.Name)
		if err != nil {
			return err
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("创建目录 %s 失败: %w", target, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("创建目录 %s 失败: %w", filepath.Dir(target), err)
		}

		mode := os.FileMode(0o644)
		if entry.Mode()&0o111 != 0 || (goos != "windows" && isToolFileName(filepath.Base(entry.Name))) {
			mode = 0o755
		}
		src, err := entry.Open()
		if err != nil {
			return fmt.Errorf("打开压缩包条目 %s 失败: %w", entry.Name, err)
		}
		dst, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			_ = src.Close()
			return fmt.Errorf("写入 %s 失败: %w", target, err)
		}
		_, copyErr := io.Copy(dst, src)
		closeDstErr := dst.Close()
		closeSrcErr := src.Close()
		if copyErr != nil {
			_ = os.Remove(target)
			return fmt.Errorf("解压 %s 失败: %w", entry.Name, copyErr)
		}
		if closeDstErr != nil || closeSrcErr != nil {
			_ = os.Remove(target)
			return fmt.Errorf("关闭 %s 失败: %v / %v", entry.Name, closeDstErr, closeSrcErr)
		}
		// 显式 chmod：umask 会让 0755 变成 0744 之类，导致工具不可执行。
		if mode == 0o755 {
			_ = os.Chmod(target, 0o755)
		}
	}
	return nil
}

// untarXZInto 用系统 tar 解压 .tar.xz。
//
// Go 标准库没有 xz 解码器，而"不加第三方依赖"是硬约束；Linux/macOS 都自带 `tar -xJf`。
// Windows 上没有这个分支（Windows 的默认下载地址是 zip）。
func untarXZInto(archive, dir, goos string) error {
	if goos == "windows" {
		return fmt.Errorf("Windows 不支持 .tar.xz（请用 .zip 的下载地址，或手动装好 ffmpeg 后用 -ffmpeg-dir 指定）")
	}
	cmd := exec.Command("tar", "-xJf", archive, "-C", dir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("解压 %s 失败（需要系统自带且支持 xz 的 tar）: %w\n%s",
			archive, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// isToolFileName 判断解压出来的这个名字是不是我们要用的工具（决定是否给可执行位）。
func isToolFileName(base string) bool {
	name := strings.ToLower(base)
	name = strings.TrimSuffix(name, ".exe")
	return name == "ffmpeg" || name == "ffprobe"
}

// safeJoin 把压缩包里的条目名接到目标目录下，并拒绝一切目录穿越。
func safeJoin(dir, name string) (string, error) {
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, `\`) {
		return "", fmt.Errorf("压缩包条目用了绝对路径，拒绝解压: %s", name)
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) || filepath.VolumeName(clean) != "" {
		return "", fmt.Errorf("压缩包条目用了绝对路径，拒绝解压: %s", name)
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("压缩包条目试图写到目标目录之外，拒绝解压: %s", name)
	}
	target := filepath.Join(dir, clean)
	rel, err := filepath.Rel(dir, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("压缩包条目试图写到目标目录之外，拒绝解压: %s", name)
	}
	return target, nil
}

// runCountdown 打印自动下载前的倒计时；yes（-yes）为真时整段跳过。
//
// sleep 可注入：单测用假 sleep 验证"确实等了 5 秒"与"-yes 一次都不等"，不必真等。
func runCountdown(w io.Writer, yes bool, seconds int, sleep func(time.Duration)) {
	if yes {
		fmt.Fprintf(w, "已指定 -yes：跳过 %d 秒倒计时，直接下载。\n", seconds)
		return
	}
	for i := seconds; i >= 1; i-- {
		fmt.Fprintf(w, "  %d ...\n", i)
		if sleep != nil {
			sleep(time.Second)
		}
	}
}
