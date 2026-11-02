package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// 关注名单（票 #58）—— 好友页三个列表的存储层。
//
// # 为什么不是三个函数
//
// 「好友 / 关注 / 粉丝」在界面上是三个 tab，但它们在存储层是**同一条查询的三种方向**：
// 都从 follows 表出发，都带同一组可见性过滤（未封禁、无拉黑）。
// 写成三个函数就会把那段过滤抄三遍 —— 而"拉黑要不要过滤"这种改动
// 一旦漏掉一处，症状是"某一份名单里还留着拉黑的人"，很难发现。
//
// 所以：一个函数 + 一个 tab 参数，过滤条件只写一次。
//
// # 为什么带 username 与 displayName
//
// 名单是**人**的列表（要显示昵称，可能还要显示 `@username`），
// 与 `ListFollowing/ListFollowers`（只回 id，给关注流筛作者用）的分工不同：
// 那两个是"我要一批 id 去查别的表"，这里是"我要一批人直接画出来"。
// 让调用方拿 id 再逐个查 users 就是 N+1。

// FollowListTab 是名单的方向。
type FollowListTab string

const (
	// FollowListMutual 是互关（= 好友，ADR 0001）。
	FollowListMutual FollowListTab = "mutual"
	// FollowListFollowing 是我关注的人。
	FollowListFollowing FollowListTab = "following"
	// FollowListFollowers 是关注我的人（粉丝）。
	FollowListFollowers FollowListTab = "followers"
)

// ErrFollowListBadTab 是"未知名单方向"。
//
// 未知值**不猜**：猜错的后果是界面显示了另一份名单，而用户会以为那是真的。
var ErrFollowListBadTab = errors.New("store: 未知的名单方向")

// FollowListEntry 是名单里的一行。
//
// 关系布尔是**相对当前用户**的方向（Following = 我关注了 TA），
// 与 `followView` 的语义一致 —— 页面因此可以拿同一套按钮逻辑画名单与搜索结果。
type FollowListEntry struct {
	// UserID 是名单里的那个人。
	UserID int64
	// Username 是登录名（可能用于 `@handle` 显示与搜索跳转）。
	Username string
	// DisplayName 是昵称。
	DisplayName string
	// Following 是"我关注了 TA"。
	Following bool
	// FollowedBy 是"TA 关注了我"。
	FollowedBy bool
	// Since 是这条边建立的时间（名单按它倒序）。
	Since time.Time
}

// Mutual 回答"这一行是不是互关"。
func (e FollowListEntry) Mutual() bool { return e.Following && e.FollowedBy }

// CanDM 回答"我能不能给这一行的人发私聊"。
//
// 名单本身已经过滤掉拉黑存续的对（见 listFollowQuery），
// 所以这里只剩互关那一项 —— 与 `FollowState.CanDM` 同口径。
func (e FollowListEntry) CanDM() bool { return e.Mutual() }

// FollowPeerState 是批量关系查询的一行（搜索结果用）。
//
// 比 `FollowState` 多一个 `Blocked`：搜索结果的按钮要按"拉黑存续"整个消失，
// 而那件事 `FollowState` 表达不了（它只有两个方向 + 两个计数）。
type FollowPeerState struct {
	FollowState
	// Blocked 表示"这一对之间存在拉黑"（方向不区分，与 can_dm 的口径一致）。
	Blocked bool
}

// CanDM 覆盖内嵌的同名方法：拉黑存续期**一律**不可私聊。
//
// 为什么必须覆盖而不是让调用方自己 `&& !Blocked`：
// 少写一次 `&&` 的后果是"被拉黑的人还能收到消息"，而那是服务端强制的性质，
// 不该依赖每个调用点都记得多写一个条件。
func (s FollowPeerState) CanDM() bool { return s.FollowState.Mutual() && !s.Blocked }

// ListFollowList 查一份关注名单。
//
// 过滤（三个 tab 共用，只写一次）：
//
//  1. **未封禁**：封禁账号在界面上不存在（公开档案也回 404），
//     留在名单里就是"点进去一个 404 页"。
//  2. **无拉黑**（双向）：拉黑的意思就是"从我的世界里拿掉"，
//     所以拉黑方与被拉黑方**都**看不到对方（票 #42 §4）。
//
// 排序：按边的建立时间倒序（最近关注的在最前）—— 与
// `ListFollowing/ListFollowers` 同一口径，也与"粉丝红点看最新"的直觉一致。
func (s *Store) ListFollowList(ctx context.Context, userID int64, tab FollowListTab) ([]FollowListEntry, error) {
	if userID <= 0 {
		return []FollowListEntry{}, nil
	}
	if tab != FollowListMutual && tab != FollowListFollowing && tab != FollowListFollowers {
		return nil, ErrFollowListBadTab
	}

	db := s.db.WithContext(ctx).Table("follows AS f").
		Select(`p.id AS user_id, p.username, p.display_name,
			EXISTS (SELECT 1 FROM follows x WHERE x.follower_id = ? AND x.followee_id = p.id) AS following,
			EXISTS (SELECT 1 FROM follows y WHERE y.follower_id = p.id AND y.followee_id = ?) AS followed_by,
			f.created_at AS since`, userID, userID).
		Joins("JOIN users p ON p.id = f.followee_id").
		Where("f.follower_id = ?", userID)

	switch tab {
	case FollowListFollowing:
		// 我关注的人：f 就是"我→TA"，join 已经对上。
	case FollowListMutual:
		// 互关：还要有一条反向边。
		db = db.Where("EXISTS (SELECT 1 FROM follows r WHERE r.follower_id = p.id AND r.followee_id = ?)", userID)
	case FollowListFollowers:
		// 粉丝：f 应当是"TA→我"，所以 join 与 where 都要换方向。
		db = s.db.WithContext(ctx).Table("follows AS f").
			Select(`p.id AS user_id, p.username, p.display_name,
				EXISTS (SELECT 1 FROM follows x WHERE x.follower_id = ? AND x.followee_id = p.id) AS following,
				EXISTS (SELECT 1 FROM follows y WHERE y.follower_id = p.id AND y.followee_id = ?) AS followed_by,
				f.created_at AS since`, userID, userID).
			Joins("JOIN users p ON p.id = f.follower_id").
			Where("f.followee_id = ?", userID)
	}

	// 共用可见性过滤（三支都在这里，不各写一遍）。
	db = db.Where("p.status = ?", StatusActive).
		Where(`NOT EXISTS (
			SELECT 1 FROM friendships b
			 WHERE b.status = 'blocked'
			   AND ((b.requester_id = ? AND b.addressee_id = p.id)
			     OR (b.requester_id = p.id AND b.addressee_id = ?))
		)`, userID, userID).
		Order("f.created_at DESC, f.id DESC")

	var rows []FollowListEntry
	if err := db.Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("store: 查询关注名单失败: %w", err)
	}
	if rows == nil {
		rows = []FollowListEntry{}
	}
	return rows, nil
}

// FollowStatesFor 批量算"我与这批人各自是什么关系"（搜索结果用）。
//
// 一条 SQL 拿全部：搜索是边打边搜的高频读端点，20 条结果逐行查就是 21 次往返
// （N+1 的典型症状是"搜索明显卡"，而它很容易被误判成"数据库慢"）。
//
// 与 `FollowStateBetween` 的差别：那边是"一个人"（附带 TA 的两个计数），
// 这里是"一批人"（只判方向，不数计数 —— 搜索结果不显示 TA 的粉丝数）。
func (s *Store) FollowStatesFor(ctx context.Context, viewerID int64, peerIDs []int64) (map[int64]FollowPeerState, error) {
	out := make(map[int64]FollowPeerState, len(peerIDs))
	if viewerID <= 0 || len(peerIDs) == 0 {
		return out, nil
	}

	const q = `
SELECT
  p.id AS user_id,
  EXISTS (SELECT 1 FROM follows x WHERE x.follower_id = ? AND x.followee_id = p.id) AS following,
  EXISTS (SELECT 1 FROM follows y WHERE y.follower_id = p.id AND y.followee_id = ?) AS followed_by,
  EXISTS (
    SELECT 1 FROM friendships b
     WHERE b.status = 'blocked'
       AND ((b.requester_id = ? AND b.addressee_id = p.id)
         OR (b.requester_id = p.id AND b.addressee_id = ?))
  ) AS blocked
FROM users p
WHERE p.id IN ?`

	var rows []struct {
		UserID     int64 `gorm:"column:user_id"`
		Following  bool  `gorm:"column:following"`
		FollowedBy bool  `gorm:"column:followed_by"`
		Blocked    bool  `gorm:"column:blocked"`
	}
	if err := s.db.WithContext(ctx).Raw(q, viewerID, viewerID, viewerID, viewerID, peerIDs).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("store: 批量查询关注关系失败: %w", err)
	}
	for _, r := range rows {
		out[r.UserID] = FollowPeerState{
			FollowState: FollowState{Following: r.Following, FollowedBy: r.FollowedBy},
			Blocked:     r.Blocked,
		}
	}
	return out, nil
}

// 保住 gorm 与 time 的引用：本文件目前只用 Table/Select/Scan 这套链式 API，
// 显式 import 让"将来要加事务或 Locking"的位置显眼。
var _ = gorm.ErrRecordNotFound
var _ = time.Time{}
