package store

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// 粉丝红点（票 #67；决议 #53）。
//
// # 零新表
//
// 红点**从既有数据派生**，不建 notifications 表：`users` 加一列 `followers_seen_at`
// （"我上次看粉丝列表是什么时候"），红点数就是"我未回关的粉丝里，关注时间晚于
// 这个水位线的人数"。一条 follows 反连接查询算完。
//
// 为什么否决 notifications 表：它会成为关系事实的**第二个真源** ——
// 关注边在 follows 里、"有新粉丝"在 notifications 里，两者必然漂移
//（取关/拉黑/封禁发生在哪一侧都要同步删行，而漏一处的症状是红点亮着却点不出人）。
//
// # 与消息未读的关系
//
// **完全没有关系**。消息未读是 `stores/auth.ts` 的 `setUnread()` 一个写入点、
// 一个接口、一个清除点；粉丝红点是另一条数、另一个接口、另一个清除点。
// 两数两源，永不互相覆盖 —— 把粉丝数并进 `unread` 会让"有私信"与"有新粉丝"
// 共享同一个角标，而它们的清除时机（读消息 vs 进粉丝 tab）根本不同。
//
// # 打扰豁免
//
// 拉黑（任一方向）与封禁/注销的人一律不计。这三个条件与 #52 的关注流谓词
// 同源（`applyAuthorVisibility` 那套语义），但**不直接复用那个函数**：
// 那里是给"帖子行"用的（`authorCol` 是 posts 的列），这里过滤的是 follows 的
// `follower_id`，形状不同。语义一致、实现各一处，是本包一贯的分工
//（`applyFollowingScope` 与 `applyAuthorVisibility` 分开也是同一理由）。

// CountNewFollowers 返回"新粉丝"数：我未回关、且关注时间晚于 followers_seen_at 的人数。
//
// 三个条件缺一不可：
//
//  1. `f.followee_id = userID` —— 我是被关注的那一方（粉丝）。
//  2. `NOT EXISTS (我→TA)` —— 我还没回关。回关即互关，那条关系已经处理完了。
//  3. `f.created_at > followers_seen_at` —— 水位线之后。NULL（从没看过）时
//     等价于"很久以前"，所以**所有**未回关的粉丝都算新。
//     这一条不能写成"NULL 时返回 0"：新注册用户被第一个人关注，
//     正是红点最该亮的时刻。
func (s *Store) CountNewFollowers(ctx context.Context, userID int64) (int64, error) {
	if userID <= 0 {
		return 0, nil
	}
	var n int64
	err := s.db.WithContext(ctx).
		Table("follows AS f").
		Joins("JOIN users p ON p.id = f.follower_id").
		Where("f.followee_id = ?", userID).
		// 注销的粉丝不点亮红点（票 #73）：与名单、搜索、档案同一条口径。
		Where("p.status = ? AND p.deletion_requested_at IS NULL", StatusActive).
		Where(`NOT EXISTS (SELECT 1 FROM follows r WHERE r.follower_id = ? AND r.followee_id = f.follower_id)`, userID).
		Where(`NOT EXISTS (
			SELECT 1 FROM friendships b
			 WHERE b.status = 'blocked'
			   AND ((b.requester_id = ? AND b.addressee_id = f.follower_id)
			     OR (b.requester_id = f.follower_id AND b.addressee_id = ?))
		)`, userID, userID).
		// ⚠ 这一条的**外层括号是必须的**，不是排版。
		//
		// GORM 把每个 `Where` 的字符串原样拼进 WHERE 链，**不会**替你补括号。
		// 少了这层括号，那个 `OR` 就会把前面四个条件全部短路成左操作数：
		//
		//	(A AND B AND C AND D) OR f.created_at > seen
		//
		// 后果不是"多算一两个"—— 而是**全站任何人在水位线之后新关注了谁都算进我的红点**。
		// 真机验收抓到的症状是"互关之后红点又亮 1"（互关那条边的 created_at
		// 比我的水位线新，于是它被算了进来），当时看不出与"我"有任何关系。
		Where(`((SELECT u.followers_seen_at FROM users u WHERE u.id = ?) IS NULL
			OR f.created_at > (SELECT u.followers_seen_at FROM users u WHERE u.id = ?))`, userID, userID).
		Count(&n).Error
	if err != nil {
		return 0, fmt.Errorf("store: 统计新粉丝失败: %w", err)
	}
	return n, nil
}

// FollowersSeenAt 读回水位线；ok=false 表示从没标记过（NULL）。
//
// 分开返回 ok 而不是给一个零值时间：零值 `time.Time{}` 是 0001-01-01，
// 它恰好**不是** NULL 的语义（NULL 表示"从没看过"，而 0001 年表示
// "上次看到公元 1 年"）。让调用方看见 ok 才能正确区分这两者。
func (s *Store) FollowersSeenAt(ctx context.Context, userID int64) (time.Time, bool, error) {
	if userID <= 0 {
		return time.Time{}, false, nil
	}
	var row struct {
		At *time.Time `gorm:"column:followers_seen_at"`
	}
	err := s.db.WithContext(ctx).
		Table(tableUsers).
		Select("followers_seen_at").
		Where("id = ?", userID).
		Take(&row).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, fmt.Errorf("store: 读取粉丝水位线失败: %w", err)
	}
	if row.At == nil {
		return time.Time{}, false, nil
	}
	return *row.At, true, nil
}

// MarkFollowersSeen 把水位线推到 at（幂等：重复标记不报错，后一次生效）。
//
// 语义是"我最后一次看粉丝列表是什么时候"，所以它**只前进不后退**的直觉是对的，
// 但实现上刻意写成**无条件覆盖**：两次标记之间夹进的新粉丝，
// 应该以最后一次"我真的看了"为准。传一个比当前更早的时间在业务上不会发生
// （调用方永远传 now），而加一个 `GREATEST` 会让"时钟回拨时水位线卡住"变成
// 一个更难查的行为。
//
// 用户不存在 → ErrNotFound（与 `SetPasswordHash` 等写入函数的约定一致：
// RowsAffected == 0 说明这行不存在，不是"成功地什么都没做"）。
func (s *Store) MarkFollowersSeen(ctx context.Context, userID int64, at time.Time) error {
	if userID <= 0 {
		return ErrNotFound
	}
	if at.IsZero() {
		at = time.Now()
	}
	res := s.db.WithContext(ctx).
		Model(&User{}).
		Where("id = ?", userID).
		Update("followers_seen_at", at)
	if res.Error != nil {
		return fmt.Errorf("store: 更新粉丝水位线失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
