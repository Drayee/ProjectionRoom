package handler

import (
	"context"
	"testing"

	"github.com/coder/websocket"

	"ProjectionRoom/internal/model"
	"ProjectionRoom/internal/service"
)

// 这个文件钉住「关注推送帧里必须带对方显示名」（票 #67）。
//
// # 为什么值得单独一个用例
//
// 真机验收发现过一个静默缺陷：`PushFollowCreated` 只填了 `UserID`，
// `DisplayName` 是空串。前端拿到事件后要立刻弹出「XXX 关注了你」，
// 而它把"名字为空"解释成"这条不该弹提示"（这是对的 —— 宁可只有红点，
// 也不要弹一条「 关注了你」）。于是症状是**红点与提示一起消失**，
// 而服务端日志里一切正常、`SendToUser` 返回 1。
//
// 这种缺陷只有两种东西能抓住：真机脚本（要起后端 + 浏览器 + 两个账号）
// 或这一条 httptest（几百毫秒）。所以两者都留着，但这一条是回归哨兵。

// readUntilType 读到指定类型的帧为止（跳过 presence 之类的旁路推送）。
//
// 为什么需要它：`/ws/user` 是**一条**连接承载所有下行事件，第二个账号上线
// 会先给第一个账号推一帧 presence。直接 `readEnvelope` 一次就断言类型，
// 测的其实是"这两件事哪个先到"，而那与关注推送毫无关系。
func readUntilType(t *testing.T, conn *websocket.Conn, want string) *model.Envelope {
	t.Helper()
	for i := 0; i < 8; i++ {
		env := readEnvelope(t, conn)
		if env.Type == want {
			return env
		}
	}
	t.Fatalf("连读 8 帧都没有 %q", want)
	return nil
}

// TestFollowCreatedFrameCarriesDisplayName 断言「有人关注了你」那一帧里
// `displayName` 是**关注者**的名字（不是收件人自己的名字）。
func TestFollowCreatedFrameCarriesDisplayName(t *testing.T) {
	env := newUserWSEnv(t)

	// alice 收到：bob 关注了她。
	conn := env.dialUser(t, env.alice, "u-follow-name")
	// 先吃掉上线时的 user-ready 帧（它不带 friend 载荷）。
	ready := readEnvelope(t, conn)
	if ready.Type != model.TypeUserReady {
		t.Fatalf("首帧应是 user-ready：got %q", ready.Type)
	}

	if ok := env.pusher.PushFollowCreated(context.Background(), env.bob.ID, env.alice.ID); !ok {
		t.Fatalf("PushFollowCreated 应返回 true（alice 在线）")
	}

	env2 := readUntilType(t, conn, model.TypeFollowCreated)
	if env2.Type != model.TypeFollowCreated {
		t.Fatalf("帧类型应是 follow-created：got %q", env2.Type)
	}
	if env2.Friend == nil {
		t.Fatalf("follow-created 必须带 friend 载荷")
	}
	if env2.Friend.UserID != env.bob.ID {
		t.Fatalf("帧里的 userId 应是关注者 bob(%d)：got %d", env.bob.ID, env2.Friend.UserID)
	}
	if env2.Friend.DisplayName != env.bob.DisplayName {
		t.Fatalf("帧里的 displayName 应是 bob 的显示名 %q：got %q（空串会让客户端整条提示都不弹）",
			env.bob.DisplayName, env2.Friend.DisplayName)
	}
}

// TestMutualFollowFramesCarryCounterpartName 断言互关的两帧各自带**对方**的名字。
//
// 这条容易写错的地方是"两个人都收到、但都带自己的名字" —— 那在界面上表现为
// 「我自己 和你互相关注，成为好友了」，而且只在互关那一瞬间出现，极难复现。
func TestMutualFollowFramesCarryCounterpartName(t *testing.T) {
	env := newUserWSEnv(t)

	aliceConn := env.dialUser(t, env.alice, "u-mutual-a")
	bobConn := env.dialUser(t, env.bob, "u-mutual-b")
	readEnvelope(t, aliceConn) // user-ready
	readEnvelope(t, bobConn)   // user-ready

	if ok := env.pusher.PushMutualFollow(context.Background(), env.alice.ID, env.bob.ID); !ok {
		t.Fatalf("PushMutualFollow 应返回 true（两边都在线）")
	}

	toAlice := readUntilType(t, aliceConn, model.TypeMutualFollow)
	if toAlice.Type != model.TypeMutualFollow || toAlice.Friend == nil {
		t.Fatalf("alice 应收到 mutual-follow 且带载荷：got %q", toAlice.Type)
	}
	if toAlice.Friend.UserID != env.bob.ID || toAlice.Friend.DisplayName != env.bob.DisplayName {
		t.Fatalf("alice 收到的应是 bob(%d, %q)：got (%d, %q)",
			env.bob.ID, env.bob.DisplayName, toAlice.Friend.UserID, toAlice.Friend.DisplayName)
	}

	toBob := readUntilType(t, bobConn, model.TypeMutualFollow)
	if toBob.Type != model.TypeMutualFollow || toBob.Friend == nil {
		t.Fatalf("bob 应收到 mutual-follow 且带载荷：got %q", toBob.Type)
	}
	if toBob.Friend.UserID != env.alice.ID || toBob.Friend.DisplayName != env.alice.DisplayName {
		t.Fatalf("bob 收到的应是 alice(%d, %q)：got (%d, %q)",
			env.alice.ID, env.alice.DisplayName, toBob.Friend.UserID, toBob.Friend.DisplayName)
	}
}

// TestFollowPushUnknownUserIsEmptyName 断言查不到显示名时是**空串**而不是崩掉。
//
// 客户端把空串解释成"这条不弹提示"，所以这个降级是安全的：
// 退化成"只有红点、没有提示"，而不是一条没有主语的句子。
func TestFollowPushUnknownUserIsEmptyName(t *testing.T) {
	env := newUserWSEnv(t)

	conn := env.dialUser(t, env.alice, "u-follow-ghost")
	readEnvelope(t, conn) // user-ready

	// 999999999 这个 id 不存在：推送不该 panic，帧里 displayName 是空串。
	if ok := env.pusher.PushFollowCreated(context.Background(), 999999999, env.alice.ID); !ok {
		t.Fatalf("收件人在线时即使关注者不存在也应投递成功（帧里名字为空）")
	}
	env2 := readUntilType(t, conn, model.TypeFollowCreated)
	if env2.Friend == nil {
		t.Fatalf("帧应带 friend 载荷")
	}
	if env2.Friend.DisplayName != "" {
		t.Fatalf("查不到用户时 displayName 应是空串：got %q", env2.Friend.DisplayName)
	}
}

// 断言：RealtimePusher 也满足票 #67 的关注推送窄接口。
var _ FollowPusher = (*service.RealtimePusher)(nil)
