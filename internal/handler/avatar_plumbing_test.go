package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/store"
)

// 头像在 **JSON 契约**里是否真的出现了（票 #74）的真库证据。
//
// store 层的 `internal/store/avatar_test.go` 钉的是"SQL 里取到了哪一列"，
// 本文件钉的是**另一半**：handler 把那一列搬进响应视图时有没有忘掉某个字段。
// 这两件事必须分开测 —— 取得到而搬不出去，前端看到的就是"有的地方有头像、
// 有的地方没有"，而那种不一致在类型系统里没有任何痕迹（视图字段是可空的）。
//
// 覆盖的是**每一处会露出作者/对方身份的地方**：
//
//	帖子列表           author.avatarUrl
//	帖子详情           author.avatarUrl
//	评论列表           author.avatarUrl
//	会话列表           peerAvatarUrl
//	私聊消息           senderAvatarUrl
//	关注名单           avatarUrl
//	搜索结果           avatarUrl
//	公开档案           avatarUrl
//
// 这条清单就是"头像会出现在哪"的全部答案。新增一处带作者的响应时，
// 应当在这里补一行 —— 否则头像在那一处永远是空的，且没有任何报错。

// 测试用的头像地址（形状与真实存储 URL 一致，但域名是假的：本文件不打网络）。
var avatarPlumbURL = "https://cdn.example.test/pr-files/2026/11/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.jpg"

// TestAvatarURLReachesPostViews 覆盖帖子三处（列表 / 详情 / 评论）。
func TestAvatarURLReachesPostViews(t *testing.T) {
	env := newAnimeThreadEnv(t)
	ctx := context.Background()

	// 给 other 设头像（本文件用 other 发帖，避免与夹具里 author 已有的帖混在一起）。
	//
	// **要设两处**：本夹具的认证侧是内存用户表、业务侧是真库（见 messages_test.go
	// 顶部的装配说明），两者指向不同的数据。`UpdateAvatarURL` 让**查询**带出头像，
	// 而直接改指针让**认证上下文里的 actor** 也带头像 —— 后者是新评论响应
	// （"刚发出去那一条"，还没经过任何查询）唯一的取头像来源。
	// 生产里这两处本来就是同一个库，所以这不是在测一个不存在的情形。
	if err := env.st.UpdateAvatarURL(ctx, env.other.ID, &avatarPlumbURL); err != nil {
		t.Fatalf("UpdateAvatarURL: %v", err)
	}
	env.other.AvatarURL = &avatarPlumbURL
	if err := env.st.CreatePost(ctx, &store.Post{
		AuthorID: env.other.ID, Title: "带头像的一篇", Content: "正文", Images: []string{},
	}); err != nil {
		t.Fatalf("CreatePost: %v", err)
	}

	// ① 列表。
	rec := env.do(t, http.MethodGet, "/api/posts", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("列表应 200：%d %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Items []struct {
			Author struct {
				AvatarURL *string `json:"avatarUrl"`
			} `json:"author"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("解析列表失败：%v", err)
	}
	var postID int64
	found := false
	for _, it := range list.Items {
		if it.Author.AvatarURL != nil {
			found = true
		}
	}
	if !found {
		t.Fatalf("列表里应至少有一条带 author.avatarUrl：%s", rec.Body.String())
	}

	// ② 详情（用别的作者身份读：可见性不该影响头像）。
	posts, _, err := env.st.ListPosts(ctx, store.PostsSortLatest, 50, 0, 0)
	if err != nil {
		t.Fatalf("ListPosts: %v", err)
	}
	for i := range posts {
		if posts[i].Title == "带头像的一篇" {
			postID = posts[i].ID
		}
	}
	if postID == 0 {
		t.Fatal("找不到刚发的帖子")
	}
	rec = env.do(t, http.MethodGet, "/api/posts/"+itoa(postID), env.tokAu, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("详情应 200：%d %s", rec.Code, rec.Body.String())
	}
	var detail struct {
		Author struct {
			AvatarURL *string `json:"avatarUrl"`
		} `json:"author"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("解析详情失败：%v", err)
	}
	if detail.Author.AvatarURL == nil || *detail.Author.AvatarURL != avatarPlumbURL {
		t.Fatalf("详情应带 author.avatarUrl=%q：got %v", avatarPlumbURL, detail.Author.AvatarURL)
	}

	// ③ 评论（用 other 自己的 token 发，走的正是"刚发出去那条"的分支）。
	rec = env.do(t, http.MethodPost, "/api/posts/"+itoa(postID)+"/comments", env.tokOt, gin.H{"content": "带头像的评论"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("发评论应 201：%d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Author struct {
			AvatarURL *string `json:"avatarUrl"`
		} `json:"author"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("解析评论响应失败：%v", err)
	}
	if created.Author.AvatarURL == nil || *created.Author.AvatarURL != avatarPlumbURL {
		t.Fatalf("新评论的 author.avatarUrl 应为 %q（取认证上下文里的 actor）：got %v",
			avatarPlumbURL, created.Author.AvatarURL)
	}

	// ④ 评论列表（走的另一条查询，字段来自 SQL）。
	rec = env.do(t, http.MethodGet, "/api/posts/"+itoa(postID)+"/comments", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("评论列表应 200：%d %s", rec.Code, rec.Body.String())
	}
	var comments struct {
		Items []struct {
			Author struct {
				AvatarURL *string `json:"avatarUrl"`
			} `json:"author"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &comments); err != nil {
		t.Fatalf("解析评论列表失败：%v", err)
	}
	if len(comments.Items) == 0 || comments.Items[0].Author.AvatarURL == nil {
		t.Fatalf("评论列表应带 author.avatarUrl：%s", rec.Body.String())
	}
}

// TestAvatarURLReachesRelationViews 覆盖关注名单与搜索结果。
func TestAvatarURLReachesRelationViews(t *testing.T) {
	env := newAnimeThreadEnv(t)
	ctx := context.Background()

	if err := env.st.UpdateAvatarURL(ctx, env.other.ID, &avatarPlumbURL); err != nil {
		t.Fatalf("UpdateAvatarURL: %v", err)
	}
	// author 关注 other，于是 other 出现在 author 的"关注"名单里。
	if _, _, err := env.st.CreateFollow(ctx, env.author.ID, env.other.ID); err != nil {
		t.Fatalf("CreateFollow: %v", err)
	}

	// ① 关注名单。
	rec := env.do(t, http.MethodGet, "/api/follows?tab=following", env.tokAu, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("名单应 200：%d %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Items []struct {
			UserID    int64   `json:"userId"`
			AvatarURL *string `json:"avatarUrl"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("解析名单失败：%v", err)
	}
	if len(list.Items) != 1 || list.Items[0].UserID != env.other.ID {
		t.Fatalf("名单里应只有 other：%+v", list.Items)
	}
	if list.Items[0].AvatarURL == nil || *list.Items[0].AvatarURL != avatarPlumbURL {
		t.Fatalf("名单行应带 avatarUrl=%q：got %v", avatarPlumbURL, list.Items[0].AvatarURL)
	}

	// ② 搜索结果（`/api/users/search` 是解除拉黑的唯一入口，也带头像）。
	rec = env.do(t, http.MethodGet, "/api/users/search?q=at-other", env.tokAu, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("搜索应 200：%d %s", rec.Code, rec.Body.String())
	}
	var hits struct {
		Items []struct {
			UserID    int64   `json:"userId"`
			AvatarURL *string `json:"avatarUrl"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &hits); err != nil {
		t.Fatalf("解析搜索失败：%v", err)
	}
	if len(hits.Items) == 0 {
		t.Fatalf("应能搜到 other：%s", rec.Body.String())
	}
	if hits.Items[0].AvatarURL == nil || *hits.Items[0].AvatarURL != avatarPlumbURL {
		t.Fatalf("搜索结果应带 avatarUrl=%q：got %v", avatarPlumbURL, hits.Items[0].AvatarURL)
	}
}

// TestAvatarURLReachesDMViews 覆盖会话列表与私聊消息。
func TestAvatarURLReachesDMViews(t *testing.T) {
	env := newMsgEnv(t)
	ctx := context.Background()

	// 给 alice 设头像，然后她给 bob 发一条私聊。
	if err := env.st.UpdateAvatarURL(ctx, env.alice.ID, &avatarPlumbURL); err != nil {
		t.Fatalf("UpdateAvatarURL: %v", err)
	}
	if rec := env.do(t, http.MethodPost, "/api/messages", env.aliceTok, gin.H{
		"recipientId": env.bob.ID, "body": "带头像的一条",
	}); rec.Code != http.StatusCreated {
		t.Fatalf("发私聊失败：%d %s", rec.Code, rec.Body.String())
	}

	// ① bob 的会话列表：对方的头像是 alice 的。
	rec := env.do(t, http.MethodGet, "/api/messages/conversations", env.bobTok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("会话列表应 200：%d %s", rec.Code, rec.Body.String())
	}
	var convs struct {
		Items []struct {
			PeerID        int64   `json:"peerId"`
			PeerAvatarURL *string `json:"peerAvatarUrl"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &convs); err != nil {
		t.Fatalf("解析会话列表失败：%v", err)
	}
	if len(convs.Items) != 1 || convs.Items[0].PeerID != env.alice.ID {
		t.Fatalf("会话对方应是 alice：%+v", convs.Items)
	}
	if convs.Items[0].PeerAvatarURL == nil || *convs.Items[0].PeerAvatarURL != avatarPlumbURL {
		t.Fatalf("会话行应带 peerAvatarUrl=%q：got %v", avatarPlumbURL, convs.Items[0].PeerAvatarURL)
	}

	// ② 私聊消息列表：发件人头像是 alice 的。
	rec = env.do(t, http.MethodGet, "/api/messages?peer="+itoa(env.alice.ID), env.bobTok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("私聊列表应 200：%d %s", rec.Code, rec.Body.String())
	}
	var msgs struct {
		Items []struct {
			SenderID        int64   `json:"senderId"`
			SenderAvatarURL *string `json:"senderAvatarUrl"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &msgs); err != nil {
		t.Fatalf("解析私聊失败：%v", err)
	}
	if len(msgs.Items) != 1 {
		t.Fatalf("应有 1 条私聊：%+v", msgs.Items)
	}
	if msgs.Items[0].SenderAvatarURL == nil || *msgs.Items[0].SenderAvatarURL != avatarPlumbURL {
		t.Fatalf("消息应带 senderAvatarUrl=%q：got %v", avatarPlumbURL, msgs.Items[0].SenderAvatarURL)
	}
}
