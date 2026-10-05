package main

import (
	"bytes"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// testEnv 是一个"外部世界都被换掉"的 app：
// 不碰真实 PATH、不真下载、不真等待、不读真实 stdin，因此每条分支都能在单测里跑。
type testEnv struct {
	app    *app
	stdout *bytes.Buffer
	stderr *bytes.Buffer
}

// newTestEnv 造一个测试用 app。opts 为 nil 时给一组能用的默认值。
func newTestEnv(t *testing.T, opts *options) *testEnv {
	t.Helper()

	if opts == nil {
		opts = &options{}
	}
	if opts.out == "" {
		opts.out = "./room-media"
	}
	if opts.fragSec == 0 {
		opts.fragSec = 2
	}
	if opts.pack == 0 {
		opts.pack = 1
	}
	if opts.uplinkMbps == 0 {
		opts.uplinkMbps = 12
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	env := &testEnv{stdout: stdout, stderr: stderr}
	env.app = &app{
		opts:   opts,
		stdin:  strings.NewReader(""),
		stdout: stdout,
		stderr: stderr,
		exeDir: t.TempDir(),
		// 常见目录默认指向临时目录：绝不去碰真实的桌面/下载目录。
		homeDir:    t.TempDir(),
		cwd:        t.TempDir(),
		sleep:      func(time.Duration) {},
		httpClient: http.DefaultClient,
		// PATH 里"什么都没有"：否则本机装了 ffmpeg 的用例就不是在测这条分支了。
		lookPath: func(string) (string, error) { return "", exec.ErrNotFound },
		goos:     runtime.GOOS,
	}
	return env
}

// out 是到目前为止的 stdout 文本。
func (e *testEnv) out() string { return e.stdout.String() }

// errOut 是到目前为止的 stderr 文本。
func (e *testEnv) errOut() string { return e.stderr.String() }
