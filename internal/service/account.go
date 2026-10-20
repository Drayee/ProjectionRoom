// 账号用例（ACCOUNTS §2/§5/§9）：注册、登录、刷新、登出、封禁、授权前置，
// 以及"忘记密码 / 邮箱验证"两条邮件链路。
//
// 本文件是"身份与授权事实"的唯一落点。三条边界：
//
//  1. **token 版本比对**只在这里（VerifyAccessToken）。internal/service/auth 只回答
//     "这串 token 是本服务签发的、结构完整且未过期"，它不碰数据库，因此
//     role/tv 在它那里**只是签发那一刻的快照**；"这个账号现在还能不能用"
//     必须由本层查库比对 users.token_version + status 才能回答（不变量 I3）。
//  2. **refresh token 原文永不落库**：库里只有 sha256 十六进制（§4）。原文只在
//     下发给客户端的那一次响应里存在。一次性邮件令牌（password_resets）同规格。
//  3. **写库分两类**（§9 / 不变量 I4）。见下面的同步写清单。
//
// # 同步写清单（不变量 I4 的**全部**例外）
//
// 除下列五处，所有写（last_seen、审计、投递结果）一律交给异步写入器
// （AccountService.submit → store.Writer），绝不在请求路径上等数据库。
// 每一处例外都必须写明 fail-open 的后果 —— 写不出后果的写，就不配当例外：
//
//	① 会话 INSERT（issueSession → store.CreateSession）
//	   fail-open 的后果：登录成功但库里没有可撤销的那一行 → 用户拿到一张
//	   **无法撤销**的 refresh（封禁、登出全部设备、重放处置全都失效）。
//	② 会话撤销（Logout / LogoutAll / handleReplay → store.RevokeSession /
//	   store.RevokeAllSessions）
//	   fail-open 的后果：重放被检测到却没有真的撤销 → 攻击者可以继续刷新；
//	   这一条在 handleReplay 里还有额外理由：先回 401 再异步撤销会留下一个
//	   "撤销尚未落库"的窗口，攻击者可以在窗口内继续刷新。
//	③ token_version 自增（BanUser / UnbanUser / SetRole / LogoutAll →
//	   store.BumpTokenVersion）
//	   fail-open 的后果：封禁/降级"不即时" —— access token 里的 role 与版本号
//	   是签发快照，不 +1 的话被禁的人还能用旧 access token 继续操作最长 15 分钟。
//	④ 一次性邮件令牌 INSERT（Forgot / Register → store.InsertPasswordReset）
//	   fail-open 的后果：信发出去了、库里却没有这一行 → 用户点开链接得到
//	   "令牌无效"，功能静默失效且没有任何报错可查。
//	⑤ 一次性邮件令牌消费（ResetPassword / VerifyEmail →
//	   store.ConsumePasswordReset / store.ConsumeEmailVerify）
//	   fail-open 的后果：令牌可重放（截获过那封邮件的人能二次改密）。
//	   注意它与 ④ 是**同一个原子动作的两半**：消费还顺带改密 / 改验证状态，
//	   四件事在 store 侧的一个事务里完成。
//
// 📌 这张清单是一次**补账**：此前本文件有 4 处注释引用 `syncWriteReason`，
// 而那个标识符在全仓库没有定义（重构 usecase→service 时丢了）—— 也就是
// "一期的同步写例外清单"从来只存在于引用的名字里。现在清单就是上面这一段，
// 下面所有引用都指向它。新增第六条例外时必须先在这里写清 fail-open 的后果。
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
	"ProjectionRoom/internal/service/mail"
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

	// ErrUserNotFound 表示"这个用户不可见"（wayfinder #33）。
	//
	// ⚠ 它把**两种**情形合成一个值，这是刻意的（与 ErrInvalidCredentials 同一条理由）：
	//   - 账号从来不存在；
	//   - 账号存在但已被封禁。
	//
	// 公开档案（GET /api/users/:id）是**免登录**端点，区分这两者等于给攻击者一条
	// 存在性探测通道 —— 他可以批量扫 id，把"存在但被封"挑出来（那是值得继续挖的目标）。
	// handler 对它的映射因此只有一个 404。
	//
	// 与 ErrUserBanned 是两个错误，别混：后者是"你**自己**被封了"（登录/取票据时
	// 已经通过口令校验，不构成枚举面，可以给出明确文案）；前者是"**别人**的资料
	// 你读不到"。
	ErrUserNotFound = errors.New("account: 用户不存在")

	// ErrUserForbidden 表示"你自己这个账号当前不允许做这件事"（#33 的 PATCH /api/me）。
	//
	// 目前唯一的来源是"账号已被封禁"：改昵称是**本人**的动作，请求方已经带着
	// 合法 access token（RequireAuth 放行了），所以这里可以给出 403 而不是 404 ——
	// 对本人隐瞒"你被封了"没有意义，他连登录都过不了。
	//
	// 为什么不直接复用 ErrUserBanned：后者的文案是"账号已被封禁"，那是**登录**语境
	// 的话；而这里是"你的账号当前不能修改资料"，给前端的下一步动作不同
	// （一个是"别再试了"，一个是"去问问管理员"）。
	ErrUserForbidden = errors.New("account: 当前账号不允许执行该操作")

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

	// ErrBadUsername 表示用户名不符合白名单（新注册用 ASCII+ 数字 + 下划线 3~20；
	// 旧用户名通过 Login 的兼容路径允许 Unicode）。
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

	// ErrEmailTaken 表示邮箱已被另一个账号绑定（注册或管理员绑定时的唯一冲突）。
	//
	// 它与 ErrBadEmail 是**两件不同的事**：前者是"格式对但这个邮箱有主了"，
	// 后者的输入压根不像邮箱。混成一个错误会让用户不知道该改什么。
	ErrEmailTaken = store.ErrEmailTaken

	// ErrResetTokenInvalid 表示重置令牌不可用（不存在 / 已过期 / 已被使用 / 用途不符）。
	//
	// **四种情况刻意共用一个错误与一个错误码**：区分它们就等于给攻击者一条
	// 探测通道（"这个 token 是过期的" 与 "这个 token 从来不存在" 是很不一样的情报）。
	ErrResetTokenInvalid = errors.New("account: 重置链接无效或已过期，请重新申请")

	// ErrVerifyTokenInvalid 表示验证令牌不可用（同上，四合一）。
	//
	// 为什么不与 ErrResetTokenInvalid 共用：两个用途的错误对用户是可操作的
	//（"重新申请重置" 与 "重新发一封验证信"），而它们**不构成枚举面** ——
	// 能拿到这个错误的前提是对方已经持有一串 256 bit 的随机串。
	ErrVerifyTokenInvalid = errors.New("account: 验证链接无效或已过期，请重新获取")

	// ErrMailUnavailable 是 mail.ErrMailUnavailable 的别名，供**上层**（handler）判断
	// "邮件能力未配置"（503）。
	//
	// 与 StoreNotFound 同一条理由：handler 只认识 service 与 service/auth，
	// 为了一个哨兵错误再 import service/mail 会让"HTTP 层依赖了什么"变得难以一眼看完。
	// 别名是同一个 error 值，errors.Is 依然成立。
	ErrMailUnavailable = mail.ErrMailUnavailable
)

// 用户名与昵称的边界（与 store 的列长度一致，见 ACCOUNTS §4）。
const (
	// MinUsernameLen / MaxUsernameLen 与 DDL 的 varchar(20) 一致。
	MinUsernameLen = 3
	MaxUsernameLen = 20
	// MaxDisplayNameLen 与 DDL 的 varchar(32) 一致。
	MaxDisplayNameLen = 32
)

// UsernamePattern 是新注册时的白名单：ASCII 字母/数字/下划线，3~20 个字符。
//
// ⚠️ 放宽到 Unicode 的逻辑在 **Login** 路径单独实现（不在这层做全局检查），
// 原因是"允许中文 username"是迁移兼容的**特例**，不是通用规则。新用户注册
// 仍走这条 ASCII 白名单，避免同形字（西里尔 а vs 拉丁 a）与 Bidi 漏洞带来的风险。
var UsernamePattern = regexp.MustCompile(`^[A-Za-z0-9_]{3,20}$`)

// 会话与哈希的常量。
const (
	// refreshTokenBytes 是 refresh token 的随机熵。32 字节 = 256 bit：
	// 它是 30 天有效的长期凭据，必须不可猜测（库里只存哈希，泄漏面只剩下发那一次）。
	refreshTokenBytes = 32

	// oneTimeTokenBytes 是一次性邮件令牌的随机熵。与 refresh 同规格（32 字节 = 256 bit）：
	// 它同样是一串"拿到就能改密码"的凭据，只是有效期短得多。
	oneTimeTokenBytes = 32

	// emailBrandName 是邮件主题里的品牌名（与 SMTP 的显示名 PR_SMTP_FROM_NAME 默认值一致）。
	// 单独一个常量而不是读配置：主题是**文案**，收件人看到的应该是站点名，
	// 而不是运维给邮件通道起的名字。
	emailBrandName = "月喵"

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
	// UserByEmail 按邮箱查找（找回密码的入口）。查询侧在一期就归一化到 lower(email)，
	// 与 users_email_lower_key 这个函数索引同口径。
	UserByEmail(ctx context.Context, email string) (*store.User, error)
	// SetUserEmail 改邮箱并同时决定验证状态（verifiedAt 非 nil = 这次改动本身就是证明）。
	SetUserEmail(ctx context.Context, id int64, email string, verifiedAt *time.Time) error
	SetUserStatus(ctx context.Context, id int64, status, reason string) error
	SetUserRole(ctx context.Context, id int64, role string) error
	// UpdateDisplayName 改昵称（wayfinder #33 的 PATCH /api/me）。
	//
	// 与 SetUserRole 并列而不是复用 CreateUser：改昵称是**局部**更新，
	// 走一遍 CreateUser 的整行校验（口令哈希非空、邮箱形状、建号不能带验证时间）
	// 会让"只想改个名字"被一堆与它无关的规则挡住。
	UpdateDisplayName(ctx context.Context, id int64, displayName string) error
	// SetPasswordHash 直接换口令哈希（OTP 改密路径用；链接令牌那条走
	// ConsumePasswordReset，它在同一个事务里连撤销会话一起做）。
	SetPasswordHash(ctx context.Context, id int64, hash string) error
	BumpTokenVersion(ctx context.Context, id int64) (int, error)
	TouchLastSeen(ctx context.Context, id int64, at time.Time) error

	// —— 会话（refresh token）
	CreateSession(ctx context.Context, sess *store.Session) error
	SessionByTokenHash(ctx context.Context, hash string) (*store.Session, error)
	RevokeSession(ctx context.Context, id int64) error
	RevokeAllSessions(ctx context.Context, userID int64) (int64, error)

	// —— 一次性邮件令牌（验证邮箱 / 重置密码）
	//
	// 两个消费方法都是**原子的**（校验 + 标记已用在同一条语句里，且跨表动作在一个
	// 事务内）：这是"单次使用"与"改密 + 撤销全部会话 + 版本号自增"能成立的唯一形态。
	InsertPasswordReset(ctx context.Context, t *store.PasswordReset) error
	ConsumePasswordReset(ctx context.Context, tokenHash, newPasswordHash string) (int64, bool, error)
	ConsumeEmailVerify(ctx context.Context, tokenHash string) (int64, bool, error)

	// —— 邮箱验证码（OTP：注册两段式 + 改密码）
	//
	// 与上面两条链路的区别只在形态：OTP 短（6 位）、要人肉输入、因此需要错误计数。
	// 计数落库（见 store/otp.go）：放内存的话重启就清零，6 位数字就变成可爆破的。
	InsertEmailOTP(ctx context.Context, t *store.PasswordReset) error
	ConsumeEmailOTP(ctx context.Context, userID int64, otpHash string, maxAttempts int) (bool, error)
	CountRecentEmailOTP(ctx context.Context, userID int64, since time.Time) (int64, error)
	LastEmailOTPSentAt(ctx context.Context, userID int64) (time.Time, error)

	// —— 注册草稿（两段式注册的中间态）
	InsertRegisterDraft(ctx context.Context, d *store.RegisterDraft) error
	DraftByTicket(ctx context.Context, ticketHash string) (*store.RegisterDraft, error)
	AttachDraftOTP(ctx context.Context, ticketHash, email, otpHash string, expiresAt time.Time) error
	ConsumeDraftOTP(ctx context.Context, ticketHash, otpHash string, maxAttempts int) (*store.RegisterDraft, bool, error)

	// —— 幂等键（写操作的重放保护）
	ReserveIdempotencyKey(ctx context.Context, scope, key, requestHash string, ttl time.Duration) (*store.IdempotencyRecord, error)
	CompleteIdempotencyKey(ctx context.Context, scope, key string, status int, response []byte) error
	ReleaseIdempotencyKey(ctx context.Context, scope, key string) error

	// —— 审计
	InsertAudit(ctx context.Context, a *store.AdminAudit) error

	// —— 异步写入器（§9）
	EventWriter() *store.Writer
}

// MailSink 是发信队列的**窄出口**（*mail.Mailer 满足它）。
//
// 为什么是接口：
//  1. 单测要注入一个**捕获型**假实现 —— 库里只存 sha256，而"点开链接"这条路径
//     需要明文 token，捕获发信内容是唯一既真实又不破坏安全契约的拿法
//     （绝不为了测试方便让接口回显 token）；
//  2. 单测还要注入一个**会阻塞**的假实现，用来断言 handler 不等待发信（不变量 I4）。
//
// 返回值的语义见 mail.Mailer.Enqueue：**它只用于日志**。让"入队失败"变成对外
// 可见的差异，就等于给攻击者一个探测服务端状态的反馈通道。
type MailSink interface {
	Enqueue(msg mail.Outbound) bool
}

// AccountService 是账号用例服务。
//
// 它拿到的都是接口/值，没有全局状态：同一个进程里可以并存多个实例
// （单测就是这么用的），"共享状态"只有走 AccountStore 的那一份。
type AccountService struct {
	store  AccountStore
	signer *auth.Signer
	ticket *auth.TicketStore

	// mail 是发信队列；**nil = 邮件能力未配置**（PR_SMTP_HOST 或账号密码为空）。
	// 这是与"账号能力关闭"同一套显式降级：相关端点返回 503，账号本身照常可用。
	mail MailSink

	// bcryptCost 是注册时的哈希代价（登录校验读的是库里的哈希，与它无关）。
	bcryptCost int
	// refreshTTL 是新建会话的有效期（与 PR_REFRESH_TTL 同源，由装配层注入）。
	refreshTTL time.Duration

	// resetTTL / emailVerifyTTL 是两类一次性令牌的有效期
	//（PR_RESET_TTL / PR_EMAIL_VERIFY_TTL，由装配层注入）。
	resetTTL       time.Duration
	emailVerifyTTL time.Duration
	// otpTTL 是邮箱验证码的有效期（分钟级）。比链接令牌短得多，
	// 因为 6 位数字的猜测空间只有 100 万，时间越短越安全。
	otpTTL time.Duration
	// otpDevEcho = true 时，发码接口的响应里会带上明文验证码（**仅本机开发**）。
	//
	// 它存在的唯一理由：本机通常没有 SMTP，而"注册 → 验证码 → 建号"这条链路
	// 必须能被端到端验证一次。生产必须关（装配层会拒绝"开着且配了 SMTP"的组合）。
	otpDevEcho bool
	// publicBaseURL 是拼链接用的对外基地址（PR_PUBLIC_BASE_URL）。
	// **绝不从请求头推导** —— 理由见 config.AuthConfig.PublicBaseURL 的说明。
	publicBaseURL string

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

	// —— 邮件链路（找回密码 / 验证邮箱）。四个都是必填的：装不出"半条"邮件链路。

	// Mail 是发信队列；**可以为 nil**（= 邮件能力未配置）。nil 时相关用例返回
	// mail.ErrMailUnavailable（handler 翻译成 503），其余账号功能完全不受影响。
	Mail MailSink
	// ResetTTL / EmailVerifyTTL 是两类一次性令牌的有效期；<= 0 报错。
	// 与 RefreshTTL 同一条理由：TTL 写错的表现是"链接点了没用"，极难归因。
	ResetTTL       time.Duration
	EmailVerifyTTL time.Duration
	// PublicBaseURL 是对外链接的基地址；为空报错（没有它拼不出可用的链接，
	// 而"用户收到一封点了没用的信"比启动失败晚得多、也难查得多）。
	PublicBaseURL string
	// OTPTTL 是邮箱验证码的有效期；<= 0 报错。
	//
	// 它比 ResetTTL / EmailVerifyTTL 短得多（分钟级）：验证码只有 6 位数字，
	// 时间窗口是它抵御爆破的主要维度之一（另外两道是错误次数与冷却期）。
	OTPTTL time.Duration
	// OTPDevEcho 打开后，发码接口会把明文验证码一并返回（**仅本机开发**）。
	//
	// ⚠ 它废掉"证明邮箱归属"这件事。装配层会拒绝"开着它且 SMTP 已配置"的组合。
	OTPDevEcho bool
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
	// 邮件链路的三项**无论邮件能力是否开启都要校验**：它们描述的是"链接长什么样、
	// 活多久"，配错了以后即便将来打开邮件能力也仍然是错的，而那种错要等到
	// 第一封邮件被点开才暴露。
	if opts.ResetTTL <= 0 {
		return nil, fmt.Errorf("account: 重置令牌 TTL 必须为正，当前 %s", opts.ResetTTL)
	}
	if opts.EmailVerifyTTL <= 0 {
		return nil, fmt.Errorf("account: 验证令牌 TTL 必须为正，当前 %s", opts.EmailVerifyTTL)
	}
	if strings.TrimSpace(opts.PublicBaseURL) == "" {
		return nil, errors.New("account: 缺少 PR_PUBLIC_BASE_URL（没有它拼不出可用的邮件链接）")
	}
	if opts.OTPTTL <= 0 {
		return nil, fmt.Errorf("account: 验证码 TTL 必须为正，当前 %s", opts.OTPTTL)
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
		store:          st,
		signer:         signer,
		ticket:         tickets,
		mail:           opts.Mail,
		bcryptCost:     cost,
		refreshTTL:     opts.RefreshTTL,
		resetTTL:       opts.ResetTTL,
		emailVerifyTTL: opts.EmailVerifyTTL,
		otpTTL:         opts.OTPTTL,
		otpDevEcho:     opts.OTPDevEcho,
		publicBaseURL:  strings.TrimRight(strings.TrimSpace(opts.PublicBaseURL), "/"),
		hashing:        make(chan struct{}, conc),
		now:            now,
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
	ID          int64   `json:"id"`
	Username    string  `json:"username"`
	DisplayName string  `json:"displayName"`
	Email       *string `json:"email,omitempty"`
	// EmailVerified 说明这个邮箱能不能用于找回密码（见 Forgot 的门控）。
	//
	// 为什么必须对外可见：前端要在账号页上提示"邮箱未验证 → 去验证"，
	// 而"能不能找回"是用户唯一能采取行动的依据。它**不是**授权依据
	//（授权判定只发生在服务端，见不变量 I3），只是一个显示用的派生值。
	EmailVerified bool       `json:"emailVerified"`
	Role          string     `json:"role"`
	Status        string     `json:"status"`
	CreatedAt     time.Time  `json:"createdAt"`
	LastSeenAt    *time.Time `json:"lastSeenAt,omitempty"`
}

// ProfileOf 把库里的用户转成对外档案（唯一出口，保证 PasswordHash 不外泄）。
func ProfileOf(u *store.User) Profile {
	if u == nil {
		return Profile{}
	}
	p := Profile{
		ID:            u.ID,
		Username:      u.Username,
		DisplayName:   u.DisplayName,
		Email:         u.Email,
		EmailVerified: u.Email != nil && u.EmailVerifiedAt != nil,
		Role:          u.Role,
		Status:        u.Status,
		CreatedAt:     u.CreatedAt,
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
// 顺序是刻意的：先走参数校验（廉价）→ 再哈希（昂贵）→ 再入库。
// 把校验放在哈希之前，既是"别为一个注定被拒的请求花 200ms CPU"，
// 也让弱口令/坏邮箱的失败文案能说清缺哪一条（§5 的"统一文案"只针对**登录**）。
//
// email 是**必填**的（本轮的契约变更）：账号必须有邮箱才可能走"忘记密码"。
// 既有账号（注册时不收邮箱的那一批）由管理员绑定邮箱补齐 —— 见 BindUserEmail。
//
// ⚠ 注册侧的**有界枚举面**（这是刻意的取舍，不是漏了防护）：
// 邮箱唯一约束被撞上时必须回一个能区分于"用户名被占用"的错误，用户才知道该换邮箱
// 还是换用户名；这确实让人能拿 /api/auth/register 去问"这个邮箱注册过没有"。
// 接受它的理由：注册本来就有每 IP 5/分钟（容量 3）的令牌桶，一次探测要占掉一个
// 注册配额，而注册比登录贵得多（一次 bcrypt）。**与 /api/auth/forgot 的"恒定 200"
// 形成对照** —— 找回入口免登录、不限"写"，所以它必须逐字相同；注册入口是写操作、
// 有限速、且必须给用户可操作的反馈。改这里的文案之前请先读这一段。
func (s *AccountService) Register(ctx context.Context, username, displayName, password, email, ip, ua string) (*Session, error) {
	name, err := CanonicalUsername(username)
	if err != nil {
		return nil, err
	}
	display, err := SanitizeDisplayName(displayName, name)
	if err != nil {
		return nil, err
	}
	addr, err := CanonicalEmail(email)
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

	// 同步写（见文件头部的同步写清单 ①）：userID 是后面一切的前提。
	u := &store.User{
		Username:     name,
		DisplayName:  display,
		PasswordHash: hash,
		Email:        &addr,
	}
	if err := s.store.CreateUser(ctx, u); err != nil {
		return nil, err // ErrUsernameTaken / ErrEmailTaken 由 store 映射，本文件用别名透传
	}

	sess, err := s.issueSession(ctx, u, ip, ua)
	if err != nil {
		return nil, err
	}

	// 注册后立刻发一封验证信（**best-effort**）：它既不是注册成功的前提，
	// 失败也不该改变注册的响应 —— 所以放在 issueSession 之后，且忽略返回值。
	// 能力关闭时 dispatchEmailToken 只记一条日志（账号仍然可用，只是暂时不能找回）。
	if _, err := s.dispatchEmailToken(ctx, u, addr, store.PurposeEmailVerify, ip, ua); err != nil &&
		!errors.Is(err, mail.ErrMailUnavailable) {
		log.Printf("[WARN] 注册后发送验证邮件失败：user=%d %v", u.ID, err)
	}
	return sess, nil
}

// Login 校验口令并建立会话。
//
// 三条不变量：
//  1. "用户不存在"与"口令错误"返回**同一个** ErrInvalidCredentials；
//  2. 两者都跑一次 bcrypt（用户不存在时跑一个固定的无效哈希）：否则响应耗时
//     本身就把它拆成了两个不同的结果（时序侧信道）；
//  3. 封禁账号在**口令校验通过之后**才报 ErrUserBanned —— 先判状态会让
//     "这个账号被封了"变成一条不需要口令的枚举手段。
//
// Login 校验口令并建立会话。
//
// 三条不变量：
//  1. "用户不存在"与"口令错误"返回**同一个** ErrInvalidCredentials；
//  2. 两者都跑一次 bcrypt（用户不存在时跑一个固定的无效哈希）：否则响应耗时
//     本身就把它拆成了两个不同的结果（时序侧信道）；
//  3. 封禁账号在**口令校验通过之后**才报 ErrUserBanned —— 先判状态会让
//     "这个账号被封了"变成一条不需要口令的枚举手段。
//
// ⚠ 迁移兼容分支（第 4 条）：库里存的是旧项目（yachiyo）的 MD5 哈希时，先按
// bcrypt 试（必然失败），再按 MD5 试；MD5 通过后**立刻把明文重新算成 bcrypt 落库**，
// 于是每一行 MD5 最多被读一次、这条路径会随用户活跃自然清空。它不破上面三条：
// 分流发生在"已经确认口令正确"之后，不增加任何可区分"账号存在与否"的观测面。
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
	// 旧项目的 MD5 哈希：bcrypt 那条路必然不通，走形状识别出来的第二条路。
	legacyHit := false
	if !ok && auth.IsLegacyMD5Hash(u.PasswordHash) {
		// 上面那次 bcrypt 校验对畸形哈希是**解析阶段就返回**的，耗时远小于一次真实
		// bcrypt；不补齐的话，"错口令打旧账号"会比"错口令打新账号 / 打不存在的账号"
		// 快出一个数量级，而那个时间差等于把"哪些账号来自旧项目"变成可枚举的情报
		// （不变量 1/2 要求的正是这两者不可区分）。所以补一次与 dummy 等价的 CPU 消耗。
		if busyErr := s.verifyDummy(password); busyErr != nil {
			return nil, busyErr
		}
		legacyHit = auth.VerifyLegacyMD5(u.PasswordHash, password)
		ok = legacyHit
	}
	if !ok {
		return nil, ErrInvalidCredentials
	}
	if u.Status != store.StatusActive {
		return nil, ErrUserBanned
	}

	// 迁移升级：口令已确认为明文，就地换成 bcrypt（异步写，I4；
	// 写失败只记日志，下次登录会再试一次 —— 这个动作是幂等的）。
	if legacyHit {
		s.submitLegacyPasswordUpgrade(u.ID, u.Username, password)
	}

	// last_seen 是"活跃度"而非授权依据 → 异步写（§9）。失败只记日志：
	// 一个心跳写不进去不该让登录失败。
	s.submit("更新 last_seen", func(ctx context.Context) error {
		return s.store.TouchLastSeen(ctx, u.ID, s.now())
	})

	return s.issueSession(ctx, u, ip, ua)
}

// submitLegacyPasswordUpgrade 把一行旧项目的 MD5 口令哈希换成 bcrypt。
//
// 为什么不改会话与 token_version：这**不是改密**（用户手里那串口令没变），
// 只是存储格式的升级。若在这里撤销全部会话，用户会在登录成功的那一刻被踢下线。
//
// 为什么先哈希再交进队列：哈希是贵的那一步（cost 内），放在闸门里做能复用
// 既有的并发上限；库里那一行的写入才是丢进异步的部分。
func (s *AccountService) submitLegacyPasswordUpgrade(userID int64, username, plain string) {
	hash, err := s.acquireAndHash(plain)
	if err != nil {
		// 闸门满 / 哈希失败：保持 MD5 不动，本次登录照常成功（下次再升级）。
		log.Printf("[WARN] 旧口令升级未哈希（user=%d）：%v", userID, err)
		return
	}
	s.submit("升级旧口令哈希", func(ctx context.Context) error {
		if err := s.store.SetPasswordHash(ctx, userID, hash); err != nil {
			return err
		}
		log.Printf("[INFO] 旧项目口令已升级为 bcrypt：user=%d username=%s", userID, username)
		return nil
	})
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
// 两步都要做，且都不能异步（见文件头部的同步写清单 ② 与 ③）：撤销会话只让 refresh
// 失效，而**已经签发的 access token** 要到 token_version 变化才失效（§5 的即时性要求）。
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

// PublicProfile 是**他人视角**的公开档案（wayfinder #33 的 GET /api/users/:id）。
//
// 与 Profile 是两个类型而不是"同一个类型少填几个字段"，这是本票最重要的一条决定：
// 字段白名单必须是**类型层面**的事实，不能靠"记得别填"。Profile 带 email / status /
// lastSeenAt —— 那是本人的档案，其中 email 是他自己的联系方式、status 是封禁状态。
// 把两者混成一个结构，"公开档案泄露了谁的邮箱"就只取决于某处有没有少赋一个字段，
// 而那类遗漏在 code review 里几乎看不出来（#33 验收第 2 条要的就是钉住它）。
//
// 字段与各自的理由：
//   - ID / Username：公开档案页的地址与"这个人叫什么"。用户名本来就已公开 ——
//     #39 的 `GET /api/users/search` 就是按 username 做子串匹配并把它回给调用方的。
//   - DisplayName：显示名。
//   - Role：只回 `user` | `admin` 的**标签**。管理端的真实边界是服务端 RequireAdmin
//     （不变量 I3），而"这个人是管理员"在社区里本来就是公开事实（界面要显示徽章）。
//     它与 status 不同：后者是**惩罚**记录，前者是**职责**标签。
//   - CreatedAt：公开档案页的"加入时间"（社区类产品普遍展示）。
//
// ⚠ **刻意不返回 `avatarChar` / `avatarColor`**（与票面的一处偏离，理由必须写清）：
// 票面把它们列进了白名单，但仓库既有的约定是**服务端契约不掺显示层**（#26 定的，
// #28/#31/#37 全部照办）—— 头像首字母与取色是纯 UI 派生，`client/src/utils/avatar.ts`
// 里那 5 行就是它。它们只需要 `id` + `displayName`，而这两个字段本响应已经给了，
// 前端调 `avatarOf(id, displayName)` 就能得到**逐字相同**的结果。
//
// 在 Go 里再实现一份的代价不是"多写几行"，而是**色板有了两个源**：改一处不改另一处
// 会让"同一个人在帖子页与档案页头像颜色不同"，而这种不一致没有编译期检查、
// 也不会有任何测试失败。本票的验收第 2 条要的是"字段白名单被钉住"，
// 而少两个字段是**更严**的白名单，不是漏做。
//
// 刻意**不在**这里的三样，以及各自的理由：
//   - Email：他人的联系方式。泄露它就是给垃圾邮件/社工一个精准名单。
//   - Status：被封禁是账号与平台之间的事，公开它会变成一种羞辱性标签
//     （而且封禁常常是可逆的，公开后无法收回）。
//   - LastSeenAt：在线行为画像。ACCOUNTS §13 的隐私口径是"不在线态之外不暴露活跃时间"。
type PublicProfile struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
	// CreatedAt 是加入时间（RFC3339）。用字符串而不是 time.Time：
	// 公开档案是纯展示页，前端只需要能格式化，不需要时区语义。
	CreatedAt string `json:"createdAt"`
}

// PublicProfileOf 把库里的用户转成**他人视角**的公开档案（唯一出口）。
//
// 与 ProfileOf 并列而不是"给它加个参数"：两条出口的字段集不同，而"少填一个字段"
// 在 Go 里没有任何编译期检查 —— 写成两个函数，至少让"公开档案有哪些字段"是一个
// 可以一眼读完的地方（public_profile_test.go 的字段白名单用例钉住它）。
func PublicProfileOf(u *store.User) PublicProfile {
	if u == nil {
		return PublicProfile{}
	}
	return PublicProfile{
		ID:          u.ID,
		Username:    u.Username,
		DisplayName: u.DisplayName,
		Role:        u.Role,
		CreatedAt:   u.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// PublicProfile 读某个用户的公开档案（wayfinder #33）。
//
// **不存在与被封禁都回 ErrUserNotFound**（本票定的统一口径）。理由与
// ErrInvalidCredentials 的"不区分用户名/口令错"是同一条：能区分它们就等于开了一条
// 存在性探测通道 —— 攻击者可以拿一批 id 扫过去，把"这个 id 存在但被封了"与
// "这个 id 从来没存在过"分开，前者是值得继续挖的目标。而公开档案是**免登录**端点，
// 那正是最容易被批量扫的入口。
//
// 代价（写在注释里，因为它是刻意的）：被封禁用户的公开档案会像不存在一样 404，
// 于是"他的帖子还在但主页 404"是一种可能出现的组合。这是可接受的：封禁的本意
// 就是让这个账号在社区里不可见，而帖子侧另有自己的处置（本期不做级联）。
func (s *AccountService) PublicProfile(ctx context.Context, userID int64) (PublicProfile, error) {
	if userID <= 0 {
		return PublicProfile{}, ErrUserNotFound
	}
	u, err := s.store.UserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return PublicProfile{}, ErrUserNotFound
		}
		return PublicProfile{}, err
	}
	if u.Status != store.StatusActive {
		return PublicProfile{}, ErrUserNotFound
	}
	return PublicProfileOf(u), nil
}

// UpdateMyDisplayName 改自己的昵称（wayfinder #33 的 PATCH /api/me）。
//
// 三步，顺序是有意的：
//
//  1. **清洗**（SanitizeDisplayName：StripControl → TrimSpace → 限长 32）。
//     清洗后为空时回落到用户名 —— 这是 §7.3 的既有规则（昵称是"显示给人看的"，
//     为空会让成员列表出现一行空白），而本方法把 username 传进去当 fallback。
//  2. **落库**（store.UpdateDisplayName，只复核形状）。
//  3. **回读**并返回改动后的事实 —— 与 admin.patchUser 同一条理由：
//     返回"我们请求改成的样子"会让"清洗把某些字符吃掉了"这种事对客户端不可见。
//
// ⚠ **不动 token_version**：昵称不是授权事实（不变量 I3 的推论）。给它 +1 会让
// 用户改个名字就把自己所有设备踢下线，而那种"莫名其妙被登出"极难归因。
//
// 审计：**不写**。它与 #29 的六个好友端点同一条判据 —— 改的是自己的资料、
// 无第三方需追责、且完全可逆（再改一次就回去了）。#39 的"解除拉黑"要审计是因为
// 它改变了**别人**的处境。
func (s *AccountService) UpdateMyDisplayName(ctx context.Context, userID int64, displayName string) (Profile, error) {
	if userID <= 0 {
		return Profile{}, ErrUserNotFound
	}
	// 先读一次拿 username（清洗的 fallback 要用它），顺带确认账号还在且未被封禁。
	u, err := s.store.UserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Profile{}, ErrUserNotFound
		}
		return Profile{}, err
	}
	if u.Status != store.StatusActive {
		return Profile{}, ErrUserForbidden
	}

	cleaned, err := SanitizeDisplayName(displayName, u.Username)
	if err != nil {
		return Profile{}, err
	}
	if err := s.store.UpdateDisplayName(ctx, userID, cleaned); err != nil {
		return Profile{}, err
	}

	// 回读：清洗可能改过值（去零宽、去首尾空白、回落成用户名），
	// 客户端应当拿到**库里现在是什么**，而不是它请求写什么。
	after, err := s.store.UserByID(ctx, userID)
	if err != nil {
		return Profile{}, err
	}
	return ProfileOf(after), nil
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
	// 版本号自增必须与状态变更同一条请求路径（同步写清单 ③）：否则"已封禁但旧 access token
	// 还能用"会留下一个长度不确定的窗口（§5 的"封禁即时生效"）。
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

// BindUserEmail 是管理员的**邮箱绑定**（口径 ②：给既有账号补齐邮箱）。
//
// 为什么管理员绑定即视为已验证（verifiedAt = now()）：管理员是可信主体 ——
// 他本来就能改任意账号的角色与状态，而要求他去收一封验证信只会让"给老账号补邮箱"
// 这条通路在实践中不可用。代价与边界：这确实让管理员能替用户声明一个归属；
// 而新增的**写入面**只有 users.email 一列（审计留痕：user.email_bind）。
//
// email 传空串 = 解绑（同时清掉 email_verified_at，见 store.SetUserEmail）。
//
// 审计**只在实际变更时**写：把一次 no-op 的 PATCH 记成一条 user.email_bind
// 会让审计表里出现"看似绑定过、其实什么都没改"的行，那是审计噪声而不是证据。
// 判"有没有变"必须同时看两列：邮箱相同但**从"未验证"变成"已验证"**同样是变更。
func (s *AccountService) BindUserEmail(ctx context.Context, actorID, targetID int64, email, ip string) error {
	if targetID <= 0 {
		return errors.New("account: 目标账号不合法")
	}
	// 空串是"解绑"，此时跳过格式校验（空不是"格式错的邮箱"）。
	addr := ""
	if strings.TrimSpace(email) != "" {
		canonical, err := CanonicalEmail(email)
		if err != nil {
			return err
		}
		addr = canonical
	}

	u, err := s.store.UserByID(ctx, targetID)
	if err != nil {
		return err
	}
	unchanged := (u.Email == nil && addr == "") ||
		(u.Email != nil && addr != "" && *u.Email == addr && u.EmailVerifiedAt != nil)
	if unchanged {
		return nil
	}

	now := s.now()
	var verifiedAt *time.Time
	action := "user.email_unbind"
	if addr != "" {
		verifiedAt = &now
		action = "user.email_bind"
	}
	if err := s.store.SetUserEmail(ctx, targetID, addr, verifiedAt); err != nil {
		return err
	}

	s.submit("写绑定邮箱审计", func(ctx context.Context) error {
		return s.store.InsertAudit(ctx, &store.AdminAudit{
			ActorID:    actorID,
			Action:     action,
			TargetType: "user",
			TargetID:   fmt.Sprintf("%d", targetID),
			// detail 里放**变更后的事实**（不是"请求改成的样子"）：审计要回答
			// "当时这个账号的邮箱是什么"，所以解绑也记一行 email="" 而不是省略字段。
			Detail: auditDetail(map[string]any{
				"email":          addr,
				"email_verified": addr != "",
			}),
			IP: optString(ip),
		})
	})
	return nil
}

// —— 内部：会话签发。

// issueSession 建立一条新会话并签发 access token。
//
// refresh 会话的 INSERT 是**同步**的（见文件头部的同步写清单 ①）。
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

// createSession 写入一行会话（同步，见文件头部的同步写清单 ①）。
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

// HashOpaqueToken 计算**一次性邮件令牌**的落库形态。
//
// 它逐字就是 HashRefreshToken（同一套算法、同一套理由），单独给一个名字只是为了让
// password_resets 的调用点读起来不像"在哈希一个 refresh token"。
// 为什么不直接改名：HashRefreshToken 是既有的公开函数，改名会波及 handler 与既有测试；
// 而"两个名字指向同一个实现"在这里是零风险的 —— 它们**必须**是同一个函数，
// 否则两处令牌的落库形态会分叉，而那种分叉不会有任何报错。
func HashOpaqueToken(token string) string { return HashRefreshToken(token) }

// newRefreshToken 生成一个不透明的 refresh token 及其哈希。
func newRefreshToken() (token string, hash string, err error) {
	return newRandomToken(refreshTokenBytes)
}

// newOpaqueToken 生成一个一次性邮件令牌及其哈希（与 refresh 同规格的随机熵）。
func newOpaqueToken() (token string, hash string, err error) {
	return newRandomToken(oneTimeTokenBytes)
}

// newRandomToken 是两类不透明令牌**唯一**的生成实现。
//
// 用 base64.RawURLEncoding：token 要进 Cookie 或 URL 的 fragment，
// 标准 base64 的 +/= 在这两处都需要额外转义，多一层编码就多一处"某一端忘了转义"的坑。
// 这也是重置链接能直接写成 `#token=<原样串>` 而不用再 escape 一遍的原因。
func newRandomToken(n int) (token string, hash string, err error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("account: 生成不透明令牌失败: %w", err)
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
