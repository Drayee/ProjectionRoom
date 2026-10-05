// 找输入文件：完全对齐一键脚本的优先级。
//
// 浏览器拿不到用户所选文件的完整本地路径（安全限制），所以脚本只能"用想要的文件名去找"；
// exe 版多了一条更省事的路径：唯一的位置参数（拖拽到 exe 上就是这个形状）。
package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ProjectionRoom/internal/service/segment"
)

// resolveInput 按优先级确定输入文件：
//
//  1. -in 显式给的路径；
//  2. 唯一的位置参数（把视频拖到 exe 上、或在命令行只给一个文件）；
//  3. 在常见目录里按文件名找（文件名来自 -name，否则取 -out 的目录名）；
//  4. 交互式询问（读 stdin）。
//
// 交互询问在非交互环境下不会挂死：stdin 被关闭/是 NUL 时读到空立即报错退出。
func (a *app) resolveInput() (string, error) {
	if v := strings.TrimSpace(a.opts.in); v != "" {
		return a.acceptInput(v, "-in")
	}

	switch len(a.opts.positional) {
	case 1:
		return a.acceptInput(a.opts.positional[0], "位置参数")
	case 0:
		// 往下走"按文件名查找"。
	default:
		return "", fmt.Errorf(
			"只接受一个位置参数（拖到 exe 上的那个视频文件），实际给了 %d 个：%s\n"+
				"请改成 -in <视频文件> 一个个指定", len(a.opts.positional),
			strings.Join(a.opts.positional, "  "))
	}

	name := a.opts.wantName()
	if name != "" && a.opts.searchByName {
		if found := a.searchByNameInCommonDirs(name); found != "" {
			a.sayf("在常见目录里找到了: %s", found)
			return a.acceptInput(found, "按文件名查找")
		}
	}

	// ---------- 4. 交互式询问 ----------
	if !a.opts.searchByName {
		a.warnf("已指定 -search-by-name=false：跳过按文件名在常见目录里查找。")
	}
	if name != "" {
		a.warnf("要找的文件名是: %s", name)
	}
	a.warnf("没找到视频文件。把文件拖到 segmenter 上，或粘贴完整路径" +
		"（资源管理器里 Shift+右键 → 复制文件地址）。")
	fmt.Fprint(a.stdout, "视频文件完整路径: ")

	line, err := bufio.NewReader(a.stdin).ReadString('\n')
	if err != nil && line == "" {
		// EOF/管道关掉：明确失败，绝不在这里等下去。
		return "", fmt.Errorf("没有从 stdin 读到路径（非交互环境请用 -in 或位置参数指定输入文件）: %w", err)
	}
	path := unquote(strings.TrimSpace(line))
	if path == "" {
		return "", fmt.Errorf("没有输入任何路径（非交互环境请用 -in 或位置参数指定输入文件）")
	}
	return a.acceptInput(path, "stdin")
}

// wantName 返回"想要的文件名"：-name 优先，否则取 -out 的目录名（与一键脚本一致）。
func (o *options) wantName() string {
	if n := strings.TrimSpace(o.name); n != "" {
		return n
	}
	base := filepath.Base(filepath.Clean(o.out))
	if base == "." || base == string(filepath.Separator) || base == "" {
		return ""
	}
	return base
}

// acceptInput 校验并规范化一个候选输入，打印"输入: 路径 (体积)"。
func (a *app) acceptInput(path, source string) (string, error) {
	p := unquote(strings.TrimSpace(path))
	if p == "" {
		return "", fmt.Errorf("%s 给出的是空路径", source)
	}
	info, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("找不到输入文件：%s（来自 %s）", p, source)
	}
	if info.IsDir() {
		return "", fmt.Errorf("输入是目录而不是视频文件：%s", p)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	// 体积用 segment.HumanBytes 打印，与摘要里的单位一致。
	a.sayf("输入: %s  (%s)", abs, segment.HumanBytes(info.Size()))
	return abs, nil
}

// searchByNameInCommonDirs 在常见目录里按文件名精确查找，返回第一个命中的路径。
//
// 名字里带路径分隔符时不做目录搜索：那已经是相对/绝对路径，只按它自己找一次。
func (a *app) searchByNameInCommonDirs(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if strings.ContainsAny(name, `/\`) {
		if info, err := os.Stat(name); err == nil && !info.IsDir() {
			if abs, err := filepath.Abs(name); err == nil {
				return abs
			}
			return name
		}
		return ""
	}
	for _, dir := range a.commonDirs() {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

// commonDirs 返回"常见目录"：桌面、下载、视频、文档、当前目录、exe 所在目录。
// 顺序即优先级；重复项与空项会被去掉（保持顺序稳定，便于排查）。
func (a *app) commonDirs() []string {
	if len(a.searchDirs) > 0 {
		return a.searchDirs
	}
	var dirs []string
	if a.homeDir != "" {
		dirs = append(dirs,
			filepath.Join(a.homeDir, "Desktop"),
			filepath.Join(a.homeDir, "Downloads"),
			filepath.Join(a.homeDir, "Videos"),
			filepath.Join(a.homeDir, "Documents"),
		)
	}
	dirs = append(dirs, a.cwd, a.exeDir)
	return dedupePaths(dirs)
}

// dedupePaths 去掉空串与重复项（大小写不敏感，Windows 上 C:\a 与 c:\a 是同一个目录）。
func dedupePaths(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		key := strings.ToLower(p)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, p)
	}
	return out
}

// unquote 去掉用户粘贴路径时常见的成对引号（Windows"复制文件地址"会带上双引号）。
func unquote(s string) string {
	s = strings.TrimSpace(s)
	for _, pair := range [][2]string{{`"`, `"`}, {`'`, `'`}} {
		if len(s) >= 2 && strings.HasPrefix(s, pair[0]) && strings.HasSuffix(s, pair[1]) {
			return strings.TrimSpace(s[1 : len(s)-1])
		}
	}
	return s
}
