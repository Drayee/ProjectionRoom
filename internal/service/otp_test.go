package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ProjectionRoom/internal/store"
)

// —— 找回密码：邮箱验证码链路 ——
//
// 本文件是「链接式改密下线、改走 6 位验证码」这次改造的判据落点。
// 它要钉住的核心事实是**未验证邮箱也能找回**——那正是旧通道解决不了、
// 也是用户实际踩到的那个问题：
//
//	旧行为：注册时填的邮箱没验证 → /forgot 恒定 200 → 不发信 → 用户永远收不到信，
//	        且界面上看不出任何异常（对外响应逐字相同）。
//	新行为：发码接口对未验证邮箱**照样发码**，因为码只到这个邮箱的真实持有者手里，
//	        攻击者填别人的地址拿不到码。
//
// 与之配套的判据（不泄露存在性、封禁不发、冷却期、用途隔离）也都在这里。

// otpFromMail 从验证码邮件正文里取出 6 位数字。
//
// 解析真实正文而不是构造预期值：这样"邮件里到底有没有码"也一并被验了。
func otpFromMail(t *testing.T, body string) string {
	t.Helper()
	// 正文形如「你正在进行「找回密码」操作，验证码是：\n\n    123456\n」。
	const marker = "验证码是："
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("邮件正文里找不到 %q：\n%s", marker, body)
	}
	rest := body[i+len(marker):]
	// 取第一段连续 6 位数字。
	start := -1
	for j, r := range rest {
		if r >= '0' && r <= '9' {
			if start < 0 {
				start = j
			}
			if j-start+1 == 6 {
				return rest[start : j+1]
			}
			continue
		}
		start = -1
	}
	t.Fatalf("邮件正文里找不到 6 位验证码：\n%s", body)
	return ""
}

// TestSendAccountOTPWorksForUnverifiedEmail 是本次改造的**核心判据**：
// 邮箱未验证也照样发码。
func TestSendAccountOTPWorksForUnverifiedEmail(t *testing.T) {
	f := newFakeStore()
	box := &captureMail{}
	svc := newTestService(t, f, AccountOptions{Mail: box})
	ctx := context.Background()

	// verified=false：这正是旧通道下"永远找不回密码"的那种账号。
	u := seedUserWithEmail(t, f, "alice", "alice@example.com", false, "")

	if _, err := svc.SendAccountOTP(ctx, "alice@example.com", "1.1.1.1", "ua"); err != nil {
		t.Fatalf("未验证邮箱也应当能收到码（对外恒定），实际 %v", err)
	}
	if box.count() != 1 {
		t.Fatalf("未验证邮箱必须真的发信（旧行为是不发），实际 %d 封", box.count())
	}
	if got := f.resetRowCount(store.PurposeEmailOTP); got != 1 {
		t.Fatalf("应当落一行验证码，实际 %d 行", got)
	}
	// 邮箱的验证状态**没有**被这次发码改变：发码不等于验证。
	after, _ := f.UserByID(ctx, u.ID)
	if after.EmailVerifiedAt != nil {
		t.Fatal("发码不该顺手把邮箱标记成已验证")
	}
}

// TestSendAccountOTPStaysConstantAcrossAccountStates 钉住"不泄露账号是否存在"。
//
// 四条路径（存在+已验证 / 存在+未验证 / 被封禁 / 不存在）对外必须是**同一个 nil**。
// 只有"确实该发信"的那两条会真的产生邮件与库行。
func TestSendAccountOTPStaysConstantAcrossAccountStates(t *testing.T) {
	f := newFakeStore()
	box := &captureMail{}
	svc := newTestService(t, f, AccountOptions{Mail: box})
	ctx := context.Background()

	seedUserWithEmail(t, f, "alice", "alice@example.com", true, "")
	seedUserWithEmail(t, f, "bob", "bob@example.com", false, "")
	seedUserWithEmail(t, f, "carol", "carol@example.com", true, store.StatusBanned)

	cases := []struct {
		name      string
		email     string
		wantMails int
	}{
		{"已验证的邮箱", "alice@example.com", 1},
		// 关键：未验证**也发**（这是与旧 Forgot 的差别）。
		{"未验证的邮箱", "bob@example.com", 1},
		// 封禁账号：改密成功也登不进来，不值得花配额。
		{"封禁账号的邮箱", "carol@example.com", 0},
		// 账号压根不存在：与上面三条**对外完全一样**。
		{"不存在的邮箱", "nobody@example.com", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := box.count()
			if _, err := svc.SendAccountOTP(ctx, tc.email, "1.1.1.1", "ua"); err != nil {
				t.Fatalf("对外必须恒定（不返回错误），实际 %v", err)
			}
			if got := box.count() - before; got != tc.wantMails {
				t.Fatalf("发信数 want=%d got=%d", tc.wantMails, got)
			}
		})
	}
}

func TestSendAccountOTPNormalizesEmail(t *testing.T) {
	f := newFakeStore()
	box := &captureMail{}
	svc := newTestService(t, f, AccountOptions{Mail: box})
	seedUserWithEmail(t, f, "alice", "alice@example.com", false, "")

	// 首尾空白 + 大小写不同：必须命中同一个账号（归一化的落点）。
	if _, err := svc.SendAccountOTP(context.Background(), "  ALICE@Example.COM ", "1.1.1.1", "ua"); err != nil {
		t.Fatalf("归一化后应当命中账号，实际 %v", err)
	}
	if box.count() != 1 {
		t.Fatalf("大小写/空白不同的同一邮箱应当命中，实际发信 %d 封", box.count())
	}
}

func TestSendAccountOTPRejectsBadEmailBeforeTouchingMail(t *testing.T) {
	f := newFakeStore()
	box := &captureMail{}
	svc := newTestService(t, f, AccountOptions{Mail: box})

	for _, bad := range []string{"", "   ", "not-an-email", "a@b", "a b@c.com", "a@b.com\nBcc: x@y.com", strings.Repeat("x", 300) + "@example.com"} {
		if _, err := svc.SendAccountOTP(context.Background(), bad, "1.1.1.1", "ua"); !errors.Is(err, ErrBadEmail) {
			t.Fatalf("邮箱 %q 应当被拒（ErrBadEmail），实际 %v", bad, err)
		}
	}
	if box.count() != 0 {
		t.Fatalf("格式非法的请求不该发信，实际 %d 封", box.count())
	}
}

func TestSendAccountOTPReportsMailUnavailableWhenNotConfigured(t *testing.T) {
	f := newFakeStore()
	// Mail 为 nil = 邮件能力未配置（handler 会把它翻成 503）。
	svc := newTestService(t, f, AccountOptions{})
	seedUserWithEmail(t, f, "alice", "alice@example.com", true, "")

	_, err := svc.SendAccountOTP(context.Background(), "alice@example.com", "1.1.1.1", "ua")
	if !errors.Is(err, ErrEmailOTPUnavailable) {
		t.Fatalf("邮件能力未配置时应当返回 ErrEmailOTPUnavailable，实际 %v", err)
	}
	// 能力没配好时**连码都不该落库**：落了就等于留下一堆永远发不出去的凭据。
	if got := f.resetRowCount(store.PurposeEmailOTP); got != 0 {
		t.Fatalf("邮件能力未配置时不该落码，实际 %d 行", got)
	}
}

func TestSendAccountOTPStaysConstantWhenQueueRejects(t *testing.T) {
	f := newFakeStore()
	box := &captureMail{reject: true} // 队列满 / 已关闭
	svc := newTestService(t, f, AccountOptions{Mail: box})
	seedUserWithEmail(t, f, "alice", "alice@example.com", false, "")

	// 队列拒收只记日志：**对外仍然是 nil**（handler 写同一个 200）。
	if _, err := svc.SendAccountOTP(context.Background(), "alice@example.com", "1.1.1.1", "ua"); err != nil {
		t.Fatalf("队列拒收不该改变对外结果，实际 %v", err)
	}
	// 码仍然在（先落库、后入队）：用户重试即可，不会出现"信到了但库里没有"。
	if got := f.resetRowCount(store.PurposeEmailOTP); got != 1 {
		t.Fatalf("入队失败也应当已经落库（顺序是先落库后入队），实际 %d 行", got)
	}
}

func TestSendAccountOTPCooldown(t *testing.T) {
	f := newFakeStore()
	box := &captureMail{}
	frozen := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	svc := newTestService(t, f, AccountOptions{
		Mail: box,
		Now:  func() time.Time { return frozen },
	})
	ctx := context.Background()
	seedUserWithEmail(t, f, "alice", "alice@example.com", false, "")

	if _, err := svc.SendAccountOTP(ctx, "alice@example.com", "", ""); err != nil {
		t.Fatalf("首次发码失败：%v", err)
	}
	if box.count() != 1 {
		t.Fatalf("首次发码应当发信，实际 %d 封", box.count())
	}

	// 冷却期内（60s）再发：**不报错**（报错等于确认了账号存在），但也不发信。
	if _, err := svc.SendAccountOTP(ctx, "alice@example.com", "", ""); err != nil {
		t.Fatalf("冷却期内也必须返回 nil（否则泄露存在性），实际 %v", err)
	}
	if box.count() != 1 {
		t.Fatalf("冷却期内不该再发信，实际 %d 封", box.count())
	}
}

// TestResetPasswordWithOTPWorksForUnverifiedEmail 是"未验证邮箱也能改密"的下半场：
// 发得出码，也要**用得上码**。
func TestResetPasswordWithOTPWorksForUnverifiedEmail(t *testing.T) {
	f := newFakeStore()
	box := &captureMail{}
	svc := newTestService(t, f, AccountOptions{Mail: box})
	ctx := context.Background()

	u := seedUserWithEmail(t, f, "alice", "alice@example.com", false, "")
	login, err := svc.Login(ctx, "alice", "abcd1234", "", "")
	if err != nil {
		t.Fatalf("登录失败：%v", err)
	}
	versionBefore := u.TokenVersion

	if _, err := svc.SendAccountOTP(ctx, "alice@example.com", "", ""); err != nil {
		t.Fatalf("发码失败：%v", err)
	}
	code := otpFromMail(t, box.last(t).Body)

	if err := svc.ResetPasswordWithOTP(ctx, "alice@example.com", code, "newpass123"); err != nil {
		t.Fatalf("凭验证码改密应当成功（未验证邮箱也要能走通）：%v", err)
	}

	// 新口令可用、旧口令失效。
	if _, err := svc.Login(ctx, "alice", "newpass123", "", ""); err != nil {
		t.Fatalf("新口令应当能登录：%v", err)
	}
	if _, err := svc.Login(ctx, "alice", "abcd1234", "", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("旧口令必须失效，实际 %v", err)
	}
	// 旧会话与旧 access 都必须失效。
	if _, err := svc.Refresh(ctx, login.RefreshToken, "", ""); err == nil {
		t.Fatal("改密后旧 refresh 必须失效")
	}
	if _, err := svc.VerifyAccessToken(ctx, login.AccessToken); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("改密后旧 access token 必须失效，实际 %v", err)
	}
	after, _ := f.UserByID(ctx, u.ID)
	if after.TokenVersion != versionBefore+1 {
		t.Fatalf("token_version 应当自增：want=%d got=%d", versionBefore+1, after.TokenVersion)
	}

	// 单次使用：同一个码不能再用。
	if err := svc.ResetPasswordWithOTP(ctx, "alice@example.com", code, "another123"); !errors.Is(err, ErrOTPInvalid) {
		t.Fatalf("验证码必须只能使用一次，实际 %v", err)
	}
}

func TestResetPasswordWithOTPRejectsWeakPasswordWithoutBurningCode(t *testing.T) {
	f := newFakeStore()
	box := &captureMail{}
	svc := newTestService(t, f, AccountOptions{Mail: box})
	ctx := context.Background()
	seedUserWithEmail(t, f, "alice", "alice@example.com", false, "")

	if _, err := svc.SendAccountOTP(ctx, "alice@example.com", "", ""); err != nil {
		t.Fatalf("发码失败：%v", err)
	}
	code := otpFromMail(t, box.last(t).Body)

	// 弱口令的错误必须与注册**逐字相同**（沿用同一套策略与文案）。
	_, regErr := svc.Register(ctx, "bob", "鲍勃", "short", "bob@example.com", "", "")
	if err := svc.ResetPasswordWithOTP(ctx, "alice@example.com", code, "short"); err == nil {
		t.Fatal("弱口令必须被拒")
	} else if err.Error() != regErr.Error() {
		t.Fatalf("弱口令文案必须与注册一致：otp=%q register=%q", err.Error(), regErr.Error())
	}

	// 关键：弱口令**不该烧掉验证码** —— 用户可以直接用同一封信里的码重试。
	if err := svc.ResetPasswordWithOTP(ctx, "alice@example.com", code, "newpass123"); err != nil {
		t.Fatalf("弱口令被拒后同一条码仍然可用，实际 %v", err)
	}
}

func TestResetPasswordWithOTPRejectsBannedAccount(t *testing.T) {
	f := newFakeStore()
	box := &captureMail{}
	svc := newTestService(t, f, AccountOptions{Mail: box})
	ctx := context.Background()
	seedUserWithEmail(t, f, "carol", "carol@example.com", true, store.StatusBanned)

	// 封禁账号连码都发不出去（SendAccountOTP 的门控），因此也没有码可验。
	if _, err := svc.SendAccountOTP(ctx, "carol@example.com", "", ""); err != nil {
		t.Fatalf("对外必须恒定，实际 %v", err)
	}
	if box.count() != 0 {
		t.Fatalf("封禁账号不该收到码，实际 %d 封", box.count())
	}
	// 即便有人手工造出一条码，校验也必须拒绝（状态在查库时就被挡住）。
	if err := svc.ResetPasswordWithOTP(ctx, "carol@example.com", "123456", "newpass123"); !errors.Is(err, ErrOTPInvalid) {
		t.Fatalf("封禁账号不得改密，实际 %v", err)
	}
}

func TestResetPasswordWithOTPRejectsUnknownAccount(t *testing.T) {
	f := newFakeStore()
	box := &captureMail{}
	svc := newTestService(t, f, AccountOptions{Mail: box})

	// 不区分"账号不存在"与"码不对"：两者都返回 ErrOTPInvalid。
	if err := svc.ResetPasswordWithOTP(context.Background(), "nobody@example.com", "123456", "newpass123"); !errors.Is(err, ErrOTPInvalid) {
		t.Fatalf("不存在的账号应当返回 ErrOTPInvalid，实际 %v", err)
	}
}

func TestResetPasswordWithOTPAttemptsExhausted(t *testing.T) {
	f := newFakeStore()
	box := &captureMail{}
	svc := newTestService(t, f, AccountOptions{Mail: box})
	ctx := context.Background()
	seedUserWithEmail(t, f, "alice", "alice@example.com", false, "")

	if _, err := svc.SendAccountOTP(ctx, "alice@example.com", "", ""); err != nil {
		t.Fatalf("发码失败：%v", err)
	}

	// 连续错满上限：前几次是"码不对"，最后一条是"这条码已作废"。
	var lastErr error
	for i := 0; i < otpMaxAttempts; i++ {
		lastErr = svc.ResetPasswordWithOTP(ctx, "alice@example.com", "000000", "newpass123")
	}
	if !errors.Is(lastErr, ErrOTPAttemptsExceeded) {
		t.Fatalf("错满 %d 次后应当报 ErrOTPAttemptsExceeded，实际 %v", otpMaxAttempts, lastErr)
	}
}
