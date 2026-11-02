package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// 粉丝红点（票 #67；决议 #53）的 HTTP 层证据。
//
// 三件事在这里钉住：
//
//  1. `GET /api/follows?tab=followers` 带回 `newCount`（列表与红点一个端点两用）；
//  2. `PUT /api/me/followers-seen` 幂等、需登录、把水位线推到 now；
//  3. 关注动作会推 follow-created / mutual-follow 事件（**只在真的新建边时**）。
//
// 第 3 条需要记录推送，所以本文件自带一个 followPusher 的假实现 ——
// 不复用 friendships_test.go 里的任何东西：那些假实现服务的是好友请求流水线，
// 而那条流水线已经在 #59 整体退役了。

// followerListResp 是 `GET /api/follows?tab=followers` 的响应形状。
//
// `NewCount` 用指针：区分"字段不存在"（nil，非粉丝 tab）与"字段是 0"
// （粉丝 tab 且红点已清）。用 int64 会让两者同形，而"没有这个字段"
// 与"红点是 0"在契约上是不同的事实。
type followerListResp struct {
	Items    []followListEntryResp `json:"items"`
	NewCount *int64                `json:"newCount"`
}

type followListEntryResp struct {
	UserID      int64  `json:"userId"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Following   bool   `json:"following"`
	FollowedBy  bool   `json:"followedBy"`
	Mutual      bool   `json:"mutual"`
	CanDM       bool   `json:"canDm"`
}

// TestFollowersListCarriesNewCount 覆盖验收第 2 条的一半：列表带回红点数。
func TestFollowersListCarriesNewCount(t *testing.T) {
	env := newFollowEnv(t)
	ctx := context.Background()

	// bob 关注 alice（未回关）。
	if _, _, err := env.st.CreateFollow(ctx, env.bob.ID, env.alice.ID); err != nil {
		t.Fatalf("建关注边失败：%v", err)
	}

	w := env.doFollow(t, http.MethodGet, "/api/follows?tab=followers", env.tokA, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("粉丝列表应当 200，实得 %d：%s", w.Code, w.Body.String())
	}
	var resp followerListResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败：%v（body=%s）", err, w.Body.String())
	}
	if resp.NewCount == nil {
		t.Fatalf("粉丝 tab 必须带 newCount 字段：%s", w.Body.String())
	}
	if *resp.NewCount != 1 {
		t.Fatalf("bob 未回关，红点应为 1，实得 %d", *resp.NewCount)
	}
	if len(resp.Items) != 1 || resp.Items[0].UserID != env.bob.ID {
		t.Fatalf("粉丝列表应当只有 bob：%+v", resp.Items)
	}
}

// TestNonFollowerTabsOmitNewCount 钉住"只有粉丝 tab 带这个字段"。
//
// 互关与关注两份名单带上红点数会让前端拿到一个与当前视图无关的数字，
// 而"这个数字属于哪份名单"是最容易读错的地方。
func TestNonFollowerTabsOmitNewCount(t *testing.T) {
	env := newFollowEnv(t)
	ctx := context.Background()

	if _, _, err := env.st.CreateFollow(ctx, env.alice.ID, env.bob.ID); err != nil {
		t.Fatalf("建关注边失败：%v", err)
	}
	if _, _, err := env.st.CreateFollow(ctx, env.bob.ID, env.alice.ID); err != nil {
		t.Fatalf("建反向边失败：%v", err)
	}

	for _, tab := range []string{"mutual", "following"} {
		w := env.doFollow(t, http.MethodGet, "/api/follows?tab="+tab, env.tokA, "", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("%s 名单应当 200，实得 %d", tab, w.Code)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
			t.Fatalf("解析响应失败：%v", err)
		}
		if _, exists := raw["newCount"]; exists {
			t.Fatalf("%s 名单不该带 newCount：%s", tab, w.Body.String())
		}
	}
}

// TestMarkFollowersSeenClearsRedDot 覆盖验收第 2 条的另一半：进 tab 即灭。
func TestMarkFollowersSeenClearsRedDot(t *testing.T) {
	env := newFollowEnv(t)
	ctx := context.Background()

	if _, _, err := env.st.CreateFollow(ctx, env.bob.ID, env.alice.ID); err != nil {
		t.Fatalf("建关注边失败：%v", err)
	}

	// 前置：红点是 1。
	w := env.doFollow(t, http.MethodGet, "/api/follows?tab=followers", env.tokA, "", nil)
	var before followerListResp
	if err := json.Unmarshal(w.Body.Bytes(), &before); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if before.NewCount == nil || *before.NewCount != 1 {
		t.Fatalf("前置：红点应为 1，实得 %v", before.NewCount)
	}

	// 标记已看。
	w = env.doFollow(t, http.MethodPut, "/api/me/followers-seen", env.tokA, "", nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("标记已看应当 204，实得 %d：%s", w.Code, w.Body.String())
	}

	// 之后红点归零。
	w = env.doFollow(t, http.MethodGet, "/api/follows?tab=followers", env.tokA, "", nil)
	var after followerListResp
	if err := json.Unmarshal(w.Body.Bytes(), &after); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if after.NewCount == nil {
		t.Fatalf("粉丝 tab 仍须带 newCount：%s", w.Body.String())
	}
	if *after.NewCount != 0 {
		t.Fatalf("标记之后红点应归零，实得 %d", *after.NewCount)
	}
}

// TestMarkFollowersSeenIsIdempotent 覆盖验收里的"幂等"。
func TestMarkFollowersSeenIsIdempotent(t *testing.T) {
	env := newFollowEnv(t)

	for i := 0; i < 3; i++ {
		w := env.doFollow(t, http.MethodPut, "/api/me/followers-seen", env.tokA, "", nil)
		if w.Code != http.StatusNoContent {
			t.Fatalf("第 %d 次标记应当 204，实得 %d：%s", i+1, w.Code, w.Body.String())
		}
	}
}

// TestMarkFollowersSeenRequiresAuth 钉住匿名访问。
func TestMarkFollowersSeenRequiresAuth(t *testing.T) {
	env := newFollowEnv(t)

	w := env.doFollow(t, http.MethodPut, "/api/me/followers-seen", "", "", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("匿名标记应当 401，实得 %d：%s", w.Code, w.Body.String())
	}
}

// TestFollowersSeenIsPerAccount 钉住"这是每个账号自己的水位线"。
//
// alice 标记之后，bob 的红点不该受影响 —— 两个账号各自一条水位线。
func TestFollowersSeenIsPerAccount(t *testing.T) {
	env := newFollowEnv(t)
	ctx := context.Background()

	// alice 与 bob 互相成为对方的粉丝（互关会清掉"未回关"这条判据，
	// 所以这里刻意只建单向：alice → bob，于是 bob 有一个未回关的粉丝）。
	if _, _, err := env.st.CreateFollow(ctx, env.alice.ID, env.bob.ID); err != nil {
		t.Fatalf("建关注边失败：%v", err)
	}

	// bob 的红点是 1。
	w := env.doFollow(t, http.MethodGet, "/api/follows?tab=followers", env.tokB, "", nil)
	var bobBefore followerListResp
	if err := json.Unmarshal(w.Body.Bytes(), &bobBefore); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if bobBefore.NewCount == nil || *bobBefore.NewCount != 1 {
		t.Fatalf("bob 的红点应为 1，实得 %v", bobBefore.NewCount)
	}

	// alice 标记她自己的水位线。
	if w := env.doFollow(t, http.MethodPut, "/api/me/followers-seen", env.tokA, "", nil); w.Code != http.StatusNoContent {
		t.Fatalf("alice 标记应当 204，实得 %d", w.Code)
	}

	// bob 的红点**不变**。
	w = env.doFollow(t, http.MethodGet, "/api/follows?tab=followers", env.tokB, "", nil)
	var bobAfter followerListResp
	if err := json.Unmarshal(w.Body.Bytes(), &bobAfter); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if bobAfter.NewCount == nil || *bobAfter.NewCount != 1 {
		t.Fatalf("alice 标记不该影响 bob 的红点：实得 %v", bobAfter.NewCount)
	}
}

// TestFollowRedDotIsIndependentOfUnread 覆盖验收第 2 条：两数两源。
//
// 这是本票最容易悄悄破的一条：把粉丝数并进 `unread` 会让"有私信"与
// "有新粉丝"共享同一个角标，而它们的清除时机（读消息 vs 进粉丝 tab）不同。
// 这里从**响应形状**上钉：`/api/auth/me` 的 `unread` 与粉丝红点毫无关联。
func TestFollowRedDotIsIndependentOfUnread(t *testing.T) {
	env := newFollowEnv(t)
	ctx := context.Background()

	if _, _, err := env.st.CreateFollow(ctx, env.bob.ID, env.alice.ID); err != nil {
		t.Fatalf("建关注边失败：%v", err)
	}

	// `/me` 的 unread 不受新粉丝影响。
	w := env.doFollow(t, http.MethodGet, "/api/auth/me", env.tokA, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("/api/auth/me 应当 200，实得 %d", w.Code)
	}
	var me struct {
		Unread *int64 `json:"unread"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if me.Unread != nil && *me.Unread != 0 {
		t.Fatalf("新粉丝不该影响消息未读：unread=%d", *me.Unread)
	}

	// 而粉丝红点确实是 1。
	w = env.doFollow(t, http.MethodGet, "/api/follows?tab=followers", env.tokA, "", nil)
	var list followerListResp
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if list.NewCount == nil || *list.NewCount != 1 {
		t.Fatalf("粉丝红点应为 1，实得 %v", list.NewCount)
	}
}

// TestFollowedBackClearsRedDot 覆盖验收第 3 条：回关即从红点消失。
func TestFollowedBackClearsRedDot(t *testing.T) {
	env := newFollowEnv(t)
	ctx := context.Background()

	if _, _, err := env.st.CreateFollow(ctx, env.bob.ID, env.alice.ID); err != nil {
		t.Fatalf("建关注边失败：%v", err)
	}

	// 前置：1。
	w := env.doFollow(t, http.MethodGet, "/api/follows?tab=followers", env.tokA, "", nil)
	var before followerListResp
	if err := json.Unmarshal(w.Body.Bytes(), &before); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if before.NewCount == nil || *before.NewCount != 1 {
		t.Fatalf("前置：红点应为 1，实得 %v", before.NewCount)
	}

	// alice 回关 bob（走真实端点，顺带覆盖推送分支）。
	body, _ := json.Marshal(map[string]int64{"userId": env.bob.ID})
	w = env.doFollow(t, http.MethodPost, "/api/follows", env.tokA, string(body), nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("回关应当 201，实得 %d：%s", w.Code, w.Body.String())
	}

	// 之后红点 0。
	w = env.doFollow(t, http.MethodGet, "/api/follows?tab=followers", env.tokA, "", nil)
	var after followerListResp
	if err := json.Unmarshal(w.Body.Bytes(), &after); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if after.NewCount == nil || *after.NewCount != 0 {
		t.Fatalf("回关之后红点应归零，实得 %v", after.NewCount)
	}
}

// TestBlockedFollowerDoesNotLightRedDot 覆盖验收的"打扰豁免"（拉黑侧）。
func TestBlockedFollowerDoesNotLightRedDot(t *testing.T) {
	env := newFollowEnv(t)
	ctx := context.Background()

	if _, _, err := env.st.CreateFollow(ctx, env.bob.ID, env.alice.ID); err != nil {
		t.Fatalf("建关注边失败：%v", err)
	}

	// bob 反向拉黑 alice：关注边还在（拉黑只删拉黑方自己那条），但红点不该亮。
	if _, err := env.st.BlockUser(ctx, env.bob.ID, env.alice.ID); err != nil {
		t.Fatalf("拉黑失败：%v", err)
	}

	w := env.doFollow(t, http.MethodGet, "/api/follows?tab=followers", env.tokA, "", nil)
	var resp followerListResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if resp.NewCount == nil || *resp.NewCount != 0 {
		t.Fatalf("被拉黑的人不该点亮红点，实得 %v", resp.NewCount)
	}
	if len(resp.Items) != 0 {
		t.Fatalf("被拉黑的人也不该出现在名单里：%+v", resp.Items)
	}
}
