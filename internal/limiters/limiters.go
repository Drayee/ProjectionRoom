// Package limiters 提供两件与业务无关的限流原语：
//
//   - keyedLimiter：按键（每 IP / 每 IP+房间码）的令牌桶，用于建房与 join 失败；
//   - Bucket：单桶令牌桶，用于"每连接字节速率配额"。
//
// 为什么不用 golang.org/x/time/rate：本仓库的硬约束是"不引入新依赖"（除 S-17 的
// quic-go / x/net 升级本身）。这两个原语一共不到 100 行，比多一个模块依赖更划算；
// 而且"按键限流 + 定期清理"这个组合 x/time/rate 也不直接提供。
//
// 为什么都用令牌桶而不是固定窗口：这些入口的正常用量是"偶发一小簇"
// （建房一次、密码打错两三次），而滥用是"持续稳定"。令牌桶对前者零约束、
// 对后者有硬上限；固定窗口会在窗口边缘被绕过，且对突发不友好。
package limiters

import (
	"sync"
	"time"
)

// Keyed 是按键限流的令牌桶集合。
//
// 语义：Allow 尝试取 1 个令牌，取到返回 true 并消耗它，取不到返回 false（不排队）。
// 时间源是 time.Now，因此它不适合做"精确的记账"，只适合做"把滥用压到无害频次"。
type Keyed struct {
	mu sync.Mutex
	// perSec 是匀速补充速率（令牌/秒）。
	perSec float64
	// burst 是桶容量，即最多能连续取走几个令牌。
	burst float64
	// buckets 是按 key 的当前状态。
	buckets map[string]*bucket
	// lastSweep 是上一次清理时间（惰性清理：没有后台协程，也就没有生命周期）。
	lastSweep time.Time
}

type bucket struct {
	tokens float64
	// last 是上次补充时间。
	last time.Time
}

// idleTTL 是"一个键多久没动过就可以被忘掉"。
//
// 取 1 小时而不是"几倍 refill 时间"：桶的容量会自动封顶，
// 忘掉一个键的全部后果就是"它的令牌恢复到满桶"——那需要一个攻击者
// 先安静一小时再来，而安静一小时已经等价于放弃这次滥用。
// 1 小时也远大于任何真实请求间隔，所以正常用户不会被"限速状态被重置"影响。
const idleTTL = time.Hour

// NewKeyed 构造按键令牌桶。perMinute 是每分钟补充的令牌数，burst 是桶容量。
// perMinute <= 0 或 burst <= 0 时返回 nil —— 调用方用它表示"这条闸门关闭"。
func NewKeyed(perMinute float64, burst int) *Keyed {
	if perMinute <= 0 || burst <= 0 {
		return nil
	}
	return &Keyed{
		perSec:  perMinute / 60,
		burst:   float64(burst),
		buckets: make(map[string]*bucket),
	}
}

// Allow 尝试为 key 取一个令牌。
// k 为 nil（闸门关闭）时永远放行。
func (k *Keyed) Allow(key string) bool {
	if k == nil {
		return true
	}

	now := time.Now()

	k.mu.Lock()
	defer k.mu.Unlock()

	k.sweepLocked(now)

	b, ok := k.buckets[key]
	if !ok {
		// 新键从满桶开始：正常用户的第一次请求永远不该被限速。
		k.buckets[key] = &bucket{tokens: k.burst - 1, last: now}
		return true
	}

	// 按经过时间补充。
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens += elapsed * k.perSec
		if b.tokens > k.burst {
			b.tokens = k.burst
		}
		b.last = now
	}

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// sweepLocked 惰性清理长期不动的键。成本是 O(键数)，而键数被
// "每 IP / 每 IP+房间码"限制在合理量级；用最长的周期触发一次，避免每请求都遍历。
func (k *Keyed) sweepLocked(now time.Time) {
	const sweepEvery = 5 * time.Minute
	if now.Sub(k.lastSweep) < sweepEvery {
		return
	}
	k.lastSweep = now
	for key, b := range k.buckets {
		if now.Sub(b.last) > idleTTL {
			delete(k.buckets, key)
		}
	}
}

// Size 返回当前跟踪的键数（测试与排障用）。
func (k *Keyed) Size() int {
	if k == nil {
		return 0
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.buckets)
}

// Bucket 是单桶令牌桶，用于"每连接字节速率配额"。
//
// 与 Keyed 的区别：它不做按 key 的记账（一条连接一个实例），但需要
// "一次请求大于桶容量"的语义 —— 见 Allow 的注释。
type Bucket struct {
	mu       sync.Mutex
	perSec   float64
	capacity float64
	tokens   float64
	last     time.Time
}

// NewBucket 构造单桶令牌桶。bytesPerSec <= 0 或 burstBytes <= 0 时返回 nil（关闭）。
func NewBucket(bytesPerSec, burstBytes int) *Bucket {
	if bytesPerSec <= 0 || burstBytes <= 0 {
		return nil
	}
	return &Bucket{
		perSec:   float64(bytesPerSec),
		capacity: float64(burstBytes),
		tokens:   float64(burstBytes),
		last:     time.Now(),
	}
}

// Allow 判断"消耗 n 个字节"是否在配额内，是则扣减并返回 true。
//
// 关键语义（与 wire 预算配合）：如果 n 本身超过桶容量，则**放行这一次但把桶清零**。
// 理由：桶容量（512 KiB）有意小于单条消息上限（4 MiB），因为"一条索引帧"确实可能
// 几 MB，而它必须能通过（否则主播一发布索引就被掐断 —— 那是既有的 1009 问题）。
// 因此这条闸门的定位是"限制持续速率"，不是"限制单帧大小"：单帧大小由
// SetReadLimit 与 model.ScanWireBudget 负责。
func (b *Bucket) Allow(n int) bool {
	if b == nil {
		return true
	}
	if n <= 0 {
		return true
	}

	now := time.Now()

	b.mu.Lock()
	defer b.mu.Unlock()

	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens += elapsed * b.perSec
		if b.tokens > b.capacity {
			b.tokens = b.capacity
		}
		b.last = now
	}

	if float64(n) > b.capacity {
		b.tokens = 0
		return true
	}
	if b.tokens < float64(n) {
		return false
	}
	b.tokens -= float64(n)
	return true
}

// Tokens 返回当前可用令牌（测试与排障用）。
func (b *Bucket) Tokens() float64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tokens
}
