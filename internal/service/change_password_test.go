package service

import (
	"context"
	"crypto/md5" //nolint:gosec // 测试里要造旧格式哈希，与 account_test.go 同一理由
	"encoding/hex"
	"errors"
	"testing"

	"ProjectionRoom/internal/service/auth"
	"ProjectionRoom/internal/store"
)

// —— 凭当前密码改密（票 #71 / 决议 #51）——
//
// 本组用例钉的是四条判据：
//  1. 当前密码不对 → ErrPasswordMismatch，**口令与会话都不动**；
//  2. 新口令不合策略 → 策略错误，口令不动（且不该烧一次 bcrypt）；
//  3. 成功 → 旧 access token 立刻失效、其余会话全被撤销、**当前设备拿到可用新会话**；
//  4. 封禁账号 → ErrUserForbidden（不是"密码错"，本人应当知道自己被封了）。

// newPasswordFixture 造一个登录态：账号 + 两枚会话（模拟"当前设备"与"另一台设备"）。
func newPasswordFixture(t *testing.T, password string) (*fakeStore, *AccountService, *Session, *Session) {
	t.Helper()
	f := newFakeStore()
	svc := newTestService(t, f, AccountOptions{})
	ctx := context.Background()

	current, err := svc.Register(ctx, "alice", "爱丽丝", password, "alice@example.com", "1.1.1.1", "ua-current")
	if err != nil {
		t.Fatalf("注册失败：%v", err)
	}
	// 第二台设备：同一账号再登录一次。
	other, err := svc.Login(ctx, "alice", password, "2.2.2.2", "ua-other")
	if err != nil {
		t.Fatalf("第二台设备登录失败：%v", err)
	}
	if f.activeSessionCount(current.User.ID) != 2 {
		t.Fatalf("夹具应当有两枚活跃会话，实际 %d", f.activeSessionCount(current.User.ID))
	}
	return f, svc, current, other
}

func TestChangeMyPasswordRejectsWrongCurrent(t *testing.T) {
	f, svc, current, _ := newPasswordFixture(t, "abcd1234")
	ctx := context.Background()

	_, err := svc.ChangeMyPassword(ctx, current.User.ID, "wrongpass1", "newpass123", "1.1.1.1", "ua-current")
	if !errors.Is(err, ErrPasswordMismatch) {
		t.Fatalf("当前密码不对应当返回 ErrPasswordMismatch，实际：%v", err)
	}

	// 会话没被撤销：一次"填错当前密码"不该把用户从所有设备上踢下来。
	// ⚠ 这一条必须在任何 Login 之前断言 —— Login 自己也会建会话。
	if got := f.activeSessionCount(current.User.ID); got != 2 {
		t.Fatalf("失败的改密不该撤销会话，活跃会话应为 2，实际 %d", got)
	}
	// 口令没变：旧口令仍能登录。
	if _, err := svc.Login(ctx, "alice", "abcd1234", "1.1.1.1", "ua"); err != nil {
		t.Fatalf("失败的改密不该动口令，旧口令应当仍能登录：%v", err)
	}
}

func TestChangeMyPasswordRejectsWeakNewPassword(t *testing.T) {
	f, svc, current, _ := newPasswordFixture(t, "abcd1234")
	ctx := context.Background()

	cases := []struct {
		name string
		pass string
		want error
	}{
		{"太短", "ab1", auth.ErrPasswordTooShort},
		{"无数字", "abcdefgh", auth.ErrPasswordNoDigit},
		{"无字母", "12345678", auth.ErrPasswordNoLetter},
		{"空", "", auth.ErrPasswordEmpty},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.ChangeMyPassword(ctx, current.User.ID, "abcd1234", tc.pass, "1.1.1.1", "ua")
			if !errors.Is(err, tc.want) {
				t.Fatalf("弱口令 %q 应当返回 %v，实际：%v", tc.pass, tc.want, err)
			}
		})
	}

	// 五次失败之后口令与会话都该原封不动（会话数要在 Login 之前看）。
	if got := f.activeSessionCount(current.User.ID); got != 2 {
		t.Fatalf("弱口令被拒不该撤销会话，活跃会话应为 2，实际 %d", got)
	}
	if _, err := svc.Login(ctx, "alice", "abcd1234", "1.1.1.1", "ua"); err != nil {
		t.Fatalf("弱口令被拒后旧口令应当仍能登录：%v", err)
	}
}

func TestChangeMyPasswordKeepsCurrentDeviceAndKicksOthers(t *testing.T) {
	f, svc, current, other := newPasswordFixture(t, "abcd1234")
	ctx := context.Background()

	sess, err := svc.ChangeMyPassword(ctx, current.User.ID, "abcd1234", "newpass123", "1.1.1.1", "ua-current")
	if err != nil {
		t.Fatalf("改密应当成功：%v", err)
	}
	if sess == nil || sess.AccessToken == "" {
		t.Fatal("改密成功必须给当前设备补发一枚会话（否则用户被自己蹬下线）")
	}

	// ① 新会话可用：access token 能通过校验，refresh 也能换到下一枚。
	if _, err := svc.VerifyAccessToken(ctx, sess.AccessToken); err != nil {
		t.Fatalf("改密后补发的新 access token 必须立刻可用：%v", err)
	}
	if _, err := svc.Refresh(ctx, sess.RefreshToken, "1.1.1.1", "ua-current"); err != nil {
		t.Fatalf("改密后补发的新 refresh token 必须立刻可用：%v", err)
	}

	// ② 旧凭据全失效：两枚旧 access token 都不认。
	for name, tok := range map[string]string{"当前设备旧 token": current.AccessToken, "另一台设备旧 token": other.AccessToken} {
		if _, err := svc.VerifyAccessToken(ctx, tok); !errors.Is(err, ErrSessionInvalid) {
			t.Fatalf("改密后 %s 必须失效（ErrSessionInvalid），实际：%v", name, err)
		}
	}

	// ③ 只有一枚活跃会话（补发的这枚）。注意上面 Refresh 又轮换了一次，仍是 1。
	//
	// ⚠ 这条必须在"拿另一台设备的旧 refresh 去刷"之前断言：那个动作会命中重放检测，
	// 处置是**撤销该用户全部会话**（见 handleReplay）—— 于是活跃会话会变成 0。
	// 那是正确的安全行为，但它会把这条判据掩盖掉。
	if got := f.activeSessionCount(current.User.ID); got != 1 {
		t.Fatalf("改密后应当只剩当前设备一枚活跃会话，实际 %d", got)
	}

	// ④ 另一台设备的旧 refresh 不能用。两种失败形态都算通过：
	// 该行已被撤销 → 重放检测（并连带撤销全部会话）；或行已被清理 → 无效。
	if _, err := svc.Refresh(ctx, other.RefreshToken, "2.2.2.2", "ua-other"); err == nil {
		t.Fatal("改密后另一台设备的 refresh token 必须失效")
	}

	// ⑤ 口令真的换了。
	if _, err := svc.Login(ctx, "alice", "abcd1234", "1.1.1.1", "ua"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("旧口令应当失效，实际：%v", err)
	}
	if _, err := svc.Login(ctx, "alice", "newpass123", "1.1.1.1", "ua"); err != nil {
		t.Fatalf("新口令应当能登录：%v", err)
	}
}

func TestChangeMyPasswordRejectsBannedAccount(t *testing.T) {
	f, svc, current, _ := newPasswordFixture(t, "abcd1234")
	ctx := context.Background()

	// 直接把库里的状态改成封禁（BanUser 会写审计，与本用例无关）。
	f.mu.Lock()
	f.users[current.User.ID].Status = store.StatusBanned
	f.mu.Unlock()

	if _, err := svc.ChangeMyPassword(ctx, current.User.ID, "abcd1234", "newpass123", "1.1.1.1", "ua"); !errors.Is(err, ErrUserForbidden) {
		t.Fatalf("封禁账号改密应当返回 ErrUserForbidden，实际：%v", err)
	}
	// 口令不该被改动。
	if _, err := svc.Login(ctx, "alice", "abcd1234", "1.1.1.1", "ua"); !errors.Is(err, ErrUserBanned) {
		t.Fatalf("封禁账号登录应当报 ErrUserBanned（说明口令没被改），实际：%v", err)
	}
}

func TestChangeMyPasswordRejectsUnknownSession(t *testing.T) {
	f := newFakeStore()
	svc := newTestService(t, f, AccountOptions{})

	for _, id := range []int64{0, -1, 999999} {
		if _, err := svc.ChangeMyPassword(context.Background(), id, "abcd1234", "newpass123", "", ""); !errors.Is(err, ErrSessionInvalid) {
			t.Fatalf("userID=%d 应当返回 ErrSessionInvalid，实际：%v", id, err)
		}
	}
}

// TestChangeMyPasswordAcceptsLegacyMD5Current 钉住旧项目账号也能改密。
//
// 为什么重要：那些账号的库里存的是 MD5（Login 有兼容分支），若改密这条只认 bcrypt，
// 症状是"能登录、却永远提示当前密码不对"—— 而这条路径只有旧账号会踩到。
func TestChangeMyPasswordAcceptsLegacyMD5Current(t *testing.T) {
	f := newFakeStore()
	svc := newTestService(t, f, AccountOptions{})
	ctx := context.Background()

	const legacyPlain = "legacy123"
	id := seedAccount(t, f, "legacyuser", "placeholder1")
	// 把口令换成旧格式的 MD5（Login 的兼容分支认它）。
	sum := md5.Sum([]byte(legacyPlain)) //nolint:gosec // 兼容旧哈希，与 account_test.go 一致
	f.mu.Lock()
	f.users[id].PasswordHash = hex.EncodeToString(sum[:])
	f.mu.Unlock()

	sess, err := svc.ChangeMyPassword(ctx, id, legacyPlain, "newpass123", "1.1.1.1", "ua")
	if err != nil {
		t.Fatalf("旧 MD5 账号凭当前密码改密应当成功：%v", err)
	}
	if sess == nil || sess.AccessToken == "" {
		t.Fatal("旧 MD5 账号改密后也必须拿到可用新会话")
	}
	// 新口令是 bcrypt 的，旧 MD5 已被替换。
	after, err := f.UserByID(ctx, id)
	if err != nil {
		t.Fatalf("读回账号失败：%v", err)
	}
	if auth.IsLegacyMD5Hash(after.PasswordHash) {
		t.Fatal("改密后库里不该还是旧 MD5 哈希")
	}
}
