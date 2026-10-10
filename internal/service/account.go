// 账号用例（ACCOUNTS §2/§5/§9）：注册、登录、刷新、登出、封禁与授权前置。
//
// 本文件是"身份与授权事实"的唯一落点。三条边界：
//
//  1. **token 版本比对**只在这里（VerifyAccessToken）。internal/service/auth 只回答
//     "这串 token 是本服务签发的、结构完整且未过期"，它不碰数据库，因此
//     role/tv 在它那里**只是签发那一刻的快照**；"这个账号现在还能不能用"
//     必须由本层查库比对 users.token_version + status 才能回答（不变量 I3）。
//  2. **refresh token 原文永不落库**：库里只有 sha256 十六进制（§4）。原文只在
//     下发给客户端的那一次响应里存在。
//  3. **写库分两类**（§9 / 不变量 I4，逐条见 syncWriteReason）：会话的创建与撤销、
//     封禁/改角色的 token_version 自增必须在请求路径上完成（否则"登录后立刻刷新"
//     会偶发失败、封禁的生效时间不确定），其余写（last_seen、审计）全部投递给
//     异步写入器。
package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"
	"unicode"

	"ProjectionRoom/internal/service/auth"
	"ProjectionRoom/internal/store"
)

// —— 领域错误：REST/WS 两侧共用，调用方一律用 errors.Is 判断。
//
// 为什么"用户不存在"与"口令错误"必须是**同一个**错误（ErrInvalidCredentials）：
// 两者分别返回不同错误时，攻击者只需看响应就能枚举出"哪些用户名存在"，
// 再对存在的那些集中撞库。登录失败的对外文案也由它统一（§5）。
var (
	// ErrUsernameTaken 表示用户名已被占用。
	//
	// 它就是 store.ErrUsernameTaken 本身（别名而不是包装）：唯一约束冲突的语义
	// 在两侧是同一件事，别名保证调用方用 errors.Is 判断时不会因经手了哪一层而失效。
	ErrUsernameTaken = store.ErrUsernameTaken

	// ErrInvalidCredentials 表示"用户名或密码错误"。
	// 刻意不区分"用户不存在"与"口令错"（见上面的说明）。
	ErrInvalidCredentials = errors.New("account: 用户名或密码错误")

	// ErrUserBanned 表示账号已被封禁。
	//
	// 与登录失败不同，这里**可以**给出明确文案：封禁不是枚举面 ——
	// 能拿到这个错误的前提是已经通过了口令校验，也就是对方本来就知道账号存在。
	ErrUserBanned = errors.New("account: 账号已被封禁")

	// ErrSessionInvalid 表示 access token 与当前账号状态不符（token_version 落后，
	// 或账号已不存在）。封禁/改密/登出全部设备都靠它即时生效（§5）。
	ErrSessionInvalid = errors.New("account: 会话已失效，请重新登录")

	// ErrRefreshReplay 表示收到了一个**已撤销**的 refresh token（重放）。
	// 触发时的处置见 Refresh：撤销该用户全部会话 + 写审计。
	ErrRefreshReplay = errors.New("account: 刷新凭据已被使用过（疑似重放），已撤销该账号的全部会话")

	// ErrHashingBusy 表示并发口令哈希闸门已满。
	//
	// 见 acquireHashing 的说明：拿不到信号量时**立刻**失败，而不是排队等待 ——
	// 排队会让攻击者用一个恒定的慢响应把正常用户的登录一起拖住。
	ErrHashingBusy = errors.New("account: 服务繁忙，请稍后重试")

	// ErrBadUsername 表示用户名不符合白名单 `^[A-Za-z0-9_]{3,20}$`（§4）。
	ErrBadUsername = errors.New("account: 用户名必须是 3-20 位字母、数字或下划线")

	// ErrBadDisplayName 表示昵称清洗后为空或超长。
	ErrBadDisplayName = fmt.Errorf("account: 昵称不能为空且不超过 %d 个字符", MaxDisplayNameLen)

	// ErrBadRole 表示角色取值不在 {user, admin}。
	ErrBadRole = errors.New("account: 角色只能是 user 或 admin")

	// ErrSelfTarget 表示管理员对自己执行了会把自己锁在门外的操作。
	ErrSelfTarget = errors.New("account: 不能对自己执行该操作")

	// StoreNotFound 是 store.ErrNotFound 的别名，供**上层**（handler）判断"账号不存在"。
	//
	// 为什么要给一个别名：handler 现在只认识 service 与 service/auth 两个下层，为了一个哨兵
	// 错误把持久化包拉进 HTTP 层，会让"handler 依赖了什么"变得难以一眼看完。
	// 别名保持了判断能力，没有扩大依赖面（它是同一个 error 值，errors.Is 依然成立）。
	StoreNotFound = store.ErrNotFound
)

// 用户名与昵称的边界（与 store 的列长度一致，见 ACCOUNTS §4）。
const (
	// MinUsernameLen / MaxUsernameLen 与 DDL 的 varchar(20) 一致。
	MinUsernameLen = 3
	MaxUsernameLen = 20
	// MaxDisplayNameLen 与 DDL 的 varchar(32) 一致。
	MaxDisplayNameLen = 32
)

// UsernamePattern 是用户名的白名单（§4）。服务端统一按**小写**存储。
var UsernamePattern = regexp.MustCompile(`^[A-Za-z0-9_]{3,20}$`)

// 会话与哈希的常量。
const (
	// refreshTokenBytes 是 refresh token 的随机熵。32 字节 = 256 bit：
	// 它是 30 天有效的长期凭据，必须不可猜测（库里只存哈希，泄漏面只剩下发那一次）。
	refreshTokenBytes = 32

	// DefaultHashingConcurrency 是并发口令哈希闸门的默认容量。
	//
	// 依据：bcrypt cost=12 单次约 200ms（本机实测 0.57s），而目标服务器只有 2 个 vCPU。
	// 不设上限时，攻击者用少量 IP 并发打 /api/auth/login 就能把 CPU 占满，
	// 症状是**正常用户的登录一起变慢**（而不是攻击者自己被挡住）。
	// 取 4 的取舍：4 × 200ms 在 2 核上刚好让两个核都忙而不至于排队失控；
	// 登录/注册各自的每 IP 令牌桶（§5）是第一道闸，这一条是"跨 IP 聚合之后"的第二道。
	DefaultHashingConcurrency = 4
)

// AccountStore 是账号用例需要的**窄接口**（依赖倒置）。
//
// 为什么不直接依赖 *store.Store：
//  1. **可测性**：单测用内存假实现就能覆盖重放检测、token 版本比对、闸门饱和等
//     分支，不需要 PostgreSQL，也不会因为"本机没库"而 skip 掉最关键的那些用例；
//  2. **不把 GORM 泄漏进业务层**：本包不 import gorm，也不知道事务、连接池、
//     方言的存在。"怎么存"是 store 的事，"存什么才成立"才是本文件的事。
//
// *store.Store 的方法集逐字满足它，装配处直接传真实实例即可。
type AccountStore interface {
	// —— 用户
	CreateUser(ctx context.Context, u *store.User) error
	UserByUsername(ctx context.Context, username string) (*store.User, error)
	UserByID(ctx context.Context, id int64) (*store.User, error)
	SetUserStatus(ctx context.Context, id int64, status, reason string) error
	SetUserRole(ctx context.Context, id int64, role string) error
	BumpTokenVersion(ctx context.Context, id int64) (int, error)
	TouchLastSeen(ctx context.Context, id int64, at time.Time) error

	// —— 会话（refresh token）
	CreateSession(ctx context.Context, sess *store.Session) error
	SessionByTokenHash(ctx context.Context, hash string) (*store.Session, error)
	RevokeSession(ctx context.Context, id int64) error
	RevokeAllSessions(ctx context.Context, userID int64) (int64, error)

	// —— 审计
	InsertAudit(ctx context.Context, a *store.AdminAudit) error

	// —— 异步写入器（§9）
	EventWriter() *store.Writer
}

// AccountService 是账号用例服务。
//
// 它拿到的都是接口/值，没有全局状态：同一个进程里可以并存多个实例
// （单测就是这么用的），"共享状态"只有走 AccountStore 的那一份。
type AccountService struct {
	store  AccountStore
	signer *auth.Signer
	ticket *auth.TicketStore

	// bcryptCost 是注册时的哈希代价（登录校验读的是库里的哈希，与它无关）。
	bcryptCost int
	// refreshTTL 是新建会话的有效期（与 PR_REFRESH_TTL 同源，由装配层注入）。
	refreshTTL time.Duration

	// hashing 是并发口令哈希闸门（容量见 DefaultHashingConcurrency）。
	hashing chan struct{}

	// now 是时间源（单测可注入，便于断言 expiresAt 的语义）。
	now func() time.Time
}

// AccountOptions 是 AccountService 的可选参数。
//
// 用 options 结构体而不是长参数表：这几个值会一起从配置变过来，
// 位置参数一多，调用点就很难看出哪个是哪个。
type AccountOptions struct {
	// BcryptCost 是注册时的哈希代价；0 取保守默认 12（与 config.DefaultBcryptCost 一致）。
	BcryptCost int
	// RefreshTTL 是 refresh 会话有效期；<= 0 报错（没有默认值兜底：TTL 写错
	// 会表现为"登录后随机被登出"，而这种症状极难归因，宁可启动就失败）。
	RefreshTTL time.Duration
	// HashingConcurrency 是并发口令哈希闸门容量；<= 0 取 DefaultHashingConcurrency。
	HashingConcurrency int
	// Now 是时间源；nil 取 time.Now。
	Now func() time.Time
}

// NewAccountService 组装账号用例。
//
// 三个依赖都必须非 nil：少了任何一个都不是"降级"，而是"授权根本无从判断"，
// 在启动那一刻失败比在第一个登录请求上失败好定位得多。
func NewAccountService(st AccountStore, signer *auth.Signer, tickets *auth.TicketStore, opts AccountOptions) (*AccountService, error) {
	if st == nil {
		return nil, errors.New("account: 缺少 AccountStore")
	}
	if signer == nil {
		return nil, errors.New("account: 缺少 auth.Signer（access token 无从签发）")
	}
	if tickets == nil {
		return nil, errors.New("account: 缺少 auth.TicketStore（WS 票据无从签发）")
	}
	if opts.RefreshTTL <= 0 {
		return nil, fmt.Errorf("account: refresh TTL 必须为正，当前 %s", opts.RefreshTTL)
	}

	cost := opts.BcryptCost
	if cost == 0 {
		cost = 12
	}
	if cost < auth.MinBcryptCost || cost > auth.MaxBcryptCost {
		return nil, fmt.Errorf("account: bcrypt cost %d 超出允许区间 [%d,%d]",
			cost, auth.MinBcryptCost, auth.MaxBcryptCost)
	}

	conc := opts.HashingConcurrency
	if conc <= 0 {
		conc = DefaultHashingConcurrency
	}

	now := opts.Now
	if now == nil {
		now = time.Now
	}

	return &AccountService{
		store:      st,
		signer:     signer,
		ticket:     tickets,
		bcryptCost: cost,
		refreshTTL: opts.RefreshTTL,
		hashing:    make(chan struct{}, conc),
		now:        now,
	}, nil
}

// —— 对外结果类型。
//
// 为什么不把 *store.User 直接交给 handler：那条路径上 PasswordHash 会跟着 JSON
// 序列化一起出去（§10 的响应面不允许它）。类型系统是这里唯一可靠的闸门 ——
// "记得别写那个字段"靠不住。
// 内部流程仍然用 *store.User（授权判定需要 token_version），只在出口处转换。

// Profile 是账号的对外档案（**不含** PasswordHash）。
type Profile struct {
	ID          int64      `json:"id"`
	Username    string     `json:"username"`
	DisplayName string     `json:"displayName"`
	Email       *string    `json:"email,omitempty"`
	Role        string     `json:"role"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"createdAt"`
	LastSeenAt  *time.Time `json:"lastSeenAt,omitempty"`
}

// ProfileOf 把库里的用户转成对外档案（唯一出口，保证 PasswordHash 不外泄）。
func ProfileOf(u *store.User) Profile {
	if u == nil {
		return Profile{}
	}
	p := Profile{
		ID:          u.ID,
		Username:    u.Username,
		DisplayName: u.DisplayName,
		Email:       u.Email,
		Role:        u.Role,
		Status:      u.Status,
		CreatedAt:   u.CreatedAt,
	}
	if !u.LastSeenAt.IsZero() {
		t := u.LastSeenAt
		p.LastSeenAt = &t
	}
	return p
}

// Session 是一次登录的产物：access token（放响应体）+ refresh token（放 HttpOnly Cookie）。
type Session struct {
	User Profile

	// AccessToken 是 HS256 JWT（§5：只存内存 + sessionStorage，绝不进 localStorage）。
	AccessToken string
	// AccessExpiresAt 是 access token 的到期时刻（客户端据此提前刷新）。
	AccessExpiresAt time.Time
	// TokenVersion 是签发时的 token_version（排障用；判定一律以库为准）。
	TokenVersion int

	// RefreshToken 是**原文**，只在本结构里存在一次。
	// handler 必须把它塞进 Set-Cookie（HttpOnly/SameSite=Lax/Path=/api/auth），
	// 绝不写进响应体 —— 一旦进了响应体，前端任何一处日志/上报都可能把它带出去。
	RefreshToken string
	// RefreshExpiresAt 是 refresh 的到期时刻（推导 Cookie 的 Max-Age）。
	RefreshExpiresAt time.Time
}

// Register 注册账号并直接登录（返回 access + refresh）。
//
// 顺序是刻意的：先走口令策略（廉价）→ 再哈希（昂贵）→ 再入库。
// 把策略检查放在哈希之前，既是"别为一个注定被拒的请求花 200ms CPU"，
// 也让弱口令的失败文案能说清缺哪一条（§5 的"统一文案"只针对**登录**）。
func (s *AccountService) Register(ctx context.Context, username, displayName, password, ip, ua string) (*Session, error) {
	name, err := CanonicalUsername(username)
	if err != nil {
		return nil, err
	}
	display, err := SanitizeDisplayName(displayName, name)
	if err != nil {
		return nil, err
	}
	if err := auth.ValidatePasswordPolicy(password); err != nil {
		return nil, err
	}

	hash, err := s.acquireAndHash(password)
	if err != nil {
		return nil, err
	}

	// 同步写（见 syncWriteReason 第 2 条）：userID 是后面一切的前提。
	u := &store.User{
		Username:     name,
		DisplayName:  display,
		PasswordHash: hash,
	}
	if err := s.store.CreateUser(ctx, u); err != nil {
		return nil, err // ErrUsernameTaken 由 store 映射，本文件用别名直接透传
	}

	return s.issueSession(ctx, u, ip, ua)
}

// Login 校验口令并建立会话。
//
// 三条不变量：
//  1. "用户不存在"与"口令错误"返回**同一个** ErrInvalidCredentials；
//  2. 两者都跑一次 bcrypt（用户不存在时跑一个固定的无效哈希）：否则响应耗时
//     本身就把它拆成了两个不同的结果（时序侧信道）；
//  3. 封禁账号在**口令校验通过之后**才报 ErrUserBanned —— 先判状态会让
//     "这个账号被封了"变成一条不需要口令的枚举手段。
func (s *AccountService) Login(ctx context.Context, username, password, ip, ua string) (*Session, error) {
	name := strings.ToLower(strings.TrimSpace(username))

	u, err := s.store.UserByUsername(ctx, name)
	switch {
	case err == nil:
		// 走下面的正常校验。
	case errors.Is(err, store.ErrNotFound):
		if busyErr := s.verifyDummy(password); busyErr != nil {
			return nil, busyErr
		}
		return nil, ErrInvalidCredentials
	default:
		return nil, err
	}

	ok, busyErr := s.verifyPassword(u.PasswordHash, password)
	if busyErr != nil {
		return nil, busyErr
	}
	if !ok {
		return nil, ErrInvalidCredentials
	}
	if u.Status != store.StatusActive {
		return nil, ErrUserBanned
	}

	// last_seen 是"活跃度"而非授权依据 → 异步写（§9）。失败只记日志：
	// 一个心跳写不进去不该让登录失败。
	s.submit("更新 last_seen", func(ctx context.Context) error {
		return s.store.TouchLastSeen(ctx, u.ID, s.now())
	})

	return s.issueSession(ctx, u, ip, ua)
}

// Refresh 用 refresh token 换一对新凭据（轮换 + 重放检测，§5）。
//
// 重放检测的两种命中形态都归一到"撤销该用户全部会话"：
//
//  1. 库里的行已经 revoked（正常路径：这个 token 已经被上一次轮换替换掉了）；
//  2. 并发双花：两个请求同时用**同一个**未撤销的 token，两边都通过了"是否已撤销"
//     的检查，于是第二个 CreateSession 会撞上 token_hash 唯一约束 —— 那也是重放。
//
// 第 2 条正是"先插入新行、再撤销旧行"这个顺序的原因：反过来（先撤销旧行再插入）
// 会让先到的请求把旧行标成 revoked，后到的那个落到形态 1 上 ——
// 于是**一次正常的并发刷新（多标签页同时刷新）会被误判成攻击**，把用户全端踢下线。
func (s *AccountService) Refresh(ctx context.Context, refreshToken, ip, ua string) (*Session, error) {
	token := strings.TrimSpace(refreshToken)
	if token == "" {
		return nil, ErrSessionInvalid
	}

	sess, err := s.store.SessionByTokenHash(ctx, HashRefreshToken(token))
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
		// 库里没有这一行：可能是过期的行已被清理，也可能压根是伪造的。
		// 两者都没有"被撤销"的证据，因此不判重放，只当无效。
		return nil, ErrSessionInvalid
	default:
		return nil, err
	}

	if sess.RevokedAt != nil {
		s.handleReplay(ctx, "refresh token 已撤销（重放）", sess.UserID, ip)
		return nil, ErrRefreshReplay
	}

	now := s.now()
	if !now.Before(sess.ExpiresAt) {
		return nil, ErrSessionInvalid
	}

	u, err := s.store.UserByID(ctx, sess.UserID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrSessionInvalid
		}
		return nil, err
	}
	if u.Status != store.StatusActive {
		return nil, ErrUserBanned
	}

	newToken, newHash, err := newRefreshToken()
	if err != nil {
		return nil, err
	}
	if err := s.createSession(ctx, u.ID, newHash, ip, ua, now); err != nil {
		if isReplay(err) {
			s.handleReplay(ctx, "refresh token 并发双花（token_hash 重复）", u.ID, ip)
			return nil, ErrRefreshReplay
		}
		return nil, err
	}
	if err := s.store.RevokeSession(ctx, sess.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	accessToken, claims, err := s.signer.Issue(u.ID, u.Role, u.TokenVersion)
	if err != nil {
		return nil, err
	}

	return &Session{
		User:             ProfileOf(u),
		AccessToken:      accessToken,
		AccessExpiresAt:  claims.ExpiresAt,
		TokenVersion:     claims.TokenVersion,
		RefreshToken:     newToken,
		RefreshExpiresAt: now.Add(s.refreshTTL),
	}, nil
}

// Logout 撤销当前会话（幂等）。
//
// 幂等是刻意的：登出失败没有任何用户可采取的动作（再点一次结果一样），
// 而把"已撤销/不存在"报成错误只会让前端弹一个没有意义的红条。
func (s *AccountService) Logout(ctx context.Context, refreshToken string) error {
	token := strings.TrimSpace(refreshToken)
	if token == "" {
		return nil
	}
	sess, err := s.store.SessionByTokenHash(ctx, HashRefreshToken(token))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	if err := s.store.RevokeSession(ctx, sess.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return nil
}

// LogoutAll 登出该用户的全部设备：撤销全部会话 + token_version +1。
//
// 两步都要做，且都不能异步：撤销会话只让 refresh 失效，而**已经签发的 access token**
// 要到 token_version 变化才失效（§5 的即时性要求）。
func (s *AccountService) LogoutAll(ctx context.Context, userID int64) error {
	if _, err := s.store.BumpTokenVersion(ctx, userID); err != nil {
		return err
	}
	if _, err := s.store.RevokeAllSessions(ctx, userID); err != nil {
		return err
	}
	return nil
}

// Me 返回当前账号的对外档案（不含 PasswordHash，见 Profile 的说明）。
func (s *AccountService) Me(ctx context.Context, userID int64) (Profile, error) {
	u, err := s.store.UserByID(ctx, userID)
	if err != nil {
		return Profile{}, err
	}
	return ProfileOf(u), nil
}

// IssueWSTicket 签发一张 WS 一次性票据（§5：浏览器无法给 WebSocket 加请求头，
// 而 access token 进 URL 会落到反代 access log 与浏览器历史里）。
//
// 这里多查一次库（而不是只信 RequireAuth 已经放进 ctx 的用户）：
// 票据是"用一次就换一条长连接"的凭据，签发的这一刻必须确认账号仍可用 ——
// 否则被封禁的账号可以用一张旧请求里拿到的票据继续建连。
func (s *AccountService) IssueWSTicket(ctx context.Context, userID int64) (string, error) {
	u, err := s.store.UserByID(ctx, userID)
	if err != nil {
		return "", err
	}
	if u.Status != store.StatusActive {
		return "", ErrUserBanned
	}
	return s.ticket.Issue(userID), nil
}

// VerifyAccessToken 是**授权事实**的唯一判定点（不变量 I3）。
//
// 它做三件事，缺一不可：
//  1. auth.Signer.Verify：签名/算法/过期之外一切不合法 → 无效；
//  2. 查库拿当前用户：账号已删除 → 会话无效；
//  3. 比对 claims.TokenVersion == users.token_version（封禁/改密/登出全部设备的即时性），
//     再检查 status == active。
//
// 第 3 条是 auth 包刻意**不做**的那一半：它不碰数据库，只回答"这串 token 是我签的"。
func (s *AccountService) VerifyAccessToken(ctx context.Context, token string) (*store.User, error) {
	claims, err := s.signer.Verify(strings.TrimSpace(token))
	if err != nil {
		// auth 的两个哨兵（ErrExpiredToken / ErrInvalidToken）原样上抛：
		// handler 要把"过期 → 401 且可静默刷新"与"无效 → 401 且必须重新登录"分开。
		return nil, err
	}

	u, err := s.store.UserByID(ctx, claims.UserID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrSessionInvalid
		}
		return nil, err
	}
	if claims.TokenVersion != u.TokenVersion {
		return nil, ErrSessionInvalid
	}
	if u.Status != store.StatusActive {
		return nil, ErrUserBanned
	}
	return u, nil
}

// BanUser 封禁一个账号（status=banned + token_version +1）并写审计。
//
// 封禁的三件事里有两件在这里做完（状态 + 版本号），第三件（断开该用户的 WS 连接）
// 属于 T8 的 hub 集成 —— 本文件不碰连接。
//
// signalBan 决定封禁还是解封：BanUser 的语义是"封禁"，解封走 UnbanUser。
// 两者的差别只有 status 与 reason 两处，共享下面这段实现。
func (s *AccountService) BanUser(ctx context.Context, actorID, targetID int64, reason, ip string) error {
	return s.setStatus(ctx, actorID, targetID, store.StatusBanned, reason, ip)
}

// UnbanUser 解封一个账号（status=active + 清空封禁理由 + token_version +1）并写审计。
func (s *AccountService) UnbanUser(ctx context.Context, actorID, targetID int64, ip string) error {
	return s.setStatus(ctx, actorID, targetID, store.StatusActive, "", ip)
}

// setStatus 是封禁/解封的共用实现。
func (s *AccountService) setStatus(ctx context.Context, actorID, targetID int64, status, reason, ip string) error {
	if targetID <= 0 {
		return errors.New("account: 目标账号不合法")
	}
	if actorID == targetID {
		// 自封禁会把最后一个管理员锁在门外，且没有任何自助恢复路径。
		return fmt.Errorf("%w（可能是最后一个管理员）", ErrSelfTarget)
	}

	reason = strings.TrimSpace(reason)
	action := "user.unban"
	if status == store.StatusBanned {
		action = "user.ban"
	}

	if err := s.store.SetUserStatus(ctx, targetID, status, reason); err != nil {
		return err
	}
	// 版本号自增必须与状态变更同一条请求路径：否则"已封禁但旧 access token 还能用"
	// 会留下一个长度不确定的窗口（§5 的"封禁即时生效"）。
	if _, err := s.store.BumpTokenVersion(ctx, targetID); err != nil {
		return err
	}

	// 审计是 §8 的 must 落点，但"审计写失败"不该让封禁本身失败 → 异步 + 重试。
	s.submit("写封禁审计", func(ctx context.Context) error {
		return s.store.InsertAudit(ctx, &store.AdminAudit{
			ActorID:    actorID,
			Action:     action,
			TargetType: "user",
			TargetID:   fmt.Sprintf("%d", targetID),
			Detail: auditDetail(map[string]any{
				"status": status,
				"reason": reason,
			}),
			IP: optString(ip),
		})
	})
	return nil
}

// SetRole 改角色并写审计（同样：两个同步写 + 一个异步审计）。
func (s *AccountService) SetRole(ctx context.Context, actorID, targetID int64, role, ip string) error {
	if targetID <= 0 {
		return errors.New("account: 目标账号不合法")
	}
	r := strings.ToLower(strings.TrimSpace(role))
	if r != store.RoleUser && r != store.RoleAdmin {
		return ErrBadRole
	}
	if actorID == targetID && r != store.RoleAdmin {
		// 自我降级 = 把自己锁在管理端之外（且需要另一个人来救）。
		return ErrSelfTarget
	}

	if err := s.store.SetUserRole(ctx, targetID, r); err != nil {
		return err
	}
	// 改角色也必须让旧 token 立刻失效：access token 里的 role 是签发快照，
	// 不 +1 的话"降级"要等 15 分钟才生效，而"升级"要重新登录才拿到新角色。
	if _, err := s.store.BumpTokenVersion(ctx, targetID); err != nil {
		return err
	}

	s.submit("写改角色审计", func(ctx context.Context) error {
		return s.store.InsertAudit(ctx, &store.AdminAudit{
			ActorID:    actorID,
			Action:     "user.role",
			TargetType: "user",
			TargetID:   fmt.Sprintf("%d", targetID),
			Detail:     auditDetail(map[string]any{"role": r}),
			IP:         optString(ip),
		})
	})
	return nil
}

// —— 内部：会话签发。

// issueSession 建立一条新会话并签发 access token。
//
// refresh 会话的 INSERT 是**同步**的（见 syncWriteReason 第 1 条）。
func (s *AccountService) issueSession(ctx context.Context, u *store.User, ip, ua string) (*Session, error) {
	now := s.now()
	token, hash, err := newRefreshToken()
	if err != nil {
		return nil, err
	}
	if err := s.createSession(ctx, u.ID, hash, ip, ua, now); err != nil {
		return nil, err
	}

	accessToken, claims, err := s.signer.Issue(u.ID, u.Role, u.TokenVersion)
	if err != nil {
		return nil, err
	}

	return &Session{
		User:             ProfileOf(u),
		AccessToken:      accessToken,
		AccessExpiresAt:  claims.ExpiresAt,
		TokenVersion:     claims.TokenVersion,
		RefreshToken:     token,
		RefreshExpiresAt: now.Add(s.refreshTTL),
	}, nil
}

// createSession 写入一行会话（同步，见 syncWriteReason 第 1 条）。
func (s *AccountService) createSession(ctx context.Context, userID int64, hash, ip, ua string, now time.Time) error {
	return s.store.CreateSession(ctx, &store.Session{
		UserID:    userID,
		TokenHash: hash,
		IP:        optString(ip),
		UserAgent: optString(ua),
		IssuedAt:  now,
		ExpiresAt: now.Add(s.refreshTTL),
	})
}

// handleReplay 是重放命中后的**统一处置**：撤销该用户全部会话 + 写审计。
//
// 只撤销"这个用户"而不是全局：重放只证明"这一串凭据泄漏了"，
// 没有证据说明别的账号也受影响。
//
// 撤销放在请求路径上是**有意的**：这条请求本来就要被拒绝，慢一点没有代价；
// 而"先回 401、再异步撤销"会留下一个窗口 —— 攻击者可以在窗口内继续刷新，
// 因为撤销还没落库（这段窗口正是 fail-open 的方向，不能留）。
func (s *AccountService) handleReplay(ctx context.Context, reason string, userID int64, ip string) {
	n, err := s.store.RevokeAllSessions(ctx, userID)
	if err != nil {
		log.Printf("[WARN] 重放检测：撤销用户 %d 的全部会话失败：%v", userID, err)
	}
	log.Printf("[WARN] 刷新凭据重放（%s）：user=%d 已撤销 %d 条会话", reason, userID, n)

	s.submit("写重放审计", func(ctx context.Context) error {
		return s.store.InsertAudit(ctx, &store.AdminAudit{
			ActorID:    userID,
			Action:     "session.replay",
			TargetType: "user",
			TargetID:   fmt.Sprintf("%d", userID),
			Detail: auditDetail(map[string]any{
				"reason":  reason,
				"revoked": n,
			}),
			IP: optString(ip),
		})
	})
}

// —— 内部：并发口令哈希闸门。

// acquireHashing 尝试占用一个哈希名额；拿不到立刻返回 false（不排队）。
//
// 取舍（§5 / 风险表）：bcrypt cost=12 单次约 200ms（本机实测 0.57s），
// 目标机器只有 2 vCPU。若允许无限并发，攻击者只要并发打登录就能让 CPU 饱和，
// 结果是**所有人的登录一起变慢**（更糟的形态：请求堆积把内存也吃掉）。
//
// 两个选择里我们选"拒绝"而不是"排队"：
//   - 排队：响应时间无上界（队尾要等 N × 200ms），攻击者用很小的并发就能把
//     正常用户排到几十秒之后，而且队列本身是被打爆的第二个资源；
//   - 拒绝（本实现）：立刻回 429/503，客户端退避重试。代价是"极端峰值下正常
//     用户可能被误拒一次"，但它有界、可重试、且能在指标/日志里看见。
func (s *AccountService) acquireHashing() bool {
	select {
	case s.hashing <- struct{}{}:
		return true
	default:
		return false
	}
}

// releaseHashing 归还名额。
func (s *AccountService) releaseHashing() { <-s.hashing }

// acquireAndHash 在闸门内计算口令哈希（注册路径）。
func (s *AccountService) acquireAndHash(password string) (string, error) {
	if !s.acquireHashing() {
		return "", ErrHashingBusy
	}
	defer s.releaseHashing()
	return auth.HashPassword(password, s.bcryptCost)
}

// verifyPassword 在闸门内校验口令；第二个返回值非 nil 表示闸门已满。
func (s *AccountService) verifyPassword(hash, password string) (bool, error) {
	if !s.acquireHashing() {
		return false, ErrHashingBusy
	}
	defer s.releaseHashing()
	return auth.VerifyPassword(hash, password), nil
}

// dummyHash 是"账号不存在"路径上用来消耗等价 CPU 的固定无效哈希。
//
// 它只需是"bcrypt 能解析、但永远不匹配"的形态：bcrypt 的解析失败与比较失败
// 都走同一条 false 路径（见 auth.VerifyPassword 的注释），因此时序一致。
// 里面刻意不写任何真实口令的哈希 —— 它只是一段满足 bcrypt 格式的常量。
const dummyHash = "$2a$12$0000000000000000000000000000000000000000000000000000.0"

// verifyDummy 消耗一次与真实校验等价的 CPU（防账号枚举的时序侧信道）。
func (s *AccountService) verifyDummy(password string) error {
	if !s.acquireHashing() {
		return ErrHashingBusy
	}
	defer s.releaseHashing()
	_ = auth.VerifyPassword(dummyHash, password)
	return nil
}

// —— 内部：异步投递。

// submit 把一个写作业投递给异步写入器（§9 / 不变量 I4）。
//
// 降级行为：写入器未挂载时（测试装配、或账号能力未启用）**起一个后台协程执行**，
// 而不是静默丢弃 —— 审计与 last_seen 都是"该发生的事"，静默丢会让
// "管理端看不到审计行"变成一个没有原因可查的现象。无论哪条分支，
// 调用方（handler 的请求路径）都不会等这次 DB 往返。
//
// 与 GORM 的翻译在 SubmitAccountJob 里（account_store.go），本文件因此不认识 gorm。
func (s *AccountService) submit(name string, fn func(ctx context.Context) error) {
	w := s.store.EventWriter()
	if w != nil {
		// 写入器提供自己的 ctx 与事务（见 store.Writer.run）：请求 ctx 一结束
		// 就被取消，直接传进来会让异步写必然失败。
		if !SubmitAccountJob(w, fn) {
			log.Printf("[WARN] 账号异步写（%s）：写入队列已满或写入器已关闭，事件被丢弃", name)
		}
		return
	}

	go func() {
		if err := fn(context.Background()); err != nil {
			log.Printf("[WARN] 账号异步写（%s）失败：%v", name, err)
		}
	}()
}

// —— 公开的小工具（handler 与测试都会用）。

// CanonicalUsername 校验并归一化用户名：白名单 `^[A-Za-z0-9_]{3,20}$` + 全小写（§4）。
//
// 为什么在本地再做一遍白名单再交给 store：store 的归一化只管"落库口径一致"，
// 它不做字符集判别；"用户名长得对不对"是产品规则，属于这一层。
func CanonicalUsername(username string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(username))
	if !UsernamePattern.MatchString(name) {
		return "", ErrBadUsername
	}
	return name, nil
}

// SanitizeDisplayName 清洗昵称（§7.3 的统一入口）。
//
// 规则：去掉零宽/Bidi 与其他控制符（Cc/Cf）→ 去首尾空白 → 限长 32 个字符。
// 清洗后为空时回落到用户名 —— 昵称是"显示给人看的"，为空会让成员列表出现一行空白。
func SanitizeDisplayName(display, fallback string) (string, error) {
	cleaned := strings.TrimSpace(StripControl(display))
	if cleaned == "" {
		cleaned = strings.TrimSpace(StripControl(fallback))
	}
	if cleaned == "" {
		return "", ErrBadDisplayName
	}
	if len([]rune(cleaned)) > MaxDisplayNameLen {
		return "", ErrBadDisplayName
	}
	return cleaned, nil
}

// StripControl 去掉控制符与不可见格式符：
//
//	U+200B-U+200F  零宽空格/断字/连接符/方向标记
//	U+202A-U+202E  Bidi 嵌入与覆盖（可让 "abc" 在屏幕上显示成 "cba" 这类欺骗）
//	U+2066-U+2069  Bidi 隔离
//	其余 Cc/Cf 类    换行、制表以及其他不可见格式符
//
// 为什么"去"而不是"转义"：这些字符在纯文本渲染里没有正当用途（§7.3 要求
// 前端一律文本插值），留着它们只会让"看着一样"的两个名字产生两个不同的身份。
func StripControl(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 0x200B && r <= 0x200F:
			return -1
		case r >= 0x202A && r <= 0x202E:
			return -1
		case r >= 0x2066 && r <= 0x2069:
			return -1
		case unicode.IsControl(r):
			return -1
		case unicode.In(r, unicode.Cf):
			return -1
		default:
			return r
		}
	}, s)
}

// HashRefreshToken 计算 refresh token 的落库形态（sha256 十六进制，§4）。
//
// 库里**只**存这个值：即便数据库被读走，攻击者拿到的也是一串不可逆的摘要。
func HashRefreshToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// newRefreshToken 生成一个不透明的 refresh token 及其哈希。
//
// 用 base64.RawURLEncoding：token 要进 Cookie，标准 base64 的 +/= 在 Cookie/头部
// 里需要额外转义，多一层编码就多一处"某一端忘了转义"的坑。
func newRefreshToken() (token string, hash string, err error) {
	buf := make([]byte, refreshTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("account: 生成 refresh token 失败: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(buf)
	return token, HashRefreshToken(token), nil
}

// auditDetail 把结构化明细编成 JSON 文本。
//
// 为什么在这一层就编好：store.InsertAudit 的 Detail 是 string，它只对
// "以 { 或 [ 开头的合法 JSON" 原样透传，其余按**字符串值**编码（详见
// canonicalAuditDetail）。若这里直接传 Go 的 map 字面量，落库的会是一个
// 被转义的字符串而不是 jsonb 对象，管理端就得再解一层。
//
// 编码失败时退化成 {}：字段值都是字符串与整数，实际编不出来，
// 真发生了也只有一种后果（明细为空），不该让审计写入整体失败。
func auditDetail(fields map[string]any) string {
	if len(fields) == 0 {
		return "{}"
	}
	b, err := json.Marshal(fields)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// optString 把可空字符串转成指针（空串 → nil，让"没有这个值"只有一种表示）。
func optString(s string) *string {
	v := strings.TrimSpace(s)
	if v == "" {
		return nil
	}
	return &v
}

// isReplay 判断错误是否为"token_hash 唯一约束冲突"（并发双花的信号）。
//
// 为什么用哨兵而不是匹配错误文案：`store.CreateSession` 撞唯一约束时返回
// `store.ErrTokenHashTaken`，这是**契约级**的判断依据；而按措辞（`strings.Contains(err,
// "token_hash")`）判断会让 store 那边一次无关的文案改写把这里静默降级成"永远不命中"
// —— 症状是并发双花不再被识别为重放，但不会有任何报错，极难发现。
//
// 口径依然窄：只有这一种错误被视为重放。宁可漏判（退化成 500，客户端重试）也不能
// 误判（把一次正常刷新当成攻击、踢掉用户全部会话）。
func isReplay(err error) bool {
	return errors.Is(err, store.ErrTokenHashTaken)
}
