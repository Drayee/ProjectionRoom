package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ProjectionRoom/internal/store"
)

// —— 自助换绑邮箱（票 #72）——
//
// 这一组用例的判据分两类：
//
//	① **功能**：码发到新地址、凭码换绑、换绑后 email_verified_at 落值、
//	   找回密码指向新地址、旧地址立刻失效；
//	② **安全**：发给旧地址的码**不能**确认新地址（这是整个功能的地基），
//	   以及"新地址已被别人占用"必须当场说而不是等输完码。

// emailChangeFixture 造一个已登录、已有邮箱的账号 + 一台捕获发信的服务。
type emailChangeFixture struct {
	svc *AccountService
	fs  *fakeStore
	box *captureMail
	u   *store.User
}

func newEmailChangeFixture(t *testing.T, oldEmail string) *emailChangeFixture {
	t.Helper()
	f := newFakeStore()
	box := &captureMail{}
	svc := newTestService(t, f, AccountOptions{Mail: box})
	u := seedUserWithEmail(t, f, "ec-alice", oldEmail, true, "")
	return &emailChangeFixture{svc: svc, fs: f, box: box, u: u}
}

// otpFromLastMail 从最近一封邮件正文里抄出 6 位验证码。
func otpFromLastMail(t *testing.T, box *captureMail) string {
	t.Helper()
	return otpFromMail(t, box.last(t).Body)
}

// TestSendEmailChangeOTPMailsNewAddress 码必须发到**新**地址。
//
// 这是换绑与"改密码"最根本的差别：找回密码发到当前邮箱（证明"我是主人"），
// 换绑发到新邮箱（证明"我能读到这个新邮箱"）。发错地方等于换绑根本没被证明。
func TestSendEmailChangeOTPMailsNewAddress(t *testing.T) {
	fx := newEmailChangeFixture(t, "old@example.com")
	ctx := context.Background()

	dev, err := fx.svc.SendEmailChangeOTP(ctx, fx.u.ID, "new@example.com", "1.1.1.1", "ua")
	if err != nil {
		t.Fatalf("发换绑码应当成功：%v", err)
	}
	if dev != "" {
		t.Fatalf("未开 dev echo 时不该回显验证码，实际 %q", dev)
	}

	msg := fx.box.last(t)
	if msg.To != "new@example.com" {
		t.Fatalf("换绑码必须发到新地址，实际发到 %q", msg.To)
	}
	// 落库那行的 email 列也必须是新地址 —— 确认时按它过滤，写错就等于把门开到别人家。
	var rows []*store.PasswordReset
	for _, r := range fx.fs.resets {
		rows = append(rows, r)
	}
	if len(rows) != 1 {
		t.Fatalf("应当落一行验证码，实际 %d 行", len(rows))
	}
	if rows[0].Email == nil || *rows[0].Email != "new@example.com" {
		t.Fatalf("验证码行的收件地址必须是新地址，实际 %v", rows[0].Email)
	}
	// 发码**不**改邮箱：真正的换绑要等确认。
	after, _ := fx.fs.UserByID(ctx, fx.u.ID)
	if after.Email == nil || *after.Email != "old@example.com" {
		t.Fatalf("发码阶段不该改邮箱，实际 %v", after.Email)
	}
}

// TestSendEmailChangeOTPRejectsTakenAddress 新地址被别的账号占用 → ErrEmailTaken。
//
// 必须**当场**说（而不是等用户输完码）：他这时还能换一个地址，
// 而输完码才发现只会让他以为"验证码发不出来"。
func TestSendEmailChangeOTPRejectsTakenAddress(t *testing.T) {
	fx := newEmailChangeFixture(t, "old@example.com")
	ctx := context.Background()
	seedUserWithEmail(t, fx.fs, "ec-bob", "taken@example.com", true, "")

	_, err := fx.svc.SendEmailChangeOTP(ctx, fx.u.ID, "taken@example.com", "1.1.1.1", "ua")
	if !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("被占用的地址应当返回 ErrEmailTaken，实际 %v", err)
	}
	if fx.box.count() != 0 {
		t.Fatalf("被占用时不该发信，实际 %d 封", fx.box.count())
	}
}

// TestSendEmailChangeOTPRejectsUnchanged 与当前邮箱相同 → ErrEmailUnchanged。
//
// 静默成功会让用户以为换绑生效了，然后去旧邮箱等一封永远不会来的信。
func TestSendEmailChangeOTPRejectsUnchanged(t *testing.T) {
	fx := newEmailChangeFixture(t, "same@example.com")
	ctx := context.Background()

	// 大小写不同也算相同（邮箱归一后是同一个地址）。
	_, err := fx.svc.SendEmailChangeOTP(ctx, fx.u.ID, "SAME@Example.COM", "1.1.1.1", "ua")
	if !errors.Is(err, ErrEmailUnchanged) {
		t.Fatalf("与当前邮箱相同应当返回 ErrEmailUnchanged，实际 %v", err)
	}
}

// TestConfirmEmailChangeFlipsAddressAndVerified 完整流程：换绑后地址变了、
// 已验证、旧地址立刻失效、找回密码指向新地址。
func TestConfirmEmailChangeFlipsAddressAndVerified(t *testing.T) {
	fx := newEmailChangeFixture(t, "old@example.com")
	ctx := context.Background()

	if _, err := fx.svc.SendEmailChangeOTP(ctx, fx.u.ID, "new@example.com", "1.1.1.1", "ua"); err != nil {
		t.Fatalf("发码失败：%v", err)
	}
	code := otpFromLastMail(t, fx.box)

	if err := fx.svc.ConfirmEmailChange(ctx, fx.u.ID, "new@example.com", code); err != nil {
		t.Fatalf("确认换绑失败：%v", err)
	}

	after, err := fx.fs.UserByID(ctx, fx.u.ID)
	if err != nil {
		t.Fatalf("回读账号失败：%v", err)
	}
	if after.Email == nil || *after.Email != "new@example.com" {
		t.Fatalf("邮箱应当已换成新地址，实际 %v", after.Email)
	}
	if after.EmailVerifiedAt == nil {
		t.Fatal("换绑成功必须把 email_verified_at 落值（码刚证明了归属）")
	}

	// 旧地址立刻失效：按旧邮箱查不到任何账号。
	if _, err := fx.fs.UserByEmail(ctx, "old@example.com"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("旧地址应当查不到账号，实际 err=%v", err)
	}
	// 新地址能查到 —— 这就是"找回密码指向新邮箱"的机制。
	if u2, err := fx.fs.UserByEmail(ctx, "new@example.com"); err != nil || u2 == nil || u2.ID != fx.u.ID {
		t.Fatalf("新地址应当查到同一个账号，实际 u=%v err=%v", u2, err)
	}
}

// TestConfirmEmailChangeRejectsCodeForOldAddress 是**本票最重要的安全判据**。
//
// 场景：账号先请求过一条"找回密码"码（发到旧地址），然后请求换绑到新地址。
// 攻击者若拿旧地址那条码来确认新地址，就等于"证明你拥有新邮箱"根本没发生 ——
// 他只要让受害者收到任意一条码，就能把账号绑到自己邮箱上，随后走找回密码接管。
//
// 因此确认必须按**收件地址**过滤，而不只是按 user_id 取最新一条。
func TestConfirmEmailChangeRejectsCodeForOldAddress(t *testing.T) {
	fx := newEmailChangeFixture(t, "old@example.com")
	ctx := context.Background()

	// 先发一条找回密码码（收件人 = 旧地址）。换绑的冷却期与它共用，
	// 所以这里直接把时间戳往前拨 2 分钟绕过冷却，模拟"用户先找回过密码"。
	if _, err := fx.svc.SendAccountOTP(ctx, "old@example.com", "1.1.1.1", "ua"); err != nil {
		t.Fatalf("发找回密码码失败：%v", err)
	}
	oldCode := otpFromLastMail(t, fx.box)
	for _, r := range fx.fs.resets {
		r.CreatedAt = r.CreatedAt.Add(-2 * time.Minute)
	}

	if _, err := fx.svc.SendEmailChangeOTP(ctx, fx.u.ID, "new@example.com", "1.1.1.1", "ua"); err != nil {
		t.Fatalf("发换绑码失败：%v", err)
	}

	// 拿**旧地址**那条码去确认新地址：必须失败。
	err := fx.svc.ConfirmEmailChange(ctx, fx.u.ID, "new@example.com", oldCode)
	if err == nil {
		t.Fatal("发给旧地址的验证码不该能确认新地址（那等于换绑没有被证明）")
	}
	if !errors.Is(err, ErrOTPInvalid) {
		t.Fatalf("应当是「验证码不可用」而不是意外错误，实际 %v", err)
	}

	// 邮箱没变。
	after, _ := fx.fs.UserByID(ctx, fx.u.ID)
	if after.Email == nil || *after.Email != "old@example.com" {
		t.Fatalf("确认失败时邮箱不该变，实际 %v", after.Email)
	}
}

// TestConfirmEmailChangeRejectsWrongCode 错误码 → ErrOTPInvalid，且邮箱不变。
func TestConfirmEmailChangeRejectsWrongCode(t *testing.T) {
	fx := newEmailChangeFixture(t, "old@example.com")
	ctx := context.Background()

	if _, err := fx.svc.SendEmailChangeOTP(ctx, fx.u.ID, "new@example.com", "1.1.1.1", "ua"); err != nil {
		t.Fatalf("发码失败：%v", err)
	}

	err := fx.svc.ConfirmEmailChange(ctx, fx.u.ID, "new@example.com", "000000")
	if !errors.Is(err, ErrOTPInvalid) {
		t.Fatalf("错误码应当返回 ErrOTPInvalid，实际 %v", err)
	}
	after, _ := fx.fs.UserByID(ctx, fx.u.ID)
	if after.Email == nil || *after.Email != "old@example.com" {
		t.Fatalf("确认失败时邮箱不该变，实际 %v", after.Email)
	}
}

// TestConfirmEmailChangeSingleUse 码只能确认一次。
//
// 若可重放，攻击者截获一条码之后能反复把账号换到不同地址。
func TestConfirmEmailChangeSingleUse(t *testing.T) {
	fx := newEmailChangeFixture(t, "old@example.com")
	ctx := context.Background()

	if _, err := fx.svc.SendEmailChangeOTP(ctx, fx.u.ID, "new@example.com", "1.1.1.1", "ua"); err != nil {
		t.Fatalf("发码失败：%v", err)
	}
	code := otpFromLastMail(t, fx.box)
	if err := fx.svc.ConfirmEmailChange(ctx, fx.u.ID, "new@example.com", code); err != nil {
		t.Fatalf("首次确认应当成功：%v", err)
	}

	// 第二次：同一条码，换成另一个地址（模拟"截获后重放到别处"）。
	err := fx.svc.ConfirmEmailChange(ctx, fx.u.ID, "attacker@example.com", code)
	if err == nil {
		t.Fatal("同一条码不该能确认第二次")
	}
	after, _ := fx.fs.UserByID(ctx, fx.u.ID)
	if after.Email == nil || *after.Email != "new@example.com" {
		t.Fatalf("重放不该改邮箱，实际 %v", after.Email)
	}
}

// TestSendEmailChangeOTPRejectsBanned 封禁账号不能换绑（403）。
func TestSendEmailChangeOTPRejectsBanned(t *testing.T) {
	fx := newEmailChangeFixture(t, "old@example.com")
	ctx := context.Background()
	fx.fs.mu.Lock()
	fx.fs.users[fx.u.ID].Status = store.StatusBanned
	fx.fs.mu.Unlock()

	if _, err := fx.svc.SendEmailChangeOTP(ctx, fx.u.ID, "new@example.com", "1.1.1.1", "ua"); !errors.Is(err, ErrUserForbidden) {
		t.Fatalf("封禁账号应当返回 ErrUserForbidden，实际 %v", err)
	}
}

// TestEmailChangeDoesNotTouchPassword 换绑不该顺手动口令或蹬掉会话。
//
// 它是"账号的一个字段变了"，不是"账号被接管"；顺手撤销全部会话会让用户
// 换个邮箱就被踢出所有设备 —— 那不是他要求的动作。
func TestEmailChangeDoesNotTouchPassword(t *testing.T) {
	fx := newEmailChangeFixture(t, "old@example.com")
	ctx := context.Background()

	before, _ := fx.fs.UserByID(ctx, fx.u.ID)
	if _, err := fx.svc.SendEmailChangeOTP(ctx, fx.u.ID, "new@example.com", "1.1.1.1", "ua"); err != nil {
		t.Fatalf("发码失败：%v", err)
	}
	code := otpFromLastMail(t, fx.box)
	if err := fx.svc.ConfirmEmailChange(ctx, fx.u.ID, "new@example.com", code); err != nil {
		t.Fatalf("确认失败：%v", err)
	}

	after, _ := fx.fs.UserByID(ctx, fx.u.ID)
	if after.PasswordHash != before.PasswordHash {
		t.Fatal("换绑邮箱不该改口令哈希")
	}
	if after.TokenVersion != before.TokenVersion {
		t.Fatal("换绑邮箱不该自增 token_version（那会蹬掉所有设备）")
	}
}

// TestSendEmailChangeOTPCooldown 60 秒内连发 → ErrOTPTooSoon。
func TestSendEmailChangeOTPCooldown(t *testing.T) {
	fx := newEmailChangeFixture(t, "old@example.com")
	ctx := context.Background()

	if _, err := fx.svc.SendEmailChangeOTP(ctx, fx.u.ID, "new@example.com", "1.1.1.1", "ua"); err != nil {
		t.Fatalf("首次发码应当成功：%v", err)
	}
	_, err := fx.svc.SendEmailChangeOTP(ctx, fx.u.ID, "other@example.com", "1.1.1.1", "ua")
	if !errors.Is(err, ErrOTPTooSoon) {
		t.Fatalf("冷却期内应当返回 ErrOTPTooSoon，实际 %v", err)
	}
}

// TestSendEmailChangeOTPRejectsBadFormat 非法地址 → ErrBadEmail（不查库）。
func TestSendEmailChangeOTPRejectsBadFormat(t *testing.T) {
	fx := newEmailChangeFixture(t, "old@example.com")
	ctx := context.Background()

	for _, bad := range []string{"", "  ", "not-an-email", "a@b", "a b@example.com"} {
		_, err := fx.svc.SendEmailChangeOTP(ctx, fx.u.ID, bad, "1.1.1.1", "ua")
		if !errors.Is(err, ErrBadEmail) {
			t.Fatalf("地址 %q 应当返回 ErrBadEmail，实际 %v", bad, err)
		}
	}
}

// TestEmailChangeMailBodyMentionsChange 邮件文案要说清"换绑"，不是"注册/找回"。
//
// 用户收到一封写着"你正在完成注册"的信会以为账号被盗。
func TestEmailChangeMailBodyMentionsChange(t *testing.T) {
	fx := newEmailChangeFixture(t, "old@example.com")
	ctx := context.Background()

	if _, err := fx.svc.SendEmailChangeOTP(ctx, fx.u.ID, "new@example.com", "1.1.1.1", "ua"); err != nil {
		t.Fatalf("发码失败：%v", err)
	}
	msg := fx.box.last(t)
	if !strings.Contains(msg.Body, "换绑") {
		t.Fatalf("邮件正文应当说明这是换绑操作，实际正文：\n%s", msg.Body)
	}
	// 主题也不能说成"注册验证码"。
	if strings.Contains(msg.Subject, "注册") || strings.Contains(msg.Subject, "找回") {
		t.Fatalf("换绑邮件的主题不该沿用注册/找回的文案，实际 %q", msg.Subject)
	}
}
