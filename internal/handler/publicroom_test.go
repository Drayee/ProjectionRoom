package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/auth"
	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/model"
	"ProjectionRoom/internal/service"
	"ProjectionRoom/internal/store"
	"ProjectionRoom/internal/usecase"
)

// 本文件是二期 T2（公开房列表）与 T3（房主的标题/公开性）的验收面。
//
// 装配形态与生产一致（同一个 NewRouter、同一组中间件），只把两处外部世界换成桩：
//
//   - rooms_meta 用一张内存表（fakeRoomMetaStore）：本文件要判的是协议面与副作用
//     （哪些房间出现、响应里有哪些字段、投递了几次 upsert），它们与 SQL 无关；
//   - 昵称出口用一张内存昵称表（fakeNameStore）。
//
// 房间本身用**真实**的 usecase.Manager：它承载 T2 最核心的那条判据
// （"内存实况 ∩ 元数据公开性"），用假房间表会把这条判据验掉。

// fakeRoomMetaStore 是内存版的 rooms_meta。
//
// 它同时满足三个端口：公开列表的批量读、房主改元数据的单行读+写、管理端的筛选读。
// 另外实现 RoomMetaSink 的 SubmitRoomMeta —— 自愈投递走异步投递接口，
// 这里让它落进同一张表，于是"自愈之后库里有没有那一行"可以被直接断言。
type fakeRoomMetaStore struct {
	mu   sync.Mutex
	rows map[string]store.RoomMeta
	// byIDCalls / listCalls / submitCalls 记录三类调用次数。
	byIDCalls   int
	listCalls   int
	submitCalls int
	// rejectSubmit 为 true 时模拟"队列满"。
	rejectSubmit bool
}

func newFakeRoomMetaStore() *fakeRoomMetaStore {
	return &fakeRoomMetaStore{rows: map[string]store.RoomMeta{}}
}

func (s *fakeRoomMetaStore) seed(m store.RoomMeta) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	if m.LastSeenAt.IsZero() {
		m.LastSeenAt = time.Now()
	}
	s.rows[m.RoomID] = m
}

func (s *fakeRoomMetaStore) get(roomID string) (store.RoomMeta, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.rows[roomID]
	return m, ok
}

func (s *fakeRoomMetaStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.rows)
}

func (s *fakeRoomMetaStore) reads() (byID, list int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byIDCalls, s.listCalls
}

// submissions 返回自愈投递的次数。
func (s *fakeRoomMetaStore) submissions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.submitCalls
}

// SubmitRoomMeta 是 RoomMetaSink 的假实现：把投递的元数据直接写进这张内存表。
//
// 真实现会把作业投进写队列（异步），这里同步落表 —— 本文件要验的是
// "缺行时投递了一次、内容是不是完整期望状态"，而"投递确实异步"由 store 的用例覆盖。
func (s *fakeRoomMetaStore) SubmitRoomMeta(m *store.RoomMeta) bool {
	if m == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rejectSubmit {
		return false
	}
	s.submitCalls++
	cp := *m
	if existing, ok := s.rows[m.RoomID]; ok {
		cp.CreatedAt = existing.CreatedAt
	}
	s.rows[m.RoomID] = cp
	return true
}

func (s *fakeRoomMetaStore) ListRoomMetas(ctx context.Context, ids []string) (map[string]store.RoomMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listCalls++
	out := make(map[string]store.RoomMeta, len(ids))
	for _, id := range ids {
		if m, ok := s.rows[id]; ok {
			out[id] = m
		}
	}
	return out, nil
}

func (s *fakeRoomMetaStore) RoomMetaByID(ctx context.Context, roomID string) (*store.RoomMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byIDCalls++
	m, ok := s.rows[roomID]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := m
	return &cp, nil
}

func (s *fakeRoomMetaStore) UpsertRoomMeta(ctx context.Context, m *store.RoomMeta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *m
	if existing, ok := s.rows[m.RoomID]; ok {
		// 与真 store 一致的覆盖语义：created_at 保持首次值。
		cp.CreatedAt = existing.CreatedAt
	}
	s.rows[m.RoomID] = cp
	return nil
}

func (s *fakeRoomMetaStore) UpdateRoomMeta(ctx context.Context, roomID string, patch store.RoomMetaPatch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.rows[roomID]
	if !ok {
		return store.ErrNotFound
	}
	if patch.Title != nil {
		m.Title = *patch.Title
	}
	if patch.IsPublic != nil {
		m.IsPublic = *patch.IsPublic
	}
	if patch.HasPassword != nil {
		m.HasPassword = *patch.HasPassword
	}
	if patch.LastSeenAt != nil {
		m.LastSeenAt = *patch.LastSeenAt
	}
	if patch.ClosedAt != nil {
		m.ClosedAt = patch.ClosedAt
	}
	s.rows[roomID] = m
	return nil
}

// QueryRoomMetas 让这张内存表也满足管理端的元数据端口（T4）。
//
// 筛选与排序口径与 store.QueryRoomMetas 一致（search 匹配房间码或标题、
// last_seen_at DESC + room_id ASC 保证分页稳定），这样 T2/T3/T4 可以共用同一张夹具表。
func (s *fakeRoomMetaStore) QueryRoomMetas(ctx context.Context, q store.RoomMetaQuery) ([]store.RoomMeta, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	pattern := strings.ToLower(strings.TrimSpace(q.Search))
	match := make([]store.RoomMeta, 0, len(s.rows))
	for _, m := range s.rows {
		if pattern != "" &&
			!strings.Contains(strings.ToLower(m.RoomID), pattern) &&
			!strings.Contains(strings.ToLower(m.Title), pattern) {
			continue
		}
		if q.PublicOnly && !m.IsPublic {
			continue
		}
		if q.PrivateOnly && m.IsPublic {
			continue
		}
		match = append(match, m)
	}
	sort.Slice(match, func(i, j int) bool {
		if !match[i].LastSeenAt.Equal(match[j].LastSeenAt) {
			return match[i].LastSeenAt.After(match[j].LastSeenAt)
		}
		return match[i].RoomID < match[j].RoomID
	})
	return match, int64(len(match)), nil
}

// fakeNameStore 是内存昵称表（只回答昵称，不含任何账号档案）。
type fakeNameStore struct {
	mu    sync.Mutex
	names map[int64]string
	calls int
}

func (s *fakeNameStore) set(id int64, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.names == nil {
		s.names = map[int64]string{}
	}
	s.names[id] = name
}

func (s *fakeNameStore) ProfileNameByID(ctx context.Context, id int64) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.names[id], nil
}

// roomMetaEnv 是一台"二期端点可用"的测试服务。
type roomMetaEnv struct {
	engine *gin.Engine
	rooms  *usecase.Manager
	hub    *service.Hub
	meta   *fakeRoomMetaStore
	// sink 与 meta 是同一个对象（它同时是读表与异步投递出口）；
	// 分开两个字段只是为了让用例读起来像在生产装配里那样"读一处、投一处"。
	sink  *fakeRoomMetaStore
	names *fakeNameStore
	svc   *fakeAccountService
	cfg   *config.Config
	// owner 是房主账号（建房时绑定它），other 是另一个用户（用于 403 用例）。
	owner      *store.User
	other      *store.User
	ownerToken string
	otherToken string
}

// newRoomMetaEnv 起一台测试服务。mutate 可改配置（例如把限速压到 1/分钟）。
func newRoomMetaEnv(t *testing.T, mutate func(*config.Config)) *roomMetaEnv {
	t.Helper()

	cfg := config.Default()
	if mutate != nil {
		mutate(cfg)
	}
	cfg.Auth.DBDSN = "host=127.0.0.1 dbname=fixture sslmode=disable"

	hub, cleanup, err := service.NewHub(cfg)
	if err != nil {
		t.Fatalf("构造 Hub 失败: %v", err)
	}
	rooms := usecase.NewManager(cfg, hub)

	svc := newFakeAccountService(t)
	owner := svc.seeded("owner", "stored-owner-pass", store.RoleUser, store.StatusActive)
	other := svc.seeded("other", "stored-other-pass", store.RoleUser, store.StatusActive)

	meta := newFakeRoomMetaStore()
	names := &fakeNameStore{names: map[int64]string{}}
	names.set(owner.ID, "房主小明")
	names.set(other.ID, "别人")

	engine := NewRouter(cfg, hub, rooms, nil,
		AuthDeps{
			Service:      svc,
			Session:      SessionConfig{AccessTTL: cfg.Auth.AccessTTL, RefreshTTL: cfg.Auth.RefreshTTL, WSTicketTTL: cfg.Auth.WSTicketTTL},
			RoomMeta:     meta,
			ProfileNames: names,
		},
		AdminDeps{Rooms: rooms, RoomMeta: meta},
	)
	t.Cleanup(func() { rooms.Stop(); cleanup() })

	return &roomMetaEnv{
		engine: engine, rooms: rooms, hub: hub, meta: meta, sink: meta, names: names, svc: svc, cfg: cfg,
		owner: owner, other: other, ownerToken: issueTestToken(t, owner), otherToken: issueTestToken(t, other),
	}
}

// issueTestToken 用与假 service 相同的测试密钥签发一张 access token。
//
// 为什么不复用 ws_test.go 的 roomFixtureToken：那个函数把 id 钉在 1（夹具账号），
// 而本文件需要"两个不同身份的 token"（房主与非房主）——
// 假 service 的校验只认签名与 tv，因此换一个 id 重新签一张就够了。
func issueTestToken(t *testing.T, u *store.User) string {
	t.Helper()
	signer, err := auth.NewSigner(handlerTestSecret, 15*time.Minute)
	if err != nil {
		t.Fatalf("构造 Signer 失败: %v", err)
	}
	tok, _, err := signer.Issue(u.ID, u.Role, u.TokenVersion)
	if err != nil {
		t.Fatalf("签发 token 失败: %v", err)
	}
	return tok
}

// —— 用例：公开房列表（T2）——

// TestPublicRoomsOnlyListsExplicitlyPublicAndNeverLeaks 覆盖 T2 的核心判据：
//
//	只有"内存里存在 + 元数据显式公开 + 未关闭"的房间出现；
//	密码房的信息**零外露**（响应里没有 password / email / role / status / ownerUserId）。
func TestPublicRoomsOnlyListsExplicitlyPublicAndNeverLeaks(t *testing.T) {
	env := newRoomMetaEnv(t, nil)

	// 三个房间：公开无密码、公开有密码、非公开。
	open, _, err := env.rooms.CreateOwned("OPEN0001", "", 0, env.owner.ID)
	if err != nil {
		t.Fatalf("建房失败: %v", err)
	}
	locked, _, err := env.rooms.CreateOwned("LOCKED01", "s3cret", 0, env.owner.ID)
	if err != nil {
		t.Fatalf("建房失败: %v", err)
	}
	privateRoom, _, err := env.rooms.CreateOwned("PRIV0001", "", 0, env.owner.ID)
	if err != nil {
		t.Fatalf("建房失败: %v", err)
	}
	env.meta.seed(store.RoomMeta{RoomID: open.ID, OwnerUserID: env.owner.ID, Title: "客厅", IsPublic: true})
	env.meta.seed(store.RoomMeta{RoomID: locked.ID, OwnerUserID: env.owner.ID, Title: "密室", IsPublic: true, HasPassword: true})
	env.meta.seed(store.RoomMeta{RoomID: privateRoom.ID, OwnerUserID: env.owner.ID, Title: "不公开", IsPublic: false})

	// 库里也造一个公开房，但内存里没有它 → 不该出现（内存实况是候选集合）。
	env.meta.seed(store.RoomMeta{RoomID: "GHOST001", OwnerUserID: env.owner.ID, Title: "幽灵", IsPublic: true})

	w := doJSON(t, env.engine, http.MethodGet, "/api/public-rooms", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("免登录访问公开列表应当 200，实际 %d（%s）", w.Code, w.Body.String())
	}

	body := w.Body.String()
	// 字段白名单：这些键一旦出现就是泄露（password 是房间密码，其余是房主账号档案）。
	for _, forbidden := range []string{"password", "email", "role", "status", "ownerUserId", "owner_user_id", "members"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("公开列表响应不得出现 %q：%s", forbidden, body)
		}
	}

	var page struct {
		Items []struct {
			RoomID      string `json:"roomId"`
			Title       string `json:"title"`
			OwnerName   string `json:"ownerName"`
			MemberCount int    `json:"memberCount"`
			HostOnline  bool   `json:"hostOnline"`
			HostOffline bool   `json:"hostOffline"`
			HasPassword bool   `json:"hasPassword"`
		} `json:"items"`
		Total  int `json:"total"`
		Limit  int `json:"limit"`
		Offset int `json:"offset"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("解析公开列表失败: %v（body=%s）", err, body)
	}

	if page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("应当只列两个公开房（OPEN0001/LOCKED01），实际 total=%d items=%+v", page.Total, page.Items)
	}
	if page.Limit != defaultPublicRoomPageLimit || page.Offset != 0 {
		t.Fatalf("缺省分页应为 limit=%d offset=0，实际 limit=%d offset=%d",
			defaultPublicRoomPageLimit, page.Limit, page.Offset)
	}

	byID := map[string]int{}
	for i, it := range page.Items {
		byID[it.RoomID] = i
		if it.OwnerName != "房主小明" {
			t.Fatalf("房主昵称应当来自昵称出口，实际 %+v", it)
		}
	}
	openIdx, ok := byID["OPEN0001"]
	if !ok {
		t.Fatalf("公开无密码房必须出现：%+v", page.Items)
	}
	lockIdx, ok := byID["LOCKED01"]
	if !ok {
		t.Fatalf("公开**密码房**也要出现（只是带 hasPassword 标记）：%+v", page.Items)
	}
	if page.Items[lockIdx].HasPassword != true {
		t.Fatalf("密码房必须带 hasPassword=true：%+v", page.Items[lockIdx])
	}
	if page.Items[openIdx].HasPassword {
		t.Fatalf("无密码房不该被标成密码房：%+v", page.Items[openIdx])
	}
	if _, ghost := byID["GHOST001"]; ghost {
		t.Fatalf("内存里不存在的房间不得出现（实况取自内存）：%+v", page.Items)
	}

	// 批量查询：只应发生一次 ListRoomMetas（不是每个房间一次）。
	if byIDCalls, listCalls := env.meta.reads(); listCalls != 1 || byIDCalls != 0 {
		t.Fatalf("元数据应当批量查一次，实际 list=%d byID=%d", listCalls, byIDCalls)
	}
}

// TestPublicRoomsMarksHostOfflineInGrace 覆盖 §6 的"宽限期内的房间仍算存在，
// 但要标 hostOffline"。用假的 bus 驱动 Leave，从而不必起真实 WebSocket。
func TestPublicRoomsMarksHostOfflineInGrace(t *testing.T) {
	env := newRoomMetaEnv(t, func(cfg *config.Config) {
		cfg.Room.HostGrace = time.Minute // 宽限期足够长：本用例不等待它到期
	})

	r, _, err := env.rooms.CreateOwned("GRACE001", "", 0, env.owner.ID)
	if err != nil {
		t.Fatalf("建房失败: %v", err)
	}
	env.meta.seed(store.RoomMeta{RoomID: r.ID, OwnerUserID: env.owner.ID, IsPublic: true, Title: "宽限期房间"})

	// 主播进房再离开 → 房间进入宽限期（Room 仍在，hostOffline 置位）。
	if err := env.rooms.Join(usecase.JoinRequest{
		RoomID: r.ID, ClientID: "host-conn", DisplayName: "主播", Role: model.RoleHost,
	}); err != nil {
		t.Fatalf("主播进房失败: %v", err)
	}
	env.rooms.Leave(r.ID, "host-conn")

	// 房间还在内存里（宽限期内不销毁）。
	if _, ok := env.rooms.Get(r.ID); !ok {
		t.Fatal("宽限期内房间不该被销毁")
	}

	w := doJSON(t, env.engine, http.MethodGet, "/api/public-rooms", "", "")
	var page struct {
		Items []struct {
			RoomID      string `json:"roomId"`
			HostOnline  bool   `json:"hostOnline"`
			HostOffline bool   `json:"hostOffline"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("解析失败: %v（body=%s）", err, w.Body.String())
	}
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("宽限期内房间仍应出现在公开列表，实际 %d（%s）", page.Total, w.Body.String())
	}
	if page.Items[0].HostOnline || !page.Items[0].HostOffline {
		t.Fatalf("应当标记 hostOnline=false / hostOffline=true，实际 %+v", page.Items[0])
	}
}

// TestPublicRoomsDegradesAndHealsMissingMeta 覆盖 §2 的"缺行降级 + 自愈投递"。
func TestPublicRoomsDegradesAndHealsMissingMeta(t *testing.T) {
	env := newRoomMetaEnv(t, nil)

	healMe, _, err := env.rooms.CreateOwned("HEAL0001", "pw12", 0, env.owner.ID)
	if err != nil {
		t.Fatalf("建房失败: %v", err)
	}
	env.meta.seed(store.RoomMeta{RoomID: "KNOWN001", OwnerUserID: env.owner.ID, Title: "有行", IsPublic: true})
	if _, _, err := env.rooms.CreateOwned("KNOWN001", "", 0, env.owner.ID); err != nil {
		t.Fatalf("建房失败: %v", err)
	}

	w := doJSON(t, env.engine, http.MethodGet, "/api/public-rooms", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("应当 200，实际 %d（%s）", w.Code, w.Body.String())
	}

	var page struct {
		Items []struct {
			RoomID string `json:"roomId"`
			Title  string `json:"title"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	// 缺行的房间照常列出（降级），因此两个房间都在。
	if page.Total != 2 {
		t.Fatalf("缺元数据的房间必须降级列出（共 2 个），实际 %d（%+v）", page.Total, page.Items)
	}
	var degraded bool
	for _, it := range page.Items {
		if it.RoomID == healMe.ID {
			degraded = true
			if it.Title != "" {
				t.Fatalf("缺元数据时标题必须是空串，实际 %q", it.Title)
			}
		}
	}
	if !degraded {
		t.Fatalf("缺元数据的房间应当出现：%+v", page.Items)
	}

	// 自愈：恰好一次投递，且带完整期望状态（owner/title/isPublic/hasPassword）。
	if n := env.meta.submissions(); n != 1 {
		t.Fatalf("缺元数据行应当投递**恰好一次**自愈 upsert，实际 %d 次", n)
	}
	got, ok := env.meta.get(healMe.ID)
	if !ok {
		t.Fatal("自愈投递之后库里应当有那一行")
	}
	if got.RoomID != healMe.ID || got.OwnerUserID != env.owner.ID {
		t.Fatalf("自愈投递的房间/房主不对：%+v", got)
	}
	if got.IsPublic || got.Title != "" || !got.HasPassword {
		t.Fatalf("自愈投递必须是「未公开 / 标题空 / 保留 hasPassword」：%+v", got)
	}
	if got.CreatedAt.IsZero() || got.LastSeenAt.IsZero() {
		t.Fatalf("自愈投递必须带时间戳：%+v", got)
	}
}

// TestPublicRoomsPaginationBounds 覆盖分页边界：越界截断、非法值 400。
func TestPublicRoomsPaginationBounds(t *testing.T) {
	env := newRoomMetaEnv(t, nil)

	for _, id := range []string{"PAGE0001", "PAGE0002", "PAGE0003"} {
		if _, _, err := env.rooms.CreateOwned(id, "", 0, env.owner.ID); err != nil {
			t.Fatalf("建房 %s 失败: %v", id, err)
		}
		env.meta.seed(store.RoomMeta{RoomID: id, OwnerUserID: env.owner.ID, IsPublic: true})
	}

	var page struct {
		Items  []json.RawMessage `json:"items"`
		Total  int               `json:"total"`
		Limit  int               `json:"limit"`
		Offset int               `json:"offset"`
	}

	// limit 越过上限 → 截断到上限（并回显生效值）。
	w := doJSON(t, env.engine, http.MethodGet, "/api/public-rooms?limit=9999", "", "")
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if page.Limit != maxPublicRoomPageLimit {
		t.Fatalf("limit=9999 应被截断到 %d，实际 %d", maxPublicRoomPageLimit, page.Limit)
	}
	if page.Total != 3 || len(page.Items) != 3 {
		t.Fatalf("total 是过滤后的总数（3），实际 %d items=%d", page.Total, len(page.Items))
	}

	// 负 offset 归零、limit=0 取缺省。
	w = doJSON(t, env.engine, http.MethodGet, "/api/public-rooms?limit=0&offset=-5", "", "")
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if page.Limit != defaultPublicRoomPageLimit || page.Offset != 0 {
		t.Fatalf("limit=0/offset=-5 应回落到 %d/0，实际 %d/%d",
			defaultPublicRoomPageLimit, page.Limit, page.Offset)
	}

	// 越界 offset → 空 items 而不是报错。
	w = doJSON(t, env.engine, http.MethodGet, "/api/public-rooms?offset=99", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("越界 offset 应当 200，实际 %d", w.Code)
	}
	_ = json.Unmarshal(w.Body.Bytes(), &page)
	if len(page.Items) != 0 || page.Total != 3 {
		t.Fatalf("越界 offset 应返回空页 + 正确 total，实际 items=%d total=%d", len(page.Items), page.Total)
	}

	// 非整数 → 400（静默降级会让"翻页翻不到东西"变成查不出来的 bug）。
	for _, bad := range []string{"limit=abc", "offset=x"} {
		w = doJSON(t, env.engine, http.MethodGet, "/api/public-rooms?"+bad, "", "")
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s 应当 400，实际 %d（%s）", bad, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), model.CodeBadRequest) {
			t.Fatalf("400 应带 %s：%s", model.CodeBadRequest, w.Body.String())
		}
	}
}

// TestPublicRoomsRateLimited 覆盖 T2 的限速判据（429 + RATE_LIMITED）。
func TestPublicRoomsRateLimited(t *testing.T) {
	env := newRoomMetaEnv(t, func(cfg *config.Config) {
		cfg.IPC.PublicRoomsPerMinute = 0.001
		cfg.IPC.PublicRoomsBurst = 2
	})

	first := doJSON(t, env.engine, http.MethodGet, "/api/public-rooms", "", "")
	if first.Code != http.StatusOK {
		t.Fatalf("第一次请求应当 200，实际 %d", first.Code)
	}
	for i := 0; i < 5; i++ {
		w := doJSON(t, env.engine, http.MethodGet, "/api/public-rooms", "", "")
		if w.Code == http.StatusTooManyRequests {
			if !strings.Contains(w.Body.String(), model.CodeRateLimited) {
				t.Fatalf("429 应带 %s：%s", model.CodeRateLimited, w.Body.String())
			}
			return
		}
	}
	t.Fatal("连续请求必须触发 429（限速未生效）")
}

// TestPublicRoomsRouteNotShadowedByRoomInfo 回归：GET /api/rooms/:roomId（既有）
// 与 GET /api/public-rooms 必须在同一棵路由树上各就各位。
func TestPublicRoomsRouteNotShadowedByRoomInfo(t *testing.T) {
	env := newRoomMetaEnv(t, nil)

	w := doJSON(t, env.engine, http.MethodGet, "/api/rooms/NOPE0001", "", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("不存在的房间码应当 404，实际 %d（%s）", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "房间不存在") {
		t.Fatalf("应当是房间查询的 404（而不是路由缺失）：%s", w.Body.String())
	}

	w = doJSON(t, env.engine, http.MethodGet, "/api/public-rooms", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("公开列表应当 200，实际 %d（%s）", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"limit":`+strconv.Itoa(defaultPublicRoomPageLimit)) {
		t.Fatalf("公开列表响应缺少分页元数据：%s", w.Body.String())
	}
}

// —— 用例：房主的标题 / 公开性（T3）——

// TestRoomMetaPatchOwnerCanSetTitleAndPublic 覆盖 T3 的正常路径：
// 房主改标题与公开性 → 200；内存侧不报错；异步投递一次 upsert（带完整期望状态）。
func TestRoomMetaPatchOwnerCanSetTitleAndPublic(t *testing.T) {
	env := newRoomMetaEnv(t, nil)

	r, _, err := env.rooms.CreateOwned("META0001", "pw12", 0, env.owner.ID)
	if err != nil {
		t.Fatalf("建房失败: %v", err)
	}
	env.meta.seed(store.RoomMeta{RoomID: r.ID, OwnerUserID: env.owner.ID, Title: "旧标题", IsPublic: false, HasPassword: true})

	w := doJSON(t, env.engine, http.MethodPatch, "/api/rooms/"+r.ID+"/meta",
		`{"title":"新标题","isPublic":true}`, env.ownerToken)
	if w.Code != http.StatusOK {
		t.Fatalf("房主改元数据应当 200，实际 %d（%s）", w.Code, w.Body.String())
	}
	var resp struct {
		RoomID   string `json:"roomId"`
		Title    string `json:"title"`
		IsPublic bool   `json:"isPublic"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.RoomID != r.ID || resp.Title != "新标题" || !resp.IsPublic {
		t.Fatalf("响应应当回显改动后的期望状态，实际 %+v", resp)
	}

	// 异步投递：恰好一次 upsert，且带**完整**期望状态（owner/title/isPublic/hasPassword）。
	if n := env.meta.submissions(); n != 1 {
		t.Fatalf("应当投递恰好一次 upsert，实际 %d 次", n)
	}
	got, ok := env.meta.get(r.ID)
	if !ok {
		t.Fatal("投递之后库里应当有那一行")
	}
	if got.RoomID != r.ID || got.OwnerUserID != env.owner.ID {
		t.Fatalf("投递的房间/房主不对：%+v", got)
	}
	if got.Title != "新标题" || !got.IsPublic {
		t.Fatalf("投递的标题/公开性不对：%+v", got)
	}
	if !got.HasPassword {
		t.Fatalf("投递必须保留 has_password（整体覆盖语义下最容易丢的字段）：%+v", got)
	}
}

// TestRoomMetaPatchRejectsNonOwnerAndMissingRoom 覆盖 T3 的两条拒绝路径：
// 非房主 403、房间不存在 404。
func TestRoomMetaPatchRejectsNonOwnerAndMissingRoom(t *testing.T) {
	env := newRoomMetaEnv(t, nil)

	r, _, err := env.rooms.CreateOwned("META0002", "", 0, env.owner.ID)
	if err != nil {
		t.Fatalf("建房失败: %v", err)
	}
	env.meta.seed(store.RoomMeta{RoomID: r.ID, OwnerUserID: env.owner.ID})

	// 非房主 → 403 + FORBIDDEN。
	w := doJSON(t, env.engine, http.MethodPatch, "/api/rooms/"+r.ID+"/meta",
		`{"title":"抢改"}`, env.otherToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("非房主改元数据应当 403，实际 %d（%s）", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), CodeForbidden) {
		t.Fatalf("403 应带 %s：%s", CodeForbidden, w.Body.String())
	}

	// 房间不存在 → 404。
	w = doJSON(t, env.engine, http.MethodPatch, "/api/rooms/NOPE0009/meta",
		`{"title":"x"}`, env.ownerToken)
	if w.Code != http.StatusNotFound {
		t.Fatalf("房间不存在应当 404，实际 %d（%s）", w.Code, w.Body.String())
	}

	// 被拒的两次都不得投递任何 upsert（"投递只记真正发生的事"）。
	if n := env.meta.submissions(); n != 0 {
		t.Fatalf("被拒的请求不该投递元数据，实际 %d 次", n)
	}
}

// TestRoomMetaPatchRequiresAuthAndValidBody 覆盖 401 与 400 两条输入边界。
func TestRoomMetaPatchRequiresAuthAndValidBody(t *testing.T) {
	env := newRoomMetaEnv(t, nil)

	r, _, err := env.rooms.CreateOwned("META0003", "", 0, env.owner.ID)
	if err != nil {
		t.Fatalf("建房失败: %v", err)
	}

	// 匿名 → 401（RequireAuth）。
	w := doJSON(t, env.engine, http.MethodPatch, "/api/rooms/"+r.ID+"/meta", `{"title":"x"}`, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("匿名改元数据应当 401，实际 %d（%s）", w.Code, w.Body.String())
	}

	cases := []struct {
		name, body string
		want       int
	}{
		{"两项都缺", `{}`, http.StatusBadRequest},
		{"不是 JSON", `not-json`, http.StatusBadRequest},
		{"标题超长", `{"title":"` + strings.Repeat("标", maxRoomTitleLen+1) + `"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := doJSON(t, env.engine, http.MethodPatch, "/api/rooms/"+r.ID+"/meta", tc.body, env.ownerToken)
			if w.Code != tc.want {
				t.Fatalf("应当 %d，实际 %d（%s）", tc.want, w.Code, w.Body.String())
			}
		})
	}
}

// TestRoomMetaPatchStripsControlChars 覆盖 §7.3 的标题清洗：
// 零宽 / Bidi 控制符必须被去掉（它们能让列表页显示成完全不同的文字）。
func TestRoomMetaPatchStripsControlChars(t *testing.T) {
	env := newRoomMetaEnv(t, nil)

	r, _, err := env.rooms.CreateOwned("META0004", "", 0, env.owner.ID)
	if err != nil {
		t.Fatalf("建房失败: %v", err)
	}

	// 前后留空白 + 中间夹 U+200B（零宽空格）与 U+202E（Bidi 覆盖）。
	dirty := "  客\u200b厅\u202e  "
	w := doJSON(t, env.engine, http.MethodPatch, "/api/rooms/"+r.ID+"/meta",
		`{"title":"`+dirty+`"}`, env.ownerToken)
	if w.Code != http.StatusOK {
		t.Fatalf("应当 200，实际 %d（%s）", w.Code, w.Body.String())
	}

	stored, ok := env.meta.get(r.ID)
	if !ok {
		t.Fatal("改标题之后库里应当有那一行")
	}
	if stored.Title != "客厅" {
		t.Fatalf("标题应当被清洗成 %q，实际 %q", "客厅", stored.Title)
	}
}

// —— 用例：房主回读元数据（T3 读侧：GET /api/rooms/:id/meta）——

// TestRoomMetaGetOwnerReadsOwnMeta 覆盖房主回读的成功路径：
// 200 + 四个字段，且响应里**不含**内部账号 id / 邮箱 / 角色 / 成员明细。
func TestRoomMetaGetOwnerReadsOwnMeta(t *testing.T) {
	env := newRoomMetaEnv(t, nil)

	r, _, err := env.rooms.CreateOwned("GETMETA1", "pw12", 0, env.owner.ID)
	if err != nil {
		t.Fatalf("建房失败: %v", err)
	}
	env.meta.seed(store.RoomMeta{
		RoomID: r.ID, OwnerUserID: env.owner.ID, Title: "我的客厅", IsPublic: true, HasPassword: true,
	})

	w := doJSON(t, env.engine, http.MethodGet, "/api/rooms/"+r.ID+"/meta", "", env.ownerToken)
	if w.Code != http.StatusOK {
		t.Fatalf("房主回读自己的元数据应当 200，实际 %d（%s）", w.Code, w.Body.String())
	}

	body := w.Body.String()
	// 字段白名单：这些键一旦出现就是泄露（内部 id / 账号档案 / 成员明细）。
	for _, forbidden := range []string{"ownerUserId", "owner_user_id", "email", "role", "status", "members", "password_hash"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("房主元数据响应不得出现 %q：%s", forbidden, body)
		}
	}

	var resp struct {
		RoomID      string `json:"roomId"`
		Title       string `json:"title"`
		IsPublic    bool   `json:"isPublic"`
		HasPassword bool   `json:"hasPassword"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v（body=%s）", err, body)
	}
	if resp.RoomID != r.ID || resp.Title != "我的客厅" || !resp.IsPublic || !resp.HasPassword {
		t.Fatalf("响应内容不对：%+v", resp)
	}

	// 顶层字段必须**只有**这四个（多一个就是协议面扩大）。
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	allowed := map[string]bool{"roomId": true, "title": true, "isPublic": true, "hasPassword": true}
	for k := range raw {
		if !allowed[k] {
			t.Fatalf("房主元数据响应字段超出白名单：%q（body=%s）", k, body)
		}
	}

	// 读-改-写闭环：PATCH 之后回读必须看到新值。
	pw := doJSON(t, env.engine, http.MethodPatch, "/api/rooms/"+r.ID+"/meta",
		`{"title":"改过的标题","isPublic":false}`, env.ownerToken)
	if pw.Code != http.StatusOK {
		t.Fatalf("PATCH 应当 200，实际 %d（%s）", pw.Code, pw.Body.String())
	}
	w = doJSON(t, env.engine, http.MethodGet, "/api/rooms/"+r.ID+"/meta", "", env.ownerToken)
	if w.Code != http.StatusOK {
		t.Fatalf("回读应当 200，实际 %d（%s）", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Title != "改过的标题" || resp.IsPublic {
		t.Fatalf("回读必须反映刚写进去的值：%+v", resp)
	}
	if !resp.HasPassword {
		t.Fatalf("回读必须保留 has_password（PATCH 只管 title/isPublic）：%+v", resp)
	}
}

// TestRoomMetaGetNonOwnerAndMissingAreBoth404 覆盖这条路由最关键的取舍：
// 非房主与"不存在"给出**逐字相同**的 404，不留存在性 oracle。
func TestRoomMetaGetNonOwnerAndMissingAreBoth404(t *testing.T) {
	env := newRoomMetaEnv(t, nil)

	// 存在的房间（元数据齐全）：非房主也必须是 404，而不是 403。
	r, _, err := env.rooms.CreateOwned("GETMETA2", "", 0, env.owner.ID)
	if err != nil {
		t.Fatalf("建房失败: %v", err)
	}
	env.meta.seed(store.RoomMeta{RoomID: r.ID, OwnerUserID: env.owner.ID, Title: "非请勿入", IsPublic: true})

	notOwner := doJSON(t, env.engine, http.MethodGet, "/api/rooms/"+r.ID+"/meta", "", env.otherToken)
	if notOwner.Code != http.StatusNotFound {
		t.Fatalf("非房主应当 404（不留存在性 oracle），实际 %d（%s）", notOwner.Code, notOwner.Body.String())
	}

	// 房间根本不存在。
	missing := doJSON(t, env.engine, http.MethodGet, "/api/rooms/NOPE0007/meta", "", env.ownerToken)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("不存在的房间应当 404，实际 %d（%s）", missing.Code, missing.Body.String())
	}

	// 两者响应**逐字相同** —— 这是"没有 oracle"的可判定证据。
	if notOwner.Body.String() != missing.Body.String() {
		t.Fatalf("非房主与不存在必须给出同一响应，实际：\n非房主=%s\n不存在=%s",
			notOwner.Body.String(), missing.Body.String())
	}

	// 顺带确认：非房主也读不到标题（响应里没有任何房间内容）。
	if strings.Contains(notOwner.Body.String(), "非请勿入") {
		t.Fatalf("非房主的 404 不得泄露标题：%s", notOwner.Body.String())
	}
}

// TestRoomMetaGetMissingRowIs404 覆盖"元数据缺行"这一条：
// 房间在内存里、房主也是本人，但库里没有行 → 404（不补默认值假成功）。
func TestRoomMetaGetMissingRowIs404(t *testing.T) {
	env := newRoomMetaEnv(t, nil)

	r, _, err := env.rooms.CreateOwned("GETMETA3", "", 0, env.owner.ID)
	if err != nil {
		t.Fatalf("建房失败: %v", err)
	}
	// 刻意**不**给这个房间种元数据行。

	w := doJSON(t, env.engine, http.MethodGet, "/api/rooms/"+r.ID+"/meta", "", env.ownerToken)
	if w.Code != http.StatusNotFound {
		t.Fatalf("元数据缺行应当 404，实际 %d（%s）", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), model.CodeRoomNotFound) {
		t.Fatalf("404 应带 %s：%s", model.CodeRoomNotFound, w.Body.String())
	}
	// 缺行是**读**路径，不该顺手写库（自愈是公开列表与 PATCH 的事）。
	if n := env.meta.submissions(); n != 0 {
		t.Fatalf("读端点不该投递任何元数据写入，实际 %d 次", n)
	}

	// 补一次 PATCH 之后就能读回（缺行不是死局）。
	if pw := doJSON(t, env.engine, http.MethodPatch, "/api/rooms/"+r.ID+"/meta",
		`{"title":"补回来的标题"}`, env.ownerToken); pw.Code != http.StatusOK {
		t.Fatalf("PATCH 应当 200，实际 %d（%s）", pw.Code, pw.Body.String())
	}
	w = doJSON(t, env.engine, http.MethodGet, "/api/rooms/"+r.ID+"/meta", "", env.ownerToken)
	if w.Code != http.StatusOK {
		t.Fatalf("补写之后回读应当 200，实际 %d（%s）", w.Code, w.Body.String())
	}
	var resp struct {
		Title string `json:"title"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Title != "补回来的标题" {
		t.Fatalf("回读应当看到补写进去的标题，实际 %q", resp.Title)
	}
}

// TestRoomMetaGetRequiresAuthAndRateLimits 覆盖 401 与 429 两条边界。
func TestRoomMetaGetRequiresAuthAndRateLimits(t *testing.T) {
	t.Run("未登录 401", func(t *testing.T) {
		env := newRoomMetaEnv(t, nil)
		r, _, err := env.rooms.CreateOwned("GETMETA4", "", 0, env.owner.ID)
		if err != nil {
			t.Fatalf("建房失败: %v", err)
		}
		env.meta.seed(store.RoomMeta{RoomID: r.ID, OwnerUserID: env.owner.ID})

		w := doJSON(t, env.engine, http.MethodGet, "/api/rooms/"+r.ID+"/meta", "", "")
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("未登录应当 401，实际 %d（%s）", w.Code, w.Body.String())
		}
	})

	t.Run("限速 429", func(t *testing.T) {
		env := newRoomMetaEnv(t, func(cfg *config.Config) {
			cfg.IPC.RoomMetaPerMinute = 0.001
			cfg.IPC.RoomMetaBurst = 1
		})
		r, _, err := env.rooms.CreateOwned("GETMETA5", "", 0, env.owner.ID)
		if err != nil {
			t.Fatalf("建房失败: %v", err)
		}
		env.meta.seed(store.RoomMeta{RoomID: r.ID, OwnerUserID: env.owner.ID})

		first := doJSON(t, env.engine, http.MethodGet, "/api/rooms/"+r.ID+"/meta", "", env.ownerToken)
		if first.Code != http.StatusOK {
			t.Fatalf("第一次请求应当 200，实际 %d（%s）", first.Code, first.Body.String())
		}
		for i := 0; i < 5; i++ {
			w := doJSON(t, env.engine, http.MethodGet, "/api/rooms/"+r.ID+"/meta", "", env.ownerToken)
			if w.Code == http.StatusTooManyRequests {
				if !strings.Contains(w.Body.String(), model.CodeRateLimited) {
					t.Fatalf("429 应带 %s：%s", model.CodeRateLimited, w.Body.String())
				}
				return
			}
		}
		t.Fatal("连续请求必须触发 429（房间元数据读取限速未生效）")
	})

	t.Run("装配残缺 503", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		cfg := config.Default()
		cfg.Auth.DBDSN = "host=127.0.0.1 dbname=x sslmode=disable"

		r := gin.New()
		api := r.Group("/api")
		// Sink 缺失：读端点也一并 503（见 registerRoomMetaRoutes 的说明）。
		registerRoomMetaRoutes(api, RoomMetaDeps{Meta: newFakeRoomMetaStore()}, cfg, newFakeAccountService(t))

		w := doJSON(t, r, http.MethodGet, "/api/rooms/ABCD1234/meta", "", "")
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("装配残缺应当 503，实际 %d（%s）", w.Code, w.Body.String())
		}
	})
}

// TestRoomMetaGetRouteNotRegisteredWithoutDSN 回归：账号能力关闭时读路由也不存在。
func TestRoomMetaGetRouteNotRegisteredWithoutDSN(t *testing.T) {
	cfg := config.Default() // Auth.DBDSN 默认为空
	cfg.Static.Serve = false

	hub, cleanup, err := service.NewHub(cfg)
	if err != nil {
		t.Fatalf("构造 Hub 失败: %v", err)
	}
	rooms := usecase.NewManager(cfg, hub)
	t.Cleanup(func() { rooms.Stop(); cleanup() })

	engine := NewRouter(cfg, hub, rooms, nil, AuthDeps{}, AdminDeps{})
	w := doJSON(t, engine, http.MethodGet, "/api/rooms/ABCD1234/meta", "", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("账号能力关闭时该路由不该存在，实际 %d（%s）", w.Code, w.Body.String())
	}
}

// TestRoomMetaAuthMissingDepReturns503 覆盖装配残缺时的语义：
// 缺 Sink 时端点存在但一律 503（比"路由凭空消失"好定位）。
func TestRoomMetaAuthMissingDepReturns503(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Default()
	cfg.Auth.DBDSN = "host=127.0.0.1 dbname=x sslmode=disable"

	r := gin.New()
	api := r.Group("/api")
	// Sink 缺失：装配残缺。
	registerRoomMetaRoutes(api, RoomMetaDeps{Meta: newFakeRoomMetaStore()}, cfg, newFakeAccountService(t))

	w := doJSON(t, r, http.MethodPatch, "/api/rooms/ABCD1234/meta", `{"title":"x"}`, "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("装配残缺应当 503，实际 %d（%s）", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), CodeAuthUnavailable) {
		t.Fatalf("应当带 %s：%s", CodeAuthUnavailable, w.Body.String())
	}
}

// TestRoomMetaRouteNotRegisteredWithoutDSN 回归：账号能力关闭时本路由不注册。
func TestRoomMetaRouteNotRegisteredWithoutDSN(t *testing.T) {
	cfg := config.Default() // Auth.DBDSN 默认为空
	cfg.Static.Serve = false

	hub, cleanup, err := service.NewHub(cfg)
	if err != nil {
		t.Fatalf("构造 Hub 失败: %v", err)
	}
	rooms := usecase.NewManager(cfg, hub)
	t.Cleanup(func() { rooms.Stop(); cleanup() })

	engine := NewRouter(cfg, hub, rooms, nil, AuthDeps{}, AdminDeps{})
	w := doJSON(t, engine, http.MethodPatch, "/api/rooms/ABCD1234/meta", `{"title":"x"}`, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("账号能力关闭时该路由不该存在，实际 %d（%s）", w.Code, w.Body.String())
	}
}
