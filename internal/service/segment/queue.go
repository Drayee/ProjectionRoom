package segment

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/utils"
)

// State 是作业状态机的状态：queued → running → done | failed。
type State string

const (
	StateQueued  State = "queued"
	StateRunning State = "running"
	StateDone    State = "done"
	StateFailed  State = "failed"
)

// jobIDLength 是作业 ID 的长度。用去掉了 I/O/0/1 的字母表，
// 因为它会出现在 URL 里（手抄、贴给别人时不容易出错）。
const jobIDLength = 12

// queueFullRetryAfter 是队列满时的建议重试间隔。
// 单个作业耗时无法预知（几秒到几分钟），因此只给一个"稍后再试"的节奏值。
const queueFullRetryAfter = 5 * time.Second

// View 是作业在 HTTP API 上的投影。
type View struct {
	JobID string `json:"jobId"`
	State State  `json:"state"`
	// Progress 是 0–1 的完成度。
	Progress float64 `json:"progress"`
	// QueuePosition 是排队位置（1 起）；running/done/failed 时为 0（省略）。
	QueuePosition int     `json:"queuePosition,omitempty"`
	Error         string  `json:"error,omitempty"`
	Result        *Result `json:"result,omitempty"`
}

// Stats 是队列当前状态的快照（监控与测试用）。
type Stats struct {
	// Running 是正在执行的作业数（不超过 Concurrency）。
	Running int
	// Queued 是排队中的作业数（不超过 QueueLength）。
	Queued int
	// Jobs 是仍在 TTL 内、可查询的作业总数。
	Jobs int
}

// Job 是一个切片作业。字段全部私有：外部只能通过 Queue 的读方法拿到 View，
// 这样状态迁移只可能发生在 Queue 内部，不会出现"外部把 running 改成 done"。
type Job struct {
	mu sync.Mutex

	id          string
	state       State
	progress    float64
	errMsg      string
	createdAt   time.Time
	startedAt   time.Time
	finishedAt  time.Time
	dir         string
	sourcePath  string
	sourceBytes int64
	info        *MediaInfo
	artifacts   *Artifacts
	layout      [][]ArtifactFile
	result      *Result
	// ready 表示源文件已落盘且前置校验已通过，可以由调度器领取。
	// 上传与探测发生在 submit 阶段，期间作业占着排队位置但不可被领取。
	ready bool
}

// ID 返回作业 ID。
func (j *Job) ID() string { return j.id }

// Snapshot 返回作业的只读快照（不含 QueuePosition，那需要队列视角）。
func (j *Job) Snapshot() View {
	j.mu.Lock()
	defer j.mu.Unlock()
	return View{
		JobID:    j.id,
		State:    j.state,
		Progress: j.progress,
		Error:    j.errMsg,
		Result:   j.result,
	}
}

func (j *Job) isReady() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.ready
}

func (j *Job) markReady() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.ready = true
}

func (j *Job) markRunning(now time.Time) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.state = StateRunning
	j.startedAt = now
	j.progress = 0
}

func (j *Job) setProgress(p float64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != StateRunning {
		return
	}
	j.progress = clamp01(p)
}

func (j *Job) markDone(artifacts *Artifacts, result *Result, layout [][]ArtifactFile, now time.Time) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.state = StateDone
	j.progress = 1
	j.artifacts = artifacts
	j.result = result
	j.layout = layout
	j.finishedAt = now
}

func (j *Job) markFailed(err error, now time.Time) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.state = StateFailed
	j.errMsg = err.Error()
	j.finishedAt = now
}

// expired 报告作业是否已过保留期。还没有结束的作业按创建时间算。
func (j *Job) expired(now time.Time, ttl time.Duration) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	ref := j.createdAt
	if !j.finishedAt.IsZero() {
		ref = j.finishedAt
	}
	return now.Sub(ref) >= ttl
}

func (j *Job) currentInfo() *MediaInfo {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.info
}

// Queue 是切片作业队列：并发 2、排队 8、令牌桶 3 作业/分钟（全部来自 config.SegmentConfig）。
//
// 它同时承担三件事：
//  1. 配额闸门（队列长度 + 令牌桶）——超限直接拒绝，不排队等；
//  2. 作业状态机与临时目录生命周期；
//  3. TTL 清理（产物与作业记录默认保留 30 分钟）。
type Queue struct {
	cfg     config.SegmentConfig
	root    string
	ctx     context.Context
	stopCtx func()

	now       func() time.Time
	probe     Prober
	process   Processor
	tools     Tools
	newID     func() (string, error)
	startOnce sync.Once
	closeOnce sync.Once
	stop      chan struct{}
	wg        sync.WaitGroup

	mu         sync.Mutex
	jobs       map[string]*Job
	order      []*Job
	running    int
	tokens     float64
	lastRefill time.Time
}

// options 是内部构造参数。它刻意不暴露成公开的 Option：
// wire 只应该看到 NewQueue(cfg)，注入点只留给同包单测。
type options struct {
	now       func() time.Time
	prober    Prober
	processor Processor
	tools     *Tools
	idGen     func() (string, error)
}

// NewQueue 构造切片队列并启动后台清理协程。
// 返回的清理函数与 service.NewHub 的用法一致，交给 wire 聚合。
func NewQueue(cfg *config.Config) (*Queue, func(), error) {
	q, err := newQueue(cfg, options{})
	if err != nil {
		return nil, nil, err
	}
	q.Start()
	return q, q.Close, nil
}

func newQueue(cfg *config.Config, o options) (*Queue, error) {
	if cfg == nil {
		return nil, errors.New("segment: 需要非空配置")
	}
	sc := cfg.Segment
	if sc.Concurrency < 1 {
		return nil, fmt.Errorf("segment: 并发数必须 >=1（%d）", sc.Concurrency)
	}
	if sc.QueueLength < 0 {
		return nil, fmt.Errorf("segment: 排队队列长度不能为负（%d）", sc.QueueLength)
	}
	if sc.RatePerMinute < 1 {
		return nil, fmt.Errorf("segment: 令牌桶速率必须 >=1/min（%d）", sc.RatePerMinute)
	}
	if sc.Burst < 1 {
		return nil, fmt.Errorf("segment: 令牌桶容量必须 >=1（%d）", sc.Burst)
	}
	if sc.TTL <= 0 || sc.SingleResponseMaxBytes <= 0 {
		return nil, errors.New("segment: TTL 与单次返回上限必须为正")
	}

	root := strings.TrimSpace(sc.TempDir)
	if root == "" {
		root = filepath.Join(os.TempDir(), "projectionroom-segment")
	}

	tools := DiscoverTools(sc.FFmpegPath)
	if o.tools != nil {
		tools = *o.tools
	}

	now := o.now
	if now == nil {
		now = time.Now
	}
	prober := o.prober
	if prober == nil {
		prober = Probe
	}
	processor := o.processor
	if processor == nil {
		processor = processorFunc(Process)
	}
	idGen := o.idGen
	if idGen == nil {
		idGen = func() (string, error) { return utils.RandomCode(utils.RoomCodeAlphabet, jobIDLength) }
	}

	ctx, cancel := context.WithCancel(context.Background())
	q := &Queue{
		cfg:        sc,
		root:       root,
		ctx:        ctx,
		stopCtx:    cancel,
		now:        now,
		probe:      prober,
		process:    processor,
		tools:      tools,
		newID:      idGen,
		stop:       make(chan struct{}),
		jobs:       make(map[string]*Job),
		tokens:     float64(sc.Burst),
		lastRefill: now(),
	}

	if !tools.Available() {
		log.Printf("WARN segment: 未找到 ffmpeg/ffprobe（PR_FFMPEG=%q）；"+
			"服务端切片作业会立即失败。请安装 ffmpeg，或让用户按 docs/SEGMENT.md 在本地切片。"+
			"注意：这**不影响**直播链路，分片仍然只在 peer 之间传输（不变量 I1）。",
			sc.FFmpegPath)
	}

	return q, nil
}

// Tools 返回已定位到的 ffmpeg/ffprobe（可能为空，表示都不可用）。
func (q *Queue) Tools() Tools { return q.tools }

// Start 启动后台清理协程（幂等）。
func (q *Queue) Start() {
	q.startOnce.Do(func() {
		q.wg.Add(1)
		go func() {
			defer q.wg.Done()
			ticker := time.NewTicker(q.cfg.CleanupInterval)
			defer ticker.Stop()
			for {
				select {
				case <-q.stop:
					return
				case <-ticker.C:
					q.Cleanup()
				}
			}
		}()
	})
}

// Close 停止清理协程并中断在跑的 ffmpeg（幂等）。
func (q *Queue) Close() {
	q.closeOnce.Do(func() {
		close(q.stop)
		q.stopCtx()
	})
	q.wg.Wait()
}

// Submit 接收一次上传：先占配额，再把源文件落盘，最后做前置校验并入队。
//
// 返回值里的 error 只表示"请求被拒"（413/422/429 等），此时作业不存在；
// 返回 nil error 表示请求已被接受（HTTP 202），作业状态可能是 queued/running，
// 也可能因为服务器没有 ffmpeg 而**立即 failed**（这时作业记录保留，便于前端展示原因）。
func (q *Queue) Submit(filename string, src io.Reader, declaredSize int64) (*View, error) {
	if declaredSize > q.cfg.MaxSourceBytes {
		return nil, fmt.Errorf("%w（上限 %s）", ErrSourceTooLarge, HumanBytes(q.cfg.MaxSourceBytes))
	}

	// 先探一个字节再排队：空上传是**客户端**错误，与服务器装没装 ffmpeg 无关。
	// 这一步必须排在"缺 ffmpeg 就直接失败"之前 —— 否则没有 ffmpeg 的机器上，
	// 空文件会拿到 202 + 一个失败的作业，而不是 422（在容器里跑 -race 时暴露的）。
	head := make([]byte, 1)
	n, readErr := io.ReadFull(src, head)
	if n == 0 {
		return nil, ErrSourceEmpty
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return nil, fmt.Errorf("segment: 读取上传失败: %w", readErr)
	}
	src = io.MultiReader(bytes.NewReader(head[:n]), src)

	job, err := q.reserve()
	if err != nil {
		return nil, err
	}

	// 服务器没装 ffmpeg：不接受上传（会白白传几个 GB），但保留作业记录并立即失败，
	// 让前端能拿到那条明确的指引文案。
	if !q.tools.Available() {
		q.failImmediately(job, ErrFFmpegMissing)
		view := job.Snapshot()
		return &view, nil
	}

	sourcePath := filepath.Join(job.dir, "source"+SourceExt(filename))
	if err := q.receive(job, src, sourcePath); err != nil {
		q.dropReservation(job)
		return nil, err
	}
	job.mu.Lock()
	job.sourcePath = sourcePath
	job.mu.Unlock()

	info, err := q.probe(q.ctx, q.tools, sourcePath)
	switch {
	case err != nil && errors.Is(err, ErrFFmpegMissing):
		// ffprobe 在这中间消失（被卸载/权限变化）：按"服务器没有 ffmpeg"处理，
		// 保留作业记录让前端能看到原因。
		q.failJob(job, ErrFFmpegMissing)
		view := job.Snapshot()
		return &view, nil

	case err != nil:
		q.dropReservation(job)
		if errors.Is(err, ErrProbeFailed) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %v", ErrProbeFailed, err)
	}

	if maxSeconds := q.cfg.MaxDuration.Seconds(); maxSeconds > 0 && info.DurationSec > maxSeconds {
		q.dropReservation(job)
		return nil, fmt.Errorf("%w：视频 %.1f 分钟，上限 %.0f 分钟",
			ErrDurationTooLong, info.DurationSec/60, maxSeconds/60)
	}

	job.mu.Lock()
	job.sourceBytes = info.SizeBytes
	job.info = info
	job.mu.Unlock()
	job.markReady()

	q.dispatch()

	if view, ok := q.Get(job.id); ok {
		return view, nil
	}
	snapshot := job.Snapshot()
	return &snapshot, nil
}

// receive 把上传流写进作业目录，并强制大小上限（边写边判，不落一个超限的大文件）。
func (q *Queue) receive(job *Job, src io.Reader, path string) error {
	if err := os.MkdirAll(job.dir, 0o755); err != nil {
		return fmt.Errorf("segment: 创建作业目录失败: %w", err)
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("segment: 创建源文件失败: %w", err)
	}

	written, copyErr := io.Copy(f, io.LimitReader(src, q.cfg.MaxSourceBytes+1))
	closeErr := f.Close()

	switch {
	case copyErr != nil:
		return fmt.Errorf("segment: 接收上传失败: %w", copyErr)
	case written > q.cfg.MaxSourceBytes:
		return fmt.Errorf("%w（上限 %s）", ErrSourceTooLarge, HumanBytes(q.cfg.MaxSourceBytes))
	case closeErr != nil:
		return fmt.Errorf("segment: 写入源文件失败: %w", closeErr)
	case written == 0:
		return ErrSourceEmpty
	}
	return nil
}

// reserve 占一个排队位并扣一个令牌。任一失败都返回 *QuotaError（handler 映射成 429）。
func (q *Queue) reserve() (*Job, error) {
	id, err := q.newID()
	if err != nil {
		return nil, fmt.Errorf("segment: 生成作业 ID 失败: %w", err)
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.order) >= q.cfg.QueueLength {
		return nil, &QuotaError{Err: ErrQueueFull, RetryAfter: queueFullRetryAfter}
	}
	if ok, wait := q.takeTokenLocked(); !ok {
		return nil, &QuotaError{Err: ErrRateLimited, RetryAfter: wait}
	}

	job := &Job{
		id:        id,
		state:     StateQueued,
		createdAt: q.now(),
		dir:       filepath.Join(q.root, "job-"+id),
	}
	q.jobs[id] = job
	q.order = append(q.order, job)
	return job, nil
}

// takeTokenLocked 做一次令牌桶判定：按经过的时间匀速补充，再尝试取 1 个。
func (q *Queue) takeTokenLocked() (bool, time.Duration) {
	now := q.now()
	ratePerSecond := float64(q.cfg.RatePerMinute) / 60
	if elapsed := now.Sub(q.lastRefill); elapsed > 0 {
		q.tokens = math.Min(float64(q.cfg.Burst), q.tokens+ratePerSecond*elapsed.Seconds())
		q.lastRefill = now
	}
	if q.tokens >= 1 {
		q.tokens--
		return true, 0
	}
	// 距离下一个令牌还差多少：向上取整到秒，避免 Retry-After: 0。
	need := time.Duration(math.Ceil((1-q.tokens)/ratePerSecond)) * time.Second
	if need < time.Second {
		need = time.Second
	}
	return false, need
}

// refundTokenLocked 把占位失败时消耗的令牌退回去（只有"请求根本没被接受"才退）。
func (q *Queue) refundTokenLocked() {
	q.tokens = math.Min(float64(q.cfg.Burst), q.tokens+1)
}

// dropReservation 撤销一次尚未开始的作业：删除记录、让出排队位、退回令牌、清理目录。
func (q *Queue) dropReservation(job *Job) {
	q.mu.Lock()
	delete(q.jobs, job.id)
	q.removeFromOrderLocked(job)
	q.refundTokenLocked()
	q.mu.Unlock()

	_ = os.RemoveAll(job.dir)
}

// failImmediately 让作业立刻进入 failed 并保留记录（不含排队位）。
func (q *Queue) failImmediately(job *Job, err error) {
	job.markFailed(err, q.now())
	q.mu.Lock()
	q.removeFromOrderLocked(job)
	q.mu.Unlock()
}

// dispatch 在还有并发余量时领取排队中的就绪作业。
// 它跳过还没准备好（上传/探测中）的作业，避免一个慢上传把整条队列堵死。
func (q *Queue) dispatch() {
	q.mu.Lock()
	var start []*Job
	for i := 0; i < len(q.order) && q.running < q.cfg.Concurrency; {
		job := q.order[i]
		if !job.isReady() {
			i++
			continue
		}
		start = append(start, job)
		q.order = append(q.order[:i], q.order[i+1:]...)
		q.running++
	}
	q.mu.Unlock()

	for _, job := range start {
		go q.run(job)
	}
}

// run 执行一个作业，结束后释放并发位并再次调度。
func (q *Queue) run(job *Job) {
	defer func() {
		q.mu.Lock()
		q.running--
		q.mu.Unlock()
		q.dispatch()
	}()

	job.markRunning(q.now())

	workDir := filepath.Join(job.dir, "work")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		q.failJob(job, err)
		return
	}

	outDir := filepath.Join(job.dir, "out")
	artifacts, err := q.process.Process(q.ctx, job.sourcePath, outDir, ProcessOptions{
		Tools:          q.tools,
		SegmentSeconds: q.cfg.SegmentSeconds,
		Auto:           true,
		Info:           job.currentInfo(),
		WorkDir:        workDir,
		Progress:       job.setProgress,
		Logf: func(format string, args ...any) {
			log.Printf("segment: 作业 %s: %s", job.id, fmt.Sprintf(format, args...))
		},
	})
	if err != nil {
		q.failJob(job, err)
		return
	}

	result, layout, err := q.buildResult(artifacts)
	if err != nil {
		q.failJob(job, err)
		return
	}

	q.removeSource(job)
	job.markDone(artifacts, result, layout, q.now())
}

// buildResult 决定交付形态：总大小不超过单次上限 → 一次返回；
// 超过 → 切成若干份（每份严格小于上限）并预先算好每份的 sha256。
func (q *Queue) buildResult(artifacts *Artifacts) (*Result, [][]ArtifactFile, error) {
	result := &Result{
		Bytes:          artifacts.TotalBytes,
		Segments:       len(artifacts.Index.Segments),
		SingleResponse: artifacts.TotalBytes <= q.cfg.SingleResponseMaxBytes,
	}
	if result.SingleResponse {
		return result, [][]ArtifactFile{artifacts.Files}, nil
	}

	groups, err := PlanParts(artifacts.Files, q.cfg.SingleResponseMaxBytes)
	if err != nil {
		return nil, nil, err
	}
	for i, group := range groups {
		size, sum, err := hashZip(artifacts.Dir, group)
		if err != nil {
			return nil, nil, err
		}
		result.Parts = append(result.Parts, Part{N: i + 1, Bytes: size, SHA256: sum})
	}
	return result, groups, nil
}

// failJob 标记失败并清掉整个作业目录（失败的作业没有任何可用产物）。
func (q *Queue) failJob(job *Job, err error) {
	job.markFailed(err, q.now())
	q.removeSource(job)
	_ = os.RemoveAll(job.dir)
}

// removeSource 删除源文件 —— 无论成功还是失败，源文件都不该留在磁盘上。
func (q *Queue) removeSource(job *Job) {
	job.mu.Lock()
	path := job.sourcePath
	job.sourcePath = ""
	job.mu.Unlock()

	if path != "" {
		_ = os.Remove(path)
	}
}

// Get 返回作业的当前视图，第二个返回值表示是否存在。
func (q *Queue) Get(id string) (*View, bool) {
	q.mu.Lock()
	job, ok := q.jobs[id]
	position := 0
	if ok {
		position = q.positionLocked(job)
	}
	q.mu.Unlock()

	if !ok {
		return nil, false
	}
	view := job.Snapshot()
	view.QueuePosition = position
	return &view, true
}

// Stats 返回队列快照。
func (q *Queue) Stats() Stats {
	q.mu.Lock()
	defer q.mu.Unlock()
	return Stats{Running: q.running, Queued: len(q.order), Jobs: len(q.jobs)}
}

// Artifacts 返回已完成作业的产物目录与清单（单次返回用）。
func (q *Queue) Artifacts(id string) (string, []ArtifactFile, error) {
	artifacts, _, result, err := q.artifactJob(id)
	if err != nil {
		return "", nil, err
	}
	if !result.SingleResponse {
		return "", nil, ErrNoParts
	}
	return artifacts.Dir, artifacts.Files, nil
}

// PartFiles 返回第 n 份分批下载的内容。
func (q *Queue) PartFiles(id string, n int) (string, []ArtifactFile, Part, error) {
	artifacts, layout, result, err := q.artifactJob(id)
	if err != nil {
		return "", nil, Part{}, err
	}
	if result.SingleResponse {
		return "", nil, Part{}, ErrNoParts
	}
	if n < 1 || n > len(layout) {
		return "", nil, Part{}, ErrPartNotFound
	}
	return artifacts.Dir, layout[n-1], result.Parts[n-1], nil
}

// artifactJob 取出已完成作业的产物视图，并区分"不存在 / 未完成 / 走分批"。
// 返回的切片都在作业锁内读取，调用方可以安全使用。
func (q *Queue) artifactJob(id string) (*Artifacts, [][]ArtifactFile, *Result, error) {
	q.mu.Lock()
	job, ok := q.jobs[id]
	q.mu.Unlock()
	if !ok {
		return nil, nil, nil, ErrJobNotFound
	}

	job.mu.Lock()
	defer job.mu.Unlock()
	switch job.state {
	case StateDone:
		return job.artifacts, job.layout, job.result, nil
	case StateFailed:
		return nil, nil, nil, fmt.Errorf("%w: %s", ErrJobNotReady, job.errMsg)
	default:
		return nil, nil, nil, ErrJobNotReady
	}
}

// Cleanup 清理过期作业与孤儿目录。后台协程按 CleanupInterval 周期调用它。
func (q *Queue) Cleanup() {
	now := q.now()

	q.mu.Lock()
	var dirs []string
	for id, job := range q.jobs {
		if job.expired(now, q.cfg.TTL) {
			delete(q.jobs, id)
			q.removeFromOrderLocked(job)
			dirs = append(dirs, job.dir)
		}
	}
	q.mu.Unlock()

	for _, dir := range dirs {
		_ = os.RemoveAll(dir)
	}

	q.sweepOrphans(now)
}

// sweepOrphans 删除临时根目录下没有对应作业记录的陈旧目录
// （例如上一次进程被强杀留下的残骸）。
func (q *Queue) sweepOrphans(now time.Time) {
	entries, err := os.ReadDir(q.root)
	if err != nil {
		return
	}

	q.mu.Lock()
	live := make(map[string]struct{}, len(q.jobs))
	for _, job := range q.jobs {
		live[job.dir] = struct{}{}
	}
	q.mu.Unlock()

	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "job-") {
			continue
		}
		full := filepath.Join(q.root, entry.Name())
		if _, ok := live[full]; ok {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) >= q.cfg.TTL {
			_ = os.RemoveAll(full)
		}
	}
}

// positionLocked 返回作业在排队切片中的 1-based 位置；不在队列中返回 0。
func (q *Queue) positionLocked(job *Job) int {
	for i, candidate := range q.order {
		if candidate == job {
			return i + 1
		}
	}
	return 0
}

func (q *Queue) removeFromOrderLocked(job *Job) {
	for i, candidate := range q.order {
		if candidate == job {
			q.order = append(q.order[:i], q.order[i+1:]...)
			return
		}
	}
}
