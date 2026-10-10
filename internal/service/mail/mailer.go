// Package mail 只负责一件事：**把一封纯文本邮件投出去**。
//
// 三条边界：
//
//  1. **不认识业务**。它不知道有账号、有令牌、有链接；只认 To/Subject/Body。
//     这样"邮件里放了什么"这件事只有业务层一个 owner，本包无法成为第二处。
//  2. **只依赖标准库**（net/smtp + crypto/tls + mime）。不引入第三方发信库：
//     本仓库的硬约束是不新增依赖，而"连服务器、AUTH、发一封信"用标准库就够了。
//  3. **绝不在日志里打印收件人以外的内容**。见 Outbound.Tag 的说明。
package mail

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// TLSMode 是发信通道的加密形态。
//
// 三种都要支持的原因：QQ 邮箱的标准端点是 465 + 隐式 SSL，但 587 + STARTTLS
// 同样可用（用户实测两个端口都能连、25 被封），而排障时需要能临时关掉加密
// 连一台本机的假 SMTP 服务器（`none`）。
type TLSMode string

const (
	// TLSImplicit：连上就是 TLS（465）。连接建立后直接开始 TLS 握手，没有明文阶段。
	TLSImplicit TLSMode = "ssl"
	// TLSStartTLS：先明文连、EHLO 之后升级为 TLS（587）。
	TLSStartTLS TLSMode = "starttls"
	// TLSNone：完全明文（只用于本机排障 —— 见 SMTPSender 里的硬校验）。
	TLSNone TLSMode = "none"
)

// Outbound 是一封待发的邮件。
//
// **Tag 是日志里唯一能出现的东西**：To 是隐私、Body 里可能带一次性 token 明文。
// 调用方把"给谁发的、为什么发的"压缩进 Tag（例如 "password_reset user=42"），
// 本包只打 Tag。这一条是需求里"日志里不打印 token 明文"的落点 ——
// 靠"记得别打"不可靠，靠"这个类型里没有别的字段被打"才可靠。
type Outbound struct {
	To      string
	Subject string
	Body    string
	Tag     string
}

// Sender 是投递能力的接口（唯一的实现是 *SMTPSender；测试注入假实现）。
type Sender interface {
	Send(ctx context.Context, msg Outbound) error
}

// SMTPOptions 是 SMTP 投递的全部参数（由 config 的 AuthConfig 映射过来）。
type SMTPOptions struct {
	Host     string
	Port     int
	User     string
	Pass     string
	From     string
	FromName string
	TLS      TLSMode
	Timeout  time.Duration

	// TLSRootCAs 是校验证书用的根池；nil = 系统根。
	//
	// 它是**信任范围收窄**的旋钮，不是"跳过校验"的开关（没有 InsecureSkipVerify）：
	// 用于自建 CA 的 SMTP 服务器，以及测试里那台自签名的假服务器
	//（与 service.AccountOptions.Now 同一类：一个真实存在、但不从环境变量暴露的注入点）。
	// 生产默认 nil，也就是"只信系统根"。
	TLSRootCAs *x509.CertPool
}

// tlsConfig 返回连接用的 TLS 配置（隐式 SSL 与 STARTTLS 共用一份）。
func (s *SMTPSender) tlsConfig() *tls.Config {
	return &tls.Config{
		ServerName: s.opts.Host,
		MinVersion: tls.VersionTLS12,
		RootCAs:    s.opts.TLSRootCAs,
	}
}

// SMTPSender 用 net/smtp 投递。
//
// 为什么每条连接都是"新建 → 投一封 → QUIT"（不做连接池）：
// 发信的频率是"每次找回请求一封"，量级是每分钟个位数；而长连接要处理
// 服务器单方面超时、QUIT 失败、AUTH 过期等一堆状态。用一次往返换掉全部状态机，
// 是这个量级上明显更划算的取舍。
type SMTPSender struct {
	opts SMTPOptions
}

// NewSMTPSender 构造投递器，并在**启动期**把三类配置错误拦下来。
//
// 三条硬校验的依据：
//  1. `TLS=none` 且 host 不是回环 → 直接拒。原因是 net/smtp 的 PlainAuth 会拒绝在
//     非 TLS 连接上发送口令（见下面的 Send）。与其让第一封邮件在运行期失败，
//     不如在这里说清楚"这个组合本就不可能工作"。
//  2. From 与 User 不同：QQ 要求发件人必须等于认证账号（否则 550）。
//     配错时用户看到的是"信发出去了但收不到"，极难归因，所以在启动期就吵。
//     From 为空时默认取 User（config 层已经这么填，这里再兜一次）。
//  3. 端口必须落在合法区间 —— 写错端口的表现是连接超时，与"网络不通"无法区分。
func NewSMTPSender(opts SMTPOptions) (*SMTPSender, error) {
	opts.Host = strings.TrimSpace(opts.Host)
	opts.User = strings.TrimSpace(opts.User)
	opts.From = strings.TrimSpace(opts.From)
	opts.FromName = strings.TrimSpace(opts.FromName)
	if opts.TLS == "" {
		opts.TLS = TLSImplicit
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}

	switch {
	case opts.Host == "":
		return nil, errors.New("mail: 缺少 SMTP host")
	case opts.Port <= 0 || opts.Port > 65535:
		return nil, fmt.Errorf("mail: SMTP 端口不合法（%d）", opts.Port)
	}
	switch opts.TLS {
	case TLSImplicit, TLSStartTLS, TLSNone:
	default:
		return nil, fmt.Errorf("mail: 非法的 TLS 模式 %q（允许 %s / %s / %s）",
			opts.TLS, TLSImplicit, TLSStartTLS, TLSNone)
	}
	if opts.TLS == TLSNone && !isLoopbackHost(opts.Host) {
		return nil, fmt.Errorf(
			"mail: PR_SMTP_TLS=%s 只允许配合回环地址使用（当前 host=%q）："+
				"net/smtp 的 PlainAuth 会拒绝在明文连接上发送口令（unencrypted connection），"+
				"这封邮件在运行期必然失败。排障请连本机假 SMTP 服务器，正式环境请用 %s 或 %s",
			TLSNone, opts.Host, TLSImplicit, TLSStartTLS)
	}
	if opts.From == "" {
		opts.From = opts.User
	}
	if opts.From == "" {
		return nil, errors.New("mail: 缺少发件人（PR_SMTP_FROM 与 PR_SMTP_USER 都为空）")
	}
	if !strings.Contains(opts.From, "@") || strings.ContainsAny(opts.From, "\r\n") {
		return nil, fmt.Errorf("mail: 发件人地址不合法（%q）", opts.From)
	}

	return &SMTPSender{opts: opts}, nil
}

// Address 返回 host:port（排障与日志用；不含任何凭据）。
func (s *SMTPSender) Address() string {
	if s == nil {
		return ""
	}
	return net.JoinHostPort(s.opts.Host, strconv.Itoa(s.opts.Port))
}

// Send 投递一封邮件。
//
// 连接形态与认证（两处容易踩的地方，逐条记下来）：
//
//   - **隐式 SSL（465）**：用 tls.Dial 拿到 *tls.Conn 再交给 smtp.NewClient。
//     它之所以可行，是因为 smtp.NewClient 里有 `_, c.tls = conn.(*tls.Conn)`
//     —— 只在 StartTLS 成功后才置位的那个内部标志，对"已经是 *tls.Conn 的连接"
//     同样会置位（这一条是读 GOROOT 的 net/smtp/smtp.go 确认的，不是推测）。
//     于是 PlainAuth 的 `!server.TLS && !isLocalhost(server.Name)` 检查能通过。
//   - **PlainAuth 的 host 必须与 NewClient 的 host 逐字相同**：PlainAuth.Start 里有
//     `if server.Name != a.host { return errors.New("wrong host name") }`，
//     而 server.Name 来自 smtp.NewClient 的第二个参数。两者传同一个字符串
//     （下面的 s.opts.Host）是硬要求，否则认证会在一个看不出原因的文案上失败。
//
// 超时：net/smtp 自己不带任何超时，所以整条连接压一个 SetDeadline
// （由 ctx 的 deadline 与配置的 Timeout 取更早的那个）。
func (s *SMTPSender) Send(ctx context.Context, msg Outbound) error {
	if s == nil {
		return errors.New("mail: 投递器未初始化")
	}
	payload, err := s.buildMessage(msg)
	if err != nil {
		return err
	}

	deadline := time.Now().Add(s.opts.Timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}

	addr := s.Address()
	dialer := &net.Dialer{Timeout: s.opts.Timeout}

	var conn net.Conn
	if s.opts.TLS == TLSImplicit {
		// ServerName 必须显式给出（隐式 SSL 下没有"先明文再升级"的机会去推导它），
		// 否则证书校验会拿 IP 去比对，直接失败。
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, s.tlsConfig())
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("mail: 连接 %s 失败: %w", addr, err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("mail: 设置连接超时失败: %w", err)
	}

	c, err := smtp.NewClient(conn, s.opts.Host)
	if err != nil {
		return fmt.Errorf("mail: 建立 SMTP 会话失败: %w", err)
	}
	defer func() { _ = c.Close() }()

	if s.opts.TLS == TLSStartTLS {
		ok, _ := c.Extension("STARTTLS")
		if !ok {
			// 静默降级为明文会把口令发出去，这是最不能容忍的一种"容错"。
			return errors.New("mail: 服务器未声明 STARTTLS，拒绝在明文连接上继续（凭据会外泄）")
		}
		if err := c.StartTLS(s.tlsConfig()); err != nil {
			return fmt.Errorf("mail: STARTTLS 升级失败: %w", err)
		}
	}

	if s.opts.User != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return errors.New("mail: 服务器未声明 AUTH，无法用账号密码登录")
		}
		if err := c.Auth(smtp.PlainAuth("", s.opts.User, s.opts.Pass, s.opts.Host)); err != nil {
			// 错误里**不含**口令（PlainAuth 自己也不会回显），但历史上认证失败的文案
			// 常常被误当成"服务器拒绝" —— 这里补一句可操作的提示。
			return fmt.Errorf("mail: SMTP 认证失败（请确认用的是授权码而不是登录密码，且发件人=%s）: %w", s.opts.From, err)
		}
	}

	if err := c.Mail(s.opts.From); err != nil {
		return fmt.Errorf("mail: MAIL FROM 被拒绝: %w", err)
	}
	if err := c.Rcpt(msg.To); err != nil {
		return fmt.Errorf("mail: RCPT TO 被拒绝: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("mail: DATA 被拒绝: %w", err)
	}
	if _, err := w.Write(payload); err != nil {
		_ = w.Close()
		return fmt.Errorf("mail: 写入正文失败: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mail: 结束正文失败: %w", err)
	}
	return c.Quit()
}

// buildMessage 组装 DATA 段的字节（头部 + 正文）。
//
// 四个安全/兼容要点：
//
//  1. **头注入**：To 与 Subject 里的 CR/LF 一律拒绝。From 只来自配置。
//     正文永远不会被拼进头部 —— 头部只由本函数按固定模板生成。
//  2. **非 ASCII 头部**：发件人名（默认 `月喵`）与中文主题必须做 RFC 2047 编码，
//     否则头部里出现裸 8-bit 字节，轻则乱码重则被拒。用标准库的 mime.QEncoding。
//  3. **正文用 base64**：正文里有中文与一次性链接。base64 一次性绕开三个坑 ——
//     8BITMIME 协商（服务器没声明就发裸 UTF-8 是不合规的）、单行 998 字节上限、
//     以及行首 '.' 的点填充。代价是正文不可直接肉眼读（要看信的内容就打开它）。
//  4. **行尾一律是 CRLF**（SMTP 的硬要求，LF-only 会被某些服务器判为畸形）。
func (s *SMTPSender) buildMessage(msg Outbound) ([]byte, error) {
	to := strings.TrimSpace(msg.To)
	if to == "" {
		return nil, errors.New("mail: 缺少收件人")
	}
	if strings.ContainsAny(to, "\r\n\x00") || strings.ContainsAny(to, " \t") {
		return nil, fmt.Errorf("mail: 收件人地址含非法字符（%q）", to)
	}
	if !strings.Contains(to, "@") || strings.HasPrefix(to, "@") || strings.HasSuffix(to, "@") {
		return nil, fmt.Errorf("mail: 收件人地址形状不合法（%q）", to)
	}
	subject := strings.TrimSpace(msg.Subject)
	if strings.ContainsAny(subject, "\r\n") {
		return nil, errors.New("mail: 主题含换行（邮件头注入）")
	}

	from := s.opts.From
	if s.opts.FromName != "" {
		// FromName 是我们自己的配置值，但它同样是头部的一部分 —— 一并编码，
		// 顺带让"配了个带换行的名字"也在编码层失去注入能力。
		from = fmt.Sprintf("%s <%s>", mime.QEncoding.Encode("utf-8", s.opts.FromName), s.opts.From)
	}

	var b strings.Builder
	writeHeader := func(k, v string) {
		b.WriteString(k)
		b.WriteString(": ")
		b.WriteString(v)
		b.WriteString("\r\n")
	}
	writeHeader("From", from)
	writeHeader("To", to)
	if subject != "" {
		writeHeader("Subject", mime.QEncoding.Encode("utf-8", subject))
	}
	writeHeader("Date", time.Now().Format(time.RFC1123Z))
	writeHeader("Message-ID", messageID(s.opts.From))
	writeHeader("MIME-Version", "1.0")
	writeHeader("Content-Type", `text/plain; charset="UTF-8"`)
	writeHeader("Content-Transfer-Encoding", "base64")
	b.WriteString("\r\n")

	// base64 的标准行宽是 76（RFC 2045）。
	encoded := base64.StdEncoding.EncodeToString([]byte(normalizeCRLF(msg.Body)))
	for len(encoded) > 76 {
		b.WriteString(encoded[:76])
		b.WriteString("\r\n")
		encoded = encoded[76:]
	}
	b.WriteString(encoded)
	b.WriteString("\r\n")

	return []byte(b.String()), nil
}

// normalizeCRLF 把正文的行尾统一成 CRLF（SMTP 的硬要求）。
func normalizeCRLF(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

// messageID 造一个形如 <随机@域名> 的 Message-ID。
//
// 为什么值得有：没有它时，部分收信方（尤其同一主题的批量信）会按内容指纹去重，
// 结果是"重发忘了密码"的邮件被静默丢弃 —— 而那正是排障时最容易被误判成
// "服务端没发"的现象。
func messageID(from string) string {
	domain := "localhost"
	if at := strings.LastIndex(from, "@"); at >= 0 && at+1 < len(from) {
		domain = from[at+1:]
	}
	// 时间戳 + 进程内自增值足够：Message-ID 只需在本机内唯一，
	// 不需要密码学随机，也不需要跨进程唯一。
	seq := msgIDSeq.Add(1)
	return fmt.Sprintf("<%d.%d@%s>", time.Now().UnixNano(), seq, domain)
}

// msgIDSeq 是 Message-ID 的进程内序号（见 messageID）。
var msgIDSeq atomic.Int64

// isLoopbackHost 判断 host 是否指向本机（用于 TLS=none 的硬校验）。
func isLoopbackHost(host string) bool {
	h := strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	if h == "localhost" {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
