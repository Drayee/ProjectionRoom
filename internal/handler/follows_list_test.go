package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"ProjectionRoom/internal/store"
)

// 好友页名单端点（票 #58）的 HTTP 面证据。
//
// 判据只落在**外部行为**上：状态码、响应形状、以及"哪些人出现了/消失了"。
// 不断言 SQL 或处理器内部的分支。

// followListView 是名单响应的形状（与 handler 的 followListEntryView 同字段名）。
type followListView struct {
	Items []struct {
		UserID      int64  `json:"userId"`
		Username    string `json:"username"`
		DisplayName string `json:"displayName"`
		Following   bool   `json:"following"`
		FollowedBy  bool   `json:"followedBy"`
		Mutual      bool   `json:"mutual"`
		CanDM       bool   `json:"canDm"`
	} `json:"items"`
}

// listIDs 把名单压成 id 集合。
func (v followListView) listIDs() map[int64]bool {
	out := make(map[int64]bool, len(v.Items))
	for _, it := range v.Items {
		out[it.UserID] = true
	}
	return out
}

// TestFollowListRequiresAuth 覆盖"名单是私有事实"：匿名一律 401。
func TestFollowListRequiresAuth(t *testing.T) {
	env := newFollowEnv(t)
	for _, tab := range []string{"mutual", "following", "followers"} {
		w := env.doFollow(t, http.MethodGet, "/api/follows?tab="+tab, "", "", nil)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("tab=%s 匿名应 401：got %d body=%s", tab, w.Code, w.Body.String())
		}
	}
}

// TestFollowListTabValidation 覆盖 tab 的校验：
//
//   - 缺失 / 空 → 400（不做"默认返回互关"：默认值属于**界面**，接口不该猜）；
//   - 未知值 → 400（同理，猜错的后果是界面显示了另一份名单）。
func TestFollowListTabValidation(t *testing.T) {
	env := newFollowEnv(t)

	if w := env.doFollow(t, http.MethodGet, "/api/follows", env.tokA, "", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("缺 tab 应 400：got %d body=%s", w.Code, w.Body.String())
	}
	if w := env.doFollow(t, http.MethodGet, "/api/follows?tab=", env.tokA, "", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("空 tab 应 400：got %d body=%s", w.Code, w.Body.String())
	}
	if w := env.doFollow(t, http.MethodGet, "/api/follows?tab=bogus", env.tokA, "", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("未知 tab 应 400：got %d body=%s", w.Code, w.Body.String())
	}
}

// TestFollowListThreeDirections 覆盖三个方向的主路径与响应形状。
func TestFollowListThreeDirections(t *testing.T) {
	env := newFollowEnv(t)
	ctx := context.Background()

	// alice ⇄ bob 互关。互关在三个名单里各出现一次（方向不同）。
	if _, _, err := env.st.CreateFollow(ctx, env.alice.ID, env.bob.ID); err != nil {
		t.Fatalf("alice→bob：%v", err)
	}
	if _, _, err := env.st.CreateFollow(ctx, env.bob.ID, env.alice.ID); err != nil {
		t.Fatalf("bob→alice：%v", err)
	}

	for _, tab := range []string{"mutual", "following", "followers"} {
		w := env.doFollow(t, http.MethodGet, "/api/follows?tab="+tab, env.tokA, "", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("tab=%s 应 200：got %d body=%s", tab, w.Code, w.Body.String())
		}
		var v followListView
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatalf("tab=%s 解析失败：%v body=%s", tab, err, w.Body.String())
		}
		if len(v.Items) != 1 || v.Items[0].UserID != env.bob.ID {
			t.Fatalf("tab=%s 只该有 bob：%+v", tab, v.Items)
		}
		it := v.Items[0]
		if !it.Following || !it.FollowedBy || !it.Mutual || !it.CanDM {
			t.Fatalf("tab=%s 的 bob 应四真：%+v", tab, it)
		}
		if it.Username != "fw-bob" || it.DisplayName != "鲍勃" {
			t.Fatalf("tab=%s 必须带 username 与 displayName：%+v", tab, it)
		}
	}
}

// TestFollowListHidesBlockedAndBanned 覆盖名单的可见性过滤（拉黑双向、封禁隐藏）。
//
// 这三条是**界面可观察**的：名单里没有的人，用户就点不到、也就发不出消息。
func TestFollowListHidesBlockedAndBanned(t *testing.T) {
	env := newFriendEnv(t)
	ctx := context.Background()

	// alice ⇄ bob（互关），alice → carol（单向）。alice 拉黑 bob。
	if _, _, err := env.st.CreateFollow(ctx, env.alice.ID, env.bob.ID); err != nil {
		t.Fatalf("alice→bob：%v", err)
	}
	if _, _, err := env.st.CreateFollow(ctx, env.bob.ID, env.alice.ID); err != nil {
		t.Fatalf("bob→alice：%v", err)
	}
	if _, _, err := env.st.CreateFollow(ctx, env.alice.ID, env.carol.ID); err != nil {
		t.Fatalf("alice→carol：%v", err)
	}
	if _, err := env.st.BlockUser(ctx, env.alice.ID, env.bob.ID); err != nil {
		t.Fatalf("拉黑 bob：%v", err)
	}

	w := env.do(t, http.MethodGet, "/api/follows?tab=following", env.tokA)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200：got %d body=%s", w.Code, w.Body.String())
	}
	var v followListView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	got := v.listIDs()
	if got[env.bob.ID] {
		t.Fatalf("被拉黑的人不该出现在我的名单里：%+v", v.Items)
	}
	if !got[env.carol.ID] {
		t.Fatalf("无关的人不该被连带过滤：%+v", v.Items)
	}

	// 封禁 carol → 从名单消失。
	if err := env.st.DB().WithContext(ctx).Model(&store.User{}).Where("id = ?", env.carol.ID).
		Update("status", store.StatusBanned).Error; err != nil {
		t.Fatalf("封禁 carol：%v", err)
	}
	w = env.do(t, http.MethodGet, "/api/follows?tab=following", env.tokA)
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if v.listIDs()[env.carol.ID] {
		t.Fatalf("被封禁的账号不该出现在名单里：%+v", v.Items)
	}
}

// TestFollowListEmptyIsEmptyArrayNotNull 覆盖空名单的形状：
// `items` 必须是 `[]` 而不是 `null` —— 前端 `v-for` 遇到 null 会炸，
// 而"新用户没有任何关注"正是最常见的首次进入路径。
func TestFollowListEmptyIsEmptyArrayNotNull(t *testing.T) {
	env := newFollowEnv(t)
	w := env.doFollow(t, http.MethodGet, "/api/follows?tab=mutual", env.tokA, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200：got %d body=%s", w.Code, w.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if string(raw["items"]) != "[]" {
		t.Fatalf("空名单的 items 必须是 []：got %s", string(raw["items"]))
	}
}

// TestSearchUsersCarriesFollowState 覆盖搜索结果的关注域字段（票 #58 验收第 3 条）。
//
// 搜索结果的按钮要按**关注关系**决定（关注/已关注/回关/发消息），
// 而拉黑存续期整个不给关注按钮 —— 所以响应必须带上这四个布尔。
// 它们由 `FollowStatesFor` 一次批量算出（不是逐人查）。
func TestSearchUsersCarriesFollowState(t *testing.T) {
	env := newFriendEnv(t)
	ctx := context.Background()

	// alice → bob 单向关注；carol → alice 单向关注（alice 该看到"回关"）。
	if _, _, err := env.st.CreateFollow(ctx, env.alice.ID, env.bob.ID); err != nil {
		t.Fatalf("alice→bob：%v", err)
	}
	if _, _, err := env.st.CreateFollow(ctx, env.carol.ID, env.alice.ID); err != nil {
		t.Fatalf("carol→alice：%v", err)
	}

	w := env.do(t, http.MethodGet, "/api/users/search?q=fr-", env.tokA)
	if w.Code != http.StatusOK {
		t.Fatalf("搜索应 200：got %d body=%s", w.Code, w.Body.String())
	}
	var res struct {
		Items []struct {
			UserID     int64  `json:"userId"`
			Username   string `json:"username"`
			Following  bool   `json:"following"`
			FollowedBy bool   `json:"followedBy"`
			Mutual     bool   `json:"mutual"`
			CanDM      bool   `json:"canDm"`
			Blocked    bool   `json:"blocked"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("解析失败：%v body=%s", err, w.Body.String())
	}
	byID := map[int64]int{}
	for i, it := range res.Items {
		byID[it.UserID] = i
	}
	bobIdx, ok := byID[env.bob.ID]
	if !ok {
		t.Fatalf("应命中 bob：%+v", res.Items)
	}
	if b := res.Items[bobIdx]; !b.Following || b.FollowedBy || b.Mutual || b.CanDM || b.Blocked {
		t.Fatalf("bob 应「我关注了 TA」且未互关：%+v", b)
	}
	carolIdx, ok := byID[env.carol.ID]
	if !ok {
		t.Fatalf("应命中 carol：%+v", res.Items)
	}
	if c := res.Items[carolIdx]; c.Following || !c.FollowedBy || c.Mutual || c.CanDM || c.Blocked {
		t.Fatalf("carol 应「TA 关注了我」且未互关：%+v", c)
	}
}

// TestFollowListEntriesExposeNoBlockDirection 覆盖"不泄露拉黑方向"：
// 名单里根本没有 blocked 字段（方向属于对方视角，见 #39 的搜索结果语义）。
func TestFollowListEntriesExposeNoBlockDirection(t *testing.T) {
	env := newFollowEnv(t)
	ctx := context.Background()
	if _, _, err := env.st.CreateFollow(ctx, env.alice.ID, env.bob.ID); err != nil {
		t.Fatalf("alice→bob：%v", err)
	}
	w := env.doFollow(t, http.MethodGet, "/api/follows?tab=following", env.tokA, "", nil)
	var raw struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(raw.Items) == 0 {
		t.Fatalf("应有 bob 一行")
	}
	for _, key := range []string{"blocked", "blockedBy", "blockedById"} {
		if _, ok := raw.Items[0][key]; ok {
			t.Fatalf("名单行不该带 %q：%+v", key, raw.Items[0])
		}
	}
}

// TestFollowListTabIsCaseSensitive 覆盖 tab 不做大小写折叠：
// 契约是四个小写字面量，接受 `MUTUAL` 只会让"合法值"变成一簇。
func TestFollowListTabIsCaseSensitive(t *testing.T) {
	env := newFollowEnv(t)
	if w := env.doFollow(t, http.MethodGet, "/api/follows?tab=MUTUAL", env.tokA, "", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("大写 tab 应 400：got %d body=%s", w.Code, w.Body.String())
	}
}

// 保住 strconv 的引用（本文件其余用例都不直接用它）。
var _ = strconv.Itoa
