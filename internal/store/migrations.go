package store

// migration 是一步迁移：版本号 + 一段按序执行的 DDL。
//
// 四条规则（ACCOUNTS §4「迁移」）：
//  1. 只做加法：不 DROP、不改既有列类型、不删约束；
//  2. 幂等：DDL 一律 IF NOT EXISTS，且**约束全部写在 CREATE TABLE 里** ——
//     `ALTER TABLE ... ADD CONSTRAINT` 不能重复执行，第二次会直接报错；
//  3. 有序：版本号严格递增，Migrate 按切片顺序执行，已应用的版本记进账本表；
//  4. 不接受任何外部输入：DDL 是常量文本，不构成注入面（§4 的说明）。
//
// 注意：DDL 文本里不能出现 '?' —— GORM 的 Exec 会把它当占位符。
type migration struct {
	version    string
	statements []string
}

// schemaMigrationsDDL 是账本表自身的 DDL。
//
// 它同时承担两个角色：第 0001 步（会被记进 schema_migrations），以及 Migrate 的
// 引导语句 —— 没有这张表就读不到"哪些版本已应用"，所以它必须在读账本之前先
// 无条件执行一次。两处共用同一份文本，避免引导语句与迁移步骤各说一套。
var schemaMigrationsDDL = []string{
	`CREATE TABLE IF NOT EXISTS schema_migrations (
	version    varchar(32) PRIMARY KEY,
	applied_at timestamptz NOT NULL DEFAULT now()
)`,
}

// migrations 是全部迁移步骤（本期：一步到位建 5 张表，ACCOUNTS §4）。
//
// 为什么是变量而不是常量：测试会临时替换它来验证"某一步失败则整体不落账"。
// 生产路径只有 Migrate 读它，且 Migrate 开头会校验版本号严格递增。
//
// 本期**不建** friendships / messages / room_snapshots（二期、三期各自立步骤），
// 也不建任何房间实时状态表（不变量 I2）。
var migrations = []migration{
	{version: "0001_schema_migrations", statements: schemaMigrationsDDL},

	{version: "0002_users", statements: []string{
		`CREATE TABLE IF NOT EXISTS users (
	id            bigserial PRIMARY KEY,
	username      varchar(20) NOT NULL UNIQUE
		CHECK (username = lower(username)),
	display_name  varchar(32) NOT NULL,
	password_hash varchar(72) NOT NULL,
	email         varchar(254) NULL,
	role          varchar(8) NOT NULL DEFAULT 'user'
		CHECK (role IN ('user', 'admin')),
	status        varchar(8) NOT NULL DEFAULT 'active'
		CHECK (status IN ('active', 'banned')),
	token_version integer NOT NULL DEFAULT 1,
	banned_reason text NULL,
	created_at    timestamptz NOT NULL DEFAULT now(),
	updated_at    timestamptz NOT NULL DEFAULT now(),
	last_seen_at  timestamptz NOT NULL DEFAULT now()
)`,
		// 说明：username 的 CHECK 把"统一小写存储"（§4）钉在数据库侧。
		// 应用层已经归一化（见 models.go 的 canonicalUsername），这里防的是
		// 绕过应用层的写入；不额外写 COMMENT ON TABLE —— 表结构的文字说明
		// 只有一个 owner：docs/ACCOUNTS.md §4。
	}},

	{version: "0003_sessions", statements: []string{
		`CREATE TABLE IF NOT EXISTS sessions (
	id         bigserial PRIMARY KEY,
	user_id    bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	token_hash char(64) NOT NULL UNIQUE,
	user_agent varchar(255) NULL,
	ip         varchar(45) NULL,
	issued_at  timestamptz NOT NULL DEFAULT now(),
	expires_at timestamptz NOT NULL,
	revoked_at timestamptz NULL
)`,
		// 按用户撤销全部会话（封禁/登出全部设备）走这个索引。
		`CREATE INDEX IF NOT EXISTS sessions_user_id_idx ON sessions (user_id)`,
		// 过期清理协程按 expires_at 扫，走这个索引。
		`CREATE INDEX IF NOT EXISTS sessions_expires_at_idx ON sessions (expires_at)`,
	}},

	{version: "0004_rooms_meta", statements: []string{
		`CREATE TABLE IF NOT EXISTS rooms_meta (
	room_id       varchar(12) PRIMARY KEY,
	owner_user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	title         varchar(60) NOT NULL DEFAULT '',
	is_public     boolean NOT NULL DEFAULT false,
	has_password  boolean NOT NULL DEFAULT false,
	created_at    timestamptz NOT NULL DEFAULT now(),
	last_seen_at  timestamptz NOT NULL DEFAULT now(),
	closed_at     timestamptz NULL
)`,
		// ListPublicRoomIDs 的查询形态：is_public + closed_at IS NULL + last_seen_at DESC。
		`CREATE INDEX IF NOT EXISTS rooms_meta_public_idx ON rooms_meta (is_public, closed_at, last_seen_at DESC)`,
	}},

	{version: "0005_admin_audit", statements: []string{
		// actor_id 刻意不设外键（与其他表的差异见 models.go 的注释）：
		// 审计要活过用户删除。
		`CREATE TABLE IF NOT EXISTS admin_audit (
	id          bigserial PRIMARY KEY,
	actor_id    bigint NOT NULL,
	action      varchar(32) NOT NULL,
	target_type varchar(16) NOT NULL,
	target_id   varchar(64) NOT NULL,
	detail      jsonb NOT NULL DEFAULT '{}'::jsonb,
	ip          varchar(45) NULL,
	created_at  timestamptz NOT NULL DEFAULT now()
)`,
		// 不额外建 created_at 索引：ListAudit 按 id DESC 分页，
		// 主键索引已经能直接服务这个 ORDER BY，多余的索引只是写放大。
	}},
}
