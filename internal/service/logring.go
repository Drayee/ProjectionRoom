package service

import (
	"bytes"
	"io"
	"log"
	"strings"
	"sync"
)

// LogRing 是管理端「查看服务端日志」的数据源：固定容量的环形缓冲。
//
// 为什么不落库（ACCOUNTS §4/§8 的决策）：
//  1. 日志行里混着用户可控文本（昵称、房间标题、聊天片段、UA）。落库等于把
//     "日志注入"从一次性输出升级成**持久化污染**，还得额外做转义与清理；
//  2. 容量必须有界。日志的增长速率没有上界（一次攻击就能刷满磁盘），
//     而"最近 N 条"才是运维真正要看的；
//  3. 管理端读取只需要"最近一段"，环形缓冲天然满足，且读取路径不碰数据库。
//
// 另外它刻意**不**解析日志格式：日志是非结构化文本，任何"结构化"都只是猜测，
// 而猜测会在格式变化时静默失效。管理端只做文本展示 + 分页（形态要求在 §8）。
type LogRing struct {
	mu       sync.Mutex
	lines    []string
	next     int
	size     int
	capacity int

	// partial 存放尚未成行的残余。log 包的每次 Write 通常是一整行，
	// 但 io.MultiWriter 与子进程输出都可能把一行拆成多次写。
	partial []byte

	total   int64
	dropped int64
}

const (
	// LogRingDefaultCapacity 是默认保留的日志行数。
	// 2000 行约几百 KB（每行通常一两百字节），足够覆盖"最近一次故障的现场"。
	LogRingDefaultCapacity = 2000
	// LogRingMaxLineBytes 是单行的保留上限。
	// 存在的意义：一条畸形超长输出（例如把整个响应体打出来）不该把缓冲区挤空。
	LogRingMaxLineBytes = 4096
	// 截断标记，让读取者知道这行不完整（而不是以为日志就这么短）。
	logRingTruncatedSuffix = "…（已截断）"
)

// NewLogRing 构造环形缓冲；capacity <= 0 时退回默认容量。
func NewLogRing(capacity int) *LogRing {
	if capacity <= 0 {
		capacity = LogRingDefaultCapacity
	}
	return &LogRing{
		lines:    make([]string, capacity),
		capacity: capacity,
	}
}

// Write 实现 io.Writer，可安全地被多个 goroutine 并发调用（log 包本身是串行的，
// 但我们还会把子进程/其它来源的输出也接进来）。
func (l *LogRing) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.partial = append(l.partial, p...)
	for {
		idx := bytes.IndexByte(l.partial, '\n')
		if idx < 0 {
			// 没有完整行：如果残余已经过长，先按上限切开存一行，
			// 否则一条永不换行的输出会把内存吃光。
			if len(l.partial) > LogRingMaxLineBytes {
				l.pushLocked(sanitizeLogLine(l.partial))
				l.partial = l.partial[:0]
			}
			break
		}
		line := l.partial[:idx]
		l.pushLocked(sanitizeLogLine(line))
		l.partial = append(l.partial[:0], l.partial[idx+1:]...)
	}
	return len(p), nil
}

// pushLocked 写入一行（调用方必须持锁）。
func (l *LogRing) pushLocked(line string) {
	l.lines[l.next] = line
	l.next = (l.next + 1) % l.capacity
	if l.size < l.capacity {
		l.size++
	} else {
		// 缓冲区已满：覆盖最旧的一行，计入丢弃。
		l.dropped++
	}
	l.total++
}

// Snapshot 返回**从最新往旧**的日志分页。
//
// offset 从最新一条开始计数（0 = 最新），limit 为本页条数；同时返回当前总条数，
// 便于管理端显示"共 N 条"。offset 超出范围时返回空切片而不是报错 ——
// 日志在持续写入，客户端拿到过期页码是正常现象。
func (l *LogRing) Snapshot(limit, offset int) ([]string, int64) {
	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	total := int64(l.size)
	if int64(offset) >= total {
		return []string{}, total
	}
	out := make([]string, 0, limit)
	// 最新一条在 (next-1+capacity)%capacity。
	for i := offset; i < l.size && len(out) < limit; i++ {
		pos := (l.next - 1 - i + l.capacity*2) % l.capacity
		out = append(out, l.lines[pos])
	}
	return out, total
}

// Len 返回当前保留的条数。
func (l *LogRing) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.size
}

// Stats 返回累计写入与因缓冲满被丢弃的条数（管理端指标）。
func (l *LogRing) Stats() (total, dropped int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.total, l.dropped
}

// InstallLogRing 把环形缓冲接到标准日志输出上，返回恢复函数。
//
// 用 MultiWriter 而不是替换：标准错误仍然保留，运维继续用 journalctl 看实时日志，
// 环形缓冲只是"给管理端留一份最近的"。返回的恢复函数让测试不污染全局状态。
func InstallLogRing(ring *LogRing) (restore func()) {
	if ring == nil {
		return func() {}
	}
	previous := log.Writer()
	log.SetOutput(io.MultiWriter(previous, ring))
	return func() { log.SetOutput(previous) }
}

// sanitizeLogLine 清洗一行日志。
//
// 为什么必须清洗：
//  1. 去掉 \r 与控制字符：否则一条 \r 就能在终端/管理端页面里"覆盖"掉前面的内容
//     （日志伪造），而日志里确实会带上用户可控文本（昵称、房间码、房间标题）；
//  2. 保留 \t：缩进是可读性的一部分，且不构成控制风险；
//  3. 截断超长行：见 LogRingMaxLineBytes。
func sanitizeLogLine(b []byte) string {
	trimmed := strings.TrimRight(string(b), "\r")
	var sb strings.Builder
	sb.Grow(len(trimmed))
	for _, r := range trimmed {
		switch {
		case r == '\t':
			sb.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			// 丢弃（包括 DEL）。不替换成可见符号：那会让正常日志变噪音。
		default:
			sb.WriteRune(r)
		}
	}
	out := sb.String()
	if len(out) > LogRingMaxLineBytes {
		out = out[:LogRingMaxLineBytes] + logRingTruncatedSuffix
	}
	return out
}
