package service

import (
	"context"
	"time"

	"ProjectionRoom/internal/store"
)

// 幂等键的**用例层出口**。
//
// 为什么不让 handler 直接用 store：handler 的 import 面被刻意收窄成
// `service` 与 `service/auth` 两个下层（见 router.go 的说明）。为了三个幂等方法
// 把持久化包拉进 HTTP 层，会让"handler 依赖了什么"变得难以一眼看完 ——
// 而这条纪律在本仓库已经因为 `service.StoreNotFound` 这样的别名被维护过一轮。
//
// 这三个方法都只是转发，没有业务逻辑：幂等的**语义**在 store 里（那里有 SQL 与
// 事务），**判定**在 handler 里（那是 HTTP 语义），这里只是通道。

// ReserveIdempotency 尝试占住一个幂等键。
//
// 返回 (非 nil, nil) = 已完成过，直接返回那次的结果；
// 返回 (nil, nil) = 占位成功，去执行；
// 返回 (nil, ErrIdempotencyKeyInFlight / ErrIdempotencyKeyConflict) = 见错误说明。
func (s *AccountService) ReserveIdempotency(
	ctx context.Context, scope, key, requestHash string, ttl time.Duration,
) (*store.IdempotencyRecord, error) {
	return s.store.ReserveIdempotencyKey(ctx, scope, key, requestHash, ttl)
}

// CompleteIdempotency 落首次请求的最终响应（供重放）。
func (s *AccountService) CompleteIdempotency(
	ctx context.Context, scope, key string, status int, response []byte,
) error {
	return s.store.CompleteIdempotencyKey(ctx, scope, key, status, response)
}

// ReleaseIdempotency 释放占位（工作失败时调用，让客户端能用同一个键重试）。
func (s *AccountService) ReleaseIdempotency(ctx context.Context, scope, key string) error {
	return s.store.ReleaseIdempotencyKey(ctx, scope, key)
}

// 幂等键的两个判定错误在用例层的别名。
//
// 与 service.StoreNotFound 同一套理由：handler 需要能判断它们（决定 409 的文案），
// 但不该为此 import store。
var (
	ErrIdempotencyKeyInFlight = store.ErrIdempotencyKeyInFlight
	ErrIdempotencyKeyConflict = store.ErrIdempotencyKeyConflict
)
