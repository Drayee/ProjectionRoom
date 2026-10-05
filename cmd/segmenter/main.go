// Command segmenter 把本地视频预处理成放映室可用的 fMP4 分片目录（SPEC §4.1、§4.5）。
//
// 它是「浏览器一键切片脚本」的可执行文件版本：用户只下载一个 exe，**不需要自己装 ffmpeg**。
// 一次运行包含脚本里原来的全部步骤：
//
//	找输入 → 找 ffmpeg（没有就下载一份） → 探测并决定直通/转码 → 按 moof 边界切分 → 打印摘要与容量提示
//
// 三种用法（都可以）：
//
//	segmenter -in movie.mkv -out ./room-media              # 显式指定输入
//	segmenter "D:\video\movie.mkv" -out ./room-media        # 只给位置参数（把视频拖到 exe 上就是这个形状）
//	segmenter -out ./room-media -name movie.mkv             # 按文件名在常见目录里找，找不到再交互式询问
//
// 默认不再要求用户选 -fragment/-transcode：工具会先探测编码，可直通就只做无损重新封装
// （-c copy），不可直通就自动转码成 H.264/AAC。两个开关仍然保留，用来强制指定。
//
// 默认每 100 片合成一个 pack-0001.bin（产物文件数从 ~1800 降到 ~18，Windows 上写盘快得多）；
// `-pack 1` 关掉打包，产出的目录与打包功能出现之前逐字节等价。
//
// 为什么必须切在 moof 边界上：SourceBuffer.appendBuffer() 只接受完整的 fMP4 片段，
// 按固定字节数切割会产出"半个 moof"，浏览器直接抛错（SPEC §4.2）。
//
// 流水线本体（必要时 ffmpeg 重新封装/转码 → 按 moof 边界切分 → 写 index.json）在
// internal/service/segment，与服务端一次性切片共用同一份实现，
// 因此"本地切片"与"服务端切片"的产物格式不可能漂移（前端零改动）。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/service/segment"
)

// 默认的 ffmpeg 下载地址（-ffmpeg-url / 环境变量 PR_FFMPEG_URL 可覆盖）。
const (
	ffmpegURLWindows = "https://www.gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip"
	ffmpegURLDarwin  = "https://evermeet.cx/ffmpeg/getrelease/zip"
	ffmpegURLLinux   = "https://johnvansickle.com/ffmpeg/releases/ffmpeg-release-amd64-static.tar.xz"

	// darwinFFprobeURL 是 macOS 上的补充下载地址：
	// evermeet 的 ffmpeg 包里**只有 ffmpeg、没有 ffprobe**，而探测编码必须要 ffprobe。
	// 所以主包解压后若缺 ffprobe，再按这个地址补一份（成败都会明确打印）。
	darwinFFprobeURL = "https://evermeet.cx/ffmpeg/getrelease/ffprobe/zip"

	// countdownSeconds 是自动下载前的倒计时秒数（与一键脚本一致，-yes 跳过）。
	countdownSeconds = 5

	// ffmpegPkgDirName 是自动下载/解压的落地目录名（相对 exe 所在目录）。
	ffmpegPkgDirName = "ffmpeg-pkg"

	// 手动安装指引：任何"找不到 ffmpeg 又下不下来"的路径都要打出来，不能静默失败。
	manualInstallHint = "手动安装指引：到 https://www.ffmpeg.org/download.html 下载，" +
		"解压后用 -ffmpeg-dir 指定它所在的 bin 目录（例如 -ffmpeg-dir C:\\ffmpeg\\bin），或把该目录加进 PATH。"
)

// boolFlagNames 是"取值不跟在后面的"开关名。
// 拆分参数时要用它判断"-x"后面那一个词是它的值，还是位置参数（见 splitArgs）。
var boolFlagNames = map[string]bool{
	"fragment":       true,
	"yes":            true,
	"search-by-name": true,
	"h":              true,
	"help":           true,
}

// usageText 是 -h / 参数写错时的中文用法说明。
const usageText = `segmenter 把一个视频切成放映室可用的分片目录（init.mp4 + pack-*.bin + index.json）。

用法:
  segmenter -in <视频文件> -out <输出目录> [其它参数]
  segmenter <视频文件> -out <输出目录>          # 唯一的位置参数会被当成输入（拖到 exe 上即可）
  segmenter -out <输出目录>                     # 不给输入：按文件名在常见目录里找，找不到再问你

参数:
`

// options 是命令行参数（与一键脚本的参数一一对应，只是名字改成了 Go 习惯的 -x 形式）。
type options struct {
	// in 是显式指定的输入视频。
	in string
	// name 是"想要的文件名"，用于在常见目录里查找。
	name string
	// out 是输出（分片）目录。
	out string
	// transcode 非空则强制转码到该视频码率（如 1200k），优先于自动判定。
	transcode string
	// fragment 为真则强制无损重新封装（-c copy），跳过自动判定。
	fragment bool
	// fragSec 是分片目标时长（秒）。
	fragSec float64
	// pack 是打包粒度（每 N 片一个 pack-*.bin；1 = 不打包）。
	pack int
	// uplinkMbps 是主播上行估计（Mbps），仅用于打印容量提示。
	uplinkMbps float64
	// ffmpegDir 是 ffmpeg/ffprobe 所在目录或可执行文件（-ffmpeg-dir / 别名 -ffmpeg）。
	ffmpegDir string
	// ffmpegURL 是 ffmpeg 下载地址覆盖（空则看环境变量与平台默认值）。
	ffmpegURL string
	// yes 为真则跳过自动下载前的倒计时。
	yes bool
	// searchByName 为假则不做"按文件名在常见目录里查找"，直接进入交互询问。
	searchByName bool
	// quiet 为真则少打印流水线日志（当前保留，供以后接 -q）。
	quiet bool
	// positional 是去掉参数之后剩下的位置参数（拖拽进来的文件就在里面）。
	positional []string
}

// app 是一次运行的全部外部依赖。
//
// 抽成结构体的唯一目的是可测：单测能换掉 exe 目录、stdin/stdout、时钟、HTTP 客户端与 PATH 查找，
// 否则"没装 ffmpeg → 倒计时 → 下载 → 解压 → 找到工具"这条最关键的路径只能靠真下 100 MB 来验证。
type app struct {
	opts   *options
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer

	// exeDir 是 exe 所在目录：自动下载落在这里，也在这里找同级 ffmpeg。
	exeDir string
	// homeDir / cwd 用于拼"常见目录"。
	homeDir string
	cwd     string
	// searchDirs 非空时覆盖默认的常见目录列表（单测用）。
	searchDirs []string

	// sleep 注入倒计时的等待（单测用假 sleep 验证"5 秒"与"-yes 跳过"）。
	sleep      func(time.Duration)
	httpClient *http.Client
	lookPath   func(string) (string, error)
	goos       string
}

func main() {
	a, err := newApp(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return // -h/--help：用法已经打印过，正常退出
		}
		fmt.Fprintf(os.Stderr, "segmenter: %v\n", err)
		os.Exit(2)
	}
	if err := a.run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "\nsegmenter 失败: %v\n", err)
		os.Exit(1)
	}
}

// newApp 解析参数并组装真实依赖。
func newApp(args []string) (*app, error) {
	opts, err := parseOptions(args, os.Stderr)
	if err != nil {
		return nil, err
	}

	exeDir := ""
	if exe, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exe)
	}
	home, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()

	return &app{
		opts:       opts,
		stdin:      os.Stdin,
		stdout:     os.Stdout,
		stderr:     os.Stderr,
		exeDir:     exeDir,
		homeDir:    home,
		cwd:        cwd,
		sleep:      time.Sleep,
		httpClient: &http.Client{Timeout: 30 * time.Minute},
		lookPath:   exec.LookPath,
		goos:       runtime.GOOS,
	}, nil
}

// parseOptions 解析命令行：先按"标志/位置参数"拆开，再交给 flag 包。
//
// 为什么要先拆：Go 的 flag 包遇到第一个非标志参数就停止解析，
// 于是 `segmenter movie.mkv -out ./cut`（拖拽 + 参数）里的 -out 会被当成位置参数丢掉。
func parseOptions(args []string, usage io.Writer) (*options, error) {
	flagArgs, positional := splitArgs(args, boolFlagNames)

	opts := &options{}
	fs := flag.NewFlagSet("segmenter", flag.ContinueOnError)
	fs.SetOutput(usage)
	fs.Usage = func() { fmt.Fprint(usage, usageText); fs.PrintDefaults() }

	fs.StringVar(&opts.in, "in", "",
		"输入视频文件；不给时按 位置参数 → 常见目录按文件名查找 → 交互式询问 的顺序找")
	fs.StringVar(&opts.name, "name", "",
		"想在常见目录里查找的文件名；不给时用 -out 的目录名（与一键脚本一致）")
	fs.BoolVar(&opts.searchByName, "search-by-name", true,
		"没给 -in 时是否按文件名在 桌面/下载/视频/文档/当前目录/exe 同级目录 里查找")
	fs.StringVar(&opts.out, "out", "./room-media", "输出（分片）目录")
	fs.StringVar(&opts.transcode, "transcode", "",
		"强制转码到指定视频码率（如 1200k）后再切；不给则自动判定直通/转码")
	fs.BoolVar(&opts.fragment, "fragment", false,
		"强制无损重新封装为 fMP4（-c copy）后再切；不给则自动判定")
	fs.Float64Var(&opts.fragSec, "frag-sec", 2, "分片目标时长（秒）")
	fs.IntVar(&opts.pack, "pack", config.DefaultPackSize,
		"打包粒度：每 N 片合成一个 pack-*.bin；1 = 不打包（逐片一个 c*.m4s）")
	fs.Float64Var(&opts.uplinkMbps, "uplink-mbps", 12, "主播上行估计（Mbps），仅用于打印容量提示")
	fs.StringVar(&opts.ffmpegDir, "ffmpeg-dir", "",
		"ffmpeg/ffprobe 所在目录（或可执行文件）；优先于 PATH 与自动下载")
	// -ffmpeg 是 -ffmpeg-dir 的历史名字。两个标志写同一个变量，
	// 因此先出现哪个都行，同时给则以最后出现的为准（flag 包的默认行为）。
	fs.StringVar(&opts.ffmpegDir, "ffmpeg", "", "-ffmpeg-dir 的别名（向后兼容）")
	fs.StringVar(&opts.ffmpegURL, "ffmpeg-url", "",
		"ffmpeg 下载地址；默认按平台，也认环境变量 PR_FFMPEG_URL")
	fs.BoolVar(&opts.yes, "yes", false,
		fmt.Sprintf("跳过自动下载前的 %d 秒倒计时（仍然会下载）", countdownSeconds))

	if err := fs.Parse(flagArgs); err != nil {
		return nil, err
	}
	opts.positional = positional
	return opts, nil
}

// splitArgs 把参数拆成"交给 flag 包的标志序列"与"位置参数"。
//
// 规则：
//   - `--` 之后一律是位置参数；
//   - `-x=v` 是一个完整标志；
//   - `-x v` 中，x 不是 bool 开关时 v 是它的值（bool 开关不吞下一个词，因为
//     `-fragment movie.mkv` 里的 movie.mkv 是输入文件，不是 -fragment 的值）；
//   - 其它以 '-' 开头（且不是单独的 "-"）的都当标志。
func splitArgs(args []string, boolFlags map[string]bool) (flags []string, positional []string) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(arg) > 1 && arg[0] == '-' {
			flags = append(flags, arg)
			name := strings.TrimLeft(arg, "-")
			if idx := strings.IndexByte(name, '='); idx >= 0 {
				continue // -x=v：值已经在里面了
			}
			if boolFlags[name] {
				continue // bool 开关不吞下一个词
			}
			if i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positional = append(positional, arg)
	}
	return flags, positional
}

// sayf 打印普通信息。
func (a *app) sayf(format string, args ...any) {
	fmt.Fprintf(a.stdout, format+"\n", args...)
}

// warnf 打印醒目警告：终端上是黄色，重定向到文件/管道时保持纯文本。
func (a *app) warnf(format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	if isTerminal(a.stderr) {
		fmt.Fprintf(a.stderr, "\x1b[33m%s\x1b[0m\n", text)
		return
	}
	fmt.Fprintf(a.stderr, "%s\n", text)
}

// isTerminal 判断写出的目标是不是终端（只有 os.File 才可能是）。
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// run 是一次完整的一键流程。
func (a *app) run(ctx context.Context) error {
	if a.opts.fragSec <= 0 {
		return fmt.Errorf("-frag-sec 必须为正（当前 %v）", a.opts.fragSec)
	}
	if a.opts.pack < 1 {
		return fmt.Errorf("-pack 必须 >=1（1 = 不打包），当前 %d", a.opts.pack)
	}
	if strings.TrimSpace(a.opts.out) == "" {
		return errors.New("-out 不能为空")
	}

	// ---------- 1. 找输入文件 ----------
	in, err := a.resolveInput()
	if err != nil {
		return err
	}
	out := filepath.Clean(a.opts.out)
	a.sayf("输出: %s", out)

	// ---------- 2. 找 ffmpeg/ffprobe（没有就自动下载） ----------
	tools, sources, toolErr := a.resolveTools(ctx)
	if toolErr != nil {
		a.warnf("警告: %v", toolErr)
		a.warnf(manualInstallHint)
		if !tools.Available() {
			a.warnf("警告: 没有可用的 ffmpeg/ffprobe，只能直接按 moof 边界切分" +
				"（仅适用于已经是 fragmented MP4 的输入）。")
		}
	} else {
		a.printTools(tools, sources)
	}

	// ---------- 3. 探测并决定直通还是转码 ----------
	var (
		info *segment.MediaInfo
		dec  decision
	)
	if tools.Available() {
		info, err = segment.Probe(ctx, tools, in)
		if err != nil {
			// 显式指定了处理方式时，探测失败不该把人拦住（例如容器很奇怪但 ffmpeg 能读）。
			if a.opts.transcode != "" || a.opts.fragment {
				a.warnf("警告: 探测失败（%v），按你显式指定的方式继续。", err)
			} else {
				return fmt.Errorf("探测失败: %w\n（可以用 -ffmpeg-dir 指定另一个 ffprobe，"+
					"或用 -transcode/-fragment 跳过自动判定）", err)
			}
		} else {
			a.printProbe(info)
		}
	}
	dec = decide(info, a.opts.transcode, a.opts.fragment)
	a.printDecision(dec, info)

	// 需要过一遍 ffmpeg 却没有它：明确报错（并给出人工安装指引），
	// 而不是让共享流水线抛一句"服务器未安装 ffmpeg"这种对 CLI 用户没意义的话。
	if dec.mode != modeDirect && tools.FFmpeg == "" {
		return fmt.Errorf("这一步（%s）需要 ffmpeg，但没有找到可用的 ffmpeg。\n%s",
			modeName(dec.mode), manualInstallHint)
	}

	// ---------- 4. 切片 ----------
	processOpts := segment.ProcessOptions{
		Tools:          tools,
		SegmentSeconds: a.opts.fragSec,
		PackSize:       a.opts.pack,
		Info:           info,
		Logf:           a.sayf,
	}
	switch dec.mode {
	case modeRemux:
		// 直通：只重新封装，不重新编码。
		processOpts.ForceFragment = true
	case modeTranscode:
		if a.opts.transcode != "" {
			// 用户显式给了码率：与 cmd/segmenter -transcode 的历史行为逐字一致。
			processOpts.Transcode = a.opts.transcode
		} else {
			// 自动转码：走服务端同款"不是 H.264/AAC 就转成 libx264 veryfast crf 23 + aac 128k"。
			processOpts.Auto = true
		}
	}

	a.sayf("切片中（每片约 %.1f 秒）…", a.opts.fragSec)
	artifacts, err := segment.Process(ctx, in, out, processOpts)
	if err != nil {
		return explainProcessError(err, dec, info)
	}

	// ---------- 5. 摘要 + 容量提示 + 下一步 ----------
	a.printSummary(out, artifacts)
	return nil
}
