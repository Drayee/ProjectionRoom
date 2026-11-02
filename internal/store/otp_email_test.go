package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// —— 换绑邮箱（票 #72）的验证码按地址过滤 ——
//
// 这一组用例只钉一件事，但它是整个换绑功能的**安全底座**：
// 一条验证码只能证明"发到哪个地址的那个码被读到了"，不能证明别的。

// TestConsumeEmailOTPForRejectsOtherAddress 是票 #72 的核心回归用例。
//
// 场景：账号 alice 请求换绑到 new@example.com，同时（或此前）还有一条
// 发给 old@example.com 的码。攻击者若拿"发给旧地址的码"来确认新地址，
// 就等于"证明你拥有新邮箱"这件事根本没被证明 —— 他只要让受害者收到
// 任意一条码，就能把账号绑到自己的邮箱上，之后走找回密码接管账号。
//
// 因此：发给 old 的码**必须**在确认 new 时被判为"不可用"。
func TestConsumeEmailOTPForRejectsOtherAddress(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	u := seedUser(t, s, "otp_email_scope")

	codeForOld := hexHash("otp-for-old-address")
	if err := s.InsertEmailOTP(ctx, &PasswordReset{
		UserID:    u.ID,
		TokenHash: codeForOld,
		Email:     strPtr("old@example.com"),
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}); err != nil {
		t.Fatalf("写入旧地址验证码失败：%v", err)
	}

	// 拿旧地址的码去确认新地址：必须失败。
	ok, err := s.ConsumeEmailOTPFor(ctx, u.ID, "new@example.com", codeForOld, 5)
	if ok {
		t.Fatal("发给旧地址的验证码不该能确认新地址")
	}
	if err != nil && !isOTPUnavailable(err) {
		t.Fatalf("地址不匹配应当是「不可用」而不是意外错误：%v", err)
	}

	// 同一条码对**它自己的**地址仍然可用（证明上面的失败是地址过滤造成的，
	// 而不是这条码本身坏了 —— 少了这一步，把 ConsumeEmailOTPFor 写成"永远返回
	// 不可用"也能让上面的断言通过）。
	ok, err = s.ConsumeEmailOTPFor(ctx, u.ID, "old@example.com", codeForOld, 5)
	if err != nil || !ok {
		t.Fatalf("发给该地址的码应当可用，ok=%v err=%v", ok, err)
	}
}

// TestConsumeEmailOTPForMatchesCaseInsensitively 地址比对必须归一。
//
// 库里存的是 canonicalEmail 归一后的全小写，而用户可能把邮箱输成
// Alice@Example.COM。若 SQL 里漏了 lower()，症状是"我明明填的就是这个邮箱"。
func TestConsumeEmailOTPForMatchesCaseInsensitively(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	u := seedUser(t, s, "otp_email_case")

	hash := hexHash("otp-case-insensitive")
	if err := s.InsertEmailOTP(ctx, &PasswordReset{
		UserID:    u.ID,
		TokenHash: hash,
		Email:     strPtr("Case@Example.COM"),
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}); err != nil {
		t.Fatalf("写入验证码失败：%v", err)
	}

	ok, err := s.ConsumeEmailOTPFor(ctx, u.ID, "case@example.com", hash, 5)
	if err != nil || !ok {
		t.Fatalf("大小写不同的同一地址应当可用，ok=%v err=%v", ok, err)
	}
}

// TestConsumeEmailOTPForEmptyAddressFailsClosed 空地址必须 fail closed。
//
// 若这里退化成"不按地址过滤"，那么任何一处漏传地址的调用都会静默变成
// "任意码都能用" —— 那是最糟的失败形态（功能照常、安全性归零）。
func TestConsumeEmailOTPForEmptyAddressFailsClosed(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	u := seedUser(t, s, "otp_email_empty")

	hash := hexHash("otp-empty-address")
	if err := s.InsertEmailOTP(ctx, &PasswordReset{
		UserID:    u.ID,
		TokenHash: hash,
		Email:     strPtr("real@example.com"),
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}); err != nil {
		t.Fatalf("写入验证码失败：%v", err)
	}

	ok, err := s.ConsumeEmailOTPFor(ctx, u.ID, "   ", hash, 5)
	if ok {
		t.Fatal("空地址不该确认任何东西")
	}
	if !errors.Is(err, ErrOTPNotFound) {
		t.Fatalf("空地址应当明确返回 ErrOTPNotFound，实际 %v", err)
	}
}
