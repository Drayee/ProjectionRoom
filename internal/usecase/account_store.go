// 账号用例与 internal/store 之间的**适配层**（唯一 import gorm 的文件）。
//
// 为什么单独一个文件：account.go 是纯业务（不认识数据库），但异步写入器的作业签名
// 是 func(ctx, *gorm.DB) error —— 这是 GORM 的类型，业务层不该为了投递一个事件而
// 认识它。把"翻译"收敛在这一个文件里，收益是双份的：
//
//   - account.go 与其单测完全不依赖 GORM（假实现几十行就够，不需要 PostgreSQL）；
//   - "哪些 gorm 细节漏到了业务层"这个问题有了一个可以一眼看完的答案：
//     只有这里。
//
// 本文件不做任何业务判断，只做方法转发与签名翻译。
package usecase

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"ProjectionRoom/internal/store"
)

// AccountStoreAdapter 让 *store.Store 满足 AccountStore。
//
// 存在的原因只有一个：AccountStore.EventWriter() 返回的是 store 的写入器，
// 而它的 Submit 收的是带 *gorm.DB 的作业 —— 业务层投递时用的是
// func(ctx) error。适配器把后者翻译成前者，业务层因此不必 import gorm。
//
// 它必须是**值**（不被复制出语义）：内部只持有一个指针与一个接口引用，
// 没有可变状态，因此按值传递是安全的。
type AccountStoreAdapter struct {
	store  *store.Store
	writer *store.Writer
}

// NewAccountStoreAdapter 构造适配器。
//
// writer 允许为 nil：那表示"本进程不异步写事件"，此时 account.go 的 submit
// 会退化成后台协程直写（行为见 AccountService.submit 的说明）。
// 生产装配必须传真实写入器，否则 §9 的"有界队列 + 失败重试"就没有生效 ——
// 这个事实会体现在 store 的写入器指标上（队列长度/丢弃数永远是 0）。
func NewAccountStoreAdapter(st *store.Store, w *store.Writer) (*AccountStoreAdapter, error) {
	if st == nil {
		return nil, errors.New("usecase: 缺少 *store.Store")
	}
	if w != nil {
		// 挂到 Store 上：只拿到 *store.Store 的上层（管理端指标接口）也要能看见它。
		st.AttachWriter(w)
	}
	return &AccountStoreAdapter{store: st, writer: w}, nil
}

// Store 返回被包装的 *store.Store（装配层排关闭顺序时用）。
func (a *AccountStoreAdapter) Store() *store.Store { return a.store }

// —— 用户 ——

func (a *AccountStoreAdapter) CreateUser(ctx context.Context, u *store.User) error {
	return a.store.CreateUser(ctx, u)
}

func (a *AccountStoreAdapter) UserByUsername(ctx context.Context, username string) (*store.User, error) {
	return a.store.UserByUsername(ctx, username)
}

func (a *AccountStoreAdapter) UserByID(ctx context.Context, id int64) (*store.User, error) {
	return a.store.UserByID(ctx, id)
}

func (a *AccountStoreAdapter) SetUserStatus(ctx context.Context, id int64, status, reason string) error {
	return a.store.SetUserStatus(ctx, id, status, reason)
}

func (a *AccountStoreAdapter) SetUserRole(ctx context.Context, id int64, role string) error {
	return a.store.SetUserRole(ctx, id, role)
}

func (a *AccountStoreAdapter) BumpTokenVersion(ctx context.Context, id int64) (int, error) {
	return a.store.BumpTokenVersion(ctx, id)
}

func (a *AccountStoreAdapter) TouchLastSeen(ctx context.Context, id int64, at time.Time) error {
	return a.store.TouchLastSeen(ctx, id, at)
}

// —— 会话 ——

func (a *AccountStoreAdapter) CreateSession(ctx context.Context, sess *store.Session) error {
	return a.store.CreateSession(ctx, sess)
}

func (a *AccountStoreAdapter) SessionByTokenHash(ctx context.Context, hash string) (*store.Session, error) {
	return a.store.SessionByTokenHash(ctx, hash)
}

func (a *AccountStoreAdapter) RevokeSession(ctx context.Context, id int64) error {
	return a.store.RevokeSession(ctx, id)
}

func (a *AccountStoreAdapter) RevokeAllSessions(ctx context.Context, userID int64) (int64, error) {
	return a.store.RevokeAllSessions(ctx, userID)
}

// —— 审计 ——

func (a *AccountStoreAdapter) InsertAudit(ctx context.Context, aud *store.AdminAudit) error {
	return a.store.InsertAudit(ctx, aud)
}

// —— 异步写入器 ——

// EventWriter 优先返回装配时显式传入的写入器；没有则回落到 Store 上挂载的那个。
//
// 两条来源都留着，是为了让"写入器由装配层创建、但 Store 由别人 attach"与
// "本适配器自己 attach"两种装配写法都能工作，而不必让调用方记住顺序。
func (a *AccountStoreAdapter) EventWriter() *store.Writer {
	if a.writer != nil {
		return a.writer
	}
	return a.store.EventWriter()
}

// SubmitAccountJob 把一个"没有 gorm 类型参数"的作业翻译成写入器接受的形态。
//
// 它是 AccountStoreAdapter 之外的第二条路：调用方（或测试）已经拿到 *store.Writer，
// 只想投递一个与 GORM 无关的作业时用它，省掉一个适配器实例。
// w 为 nil（未挂载写入器）时返回 false —— 与 nil 写入器直接调用的一致语义。
func SubmitAccountJob(w *store.Writer, fn func(ctx context.Context) error) bool {
	if w == nil || fn == nil {
		return false
	}
	return w.Submit(func(ctx context.Context, _ *gorm.DB) error {
		// 这里**故意**忽略 tx：本包的事件写全部走 store 的仓储方法
		//（它们自己持有 *gorm.DB 与参数化查询），不使用调用方给的事务。
		// 代价是"一个作业不再是一个事务"，收益是业务层不必知道 GORM 的存在。
		// 账号层的每个事件都是单条写（一条审计 / 一次 last_seen），
		// 因此这条取舍没有把"多行必须一起成立"的语义破坏掉。
		return fn(ctx)
	})
}

// SubmitWriterJob 让 *store.Writer **直接**满足 handler 的 AdminAuditSink
// （SubmitJob(func(ctx) error) bool）。
//
// 为什么需要它（而不是让 handler 拿 *store.Writer 自己调 Submit）：
// handler 从一期起就不 import gorm（见 handler/errors.go 里 storeNotFound 别名的说明），
// 而 Submit 的参数类型是 func(ctx, *gorm.DB) error。这个一行包装把 GORM 挡在
// 已经认识它的那一层（本文件是本包唯一 import gorm 的文件），
// 于是"审计由谁写、写几次"在 handler 里仍然只表现为一次 SubmitJob 调用。
func SubmitWriterJob(w *store.Writer, fn func(ctx context.Context) error) bool {
	return SubmitAccountJob(w, fn)
}

// WriterJobs 是给 handler 用的**写队列出口**：指标 + 无 gorm 参数的投递。
//
// 为什么把它做成一个类型而不是让 handler 拿 *store.Writer：
// handler.AdminWriterStats 需要 QueueLen/Stats，handler.AdminAuditSink 需要 SubmitJob，
// 两者都是同一个对象的两个面。让本类型的**方法集**同时满足它们，
// 装配处就只需要写一次 deps.Writer = usecase.NewWriterJobs(res.Writer)。
//
// 它零语义、零状态（只持一个指针），因此按值/按指针传递都安全。
type WriterJobs struct {
	w *store.Writer
}

// NewWriterJobs 构造写队列出口；w 为 nil 时返回 nil（调用方按"没有写队列"处理）。
func NewWriterJobs(w *store.Writer) *WriterJobs {
	if w == nil {
		return nil
	}
	return &WriterJobs{w: w}
}

// SubmitJob 投递一个"只吃 context"的作业（签名翻译见 SubmitAccountJob）。
func (j *WriterJobs) SubmitJob(fn func(ctx context.Context) error) bool {
	if j == nil {
		return false
	}
	return SubmitWriterJob(j.w, fn)
}

// QueueLen 转发写入器的队列长度指标（瞬时 gauge）。
func (j *WriterJobs) QueueLen() int {
	if j == nil || j.w == nil {
		return 0
	}
	return j.w.QueueLen()
}

// Stats 转发写入器的累计统计指标。
func (j *WriterJobs) Stats() store.WriterStats {
	if j == nil || j.w == nil {
		return store.WriterStats{}
	}
	return j.w.Stats()
}

// Writer 返回被包装的写入器（装配层排关闭顺序时用）。
func (j *WriterJobs) Writer() *store.Writer {
	if j == nil {
		return nil
	}
	return j.w
}

// ProfileNameLookup 是"取昵称"的窄出口（二期 T2 的公开房列表用）。
//
// 它为什么必须是**独立于 usecase.Profile** 的一个类型：Profile 里带 email /
// role / status（那是账号档案的面），而公开房列表只需要一个字符串。
// 让 handler 拿到的唯一形态就是"一个 int64 → 一个 string"，
// 于是"列表响应里不会出现房主邮箱/角色/状态"这件事在类型层面成立，
// 而不是靠"记得别把那几个字段写进响应"。
//
// 实现细节：它走 store.UserByID（既有只读方法，返回完整 *store.User）。
//
//	为什么不用一条只 SELECT display_name 的自定义查询：那需要改动 store 的文件面
//	（本期范围冻结在 roommeta.go 一个文件）。这里如实记录代价：昵称查询会多读
//	几列（含 password_hash）到**进程内存**，但它一步都没有离开本函数 ——
//	返回给调用方的只有一个 string。数据库往返次数、查询数都没有变。
//	若将来想要"连内存都不经过"，正确的做法是在 store 侧加一个
//	DisplayNameByID(ctx, id) (string, error)（带单列 Pluck），本函数改调它即可。
type ProfileNameLookup struct {
	store *store.Store
}

// NewProfileNameLookup 构造昵称出口。st 为 nil 时返回 nil（调用方按"查不了昵称"处理：
// 公开房列表会把 ownerName 显示成空串，而不是报错）。
func NewProfileNameLookup(st *store.Store) *ProfileNameLookup {
	if st == nil {
		return nil
	}
	return &ProfileNameLookup{store: st}
}

// ProfileNameByID 返回账号的昵称。
//
// 账号不存在时返回空串 + nil：调用方（公开房列表）要的是"显示什么"，
// 而"房主账号已被删除"这件事在那一层没有任何可操作的差别 ——
// 把它们合成一个错误会让列表因为一个已删除的房主而整体失败。
//
// 真正的查询错误（连接断了/超时）原样返回，让调用方能记一条可排查的日志。
func (p *ProfileNameLookup) ProfileNameByID(ctx context.Context, id int64) (string, error) {
	if p == nil || p.store == nil || id <= 0 {
		return "", nil
	}
	u, err := p.store.UserByID(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", nil
		}
		return "", err
	}
	if u == nil {
		return "", nil
	}
	return u.DisplayName, nil
}
