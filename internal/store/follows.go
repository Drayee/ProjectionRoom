package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 关注边（follows）。wayfinder #56 —— ADR 0001 的存储层落地。
//
// # 语义（GLOSSARY「Social graph」）
//
//   - **Follow** 是**单向边** A→B，免同意，不需要对方批准。
//   - **Mutual follow**（互关）是两条边同时存在的**合取**，不是一个状态值。
//   - **Friend**（好友）≡ 互关（产品上的同义词，不是另一个工作流）。
//   - **can_dm** ≡ mutual_follow ∧ ¬blocked_either。
//
// # 为什么边是**两行**而不是一个状态
//
// A→B 与 B→A 是两条独立的行。"互关"因此永远是**派生**事实，
// 不存在"忘了更新互关标记"这种漂移（那正是把互关做成状态值的代价）。
// 代价是"查互关"要两条边各查一次 —— 用一条 SQL 的 EXISTS 解决（见 followStateIn）。
//
// # 为什么 blocked 仍然用 friendships 表
//
// 拉黑是**有向**的（只有拉黑方能解除），且它的行里要记"谁拉黑了谁"。
// 那与"一对用户之间可有两条有向边"的关注模型形状不同，硬塞进同一张表会
// 让 blocked 行的唯一键被迫变成"单向对"而不再是 (follower, followee)。
// 票 59（收缩）会把 friendships 收缩成"只留 blocked 态"，但**表**仍是那一张 ——
// 那是本票之后的事。

// 关注域的语义错误。store 定义、handler 翻译成 4xx
// （同 friendships.go / messages.go 的纪律：不把 SQL 细节泄漏到 HTTP 层）。
var (
	// ErrFollowSelfTarget 是"不能关注自己"。
	//
	// 为什么单独一个哨兵而不是复用 ErrSelfRelation：那条的文案是
	// "不能对自己建立关系"（好友域），而关注域的界面文案是"不能关注自己"。
	// 两者语义不同、文案不同，分开让 handler 能给出正确的提示。
	ErrFollowSelfTarget = errors.New("store: 不能关注自己")

	// ErrFollowBlocked 是"你们之间存在拉黑关系，服务端拒绝建立关注边"。
	//
	// 方向不区分（与私聊的 ErrMessageBlocked 同一条口径）：拉黑是单向语义，
	// 但"拉黑期间任何一方都无法关注另一方"是 ADR 0001 明确的规则 ——
	// 否则被拉黑方只要关注拉黑方就绕过了"世界清净"这个诉求。
	ErrFollowBlocked = errors.New("store: 你们之间存在拉黑关系，无法关注")

	// ErrFollowTargetNotFound 是"被关注的对象不存在或已被封禁"。
	//
	// 不存在与被封禁共用它（与公开档案的 ErrUserNotFound 同一条理由）：
	// 能区分它们就等于开了一条存在性探测通道。
	ErrFollowTargetNotFound = errors.New("store: 用户不存在")
)

// Follow 是一条关注边（follower → followee）。
//
// 方向写在字段名里（不是 follower/followed）：后者读起来像"关注了的人"，
// 而 follower 是**发起关注的人**、followee 是**被关注的人** —— 方向反了会让
// "我在关注谁"与"谁在关注我"在代码里悄悄互换，而那种错不报错。
type Follow struct {
	ID         int64     `gorm:"column:id;primaryKey"`
	FollowerID int64     `gorm:"column:follower_id"`
	FolloweeID int64     `gorm:"column:followee_id"`
	CreatedAt  time.Time `gorm:"column:created_at"`
}

// TableName 见 User.TableName 的说明。
func (Follow) TableName() string { return "follows" }

// FollowState 是"我与某人的关系"的**派生**视图。
//
// 它不是表里的一行，也不落库：每一次读都是**现在**的真相
// （边被删、被加、被拉黑挡住，状态立刻跟着变）。这与 friendships 的
// "一行一个状态"模型的根本差别就在这里。
type FollowState struct {
	// Following 是"我关注了对方"。
	Following bool
	// FollowedBy 是"对方关注了我"。
	FollowedBy bool
	// FollowerCount 是**对方**的粉丝数（关注对方的人数）。
	//
	// 计数挂在"对方"这一侧是刻意的：这个结构回答的是
	// "我与 TA 的关系，以及 TA 的关系数字"，而调用方（档案页）要显示的
	// "TA 的关注数 / 粉丝数"正是这两个值。
	FollowerCount int64
	// FollowingCount 是**对方**的关注数（对方关注的人数）。
	FollowingCount int64
}

// Mutual 回答"我们是不是互关"（好友的判定，ADR 0001）。
func (s FollowState) Mutual() bool { return s.Following && s.FollowedBy }

// CanDM 回答"我能不能给这个人发私聊"。
//
// can_dm(A,B) = mutual_follow(A,B) ∧ ¬blocked_either(A,B)（ADR 0001）。
//
// 拉黑这一项**不**在结构里表达，而是在 CanDM 的调用侧叠加 ——
// 因为"是否被拉黑"是**对方视角**的事实（谁拉黑了谁），把它塞进一个
// 描述"我与 TA"的公开结构里会让方向丢失。
// 调用方（store.CanDM）用同一段 SQL 把拉黑一起算掉，这是 can_dm 的**唯一**实现。
func (s FollowState) CanDM() bool { return s.Mutual() }

// CreateFollow 建立一条关注边（幂等）。
//
// 返回 (边, 是否新建, error)。
//
// 三个裁决，都在**一条语句**里完成：
//
//  1. 自关注 → ErrFollowSelfTarget（应用层第一道；DB 的 CHECK 是最后一道）。
//  2. 任一方向存在拉黑 → ErrFollowBlocked（票 42：拉黑存续期双向禁止新关注）。
//  3. 边已存在 → **不报错**，返回那一条边（created=false）。
//
// 为什么第 3 条不是 409：重复关注是**幂等**的，而不是冲突 ——
// 用户双击「关注」或网络重发都只该得到同一条边。真正需要 409 的场景
// （"同键不同体"）由幂等键那一层管（见 handler 的 withIdempotency）。
//
// 第 2 条与第 3 条的**顺序**有讲究：拉黑判定在插入之前，所以
// "拉黑期间点关注"得到的是 403 而不是"关注成功但对方看不见"。
func (s *Store) CreateFollow(ctx context.Context, followerID, followeeID int64) (*Follow, bool, error) {
	if followerID <= 0 || followeeID <= 0 {
		return nil, false, ErrFollowTargetNotFound
	}
	if followerID == followeeID {
		return nil, false, ErrFollowSelfTarget
	}

	// 拉黑判定：两个方向都查（方向不区分，见 ErrFollowBlocked 的说明）。
	blocked, _, err := s.BlockStatusBetween(ctx, followerID, followeeID)
	if err != nil {
		return nil, false, err
	}
	if blocked {
		return nil, false, ErrFollowBlocked
	}

	row := &Follow{
		FollowerID: followerID,
		FolloweeID: followeeID,
		CreatedAt:  time.Now(),
	}
	// ON CONFLICT DO NOTHING：重复关注不新建行。之后回读那一条（可能是刚才写的，
	// 也可能是并发下先落的那条）—— 返回的边因此**总是**真实存在的那一条。
	res := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
		Create(row)
	if res.Error != nil {
		if isUniqueViolation(res.Error) {
			// 唯一键兜底（与 ON CONFLICT 双保险：驱动/方言变化时仍不炸）。
			existing, err := s.followRow(ctx, followerID, followeeID)
			if err != nil {
				return nil, false, err
			}
			return existing, false, nil
		}
		return nil, false, fmt.Errorf("store: 建立关注边失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		existing, err := s.followRow(ctx, followerID, followeeID)
		if err != nil {
			return nil, false, err
		}
		return existing, false, nil
	}
	return row, true, nil
}

// followRow 回读一条存在的关注边（没有则 ErrFollowTargetNotFound）。
func (s *Store) followRow(ctx context.Context, followerID, followeeID int64) (*Follow, error) {
	var row Follow
	err := s.db.WithContext(ctx).
		Where("follower_id = ? AND followee_id = ?", followerID, followeeID).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrFollowTargetNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: 查询关注边失败: %w", err)
	}
	return &row, nil
}

// RemoveFollow 删掉"我 → TA"这条边（取关）。
//
// 返回 (是否真的删掉了, error)。
//
// 只删**自己这一个方向**：反向边（TA → 我）是对方的事，单方取关不该替对方做决定。
// 删自己这一条不会让对方失去关注我的权利 —— 但**会让互关消失**，
// 于是 can_dm 随之失效（票 57 落这条）。
//
// 重复取关是 no-op（返回 false、不报错）：取关按钮可能被连点，
// 而"我已经不关注 TA 了"不是错误。
func (s *Store) RemoveFollow(ctx context.Context, followerID, followeeID int64) (bool, error) {
	if followerID <= 0 || followeeID <= 0 {
		return false, ErrFollowTargetNotFound
	}
	res := s.db.WithContext(ctx).
		Where("follower_id = ? AND followee_id = ?", followerID, followeeID).
		Delete(&Follow{})
	if res.Error != nil {
		return false, fmt.Errorf("store: 取消关注失败: %w", res.Error)
	}
	return res.RowsAffected > 0, nil
}

// IsFollowing 回答"我是否关注了 TA"。
func (s *Store) IsFollowing(ctx context.Context, followerID, followeeID int64) (bool, error) {
	if followerID <= 0 || followeeID <= 0 {
		return false, nil
	}
	var n int64
	err := s.db.WithContext(ctx).Model(&Follow{}).
		Where("follower_id = ? AND followee_id = ?", followerID, followeeID).
		Count(&n).Error
	if err != nil {
		return false, fmt.Errorf("store: 查询关注关系失败: %w", err)
	}
	return n > 0, nil
}

// FollowStateBetween 算出"我与 TA 的关系"以及 TA 的两个计数。
//
// 一次查询拿全部（不是四次往返）：
//
//   - 两个方向的边各一个 EXISTS；
//   - TA 的粉丝数与关注数各一个子查询 COUNT。
//
// 计数用子查询而不是 JOIN + GROUP BY：结果集恒为一行，
// JOIN 形态要小心"一对多 JOIN 之后 COUNT 被放大"那个经典 off-by-N。
func (s *Store) FollowStateBetween(ctx context.Context, viewerID, peerID int64) (FollowState, error) {
	var st FollowState
	if viewerID <= 0 || peerID <= 0 {
		return st, nil
	}
	const q = `
SELECT
  EXISTS (SELECT 1 FROM follows WHERE follower_id = ? AND followee_id = ?) AS following,
  EXISTS (SELECT 1 FROM follows WHERE follower_id = ? AND followee_id = ?) AS followed_by,
  (SELECT COUNT(*) FROM follows WHERE followee_id = ?) AS follower_count,
  (SELECT COUNT(*) FROM follows WHERE follower_id = ?) AS following_count`
	var row struct {
		Following      bool  `gorm:"column:following"`
		FollowedBy     bool  `gorm:"column:followed_by"`
		FollowerCount  int64 `gorm:"column:follower_count"`
		FollowingCount int64 `gorm:"column:following_count"`
	}
	if err := s.db.WithContext(ctx).Raw(q, viewerID, peerID, peerID, viewerID, peerID, peerID).
		Scan(&row).Error; err != nil {
		return st, fmt.Errorf("store: 查询关注关系状态失败: %w", err)
	}
	st.Following = row.Following
	st.FollowedBy = row.FollowedBy
	st.FollowerCount = row.FollowerCount
	st.FollowingCount = row.FollowingCount
	return st, nil
}

// CanDM 回答"a 能不能给 b 发私聊"（ADR 0001 的 can_dm 谓词）。
//
// can_dm(A,B) := mutual_follow(A,B) ∧ NOT blocked_either(A,B)。
//
// 这是**唯一**的实现：私聊发送路径（#57 换门闩）、档案页的"能否发消息"按钮、
// 搜索结果的关系按钮，全部走它 —— 三处各写一遍谓词必然漂移。
//
// 拉黑判定与互关判定在**同一次**查询里完成（一条 SQL），不分成两次往返：
// 两次查之间对方刚好拉黑，就会漏掉一次拦截 —— 而"被拉黑方发不进消息"
// 是服务端强制的性质，不该留这个窗口。
func (s *Store) CanDM(ctx context.Context, a, b int64) (bool, error) {
	if a <= 0 || b <= 0 || a == b {
		return false, nil
	}
	const q = `
SELECT
  (EXISTS (SELECT 1 FROM follows WHERE follower_id = ? AND followee_id = ?)
   AND EXISTS (SELECT 1 FROM follows WHERE follower_id = ? AND followee_id = ?))
  AND NOT EXISTS (
    SELECT 1 FROM friendships
     WHERE status = 'blocked'
       AND ((requester_id = ? AND addressee_id = ?) OR (requester_id = ? AND addressee_id = ?))
  )`
	var ok bool
	if err := s.db.WithContext(ctx).Raw(q, a, b, b, a, a, b, b, a).Scan(&ok).Error; err != nil {
		return false, fmt.Errorf("store: 校验私聊资格失败: %w", err)
	}
	return ok, nil
}

// LegacyFollowBackfill 是一次存量回填的结果（供调用方记日志与验收）。
type LegacyFollowBackfill struct {
	// FollowsCreated 是新建的关注边条数（accepted 每行 2 条、pending 每行 1 条）。
	FollowsCreated int
	// RequestsRetired 是被终结的 pending 请求行数（票 42：pending → 单向 follow）。
	RequestsRetired int
}

// BackfillLegacyFollows 把**旧的 friendships 行**回填成关注边（票 42 决议）。
//
// # 映射规则（ADR 0001）
//
//	accepted → **双向** follow（R→A 与 A→R 各一条）—— 旧好友即互关。
//	pending  → **单向** follow（requester → addressee）—— 我发出的请求变成我的关注。
//	blocked  → **不动**。拉黑不走关注边（它有自己的表与谓词），
//	           且"拉黑方的 follow 被删、被拉黑方的 follow 可留"是 #57 的拉黑事务。
//
// # 为什么 pending 的请求行被**终结**（删掉）
//
// 票 42 明确"废弃好友请求"：pending 行若留着，请求方在旧界面上仍会看到
// "等对方处理"，而对方那边已经不会处理了（没有 accept 路径了 —— 那是 #59 的活）。
// 留着它就是一条**永远等不到的请求**。所以这里把它换成一条单向关注边，
// 让"我关注了 TA"这件事在关系模型里有位置，而请求本身终止。
//
// # 为什么这不是一条**迁移 DDL**
//
// 本仓库的迁移规约是「只做加法」（migrate_test.go 的静态检查禁止
// drop / truncate / delete / update 出现在迁移语句里）。而这段回填要**删行**
// （终结 pending 请求），它不是"改结构"，是"搬数据"——
// 所以它是**应用层的一次性回填**，不是迁移步骤。
//
// 迁移规约针对的是 DDL；而"把旧数据搬进新表"是一次性的、可重跑的、
// 需要独立验收的动作（规格的风险登记第 ① 条正是"迁移需在隔离测试库先演练"）。
//
// # 幂等
//
// 重复调用不会产生第二条边（唯一键 + ON CONFLICT DO NOTHING），
// 也不会重复终结请求行（第二次找不到 pending 行）。
// 于是它可以在启动路径上被安全地重复调用。
//
// # 调用方
//
// 本票（#56）只提供**能力**与真库证据；**何时调用**由 #57 决定
// （那一票负责存量迁移的演练与 can_dm 换门闩）。
// 之所以不自动跑：删 pending 请求行是**不可逆**的数据动作，
// 应当在切换前被显式触发一次，而不是夹在每次启动里。
func (s *Store) BackfillLegacyFollows(ctx context.Context) (LegacyFollowBackfill, error) {
	var out LegacyFollowBackfill

	// 先取 pending 的行（在删之前取）：它们的 requester/addressee 决定了
	// 要补哪一条单向边。
	type legacyRow struct {
		ID          int64
		RequesterID int64
		AddresseeID int64
		Status      string
	}
	var rows []legacyRow
	if err := s.db.WithContext(ctx).
		Table("friendships").
		Select("id, requester_id, addressee_id, status").
		Where("status IN (?, ?)", FriendshipAccepted, FriendshipPending).
		Find(&rows).Error; err != nil {
		return out, fmt.Errorf("store: 读取旧好友关系失败: %w", err)
	}

	for _, r := range rows {
		switch r.Status {
		case FriendshipAccepted:
			// 旧好友 = 互关：两个方向各补一条。
			for _, pair := range [][2]int64{{r.RequesterID, r.AddresseeID}, {r.AddresseeID, r.RequesterID}} {
				if _, created, err := s.CreateFollow(ctx, pair[0], pair[1]); err != nil {
					return out, fmt.Errorf("store: 回填关注边失败: %w", err)
				} else if created {
					out.FollowsCreated++
				}
			}
		case FriendshipPending:
			// 旧请求 = 我关注了 TA（单向），然后终结这条请求行。
			if _, created, err := s.CreateFollow(ctx, r.RequesterID, r.AddresseeID); err != nil {
				return out, fmt.Errorf("store: 回填关注边失败: %w", err)
			} else if created {
				out.FollowsCreated++
			}
			res := s.db.WithContext(ctx).
				Where("id = ? AND status = ?", r.ID, FriendshipPending).
				Delete(&Friendship{})
			if res.Error != nil {
				return out, fmt.Errorf("store: 终结旧好友请求失败: %w", res.Error)
			}
			out.RequestsRetired += int(res.RowsAffected)
		}
	}
	return out, nil
}

// CountFollowers 返回某人的粉丝数（关注他的人数）。
func (s *Store) CountFollowers(ctx context.Context, userID int64) (int64, error) {
	if userID <= 0 {
		return 0, nil
	}
	var n int64
	err := s.db.WithContext(ctx).Model(&Follow{}).
		Where("followee_id = ?", userID).
		Count(&n).Error
	if err != nil {
		return 0, fmt.Errorf("store: 统计粉丝数失败: %w", err)
	}
	return n, nil
}

// CountFollowing 返回某人的关注数（他关注的人数）。
func (s *Store) CountFollowing(ctx context.Context, userID int64) (int64, error) {
	if userID <= 0 {
		return 0, nil
	}
	var n int64
	err := s.db.WithContext(ctx).Model(&Follow{}).
		Where("follower_id = ?", userID).
		Count(&n).Error
	if err != nil {
		return 0, fmt.Errorf("store: 统计关注数失败: %w", err)
	}
	return n, nil
}

// ListFollowing 返回"我关注的人"的 id 列表（按关注时间倒序）。
//
// 刻意**不返回**用户对象：调用方（关注流 #52/#66、名单 #58）要的是
// "作者 id 的集合"，而把 users 表 JOIN 进来会让这个方法随 users 表变大而变慢。
// 昵称与头像由那些调用方按 id 批量取。
func (s *Store) ListFollowing(ctx context.Context, userID int64) ([]int64, error) {
	if userID <= 0 {
		return []int64{}, nil
	}
	var ids []int64
	err := s.db.WithContext(ctx).Model(&Follow{}).
		Where("follower_id = ?", userID).
		Order("created_at DESC, id DESC").
		Pluck("followee_id", &ids).Error
	if err != nil {
		return nil, fmt.Errorf("store: 查询关注列表失败: %w", err)
	}
	if ids == nil {
		ids = []int64{}
	}
	return ids, nil
}

// ListFollowers 返回"关注我的人"的 id 列表（按关注时间倒序）。
//
// 排序按关注时间倒序：最近关注的人在最前 —— 那正是粉丝红点（wayfinder #53/#67）
// 判"新粉丝"时的直觉顺序（最新关注最可能是还没回关的那个）。
func (s *Store) ListFollowers(ctx context.Context, userID int64) ([]int64, error) {
	if userID <= 0 {
		return []int64{}, nil
	}
	var ids []int64
	err := s.db.WithContext(ctx).Model(&Follow{}).
		Where("followee_id = ?", userID).
		Order("created_at DESC, id DESC").
		Pluck("follower_id", &ids).Error
	if err != nil {
		return nil, fmt.Errorf("store: 查询粉丝列表失败: %w", err)
	}
	if ids == nil {
		ids = []int64{}
	}
	return ids, nil
}
