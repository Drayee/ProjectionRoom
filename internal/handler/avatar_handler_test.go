package handler

// 头像覆盖层（决议 #54 / 票 #74）的 HTTP 面。
//
// 本文件只判**协议面**：三态（不传 / null / 有值）、错误码、以及"外链被拒"
// 这条安全边界。领域规则（旧对象异步删、幂等、空串归一）在
// service/avatar_test.go 里判 —— 那里能用假后端观察"删了几次"，
// HTTP 层看不到那些副作用。

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/service"
	"ProjectionRoom/internal/service/auth"
	"ProjectionRoom/internal/store"
)

// fakeAvatarBackend 是 handler 侧的头像后端替身（admin_test.go 的 newAdminEnv
// 默认注入了它，于是"改头像"这条路径在 handler 用例里是通的）。
//
// OwnsURL 用前缀判而不做完整形态校验：形态校验由 storage 包的单测
// （minio_test.go）逐变体钉住，这里要判的是**服务层有没有把请求交过来**。
type fakeAvatarBackend struct {
	prefix string
}

func newFakeAvatarBackend() *fakeAvatarBackend {
	return &fakeAvatarBackend{prefix: "https://cdn.example.test/pr-files/"}
}

func (b *fakeAvatarBackend) OwnsURL(u string) bool {
	return strings.HasPrefix(u, b.prefix)
}

func (b *fakeAvatarBackend) Remove(_ context.Context, _ string) error {
	return nil
}

// avatarHandlerURL 造一个本桶的合法头像 URL。
func avatarHandlerURL(n string) string {
	return "https://cdn.example.test/pr-files/2026/11/" + strings.Repeat(n, 32) + ".jpg"
}

// TestPatchMeSetsAvatarURL 是正常路径：`{"avatarUrl": "..."}` → 200 且档案带它。
func TestPatchMeSetsAvatarURL(t *testing.T) {
	env := newAdminEnv(t, nil)
	url := avatarHandlerURL("a")

	payload, _ := json.Marshal(map[string]any{"avatarUrl": url})
	w := doJSON(t, env.engine, http.MethodPatch, "/api/me", string(payload), env.userToken)
	if w.Code != http.StatusOK {
		t.Fatalf("设头像应 200：got %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), url) {
		t.Errorf("响应应带新头像：%s", w.Body.String())
	}

	after, ok := env.st.user(env.user.ID)
	if !ok {
		t.Fatalf("用户 %d 应当还在", env.user.ID)
	}
	if after.AvatarURL == nil || *after.AvatarURL != url {
		t.Errorf("库里应已落值：got %v", after.AvatarURL)
	}

	// 公开档案也要带 —— 否则别人看不到头像，只有自己看得到。
	pub := doJSON(t, env.engine, http.MethodGet, "/api/users/"+strconv.FormatInt(env.user.ID, 10), "", "")
	if !strings.Contains(pub.Body.String(), url) {
		t.Errorf("公开档案应带头像：%s", pub.Body.String())
	}
}

// TestPatchMeClearsAvatarURLWithNull 是"null = 清除"这一态。
//
// 与"不传字段"必须分开：前端清空头像时发的是 `{"avatarUrl": null}`，
// 而只改昵称时**不**该动头像。把两者混成一种会让"改昵称顺手清掉头像"。
func TestPatchMeClearsAvatarURLWithNull(t *testing.T) {
	env := newAdminEnv(t, nil)
	url := avatarHandlerURL("b")

	// 先设上。
	payload, _ := json.Marshal(map[string]any{"avatarUrl": url})
	if w := doJSON(t, env.engine, http.MethodPatch, "/api/me", string(payload), env.userToken); w.Code != http.StatusOK {
		t.Fatalf("设头像应 200：got %d %s", w.Code, w.Body.String())
	}

	// 再用 null 清掉。
	w := doJSON(t, env.engine, http.MethodPatch, "/api/me", `{"avatarUrl":null}`, env.userToken)
	if w.Code != http.StatusOK {
		t.Fatalf("清除头像应 200：got %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "avatarUrl") {
		t.Errorf("清除后响应里不该再有 avatarUrl：%s", w.Body.String())
	}

	after, ok := env.st.user(env.user.ID)
	if !ok {
		t.Fatalf("用户 %d 应当还在", env.user.ID)
	}
	if after.AvatarURL != nil {
		t.Errorf("库里应已置空：got %q", *after.AvatarURL)
	}
}

// TestPatchMeDisplayNameOnlyKeepsAvatar 是"不传字段 = 不改"这一态。
//
// 这条用例是那个三态设计的**存在理由**：只改昵称的请求里没有 avatarUrl 键，
// 头像必须原封不动。若实现用"指针为 nil 就当作清除"，这条立刻红。
func TestPatchMeDisplayNameOnlyKeepsAvatar(t *testing.T) {
	env := newAdminEnv(t, nil)
	url := avatarHandlerURL("c")

	payload, _ := json.Marshal(map[string]any{"avatarUrl": url})
	if w := doJSON(t, env.engine, http.MethodPatch, "/api/me", string(payload), env.userToken); w.Code != http.StatusOK {
		t.Fatalf("设头像应 200：got %d %s", w.Code, w.Body.String())
	}

	w := doJSON(t, env.engine, http.MethodPatch, "/api/me", `{"displayName":"只改名字"}`, env.userToken)
	if w.Code != http.StatusOK {
		t.Fatalf("改昵称应 200：got %d %s", w.Code, w.Body.String())
	}

	after, ok := env.st.user(env.user.ID)
	if !ok {
		t.Fatalf("用户 %d 应当还在", env.user.ID)
	}
	if after.AvatarURL == nil || *after.AvatarURL != url {
		t.Fatalf("只改昵称不该动头像：got %v want %q", after.AvatarURL, url)
	}
	if after.DisplayName != "只改名字" {
		t.Errorf("昵称应已更新：got %q", after.DisplayName)
	}
}

// TestPatchMeRejectsForeignAvatarURL 是安全核心：外链 → 400 + 专用码。
//
// 决议 #54 §8 明确否决"外链直填"。若这里放行，攻击者可以把自己的头像设成
// 任意第三方地址 —— 每一个看到他的人都会向那个地址发一次 GET（带 Referer 与
// 真实 IP）。那不是头像功能，是无痕追踪像素。
func TestPatchMeRejectsForeignAvatarURL(t *testing.T) {
	env := newAdminEnv(t, nil)

	for _, bad := range []string{
		"https://evil.example.com/pr-files/2026/11/" + strings.Repeat("a", 32) + ".jpg",
		"http://cdn.example.test/pr-files/2026/11/" + strings.Repeat("a", 32) + ".jpg",
		"//cdn.example.test/pr-files/2026/11/" + strings.Repeat("a", 32) + ".jpg",
		"javascript:alert(1)",
		"data:image/png;base64,AAAA",
	} {
		payload, _ := json.Marshal(map[string]any{"avatarUrl": bad})
		w := doJSON(t, env.engine, http.MethodPatch, "/api/me", string(payload), env.userToken)
		if w.Code != http.StatusBadRequest {
			t.Errorf("外链应 400（%q）：got %d %s", bad, w.Code, w.Body.String())
			continue
		}
		if !strings.Contains(w.Body.String(), CodeAvatarURLInvalid) {
			t.Errorf("错误码应是 %s：%s", CodeAvatarURLInvalid, w.Body.String())
		}
	}

	// 被拒之后库里必须还是空的。
	after, ok := env.st.user(env.user.ID)
	if !ok {
		t.Fatalf("用户 %d 应当还在", env.user.ID)
	}
	if after.AvatarURL != nil {
		t.Fatalf("被拒的请求不该改库：got %q", *after.AvatarURL)
	}
}

// TestPatchMeAvatarUnavailableWithoutStorage 钉住"存储没配 → 503 + AUTH_UNAVAILABLE"。
//
// 决议 #54 §9 要求前端据此显示 CapabilityNotice（而不是报错重试）。
// 这个 env 刻意**不**注入 Avatar 后端。
func TestPatchMeAvatarUnavailableWithoutStorage(t *testing.T) {
	env := newAdminEnvWithoutAvatar(t)
	url := avatarHandlerURL("d")

	payload, _ := json.Marshal(map[string]any{"avatarUrl": url})
	w := doJSON(t, env.engine, http.MethodPatch, "/api/me", string(payload), env.userToken)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("未配置对象存储应 503：got %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), CodeAuthUnavailable) {
		t.Errorf("错误码应是 %s：%s", CodeAuthUnavailable, w.Body.String())
	}

	after, ok := env.st.user(env.user.ID)
	if !ok {
		t.Fatalf("用户 %d 应当还在", env.user.ID)
	}
	if after.AvatarURL != nil {
		t.Fatalf("不可用时不该写库：got %q", *after.AvatarURL)
	}
}

// TestPatchMeRejectsNeitherField 钉住"两个字段都没有 → 400"。
func TestPatchMeRejectsNeitherField(t *testing.T) {
	env := newAdminEnv(t, nil)

	w := doJSON(t, env.engine, http.MethodPatch, "/api/me", `{}`, env.userToken)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("空 body 应 400：got %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "avatarUrl") {
		t.Errorf("400 的文案应点名两个字段：%s", w.Body.String())
	}
}

// TestPatchMeBothFieldsAtOnce 钉住"一次请求改两样"。
func TestPatchMeBothFieldsAtOnce(t *testing.T) {
	env := newAdminEnv(t, nil)
	url := avatarHandlerURL("e")

	payload, _ := json.Marshal(map[string]any{"displayName": "双改", "avatarUrl": url})
	w := doJSON(t, env.engine, http.MethodPatch, "/api/me", string(payload), env.userToken)
	if w.Code != http.StatusOK {
		t.Fatalf("同时改两样应 200：got %d %s", w.Code, w.Body.String())
	}

	after, ok := env.st.user(env.user.ID)
	if !ok {
		t.Fatalf("用户 %d 应当还在", env.user.ID)
	}
	if after.DisplayName != "双改" {
		t.Errorf("昵称应已更新：got %q", after.DisplayName)
	}
	if after.AvatarURL == nil || *after.AvatarURL != url {
		t.Errorf("头像应已更新：got %v", after.AvatarURL)
	}
}

// TestPatchMeAvatarNeedsAuth 钉住"未登录不能改头像"（与改昵称同一条门）。
func TestPatchMeAvatarNeedsAuth(t *testing.T) {
	env := newAdminEnv(t, nil)
	payload, _ := json.Marshal(map[string]any{"avatarUrl": avatarHandlerURL("f")})

	w := doJSON(t, env.engine, http.MethodPatch, "/api/me", string(payload), "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("未登录改头像应 401：got %d %s", w.Code, w.Body.String())
	}
}

// newAdminEnvWithoutAvatar 与 newAdminEnv 只差一处：不注入头像后端。
//
// 为什么要单独一个构造函数而不是给 newAdminEnv 加参数：那个函数的调用点有
// 几十处，加参数会改动它们全部，而只有本文件的一条用例需要"没有存储"的世界。
func newAdminEnvWithoutAvatar(t *testing.T) *adminEnv {
	t.Helper()

	cfg := config.Default()
	cfg.Segment.TempDir = t.TempDir()
	cfg.Static.Serve = false
	cfg.Auth.DBDSN = "host=127.0.0.1 dbname=fixture sslmode=disable"
	cfg.Auth.PublicBaseURL = "https://pr.test"

	signer, err := auth.NewSigner(handlerTestSecret, 15*time.Minute)
	if err != nil {
		t.Fatalf("构造 Signer 失败: %v", err)
	}
	st := newFakeAdminStore()
	user := st.seed("alice", store.RoleUser, store.StatusActive)

	svc, err := service.NewAccountService(st, signer, auth.NewTicketStore(cfg.Auth.WSTicketTTL), service.AccountOptions{
		RefreshTTL:     cfg.Auth.RefreshTTL,
		EmailVerifyTTL: cfg.Auth.EmailVerifyTTL,
		OTPTTL:         cfg.Auth.OTPTTL,
		PublicBaseURL:  cfg.Auth.PublicBaseURL,
		DeletionGrace:  cfg.Auth.DeletionGrace,
		// 关键：**不**给 Avatar。
	})
	if err != nil {
		t.Fatalf("构造 AccountService 失败: %v", err)
	}

	hub, cleanup, err := service.NewHub(cfg)
	if err != nil {
		t.Fatalf("构造 Hub 失败: %v", err)
	}
	rooms := service.NewManager(cfg, hub)
	engine := NewRouter(cfg, hub, rooms, nil, nil,
		AuthDeps{
			Service:     svc,
			UserProfile: svc,
			Session: SessionConfig{
				AccessTTL:   cfg.Auth.AccessTTL,
				RefreshTTL:  cfg.Auth.RefreshTTL,
				WSTicketTTL: cfg.Auth.WSTicketTTL,
			},
			ProfileNames: st,
		},
		AdminDeps{},
	)
	t.Cleanup(func() { rooms.Stop(); cleanup() })

	userTok, _, err := signer.Issue(user.ID, user.Role, user.TokenVersion)
	if err != nil {
		t.Fatalf("签发 token 失败: %v", err)
	}

	return &adminEnv{
		engine: engine, rooms: rooms, st: st, svc: svc,
		user: *user, userToken: userTok, cfg: cfg,
	}
}
