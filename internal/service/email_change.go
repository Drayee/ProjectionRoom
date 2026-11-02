package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"ProjectionRoom/internal/service/mail"
	"ProjectionRoom/internal/store"
)

// 自助换绑邮箱（票 #72 / 决议 #51）。
//
// # 两条路，刻意不同路
//
//	① **自助**（本文件）：验证码发到**新地址** → 凭码确认 → `email_verified_at` 落值。
//	   证明的是"我能读到这个新邮箱"，这正好就是换绑要的那件事。
//	② **管理员代办**（`AccountService.BindUserEmail`，票 #41 已有）：管理员直接写，
//	   并**同时**标记已验证 —— 管理员是可信主体，要求他去收信会让"给老账号补邮箱"
//	   在实践中不可用。两路的差别不在"谁更可信"，而在"这次变更由谁发起"：
//	   自助变更需要归属证明，代办变更由发起人的权限承担。
//
// # 为什么必须先发到新地址、再确认
//
// 若沿用"发到当前邮箱"再改（常见于一些站点），那么**拿到会话的攻击者**只要
// 让受害者收到一封信就能改绑 —— 而那封信受害者根本不会读（他不知道有人在改）。
// 发到新地址则相反：只有真正持有新邮箱的人才能完成换绑。
//
// # 与找回密码那条门的关系
//
// 换绑成功之后，找回密码自然指向新邮箱 —— 因为那条门就是按 `users.email` 查的
// （`ResetPasswordWithOTP` → `UserByEmail`）。旧邮箱**立刻失效**：它不再是这个
// 账号的 email，`UserByEmail(旧)` 查不到任何行。
//
// 这也意味着"旧邮箱的持有者"从此无法再找回这个账号 —— 那正是换绑的语义。

// ErrEmailUnchanged 表示"新邮箱与当前邮箱相同"。
//
// 单独一个错误而不是静默成功：静默成功会让用户以为换绑生效了（其实什么都没变），
// 而他接下来可能就去旧邮箱等一封永远不会来的信。
var ErrEmailUnchanged = errors.New("account: 新邮箱与当前邮箱相同")

// otpMailEmailChange 是"换绑邮箱确认"的验证码邮件。
//
// ⚠ 与 otpMailRegister / otpMailPassword 一样，它**只决定邮件文案**，
// 落库时仍写 store.PurposeEmailOTP（见 otp.go 文件头对两套取值的说明）。
const otpMailEmailChange = "email_change"

// SendEmailChangeOTP 给**新地址**发一条换绑确认码。
//
// 门控顺序（顺序本身是安全性质）：
//
//	① 账号有效性（会话已由 handler 验过，这里再确认一次状态）
//	② 邮箱格式（不依赖库）→ ErrBadEmail
//	③ 邮件能力（不依赖库）→ ErrEmailOTPUnavailable
//	④ 新地址已被**别的账号**占用 → ErrEmailTaken（409）
//	⑤ 与当前邮箱相同 → ErrEmailUnchanged（400，不是错误而是"没必要"）
//	⑥ 冷却期 → ErrOTPTooSoon（429）
//
// 与 `SendAccountOTP`（找回密码）的关键差别：那个接口**必须**恒定 200
// （否则就是账号枚举通道），而这里调用方已经登录、账号早已确定，
// 所以它可以如实报错 —— 而这正是用户需要的（"这个邮箱被别人占了"）。
//
// 唯一的例外是 ④ 的文案：只说"该邮箱已被其他账号绑定"，绝不透露是哪个账号。
func (s *AccountService) SendEmailChangeOTP(ctx context.Context, userID int64, newEmail, ip, ua string) (devCode string, err error) {
	if userID <= 0 {
		return "", ErrSessionInvalid
	}
	u, err := s.store.UserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", ErrSessionInvalid
		}
		return "", err
	}
	if u.Status != store.StatusActive {
		return "", ErrUserForbidden
	}

	addr, err := CanonicalEmail(newEmail)
	if err != nil {
		return "", err
	}
	// 与 register 的同名判断逐条对齐：dev echo 模式下"没有发信通道"不是失败。
	if s.mail == nil && !s.otpDevEcho {
		return "", ErrEmailOTPUnavailable
	}

	// 已被**别的**账号占用：当场说（用户要换一个地址，而不是等输完码才发现）。
	// 自己占用自己的当前邮箱不算冲突 —— 那种情况由下面的 ErrEmailUnchanged 处理，
	// 它给出的文案更准确（"和现在一样"而不是"被别人占了"）。
	if other, err := s.store.UserByEmail(ctx, addr); err == nil {
		if other == nil || other.ID != userID {
			return "", ErrEmailTaken
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}

	if u.Email != nil && strings.EqualFold(strings.TrimSpace(*u.Email), addr) {
		return "", ErrEmailUnchanged
	}

	// 冷却期与找回密码共用一条：同一账号 60 秒内只发一条码。
	//
	// 共用是刻意的 —— `InsertEmailOTP` 本身就会作废该账号所有未用的旧码
	// （"最新一条是唯一有效的那条"），所以两条门本来就不可能并存两条活码。
	// 用一个冷却窗口覆盖两者，与那个存储语义一致。
	last, err := s.store.LastEmailOTPSentAt(ctx, userID)
	if err != nil {
		return "", err
	}
	if !last.IsZero() && s.now().Sub(last) < otpCooldown {
		return "", ErrOTPTooSoon
	}

	dev, err := s.dispatchEmailChangeOTP(ctx, u, addr, ip, ua)
	if err != nil {
		if errors.Is(err, mail.ErrMailUnavailable) {
			return "", err
		}
		log.Printf("[WARN] 换绑邮箱验证码发送失败：user=%d %v", u.ID, err)
		return "", nil
	}
	return dev, nil
}

// ConfirmEmailChange 校验换绑码并把账号的邮箱换成新地址（成功即视为已验证）。
//
// 顺序：格式（廉价）→ 账号有效性 → 校验码 → 写库。
//
// **校验码必须按新地址过滤**（`store.ConsumeEmailOTPFor`）：这是整个功能的安全
// 底座 —— 只按 user_id 取"最新一条"会让一条发给旧地址的码也能确认新地址，
// 于是"证明你拥有新邮箱"根本没被证明。详见 store.ConsumeEmailOTPFor 的注释。
func (s *AccountService) ConfirmEmailChange(ctx context.Context, userID int64, newEmail, code string) error {
	if userID <= 0 {
		return ErrSessionInvalid
	}
	addr, err := CanonicalEmail(newEmail)
	if err != nil {
		return err
	}

	u, err := s.store.UserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrSessionInvalid
		}
		return err
	}
	if u.Status != store.StatusActive {
		return ErrUserForbidden
	}

	ok, err := s.store.ConsumeEmailOTPFor(ctx, userID, addr, HashOpaqueToken(strings.TrimSpace(code)), otpMaxAttempts)
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

	// 码通过 = 归属已证明，因此直接落"已验证"（与注册建号同一语义）。
	now := s.now()
	if err := s.store.SetUserEmail(ctx, userID, addr, &now); err != nil {
		if errors.Is(err, store.ErrEmailTaken) {
			// 发码与确认之间被别人抢走了这个地址。如实报 409（用户要换一个）。
			return ErrEmailTaken
		}
		return err
	}
	log.Printf("[INFO] 邮箱已自助换绑：user=%d（新地址已验证，旧地址即刻失效）", userID)
	return nil
}

// dispatchEmailChangeOTP 生成换绑验证码、落库、再投递。
//
// 与 dispatchAccountOTP 的**唯一实质差别**是那一行的 `email` 列：
// 这里写的是**新地址**（收件人），而找回密码写的是当前地址。确认时按这一列过滤，
// 所以"这一行证明的是哪个地址"完全由它决定 —— 写错就等于把门开到别人家。
func (s *AccountService) dispatchEmailChangeOTP(
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
		Email:     &addr, // 新地址：确认时按它过滤，见 ConsumeEmailOTPFor
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
		Subject: otpEmailSubject(otpMailEmailChange),
		Body:    otpEmailBody(otpMailEmailChange, code, ttl),
		// Tag 里**不放验证码**，也**不放新地址**：日志是给人看的排障信息，
		// 不是凭据载体；而"某人正在把账号换绑到某个地址"本身就是敏感信息。
		Tag: fmt.Sprintf("otp.%s user=%d", otpMailEmailChange, u.ID),
	}
	if s.mail == nil {
		log.Printf("[INFO] 换绑验证码已生成（dev echo，未投递）：%s", msg.Tag)
		return devCode, nil
	}
	if !s.mail.Enqueue(msg) {
		log.Printf("[WARN] 邮件队列已满或已关闭，丢弃投递：%s", msg.Tag)
		return devCode, nil
	}
	log.Printf("[INFO] 换绑验证码邮件已入队：%s", msg.Tag)
	return devCode, nil
}
