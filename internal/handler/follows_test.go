package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/service"
	"ProjectionRoom/internal/service/auth"
	"ProjectionRoom/internal/store"
)

// 关注域端点的**协议面**证据。wayfinder #56。
//
// 与 store/follows_test.go 的分工：那里验库里的边与约束，
// 这里验 HTTP 契约（401 / 400 / 404 / 403 的状态码矩阵、幂等键重放、
// 响应体形状、以及"拉黑存续期禁止新关注"这条业务规则的对外表述）。
//
// 与 handler/friendships_test.go 同装配策略：真 store（PostgreSQL）+ 真实 Signer，
// 认证侧用内存用户表。无库环境（未设 PR_TEST_DB_DSN）整组 Skip。

// followEnv 是关注域端点的测试装配。
type followEnv struct {
	engine *gin.Engine
	st     *store.Store
	alice  *store.User
	bob    *store.User
	tokA   string
	tokB   string
}

// newFollowEnv 起一台"关注域可用"的引擎（真 store + 真实 Signer）。
//
// 不接推送（pusher 传 nil）：本文件的用例判的是 HTTP 面的事实。
// 需要判"关注会不会推事件"的用例走 newFollowEnvWithPusher（票 #67）。
func newFollowEnv(t *testing.T) *followEnv {
	t.Helper()
	return newFollowEnvWithPusher(t, nil)
}

// newFollowEnvWithPusher 与 newFollowEnv 同形，只是把 FollowPusher 换成给定实现。
//
// 为什么要参数化而不是在 followEnv 上留一个可变字段：pusher 是在
// NewRouter 装配时注入的，之后再改那个字段不会影响已经注册的路由 ——
// 那会造出"看起来注入了、其实没生效"的假绿。
func newFollowEnvWithPusher(t *testing.T, pusher FollowPusher) *followEnv {
	t.Helper()

	st := requireFriendTestDB(t)

	cfg := config.Default()
	cfg.Segment.TempDir = t.TempDir()
	cfg.Static.Serve = false
	cfg.Auth.DBDSN = "host=127.0.0.1 dbname=fixture sslmode=disable"
	if cfg.Auth.PublicBaseURL == "" {
		cfg.Auth.PublicBaseURL = "https://pr.test"
	}
	// 放宽限速：本文件的用例会在同一个 IP 上连打十几个请求，
	// 而登录桶默认 10/min、容量 5 —— 那会把参数校验打成 429。
	cfg.IPC.LoginPerMinute = 1000
	cfg.IPC.LoginBurst = 200
	cfg.IPC.RoomMetaPerMinute = 1000
	cfg.IPC.RoomMetaBurst = 200

	alice := newFriendUser(t, st, "fw-alice", "爱丽丝")
	bob := newFriendUser(t, st, "fw-bob", "鲍勃")

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

	hub, cleanup, err := service.NewHub(cfg)
	if err != nil {
		t.Fatalf("构造 Hub 失败：%v", err)
	}
	rooms := service.NewManager(cfg, hub)
	t.Cleanup(func() { rooms.Stop(); cleanup() })

	engine := NewRouter(cfg, hub, rooms, nil, nil,
		AuthDeps{
			Service: svc,
			Session: SessionConfig{
				AccessTTL:   cfg.Auth.AccessTTL,
				RefreshTTL:  cfg.Auth.RefreshTTL,
				WSTicketTTL: cfg.Auth.WSTicketTTL,
			},
			Store: st,
			// 票 #67：关注事件的推送出口。nil 时 handler 只落库不推送
			//（§7.2 的"否则只落库"分支）。
			FollowPusher: pusher,
		},
		AdminDeps{
			AuditSink:   &fakeAdminWriter{},
			AuditWriter: &recordingAuditWriter{},
		},
	)

	return &followEnv{
		engine: engine, st: st,
		alice: alice, bob: bob,
		tokA: friendToken(t, signer, alice),
		tokB: friendToken(t, signer, bob),
	}
}

// followRespView 是响应体的形状（与 handler 的 followRespView 同字段名）。
type followRespView struct {
	UserID        int64  `json:"userId"`
	DisplayName   string `json:"displayName"`
	Following     bool   `json:"following"`
	FollowedBy    bool   `json:"followedBy"`
	Mutual        bool   `json:"mutual"`
	CanDM         bool   `json:"canDm"`
	FollowerCount int64  `json:"followerCount"`
	FollowingCnt  int64  `json:"followingCount"`
}

// doFollow 发一个可带自定义头的请求（幂等键那一支需要自定义头）。
func (e *followEnv) doFollow(t *testing.T, method, path, token, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req, err := http.NewRequest(method, path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.RemoteAddr = "203.0.113.7:12345"
	w := httptest.NewRecorder()
	e.engine.ServeHTTP(w, req)
	return w
}

// TestFollowEndpointsRequireAuth 覆盖验收第 2 条：需登录。
//
// 三个端点（关注 / 取关 / 查关系）都要求登录 —— 关系是**私有**事实
// （"我有没有关注 TA"不是公开信息）。
func TestFollowEndpointsRequireAuth(t *testing.T) {
	env := newFollowEnv(t)
	ctx := context.Background()

	if _, _, err := env.st.CreateFollow(ctx, env.alice.ID, env.bob.ID); err != nil {
		t.Fatalf("造一条关注边：%v", err)
	}

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/follows"},
		{http.MethodDelete, "/api/follows/" + strconv.FormatInt(env.bob.ID, 10)},
		{http.MethodGet, "/api/follows/" + strconv.FormatInt(env.bob.ID, 10)},
	}
	for _, c := range cases {
		w := env.doFollow(t, c.method, c.path, "", "", nil)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s 匿名应 401：got %d body=%s", c.method, c.path, w.Code, w.Body.String())
		}
	}
}

// TestFollowThenUnfollowSwitchesState 覆盖验收第 2 条的主路径：
// 关注 → 关系态变成 following → 取关 → 回到 none。
//
// 关系态**必须由服务端回**（不是前端自己记的）：前端记的那份会与服务端漂移，
// 而"按钮显示已关注但服务端没有这条边"正是要避免的症状。
func TestFollowThenUnfollowSwitchesState(t *testing.T) {
	env := newFollowEnv(t)
	target := strconv.FormatInt(env.bob.ID, 10)

	// 初始：没有关注。
	w := env.doFollow(t, http.MethodGet, "/api/follows/"+target, env.tokA, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("查关系应 200：got %d body=%s", w.Code, w.Body.String())
	}
	var st followRespView
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("解析响应失败：%v body=%s", err, w.Body.String())
	}
	if st.Following || st.FollowedBy || st.Mutual {
		t.Fatalf("初始应当没有关注：%+v", st)
	}

	// 关注。
	w = env.doFollow(t, http.MethodPost, "/api/follows", env.tokA, `{"userId":`+target+`}`, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("关注应 201：got %d body=%s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("解析响应失败：%v body=%s", err, w.Body.String())
	}
	if !st.Following || st.FollowedBy || st.Mutual {
		t.Fatalf("关注后应是单向 following：%+v", st)
	}
	if st.FollowerCount != 1 {
		t.Fatalf("bob 的粉丝数应为 1：%+v", st)
	}

	// 库里的边真的落了。
	ok, err := env.st.IsFollowing(context.Background(), env.alice.ID, env.bob.ID)
	if err != nil || !ok {
		t.Fatalf("库里应当有 alice→bob 这条边：ok=%v err=%v", ok, err)
	}

	// 取关。
	w = env.doFollow(t, http.MethodDelete, "/api/follows/"+target, env.tokA, "", nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("取关应 204：got %d body=%s", w.Code, w.Body.String())
	}
	w = env.doFollow(t, http.MethodGet, "/api/follows/"+target, env.tokA, "", nil)
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("解析响应失败：%v body=%s", err, w.Body.String())
	}
	if st.Following {
		t.Fatalf("取关后不应再是 following：%+v", st)
	}
}

// TestFollowRejectsSelfFollowAndMissingTarget 覆盖验收第 2 条：
// 自关注 400、对象不存在或封禁 404（防存在性探测同口径）。
func TestFollowRejectsSelfFollowAndMissingTarget(t *testing.T) {
	env := newFollowEnv(t)

	// 自关注 → 400。
	w := env.doFollow(t, http.MethodPost, "/api/follows", env.tokA,
		`{"userId":`+strconv.FormatInt(env.alice.ID, 10)+`}`, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("自关注应 400：got %d body=%s", w.Code, w.Body.String())
	}

	// userId 不是正整数 → 400。
	for _, body := range []string{`{"userId":0}`, `{"userId":-1}`, `{}`} {
		w = env.doFollow(t, http.MethodPost, "/api/follows", env.tokA, body, nil)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body=%s 应 400：got %d body=%s", body, w.Code, w.Body.String())
		}
	}

	// 请求体不是合法 JSON → 400。
	w = env.doFollow(t, http.MethodPost, "/api/follows", env.tokA, `{oops`, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("坏 JSON 应 400：got %d body=%s", w.Code, w.Body.String())
	}

	// 不存在的用户 → 404（与"不存在"与"被封禁"同口径，见 service.PublicProfile）。
	w = env.doFollow(t, http.MethodPost, "/api/follows", env.tokA, `{"userId":999999}`, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("不存在的用户应 404：got %d body=%s", w.Code, w.Body.String())
	}

	// 被封禁的用户 → 404（**同一个响应**，防存在性探测）。
	banned := newFriendUser(t, env.st, "fw-banned", "封禁者")
	if err := env.st.SetUserStatus(context.Background(), banned.ID, store.StatusBanned, "测试"); err != nil {
		t.Fatalf("封禁用户失败：%v", err)
	}
	wMissing := env.doFollow(t, http.MethodPost, "/api/follows", env.tokA, `{"userId":999999}`, nil)
	wBanned := env.doFollow(t, http.MethodPost, "/api/follows", env.tokA,
		`{"userId":`+strconv.FormatInt(banned.ID, 10)+`}`, nil)
	if wBanned.Code != http.StatusNotFound {
		t.Fatalf("被封禁的用户应 404：got %d body=%s", wBanned.Code, wBanned.Body.String())
	}
	if wMissing.Body.String() != wBanned.Body.String() {
		t.Fatalf("不存在与被封禁的响应体必须完全相同：\n  不存在 = %s\n  被封禁 = %s",
			wMissing.Body.String(), wBanned.Body.String())
	}
}

// TestFollowBlockedBothDirectionsRejected 覆盖验收第 4 条：
// 拉黑存续期**双向**禁止新关注（票 42 决议）。
//
// 三件事要钉住：
//  1. 拉黑方关注被拉黑方 → 403；
//  2. 被拉黑方关注拉黑方 → 403（方向不区分）；
//  3. **两边都不能建边**（库里真的没有边，而不只是 HTTP 层拒了）。
func TestFollowBlockedBothDirectionsRejected(t *testing.T) {
	env := newFollowEnv(t)
	ctx := context.Background()
	a := strconv.FormatInt(env.alice.ID, 10)
	b := strconv.FormatInt(env.bob.ID, 10)

	if _, err := env.st.BlockUser(ctx, env.alice.ID, env.bob.ID); err != nil {
		t.Fatalf("alice 拉黑 bob 失败：%v", err)
	}

	w := env.doFollow(t, http.MethodPost, "/api/follows", env.tokA, `{"userId":`+b+`}`, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("拉黑方关注被拉黑方应 403：got %d body=%s", w.Code, w.Body.String())
	}
	w = env.doFollow(t, http.MethodPost, "/api/follows", env.tokB, `{"userId":`+a+`}`, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("被拉黑方关注拉黑方应 403：got %d body=%s", w.Code, w.Body.String())
	}

	// 库里不该有任何一条边。
	var n int64
	if err := env.st.DB().WithContext(ctx).Model(&store.Follow{}).Count(&n).Error; err != nil {
		t.Fatalf("统计关注边失败：%v", err)
	}
	if n != 0 {
		t.Fatalf("拉黑期间不该有任何关注边，实际 %d 条", n)
	}
}

// TestFollowIdempotencyKeyReplaysSameResponse 覆盖验收第 2 条的幂等键部分：
// 同一个键 + 同一份请求体 → 原样返回首次的响应，不重复执行。
func TestFollowIdempotencyKeyReplaysSameResponse(t *testing.T) {
	env := newFollowEnv(t)
	target := strconv.FormatInt(env.bob.ID, 10)
	body := `{"userId":` + target + `}`

	const key = "follow-key-1"
	req := func(k string, b string) *httptest.ResponseRecorder {
		t.Helper()
		return env.doFollow(t, http.MethodPost, "/api/follows", env.tokA, b,
			map[string]string{"Idempotency-Key": k})
	}

	first := req(key, body)
	if first.Code != http.StatusCreated {
		t.Fatalf("首次关注应 201：got %d body=%s", first.Code, first.Body.String())
	}
	second := req(key, body)
	if second.Code != first.Code {
		t.Fatalf("重放的状态码应与首次相同：want %d got %d", first.Code, second.Code)
	}
	// 重放的响应体与首次**语义相同**。
	//
	// ⚠ 不能断言"逐字相同"：幂等响应存在 idempotency_keys.response（jsonb 列），
	// PostgreSQL 会把 jsonb 重新序列化（键排序、键值间加空格）——
	// 于是重放的字节与首次一定不同。这是 jsonb 的存储行为，不是缺陷；
	// 契约是"客户端**不可分辨**"，而客户端读的是解析后的 JSON。
	// 断言语义相等既钉住了契约，又不会因 jsonb 的规范化而假红。
	var firstBody, secondBody map[string]any
	if err := json.Unmarshal(first.Body.Bytes(), &firstBody); err != nil {
		t.Fatalf("首次响应不是 JSON：%v（%s）", err, first.Body.String())
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondBody); err != nil {
		t.Fatalf("重放响应不是 JSON：%v（%s）", err, second.Body.String())
	}
	if fmt.Sprint(firstBody) != fmt.Sprint(secondBody) {
		t.Fatalf("重放的响应内容应与首次相同：\n  首次 = %v\n  重放 = %v", firstBody, secondBody)
	}

	// 同键不同体 → 409（客户端 bug，必须拒绝而不是返回旧结果）。
	w := req(key, `{"userId":`+strconv.FormatInt(env.alice.ID, 10)+`}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("同键不同体应 409：got %d body=%s", w.Code, w.Body.String())
	}
}

// TestFollowMutualEnablesCanDM 覆盖"互关 = 好友 = 可私聊"这条链的前半段
// （can_dm 谓词的落点；发送路径换门闩是 #57 的活）。
func TestFollowMutualEnablesCanDM(t *testing.T) {
	env := newFollowEnv(t)
	ctx := context.Background()
	target := strconv.FormatInt(env.bob.ID, 10)

	// alice 单向关注 bob：不可私聊。
	if _, _, err := env.st.CreateFollow(ctx, env.alice.ID, env.bob.ID); err != nil {
		t.Fatalf("建边失败：%v", err)
	}
	w := env.doFollow(t, http.MethodGet, "/api/follows/"+target, env.tokA, "", nil)
	var st followRespView
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if st.CanDM {
		t.Fatalf("单向关注不该能私聊：%+v", st)
	}

	// bob 回关：互关 → 可私聊。
	if _, _, err := env.st.CreateFollow(ctx, env.bob.ID, env.alice.ID); err != nil {
		t.Fatalf("建反向边失败：%v", err)
	}
	w = env.doFollow(t, http.MethodGet, "/api/follows/"+target, env.tokA, "", nil)
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if !st.Mutual || !st.CanDM {
		t.Fatalf("互关应当可私聊：%+v", st)
	}
}
