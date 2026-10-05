package segment

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/model"
)

// ---------- 测试替身 ----------

// fakeClock 是可注入的假时钟：令牌桶的"每分钟 3 个"因此不用真等一分钟。
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fakeProcessor 是可注入的流水线：可以在 Process 里阻塞，用来观察并发与排队。
type fakeProcessor struct {
	started chan string
	release chan struct{}

	mu       sync.Mutex
	calls    int
	failWith error
	// live/max 记录同时处于 Process 内的作业数，用于断言并发上限。
	live int32
	max  int32
	// extraBytes 用来把假产物撑大，便于验证 manifest 分批。
	extraBytes int
}

func (p *fakeProcessor) Process(_ context.Context, _, outDir string, _ ProcessOptions) (*Artifacts, error) {
	current := atomic.AddInt32(&p.live, 1)
	for {
		observed := atomic.LoadInt32(&p.max)
		if current <= observed || atomic.CompareAndSwapInt32(&p.max, observed, current) {
			break
		}
	}
	defer atomic.AddInt32(&p.live, -1)

	if p.started != nil {
		p.started <- outDir
	}
	if p.release != nil {
		<-p.release
	}

	p.mu.Lock()
	p.calls++
	failure := p.failWith
	extra := p.extraBytes
	p.mu.Unlock()

	if failure != nil {
		return nil, failure
	}
	return writeFakeArtifacts(outDir, extra)
}

func (p *fakeProcessor) maxConcurrent() int { return int(atomic.LoadInt32(&p.max)) }
func (p *fakeProcessor) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// writeFakeArtifacts 造一份自洽的假产物（不依赖 ffmpeg）：
// index.json + init.mp4 + 两个分片，内容固定，便于校验 zip 与 sha256。
func writeFakeArtifacts(outDir string, extraBytes int) (*Artifacts, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}

	index := &model.Index{
		Version:       1,
		InitFile:      InitFileName,
		MimeType:      `video/mp4; codecs="avc1.64001f"`,
		TotalDuration: 4,
		SegmentSec:    2,
		BitrateBps:    8000,
		TotalBytes:    64,
		Segments: []model.Segment{
			{Index: 1, File: "c00001.m4s", Size: 32, Duration: 2, StartPTS: 0, Keyframe: true},
			{Index: 2, File: "c00002.m4s", Size: 32, Duration: 2, StartPTS: 2, Keyframe: true},
		},
	}

	files := map[string][]byte{
		IndexFileName: []byte(`{"version":1,"initFile":"init.mp4"}`),
		InitFileName:  bytes.Repeat([]byte{0xA1}, 48),
		"c00001.m4s":  bytes.Repeat([]byte{0xB2}, 32+extraBytes),
		"c00002.m4s":  bytes.Repeat([]byte{0xC3}, 32+extraBytes),
	}
	for name, payload := range files {
		if err := os.WriteFile(filepath.Join(outDir, name), payload, 0o644); err != nil {
			return nil, err
		}
	}

	return CollectArtifacts(outDir, index)
}

// ---------- 辅助 ----------

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Segment.TempDir = t.TempDir()
	// 单测直接调 Cleanup()，后台周期设得很长，避免协程随机插手。
	cfg.Segment.CleanupInterval = time.Hour
	return cfg
}

func defaultProber(durationSec float64) Prober {
	return func(context.Context, Tools, string) (*MediaInfo, error) {
		return &MediaInfo{
			DurationSec: durationSec,
			HasVideo:    true,
			VideoCodec:  "h264",
			HasAudio:    true,
			AudioCodec:  "aac",
		}, nil
	}
}

type queueFixture struct {
	q        *Queue
	clock    *fakeClock
	proc     *fakeProcessor
	toolsSet bool
}

func newTestQueue(t *testing.T, cfg *config.Config, clock *fakeClock, prober Prober, proc Processor, tools *Tools) *Queue {
	t.Helper()

	now := time.Now
	if clock != nil {
		now = clock.Now
	}
	if prober == nil {
		prober = defaultProber(10)
	}
	if proc == nil {
		proc = &fakeProcessor{}
	}
	resolved := Tools{FFmpeg: "ffmpeg", FFprobe: "ffprobe"}
	if tools != nil {
		resolved = *tools
	}

	q, err := newQueue(cfg, options{now: now, prober: prober, processor: proc, tools: &resolved})
	if err != nil {
		t.Fatalf("构造队列失败: %v", err)
	}
	// 清理顺序：先等在跑的假作业结束（否则它会和 t.TempDir 的 RemoveAll 抢目录），再关队列。
	// t.Cleanup 是后进先出，因此这段一定早于 testConfig 里 t.TempDir 的清理。
	t.Cleanup(func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if q.Stats().Running == 0 {
				break
			}
			time.Sleep(2 * time.Millisecond)
		}
		q.Close()
	})
	return q
}

func submitBytes(t *testing.T, q *Queue, payload []byte) (*View, error) {
	t.Helper()
	return q.Submit("movie.mp4", bytes.NewReader(payload), int64(len(payload)))
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", what)
}

// ---------- 并发上限 ----------

func TestConcurrencyLimitIsTwo(t *testing.T) {
	cfg := testConfig(t)
	cfg.Segment.Concurrency = 2
	cfg.Segment.QueueLength = 8
	cfg.Segment.RatePerMinute = 600
	cfg.Segment.Burst = 100

	proc := &fakeProcessor{started: make(chan string, 16), release: make(chan struct{}, 16)}
	q := newTestQueue(t, cfg, newFakeClock(), nil, proc, nil)

	for i := 0; i < 4; i++ {
		if _, err := submitBytes(t, q, []byte("payload")); err != nil {
			t.Fatalf("第 %d 次提交不应失败: %v", i+1, err)
		}
	}

	// 只有 2 个能进入 running。
	for i := 0; i < 2; i++ {
		select {
		case <-proc.started:
		case <-time.After(2 * time.Second):
			t.Fatal("应有 2 个作业进入 running")
		}
	}
	if got := q.Stats().Running; got != 2 {
		t.Fatalf("running 应为 2，实际 %d", got)
	}
	if got := q.Stats().Queued; got != 2 {
		t.Fatalf("queued 应为 2，实际 %d", got)
	}

	// 第 3 个不能抢跑。
	select {
	case <-proc.started:
		t.Fatal("并发上限被突破：第 3 个作业也开始了")
	case <-time.After(150 * time.Millisecond):
	}

	// 放行：并发位空出来后，剩下的作业才被调度。
	proc.release <- struct{}{}
	proc.release <- struct{}{}
	waitFor(t, "剩余作业被调度", 2*time.Second, func() bool { return proc.callCount() >= 2 })
	proc.release <- struct{}{}
	proc.release <- struct{}{}

	waitFor(t, "全部作业结束", 5*time.Second, func() bool {
		stats := q.Stats()
		return stats.Running == 0 && stats.Queued == 0
	})

	if got := proc.maxConcurrent(); got != 2 {
		t.Fatalf("同时运行数峰值应为 2，实际 %d", got)
	}
	if got := proc.callCount(); got != 4 {
		t.Fatalf("应有 4 个作业跑完，实际 %d", got)
	}
}

// ---------- 队列长度 ----------

func TestQueueLengthRejectsExtraRequests(t *testing.T) {
	cfg := testConfig(t)
	cfg.Segment.Concurrency = 1
	cfg.Segment.QueueLength = 8
	cfg.Segment.RatePerMinute = 600
	cfg.Segment.Burst = 100

	proc := &fakeProcessor{started: make(chan string, 16), release: make(chan struct{}, 16)}
	q := newTestQueue(t, cfg, newFakeClock(), nil, proc, nil)

	// 第 1 个占住唯一的并发位，其后 8 个排队（队列长度按"排队数"算，不含 running）。
	for i := 0; i < 9; i++ {
		if _, err := submitBytes(t, q, []byte("payload")); err != nil {
			t.Fatalf("第 %d 个请求不应被拒: %v", i+1, err)
		}
	}
	<-proc.started

	stats := q.Stats()
	if stats.Running != 1 || stats.Queued != 8 {
		t.Fatalf("应为 1 running + 8 queued，实际 %+v", stats)
	}

	_, err := submitBytes(t, q, []byte("payload"))
	var quota *QuotaError
	if !errors.As(err, &quota) {
		t.Fatalf("队列满时应返回 *QuotaError，实际 %v", err)
	}
	if !errors.Is(quota.Err, ErrQueueFull) {
		t.Fatalf("错误语义应为 ErrQueueFull，实际 %v", quota.Err)
	}
	if quota.RetryAfter <= 0 {
		t.Fatalf("队列满应给出正的重试间隔，实际 %v", quota.RetryAfter)
	}

	// 被拒的请求不应留下任何作业记录或临时目录。
	if got := q.Stats().Jobs; got != 9 {
		t.Fatalf("被拒请求不应创建作业，实际作业数 %d", got)
	}

	// 收尾：把所有被卡住的作业一次性放行，避免测试结束时还有作业在写目录。
	for i := 0; i < 16; i++ {
		proc.release <- struct{}{}
	}
	waitFor(t, "队列清空", 5*time.Second, func() bool {
		stats := q.Stats()
		return stats.Running == 0 && stats.Queued == 0
	})
}

// ---------- 令牌桶 ----------

func TestTokenBucketAllowsThreePerMinute(t *testing.T) {
	cfg := testConfig(t)
	cfg.Segment.Concurrency = 8
	cfg.Segment.QueueLength = 64
	cfg.Segment.RatePerMinute = 3
	cfg.Segment.Burst = 3

	clock := newFakeClock()
	q := newTestQueue(t, cfg, clock, nil, &fakeProcessor{}, nil)

	for i := 0; i < 3; i++ {
		if _, err := submitBytes(t, q, []byte("payload")); err != nil {
			t.Fatalf("桶里有 3 个令牌，第 %d 个请求不应被拒: %v", i+1, err)
		}
	}

	_, err := submitBytes(t, q, []byte("payload"))
	var quota *QuotaError
	if !errors.As(err, &quota) || !errors.Is(quota.Err, ErrRateLimited) {
		t.Fatalf("第 4 个请求应被限流，实际 %v", err)
	}
	// 速率 3/min = 每 20s 补 1 个令牌。
	if quota.RetryAfter != 20*time.Second {
		t.Fatalf("Retry-After 应为 20s，实际 %v", quota.RetryAfter)
	}

	// 注入假时钟前进 21 秒（不 sleep），应当又能提交一个。
	clock.Advance(21 * time.Second)
	if _, err := submitBytes(t, q, []byte("payload")); err != nil {
		t.Fatalf("过了 21 秒应补回 1 个令牌: %v", err)
	}
	if _, err := submitBytes(t, q, []byte("payload")); err == nil {
		t.Fatal("只补回 1 个令牌，紧接着的第 2 个请求应被限流")
	}

	// 再前进 1 分钟：桶补满到容量 3。
	clock.Advance(time.Minute)
	for i := 0; i < 3; i++ {
		if _, err := submitBytes(t, q, []byte("payload")); err != nil {
			t.Fatalf("桶补满后第 %d 个请求不应被拒: %v", i+1, err)
		}
	}
	if _, err := submitBytes(t, q, []byte("payload")); err == nil {
		t.Fatal("桶容量为 3，第 4 个请求应被限流")
	}
}

// ---------- 前置校验 ----------

func TestDurationLimitRejectsLongVideo(t *testing.T) {
	cfg := testConfig(t)
	cfg.Segment.MaxDuration = 60 * time.Minute

	q := newTestQueue(t, cfg, nil, defaultProber(61*60), &fakeProcessor{}, nil)

	_, err := submitBytes(t, q, []byte("payload"))
	if !errors.Is(err, ErrDurationTooLong) {
		t.Fatalf("超过时长上限应返回 ErrDurationTooLong，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "61.0 分钟") {
		t.Fatalf("错误信息应说明实际时长与上限，实际 %v", err)
	}
	// 校验失败必须不留下作业记录与临时目录（否则用户改了再传就没配额了）。
	if got := q.Stats().Jobs; got != 0 {
		t.Fatalf("被拒的作业不应留下记录，实际 %d", got)
	}
	assertNoJobDirs(t, cfg.Segment.TempDir)
}

func TestSizeLimitRejectsLargeUpload(t *testing.T) {
	cfg := testConfig(t)
	cfg.Segment.MaxSourceBytes = 1024

	q := newTestQueue(t, cfg, nil, nil, &fakeProcessor{}, nil)

	// 声明长度就超限：连读都不读（Content-Length 已知的常见路径）。
	_, err := q.Submit("big.mp4", bytes.NewReader(make([]byte, 4096)), 4096)
	if !errors.Is(err, ErrSourceTooLarge) {
		t.Fatalf("声明长度超限应返回 ErrSourceTooLarge，实际 %v", err)
	}

	// 长度未知（分块传输）：边写边判，超过 1 字节就停。
	_, err = q.Submit("big.mp4", bytes.NewReader(make([]byte, 4096)), -1)
	if !errors.Is(err, ErrSourceTooLarge) {
		t.Fatalf("流式写入超限应返回 ErrSourceTooLarge，实际 %v", err)
	}

	// 两次被拒都不应留下残留（作业目录在拒绝时就被删掉）。
	assertNoJobDirs(t, cfg.Segment.TempDir)

	// 正好等于上限：放行。
	if _, err := q.Submit("ok.mp4", bytes.NewReader(make([]byte, 1024)), -1); err != nil {
		t.Fatalf("等于上限的文件应被接受: %v", err)
	}
}

func TestEmptyUploadRejected(t *testing.T) {
	cfg := testConfig(t)
	q := newTestQueue(t, cfg, nil, nil, &fakeProcessor{}, nil)

	if _, err := q.Submit("empty.mp4", bytes.NewReader(nil), 1); !errors.Is(err, ErrSourceEmpty) {
		t.Fatalf("空文件应返回 ErrSourceEmpty，实际 %v", err)
	}
	if got := q.Stats().Jobs; got != 0 {
		t.Fatalf("空文件不应留下作业，实际 %d", got)
	}
}

func TestProbeFailureRejected(t *testing.T) {
	cfg := testConfig(t)
	prober := func(context.Context, Tools, string) (*MediaInfo, error) {
		return nil, ErrProbeFailed
	}
	q := newTestQueue(t, cfg, nil, prober, &fakeProcessor{}, nil)

	if _, err := submitBytes(t, q, []byte("payload")); !errors.Is(err, ErrProbeFailed) {
		t.Fatalf("探测失败应返回 ErrProbeFailed，实际 %v", err)
	}
	if got := q.Stats().Jobs; got != 0 {
		t.Fatalf("探测失败的作业不应留下记录，实际 %d", got)
	}
}

// ---------- ffmpeg 缺失 ----------

func TestMissingFFmpegFailsJobImmediately(t *testing.T) {
	cfg := testConfig(t)
	tools := Tools{} // 模拟"服务器没装 ffmpeg"
	q := newTestQueue(t, cfg, nil, nil, &fakeProcessor{}, &tools)

	view, err := submitBytes(t, q, []byte("payload"))
	if err != nil {
		t.Fatalf("缺 ffmpeg 时请求仍应被接受（202 + 立即失败），实际 %v", err)
	}
	if view.State != StateFailed {
		t.Fatalf("作业应立刻 failed，实际 %q", view.State)
	}
	if !strings.Contains(view.Error, "docs/SEGMENT.md") {
		t.Fatalf("失败原因必须给出本地切片的指引，实际 %q", view.Error)
	}
	if got := q.Stats().Queued; got != 0 {
		t.Fatalf("立即失败的作业不应占排队位，实际 %d", got)
	}
}

// ---------- 失败作业 ----------

func TestProcessorFailureMarksJobFailedAndCleansUp(t *testing.T) {
	cfg := testConfig(t)
	proc := &fakeProcessor{failWith: errors.New("ffmpeg 执行失败: 模拟")}
	q := newTestQueue(t, cfg, nil, nil, proc, nil)

	view, err := submitBytes(t, q, []byte("payload"))
	if err != nil {
		t.Fatalf("提交不应失败: %v", err)
	}

	waitFor(t, "作业进入 failed", 3*time.Second, func() bool {
		current, ok := q.Get(view.JobID)
		return ok && current.State == StateFailed
	})

	current, _ := q.Get(view.JobID)
	if !strings.Contains(current.Error, "模拟") {
		t.Fatalf("失败原因应原样保留，实际 %q", current.Error)
	}
	assertNoJobDirs(t, cfg.Segment.TempDir)
}

// ---------- 成功作业与源文件清理 ----------

func TestJobCompletesAndDeletesSource(t *testing.T) {
	cfg := testConfig(t)
	q := newTestQueue(t, cfg, nil, nil, &fakeProcessor{}, nil)

	view, err := submitBytes(t, q, []byte("uploaded-video-bytes"))
	if err != nil {
		t.Fatalf("提交不应失败: %v", err)
	}

	waitFor(t, "作业完成", 3*time.Second, func() bool {
		current, ok := q.Get(view.JobID)
		return ok && current.State == StateDone
	})

	done, _ := q.Get(view.JobID)
	if done.Result == nil {
		t.Fatal("完成的作业必须有 result")
	}
	if !done.Result.SingleResponse {
		t.Fatalf("小产物应走单次返回，实际 %+v", done.Result)
	}
	if done.Result.Segments != 2 {
		t.Fatalf("分片数应为 2，实际 %d", done.Result.Segments)
	}
	// 文件数与分片数是两个概念，必须分开命名：这里的假产物是逐片一个文件，
	// 所以 2 片 + index.json + init.mp4 = 4 个文件。
	if done.Result.Files != 4 {
		t.Fatalf("产物文件数应为 4，实际 %d", done.Result.Files)
	}

	// 源文件必须已删除，产物必须还在。
	entries := readDirNames(t, cfg.Segment.TempDir)
	if len(entries) != 1 {
		t.Fatalf("应只剩 1 个作业目录，实际 %v", entries)
	}
	jobDir := filepath.Join(cfg.Segment.TempDir, entries[0])
	if matches := globNames(t, jobDir, "source.*"); len(matches) != 0 {
		t.Fatalf("源文件应在作业完成后删除，实际残留 %v", matches)
	}
	if matches := globNames(t, jobDir, "out/*"); len(matches) == 0 {
		t.Fatal("产物目录不应被删除")
	}
}

// ---------- manifest 分批 ----------

func TestLargeResultGoesThroughManifest(t *testing.T) {
	cfg := testConfig(t)
	// 用一个很小、可注入的上限来验证"超过上限走 manifest"，不必真造 1GB 文件。
	// 假产物约 1945 字节（33+48+2×(32+900)），上限 1500 → 必然分批。
	cfg.Segment.SingleResponseMaxBytes = 1500

	proc := &fakeProcessor{extraBytes: 900}
	q := newTestQueue(t, cfg, nil, nil, proc, nil)

	view, err := submitBytes(t, q, []byte("payload"))
	if err != nil {
		t.Fatalf("提交不应失败: %v", err)
	}
	waitFor(t, "作业完成", 3*time.Second, func() bool {
		current, ok := q.Get(view.JobID)
		return ok && current.State == StateDone
	})

	done, _ := q.Get(view.JobID)
	result := done.Result
	if result.SingleResponse {
		t.Fatalf("产物超过上限时不该走单次返回: %+v", result)
	}
	if len(result.Parts) < 2 {
		t.Fatalf("应切成至少 2 份，实际 %d", len(result.Parts))
	}

	var total int64
	for i, part := range result.Parts {
		if part.N != i+1 {
			t.Fatalf("分批编号应连续，第 %d 份是 %d", i+1, part.N)
		}
		if part.Bytes >= cfg.Segment.SingleResponseMaxBytes {
			t.Fatalf("第 %d 份 %d 字节不应达到单份上限 %d", part.N, part.Bytes, cfg.Segment.SingleResponseMaxBytes)
		}
		if len(part.SHA256) != 64 {
			t.Fatalf("第 %d 份缺少 sha256: %q", part.N, part.SHA256)
		}
		total += part.Bytes
	}
	if total <= result.Bytes {
		t.Fatalf("各份 zip 之和应大于产物原始字节数（zip 有头开销），实际 %d <= %d", total, result.Bytes)
	}

	// 单次返回路径必须明确拒绝：这个作业只能走分批。
	if _, _, err := q.Artifacts(view.JobID); !errors.Is(err, ErrNoParts) {
		t.Fatalf("分批作业的 Artifacts 应返回 ErrNoParts，实际 %v", err)
	}
	if _, _, _, err := q.PartFiles(view.JobID, 99); !errors.Is(err, ErrPartNotFound) {
		t.Fatalf("越界分批编号应返回 ErrPartNotFound，实际 %v", err)
	}
}

// ---------- 未知作业 ----------

func TestUnknownJobLookups(t *testing.T) {
	cfg := testConfig(t)
	q := newTestQueue(t, cfg, nil, nil, &fakeProcessor{}, nil)

	if _, ok := q.Get("NOPE"); ok {
		t.Fatal("未知作业不应可查询")
	}
	if _, _, _, err := q.PartFiles("NOPE", 1); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("未知作业应返回 ErrJobNotFound，实际 %v", err)
	}
}

// ---------- TTL 清理 ----------

func TestTTLCleanupRemovesJobAndArtifacts(t *testing.T) {
	cfg := testConfig(t)
	cfg.Segment.TTL = 30 * time.Minute

	clock := newFakeClock()
	var mu sync.Mutex
	dirs := map[string]string{} // jobID → 产物目录
	proc := processorFunc(func(_ context.Context, _ string, outDir string, _ ProcessOptions) (*Artifacts, error) {
		mu.Lock()
		defer mu.Unlock()
		return writeFakeArtifacts(outDir, 0)
	})
	q := newTestQueue(t, cfg, clock, nil, proc, nil)

	view, err := submitBytes(t, q, []byte("payload"))
	if err != nil {
		t.Fatalf("提交不应失败: %v", err)
	}
	waitFor(t, "作业完成", 3*time.Second, func() bool {
		current, ok := q.Get(view.JobID)
		return ok && current.State == StateDone
	})

	entries := readDirNames(t, cfg.Segment.TempDir)
	if len(entries) != 1 {
		t.Fatalf("应有 1 个作业目录，实际 %v", entries)
	}
	mu.Lock()
	dirs[view.JobID] = filepath.Join(cfg.Segment.TempDir, entries[0])
	mu.Unlock()

	// 还没到 TTL：清理不应动它。
	clock.Advance(29 * time.Minute)
	q.Cleanup()
	if _, ok := q.Get(view.JobID); !ok {
		t.Fatal("未到 TTL 的作业不应被清理")
	}

	// 过了 TTL：作业记录与产物一起消失。
	clock.Advance(2 * time.Minute)
	q.Cleanup()
	if _, ok := q.Get(view.JobID); ok {
		t.Fatal("过了 TTL 的作业应被清理")
	}
	if _, err := os.Stat(dirs[view.JobID]); !os.IsNotExist(err) {
		t.Fatalf("产物目录应被删除，实际 err=%v", err)
	}
}

func TestSweepOrphansRemovesStaleDirs(t *testing.T) {
	cfg := testConfig(t)
	cfg.Segment.TTL = 10 * time.Minute

	orphan := filepath.Join(cfg.Segment.TempDir, "job-ORPHAN000001")
	if err := os.MkdirAll(filepath.Join(orphan, "out"), 0o755); err != nil {
		t.Fatalf("造孤儿目录失败: %v", err)
	}
	// 把修改时间推到 TTL 之前（模拟上一次进程被强杀留下的残骸）。
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(orphan, old, old); err != nil {
		t.Fatalf("调整目录时间失败: %v", err)
	}

	// 这里用真实时钟：孤儿判据是"目录修改时间早于 now-TTL"，假时钟停在过去会算不出正差。
	q := newTestQueue(t, cfg, nil, nil, &fakeProcessor{}, nil)
	q.Cleanup()

	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("陈旧孤儿目录应被清理，实际 err=%v", err)
	}
}

// ---------- 小工具 ----------

func assertNoJobDirs(t *testing.T, root string) {
	t.Helper()
	if names := readDirNames(t, root); len(names) != 0 {
		t.Fatalf("不应残留作业目录: %v", names)
	}
}

func readDirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("读取目录 %s 失败: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func globNames(t *testing.T, dir, pattern string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		t.Fatalf("通配匹配失败: %v", err)
	}
	return matches
}
