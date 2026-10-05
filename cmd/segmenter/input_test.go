package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestSplitArgs 锁定"拖拽 + 参数"这个必须支持的形状：
// Go 的 flag 包遇到第一个位置参数就停止解析，所以必须先把标志与位置参数拆开。
func TestSplitArgs(t *testing.T) {
	cases := []struct {
		name           string
		args           []string
		wantFlags      []string
		wantPositional []string
	}{
		{
			name:           "拖拽文件 + 后面的参数",
			args:           []string{"movie.mkv", "-out", "cut"},
			wantFlags:      []string{"-out", "cut"},
			wantPositional: []string{"movie.mkv"},
		},
		{
			name:           "参数在前",
			args:           []string{"-in", "movie.mkv", "-out", "cut"},
			wantFlags:      []string{"-in", "movie.mkv", "-out", "cut"},
			wantPositional: nil,
		},
		{
			name:           "bool 开关不吞下一个词",
			args:           []string{"-fragment", "movie.mkv"},
			wantFlags:      []string{"-fragment"},
			wantPositional: []string{"movie.mkv"},
		},
		{
			name:           "-x=v 是一个完整标志",
			args:           []string{"-pack=1", "movie.mkv"},
			wantFlags:      []string{"-pack=1"},
			wantPositional: []string{"movie.mkv"},
		},
		{
			name:           "-- 之后一律是位置参数",
			args:           []string{"-out", "cut", "--", "-weird-name.mkv"},
			wantFlags:      []string{"-out", "cut"},
			wantPositional: []string{"-weird-name.mkv"},
		},
		{
			name:           "单独的短横线是位置参数",
			args:           []string{"-"},
			wantFlags:      nil,
			wantPositional: []string{"-"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flags, positional := splitArgs(tc.args, boolFlagNames)
			if strings.Join(flags, " ") != strings.Join(tc.wantFlags, " ") {
				t.Fatalf("标志 = %v，应为 %v", flags, tc.wantFlags)
			}
			if strings.Join(positional, " ") != strings.Join(tc.wantPositional, " ") {
				t.Fatalf("位置参数 = %v，应为 %v", positional, tc.wantPositional)
			}
		})
	}
}

// TestParseOptionsFlagNames 锁定对外承诺的 flag 名字与默认值
// （-ffmpeg-dir 是正式名字，-ffmpeg 是必须保留的别名）。
func TestParseOptionsFlagNames(t *testing.T) {
	opts, err := parseOptions([]string{"movie.mkv", "-out", "cut"}, os.Stderr)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if opts.out != "cut" || len(opts.positional) != 1 || opts.positional[0] != "movie.mkv" {
		t.Fatalf("位置参数 + -out 没解析对: %+v", opts)
	}

	opts, err = parseOptions([]string{"-ffmpeg-dir", `D:\ffmpeg\bin`}, os.Stderr)
	if err != nil {
		t.Fatalf("解析 -ffmpeg-dir 失败: %v", err)
	}
	if opts.ffmpegDir != `D:\ffmpeg\bin` {
		t.Fatalf("-ffmpeg-dir 没生效: %q", opts.ffmpegDir)
	}

	// 别名 -ffmpeg 必须与 -ffmpeg-dir 等价（向后兼容）。
	opts, err = parseOptions([]string{"-ffmpeg", `D:\ffmpeg\bin`}, os.Stderr)
	if err != nil {
		t.Fatalf("解析 -ffmpeg 失败: %v", err)
	}
	if opts.ffmpegDir != `D:\ffmpeg\bin` {
		t.Fatalf("-ffmpeg 别名没生效: %q", opts.ffmpegDir)
	}

	opts, err = parseOptions(nil, os.Stderr)
	if err != nil {
		t.Fatalf("默认值解析失败: %v", err)
	}
	if opts.out != "./room-media" || opts.fragSec != 2 || opts.uplinkMbps != 12 || !opts.searchByName {
		t.Fatalf("默认值不对: %+v", opts)
	}
}

// TestWantName 锁定"想要的文件名"的来源：-name 优先，否则取 -out 的目录名（与一键脚本一致）。
func TestWantName(t *testing.T) {
	cases := []struct {
		name string
		opts options
		want string
	}{
		{"-name 优先", options{name: "movie.mkv", out: "./room-media"}, "movie.mkv"},
		{"从 -out 目录名推断", options{out: "./room-media"}, "room-media"},
		{"从带分隔符的 -out 推断", options{out: filepath.Join("D:", "videos", "my-video")}, "my-video"},
		{"-out 是当前目录时不推断", options{out: "."}, ""},
		{"-out 是根时不推断", options{out: string(filepath.Separator)}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.opts.wantName(); got != tc.want {
				t.Fatalf("wantName() = %q，应为 %q", got, tc.want)
			}
		})
	}
}

// TestSearchByNameInCommonDirs 用 t.TempDir() 造目录树验证"按文件名在常见目录里查找"：
// 必须是**第一个命中的目录**胜出，且只认文件不认目录。
func TestSearchByNameInCommonDirs(t *testing.T) {
	root := t.TempDir()
	empty := filepath.Join(root, "empty")
	first := filepath.Join(root, "a-first")
	second := filepath.Join(root, "b-second")
	sameNameDir := filepath.Join(root, "c-dir-with-same-name", "movie.mkv")
	for _, dir := range []string{empty, first, second, sameNameDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	want := filepath.Join(first, "movie.mkv")
	if err := os.WriteFile(want, []byte("not really a video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "movie.mkv"), []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}

	env := newTestEnv(t, &options{name: "movie.mkv", out: "./room-media"})
	env.app.searchDirs = []string{empty, first, second, filepath.Join(root, "c-dir-with-same-name")}

	if got := env.app.searchByNameInCommonDirs("movie.mkv"); got != want {
		t.Fatalf("应命中第一个目录里的文件 %s，实际 %q", want, got)
	}
	if got := env.app.searchByNameInCommonDirs("nothing-here.mkv"); got != "" {
		t.Fatalf("找不到时应返回空，实际 %q", got)
	}
	// 名字里带分隔符：按路径找，不做目录搜索。
	abs := filepath.Join(second, "movie.mkv")
	if got := env.app.searchByNameInCommonDirs(abs); got != abs {
		t.Fatalf("带分隔符的名字应按路径解析，期望 %q，实际 %q", abs, got)
	}
	if got := env.app.searchByNameInCommonDirs(filepath.Join(root, "nope.mkv")); got != "" {
		t.Fatalf("带分隔符且不存在时应返回空，实际 %q", got)
	}
}

// TestCommonDirsDefault 锁定常见目录的清单与顺序：
// 桌面、下载、视频、文档、当前目录、exe 所在目录。
func TestCommonDirsDefault(t *testing.T) {
	env := newTestEnv(t, nil)
	dirs := env.app.commonDirs()
	want := []string{
		filepath.Join(env.app.homeDir, "Desktop"),
		filepath.Join(env.app.homeDir, "Downloads"),
		filepath.Join(env.app.homeDir, "Videos"),
		filepath.Join(env.app.homeDir, "Documents"),
		env.app.cwd,
		env.app.exeDir,
	}
	if strings.Join(dirs, "|") != strings.Join(want, "|") {
		t.Fatalf("常见目录清单不对：\n得到 %v\n应为 %v", dirs, want)
	}

	// 重复项要去掉（cwd 与 exeDir 相同的情况）。
	env.app.cwd = env.app.exeDir
	if got := env.app.commonDirs(); len(got) != len(want)-1 {
		t.Fatalf("重复目录应被去掉，实际 %v", got)
	}
}

// TestResolveInputFromPositional 是"把视频拖到 exe 上"的形状：唯一的位置参数就是输入。
func TestResolveInputFromPositional(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	env := newTestEnv(t, &options{positional: []string{video}, out: filepath.Join(dir, "cut")})
	got, err := env.app.resolveInput()
	if err != nil {
		t.Fatalf("位置参数应当被当成输入: %v", err)
	}
	if got != video {
		t.Fatalf("输入 = %q，应为 %q", got, video)
	}
	if !strings.Contains(env.out(), "输入: ") {
		t.Fatalf("应当打印输入文件与体积，实际 %q", env.out())
	}
}

// TestResolveInputErrors 覆盖几种必须明确失败的形状。
func TestResolveInputErrors(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("-in 指向不存在的文件", func(t *testing.T) {
		env := newTestEnv(t, &options{in: filepath.Join(dir, "nope.mkv")})
		if _, err := env.app.resolveInput(); err == nil {
			t.Fatal("显式给了不存在的 -in 时必须报错，而不是静默去找别的文件")
		}
	})

	t.Run("-in 指向目录", func(t *testing.T) {
		env := newTestEnv(t, &options{in: dir})
		if _, err := env.app.resolveInput(); err == nil {
			t.Fatal("输入是目录时必须报错")
		}
	})

	t.Run("多个位置参数", func(t *testing.T) {
		env := newTestEnv(t, &options{positional: []string{video, video}})
		_, err := env.app.resolveInput()
		if err == nil {
			t.Fatal("多个位置参数必须报错")
		}
		if !strings.Contains(err.Error(), "只接受一个位置参数") {
			t.Fatalf("错误信息应说明只接受一个位置参数，实际 %q", err)
		}
	})

	t.Run("位置参数指向不存在的文件", func(t *testing.T) {
		env := newTestEnv(t, &options{positional: []string{filepath.Join(dir, "nope.mkv")}})
		if _, err := env.app.resolveInput(); err == nil {
			t.Fatal("位置参数指向不存在的文件时必须报错")
		}
	})
}

// TestResolveInputFromStdin 覆盖交互式询问：管道喂一个路径能读到，
// 空输入必须**报错退出而不是挂死**（这个用例如果挂死，测试会超时失败）。
func TestResolveInputFromStdin(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("管道喂路径", func(t *testing.T) {
		env := newTestEnv(t, &options{out: filepath.Join(dir, "cut"), searchByName: true})
		env.app.stdin = strings.NewReader(video + "\r\n") // 模拟 Windows 的 CRLF
		got, err := env.app.resolveInput()
		if err != nil {
			t.Fatalf("应当读到管道里的路径: %v", err)
		}
		if got != video {
			t.Fatalf("输入 = %q，应为 %q", got, video)
		}
		if !strings.Contains(env.out(), "视频文件完整路径: ") {
			t.Fatalf("应当打印交互提示，实际 %q", env.out())
		}
	})

	t.Run("带引号的路径", func(t *testing.T) {
		env := newTestEnv(t, &options{out: filepath.Join(dir, "cut")})
		env.app.stdin = strings.NewReader(`"` + video + `"` + "\n")
		got, err := env.app.resolveInput()
		if err != nil {
			t.Fatalf("带引号的路径应当能解析: %v", err)
		}
		if got != video {
			t.Fatalf("输入 = %q，应为 %q", got, video)
		}
	})

	t.Run("空输入报错而不是挂死", func(t *testing.T) {
		env := newTestEnv(t, &options{out: filepath.Join(dir, "cut")})
		env.app.stdin = strings.NewReader("") // 相当于 stdin 被关闭 / 是 NUL
		_, err := env.app.resolveInput()
		if err == nil {
			t.Fatal("stdin 为空时必须报错")
		}
		if !strings.Contains(err.Error(), "非交互环境") {
			t.Fatalf("错误信息应说明非交互环境怎么办，实际 %q", err)
		}
	})

	t.Run("只有换行也算空", func(t *testing.T) {
		env := newTestEnv(t, &options{out: filepath.Join(dir, "cut")})
		env.app.stdin = strings.NewReader("\n")
		if _, err := env.app.resolveInput(); err == nil {
			t.Fatal("只按回车不当成有效路径")
		}
	})
}

// TestResolveInputSearchesCommonDirs 把"按文件名查常见目录"接到 resolveInput 上：
// 命中就直接用，没命中才落到交互询问。
func TestResolveInputSearchesCommonDirs(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	env := newTestEnv(t, &options{name: "movie.mkv", out: filepath.Join(dir, "cut"), searchByName: true})
	env.app.searchDirs = []string{dir}
	got, err := env.app.resolveInput()
	if err != nil {
		t.Fatalf("应当在常见目录里找到文件: %v", err)
	}
	if got != video {
		t.Fatalf("输入 = %q，应为 %q", got, video)
	}
	if !strings.Contains(env.out(), "在常见目录里找到了") {
		t.Fatalf("应当提示「在常见目录里找到了」，实际 %q", env.out())
	}

	// 关掉按文件名查找：即使文件就在目录里也要去问用户。
	env = newTestEnv(t, &options{name: "movie.mkv", out: filepath.Join(dir, "cut"), searchByName: false})
	env.app.searchDirs = []string{dir}
	env.app.stdin = strings.NewReader(video + "\n")
	got, err = env.app.resolveInput()
	if err != nil {
		t.Fatalf("关掉查找后应走交互询问: %v", err)
	}
	if got != video {
		t.Fatalf("输入 = %q，应为 %q", got, video)
	}
	if !strings.Contains(env.errOut(), "search-by-name=false") {
		t.Fatalf("应当说明跳过了按文件名查找，实际 %q", env.errOut())
	}
}

// TestUnquote 覆盖用户从资源管理器粘贴路径时的引号。
func TestUnquote(t *testing.T) {
	cases := map[string]string{
		`"C:\video\movie.mkv"`: `C:\video\movie.mkv`,
		`'./movie.mkv'`:        `./movie.mkv`,
		`movie.mkv`:            `movie.mkv`,
		`  "movie.mkv"  `:      `movie.mkv`,
	}
	for in, want := range cases {
		if got := unquote(in); got != want {
			t.Fatalf("unquote(%q) = %q，应为 %q", in, got, want)
		}
	}
}

// TestExeSuffix 只影响查找用的文件名，平台对了才不会"在 Windows 上找 ffmpeg"。
func TestExeSuffix(t *testing.T) {
	env := newTestEnv(t, nil)
	env.app.goos = "windows"
	if got := env.app.exeSuffix(); got != ".exe" {
		t.Fatalf("windows 后缀 = %q", got)
	}
	env.app.goos = "linux"
	if got := env.app.exeSuffix(); got != "" {
		t.Fatalf("linux 后缀 = %q", got)
	}
	if runtime.GOOS == "" {
		t.Fatal("runtime.GOOS 不该为空")
	}
}
