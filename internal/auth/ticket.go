package auth

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

const (
	// ticketBytes 是票据的随机熵（字节）。32 字节 = 256 bit：票据会以
	// `?ticket=...` 的形式进 nginx access log 与浏览器历史，虽然只活 30 秒，
	// 但它替代 access token 出现在 URL 里，所以强度按"可长期暴露的凭证"来定。
	ticketBytes = 32
	// jtiBytes 是 access token 里 jti 的长度。16 字节足够在 15 分钟窗口内不碰撞；
	// 它只需要唯一，不需要抵抗猜测（每次登录都会换新）。
	jtiBytes = 16
	// DefaultTicketTTL 是 NewTicketStore 收到非正 TTL 时的兜底值，
	// 与 config.DefaultWSTicketTTL 一致（见 NewTicketStore 的说明）。
	DefaultTicketTTL = 30 * time.Second
	// maxTicketLen 是 Consume 愿意处理的最长票据。我们自己签发的票据长度固定，
	// 这一条只是为了不拿攻击者给的任意长字符串去做哈希/建表。
	maxTicketLen = 128
)

// TicketStore 是 WebSocket 一次性票据的内存表。
//
// 为什么需要它：浏览器的 WebSocket API 无法自定义请求头，把 access token 直接拼进
// URL 会把它写进 nginx access log、浏览器历史与 Referer。票据是"30 秒有效 +
// 单次使用"的短命替代品（ACCOUNTS §5）。代价是它只在本进程内存里，
// 因此本期明确不做多实例部署。
type TicketStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	tickets map[string]ticketEntry
}

// ticketEntry 是票据对应的身份与到期时刻。票据本身不含任何身份信息，
// 身份只在服务端这张表里，客户端无法通过解码票据得知或伪造它。
type ticketEntry struct {
	userID  int64
	expires time.Time
}

// NewTicketStore 构造票据表；ttl <= 0 时退回 DefaultTicketTTL。
//
// 为什么不 fail closed（把 ttl <= 0 当成"所有票据立刻过期"）：那样 WS 鉴权会整体
// 静默失效，症状是"带票据连不上"，比配置错误更难定位。config 已经把
// PR_WS_TICKET_TTL 限制在 (0,60s]，走到这里的 0 只可能是忘了接线，
// 此时退回与配置默认值相同的 30s，让链路照常工作。
func NewTicketStore(ttl time.Duration) *TicketStore {
	if ttl <= 0 {
		ttl = DefaultTicketTTL
	}
	return &TicketStore{ttl: ttl, tickets: make(map[string]ticketEntry)}
}

// Issue 生成一张绑定 userID 的票据，返回不透明随机串。
//
// 随机源失败时返回空串：Consume 永远查不到空串，于是这一步是 fail closed
// （拿不到票据 = 连不上），而不是"降级成一个可猜测的票据"。
func (t *TicketStore) Issue(userID int64) string {
	tok, err := newOpaqueToken(ticketBytes)
	if err != nil {
		return ""
	}
	t.mu.Lock()
	t.tickets[tok] = ticketEntry{userID: userID, expires: time.Now().Add(t.ttl)}
	t.mu.Unlock()
	return tok
}

// Consume 取出票据对应的用户，并**立即删除**它——同一张票据只有一个调用者能拿到
// ok=true。
//
// 并发正确性来自"查与删在同一个互斥区里"：一旦拆成"先查后删"或分两次加锁，
// 并发的握手请求就会有多个看到命中（票据复用）。这是本类型唯一的并发要点，
// 也是 ticket_test.go 里 100 goroutine 用例要钉住的性质。
//
// 过期条目返回 false 并顺手删除：它已经没有价值，留着只会增长内存。
func (t *TicketStore) Consume(ticket string) (int64, bool) {
	if ticket == "" || len(ticket) > maxTicketLen {
		return 0, false
	}

	now := time.Now()

	t.mu.Lock()
	defer t.mu.Unlock()

	entry, ok := t.tickets[ticket]
	if !ok {
		return 0, false
	}
	delete(t.tickets, ticket)

	if !now.Before(entry.expires) {
		return 0, false
	}
	return entry.userID, true
}

// Sweep 删除所有已过期条目并返回删除条数，供后台定时调用（与房间清扫同频）。
//
// 只删过期项：未过期的票据是"客户端已取票、还没完成握手"的正常中间态，
// 顺手清掉会随机打断正在建立的连接。now 由调用方传入，让清理逻辑可测且
// 不需要在被清理的数据里再读一次时钟。
func (t *TicketStore) Sweep(now time.Time) int {
	t.mu.Lock()
	defer t.mu.Unlock()

	n := 0
	for k, entry := range t.tickets {
		if !now.Before(entry.expires) {
			delete(t.tickets, k)
			n++
		}
	}
	return n
}

// Len 返回当前票据数（测试与指标用）。
func (t *TicketStore) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.tickets)
}

// newOpaqueToken 生成 n 字节随机数的 base64url（无填充）表示。
//
// 为什么用 RawURLEncoding 而不是标准 base64：票据会被拼进查询串（?ticket=...），
// 标准表里的 '+' '/' '=' 在 URL 里需要再编码一层，而多一层编码就多一处
// "某一端忘了转义"的坑；RawURLEncoding 的表面对 URL 与 cookie 都安全。
func newOpaqueToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
