package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 好友关系的三种状态（docs/ACCOUNTS.md §7.1）。
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
	// ErrFriendshipForbidden 是"这条关系不归你管"（pending 只有 addressee 能处理、
	// 删好友只有双方能删）。handler 统一翻译成 404 而非 403 —— 避免存在性探测：
	// 403 会告诉别人"这条关系存在，只是不归你"。
	ErrFriendshipForbidden = errors.New("store: 无权操作这条好友关系")
	// ErrAlreadyFriends 是"这段关系已经是 accepted"。
	ErrAlreadyFriends = errors.New("store: 已经是好友")
	// ErrRequestAlreadyPending 是"已有一条待处理请求"。
	ErrRequestAlreadyPending = errors.New("store: 已有待处理的好友请求")
	// ErrBlockedByTarget 是"对方已拉黑你，发不出请求"（服务端强制，§7.1）。
	ErrBlockedByTarget = errors.New("store: 对方已将你拉黑")
	// ErrSelfRelation 是"不能对自己发起好友/拉黑"。
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

// 我与另一个人的关系状态（#39 的搜索结果要带上它）。
//
// 与前端 `types/friends.ts` 的 RelationState 逐字对应 —— 两端各有一份同值常量，
// 而 protobuf 在这条链路上不参与（好友域是纯 REST），所以没有生成代码能保证一致。
// 改名/加值时必须两边同步：症状是前端把未知状态显示成"还不是好友"（它的 default 分支），
// 而那种错**不会报错**。
const (
	// RelationNone 是没有关系（可以发好友请求）。
	RelationNone = "none"
	// RelationFriend 是 accepted（已对称，任一方都能看到对方）。
	RelationFriend = "friend"
	// RelationPendingOut 是我发出的 pending 请求（等对方处理）。
	RelationPendingOut = "pending-out"
	// RelationPendingIn 是对方发给我的 pending 请求（去收件箱处理）。
	RelationPendingIn = "pending-in"
	// RelationBlocked 是存在拉黑关系（不区分方向，见 handler 的说明）。
	RelationBlocked = "blocked"
)

// RelationStateOf 回答"actor 与 peer 之间现在是什么关系"（#39 的搜索用）。
//
// pending 的**方向**由登记角色判：requester 是 actor → pending-out，反之 pending-in。
// blocked 不区分方向：搜索结果的按钮语义在两种拉黑下都一样（不能加好友），
// 而"是谁拉黑了谁"是对方视角的信息，不该由这个字段泄露。
//
// 传 nil 表示"没有关系行"（RelationNone）—— 调用方（handler）从批量查询里
// 拿不到对应行时就传 nil，不必为"没有关系"再查一次库。
func RelationStateOf(f *Friendship, actorID int64) string {
	if f == nil {
		return RelationNone
	}
	switch f.Status {
	case FriendshipAccepted:
		return RelationFriend
	case FriendshipBlocked:
		return RelationBlocked
	case FriendshipPending:
		if f.RequesterID == actorID {
			return RelationPendingOut
		}
		return RelationPendingIn
	default:
		return RelationNone
	}
}

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

// lockFriendPair 在事务里锁住 (a,b) 两个方向的既有行，供"裁决"用。
//
// 为什么必须锁：唯一约束只挡 (A,B) 方向的重复，挡不住 (B,A)。并发下 A、B 同时
// 向对方发请求，若不锁，两条 pending 都会插入，"一段关系只存一行"就破了。
// 锁两个方向（无论有没有行，用 FOR UPDATE 锁住会命中的区间），再裁决，唯一约束
// 作为最后的兜底。
func lockFriendPair(tx *gorm.DB, a, b int64) (*Friendship, error) {
	var row Friendship
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where(`(requester_id = ? AND addressee_id = ?) OR (requester_id = ? AND addressee_id = ?)`,
			a, b, b, a).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil // 两个方向都没有：可以新建
	}
	if err != nil {
		return nil, fmt.Errorf("store: 锁定好友关系失败: %w", err)
	}
	return &row, nil
}

// CreateFriendshipRequest 发起好友请求（pending）。
//
// 裁决顺序（都在事务里，先锁后判）：
//  1. 反方向已 accepted → ErrAlreadyFriends（对方已经发过且你们已是好友）；
//  2. 任一方已是 blocked → ErrBlockedByTarget（被拉黑方发不出请求，§7.1 服务端强制）；
//  3. 已有一条 pending（任一方向）→ ErrRequestAlreadyPending（幂等提示，不新建）；
//  4. 都没有 → 插入 pending，requester=发起方。
func (s *Store) CreateFriendshipRequest(ctx context.Context, requesterID, addresseeID int64) (*Friendship, error) {
	if requesterID <= 0 || addresseeID <= 0 {
		return nil, ErrFriendshipNotFound
	}
	if requesterID == addresseeID {
		return nil, ErrSelfRelation
	}

	var created *Friendship
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := lockFriendPair(tx, requesterID, addresseeID)
		if err != nil {
			return err
		}
		if row != nil {
			switch row.Status {
			case FriendshipAccepted:
				return ErrAlreadyFriends
			case FriendshipBlocked:
				return ErrBlockedByTarget
			case FriendshipPending:
				return ErrRequestAlreadyPending
			}
		}
		now := time.Now()
		created = &Friendship{
			RequesterID: requesterID,
			AddresseeID: addresseeID,
			Status:      FriendshipPending,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := tx.Create(created).Error; err != nil {
			// 唯一约束兜底：并发竞态下真撞了，当作"已有待处理请求"。
			if isUniqueViolation(err) {
				return ErrRequestAlreadyPending
			}
			return fmt.Errorf("store: 创建好友请求失败: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// AcceptFriendshipRequest 接受好友请求：pending → accepted。
// 只有 addressee 能 accept（§7.1 有向语义），否则 ErrFriendshipForbidden。
func (s *Store) AcceptFriendshipRequest(ctx context.Context, requestID, actorID int64) (*Friendship, error) {
	if requestID <= 0 {
		return nil, ErrFriendshipNotFound
	}
	var row *Friendship
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var f Friendship
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", requestID).Take(&f).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrFriendshipNotFound
		}
		if err != nil {
			return fmt.Errorf("store: 查询好友请求失败: %w", err)
		}
		if f.Status != FriendshipPending {
			// 已经处理过（accepted / blocked）：当作不存在，不暴露内部状态。
			return ErrFriendshipNotFound
		}
		if f.AddresseeID != actorID {
			return ErrFriendshipForbidden
		}
		if err := tx.Model(&Friendship{}).Where("id = ?", requestID).
			Updates(map[string]any{"status": FriendshipAccepted, "updated_at": time.Now()}).Error; err != nil {
			return fmt.Errorf("store: 接受好友请求失败: %w", err)
		}
		f.Status = FriendshipAccepted
		row = &f
		return nil
	})
	if err != nil {
		return nil, err
	}
	return row, nil
}

// RejectFriendshipRequest 拒绝好友请求：删掉 pending 行。只有 addressee 能 reject。
func (s *Store) RejectFriendshipRequest(ctx context.Context, requestID, actorID int64) error {
	if requestID <= 0 {
		return ErrFriendshipNotFound
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var f Friendship
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", requestID).Take(&f).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrFriendshipNotFound
		}
		if err != nil {
			return fmt.Errorf("store: 查询好友请求失败: %w", err)
		}
		if f.Status != FriendshipPending {
			return ErrFriendshipNotFound
		}
		if f.AddresseeID != actorID {
			return ErrFriendshipForbidden
		}
		if err := tx.Delete(&Friendship{}, requestID).Error; err != nil {
			return fmt.Errorf("store: 拒绝好友请求失败: %w", err)
		}
		return nil
	})
}

// ListFriends 返回某用户的好友（accepted，对称语义：任一方都能查到对方）。
// 返回的每一行里 PeerName 是**对方**的显示名。
func (s *Store) ListFriends(ctx context.Context, userID int64) ([]Friendship, error) {
	var rows []Friendship
	err := s.db.WithContext(ctx).
		Table("friendships AS f").
		Select("f.id, f.requester_id, f.addressee_id, f.status, f.blocked_by_id, f.created_at, f.updated_at,"+
			" u.display_name AS peer_name").
		Joins("JOIN users u ON u.id = CASE WHEN f.requester_id = ? THEN f.addressee_id ELSE f.requester_id END", userID).
		Where("f.status = ? AND (f.requester_id = ? OR f.addressee_id = ?)", FriendshipAccepted, userID, userID).
		Order("f.updated_at DESC, f.id DESC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("store: 查询好友列表失败: %w", err)
	}
	return rows, nil
}

// ListIncomingRequests 返回发给 userID 的 pending 请求（收件箱，PeerName 是请求方）。
func (s *Store) ListIncomingRequests(ctx context.Context, userID int64) ([]Friendship, error) {
	var rows []Friendship
	err := s.db.WithContext(ctx).
		Table("friendships AS f").
		Select("f.id, f.requester_id, f.addressee_id, f.status, f.blocked_by_id, f.created_at, f.updated_at,"+
			" u.display_name AS peer_name").
		Joins("JOIN users u ON u.id = f.requester_id").
		Where("f.status = ? AND f.addressee_id = ?", FriendshipPending, userID).
		Order("f.created_at DESC, f.id DESC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("store: 查询待处理请求失败: %w", err)
	}
	return rows, nil
}

// ListOutgoingRequests 返回 userID 发出的 pending 请求（PeerName 是被请求方）。
func (s *Store) ListOutgoingRequests(ctx context.Context, userID int64) ([]Friendship, error) {
	var rows []Friendship
	err := s.db.WithContext(ctx).
		Table("friendships AS f").
		Select("f.id, f.requester_id, f.addressee_id, f.status, f.blocked_by_id, f.created_at, f.updated_at,"+
			" u.display_name AS peer_name").
		Joins("JOIN users u ON u.id = f.addressee_id").
		Where("f.status = ? AND f.requester_id = ?", FriendshipPending, userID).
		Order("f.created_at DESC, f.id DESC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("store: 查询已发出请求失败: %w", err)
	}
	return rows, nil
}

// RemoveFriendship 删除好友关系（accepted → 删除）。双方都能删（对称语义）。
func (s *Store) RemoveFriendship(ctx context.Context, friendshipID, actorID int64) error {
	if friendshipID <= 0 {
		return ErrFriendshipNotFound
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var f Friendship
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", friendshipID).Take(&f).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrFriendshipNotFound
		}
		if err != nil {
			return fmt.Errorf("store: 查询好友关系失败: %w", err)
		}
		if f.Status != FriendshipAccepted {
			return ErrFriendshipNotFound
		}
		if f.RequesterID != actorID && f.AddresseeID != actorID {
			return ErrFriendshipForbidden
		}
		if err := tx.Delete(&Friendship{}, friendshipID).Error; err != nil {
			return fmt.Errorf("store: 删除好友关系失败: %w", err)
		}
		return nil
	})
}

// BlockUser 拉黑某人（服务端强制，§7.1）。
//
// 与 CreateFriendshipRequest 同一条"一段关系只存一行"的裁决：
//   - 已有一条关系（任何方向、任何状态）→ 原地转成 blocked，blocked_by_id=actor；
//   - 没有 → 新建 blocked 行，requester=actor（拉黑方）、addressee=target。
//
// 这样拉黑会**覆盖**已有的好友/待处理请求，天然实现"好友请求被拒、列表互不可见"。
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
		existing, err := lockFriendPair(tx, actorID, targetID)
		if err != nil {
			return err
		}
		if existing != nil {
			// 原地转 blocked：无论之前是 pending 还是 accepted，拉黑都覆盖它。
			if err := tx.Model(&Friendship{}).Where("id = ?", existing.ID).
				Updates(map[string]any{
					"status":        FriendshipBlocked,
					"blocked_by_id": actorID,
					"updated_at":    time.Now(),
				}).Error; err != nil {
				return fmt.Errorf("store: 拉黑失败: %w", err)
			}
			existing.Status = FriendshipBlocked
			existing.BlockedByID = &actorID
			row = existing
			return nil
		}
		now := time.Now()
		row = &Friendship{
			RequesterID: actorID,
			AddresseeID: targetID,
			Status:      FriendshipBlocked,
			BlockedByID: &actorID,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := tx.Create(row).Error; err != nil {
			if isUniqueViolation(err) {
				// 并发竞态兜底：已有关系，重读一次返回它（调用方拿到的是 blocked 事实）。
				var f Friendship
				if gerr := tx.Where(`(requester_id = ? AND addressee_id = ?) OR (requester_id = ? AND addressee_id = ?)`,
					actorID, targetID, targetID, actorID).Take(&f).Error; gerr == nil {
					row = &f
					return nil
				}
			}
			return fmt.Errorf("store: 拉黑失败: %w", err)
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

// FriendshipByID 按主键取一条关系行（只读）。
//
// 用途只有一个：删除/变更**之前**记下"对方是谁"（实时推送需要收件人 id）。
// 之所以不并进 RemoveFriendship 的返回值：那会改一个已被 #29 真库用例覆盖的
// 签名，而"尽力而为地取一个收件人 id"不该让那个签名变形。
//
// 不做权限判定（这里没有 actor 概念）：调用方拿到的行只用于自己算对方是谁，
// 真正的越权判定仍在 RemoveFriendship 的事务里（I3：授权在服务端，且在最知道
// 事实的那一层）。
func (s *Store) FriendshipByID(ctx context.Context, id int64) (*Friendship, error) {
	if id <= 0 {
		return nil, ErrFriendshipNotFound
	}
	var f Friendship
	err := s.db.WithContext(ctx).Where("id = ?", id).Take(&f).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrFriendshipNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: 查询好友关系失败: %w", err)
	}
	return &f, nil
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

// areFriendsIn 判断 a 与 b 之间是否有一条 accepted 的好友关系（任一方向）。
//
// 与 blockStatusBetweenIn 同一条纪律：吃 *gorm.DB 而不是连接池，让"发消息前的
// 关系校验"能跑在 SendDM 的**同一个事务**里 —— 否则"先查是好友"与"再插入消息"
// 之间，对方刚删了好友，消息就发给了已经不再是好友的人。
// accepted 是**对称**的（§7.1），所以一条查询两个方向都认。
func areFriendsIn(ctx context.Context, db *gorm.DB, a, b int64) (bool, error) {
	var f Friendship
	err := db.WithContext(ctx).
		Where(`status = ? AND ((requester_id = ? AND addressee_id = ?) OR (requester_id = ? AND addressee_id = ?))`,
			FriendshipAccepted, a, b, b, a).
		Take(&f).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: 查询好友关系失败: %w", err)
	}
	return true, nil
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
//  2. **带上关系状态**。`relation ∈ none|friend|pending-out|pending-in|blocked`
//     由调用方（handler）再查一次关系行得到 —— 不在这里返回，是因为"关系"是
//     friendships 域的事实，而本方法的职责是"在 users 里找人"。
//     少了它前端每个结果还得再问一次，且"加好友"按钮的可用性要靠猜。
//  3. **blocked 行仍然作为结果出现**（relation=blocked，不是隐藏）。
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
		err := s.db.WithContext(ctx).
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
	err := s.db.WithContext(ctx).
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
	BlockedID   int64     `gorm:"column:blocked_id"`
	DisplayName string    `gorm:"column:display_name"`
	Username    string    `gorm:"column:username"`
	BlockedAt   time.Time `gorm:"column:blocked_at"`
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
		        u.display_name, u.username, f.updated_at AS blocked_at`, actorID).
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
//  3. **删除后可以重新发请求**：行没了，CreateFriendshipRequest 的裁决自然放行
//     （验收第 1 条）。这也意味着解除拉黑是一个**可逆的、完整的**动作 ——
//     与 #28 记录的"拉黑单向不可逆"痛点对应。
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
