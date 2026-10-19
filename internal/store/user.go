package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CreateUser 插入一个用户；username 唯一约束冲突返回 ErrUsernameTaken。
//
// 写入前统一归一化 username（去首尾空白 + 小写），并把 Role/Status/TokenVersion
// 的零值补成与 DDL 默认值一致的取值（见 applyUserDefaults 的说明）。
func (s *Store) CreateUser(ctx context.Context, u *User) error {
	if u == nil {
		return errors.New("store: CreateUser 收到 nil")
	}
	u.Username = canonicalUsername(u.Username)
	if u.Username == "" {
		return errors.New("store: username 不能为空")
	}
	if len([]rune(u.Username)) > maxUsernameLen {
		return fmt.Errorf("store: username 超过 %d 个字符", maxUsernameLen)
	}
	u.DisplayName = strings.TrimSpace(u.DisplayName)
	if u.DisplayName == "" {
		return errors.New("store: display_name 不能为空")
	}
	if len([]rune(u.DisplayName)) > maxDisplayNameLen {
		return fmt.Errorf("store: display_name 超过 %d 个字符", maxDisplayNameLen)
	}
	if u.PasswordHash == "" {
		return errors.New("store: password_hash 不能为空")
	}
	// 邮箱可空：nil 与空串都表示"没有邮箱"。这里只做**落库前的形状**校验
	//（去控制字符、全小写、长度）；"它长得像不像邮箱"是产品规则，属于
	// service.CanonicalEmail（那一层拒绝非法格式，本层不做正则）。
	if u.Email != nil {
		addr := canonicalEmail(*u.Email)
		if addr == "" {
			u.Email = nil
		} else {
			if len(addr) > maxEmailLen {
				return fmt.Errorf("store: email 超过 %d 个字符", maxEmailLen)
			}
			u.Email = &addr
		}
	}
	if u.Email != nil && u.EmailVerifiedAt != nil {
		// 注册是唯一的建号路径，而注册时邮箱必然未验证（要收一封验证信）。
		// 允许"建号即已验证"等于绕开整条归属证明链，所以这里直接拒。
		return errors.New("store: 新建账号不能带 email_verified_at（归属证明要走一次验证邮件）")
	}
	applyUserDefaults(u)

	if err := s.db.WithContext(ctx).Create(u).Error; err != nil {
		if isUniqueViolation(err) {
			// users 上有**两个**唯一约束（username 与 lower(email)），提示文案完全不同
			// （换名字 vs 换邮箱），所以必须分辨。分辨依据是约束名，不是错误文案。
			if uniqueViolationConstraint(err) == emailUniqueIndex {
				return ErrEmailTaken
			}
			// 拿不到约束名时落到 username 分支：两者都是 409、都不泄露额外信息，
			// 且这个默认与"加 email 之前"的行为逐字一致（向后兼容的一侧）。
			return ErrUsernameTaken
		}
		return fmt.Errorf("store: 创建用户失败: %w", err)
	}
	return nil
}

// UserByUsername 按用户名查用户；未找到返回 ErrNotFound。
//
// 查询侧用同一套归一化，所以调用方传 "Alice" 也能命中库里的小写行。
// 空用户名直接当"没找到"，不去查库（也避免把空串交给数据库）。
func (s *Store) UserByUsername(ctx context.Context, username string) (*User, error) {
	name := canonicalUsername(username)
	if name == "" {
		return nil, ErrNotFound
	}
	var u User
	if err := s.db.WithContext(ctx).Where("username = ?", name).Take(&u).Error; err != nil {
		return nil, wrapDBErr("按用户名查用户", err)
	}
	return &u, nil
}

// UserByID 按主键查用户；不存在返回 ErrNotFound（§10 的 GET /api/users/:id 与
// RequireAuth 都靠它判"账号是否还在"）。
func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	var u User
	if err := s.db.WithContext(ctx).Where("id = ?", id).Take(&u).Error; err != nil {
		return nil, wrapDBErr("按 ID 查用户", err)
	}
	return &u, nil
}

// UserByEmail 按邮箱查用户（找回密码与"邮箱已被占用"判定的读入口）；
// 不存在返回 ErrNotFound。
//
// 查询写的是 `lower(email) = ?` 而不是 `email = ?`，三个理由：
//  1. 它是 users_email_lower_key 这个**函数索引**能命中的唯一形态（写错就是全表扫）；
//  2. 即使库里被旁路写入大小写混杂的值，也仍然匹配得上；
//  3. "唯一性按 lower(email) 判"与"查询按 lower(email) 找"必须同口径 ——
//     否则会出现"查得到两行"或"唯一约束挡住了却查不到"这类矛盾。
//
// 空邮箱直接当"没找到"，不去查库（也避免把空串交给数据库：
// `lower(email) = ”` 只会白白扫一遍，而且"没有邮箱"本来就不该匹配任何账号）。
func (s *Store) UserByEmail(ctx context.Context, email string) (*User, error) {
	addr := canonicalEmail(email)
	if addr == "" {
		return nil, ErrNotFound
	}
	var u User
	if err := s.db.WithContext(ctx).Where("lower(email) = ?", addr).Take(&u).Error; err != nil {
		return nil, wrapDBErr("按邮箱查用户", err)
	}
	return &u, nil
}

// SetUserEmail 改账号的邮箱，并**同时**决定它的验证状态。
//
// verifiedAt 非 nil = 这次改动本身就是归属证明（管理员绑定走这条：管理员是可信主体，
// 因此不需要走验证邮件 —— 这正是"给既有账号补邮箱"绕开鸡生蛋的那条路）；
// nil = 改完之后是未验证状态（将来用户自助换绑走这条，必须先收一封验证信）。
//
// email 传空串 = 解绑：email 与 email_verified_at **一起**置 NULL。
// 两列必须同时动 —— 留下"已验证"却没有邮箱是自相矛盾的状态，而留下"邮箱"却没有
// 验证时间会让一个从未证明过的邮箱进入找回链路。
//
// 唯一冲突（同一个邮箱属于另一个账号）返回 ErrEmailTaken。
func (s *Store) SetUserEmail(ctx context.Context, id int64, email string, verifiedAt *time.Time) error {
	if id <= 0 {
		return errors.New("store: SetUserEmail 需要有效的 user_id")
	}
	addr := canonicalEmail(email)
	updates := map[string]any{
		"email":             nil,
		"email_verified_at": nil,
	}
	if addr != "" {
		if len(addr) > maxEmailLen {
			return fmt.Errorf("store: email 超过 %d 个字符", maxEmailLen)
		}
		updates["email"] = addr
		updates["email_verified_at"] = verifiedAt
	}

	res := s.db.WithContext(ctx).Model(&User{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		if isUniqueViolation(res.Error) {
			return ErrEmailTaken
		}
		return fmt.Errorf("store: 改用户邮箱失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// canonicalEmail 把邮箱归一到存储与查询共用的规范形。
//
// 两步，都不是装饰：
//  1. **去掉所有控制字符与空白**（不只是首尾）：邮箱会被拼进 SMTP 的
//     `RCPT TO` / `To:` 头，里面出现 CR/LF 就是邮件头注入（攻击者在后面追加
//     `Bcc:` 之类）。服务层的正则已经拦住了它，这里是第二道 —— store 也可能
//     被别的调用方直接用；而且"库里存着一个带换行的邮箱"本身就是一颗定时炸弹。
//  2. **全小写**：与 username 全小写存储同口径，也让 users_email_lower_key
//     这个函数索引的语义（大小写不敏感的唯一性）在存储侧就成立。
func canonicalEmail(email string) string {
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return -1
		}
		return r
	}, email)
	return strings.ToLower(strings.TrimSpace(cleaned))
}

// SetUserStatus 改账号状态（active / banned）并维护 banned_reason。
//
// 语义细节：
//   - status=banned 且给了 reason → 记下理由；没给 → banned_reason 置 NULL；
//   - status=active（解封）→ 一定清空 banned_reason，不留"陈旧的封禁理由"；
//   - 目标不存在 → ErrNotFound。
//
// ⚠ 封禁的"立即失效"还需要 +1 token_version 并断开该用户的 WS（§5），
// 那是服务层（internal/service）的组合动作（SetUserStatus + BumpTokenVersion + hub 断连）。
// 本方法刻意只改状态列：把"版本号变更"藏在改状态里，会让审计看不清到底动了什么。
func (s *Store) SetUserStatus(ctx context.Context, id int64, status, reason string) error {
	if !isValidStatus(status) {
		return fmt.Errorf("store: 非法的 status %q（允许 %s / %s）", status, StatusActive, StatusBanned)
	}
	updates := map[string]any{
		"status":        status,
		"banned_reason": nil,
	}
	if status == StatusBanned {
		if r := strings.TrimSpace(reason); r != "" {
			updates["banned_reason"] = r
		}
	}

	res := s.db.WithContext(ctx).Model(&User{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		return fmt.Errorf("store: 改用户状态失败: %w", res.Error)
	}
	// PostgreSQL 的 UPDATE 即使写入相同的值也算命中一行，所以 0 = 确实没有这个 id。
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// SetUserRole 改角色（user / admin）；非法角色在应用层就报错，目标不存在返回 ErrNotFound。
func (s *Store) SetUserRole(ctx context.Context, id int64, role string) error {
	if !isValidRole(role) {
		return fmt.Errorf("store: 非法的 role %q（允许 %s / %s）", role, RoleUser, RoleAdmin)
	}
	res := s.db.WithContext(ctx).Model(&User{}).Where("id = ?", id).Update("role", role)
	if res.Error != nil {
		return fmt.Errorf("store: 改用户角色失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// SetPasswordHash 直接换口令哈希。
//
// ⚠ 它**只改口令**，不动会话与 token_version。改密必须同时让旧凭据失效，
// 因此调用方（service.replacePassword）紧接着要调 RevokeAllSessions + BumpTokenVersion。
// 链接令牌那条路径不需要它：ConsumePasswordReset 把四件事放在同一个事务里。
//
// 单独提供这个方法而不是复用 ConsumePasswordReset：后者要求一个待消费的令牌，
// 而 OTP 路径的令牌已经在 ConsumeEmailOTP 里消费掉了（那是它自己的单次使用点）。
func (s *Store) SetPasswordHash(ctx context.Context, id int64, hash string) error {
	if strings.TrimSpace(hash) == "" {
		return errors.New("store: SetPasswordHash 缺少口令哈希")
	}
	res := s.db.WithContext(ctx).Model(&User{}).Where("id = ?", id).Update("password_hash", hash)
	if res.Error != nil {
		return fmt.Errorf("store: 更新口令失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// BumpTokenVersion 把 token_version 加一并返回新值（§5：改密/封禁/登出全部设备
// 靠它让该用户所有 access token 立刻失效）。
//
// 必须是**单条语句** + RETURNING：分"先读后写"两步会在并发封禁/登录时交错，
// 返回值就不再是"本次自增后的版本号"，而 access token 的有效性判断依赖它是准的。
func (s *Store) BumpTokenVersion(ctx context.Context, id int64) (int, error) {
	return bumpTokenVersionIn(ctx, s.db, id)
}

// bumpTokenVersionIn 是 BumpTokenVersion 的**语句级唯一 owner**（理由同
// revokeAllSessionsIn）：改密事务必须能让"新口令"与"旧 token 全失效"同时成立，
// 因此这条语句要能在事务句柄上执行，而不是只认连接池。
func bumpTokenVersionIn(ctx context.Context, db *gorm.DB, id int64) (int, error) {
	var bumped User
	res := db.WithContext(ctx).
		Model(&bumped).
		Clauses(clause.Returning{Columns: []clause.Column{{Name: "token_version"}}}).
		Where("id = ?", id).
		Update("token_version", gorm.Expr("token_version + ?", 1))
	if res.Error != nil {
		return 0, fmt.Errorf("store: 自增 token_version 失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return 0, ErrNotFound
	}
	return bumped.TokenVersion, nil
}

// TouchLastSeen 记录"最后活跃时间"。
//
// 用 UpdateColumn 而不是 Update：心跳不是档案变更，不该蹭到 updated_at
// （否则 updated_at 会退化成"最后一次心跳"，管理端看不出用户资料到底什么时候改的）。
func (s *Store) TouchLastSeen(ctx context.Context, id int64, at time.Time) error {
	if at.IsZero() {
		at = time.Now()
	}
	res := s.db.WithContext(ctx).Model(&User{}).Where("id = ?", id).UpdateColumn("last_seen_at", at)
	if res.Error != nil {
		return fmt.Errorf("store: 更新 last_seen_at 失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// ListUsers 按分页返回用户与其总数（管理端用户列表，§8）。
//
// 排序固定为 id ASC：分页要稳定，"按 id 是唯一的单调列"这件事必须写死，
// 不能让调用方传排序字段（那既是注入面也是不确定的分页）。
func (s *Store) ListUsers(ctx context.Context, q UserQuery) ([]User, int64, error) {
	limit, offset := clampPage(q.Limit, q.Offset)

	// filter 每次返回一个全新的查询：Count 与 Find 必须各自构造，
	// 复用同一个 *gorm.DB 会把 Count 的 SELECT 串进 Find。
	filter := func() *gorm.DB {
		tx := s.db.WithContext(ctx).Model(&User{})
		if pattern := likePattern(q.Search); pattern != "" {
			tx = tx.Where(`username ILIKE ? ESCAPE '\' OR display_name ILIKE ? ESCAPE '\'`, pattern, pattern)
		}
		return tx
	}

	var total int64
	if err := filter().Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("store: 统计用户数失败: %w", err)
	}

	users := make([]User, 0, limit)
	if err := filter().Order("id ASC").Limit(limit).Offset(offset).Find(&users).Error; err != nil {
		return nil, 0, fmt.Errorf("store: 查询用户列表失败: %w", err)
	}
	return users, total, nil
}

// canonicalUsername 把用户名归一到存储规范形：去首尾空白 + 全小写（§4）。
//
// 只做 ASCII 小写（strings.ToLower 对某些 Unicode 会做长度变化，而用户名本来就
// 只允许 [A-Za-z0-9_]）；DDL 的 CHECK (username = lower(username)) 是兜底。
func canonicalUsername(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// applyUserDefaults 补齐 Role/Status/TokenVersion/LastSeenAt 的零值。
//
// 为什么需要它：GORM 的 INSERT 会显式写入每一列，DDL 里的 DEFAULT 在这条路径上
// 根本用不到，零值会变成字面空串并撞上 CHECK 约束（role/status），或者写成
// 0001-01-01（last_seen_at）。所以默认值必须在 Go 侧再写一遍 ——
// 两处必须一致：user_test.go 的 TestUserColumnDefaultsMatchGoDefaults 会插入一行
// 省略这些列的数据，直接比对 DDL 默认值与这里的常量是否一致。
//
// LastSeenAt 补成"现在"的语义：刚创建的用户，最后活跃时间就是创建时间
// （否则管理端会看到一个"从来没有活跃过"的账号，而且零值时间会污染比较和排序）。
func applyUserDefaults(u *User) {
	if u.Role == "" {
		u.Role = RoleUser
	}
	if u.Status == "" {
		u.Status = StatusActive
	}
	if u.TokenVersion <= 0 {
		u.TokenVersion = 1
	}
	if u.LastSeenAt.IsZero() {
		u.LastSeenAt = time.Now()
	}
}

// likePattern 把搜索串变成 ILIKE 的模式串：先转义通配符，再两侧加 %。
//
// 为什么要 ESCAPE：ILIKE 里 % 和 _ 是通配符，管理员搜 "a_b" 时直觉是字面匹配；
// 当模式用会出现"看着搜错了"的结果。注意这里转义的是**数据**，
// SQL 文本里始终只有占位符 —— 变量从不进 SQL 文本。
func likePattern(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('%')
	for _, r := range s {
		if r == '\\' || r == '%' || r == '_' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('%')
	return b.String()
}

// trimControl 去掉首尾空白与控制符（保留中间内容）—— 用于 IP、UA 这类短字段，
// 避免把 "" 或带换行的值写进库里。
func trimControl(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	})
}
