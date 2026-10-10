package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"ProjectionRoom/internal/service/auth"
	"ProjectionRoom/internal/service/mail"
	"ProjectionRoom/internal/store"
)

// —— 一次性邮件链路的用例 ——
//
// 本文件覆盖的判据分成三组：
//
//  1. **令牌生命周期**：生成 → 只存 sha256 → 校验 → 标记已用 → 再用必须失败；
//     过期与"用途不符"同样必须失败。
//  2. **找回入口的对外恒定**：邮箱（不存在 / 未验证 / 已封禁）一律返回 nil，
//     且**不发信、不落行**。这是"不泄露账号是否存在"的落点。
//  3. **重置的原子后果**：改密成功的同时，该账号全部会话被撤销、token_version 自增
//     （旧 access 与旧 refresh 都必须失效）。
//
// 为什么这些能放在没有 PostgreSQL 的机器上跑：AccountStore 是接口，下面的
// captureMail 也是接口实现 —— 而"库里只存 sha256"这类判据本身就是**纯函数性质**，
// 它不需要 SQL 才能验。真库侧的事务与唯一索引另有 store 包的用例。

// captureMail 是一个**捕获型**发信出口：它把邮件留在内存里。
//
// 它存在的唯一理由：库里只存 sha256，而"点开链接"这条路径需要原文。
// 捕获发信内容是我们唯一既能拿到原文、又不破坏"响应体里没有令牌"这条安全契约的
// 办法 —— 绝不为了测试方便给接口加一个回显字段。
type captureMail struct {
	mu     sync.Mutex
	msgs   []mail.Outbound
	reject bool
	// block 非 nil 时 Enqueue 会一直等到它被关闭（用来验"入队不阻塞调用方"）。
	block chan struct{}
}

func (c *captureMail) Enqueue(msg mail.Outbound) bool {
	if c.block != nil {
		<-c.block
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reject {
		return false
	}
	c.msgs = append(c.msgs, msg)
	return true
}

func (c *captureMail) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.msgs)
}

func (c *captureMail) last(t *testing.T) mail.Outbound {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.msgs) == 0 {
		t.Fatal("没有捕获到任何邮件")
	}
	return c.msgs[len(c.msgs)-1]
}

// tokenFromMail 从邮件正文里取出一次性链接上的令牌。
//
// 解析的是真实正文（不是模拟），因此顺带钉住了"链接形态"本身：
// 断言里带 `#token=`，所以链接改成 query 形态会直接让这些用例变红。
func tokenFromMail(t *testing.T, body string) string {
	t.Helper()
	const marker = "#token="
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("邮件正文里找不到 %q：\n%s", marker, body)
	}
	rest := body[i+len(marker):]
	if j := strings.IndexAny(rest, " \r\n"); j >= 0 {
		rest = rest[:j]
	}
	if rest == "" {
		t.Fatal("邮件正文里的令牌为空")
	}
	return rest
}

// seedUserWithEmail 造一个账号并直接设定邮箱/验证状态（跳过注册流程）。
func seedUserWithEmail(t *testing.T, f *fakeStore, username, email string, verified bool, status string) *store.User {
	t.Helper()
	id := seedAccount(t, f, username, "abcd1234")
	f.mu.Lock()
	defer f.mu.Unlock()
	u := f.users[id]
	if email != "" {
		addr := strings.ToLower(email)
		u.Email = &addr
		if verified {
			now := time.Now()
			u.EmailVerifiedAt = &now
		}
	}
	if status != "" {
		u.Status = status
	}
	cp := *u
	return &cp
}

// —— 找回：门控与对外恒定 ——

func TestForgotSendsOnlyForVerifiedEmail(t *testing.T) {
	f := newFakeStore()
	box := &captureMail{}
	svc := newTestService(t, f, AccountOptions{Mail: box})
	ctx := context.Background()

	verified := seedUserWithEmail(t, f, "alice", "alice@example.com", true, "")
	seedUserWithEmail(t, f, "bob", "bob@example.com", false, "")
	seedUserWithEmail(t, f, "carol", "carol@example.com", true, store.StatusBanned)
	seedUserWithEmail(t, f, "dave", "", false, "")

	cases := []struct {
		name       string
		email      string
		wantMails  int
		wantUserID int64
	}{
		{"已验证的邮箱", "alice@example.com", 1, verified.ID},
		// 未验证 = 归属没有被证明。发信会让任何人靠"注册时填别人的邮箱"
		// 把受害者的找回入口变成自己的。
		{"未验证的邮箱", "bob@example.com", 0, 0},
		// 封禁账号：改密也登不进来（登录在口令通过后仍因 status 拒绝），不值得花配额。
		{"封禁账号的邮箱", "carol@example.com", 0, 0},
		// 账号压根不存在：与上面两条**对外完全一样**。
		{"不存在的邮箱", "nobody@example.com", 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := box.count()
			// 关键断言①：四条路径**都返回 nil**（handler 因此都写同一个 200）。
			if err := svc.Forgot(ctx, tc.email, "1.1.1.1", "ua"); err != nil {
				t.Fatalf("Forgot 不该返回错误（对外必须恒定），实际 %v", err)
			}
			if got := box.count() - before; got != tc.wantMails {
				t.Fatalf("发信数 want=%d got=%d", tc.wantMails, got)
			}
		})
	}

	// 关键断言②：只有"已验证 + 活跃"那一条**落了库**。
	if got := f.resetRowCount(store.PurposePasswordReset); got != 1 {
		t.Fatalf("只有已验证的邮箱该留下令牌行，实际 %d 行", got)
	}
	if got := f.resetRowCount(store.PurposeEmailVerify); got != 0 {
		t.Fatalf("找回不该产生验证用途的行，实际 %d 行", got)
	}
	_ = verified
}

func TestForgotNormalizesEmailBeforeLookupAndLimiter(t *testing.T) {
	f := newFakeStore()
	box := &captureMail{}
	svc := newTestService(t, f, AccountOptions{Mail: box})
	seedUserWithEmail(t, f, "alice", "alice@example.com", true, "")

	// 首尾空白 + 大小写不同：必须命中同一个账号（归一化的落点）。
	if err := svc.Forgot(context.Background(), "  ALICE@Example.COM ", "1.1.1.1", "ua"); err != nil {
		t.Fatalf("归一化后应当命中账号，实际 %v", err)
	}
	if box.count() != 1 {
		t.Fatalf("大小写/空白不同的同一邮箱应当命中，实际发信 %d 封", box.count())
	}
}

func TestForgotRejectsBadEmailBeforeTouchingMail(t *testing.T) {
	f := newFakeStore()
	box := &captureMail{}
	svc := newTestService(t, f, AccountOptions{Mail: box})

	for _, bad := range []string{"", "   ", "not-an-email", "a@b", "a b@c.com", "a@b.com\nBcc: x@y.com", strings.Repeat("x", 300) + "@example.com"} {
		if err := svc.Forgot(context.Background(), bad, "1.1.1.1", "ua"); !errors.Is(err, ErrBadEmail) {
			t.Fatalf("邮箱 %q 应当被拒（ErrBadEmail），实际 %v", bad, err)
		}
	}
	if box.count() != 0 {
		t.Fatalf("格式非法的请求不该发信，实际 %d 封", box.count())
	}
}

func TestForgotReportsMailUnavailableWhenNotConfigured(t *testing.T) {
	f := newFakeStore()
	// Mail 为 nil = 邮件能力未配置（handler 会把它翻成 503）。
	svc := newTestService(t, f, AccountOptions{})
	seedUserWithEmail(t, f, "alice", "alice@example.com", true, "")

	err := svc.Forgot(context.Background(), "alice@example.com", "1.1.1.1", "ua")
	if !errors.Is(err, ErrMailUnavailable) {
		t.Fatalf("邮件能力未配置时应当返回 ErrMailUnavailable，实际 %v", err)
	}
	// 能力没配好时**连令牌都不该落库**：落了就等于留下一堆永远发不出去的凭据。
	if got := f.resetRowCount(store.PurposePasswordReset); got != 0 {
		t.Fatalf("邮件能力未配置时不该落令牌行，实际 %d 行", got)
	}
}

func TestForgotStaysConstantWhenQueueRejects(t *testing.T) {
	f := newFakeStore()
	box := &captureMail{reject: true} // 队列满 / 已关闭
	svc := newTestService(t, f, AccountOptions{Mail: box})
	seedUserWithEmail(t, f, "alice", "alice@example.com", true, "")

	// 队列拒收只记日志：**对外仍然是 nil**（handler 写同一个 200）。
	if err := svc.Forgot(context.Background(), "alice@example.com", "1.1.1.1", "ua"); err != nil {
		t.Fatalf("队列拒收不该改变对外结果，实际 %v", err)
	}
	// 令牌行仍然在（先落库、后入队）：用户重试即可，不会出现"信到了但库里没有"。
	if got := f.resetRowCount(store.PurposePasswordReset); got != 1 {
		t.Fatalf("入队失败也应当已经落库（顺序是先落库后入队），实际 %d 行", got)
	}
}

// —— 令牌的生命周期 ——

func TestResetTokenLifecycle(t *testing.T) {
	f := newFakeStore()
	box := &captureMail{}
	svc := newTestService(t, f, AccountOptions{Mail: box})
	ctx := context.Background()
	u := seedUserWithEmail(t, f, "alice", "alice@example.com", true, "")

	// 登录一次，好让"改密撤销全部会话"有东西可撤。
	login, err := svc.Login(ctx, "alice", "abcd1234", "", "")
	if err != nil {
		t.Fatalf("登录失败：%v", err)
	}
	if _, err := svc.VerifyAccessToken(ctx, login.AccessToken); err != nil {
		t.Fatalf("登录后的 access token 应当有效：%v", err)
	}

	if err := svc.Forgot(ctx, "alice@example.com", "", ""); err != nil {
		t.Fatalf("Forgot 失败：%v", err)
	}
	body := box.last(t).Body
	token := tokenFromMail(t, body)

	// ① 库里只有 sha256，且**不等于**原文。
	hash := HashOpaqueToken(token)
	sum := sha256.Sum256([]byte(token))
	if hash != hex.EncodeToString(sum[:]) {
		t.Fatalf("落库形态必须是 sha256 十六进制")
	}
	f.mu.Lock()
	stored := *f.resets[1]
	f.mu.Unlock()
	if stored.TokenHash != hash {
		t.Fatalf("库里存的不是这串令牌的 sha256：%s", stored.TokenHash)
	}
	if strings.Contains(stored.TokenHash, token) {
		t.Fatal("库里出现了令牌原文 —— 那是一处真实的凭据泄漏")
	}
	if stored.Purpose != store.PurposePasswordReset {
		t.Fatalf("用途不符：%s", stored.Purpose)
	}

	// ② 首次消费成功：口令换掉、会话全撤销、版本号自增。
	if err := svc.ResetPassword(ctx, token, "newpass123"); err != nil {
		t.Fatalf("首次重置应当成功：%v", err)
	}
	if _, err := svc.Login(ctx, "alice", "newpass123", "", ""); err != nil {
		t.Fatalf("新口令应当能登录：%v", err)
	}
	if _, err := svc.Login(ctx, "alice", "abcd1234", "", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("旧口令必须失效，实际 %v", err)
	}
	// 旧 refresh 必须失效（会话被撤销）。
	if _, err := svc.Refresh(ctx, login.RefreshToken, "", ""); err == nil {
		t.Fatal("改密后旧 refresh 必须失效")
	}
	// 旧 access 必须失效（token_version 变了）。
	if _, err := svc.VerifyAccessToken(ctx, login.AccessToken); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("改密后旧 access token 必须失效，实际 %v", err)
	}

	// ③ 单次使用：再用同一个令牌必须失败，且错误码与"非法/过期"完全一样。
	if err := svc.ResetPassword(ctx, token, "another123"); !errors.Is(err, ErrResetTokenInvalid) {
		t.Fatalf("令牌必须只能使用一次，实际 %v", err)
	}
	if _, err := svc.Login(ctx, "alice", "another123", "", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("被拒的第二次重置不该改掉口令")
	}
	if u.ID != stored.UserID {
		t.Fatal("令牌行必须绑定到发起找回的那个账号")
	}
}

func TestResetTokenExpiryAndShape(t *testing.T) {
	f := newFakeStore()
	box := &captureMail{}
	svc := newTestService(t, f, AccountOptions{Mail: box})
	ctx := context.Background()
	seedUserWithEmail(t, f, "alice", "alice@example.com", true, "")

	// 过期的行：直接以"过期时刻在过去"的姿态落库。
	// 用显式的时间点而不是"把 TTL 设成 1ms 再 sleep"是刻意的 —— 后者依赖
	// 应用时钟与数据库时钟一致（真库在隧道另一头时那种假设不成立）。
	// 过期判定本身由 store 的 `expires_at > now()` 承担，这里验的是它的后果。
	past := time.Now().Add(-time.Minute)
	f.mu.Lock()
	f.nextResetID++
	f.resets[f.nextResetID] = &store.PasswordReset{
		ID: f.nextResetID, UserID: 1, Purpose: store.PurposePasswordReset,
		TokenHash: HashOpaqueToken("expired-token"), ExpiresAt: past,
	}
	f.mu.Unlock()

	if err := svc.ResetPassword(ctx, "expired-token", "newpass123"); !errors.Is(err, ErrResetTokenInvalid) {
		t.Fatalf("过期令牌必须被拒，实际 %v", err)
	}

	// 形状不对/空令牌：同样是同一个错误（不区分，避免探测）。
	for _, bad := range []string{"", "   ", "a", "!!!not-base64!!!"} {
		if err := svc.ResetPassword(ctx, bad, "newpass123"); !errors.Is(err, ErrResetTokenInvalid) {
			t.Fatalf("令牌 %q 应当被拒（ErrResetTokenInvalid），实际 %v", bad, err)
		}
	}
}

func TestResetRejectsWeakPasswordWithoutBurningToken(t *testing.T) {
	f := newFakeStore()
	box := &captureMail{}
	svc := newTestService(t, f, AccountOptions{Mail: box})
	ctx := context.Background()
	seedUserWithEmail(t, f, "alice", "alice@example.com", true, "")

	if err := svc.Forgot(ctx, "alice@example.com", "", ""); err != nil {
		t.Fatalf("Forgot 失败：%v", err)
	}
	token := tokenFromMail(t, box.last(t).Body)

	// 弱口令的错误必须与注册**逐字相同**（沿用同一套策略与文案）。
	_, regErr := svc.Register(ctx, "bob", "鲍勃", "short", "bob@example.com", "", "")
	if err := svc.ResetPassword(ctx, token, "short"); err == nil {
		t.Fatal("弱口令必须被拒")
	} else if err.Error() != regErr.Error() {
		t.Fatalf("弱口令文案必须与注册一致：reset=%q register=%q", err.Error(), regErr.Error())
	}

	// 关键：弱口令**不该烧掉令牌** —— 用户可以直接用同一封邮件里的链接重试。
	if err := svc.ResetPassword(ctx, token, "newpass123"); err != nil {
		t.Fatalf("弱口令被拒后同一令牌仍然可用，实际 %v", err)
	}
}

func TestTokenPurposeIsolation(t *testing.T) {
	f := newFakeStore()
	box := &captureMail{}
	svc := newTestService(t, f, AccountOptions{Mail: box})
	ctx := context.Background()
	seedUserWithEmail(t, f, "alice", "alice@example.com", true, "")

	// 注册一个账号以触发"验证邮箱"用途的令牌（走真实路径）。
	if _, err := svc.Register(ctx, "bob", "鲍勃", "abcd1234", "bob@example.com", "", ""); err != nil {
		t.Fatalf("注册失败：%v", err)
	}
	if got := f.resetRowCount(store.PurposeEmailVerify); got != 1 {
		t.Fatalf("注册应当留下一行验证令牌，实际 %d 行", got)
	}
	verifyToken := tokenFromMail(t, box.last(t).Body)

	// ① 验证令牌**不能**当重置令牌用。
	if err := svc.ResetPassword(ctx, verifyToken, "newpass123"); !errors.Is(err, ErrResetTokenInvalid) {
		t.Fatalf("验证令牌不得用于重置密码，实际 %v", err)
	}
	// 而且这一次尝试不能把它烧掉（消费查的是 purpose，WHERE 不命中就什么都不改）。
	if err := svc.VerifyEmail(ctx, verifyToken); err != nil {
		t.Fatalf("用途不符的尝试不该烧掉验证令牌：%v", err)
	}

	// ② 重置令牌也不能当验证令牌用。
	if err := svc.Forgot(ctx, "alice@example.com", "", ""); err != nil {
		t.Fatalf("Forgot 失败：%v", err)
	}
	resetToken := tokenFromMail(t, box.last(t).Body)
	if err := svc.VerifyEmail(ctx, resetToken); !errors.Is(err, ErrVerifyTokenInvalid) {
		t.Fatalf("重置令牌不得用于验证邮箱，实际 %v", err)
	}
	if err := svc.ResetPassword(ctx, resetToken, "newpass123"); err != nil {
		t.Fatalf("用途不符的尝试不该烧掉重置令牌：%v", err)
	}
}

func TestVerifyEmailThenForgotWorks(t *testing.T) {
	f := newFakeStore()
	box := &captureMail{}
	svc := newTestService(t, f, AccountOptions{Mail: box})
	ctx := context.Background()

	// 注册 → 邮箱落库但**未验证**。
	if _, err := svc.Register(ctx, "alice", "爱丽丝", "abcd1234", "alice@example.com", "", ""); err != nil {
		t.Fatalf("注册失败：%v", err)
	}
	// 未验证期间：找回恒定成功，但不发信、不落行。
	if err := svc.Forgot(ctx, "alice@example.com", "", ""); err != nil {
		t.Fatalf("未验证期间的找回也必须恒定成功：%v", err)
	}
	if got := f.resetRowCount(store.PurposePasswordReset); got != 0 {
		t.Fatalf("未验证邮箱不该产生重置令牌，实际 %d 行", got)
	}

	// 点开验证链接。
	verifyToken := tokenFromMail(t, box.last(t).Body)
	if err := svc.VerifyEmail(ctx, verifyToken); err != nil {
		t.Fatalf("验证邮箱失败：%v", err)
	}
	u, err := f.UserByID(ctx, 1)
	if err != nil {
		t.Fatalf("回读账号失败：%v", err)
	}
	if u.EmailVerifiedAt == nil {
		t.Fatal("验证之后 email_verified_at 必须落值")
	}
	// 验证令牌单次使用。
	if err := svc.VerifyEmail(ctx, verifyToken); !errors.Is(err, ErrVerifyTokenInvalid) {
		t.Fatalf("验证令牌必须单次使用，实际 %v", err)
	}

	// 验证之后：这个邮箱**从此可以找回**。
	if err := svc.Forgot(ctx, "alice@example.com", "", ""); err != nil {
		t.Fatalf("验证后找回失败：%v", err)
	}
	if got := f.resetRowCount(store.PurposePasswordReset); got != 1 {
		t.Fatalf("验证之后应当能产生重置令牌，实际 %d 行", got)
	}
}

func TestResetTTLFromInjectedClock(t *testing.T) {
	f := newFakeStore()
	box := &captureMail{}
	frozen := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	svc := newTestService(t, f, AccountOptions{
		Mail:     box,
		Now:      func() time.Time { return frozen },
		ResetTTL: 17 * time.Minute,
	})
	seedUserWithEmail(t, f, "alice", "alice@example.com", true, "")

	if err := svc.Forgot(context.Background(), "alice@example.com", "", ""); err != nil {
		t.Fatalf("Forgot 失败：%v", err)
	}
	f.mu.Lock()
	row := *f.resets[1]
	f.mu.Unlock()

	// 有效期由**注入的时钟**推导：可注入时钟的意义就是让这类断言不依赖墙上时间。
	ensureTimeEqual(t, "令牌 expires_at", frozen.Add(17*time.Minute), row.ExpiresAt)
	ensureTimeEqual(t, "令牌 created_at", frozen, row.CreatedAt)
}

func TestRegisterRequiresAndStoresEmail(t *testing.T) {
	f := newFakeStore()
	svc := newTestService(t, f, AccountOptions{Mail: &captureMail{}})
	ctx := context.Background()

	// 缺邮箱 = 不可接受：账号必须有邮箱才可能走找回。
	if _, err := svc.Register(ctx, "alice", "爱丽丝", "abcd1234", "", "", ""); !errors.Is(err, ErrBadEmail) {
		t.Fatalf("缺邮箱应当被拒（ErrBadEmail），实际 %v", err)
	}
	// 邮箱唯一：同一个邮箱不能绑到两个账号。
	if _, err := svc.Register(ctx, "alice", "爱丽丝", "abcd1234", "same@example.com", "", ""); err != nil {
		t.Fatalf("首次注册失败：%v", err)
	}
	if _, err := svc.Register(ctx, "bob", "鲍勃", "abcd1234", "SAME@example.com", "", ""); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("同一邮箱（大小写不同）必须被判为已占用，实际 %v", err)
	}
	// 落库形态是全小写（与 users_email_lower_key 这个函数索引同口径）。
	u, err := f.UserByEmail(ctx, "SAME@EXAMPLE.COM")
	if err != nil {
		t.Fatalf("按邮箱回查失败：%v", err)
	}
	if u.Email == nil || *u.Email != "same@example.com" {
		t.Fatalf("邮箱必须以全小写落库，实际 %v", u.Email)
	}
	// 注册时邮箱**必然未验证**（要收一封验证信）。
	if u.EmailVerifiedAt != nil {
		t.Fatal("注册时邮箱不可能是已验证状态")
	}
}

func TestBindUserEmailMarksVerifiedAndAuditsOnce(t *testing.T) {
	f := newFakeStore()
	svc := newTestService(t, f, AccountOptions{Mail: &captureMail{}})
	ctx := context.Background()
	id := seedAccount(t, f, "alice", "abcd1234")

	const actor = int64(99)
	if err := svc.BindUserEmail(ctx, actor, id, "  Alice@Example.COM ", "1.2.3.4"); err != nil {
		t.Fatalf("管理员绑定邮箱失败：%v", err)
	}
	u, err := f.UserByID(ctx, id)
	if err != nil {
		t.Fatalf("回读失败：%v", err)
	}
	if u.Email == nil || *u.Email != "alice@example.com" {
		t.Fatalf("邮箱应当归一化后落库，实际 %v", u.Email)
	}
	if u.EmailVerifiedAt == nil {
		t.Fatal("管理员绑定即已验证（这是绕开验证链鸡生蛋的那条路）")
	}
	// 绑定之后**立刻**可以找回。
	if err := svc.Forgot(ctx, "alice@example.com", "", ""); err != nil {
		t.Fatalf("绑定后找回失败：%v", err)
	}

	// 审计只记一条（异步投递：等它到达）。
	waitFor(t, 2*time.Second, "user.email_bind 审计到达", func() bool {
		return len(f.auditRows("user.email_bind")) == 1
	})
	audits := f.auditRows("user.email_bind")
	if audits[0].ActorID != actor || audits[0].TargetID != "1" {
		t.Fatalf("审计的 actor/target 不对：%+v", audits[0])
	}

	// 同一个邮箱再绑一次：**真正的 no-op**，不写第二行审计。
	if err := svc.BindUserEmail(ctx, actor, id, "alice@example.com", "1.2.3.4"); err != nil {
		t.Fatalf("重复绑定应当成功（幂等）：%v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := len(f.auditRows("user.email_bind")); got != 1 {
		t.Fatalf("no-op 的绑定不该写审计，实际 %d 条", got)
	}

	// 邮箱格式非法：与"已被占用"是不同的错误。
	if err := svc.BindUserEmail(ctx, actor, id, "not-an-email", ""); !errors.Is(err, ErrBadEmail) {
		t.Fatalf("非法邮箱应当被拒，实际 %v", err)
	}
	// 解绑：两列一起清空，并留下一条 unbind 审计。
	if err := svc.BindUserEmail(ctx, actor, id, "", ""); err != nil {
		t.Fatalf("解绑失败：%v", err)
	}
	u, _ = f.UserByID(ctx, id)
	if u.Email != nil || u.EmailVerifiedAt != nil {
		t.Fatalf("解绑必须同时清掉验证时间，实际 email=%v verified=%v", u.Email, u.EmailVerifiedAt)
	}
	waitFor(t, 2*time.Second, "user.email_unbind 审计到达", func() bool {
		return len(f.auditRows("user.email_unbind")) == 1
	})
}

func TestEmailLinkShapeAndNoHostDerivation(t *testing.T) {
	f := newFakeStore()
	box := &captureMail{}
	svc := newTestService(t, f, AccountOptions{
		Mail:          box,
		PublicBaseURL: "https://pr.example.com/",
	})
	seedUserWithEmail(t, f, "alice", "alice@example.com", true, "")

	if err := svc.Forgot(context.Background(), "alice@example.com", "", ""); err != nil {
		t.Fatalf("Forgot 失败：%v", err)
	}
	body := box.last(t).Body
	// 链接前缀必须来自配置（并去掉尾斜杠），令牌在 fragment 里。
	if !strings.Contains(body, "https://pr.example.com/reset#token=") {
		t.Fatalf("链接形态不对（应当是 <base>/reset#token=<串>）：\n%s", body)
	}
	// 正文里不能有明文口令、也不能出现 "30m0s" 这种内部细节。
	if strings.Contains(body, "abcd1234") {
		t.Fatal("正文里出现了明文口令")
	}
	if strings.Contains(body, "30m0s") {
		t.Fatalf("有效期必须以人话给出：\n%s", body)
	}
	if !strings.Contains(body, "30 分钟") {
		t.Fatalf("正文必须说明有效期：\n%s", body)
	}
	if !strings.Contains(body, "忽略") {
		t.Fatalf("正文必须有「不是你本人操作就忽略」一句：\n%s", body)
	}
}

// —— 辅助 ——

// ensureTimeEqual 断言两个时间点相同（给 1µs 容差，避开 timestamptz 的微秒分辨率）。
func ensureTimeEqual(t *testing.T, what string, want, got time.Time) {
	t.Helper()
	if d := got.Sub(want); d > time.Microsecond || d < -time.Microsecond {
		t.Fatalf("%s 不对：want=%s got=%s", what, want.Format(time.RFC3339Nano), got.Format(time.RFC3339Nano))
	}
}

// auditRows 返回某个 action 的审计行（副本）。既有的 auditActions 只回 action 名，
// 这里需要看 actor/target，所以是另一个更窄的读取口。
func (f *fakeStore) auditRows(action string) []store.AdminAudit {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.AdminAudit, 0, len(f.audits))
	for _, a := range f.audits {
		if a.Action == action {
			out = append(out, a)
		}
	}
	return out
}

// 让"注册时用的密码策略"与用例里的口令保持显式关系（避免用例与策略漂移）。
var _ = auth.ValidatePasswordPolicy
