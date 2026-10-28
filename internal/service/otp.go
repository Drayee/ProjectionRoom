package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"ProjectionRoom/internal/service/auth"
	"ProjectionRoom/internal/service/mail"
	"ProjectionRoom/internal/store"
)

// 邮箱验证码（OTP）的用例：**注册第二段**与**改密码**共用同一套发码/校验逻辑。
//
// 与链接令牌（password_reset.go）的关系：
//
//	链接 = 点一下就用掉，可以很长（256 bit）；
//	OTP  = 人要读出来手输，所以只有 6 位数字 —— 于是必须叠加三道防线：
//	       ① 短 TTL（分钟级）；
//	       ② 错误次数上限（落库，重启不清零）；
//	       ③ 发送频率闸门（每邮箱冷却 + 每 IP 配额，在 handler 的限流器上）。
//
// # 为什么 6 位数字仍然安全
//
// 6 位 = 100 万种。没有 ② 时它几分钟就能被爆破；有 ② 之后单条码最多试 5 次，
// 成功概率 5/10^6 ≈ 0.0005%。再叠加"发新码会作废旧码"（见 store.InsertEmailOTP），
// 攻击者无法靠"攒很多条码一起试"把概率乘上去。
//
// # 三条对外性质
//
//  1. **发码接口不泄露账号是否存在**（改密码场景）：与 Forgot 同一条口径 ——
//     恒定 200，只在服务端日志里区分。
//  2. **注册场景相反**：那里必须告诉用户"这个邮箱已被占用"（否则他填完验证码才
//     发现白填），而注册入口本身有 IP 限流与 bcrypt 成本，可枚举面被接受
//     （与 Register 的既有取舍一致，见 account.go 的说明）。
//  3. **验证码明文只出现在邮件正文与内存里**：库里只有 sha256，响应体里永不出现。

// OTP 的三条策略常量。
const (
	// otpDigits 是验证码位数。6 位是"人能顺畅读出"与"空间够大"的折中。
	otpDigits = 6
	// otpMaxAttempts 是单条码允许的错误次数。超过即作废（见 store.ConsumeEmailOTP）。
	otpMaxAttempts = 5
	// otpCooldown 是同一邮箱两次发码之间的最小间隔。
	//
	// 60 秒的理由：它挡掉"连点重发"与最简单的脚本轰炸，又不会让正常用户
	//（没收到信 → 想再发一次）等太久。真正的防轰炸靠每 IP / 每邮箱的令牌桶。
	otpCooldown = 60 * time.Second
)

// ErrOTPInvalid 是"验证码不对"的统一出口。
//
// 不区分"没发过 / 已过期 / 已用过 / 输错了"：区分它们等于给攻击者一条
// 判断"这个码还有没有效"的探测通道，而用户在这些情况下要做的事完全一样
// （重新获取一条新的）。
var ErrOTPInvalid = errors.New("account: 验证码不正确或已失效，请重新获取")

// ErrOTPTooSoon 表示还在冷却期内。
var ErrOTPTooSoon = errors.New("account: 验证码刚发过，请稍后再试")

// ErrOTPAttemptsExceeded 表示这条码已经因错误次数过多而作废。
var ErrOTPAttemptsExceeded = errors.New("account: 验证码错误次数过多，已作废，请重新获取")

// ErrDraftTicketInvalid 表示注册 ticket 不可用（不存在 / 已用 / 已过期）。
var ErrDraftTicketInvalid = errors.New("account: 注册会话已失效，请重新开始注册")

// ErrEmailOTPUnavailable 表示邮件能力未配置（与 mail.ErrMailUnavailable 同义，
// 但用于 OTP 链路，让 handler 能给出"验证码通道未开启"的准确文案）。
var ErrEmailOTPUnavailable = mail.ErrMailUnavailable

// newOTP 生成一个 6 位数字验证码及其哈希。
//
// 用 crypto/rand 而不是 math/rand：后者是可预测的（种子一旦被推出来，
// 攻击者能算出"下一个验证码"）。这里用 crypto/rand 取模 —— 对 100 万这种
// 远小于 2^63 的范围，取模偏差可以忽略（不需要拒绝采样）。
func newOTP() (code string, hash string, err error) {
	max := big.NewInt(1)
	for i := 0; i < otpDigits; i++ {
		max.Mul(max, big.NewInt(10))
	}
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", "", fmt.Errorf("account: 生成验证码失败: %w", err)
	}
	code = fmt.Sprintf("%0*d", otpDigits, n.Int64())
	return code, HashOpaqueToken(code), nil
}

// 验证码邮件的两种「用途」。
//
// ⚠ 它们**只决定邮件文案**（"完成注册"还是"重置密码"），**不是**数据库里的
// `password_resets.purpose` —— 落库时两种都写成 store.PurposeEmailOTP
// （见 dispatchAccountOTP 与 AttachDraftOTP）。刻意分成两套取值：
// 邮件的"为什么要发这封信"与库里的"这行是什么凭据"是两件事，混用会让
// 某天想加第三种邮件（比如"换绑邮箱确认"）时被迫往 DB 的 CHECK 里加值。
const (
	// otpMailRegister 是注册第二段的验证码邮件。
	otpMailRegister = "register"
	// otpMailPassword 是"找回密码（验证码改密）"的验证码邮件。
	otpMailPassword = "password"
)

// otpEmailBody 组装验证码邮件正文。
//
// 与链接邮件同样只放"用户要做的那一件事"：验证码、有效期、不是你本人就忽略。
// **不**放用户名/昵称 —— 收件箱被转发时，正文越少越好。
func otpEmailBody(kind, code string, ttl time.Duration) string {
	action := "完成注册"
	if kind == otpMailPassword {
		action = "找回密码"
	}
	return fmt.Sprintf(`%s · 验证码

你正在进行「%s」操作，验证码是：

    %s

这个验证码在 %s 内有效，且只能使用一次。连续输错 %d 次后需要重新获取。

我们不会向你索要这个验证码。如果不是你本人操作，忽略这封邮件即可。
`, emailBrandName, action, code, humanTTL(ttl), otpMaxAttempts)
}

// otpEmailSubject 返回主题（常量，不含用户输入 —— 邮件头是 CRLF 注入的经典位置）。
func otpEmailSubject(kind string) string {
	if kind == otpMailPassword {
		return emailBrandName + "：找回密码验证码"
	}
	return emailBrandName + "：注册验证码"
}

// SendAccountOTP 给**已存在的账号**发一条「找回密码」验证码。
//
// 门控顺序（顺序本身就是安全性质）：
//
//	① 邮箱格式（不依赖库）→ ErrBadEmail
//	② 邮件能力（不依赖库）→ ErrEmailOTPUnavailable
//	③ 查库：**任何结果都返回 nil**（不泄露账号是否存在）
//	④ 被封禁 → 返回 nil（不发信：改密也登不进来，见下）
//	⑤ 冷却期内 → 也返回 nil（**不**报错：报错就等于确认了账号存在）
//
// # 为什么**不**再要求 email_verified_at
//
// 这条门控曾经存在，是从链接通道原样搬过来的。它对链接是必需的（否则攻击者
// 填受害者的邮箱，受害者点开链接就把**攻击者账号**的密码改了），但对验证码
// 完全不适用：码只发给这个邮箱的真实持有者，攻击者填别人的地址只能让受害者
// 收到一封莫名其妙的验证码 —— 他自己拿不到那 6 位数字，也就改不了任何东西。
//
// 而这道门控的代价是真实的：老账号（早期单段注册建的、或那封 24 小时的验证信
// 过期/进垃圾箱/当时实例根本没配 SMTP）会永久停在"未验证"，于是**永远找不回
// 密码**，且因为对外恒定 200，用户只会看到"已发送"却收不到信，无从自查。
// 这曾是一个真实的支持问题，也正是本次改造要解掉的那个。
//
// 被封禁的账号仍然不发信：改密成功也登不进来（Login 在口令通过之后仍会因
// status 拒绝），发信只会把发信配额花在无效请求上。这个门控同样不对外可见。
func (s *AccountService) SendAccountOTP(ctx context.Context, email, ip, ua string) (devCode string, err error) {
	addr, err := CanonicalEmail(email)
	if err != nil {
		return "", err
	}
	// 与 register 的同名判断逐条对齐：dev echo 模式下"没有发信通道"不是失败，
	// 否则那个开关就永远只能用在"已经配好 SMTP"的环境里 —— 而那时它被禁止。
	if s.mail == nil && !s.otpDevEcho {
		return "", ErrEmailOTPUnavailable
	}

	u, err := s.store.UserByEmail(ctx, addr)
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
		return "", nil
	default:
		return "", err
	}
	if u.Status != store.StatusActive {
		return "", nil
	}

	last, err := s.store.LastEmailOTPSentAt(ctx, u.ID)
	if err != nil {
		return "", err
	}
	if !last.IsZero() && s.now().Sub(last) < otpCooldown {
		// 冷却期：对外仍是"已受理"（不泄露存在性），只在日志里记一笔。
		log.Printf("[INFO] 验证码冷却中，跳过发送：user=%d（距上次 %s）", u.ID, s.now().Sub(last).Round(time.Second))
		return "", nil
	}

	dev, err := s.dispatchAccountOTP(ctx, u, addr, ip, ua)
	if err != nil {
		if errors.Is(err, mail.ErrMailUnavailable) {
			return "", err
		}
		log.Printf("[WARN] 找回密码验证码发送失败：user=%d %v", u.ID, err)
		return "", nil
	}
	return dev, nil
}

// ResetPasswordWithOTP 校验「找回密码」验证码，通过后直接设置新口令。
//
// 为什么把"校验 + 改密"合成一个用例而不是两步：两步之间需要一个"已验证"的中间凭据，
// 而那等于再发明一种令牌。合成一步则校验通过即改密，中间没有任何可重放的窗口。
//
// 与 SendAccountOTP 对齐：这里同样**不要求** email_verified_at ——
// 能拿出这个码本身就是归属证明（码只发到这个邮箱），再要求一次"已验证"
// 只会让未验证邮箱的账号连最后一步也走不通。
//
// 顺序：口令策略（廉价）→ 查库 → 校验码 → 哈希（昂贵）→ 改密+撤销会话。
// 把策略检查放在哈希之前：弱口令不该烧掉一条有效的验证码。
func (s *AccountService) ResetPasswordWithOTP(ctx context.Context, email, code, password string) error {
	addr, err := CanonicalEmail(email)
	if err != nil {
		return err
	}
	if err := auth.ValidatePasswordPolicy(password); err != nil {
		return err
	}

	u, err := s.store.UserByEmail(ctx, addr)
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
		// 不区分"账号不存在"与"码不对"：两者都返回 ErrOTPInvalid。
		return ErrOTPInvalid
	default:
		return err
	}
	if u.Status != store.StatusActive {
		return ErrOTPInvalid
	}

	// 先校验验证码（它自带错误计数），再哈希口令：
	// 反过来会让"码错了也白跑一次 bcrypt"，那是可被用来打满哈希闸门的路径。
	ok, err := s.store.ConsumeEmailOTP(ctx, u.ID, HashOpaqueToken(strings.TrimSpace(code)), otpMaxAttempts)
	if err != nil {
		if errors.Is(err, store.ErrOTPAttemptsExceeded) {
			return ErrOTPAttemptsExceeded
		}
		if errors.Is(err, store.ErrOTPNotFound) {
			return ErrOTPInvalid
		}
		return err
	}
	if !ok {
		return ErrOTPInvalid
	}

	hash, err := s.acquireAndHash(password)
	if err != nil {
		return err
	}
	if err := s.replacePassword(ctx, u.ID, hash); err != nil {
		return err
	}
	log.Printf("[INFO] 口令已通过邮箱验证码重置：user=%d（该账号全部会话已撤销）", u.ID)
	return nil
}

// replacePassword 换口令并撤销该账号全部会话（含 token_version 自增）。
//
// 三步缺一不可，且**顺序无关但全都要做**：少撤销会话，旧 refresh 还能刷新；
// 少自增版本号，旧 access token 在 15 分钟内仍然有效。用户看到的却是"改密成功"。
//
// 为什么不合成一个 store 方法（像已删除的 ConsumePasswordReset 那样）：
// 验证码的消费已经在 ConsumeEmailOTP 里独立完成（那是它自己的单次使用点与错误计数点），
// 因此这里没有"待消费的令牌"可以一并塞进同一个事务。硬合并会把"一条码最多错 5 次"
// 的计数逻辑与改密耦合在一起，而它们本该各自可测。
func (s *AccountService) replacePassword(ctx context.Context, userID int64, newHash string) error {
	if err := s.store.SetPasswordHash(ctx, userID, newHash); err != nil {
		return err
	}
	if _, err := s.store.RevokeAllSessions(ctx, userID); err != nil {
		return err
	}
	if _, err := s.store.BumpTokenVersion(ctx, userID); err != nil {
		return err
	}
	return nil
}

// dispatchAccountOTP 生成验证码、同步落库、然后投进异步发信队列。
//
// 与 dispatchEmailToken 同一条硬约束：**先落库，再入队**。
// 反过来的话会出现"用户收到信、输码时库里还没有这一行"。
//
// 返回的 `devCode` 只在 `otpDevEcho` 打开时非空（**仅本机开发**）：
// 生产恒为空串，因此它不会经任何路径泄漏到响应体里。
func (s *AccountService) dispatchAccountOTP(
	ctx context.Context, u *store.User, addr, ip, ua string,
) (devCode string, err error) {
	if s.mail == nil && !s.otpDevEcho {
		return "", mail.ErrMailUnavailable
	}
	if u == nil || u.ID <= 0 {
		return "", errors.New("account: 缺少账号，无法生成验证码")
	}

	code, hash, err := newOTP()
	if err != nil {
		return "", err
	}
	now := s.now()
	ttl := s.otpTTL

	row := &store.PasswordReset{
		UserID:    u.ID,
		Purpose:   store.PurposeEmailOTP,
		TokenHash: hash,
		Email:     &addr,
		IP:        optString(ip),
		UserAgent: optString(ua),
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}
	if err := s.store.InsertEmailOTP(ctx, row); err != nil {
		return "", err
	}

	if s.otpDevEcho {
		devCode = code
	}

	msg := mail.Outbound{
		To:      addr,
		Subject: otpEmailSubject(otpMailPassword),
		Body:    otpEmailBody(otpMailPassword, code, ttl),
		// Tag 里**不放验证码**：日志是给人看的排障信息，不是凭据载体。
		Tag: fmt.Sprintf("otp.%s user=%d", otpMailPassword, u.ID),
	}
	if s.mail == nil {
		// 只可能是 dev echo 模式：不投递，只回显。
		log.Printf("[INFO] 验证码已生成（dev echo，未投递）：%s", msg.Tag)
		return devCode, nil
	}
	if !s.mail.Enqueue(msg) {
		log.Printf("[WARN] 邮件队列已满或已关闭，丢弃投递：%s", msg.Tag)
		return devCode, nil
	}
	log.Printf("[INFO] 验证码邮件已入队：%s", msg.Tag)
	return devCode, nil
}
