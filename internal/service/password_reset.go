package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"ProjectionRoom/internal/service/auth"
	"ProjectionRoom/internal/service/mail"
	"ProjectionRoom/internal/store"
)

// 一次性邮件链路的用例：**忘记密码**、**重置密码**、**验证邮箱**。
//
// 三条链路共用一个令牌生命周期（生成 → 只存 sha256 → 单次使用 → 到期即废），
// 差别只有 purpose、TTL 与"消费之后做什么"。
//
// # 两条不可动摇的对外性质
//
//  1. **找回入口对"账号是否存在"逐字不敏感**（见 Forgot 的说明）。响应体由 handler
//     用同一个字面量产出，本层返回的错误里也不含任何存在性信号。
//  2. **明文令牌只存在于两个地方**：写进邮件正文的那一份，以及 store 里那条
//     sha256 摘要之外的内存。日志只打 Tag（见 mail.Outbound 的说明），
//     响应体里永不出现它（那是把"单次使用"降级成"谁看到响应谁能改密"）。
//
// 本文件的方法都挂在 AccountService 上而不是另立一个服务：它们需要**同一套**
// 依赖（store / 注入时钟 / bcrypt 闸门 / 发信队列），另立服务只会让这些依赖
// 出现第二份，其中"两个 bcrypt 闸门"尤其危险（等于把哈希并发上限悄悄翻倍）。

// forgotOK 是 Forgot 的"对外成功"契约说明（真正的响应字面量在 handler）。
//
// 这里刻意不放一个 string 常量：响应体的 owner 只有一个（handler 的那个字面量），
// 在 service 里再放一份"同样的字符串"只会变成第二个可能漂移的真相。

// Forgot 处理"忘记密码"请求：给**已验证**的邮箱发一封重置信。
//
// 门控顺序（**顺序本身就是安全性质**，改动前请读完）：
//
//  1. 邮箱格式校验 → ErrBadEmail（400）。判据不依赖数据库，因此与"存不存在"无关；
//  2. 邮件能力检查 → mail.ErrMailUnavailable（503）。同样不依赖数据库；
//  3. 查库（是否命中、是否已验证、是否可用）→ **任何结果都返回 nil**；
//  4. 打不开门控就到此为止：返回 nil，不发信、不落库。
//
// 于是对外可见的只有三种结果：400（格式）/ 503（能力未配置）/ 200（其余全部）——
// 三种判据都不区分账号是否存在。发信失败、队列满、邮箱未验证、账号被封禁
// 一律只记服务端日志。
//
// 为什么"邮箱未验证"不发信：否则任何人只要在注册时填上受害者的邮箱，就能把
// 受害者的找回入口变成自己的（信发到受害者邮箱、token 却指向攻击者的账号）。
// 这一条也是"管理员绑定即已验证"存在的原因 —— 它给了老账号一条正当的补齐路径。
//
// 为什么被封禁的账号不发信：改密也登不进来（登录在口令通过之后仍会因 status 拒绝），
// 发信只会把发信配额花在无效请求上。这个门控同样不对外可见。
func (s *AccountService) Forgot(ctx context.Context, email, ip, ua string) error {
	addr, err := CanonicalEmail(email)
	if err != nil {
		return err
	}
	if s.mail == nil {
		return mail.ErrMailUnavailable
	}

	u, err := s.store.UserByEmail(ctx, addr)
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
		// 账号不存在：不落库、不发信，对外与本函数末尾完全同一条返回路径。
		return nil
	default:
		// 真错误（连接断了/超时）→ 500。这一条**不构成枚举面**：
		// 它发生在两条路径都会经过的那一次查询上，与"这个邮箱是否存在"无关。
		return err
	}

	if u.Email == nil || u.EmailVerifiedAt == nil || u.Status != store.StatusActive {
		return nil
	}

	if _, err := s.dispatchEmailToken(ctx, u, *u.Email, store.PurposePasswordReset, ip, ua); err != nil {
		// 发信链路的失败**只记日志**：对外仍然是那一个 200。
		// （唯一的例外——邮件能力未配置——在函数开头就已经返回 503 了，
		//  那是一个"服务端能力状态"，不是"这次请求的结果"。）
		if errors.Is(err, mail.ErrMailUnavailable) {
			return err
		}
		log.Printf("[WARN] 重置邮件发送流程失败：user=%d %v", u.ID, err)
	}
	return nil
}

// ResetPassword 用一次性令牌设置新口令（成功时该账号的全部会话都会被撤销）。
//
// 顺序：口令策略（廉价）→ 哈希（昂贵）→ 原子的"消费令牌 + 改密 + 撤销全部会话
// + 版本号自增"。把策略检查放在哈希之前有两个好处：不为注定被拒的请求花 CPU，
// 以及**弱口令不会烧掉令牌**（用户可以直接用同一封邮件里的链接重试一次强口令）。
// 这与需求里"弱密码沿用注册时的策略与错误码"是同一件事。
func (s *AccountService) ResetPassword(ctx context.Context, token, password string) error {
	tok := strings.TrimSpace(token)
	if tok == "" {
		return ErrResetTokenInvalid
	}
	if err := auth.ValidatePasswordPolicy(password); err != nil {
		return err
	}
	hash, err := s.acquireAndHash(password)
	if err != nil {
		return err
	}

	userID, ok, err := s.store.ConsumePasswordReset(ctx, HashOpaqueToken(tok), hash)
	if err != nil {
		return err
	}
	if !ok {
		return ErrResetTokenInvalid
	}
	// 日志里**只有 user_id**：令牌明文不落日志，也不落响应体。
	log.Printf("[INFO] 口令已通过一次性令牌重置：user=%d（该账号全部会话已撤销）", userID)
	return nil
}

// VerifyEmail 用一次性令牌证明邮箱归属（成功时该邮箱才可用于找回）。
//
// 不需要登录：链接是从邮件里点开的，用户此刻可能是未登录状态。
// 令牌本身就是凭据，而它只有 256 bit 随机 + 单次使用 + 24 小时三条性质在支撑 ——
// 因此它必须与重置令牌一样受 IP 限速（在 handler 的闸门上）。
func (s *AccountService) VerifyEmail(ctx context.Context, token string) error {
	tok := strings.TrimSpace(token)
	if tok == "" {
		return ErrVerifyTokenInvalid
	}
	userID, ok, err := s.store.ConsumeEmailVerify(ctx, HashOpaqueToken(tok))
	if err != nil {
		return err
	}
	if !ok {
		return ErrVerifyTokenInvalid
	}
	log.Printf("[INFO] 邮箱已验证：user=%d（该邮箱现在可以用于找回密码）", userID)
	return nil
}

// dispatchEmailToken 生成一次性令牌、**同步落库**、然后投进异步发信队列。
//
// 这是三条链路（注册后验证 / 找回密码 / 将来的重发验证）**唯一**的令牌入口：
// "生成什么、怎么存、链接长什么样、什么时候过期"只有这一处实现。
//
// 两件事的顺序是硬约束：**先落库，再入队**。
// 反过来的话（先发信后落库）会出现"用户收到信、点开链接时库里还没有这一行"，
// 而那个窗口没有任何办法补救；当前的顺序最坏只是"入了队但进程崩了" ——
// 用户没收到信，库里那一行 30 分钟后自然过期，重试即可。
//
// 返回 (false, nil) 表示"邮件没能入队"（队列满/已关闭）：调用方只记日志。
func (s *AccountService) dispatchEmailToken(
	ctx context.Context, u *store.User, addr, purpose, ip, ua string,
) (sent bool, err error) {
	if s.mail == nil {
		return false, mail.ErrMailUnavailable
	}
	if u == nil || u.ID <= 0 {
		return false, errors.New("account: 缺少账号，无法生成令牌")
	}

	token, hash, err := newOpaqueToken()
	if err != nil {
		return false, err
	}

	ttl := s.emailVerifyTTL
	if purpose == store.PurposePasswordReset {
		ttl = s.resetTTL
	}
	now := s.now()

	// 同步写（见文件头部的同步写清单 ④）：这一行丢了 = 用户点开链接必然失败。
	row := &store.PasswordReset{
		UserID:    u.ID,
		Purpose:   purpose,
		TokenHash: hash,
		Email:     &addr,
		IP:        optString(ip),
		UserAgent: optString(ua),
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}
	if err := s.store.InsertPasswordReset(ctx, row); err != nil {
		return false, err
	}

	msg := mail.Outbound{
		To:      addr,
		Subject: emailSubject(purpose),
		Body:    emailBody(purpose, s.emailLink(purpose, token), ttl),
		// Tag 是日志里**唯一**会出现的东西：不放 token、不放收件地址。
		Tag: fmt.Sprintf("%s user=%d", purpose, u.ID),
	}
	if !s.mail.Enqueue(msg) {
		// 队列满或已关闭：丢这一封 + 记账。对外仍然是 200（不泄露、不给反馈）。
		log.Printf("[WARN] 邮件队列已满或已关闭，丢弃投递：%s（purpose=%s）", msg.Tag, purpose)
		return false, nil
	}
	log.Printf("[INFO] 邮件已入队：%s", msg.Tag)
	return true, nil
}

// emailLink 拼装一次性链接。
//
// 形如 `<PR_PUBLIC_BASE_URL>/reset#token=<一次性串>`，两个要点：
//
//  1. **域名来自配置，绝不从请求头推导**。Host / X-Forwarded-Host 由请求方控制：
//     攻击者用 `Host: evil.com` 触发一次找回，受害者收到的就是指向 evil.com 的链接 ——
//     点一下 token 就进了攻击者的服务器。这是"密码重置链接投毒"的现成路径。
//  2. **令牌放在 fragment（`#`）里，不放 query（`?`）**。fragment 不会被浏览器
//     发往服务器，因此不进 nginx access log、不进 Referer；query 会同时进这两处，
//     而一次误配的反代日志就等于一份可用的凭据清单。
//     代价是前端必须自己读 location.hash（本项目是 history 路由模式，
//     vue-router 不会消费 fragment）—— 那是前端契约的一部分。
//
// token 本身是 base64url（RawURLEncoding），字符集天然 URL 安全，因此不需要再编码。
func (s *AccountService) emailLink(purpose, token string) string {
	path := "/verify"
	if purpose == store.PurposePasswordReset {
		path = "/reset"
	}
	return fmt.Sprintf("%s%s#token=%s", s.publicBaseURL, path, token)
}

// emailSubject 返回邮件主题（两种用途固定两句，不含任何用户输入）。
//
// 主题是**常量**而不是拼出来的：邮件头是把用户输入变成攻击面的经典位置
// （CRLF 注入），而这里没有任何拼装，这一整类问题就不存在。
func emailSubject(purpose string) string {
	if purpose == store.PurposePasswordReset {
		return emailBrandName + "：重置密码"
	}
	return emailBrandName + "：验证你的邮箱"
}

// emailBody 组装纯文本正文。
//
// 三条要求都在里面：一次性链接、有效期说明、"不是你本人操作就忽略"。
// 正文里**不放明文密码**（需求明确），也不放用户名/昵称等可被用来做社会工程的信息 ——
// 收件箱被转发时，正文越少越好。
func emailBody(purpose, link string, ttl time.Duration) string {
	valid := humanTTL(ttl)
	if purpose == store.PurposePasswordReset {
		return fmt.Sprintf(`%s · 重置密码

有人（希望是你本人）为这个邮箱对应的账号申请了重置密码。

请在 %s 内点开下面的链接设置新密码。这个链接**只能使用一次**：

%s

链接打开后由你自己设置新密码 —— 我们不会把密码发给你，也不会有人向你索要它。

如果不是你本人操作，忽略这封邮件即可，你的密码不会被改动。
`, emailBrandName, valid, link)
	}
	return fmt.Sprintf(`%s · 验证邮箱

请在 %s 内点开下面的链接，完成邮箱验证：

%s

这个链接只能使用一次。验证之后，这个邮箱才能用于找回密码。

如果不是你本人操作，忽略这封邮件即可。
`, emailBrandName, valid, link)
}

// humanTTL 把 TTL 说成人话（"30 分钟" / "24 小时"）。
//
// 为什么不直接打印 Go 的 Duration：`30m0s` 出现在给用户看的正文里像是一处泄漏的
// 内部细节，而"有效期说明"是这封邮件里用户唯一需要读懂的数字。
func humanTTL(ttl time.Duration) string {
	if ttl >= time.Hour {
		h := int(ttl.Hours())
		if ttl == time.Duration(h)*time.Hour {
			return fmt.Sprintf("%d 小时", h)
		}
		return fmt.Sprintf("%d 小时 %d 分钟", h, int(ttl.Minutes())-h*60)
	}
	if ttl >= time.Minute {
		return fmt.Sprintf("%d 分钟", int(ttl.Minutes()))
	}
	return fmt.Sprintf("%d 秒", int(ttl.Seconds()))
}
