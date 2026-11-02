package handler

import (
	"context"
	"net/http"
	"testing"

	"ProjectionRoom/internal/store"
)

// 本文件是票 #64 的**协议面**证据：三条收藏端点的路径/方法/幂等/状态码，
// 以及"列表与详情批量回填 favorited 且不做 N+1"。
//
// 与 store/favorites_test.go 的分工：那里验库里的行与失效语义；
// 这里验 HTTP 契约（路由挂在哪、未登录 401、对象不存在 404、重复 PUT 不报错）。
//
// 装配复用 newAnimeThreadEnv（它已经起了一台"帖子域 + 番剧域都可用"的引擎），
// 因为收藏是**多态**的，只起帖子域就没法验 room/anime 两支。

// favEnv 是收藏端点的测试装配。
type favEnv struct {
	*atEnv
	// roomID 是 e.author 名下的一个公开房间（收藏房间那一支用）。
	roomID string
}

// newFavEnv 在 atEnv 之上补一个可收藏的房间。
//
// 房间用 UpsertRoomMeta 直插而不是走 `POST /api/rooms`：建房要求内存管理器
// 起真实房间，而收藏只关心 rooms_meta 里的可见性（is_public ∧ closed_at IS NULL）。
// 用 HTTP 建房会把这条用例变成"建房链路的集成测试"，那不是它的判据。
func newFavEnv(t *testing.T) *favEnv {
	t.Helper()
	env := newAnimeThreadEnv(t)
	ctx := context.Background()
	roomID := "FAVROOM1"
	if err := env.st.UpsertRoomMeta(ctx, &store.RoomMeta{
		RoomID: roomID, OwnerUserID: env.author.ID, Title: "可收藏的放映室", IsPublic: true,
	}); err != nil {
		t.Fatalf("建房间元数据失败：%v", err)
	}
	return &favEnv{atEnv: env, roomID: roomID}
}

// createPost 通过 HTTP 发一篇帖子（走完整装配路径，不直插库）。
func (e *favEnv) createPost(t *testing.T, title, token string) int64 {
	t.Helper()
	w := e.do(t, http.MethodPost, "/api/posts", token, map[string]any{
		"title": title, "content": "正文",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("发帖失败：%d %s", w.Code, w.Body.String())
	}
	var row struct {
		ID int64 `json:"id"`
	}
	decodeJSON(t, w, &row)
	if row.ID <= 0 {
		t.Fatalf("发帖未回 id：%s", w.Body.String())
	}
	return row.ID
}

// TestFavoriteEndpointsRequireAuth 钉住「全部需登录」。
//
// 未登录**不是**静默成功：前端据 401 引导登录（决议 #48 §6 末条）。
func TestFavoriteEndpointsRequireAuth(t *testing.T) {
	e := newFavEnv(t)
	cases := []struct{ method, path string }{
		{http.MethodGet, "/api/favorites?type=post"},
		{http.MethodPut, "/api/posts/1/favorite"},
		{http.MethodDelete, "/api/posts/1/favorite"},
		{http.MethodPut, "/api/animes/1/favorite"},
		{http.MethodPut, "/api/rooms/FAVROOM1/favorite"},
		{http.MethodDelete, "/api/rooms/FAVROOM1/favorite"},
	}
	for _, tc := range cases {
		w := e.do(t, tc.method, tc.path, "", nil)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s 未登录应 401，实际 %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
}

// TestFavoritePostRoundTrip 钉住 PUT/DELETE 的往返与幂等。
//
// 幂等的可观察形态：第二次 PUT 仍 200 且 favorited=true，只是 created=false；
// 第二次 DELETE 仍 200 且 favorited=false，只是 removed=false。
// **不是 409/404** —— 前端重试不该看到失败。
func TestFavoritePostRoundTrip(t *testing.T) {
	e := newFavEnv(t)
	postID := e.createPost(t, "可收藏的帖子", e.tokAu)
	path := "/api/posts/" + itoa(postID) + "/favorite"

	w := e.do(t, http.MethodPut, path, e.tokOt, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("首次收藏应 200，实际 %d %s", w.Code, w.Body.String())
	}
	// 每次解码都换一个**新**结构体：created / removed 带 omitempty，
	// false 时字段不出现，复用同一个变量会把上一次的 true 留在里面
	//（这个坑真的踩过：第二次 PUT 的 created 明明是 false，断言却看到 true）。
	var first struct {
		Favorited bool `json:"favorited"`
		Created   bool `json:"created"`
	}
	decodeJSON(t, w, &first)
	if !first.Favorited || !first.Created {
		t.Fatalf("首次收藏应 favorited=true created=true，实际 %+v", first)
	}

	w = e.do(t, http.MethodPut, path, e.tokOt, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("重复收藏应仍 200，实际 %d %s", w.Code, w.Body.String())
	}
	var again struct {
		Favorited bool `json:"favorited"`
		Created   bool `json:"created"`
	}
	decodeJSON(t, w, &again)
	if !again.Favorited || again.Created {
		t.Fatalf("重复收藏应 favorited=true created=false，实际 %+v", again)
	}

	w = e.do(t, http.MethodDelete, path, e.tokOt, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("取消收藏应 200，实际 %d %s", w.Code, w.Body.String())
	}
	var removed struct {
		Favorited bool `json:"favorited"`
		Removed   bool `json:"removed"`
	}
	decodeJSON(t, w, &removed)
	if removed.Favorited || !removed.Removed {
		t.Fatalf("取消收藏应 favorited=false removed=true，实际 %+v", removed)
	}

	w = e.do(t, http.MethodDelete, path, e.tokOt, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("重复取消应仍 200，实际 %d %s", w.Code, w.Body.String())
	}
	var removedAgain struct {
		Favorited bool `json:"favorited"`
		Removed   bool `json:"removed"`
	}
	decodeJSON(t, w, &removedAgain)
	if removedAgain.Favorited || removedAgain.Removed {
		t.Fatalf("重复取消应 favorited=false removed=false，实际 %+v", removedAgain)
	}
}

// TestFavoriteMissingTargetIs404 钉住「对象不存在 → 404，且不写孤儿行」。
func TestFavoriteMissingTargetIs404(t *testing.T) {
	e := newFavEnv(t)
	cases := []string{
		"/api/posts/99999999/favorite",
		"/api/animes/99999999/favorite",
		"/api/rooms/NOSUCHROOM/favorite",
	}
	for _, path := range cases {
		w := e.do(t, http.MethodPut, path, e.tokOt, nil)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s 应 404，实际 %d %s", path, w.Code, w.Body.String())
		}
	}
}

// TestFavoriteListRequiresType 钉住 type 必填且只能是三个值。
//
// 缺省 type 返回什么都说不清（"全部类型混排"没有定义好的排序），
// 而猜一个默认值会让客户端传错时安静地拿到别的东西。
func TestFavoriteListRequiresType(t *testing.T) {
	e := newFavEnv(t)
	for _, q := range []string{"", "?type=", "?type=bogus", "?type=POSTS"} {
		w := e.do(t, http.MethodGet, "/api/favorites"+q, e.tokOt, nil)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("/api/favorites%s 应 400，实际 %d %s", q, w.Code, w.Body.String())
		}
	}
}

// TestFavoriteListOnlyMine 钉住「仅自己可见」：别人的收藏列表里没有我的条目。
func TestFavoriteListOnlyMine(t *testing.T) {
	e := newFavEnv(t)
	postID := e.createPost(t, "只有我收藏", e.tokAu)
	path := "/api/posts/" + itoa(postID) + "/favorite"
	if w := e.do(t, http.MethodPut, path, e.tokOt, nil); w.Code != http.StatusOK {
		t.Fatalf("收藏失败：%d %s", w.Code, w.Body.String())
	}

	mine := e.do(t, http.MethodGet, "/api/favorites?type=post", e.tokOt, nil)
	if mine.Code != http.StatusOK {
		t.Fatalf("我的收藏列表应 200，实际 %d %s", mine.Code, mine.Body.String())
	}
	var list struct {
		Items []struct {
			TargetID string `json:"targetId"`
			Valid    bool   `json:"valid"`
			Title    string `json:"title"`
		} `json:"items"`
	}
	decodeJSON(t, mine, &list)
	if len(list.Items) != 1 || list.Items[0].TargetID != itoa(postID) {
		t.Fatalf("我的列表应有那一条，实际 %+v", list.Items)
	}
	if !list.Items[0].Valid || list.Items[0].Title == "" {
		t.Fatalf("有效条目应带标题，实际 %+v", list.Items[0])
	}

	theirs := e.do(t, http.MethodGet, "/api/favorites?type=post", e.tokAu, nil)
	decodeJSON(t, theirs, &list)
	if len(list.Items) != 0 {
		t.Fatalf("作者不该看到别人的收藏，实际 %+v", list.Items)
	}
}

// TestPostViewsCarryFavorited 钉住「列表与详情都回填 favorited」。
//
// 这条同时是「禁 N+1」的协议面证据：列表一次请求回 20 条，每条都带
// favorited —— 若实现是逐条查，它会**照样通过**（结果一样），所以真正钉住
// N+1 的是 store 层的 FavoriteStatesFor 形状（见 store/favorites_test.go
// 的 TestFavoriteStatesForIsBatch）。这里钉的是"两个视图都带这个字段"。
func TestPostViewsCarryFavorited(t *testing.T) {
	e := newFavEnv(t)
	postID := e.createPost(t, "要收藏的帖子", e.tokAu)

	// 收藏前：列表与详情都是 false。
	assertFavorited := func(path string, want bool) {
		t.Helper()
		w := e.do(t, http.MethodGet, path, e.tokOt, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("%s 应 200，实际 %d %s", path, w.Code, w.Body.String())
		}
		if path == "/api/posts" {
			var page struct {
				Items []struct {
					ID        int64 `json:"id"`
					Favorited bool  `json:"favorited"`
				} `json:"items"`
			}
			decodeJSON(t, w, &page)
			for _, it := range page.Items {
				if it.ID == postID && it.Favorited != want {
					t.Fatalf("列表里那篇 favorited 应为 %v，实际 %v", want, it.Favorited)
				}
			}
			return
		}
		var one struct {
			Favorited bool `json:"favorited"`
		}
		decodeJSON(t, w, &one)
		if one.Favorited != want {
			t.Fatalf("%s favorited 应为 %v，实际 %v", path, want, one.Favorited)
		}
	}

	assertFavorited("/api/posts", false)
	assertFavorited("/api/posts/"+itoa(postID), false)

	if w := e.do(t, http.MethodPut, "/api/posts/"+itoa(postID)+"/favorite", e.tokOt, nil); w.Code != http.StatusOK {
		t.Fatalf("收藏失败：%d %s", w.Code, w.Body.String())
	}

	assertFavorited("/api/posts", true)
	assertFavorited("/api/posts/"+itoa(postID), true)

	// 匿名视角永远是 false：没有 viewer 就没有收藏这回事。
	w := e.do(t, http.MethodGet, "/api/posts/"+itoa(postID), "", nil)
	var anon struct {
		Favorited bool `json:"favorited"`
	}
	decodeJSON(t, w, &anon)
	if anon.Favorited {
		t.Fatalf("匿名视角 favorited 必须为 false")
	}
}

// TestFavoriteAnimeAndRoomTypes 验番剧与放映室两支真的通。
//
// 三条路径共用一套处理器族，只有 id 取法不同 —— 这正是最容易被复制粘贴写错的地方。
func TestFavoriteAnimeAndRoomTypes(t *testing.T) {
	e := newFavEnv(t)
	animeID := e.createAnime(t, "可追的番")

	for _, tc := range []struct {
		name     string
		path     string
		typ      string
		targetID string
	}{
		{"番剧", "/api/animes/" + itoa(animeID) + "/favorite", "anime", itoa(animeID)},
		{"放映室", "/api/rooms/" + e.roomID + "/favorite", "room", e.roomID},
	} {
		w := e.do(t, http.MethodPut, tc.path, e.tokOt, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("%s 收藏应 200，实际 %d %s", tc.name, w.Code, w.Body.String())
		}
		list := e.do(t, http.MethodGet, "/api/favorites?type="+tc.typ, e.tokOt, nil)
		if list.Code != http.StatusOK {
			t.Fatalf("%s 列表应 200，实际 %d %s", tc.name, list.Code, list.Body.String())
		}
		var rows struct {
			Items []struct {
				TargetID   string `json:"targetId"`
				TargetType string `json:"targetType"`
				Valid      bool   `json:"valid"`
			} `json:"items"`
		}
		decodeJSON(t, list, &rows)
		if len(rows.Items) != 1 {
			t.Fatalf("%s 列表应有 1 条，实际 %+v", tc.name, rows.Items)
		}
		if rows.Items[0].TargetID != tc.targetID || rows.Items[0].TargetType != tc.typ || !rows.Items[0].Valid {
			t.Fatalf("%s 条目形状不对：%+v", tc.name, rows.Items[0])
		}
	}
}

// TestFavoriteInvalidIDIs400 钉住路径参数校验。
func TestFavoriteInvalidIDIs400(t *testing.T) {
	e := newFavEnv(t)
	for _, path := range []string{"/api/posts/abc/favorite", "/api/posts/0/favorite", "/api/animes/abc/favorite"} {
		w := e.do(t, http.MethodPut, path, e.tokOt, nil)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s 应 400，实际 %d %s", path, w.Code, w.Body.String())
		}
	}
}
