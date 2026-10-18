package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// 本文件是 wayfinder #31 的真库证据：私聊落库、历史游标、已读上报、未读计数、
// 会话聚合，以及"拉黑由服务端强制"这条 §7.1 的硬性质。
//
// 判据全部落在**库里的真实行**上（不 mock）：私聊的正确性有一半是 DDL 的
// CHECK 约束与部分索引的形状，那是 mock 验不出来的东西。

// befriend 让 a 与 b 成为 accepted 好友（§7.2 要求私聊限定在好友之间）。
func befriend(t *testing.T, s *Store, a, b *User) {
	t.Helper()
	ctx := context.Background()
	req, err := s.CreateFriendshipRequest(ctx, a.ID, b.ID)
	if err != nil {
		t.Fatalf("发好友请求失败：%v", err)
	}
	if _, err := s.AcceptFriendshipRequest(ctx, req.ID, b.ID); err != nil {
		t.Fatalf("接受好友请求失败：%v", err)
	}
}

func TestSendDMAndListHistory(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()

	a := seedUser(t, s, "dm-alice")
	b := seedUser(t, s, "dm-bob")
	befriend(t, s, a, b)

	first, err := s.SendDM(ctx, a.ID, b.ID, "  第一条  ")
	if err != nil {
		t.Fatalf("发私聊失败：%v", err)
	}
	// 正文首尾空白由 store 复核（清洗在 service 层，这里验的是"落库形态"）。
	if first.Body != "第一条" {
		t.Fatalf("正文应被 TrimSpace：got %q", first.Body)
	}
	if first.Kind != MessageKindDM {
		t.Fatalf("kind 应为 dm：got %q", first.Kind)
	}
	if first.RecipientID == nil || *first.RecipientID != b.ID {
		t.Fatalf("recipient_id 应为 %d：got %v", b.ID, first.RecipientID)
	}
	if first.RoomID != nil {
		t.Fatalf("dm 行的 room_id 必须是 NULL：got %v", *first.RoomID)
	}
	if first.SenderName != a.DisplayName {
		t.Fatalf("应回带发送者昵称：want %q got %q", a.DisplayName, first.SenderName)
	}
	if first.ID == 0 || first.CreatedAt.IsZero() {
		t.Fatalf("应回填自增 id 与 created_at：%+v", first)
	}

	// 回一条（反方向），再发一条，验证历史包含**双向**。
	if _, err := s.SendDM(ctx, b.ID, a.ID, "回复"); err != nil {
		t.Fatalf("回私聊失败：%v", err)
	}
	third, err := s.SendDM(ctx, a.ID, b.ID, "第三条")
	if err != nil {
		t.Fatalf("发第三条失败：%v", err)
	}

	rows, hasMore, err := s.ListDM(ctx, a.ID, b.ID, 0, 50)
	if err != nil {
		t.Fatalf("查历史失败：%v", err)
	}
	if hasMore {
		t.Fatalf("3 条消息不该 hasMore")
	}
	if len(rows) != 3 {
		t.Fatalf("历史应有 3 条（双向都算）：got %d", len(rows))
	}
	// 最新在前。
	if rows[0].ID != third.ID {
		t.Fatalf("历史应按 id DESC（最新在前）：首行 id want %d got %d", third.ID, rows[0].ID)
	}
	for i := 1; i < len(rows); i++ {
		if rows[i].ID >= rows[i-1].ID {
			t.Fatalf("历史顺序必须严格递减：第 %d 行 id=%d >= 第 %d 行 id=%d", i, rows[i].ID, i-1, rows[i-1].ID)
		}
	}

	// before 游标：取 third 之前的一页，应当不含 third。
	page2, _, err := s.ListDM(ctx, a.ID, b.ID, third.ID, 50)
	if err != nil {
		t.Fatalf("用 before 查历史失败：%v", err)
	}
	if len(page2) != 2 {
		t.Fatalf("before=%d 应返回 2 条：got %d", third.ID, len(page2))
	}
	for _, m := range page2 {
		if m.ID >= third.ID {
			t.Fatalf("before 游标必须排除 >= 它的行：got id=%d", m.ID)
		}
	}

	// 第三方视角：c 看不到 a 与 b 的对话。
	c := seedUser(t, s, "dm-carol")
	other, _, err := s.ListDM(ctx, c.ID, b.ID, 0, 50)
	if err != nil {
		t.Fatalf("第三方查历史失败：%v", err)
	}
	if len(other) != 0 {
		t.Fatalf("第三方不该看到别人的对话：got %d 条", len(other))
	}
}

// TestSendDMRejectsBadInput 覆盖坏输入：自收、收件人不存在、空白、超长。
// TestSendDMRejectsNonFriends 钉住 §7.2「先校验关系」：
// 非好友之间不允许发私聊（否则私聊退化成"知道 userId 就能骚扰任何人"）。
func TestSendDMRejectsNonFriends(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()

	a := seedUser(t, s, "dm-nofriend-a")
	b := seedUser(t, s, "dm-nofriend-b")

	// 没有任何关系：不是好友 → ErrMessageNotFriends。
	if _, err := s.SendDM(ctx, a.ID, b.ID, "hi"); !errors.Is(err, ErrMessageNotFriends) {
		t.Fatalf("非好友发送应返回 ErrMessageNotFriends：got %v", err)
	}
	// 只有 pending（a 发了请求、b 没接受）：仍然不是好友 → 拒。
	req, err := s.CreateFriendshipRequest(ctx, a.ID, b.ID)
	if err != nil {
		t.Fatalf("发好友请求失败：%v", err)
	}
	_ = req
	if _, err := s.SendDM(ctx, a.ID, b.ID, "hi"); !errors.Is(err, ErrMessageNotFriends) {
		t.Fatalf("pending 状态仍应返回 ErrMessageNotFriends：got %v", err)
	}

	// 被拒的消息不得落库。
	var n int64
	if err := s.DB().Model(&Message{}).Where("sender_id = ? AND recipient_id = ?", a.ID, b.ID).Count(&n).Error; err != nil {
		t.Fatalf("统计消息失败：%v", err)
	}
	if n != 0 {
		t.Fatalf("被拒的消息不该落库：got %d 条", n)
	}
}

func TestSendDMRejectsBadInput(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()

	a := seedUser(t, s, "dm-bad-a")

	if _, err := s.SendDM(ctx, a.ID, a.ID, "自言自语"); !errors.Is(err, ErrMessageSelfTarget) {
		t.Fatalf("给自己发应返回 ErrMessageSelfTarget：got %v", err)
	}
	if _, err := s.SendDM(ctx, a.ID, 999999, "发给不存在的人"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("收件人不存在应返回 ErrNotFound：got %v", err)
	}
	if _, err := s.SendDM(ctx, a.ID, a.ID+1, "   "); err == nil {
		t.Fatalf("空白正文应被拒")
	}

	b := seedUser(t, s, "dm-bad-b")
	befriend(t, s, a, b)
	// 超长：按字符算（2001 个汉字也必须被拒 —— 按字节算会让中文在 667 字就被拒，
	// 与 DDL 的 text 列口径不符）。
	if _, err := s.SendDM(ctx, a.ID, b.ID, strings.Repeat("字", MaxMessageBodyLen+1)); err == nil {
		t.Fatalf("超过 %d 字符应被拒", MaxMessageBodyLen)
	}
	// 边界值：正好 2000 字符必须能发。
	if _, err := s.SendDM(ctx, a.ID, b.ID, strings.Repeat("字", MaxMessageBodyLen)); err != nil {
		t.Fatalf("正好 %d 字符应当可以发送：%v", MaxMessageBodyLen, err)
	}
}

// TestSendDMBlockedIsEnforcedByServer 是 §7.1"服务端强制"的直接证据：
// 拉黑之后，两个方向都发不进去（不是前端隐藏按钮）。
func TestSendDMBlockedIsEnforcedByServer(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()

	a := seedUser(t, s, "dm-block-a")
	b := seedUser(t, s, "dm-block-b")
	befriend(t, s, a, b)

	// 拉黑前：双向都通。
	if _, err := s.SendDM(ctx, a.ID, b.ID, "拉黑前"); err != nil {
		t.Fatalf("拉黑前应当能发：%v", err)
	}

	if _, err := s.BlockUser(ctx, a.ID, b.ID); err != nil {
		t.Fatalf("拉黑失败：%v", err)
	}

	// 被拉黑方（b）发给拉黑方（a）：必须被拒。
	if _, err := s.SendDM(ctx, b.ID, a.ID, "被拉黑后"); !errors.Is(err, ErrMessageBlocked) {
		t.Fatalf("被拉黑方发送应返回 ErrMessageBlocked：got %v", err)
	}
	// 反方向（a 发给 b）也拒：见 store.ErrMessageBlocked 的双向理由。
	if _, err := s.SendDM(ctx, a.ID, b.ID, "拉黑方自己发"); !errors.Is(err, ErrMessageBlocked) {
		t.Fatalf("反方向也应返回 ErrMessageBlocked：got %v", err)
	}

	// 库里不能出现那两条被拒的消息（拦截必须发生在插入之前）。
	var n int64
	if err := s.DB().Model(&Message{}).Where("body IN ?", []string{"被拉黑后", "拉黑方自己发"}).Count(&n).Error; err != nil {
		t.Fatalf("统计消息失败：%v", err)
	}
	if n != 0 {
		t.Fatalf("被拒的消息不该落库：got %d 条", n)
	}
}

func TestMarkDMReadAndUnreadCount(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()

	a := seedUser(t, s, "dm-read-a")
	b := seedUser(t, s, "dm-read-b")
	c := seedUser(t, s, "dm-read-c")
	befriend(t, s, a, b)
	// 下面那两条消息是 c → b，所以需要的是 **b 与 c** 的好友关系（不是 a 与 c）：
	// §7.2 之后私聊限定在好友之间，缺这条会得到 ErrMessageNotFriends。
	befriend(t, s, b, c)

	m1, err := s.SendDM(ctx, a.ID, b.ID, "1")
	if err != nil {
		t.Fatalf("发消息失败：%v", err)
	}
	if _, err := s.SendDM(ctx, a.ID, b.ID, "2"); err != nil {
		t.Fatalf("发消息失败：%v", err)
	}
	if _, err := s.SendDM(ctx, c.ID, b.ID, "c 发的"); err != nil {
		t.Fatalf("发消息失败：%v", err)
	}

	// b 的未读：3 条（两条来自 a、一条来自 c）。
	unread, err := s.CountUnreadDM(ctx, b.ID)
	if err != nil {
		t.Fatalf("统计未读失败：%v", err)
	}
	if unread != 3 {
		t.Fatalf("b 的未读应为 3：got %d", unread)
	}
	// 发送方自己没有未读（未读是收件人的事实）。
	if n, err := s.CountUnreadDM(ctx, a.ID); err != nil || n != 0 {
		t.Fatalf("发送方不该有未读：got %d err=%v", n, err)
	}

	// b 上报"读到 a 的第一条"：只应影响那一行。
	updated, err := s.MarkDMRead(ctx, b.ID, a.ID, m1.ID)
	if err != nil {
		t.Fatalf("标记已读失败：%v", err)
	}
	if updated != 1 {
		t.Fatalf("应只标记 1 行：got %d", updated)
	}
	if n, _ := s.CountUnreadDM(ctx, b.ID); n != 2 {
		t.Fatalf("标记后未读应为 2：got %d", n)
	}

	// 幂等：重复上报同样的位置 → 0 行（read_at IS NULL 让它是 no-op）。
	again, err := s.MarkDMRead(ctx, b.ID, a.ID, m1.ID)
	if err != nil {
		t.Fatalf("重复标记已读失败：%v", err)
	}
	if again != 0 {
		t.Fatalf("重复标记应为 no-op（0 行）：got %d", again)
	}

	// **发送方不能替收件人标已读**：a 调 MarkDMRead 指向自己发出的消息 → 0 行。
	if n, err := s.MarkDMRead(ctx, a.ID, b.ID, m1.ID); err != nil || n != 0 {
		t.Fatalf("发送方不该能把消息标成对方已读：got %d err=%v", n, err)
	}

	// upTo <= 0 不是"全部已读"（否则一个少传参数的请求会清空所有未读）。
	if n, err := s.MarkDMRead(ctx, b.ID, a.ID, 0); err == nil || n != 0 {
		t.Fatalf("upTo<=0 应被拒（且不标记任何行）：got %d err=%v", n, err)
	}
	if n, _ := s.CountUnreadDM(ctx, b.ID); n != 2 {
		t.Fatalf("被拒的上报不该改变未读：got %d", n)
	}
}

func TestListDMConversations(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()

	a := seedUser(t, s, "dm-conv-a")
	b := seedUser(t, s, "dm-conv-b")
	c := seedUser(t, s, "dm-conv-c")
	befriend(t, s, a, b)
	befriend(t, s, a, c)

	// a ↔ b 两条，a ↔ c 一条（c 那条更新）。
	if _, err := s.SendDM(ctx, a.ID, b.ID, "给 b 的第一条"); err != nil {
		t.Fatalf("发消息失败：%v", err)
	}
	if _, err := s.SendDM(ctx, b.ID, a.ID, "b 的回复"); err != nil {
		t.Fatalf("发消息失败：%v", err)
	}
	last, err := s.SendDM(ctx, c.ID, a.ID, "c 的消息")
	if err != nil {
		t.Fatalf("发消息失败：%v", err)
	}

	convs, err := s.ListDMConversations(ctx, a.ID)
	if err != nil {
		t.Fatalf("查会话列表失败：%v", err)
	}
	if len(convs) != 2 {
		t.Fatalf("a 应有 2 个会话：got %d（%+v）", len(convs), convs)
	}
	// 按最近消息倒序：c 那条最新。
	if convs[0].PeerID != c.ID {
		t.Fatalf("首个会话应是 c：got peer=%d", convs[0].PeerID)
	}
	if convs[0].LastID != last.ID {
		t.Fatalf("会话的 last_id 应是最近一条：want %d got %d", last.ID, convs[0].LastID)
	}
	if convs[0].PeerName != c.DisplayName {
		t.Fatalf("会话应带对方昵称：want %q got %q", c.DisplayName, convs[0].PeerName)
	}
	// a 的未读：只有 c 发来的那条（b 的回复是发给 a 的？不 —— 是 b→a，所以也算未读）。
	// 精确断言：c 会话未读 1；b 会话未读 1（b 的回复未读）。
	var bConv *DMConversation
	for i := range convs {
		if convs[i].PeerID == b.ID {
			bConv = &convs[i]
		}
	}
	if bConv == nil {
		t.Fatalf("应有与 b 的会话")
	}
	if bConv.UnreadCount != 1 {
		t.Fatalf("与 b 的会话未读应为 1：got %d", bConv.UnreadCount)
	}

	// 标记 c 的消息已读后，该会话未读归零。
	if _, err := s.MarkDMRead(ctx, a.ID, c.ID, last.ID); err != nil {
		t.Fatalf("标记已读失败：%v", err)
	}
	convs2, err := s.ListDMConversations(ctx, a.ID)
	if err != nil {
		t.Fatalf("再查会话列表失败：%v", err)
	}
	for _, cv := range convs2 {
		if cv.PeerID == c.ID && cv.UnreadCount != 0 {
			t.Fatalf("c 会话的未读应归零：got %d", cv.UnreadCount)
		}
	}

	// 完全没有消息的用户：空列表（不是 nil 崩溃）。
	d := seedUser(t, s, "dm-conv-d")
	empty, err := s.ListDMConversations(ctx, d.ID)
	if err != nil {
		t.Fatalf("空会话列表查询失败：%v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("没有消息的用户应得到空列表：got %d", len(empty))
	}
}

// TestMessagesTableCheckConstraints 钉住 0012 的三条 CHECK：
// 它们是"哪一列必填"的唯一强制点（代码里的 if 会被下一个调用点忘掉）。
func TestMessagesTableCheckConstraints(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	a := seedUser(t, s, "dm-chk-a")
	b := seedUser(t, s, "dm-chk-b")

	cases := []struct {
		name string
		sql  string
		args []any
	}{
		{
			name: "kind 不在枚举内",
			sql:  `INSERT INTO messages (kind, sender_id, recipient_id, body) VALUES ('xx', ?, ?, 'hi')`,
			args: []any{a.ID, b.ID},
		},
		{
			name: "dm 缺 recipient_id",
			sql:  `INSERT INTO messages (kind, sender_id, body) VALUES ('dm', ?, 'hi')`,
			args: []any{a.ID},
		},
		{
			name: "dm 带了 room_id",
			sql:  `INSERT INTO messages (kind, room_id, sender_id, recipient_id, body) VALUES ('dm', 'abc123', ?, ?, 'hi')`,
			args: []any{a.ID, b.ID},
		},
		{
			name: "room 缺 room_id",
			sql:  `INSERT INTO messages (kind, sender_id, body) VALUES ('room', ?, 'hi')`,
			args: []any{a.ID},
		},
		{
			name: "空白正文",
			sql:  `INSERT INTO messages (kind, sender_id, recipient_id, body) VALUES ('dm', ?, ?, '   ')`,
			args: []any{a.ID, b.ID},
		},
	}
	for _, tc := range cases {
		if err := s.DB().WithContext(ctx).Exec(tc.sql, tc.args...).Error; err == nil {
			t.Fatalf("%s：应当被 CHECK 拒绝，但写进去了", tc.name)
		}
	}
}
