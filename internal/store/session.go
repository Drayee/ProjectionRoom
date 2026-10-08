package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// CreateSession 写入一行 refresh 会话（§4/§5）。
//
// 校验与归一化：
//   - UserID 必须 > 0；
//   - TokenHash 必须是 64 位 sha256 十六进制（见 canonicalTokenHash）；
//   - ExpiresAt 必须给出（DDL 里是 NOT NULL 且无默认值）；
//   - IssuedAt 为零值 → 补成 now()。DDL 虽然有 DEFAULT now()，但 GORM 的 INSERT
//     会显式带上该列，默认值在这条路径上不会被用到，零值会写成 0001-01-01。
func (s *Store) CreateSession(ctx context.Context, sess *Session) error {
	if sess == nil {
		return errors.New("store: CreateSession 收到 nil")
	}
	if sess.UserID <= 0 {
		return errors.New("store: session 缺少 user_id")
	}
	hash, err := canonicalTokenHash(sess.TokenHash)
	if err != nil {
		return err
	}
	sess.TokenHash = hash
	if sess.ExpiresAt.IsZero() {
		return errors.New("store: session 缺少 expires_at")
	}
	if sess.IssuedAt.IsZero() {
		sess.IssuedAt = time.Now()
	}
	if sess.UserAgent != nil {
		v := trimControl(*sess.UserAgent)
		sess.UserAgent = &v
	}
	if sess.IP != nil {
		v := trimControl(*sess.IP)
		sess.IP = &v
	}

	if err := s.db.WithContext(ctx).Create(sess).Error; err != nil {
		if isUniqueViolation(err) {
			// sessions 表上只有 token_hash 一个唯一约束，所以这里的唯一冲突没有歧义。
			// 上层把它当作"并发双花/哈希复用"来判（errors.Is(err, ErrTokenHashTaken)）。
			return ErrTokenHashTaken
		}
		return fmt.Errorf("store: 创建会话失败: %w", err)
	}
	return nil
}

// SessionByTokenHash 按哈希查会话（刷新流程的第一步）；不存在返回 ErrNotFound。
//
// 格式不对的哈希直接当"不存在"返回：库里不可能有这行，调用方只需要知道
// "这个 refresh token 无效"。写入侧（CreateSession）才是要吵的地方。
func (s *Store) SessionByTokenHash(ctx context.Context, hash string) (*Session, error) {
	h, err := canonicalTokenHash(hash)
	if err != nil {
		return nil, ErrNotFound
	}
	var sess Session
	if err := s.db.WithContext(ctx).Where("token_hash = ?", h).Take(&sess).Error; err != nil {
		return nil, wrapDBErr("按 token_hash 查会话", err)
	}
	return &sess, nil
}

// RevokeSession 撤销单个会话（登出）。
//
// COALESCE 让重复撤销保持**首次**撤销时间：登出两次不该把 revoked_at 推后
// （§5 的重放检测依赖"这个 token 什么时候被撤销的"）。
// RowsAffected 依然能区分两种情况：0 = 行不存在（ErrNotFound），
// 1 = 行存在（即使已经撤销过 —— PostgreSQL 写相同值也算命中一行）。
func (s *Store) RevokeSession(ctx context.Context, id int64) error {
	res := s.db.WithContext(ctx).
		Model(&Session{}).
		Where("id = ?", id).
		Update("revoked_at", gorm.Expr("COALESCE(revoked_at, now())"))
	if res.Error != nil {
		return fmt.Errorf("store: 撤销会话失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokeAllSessions 撤销某用户的全部**未撤销**会话，返回本次真正撤销的行数。
//
// 只计本次撤销的行（WHERE revoked_at IS NULL），所以返回值可以原样作为
// "踢掉了几台设备"上报；重复调用会返回 0 而不是重复计数。
// 撤销时间用数据库的 now()，避免应用与 DB 时钟漂移让"撤销早于签发"这类判断错乱。
func (s *Store) RevokeAllSessions(ctx context.Context, userID int64) (int64, error) {
	res := s.db.WithContext(ctx).
		Model(&Session{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Update("revoked_at", gorm.Expr("now()"))
	if res.Error != nil {
		return 0, fmt.Errorf("store: 撤销用户全部会话失败: %w", res.Error)
	}
	return res.RowsAffected, nil
}

// DeleteExpiredSessions 删除 expires_at 早于 before 的会话行（清理协程用），
// 返回删除行数。传什么基准时间（now() 还是 now()-宽限期）由调用方决定。
//
// 条件只看 expires_at，不看 revoked_at：撤销过但**尚未过期**的行要留着 ——
// 它是 §5 重放检测的证据（收到已撤销 token 时才认得出这是重放）。
func (s *Store) DeleteExpiredSessions(ctx context.Context, before time.Time) (int64, error) {
	if before.IsZero() {
		return 0, errors.New("store: DeleteExpiredSessions 需要一个明确的时间基准")
	}
	res := s.db.WithContext(ctx).Where("expires_at < ?", before).Delete(&Session{})
	if res.Error != nil {
		return 0, fmt.Errorf("store: 清理过期会话失败: %w", res.Error)
	}
	return res.RowsAffected, nil
}

// canonicalTokenHash 校验并规范化 refresh token 的哈希（转小写、去空白）。
//
// 这里刻意做格式校验：本列存的是 sha256 十六进制（§4/§5），如果调用方误把
// **原始 token** 传进来，那是一处真实的凭据泄漏（库里存了可直接使用的凭证）。
// 长度 + 字符集校验能挡住 base64、原始字节这类常见误用；
// 它挡不住"有人把原始 token 做了一遍 hex 编码"——那属于调用方必须遵守的契约
// （哈希由 auth 层计算），不是这里能验证的。
func canonicalTokenHash(hash string) (string, error) {
	h := strings.ToLower(strings.TrimSpace(hash))
	if len(h) != tokenHashHexLen {
		return "", fmt.Errorf("store: token_hash 必须是 %d 位 sha256 十六进制（收到 %d 位）", tokenHashHexLen, len(h))
	}
	for i := 0; i < len(h); i++ {
		c := h[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			continue
		}
		return "", errors.New("store: token_hash 含非十六进制字符")
	}
	return h, nil
}
