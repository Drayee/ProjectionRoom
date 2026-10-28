package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"ProjectionRoom/internal/service/mail"
	"ProjectionRoom/internal/store"
)

// 一次性邮件**链接**链路的用例：只剩「验证邮箱」一条。
//
// ⚠ 本文件曾同时承载"忘记密码 → 重置密码"那条通道。它已被删除
//（连同 `/api/auth/forgot` + `/api/auth/reset`）：改密码现在只走 6 位验证码，
// 见 otp.go 的 SendAccountOTP / ResetPasswordWithOTP。
//
// 删除的理由**不是**"链接不安全"——恰恰相反，256 bit 的链接比 6 位数字更抗爆破。
// 理由是它解决不了那个真实的问题：链接通道必须 gate 在 `email_verified_at` 上
//（否则攻击者填受害者的邮箱，受害者点开链接就把**攻击者账号**的密码改掉了），
// 于是"邮箱未验证"的老账号在链接通道上**永远**找不回密码。
// 验证码没有这个两难：码只发给邮箱的真实持有者，攻击者填别人的地址，
// 收到的码在受害者手里，攻击者拿不到 —— 所以它可以对未验证邮箱开放。
//
// 留下的这一条仍是不可动摇的对外性质：
//
//   - **明文令牌只存在于两个地方**：写进邮件正文的那一份，以及 store 里那条
//     sha256 摘要之外的内存。日志只打 Tag（见 mail.Outbound 的说明），
//     响应体里永不出现它（那是把"单次使用"降级成"谁看到响应谁能改密"）。
//
// 本文件的方法都挂在 AccountService 上而不是另立一个服务：它们需要**同一套**
// 依赖（store / 注入时钟 / 发信队列），另立服务只会让这些依赖出现第二份。

// VerifyEmail 用一次性令牌证明邮箱归属（成功时该邮箱才可用于找回）。
//
// 不需要登录：链接是从邮件里点开的，用户此刻可能是未登录状态。
// 令牌本身就是凭据，而它只有 256 bit 随机 + 单次使用 + 24 小时三条性质在支撑 ——
// 因此它必须受 IP 限速（在 handler 的闸门上）。
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

// dispatchEmailToken 生成一次性验证令牌、**同步落库**、然后投进异步发信队列。
//
// 这是「验证邮箱」链路**唯一**的令牌入口："生成什么、怎么存、链接长什么样、
// 什么时候过期"只有这一处实现。
//
// 两件事的顺序是硬约束：**先落库，再入队**。
// 反过来的话（先发信后落库）会出现"用户收到信、点开链接时库里还没有这一行"，
// 而那个窗口没有任何办法补救；当前的顺序最坏只是"入了队但进程崩了" ——
// 用户没收到信，库里那一行到期后自然作废，重试即可。
//
// 返回 (false, nil) 表示"邮件没能入队"（队列满/已关闭）：调用方只记日志。
func (s *AccountService) dispatchEmailToken(
	ctx context.Context, u *store.User, addr, ip, ua string,
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
	now := s.now()

	// 同步写（见文件头部的同步写清单 ④）：这一行丢了 = 用户点开链接必然失败。
	row := &store.PasswordReset{
		UserID:    u.ID,
		Purpose:   store.PurposeEmailVerify,
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
		Subject: emailSubject(),
		Body:    emailBody(s.emailLink(token), ttl),
		// Tag 是日志里**唯一**会出现的东西：不放 token、不放收件地址。
		Tag: fmt.Sprintf("%s user=%d", store.PurposeEmailVerify, u.ID),
	}
	if !s.mail.Enqueue(msg) {
		// 队列满或已关闭：丢这一封 + 记账。调用方只记日志，不对外报错。
		log.Printf("[WARN] 邮件队列已满或已关闭，丢弃投递：%s", msg.Tag)
		return false, nil
	}
	log.Printf("[INFO] 邮件已入队：%s", msg.Tag)
	return true, nil
}

// emailLink 拼装验证链接。
//
// 形如 `<PR_PUBLIC_BASE_URL>/verify#token=<一次性串>`，两个要点：
//
//  1. **域名来自配置，绝不从请求头推导**。Host / X-Forwarded-Host 由请求方控制：
//     攻击者用 `Host: evil.com` 触发一次发信，受害者收到的就是指向 evil.com 的链接 ——
//     点一下 token 就进了攻击者的服务器。这是"链接投毒"的现成路径。
//  2. **令牌放在 fragment（`#`）里，不放 query（`?`）**。fragment 不会被浏览器
//     发往服务器，因此不进 nginx access log、不进 Referer；query 会同时进这两处，
//     而一次误配的反代日志就等于一份可用的凭据清单。
//     代价是前端必须自己读 location.hash（本项目是 history 路由模式，
//     vue-router 不会消费 fragment）—— 那是前端契约的一部分。
//
// token 本身是 base64url（RawURLEncoding），字符集天然 URL 安全，因此不需要再编码。
func (s *AccountService) emailLink(token string) string {
	return fmt.Sprintf("%s/verify#token=%s", s.publicBaseURL, token)
}

// emailSubject 返回验证邮件的主题（**常量**，不含任何用户输入）。
//
// 主题是常量而不是拼出来的：邮件头是把用户输入变成攻击面的经典位置
// （CRLF 注入），而这里没有任何拼装，这一整类问题就不存在。
func emailSubject() string {
	return emailBrandName + "：验证你的邮箱"
}

// emailBody 组装验证邮件的纯文本正文。
//
// 三条要求都在里面：一次性链接、有效期说明、"不是你本人操作就忽略"。
// 正文里**不放**用户名/昵称等可被用来做社会工程的信息 —— 收件箱被转发时，正文越少越好。
func emailBody(link string, ttl time.Duration) string {
	return fmt.Sprintf(`%s · 验证邮箱

请在 %s 内点开下面的链接，完成邮箱验证：

%s

这个链接只能使用一次。验证之后，这个邮箱才能用于找回密码。

如果不是你本人操作，忽略这封邮件即可。
`, emailBrandName, humanTTL(ttl), link)
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
