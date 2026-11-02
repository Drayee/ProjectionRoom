package handler

import (
	"net/http"
	"testing"
	"time"

	"ProjectionRoom/internal/store"
)

// 票 #70 的两组端点：删自己的帖 + 我的放映室（owner 全部房）。
//
// 判据组：
//
//  1. **删帖的 403/404 矩阵**：不是自己的 → 403；不存在 → 404；匿名 → 401。
//     三者必须分开：把"别人的帖"回成 404 看似更安全，但会让"我的帖哪去了"
//     变成一次无解排查；而把"不存在"回成 403 会泄漏"这个 id 存在"。
//  2. **删除后详情 404**：这是"真的删了"的唯一可观察形态。
//  3. **我的放映室要含未公开与已关闭**：与 `GET /api/users/:id/rooms` 的口径
//     刻意相反 —— 房主是唯一需要看到那间房来重新开放它的人。
//  4. **只认 token 里的身份**：没有 owner 参数可传，也不能从 query 取。

// delPostEnv 是删帖用例的装配（复用板块测试的三用户引擎）。
func delPostEnv(t *testing.T) (*atEnv, int64) {
	t.Helper()
	env := newAnimeThreadEnv(t)
	p := &store.Post{AuthorID: env.author.ID, Title: "我要删的帖", Content: "正文", Images: []string{}}
	if err := env.st.CreatePost(t.Context(), p); err != nil {
		t.Fatalf("CreatePost: %v", err)
	}
	return env, p.ID
}

// TestDeletePostRequiresAuth 钉住匿名一律 401（不 404 —— 那个会让人以为路由不存在）。
func TestDeletePostRequiresAuth(t *testing.T) {
	env, postID := delPostEnv(t)

	w := env.do(t, http.MethodDelete, "/api/posts/"+itoa(postID), "", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("匿名删帖应当 401，实得 %d：%s", w.Code, w.Body.String())
	}
}

// TestDeletePostForbiddenForOthers 钉住「只有作者能删」这条授权（I3 在服务端）。
func TestDeletePostForbiddenForOthers(t *testing.T) {
	env, postID := delPostEnv(t)

	w := env.do(t, http.MethodDelete, "/api/posts/"+itoa(postID), env.tokOt, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("别人删帖应当 403，实得 %d：%s", w.Code, w.Body.String())
	}
	// 被拒绝之后帖子必须还在
	w2 := env.do(t, http.MethodGet, "/api/posts/"+itoa(postID), "", nil)
	if w2.Code != http.StatusOK {
		t.Fatalf("403 之后帖子不该消失，详情实得 %d", w2.Code)
	}
}

// TestDeletePostMissingIs404 钉住不存在的帖子是 404 而不是 403。
func TestDeletePostMissingIs404(t *testing.T) {
	env, _ := delPostEnv(t)

	cases := []struct {
		path string
		want int
	}{
		// 格式合法的 id，库里没有这一行 → 404
		{"/api/posts/999999999", http.StatusNotFound},
		// 参数格式问题（0 / 负数 / 非数字）→ 400，与"不存在"必须分开：
		// 混成同一个码会让前端无法区分"链接是错的"与"这帖已经被删了"。
		{"/api/posts/0", http.StatusBadRequest},
		{"/api/posts/-3", http.StatusBadRequest},
		{"/api/posts/abc", http.StatusBadRequest},
	}
	for _, tc := range cases {
		w := env.do(t, http.MethodDelete, tc.path, env.tokAu, nil)
		if w.Code != tc.want {
			t.Fatalf("%s 应当 %d，实得 %d：%s", tc.path, tc.want, w.Code, w.Body.String())
		}
	}
}

// TestDeletePostByAuthorSucceeds 钉住作者删自己的帖成功且之后 404。
func TestDeletePostByAuthorSucceeds(t *testing.T) {
	env, postID := delPostEnv(t)

	w := env.do(t, http.MethodDelete, "/api/posts/"+itoa(postID), env.tokAu, nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("作者删自己的帖应当 204，实得 %d：%s", w.Code, w.Body.String())
	}

	w2 := env.do(t, http.MethodGet, "/api/posts/"+itoa(postID), "", nil)
	if w2.Code != http.StatusNotFound {
		t.Fatalf("删掉之后详情应当 404，实得 %d：%s", w2.Code, w2.Body.String())
	}

	// 幂等：再删一次是 404（不是 500，也不是 204 —— 它已经不在了）
	w3 := env.do(t, http.MethodDelete, "/api/posts/"+itoa(postID), env.tokAu, nil)
	if w3.Code != http.StatusNotFound {
		t.Fatalf("重复删应当 404，实得 %d：%s", w3.Code, w3.Body.String())
	}
}

// TestDeletePostRemovesFromAuthorList 钉住"删完之后它不在我的列表里"。
func TestDeletePostRemovesFromAuthorList(t *testing.T) {
	env, postID := delPostEnv(t)

	before := env.do(t, http.MethodGet, "/api/users/"+itoa(env.author.ID)+"/posts", "", nil)
	if before.Code != http.StatusOK {
		t.Fatalf("删前读作者帖失败：%d", before.Code)
	}
	var listBefore profilePostListResp
	mustDecode(t, before, &listBefore)
	if len(listBefore.Items) != 1 {
		t.Fatalf("删前应当 1 篇，实得 %d", len(listBefore.Items))
	}

	if w := env.do(t, http.MethodDelete, "/api/posts/"+itoa(postID), env.tokAu, nil); w.Code != http.StatusNoContent {
		t.Fatalf("删帖失败：%d %s", w.Code, w.Body.String())
	}

	after := env.do(t, http.MethodGet, "/api/users/"+itoa(env.author.ID)+"/posts", "", nil)
	var listAfter profilePostListResp
	mustDecode(t, after, &listAfter)
	if len(listAfter.Items) != 0 {
		t.Fatalf("删后作者帖应当为空，实得 %d", len(listAfter.Items))
	}
	if listAfter.Items == nil {
		t.Fatalf("空列表必须是 [] 而不是 null")
	}
}

// myRoomsEnv 造三间房（公开未关 / 私密 / 已关闭），全部属于 author。
func myRoomsEnv(t *testing.T) *atEnv {
	t.Helper()
	env := newAnimeThreadEnv(t)
	closed := time.Now().Add(-time.Hour)
	metas := []*store.RoomMeta{
		{RoomID: "MYOpen0001", OwnerUserID: env.author.ID, Title: "公开房", IsPublic: true},
		{RoomID: "MYPriv0001", OwnerUserID: env.author.ID, Title: "私密房", IsPublic: false},
		{RoomID: "MYClos0001", OwnerUserID: env.author.ID, Title: "已关房", IsPublic: true, ClosedAt: &closed},
		{RoomID: "OTHER00001", OwnerUserID: env.other.ID, Title: "别人的房", IsPublic: true},
	}
	for _, m := range metas {
		if err := env.st.UpsertRoomMeta(t.Context(), m); err != nil {
			t.Fatalf("UpsertRoomMeta(%s): %v", m.RoomID, err)
		}
	}
	return env
}

// myRoomItemResp 是测试侧**独立**的响应形状。
//
// 刻意不复用 myrooms.go 里的 myRoomItem：那样字段改名（或 JSON tag 写错）
// 会让测试跟着一起改，判据就退化成"代码和自己一致"。这里是一份独立的契约抄本。
type myRoomItemResp struct {
	RoomID   string `json:"roomId"`
	Title    string `json:"title"`
	IsPublic bool   `json:"isPublic"`
	IsClosed bool   `json:"isClosed"`
}

type myRoomListResp struct {
	Items   []myRoomItemResp `json:"items"`
	HasMore bool             `json:"hasMore"`
}

// TestMyRoomsRequiresAuth 钉住匿名 401。
func TestMyRoomsRequiresAuth(t *testing.T) {
	env := myRoomsEnv(t)
	w := env.do(t, http.MethodGet, "/api/rooms/mine", "", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("匿名读我的放映室应当 401，实得 %d：%s", w.Code, w.Body.String())
	}
}

// TestMyRoomsIncludesAllStates 是这张票的核心判据：未公开与已关闭的房也要在。
func TestMyRoomsIncludesAllStates(t *testing.T) {
	env := myRoomsEnv(t)

	w := env.do(t, http.MethodGet, "/api/rooms/mine", env.tokAu, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("读我的放映室失败：%d %s", w.Code, w.Body.String())
	}
	var list myRoomListResp
	mustDecode(t, w, &list)

	got := map[string]myRoomItemResp{}
	for _, it := range list.Items {
		got[it.RoomID] = it
	}
	if len(got) != 3 {
		t.Fatalf("应当看到自己的全部三间房，实得 %v", got)
	}
	for _, id := range []string{"MYOpen0001", "MYPriv0001", "MYClos0001"} {
		if _, ok := got[id]; !ok {
			t.Fatalf("缺少 %s，实得 %v", id, got)
		}
	}
	if _, ok := got["OTHER00001"]; ok {
		t.Fatalf("不该看到别人的房")
	}
	// 状态要能区分出来，否则前端画不出「私密 / 已关闭」的标记
	if got["MYPriv0001"].IsPublic {
		t.Fatalf("私密房的 isPublic 应当 false")
	}
	if !got["MYClos0001"].IsClosed {
		t.Fatalf("已关房的 isClosed 应当 true")
	}
}

// TestMyRoomsEmptyIsArray 钉住空结果是 [] 而不是 null。
func TestMyRoomsEmptyIsArray(t *testing.T) {
	env := newAnimeThreadEnv(t)

	w := env.do(t, http.MethodGet, "/api/rooms/mine", env.tokOt, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("读我的放映室失败：%d", w.Code)
	}
	var list myRoomListResp
	mustDecode(t, w, &list)
	if list.Items == nil {
		t.Fatalf("空列表必须是 [] 而不是 null")
	}
	if len(list.Items) != 0 {
		t.Fatalf("没有房的用户应当返回空，实得 %d", len(list.Items))
	}
}
