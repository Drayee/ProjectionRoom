package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// 注册草稿（两段式注册的中间态）的仓储。
//
// 为什么需要一张表而不是把状态放前端：第一段（用户名 + 密码）通过校验后，第二段
// 才收邮箱验证码。中间必须记住"这个 ticket 对应哪个用户名、哪份已算好的口令哈希"——
// 放前端等于把口令哈希交给浏览器，放内存则在重启后丢失（用户会卡在一个永远
// 验证不过的 ticket 上）。
//
// 三条约束：
//  1. **不含明文口令**：`password_hash` 是已经算好的 bcrypt，第二段直接搬进 users。
//     这也让整个注册流程只跑一次 bcrypt（那是最贵的一步）。
//  2. **ticket 只存 sha256**：与 token_hash 同一条纪律，原文只在下发那一次存在。
//  3. **单次消费**：`consumed_at` 非空即作废。保留行而不是删除，是为了让
//     "同 ticket 重放"能给出确定的错误（"这次注册已完成"），而不是含糊的"ticket 无效"。

// ErrDraftNotFound 表示 ticket 不存在 / 已用 / 已过期。
var ErrDraftNotFound = errors.New("store: 注册 ticket 不可用")

// ErrDraftUsernameTaken 表示起草期间该用户名被别人抢走了。
var ErrDraftUsernameTaken = errors.New("store: 用户名已被占用")

// InsertRegisterDraft 写入一份注册草稿（同步写：后续第二段全靠它）。
func (s *Store) InsertRegisterDraft(ctx context.Context, d *RegisterDraft) error {
	if d == nil {
		return errors.New("store: InsertRegisterDraft 收到 nil")
	}
	if d.Username == "" {
		return errors.New("store: 注册草稿缺少 username")
	}
	if d.PasswordHash == "" {
		return errors.New("store: 注册草稿缺少口令哈希")
	}
	if d.ExpiresAt.IsZero() {
		return errors.New("store: 注册草稿缺少 expires_at")
	}
	hash, err := canonicalTokenHash(d.TicketHash)
	if err != nil {
		return err
	}
	d.TicketHash = hash
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now()
	}
	if d.Email != nil {
		addr := canonicalEmail(*d.Email)
		if addr == "" {
			d.Email = nil
		} else {
			d.Email = &addr
		}
	}

	if err := s.db.WithContext(ctx).Create(d).Error; err != nil {
		return fmt.Errorf("store: 写入注册草稿失败: %w", err)
	}
	return nil
}

// DraftByTicket 按 ticket 哈希读一份**仍然可用**的草稿（不改状态）。
//
// 它给"重发验证码"用：那种路径不该消费草稿，只需要知道它的邮箱与是否还活着。
func (s *Store) DraftByTicket(ctx context.Context, ticketHash string) (*RegisterDraft, error) {
	hash, err := canonicalTokenHash(ticketHash)
	if err != nil {
		return nil, ErrDraftNotFound
	}
	var d RegisterDraft
	res := s.db.WithContext(ctx).
		Where("ticket_hash = ? AND consumed_at IS NULL AND expires_at > now()", hash).
		Limit(1).Find(&d)
	if res.Error != nil {
		return nil, fmt.Errorf("store: 查询注册草稿失败: %w", res.Error)
	}
	if d.ID == 0 {
		return nil, ErrDraftNotFound
	}
	return &d, nil
}

// AttachDraftOTP 给草稿挂上一条新的验证码（作废旧码）。
//
// 与账号路径的 InsertEmailOTP 对称：都是"作废旧码 + 写新码"在一个事务里，
// 让"最新一条"成为唯一有效的那条。
func (s *Store) AttachDraftOTP(ctx context.Context, ticketHash, email, otpHash string, expiresAt time.Time) error {
	hash, err := canonicalTokenHash(ticketHash)
	if err != nil {
		return ErrDraftNotFound
	}
	addr := canonicalEmail(email)
	if addr == "" {
		return errors.New("store: 草稿验证码缺少邮箱")
	}
	otp, err := canonicalTokenHash(otpHash)
	if err != nil {
		return err
	}
	if expiresAt.IsZero() {
		return errors.New("store: 草稿验证码缺少 expires_at")
	}

	res := s.db.WithContext(ctx).Model(&RegisterDraft{}).
		Where("ticket_hash = ? AND consumed_at IS NULL AND expires_at > now()", hash).
		Updates(map[string]any{
			"email":          addr,
			"email_otp_hash": otp,
			// 换新码时把错误计数清零：旧码的失败次数不该拖累新码。
			"attempts": 0,
		})
	if res.Error != nil {
		return fmt.Errorf("store: 挂载草稿验证码失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrDraftNotFound
	}
	return nil
}

// ConsumeDraft 把草稿标记已消费（建号成功后调用，与建号同一事务）。
//
// 返回 ok=false 表示草稿已被别的请求消费掉（并发双击），调用方据此避免重复建号。
func (s *Store) ConsumeDraftIn(ctx context.Context, tx *gorm.DB, draftID int64) (bool, error) {
	res := tx.WithContext(ctx).Exec(
		`UPDATE register_drafts SET consumed_at = now()
		  WHERE id = ? AND consumed_at IS NULL`, draftID)
	if res.Error != nil {
		return false, fmt.Errorf("store: 消费注册草稿失败: %w", res.Error)
	}
	return res.RowsAffected > 0, nil
}

// DeleteExpiredRegisterDrafts 清理过期草稿（清理协程用）。
func (s *Store) DeleteExpiredRegisterDrafts(ctx context.Context, before time.Time) (int64, error) {
	if before.IsZero() {
		return 0, errors.New("store: DeleteExpiredRegisterDrafts 需要一个明确的时间基准")
	}
	res := s.db.WithContext(ctx).Where("expires_at < ?", before).Delete(&RegisterDraft{})
	if res.Error != nil {
		return 0, fmt.Errorf("store: 清理过期注册草稿失败: %w", res.Error)
	}
	return res.RowsAffected, nil
}
