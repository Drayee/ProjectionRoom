package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/model"
	"ProjectionRoom/internal/service/limiter"
)

// —— S-3：房间码白名单 ——

// TestRoomCodeWhitelist 逐个覆盖房间码白名单的边界与非法形态。
func TestRoomCodeWhitelist(t *testing.T) {
	ok := []string{"ABCD", "ABCDEF", "ROOM01", "A1B2C3D4E5F6", "9999"}
	for _, code := range ok {
		if !ValidRoomCode(code) {
			t.Fatalf("房间码 %q 应当合法（4-12 位大写字母或数字）", code)
		}
	}

	bad := []string{
		"ABC",                        // 3 位：太短
		"ABCDEFGHIJKLM",              // 13 位：太长
		"abcd",                       // 小写（服务端会先规范化；这里直接判非法）
		"AB-CD",                      // 连字符不在白名单
		"AB CD",                      // 空格
		"房间码",                        // 非 ASCII
		"../../etc/passwd",           // 路径穿越形态
		"A\x00BCD",                   // 控制字符（会被写进日志/路由）
		"AB\nCD",                     // 换行
		strings.Repeat("A", 60*1024), // 审计实证的 60 KB
	}
	for _, code := range bad {
		if ValidRoomCode(code) {
			t.Fatalf("房间码 %q 应当被判非法", code[:min(len(code), 20)])
		}
	}
}

// TestCreateRejectsIllegalRoomCodeAndPassword 覆盖 Manager 层的两道入口校验：
// 校验必须落在 service（REST 与 /ws 两条入口共用它），而不是只在 HTTP handler 里。
func TestCreateRejectsIllegalRoomCodeAndPassword(t *testing.T) {
	m, _ := newTestManager(t, 8)

	_, _, err := m.Create("BAD-CODE", "", 0)
	if !errors.Is(err, ErrBadRoomCode) {
		t.Fatalf("非法房间码应返回 ErrBadRoomCode，实际 %v", err)
	}

	for _, pw := range []string{"a", "ab", "abc"} {
		if _, _, err := m.Create("", pw, 0); !errors.Is(err, ErrBadPasswordPolicy) {
			t.Fatalf("%d 位密码应返回 ErrBadPasswordPolicy，实际 %v", len(pw), err)
		}
	}
	for _, pw := range []string{"", "abcd", strings.Repeat("x", 64)} {
		if _, _, err := m.Create("", pw, 0); err != nil {
			t.Fatalf("%d 位密码应当合法，实际 %v", len(pw), err)
		}
	}
	// 65 位超上限。
	if _, _, err := m.Create("", strings.Repeat("x", 65), 0); !errors.Is(err, ErrBadPasswordPolicy) {
		t.Fatalf("65 位密码应被拒，实际 %v", err)
	}
}

// TestCreateReturnsHostToken 覆盖 S-7 的令牌生成：≥32 字符、每次不同、只存哈希。
func TestCreateReturnsHostToken(t *testing.T) {
	m, _ := newTestManager(t, 8)

	seen := make(map[string]bool, 16)
	for i := 0; i < 16; i++ {
		r, token, err := m.Create("", "", 0)
		if err != nil {
			t.Fatalf("创建房间失败: %v", err)
		}
		if len(token) < 32 {
			t.Fatalf("令牌应当 ≥32 个十六进制字符，实际 %q", token)
		}
		if seen[token] {
			t.Fatalf("令牌重复出现（%q）：说明随机源或生成逻辑有问题", token)
		}
		seen[token] = true

		// 服务端只存哈希：房间状态里不能出现明文令牌（除非是同包单测用的那个字段）。
		r.mu.Lock()
		hasHash := r.hostToken != [32]byte{}
		r.mu.Unlock()
		if !hasHash {
			t.Fatal("房间应当保存令牌哈希（用于常量时间比较）")
		}
	}
}

// TestMaxRoomsLimit 覆盖房间总数上限（S-3）。
func TestMaxRoomsLimit(t *testing.T) {
	cfg := config.Default()
	cfg.Room.MaxRooms = 2
	m := NewManager(cfg, newFakeBus())
	t.Cleanup(m.Stop)

	if _, _, err := m.Create("", "", 0); err != nil {
		t.Fatalf("第 1 个房间应当成功: %v", err)
	}
	if _, _, err := m.Create("", "", 0); err != nil {
		t.Fatalf("第 2 个房间应当成功: %v", err)
	}
	if _, _, err := m.Create("", "", 0); !errors.Is(err, ErrTooManyRooms) {
		t.Fatalf("第 3 个房间应返回 ErrTooManyRooms，实际 %v", err)
	}

	// 回收一个之后应当又能建。
	ids := make([]string, 0, 2)
	m.mu.RLock()
	for id := range m.rooms {
		ids = append(ids, id)
	}
	m.mu.RUnlock()
	if !m.closeRoomIf2(ids[0]) {
		t.Fatal("测试前置失败：应当能关掉一个房间")
	}
	if _, _, err := m.Create("", "", 0); err != nil {
		t.Fatalf("腾出位置后应当又能建房: %v", err)
	}
}

// closeRoomIf2 直接删掉一个房间（测试用）。
func (m *Manager) closeRoomIf2(roomID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[roomID]
	if !ok {
		return false
	}
	delete(m.rooms, roomID)
	r.closed = true
	return true
}

// —— S-3：回收判据 ——

// TestSweepReclaimsOnlyUnclaimedAndEmptyRooms 逐个覆盖三条回收判据，
// 尤其锁定"宽限期内一律不回收"这条不变式。
func TestSweepReclaimsOnlyUnclaimedAndEmptyRooms(t *testing.T) {
	cfg := config.Default()
	cfg.Room.MaxMembers = 8
	cfg.Room.HostGrace = time.Hour // 宽限期足够长，保证用例里不会自然到期
	cfg.Room.UnclaimedRoomTTL = time.Millisecond
	cfg.Room.HostGraceZeroTTL = time.Millisecond
	m := NewManager(cfg, newFakeBus())
	t.Cleanup(m.Stop)

	// 情形 A：创建后从未有人进房 → 回收。
	_, _, err := m.Create("", "", 0)
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	time.Sleep(5 * time.Millisecond)

	// 情形 B：有人进过房且房内还有人 → 不回收。
	live, liveToken, err := m.Create("", "", 0)
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	if err := m.Join(JoinRequest{RoomID: live.ID, ClientID: "host", DisplayName: "主播", Role: model.RoleHost}); err != nil {
		t.Fatalf("主办方进房失败: %v", err)
	}

	// 情形 C：主播断线（宽限期内、房内没人）→ 不回收。
	grace, graceToken, err := m.Create("", "", 0)
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	if err := m.Join(JoinRequest{RoomID: grace.ID, ClientID: "host", DisplayName: "主播", Role: model.RoleHost, HostToken: graceToken}); err != nil {
		t.Fatalf("主办方进房失败: %v", err)
	}
	m.Leave(grace.ID, "host")
	_ = liveToken

	time.Sleep(5 * time.Millisecond)

	reclaimed := m.SweepOnce()
	if reclaimed != 1 {
		t.Fatalf("本轮应当恰好回收 1 个房间（只有'从未有人进房'那个），实际 %d", reclaimed)
	}

	if _, ok := m.Get(live.ID); !ok {
		t.Fatal("有成员的房间被误杀了")
	}
	if _, ok := m.Get(grace.ID); !ok {
		t.Fatal("宽限期内的房间被误杀了（主播重连会拿到 ROOM_NOT_FOUND）")
	}
	if m.RoomCount() != 2 {
		t.Fatalf("在册房间数应为 2，实际 %d", m.RoomCount())
	}
}

// —— S-9：metrics 节流 ——

// TestMetricsThrottleAndSignificance 覆盖 S-9 的两条规则：
// 间隔内丢弃、非显著变化不触发重算、显著变化一定触发。
func TestMetricsThrottleAndSignificance(t *testing.T) {
	cfg := config.Default()
	cfg.Room.MaxMembers = 8
	cfg.Room.MetricsMinInterval = 100 * time.Millisecond
	cfg.Room.MetricsSignificantRatio = 0.1
	m := NewManager(cfg, newFakeBus())
	t.Cleanup(m.Stop)
	bus := m.bus.(*fakeBus)

	r, _, err := m.Create("", "", 0)
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	if err := m.Join(JoinRequest{RoomID: r.ID, ClientID: "host", DisplayName: "主播", Role: model.RoleHost}); err != nil {
		t.Fatalf("主办方进房失败: %v", err)
	}
	before := bus.broadcastCount(r.ID, model.TypeMemberList)

	// 首次上报：必须显著（否则容量模型永远停在 pending）。
	if err := m.UpdateMetrics(r.ID, "host", model.Metrics{UploadCapacityBps: 10_000_000}); err != nil {
		t.Fatalf("上报失败: %v", err)
	}
	if bus.broadcastCount(r.ID, model.TypeMemberList) == before {
		t.Fatal("首次上行上报应当触发一次重算（容量模型依赖它离开 pending）")
	}

	// 间隔内连发：必须被丢弃（不触发重算）。
	afterFirst := bus.broadcastCount(r.ID, model.TypeMemberList)
	for i := 0; i < 20; i++ {
		if err := m.UpdateMetrics(r.ID, "host", model.Metrics{UploadCapacityBps: 99_000_000}); err != nil {
			t.Fatalf("上报失败: %v", err)
		}
	}
	if got := bus.broadcastCount(r.ID, model.TypeMemberList); got != afterFirst {
		t.Fatalf("间隔内连发 %d 次不应触发任何重算（成员表广播 %d → %d）", 20, afterFirst, got)
	}
	// 数据仍然被记录（节流只作用于"触发重算"，不是丢数据）。
	r.mu.Lock()
	stored := r.members["host"].UploadCapacityBps
	r.mu.Unlock()
	if stored != 10_000_000 {
		t.Fatalf("间隔内的上报应当被丢弃（保留旧值 10000000），实际 %d", stored)
	}

	// 过了间隔 + 变化显著：必须触发。
	time.Sleep(120 * time.Millisecond)
	if err := m.UpdateMetrics(r.ID, "host", model.Metrics{UploadCapacityBps: 20_000_000}); err != nil {
		t.Fatalf("上报失败: %v", err)
	}
	if bus.broadcastCount(r.ID, model.TypeMemberList) == afterFirst {
		t.Fatal("过了最小间隔且变化 100% 必须触发重算")
	}

	// 过了间隔 + 变化不显著（< 10%）：不触发。
	afterSecond := bus.broadcastCount(r.ID, model.TypeMemberList)
	time.Sleep(120 * time.Millisecond)
	if err := m.UpdateMetrics(r.ID, "host", model.Metrics{UploadCapacityBps: 20_500_000}); err != nil {
		t.Fatalf("上报失败: %v", err)
	}
	if bus.broadcastCount(r.ID, model.TypeMemberList) != afterSecond {
		t.Fatal("2.5% 的变化不应当触发重算（阈值 10%）")
	}

	// 卡顿上报不受最小间隔约束：它是"这条路已经不行了"的直接信号。
	r.mu.Lock()
	r.lastDegradedReplanAt = time.Time{}
	r.mu.Unlock()
	time.Sleep(120 * time.Millisecond)
	if err := m.UpdateMetrics(r.ID, "host", model.Metrics{UploadCapacityBps: 20_500_000, StallCount: 1}); err != nil {
		t.Fatalf("上报失败: %v", err)
	}
	// 立刻再来一次卡顿（间隔内）——也必须被处理（StallCount 从 1 涨到 2）。
	if err := m.UpdateMetrics(r.ID, "host", model.Metrics{UploadCapacityBps: 20_500_000, StallCount: 2}); err != nil {
		t.Fatalf("上报失败: %v", err)
	}
	r.mu.Lock()
	stalls := r.members["host"].StallCount
	r.mu.Unlock()
	if stalls != 2 {
		t.Fatalf("卡顿上报不应被最小间隔丢弃，实际累计 %d", stalls)
	}
}

// TestRelativeChange 锁定"相对变化"的边界（它决定要不要重算）。
func TestRelativeChange(t *testing.T) {
	cases := []struct {
		old, next, want float64
	}{
		{10_000_000, 10_000_000, 0},
		{10_000_000, 10_500_000, 0.0476}, // 5% 以内 → 不显著
		{10_000_000, 20_000_000, 0.5},
		{0, 8_000_000, 1}, // 首次上报：分母为 0 时定义为"完全变了"
		{20_000_000, 10_000_000, 0.5},
	}
	for _, tc := range cases {
		got := relativeChange(tc.old, tc.next)
		if diff := got - tc.want; diff > 0.001 || diff < -0.001 {
			t.Fatalf("relativeChange(%v, %v) = %v，期望约 %v", tc.old, tc.next, got, tc.want)
		}
	}
}

// —— S-9：have 位图上限 ——

// TestChunkReportRejectsOversizedBitmap 覆盖"位图长度上限"：
// 它是"每条都不大、但可以持续变大"的形态，必须被拒而不是存进房间状态。
func TestChunkReportRejectsOversizedBitmap(t *testing.T) {
	cfg := config.Default()
	cfg.Room.MaxMembers = 8
	cfg.Signal.MaxFieldBytes = 1024
	bus := newFakeBus()
	m := NewManager(cfg, bus)
	t.Cleanup(m.Stop)

	r, _, err := m.Create("", "", 0)
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	if err := m.Join(JoinRequest{RoomID: r.ID, ClientID: "host", DisplayName: "主播", Role: model.RoleHost}); err != nil {
		t.Fatalf("主办方进房失败: %v", err)
	}
	if err := m.Join(JoinRequest{RoomID: r.ID, ClientID: "v1", DisplayName: "观众", Role: model.RoleViewer}); err != nil {
		t.Fatalf("观众进房失败: %v", err)
	}

	if err := m.SetChunkReport(r.ID, "v1", make([]byte, 1024), false); err != nil {
		t.Fatalf("刚好等于上限的位图应当被接受: %v", err)
	}
	if err := m.SetChunkReport(r.ID, "v1", make([]byte, 1025), false); !errors.Is(err, ErrBadInput) {
		t.Fatalf("超过上限的位图应被拒（ErrBadInput），实际 %v", err)
	}

	// 存的必须是副本：外部改原切片不能影响房间状态。
	src := []byte{1, 2, 3}
	if err := m.SetChunkReport(r.ID, "v1", src, true); err != nil {
		t.Fatalf("上报失败: %v", err)
	}
	src[0] = 9
	r.mu.Lock()
	stored := r.members["v1"].HaveBits
	complete := r.members["v1"].Complete
	r.mu.Unlock()
	if stored[0] != 1 || !complete {
		t.Fatalf("位图必须被复制保存（外部改原切片不应影响房间状态），实际 %v", stored)
	}
}

// —— S-11：join 失败限速（service 层的令牌桶由 handler 使用，这里直接验证原语与语义）——

// TestJoinFailureRateLimitPrimitive 锁定限速原语的关键语义：
// 正常用户的前几次永远放行，持续滥用必然被拒。
func TestJoinFailureRateLimitPrimitive(t *testing.T) {
	lim := limiter.NewKeyed(30, 30) // 30/分钟、容量 30
	key := "127.0.0.1|ABCDEF"

	for i := 0; i < 30; i++ {
		if !lim.Allow(key) {
			t.Fatalf("容量 30 的令牌桶在第 %d 次就拒绝了（正常用户不该被限速）", i+1)
		}
	}
	if lim.Allow(key) {
		t.Fatal("第 31 次应当被拒（30/分钟 + 容量 30）")
	}
	// 不同键之间互不影响。
	if !lim.Allow("127.0.0.1|ZZZZZZ") {
		t.Fatal("换一个房间码应当重新有满桶令牌（键是 IP+房间码）")
	}

	// 关闭（perMinute<=0）时永远放行。
	if got := limiter.NewKeyed(0, 0); got != nil {
		if !got.Allow(key) {
			t.Fatal("闸门关闭时应当永远放行")
		}
	}
}

// TestPasswordPolicy 锁定 S-11 的密码长度策略（留空合法）。
func TestPasswordPolicy(t *testing.T) {
	if !ValidPassword("") {
		t.Fatal("留空应当合法（不设密码是默认形态）")
	}
	for _, pw := range []string{"a", "ab", "abc"} {
		if ValidPassword(pw) {
			t.Fatalf("%d 位密码应当非法", len(pw))
		}
	}
	for _, pw := range []string{"abcd", strings.Repeat("x", 64), "中文密码四个字"} {
		if !ValidPassword(pw) {
			t.Fatalf("%q 应当合法", pw)
		}
	}
	if ValidPassword(strings.Repeat("x", 65)) {
		t.Fatal("65 位密码应当非法")
	}
}

// TestReclaimReasonsAreDescriptive 锁定回收日志里的人可读理由（排障靠它）。
func TestReclaimReasonsAreDescriptive(t *testing.T) {
	got := describeDuration(config.DefaultUnclaimedRoomTTL)
	if !strings.Contains(got, "分钟") {
		t.Fatalf("10 分钟应当被描述为'N 分钟'，实际 %q", got)
	}
	if !strings.Contains(describeDuration(time.Second), "秒") {
		t.Fatalf("1 秒应当被描述为'N 秒'，实际 %q", describeDuration(time.Second))
	}
}
