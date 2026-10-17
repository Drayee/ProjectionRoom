package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// strPtr 取一个字符串指针（PasswordReset.Email 是可空列）。
func strPtr(s string) *string { return &s }

// isOTPUnavailable 判断一个错误是否属于"这条码不可用"的那一类。
//
// 两者对调用方是同一个结论（要重新获取一条），但其中的"次数用尽"需要单独文案，
// 因此 store 层分成两个哨兵。用例里只关心"是不是那类"，用这个函数收口。
func isOTPUnavailable(err error) bool {
	return errors.Is(err, ErrOTPNotFound) || errors.Is(err, ErrOTPAttemptsExceeded)
}

// —— 邮箱验证码（OTP）的真库用例 ——
//
// 这里只覆盖**必须在真库上验**的那几条：SQL 的空值语义、单次消费的原子性、
// 错误计数的落库形态。纯逻辑（验证码位数、冷却期）不在这里。

// TestLastEmailOTPSentAtWithoutRows 是一条**回归用例**（真踩过的 500）。
//
// 症状：`/api/auth/password/otp` 首次调用返回 500，日志是
// `sql: Scan error ... unsupported Scan, storing driver.Value type <nil> into type *time.Time`。
//
// 根因：实现写成 `Select("max(created_at)").Scan(&last)`，而"这个账号一条码都没发过"
// 时 `max()` 返回 SQL NULL —— 那是最常见的**首次**状态，不是异常。
// 修法是用 `sql.NullTime` 明确表达"可能没有行/可能是 NULL"。
//
// 这条用例的价值在于：它断言的是"空表时返回零值且不报错"，
// 而任何把 NULL 扫进 *time.Time 的写法都会在这里立刻炸。
func TestLastEmailOTPSentAtWithoutRows(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	u := seedUser(t, s, "otp_no_rows")

	got, err := s.LastEmailOTPSentAt(ctx, u.ID)
	if err != nil {
		t.Fatalf("没有验证码行时不该报错（首次发码必经此状态）：%v", err)
	}
	if !got.IsZero() {
		t.Fatalf("没有验证码行时应返回零值，实际 %v", got)
	}
}

// TestLastEmailOTPSentAtReturnsLatest 有行时返回最近一条的时刻。
func TestLastEmailOTPSentAtReturnsLatest(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	u := seedUser(t, s, "otp_has_rows")

	older := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	newer := time.Now().Add(-time.Hour).Truncate(time.Second)
	for i, at := range []time.Time{older, newer} {
		row := &PasswordReset{
			UserID:    u.ID,
			Purpose:   PurposeEmailOTP,
			TokenHash: hexHash("otp-latest-" + string(rune('a'+i))),
			Email:     strPtr("otp@example.com"),
			CreatedAt: at,
			ExpiresAt: at.Add(5 * time.Minute),
		}
		if err := s.InsertPasswordReset(ctx, row); err != nil {
			t.Fatalf("写入验证码行失败：%v", err)
		}
	}

	got, err := s.LastEmailOTPSentAt(ctx, u.ID)
	if err != nil {
		t.Fatalf("查询最近验证码时间失败：%v", err)
	}
	if got.UTC().Truncate(time.Second) != newer.UTC().Truncate(time.Second) {
		t.Fatalf("应当返回最近一条的时刻 %v，实际 %v", newer, got)
	}
}

// TestConsumeEmailOTPWrongCodeIncrementsAttempts 错误次数必须**落库**。
//
// 为什么这条必须在真库上验：它的全部意义是"重启进程不会清零计数"。
// 内存版计数器能过所有的逻辑测试，却挡不住"重启再来 5 次"这个攻击路径。
func TestConsumeEmailOTPWrongCodeIncrementsAttempts(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	u := seedUser(t, s, "otp_attempts")

	correct := hexHash("otp-correct")
	if err := s.InsertEmailOTP(ctx, &PasswordReset{
		UserID:    u.ID,
		TokenHash: correct,
		Email:     strPtr("attempts@example.com"),
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}); err != nil {
		t.Fatalf("写入验证码失败：%v", err)
	}

	// 连错两次：ok 都是 false，且都**不是**"次数用尽"。
	for i := 0; i < 2; i++ {
		ok, err := s.ConsumeEmailOTP(ctx, u.ID, hexHash("otp-wrong"), 5)
		if err != nil {
			t.Fatalf("第 %d 次错误校验不该报错：%v", i+1, err)
		}
		if ok {
			t.Fatalf("第 %d 次错误校验不该通过", i+1)
		}
	}

	// 计数必须真的落库（新连接/新进程读到的也是 2）。
	var attempts int
	if err := s.db.Raw(
		`SELECT attempts FROM password_resets WHERE purpose = ? AND user_id = ?`,
		PurposeEmailOTP, u.ID).Scan(&attempts).Error; err != nil {
		t.Fatalf("读错误计数失败：%v", err)
	}
	if attempts != 2 {
		t.Fatalf("错误两次后 attempts 应为 2，实际 %d", attempts)
	}

	// 正确码仍然能用（还没到上限）。
	ok, err := s.ConsumeEmailOTP(ctx, u.ID, correct, 5)
	if err != nil || !ok {
		t.Fatalf("错误两次后正确码仍应可用，ok=%v err=%v", ok, err)
	}

	// 单次消费：再试一次必然失败。
	ok, err = s.ConsumeEmailOTP(ctx, u.ID, correct, 5)
	if err != nil {
		// 已被烧掉时可能返回 ErrOTPNotFound，那也是"不可用"。
		if !isOTPUnavailable(err) {
			t.Fatalf("单次消费第二次不该报意外错误：%v", err)
		}
		ok = false
	}
	if ok {
		t.Fatal("同一条验证码不该被消费两次")
	}
}

// TestConsumeEmailOTPExhaustsAtLimit 达到上限即作废（"用尽"必须是终态）。
func TestConsumeEmailOTPExhaustsAtLimit(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	u := seedUser(t, s, "otp_exhaust")

	correct := hexHash("otp-exhaust-correct")
	if err := s.InsertEmailOTP(ctx, &PasswordReset{
		UserID:    u.ID,
		TokenHash: correct,
		Email:     strPtr("exhaust@example.com"),
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}); err != nil {
		t.Fatalf("写入验证码失败：%v", err)
	}

	const maxAttempts = 3
	for i := 0; i < maxAttempts-1; i++ {
		ok, err := s.ConsumeEmailOTP(ctx, u.ID, hexHash("otp-wrong"), maxAttempts)
		if err != nil || ok {
			t.Fatalf("第 %d 次错误应当是「不可用但不报错」，ok=%v err=%v", i+1, ok, err)
		}
	}
	// 达到上限的那一次返回 ErrOTPAttemptsExceeded（调用方要据此说"这条码已废"）。
	if _, err := s.ConsumeEmailOTP(ctx, u.ID, hexHash("otp-wrong"), maxAttempts); err == nil {
		t.Fatal("达到上限时应当返回 ErrOTPAttemptsExceeded")
	}

	// 用尽之后连**正确码**也不能过（终态）。
	ok, err := s.ConsumeEmailOTP(ctx, u.ID, correct, maxAttempts)
	if ok {
		t.Fatal("用尽之后正确码也不该通过")
	}
	if err != nil && !isOTPUnavailable(err) {
		t.Fatalf("用尽之后不该报意外错误：%v", err)
	}
}

// TestInsertEmailOTPInvalidatesPrevious 发新码必须作废旧码。
//
// 否则用户点两次"发送"，两条码都活着，"我输的是哪一条"就变成猜谜；
// 更糟的是攻击者可以攒很多条码一起试，把 5 次的预算乘以条数。
func TestInsertEmailOTPInvalidatesPrevious(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	u := seedUser(t, s, "otp_rotate")

	first := hexHash("otp-first")
	if err := s.InsertEmailOTP(ctx, &PasswordReset{
		UserID:    u.ID,
		TokenHash: first,
		Email:     strPtr("rotate@example.com"),
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}); err != nil {
		t.Fatalf("写入第一条失败：%v", err)
	}

	second := hexHash("otp-second")
	if err := s.InsertEmailOTP(ctx, &PasswordReset{
		UserID:    u.ID,
		TokenHash: second,
		Email:     strPtr("rotate@example.com"),
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}); err != nil {
		t.Fatalf("写入第二条失败：%v", err)
	}

	// 旧码必须已经作废。
	ok, err := s.ConsumeEmailOTP(ctx, u.ID, first, 5)
	if ok {
		t.Fatal("发新码之后旧码不该还能用")
	}
	if err != nil && !isOTPUnavailable(err) {
		t.Fatalf("旧码不该报意外错误：%v", err)
	}

	// 新码可用。
	ok, err = s.ConsumeEmailOTP(ctx, u.ID, second, 5)
	if err != nil || !ok {
		t.Fatalf("新码应当可用，ok=%v err=%v", ok, err)
	}
}
