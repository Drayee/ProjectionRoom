package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// 一次性邮件令牌的仓储（验证邮箱 / 验证码改密）。
//
// 本文件的全部价值集中在**两件事**上：
//
//  1. **"校验"与"标记已用"必须是同一条语句**（`UPDATE ... WHERE used_at IS NULL
//     ... RETURNING`）。分两步（先 SELECT 再 UPDATE）会留下并发窗口：两个同时
//     到达的请求都能通过校验，于是同一个 token 被用两次 —— 对验证码来说，
//     那等于"截获过邮件的人可以再改一次密码"。
//  2. **跨表动作必须在同一个事务里**。验证邮箱要同时成立两件事：令牌标记已用、
//     email_verified_at 落值。少任何一件都会留下一个 fail-open 的窗口
//     （token 可重放 / 邮箱已验证但库里没记）。
//
// ⚠ 历史上还有第三条（"改密 = 消费令牌 + 换口令 + 撤销全部会话 + 版本号自增"
// 四合一）。那条链接通道已随 `/api/auth/reset` 一起下线；改密码现在走验证码，
// 由 store.ConsumeEmailOTP（消费码）+ store.SetPasswordHash / RevokeAllSessions /
// BumpTokenVersion（三步）组成，见 service.replacePassword。

// InsertPasswordReset 写入一行一次性令牌（同步写，见 service 的 syncWriteReason 第 3 条）。
//
// 校验与归一化与 CreateSession 同风格：TokenHash 必须是 64 位 sha256 十六进制
// （拒绝把**原文**误存进来 —— 那是一处真实的凭据泄漏），ExpiresAt 必须给出。
func (s *Store) InsertPasswordReset(ctx context.Context, t *PasswordReset) error {
	if err := normalizePasswordReset(t); err != nil {
		return err
	}
	if err := s.db.WithContext(ctx).Create(t).Error; err != nil {
		return fmt.Errorf("store: 写入一次性令牌失败: %w", err)
	}
	return nil
}

// normalizePasswordReset 校验并归一化一行令牌（InsertPasswordReset 与
// InsertEmailOTP 共用）。
//
// 抽出来的理由：OTP 与链接令牌的字段约束**完全相同**（哈希形状、expires_at 必填、
// 邮箱归一、控制字符剥离），任何一处漂移都会让"某一种令牌忘了校验"成为漏洞。
// 唯一允许不同的是 user_id：OTP 在注册第二段之前账号还没建，那种情况下它是 0
// （DDL 已把该列改为可空，见迁移 0008）。
func normalizePasswordReset(t *PasswordReset) error {
	if t == nil {
		return errors.New("store: 收到 nil 令牌")
	}
	if !isValidTokenPurpose(t.Purpose) {
		return fmt.Errorf("store: 非法的 purpose %q（允许 %s / %s）",
			t.Purpose, PurposeEmailVerify, PurposeEmailOTP)
	}
	hash, err := canonicalTokenHash(t.TokenHash)
	if err != nil {
		return err
	}
	t.TokenHash = hash
	if t.ExpiresAt.IsZero() {
		return errors.New("store: password_resets 缺少 expires_at")
	}
	if t.CreatedAt.IsZero() {
		// DDL 有 DEFAULT now()，但 GORM 的 INSERT 会显式带上该列，
		// 零值会被写成 0001-01-01（与 CreateSession 同一个坑）。
		t.CreatedAt = time.Now()
	}
	if t.Email != nil {
		addr := canonicalEmail(*t.Email)
		if addr == "" {
			t.Email = nil
		} else {
			t.Email = &addr
		}
	}
	if t.UserAgent != nil {
		v := trimControl(*t.UserAgent)
		t.UserAgent = &v
	}
	if t.IP != nil {
		v := trimControl(*t.IP)
		t.IP = &v
	}
	return nil
}

// ConsumeEmailVerify 原子地消费一个**验证**令牌并把该账号的邮箱标记为已验证。
//
// 事务里两件事：① 令牌标记已用；② users.email_verified_at = now()。
//
// ② 带一个**防错配**条件：只有当 `users.email` 仍然等于令牌签发时记下的那个地址时
// 才落验证。否则会出现"用户先换了邮箱、再点开旧邮件的链接 → 把一个从未证明过的
// 新地址标记成已验证" —— 那条路径直接击穿整个门控。
//
// 令牌在地址已变的情况下同样被烧掉（fail closed）：不可用就是不可用。
func (s *Store) ConsumeEmailVerify(ctx context.Context, tokenHash string) (userID int64, ok bool, err error) {
	hash, err := canonicalTokenHash(tokenHash)
	if err != nil {
		return 0, false, nil
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row struct {
			UserID int64
			Email  *string
		}
		if err := tx.Raw(consumeTokenSQL, hash, PurposeEmailVerify).Scan(&row).Error; err != nil {
			return fmt.Errorf("store: 消费验证令牌失败: %w", err)
		}
		if row.UserID == 0 || row.Email == nil {
			return nil
		}

		res := tx.Exec(
			`UPDATE users SET email_verified_at = now()
			  WHERE id = ? AND email IS NOT NULL AND lower(email) = ?`,
			row.UserID, *row.Email)
		if res.Error != nil {
			return fmt.Errorf("store: 标记邮箱已验证失败: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			// 地址在这一轮里变过了（或被解绑）：令牌已烧掉，但不落验证。
			return nil
		}
		userID, ok = row.UserID, true
		return nil
	})
	if err != nil {
		return 0, false, err
	}
	return userID, ok, nil
}

// DeleteExpiredPasswordResets 删除 expires_at 早于 before 的行（清理协程用）。
//
// 与 DeleteExpiredSessions 一样，基准时间由调用方决定（now() 还是 now()-宽限）。
// 这里**不**额外保留"已用但未过期"的行：重置令牌用完就没有取证价值
// （与 refresh 的会话行不同 —— 那一行是重放检测的证据，这一行不是）。
func (s *Store) DeleteExpiredPasswordResets(ctx context.Context, before time.Time) (int64, error) {
	if before.IsZero() {
		return 0, errors.New("store: DeleteExpiredPasswordResets 需要一个明确的时间基准")
	}
	res := s.db.WithContext(ctx).Where("expires_at < ?", before).Delete(&PasswordReset{})
	if res.Error != nil {
		return 0, fmt.Errorf("store: 清理过期令牌失败: %w", res.Error)
	}
	return res.RowsAffected, nil
}

// consumeTokenSQL 是"校验并标记已用"的**唯一**实现（两个用途共用）。
//
// 逐条说明为什么长这样：
//   - `used_at IS NULL` 是单次使用的落点；
//   - `expires_at > now()` 用**数据库**时钟：应用与库的时间可能漂移，
//     而"什么时候过期"只该有一个权威（与 RevokeAllSessions 用 now() 同一条理由）；
//   - `purpose = ?` 必须进 WHERE：否则一封"验证邮箱"的信可以直接当"重置密码"用；
//   - `RETURNING user_id` 是"0 行 = 不可用"的判据，也是拿到目标账号的唯一途径
//     （调用方无法伪造一个 user_id 进来）。
//
// 注意它**不是** DDL，所以可以使用 '?' 占位符（migrations.go 的那条限制只针对 DDL）。
const consumeTokenSQL = `UPDATE password_resets
   SET used_at = now()
 WHERE token_hash = ?
   AND purpose = ?
   AND used_at IS NULL
   AND expires_at > now()
RETURNING user_id, email`

// isValidTokenPurpose 与 DDL 的 CHECK 约束对应（应用层先给可读错误）。
//
// ⚠ 这里**比 DDL 更窄**：DDL 还允许 `'password_reset'`（迁移 0008 的历史遗留，
// 见 models.go 的说明），但应用层不再接受它 —— 那条链路已删除，允许写入只会
// 制造永远不会被消费的死行。这类"应用层比 DB 更严"是刻意的：DB 的 CHECK 是
// 最后一道兜底（且受「只做加法」规约约束不能收窄），应用层才是真正的策略。
func isValidTokenPurpose(p string) bool {
	return p == PurposeEmailVerify || p == PurposeEmailOTP
}
