package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// 账号注销的冷静期（票 #73 / 决议 #51 §5）。
//
// # 为什么是"打标记 + 后台清扫"而不是"立刻删"
//
// 用户点下注销的那一刻，最有价值的判断依据是"他会不会后悔"。立刻硬删等于把
// 一次误点变成不可挽回的数据丢失（帖子、评论、关注边、收藏、私聊全部 CASCADE
// 带走，没有备份路径）。所以这里只写一列时间戳，真正的删除交给
// `PurgeExpiredDeletions` —— 它在后台按批跑，**不在任何请求路径上**（铁律 I4）。
//
// # 为什么这一列就是全部状态
//
// `deletion_requested_at` 非空 = 在冷静期；值 = 起点；`+ grace` = 到期时刻；
// 撤销 = 置回 NULL。再加 `deleted_at` / `is_deleted` 会造出"两列都非空"这种
// 没有语义的中间态，而没有任何用例能覆盖它。
//
// # 为什么不动 status
//
// 冷静期内用户**必须还能登录**（否则撤销不了），而 `status` 同时是登录门
// （`Login` 拒 banned）与对外可见性门。混用会让"自己要走"与"被封"在审计里
// 无法区分。所以 `status` 保持 `active`，对外隐藏靠 `deletion_requested_at IS NULL`
// 这条谓词（与 `status = 'active'` 一起构成"可见账号"的唯一真源）。
//
// # 为什么 30 天
//
// 决议 #51 定的产品值。它的作用是给"冲动注销"一个反悔窗口，同时也是
// 数据保留声明里"我们最多保留 30 天"的承诺。

// ErrDeletionNotRequested 表示"这个账号不在冷静期里"。
//
// 撤销端点用它区分两种失败：账号不存在（ErrNotFound）与"本来就没申请注销"。
// 后者不该静默成功 —— 界面上的"撤销注销"按钮只会在横幅存在时出现，
// 若它返回 204，用户会以为撤销了而实际什么也没发生。
var ErrDeletionNotRequested = errors.New("store: 该账号没有待执行的注销")

// DeletionState 是注销相关的只读事实（给 /me 的横幅用）。
//
// RequestedAt 为 nil 表示不在冷静期，此时 Deadline 也无意义。
type DeletionState struct {
	RequestedAt *time.Time
	Deadline    *time.Time
}

// Pending 表示"正在冷静期里"。
func (s DeletionState) Pending() bool { return s.RequestedAt != nil }

// DeletionStateOf 从用户行算出注销状态；grace <= 0 时 Deadline 留空。
//
// 抽成函数而不是在 handler 里算：截止时刻 = 起点 + 宽限期，而宽限期是配置
// （`PR_DELETION_GRACE`）。让 handler 自己加一遍，等于把"多久算到期"
// 变成两处实现 —— 而横幅显示的日期与清扫实际动手的时刻必须一致。
func DeletionStateOf(u *User, grace time.Duration) DeletionState {
	if u == nil || u.DeletionRequestedAt == nil {
		return DeletionState{}
	}
	at := *u.DeletionRequestedAt
	st := DeletionState{RequestedAt: &at}
	if grace > 0 {
		d := at.Add(grace)
		st.Deadline = &d
	}
	return st
}

// RequestAccountDeletion 把账号置入冷静期，返回（可能是既有的）起点时刻。
//
// # 幂等且**不延期**
//
// 已经在冷静期里再调一次，返回原来的起点而不是把它推后 —— 否则用户每刷新
// 一次页面就多 30 天，而"到期真删"永远不会发生（每次访问都在续命）。
// 这个选择也让端点天然幂等，双击不会改变任何状态。
//
// # 只写一列
//
// 刻意不在这里撤销会话或 +1 token_version：冷静期内用户要能登录看横幅、点撤销。
// 会话的处置留给真正的删除（那时整行 CASCADE 带走，sessions 也在其中）。
func (s *Store) RequestAccountDeletion(ctx context.Context, userID int64, at time.Time) (time.Time, error) {
	if userID <= 0 {
		return time.Time{}, ErrNotFound
	}
	if at.IsZero() {
		at = time.Now()
	}

	// 先读一次：已在冷静期就原样返回（不延期），也顺便区分"没这个账号"。
	var row User
	err := s.db.WithContext(ctx).
		Select("id, deletion_requested_at").
		Where("id = ?", userID).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return time.Time{}, ErrNotFound
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("store: 查询注销状态失败: %w", err)
	}
	if row.DeletionRequestedAt != nil {
		return *row.DeletionRequestedAt, nil
	}

	// 条件里再判一次 NULL：两个并发请求都读到"未注销"时，只有先到的那个
	// 真正写入。第二个拿到 RowsAffected=0，于是回读既有值（与上面同一条语义）。
	res := s.db.WithContext(ctx).Model(&User{}).
		Where("id = ? AND deletion_requested_at IS NULL", userID).
		Update("deletion_requested_at", at)
	if res.Error != nil {
		return time.Time{}, fmt.Errorf("store: 写入注销申请失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		// 要么并发先写入了，要么行刚被删。回读一次给出准确答案。
		var again User
		if err := s.db.WithContext(ctx).Select("id, deletion_requested_at").
			Where("id = ?", userID).Take(&again).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return time.Time{}, ErrNotFound
			}
			return time.Time{}, fmt.Errorf("store: 回读注销状态失败: %w", err)
		}
		if again.DeletionRequestedAt != nil {
			return *again.DeletionRequestedAt, nil
		}
		return time.Time{}, ErrNotFound
	}
	return at, nil
}

// CancelAccountDeletion 撤销注销申请（置回 NULL）。
//
// 不在冷静期里返回 ErrDeletionNotRequested（而不是静默成功），理由见该错误。
func (s *Store) CancelAccountDeletion(ctx context.Context, userID int64) error {
	if userID <= 0 {
		return ErrNotFound
	}
	res := s.db.WithContext(ctx).Model(&User{}).
		Where("id = ? AND deletion_requested_at IS NOT NULL", userID).
		Update("deletion_requested_at", nil)
	if res.Error != nil {
		return fmt.Errorf("store: 撤销注销申请失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		// 区分"没这个账号"与"本来就没申请"：前者 404，后者 409 语义。
		var count int64
		if err := s.db.WithContext(ctx).Model(&User{}).
			Where("id = ?", userID).Count(&count).Error; err != nil {
			return fmt.Errorf("store: 检查账号存在性失败: %w", err)
		}
		if count == 0 {
			return ErrNotFound
		}
		return ErrDeletionNotRequested
	}
	return nil
}

// PurgeExpiredDeletions 硬删一批已过冷静期的账号，返回删除行数。
//
// # 为什么按批
//
// 一次删掉大量用户会长时间持有锁并放大单事务的回滚段（这些行还要连带
// CASCADE 删掉它们的帖子/评论/点赞/关注/收藏/私聊）。批大小由调用方给，
// 清扫协程每轮跑一次，直到返回 0 为止。
//
// # 为什么用子查询而不是一条 DELETE ... WHERE
//
// PostgreSQL 的 `DELETE ... LIMIT` 不存在。子查询先把 id 挑出来（走
// `users (deletion_requested_at)` 索引），再按 id 删 —— 两段都在同一条语句里，
// 不需要应用层先 SELECT 再 DELETE（那会引入"选中的行已被撤销"的窗口）。
//
// # 为什么要 ORDER BY
//
// 到期久的先删，让积压的账号按时间顺序消失；不排序时批处理会随机挑行，
// 长期运行后可能出现"某几个账号永远轮不到"。
//
// # I4
//
// 本方法**只能被后台清扫协程调用**，不能出现在任何 handler 里。它是一次
// 多行写入，放在请求路径上会让一次注销拖慢一个普通请求，且无法重试。
func (s *Store) PurgeExpiredDeletions(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	if limit <= 0 {
		limit = 100
	}
	if cutoff.IsZero() {
		return 0, nil
	}
	const q = `
DELETE FROM users
 WHERE id IN (
   SELECT id FROM users
    WHERE deletion_requested_at IS NOT NULL
      AND deletion_requested_at <= ?
    ORDER BY deletion_requested_at ASC
    LIMIT ?
 )`
	res := s.db.WithContext(ctx).Exec(q, cutoff, limit)
	if res.Error != nil {
		return 0, fmt.Errorf("store: 清理到期注销账号失败: %w", res.Error)
	}
	return res.RowsAffected, nil
}

// CountExpiredDeletions 统计已过期但还没被清掉的账号数（监控与用例用）。
func (s *Store) CountExpiredDeletions(ctx context.Context, cutoff time.Time) (int64, error) {
	if cutoff.IsZero() {
		return 0, nil
	}
	var n int64
	err := s.db.WithContext(ctx).Model(&User{}).
		Where("deletion_requested_at IS NOT NULL AND deletion_requested_at <= ?", cutoff).
		Count(&n).Error
	if err != nil {
		return 0, fmt.Errorf("store: 统计到期注销账号失败: %w", err)
	}
	return n, nil
}
