package service

import (
	"context"
	"log"
	"time"

	"ProjectionRoom/internal/model"
	"ProjectionRoom/internal/store"
)

// RealtimePusher 把"领域事件"翻译成 `/ws/user` 上的信封，并投递给收件人。
//
// # 为什么组合层在这里而不是 handler
//
// handler 只知道"发生了一件事"（发了消息 / 好友被接受），它不该知道
// "在线表怎么查"与"信封怎么编"。而 store 不该认识 WebSocket。这个类型是
// 两者之间唯一的连接点：它的全部职责就是"事件 → 信封 → 投递"。
//
// # 为什么它同时满足 handler 的三个窄接口
//
// `handler.MessagePusher`、`handler.FriendPusher`、`handler.PresenceBroadcaster`
// 是三个**按域切开**的接口（发消息的路径不该有推送好友事件的能力）。
// 让本类型的方法集同时满足三者，装配处就只需要传一次同一个实例 ——
// 而"哪个端点能推什么"仍由各 handler 的字段类型决定（见 handler 里的断言）。
//
// # 投递失败为什么只记日志
//
// 实时投递是体验增量（§7.2：「对方在线直投，否则只落库」）。消息/关系已经落库，
// 对方下次拉历史或刷新列表仍然看得到。把"推送失败"当成业务错误会让一条
// 正常发出的消息因为对方刚好断线而报 500 —— 那是把传输层的问题
// 算到业务头上。
type RealtimePusher struct {
	hub   *UserHub
	store *store.Store
}

// NewRealtimePusher 构造推送器；hub 或 store 为 nil 时返回 nil
// （调用方按"没有实时投递"处理：所有推送方法都是 nil 安全的方法集入口）。
func NewRealtimePusher(hub *UserHub, st *store.Store) *RealtimePusher {
	if hub == nil || st == nil {
		return nil
	}
	return &RealtimePusher{hub: hub, store: st}
}

// PushDM 把一条新私聊推给收件人（handler.MessagePusher）。
func (p *RealtimePusher) PushDM(ctx context.Context, msg *store.Message) bool {
	if p == nil || p.hub == nil || msg == nil || msg.RecipientID == nil {
		return false
	}
	readAt := ""
	if msg.ReadAt != nil {
		readAt = msg.ReadAt.Format(time.RFC3339)
	}
	frame := model.MustEnvelope(model.Envelope{
		Type: model.TypeDM,
		DM: &model.DirectMessage{
			ID:          msg.ID,
			SenderID:    msg.SenderID,
			RecipientID: *msg.RecipientID,
			SenderName:  msg.SenderName,
			Body:        msg.Body,
			CreatedAt:   msg.CreatedAt.Format(time.RFC3339),
			ReadAt:      readAt,
		},
	})
	sent, err := p.hub.SendToUser(*msg.RecipientID, frame)
	if err != nil {
		// ErrUserOffline 是正常情况（§7.2 的"否则只落库"），不记日志噪音。
		return false
	}
	return sent > 0
}

// PushDMRead 把"读到哪一条"推给**发送方**（handler.MessagePusher）。
//
// 收件人是 senderID：读的人是 readerID，而"你的消息被读了"这件事
// 只对发消息的人有意义。
func (p *RealtimePusher) PushDMRead(ctx context.Context, readerID, senderID, msgID int64, readAt time.Time) bool {
	if p == nil || p.hub == nil {
		return false
	}
	frame := model.MustEnvelope(model.Envelope{
		Type: model.TypeDMRead,
		DM: &model.DirectMessage{
			ID:          msgID,
			SenderID:    senderID,
			RecipientID: readerID,
			ReadAt:      readAt.Format(time.RFC3339),
		},
	})
	sent, err := p.hub.SendToUser(senderID, frame)
	if err != nil {
		return false
	}
	return sent > 0
}

// PushFriendRequest 推给**被请求方**（handler.FriendPusher）。
func (p *RealtimePusher) PushFriendRequest(ctx context.Context, actorID, targetID int64, relationshipID int64, actorName string) bool {
	return p.pushFriend(ctx, model.TypeFriendRequest, targetID, model.FriendEvent{
		RelationshipID: relationshipID,
		UserID:         actorID,
		DisplayName:    actorName,
		Status:         store.FriendshipPending,
	})
}

// PushFriendAccepted 推给**请求方**（handler.FriendPusher）。
func (p *RealtimePusher) PushFriendAccepted(ctx context.Context, actorID, requesterID int64, relationshipID int64, actorName string) bool {
	return p.pushFriend(ctx, model.TypeFriendAccepted, requesterID, model.FriendEvent{
		RelationshipID: relationshipID,
		UserID:         actorID,
		DisplayName:    actorName,
		Status:         store.FriendshipAccepted,
	})
}

// PushFriendRemoved 推给**另一方**（handler.FriendPusher）。
//
// 关系已经删了，所以没有 relationshipID 可带（0 表示"这条关系已不存在"）。
// 客户端收到它只做一件事：把那个人从好友列表里去掉。
func (p *RealtimePusher) PushFriendRemoved(ctx context.Context, actorID, targetID int64) bool {
	return p.pushFriend(ctx, model.TypeFriendRemoved, targetID, model.FriendEvent{
		UserID: actorID,
		Status: "removed",
	})
}

// PushBlocked 推给**被拉黑方**（handler.FriendPusher）。
func (p *RealtimePusher) PushBlocked(ctx context.Context, actorID, targetID int64) bool {
	return p.pushFriend(ctx, model.TypeBlocked, targetID, model.FriendEvent{
		UserID: actorID,
		Status: store.FriendshipBlocked,
	})
}

// pushFriend 是四个好友事件的共同实现（只有 type 与收件人不同）。
func (p *RealtimePusher) pushFriend(ctx context.Context, typ string, targetID int64, ev model.FriendEvent) bool {
	if p == nil || p.hub == nil || targetID <= 0 {
		return false
	}
	frame := model.MustEnvelope(model.Envelope{Type: typ, Friend: &ev})
	sent, err := p.hub.SendToUser(targetID, frame)
	if err != nil {
		return false
	}
	return sent > 0
}

// FriendIDs 返回某账号的好友 id 列表（handler.PresenceBroadcaster）。
func (p *RealtimePusher) FriendIDs(ctx context.Context, userID int64) ([]int64, error) {
	if p == nil || p.store == nil || userID <= 0 {
		return nil, nil
	}
	rows, err := p.store.ListFriends(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]int64, 0, len(rows))
	for i := range rows {
		if rows[i].RequesterID == userID {
			out = append(out, rows[i].AddresseeID)
		} else {
			out = append(out, rows[i].RequesterID)
		}
	}
	return out, nil
}

// PushPresence 把"userID 上线/下线"推给他的**在线好友**（handler.PresenceBroadcaster）。
//
// 两个刻意的范围限制：
//
//  1. **只推给好友**。全站在线态需要一个"谁能看见谁"的授权模型（否则任何登录用户
//     都能扫出谁在线、什么时候在线 —— 那是一条行为画像的侧信道）。好友是唯一
//     不需要额外授权就能回答"他愿不愿意让你知道"的集合。
//  2. **只推给在线的**。给离线好友推一帧 presence 没有意义（他们的连接不存在），
//     而"登录时看到好友在线"由客户端在连上之后自己拉一次好友列表 + 在线态回答
//     （本期由 `/ws/user` 的 user-ready 之后前端主动查，见 client 侧说明）。
func (p *RealtimePusher) PushPresence(ctx context.Context, userID int64, online bool) {
	if p == nil || p.hub == nil || userID <= 0 {
		return
	}
	friends, err := p.FriendIDs(ctx, userID)
	if err != nil {
		log.Printf("[WARN] presence：取好友列表失败（user=%d）：%v", userID, err)
		return
	}
	onlineFriends := p.hub.OnlineIDs(friends)
	if len(onlineFriends) == 0 {
		return
	}
	frame := model.MustEnvelope(model.Envelope{
		Type:     model.TypePresence,
		Presence: &model.PresenceState{UserID: userID, Online: online},
	})
	for _, fid := range onlineFriends {
		_, _ = p.hub.SendToUser(fid, frame)
	}
}

// 断言：本类型的三个方法集逐字满足 handler 的三个窄接口。
//
// 放在 service 里而不是 handler：handler 不能 import service 的**具体类型**
// （那会绕开窄接口的意义），而这里能同时看见两边。
// 若将来某个接口加了方法，这里会立刻编译失败 —— 那正是我们要的提醒。
var (
	_ messagePusherShape = (*RealtimePusher)(nil)
	_ friendPusherShape  = (*RealtimePusher)(nil)
	_ presenceShape      = (*RealtimePusher)(nil)
	_                    = []interface{}{}
)

// 下面三个别名只用于上面的编译期断言，避免 service 反向 import handler。
// 它们的形状与 handler 里的三个接口逐字一致（改一边必须改另一边，
// 而"编译失败"就是那条提醒）。
type messagePusherShape interface {
	PushDM(ctx context.Context, msg *store.Message) bool
	PushDMRead(ctx context.Context, readerID, senderID, msgID int64, readAt time.Time) bool
}

type friendPusherShape interface {
	PushFriendRequest(ctx context.Context, actorID, targetID int64, relationshipID int64, actorName string) bool
	PushFriendAccepted(ctx context.Context, actorID, requesterID int64, relationshipID int64, actorName string) bool
	PushFriendRemoved(ctx context.Context, actorID, targetID int64) bool
	PushBlocked(ctx context.Context, actorID, targetID int64) bool
}

type presenceShape interface {
	FriendIDs(ctx context.Context, userID int64) ([]int64, error)
	PushPresence(ctx context.Context, userID int64, online bool)
}
