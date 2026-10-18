package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// 私聊域的上限（docs/ACCOUNTS.md §7.2）。
//
// **必须与 handler 的校验、前端的输入限制同值**（同一条纪律见 posts.go）：
// 不一致的症状是"输入框能打进去、保存报 400"，而用户看不出是长度问题。
const (
	// MaxMessageBodyLen 是消息正文上限（§7.2：≤2000）。列是 text，
	// 上限由这里与 DDL 的 CHECK (btrim(body) <> '') 一起管。
	MaxMessageBodyLen = 2000
	// defaultMessagePage 是历史分页的条数（§7.2 定死 50）。
	//
	// 与帖子域不同，这个值**不做成参数**：私聊历史是"往上翻"的动作，
	// 页大小由协议固定让前端不必传、也让两端的翻页逻辑只存在一套。
	defaultMessagePage = 50
)

// 消息种类（§4 的 kind 列）。
const (
	MessageKindDM   = "dm"
	MessageKindRoom = "room"
)

// 私聊域的语义错误。store 定义、handler 翻译成 4xx
// （同 anime.go / posts.go / friendships.go 的纪律：不把 SQL 细节泄漏到 HTTP 层）。
var (
	// ErrMessageNotFound 是"这条消息不存在"。
	ErrMessageNotFound = errors.New("store: 消息不存在")
	// ErrMessageSelfTarget 是"不能给自己发私聊"。
	//
	// 为什么在 store 层拒：会话的主键是"一对用户"，自己与自己这一对
	// 在界面上没有入口、也没有语义（发出去永远显示成"我发给我"）。
	// 让它 400 而不是静默落库，是为了不让库里出现一类无人能读的行。
	ErrMessageSelfTarget = errors.New("store: 不能给自己发私聊")
	// ErrMessageBlocked 是"你们之间存在拉黑关系，服务端拒绝投递"（§7.1）。
	//
	// ⚠ 这是**服务端强制**，不是前端隐藏：被拉黑方即便自己拼 HTTP 请求也发不进来。
	// 方向不区分（谁拉黑谁都不投递）：拉黑是单向语义（"我不想收到你的消息"），
	// 但一个连自己发出的消息都收不到回执的通道对双方都没有意义，
	// 因此双向都拒 —— 与 §7.1"列表互不可见"是同一条口径。
	ErrMessageBlocked = errors.New("store: 你们之间存在拉黑关系，无法发送私聊")
	// ErrMessageNotFriends 是"你们不是好友"（§7.2「先校验关系 + 非拉黑」）。
	//
	// 为什么私聊必须限定在好友之间：否则私聊退化成"知道 userId 就能骚扰任何人"，
	// 而 userId 在公开档案（#33）落地后是公开的。好友关系是唯一不靠"对方愿不愿意"
	// 就能在服务端判定的门槛。
	ErrMessageNotFriends = errors.New("store: 只能给好友发私聊")
)

// Message 对应 messages 表（§4）。
//
// RoomID / RecipientID 都是可空列，与 DDL 的 CHECK 约束一一对应：
// `kind=dm` 只填 RecipientID，`kind=room` 只填 RoomID。
type Message struct {
	ID          int64      `gorm:"column:id;primaryKey"`
	Kind        string     `gorm:"column:kind"`
	RoomID      *string    `gorm:"column:room_id"`
	SenderID    int64      `gorm:"column:sender_id"`
	RecipientID *int64     `gorm:"column:recipient_id"`
	Body        string     `gorm:"column:body"`
	CreatedAt   time.Time  `gorm:"column:created_at"`
	ReadAt      *time.Time `gorm:"column:read_at"`

	// SenderName 是查询时 JOIN users 带出的显示名（只读，同 posts.AuthorName）。
	SenderName string `gorm:"column:sender_name;->"`
}

// TableName 见 User.TableName 的说明。
func (Message) TableName() string { return "messages" }

// SendDM 写入一条私聊，返回落库后的行（含自增 id 与 created_at）。
//
// 四件事在**一个事务**里：存在性/关系校验、插入、回读发送者昵称。
// 为什么校验也必须在事务里：先查"没拉黑"再插入，两条语句之间对方可能刚好拉黑，
// 于是"服务端强制"就有一个可插入的窗口。事务隔离级别是默认的 READ COMMITTED，
// 所以这里不是严格的线性化 —— 但它把窗口从"任意长"缩到"同一事务内的几条语句"。
//
// 正文清洗**不在这里**（与 CreateUser 同一条纪律）：调用方先
// service.StripControl → TrimSpace，存储层只复核"非空 + 长度"。
func (s *Store) SendDM(ctx context.Context, senderID, recipientID int64, body string) (*Message, error) {
	if senderID <= 0 || recipientID <= 0 {
		return nil, ErrMessageNotFound
	}
	if senderID == recipientID {
		return nil, ErrMessageSelfTarget
	}
	text := strings.TrimSpace(body)
	if text == "" {
		return nil, errors.New("store: 消息正文不能为空")
	}
	if len([]rune(text)) > MaxMessageBodyLen {
		return nil, fmt.Errorf("store: 消息超过 %d 个字符", MaxMessageBodyLen)
	}

	msg := &Message{
		Kind:        MessageKindDM,
		SenderID:    senderID,
		RecipientID: &recipientID,
		Body:        text,
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 收件人必须存在（FK 也会挡，但那条错误没有可操作信息；
		// 而且"发给一个不存在的 id"与"发给一个已销号的 id"对用户是同一种失败）。
		var exists int64
		if err := tx.Table("users").Where("id = ?", recipientID).Count(&exists).Error; err != nil {
			return fmt.Errorf("store: 检查收件人失败: %w", err)
		}
		if exists == 0 {
			return ErrNotFound
		}
		// 拉黑关系：两个方向都查（见 ErrMessageBlocked 的说明）。
		// 走事务句柄而不是连接池：这条判断与插入必须在同一个事务里（见上面的窗口说明）。
		blocked, _, err := blockStatusBetweenIn(ctx, tx, senderID, recipientID)
		if err != nil {
			return err
		}
		if blocked {
			return ErrMessageBlocked
		}

		// 好友关系：§7.2「服务端先校验关系 + 非拉黑」——"关系"就是必须是 accepted 好友。
		// 非好友之间不允许发私聊（否则私聊退化成"知道 id 就能骚扰任何人"的通道）。
		friends, err := areFriendsIn(ctx, tx, senderID, recipientID)
		if err != nil {
			return err
		}
		if !friends {
			return ErrMessageNotFriends
		}

		msg.CreatedAt = time.Now()
		if err := tx.Create(msg).Error; err != nil {
			return fmt.Errorf("store: 写入私聊失败: %w", err)
		}
		var name string
		if err := tx.Table("users").Where("id = ?", senderID).
			Select("display_name").Scan(&name).Error; err != nil {
			return fmt.Errorf("store: 查询发送者昵称失败: %w", err)
		}
		msg.SenderName = name
		return nil
	})
	if err != nil {
		return nil, err
	}
	return msg, nil
}

// ListDM 返回 peer 与 viewer 之间的一页私聊历史（**最新在前**），以及 hasMore。
//
// 三个刻意的形状决定：
//
//  1. **before 是 id 游标，不是 offset**：聊天是"往上翻"，而新消息会不断插在
//     头部 —— offset 分页在翻页期间会重复/漏行（#25 的日志分页踩过同一个坑）。
//     `id < before` 天然稳定：id 单调递增，与 created_at 同序。
//  2. **多取一行判 hasMore**：与 ListPosts 同一条理由，不做 COUNT。
//  3. **只返回 kind='dm'**：房间消息与私聊共用一张表（§4），但两者的
//     可见性规则完全不同，混进一个结果集就是把两套规则糊在一起。
//
// 返回顺序是**id DESC**（最新在前）：前端从下往上渲染、往上翻更早的，
// 与聊天界面的读法一致；反过来会让"第一屏"必须先把整页倒序一次。
func (s *Store) ListDM(ctx context.Context, viewerID, peerID, before int64, limit int) ([]Message, bool, error) {
	if viewerID <= 0 || peerID <= 0 {
		return nil, false, ErrMessageNotFound
	}
	if limit <= 0 || limit > defaultMessagePage {
		// 上限就是协议值：调用方传大了不该拿到更大的页（那会让"翻页"变成
		// 一次全量拉取，而私聊历史可以很长）。
		limit = defaultMessagePage
	}

	tx := s.db.WithContext(ctx).
		Table("messages AS m").
		Select("m.*, u.display_name AS sender_name").
		Joins("JOIN users u ON u.id = m.sender_id").
		Where("m.kind = ?", MessageKindDM).
		Where(`(m.sender_id = ? AND m.recipient_id = ?) OR (m.sender_id = ? AND m.recipient_id = ?)`,
			viewerID, peerID, peerID, viewerID)
	if before > 0 {
		tx = tx.Where("m.id < ?", before)
	}

	var rows []Message
	if err := tx.Order("m.id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, false, fmt.Errorf("store: 查询私聊历史失败: %w", err)
	}

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	return rows, hasMore, nil
}

// MarkDMRead 把 viewer 收到的、id <= upTo 的未读私聊置为已读，返回本次影响的行数。
//
// 两条判据：
//
//  1. **只动 recipient_id = viewer 的行**：已读位置是收件人的事实。允许发送方
//     "把消息标成对方已读"会让已读回执变成一句谎话。
//  2. **幂等**：`read_at IS NULL` 让重复上报变成 no-op（返回 0），
//     因此它可以被安全地重试 —— 这也是它用登录桶而不是注册桶的依据（#27 的分档纪律）。
//
// upTo <= 0 时**不**当作"全部已读"：那会让一个少传参数的请求静默清空所有未读，
// 而"全部已读"是另一个明确的动作（传当前最大 id）。这里返回 0 行，由 handler 报 400。
func (s *Store) MarkDMRead(ctx context.Context, viewerID, peerID, upTo int64) (int64, error) {
	if viewerID <= 0 || peerID <= 0 || upTo <= 0 {
		return 0, ErrMessageNotFound
	}
	res := s.db.WithContext(ctx).Model(&Message{}).
		Where("kind = ? AND recipient_id = ? AND sender_id = ? AND id <= ? AND read_at IS NULL",
			MessageKindDM, viewerID, peerID, upTo).
		Update("read_at", time.Now())
	if res.Error != nil {
		return 0, fmt.Errorf("store: 标记已读失败: %w", res.Error)
	}
	return res.RowsAffected, nil
}

// CountUnreadDM 返回某用户的未读私聊总数（`GET /api/auth/me` 的 unread 字段）。
//
// 走 messages_dm_unread_idx 这条部分索引：`(recipient_id, id DESC) WHERE kind='dm'
// AND read_at IS NULL`。于是"已读的行"完全不进这次扫描 —— 未读计数只与**未读量**
// 成正比，与历史总量无关。
//
// 为什么不用反范式计数列：见 0012 迁移的注释（漂移后没有对账路径）。
func (s *Store) CountUnreadDM(ctx context.Context, userID int64) (int64, error) {
	if userID <= 0 {
		return 0, nil
	}
	var n int64
	err := s.db.WithContext(ctx).Model(&Message{}).
		Where("kind = ? AND recipient_id = ? AND read_at IS NULL", MessageKindDM, userID).
		Count(&n).Error
	if err != nil {
		return 0, fmt.Errorf("store: 统计未读私聊失败: %w", err)
	}
	return n, nil
}

// ListDMConversations 返回某用户的会话列表（每个对话一行，按最近消息倒序）。
//
// 这是"会话是派生视图"这条决定的落点（见 0012 迁移注释）：会话没有自己的表，
// 每次按 messages 聚合出来。用一条窗口函数而不是 N 次单查：
// 每个 peer 取"我参与的、id 最大的那条 dm"，再按该 id 倒序。
//
// peer 用 CASE 归一（我发出的取收件人，我收到的取发送人）：两个方向必须落在
// 同一个会话键上，否则同一个对话会出现两行。
//
// 未读数在这里一并算（按 peer 的相关子查询）：会话列表本来就要显示每个对话的
// 未读徽标，分两次查会让"列表"与"徽标"有机会不一致。
func (s *Store) ListDMConversations(ctx context.Context, viewerID int64) ([]DMConversation, error) {
	if viewerID <= 0 {
		return nil, nil
	}
	const q = `
WITH mine AS (
	SELECT m.id, m.sender_id, m.recipient_id, m.body, m.created_at, m.read_at,
		CASE WHEN m.sender_id = ? THEN m.recipient_id ELSE m.sender_id END AS peer_id
	  FROM messages m
	 WHERE m.kind = 'dm' AND (m.sender_id = ? OR m.recipient_id = ?)
), latest AS (
	SELECT DISTINCT ON (peer_id) peer_id, id, sender_id, body, created_at
	  FROM mine
	 ORDER BY peer_id, id DESC
)
SELECT l.peer_id,
       u.display_name AS peer_name,
       l.id           AS last_id,
       l.sender_id    AS last_sender_id,
       l.body         AS last_body,
       l.created_at   AS last_created_at,
       (SELECT COUNT(*) FROM mine x
         WHERE x.peer_id = l.peer_id AND x.read_at IS NULL AND x.recipient_id = ?) AS unread
  FROM latest l
  JOIN users u ON u.id = l.peer_id
 ORDER BY l.id DESC`
	var rows []DMConversation
	if err := s.db.WithContext(ctx).Raw(q, viewerID, viewerID, viewerID, viewerID).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("store: 查询会话列表失败: %w", err)
	}
	return rows, nil
}

// DMConversation 是会话列表的一行（**查询产物，不是表**）。
//
// 它没有 TableName：GORM 的 Raw().Scan() 按列名回填，不参与任何 Create/Update。
type DMConversation struct {
	PeerID      int64     `gorm:"column:peer_id"`
	PeerName    string    `gorm:"column:peer_name"`
	LastID      int64     `gorm:"column:last_id"`
	LastSender  int64     `gorm:"column:last_sender_id"`
	LastBody    string    `gorm:"column:last_body"`
	LastSentAt  time.Time `gorm:"column:last_created_at"`
	UnreadCount int64     `gorm:"column:unread"`
}
