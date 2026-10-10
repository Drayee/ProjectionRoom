package store

import "time"

// 表名集中一处，避免各文件里出现拼错的字面量。
const (
	tableUsers            = "users"
	tableSessions         = "sessions"
	tableRoomsMeta        = "rooms_meta"
	tableAdminAudit       = "admin_audit"
	tablePasswordResets   = "password_resets"
	tableSchemaMigrations = "schema_migrations"
)

// 角色与状态取值（ACCOUNTS §4）。DDL 里有 CHECK 约束兜底，
// 写入前这里先校验，让非法值在应用层就得到可读的错误。
const (
	RoleUser  = "user"
	RoleAdmin = "admin"

	StatusActive = "active"
	StatusBanned = "banned"
)

// 一次性邮件令牌的用途（password_resets.purpose，DDL 里的 CHECK 与这里一一对应）。
//
// 用途必须进 WHERE 条件：否则一封"验证邮箱"的信可以被拿去当"重置密码"的凭据用
// （两串 token 的生成方式、存储形态、消费路径全都一样，唯一的区分就是它）。
const (
	// PurposeEmailVerify 证明"这个邮箱属于这个账号"。
	PurposeEmailVerify = "email_verify"
	// PurposePasswordReset 授权"改这个账号的口令"。
	PurposePasswordReset = "password_reset"
)

// 列长度上限（与 DDL 的 varchar 长度一致）。用于写入前的快速失败：
// 超过长度时 PostgreSQL 会直接报错（varchar 不做静默截断），
// 提前给出可读错误比让约束报错更好定位。
const (
	maxUsernameLen    = 20
	maxDisplayNameLen = 32
	maxTitleLen       = 60
	// maxEmailLen 与 DDL 的 varchar(254) 一致（RFC 5321 对邮件地址长度上限的规定值）。
	maxEmailLen     = 254
	tokenHashHexLen = 64 // sha256 的十六进制长度 = sessions.token_hash char(64)
)

// User 对应 users 表（ACCOUNTS §4）。
//
// Username 的规范形是「去首尾空白 + 全小写」（§4：服务端统一小写存储）。
// 归一化由 store 在写入与查询两侧统一施加（CreateUser / UserByUsername / ListUsers），
// 这样无论哪个调用方漏了归一化，唯一约束与登录查询都落在同一个规范形上；
// DDL 里还有 CHECK (username = lower(username)) 作为最后一道兜底。
type User struct {
	ID           int64   `gorm:"column:id;primaryKey"`
	Username     string  `gorm:"column:username"`
	DisplayName  string  `gorm:"column:display_name"`
	PasswordHash string  `gorm:"column:password_hash"`
	Email        *string `gorm:"column:email"`
	// EmailVerifiedAt 是邮箱归属被证实的时刻；nil = 未验证。
	//
	// 授权事实：**只有非 nil 的邮箱能用于找回**（见 service.Forgot 的门控）。
	// 它与 Email 分成两列而不是"Email 非空即已验证"，是因为"填了邮箱"与
	// "证明了这个邮箱是他的"是两件事 —— 前者是用户的自述，后者才有授权含义。
	EmailVerifiedAt *time.Time `gorm:"column:email_verified_at"`
	Role            string     `gorm:"column:role"`
	Status          string     `gorm:"column:status"`
	TokenVersion    int        `gorm:"column:token_version"`
	BannedReason    *string    `gorm:"column:banned_reason"`
	CreatedAt       time.Time  `gorm:"column:created_at"`
	UpdatedAt       time.Time  `gorm:"column:updated_at"`
	LastSeenAt      time.Time  `gorm:"column:last_seen_at"`
}

// TableName 固定表名：GORM 默认会把 RoomMeta 复数化成 room_metas，
// 与 ACCOUNTS §4 的表名不一致，所以每张表都显式声明。
func (User) TableName() string { return tableUsers }

// Session 对应 sessions 表：refresh token 的落库形态（§4/§5）。
//
// 库里只存 sha256 十六进制（TokenHash），原始 token 永不落库；
// 轮换 + 重放检测靠 RevokedAt 与"旧行置 revoked"实现。
type Session struct {
	ID        int64      `gorm:"column:id;primaryKey"`
	UserID    int64      `gorm:"column:user_id"`
	TokenHash string     `gorm:"column:token_hash"`
	UserAgent *string    `gorm:"column:user_agent"`
	IP        *string    `gorm:"column:ip"`
	IssuedAt  time.Time  `gorm:"column:issued_at"`
	ExpiresAt time.Time  `gorm:"column:expires_at"`
	RevokedAt *time.Time `gorm:"column:revoked_at"`
}

// TableName 见 User.TableName 的说明。
func (Session) TableName() string { return tableSessions }

// RoomMeta 对应 rooms_meta：房间**元数据**（不变量 I2：实时状态不入库）。
type RoomMeta struct {
	RoomID string `gorm:"column:room_id;primaryKey"`
	// OwnerUserID 是建房者（§6：建房必须登录并绑定房主）。
	OwnerUserID int64      `gorm:"column:owner_user_id"`
	Title       string     `gorm:"column:title"`
	IsPublic    bool       `gorm:"column:is_public"`
	HasPassword bool       `gorm:"column:has_password"`
	CreatedAt   time.Time  `gorm:"column:created_at"`
	LastSeenAt  time.Time  `gorm:"column:last_seen_at"`
	ClosedAt    *time.Time `gorm:"column:closed_at"`
}

// TableName 见 User.TableName 的说明。
func (RoomMeta) TableName() string { return tableRoomsMeta }

// RoomMetaPatch 是 rooms_meta 的局部更新参数：nil 字段 = 不改。
//
// 想清空 closed_at（房间码复用后重新开放）请走 UpsertRoomMeta —— 它会整体覆盖
// closed_at；这里表达不了"置 NULL"，因为 nil 已经被"不改"占用了。
type RoomMetaPatch struct {
	Title       *string
	IsPublic    *bool
	HasPassword *bool
	LastSeenAt  *time.Time
	ClosedAt    *time.Time
}

// AdminAudit 对应 admin_audit：管理端写操作的审计行（§8 的 must 落点）。
//
// 这张表**故意不设** actor_id 外键（与其他表不同）：审计要能活过用户删除，
// 否则"谁删了谁"这条记录会随着删除一起消失。
type AdminAudit struct {
	ID         int64     `gorm:"column:id;primaryKey"`
	ActorID    int64     `gorm:"column:actor_id"`
	Action     string    `gorm:"column:action"`
	TargetType string    `gorm:"column:target_type"`
	TargetID   string    `gorm:"column:target_id"`
	Detail     string    `gorm:"column:detail;type:jsonb"`
	IP         *string   `gorm:"column:ip"`
	CreatedAt  time.Time `gorm:"column:created_at"`
}

// TableName 见 User.TableName 的说明。
func (AdminAudit) TableName() string { return tableAdminAudit }

// PasswordReset 对应 password_resets：一次性邮件令牌（验证邮箱 / 重置密码两种用途）。
//
// **库里只有 TokenHash**（sha256 十六进制）：原文只在下发那一封邮件里存在，
// 与本项目 refresh token 的做法一致（§4）。
//
// 单次使用靠 UsedAt：消费是一条带条件的 UPDATE ... RETURNING（见
// Store.ConsumePasswordReset / ConsumeEmailVerify），"校验"与"标记已用"
// 在同一条语句里完成，因此两个并发请求不可能都通过。
type PasswordReset struct {
	ID        int64      `gorm:"column:id;primaryKey"`
	UserID    int64      `gorm:"column:user_id"`
	Purpose   string     `gorm:"column:purpose"`
	TokenHash string     `gorm:"column:token_hash"`
	Email     *string    `gorm:"column:email"`
	IP        *string    `gorm:"column:ip"`
	UserAgent *string    `gorm:"column:user_agent"`
	CreatedAt time.Time  `gorm:"column:created_at"`
	ExpiresAt time.Time  `gorm:"column:expires_at"`
	UsedAt    *time.Time `gorm:"column:used_at"`
}

// TableName 见 User.TableName 的说明。
func (PasswordReset) TableName() string { return tablePasswordResets }

// UserQuery 是用户列表的查询条件（管理端：分页 + 搜索，§8）。
type UserQuery struct {
	// Search 匹配 username 或 display_name 的子串；空 = 不过滤。
	// 输入里的 % / _ / \ 按字面处理（见 likePattern）。
	Search string
	// Limit <= 0 取默认值（50），> 200 被截断；Offset < 0 视为 0。
	Limit  int
	Offset int
}

// schemaMigration 对应账本表 schema_migrations（version PK + applied_at）。
// 它不导出：外部只需要 Migrate 返回的 MigrateReport。
type schemaMigration struct {
	Version   string    `gorm:"column:version;primaryKey"`
	AppliedAt time.Time `gorm:"column:applied_at"`
}

// TableName 见 User.TableName 的说明。
func (schemaMigration) TableName() string { return tableSchemaMigrations }

// isValidRole / isValidStatus 与 DDL 的 CHECK 约束对应（应用层先给可读错误）。
func isValidRole(role string) bool {
	return role == RoleUser || role == RoleAdmin
}

func isValidStatus(status string) bool {
	return status == StatusActive || status == StatusBanned
}
