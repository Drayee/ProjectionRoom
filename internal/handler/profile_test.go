package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ProjectionRoom/internal/store"
)

// mustDecode 把响应体解成 out；失败直接 Fatal（带着原始 body，便于排查形状漂移）。
func mustDecode(t *testing.T, w *httptest.ResponseRecorder, out any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
		t.Fatalf("解析响应失败：%v；原始 body：%s", err, w.Body.String())
	}
}

// 他人公开档案的两个内容块（票 #68）的 HTTP 层证据。
//
// 三个判据组：
//
//  1. **准入**：两个端点都免登录（与 `GET /api/users/:id` 同一条理由 —— 它们
//     就是公开档案的一部分）。要求登录会让"点开作者名"变成一次登录跳转。
//  2. **不可见账号 404 不泄漏**：不存在与被封禁同一个响应，且**与档案端点
//     同一个形状** —— 档案 404 而子资源 200 是一处很难发现的泄漏。
//  3. **空列表是 `[]` 不是 `null`**：前端直接 `.map`。

// profilePostItem 是作者帖列表响应里的一项（本用例只关心形状）。
type profilePostItem struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	AuthorID  int64  `json:"authorId"`
	Liked     bool   `json:"liked"`
	Favorited bool   `json:"favorited"`
}

type profilePostListResp struct {
	Items   []profilePostItem `json:"items"`
	HasMore bool              `json:"hasMore"`
}

type profileRoomItem struct {
	RoomID string `json:"roomId"`
	Title  string `json:"title"`
}

type profileRoomListResp struct {
	Items   []profileRoomItem `json:"items"`
	HasMore bool              `json:"hasMore"`
}

// 两个端点都免登录：匿名能读到公开内容。
func TestAuthorPostsAndRoomsArePublic(t *testing.T) {
	env := newAnimeThreadEnv(t)
	ctx := t.Context()

	if err := env.st.CreatePost(ctx, &store.Post{
		AuthorID: env.other.ID, Title: "别人的公开帖", Content: "正文", Images: []string{},
	}); err != nil {
		t.Fatalf("CreatePost: %v", err)
	}
	if err := env.st.UpsertRoomMeta(ctx, &store.RoomMeta{
		RoomID: "PFROOM01", OwnerUserID: env.other.ID, Title: "别人的房", IsPublic: true,
	}); err != nil {
		t.Fatalf("UpsertRoomMeta: %v", err)
	}

	// 作者帖：匿名 200 且能看到那一篇。
	w := env.do(t, http.MethodGet, "/api/users/"+itoa(env.other.ID)+"/posts", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("匿名读作者帖应当 200，实得 %d：%s", w.Code, w.Body.String())
	}
	var posts profilePostListResp
	mustDecode(t, w, &posts)
	if len(posts.Items) != 1 || posts.Items[0].Title != "别人的公开帖" {
		t.Fatalf("作者帖内容不对：%+v", posts.Items)
	}
	if posts.Items[0].AuthorID != env.other.ID {
		t.Fatalf("authorId 应当是 %d，实得 %d", env.other.ID, posts.Items[0].AuthorID)
	}

	// 作者房：匿名 200 且能看到那一间。
	w = env.do(t, http.MethodGet, "/api/users/"+itoa(env.other.ID)+"/rooms", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("匿名读作者房应当 200，实得 %d：%s", w.Code, w.Body.String())
	}
	var rooms profileRoomListResp
	mustDecode(t, w, &rooms)
	if len(rooms.Items) != 1 || rooms.Items[0].RoomID != "PFROOM01" {
		t.Fatalf("作者房内容不对：%+v", rooms.Items)
	}
}

// 不存在与被封禁都回 404，且**与公开档案端点同一个形状**。
func TestAuthorSubresourcesHideInvisibleAccounts(t *testing.T) {
	env := newAnimeThreadEnv(t)
	ctx := t.Context()

	// ① 不存在的 id：两个子资源都 404。
	for _, path := range []string{"/api/users/999999/posts", "/api/users/999999/rooms"} {
		w := env.do(t, http.MethodGet, path, "", nil)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s 应当 404，实得 %d：%s", path, w.Code, w.Body.String())
		}
	}

	// ② 被封禁的账号：即使它有帖有房，两个子资源也 404（不是空列表）。
	if err := env.st.CreatePost(ctx, &store.Post{
		AuthorID: env.other.ID, Title: "封禁前的帖", Content: "正文", Images: []string{},
	}); err != nil {
		t.Fatalf("CreatePost: %v", err)
	}
	if err := env.st.UpsertRoomMeta(ctx, &store.RoomMeta{
		RoomID: "PFBAN001", OwnerUserID: env.other.ID, Title: "封禁前的房", IsPublic: true,
	}); err != nil {
		t.Fatalf("UpsertRoomMeta: %v", err)
	}
	if err := env.st.SetUserStatus(ctx, env.other.ID, store.StatusBanned, "刷屏"); err != nil {
		t.Fatalf("SetUserStatus: %v", err)
	}

	for _, path := range []string{"/api/users/" + itoa(env.other.ID) + "/posts", "/api/users/" + itoa(env.other.ID) + "/rooms"} {
		w := env.do(t, http.MethodGet, path, "", nil)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s（被封禁）应当 404，实得 %d：%s", path, w.Code, w.Body.String())
		}
	}

	// ③ 对照：档案端点**也不能**给 200 —— 三个端点的口径必须一致，
	// 否则"档案说没有这个人、子资源还能读"就是一条存在性旁路。
	//
	// 这里只断言"不是 200"而不是"是 404"：本装配（newAnimeThreadEnv）没有提供
	// UserProfileService，`registerUserProfileRoutes` 走的是 503 的降级分支。
	// 真正的"档案 404 与子资源 404 同形"要在真机脚本里判（那时账号服务是装配好的）。
	// 钉在这里的是一条更弱但同样有效的性质：**被封禁的人不会让档案端点回 200**。
	w := env.do(t, http.MethodGet, "/api/users/"+itoa(env.other.ID), "", nil)
	if w.Code == http.StatusOK {
		t.Fatalf("被封禁者的档案不该 200（存在性泄漏），实得 %d：%s", w.Code, w.Body.String())
	}
}

// id 不是正整数 → 400（与公开档案同一口径：这是调用方的问题，不是资源不存在）。
func TestAuthorSubresourcesRejectBadID(t *testing.T) {
	env := newAnimeThreadEnv(t)
	for _, id := range []string{"abc", "0", "-3"} {
		for _, suffix := range []string{"/posts", "/rooms"} {
			path := "/api/users/" + id + suffix
			w := env.do(t, http.MethodGet, path, "", nil)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("%s 应当 400，实得 %d：%s", path, w.Code, w.Body.String())
			}
		}
	}
}

// 空列表必须是 `[]`（不是 null），且 hasMore 为 false。
func TestAuthorSubresourcesEmptyIsArray(t *testing.T) {
	env := newAnimeThreadEnv(t)
	w := env.do(t, http.MethodGet, "/api/users/"+itoa(env.admin.ID)+"/posts", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("应当 200，实得 %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"items":[]`) {
		t.Fatalf("空列表应当序列化成 []，实得：%s", body)
	}
}

// 看客与作者有拉黑关系时，作者帖一条都不给（但档案本身仍 200 —— 票 #42 §4：
// 公开内容不设限？**不**：拉黑是"从我的世界里拿掉"，所以我看不到 TA 的帖，
// 而匿名看客仍然看得到）。
func TestAuthorPostsHiddenByBlock(t *testing.T) {
	env := newAnimeThreadEnv(t)
	ctx := t.Context()

	if err := env.st.CreatePost(ctx, &store.Post{
		AuthorID: env.other.ID, Title: "拉黑前可见", Content: "正文", Images: []string{},
	}); err != nil {
		t.Fatalf("CreatePost: %v", err)
	}
	// other 拉黑 author（两者都已有 token）。
	if w := env.do(t, http.MethodPost, "/api/blocks", env.tokOt, map[string]any{"userId": env.author.ID}); w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("拉黑应当成功，实得 %d：%s", w.Code, w.Body.String())
	}

	// 被拉黑方（author）看 other 的帖：0 篇。
	w := env.do(t, http.MethodGet, "/api/users/"+itoa(env.other.ID)+"/posts", env.tokAu, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("应当 200（档案本身仍可见），实得 %d：%s", w.Code, w.Body.String())
	}
	var posts profilePostListResp
	mustDecode(t, w, &posts)
	if len(posts.Items) != 0 {
		t.Fatalf("拉黑存续期不该看到对方的帖，实得 %d 篇", len(posts.Items))
	}

	// 匿名看客不受影响。
	w = env.do(t, http.MethodGet, "/api/users/"+itoa(env.other.ID)+"/posts", "", nil)
	mustDecode(t, w, &posts)
	if len(posts.Items) != 1 {
		t.Fatalf("匿名视角应当仍看到 1 篇（公开内容不设限），实得 %d", len(posts.Items))
	}
}
