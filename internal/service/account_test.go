package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ProjectionRoom/internal/service/auth"
	"ProjectionRoom/internal/store"
)

// —— 假 store：内存实现（AccountStore 的全部方法）。
//
// 为什么用内存实现而不是真库：本文件要覆盖的是**分支**（重名、重放、版本号不匹配、
// 闸门饱和），而不是 SQL。真库只能验后者，且没有 PG 时就只能 skip ——
// 那等于把最关键的几条判据长期搁置在"没跑过"的状态。

type fakeStore struct {
	mu       sync.Mutex
	users    map[int64]*store.User
	byName   map[string]int64
	sessions map[int64]*store.Session
	audits   []store.AdminAudit

	nextUserID    int64
	nextSessionID int64

	// createUserErr / createSessionErr 是注入的失败（便于构造唯一约束冲突）。
	createUserErr    error
	createSessionErr error
	createUserCalls  int
	createSessionCnt int

	// revokeAllCalls 用于断言"重放触发全会话撤销"。
	revokeAllCalls int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		users:    map[int64]*store.User{},
		byName:   map[string]int64{},
		sessions: map[int64]*store.Session{},
	}
}

// EventWriter 返回 nil：假实现不挂写入器，此时 AccountService.submit 走
// "后台协程直写"分支 —— 审计与 last_seen 仍然会到达，只是不经过有界队列。
func (f *fakeStore) EventWriter() *store.Writer { return nil }

func (f *fakeStore) CreateUser(_ context.Context, u *store.User) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createUserCalls++
	if f.createUserErr != nil {
		return f.createUserErr
	}
	name := strings.ToLower(strings.TrimSpace(u.Username))
	if _, ok := f.byName[name]; ok {
		return store.ErrUsernameTaken
	}
	f.nextUserID++
	u.ID = f.nextUserID
	if u.Role == "" {
		u.Role = store.RoleUser
	}
	if u.Status == "" {
		u.Status = store.StatusActive
	}
	if u.TokenVersion == 0 {
		u.TokenVersion = 1
	}
	u.CreatedAt = time.Now()
	cp := *u
	f.users[u.ID] = &cp
	f.byName[name] = u.ID
	return nil
}

func (f *fakeStore) UserByUsername(_ context.Context, username string) (*store.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.byName[strings.ToLower(strings.TrimSpace(username))]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *f.users[id]
	return &cp, nil
}

func (f *fakeStore) UserByID(_ context.Context, id int64) (*store.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *u
	return &cp, nil
}

func (f *fakeStore) SetUserStatus(_ context.Context, id int64, status, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[id]
	if !ok {
		return store.ErrNotFound
	}
	u.Status = status
	if status == store.StatusBanned {
		r := reason
		u.BannedReason = &r
	} else {
		u.BannedReason = nil
	}
	return nil
}

func (f *fakeStore) SetUserRole(_ context.Context, id int64, role string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[id]
	if !ok {
		return store.ErrNotFound
	}
	u.Role = role
	return nil
}

func (f *fakeStore) BumpTokenVersion(_ context.Context, id int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[id]
	if !ok {
		return 0, store.ErrNotFound
	}
	u.TokenVersion++
	return u.TokenVersion, nil
}

func (f *fakeStore) TouchLastSeen(_ context.Context, id int64, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[id]
	if !ok {
		return store.ErrNotFound
	}
	u.LastSeenAt = at
	return nil
}

func (f *fakeStore) CreateSession(_ context.Context, sess *store.Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createSessionCnt++
	if f.createSessionErr != nil {
		return f.createSessionErr
	}
	for _, s := range f.sessions {
		if s.TokenHash == sess.TokenHash {
			// 与真实 store 的措辞一致（isReplay 按它判断并发双花）。
			return store.ErrTokenHashTaken
		}
	}
	f.nextSessionID++
	sess.ID = f.nextSessionID
	cp := *sess
	f.sessions[sess.ID] = &cp
	return nil
}

func (f *fakeStore) SessionByTokenHash(_ context.Context, hash string) (*store.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.sessions {
		if s.TokenHash == hash {
			cp := *s
			return &cp, nil
		}
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) RevokeSession(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[id]
	if !ok {
		return store.ErrNotFound
	}
	if s.RevokedAt == nil {
		now := time.Now()
		s.RevokedAt = &now
	}
	return nil
}

func (f *fakeStore) RevokeAllSessions(_ context.Context, userID int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revokeAllCalls++
	var n int64
	for _, s := range f.sessions {
		if s.UserID == userID && s.RevokedAt == nil {
			now := time.Now()
			s.RevokedAt = &now
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) InsertAudit(_ context.Context, a *store.AdminAudit) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audits = append(f.audits, *a)
	return nil
}

// —— 计数/读取辅助（都有锁）。

func (f *fakeStore) sessionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sessions)
}

func (f *fakeStore) activeSessionCount(userID int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.sessions {
		if s.UserID == userID && s.RevokedAt == nil {
			n++
		}
	}
	return n
}

func (f *fakeStore) revokeCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.revokeAllCalls
}

func (f *fakeStore) auditCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.audits)
}

func (f *fakeStore) auditActions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.audits))
	for _, a := range f.audits {
		out = append(out, a.Action)
	}
	return out
}

// waitUntil 轮询等待条件成立（异步写得在后台协程里落地，不能直接断言）。
//
// 名字与 host_grace_test.go 的 waitFor 区分开：那个是带 timeout 参数的版本，
// 两者签名不同，重名会让本包编译不过。
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", what)
}

// —— 组装。

const accountTestJWTSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// newTestService 构造一个用假 store 的账号服务。
//
// bcrypt cost 用 auth.MinBcryptCost(10)：本文件要跑十几次哈希，
// 用 12 会让整个包的单测多花好几秒。代价参数不影响任何被测逻辑
// （生产值由配置传入，见 AccountOptions.BcryptCost）。
func newTestService(t *testing.T, f *fakeStore, opts AccountOptions) *AccountService {
	t.Helper()
	if opts.BcryptCost == 0 {
		opts.BcryptCost = auth.MinBcryptCost
	}
	if opts.RefreshTTL == 0 {
		opts.RefreshTTL = 30 * 24 * time.Hour
	}
	signer, err := auth.NewSigner(accountTestJWTSecret, 15*time.Minute)
	if err != nil {
		t.Fatalf("构造 Signer 失败：%v", err)
	}
	svc, err := NewAccountService(f, signer, auth.NewTicketStore(30*time.Second), opts)
	if err != nil {
		t.Fatalf("构造 AccountService 失败：%v", err)
	}
	return svc
}

// seedAccount 直接在假库里放一个账号，返回其 id（跳过注册流程）。
func seedAccount(t *testing.T, f *fakeStore, username, password string) int64 {
	t.Helper()
	hash, err := auth.HashPassword(password, auth.MinBcryptCost)
	if err != nil {
		t.Fatalf("哈希失败：%v", err)
	}
	u := &store.User{Username: username, DisplayName: username, PasswordHash: hash}
	if err := f.CreateUser(context.Background(), u); err != nil {
		t.Fatalf("写入账号失败：%v", err)
	}
	return u.ID
}

// —— 注册 ——

func TestRegisterDuplicateUsernameReturnsTaken(t *testing.T) {
	f := newFakeStore()
	svc := newTestService(t, f, AccountOptions{})

	if _, err := svc.Register(context.Background(), "Alice", "爱丽丝", "abcd1234", "1.1.1.1", "ua"); err != nil {
		t.Fatalf("首次注册应当成功：%v", err)
	}
	// 大小写不同、语义同一个用户名：store 侧归一化成小写，因此必须命中唯一冲突。
	_, err := svc.Register(context.Background(), "ALICE", "另一个", "abcd1234", "1.1.1.1", "ua")
	if !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("重名注册应当返回 ErrUsernameTaken，实际：%v", err)
	}
	if f.sessionCount() != 1 {
		t.Fatalf("失败的注册不该留下会话，实际 %d 条", f.sessionCount())
	}
}

func TestRegisterRejectsBadUsernameWithoutHashing(t *testing.T) {
	f := newFakeStore()
	svc := newTestService(t, f, AccountOptions{})

	for _, name := range []string{"ab", "中文名", "with space", strings.Repeat("x", 21), "a-b"} {
		if _, err := svc.Register(context.Background(), name, "昵称", "abcd1234", "", ""); !errors.Is(err, ErrBadUsername) {
			t.Fatalf("用户名 %q 应当被拒（ErrBadUsername），实际：%v", name, err)
		}
	}
	if f.createUserCalls != 0 {
		t.Fatalf("非法用户名不该走到入库，实际 CreateUser 被调用 %d 次", f.createUserCalls)
	}
}

func TestRegisterRejectsWeakPassword(t *testing.T) {
	f := newFakeStore()
	svc := newTestService(t, f, AccountOptions{})

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
			_, err := svc.Register(context.Background(), "alice", "昵称", tc.pass, "", "")
			if !errors.Is(err, tc.want) {
				t.Fatalf("期望 %v，实际 %v", tc.want, err)
			}
		})
	}
	if f.createUserCalls != 0 {
		t.Fatalf("弱口令不该走到入库，实际 CreateUser 被调用 %d 次", f.createUserCalls)
	}
}

func TestRegisterSanitizesDisplayName(t *testing.T) {
	f := newFakeStore()
	svc := newTestService(t, f, AccountOptions{})

	// 含零宽空格与 Bidi 覆盖符的昵称：清洗后应当只剩可见字符。
	dirty := "  a\u200bb\u202ec  "
	sess, err := svc.Register(context.Background(), "alice", dirty, "abcd1234", "", "")
	if err != nil {
		t.Fatalf("注册失败：%v", err)
	}
	if sess.User.DisplayName != "abc" {
		t.Fatalf("昵称清洗错误：want=%q got=%q", "abc", sess.User.DisplayName)
	}

	// 空昵称（清洗后为空）回落到用户名。
	sess2, err := svc.Register(context.Background(), "bob", "\u200b\u200c", "abcd1234", "", "")
	if err != nil {
		t.Fatalf("注册失败：%v", err)
	}
	if sess2.User.DisplayName != "bob" {
		t.Fatalf("空昵称应回落到用户名，实际 %q", sess2.User.DisplayName)
	}
}

// —— 登录 ——

func TestLoginDoesNotLeakAccountExistence(t *testing.T) {
	f := newFakeStore()
	svc := newTestService(t, f, AccountOptions{})
	seedAccount(t, f, "alice", "abcd1234")

	_, errMissing := svc.Login(context.Background(), "nobody", "abcd1234", "", "")
	_, errWrong := svc.Login(context.Background(), "alice", "wrongpass1", "", "")

	if !errors.Is(errMissing, ErrInvalidCredentials) {
		t.Fatalf("账号不存在应当返回 ErrInvalidCredentials，实际：%v", errMissing)
	}
	if !errors.Is(errWrong, ErrInvalidCredentials) {
		t.Fatalf("口令错误应当返回 ErrInvalidCredentials，实际：%v", errWrong)
	}
	// 同一个错误的**同一个值**：调用方无法从错误对象上区分这两条路径。
	if errMissing != errWrong {
		t.Fatalf("两条路径必须返回同一个错误值：missing=%v wrong=%v", errMissing, errWrong)
	}
}

func TestLoginBannedUserRejected(t *testing.T) {
	f := newFakeStore()
	svc := newTestService(t, f, AccountOptions{})
	id := seedAccount(t, f, "alice", "abcd1234")
	if err := f.SetUserStatus(context.Background(), id, store.StatusBanned, "刷屏"); err != nil {
		t.Fatalf("封禁失败：%v", err)
	}

	_, err := svc.Login(context.Background(), "alice", "abcd1234", "", "")
	if !errors.Is(err, ErrUserBanned) {
		t.Fatalf("封禁账号登录应当返回 ErrUserBanned，实际：%v", err)
	}

	// 口令错误时仍然只报凭据错误（封禁不该变成一条不需要口令的枚举手段）。
	if _, err := svc.Login(context.Background(), "alice", "wrongpass1", "", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("封禁账号 + 口令错误应当仍报凭据错误，实际：%v", err)
	}
}

func TestLoginUpdatesLastSeenAsynchronously(t *testing.T) {
	f := newFakeStore()
	svc := newTestService(t, f, AccountOptions{})
	id := seedAccount(t, f, "alice", "abcd1234")

	if _, err := svc.Login(context.Background(), "alice", "abcd1234", "9.9.9.9", "ua"); err != nil {
		t.Fatalf("登录失败：%v", err)
	}
	waitUntil(t, "last_seen 被异步更新", func() bool {
		u, err := f.UserByID(context.Background(), id)
		return err == nil && !u.LastSeenAt.IsZero()
	})
}

// —— 刷新与重放检测 ——

func TestRefreshRotatesAndDetectsReplay(t *testing.T) {
	f := newFakeStore()
	svc := newTestService(t, f, AccountOptions{})
	ctx := context.Background()

	login, err := svc.Register(ctx, "alice", "爱丽丝", "abcd1234", "", "")
	if err != nil {
		t.Fatalf("注册失败：%v", err)
	}
	old := login.RefreshToken
	oldHash := HashRefreshToken(old)
	if oldHash == old {
		t.Fatal("库里必须存哈希，不能存原文")
	}
	if _, err := f.SessionByTokenHash(ctx, old); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("库里不得出现 refresh token 原文对应的行（只能存 sha256）")
	}

	// 第一次刷新：轮换成功。
	rotated, err := svc.Refresh(ctx, old, "", "")
	if err != nil {
		t.Fatalf("刷新失败：%v", err)
	}
	if rotated.RefreshToken == old {
		t.Fatal("刷新必须轮换 refresh token（新旧不得相同）")
	}
	if rotated.AccessToken == login.AccessToken {
		t.Fatal("刷新应当签发新的 access token")
	}

	// 旧行已撤销，新行可用。
	oldSess, err := f.SessionByTokenHash(ctx, oldHash)
	if err != nil {
		t.Fatalf("查旧会话失败：%v", err)
	}
	if oldSess.RevokedAt == nil {
		t.Fatal("轮换必须撤销旧行")
	}
	if f.activeSessionCount(login.User.ID) != 1 {
		t.Fatalf("轮换后应当恰好剩 1 条活跃会话，实际 %d", f.activeSessionCount(login.User.ID))
	}

	// 再用旧的 refresh：判定为重放。
	before := f.revokeCallCount()
	_, err = svc.Refresh(ctx, old, "5.5.5.5", "")
	if !errors.Is(err, ErrRefreshReplay) {
		t.Fatalf("旧 refresh 复用应当返回 ErrRefreshReplay，实际：%v", err)
	}
	if f.revokeCallCount() != before+1 {
		t.Fatalf("重放必须触发一次全量撤销（RevokeAllSessions），实际调用次数 %d → %d",
			before, f.revokeCallCount())
	}
	if f.activeSessionCount(login.User.ID) != 0 {
		t.Fatalf("重放后该用户的活跃会话必须为 0，实际 %d", f.activeSessionCount(login.User.ID))
	}

	// 连新签发的 refresh 也被一起撤销（同一账号全端下线）。
	if _, err := svc.Refresh(ctx, rotated.RefreshToken, "", ""); !errors.Is(err, ErrRefreshReplay) {
		t.Fatalf("重放处置必须撤销该账号全部会话（含新签发的那条），实际：%v", err)
	}

	// 重放处置必须留审计。
	waitUntil(t, "重放审计落库", func() bool {
		for _, a := range f.auditActions() {
			if a == "session.replay" {
				return true
			}
		}
		return false
	})
}

func TestRefreshRejectsUnknownToken(t *testing.T) {
	f := newFakeStore()
	svc := newTestService(t, f, AccountOptions{})

	if _, err := svc.Refresh(context.Background(), "not-a-real-token", "", ""); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("未知 refresh 应当返回 ErrSessionInvalid，实际：%v", err)
	}
	if f.revokeCallCount() != 0 {
		t.Fatal("未知 token（没有行、也没有被撤销的证据）不该触发全量撤销")
	}
}

func TestRefreshRejectsExpiredSession(t *testing.T) {
	f := newFakeStore()
	svc := newTestService(t, f, AccountOptions{RefreshTTL: time.Hour})
	ctx := context.Background()

	login, err := svc.Register(ctx, "alice", "爱丽丝", "abcd1234", "", "")
	if err != nil {
		t.Fatalf("注册失败：%v", err)
	}
	// 把这一行改成"已过期但未撤销"（轮换前的正常过期形态）。
	f.mu.Lock()
	for _, s := range f.sessions {
		s.ExpiresAt = time.Now().Add(-time.Minute)
	}
	f.mu.Unlock()

	if _, err := svc.Refresh(ctx, login.RefreshToken, "", ""); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("过期会话应当返回 ErrSessionInvalid，实际：%v", err)
	}
	if f.revokeCallCount() != 0 {
		t.Fatal("单纯过期不是重放，不该撤销全部会话")
	}
}

// —— 登出 ——

func TestLogoutIsIdempotent(t *testing.T) {
	f := newFakeStore()
	svc := newTestService(t, f, AccountOptions{})
	ctx := context.Background()

	login, err := svc.Register(ctx, "alice", "爱丽丝", "abcd1234", "", "")
	if err != nil {
		t.Fatalf("注册失败：%v", err)
	}
	if err := svc.Logout(ctx, login.RefreshToken); err != nil {
		t.Fatalf("登出失败：%v", err)
	}
	if f.activeSessionCount(login.User.ID) != 0 {
		t.Fatal("登出后不该有活跃会话")
	}
	// 第二次登出、以及空 token：都必须静默成功（幂等）。
	if err := svc.Logout(ctx, login.RefreshToken); err != nil {
		t.Fatalf("重复登出必须幂等成功，实际：%v", err)
	}
	if err := svc.Logout(ctx, ""); err != nil {
		t.Fatalf("空 token 登出必须幂等成功，实际：%v", err)
	}
}

func TestLogoutAllBumpsTokenVersion(t *testing.T) {
	f := newFakeStore()
	svc := newTestService(t, f, AccountOptions{})
	ctx := context.Background()

	login, err := svc.Register(ctx, "alice", "爱丽丝", "abcd1234", "", "")
	if err != nil {
		t.Fatalf("注册失败：%v", err)
	}
	before, _ := f.UserByID(ctx, login.User.ID)

	if err := svc.LogoutAll(ctx, login.User.ID); err != nil {
		t.Fatalf("登出全部设备失败：%v", err)
	}
	after, _ := f.UserByID(ctx, login.User.ID)
	if after.TokenVersion != before.TokenVersion+1 {
		t.Fatalf("LogoutAll 必须 token_version+1（旧 access token 立即失效），%d → %d",
			before.TokenVersion, after.TokenVersion)
	}
	if f.activeSessionCount(login.User.ID) != 0 {
		t.Fatal("LogoutAll 必须撤销全部会话")
	}
	// 旧 access token 立刻失效。
	if _, err := svc.VerifyAccessToken(ctx, login.AccessToken); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("LogoutAll 之后旧 access token 应当被拒，实际：%v", err)
	}
}

// —— 授权前置（token 版本比对） ——

func TestVerifyAccessTokenChecksTokenVersion(t *testing.T) {
	f := newFakeStore()
	svc := newTestService(t, f, AccountOptions{})
	ctx := context.Background()

	login, err := svc.Register(ctx, "alice", "爱丽丝", "abcd1234", "", "")
	if err != nil {
		t.Fatalf("注册失败：%v", err)
	}

	u, err := svc.VerifyAccessToken(ctx, login.AccessToken)
	if err != nil {
		t.Fatalf("刚签发的 token 必须可用：%v", err)
	}
	if u.ID != login.User.ID {
		t.Fatalf("解析出的用户不对：want=%d got=%d", login.User.ID, u.ID)
	}

	// token_version +1（封禁/改密/登出全部设备都走它）：旧 token 立刻失效。
	if _, err := f.BumpTokenVersion(ctx, u.ID); err != nil {
		t.Fatalf("自增版本号失败：%v", err)
	}
	if _, err := svc.VerifyAccessToken(ctx, login.AccessToken); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("tv 不匹配必须返回 ErrSessionInvalid，实际：%v", err)
	}
}

func TestVerifyAccessTokenRejectsBannedAndGarbage(t *testing.T) {
	f := newFakeStore()
	svc := newTestService(t, f, AccountOptions{})
	ctx := context.Background()

	login, err := svc.Register(ctx, "alice", "爱丽丝", "abcd1234", "", "")
	if err != nil {
		t.Fatalf("注册失败：%v", err)
	}

	if _, err := svc.VerifyAccessToken(ctx, "garbage"); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("伪造 token 应当返回 auth.ErrInvalidToken，实际：%v", err)
	}
	if _, err := svc.VerifyAccessToken(ctx, ""); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("空 token 应当返回 auth.ErrInvalidToken，实际：%v", err)
	}

	// 账号被封禁：token 本身仍然合法（签名/未过期/版本号一致），
	// 但状态检查必须拦住它，并且报"被封禁"而不是"会话无效"。
	if err := f.SetUserStatus(ctx, login.User.ID, store.StatusBanned, "刷屏"); err != nil {
		t.Fatalf("封禁失败：%v", err)
	}
	if _, err := svc.VerifyAccessToken(ctx, login.AccessToken); !errors.Is(err, ErrUserBanned) {
		t.Fatalf("封禁账号应当返回 ErrUserBanned，实际：%v", err)
	}

	// 解封后立即恢复（状态是判定的实时依据，不是签发时的快照）。
	if err := f.SetUserStatus(ctx, login.User.ID, store.StatusActive, ""); err != nil {
		t.Fatalf("解封失败：%v", err)
	}
	if _, err := svc.VerifyAccessToken(ctx, login.AccessToken); err != nil {
		t.Fatalf("解封后同一个 access token 应当立刻可用，实际：%v", err)
	}

	// 账号被删除（查不到）：一律当会话无效，不泄漏"这个 id 存在过"。
	f.mu.Lock()
	delete(f.users, login.User.ID)
	delete(f.byName, "alice")
	f.mu.Unlock()
	if _, err := svc.VerifyAccessToken(ctx, login.AccessToken); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("账号被删除后应当返回 ErrSessionInvalid，实际：%v", err)
	}
}

// —— 封禁 / 改角色 ——

func TestBanUserWritesStatusAndAudit(t *testing.T) {
	f := newFakeStore()
	svc := newTestService(t, f, AccountOptions{})
	ctx := context.Background()

	adminID := seedAccount(t, f, "admin", "abcd1234")
	targetID := seedAccount(t, f, "target", "abcd1234")
	before, _ := f.UserByID(ctx, targetID)

	if err := svc.BanUser(ctx, adminID, targetID, "刷屏", "10.0.0.1"); err != nil {
		t.Fatalf("封禁失败：%v", err)
	}
	after, _ := f.UserByID(ctx, targetID)
	if after.Status != store.StatusBanned {
		t.Fatalf("状态应为 banned，实际 %q", after.Status)
	}
	if after.BannedReason == nil || *after.BannedReason != "刷屏" {
		t.Fatalf("封禁理由未落库：%v", after.BannedReason)
	}
	if after.TokenVersion != before.TokenVersion+1 {
		t.Fatalf("封禁必须 token_version+1，%d → %d", before.TokenVersion, after.TokenVersion)
	}

	waitUntil(t, "封禁审计落库", func() bool { return f.auditCount() >= 1 })
	audits := f.audits
	audit := audits[0]
	if audit.ActorID != adminID || audit.Action != "user.ban" || audit.TargetID != strconv.FormatInt(targetID, 10) {
		t.Fatalf("审计内容不对：%+v", audit)
	}
	if audit.IP == nil || *audit.IP != "10.0.0.1" {
		t.Fatalf("审计应当记录来源 IP：%v", audit.IP)
	}
	if !strings.Contains(audit.Detail, "刷屏") {
		t.Fatalf("审计明细应含理由：%q", audit.Detail)
	}

	// 解封：清理由、再 +1。
	if err := svc.UnbanUser(ctx, adminID, targetID, ""); err != nil {
		t.Fatalf("解封失败：%v", err)
	}
	unbanned, _ := f.UserByID(ctx, targetID)
	if unbanned.Status != store.StatusActive || unbanned.BannedReason != nil {
		t.Fatalf("解封后状态/理由不对：status=%q reason=%v", unbanned.Status, unbanned.BannedReason)
	}
}

func TestBanUserRejectsSelfTarget(t *testing.T) {
	f := newFakeStore()
	svc := newTestService(t, f, AccountOptions{})
	adminID := seedAccount(t, f, "admin", "abcd1234")

	if err := svc.BanUser(context.Background(), adminID, adminID, "手滑", ""); !errors.Is(err, ErrSelfTarget) {
		t.Fatalf("自封禁应当被拒，实际：%v", err)
	}
}

func TestSetRoleBumpsVersionAndAudits(t *testing.T) {
	f := newFakeStore()
	svc := newTestService(t, f, AccountOptions{})
	ctx := context.Background()

	adminID := seedAccount(t, f, "admin", "abcd1234")
	targetID := seedAccount(t, f, "target", "abcd1234")
	before, _ := f.UserByID(ctx, targetID)

	if err := svc.SetRole(ctx, adminID, targetID, "admin", "10.0.0.1"); err != nil {
		t.Fatalf("改角色失败：%v", err)
	}
	after, _ := f.UserByID(ctx, targetID)
	if after.Role != store.RoleAdmin {
		t.Fatalf("角色应为 admin，实际 %q", after.Role)
	}
	if after.TokenVersion != before.TokenVersion+1 {
		t.Fatalf("改角色必须 token_version+1（否则旧 token 里的 role 快照还有效）")
	}
	waitUntil(t, "改角色审计落库", func() bool {
		for _, a := range f.auditActions() {
			if a == "user.role" {
				return true
			}
		}
		return false
	})

	if err := svc.SetRole(ctx, adminID, targetID, "root", ""); !errors.Is(err, ErrBadRole) {
		t.Fatalf("非法角色应当被拒，实际：%v", err)
	}
	if err := svc.SetRole(ctx, adminID, adminID, "user", ""); !errors.Is(err, ErrSelfTarget) {
		t.Fatalf("自我降级应当被拒，实际：%v", err)
	}
}

// —— Me / 票据 ——

func TestMeAndTicket(t *testing.T) {
	f := newFakeStore()
	svc := newTestService(t, f, AccountOptions{})
	ctx := context.Background()

	login, err := svc.Register(ctx, "alice", "爱丽丝", "abcd1234", "", "")
	if err != nil {
		t.Fatalf("注册失败：%v", err)
	}

	p, err := svc.Me(ctx, login.User.ID)
	if err != nil {
		t.Fatalf("Me 失败：%v", err)
	}
	if p.Username != "alice" || p.ID != login.User.ID {
		t.Fatalf("档案不对：%+v", p)
	}

	ticket, err := svc.IssueWSTicket(ctx, login.User.ID)
	if err != nil {
		t.Fatalf("签发票据失败：%v", err)
	}
	if ticket == "" {
		t.Fatal("票据不能为空")
	}

	// 封禁之后不得再签发新票据（否则被封账号能用一张旧票据继续建连）。
	if err := f.SetUserStatus(ctx, login.User.ID, store.StatusBanned, ""); err != nil {
		t.Fatalf("封禁失败：%v", err)
	}
	if _, err := svc.IssueWSTicket(ctx, login.User.ID); !errors.Is(err, ErrUserBanned) {
		t.Fatalf("封禁账号签发票据应当被拒，实际：%v", err)
	}
}

// —— 并发 bcrypt 闸门 ——

// TestHashingGateRejectsWhenSaturated 是本文件最关键的一条：
// 闸门满时**立刻**返回 ErrHashingBusy，绝不排队。
func TestHashingGateRejectsWhenSaturated(t *testing.T) {
	f := newFakeStore()
	svc := newTestService(t, f, AccountOptions{HashingConcurrency: 1})

	// 手工占满唯一的那个名额（等价于"已经有一个请求正在做 bcrypt"）。
	//
	// 用一个显式标志控制释放（而不是 defer）：释放动作必须发生在"验证过
	// 名额未释放时确实挡得住"之后，否则会与后面那个等待名额的请求抢同一个令牌，
	// 测试变成一条靠时序赛跑的用例。
	svc.hashing <- struct{}{}
	granted := true
	defer func() {
		if granted {
			<-svc.hashing
		}
	}()

	// 注册路径：闸门满 → 立刻 ErrHashingBusy，且**没有**走到入库。
	if _, err := svc.Register(context.Background(), "alice", "爱丽丝", "abcd1234", "", ""); !errors.Is(err, ErrHashingBusy) {
		t.Fatalf("闸门满时注册应当返回 ErrHashingBusy，实际：%v", err)
	}
	if f.createUserCalls != 0 {
		t.Fatal("闸门满时不该入库")
	}

	// 登录路径（账号存在 / 不存在两条分支）：同样必须立刻返回 ErrHashingBusy。
	seedAccount(t, f, "bob", "abcd1234")
	if _, err := svc.Login(context.Background(), "bob", "abcd1234", "", ""); !errors.Is(err, ErrHashingBusy) {
		t.Fatalf("闸门满时登录应当返回 ErrHashingBusy，实际：%v", err)
	}
	if _, err := svc.Login(context.Background(), "nobody", "abcd1234", "", ""); !errors.Is(err, ErrHashingBusy) {
		t.Fatalf("闸门满时（账号不存在路径）也应当返回 ErrHashingBusy，实际：%v", err)
	}

	// 并发调用必须全部被**立刻**拒绝：任何一个"排队等到了名额"都会让本断言超时。
	const n = 32
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = svc.Login(context.Background(), "bob", "abcd1234", "", "")
		}(i)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("闸门满时并发登录发生了排队等待（必须立刻返回，不能等名额）")
	}
	for i, err := range errs {
		if !errors.Is(err, ErrHashingBusy) {
			t.Fatalf("第 %d 个并发请求应当返回 ErrHashingBusy，实际：%v", i, err)
		}
	}

	// 释放名额后恢复正常：闸门是"限流"不是"熔断"。
	//
	// 释放必须晚于上面的断言：占位的那一个令牌一旦提前被收掉，deferred 的释放
	// 就会永远等不到令牌（测试进程直接挂死）。
	<-svc.hashing
	granted = false
	if _, err := svc.Login(context.Background(), "bob", "abcd1234", "", ""); err != nil {
		t.Fatalf("名额释放后登录应当成功，实际：%v", err)
	}
}

// TestHashingGateSerializesUnderLoad 断言闸门确实起到"限并发"的作用：
// 容量 1 时，两个并发登录**串行**完成，而不是并行。
//
// 这是"闸门真的接在 bcrypt 上"的端到端证据（前一个用例只验了入口行为：
// 满了会立刻拒；这个用例验出口行为：同一时刻最多一个 bcrypt 在跑）。
func TestHashingGateSerializesUnderLoad(t *testing.T) {
	f := newFakeStore()
	svc := newTestService(t, f, AccountOptions{HashingConcurrency: 1})
	seedAccount(t, f, "bob", "abcd1234")

	// 先测一次真实 bcrypt 的耗时（一次成功登录正好包含它）。
	base := time.Now()
	if _, err := svc.Login(context.Background(), "bob", "abcd1234", "", ""); err != nil {
		t.Fatalf("基准登录失败：%v", err)
	}
	oneBcrypt := time.Since(base)

	// 两个并发登录：容量 1 时它们必须串行，总墙钟时间不小于单次 bcrypt 的一半
	//（取一半是为了吸收调度抖动，同时仍能抓住"两个 bcrypt 真并行"的形态）。
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 成功或被闸门拒绝都算"没有并发跑 bcrypt"。
			_, _ = svc.Login(context.Background(), "bob", "abcd1234", "", "")
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	if elapsed < oneBcrypt/2 {
		t.Fatalf("两个并发登录在 %s 内完成，而单次 bcrypt 要约 %s：闸门没有串行化（可能没接在 bcrypt 上）",
			elapsed, oneBcrypt)
	}
}

// —— 构造校验 ——

func TestNewAccountServiceValidatesInputs(t *testing.T) {
	f := newFakeStore()
	signer, _ := auth.NewSigner(accountTestJWTSecret, time.Minute)
	tickets := auth.NewTicketStore(time.Second)

	if _, err := NewAccountService(nil, signer, tickets, AccountOptions{RefreshTTL: time.Hour}); err == nil {
		t.Fatal("缺少 store 应当报错")
	}
	if _, err := NewAccountService(f, nil, tickets, AccountOptions{RefreshTTL: time.Hour}); err == nil {
		t.Fatal("缺少 signer 应当报错")
	}
	if _, err := NewAccountService(f, signer, nil, AccountOptions{RefreshTTL: time.Hour}); err == nil {
		t.Fatal("缺少票据表应当报错")
	}
	if _, err := NewAccountService(f, signer, tickets, AccountOptions{}); err == nil {
		t.Fatal("refresh TTL 为 0 应当报错（没有默认值可兜底）")
	}
	if _, err := NewAccountService(f, signer, tickets, AccountOptions{RefreshTTL: time.Hour, BcryptCost: 4}); err == nil {
		t.Fatal("bcrypt cost 越界应当报错")
	}
}
