package usecase

import (
	"errors"
	"sync"
	"testing"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/model"
)

// 本文件是二期 T1 的验收面：房间快照、房主元数据校验、管理端强关。
//
// 三条判据，逐条对应一个"不这么做就会出事"的场景：
//
//	ListRooms       只读快照必须**不受并发改动影响**（公开列表边遍历边有人进出房，
//	                拿到半个成员表或读到释放后的 map 都是崩溃/脏读）
//	SetRoomMeta     不存在 → ErrNotFound；非房主 → ErrNotRoomOwner
//	CloseRoomByAdmin 走既有的 closeRoom 路径（广播 room-closed + bus.CloseRoom）

// newSnapshotManager 起一个带假 bus 的 Manager（不起后台清扫协程）。
func newSnapshotManager(t *testing.T) (*Manager, *fakeBus) {
	t.Helper()
	bus := newFakeBus()
	m := NewManager(config.Default(), bus)
	// 这里刻意**不**调用 Start()：本文件不依赖回收逻辑，起协程只会让用例有额外的生命周期。
	return m, bus
}

// TestListRoomsSnapshotIsIndependentOfConcurrentChanges 覆盖 T1 的核心判据：
// 快照是**拷贝**，后续的并发改动（进房 / 离房 / 关房）不会改到已返回的切片。
func TestListRoomsSnapshotIsIndependentOfConcurrentChanges(t *testing.T) {
	m, _ := newSnapshotManager(t)

	r, _, err := m.CreateOwned("SNAP0001", "pw12", 0, 42)
	if err != nil {
		t.Fatalf("建房失败: %v", err)
	}

	first := m.ListRooms()
	if len(first) != 1 {
		t.Fatalf("应当有 1 个房间，实际 %d", len(first))
	}
	snap := first[0]
	if snap.ID != r.ID || snap.OwnerUserID != 42 || !snap.HasPassword {
		t.Fatalf("快照的静态字段不对：%+v", snap)
	}
	if snap.MemberCount != 0 || snap.HostOnline || snap.HostInGrace {
		t.Fatalf("新房间不该有成员/主播：%+v", snap)
	}
	if snap.MaxDepth != config.Default().Room.MaxDepth {
		t.Fatalf("MaxDepth 应当来自配置，实际 %d", snap.MaxDepth)
	}

	// 并发改动：主播进房（成员数变化）。
	if err := m.Join(JoinRequest{
		RoomID: r.ID, ClientID: "c1", DisplayName: "主播", Role: model.RoleHost, Password: "pw12",
	}); err != nil {
		t.Fatalf("进房失败: %v", err)
	}
	// 已经拿到的那份快照必须原样（快照 = 那一刻的事实）。
	if first[0].MemberCount != 0 || first[0].HostOnline {
		t.Fatalf("已返回的快照不得被后续改动改写：%+v", first[0])
	}

	// 再取一次：这次应当反映新的事实。
	second := m.ListRooms()
	if len(second) != 1 || second[0].MemberCount != 1 || !second[0].HostOnline {
		t.Fatalf("新快照应当反映进房后的事实：%+v", second)
	}

	// 主播离开 → 进入宽限期，快照要标出来。
	m.Leave(r.ID, "c1")
	third := m.ListRooms()
	if len(third) != 1 {
		t.Fatalf("宽限期内房间仍在册，实际 %d 个", len(third))
	}
	if third[0].HostOnline || !third[0].HostInGrace {
		t.Fatalf("宽限期内应当是 HostOnline=false / HostInGrace=true：%+v", third[0])
	}

	// 关房之后不再出现在快照里。
	if err := m.CloseRoomByAdmin(r.ID, "管理员关闭"); err != nil {
		t.Fatalf("强关失败: %v", err)
	}
	if got := m.ListRooms(); len(got) != 0 {
		t.Fatalf("关房之后不该再出现在列表里：%+v", got)
	}
}

// TestListRoomsIsRaceFreeUnderConcurrentMutation 在 -race 下断言"边读边改"不产生数据竞争。
//
// 它对应的是生产里最真实的一种并发：公开房列表在被拉取的同时，
// 房间里有人进出、主播上下线、管理员强关。
func TestListRoomsIsRaceFreeUnderConcurrentMutation(t *testing.T) {
	m, _ := newSnapshotManager(t)

	r, _, err := m.CreateOwned("RACE0001", "", 0, 7)
	if err != nil {
		t.Fatalf("建房失败: %v", err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// 读侧：持续取快照。
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = m.ListRooms()
				}
			}
		}()
	}

	// 写侧：反复进房/离房。
	for i := 0; i < 2; i++ {
		wg.Add(1)
		clientID := "race-client-" + string(rune('a'+i))
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					if err := m.Join(JoinRequest{
						RoomID: r.ID, ClientID: clientID, DisplayName: "观众", Role: model.RoleViewer,
					}); err == nil {
						m.Leave(r.ID, clientID)
					}
				}
			}
		}()
	}

	// 写侧：反复改元数据（内存侧的授权 + 无副作用路径）。
	wg.Add(1)
	go func() {
		defer wg.Done()
		other := "另一个标题"
		for {
			select {
			case <-stop:
				return
			default:
				_ = m.SetRoomMeta(r.ID, 7, &other, nil)
			}
		}
	}()

	// 让并发跑一小段时间，然后收网。
	for i := 0; i < 200; i++ {
		_ = m.RoomCount()
	}
	close(stop)
	wg.Wait()
}

// TestSnapshotReadsPlaybackProgress 覆盖快照里的 PlayingIndex：
// 主播下发控制指令后 seq 会递增，列表页据此把"正在放映"的房间排在前面。
func TestSnapshotReadsPlaybackProgress(t *testing.T) {
	m, _ := newSnapshotManager(t)

	r, _, err := m.CreateOwned("PLAY0001", "", 0, 5)
	if err != nil {
		t.Fatalf("建房失败: %v", err)
	}
	if err := m.Join(JoinRequest{RoomID: r.ID, ClientID: "h1", DisplayName: "主播", Role: model.RoleHost}); err != nil {
		t.Fatalf("进房失败: %v", err)
	}

	if got := m.ListRooms()[0].PlayingIndex; got != 0 {
		t.Fatalf("还没下发过控制指令时 PlayingIndex 应当是 0，实际 %d", got)
	}

	if err := m.HandleControl(r.ID, "h1", model.Envelope{
		Type: model.TypeRoomControl, Action: model.ActionPlay, CurrentTime: 12.5,
	}); err != nil {
		t.Fatalf("下发控制失败: %v", err)
	}
	if got := m.ListRooms()[0].PlayingIndex; got != 1 {
		t.Fatalf("下发一次控制之后 PlayingIndex 应当是 1，实际 %d", got)
	}
}

// TestSetRoomMetaAuthorizationAndNotFound 覆盖 T1 的两条判据：
// 房间不存在 → ErrNotFound；调用者不是房主 → ErrNotRoomOwner；空 patch → ErrBadRoomMeta。
func TestSetRoomMetaAuthorizationAndNotFound(t *testing.T) {
	m, _ := newSnapshotManager(t)

	r, _, err := m.CreateOwned("META0001", "", 0, 100)
	if err != nil {
		t.Fatalf("建房失败: %v", err)
	}
	title := "客厅"
	isPublic := true

	// 房主本人：成功。
	if err := m.SetRoomMeta(r.ID, 100, &title, &isPublic); err != nil {
		t.Fatalf("房主改元数据应当成功，实际 %v", err)
	}
	// 只改一项也合法。
	if err := m.SetRoomMeta(r.ID, 100, &title, nil); err != nil {
		t.Fatalf("只改标题应当成功，实际 %v", err)
	}
	if err := m.SetRoomMeta(r.ID, 100, nil, &isPublic); err != nil {
		t.Fatalf("只改公开性应当成功，实际 %v", err)
	}

	// 非房主 → ErrNotRoomOwner。
	if err := m.SetRoomMeta(r.ID, 101, &title, nil); !errors.Is(err, ErrNotRoomOwner) {
		t.Fatalf("非房主应当拿到 ErrNotRoomOwner，实际 %v", err)
	}
	// 无房主身份（0）→ 同样拒绝。
	if err := m.SetRoomMeta(r.ID, 0, &title, nil); !errors.Is(err, ErrNotRoomOwner) {
		t.Fatalf("匿名（actor=0）应当拿到 ErrNotRoomOwner，实际 %v", err)
	}

	// 空 patch → ErrBadRoomMeta（静默 no-op 会让"点了没反应"变成查不出来的 bug）。
	if err := m.SetRoomMeta(r.ID, 100, nil, nil); !errors.Is(err, ErrBadRoomMeta) {
		t.Fatalf("空 patch 应当拿到 ErrBadRoomMeta，实际 %v", err)
	}

	// 房间不存在 → ErrNotFound（与房间用例的既有 not-found 语义一致）。
	if err := m.SetRoomMeta("NOPE0001", 100, &title, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("房间不存在应当拿到 ErrNotFound，实际 %v", err)
	}

	// 房间已被关掉 → 同样是 ErrNotFound（拿到指针之前房间可能刚被销毁）。
	if err := m.CloseRoomByAdmin(r.ID, "测试关闭"); err != nil {
		t.Fatalf("强关失败: %v", err)
	}
	if err := m.SetRoomMeta(r.ID, 100, &title, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("关房之后应当拿到 ErrNotFound，实际 %v", err)
	}
}

// TestCloseRoomByAdminUsesExistingPath 覆盖 T1 的"受控导出"：
// 强关必须走同一条 closeRoom 路径（广播 room-closed + bus.CloseRoom），
// 而不是另写一份只删 map 的实现。
func TestCloseRoomByAdminUsesExistingPath(t *testing.T) {
	m, bus := newSnapshotManager(t)

	r, _, err := m.CreateOwned("ADMIN001", "", 0, 1)
	if err != nil {
		t.Fatalf("建房失败: %v", err)
	}
	if err := m.Join(JoinRequest{RoomID: r.ID, ClientID: "h1", DisplayName: "主播", Role: model.RoleHost}); err != nil {
		t.Fatalf("进房失败: %v", err)
	}

	if err := m.CloseRoomByAdmin(r.ID, "管理员强制关闭了该房间"); err != nil {
		t.Fatalf("强关失败: %v", err)
	}

	// ① 房间从内存里摘掉。
	if _, ok := m.Get(r.ID); ok {
		t.Fatal("强关之后房间必须从内存里消失")
	}
	if m.RoomCount() != 0 {
		t.Fatalf("房间数应当归零，实际 %d", m.RoomCount())
	}
	// ② 房内收到 room-closed 广播（带原因）。
	env := bus.lastBroadcastOfType(t, r.ID, model.TypeRoomClosed)
	if env.Code != model.CodeRoomClosed {
		t.Fatalf("关房广播应当带 %s，实际 %q", model.CodeRoomClosed, env.Code)
	}
	if env.Message != "管理员强制关闭了该房间" {
		t.Fatalf("关房原因应当原样广播，实际 %q", env.Message)
	}
	// ③ bus.CloseRoom 被调用（生产里它就是 service.Hub.CloseRoom，负责断开连接）。
	bus.mu.Lock()
	closed := append([]string(nil), bus.closed...)
	bus.mu.Unlock()
	if len(closed) != 1 || closed[0] != r.ID {
		t.Fatalf("强关必须走 bus.CloseRoom(%s)，实际 %v", r.ID, closed)
	}

	// 房间不存在 → ErrNotFound（管理端要能区分"关掉了"与"本来就没了"）。
	if err := m.CloseRoomByAdmin("NOPE0002", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("关闭不存在的房间应当拿到 ErrNotFound，实际 %v", err)
	}
	// 重复关闭 → 也是 ErrNotFound（第一次已经把房间摘掉了）。
	if err := m.CloseRoomByAdmin(r.ID, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复关闭应当拿到 ErrNotFound，实际 %v", err)
	}
}

// TestRoomHasPasswordExposesOnlyBoolean 覆盖 T2/T3 用到的 HasPassword 出口：
// 它只回答"有没有密码"，绝不把密码原文带出包。
func TestRoomHasPasswordExposesOnlyBoolean(t *testing.T) {
	m, _ := newSnapshotManager(t)

	withPw, _, err := m.CreateOwned("HASPW001", "s3cret", 0, 1)
	if err != nil {
		t.Fatalf("建房失败: %v", err)
	}
	without, _, err := m.CreateOwned("NOPW0001", "", 0, 1)
	if err != nil {
		t.Fatalf("建房失败: %v", err)
	}

	if !withPw.HasPassword() {
		t.Fatal("设了密码的房间 HasPassword 必须为 true")
	}
	if without.HasPassword() {
		t.Fatal("没设密码的房间 HasPassword 必须为 false")
	}
}
