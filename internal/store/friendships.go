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
)

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
