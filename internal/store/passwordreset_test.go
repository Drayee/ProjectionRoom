package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// —— 一次性邮件令牌（password_resets）的真库用例 ——
//
// 这些判据**只能在真库上验**，它们是 store 层存在的全部理由：
//
//   - "校验 + 标记已用"必须是**一条**带条件的 UPDATE ... RETURNING（并发下不能都成功）；
//   - 消费 + 改密 + 撤销全部会话 + 版本号自增必须是**一个事务**（少哪一件都会
//     留下一个可用的旧凭据，而用户看到的却是"改密成功"）；
//   - 邮箱的唯一性靠 users_email_lower_key 这个**部分唯一 + 函数索引**兜住；
//   - 过期判定用的是**数据库时钟**（expires_at > now()），不是应用时钟。

// seedResetRow 直接插一行令牌（绕过 service，便于精确控制用途/过期时间）。
func seedResetRow(t *testing.T, s *Store, userID int64, purpose, hash string, expiresAt time.Time) *PasswordReset {
	t.Helper()
	row := &PasswordReset{
		UserID:    userID,
		Purpose:   purpose,
		TokenHash: hash,
		ExpiresAt: expiresAt,
	}
	if err := s.InsertPasswordReset(context.Background(), row); err != nil {
		t.Fatalf("写入令牌失败：%v", err)
	}
	return row
}

// TestInsertPasswordResetValidatesShape 覆盖写入侧的守卫。
//
// 最关键的一条是**拒绝把原文当哈希存进来**：那等于在库里放一串可直接使用的凭据
// （一期 refresh token 的 canonicalTokenHash 是同一道防线）。
func TestInsertPasswordResetValidatesShape(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	u := seedUser(t, s, "alice")

	cases := []struct {
		name string
		row  *PasswordReset
	}{
		{"nil", nil},
		{"缺 user_id", &PasswordReset{Purpose: PurposeEmailVerify, TokenHash: hexHash("a"), ExpiresAt: time.Now().Add(time.Hour)}},
		{"purpose 非法", &PasswordReset{UserID: u.ID, Purpose: "whatever", TokenHash: hexHash("a"), ExpiresAt: time.Now().Add(time.Hour)}},
		{"purpose 为空", &PasswordReset{UserID: u.ID, TokenHash: hexHash("a"), ExpiresAt: time.Now().Add(time.Hour)}},
		{"缺 expires_at", &PasswordReset{UserID: u.ID, Purpose: PurposeEmailVerify, TokenHash: hexHash("a")}},
		// 64 位但不是十六进制：长度对、字符集不对（例如误存了 base64 的原文）。
		{"哈希不是十六进制", &PasswordReset{UserID: u.ID, Purpose: PurposeEmailVerify,
			TokenHash: strings.Repeat("z", 64), ExpiresAt: time.Now().Add(time.Hour)}},
		// 原文（43 字符的 base64url）直接当哈希传进来：必须被拒。
		{"哈希长度不对（可能是原文）", &PasswordReset{UserID: u.ID, Purpose: PurposeEmailVerify,
			TokenHash: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", ExpiresAt: time.Now().Add(time.Hour)}},
		// 已下线的 purpose：链接式改密的 `password_reset` 必须被拒（迁移 0015 收窄了 CHECK）。
		{"已下线的 password_reset 用途", &PasswordReset{UserID: u.ID, Purpose: "password_reset",
			TokenHash: hexHash("a"), ExpiresAt: time.Now().Add(time.Hour)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.InsertPasswordReset(ctx, tc.row); err == nil {
				t.Fatal("应当被拒绝")
			}
		})
	}
}

// TestConsumeEmailVerifyMarksVerified 验验证链路：单次使用 + 只认当前邮箱。
func TestConsumeEmailVerifyMarksVerified(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	u := seedUser(t, s, "alice")

	addr := "alice@example.com"
	if err := s.SetUserEmail(ctx, u.ID, addr, nil); err != nil { // 有邮箱、未验证
		t.Fatalf("设置邮箱失败：%v", err)
	}
	row := seedResetRow(t, s, u.ID, PurposeEmailVerify, hexHash("v1"), time.Now().Add(time.Hour))
	row.Email = &addr
	if err := s.DB().Exec(`UPDATE password_resets SET email = ? WHERE id = ?`, addr, row.ID).Error; err != nil {
		t.Fatalf("补 email 失败：%v", err)
	}

	if id, ok, err := s.ConsumeEmailVerify(ctx, hexHash("v1")); err != nil || !ok || id != u.ID {
		t.Fatalf("验证应当成功：ok=%v id=%d err=%v", ok, id, err)
	}
	after, _ := s.UserByID(ctx, u.ID)
	if after.EmailVerifiedAt == nil {
		t.Fatal("验证之后 email_verified_at 必须落值")
	}
	// 单次使用。
	if _, ok, _ := s.ConsumeEmailVerify(ctx, hexHash("v1")); ok {
		t.Fatal("验证令牌必须只能使用一次")
	}

	// **防错配**：令牌签发之后邮箱被换掉，旧令牌不得把新地址标记成已验证。
	if err := s.SetUserEmail(ctx, u.ID, "new@example.com", nil); err != nil {
		t.Fatalf("换邮箱失败：%v", err)
	}
	row2 := seedResetRow(t, s, u.ID, PurposeEmailVerify, hexHash("v2"), time.Now().Add(time.Hour))
	if err := s.DB().Exec(`UPDATE password_resets SET email = ? WHERE id = ?`, addr, row2.ID).Error; err != nil {
		t.Fatalf("补 email 失败：%v", err)
	}
	if _, ok, err := s.ConsumeEmailVerify(ctx, hexHash("v2")); err != nil || ok {
		t.Fatalf("邮箱已变时旧令牌不得通过：ok=%v err=%v", ok, err)
	}
	var used sql.NullTime
	if err := s.DB().Raw(`SELECT used_at FROM password_resets WHERE id = ?`, row2.ID).Scan(&used).Error; err != nil {
		t.Fatalf("回读失败：%v", err)
	}
	if !used.Valid {
		t.Fatal("防错配路径也要把令牌烧掉（fail closed）")
	}
	after2, _ := s.UserByID(ctx, u.ID)
	if after2.EmailVerifiedAt != nil {
		t.Fatal("换过邮箱之后不该留下「已验证」状态")
	}
}

// TestDeleteExpiredPasswordResets 验清理（只按 expires_at，不留"已用未过期"的行）。
func TestDeleteExpiredPasswordResets(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	u := seedUser(t, s, "alice")

	seedResetRow(t, s, u.ID, PurposeEmailVerify, hexHash("old"), time.Now().Add(-2*time.Hour))
	seedResetRow(t, s, u.ID, PurposeEmailVerify, hexHash("fresh"), time.Now().Add(time.Hour))

	n, err := s.DeleteExpiredPasswordResets(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("清理失败：%v", err)
	}
	if n != 1 {
		t.Fatalf("应当删掉 1 行过期令牌，实际 %d", n)
	}
	if _, err := s.DeleteExpiredPasswordResets(ctx, time.Time{}); err == nil {
		t.Fatal("零值基准必须被拒（否则会删掉全部未过期的行）")
	}
}

// —— 邮箱列：唯一性与归一化 ——

func TestUserEmailUniquenessIsCaseInsensitive(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()

	addr := "Alice@Example.COM"
	first := &User{Username: "alice", DisplayName: "Alice", PasswordHash: "h", Email: &addr}
	if err := s.CreateUser(ctx, first); err != nil {
		t.Fatalf("首次建号失败：%v", err)
	}
	// 落库形态是全小写（canonicalEmail 的落点）。
	got, err := s.UserByID(ctx, first.ID)
	if err != nil {
		t.Fatalf("回读失败：%v", err)
	}
	if got.Email == nil || *got.Email != "alice@example.com" {
		t.Fatalf("邮箱必须以全小写落库，实际 %v", got.Email)
	}
	// 大小写不同的同一邮箱：唯一索引（建在 lower(email) 上）必须拦住它。
	dup := "ALICE@example.com"
	err = s.CreateUser(ctx, &User{Username: "bob", DisplayName: "Bob", PasswordHash: "h", Email: &dup})
	if !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("同一邮箱（大小写不同）应当返回 ErrEmailTaken，实际 %v", err)
	}
	// 这必须是**专用**哨兵：拿 ErrUsernameTaken 会让用户去改用户名。
	if errors.Is(err, ErrUsernameTaken) {
		t.Fatal("邮箱冲突不该被报成用户名冲突")
	}

	// 多个 NULL 邮箱互不冲突（部分唯一索引的 WHERE 子句）。
	for _, name := range []string{"carol", "dave"} {
		if err := s.CreateUser(ctx, &User{Username: name, DisplayName: name, PasswordHash: "h"}); err != nil {
			t.Fatalf("%s 无邮箱应当能建号：%v", name, err)
		}
	}

	// 按邮箱查：大小写不敏感，且空邮箱不去查库（直接 ErrNotFound）。
	found, err := s.UserByEmail(ctx, "  alice@EXAMPLE.com ")
	if err != nil || found.ID != first.ID {
		t.Fatalf("按邮箱查应当命中并归一化，实际 id=%d err=%v", found.ID, err)
	}
	if _, err := s.UserByEmail(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("空邮箱应当返回 ErrNotFound，实际 %v", err)
	}
	if _, err := s.UserByEmail(ctx, "nobody@example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的邮箱应当返回 ErrNotFound，实际 %v", err)
	}
}

// TestSetUserEmailBindsVerifiedAndClearsBothColumns 验两列必须一起动。
func TestSetUserEmailBindsVerifiedAndClearsBothColumns(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	u := seedUser(t, s, "alice")

	now := time.Now()
	if err := s.SetUserEmail(ctx, u.ID, "Alice@Example.com", &now); err != nil {
		t.Fatalf("绑定失败：%v", err)
	}
	got, _ := s.UserByID(ctx, u.ID)
	if got.Email == nil || *got.Email != "alice@example.com" {
		t.Fatalf("应当归一化成小写，实际 %v", got.Email)
	}
	if got.EmailVerifiedAt == nil {
		t.Fatal("verifiedAt 非 nil 时必须落值（管理员绑定即已验证）")
	}

	// 换邮箱但**不带**验证时间：验证状态必须回到"未验证"。
	if err := s.SetUserEmail(ctx, u.ID, "other@example.com", nil); err != nil {
		t.Fatalf("换绑失败：%v", err)
	}
	got, _ = s.UserByID(ctx, u.ID)
	if got.EmailVerifiedAt != nil {
		t.Fatal("换邮箱必须同时清掉验证时间（留下自相矛盾的状态）")
	}

	// 解绑：两列一起清空。
	if err := s.SetUserEmail(ctx, u.ID, "", nil); err != nil {
		t.Fatalf("解绑失败：%v", err)
	}
	got, _ = s.UserByID(ctx, u.ID)
	if got.Email != nil || got.EmailVerifiedAt != nil {
		t.Fatalf("解绑必须同时清空两列：email=%v verified=%v", got.Email, got.EmailVerifiedAt)
	}

	// 非法输入。
	if err := s.SetUserEmail(ctx, 0, "a@b.com", nil); err == nil {
		t.Fatal("user_id 非法必须被拒")
	}
	if err := s.SetUserEmail(ctx, 999999, "a@b.com", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的账号应当返回 ErrNotFound，实际 %v", err)
	}
	// 超长邮箱（>254）：在应用层就拒。
	long := strings.Repeat("a", 250) + "@example.com"
	if err := s.SetUserEmail(ctx, u.ID, long, nil); err == nil {
		t.Fatal("超长邮箱必须被拒")
	}
	// 控制字符会被剥离（canonicalEmail 的第二道防线）：换行不该进库。
	if err := s.SetUserEmail(ctx, u.ID, "a@b.com\r\nBcc: x@y.com", nil); err != nil {
		t.Fatalf("应当剥离控制字符而不是报错：%v", err)
	}
	got, _ = s.UserByID(ctx, u.ID)
	if got.Email != nil && strings.ContainsAny(*got.Email, "\r\n") {
		t.Fatalf("库里不该出现带换行的邮箱：%q", *got.Email)
	}
}

// TestCreateUserRejectsPreVerifiedEmail 钉住"注册即未验证"这条不变量。
func TestCreateUserRejectsPreVerifiedEmail(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	now := time.Now()
	addr := "a@b.com"
	err := s.CreateUser(ctx, &User{
		Username: "alice", DisplayName: "Alice", PasswordHash: "h",
		Email: &addr, EmailVerifiedAt: &now,
	})
	if err == nil {
		t.Fatal("建号即已验证等于绕开整条归属证明链，必须被拒")
	}
}
