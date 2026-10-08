package usecase

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/model"
)

// 本文件锁定「主播断线 → 宽限期 → 重连恢复 / 到期销毁」这条链路。
//
// 它对应的真实故障：任何一次 WS 断开（网络抖动 / 页面刷新 / 服务端重启 / 半开连接）
// 都会走 Leave；旧实现把"主播断线"当成"主播离开"，立刻销毁房间，
// 主播自动重连只能拿到 ROOM_NOT_FOUND，房间码彻底作废、观众全掉。

// newGraceManager 构造一个宽限期可控的 Manager：
// "到期销毁"的用例必须能把宽限期压到毫秒级，而不是真的等 60 秒。
func newGraceManager(t *testing.T, maxMembers int, grace time.Duration) (*Manager, *fakeBus) {
	t.Helper()

	cfg := config.Default()
	cfg.Room.MaxMembers = maxMembers
	cfg.Room.HostGrace = grace
	bus := newFakeBus()

	return NewManager(cfg, bus), bus
}

// waitFor 轮询等待条件成立。
// 宽限期到期是定时器触发的异步过程，裸 sleep 要么慢要么不稳。
func waitFor(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时（%v）: %s", timeout, desc)
}

// hostOfflineState 读取房间的宽限期状态（测试用）。
func hostOfflineState(t *testing.T, r *Room) (offline bool, timerRunning bool, hostID string) {
	t.Helper()

	r.mu.Lock()
	defer r.mu.Unlock()

	return r.hostOffline, r.hostGraceTimer != nil, r.HostID
}

// setupPlayingRoom 搭一个"正在播"的房间：主播 + 观众，已发布索引、已下发一次控制（seq=1）。
//
// 第二个返回值是创建响应里的**主播复位令牌**（S-7）：宽限期内重连主播位必须带上它。
func setupPlayingRoom(t *testing.T, m *Manager) (*Room, string) {
	t.Helper()

	r, hostToken, err := m.Create("", "", 0)
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	if err := m.Join(JoinRequest{RoomID: r.ID, ClientID: "host", DisplayName: "主播", Role: model.RoleHost, Password: ""}); err != nil {
		t.Fatalf("主播进房失败: %v", err)
	}
	if err := m.Join(JoinRequest{RoomID: r.ID, ClientID: "viewer", DisplayName: "观众", Role: model.RoleViewer, Password: ""}); err != nil {
		t.Fatalf("观众进房失败: %v", err)
	}

	index := sampleMediaIndex(2_000_000)
	if err := m.SetMediaIndex(r.ID, "host", &index); err != nil {
		t.Fatalf("发布分片索引失败: %v", err)
	}
	if err := m.HandleControl(r.ID, "host", model.Envelope{Action: model.ActionPlay, CurrentTime: 12.5}); err != nil {
		t.Fatalf("主播下发播放控制失败: %v", err)
	}

	return r, hostToken
}

// 契约 a：主播 Leave 后房间仍然存在、HostID 为空、MediaIndex/lastPlayback 保留，
// 且**没有**广播 room-closed、没有断开任何连接。
func TestHostLeaveEntersGraceAndKeepsState(t *testing.T) {
	m, bus := newGraceManager(t, 8, time.Minute)
	r, _ := setupPlayingRoom(t, m)

	m.Leave(r.ID, "host")

	if _, ok := m.Get(r.ID); !ok {
		t.Fatal("主播断线后房间必须进入宽限期，而不是立刻销毁")
	}
	if offline, timerRunning, hostID := hostOfflineState(t, r); !offline || !timerRunning || hostID != "" {
		t.Fatalf("宽限期状态不正确: offline=%t timer=%t hostID=%q", offline, timerRunning, hostID)
	}

	hostID, members, playback, _, mediaIndex := r.Snapshot(8)
	if hostID != "" {
		t.Fatalf("主播离线期间 hostId 必须为空，实际 %q", hostID)
	}
	if len(members) != 1 || members[0].ID != "viewer" {
		t.Fatalf("宽限期内观众必须留在房里: %+v", members)
	}
	if mediaIndex == nil || len(mediaIndex.Segments) != 2 {
		t.Fatalf("宽限期内分片索引必须保留: %+v", mediaIndex)
	}
	if playback.Seq != 1 || playback.CurrentTime != 12.5 || playback.Paused {
		t.Fatalf("宽限期内播放状态必须保留: %+v", playback)
	}

	if got := bus.broadcastCount(r.ID, model.TypeRoomClosed); got != 0 {
		t.Fatalf("宽限期内不得广播 room-closed，实际 %d 条", got)
	}
	if got := bus.broadcastCount(r.ID, model.TypeMemberLeft); got != 1 {
		t.Fatalf("应广播恰好 1 条 member-left，实际 %d 条", got)
	}
	if len(bus.closed) != 0 {
		t.Fatalf("宽限期内不得关闭房间连接: %#v", bus.closed)
	}
	if got := bus.excepts[r.ID][len(bus.excepts[r.ID])-1]; got != "host" {
		t.Fatalf("member-left 应排除离开者本人，实际 except=%q", got)
	}
}

// 契约 b：宽限期内主播重新 Join(role=host) 成功恢复，
// 且 Playback.Seq 不回退、MediaIndex 仍在、宽限定时器被取消。
func TestHostRejoinWithinGraceRestoresRoom(t *testing.T) {
	m, bus := newGraceManager(t, 8, 200*time.Millisecond)
	r, _ := setupPlayingRoom(t, m)

	// S-7：宽限期内接回主播位必须带上创建时下发的复位令牌。
	hostToken := r.hostTokenPlain
	m.Leave(r.ID, "host")
	if err := m.Join(JoinRequest{RoomID: r.ID, ClientID: "host", DisplayName: "主播", Role: model.RoleHost, Password: "", HostToken: hostToken}); err != nil {
		t.Fatalf("宽限期内主播重连（带正确令牌）必须成功，实际 %v", err)
	}

	joined := bus.lastDirectOfType(t, "host", model.TypeJoined)
	if joined.HostID != "host" || joined.SelfID != "host" {
		t.Fatalf("恢复后的入房快照不正确: %+v", joined)
	}
	if joined.Playback == nil || joined.Playback.Seq != 1 || joined.Playback.CurrentTime != 12.5 || joined.Playback.Paused {
		t.Fatalf("恢复后必须延续播放状态（seq 不得回退）: %+v", joined.Playback)
	}
	if joined.MediaIndex == nil || len(joined.MediaIndex.Segments) != 2 {
		t.Fatal("恢复后入房快照必须仍带分片索引")
	}

	if offline, timerRunning, hostID := hostOfflineState(t, r); offline || timerRunning || hostID != "host" {
		t.Fatalf("恢复后必须清掉离线标记并取消宽限定时器: offline=%t timer=%t hostID=%q", offline, timerRunning, hostID)
	}
	if got := bus.broadcastCount(r.ID, model.TypeRoomClosed); got != 0 {
		t.Fatalf("恢复过程不得广播 room-closed，实际 %d 条", got)
	}

	// 观众收到 member-joined（成员表里重新出现主播）。
	memberJoined := bus.lastBroadcastOfType(t, r.ID, model.TypeMemberJoined)
	if memberJoined.Member == nil || memberJoined.Member.ID != "host" {
		t.Fatalf("观众应收到主播的 member-joined: %+v", memberJoined)
	}

	// 恢复后 seq 继续单调递增：下一条控制是 2，而不是从 1 重新开始。
	if err := m.HandleControl(r.ID, "host", model.Envelope{Action: model.ActionPause, CurrentTime: 30}); err != nil {
		t.Fatalf("恢复后主播控制失败: %v", err)
	}
	if got := bus.lastBroadcastOfType(t, r.ID, model.TypeRoomControl).Playback.Seq; got != 2 {
		t.Fatalf("恢复后 seq 必须接着原来的值递增（期望 2），实际 %d", got)
	}

	// 关键回归：宽限期定时器必须真的被取消 —— 等到原定到期时间之后房间仍在。
	time.Sleep(300 * time.Millisecond)
	if _, ok := m.Get(r.ID); !ok {
		t.Fatal("主播已重连，宽限定时器不得再把房间关掉")
	}
	if got := bus.broadcastCount(r.ID, model.TypeRoomClosed); got != 0 {
		t.Fatalf("主播已重连，不得因宽限期到期广播 room-closed，实际 %d 条", got)
	}
}

// 契约 c：宽限期到期（测试里压到几十毫秒）仍无主播才销毁房间并广播 room-closed。
func TestHostGraceExpiryClosesRoom(t *testing.T) {
	m, bus := newGraceManager(t, 8, 80*time.Millisecond)
	r, _ := setupPlayingRoom(t, m)

	m.Leave(r.ID, "host")
	if _, ok := m.Get(r.ID); !ok {
		t.Fatal("刚进入宽限期时房间必须还在")
	}

	// 注意要同时等"房间被摘除"和"room-closed 已广播"：
	// closeRoomIf 是先摘房间、后广播，只等前者会读到还没发出的广播。
	waitFor(t, 3*time.Second, "宽限期到期后房间被销毁并广播 room-closed", func() bool {
		_, ok := m.Get(r.ID)
		return !ok && bus.broadcastCount(r.ID, model.TypeRoomClosed) == 1
	})

	closed := bus.lastBroadcastOfType(t, r.ID, model.TypeRoomClosed)
	if closed.Code != model.CodeRoomClosed {
		t.Fatalf("到期销毁必须带上 ROOM_CLOSED 错误码: %+v", closed)
	}
	if closed.Message == "" || !strings.Contains(closed.Message, "主播离线超过") {
		t.Fatalf("room-closed 的 message 必须说明原因: %q", closed.Message)
	}
	if len(bus.closed) != 1 || bus.closed[0] != r.ID {
		t.Fatalf("到期销毁必须关闭房间连接: %#v", bus.closed)
	}

	// 关闭后用同一个房间码也能重新创建：房间码没有被永久占住。
	if _, _, err := m.Create(r.ID, "", 0); err != nil {
		t.Fatalf("房间销毁后房间码应可复用: %v", err)
	}
}

// 契约 c 的边界：HostGrace <= 0 时退回"立即销毁"的旧语义（配置写错也不能把房间永久留着）。
func TestHostGraceDisabledClosesImmediately(t *testing.T) {
	m, bus := newGraceManager(t, 8, 0)
	r, _ := setupPlayingRoom(t, m)

	m.Leave(r.ID, "host")

	if _, ok := m.Get(r.ID); ok {
		t.Fatal("宽限期被关闭时，主播断线应立即销毁房间")
	}
	if got := bus.broadcastCount(r.ID, model.TypeRoomClosed); got != 1 {
		t.Fatalf("应立即广播 1 条 room-closed，实际 %d 条", got)
	}
}

// 契约 d：宽限期内房内成员清空时，CloseRoomIfEmpty 与 Leave 的空房分支都不得删除房间。
func TestCloseRoomIfEmptyKeepsRoomDuringHostGrace(t *testing.T) {
	m, _ := newGraceManager(t, 8, time.Minute)
	r, _ := setupPlayingRoom(t, m)

	m.Leave(r.ID, "host")   // 主播断线 → 宽限期开始
	m.Leave(r.ID, "viewer") // 最后一个人也走了，房间只剩元数据

	if got := r.MemberInfos(); len(got) != 0 {
		t.Fatalf("前置条件不成立：房内应已无人，实际 %+v", got)
	}
	if _, ok := m.Get(r.ID); !ok {
		t.Fatal("宽限期内房间清空也不得删除（那等于把房间码作废）")
	}

	m.CloseRoomIfEmpty(r.ID)
	if _, ok := m.Get(r.ID); !ok {
		t.Fatal("宽限期内 CloseRoomIfEmpty 不得删除房间")
	}

	// 反向证明这道闸门是有效的：不在宽限期的空房间照旧会被清理。
	other, _, err := m.Create("", "", 0)
	if err != nil {
		t.Fatalf("创建对照房间失败: %v", err)
	}
	m.CloseRoomIfEmpty(other.ID)
	if _, ok := m.Get(other.ID); ok {
		t.Fatal("非宽限期的空房间应被 CloseRoomIfEmpty 清理")
	}
}

// 宽限期内观众的进出照旧工作：离开广播 member-left 且不清空房间；
// 新观众也能进房（房间与分片索引还在，见 TestViewerCanJoinDuringHostGrace）。
func TestViewerLeaveDuringGraceKeepsRoom(t *testing.T) {
	m, bus := newGraceManager(t, 8, time.Minute)
	r, _ := setupPlayingRoom(t, m)

	m.Leave(r.ID, "host")
	m.Leave(r.ID, "viewer")

	if left := bus.lastBroadcastOfType(t, r.ID, model.TypeMemberLeft); left.ClientID != "viewer" {
		t.Fatalf("观众离开应照旧广播 member-left: %+v", left)
	}
	if _, ok := m.Get(r.ID); !ok {
		t.Fatal("宽限期内观众离开不得销毁房间")
	}
	if err := m.Join(JoinRequest{RoomID: r.ID, ClientID: "late", DisplayName: "迟到观众", Role: model.RoleViewer, Password: ""}); err != nil {
		t.Fatalf("宽限期内观众应能回房（否则同一次抖动里掉线的观众最长 60s 回不来），实际 %v", err)
	}
}

// 契约：主播离线宽限期内观众可以进房 —— 房间、分片索引、断线前的播放状态都还在，
// 新观众据此对齐后由界面显示"等待主播重连"（服务端不做特殊处理）。
func TestViewerCanJoinDuringHostGrace(t *testing.T) {
	m, bus := newGraceManager(t, 8, time.Minute)
	r, _ := setupPlayingRoom(t, m)

	m.Leave(r.ID, "host")

	if err := m.Join(JoinRequest{RoomID: r.ID, ClientID: "late", DisplayName: "迟到观众", Role: model.RoleViewer, Password: ""}); err != nil {
		t.Fatalf("宽限期内观众进房应成功，实际 %v", err)
	}

	joined := bus.lastDirectOfType(t, "late", model.TypeJoined)
	if joined.SelfID != "late" {
		t.Fatalf("入房快照的 selfId 不正确: %+v", joined)
	}
	// 当前无主播：客户端从 hostId 为空 + members 里没有 host 推导出"等待主播重连"。
	if joined.HostID != "" {
		t.Fatalf("主播离线期间入房快照的 hostId 必须为空，实际 %q", joined.HostID)
	}
	for _, mem := range joined.Members {
		if mem.Role == model.RoleHost {
			t.Fatalf("主播离线期间成员表里不应有 host: %+v", joined.Members)
		}
	}
	if len(joined.Members) != 2 {
		t.Fatalf("成员表应含既有观众与本人: %+v", joined.Members)
	}
	// 断线前的分片索引与播放状态必须照发，否则新观众无从对齐。
	if joined.MediaIndex == nil || len(joined.MediaIndex.Segments) != 2 {
		t.Fatalf("入房快照必须携带保留的分片索引: %+v", joined.MediaIndex)
	}
	if joined.Playback == nil || joined.Playback.Seq != 1 || joined.Playback.CurrentTime != 12.5 || joined.Playback.Paused {
		t.Fatalf("入房快照必须携带断线前的播放状态: %+v", joined.Playback)
	}

	// member-joined 照常广播给房内其他人（不含本人）。
	memberJoined := bus.lastBroadcastOfType(t, r.ID, model.TypeMemberJoined)
	if memberJoined.Member == nil || memberJoined.Member.ID != "late" {
		t.Fatalf("应广播 member-joined: %+v", memberJoined)
	}
	if except := bus.lastExceptFor(t, r.ID, model.TypeMemberJoined); except != "late" {
		t.Fatalf("member-joined 应排除加入者本人，实际 except=%q", except)
	}

	// 契约要求：宽限期内不重算拓扑 —— 没有根节点，新观众拿不到（也不需要）分配。
	if got := bus.directCount("late", model.TypeParentAssignment); got != 0 {
		t.Fatalf("宽限期内不应下发拓扑分配，实际 %d 条", got)
	}
}

// 反向断言：房间刚 Create、主播**从未**进房时，观众仍然拿到 ROOM_NOT_READY。
// 宽限期只对"有过主播的房间"成立，不能退化成"谁都能先进来等主播"。
func TestViewerRejectedBeforeHostEverJoins(t *testing.T) {
	m, _ := newGraceManager(t, 8, time.Minute)

	r, _, err := m.Create("", "", 0)
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	if err := m.Join(JoinRequest{RoomID: r.ID, ClientID: "early", DisplayName: "抢跑观众", Role: model.RoleViewer, Password: ""}); !errors.Is(err, ErrNotReady) {
		t.Fatalf("主播从未进房的房间，观众进房应返回 ErrNotReady，实际 %v", err)
	}
	if got := r.MemberInfos(); len(got) != 0 {
		t.Fatalf("被拒的观众不应留在成员表里: %+v", got)
	}
}

// 宽限期内观众的度量上报不得触发拓扑重算：没有主播就没有根节点，
// 重算只会把刻意保留的分配与容量口径抹成 pending（观众会看到容量突然变未知）。
func TestMetricsDuringGraceDoesNotWipePlan(t *testing.T) {
	m, bus := newGraceManager(t, 16, time.Minute)
	r, _ := setupPlayingRoom(t, m)

	// 先让房间有一套真实的分配与容量口径（主播 16 Mbps / 2 Mbps 码率 → fanout）。
	if err := m.UpdateMetrics(r.ID, "host", model.Metrics{UploadCapacityBps: 16_000_000, RTTMs: 10}); err != nil {
		t.Fatalf("主播上报度量失败: %v", err)
	}
	capacityBefore := bus.broadcastCount(r.ID, model.TypeCapacity)

	m.Leave(r.ID, "host")

	// 宽限期内观众继续按 5s 周期上报度量。
	if err := m.UpdateMetrics(r.ID, "viewer", model.Metrics{UploadCapacityBps: 5_000_000, RTTMs: 20}); err != nil {
		t.Fatalf("宽限期内观众上报度量失败: %v", err)
	}

	if got := bus.broadcastCount(r.ID, model.TypeCapacity); got != capacityBefore {
		t.Fatalf("宽限期内不得重算/广播容量：之前 %d 条，之后 %d 条", capacityBefore, got)
	}

	r.mu.Lock()
	mode := r.plan.Mode
	assigned := len(r.plan.Assignments)
	r.mu.Unlock()
	if mode == ModePending || assigned == 0 {
		t.Fatalf("宽限期内必须保留原分配，实际 mode=%q 已安置=%d", mode, assigned)
	}
}

// TestHostGraceExpiryRacesRejoinSafely 压力覆盖最危险的那处竞态：
// "宽限期到期"跑在定时器协程里，"主播重连"跑在 handler 协程里。
// 本用例不预设谁赢，只要求终态自洽 —— 要么房间还在且主播在房（重连赢了），
// 要么房间已销毁且恰好广播过一次 room-closed（到期赢了）；
// 绝不允许"重连成功却被到期定时器关掉"这种撕裂状态。
func TestHostGraceExpiryRacesRejoinSafely(t *testing.T) {
	const iterations = 200

	for i := 0; i < iterations; i++ {
		grace := time.Millisecond
		m, bus := newGraceManager(t, 8, grace)

		r, hostToken, err := m.Create("", "", 0)
		if err != nil {
			t.Fatalf("第 %d 次创建房间失败: %v", i, err)
		}
		if err := m.Join(JoinRequest{RoomID: r.ID, ClientID: "host", DisplayName: "主播", Role: model.RoleHost, Password: ""}); err != nil {
			t.Fatalf("第 %d 次主播进房失败: %v", i, err)
		}

		// 主播断线 → 宽限期开始（定时器协程即将醒来）。
		m.Leave(r.ID, "host")

		if i%2 == 1 {
			// 让定时器先赢：等房间真的被销毁再重连。
			waitFor(t, 3*time.Second, "宽限期到期销毁房间", func() bool {
				_, ok := m.Get(r.ID)
				return !ok
			})
		}

		joinErr := m.Join(JoinRequest{RoomID: r.ID, ClientID: "host", DisplayName: "主播", Role: model.RoleHost, Password: "", HostToken: hostToken})
		if joinErr == nil {
			// 重连赢了：等够"定时器本该触发"的时间，房间必须还在（取消必须真的生效）。
			time.Sleep(4 * grace)
		} else {
			// 到期赢了：closeRoomIf 先摘房间、后广播，要等广播真的发出来。
			waitFor(t, 3*time.Second, "room-closed 广播", func() bool {
				return bus.broadcastCount(r.ID, model.TypeRoomClosed) == 1
			})
		}

		room, alive := m.Get(r.ID)
		closed := bus.broadcastCount(r.ID, model.TypeRoomClosed)

		switch {
		case joinErr == nil:
			if !alive {
				t.Fatalf("第 %d 次：重连成功却被宽限定时器关掉了房间", i)
			}
			if room.HostID != "host" {
				t.Fatalf("第 %d 次：重连成功后 HostID 应为 host，实际 %q", i, room.HostID)
			}
			if closed != 0 {
				t.Fatalf("第 %d 次：重连成功后不得广播 room-closed，实际 %d 条", i, closed)
			}
		case errors.Is(joinErr, ErrNotFound):
			if alive {
				t.Fatalf("第 %d 次：Join 报房间不存在，但房间还在", i)
			}
			if closed != 1 {
				t.Fatalf("第 %d 次：房间销毁应恰好广播 1 条 room-closed，实际 %d 条", i, closed)
			}
		default:
			t.Fatalf("第 %d 次：重连只可能成功或 ROOM_NOT_FOUND，实际 %v", i, joinErr)
		}
	}
}

// roomPresence 原子地读取"房间是否还在 m.rooms 里"与"是否已标记 closed"。
// 必须同一个临界区里读：分开读会把"Get 时还在 map、随后被摘除"的两瞬间拼成一个假状态。
// 锁序与生产代码一致（m.mu → r.mu）。
func roomPresence(m *Manager, roomID string) (inMap bool, closed bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	r, ok := m.rooms[roomID]
	if !ok {
		return false, false
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	return true, r.closed
}

// roomClosedFlag 读取房间的 closed 标记（测试用）。
func roomClosedFlag(t *testing.T, r *Room) bool {
	t.Helper()

	r.mu.Lock()
	defer r.mu.Unlock()

	return r.closed
}

// TestRoomRemovalPathsMarkClosed 锁定不变式：
// **任何把房间摘出 m.rooms 的路径都必须同时置 r.closed**。
// 否则一个已拿到 *Room 指针、还没拿到 r.mu 的并发 Join 会把自己加进一个
// 查不到的孤儿房间（后续 Get / 广播全部失效）。
func TestRoomRemovalPathsMarkClosed(t *testing.T) {
	t.Run("CloseRoomIfEmpty", func(t *testing.T) {
		m, _ := newGraceManager(t, 8, time.Minute)
		r, _, err := m.Create("", "", 0)
		if err != nil {
			t.Fatalf("创建房间失败: %v", err)
		}

		m.CloseRoomIfEmpty(r.ID)

		if _, ok := m.Get(r.ID); ok {
			t.Fatal("非宽限期的空房间应被清理")
		}
		if !roomClosedFlag(t, r) {
			t.Fatal("CloseRoomIfEmpty 摘除房间时必须置 r.closed")
		}
	})

	t.Run("LeaveLastMember", func(t *testing.T) {
		m, _ := newGraceManager(t, 8, time.Minute)
		r, _, err := m.Create("", "", 0)
		if err != nil {
			t.Fatalf("创建房间失败: %v", err)
		}

		// 白盒构造"没有主播、不在宽限期、房内只剩一个观众"的状态。
		// 这条分支在正常时序里几乎不可达（有过主播的房间，主播离开必然进入宽限期），
		// 但它确实是一条"把房间摘出 m.rooms"的路径，必须遵守同一条不变式。
		r.mu.Lock()
		r.members["v1"] = &Member{
			ID: "v1", DisplayName: "观众", Role: model.RoleViewer, Depth: 1, JoinedAt: time.Now(),
		}
		r.order = append(r.order, "v1")
		r.mu.Unlock()

		m.Leave(r.ID, "v1")

		if _, ok := m.Get(r.ID); ok {
			t.Fatal("最后一名成员在非宽限期离开后，空房间应被清理")
		}
		if !roomClosedFlag(t, r) {
			t.Fatal("Leave 摘除空房间时必须置 r.closed")
		}
	})
}

// TestJoinNeverLandsInOrphanRoom 压力覆盖"Join 与摘除房间"的竞态：
// 不允许出现"加入成功但房间已不在 m.rooms 里"（孤儿房间收不到任何广播、Get 也查不到），
// 也不允许出现"房间还在 m.rooms 里却已被标记 closed"。
func TestJoinNeverLandsInOrphanRoom(t *testing.T) {
	const iterations = 300

	for i := 0; i < iterations; i++ {
		m, _ := newGraceManager(t, 8, time.Minute)
		r, _, err := m.Create("", "", 0)
		if err != nil {
			t.Fatalf("第 %d 次创建房间失败: %v", i, err)
		}
		if err := m.Join(JoinRequest{RoomID: r.ID, ClientID: "host", DisplayName: "主播", Role: model.RoleHost, Password: ""}); err != nil {
			t.Fatalf("第 %d 次主播进房失败: %v", i, err)
		}
		if err := m.Join(JoinRequest{RoomID: r.ID, ClientID: "v1", DisplayName: "观众", Role: model.RoleViewer, Password: ""}); err != nil {
			t.Fatalf("第 %d 次观众进房失败: %v", i, err)
		}
		// 宽限期开始：Join 可以成功，而 CloseRoomIfEmpty 绝不能删掉这个房间。
		m.Leave(r.ID, "host")

		var wg sync.WaitGroup
		var joinErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			joinErr = m.Join(JoinRequest{RoomID: r.ID, ClientID: "late", DisplayName: "迟到观众", Role: model.RoleViewer, Password: ""})
		}()
		go func() {
			defer wg.Done()
			m.CloseRoomIfEmpty(r.ID)
		}()
		wg.Wait()

		inMap, closed := roomPresence(m, r.ID)

		if joinErr == nil {
			// 加入成功：房间必须仍在 m.rooms 里，成员在里面，且没有被标记 closed。
			if !inMap {
				t.Fatalf("第 %d 次：Join 成功但房间已不在 m.rooms（孤儿房间）", i)
			}
			if closed {
				t.Fatalf("第 %d 次：Join 成功但房间已被标记 closed", i)
			}
			room, ok := m.Get(r.ID)
			if !ok || !room.IsMember("late") {
				t.Fatalf("第 %d 次：Join 成功但成员不在房里", i)
			}
		} else if !errors.Is(joinErr, ErrNotFound) && !errors.Is(joinErr, ErrNotReady) && !errors.Is(joinErr, ErrFull) {
			t.Fatalf("第 %d 次：意料之外的错误 %v", i, joinErr)
		}

		// 通用不变式：不允许"房间还在 map 里 + 已被标记 closed"这种撕裂态。
		if inMap && closed {
			t.Fatalf("第 %d 次：房间还在 m.rooms 里却被标记 closed", i)
		}
	}
}
