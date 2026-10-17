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

	// 0008：邮箱验证码（OTP）+ 注册两段式 + 写操作幂等键。
	//
	// 三件事合成一步，因为它们互相依赖、且都只做加法：
	//
	//  ① **OTP 复用 password_resets 表**（新增 purpose `email_otp`）。
	//     理由与 0007 把 email_verify 塞进同一张表逐字相同：生命周期语义一样
	//     （随机串、库里只存 sha256、单次使用、到期即废），合成一张表让"单次使用"
	//     只有一份实现。OTP 只是**短、数字、会输错**的令牌，额外需要 `attempts`
	//     计数（见 ②）—— 它放在这张表上比另立一张表更省一次事务。
	//
	//  ② `attempts`：错误尝试计数。**必须落库**而不是放内存：
	//     放内存的话，攻击者重启进程（或换一个实例）就重置了计数，6 位数字的
	//     爆破空间只有 100 万，去掉内存计数等于把它变成可爆破的。
	//
	//  ③ **注册草稿表**（`register_drafts`）：两段式注册的中间态。
	//     第一段（username+password）校验通过后写一行，返回一个不透明 `ticket`；
	//     第二段凭 ticket + 邮箱验证码完成建号。草稿有 TTL，且**不含明文口令**
	//     （存的是 bcrypt 哈希 —— 它本来就要落进 users，提前算好可以省掉第二段的一次 bcrypt）。
	//
	//  ④ **幂等键表**（`idempotency_keys`）：写操作的重放保护。
	//     同一个键 + 同一个操作重放时直接返回首次的结果，不重复建号、不重复发信。
	//     键的作用域是 `(scope, key)`：scope 区分端点（`register.start` /
	//     `register.verify` / `otp.send`），key 是客户端生成的 UUID。
	{version: "0008_otp_and_drafts", statements: []string{
		// ① purpose 的 CHECK 要扩容：DROP + ADD 是"改约束"而非"删数据"，
		//    但它**不可重复执行**（第二次 DROP 会报"不存在"）。
		//    因此用 DO 块先探测再改，保证这一步幂等（迁移规则第 2 条）。
		`DO $$
DECLARE
  conname_found text;
BEGIN
  SELECT con.conname INTO conname_found
    FROM pg_constraint con
    JOIN pg_class rel ON rel.oid = con.conrelid
   WHERE rel.relname = 'password_resets'
     AND con.contype = 'c'
     AND pg_get_constraintdef(con.oid) LIKE '%email_verify%';
  IF conname_found IS NOT NULL THEN
    EXECUTE format('ALTER TABLE password_resets DROP CONSTRAINT %I', conname_found);
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint con
      JOIN pg_class rel ON rel.oid = con.conrelid
     WHERE rel.relname = 'password_resets'
       AND con.contype = 'c'
       AND pg_get_constraintdef(con.oid) LIKE '%email_otp%'
  ) THEN
    ALTER TABLE password_resets ADD CONSTRAINT password_resets_purpose_check
      CHECK (purpose IN ('email_verify', 'password_reset', 'email_otp'));
  END IF;
END $$`,

		// ② 错误尝试计数（默认 0；老行自动获得默认值）。
		`ALTER TABLE password_resets ADD COLUMN IF NOT EXISTS attempts integer NOT NULL DEFAULT 0`,

		// 说明：**不**把 user_id 改成可空。迁移规则第 1 条（只做加法）与
		// schema 契约测试（password_resets.user_id NOT NULL）都钉住了这一点，
		// 而它们是对的：这张表的每一行都必须属于某个账号，否则"按用户撤销令牌"
		// 会漏掉一批无主的行。
		//
		// 因此注册两段式里"账号还没建"的那一步，验证码行挂在**草稿**上而不是这里 ——
		// 见下面的 register_drafts.email_otp_hash：草稿本身就是"待建账号"的载体，
		// 它的验证码跟着草稿走，账号建好后草稿即被消费。好处是这张表的
		// "单次使用 + 按用户归属"两条语义完全不用变。

		// OTP 按 (user_id, purpose) 查"最近一条"来判冷却与作废旧码。
		`CREATE INDEX IF NOT EXISTS password_resets_otp_lookup_idx
		   ON password_resets (user_id, purpose, created_at DESC)`,

		// ③ 注册草稿。
		//
		// `ticket_hash` 只存 sha256（与 token_hash 同一条纪律：原文只在下发那一次存在）。
		// `password_hash` 是**已经算好的 bcrypt**：第二段建号时直接搬进 users，
		// 于是整个注册流程只跑一次 bcrypt（那是最贵的一步）。
		//
		// `consumed_at` 而不是删除行：消费后保留一小段时间，让"同 ticket 重放"
		// 能给出确定的错误（"这次注册已完成"），而不是含糊的"ticket 无效"。
		`CREATE TABLE IF NOT EXISTS register_drafts (
	id            bigserial PRIMARY KEY,
	ticket_hash   char(64) NOT NULL UNIQUE,
	username      varchar(20) NOT NULL,
	display_name  varchar(64) NOT NULL,
	password_hash varchar(255) NOT NULL,
	email         varchar(254) NULL,
	email_otp_hash char(64) NULL,
	ip            varchar(45) NULL,
	user_agent    varchar(255) NULL,
	attempts      integer NOT NULL DEFAULT 0,
	created_at    timestamptz NOT NULL DEFAULT now(),
	expires_at    timestamptz NOT NULL,
	consumed_at   timestamptz NULL
)`,
		`CREATE INDEX IF NOT EXISTS register_drafts_expires_at_idx ON register_drafts (expires_at)`,
		// ⚠ 上面两条 CREATE 只对"新库"生效：`CREATE TABLE IF NOT EXISTS` 在表已存在时
		// **什么都不做**，包括不会补列。因此凡是在建表语句之后又加过的列，都必须在这里
		// 再写一条 ADD COLUMN IF NOT EXISTS —— 否则"先跑过一次旧版迁移的库"会永远缺列，
		// 而缺列只会在运行期报错（这个坑真的踩过一次：email_otp_hash 缺失，
		// 表结构测试先抓到了它）。
		`ALTER TABLE register_drafts ADD COLUMN IF NOT EXISTS email varchar(254) NULL`,
		`ALTER TABLE register_drafts ADD COLUMN IF NOT EXISTS email_otp_hash char(64) NULL`,
		`ALTER TABLE register_drafts ADD COLUMN IF NOT EXISTS attempts integer NOT NULL DEFAULT 0`,
		// 同一用户名不并发起草两份：唯一索引挡不住（草稿会过期），
		// 所以这里只做普通索引，真正的唯一性仍由 users.username 保证。
		`CREATE INDEX IF NOT EXISTS register_drafts_username_idx ON register_drafts (username)`,

		// ④ 幂等键。
		//
		// 键是 `(scope, key)`：scope 是端点名，key 是客户端生成的 UUID。
		// `response` 存首次成功的响应体（jsonb），重放时原样返回 —— 这样"重试"
		// 与"第一次"对客户端完全不可区分，包括 ticket / 会话这类一次性内容。
		//
		// ⚠ 存响应体意味着**它不能含长期凭据**：注册两段式的响应里只有 ticket
		//（草稿凭据，本身有 TTL 且单次消费）与用户档案，没有 refresh token
		//（那个只走 HttpOnly Cookie，重放时无法也无需重发）。
		`CREATE TABLE IF NOT EXISTS idempotency_keys (
	scope       varchar(32) NOT NULL,
	key         varchar(64) NOT NULL,
	request_hash char(64) NOT NULL,
	status      integer NOT NULL,
	response    jsonb NOT NULL,
	created_at  timestamptz NOT NULL DEFAULT now(),
	expires_at  timestamptz NOT NULL,
	PRIMARY KEY (scope, key)
)`,
		`CREATE INDEX IF NOT EXISTS idempotency_keys_expires_at_idx ON idempotency_keys (expires_at)`,
	}},

	// 0009：番剧目录（anime）。wayfinder #37，`/forum` 的数据底座。
	//
	// # 为什么 aliases 是 jsonb 而不是子表
	//
	// 别名（原名 / 简称 / 民间译名）只被两处用到：列表展示，以及 `?q=` 的模糊匹配。
	// 它没有独立的生命周期（不会单独增删、不被别的实体引用），拆成 `anime_aliases`
	// 子表会为每次列表查询引入一次 JOIN，却换不到任何查询能力。
	// 存成 jsonb 数组则一次写入、一次读出，语义上就是「这个条目的若干个名字」。
	//
	// # title 为什么唯一
	//
	// 目录的价值在于"一个番剧只有一条"。允许重名，`?q=` 会返回两行看起来一样的条目，
	// 而主题串（#38）只会挂在其中一条上 —— 用户无从判断该点哪个。
	// 唯一约束把这件事挡在数据库层面：重复创建返回 409，而不是悄悄多一条。
	//
	// # 为什么没有 is_active / 软删除
	//
	// 本票的 DELETE 是硬删。软删除需要"已经挂了这个番剧的主题串怎么办"的答案，
	// 而主题串（#38）还没落地 —— 现在加 `deleted_at` 只会让那个问题被提前假装解决。
	{version: "0009_anime", statements: []string{
		`CREATE TABLE IF NOT EXISTS anime (
	id         bigserial PRIMARY KEY,
	title      varchar(120) NOT NULL UNIQUE,
	aliases    jsonb NOT NULL DEFAULT '[]'::jsonb,
	cover_url  varchar(512) NULL,
	summary    text NOT NULL DEFAULT '',
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now()
)`,
		// 搜索是 `title ILIKE '%q%'` 与 `created_at DESC` 的组合。
		// lower(title) 的普通 B-tree 服务不了 ILIKE '%x%'，但它能让"按标题等值/前缀"
		// 这类将来会有的查询走上索引；模糊匹配靠表大小兜着 —— 目录是人工维护的小表
		// （几十到几千行），不为它引入 pg_trgm 扩展。
		`CREATE INDEX IF NOT EXISTS anime_title_lower_idx ON anime (lower(title))`,
		`CREATE INDEX IF NOT EXISTS anime_created_at_idx ON anime (created_at DESC)`,
	}},

	// 0010：帖子（posts / post_comments / post_likes）。wayfinder #27。
	//
	// # 为什么 like_count / comment_count 是**反范式列**而不是查询时聚合
	//
	// 列表页一次出 20 行，每行都要显示点赞数与评论数。若查询时聚合，
	// 要么每行一个子查询（20 次额外扫描），要么 LEFT JOIN + GROUP BY
	//（把 limit 20 的简单索引扫描变成"先扫全量再聚合"）。两者在列表变长后
	// 都会明显变慢，而这两个数在页面上只是"参考量级"。
	//
	// 反范式的代价是**必须原子维护**：点赞/评论的插入与计数的增减放在**同一个事务**里
	//（见 store/posts.go 的 TogglePostLike / AddPostComment / DeletePostComment）。
	// 分开写会出现"纪录有了但计数没加"的永久漂移，而且没有对账路径能发现它。
	//
	// # 三张表的约束
	//
	//   - `post_likes` 主键是 (post_id, user_id)：一个用户对一篇帖子只能有一条点赞，
	//     "重复点赞"在数据库层面就不可能 —— toggle 的实现因此可以靠
	//     `INSERT … ON CONFLICT DO NOTHING` 的 RowsAffected 判断方向，不必先查。
	//   - `images` 用 jsonb 存 URL 数组（前端消费的形状就是 string[]）。
	//     **只存外链**：服务端不做图片上传（本票 out）。
	//   - `content` 用 text：正文上限由应用层管（清洗口径 §7.3），不靠 varchar
	//     截断 —— 截断会静默改用户内容，比拒绝更糟。
	//   - `anime_id` 本期**不填**，只预留：番剧板块票（#38）会用到它。
	//     现在加一列是加法，将来不必再来一次迁移。
	//   - 所有外键 ON DELETE CASCADE：删号删帖、删帖删评论/点赞。
	//     账号注销路径（#29 之后）因此不会留下孤儿子行。
	{version: "0010_posts", statements: []string{
		`CREATE TABLE IF NOT EXISTS posts (
	id            bigserial PRIMARY KEY,
	author_id     bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	title         varchar(60) NOT NULL,
	content       text NOT NULL,
	images        jsonb NOT NULL DEFAULT '[]'::jsonb,
	like_count    integer NOT NULL DEFAULT 0 CHECK (like_count >= 0),
	comment_count integer NOT NULL DEFAULT 0 CHECK (comment_count >= 0),
	anime_id      bigint NULL,
	created_at    timestamptz NOT NULL DEFAULT now(),
	updated_at    timestamptz NOT NULL DEFAULT now()
)`,
		// 列表默认按最新：这个索引让 "ORDER BY created_at DESC LIMIT n" 直接走索引。
		`CREATE INDEX IF NOT EXISTS posts_created_at_idx ON posts (created_at DESC)`,
		// 「热门」的排序键。表达式索引（like_count + comment_count）让排序不必回表计算；
		// 第二键 created_at 保证同分时顺序稳定（否则翻页会出现重复/漏行）。
		`CREATE INDEX IF NOT EXISTS posts_hot_idx ON posts ((like_count + comment_count) DESC, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS posts_author_idx ON posts (author_id)`,

		`CREATE TABLE IF NOT EXISTS post_comments (
	id         bigserial PRIMARY KEY,
	post_id    bigint NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
	author_id  bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	content    text NOT NULL,
	created_at timestamptz NOT NULL DEFAULT now()
)`,
		// 评论按 (post_id, created_at) 取：一篇文章的评论列表是唯一的热路径。
		// 升序（老→新）与聊天界面一致；分页靠 (post_id, created_at, id) 稳定排序。
		`CREATE INDEX IF NOT EXISTS post_comments_post_idx ON post_comments (post_id, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS post_comments_author_idx ON post_comments (author_id)`,

		`CREATE TABLE IF NOT EXISTS post_likes (
	post_id    bigint NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
	user_id    bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (post_id, user_id)
)`,
		// 反查"我赞过哪些"（将来"我的点赞"要用），也服务 ON DELETE CASCADE 的清理。
		`CREATE INDEX IF NOT EXISTS post_likes_user_idx ON post_likes (user_id)`,
	}},
}
