package handler

import (
	"encoding/json"
	"net/http"
	"testing"

	"ProjectionRoom/internal/store"
)

// 关注流（票 #66）的 handler 层证据。
//
// 这一层只该证明一件事：**匿名请求 `?sort=following` 被明确拒绝**。
// 排序与过滤是 store 的事（见 internal/store/flow_test.go），handler 的责任是
// 把"没有看客"翻译成一个客户端能分诊的状态码 —— 而不是悄悄返回空列表。
//
// 为什么必须是 401 而不是 400：
// 400 的意思是"你的参数写错了"，用户看到会去改 URL；而这里参数是对的，
// 缺的是登录态。前端的 authedFetch 对 401 有一条既定的分诊路径（刷新 token
// 或弹登录），400 则会走"参数错误"的提示 —— 两者对用户是完全不同的话。

// TestFollowingFlowRejectsAnonymous 钉住匿名请求关注流 → 401。
func TestFollowingFlowRejectsAnonymous(t *testing.T) {
	env := newAnimeThreadEnv(t)

	for _, path := range []string{
		"/api/posts?sort=following",
		"/api/posts?sort=following&limit=5",
		"/api/posts?sort=FOLLOWING", // 大小写不敏感（与其他 sort 值同口径）
	} {
		rec := env.do(t, http.MethodGet, path, "", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s 匿名应当 401，实际 %d：%s", path, rec.Code, rec.Body.String())
		}
		var body struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s 响应不是 JSON：%v", path, err)
		}
		if body.Error == "" {
			t.Fatalf("%s 的 401 必须带一句人话（明确失败不是静默）", path)
		}
	}
}

// TestFollowingFlowAcceptsLoggedIn 是上面那条的对照：登录后同一个 URL 必须是 200。
//
// 没有这条，一个"永远 401"的实现也能让上面那条通过。
func TestFollowingFlowAcceptsLoggedIn(t *testing.T) {
	env := newAnimeThreadEnv(t)
	ctx := t.Context()

	// 让 author 关注 other，这样关注流里至少有一篇（other 发一篇帖）。
	if err := env.st.CreatePost(ctx, &store.Post{
		AuthorID: env.other.ID, Title: "第三方的一篇", Content: "正文", Images: []string{},
	}); err != nil {
		t.Fatalf("CreatePost: %v", err)
	}
	if _, _, err := env.st.CreateFollow(ctx, env.author.ID, env.other.ID); err != nil {
		t.Fatalf("CreateFollow: %v", err)
	}

	rec := env.do(t, http.MethodGet, "/api/posts?sort=following", env.tokAu, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("登录后关注流应当 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []struct {
			AuthorID int64 `json:"authorId"`
		} `json:"items"`
		HasMore bool `json:"hasMore"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是 JSON：%v", err)
	}
	if len(body.Items) == 0 {
		t.Fatalf("关注流里应当有 other 的那一篇")
	}
	// author 只关注了 other，所以流里只该有 other 的帖子。
	for _, it := range body.Items {
		if it.AuthorID != env.other.ID {
			t.Fatalf("关注流里出现了没关注的人：author=%d（应当只有 other=%d）", it.AuthorID, env.other.ID)
		}
	}
}

// TestPostSortStillRejectsUnknown 钉住"未知 sort 仍然是 400"。
//
// 扩展枚举时最容易顺手把 default 分支放宽成"当 latest 处理"——
// 那会让 `?sort=folowing`（拼错）静默变成最新列表，用户以为自己看的是关注流。
func TestPostSortStillRejectsUnknown(t *testing.T) {
	env := newAnimeThreadEnv(t)

	for _, path := range []string{
		"/api/posts?sort=folowing", // 少一个 l
		"/api/posts?sort=bogus",
		"/api/posts?sort=following2",
	} {
		rec := env.do(t, http.MethodGet, path, env.tokAu, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s 应当 400，实际 %d：%s", path, rec.Code, rec.Body.String())
		}
	}
}
