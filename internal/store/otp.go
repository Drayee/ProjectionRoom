package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// 邮箱验证码（OTP）的仓储。
//
// 两条路径，机制相同、归属不同：
//
//	① **已存在账号**（改密码 / 补验证）：行落在 `password_resets`（purpose = email_otp），
//	   与链接令牌共用"只存 sha256、单次使用、到期即废"这一整套实现。
//	② **注册两段式**（账号还没建）：行落在 `register_drafts` 上（`email_otp_hash` +
//	   `email` 两列）。不把 user_id 改成可空是刻意的 —— 迁移只做加法，
//	   而 schema 契约也要求 password_resets 的每一行都属于某个账号。
//
// 两条路径共用 `ErrOTPAttemptsExceeded` 与同样的 6 位数字 + 错误计数语义。

// ErrOTPAttemptsExceeded 表示这条码因错误次数用尽而被作废。
//
// 单独一个错误而不是并入"码不对"：服务端需要据此告诉用户"这条码已作废，
// 请重新获取"，否则他会一直重试同一串已经死掉的数字。
var ErrOTPAttemptsExceeded = errors.New("store: 验证码错误次数过多，已作废")

// ErrOTPNotFound 表示没有可用的验证码（没发过 / 已用 / 已过期）。
var ErrOTPNotFound = errors.New("store: 没有可用的验证码")

// InsertEmailOTP 给**已存在的账号**写入一条验证码并作废同用途旧码（同一事务）。
func (s *Store) InsertEmailOTP(ctx context.Context, t *PasswordReset) error {
	if t == nil {
		return errors.New("store: InsertEmailOTP 收到 nil")
	}
	if t.UserID <= 0 {
		return errors.New("store: 账号验证码缺少 user_id（注册草稿走 InsertDraftOTP）")
	}
	t.Purpose = PurposeEmailOTP
	if t.ExpiresAt.IsZero() {
		return errors.New("store: 验证码缺少 expires_at")
	}
	if t.Email == nil || canonicalEmail(*t.Email) == "" {
		// OTP **必须**带邮箱：它是唯一能把码和收件人绑起来的东西
		//（链接令牌靠 token 本身足够长，OTP 只有 6 位，必须叠加收件人维度）。
		return errors.New("store: 验证码必须带邮箱")
	}
	t.Attempts = 0

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 作废该账号同用途的未用旧码：让"最新一条"成为唯一有效的那条。
		// 用 UPDATE 而不是 DELETE：这些行留着能回答"刚才给这个账号发过几条"。
		if err := tx.Exec(
			`UPDATE password_resets SET used_at = now()
			  WHERE purpose = ? AND user_id = ? AND used_at IS NULL`,
			PurposeEmailOTP, t.UserID).Error; err != nil {
			return fmt.Errorf("store: 作废旧验证码失败: %w", err)
		}
		if err := normalizePasswordReset(t); err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Create(t).Error; err != nil {
			return fmt.Errorf("store: 写入验证码失败: %w", err)
		}
		return nil
	})
}

// ConsumeEmailOTP 校验**账号**验证码；正确则立即标记已用。
//
// 三件事在同一个事务里，否则会留下可爆破的窗口：
//
//	① 找到"该账号最近一条未用未过期的码"（FOR UPDATE 串行化并发校验）；
//	② 比对哈希 —— 不匹配就 attempts+1，达到上限时把该行作废；
//	③ 匹配就标记已用。
//
// 把 ② 与 ③ 分成两步（先查后改）会让两个并发的正确请求都通过校验，
// 于是同一条码被消费两次。
func (s *Store) ConsumeEmailOTP(ctx context.Context, userID int64, otpHash string, maxAttempts int) (ok bool, err error) {
	if userID <= 0 {
		return false, errors.New("store: 校验账号验证码缺少 user_id")
	}
	return s.consumeOTPIn(ctx, otpTarget{userID: userID}, otpHash, maxAttempts)
}

// ConsumeDraftOTP 校验**注册草稿**上的验证码；正确则清掉草稿上的码并返回草稿。
//
// 与 ConsumeEmailOTP 共用同一套比对/计数逻辑（consumeOTPIn），差别只在表与列。
func (s *Store) ConsumeDraftOTP(ctx context.Context, ticketHash, otpHash string, maxAttempts int) (draft *RegisterDraft, ok bool, err error) {
	hash, err := canonicalTokenHash(ticketHash)
	if err != nil {
		return nil, false, nil
	}
	if maxAttempts <= 0 {
		maxAttempts = 5
	}

	// 同 consumeOTPIn：不可用的结论**不能**从回调里 return（会回滚掉作废更新）。
	var unavailable error

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var d RegisterDraft
		res := tx.Raw(
			`SELECT * FROM register_drafts
			  WHERE ticket_hash = ? AND consumed_at IS NULL AND expires_at > now()
			  LIMIT 1
			  FOR UPDATE`, hash).Scan(&d)
		if res.Error != nil {
			return fmt.Errorf("store: 查询注册草稿失败: %w", res.Error)
		}
		if d.ID == 0 {
			unavailable = ErrOTPNotFound
			return nil
		}

		if d.EmailOTPHash == nil || *d.EmailOTPHash == "" {
			unavailable = ErrOTPNotFound
			return nil
		}
		want, err := canonicalTokenHash(*d.EmailOTPHash)
		if err != nil {
			unavailable = ErrOTPNotFound
			return nil
		}
		got, err := canonicalTokenHash(otpHash)
		if err != nil {
			got = ""
		}

		if d.Attempts >= maxAttempts {
			// 已用尽：把草稿烧掉，避免它继续被读到。
			if err := tx.Exec(`UPDATE register_drafts SET consumed_at = now() WHERE id = ?`, d.ID).Error; err != nil {
				return fmt.Errorf("store: 作废超限草稿失败: %w", err)
			}
			unavailable = ErrOTPAttemptsExceeded
			return nil
		}

		if got == "" || got != want {
			next := d.Attempts + 1
			updates := map[string]any{"attempts": next}
			var consumed *time.Time
			if next >= maxAttempts {
				now := time.Now()
				consumed = &now
				updates["consumed_at"] = consumed
			}
			if err := tx.Model(&RegisterDraft{}).Where("id = ?", d.ID).Updates(updates).Error; err != nil {
				return fmt.Errorf("store: 记录验证码错误次数失败: %w", err)
			}
			if consumed != nil {
				unavailable = ErrOTPAttemptsExceeded
			}
			return nil
		}

		// 正确：清掉码（单次使用）+ 把 attempts 归零（后续建号不再受它影响）。
		if err := tx.Model(&RegisterDraft{}).Where("id = ?", d.ID).
			Updates(map[string]any{"email_otp_hash": nil, "attempts": 0}).Error; err != nil {
			return fmt.Errorf("store: 消费草稿验证码失败: %w", err)
		}
		d.EmailOTPHash = nil
		draft, ok = &d, true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if unavailable != nil {
		// 事务已提交（该作废的都作废了），再把结论交给调用方。
		return nil, false, unavailable
	}
	return draft, ok, nil
}

// otpTarget 指明要校验哪一类 OTP（只有账号这一路走通用实现；草稿单独实现，
// 因为它的表与列名都不同，硬套会让 SQL 里出现两个分支）。
type otpTarget struct {
	userID int64
}

// consumeOTPIn 是**账号** OTP 的比对与消费实现。
func (s *Store) consumeOTPIn(ctx context.Context, target otpTarget, otpHash string, maxAttempts int) (ok bool, err error) {
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	hash, err := canonicalTokenHash(otpHash)
	if err != nil {
		// 形状不对的哈希不可能命中库里任何一行 —— 按"不可用"处理，不去查库。
		return false, nil
	}

	// unavailable 承载"不可用"的结论（不存在 / 次数用尽 / 已被消费）。
	//
	// 为什么不用 `return err`：从 Transaction 回调里返回 error 会让 GORM **回滚**，
	// 那些"记错误次数""作废超限行"的 UPDATE 会跟着被撤掉 —— 上限就形同虚设。
	// 这个坑在真库用例里被 TestConsumeEmailOTPExhaustsAtLimit 抓到过一次。
	var unavailable error

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var cur PasswordReset
		// FOR UPDATE 锁住这一行：并发的两次校验必须串行，否则两边都读到 attempts=0，
		// 各自 +1 之后实际只加了一次（丢失更新），上限就被放大了。
		res := tx.Raw(
			`SELECT * FROM password_resets
			  WHERE purpose = ? AND user_id = ? AND used_at IS NULL AND expires_at > now()
			  ORDER BY created_at DESC
			  LIMIT 1
			  FOR UPDATE`,
			PurposeEmailOTP, target.userID).Scan(&cur)
		if res.Error != nil {
			return fmt.Errorf("store: 查询验证码失败: %w", res.Error)
		}
		if cur.ID == 0 {
			// ⚠ "不可用"**不能**用 `return err` 表达：从 Transaction 的回调里返回 error
			// 会触发**回滚** —— 而下面那些"作废这条码"的 UPDATE 必须真的落下。
			// 所以这类结论记在不可用标志里，回调返回 nil 让事务提交。
			unavailable = ErrOTPNotFound
			return nil
		}

		if cur.Attempts >= maxAttempts {
			if err := tx.Exec(`UPDATE password_resets SET used_at = now() WHERE id = ?`, cur.ID).Error; err != nil {
				return fmt.Errorf("store: 作废超限验证码失败: %w", err)
			}
			unavailable = ErrOTPAttemptsExceeded
			return nil
		}

		if cur.TokenHash != hash {
			next := cur.Attempts + 1
			// 达到上限的这一次就把行作废：留着一个"错满但还活着"的码没有意义，
			// 而"用尽"必须是终态（否则第 6 次再猜对仍然能过）。
			var usedAt *time.Time
			if next >= maxAttempts {
				now := time.Now()
				usedAt = &now
			}
			if err := tx.Model(&PasswordReset{}).Where("id = ?", cur.ID).
				Updates(map[string]any{"attempts": next, "used_at": usedAt}).Error; err != nil {
				return fmt.Errorf("store: 记录验证码错误次数失败: %w", err)
			}
			if usedAt != nil {
				unavailable = ErrOTPAttemptsExceeded
			}
			return nil
		}

		res2 := tx.Exec(
			`UPDATE password_resets SET used_at = now()
			  WHERE id = ? AND used_at IS NULL`, cur.ID)
		if res2.Error != nil {
			return fmt.Errorf("store: 消费验证码失败: %w", res2.Error)
		}
		if res2.RowsAffected == 0 {
			unavailable = ErrOTPNotFound
			return nil
		}
		ok = true
		return nil
	})
	if err != nil {
		return false, err
	}
	if unavailable != nil {
		// 事务已提交（该落的状态都落了），再把"不可用"的结论交给调用方。
		return false, unavailable
	}
	return ok, nil
}

// CountRecentEmailOTP 数一下某账号在 since 之后签发了几条验证码（配额用）。
func (s *Store) CountRecentEmailOTP(ctx context.Context, userID int64, since time.Time) (int64, error) {
	if userID <= 0 {
		return 0, errors.New("store: 统计验证码缺少 user_id")
	}
	var n int64
	err := s.db.WithContext(ctx).Model(&PasswordReset{}).
		Where("purpose = ? AND user_id = ? AND created_at >= ?", PurposeEmailOTP, userID, since).
		Count(&n).Error
	if err != nil {
		return 0, fmt.Errorf("store: 统计验证码签发次数失败: %w", err)
	}
	return n, nil
}

// LastEmailOTPSentAt 返回某账号最近一次签发验证码的时刻（零值 = 从没发过）。
//
// 冷却期用它：它回答的是"隔了多久"而不是"发了几条"，而 60 秒冷却正是按
// "隔了多久"定义的。
func (s *Store) LastEmailOTPSentAt(ctx context.Context, userID int64) (time.Time, error) {
	if userID <= 0 {
		return time.Time{}, errors.New("store: 查询验证码时间缺少 user_id")
	}
	// 注意：这里**不能**写 `Select("max(created_at)").Scan(&last)`。
	//
	// 一行都没发过时 `max()` 返回 SQL NULL，而 GORM 会把它扫进 *time.Time，
	// 报 "unsupported Scan, storing driver.Value type <nil> into type *time.Time"。
	// 那是一个 500 —— 而"这个账号还没发过码"是完全正常的状态（首次发码必经）。
	//
	// 用 Row().Scan(&sql.NullTime) 明确表达"可能没有行/可能是 NULL"。
	var last sql.NullTime
	row := s.db.WithContext(ctx).Raw(
		`SELECT max(created_at) FROM password_resets WHERE purpose = ? AND user_id = ?`,
		PurposeEmailOTP, userID).Row()
	if err := row.Scan(&last); err != nil {
		return time.Time{}, fmt.Errorf("store: 查询最近验证码时间失败: %w", err)
	}
	if !last.Valid {
		return time.Time{}, nil
	}
	return last.Time, nil
}
