// Package store 是仓库唯一的持久化 owner（ACCOUNTS §3）：连接、模型、迁移与仓储
// 都收敛在这里，上层只看见本包导出的方法与模型。
//
// 三条边界：
//  1. 表结构的真相只有一处 —— migrations.go 里的 DDL（有序、幂等、只做加法）。
//     模型只做列映射，不用 AutoMigrate，也不写 default/not null 这类 tag：
//     同一个约束有两个 owner，迟早会在改一处忘一处时变成线上事故。
//  2. 业务查询一律参数化占位（`Where("username = ?", v)`），任何变量都不进 SQL 文本。
//     迁移 DDL 是常量文本、不接受外部输入，因此不构成注入面（§4 的说明）。
//  3. 写库分两类（§9）：事件类走 Writer（单写协程 + 有界队列 + 指数退避重试），
//     由上层投递、绝不阻塞 WS/REST 处理路径（不变量 I4）。
package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// —— 哨兵错误：上层用 errors.Is 判断，不需要 import gorm 或驱动。
var (
	// ErrNoDSN 表示没有配置数据库连接串（PR_DB_DSN 为空 = 账号能力关闭）。
	ErrNoDSN = errors.New("store: 未配置 PR_DB_DSN")

	// ErrNotFound 表示按主键/唯一键没查到记录。
	//
	// 为什么要自己的哨兵：调用方不该 import gorm 才能判断"没找到"，
	// 也不该依赖 gorm.ErrRecordNotFound 这个实现细节（换驱动就崩）。
	ErrNotFound = errors.New("store: 记录不存在")

	// ErrUsernameTaken 表示 users.username 唯一约束冲突。
	ErrUsernameTaken = errors.New("store: 用户名已被占用")

	// ErrTokenHashTaken 表示 sessions.token_hash 唯一约束冲突。
	//
	// 它在上层是一个**并发双花**信号：两个并发刷新拿着同一个 refresh token 各写一行，
	// 只有一个能成功（§5 的轮换 + 重放检测）。调用方请用 errors.Is 判断，
	// 不要去匹配错误文案 —— 文案会变，哨兵不会。
	ErrTokenHashTaken = errors.New("store: token_hash 已被占用")
)

// —— 连接池与分页的约定值。
const (
	// pingTimeout 是 Open/Ping 单次等待的上限：启动期连不上要快速失败，
	// 而不是把进程挂在 TCP 超时上。
	pingTimeout = 5 * time.Second

	// 连接池：本部署是单实例 + 一个 SSH 隧道后面的 PostgreSQL，
	// 建连（TCP + 认证 + 隧道）远比查询本身贵，所以连接要复用；
	// ConnMaxLifetime 让隧道重启后的死连接自然淘汰，不用重启进程。
	maxOpenConns    = 10
	maxIdleConns    = 5
	connMaxLifetime = time.Hour
	connMaxIdleTime = 10 * time.Minute

	// 分页：管理端列表的缺省条数与上限（上限防"一次拖全表"）。
	defaultPageLimit = 50
	maxPageLimit     = 200
)

// Store 是持久化入口：持有连接池与（可选挂载的）事件写入器。
type Store struct {
	db     *gorm.DB
	writer *Writer

	// mu 保护 writer 的挂载/读取。装配发生在启动期，但读取可能来自
	// 指标接口等多个协程，无锁读写就是数据竞争。
	mu sync.RWMutex
}

// Open 建立连接池并 ping 一次；dsn 为空返回 ErrNoDSN。
//
// 这里刻意不做迁移：迁移由装配层显式调用 Migrate，让"连接 → 迁移 → 起服务"
// 这个顺序在启动代码里可见、可测、可中断。
func Open(ctx context.Context, dsn string) (*Store, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, ErrNoDSN
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		// 首次连接的超时与 ctx 由我们自己控制（gorm 的自动 ping 用 Background，
		// 会让 context 形同虚设）。
		DisableAutomaticPing: true,
		// 本层每个方法都是一条语句，不需要 GORM 再包一层事务（省 2 次往返）。
		SkipDefaultTransaction: true,
		// 唯一/外键/CHECK 冲突翻译成 gorm 哨兵错误（见 isUniqueViolation）。
		TranslateError: true,
		// 静默 GORM 自带日志：它会把 SQL 与**参数值**（含 password_hash）打进
		// 服务端日志，而本层错误一律通过返回值上抛，由调用方决定记什么。
		Logger: gormlogger.Discard,
	})
	if err != nil {
		return nil, fmt.Errorf("store: 打开数据库失败: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("store: 取连接池失败: %w", err)
	}
	sqlDB.SetMaxOpenConns(maxOpenConns)
	sqlDB.SetMaxIdleConns(maxIdleConns)
	sqlDB.SetConnMaxLifetime(connMaxLifetime)
	sqlDB.SetConnMaxIdleTime(connMaxIdleTime)

	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("store: 连接数据库失败: %w", err)
	}

	return &Store{db: db}, nil
}

// DB 暴露底层 *gorm.DB，供本包测试与装配层诊断使用。
// 业务代码请用本包的方法：它们才是参数化写法与 schema 约定的落点。
func (s *Store) DB() *gorm.DB { return s.db }

// Ping 探活（启动自检与健康检查用）。
func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("store: Store 未初始化")
	}
	sqlDB, err := s.db.DB()
	if err != nil {
		return fmt.Errorf("store: 取连接池失败: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		return fmt.Errorf("store: ping 数据库失败: %w", err)
	}
	return nil
}

// Close 关闭连接池。
//
// 它**不**代为关闭事件写入器：Writer.Close 需要 timeout 参数，语义不同，
// 在这里猜一个超时会把"没 flush 完"变成静默丢事件。装配顺序必须是：
// 先 w.Close(timeout)，再 s.Close()；否则写协程会在已关闭的连接池上重试。
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	sqlDB, err := s.db.DB()
	if err != nil {
		return fmt.Errorf("store: 取连接池失败: %w", err)
	}
	if err := sqlDB.Close(); err != nil {
		return fmt.Errorf("store: 关闭数据库失败: %w", err)
	}
	return nil
}

// AttachWriter 挂上事件写入器（§9 的事件类），让只拿到 *Store 的上层
// （usecase/handler）也能投递事件，而不必各自持有 Writer。
// 传 nil 表示本进程不写事件（账号能力关闭时的形态）。
func (s *Store) AttachWriter(w *Writer) {
	s.mu.Lock()
	s.writer = w
	s.mu.Unlock()
}

// EventWriter 返回已挂载的事件写入器；未挂载返回 nil（调用方需自行判空）。
func (s *Store) EventWriter() *Writer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.writer
}

// clampPage 归一化分页参数：非正 limit 取默认、超上限截断、负 offset 归零。
// 集中一处是为了让"每一页有多少条"这件事只有一个 owner（防某个接口漏了上限）。
func clampPage(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = defaultPageLimit
	}
	if limit > maxPageLimit {
		limit = maxPageLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// wrapDBErr 把 gorm 的"没找到"翻译成本包的 ErrNotFound，其余错误补上操作上下文。
func wrapDBErr(op string, err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return fmt.Errorf("store: %s 失败: %w", op, err)
}

// isUniqueViolation 判断错误是不是唯一约束冲突（PostgreSQL 错误码 23505）。
//
// 两条路径都认：打开 TranslateError 后 gorm 会翻成 ErrDuplicatedKey，而未翻译的
// 原始错误里带着 *pgconn.PgError。两个都判，是为了不依赖"驱动恰好实现了翻译"
// 这个隐含前提 —— 唯一约束冲突是业务语义（用户名占用），判错代价很大。
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
