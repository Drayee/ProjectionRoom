package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCreateSessionAndLookup(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	u := seedUser(t, s, "alice")

	ua := "Mozilla/5.0（测试）"
	ip := "203.0.113.7"
	sess := &Session{
		UserID:    u.ID,
		TokenHash: hexHash("refresh-1"),
		UserAgent: &ua,
		IP:        &ip,
		ExpiresAt: time.Now().Add(30 * 24 * time.Hour),
		// IssuedAt 故意留零值：GORM 的 INSERT 会显式带上该列，DDL 默认值用不到。
	}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatalf("创建会话失败：%v", err)
	}
	if sess.ID == 0 {
		t.Fatalf("创建后应回填自增主键")
	}
	if sess.IssuedAt.IsZero() {
		t.Fatalf("IssuedAt 零值应被补成当前时间")
	}
	ensureTimeClose(t, "expires_at", sess.ExpiresAt, sess.ExpiresAt)

	// 查询侧归一化：传大写十六进制也能命中（哈希转小写后匹配 char(64) 列）。
	got, err := s.SessionByTokenHash(ctx, strings.ToUpper(sess.TokenHash))
	if err != nil {
		t.Fatalf("按 token_hash 查会话失败：%v", err)
	}
	if got.ID != sess.ID {
		t.Fatalf("查到的会话不对：want id=%d got id=%d", sess.ID, got.ID)
	}
	// char(64) 列回读不能带尾随空格（否则重放检测的比较会全面失效）。
	if got.TokenHash != sess.TokenHash {
		t.Fatalf("token_hash 往返不一致：want=%q got=%q（长度 %d/%d）",
			sess.TokenHash, got.TokenHash, len(sess.TokenHash), len(got.TokenHash))
	}
	if got.UserAgent == nil || *got.UserAgent != ua {
		t.Fatalf("user_agent 往返失败：%v", got.UserAgent)
	}
	if got.IP == nil || *got.IP != ip {
		t.Fatalf("ip 往返失败：%v", got.IP)
	}
	if got.RevokedAt != nil {
		t.Fatalf("新建会话不该是已撤销状态")
	}
}

func TestCreateSessionValidation(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	u := seedUser(t, s, "alice")
	future := time.Now().Add(time.Hour)

	cases := []struct {
		name string
		sess *Session
		want string
	}{
		{"nil", nil, "nil"},
		{"缺 user_id", &Session{TokenHash: hexHash("a"), ExpiresAt: future}, "user_id"},
		{"缺 token_hash", &Session{UserID: u.ID, ExpiresAt: future}, "token_hash"},
		{"token_hash 长度不对", &Session{UserID: u.ID, TokenHash: "abc", ExpiresAt: future}, "sha256"},
		{"token_hash 非十六进制", &Session{UserID: u.ID, TokenHash: strings.Repeat("z", 64), ExpiresAt: future}, "十六进制"},
		{"缺 expires_at", &Session{UserID: u.ID, TokenHash: hexHash("b")}, "expires_at"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := s.CreateSession(ctx, c.sess)
			if err == nil {
				t.Fatalf("应该报错")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("错误信息应包含 %q，实际 %v", c.want, err)
			}
		})
	}

	// 外键：用户不存在时插入必须失败（§4 的 REFERENCES users(id)）。
	err := s.CreateSession(ctx, &Session{UserID: 999999, TokenHash: hexHash("ghost"), ExpiresAt: future})
	if err == nil {
		t.Fatalf("不存在的 user_id 应该被外键挡住")
	}

	var count int64
	if err := s.DB().Model(&Session{}).Count(&count).Error; err != nil {
		t.Fatalf("统计会话失败：%v", err)
	}
	if count != 0 {
		t.Fatalf("校验失败的调用不该写入任何行，实际 %d 行", count)
	}
}

func TestCreateSessionRejectsDuplicateTokenHash(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	u := seedUser(t, s, "alice")
	hash := hexHash("same-refresh")
	future := time.Now().Add(time.Hour)

	if err := s.CreateSession(ctx, &Session{UserID: u.ID, TokenHash: hash, ExpiresAt: future}); err != nil {
		t.Fatalf("第一次创建失败：%v", err)
	}
	// 同一个 token 落两次 = 调用方复用了刷新凭证（或哈希算错），必须吵。
	// 返回值必须是哨兵错误：上层（usecase 的并发双花判定）靠 errors.Is 认它，
	// 而不是匹配错误文案。
	err := s.CreateSession(ctx, &Session{UserID: u.ID, TokenHash: hash, ExpiresAt: future})
	if err == nil {
		t.Fatalf("重复 token_hash 应该被唯一约束挡住")
	}
	if !errors.Is(err, ErrTokenHashTaken) {
		t.Fatalf("重复 token_hash 应返回 ErrTokenHashTaken，实际 %v", err)
	}
	// 并且不能被误判成用户名冲突。
	if errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("会话冲突不该映射成 ErrUsernameTaken，实际 %v", err)
	}
}

func TestSessionByTokenHashNotFound(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()

	if _, err := s.SessionByTokenHash(ctx, hexHash("nope")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的哈希应返回 ErrNotFound，实际 %v", err)
	}
	// 格式非法 = 库里不可能有 → 也返回 ErrNotFound（读路径不该抛校验错误）。
	if _, err := s.SessionByTokenHash(ctx, "not-a-hash"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("格式非法的哈希应返回 ErrNotFound，实际 %v", err)
	}
	if _, err := s.SessionByTokenHash(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("空哈希应返回 ErrNotFound，实际 %v", err)
	}
}

func TestRevokeSession(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	u := seedUser(t, s, "alice")
	sess := &Session{UserID: u.ID, TokenHash: hexHash("r1"), ExpiresAt: time.Now().Add(time.Hour)}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatalf("创建会话失败：%v", err)
	}

	if err := s.RevokeSession(ctx, sess.ID); err != nil {
		t.Fatalf("撤销失败：%v", err)
	}
	first, err := s.SessionByTokenHash(ctx, sess.TokenHash)
	if err != nil {
		t.Fatalf("查会话失败：%v", err)
	}
	if first.RevokedAt == nil {
		t.Fatalf("撤销后 revoked_at 必须有值")
	}
	revokedAt := *first.RevokedAt

	// 重复撤销是幂等的成功，且**不改**首次撤销时间（重放检测依赖它）。
	time.Sleep(5 * time.Millisecond)
	if err := s.RevokeSession(ctx, sess.ID); err != nil {
		t.Fatalf("重复撤销不该报错：%v", err)
	}
	second, err := s.SessionByTokenHash(ctx, sess.TokenHash)
	if err != nil {
		t.Fatalf("查会话失败：%v", err)
	}
	if second.RevokedAt == nil || !second.RevokedAt.Equal(revokedAt) {
		t.Fatalf("重复撤销不该推后 revoked_at：first=%s second=%v", revokedAt, second.RevokedAt)
	}

	if err := s.RevokeSession(ctx, 999999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的会话应返回 ErrNotFound，实际 %v", err)
	}
}

func TestRevokeAllSessions(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	alice := seedUser(t, s, "alice")
	bob := seedUser(t, s, "bob")
	future := time.Now().Add(time.Hour)

	for i, hash := range []string{hexHash("a1"), hexHash("a2"), hexHash("a3")} {
		if err := s.CreateSession(ctx, &Session{UserID: alice.ID, TokenHash: hash, ExpiresAt: future}); err != nil {
			t.Fatalf("建 alice 的第 %d 个会话失败：%v", i+1, err)
		}
	}
	if err := s.CreateSession(ctx, &Session{UserID: bob.ID, TokenHash: hexHash("b1"), ExpiresAt: future}); err != nil {
		t.Fatalf("建 bob 的会话失败：%v", err)
	}

	n, err := s.RevokeAllSessions(ctx, alice.ID)
	if err != nil {
		t.Fatalf("撤销全部会话失败：%v", err)
	}
	if n != 3 {
		t.Fatalf("应该撤销 3 个会话，实际 %d", n)
	}
	// 重复调用：只计本次真正撤销的行。
	n, err = s.RevokeAllSessions(ctx, alice.ID)
	if err != nil {
		t.Fatalf("重复撤销失败：%v", err)
	}
	if n != 0 {
		t.Fatalf("第二次应该撤销 0 行，实际 %d", n)
	}
	// bob 的会话不受影响。
	bobSess, err := s.SessionByTokenHash(ctx, hexHash("b1"))
	if err != nil {
		t.Fatalf("查 bob 的会话失败：%v", err)
	}
	if bobSess.RevokedAt != nil {
		t.Fatalf("撤销别人的会话不该影响 bob")
	}
}

func TestDeleteExpiredSessions(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	u := seedUser(t, s, "alice")
	now := time.Now()

	expired := &Session{UserID: u.ID, TokenHash: hexHash("expired"), ExpiresAt: now.Add(-time.Hour)}
	alive := &Session{UserID: u.ID, TokenHash: hexHash("alive"), ExpiresAt: now.Add(time.Hour)}
	revokedButAlive := &Session{UserID: u.ID, TokenHash: hexHash("revoked-alive"), ExpiresAt: now.Add(time.Hour)}
	for _, sess := range []*Session{expired, alive, revokedButAlive} {
		if err := s.CreateSession(ctx, sess); err != nil {
			t.Fatalf("创建会话失败：%v", err)
		}
	}
	if err := s.RevokeSession(ctx, revokedButAlive.ID); err != nil {
		t.Fatalf("撤销失败：%v", err)
	}

	n, err := s.DeleteExpiredSessions(ctx, now)
	if err != nil {
		t.Fatalf("清理过期会话失败：%v", err)
	}
	if n != 1 {
		t.Fatalf("只应删掉 1 行过期会话，实际 %d", n)
	}
	if _, err := s.SessionByTokenHash(ctx, expired.TokenHash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("过期行应被删除，实际 %v", err)
	}
	// 已撤销但未过期的行必须保留：它是 §5 重放检测的证据。
	if _, err := s.SessionByTokenHash(ctx, revokedButAlive.TokenHash); err != nil {
		t.Fatalf("已撤销未过期的行应保留（重放检测证据），实际 %v", err)
	}
	if _, err := s.SessionByTokenHash(ctx, alive.TokenHash); err != nil {
		t.Fatalf("未过期行不该被删：%v", err)
	}

	if _, err := s.DeleteExpiredSessions(ctx, time.Time{}); err == nil {
		t.Fatalf("零值时间基准应该报错（防误删全表）")
	}
}
