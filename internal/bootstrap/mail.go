package bootstrap

import (
	"log"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/service/mail"
)

// —— 邮件通道（SMTP）的装配 ——

// NewMailer 构造"投递器 + 有界队列 + 单写协程"。
//
// 三种结果，逐条说明为什么这么选：
//
//  1. **邮件能力未配置**（PR_SMTP_HOST / PR_SMTP_USER / PR_SMTP_PASS 任一为空）
//     → 返回 nil 队列。这是与"账号能力关闭"同一套显式降级：发码与验证邮箱
//     一律 503 MAIL_UNAVAILABLE，而登录/注册/房间等一切功能完全不受影响。
//     不拒绝启动，因为"还没配好邮件"是一种**正常的部署阶段**。
//  2. **只配了一部分** → 同样返回 nil，但多打一条 WARN。
//     "配了一半"在实践中通常意味着有人以为它已经生效了 —— 静默降级是最难发现的事故，
//     而这条日志是唯一能在启动几分钟内看出问题的东西。
//  3. **配齐了但参数互相矛盾**（例如 TLS=none 配了公网 host、From 不是合法地址）
//     → **拒绝启动**。这类错误的表现是"第一封邮件发不出去"，而那时人已经离开了终端；
//     在启动期说清楚要便宜得多（mail.NewSMTPSender 负责判定，它才是懂那些规则的那一层）。
//
// 返回的 cleanup 关队列并给工作协程一段 flush 时间。ProviderSet 的顺序保证它在
// **服务停止之后**才执行（否则正在投递的那封邮件会被从中途掐断）。
func NewMailer(cfg *config.Config) (*mail.Mailer, func(), error) {
	if cfg == nil {
		return nil, nil, errConfigMissing
	}
	if !cfg.Auth.MailEnabled() {
		if cfg.Auth.MailPartiallyConfigured() {
			log.Printf("[WARN] 邮件能力**未启用**：PR_SMTP_HOST / PR_SMTP_USER / PR_SMTP_PASS " +
				"只配了一部分 —— 按「未配置」处理，发码接口返回 503 (MAIL_UNAVAILABLE)")
		} else {
			log.Printf("[WARN] 未配置 SMTP：找回密码与邮箱验证已关闭" +
				"（/api/auth/password/otp 返回 503 (MAIL_UNAVAILABLE)；账号其余功能不受影响）")
		}
		return nil, func() {}, nil
	}

	sender, err := mail.NewSMTPSender(mail.SMTPOptions{
		Host:     cfg.Auth.SMTPHost,
		Port:     cfg.Auth.SMTPPort,
		User:     cfg.Auth.SMTPUser,
		Pass:     cfg.Auth.SMTPPass,
		From:     cfg.Auth.SMTPFrom,
		FromName: cfg.Auth.SMTPFromName,
		TLS:      mail.TLSMode(cfg.Auth.SMTPTLS),
		Timeout:  cfg.Auth.SMTPTimeout,
	})
	if err != nil {
		return nil, nil, err
	}

	m, cleanup := mail.NewMailer(sender, mail.Options{
		QueueSize:   mail.DefaultQueueSize,
		SendTimeout: cfg.Auth.SMTPTimeout,
		OnError: func(tag string, err error) {
			// tag 是调用方给的"哪个账号、为什么发"，**不含** token 与收件地址。
			log.Printf("[WARN] 邮件投递失败（%s）：%v", tag, err)
		},
	})
	// 日志里只打地址与形态：SMTPPass 永不进日志（与 JWTSecret 同一条纪律）。
	log.Printf("邮件能力已启用：%s（TLS=%s，发件人=%s）", sender.Address(), cfg.Auth.SMTPTLS, cfg.Auth.SMTPFrom)
	return m, cleanup, nil
}
