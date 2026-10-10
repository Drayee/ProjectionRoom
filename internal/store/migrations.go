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

	{version: "0006_users_email_unique", statements: []string{
		// email_verified_at 是"这个邮箱的归属已被证明"的唯一判据（NULL = 未验证）。
		//
		// 为什么必须有它：找回密码只会把信发到**已验证**的邮箱上。没有这一列时，
		// 任何人在注册时填上别人的邮箱，就能把那个人的找回入口变成自己的
		//（信发到对方邮箱、token 却属于攻击者的账号）。见 service.Forgot 的门控。
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS email_verified_at timestamptz NULL`,
		// 一个邮箱只能属于一个账号 —— 否则"按邮箱找回"会命中两行，"给谁改密"没有唯一答案。
		//
		// 三个设计点：
		//  1. **部分**唯一索引（WHERE email IS NOT NULL）：email 可空（老账号全是 NULL），
		//     多个 NULL 语义上是"都没有邮箱"，不该互相冲突；
		//  2. **lower(email)**：大小写不同的同一邮箱视为同一个（与 username 全小写存储同口径）；
		//  3. 用 CREATE UNIQUE INDEX 而不是 ALTER TABLE ADD CONSTRAINT：迁移规则第 2 条 ——
		//     后者不能重复执行。
		// 代价：DDL 层因此没有 CHECK (email = lower(email)) 兜底，归一化由
		// service.CanonicalEmail 与 store.SetUserEmail 承担；查询侧一律写
		// lower(email) = ?，所以即便库里出现脏值也不会漏匹配（也不会重复命中：索引在 lower 上）。
		//
		// ⚠ 若既有的非 NULL email 里有重复值，这一步会**失败并让启动失败** —— 这是刻意的
		// （要吵，不要静默跳过）。上线前可先跑：
		//   SELECT lower(email), count(*) FROM users WHERE email IS NOT NULL GROUP BY 1 HAVING count(*) > 1;
		`CREATE UNIQUE INDEX IF NOT EXISTS users_email_lower_key ON users (lower(email)) WHERE email IS NOT NULL`,
	}},

	{version: "0007_password_resets", statements: []string{
		// 一次性邮件令牌（purpose 区分两种用途）。
		//
		// 表名沿用批准时的 password_resets，但它同时承载 email_verify ——
		// 两者的**生命周期语义逐字相同**（32 字节随机、库里只存 sha256、单次使用、
		// 到期即废），差别只有 TTL 与消费后的动作。合成一张表让"单次使用"只有一份实现，
		// 也避免了第二张表迟早出现的"某一张忘了加 used_at"。
		//
		// purpose 的 CHECK 写在 CREATE TABLE 里（迁移规则第 2 条：约束必须能重复执行）。
		`CREATE TABLE IF NOT EXISTS password_resets (
	id         bigserial PRIMARY KEY,
	user_id    bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	purpose    varchar(16) NOT NULL
		CHECK (purpose IN ('email_verify', 'password_reset')),
	token_hash char(64) NOT NULL UNIQUE,
	email      varchar(254) NULL,
	ip         varchar(45) NULL,
	user_agent varchar(255) NULL,
	created_at timestamptz NOT NULL DEFAULT now(),
	expires_at timestamptz NOT NULL,
	used_at    timestamptz NULL
)`,
		// 说明：token_hash 只存 sha256 十六进制（照一期 sessions 的做法），
		// 原文只在下发那一次存在（响应里永不出现、日志里永不打印）。
		// user_id NOT NULL：这一行永远属于某个账号 —— 未注册或未验证的邮箱
		// **不会**产生行（对外仍是恒定 200，见 service.Forgot）。

		// 按用户撤销/自查（将来"重发验证邮件"与排障都要按 user 查）。
		`CREATE INDEX IF NOT EXISTS password_resets_user_id_idx ON password_resets (user_id)`,
		// 过期清理协程按 expires_at 扫，走这个索引（照 sessions_expires_at_idx 的先例）。
		`CREATE INDEX IF NOT EXISTS password_resets_expires_at_idx ON password_resets (expires_at)`,
	}},
}
