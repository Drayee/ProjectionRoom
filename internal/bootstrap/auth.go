package bootstrap

import (
	"fmt"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/service"
	"ProjectionRoom/internal/service/auth"
)

// —— 账号能力的装配（存储适配器 / 签发器 / 票据表 / 账号用例）——
//
// 这一组是账号层（ACCOUNTS §5/§9）的四条装配边，全部围绕"账号能力是否开启"
// （cfg.Auth.DBDSN 是否为空）决定是否产出实例。

// NewAccountStore 构造账号用例的存储适配器（handler 与 service 都不 import gorm，
// 翻译只发生在这一层）。
func NewAccountStore(res *DBResources) (*service.AccountStoreAdapter, error) {
	if res == nil || res.Store == nil {
		// 账号能力关闭（没有库）时返回 nil：可选依赖，由 NewAccountService 判空。
		return nil, nil
	}
	return service.NewAccountStoreAdapter(res.Store, res.Writer)
}

// NewSigner 构造 access token 签发器。
//
// 账号能力关闭时返回 nil（不发 access token 也就没有密钥可信）。
// 密钥缺失/过短时 auth.NewSigner 自己会报错，且 config.Load 已经先拦了一道 ——
// 两道都保留：config 负责"配置层面明确"，这里负责"装配层面不可绕过"。
func NewSigner(cfg *config.Config) (*auth.Signer, error) {
	if cfg == nil || cfg.Auth.DBDSN == "" {
		return nil, nil
	}
	return auth.NewSigner(cfg.Auth.JWTSecret, cfg.Auth.AccessTTL)
}

// NewTicketStore 构造 WS 一次性票据表（账号能力关闭时返回 nil）。
func NewTicketStore(cfg *config.Config) *auth.TicketStore {
	if cfg == nil || cfg.Auth.DBDSN == "" {
		return nil
	}
	return auth.NewTicketStore(cfg.Auth.WSTicketTTL)
}

// NewAccountService 构造账号用例（账号能力关闭时返回 nil）。
func NewAccountService(
	cfg *config.Config,
	st *service.AccountStoreAdapter,
	signer *auth.Signer,
	tickets *auth.TicketStore,
) (*service.AccountService, error) {
	if cfg == nil {
		return nil, errConfigMissing
	}
	if cfg.Auth.DBDSN == "" {
		return nil, nil
	}
	if st == nil || signer == nil || tickets == nil {
		// config 说"账号开启"但某条依赖没建出来 —— 装配 bug，必须吵。
		return nil, fmt.Errorf("%w（适配器=%v 签发器=%v 票据表=%v）",
			errAccountDepsIncomplete, st != nil, signer != nil, tickets != nil)
	}
	return service.NewAccountService(st, signer, tickets, service.AccountOptions{
		BcryptCost: cfg.Auth.BcryptCost,
		RefreshTTL: cfg.Auth.RefreshTTL,
		// 并发口令哈希闸门的容量：取舍见 service.DefaultHashingConcurrency 的说明。
		// 走配置（PR_AUTH_HASH_CONCURRENCY）而不是写死默认值：服务器是 2 vCPU，
		// 而单次 bcrypt(cost=12) 实测约 0.57s —— 容量该多大取决于机器能承受多少
		// 并发的 CPU 密集校验，这是部署事实而不是代码事实。
		HashingConcurrency: cfg.Auth.HashingConcurrency,
	})
}
