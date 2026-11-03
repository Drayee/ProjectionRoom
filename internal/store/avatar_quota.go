package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// 头像变更频控的存储层（票 #75 / 决议 #54 §3 审核四件套之一）。
//
// 这一层的全部职责是**一个原子动作**：「如果今天还没用满，就把今天的计数 +1」。
// 它不判断"要不要限"（那是策略，在 service 里，见 service.DefaultAvatarChangesPerDay），
// 也不决定超限之后 HTTP 怎么回（那是 handler 的事）。
//
// # 为什么必须是单条语句
//
// 「读今天的次数 → 判断 → 写回」是两次往返，两次之间有一个可以被并发穿过的窗口：
// 同一秒里连开两个标签页点保存，两边都读到 4（还没满），两边都写回 5，
// 于是五次限制被用掉了六次。把判断塞进 UPDATE 的 WHERE 里，让数据库的
// 行锁替我们做这件事，是这个计数唯一的正确实现。
//
// # 跨日为什么不需要定时任务
//
// `avatar_changes_on` 与"今天"不相等就代表"这是新的一天"，`CASE` 分支
// 直接把计数从 1 重新开始。没有需要清理的过期行，也没有"零点任务没跑"
// 这种失败模式 —— 日期是每次写入时现算的。

// ErrAvatarQuotaExceeded 表示今天的头像变更次数已经用满。
//
// 它是 store 层的哨兵，由 service 翻译成面向用户的错误（带"明天再试"这类
// 可操作信息）。分成两层是因为"用满了"在这两层意味着不同的东西：
// 这里只是"那一条 UPDATE 没能匹配到行"，service 才知道限额是多少。
var ErrAvatarQuotaExceeded = errors.New("今天的头像变更次数已用满")

// ConsumeAvatarChange 尝试消耗一次今天的头像变更额度。
//
// 返回 nil = 额度已扣减（调用方可以继续写 avatar_url）；
// 返回 ErrAvatarQuotaExceeded = 今天已经用满 limit 次；
// 返回 ErrNotFound = 没有这个用户。
//
// `today` 由调用方给（不用 SQL 的 CURRENT_DATE）：测试要能"跳到明天"，
// 而 CURRENT_DATE 依赖数据库服务器的时区与时钟，测不了跨日口径 ——
// 那正是本票验收里明确要求进测资的一条。
//
// `limit <= 0` 视为"不限额"：直接 +1 并返回，不查上限。
func (s *Store) ConsumeAvatarChange(ctx context.Context, userID int64, today time.Time, limit int) error {
	if userID <= 0 {
		return ErrNotFound
	}
	day := dateOnly(today)

	if limit <= 0 {
		res := s.db.WithContext(ctx).Exec(
			`UPDATE users
			    SET avatar_changes_on = CASE WHEN avatar_changes_on = ? THEN avatar_changes_on ELSE ? END,
			        avatar_change_count = CASE WHEN avatar_changes_on = ? THEN avatar_change_count + 1 ELSE 1 END
			  WHERE id = ?`, day, day, day, userID)
		if res.Error != nil {
			return fmt.Errorf("扣减头像变更额度：%w", res.Error)
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}
		return nil
	}

	// WHERE 里的两个分支对应两种"可以改"的情形：
	//   ① `avatar_changes_on IS DISTINCT FROM today` —— 换了一天（含从未改过，NULL）；
	//   ② `avatar_change_count < limit` —— 还是今天，但没用满。
	//
	// `IS DISTINCT FROM` 而不是 `<>`：NULL <> '2026-11-03' 的结果是 NULL（不是 true），
	// 从未改过头像的行会被 `<>` 静默排除掉 —— 症状是"新账号第一次改头像就报 429"。
	// WHERE 里的第三个参数是 limit，SET 里**没有**它（要用 today 与 today 比两次，
	// 因为 CASE 的两个分支各自需要一次比较，而占位符是有序的）。
	res := s.db.WithContext(ctx).Exec(
		`UPDATE users
		    SET avatar_changes_on = ?,
		        avatar_change_count = CASE WHEN avatar_changes_on IS DISTINCT FROM ? THEN 1 ELSE avatar_change_count + 1 END
		  WHERE id = ?
		    AND (avatar_changes_on IS DISTINCT FROM ? OR avatar_change_count < ?)`,
		day, day, userID, day, limit)
	if res.Error != nil {
		return fmt.Errorf("扣减头像变更额度：%w", res.Error)
	}
	if res.RowsAffected == 1 {
		return nil
	}

	// 一行都没动：要么这个 id 不存在，要么今天已满。两者对用户的下一步完全不同
	//（"账号没了" vs "明天再来"），所以必须分开报，不能让调用方去猜。
	var exists int64
	if err := s.db.WithContext(ctx).Model(&User{}).Where("id = ?", userID).Count(&exists).Error; err != nil {
		return fmt.Errorf("判断头像变更额度失败：%w", err)
	}
	if exists == 0 {
		return ErrNotFound
	}
	return ErrAvatarQuotaExceeded
}

// AvatarChangeQuota 读回"今天已经用掉几次"，只用于展示与测试断言。
//
// 读与写走同一份日期口径（dateOnly），所以它和 ConsumeAvatarChange 的
// 判定永远一致。`avatar_changes_on` 不是今天就返回 0（跨日即归零，
// 不需要等待任何清理动作）。
func (s *Store) AvatarChangeQuota(ctx context.Context, userID int64, today time.Time) (int, error) {
	if userID <= 0 {
		return 0, ErrNotFound
	}
	var row struct {
		On    *time.Time
		Count int
	}
	err := s.db.WithContext(ctx).Model(&User{}).
		Select("avatar_changes_on AS on, avatar_change_count AS count").
		Where("id = ?", userID).
		Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("读取头像变更次数：%w", err)
	}
	if row.On == nil {
		return 0, nil
	}
	if !dateOnly(*row.On).Equal(dateOnly(today)) {
		return 0, nil
	}
	return row.Count, nil
}

// dateOnly 把时间截到"日"。
//
// 用 UTC 而不是本地时区：库里这一列是 `date`（无时区），写入时如果按服务器
// 本地时区截断，同一行在不同部署（UTC 与 Asia/Shanghai）上会落在不同的日子，
// 而"每人每日 5 次"是产品口径，不该随部署的 TZ 变化。UTC 是一个确定的锚。
//
// 代价是"零点"发生在 UTC 零点（北京时间 08:00）—— 这是有意的取舍：
// 用户看到的纠课文案是"每天可以改 5 次"，不承诺在本地零点整重置。
func dateOnly(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}
