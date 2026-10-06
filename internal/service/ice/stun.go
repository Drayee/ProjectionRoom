package ice

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// STUN 报文常量（RFC 5389 §6 / §15.1 / §15.2）。
const (
	stunHeaderLen      = 20
	stunMagicCookie    = 0x2112A442
	stunBindingRequest = 0x0001
	stunBindingSuccess = 0x0101
	// defaultSTUNPort 是 RFC 7064 约定的 STUN 默认端口。
	defaultSTUNPort = "3478"
	// readBufSize 足够容纳带 XOR-MAPPED-ADDRESS 的响应（含 IPv6 属性）。
	readBufSize = 512
)

// UDPProber 是生产探测器：对每个 STUN 发一条 UDP Binding Request，
// 收到**同一个事务**的 Success Response 就算成功。
//
// 为什么用 udp4 而不是 udp：本机（以及很多公司网络 + 隧道 的组合）常见
// "有 AAAA 记录但没有可用 IPv6 出口"，用 udp4 探测可以避免把这类服务器误判成不可用。
// 下发给客户端的 URL 原样保留，走 IPv4 还是 IPv6 完全由浏览器决定，
// 因此这里的 IPv4 视角不会影响客户端拿 IPv6 srflx 候选的能力。
type UDPProber struct {
	// Timeout 是单条探测的预算（契约值 1.5s）；<=0 时退化为 ProbeTimeout。
	Timeout time.Duration
}

// NewUDPProber 构造 UDP 探测器。
func NewUDPProber(timeout time.Duration) *UDPProber { return &UDPProber{Timeout: timeout} }

// Probe 并发探完 urls，返回与 urls 等长同序的结果；单条失败不影响其它条目。
func (p *UDPProber) Probe(ctx context.Context, urls []string) []Observation {
	out := make([]Observation, len(urls))

	var wg sync.WaitGroup
	for i, u := range urls {
		wg.Add(1)
		go func(i int, u string) {
			defer wg.Done()
			ok, rtt := p.probeOne(ctx, u)
			out[i] = Observation{URL: u, OK: ok, RTT: rtt}
		}(i, u)
	}
	wg.Wait()

	return out
}

// probeOne 探一条 URL，返回是否成功与往返延迟（失败时延迟为 0）。
func (p *UDPProber) probeOne(ctx context.Context, rawURL string) (bool, time.Duration) {
	hostport, err := parseSTUNURL(rawURL)
	if err != nil {
		return false, 0
	}

	timeout := p.Timeout
	if timeout <= 0 {
		timeout = ProbeTimeout
	}

	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "udp4", hostport)
	if err != nil {
		return false, 0
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return false, 0
	}
	// ctx 取消（服务关闭、测试结束）时立刻把读操作踢醒，不必等满 1.5s。
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()

	txID, err := newTransactionID()
	if err != nil {
		return false, 0
	}

	start := time.Now()
	if _, err := conn.Write(buildBindingRequest(txID)); err != nil {
		return false, 0
	}

	buf := make([]byte, readBufSize)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			// 超时、ICMP port unreachable（被 Go 转成本地读错误）都归为"这条不可用"。
			return false, 0
		}
		if isBindingSuccess(buf[:n], txID) {
			return true, time.Since(start)
		}
		// 不匹配的报文（例如服务器回的 400/其它事务）不算成功，但也别立刻放弃：
		// 在剩余预算里继续等正确的那个响应。
		if time.Now().After(start.Add(timeout)) {
			return false, 0
		}
	}
}

// buildBindingRequest 生成 RFC 5389 §6 的 Binding Request：20 字节、无属性。
func buildBindingRequest(txID [12]byte) []byte {
	b := make([]byte, stunHeaderLen)
	binary.BigEndian.PutUint16(b[0:2], stunBindingRequest)
	binary.BigEndian.PutUint16(b[2:4], 0) // 属性长度 = 0
	binary.BigEndian.PutUint32(b[4:8], stunMagicCookie)
	copy(b[8:20], txID[:])
	return b
}

// isBindingSuccess 校验一条报文确实是"针对我们这个事务的 Binding Success Response"。
// 只认 RFC 5389：magic cookie、事务 ID、消息类型三者都对，且声明的属性长度不超出实际长度。
// （这不是完整的 STUN 解析器——我们不需要 XOR-MAPPED-ADDRESS，只需要"它在并且答对了"。）
func isBindingSuccess(resp []byte, txID [12]byte) bool {
	if len(resp) < stunHeaderLen {
		return false
	}
	if binary.BigEndian.Uint16(resp[0:2]) != stunBindingSuccess {
		return false
	}
	if binary.BigEndian.Uint32(resp[4:8]) != stunMagicCookie {
		return false
	}
	if !bytes.Equal(resp[8:20], txID[:]) {
		return false
	}
	if attrLen := int(binary.BigEndian.Uint16(resp[2:4])); stunHeaderLen+attrLen > len(resp) {
		return false
	}
	return true
}

// newTransactionID 生成 12 字节事务 ID。
// 必须随机：固定值会把不同窗口、不同并发探测的响应混在一起。
func newTransactionID() ([12]byte, error) {
	var id [12]byte
	_, err := rand.Read(id[:])
	return id, err
}

// parseSTUNURL 把 stun:/stuns: URL 解析成 net.Dial 能用的 host:port。
//
// RFC 7064：scheme 后面直接跟 host[:port]，端口缺省 3478。
// 这里额外容忍 `?transport=udp` 这类查询串（有些配置里会带上）。
func parseSTUNURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("ice: STUN URL 为空")
	}

	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "stuns:"):
		s = s[len("stuns:"):]
	case strings.HasPrefix(lower, "stun:"):
		s = s[len("stun:"):]
	}
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return "", fmt.Errorf("ice: STUN URL %q 缺少主机名", raw)
	}
	// RFC 7064 里 scheme 后面直接跟 host，不带斜杠；带斜杠的一律当配置错误
	// （典型是误写成 https URL），而不是猜一个主机名出来。
	if strings.Contains(s, "/") {
		return "", fmt.Errorf("ice: STUN URL %q 不能带路径", raw)
	}

	if _, _, err := net.SplitHostPort(s); err == nil {
		return s, nil
	}
	// 没有端口：整串就是主机（IPv6 字面量交给 JoinHostPort 补方括号）。
	return net.JoinHostPort(s, defaultSTUNPort), nil
}

// hostOf 取 URL 里的主机名（小写、去方括号）；解析失败返回空串。
func hostOf(raw string) string {
	hostport, err := parseSTUNURL(raw)
	if err != nil {
		return ""
	}
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.Trim(host, "[]"))
}
