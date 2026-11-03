package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 关系行的状态取值（docs/ACCOUNTS.md §7.1）。
//
// 票 #59 之后**只有 blocked 是活的**：好友请求流水线（pending）与「已接受」
// （accepted）都已退役 —— 好友 ≡ 互关，判据只看 follows 表的两条边，
// friendships 表只剩「拉黑」这一种用途。
//
// pending / accepted 两个常量**刻意保留**：`BackfillLegacyFollows` 仍要按它们
// 去读**历史行**（旧库里的存量请求与好友），那是只读的一次性回填，
// 与"应用层还在产生这两种状态"是两件事。删掉常量会让那段回填变成魔法字符串，
// 而魔法字符串正是回填演练里最容易写错的地方。
const (
	FriendshipPending  = "pending"
	FriendshipAccepted = "accepted"
	FriendshipBlocked  = "blocked"
)

// 本域语义错误。store 定义、handler 翻译成 404/409（不把 SQL 细节泄漏到 HTTP 层，
// 同 anime.go / posts.go 的纪律）。
var (
	// ErrFriendshipNotFound 是"按 id 查不到这条关系"。
	ErrFriendshipNotFound = errors.New("store: 好友关系不存在")
	// ErrSelfRelation 是"不能对自己建立拉黑"。
	ErrSelfRelation = errors.New("store: 不能对自己建立关系")
	// ErrBlockedRowNotFound 是"这一行拉黑不是你的"或"压根没有这一行"（#39 的解除拉黑）。
	//
	// 与 ErrFriendshipNotFound 分开而不是复用：本票的三处新端点里有**两处**需要表达
	// "解除的对象不在你的黑名单里"，而它们的共同点是"既不能确认也不能否认它存在" ——
	// 这正是 #29 把越权统一翻成 404 的同一条理由（防存在性探测）。
	// 分成新哨兵让"黑名单缺口"与"好友关系缺口"在错误链上可分辨，
	// 而两者到 HTTP 层都落 404，对外的不可分辨性保持不变。
	ErrBlockedRowNotFound = errors.New("store: 黑名单里没有这一行")
)

// 好友域搜索的边界（wayfinder #39）。
//
// searchLimit 是"搜人"的结果集上限。**刻意不做分页**（票面 out 段）：
// 搜人是低频小结果集，LIMIT 20 足够；真需要再单独一票，别现在猜参数。
// 同理没有 offset/cursor —— 多一个可调参数就多一条"翻页边界不一致"的路径
// （#27 的分页教训：一个域里混两种分页风格是 off-by-one 的温床）。
const searchLimit = 20

// Friendship 是关系行。requester/addressee 按登记时的角色写入，此后不变。
type Friendship struct {
	ID          int64     `gorm:"column:id;primaryKey"`
	RequesterID int64     `gorm:"column:requester_id"`
	AddresseeID int64     `gorm:"column:addressee_id"`
	Status      string    `gorm:"column:status"`
	BlockedByID *int64    `gorm:"column:blocked_by_id"`
	CreatedAt   time.Time `gorm:"column:created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`

	// 查询时 JOIN users 带出的显示名（只读，同 posts.AuthorName）。
	PeerName string `gorm:"column:peer_name;->"`
}

func (Friendship) TableName() string { return "friendships" }

// blockRowIn 是**拉黑行的语句级唯一 owner**：锁住 (a,b) 两个方向的既有行，
// 没有就新建，有就原地转成 blocked。
//
// # 为什么必须锁
//
// 唯一约束只挡 (A,B) 方向的重复，挡不住 (B,A)。并发下 A、B 同时拉黑对方，
// 若不锁，两条 blocked 行都会插入，「一对人只有一行关系」就破了 —— 而那一行
// 是"谁拉黑了谁"的唯一记录，重复之后解除拉黑只能解掉一半。
// 锁两个方向（无论有没有行，用 FOR UPDATE 锁住会命中的区间），再裁决，
// 唯一约束作为最后的兜底。
//
// # 票 #59 之后它只服务拉黑
//
// 旧版本这里还要裁决 pending / accepted 的请求流水线（"已经是好友"、
// "已有待处理请求"），那条流水线随票 #59 退役 —— 好友 ≡ 互关，没有"请求"这回事。
// 因此**撞到任何状态的历史行一律原地转 blocked**（不再有"不能覆盖 accepted"的分支）：
// 历史行只是旧版本的残留，让它继续挡着拉黑才是错的。
func blockRowIn(ctx context.Context, tx *gorm.DB, a, b int64) (*Friendship, error) {
	var existing Friendship
	err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where(`(requester_id = ? AND addressee_id = ?) OR (requester_id = ? AND addressee_id = ?)`,
			a, b, b, a).
		Take(&existing).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		now := time.Now()
		row := &Friendship{
			RequesterID: a,
			AddresseeID: b,
			Status:      FriendshipBlocked,
			BlockedByID: &a,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if cerr := tx.WithContext(ctx).Create(row).Error; cerr != nil {
			if isUniqueViolation(cerr) {
				// 并发竞态兜底：另一条事务刚插进去，重读一次返回它。
				var f Friendship
				if gerr := tx.WithContext(ctx).
					Where(`(requester_id = ? AND addressee_id = ?) OR (requester_id = ? AND addressee_id = ?)`,
						a, b, b, a).Take(&f).Error; gerr == nil {
					return &f, nil
				}
			}
			return nil, fmt.Errorf("store: 拉黑失败: %w", cerr)
		}
		return row, nil
	case err != nil:
		return nil, fmt.Errorf("store: 锁定拉黑关系失败: %w", err)
	}

	if err := tx.WithContext(ctx).Model(&Friendship{}).Where("id = ?", existing.ID).
		Updates(map[string]any{
			"status":        FriendshipBlocked,
			"blocked_by_id": a,
			"updated_at":    time.Now(),
		}).Error; err != nil {
		return nil, fmt.Errorf("store: 拉黑失败: %w", err)
	}
	existing.Status = FriendshipBlocked
	existing.BlockedByID = &a
	return &existing, nil
}

// BlockUser 拉黑某人（服务端强制，§7.1）。
//
// 一次事务里做两件事，**顺序不能反**：
//
//  1. 写/覆盖 friendships 的 blocked 行（`blockRowIn`）—— 拉黑事实的唯一记录。
//  2. 删掉 `actor → target` 那条关注边，**保留** `target → actor`（票 #42 §4）。
//
// # 为什么删 A→B 却留 B→A
//
// 关注是单向边，"我拉黑你"要表达的是"我不想再跟你有来往"，所以**我**的关注
// 应当消失（我不再订阅你的内容）。而对方关不关注我是**他的**选择，我没有权利
// 替他取关 —— 拉黑是我对自己的约束，不是对他的惩罚。两条边的不对称正是
// "拉黑是单向的"在数据上的样子。
//
// 保留 B→A 还有一层：解除拉黑后如果 B 仍然关注 A，那 A 只要回关就恢复互关，
// 不必让 B 重新关注一次。若这里把两条边都删了，解黑就等于"把对方的关注也弄丢了"。
//
// # 为什么"双方都不能新关注"不在这里写
//
// 那条规则由 `CreateFollow` 的裁决保证（`hasBlockBetweenIn` → ErrFollowBlocked，
// 双向判定）。在这里再删一次 B→A 只会让上面那条不对称失效，而在这里加一个
// "禁止"的判断则是把同一条规则写成两份 —— 两份真源迟早会分叉。
//
// 拉黑自己返回 ErrSelfRelation。
func (s *Store) BlockUser(ctx context.Context, actorID, targetID int64) (*Friendship, error) {
	if actorID <= 0 || targetID <= 0 {
		return nil, ErrFriendshipNotFound
	}
	if actorID == targetID {
		return nil, ErrSelfRelation
	}

	var row *Friendship
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := blockRowIn(ctx, tx, actorID, targetID)
		if err != nil {
			return err
		}
		row = locked
		// 删 actor→target 的边（保留反向），见上面「为什么删 A→B 却留 B→A」。
		if err := tx.WithContext(ctx).
			Where("follower_id = ? AND followee_id = ?", actorID, targetID).
			Delete(&Follow{}).Error; err != nil {
			return fmt.Errorf("store: 拉黑时清理关注边失败: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// 回带被拉黑方的显示名：block 响应的"对方"是 addressee。
	if row != nil {
		var name string
		if err := s.db.WithContext(ctx).Table("users").Where("id = ?", row.AddresseeID).
			Select("display_name").Scan(&name).Error; err == nil {
			row.PeerName = name
		}
	}
	return row, nil
}

// BlockStatusBetween 回答"a 与 b 之间有没有 blocked 关系，以及是谁拉黑了谁"。
//
// 这是给 #31（私聊）留的窄出口：发消息前要判"被拉黑方发不进消息"。
// 返回 (blocked bool, blockedByID int64)；blocked=false 时 blockedByID 无意义。
func (s *Store) BlockStatusBetween(ctx context.Context, a, b int64) (bool, int64, error) {
	return blockStatusBetweenIn(ctx, s.db, a, b)
}

// blockStatusBetweenIn 是 BlockStatusBetween 的**语句级唯一 owner**。
//
// 单独暴露一个吃 *gorm.DB 的版本，是为了让"发消息前的拉黑判断"能跑在**同一个事务**
// 里（见 store/messages.go 的 SendDM）：用连接池做这次判断、再回事务插入，
// 两条语句之间对方刚好拉黑就会漏掉一次拦截 —— 而"被拉黑方发不进消息"
// 是 §7.1 要求**服务端强制**的性质，不该留这个窗口。
func blockStatusBetweenIn(ctx context.Context, db *gorm.DB, a, b int64) (bool, int64, error) {
	var f Friendship
	err := db.WithContext(ctx).
		Where(`status = ? AND ((requester_id = ? AND addressee_id = ?) OR (requester_id = ? AND addressee_id = ?))`,
			FriendshipBlocked, a, b, b, a).
		Take(&f).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, fmt.Errorf("store: 查询拉黑关系失败: %w", err)
	}
	return true, derefInt64(f.BlockedByID), nil
}

func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// UserSearchHit 是"按用户名找人"的一行（wayfinder #39）。
//
// 带出 username 与 display_name 两列：**用户名与昵称一起搜**（前端「找朋友」的
// placeholder 就写着"按用户名或昵称搜索"），只带一列会让另一半输入永远搜不到东西，
// 而那种半残在界面上表现为"我明明记得他叫什么，却搜不出来"。
type UserSearchHit struct {
	ID          int64  `gorm:"column:id;primaryKey"`
	Username    string `gorm:"column:username"`
	DisplayName string `gorm:"column:display_name"`
	// AvatarURL 是像素头像（票 #74）；nil = 没有。
	//
	// 搜索结果行也显示头像：它是「找朋友」唯一的结果列表，行首空着会与
	// 好友页其它名单（都带头像）长得不一样，而"同一个人在不同列表里
	// 有的带头像有的不带"看起来像数据丢了。
	AvatarURL *string `gorm:"column:avatar_url"`
}

// TableName 走 users（本类型只是 users 的一个只读投影，不建表）。
func (UserSearchHit) TableName() string { return tableUsers }

// SearchUsers 按用户名/昵称模糊匹配找人，排除自己，最多 searchLimit 条（#39）。
//
// 三处刻意的选择：
//
//  1. **排除自己**。搜索结果里出现"我自己"没有意义，而"加自己为好友"是
//     服务端明确拒绝的（ErrSelfRelation）—— 让一个注定失败的按钮出现在界面上，
//     比不让它出现更糟。同理，自己也能在结果里看到自己会让人以为账号信息泄露。
//  2. **关系状态不在这里返回**。本方法的职责是"在 users 里找人"；
//     "我跟他什么关系"是关注域（`FollowStatesFor`）与拉黑域（`ListBlocked`）
//     的事实，由调用方（handler）各查一次再拼上去。票 #59 之前这里还提过
//     `relation ∈ none|friend|pending-out|pending-in|blocked` 那套流水线取值，
//     它随好友请求一起退役了。
//  3. **被拉黑的人仍然作为结果出现**（前端据 `blocked` 布尔渲染成"已拉黑"）。
//     它要用来显示"已拉黑" —— 用户需要能看见自己拉黑了谁，而搜索是唯一
//     不需要先记住 userId 的入口。隐藏它会让"解除拉黑"没有入口。
//
// 匹配用 ILIKE + likePattern（转义 % / _ 后两侧加 %），与 ListUsers 同一口径：
// 搜 "a_b" 时直觉是字面匹配。空关键词直接返回空（不去查库，也不把空串交给数据库）。
//
// 排序固定 id ASC：结果集有上限且小，稳定比"相关度"重要
// （票面 out 段明确不做相关性排序）。
func (s *Store) SearchUsers(ctx context.Context, keyword string, excludeID int64) ([]UserSearchHit, error) {
	pattern := likePattern(keyword)
	if pattern == "" {
		return []UserSearchHit{}, nil
	}
	if excludeID > 0 {
		var rows []UserSearchHit
		// 可见账号过滤（票 #73）：注销与封禁的账号都不该被搜到。
		// 用 applyVisibleAccount 而不是手写条件 —— 这条口径在档案、名单、
		// 红点、流谓词四处都要成立，见 visibility.go 的文件头。
		err := applyVisibleAccount(s.db.WithContext(ctx), "").
			Where(`(username ILIKE ? ESCAPE '\' OR display_name ILIKE ? ESCAPE '\') AND id <> ?`,
				pattern, pattern, excludeID).
			Order("id ASC").Limit(searchLimit).
			Find(&rows).Error
		if err != nil {
			return nil, fmt.Errorf("store: 搜索用户失败: %w", err)
		}
		return rows, nil
	}
	var rows []UserSearchHit
	err := applyVisibleAccount(s.db.WithContext(ctx), "").
		Where(`username ILIKE ? ESCAPE '\' OR display_name ILIKE ? ESCAPE '\'`, pattern, pattern).
		Order("id ASC").Limit(searchLimit).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("store: 搜索用户失败: %w", err)
	}
	return rows, nil
}

// BlockedRow 是"我拉黑的人"的一行（wayfinder #39）。
//
// 与 Friendship 分开而不是复用：黑名单只需要"对方是谁 + 什么时候拉黑的"，
// 不需要关系行的登记角色（requester/addressee 在 blocked 语义下已无意义 ——
// 拉黑方是 blocked_by_id，被拉黑方是另一方）。复用会诱导调用方去读
// RequesterID 并误判方向，而那种误判没有编译期保护。
type BlockedRow struct {
	// BlockedID 是**被拉黑方**的 user id。
	BlockedID   int64  `gorm:"column:blocked_id"`
	DisplayName string `gorm:"column:display_name"`
	Username    string `gorm:"column:username"`
	// AvatarURL 是像素头像（票 #74）；nil = 没有。
	AvatarURL *string   `gorm:"column:avatar_url"`
	BlockedAt time.Time `gorm:"column:blocked_at"`
}

// ListBlocked 返回 actor 拉黑的全部人（只列 blocked_by_id = actor，有向语义，#39）。
//
// 为什么只列一方：拉黑是**单向**的（§7.1）。"别人拉黑我"的那一行对我不可见 ——
// 我能看到的只有我拉黑了谁。混进来会让被拉黑方看见"原来他拉黑了我"，
// 而那正是拉黑方刻意不让对方知道的事（前端黑名单栏的说明就写着
// "只有拉黑方能解除，被拉黑方看不到这一行"）。
//
// JOIN users 取显示名与用户名：黑名单要能认出"这人是谁、当初为什么拉黑"，
// 只有一个 id 的名单是不可执行的。
// 排序按 blocked_at DESC：最近拉黑的在最前（最可能需要撤销的正是刚拉黑的）。
func (s *Store) ListBlocked(ctx context.Context, actorID int64) ([]BlockedRow, error) {
	var rows []BlockedRow
	err := s.db.WithContext(ctx).
		Table("friendships AS f").
		Select(`CASE WHEN f.requester_id = ? THEN f.addressee_id ELSE f.requester_id END AS blocked_id,
		        u.display_name, u.username, `+maskedAvatarSQL("u")+` AS avatar_url,
		        f.updated_at AS blocked_at`, actorID).
		Joins("JOIN users u ON u.id = CASE WHEN f.requester_id = ? THEN f.addressee_id ELSE f.requester_id END", actorID).
		Where("f.status = ? AND f.blocked_by_id = ?", FriendshipBlocked, actorID).
		Order("f.updated_at DESC, f.id DESC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("store: 查询黑名单失败: %w", err)
	}
	return rows, nil
}

// Unblock 解除拉黑：删掉"actor 拉黑 target"的那一行，关系回到"没有关系"（#39）。
//
// 三个设计点：
//
//  1. **只认自己拉黑的那一行**。`blocked_by_id = actor` 与"另一方是 target"两个条件
//     同时成立才删。别人拉黑我的行即便我知道它的存在也动不了（返回 ErrBlockedRowNotFound）。
//  2. **删除而不是回退状态**。关系回到"没有关系"，**不是**回到好友或 pending
//     （#28 的 mock 与前端说明都是这个语义）。回退状态需要决定"回到哪一个"，
//     而"被拉黑前是什么"并没有被可靠记录 —— requester/addressee 只是登记角色，
//     不表示拉黑前的状态。
//  3. **删除后可以重新关注**：行没了，`CreateFollow` 的拉黑裁决自然放行
//     （验收第 1 条）。若解除时对方仍关注着你，你回关即恢复互关 ——
//     这正是 `BlockUser` 刻意保留反向关注边的原因。
//     解除拉黑因此是一个**可逆的、完整的**动作，与 #28 记录的
//     "拉黑单向不可逆"痛点对应。
func (s *Store) Unblock(ctx context.Context, actorID, targetID int64) error {
	if actorID <= 0 || targetID <= 0 {
		return ErrBlockedRowNotFound
	}
	if actorID == targetID {
		return ErrSelfRelation
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 锁住这一行再删：并发下"解除拉黑"与"再次拉黑"交错时，
		// 后到的那次必须看到确定的行状态（而不是基于一次过期的读）。
		var f Friendship
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where(`status = ? AND blocked_by_id = ? AND
				((requester_id = ? AND addressee_id = ?) OR (requester_id = ? AND addressee_id = ?))`,
				FriendshipBlocked, actorID, actorID, targetID, targetID, actorID).
			Take(&f).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrBlockedRowNotFound
		}
		if err != nil {
			return fmt.Errorf("store: 查询拉黑关系失败: %w", err)
		}
		if err := tx.Delete(&Friendship{}, f.ID).Error; err != nil {
			return fmt.Errorf("store: 解除拉黑失败: %w", err)
		}
		return nil
	})
}
