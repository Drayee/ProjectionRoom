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
// ⚠ 本文件曾覆盖"忘记密码 → 重置密码"那条**链接**通道（Forgot / ResetPassword）。
// 那条通道已随 `/api/auth/forgot` + `/api/auth/reset` 一起删除，改密码现在只走
// 6 位验证码（用例见 otp_test.go）。因此这里只剩：
//
//  1. **验证邮箱令牌的生命周期**：生成 → 只存 sha256 → 校验 → 标记已用 → 再用必须失败；
//     过期与"用途不符"同样必须失败。
//  2. **链接形态**：域名来自配置（绝不从请求头推导）、令牌在 fragment 里。
//  3. **注册/管理员绑定**两条会落邮箱的路径。
//
// 为什么这些能放在没有 PostgreSQL 的机器上跑：AccountStore 是接口，下面的
// captureMail 也是接口实现 —— 而"库里只存 sha256"这类判据本身就是**纯函数性质**，
// 它不需要 SQL 才能验。真库侧的事务与唯一索引另有 store 包的用例。

// captureMail 是一个**捕获型**发信出口：它把邮件留在内存里。
//
// 它存在的唯一理由：库里只存 sha256，而"点开链接 / 抄出验证码"需要原文。
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

// —— 验证邮箱：令牌生命周期 ——

func TestVerifyEmailTokenLifecycle(t *testing.T) {
	f := newFakeStore()
	box := &captureMail{}
	svc := newTestService(t, f, AccountOptions{Mail: box})
	ctx := context.Background()

	// 注册会立刻发一封验证信（best-effort）。
	if _, err := svc.Register(ctx, "alice", "爱丽丝", "abcd1234", "alice@example.com", "", ""); err != nil {
		t.Fatalf("注册失败：%v", err)
	}
	if box.count() != 1 {
		t.Fatalf("注册后应当发出 1 封验证信，实际 %d", box.count())
	}
	token := tokenFromMail(t, box.last(t).Body)

	// ① 库里只有 sha256，且**不等于**原文。
	hash := HashOpaqueToken(token)
	sum := sha256.Sum256([]byte(token))
	if hash != hex.EncodeToString(sum[:]) {
		t.Fatal("落库形态必须是 sha256 十六进制")
	}
	f.mu.Lock()
	var stored *store.PasswordReset
	for _, r := range f.resets {
		if r.Purpose == store.PurposeEmailVerify {
			stored = r
		}
	}
	f.mu.Unlock()
	if stored == nil {
		t.Fatal("应当留下一行验证令牌")
	}
	if stored.TokenHash != hash {
		t.Fatalf("库里存的不是这串令牌的 sha256：%s", stored.TokenHash)
	}
	if strings.Contains(stored.TokenHash, token) {
		t.Fatal("库里出现了令牌原文 —— 那是一处真实的凭据泄漏")
	}

	// ② 首次验证成功。
	if err := svc.VerifyEmail(ctx, token); err != nil {
		t.Fatalf("首次验证应当成功：%v", err)
	}
	u, err := f.UserByID(ctx, stored.UserID)
	if err != nil {
		t.Fatalf("回读账号失败：%v", err)
	}
	if u.EmailVerifiedAt == nil {
		t.Fatal("验证之后 email_verified_at 必须落值")
	}

	// ③ 单次使用：再用同一串必须失败，且错误码与"非法/过期"完全一样。
	if err := svc.VerifyEmail(ctx, token); !errors.Is(err, ErrVerifyTokenInvalid) {
		t.Fatalf("令牌必须只能使用一次，实际 %v", err)
	}
}

func TestVerifyEmailTokenExpiryAndShape(t *testing.T) {
	f := newFakeStore()
	box := &captureMail{}
	svc := newTestService(t, f, AccountOptions{Mail: box})
	ctx := context.Background()
	u := seedUserWithEmail(t, f, "alice", "alice@example.com", false, "")

	// 过期的行：直接以"过期时刻在过去"的姿态落库。
	// 用显式的时间点而不是"把 TTL 设成 1ms 再 sleep"是刻意的 —— 后者依赖
	// 应用时钟与数据库时钟一致（真库在隧道另一头时那种假设不成立）。
	// 过期判定本身由 store 的 `expires_at > now()` 承担，这里验的是它的后果。
	past := time.Now().Add(-time.Minute)
	addr := "alice@example.com"
	f.mu.Lock()
	f.nextResetID++
	f.resets[f.nextResetID] = &store.PasswordReset{
		ID: f.nextResetID, UserID: u.ID, Purpose: store.PurposeEmailVerify,
		TokenHash: HashOpaqueToken("expired-token"), Email: &addr, ExpiresAt: past,
	}
	f.mu.Unlock()

	if err := svc.VerifyEmail(ctx, "expired-token"); !errors.Is(err, ErrVerifyTokenInvalid) {
		t.Fatalf("过期令牌必须被拒，实际 %v", err)
	}

	// 形状不对/空令牌：同样是同一个错误（不区分，避免探测）。
	for _, bad := range []string{"", "   ", "a", "!!!not-base64!!!"} {
		if err := svc.VerifyEmail(ctx, bad); !errors.Is(err, ErrVerifyTokenInvalid) {
			t.Fatalf("令牌 %q 应当被拒（ErrVerifyTokenInvalid），实际 %v", bad, err)
		}
	}
}

// TestVerifyTokenNotUsableAsOTP 钉住"用途隔离"：验证链接的令牌**不能**当验证码用。
//
// 这条判据在删掉链接式改密之后仍然重要：改密码走 OTP 表里的
// purpose=email_otp 行，而验证链接是 purpose=email_verify 行 ——
// 两者的消费入口查的是不同 purpose，因此一串链接令牌猜不中任何一条 OTP。
func TestVerifyTokenNotUsableAsOTP(t *testing.T) {
	f := newFakeStore()
	box := &captureMail{}
	svc := newTestService(t, f, AccountOptions{Mail: box})
	ctx := context.Background()

	if _, err := svc.Register(ctx, "alice", "爱丽丝", "abcd1234", "alice@example.com", "", ""); err != nil {
		t.Fatalf("注册失败：%v", err)
	}
	verifyToken := tokenFromMail(t, box.last(t).Body)

	// 拿验证链接的令牌当验证码去改密：必须是"码不对"，而不是"改密成功"。
	err := svc.ResetPasswordWithOTP(ctx, "alice@example.com", verifyToken, "newpass123")
	if !errors.Is(err, ErrOTPInvalid) {
		t.Fatalf("验证链接令牌不得当作验证码使用，实际 %v", err)
	}
	// 而且这一次尝试不能把验证令牌烧掉（消费查的是 purpose，WHERE 不命中就什么都不改）。
	if err := svc.VerifyEmail(ctx, verifyToken); err != nil {
		t.Fatalf("用途不符的尝试不该烧掉验证令牌：%v", err)
	}
}

// —— 验证邮件的形态 ——

func TestEmailLinkShapeAndNoHostDerivation(t *testing.T) {
	f := newFakeStore()
	box := &captureMail{}
	svc := newTestService(t, f, AccountOptions{
		Mail:          box,
		PublicBaseURL: "https://pr.example.com/",
	})
	ctx := context.Background()

	if _, err := svc.Register(ctx, "alice", "爱丽丝", "abcd1234", "alice@example.com", "", ""); err != nil {
		t.Fatalf("注册失败：%v", err)
	}
	body := box.last(t).Body
	// 链接前缀必须来自配置（并去掉尾斜杠），令牌在 fragment 里。
	if !strings.Contains(body, "https://pr.example.com/verify#token=") {
		t.Fatalf("链接形态不对（应当是 <base>/verify#token=<串>）：\n%s", body)
	}
	// 正文里不能有明文口令、也不能出现 "24h0m0s" 这种内部细节。
	if strings.Contains(body, "abcd1234") {
		t.Fatal("正文里出现了明文口令")
	}
	if strings.Contains(body, "24h0m0s") {
		t.Fatalf("有效期必须以人话给出：\n%s", body)
	}
	if !strings.Contains(body, "24 小时") {
		t.Fatalf("正文必须说明有效期：\n%s", body)
	}
	if !strings.Contains(body, "忽略") {
		t.Fatalf("正文必须有「不是你本人操作就忽略」一句：\n%s", body)
	}
}

func TestVerifyEmailTTLFromInjectedClock(t *testing.T) {
	f := newFakeStore()
	box := &captureMail{}
	frozen := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	svc := newTestService(t, f, AccountOptions{
		Mail:           box,
		Now:            func() time.Time { return frozen },
		EmailVerifyTTL: 17 * time.Minute,
	})

	if _, err := svc.Register(context.Background(), "alice", "爱丽丝", "abcd1234", "alice@example.com", "", ""); err != nil {
		t.Fatalf("注册失败：%v", err)
	}
	f.mu.Lock()
	var row *store.PasswordReset
	for _, r := range f.resets {
		if r.Purpose == store.PurposeEmailVerify {
			row = r
		}
	}
	f.mu.Unlock()
	if row == nil {
		t.Fatal("注册应当留下一行验证令牌")
	}

	// 有效期由**注入的时钟**推导：可注入时钟的意义就是让这类断言不依赖墙上时间。
	ensureTimeEqual(t, "令牌 expires_at", frozen.Add(17*time.Minute), row.ExpiresAt)
	ensureTimeEqual(t, "令牌 created_at", frozen, row.CreatedAt)
}

// —— 落邮箱的两条路径 ——

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
