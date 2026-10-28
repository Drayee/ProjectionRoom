package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/model"
	"ProjectionRoom/internal/service"
	"ProjectionRoom/internal/service/auth"
	"ProjectionRoom/internal/store"
)

// 本文件是 `/ws/user` 的协议面证据（wayfinder #31）。
//
// 判据分四组：
//  1. **准入**：无票据 401、坏票据 401、Origin 被拒 403、封禁 1008；
//  2. **就绪帧**：连上之后第一帧是 user-ready（客户端据此知道通道可用）；
//  3. **投递**：一条私聊推到收件人的全部连接；已读回执推给发送方；
//  4. **离线**：对方不在线时不报错（§7.2 的"只落库"分支）。
//
// 全部用真实 WebSocket + 完整 NewRouter，而不是直接调 handler：
// 票据消费、Origin 白名单、升级、Hub 注册这些都在链路里，绕开它们就测不到
// 真正的准入语义。

// userWSEnv 是 /ws/user 用例的装配。
type userWSEnv struct {
	srv   *httptest.Server
	hub   *service.UserHub
	st    *store.Store
	cfg   *config.Config
	alice *store.User
	bob   *store.User
	// tickets 与 /api/auth/ws-ticket 是同一个实例（签发的票 /ws/user 才认）。
	tickets *auth.TicketStore
	// pusher 是生产用的推送器（用例直接调它来"模拟领域事件"）。
	pusher *service.RealtimePusher
	// acctStore 是认证侧的内存用户表（票据 → Me 会查它）。
	// 用例中途造新用户时必须两边都写（见 messages_test.go 的同一条说明）。
	acctStore *fakeAdminStore
}

// newUserWSEnv 起一个带真库 + 真 UserHub 的服务。
func newUserWSEnv(t *testing.T) *userWSEnv {
	t.Helper()

	st := requireMsgTestDB(t)
	ctx := context.Background()

	cfg := config.Default()
	cfg.Segment.TempDir = t.TempDir()
	cfg.Static.Serve = false
	cfg.Auth.DBDSN = "host=127.0.0.1 dbname=fixture sslmode=disable"
	if cfg.Auth.PublicBaseURL == "" {
		cfg.Auth.PublicBaseURL = "https://pr.test"
	}
	cfg.IPC.LoginPerMinute = 1000
	cfg.IPC.LoginBurst = 200
	cfg.IPC.RoomMetaPerMinute = 1000
	cfg.IPC.RoomMetaBurst = 200

	alice := &store.User{Username: "ws-alice", DisplayName: "爱丽丝", PasswordHash: "hash-a"}
	if err := st.CreateUser(ctx, alice); err != nil {
		t.Fatalf("建 alice 失败：%v", err)
	}
	bob := &store.User{Username: "ws-bob", DisplayName: "鲍勃", PasswordHash: "hash-b"}
	if err := st.CreateUser(ctx, bob); err != nil {
		t.Fatalf("建 bob 失败：%v", err)
	}
	// alice 与 bob 默认是好友：§7.2 之后私聊限定在好友之间，
	// 而本文件的投递用例验的是"推没推到"，不是关系门槛本身。
	// （TestPresenceOnlyToFriends 会另建 carol 作为"非好友"的对照。）
	reqAB, err := st.CreateFriendshipRequest(ctx, alice.ID, bob.ID)
	if err != nil {
		t.Fatalf("建好友请求失败：%v", err)
	}
	if _, err := st.AcceptFriendshipRequest(ctx, reqAB.ID, bob.ID); err != nil {
		t.Fatalf("接受好友请求失败：%v", err)
	}

	signer, err := auth.NewSigner(handlerTestSecret, 15*time.Minute)
	if err != nil {
		t.Fatalf("构造 Signer 失败：%v", err)
	}
	tickets := auth.NewTicketStore(cfg.Auth.WSTicketTTL)

	acctStore := newFakeAdminStore()
	acctStore.users[alice.ID] = alice
	acctStore.users[bob.ID] = bob
	acctStore.nextID = bob.ID
	svc, err := service.NewAccountService(acctStore, signer, tickets, service.AccountOptions{
		RefreshTTL:     cfg.Auth.RefreshTTL,
		EmailVerifyTTL: cfg.Auth.EmailVerifyTTL,
		OTPTTL:         cfg.Auth.OTPTTL,
		PublicBaseURL:  cfg.Auth.PublicBaseURL,
	})
	if err != nil {
		t.Fatalf("构造 AccountService 失败：%v", err)
	}

	hub, cleanupHub, err := service.NewHub(cfg)
	if err != nil {
		t.Fatalf("构造 Hub 失败：%v", err)
	}
	userHub, cleanupUserHub, err := service.NewUserHub(cfg)
	if err != nil {
		t.Fatalf("构造 UserHub 失败：%v", err)
	}
	rooms := service.NewManager(cfg, hub)
	t.Cleanup(func() { rooms.Stop(); cleanupUserHub(); cleanupHub() })

	pusher := service.NewRealtimePusher(userHub, st)

	engine := NewRouter(cfg, hub, rooms, nil, nil,
		AuthDeps{
			Service: svc,
			Session: SessionConfig{
				AccessTTL:   cfg.Auth.AccessTTL,
				RefreshTTL:  cfg.Auth.RefreshTTL,
				WSTicketTTL: cfg.Auth.WSTicketTTL,
			},
			Store:         st,
			Tickets:       tickets,
			UserHub:       userHub,
			MessagePusher: pusher,
			FriendPusher:  pusher,
			Presence:      pusher,
		},
		AdminDeps{},
	)
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)

	return &userWSEnv{
		srv: srv, hub: userHub, st: st, cfg: cfg,
		alice: alice, bob: bob, tickets: tickets, pusher: pusher,
		acctStore: acctStore,
	}
}

// ticketFor 给某个账号签一张一次性票据（与 /api/auth/ws-ticket 走同一张表）。
func (e *userWSEnv) ticketFor(t *testing.T, u *store.User) string {
	t.Helper()
	return e.tickets.Issue(u.ID)
}

// wsURL 拼 /ws/user 的地址。
func (e *userWSEnv) wsURL(ticket, clientID string) string {
	url := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/ws/user?ticket=" + ticket
	if clientID != "" {
		url += "&clientId=" + clientID
	}
	return url
}

// dialUser 连上 /ws/user（失败直接 Fatal）。
func (e *userWSEnv) dialUser(t *testing.T, u *store.User, clientID string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, e.wsURL(e.ticketFor(t, u), clientID), nil)
	if err != nil {
		t.Fatalf("连接 /ws/user 失败：%v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "test done") })
	return conn
}

// dialUserWithTicket 用**现成的**票据连上 /ws/user。
//
// 与 dialUser 的区别只是"票从哪来"：这里让用例自己先 Issue 一张，
// 用于"同一个账号要连两次"或"刚创建、还没进认证侧夹具"的用户。
func (e *userWSEnv) dialUserWithTicket(t *testing.T, ticket, clientID string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, e.wsURL(ticket, clientID), nil)
	if err != nil {
		t.Fatalf("连接 /ws/user 失败：%v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "test done") })
	return conn
}

// readEnvelope 读一帧并解码（带超时，避免用例挂死）。
func readEnvelope(t *testing.T, conn *websocket.Conn) *model.Envelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("读帧失败：%v", err)
	}
	env, err := model.Unmarshal(data)
	if err != nil {
		t.Fatalf("解码帧失败：%v", err)
	}
	return env
}

// TestUserWSRequiresTicket 覆盖准入第 1 条：票据是必填的（与 /ws 的游客路径不同）。
func TestUserWSRequiresTicket(t *testing.T) {
	env := newUserWSEnv(t)

	// 完全不带 ticket 参数。
	url := "ws" + strings.TrimPrefix(env.srv.URL, "http") + "/ws/user"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, url, nil)
	if err == nil {
		t.Fatalf("不带票据不该连上")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		got := 0
		if resp != nil {
			got = resp.StatusCode
		}
		t.Fatalf("不带票据应 401：got %d", got)
	}

	// 带了无效票据。
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	_, resp2, err := websocket.Dial(ctx2, env.wsURL("not-a-real-ticket", ""), nil)
	if err == nil {
		t.Fatalf("坏票据不该连上")
	}
	if resp2 == nil || resp2.StatusCode != http.StatusUnauthorized {
		got := 0
		if resp2 != nil {
			got = resp2.StatusCode
		}
		t.Fatalf("坏票据应 401：got %d", got)
	}
}

// TestUserWSOriginEnforced 覆盖准入第 2 条：Origin 白名单与 /ws 同一把尺子。
func TestUserWSOriginEnforced(t *testing.T) {
	env := newUserWSEnv(t)

	ticket := env.ticketFor(t, env.alice)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 伪造一个不在白名单里的 Origin。
	_, resp, err := websocket.Dial(ctx, env.wsURL(ticket, ""), &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"http://evil.example"}},
	})
	if err == nil {
		t.Fatalf("非法 Origin 不该连上")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		got := 0
		if resp != nil {
			got = resp.StatusCode
		}
		t.Fatalf("非法 Origin 应 403：got %d", got)
	}
}

// TestUserWSReadyFrame 覆盖判据第 2 条：连上之后第一帧是 user-ready。
func TestUserWSReadyFrame(t *testing.T) {
	env := newUserWSEnv(t)

	conn := env.dialUser(t, env.alice, "alice-1")
	env1 := readEnvelope(t, conn)
	if env1.Type != model.TypeUserReady {
		t.Fatalf("第一帧应是 %s：got %q", model.TypeUserReady, env1.Type)
	}
	if !env1.UserChannelReady {
		t.Fatalf("user-ready 帧应带 UserChannelReady=true")
	}

	// Hub 里应该真的有这条连接。
	if !env.hub.Online(env.alice.ID) {
		t.Fatalf("alice 连上之后 Hub 应认为她在线")
	}
}

// TestUserWSDeliversDM 覆盖判据第 3 条：一条私聊推到收件人的全部连接。
func TestUserWSDeliversDM(t *testing.T) {
	env := newUserWSEnv(t)
	ctx := context.Background()

	// bob 开两条连接（手机 + 电脑），两条都该收到。
	conn1 := env.dialUser(t, env.bob, "bob-1")
	readEnvelope(t, conn1) // 吃掉 ready 帧
	conn2 := env.dialUser(t, env.bob, "bob-2")
	readEnvelope(t, conn2)

	// 造一条消息（真库）再推。
	msg, err := env.st.SendDM(ctx, env.alice.ID, env.bob.ID, "你好")
	if err != nil {
		t.Fatalf("发消息失败：%v", err)
	}
	if !env.pusher.PushDM(ctx, msg) {
		t.Fatalf("PushDM 应报告至少一条连接收到")
	}

	for i, conn := range []*websocket.Conn{conn1, conn2} {
		got := readEnvelope(t, conn)
		if got.Type != model.TypeDM {
			t.Fatalf("第 %d 条连接应收到 dm：got %q", i+1, got.Type)
		}
		if got.DM == nil {
			t.Fatalf("第 %d 条连接的 dm 载荷为空", i+1)
		}
		if got.DM.ID != msg.ID || got.DM.Body != "你好" {
			t.Fatalf("第 %d 条连接的载荷不对：%+v", i+1, got.DM)
		}
		if got.DM.SenderID != env.alice.ID || got.DM.RecipientID != env.bob.ID {
			t.Fatalf("第 %d 条连接的收发方不对：%+v", i+1, got.DM)
		}
		if got.DM.SenderName != env.alice.DisplayName {
			t.Fatalf("第 %d 条连接应带发送者昵称：%q", i+1, got.DM.SenderName)
		}
	}
}

// TestUserWSDeliversDMRead 覆盖判据第 3 条的后半：已读回执推给**发送方**。
func TestUserWSDeliversDMRead(t *testing.T) {
	env := newUserWSEnv(t)
	ctx := context.Background()

	// alice 是发送方，她开一条连接等回执。
	aliceConn := env.dialUser(t, env.alice, "alice-r")
	readEnvelope(t, aliceConn)

	msg, err := env.st.SendDM(ctx, env.alice.ID, env.bob.ID, "读我")
	if err != nil {
		t.Fatalf("发消息失败：%v", err)
	}
	if _, err := env.st.MarkDMRead(ctx, env.bob.ID, env.alice.ID, msg.ID); err != nil {
		t.Fatalf("标记已读失败：%v", err)
	}

	// bob 是读的人，alice 是收件人（回执的收件人）。
	if !env.pusher.PushDMRead(ctx, env.bob.ID, env.alice.ID, msg.ID, time.Now()) {
		t.Fatalf("PushDMRead 应报告送达")
	}

	got := readEnvelope(t, aliceConn)
	if got.Type != model.TypeDMRead {
		t.Fatalf("应收到 dm-read：got %q", got.Type)
	}
	if got.DM == nil || got.DM.ID != msg.ID {
		t.Fatalf("回执应带被读的那条消息 id：%+v", got.DM)
	}
	if got.DM.ReadAt == "" {
		t.Fatalf("回执应带 readAt")
	}
	// 回执**不带正文**（客户端按 Type 决定渲染，不靠字段有无去猜）。
	if got.DM.Body != "" {
		t.Fatalf("回执不该带正文：%q", got.DM.Body)
	}
}

// TestUserWSOfflineIsNotAnError 覆盖判据第 4 条：对方不在线时推送返回 false 但不 panic。
func TestUserWSOfflineIsNotAnError(t *testing.T) {
	env := newUserWSEnv(t)
	ctx := context.Background()

	// 没有任何连接。
	if env.hub.Online(env.bob.ID) {
		t.Fatalf("bob 不该在线")
	}
	msg, err := env.st.SendDM(ctx, env.alice.ID, env.bob.ID, "离线消息")
	if err != nil {
		t.Fatalf("发消息失败：%v", err)
	}
	// 推送报告"没人收到"，但**消息已经在库里**（§7.2 的"否则只落库"）。
	if env.pusher.PushDM(ctx, msg) {
		t.Fatalf("没有连接时 PushDM 应返回 false")
	}
	rows, _, err := env.st.ListDM(ctx, env.bob.ID, env.alice.ID, 0, 50)
	if err != nil {
		t.Fatalf("查历史失败：%v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("离线消息应已落库：got %d 条", len(rows))
	}
}

// TestUserWSCloseByUserOnBan 覆盖"封禁要同时切断两条通道"：
// CloseByUser 之后该账号的用户通道连接必须被关掉。
func TestUserWSCloseByUserOnBan(t *testing.T) {
	env := newUserWSEnv(t)

	conn := env.dialUser(t, env.alice, "alice-ban")
	readEnvelope(t, conn)

	if !env.hub.Online(env.alice.ID) {
		t.Fatalf("连上后应在线")
	}
	if n := env.hub.CloseByUser(env.alice.ID); n != 1 {
		t.Fatalf("应断开 1 条连接：got %d", n)
	}
	if env.hub.Online(env.alice.ID) {
		t.Fatalf("断开后不该在线")
	}

	// 客户端应当看到一次非正常关闭（1008 + 可读原因）。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, _, err := conn.Read(ctx)
		if err == nil {
			continue // 丢弃关闭前的残余帧
		}
		status := websocket.CloseStatus(err)
		if status != websocket.StatusPolicyViolation {
			t.Fatalf("应以 1008 关闭：got %v", status)
		}
		break
	}
}

// TestUserWSRejectsDuplicateClientID 覆盖"同一 clientId 不能有两条活跃连接"。
func TestUserWSRejectsDuplicateClientID(t *testing.T) {
	env := newUserWSEnv(t)

	_ = env.dialUser(t, env.alice, "same-id")

	// 第二个连接用同一个 clientId，应当被拒（1008）。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, env.wsURL(env.ticketFor(t, env.alice), "same-id"), nil)
	if err != nil {
		t.Fatalf("握手本身应成功（拒绝发生在升级之后）：%v", err)
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "done") }()

	// 读循环会先拿到一条错误信封，然后连接被 1008 关闭。
	for {
		_, _, err := conn.Read(ctx)
		if err == nil {
			continue
		}
		if status := websocket.CloseStatus(err); status != websocket.StatusPolicyViolation {
			t.Fatalf("重复 clientId 应以 1008 关闭：got %v", status)
		}
		break
	}
}

// TestUserWSUnregisterReportsOffline 钉住 UserHub 的上下线语义：
// 只有**最后一条**连接断开才报告 wentOffline（两条连接同时断时不能都报）。
func TestUserWSUnregisterReportsOffline(t *testing.T) {
	env := newUserWSEnv(t)

	// 必须把 ready 帧读掉：不读的话服务端写 pump 会在队列积压后关掉连接，
	// 于是 a1 可能在断言之前就被自动注销了（那是另一条路径，不是本用例要验的）。
	c1 := env.dialUser(t, env.alice, "a1")
	readEnvelope(t, c1)
	c2 := env.dialUser(t, env.alice, "a2")
	readEnvelope(t, c2)

	if _, offline := env.hub.Unregister("a1"); offline {
		t.Fatalf("还有一条连接在时不该报告离线")
	}
	if _, offline := env.hub.Unregister("a2"); !offline {
		t.Fatalf("最后一条断开时应报告离线")
	}
	if env.hub.Online(env.alice.ID) {
		t.Fatalf("全部断开后不该在线")
	}
}

// TestUserHubRejectsZeroUser 钉住"用户通道只服务已登录账号"。
func TestUserHubRejectsZeroUser(t *testing.T) {
	env := newUserWSEnv(t)

	if _, err := env.hub.Register("zero", 0, nil); err == nil {
		t.Fatalf("userID=0 应被拒")
	}
}

// TestPresenceOnlyToFriends 钉住 presence 的范围：只推给**好友**且只推在线的。
func TestPresenceOnlyToFriends(t *testing.T) {
	env := newUserWSEnv(t)
	ctx := context.Background()

	// alice 与 bob 在夹具里已经是好友（见 newUserWSEnv）；carol 不是。
	bobConn := env.dialUser(t, env.bob, "bob-p")
	readEnvelope(t, bobConn) // ready

	// alice 上线 → bob（好友，在线）应收到 presence。
	env.pusher.PushPresence(ctx, env.alice.ID, true)
	got := readEnvelope(t, bobConn)
	if got.Type != model.TypePresence {
		t.Fatalf("好友应收到 presence：got %q", got.Type)
	}
	if got.Presence == nil || got.Presence.UserID != env.alice.ID || !got.Presence.Online {
		t.Fatalf("presence 载荷不对：%+v", got.Presence)
	}

	// carol 不是好友。她真连一条 WS，然后断言**收不到** alice 的 presence。
	//
	// 用真连接而不是伪造内部状态：presence 的范围判据就是"推送时按好友列表过滤"，
	// 而这条判据只有在目标真的在线时才有意义（离线的话"没收到"是废话）。
	carol := &store.User{Username: "ws-carol", DisplayName: "卡罗尔", PasswordHash: "hash-c"}
	if err := env.st.CreateUser(ctx, carol); err != nil {
		t.Fatalf("建 carol 失败：%v", err)
	}
	// carol 也要能通过认证侧（票据 → Me 会查内存表），否则她连不上。
	env.acctStore.mu.Lock()
	env.acctStore.users[carol.ID] = carol
	env.acctStore.mu.Unlock()
	carolConn := env.dialUserWithTicket(t, env.tickets.Issue(carol.ID), "carol-p")
	readEnvelope(t, carolConn) // ready

	env.pusher.PushPresence(ctx, env.alice.ID, true)

	// bob 应再收到一条 presence（他仍是好友）。
	got = readEnvelope(t, bobConn)
	if got.Type != model.TypePresence {
		t.Fatalf("bob 应再收到 presence：got %q", got.Type)
	}

	// carol 不该收到任何帧：读超时即证明"没推给她"。
	// 2 秒足够 —— 同一进程内的投递是即时的，若推了必然在这之前到达。
	shortCtx, shortCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shortCancel()
	if _, _, err := carolConn.Read(shortCtx); err == nil {
		t.Fatalf("carol 不是 alice 的好友，不该收到 presence")
	}

	// 顺带钉住 FriendIDs 的范围（它是 presence 过滤的依据）。
	ids, err := env.pusher.FriendIDs(ctx, env.alice.ID)
	if err != nil {
		t.Fatalf("取好友列表失败：%v", err)
	}
	for _, id := range ids {
		if id == carol.ID {
			t.Fatalf("carol 不该在 alice 的好友列表里：%v", ids)
		}
	}
	found := false
	for _, id := range ids {
		if id == env.bob.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("bob 应在 alice 的好友列表里：%v", ids)
	}
}

// 断言：RealtimePusher 在编译期满足 handler 的三个窄接口。
var (
	_ MessagePusher       = (*service.RealtimePusher)(nil)
	_ FriendPusher        = (*service.RealtimePusher)(nil)
	_ PresenceBroadcaster = (*service.RealtimePusher)(nil)
	_                     = errors.New
)
