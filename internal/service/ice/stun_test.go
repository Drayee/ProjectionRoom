package ice

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// fakeSTUN 是一个最小可用的本地 STUN 服务器：收到 Binding Request 就按 RFC 5389
// 回一条带 XOR-MAPPED-ADDRESS 的 Success Response。
//
// 为什么在单测里自己起一台：真机探公网 STUN 会依赖外网与那几台服务器的可用性，
// 那种断言无法复现。真实网络的表现由 README 里的实测记录覆盖，
// 这里只锁定"报文构造/校验/超时"这些我们自己负责的语义。
type fakeSTUN struct {
	conn net.PacketConn
	// badTxID 为 true 时故意回错事务 ID（验证"不匹配就不算成功"）。
	badTxID bool
	// silent 为 true 时收到请求也不回应（模拟无响应的 STUN，例如 stun.qq.com）。
	silent bool
}

// startFakeSTUN 起一台本地假 STUN，返回它的本地地址（127.0.0.1:port）。
func startFakeSTUN(t *testing.T, mutate func(*fakeSTUN)) string {
	t.Helper()

	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起本地假 STUN 失败: %v", err)
	}

	f := &fakeSTUN{conn: conn}
	if mutate != nil {
		mutate(f)
	}
	go f.serve()
	t.Cleanup(func() { _ = conn.Close() })

	return conn.LocalAddr().String()
}

func (f *fakeSTUN) serve() {
	buf := make([]byte, 1024)
	for {
		n, addr, err := f.conn.ReadFrom(buf)
		if err != nil {
			return // 连接已关闭
		}
		if n < stunHeaderLen || f.silent {
			continue
		}

		var txID [12]byte
		copy(txID[:], buf[8:20])
		if f.badTxID {
			txID[0] ^= 0xFF
		}

		// 顺带回一个真实形状的 XOR-MAPPED-ADDRESS，确保"带属性也能被正确识别"。
		if _, err := f.conn.WriteTo(buildBindingSuccess(txID, xorMappedAttr(addr)), addr); err != nil {
			return
		}
	}
}

// buildBindingSuccess 构造一条 RFC 5389 的 Binding Success Response。
func buildBindingSuccess(txID [12]byte, attrs []byte) []byte {
	b := make([]byte, stunHeaderLen+len(attrs))
	binary.BigEndian.PutUint16(b[0:2], stunBindingSuccess)
	binary.BigEndian.PutUint16(b[2:4], uint16(len(attrs)))
	binary.BigEndian.PutUint32(b[4:8], stunMagicCookie)
	copy(b[8:20], txID[:])
	copy(b[20:], attrs)
	return b
}

// xorMappedAttr 按 RFC 5389 §15.2 构造 XOR-MAPPED-ADDRESS（IPv4）。
func xorMappedAttr(addr net.Addr) []byte {
	udp, ok := addr.(*net.UDPAddr)
	if !ok {
		return nil
	}
	ip4 := udp.IP.To4()
	if ip4 == nil {
		return nil
	}

	attr := make([]byte, 12)
	binary.BigEndian.PutUint16(attr[0:2], 0x0020) // XOR-MAPPED-ADDRESS
	binary.BigEndian.PutUint16(attr[2:4], 8)
	attr[4] = 0 // reserved
	attr[5] = 0x01
	binary.BigEndian.PutUint16(attr[6:8], uint16(udp.Port)^uint16(stunMagicCookie>>16))
	xor := binary.BigEndian.Uint32(ip4) ^ stunMagicCookie
	binary.BigEndian.PutUint32(attr[8:12], xor)
	return attr
}

// closedUDPAddr 返回一个刚刚被释放的本地 UDP 地址（用来模拟"没人应答"）。
func closedUDPAddr(t *testing.T) string {
	t.Helper()

	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("申请临时端口失败: %v", err)
	}
	addr := conn.LocalAddr().String()
	_ = conn.Close()

	return addr
}

// TestUDPProberSuccess 一次真实的本机 UDP 往返：成功、拿到 RTT、识别出带属性的响应。
func TestUDPProberSuccess(t *testing.T) {
	addr := startFakeSTUN(t, nil)
	prober := NewUDPProber(ProbeTimeout)

	got := prober.Probe(context.Background(), []string{"stun:" + addr})
	if len(got) != 1 {
		t.Fatalf("应返回 1 条观测，实际 %d", len(got))
	}
	if !got[0].OK {
		t.Fatalf("本机假 STUN 应当应答成功：%+v", got[0])
	}
	if got[0].RTT < 0 || got[0].RTT > ProbeTimeout {
		t.Fatalf("RTT 应落在 (0, 1.5s]，实际 %v", got[0].RTT)
	}
	if got[0].URL != "stun:"+addr {
		t.Fatalf("观测应带上原样的 URL，实际 %q", got[0].URL)
	}
}

// TestUDPProberRejectsMismatchedTransaction 事务 ID 对不上的响应不算成功。
// 这条保证"同一时间窗里多条探测不会互相认领对方的响应"。
func TestUDPProberRejectsMismatchedTransaction(t *testing.T) {
	addr := startFakeSTUN(t, func(f *fakeSTUN) { f.badTxID = true })

	// 用短超时让用例快速结束（生产用 1.5s）。
	prober := NewUDPProber(200 * time.Millisecond)
	got := prober.Probe(context.Background(), []string{"stun:" + addr})

	if got[0].OK {
		t.Fatalf("事务 ID 不匹配时不应判成功：%+v", got[0])
	}
	if got[0].RTT != 0 {
		t.Fatalf("失败时 RTT 应为 0，实际 %v", got[0].RTT)
	}
}

// TestUDPProberTimesOutOnSilentServer 无响应的服务器（stun.qq.com 的本地等价物）
// 必须在预算内判定失败，而不是挂住整轮探测。
func TestUDPProberTimesOutOnSilentServer(t *testing.T) {
	addr := startFakeSTUN(t, func(f *fakeSTUN) { f.silent = true })
	prober := NewUDPProber(200 * time.Millisecond)

	start := time.Now()
	got := prober.Probe(context.Background(), []string{"stun:" + addr})
	elapsed := time.Since(start)

	if got[0].OK {
		t.Fatalf("无响应服务器不该判成功：%+v", got[0])
	}
	if elapsed > time.Second {
		t.Fatalf("单条超时预算 200ms 却用了 %v", elapsed)
	}
}

// TestUDPProberFailsOnClosedPort 没人监听的端口要么收到 ICMP 不可达、要么读超时，
// 两种都必须归为"失败"而不是 panic 或挂死。
func TestUDPProberFailsOnClosedPort(t *testing.T) {
	prober := NewUDPProber(200 * time.Millisecond)
	got := prober.Probe(context.Background(), []string{"stun:" + closedUDPAddr(t)})

	if got[0].OK {
		t.Fatalf("没有监听方的端口不该判成功：%+v", got[0])
	}
}

// TestUDPProberIsConcurrent 并发是契约的一部分：三条各 500ms 超时的条目，
// 总耗时应该接近 500ms 而不是 1500ms（否则 7 条默认列表会把一轮探测拖到 10s）。
func TestUDPProberIsConcurrent(t *testing.T) {
	urls := []string{
		"stun:" + startFakeSTUN(t, func(f *fakeSTUN) { f.silent = true }),
		"stun:" + startFakeSTUN(t, func(f *fakeSTUN) { f.silent = true }),
		"stun:" + startFakeSTUN(t, func(f *fakeSTUN) { f.silent = true }),
	}
	prober := NewUDPProber(500 * time.Millisecond)

	start := time.Now()
	got := prober.Probe(context.Background(), urls)
	elapsed := time.Since(start)

	if len(got) != len(urls) {
		t.Fatalf("应返回 %d 条观测，实际 %d", len(urls), len(got))
	}
	for _, o := range got {
		if o.OK {
			t.Fatalf("silent 服务器不该判成功：%+v", o)
		}
	}
	if elapsed > 1200*time.Millisecond {
		t.Fatalf("三条并发探测耗时 %v，看起来是串行的（并发应接近 500ms）", elapsed)
	}
}

// TestUDPProberBadURL 非法/空 URL 归为失败，不影响同一批里的其它条目。
func TestUDPProberBadURL(t *testing.T) {
	prober := NewUDPProber(200 * time.Millisecond)
	got := prober.Probe(context.Background(), []string{"stun:", "", "not-a-url"})

	if len(got) != 3 {
		t.Fatalf("应保持与入参等长，实际 %d", len(got))
	}
	for _, o := range got {
		if o.OK {
			t.Fatalf("非法 URL 不该判成功：%+v", o)
		}
	}
}

// TestUDPProberEmptyList 空列表返回空结果（首窗口前的降级路径会走到这里）。
func TestUDPProberEmptyList(t *testing.T) {
	prober := NewUDPProber(ProbeTimeout)
	if got := prober.Probe(context.Background(), nil); len(got) != 0 {
		t.Fatalf("空列表应返回空结果，实际 %v", got)
	}
}

// TestParseSTUNURL 锁定 URL 解析：scheme、缺省端口、查询串、错误输入。
func TestParseSTUNURL(t *testing.T) {
	ok := []struct{ in, want string }{
		{"stun:stun.l.google.com:19302", "stun.l.google.com:19302"},
		{"stun:stun.douyucdn.cn:18000", "stun.douyucdn.cn:18000"},
		{"stuns:stun.example.com", "stun.example.com:3478"},
		{"stun:stun.example.com", "stun.example.com:3478"},
		{"STUN:Stun.Example.com:3478", "Stun.Example.com:3478"},
		{"  stun:stun.example.com:3478?transport=udp  ", "stun.example.com:3478"},
		{"stun:127.0.0.1:3478", "127.0.0.1:3478"},
	}
	for _, tc := range ok {
		got, err := parseSTUNURL(tc.in)
		if err != nil {
			t.Fatalf("parseSTUNURL(%q) 不应报错: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("parseSTUNURL(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}

	for _, bad := range []string{"", "   ", "stun:", "stuns:", "stun:host/path", "stun://host"} {
		if _, err := parseSTUNURL(bad); err == nil {
			t.Fatalf("parseSTUNURL(%q) 应当报错", bad)
		}
	}
}

// TestBuildBindingRequestShape 报文形状：20 字节、类型 0x0001、magic cookie、事务 ID。
func TestBuildBindingRequestShape(t *testing.T) {
	var txID [12]byte
	for i := range txID {
		txID[i] = byte(i)
	}

	req := buildBindingRequest(txID)

	if len(req) != stunHeaderLen {
		t.Fatalf("Binding Request 应为 20 字节，实际 %d", len(req))
	}
	if binary.BigEndian.Uint16(req[0:2]) != stunBindingRequest {
		t.Fatalf("消息类型应为 0x0001，实际 0x%04x", binary.BigEndian.Uint16(req[0:2]))
	}
	if binary.BigEndian.Uint16(req[2:4]) != 0 {
		t.Fatalf("无属性请求的属性长度应为 0，实际 %d", binary.BigEndian.Uint16(req[2:4]))
	}
	if binary.BigEndian.Uint32(req[4:8]) != 0x2112A442 {
		t.Fatalf("magic cookie 应为 0x2112A442，实际 0x%08x", binary.BigEndian.Uint32(req[4:8]))
	}
	for i := 0; i < 12; i++ {
		if req[8+i] != byte(i) {
			t.Fatalf("事务 ID 第 %d 字节被改了：%02x", i, req[8+i])
		}
	}
}

// TestIsBindingSuccess 校验逻辑的边界：短包、错类型、错 cookie、错事务 ID、属性长度溢出。
func TestIsBindingSuccess(t *testing.T) {
	txID := [12]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	base := buildBindingSuccess(txID, nil)

	if !isBindingSuccess(base, txID) {
		t.Fatal("合法的 Success Response 应被接受")
	}

	short := base[:19]
	if isBindingSuccess(short, txID) {
		t.Fatal("不足 20 字节的包不该被接受")
	}

	wrongType := append([]byte(nil), base...)
	binary.BigEndian.PutUint16(wrongType[0:2], stunBindingRequest)
	if isBindingSuccess(wrongType, txID) {
		t.Fatal("Binding Request 不该被当成响应")
	}

	wrongCookie := append([]byte(nil), base...)
	binary.BigEndian.PutUint32(wrongCookie[4:8], 0xDEADBEEF)
	if isBindingSuccess(wrongCookie, txID) {
		t.Fatal("cookie 不符不该被接受")
	}

	otherTx := txID
	otherTx[0]++
	if isBindingSuccess(base, otherTx) {
		t.Fatal("事务 ID 不符不该被接受")
	}

	overflow := append([]byte(nil), base...)
	binary.BigEndian.PutUint16(overflow[2:4], 4096) // 声明的属性长度远超实际
	if isBindingSuccess(overflow, txID) {
		t.Fatal("属性长度溢出的包不该被接受")
	}
}

// TestNewTransactionIDIsRandom 事务 ID 必须每次不同，否则并发探测会互相认领响应。
func TestNewTransactionIDIsRandom(t *testing.T) {
	seen := make(map[[12]byte]bool)
	for i := 0; i < 64; i++ {
		id, err := newTransactionID()
		if err != nil {
			t.Fatalf("生成事务 ID 失败: %v", err)
		}
		if seen[id] {
			t.Fatal("64 次生成里出现了重复的事务 ID")
		}
		seen[id] = true
	}
}
