package mail

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// —— 投递通道：一条**可执行**的证据 ——
//
// 这里起一台本机的假 SMTP 服务器，把"隐式 SSL(465) + PlainAuth"这条组合
// 端到端跑一遍。它证明的是一件很容易靠记忆搞错的事：
//
//	net/smtp 的 PlainAuth 在 `!server.TLS && !isLocalhost(server.Name)` 时会直接
//	报 "unencrypted connection"，而隐式 SSL 下我们**没有**调用 StartTLS
//	（那是 STARTTLS 模式才有的动作）—— 那么 AUTH 凭什么能过？
//	因为 smtp.NewClient 里有 `_, c.tls = conn.(*tls.Conn)`：把一条已经是
//	TLS 的连接交给它，那个内部标志同样会置位（读 GOROOT 的 net/smtp/smtp.go 确认）。
//
// 源码阅读会被"驱动/标准库换版本"推翻，所以这里用一台真服务器把它钉成断言。
// 同时它也顺带验了：EHLO/AUTH/MAIL/RCPT/DATA 全流程、我们写进 DATA 段的字节。

// startFakeSMTPServer 起一台本机假 SMTP 服务器。
//
// implicitTLS=true 时用 tls.Listen（465 的形态）；false 时先明文，等客户端发
// STARTTLS 再升级（587 的形态）。
func startFakeSMTPServer(t *testing.T, implicitTLS bool) (port int, pool *x509.CertPool, received func() []string) {
	t.Helper()

	cert, pool := selfSignedCert(t)
	cfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}

	var (
		ln  net.Listener
		err error
	)
	if implicitTLS {
		ln, err = tls.Listen("tcp", "127.0.0.1:0", cfg)
	} else {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		t.Fatalf("监听失败：%v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	var (
		mu    sync.Mutex
		lines []string
	)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveFakeSMTP(conn, cfg, implicitTLS, func(l string) {
				mu.Lock()
				lines = append(lines, l)
				mu.Unlock()
			})
		}
	}()

	addr := ln.Addr().(*net.TCPAddr)
	return addr.Port, pool, func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := make([]string, len(lines))
		copy(out, lines)
		return out
	}
}

// serveFakeSMTP 实现刚好够用的 SMTP 对话（不解析 MIME，只管协议）。
func serveFakeSMTP(conn net.Conn, cfg *tls.Config, implicitTLS bool, record func(string)) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	write := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
	write("220 fake ESMTP ready")

	// 隐式 SSL 下连接已经是 TLS；STARTTLS 模式下要等客户端升级。
	// 升级之后必须**换掉**读写对象，否则后续对话仍然走明文连接（会静默失配）。
	upgraded := implicitTLS

	r := bufio.NewReader(conn)
	inData := false
	var data strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if inData {
			// DATA 段：以单独一个 "." 结束。
			if line == "." {
				inData = false
				record(data.String())
				data.Reset()
				write("250 2.0.0 Ok: queued")
				continue
			}
			data.WriteString(line)
			data.WriteString("\n")
			continue
		}

		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "EHLO"):
			write("250-fake greets you")
			write("250-AUTH PLAIN")
			write("250-8BITMIME")
			if !upgraded {
				write("250-STARTTLS")
			}
			write("250 SIZE 10485760")
		case strings.HasPrefix(upper, "HELO"):
			write("250 fake")
		case strings.HasPrefix(upper, "STARTTLS"):
			write("220 2.0.0 Ready to start TLS")
			tlsConn := tls.Server(conn, cfg)
			if err := tlsConn.Handshake(); err != nil {
				return
			}
			conn = tlsConn
			r = bufio.NewReader(conn)
			upgraded = true
		case strings.HasPrefix(upper, "AUTH PLAIN"):
			// net/smtp 的 PlainAuth 把 `\0user\0pass` 一起发在 AUTH 行里。
			parts := strings.SplitN(line, " ", 3)
			if len(parts) < 3 {
				write("535 5.7.8 Missing credentials")
				continue
			}
			raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(parts[2]))
			if err != nil {
				write("535 5.7.8 Bad encoding")
				continue
			}
			record("AUTH " + string(raw))
			write("235 2.7.0 Accepted")
		case strings.HasPrefix(upper, "MAIL FROM"):
			record(line)
			write("250 2.1.0 Ok")
		case strings.HasPrefix(upper, "RCPT TO"):
			record(line)
			write("250 2.1.5 Ok")
		case upper == "DATA":
			write("354 End data with <CR><LF>.<CR><LF>")
			inData = true
		case strings.HasPrefix(upper, "QUIT"):
			write("221 2.0.0 Bye")
			return
		default:
			write("250 2.0.0 Ok")
		}
	}
}

// selfSignedCert 生成一张覆盖 127.0.0.1 的自签证书，并给出只信任它的根池。
func selfSignedCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成密钥失败：%v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "fake-smtp"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("自签证书失败：%v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("解析自签证书失败：%v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

// TestSendOverImplicitTLS 是那条"靠读源码得出、再用真服务器钉住"的证据：
// 465 形态（tls.Dial + smtp.NewClient）下的 PlainAuth **能通过**。
func TestSendOverImplicitTLS(t *testing.T) {
	port, pool, received := startFakeSMTPServer(t, true)
	sender, err := NewSMTPSender(SMTPOptions{
		Host: "127.0.0.1", Port: port,
		User: "sender@example.com", Pass: "auth-code",
		From: "sender@example.com", FromName: "月喵",
		TLS: TLSImplicit, Timeout: 5 * time.Second, TLSRootCAs: pool,
	})
	if err != nil {
		t.Fatalf("构造投递器失败：%v", err)
	}

	body := "月喵 · 重置密码\r\n\r\n链接： https://pr.example.com/reset#token=ABC123\r\n"
	if err := sender.Send(context.Background(), Outbound{
		To: "drayee@qq.com", Subject: "月喵：重置密码", Body: body,
	}); err != nil {
		t.Fatalf("隐式 SSL 投递应当成功（PlainAuth 必须能过）：%v", err)
	}

	log := strings.Join(received(), "\n")
	if !strings.Contains(log, "AUTH \x00sender@example.com\x00auth-code") {
		t.Fatalf("AUTH PLAIN 没有按预期发送：\n%s", log)
	}
	if !strings.Contains(log, "MAIL FROM:<sender@example.com>") {
		t.Fatalf("MAIL FROM 不对：\n%s", log)
	}
	if !strings.Contains(log, "RCPT TO:<drayee@qq.com>") {
		t.Fatalf("RCPT TO 不对：\n%s", log)
	}
	// 发件人名与主题必须是 RFC 2047 编码（含中文的裸 8-bit 头部会被拒或乱码）。
	if !strings.Contains(log, "From: =?utf-8?") {
		t.Fatalf("From 的显示名没有做 RFC 2047 编码：\n%s", log)
	}
	if !strings.Contains(log, "Subject: =?utf-8?") {
		t.Fatalf("Subject 没有做 RFC 2047 编码：\n%s", log)
	}
	// 正文必须是 base64，且解出来逐字等于原文（含 CRLF 归一化后的形态）。
	gotBody, ok := decodeBase64Body(log)
	if !ok {
		t.Fatalf("DATA 段里找不到 base64 正文：\n%s", log)
	}
	if gotBody != body {
		t.Fatalf("正文往返不一致：\nwant=%q\ngot =%q", body, gotBody)
	}
}

// TestSendOverStartTLS 验 587 形态（明文连上 → EHLO → STARTTLS → 升级后再认证）。
func TestSendOverStartTLS(t *testing.T) {
	port, pool, received := startFakeSMTPServer(t, false)
	sender, err := NewSMTPSender(SMTPOptions{
		Host: "127.0.0.1", Port: port,
		User: "sender@example.com", Pass: "auth-code",
		From: "sender@example.com", FromName: "月喵",
		TLS: TLSStartTLS, Timeout: 5 * time.Second, TLSRootCAs: pool,
	})
	if err != nil {
		t.Fatalf("构造投递器失败：%v", err)
	}
	if err := sender.Send(context.Background(), Outbound{
		To: "drayee@qq.com", Subject: "验证你的邮箱", Body: "https://pr.example.com/verify#token=XYZ\n",
	}); err != nil {
		t.Fatalf("STARTTLS 投递应当成功：%v", err)
	}
	if log := strings.Join(received(), "\n"); !strings.Contains(log, "RCPT TO:<drayee@qq.com>") {
		t.Fatalf("STARTTLS 模式的投递没走到 RCPT：\n%s", log)
	}
}

// decodeBase64Body 从 DATA 段里取出 base64 正文并解码。
func decodeBase64Body(log string) (string, bool) {
	const marker = "Content-Transfer-Encoding: base64"
	i := strings.Index(log, marker)
	if i < 0 {
		return "", false
	}
	rest := strings.TrimLeft(log[i+len(marker):], "\r\n")
	// 跳过头与正文之间的空行。
	lines := strings.Split(rest, "\n")
	start := 0
	for idx, l := range lines {
		if strings.TrimSpace(l) != "" {
			start = idx
			break
		}
	}
	if start >= len(lines) {
		return "", false
	}
	var b strings.Builder
	for _, l := range lines[start:] {
		l = strings.TrimSpace(l)
		if l == "" {
			break
		}
		b.WriteString(l)
	}
	raw := strings.TrimSpace(b.String())
	if raw == "" {
		return "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return "", false
	}
	return string(decoded), true
}

// —— 配置校验 ——

func TestNewSMTPSenderRejectsUnusableConfig(t *testing.T) {
	base := SMTPOptions{Host: "smtp.qq.com", Port: 465, User: "u@qq.com", Pass: "p", TLS: TLSImplicit}
	cases := []struct {
		name    string
		mutate  func(*SMTPOptions)
		wantErr string
	}{
		{"缺 host", func(o *SMTPOptions) { o.Host = "" }, "缺少 SMTP host"},
		{"端口非法", func(o *SMTPOptions) { o.Port = 0 }, "端口不合法"},
		{"TLS 模式非法", func(o *SMTPOptions) { o.TLS = "maybe" }, "非法的 TLS 模式"},
		// 这条是关键：net/smtp 的 PlainAuth 在明文连接上会直接拒绝，
		// 所以这个组合"必然失败"，必须在启动期就拒，而不是等第一封邮件。
		{"明文配公网 host", func(o *SMTPOptions) { o.TLS = TLSNone }, "只允许配合回环地址"},
		{"From 不是地址", func(o *SMTPOptions) { o.From = "not-an-address" }, "发件人地址不合法"},
		{"From 带换行", func(o *SMTPOptions) { o.From = "a@b.com\r\nBcc: x@y.com" }, "发件人地址不合法"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := base
			tc.mutate(&opts)
			_, err := NewSMTPSender(opts)
			if err == nil {
				t.Fatalf("应当报错（%s）", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("错误文案应当提到 %q，实际 %v", tc.wantErr, err)
			}
		})
	}

	// 回环 + none 是允许的（本机排障形态）。
	if _, err := NewSMTPSender(SMTPOptions{Host: "127.0.0.1", Port: 2525, From: "a@b.com", TLS: TLSNone}); err != nil {
		t.Fatalf("回环 + TLS=none 应当允许（排障形态）：%v", err)
	}
	// From 为空时回落到 User（QQ 要求发件人等于认证账号）。
	s, err := NewSMTPSender(SMTPOptions{Host: "smtp.qq.com", Port: 465, User: "u@qq.com", TLS: TLSImplicit})
	if err != nil {
		t.Fatalf("From 为空应当回落到 User：%v", err)
	}
	if s.opts.From != "u@qq.com" {
		t.Fatalf("From 应当等于 User，实际 %q", s.opts.From)
	}
}

// —— 头注入 ——

func TestBuildMessageRejectsHeaderInjection(t *testing.T) {
	s, err := NewSMTPSender(SMTPOptions{
		Host: "smtp.qq.com", Port: 465, From: "u@qq.com", TLS: TLSImplicit, Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}

	bad := []Outbound{
		{To: "victim@x.com\r\nBcc: attacker@y.com", Body: "hi"},
		{To: "victim@x.com\nBcc: attacker@y.com", Body: "hi"},
		{To: "victim@x.com", Subject: "你好\r\nBcc: attacker@y.com", Body: "hi"},
		{To: "", Body: "hi"},
		{To: "@x.com", Body: "hi"},
		{To: "no-at-sign", Body: "hi"},
		{To: "vic tim@x.com", Body: "hi"},
	}
	for i, msg := range bad {
		if _, err := s.buildMessage(msg); err == nil {
			t.Fatalf("第 %d 条应当被拒（邮件头注入/非法地址）", i)
		}
	}

	// 合法的一封：确认 From 只来自配置、正文不会进头部。
	payload, err := s.buildMessage(Outbound{To: "ok@x.com", Subject: "月喵", Body: "From: evil@y.com\r\nBcc: z@w.com\n"})
	if err != nil {
		t.Fatalf("合法的邮件不该被拒：%v", err)
	}
	head := string(payload)
	if strings.Count(head, "From:") != 1 {
		t.Fatalf("头部里出现了第二个 From（正文渗进了头部？）：\n%s", head)
	}
	if strings.Contains(head, "Bcc:") {
		t.Fatalf("正文里的 Bcc 渗进了头部（邮件头注入）：\n%s", head)
	}
}

// —— 队列：不阻塞、满了就丢、退出时排空 ——

// blockingSender 是一个"投递很慢"的假实现（用 gate 控制什么时候放行）。
type blockingSender struct {
	gate   chan struct{}
	mu     sync.Mutex
	sent   []Outbound
	failed bool
}

func (b *blockingSender) Send(ctx context.Context, msg Outbound) error {
	if b.gate != nil {
		select {
		case <-b.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failed {
		return errBoom
	}
	b.sent = append(b.sent, msg)
	return nil
}

func (b *blockingSender) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.sent)
}

var errBoom = &boomError{}

type boomError struct{}

func (*boomError) Error() string { return "boom" }

// TestMailerEnqueueNeverBlocks 是不变量 I4 的直接判据：
// 投递被卡住时，Enqueue **必须立刻返回**（它绝不等待 SMTP）。
func TestMailerEnqueueNeverBlocks(t *testing.T) {
	gate := make(chan struct{})
	sender := &blockingSender{gate: gate}
	m, cleanup := NewMailer(sender, Options{QueueSize: 2, SendTimeout: time.Minute})
	defer cleanup()

	start := time.Now()
	if !m.Enqueue(Outbound{To: "a@x.com", Tag: "t"}) {
		t.Fatal("队列未满时应当入队成功")
	}
	// 第一封正卡在 sender 里（工作协程被占住），后续入队仍然必须是同步返回的。
	if !m.Enqueue(Outbound{To: "b@x.com", Tag: "t"}) {
		t.Fatal("第二封也应当入队成功（队列容量 2）")
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("Enqueue 被阻塞了 %s：它绝不该等投递", elapsed)
	}

	// 队列满：**返回 false 但不阻塞**，并且计入 Dropped。
	start = time.Now()
	if m.Enqueue(Outbound{To: "c@x.com", Tag: "t"}) {
		t.Fatal("队列已满时应当返回 false")
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("队列满时的 Enqueue 也被阻塞了 %s", elapsed)
	}
	if got := m.Stats().Dropped; got != 1 {
		t.Fatalf("丢弃计数应当为 1，实际 %d", got)
	}
	if got := m.QueueLen(); got != 2 {
		t.Fatalf("队内应当还有 2 封（工作协程正卡在第一封上），实际 %d", got)
	}

	close(gate)
	waitForMail(t, func() bool { return sender.count() == 2 })
}

// TestMailerDrainsOnClose 验"服务停了之后才关队列"这段语义的落点：
// Close 会把已经入队的邮件发完再返回。
func TestMailerDrainsOnClose(t *testing.T) {
	sender := &blockingSender{}
	m, _ := NewMailer(sender, Options{QueueSize: 8, SendTimeout: time.Second})

	for i := 0; i < 3; i++ {
		if !m.Enqueue(Outbound{To: "a@x.com", Tag: "t"}) {
			t.Fatalf("第 %d 封应当入队成功", i)
		}
	}
	m.Close()
	if got := sender.count(); got != 3 {
		t.Fatalf("Close 应当把队列排空（全部投递），实际投递 %d 封", got)
	}
	if got := m.Stats().Sent; got != 3 {
		t.Fatalf("Sent 计数应当为 3，实际 %d", got)
	}
}

// TestMailerRejectsAfterClose 验关队列之后的新请求**不会 panic**、
// 也不会被静默接受（send on closed channel 是这类实现最常见的崩溃形态）。
func TestMailerRejectsAfterClose(t *testing.T) {
	sender := &blockingSender{}
	m, _ := NewMailer(sender, Options{QueueSize: 4})
	m.Close()
	m.Close() // 幂等
	if m.Enqueue(Outbound{To: "a@x.com"}) {
		t.Fatal("关闭之后不该再接受投递")
	}
	if got := m.Stats().Rejected; got != 1 {
		t.Fatalf("Rejected 计数应当为 1，实际 %d", got)
	}
}

// TestMailerCountsFailures 验投递失败的计数与通知（排障要靠它）。
func TestMailerCountsFailures(t *testing.T) {
	var gotTag string
	var mu sync.Mutex
	sender := &blockingSender{failed: true}
	m, cleanup := NewMailer(sender, Options{
		QueueSize: 4,
		OnError: func(tag string, err error) {
			mu.Lock()
			gotTag = tag
			mu.Unlock()
		},
	})
	defer cleanup()

	m.Enqueue(Outbound{To: "a@x.com", Tag: "password_reset user=7"})
	waitForMail(t, func() bool { return m.Stats().Failed == 1 })
	mu.Lock()
	defer mu.Unlock()
	if gotTag != "password_reset user=7" {
		t.Fatalf("失败通知应当带上 Tag（日志里唯一允许出现的东西），实际 %q", gotTag)
	}
}

// TestNewMailerWithoutSender 验"发信出口为 nil"时的显式降级。
func TestNewMailerWithoutSender(t *testing.T) {
	m, cleanup := NewMailer(nil, Options{})
	if m != nil {
		t.Fatal("没有 sender 时应当返回 nil（调用方据此判「邮件能力未配置」）")
	}
	cleanup() // 必须是可调用的空操作
	if m.Enqueue(Outbound{}) {
		t.Fatal("nil 队列的 Enqueue 应当返回 false 而不是 panic")
	}
	var m2 *Mailer
	if m2.Enqueue(Outbound{}) {
		t.Fatal("typed nil 的 Enqueue 也应当返回 false")
	}
	if m2.Stats() != (Stats{}) {
		t.Fatal("nil 队列的 Stats 应当是零值")
	}
}

// waitForMail 轮询等待（异步投递的断言用它，避免固定 sleep）。
func waitForMail(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("等待超时")
}
