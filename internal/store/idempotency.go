package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// 写操作幂等键的仓储。
//
// 要解决的问题：注册（两段式）、发验证码这类操作**重试是常态**——
// 用户双击、网络超时后前端自动重发、反代重放。没有幂等键时，一次重试可能：
//
//	· 建出两个账号（register.verify 被重放）；
//	· 发出两封验证码（otp.send 被重放），而**第一封立刻作废**（因为发新码会作废旧码）
//	  —— 用户拿着第一封信里的码永远验证不过，这是最坏的形态；
//	· 白烧一次 bcrypt 与一次邮件配额。
//
// 设计取舍：
//
//  1. **键是 (scope, key)**：scope 区分端点（`register.start` / `register.verify` /
//     `otp.send`），key 是客户端生成的 UUID。不带 scope 会让"同一串 UUID 撞到
//     另一个端点"变成合法命中。
//  2. **存首次的响应体**（jsonb），重放时原样返回 —— 这样"重试"与"第一次"对客户端
//     完全不可区分。因此**存进去的响应不能含长期凭据**：本项目的 refresh token 走
//     HttpOnly Cookie，不在响应体里，这条约束天然满足。
//  3. **`request_hash` 必须参与判定**：同一个 key 配不同的请求体是**客户端 bug**
//     （或有人拿别人的 key 猜），必须拒绝而不是返回旧结果 —— 否则"用同一个 key
//     改了用户名再提交"会拿到第一次的响应，用户以为改了其实没改。
//  4. **先占位再执行**（Reserve）：占位成功才执行真正的工作，执行完再落响应。
//     反过来（先执行再写键）在并发下会让两个请求都执行一遍。

// ErrIdempotencyKeyConflict 表示同一个 key 被用于了不同的请求体。
var ErrIdempotencyKeyConflict = errors.New("store: 幂等键已用于另一个不同的请求")

// ErrIdempotencyKeyInFlight 表示同一个 key 的首次请求还在执行中。
//
// 调用方应把它翻译成 409/重试提示，而**不是**当成失败吞掉：那会让用户
// 以为操作没生效而再点一次。
var ErrIdempotencyKeyInFlight = errors.New("store: 幂等键对应的请求正在处理中")

// IdempotencyRecord 是一条幂等记录。
type IdempotencyRecord struct {
	Scope       string          `gorm:"column:scope;primaryKey"`
	Key         string          `gorm:"column:key;primaryKey"`
	RequestHash string          `gorm:"column:request_hash"`
	Status      int             `gorm:"column:status"`
	Response    json.RawMessage `gorm:"column:response"`
	CreatedAt   time.Time       `gorm:"column:created_at"`
	ExpiresAt   time.Time       `gorm:"column:expires_at"`
}

// TableName 见 User.TableName 的说明。
func (IdempotencyRecord) TableName() string { return tableIdempotencyKeys }

// ReserveIdempotencyKey 尝试占住一个幂等键。
//
// 返回 (existing, nil) 表示**已经完成过**：existing 是首次的响应，调用方直接返回它。
// 返回 (nil, nil) 表示占位成功：调用方应当执行工作，然后调 CompleteIdempotencyKey。
// 返回 (nil, ErrIdempotencyKeyInFlight) 表示同键请求正在执行。
// 返回 (nil, ErrIdempotencyKeyConflict) 表示同键但请求体不同。
//
// 实现用 INSERT ... ON CONFLICT DO NOTHING 而不是"先查再插"：后者在并发下
// 两个请求都会读到"不存在"，然后都去执行工作。
func (s *Store) ReserveIdempotencyKey(
	ctx context.Context,
	scope, key, requestHash string,
	ttl time.Duration,
) (*IdempotencyRecord, error) {
	if scope == "" || key == "" {
		return nil, errors.New("store: 幂等键缺少 scope 或 key")
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	hash, err := canonicalTokenHash(requestHash)
	if err != nil {
		return nil, err
	}

	// 占位行：status=0 + response='{}' 表示"已占住但还没结果"。
	res := s.db.WithContext(ctx).Exec(
		`INSERT INTO idempotency_keys (scope, key, request_hash, status, response, created_at, expires_at)
		 VALUES (?, ?, ?, 0, '{}'::jsonb, now(), now() + make_interval(secs => ?))
		 ON CONFLICT (scope, key) DO NOTHING`,
		scope, key, hash, int(ttl.Seconds()))
	if res.Error != nil {
		return nil, fmt.Errorf("store: 占用幂等键失败: %w", res.Error)
	}

	// 无论占位成功与否都读回来：占位成功时它就是刚写的占位行。
	var rec IdempotencyRecord
	find := s.db.WithContext(ctx).
		Where("scope = ? AND key = ?", scope, key).
		Limit(1).Find(&rec)
	if find.Error != nil {
		return nil, fmt.Errorf("store: 读取幂等键失败: %w", find.Error)
	}
	if rec.Key == "" {
		// 理论上不可达（刚插入或已存在），保守地当成"在执行"。
		return nil, ErrIdempotencyKeyInFlight
	}

	if rec.RequestHash != hash {
		return nil, ErrIdempotencyKeyConflict
	}
	if rec.Status == 0 {
		if res.RowsAffected == 1 {
			// 我们刚占住它，可以执行。
			return nil, nil
		}
		// 别人占着。
		return nil, ErrIdempotencyKeyInFlight
	}
	return &rec, nil
}

// CompleteIdempotencyKey 落首次请求的最终响应。
//
// `status` 是 HTTP 状态码；`response` 是**可以安全重放的**响应体。
// 只有成功路径需要调它 —— 失败不落键，让客户端能用同一个 key 重试
// （失败往往正是"值得重试"的那一类：网络抖、邮件服务暂时不可用）。
func (s *Store) CompleteIdempotencyKey(
	ctx context.Context,
	scope, key string,
	status int,
	response []byte,
) error {
	if len(response) == 0 {
		response = []byte("{}")
	}
	res := s.db.WithContext(ctx).Exec(
		`UPDATE idempotency_keys
		    SET status = ?, response = ?::jsonb
		  WHERE scope = ? AND key = ? AND status = 0`,
		status, string(response), scope, key)
	if res.Error != nil {
		return fmt.Errorf("store: 落幂等键响应失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		// 键不存在（被清理了）或已经完成（并发完成）：两者都不该让业务失败。
		// 业务本身已经成功，这里只是"少了一次重放保护"。
		return nil
	}
	return nil
}

// ReleaseIdempotencyKey 释放一个占位（工作失败时调用）。
//
// 释放而不是留着占位行：失败的请求必须能用同一个 key 重试，
// 否则用户重试时只会看到"正在处理中"，永远拿不到结果。
func (s *Store) ReleaseIdempotencyKey(ctx context.Context, scope, key string) error {
	res := s.db.WithContext(ctx).Exec(
		`DELETE FROM idempotency_keys WHERE scope = ? AND key = ? AND status = 0`,
		scope, key)
	if res.Error != nil {
		return fmt.Errorf("store: 释放幂等键失败: %w", res.Error)
	}
	return nil
}

// DeleteExpiredIdempotencyKeys 清理过期幂等键（清理协程用）。
func (s *Store) DeleteExpiredIdempotencyKeys(ctx context.Context, before time.Time) (int64, error) {
	if before.IsZero() {
		return 0, errors.New("store: DeleteExpiredIdempotencyKeys 需要一个明确的时间基准")
	}
	res := s.db.WithContext(ctx).Where("expires_at < ?", before).Delete(&IdempotencyRecord{})
	if res.Error != nil {
		return 0, fmt.Errorf("store: 清理过期幂等键失败: %w", res.Error)
	}
	return res.RowsAffected, nil
}
