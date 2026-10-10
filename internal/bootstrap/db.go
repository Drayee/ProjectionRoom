package bootstrap

import (
	"context"
	"errors"
	"log"
	"time"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/store"
)

// —— 数据库资源（连接池 + 事件写入器）与装配期的哨兵/常量 ——
//
// 本文件是装配链的最底层：另外三个装配文件（auth.go / admin.go / engine.go）
// 都消费这里的 *DBResources。package 级的三条装配哨兵与装配常量也集中在这里，
// 因为它们是"整条装配链"共用的判据与预算，不属于任何单一职责。

// 装配期的两类"硬错误"：都表示"配置说账号开启，但装配链断了"，
// 必须拒绝启动（症状若是"所有登录 500"，没人能从日志里看出是装配问题）。
var (
	errConfigMissing         = errors.New("cmd: 装配缺少配置")
	errAccountDepsIncomplete = errors.New("cmd: 账号能力已启用，但 store/signer/ticket 未能装配")
	errAccountStoreNil       = errors.New("cmd: 账号存储适配器为空")
)

// —— 账号层（ACCOUNTS §5/§9）的装配常量。
const (
	// accountOpenTimeout 是启动期连库 + 迁移的总预算。
	// 超时即拒绝启动：带着"连不上库"的账号层跑起来，症状会是"所有登录 500"，
	// 比启动失败难查得多（这也是 config 里 DBDSN 非空即硬校验密钥的同一条理由）。
	accountOpenTimeout = 30 * time.Second

	// writerGracefulClose 是退出时给写队列的 flush 时间（§9 的事件类不可丢）。
	writerGracefulClose = 5 * time.Second

	// writerQueueSize 是事件写队列长度。1024 的依据：账号层的事件是"登录心跳 +
	// 审计"量级（每人每分钟个位数），1024 能吸收一次数据库抖动而不丢事件。
	writerQueueSize = 1024
)

// DBResources 是"连接池 + 事件写入器"这一对必须一起、且按固定顺序关闭的资源。
//
// 为什么要显式配对：writer 必须先 flush 再关连接池（顺序反了写协程只会在一个
// 已关闭的池上重试），而 wire 的 cleanup 执行顺序是**生成代码里的隐式约定**，
// 读 ProviderSet 看不出来。把顺序写死在一个 return 里，比依赖那个隐式顺序可靠。
//
// 它被导出是因为 wire 的注入代码生成在 cmd 包（cross-package provider 的
// 签名必须是导出标识符）。
type DBResources struct {
	Store  *store.Store
	Writer *store.Writer
}

// NewDBResources 打开连接、跑迁移、起事件写入器，并把它们的关闭顺序固定下来。
func NewDBResources(cfg *config.Config) (*DBResources, func(), error) {
	if cfg == nil {
		return nil, nil, errConfigMissing
	}
	// PR_DB_DSN 为空 = 账号能力关闭（§5）。此时**不连库、不迁移**，
	// 服务照常以纯游客模式启动 —— 这是 T1 明确要求的渐进上线路径。
	if cfg.Auth.DBDSN == "" {
		log.Printf("[WARN] 未配置 PR_DB_DSN：账号能力已关闭（观众仍可按房间码进房；建房需要账号）")
		return &DBResources{}, func() {}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), accountOpenTimeout)
	defer cancel()

	st, err := store.Open(ctx, cfg.Auth.DBDSN)
	if err != nil {
		return nil, nil, err
	}
	if _, err := st.Migrate(ctx); err != nil {
		_ = st.Close()
		return nil, nil, err
	}
	log.Printf("账号能力已启用：数据库已连接、迁移已补齐")

	w := store.NewWriter(st.DB(), store.WriterOptions{
		QueueSize: writerQueueSize,
		// 事件类不可丢（§9）：给足重试次数，让一次数据库抖动不至于变成数据丢失。
		MaxRetries: 5,
		OnError:    func(err error) { log.Printf("[WARN] %v", err) },
	})

	cleanup := func() {
		if err := w.Close(writerGracefulClose); err != nil {
			log.Printf("[WARN] %v", err)
		}
		if err := st.Close(); err != nil {
			log.Printf("[WARN] 关闭数据库连接池失败：%v", err)
		}
	}
	return &DBResources{Store: st, Writer: w}, cleanup, nil
}
