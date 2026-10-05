// Command stubtools 是测试用的**假 ffmpeg/ffprobe**。
//
// 存在的理由：cmd/segmenter 最关键的路径是"本机没装 ffmpeg → 自动下载 → 解压 → 找到工具 →
// 继续切片"。真去下 100 MB 的官方包既慢又不稳定，所以用这个小程序顶上：
//
//   - 文件名里含 ffprobe 时：打印一份固定的探测 JSON（形状与
//     `ffprobe -print_format json -show_format -show_streams` 一致），让自动判定走"直通"分支；
//   - 其它情况（被当成 ffmpeg）：把内嵌的 fragmented MP4 写到最后那个参数（输出路径）上，
//     这样真正的切片器（internal/service/mp4）能照常切出 init.mp4 + 分片 + index.json；
//   - 环境变量 PR_STUB_LOG 非空时，把"我是谁 + 收到的参数"追加进去 ——
//     这是"找到了工具并把它作为 ffmpeg 路径传下去"的取证。
//
// 只用于测试：它在 testdata/ 下，不会被 go build ./... 编译。
package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// passthroughFixture 是一份真实的 fragmented MP4（H.264 + AAC，6 秒，每 2 秒一个 moof），
// 由真 ffmpeg 生成一次后随测试保留（生成命令见 README/测试注释）。
//
//go:embed passthrough_fixture.mp4
var passthroughFixture []byte

func main() {
	if strings.Contains(strings.ToLower(filepath.Base(os.Args[0])), "ffprobe") {
		ffprobe()
		return
	}
	ffmpeg()
}

// ffprobe 打印探测 JSON。
func ffprobe() {
	fmt.Print(`{"streams":[` +
		`{"codec_name":"h264","codec_type":"video"},` +
		`{"codec_name":"aac","codec_type":"audio"}],` +
		`"format":{"duration":"6.000000","size":"65536"}}`)
}

// ffmpeg 把内嵌的分片 MP4 写到最后一个参数（输出路径）。
func ffmpeg() {
	logArgs()

	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "stub ffmpeg: 没有输出路径参数")
		os.Exit(1)
	}
	out := os.Args[len(os.Args)-1]
	if err := os.WriteFile(out, passthroughFixture, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "stub ffmpeg: 写入 %s 失败: %v\n", out, err)
		os.Exit(1)
	}
}

// logArgs 把收到的参数追加到 PR_STUB_LOG 指向的文件里（供测试/人工核对）。
func logArgs() {
	path := strings.TrimSpace(os.Getenv("PR_STUB_LOG"))
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", os.Args[0], strings.Join(os.Args[1:], " "))
}
