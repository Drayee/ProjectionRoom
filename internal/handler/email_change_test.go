package handler

// 自助换绑邮箱（票 #72）的 HTTP 层判据。
//
// 与 account_settings_test.go 的改密用例同一套理由：这里也用**真的**
// AccountService —— 换绑的核心判据是"验证码是发给哪个地址的、确认时认不认它"，
// 那全在 store/service 里（`ConsumeEmailOTPFor` 的 WHERE 过滤）。
// 用假实现只能验"我调用了那个方法"。
//
// 本文件刻意与改密分开：换绑有一件改密没有的事要钉住 —— 它与
// 管理员代办（`PATCH /api/admin/users/:id {email}`）**并存且互不干扰**。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sendEmailOTP 打一次发码端点，返回响应与（dev echo 下的）验证码。
func (e *settingsEnv) sendEmailOTP(t *testing.T, access, email, idemKey string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	body := `{"email":"` + email + `"}`
	w := e.doAuthed(t, http.MethodPost, "/api/me/email/otp", access, body, idemKey)
	var resp struct {
		Status  string `json:"status"`
		DevCode string `json:"devCode"`
	}
	// 解析失败不算错误（错误响应体形状不同），由调用方按状态码判。
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w, resp.DevCode
}

// confirmEmail 打一次确认端点。
func (e *settingsEnv) confirmEmail(t *testing.T, access, email, code, idemKey string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"email":"` + email + `","code":"` + code + `"}`
	return e.doAuthed(t, http.MethodPut, "/api/me/email", access, body, idemKey)
}

// doAuthed 发一个带 Bearer 与可选幂等键的请求。
//
// 为什么不复用 doJSON：那个不接受幂等键，而换绑的两个端点都要验幂等。
func (e *settingsEnv) doAuthed(t *testing.T, method, path, access, body, idemKey string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if access != "" {
		req.Header.Set("Authorization", "Bearer "+access)
	}
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	req.RemoteAddr = "203.0.113.7:12345"
	w := httptest.NewRecorder()
	e.engine.ServeHTTP(w, req)
	return w
}

func TestEmailChangeRequiresAuth(t *testing.T) {
	env := newSettingsEnvWith(t, true)

	if w, _ := env.sendEmailOTP(t, "", "new@example.com", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("匿名发换绑码应当 401，实际 %d（body=%s）", w.Code, w.Body.String())
	}
	if w := env.confirmEmail(t, "", "new@example.com", "123456", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("匿名确认换绑应当 401，实际 %d（body=%s）", w.Code, w.Body.String())
	}
}

// TestEmailChangeHappyPath 是主路径：发码 → 拿码 → 确认 → 邮箱与验证态都更新。
func TestEmailChangeHappyPath(t *testing.T) {
	env := newSettingsEnvWith(t, true)
	access, _ := env.register(t, "alice", "abcd1234")

	w, code := env.sendEmailOTP(t, access, "brand-new@example.com", "")
	if w.Code != http.StatusOK {
		t.Fatalf("发换绑码应当 200，实际 %d（body=%s）", w.Code, w.Body.String())
	}
	if code == "" {
		t.Fatalf("dev echo 下应当回显验证码，实际 body=%s", w.Body.String())
	}

	// 发码阶段邮箱还没变 —— 这一步只证明"我能读到新地址"。
	me := env.meWith(t, access)
	if !strings.Contains(me.Body.String(), "alice@example.com") {
		t.Fatalf("发码阶段不该改邮箱，/me 实际 %s", me.Body.String())
	}

	got := env.confirmEmail(t, access, "brand-new@example.com", code, "")
	if got.Code != http.StatusOK {
		t.Fatalf("确认换绑应当 200，实际 %d（body=%s）", got.Code, got.Body.String())
	}
	// 响应体必须带更新后的 Profile：前端要立刻撤掉"未验证"提示、
	// 把邮箱显示成新地址（等下一次 /me 会让界面中间那一帧还是旧的）。
	body := got.Body.String()
	if !strings.Contains(body, "brand-new@example.com") {
		t.Fatalf("响应体应当带新邮箱，实际 %s", body)
	}
	if !strings.Contains(body, `"emailVerified":true`) {
		t.Fatalf("响应体应当说明新邮箱已验证，实际 %s", body)
	}

	// 落库确认：/me 也是新地址。
	after := env.meWith(t, access)
	if !strings.Contains(after.Body.String(), "brand-new@example.com") {
		t.Fatalf("换绑后 /me 应当是新邮箱，实际 %s", after.Body.String())
	}
	if strings.Contains(after.Body.String(), "alice@example.com") {
		t.Fatalf("换绑后旧邮箱不该再出现，实际 %s", after.Body.String())
	}
}

// TestEmailChangeRejectsTakenAddress 新地址被别的账号占用 → 409 EMAIL_TAKEN。
//
// 必须当场说：用户这时还能换一个地址。等输完码才报错只会让他以为码发不出来。
func TestEmailChangeRejectsTakenAddress(t *testing.T) {
	env := newSettingsEnvWith(t, true)
	env.register(t, "alice", "abcd1234")
	env.register(t, "bob", "abcd1234") // 占住 bob@example.com
	access, _ := env.login(t, "alice", "abcd1234")

	w, _ := env.sendEmailOTP(t, access, "bob@example.com", "")
	if w.Code != http.StatusConflict {
		t.Fatalf("被占用的地址应当 409，实际 %d（body=%s）", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), CodeEmailTaken) {
		t.Fatalf("错误码应当是 %s，实际 %s", CodeEmailTaken, w.Body.String())
	}
}

// TestEmailChangeRejectsUnchanged 与当前邮箱相同 → 400（不是静默成功）。
//
// 静默成功会让用户去旧邮箱等一封永远不会来的信。
func TestEmailChangeRejectsUnchanged(t *testing.T) {
	env := newSettingsEnvWith(t, true)
	access, _ := env.register(t, "alice", "abcd1234")

	w, _ := env.sendEmailOTP(t, access, "alice@example.com", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("与当前邮箱相同应当 400，实际 %d（body=%s）", w.Code, w.Body.String())
	}
	// 大小写不同也算同一个地址（归一后相同）。
	w2, _ := env.sendEmailOTP(t, access, "ALICE@Example.COM", "")
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("大小写不同仍是同一个地址，应当 400，实际 %d（body=%s）", w2.Code, w2.Body.String())
	}
}

// TestEmailChangeRejectsMissingFields 少填一栏 → 400（不是 500）。
//
// 这条钉住的是 accountErrorResponse 里那个 errEmailChangeFieldsRequired 分支：
// 漏登记会落到 default 变 500，症状是"少填一栏"被显示成"服务端内部错误"。
func TestEmailChangeRejectsMissingFields(t *testing.T) {
	env := newSettingsEnvWith(t, true)
	access, _ := env.register(t, "alice", "abcd1234")

	cases := []struct {
		name, path, body string
		method           string
	}{
		{"发码缺 email", "/api/me/email/otp", `{}`, http.MethodPost},
		{"发码 email 全空白", "/api/me/email/otp", `{"email":"   "}`, http.MethodPost},
		{"确认缺 code", "/api/me/email", `{"email":"new@example.com"}`, http.MethodPut},
		{"确认缺 email", "/api/me/email", `{"code":"123456"}`, http.MethodPut},
		{"确认两栏都空", "/api/me/email", `{"email":"","code":""}`, http.MethodPut},
	}
	for _, tc := range cases {
		w := env.doAuthed(t, tc.method, tc.path, access, tc.body, "")
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s 应当 400，实际 %d（body=%s）", tc.name, w.Code, w.Body.String())
		}
	}
}

// TestEmailChangeRejectsWrongCode 错误码 → 400 OTP_INVALID，邮箱不变。
func TestEmailChangeRejectsWrongCode(t *testing.T) {
	env := newSettingsEnvWith(t, true)
	access, _ := env.register(t, "alice", "abcd1234")

	if w, _ := env.sendEmailOTP(t, access, "brand-new@example.com", ""); w.Code != http.StatusOK {
		t.Fatalf("发码应当 200，实际 %d（body=%s）", w.Code, w.Body.String())
	}
	got := env.confirmEmail(t, access, "brand-new@example.com", "000000", "")
	if got.Code != http.StatusBadRequest {
		t.Fatalf("错误码应当 400，实际 %d（body=%s）", got.Code, got.Body.String())
	}
	if !strings.Contains(got.Body.String(), CodeOTPInvalid) {
		t.Fatalf("错误码应当是 %s，实际 %s", CodeOTPInvalid, got.Body.String())
	}
	if me := env.meWith(t, access); !strings.Contains(me.Body.String(), "alice@example.com") {
		t.Fatalf("确认失败时邮箱不该变，/me 实际 %s", me.Body.String())
	}
}

// TestEmailChangeCodeIsBoundToAddress 是**本票最重要的安全判据**。
//
// 场景：账号先请求过一条「找回密码」码（发到**旧**地址），再请求换绑到新地址。
// 拿旧地址那条码去确认新地址必须失败 —— 否则"我能读到新邮箱"这件事
// 根本没被证明，攻击者只要让受害者收到任意一条码就能把账号绑到自己邮箱上，
// 随后走找回密码接管。
func TestEmailChangeCodeIsBoundToAddress(t *testing.T) {
	env := newSettingsEnvWith(t, true)
	access, _ := env.register(t, "alice", "abcd1234")

	// 发一条找回密码码（收件人 = 当前地址 alice@example.com）。
	pw := doJSON(t, env.engine, http.MethodPost, "/api/auth/password/otp",
		`{"email":"alice@example.com"}`, "")
	if pw.Code != http.StatusOK {
		t.Fatalf("发找回密码码应当 200，实际 %d（body=%s）", pw.Code, pw.Body.String())
	}
	var pwResp struct {
		DevCode string `json:"devCode"`
	}
	if err := json.Unmarshal(pw.Body.Bytes(), &pwResp); err != nil || pwResp.DevCode == "" {
		t.Fatalf("dev echo 下应当拿到找回密码的验证码，实际 body=%s", pw.Body.String())
	}

	// 把已落库的那条码往前拨 2 分钟，绕过与换绑共用的 60 秒冷却。
	//
	// 为什么不 `time.Sleep(61s)`：那会让整包用例多跑一分多钟，而这里要验的
	// 是"码绑定的是哪个地址"，与时钟无关。真 store 的冷却另有 store/service
	// 层用例覆盖，这里只需要让第二次发码能过去。
	env.store.mu.Lock()
	for _, r := range env.store.resets {
		r.CreatedAt = r.CreatedAt.Add(-2 * time.Minute)
	}
	env.store.mu.Unlock()

	// 再发一条换绑码到新地址。
	w, newCode := env.sendEmailOTP(t, access, "brand-new@example.com", "")
	if w.Code != http.StatusOK {
		t.Fatalf("发换绑码应当 200，实际 %d（body=%s）", w.Code, w.Body.String())
	}
	if newCode == "" {
		t.Fatalf("dev echo 下应当回显验证码，实际 body=%s", w.Body.String())
	}

	// 用**旧地址**那条码确认**新地址**：必须失败。
	got := env.confirmEmail(t, access, "brand-new@example.com", pwResp.DevCode, "")
	if got.Code == http.StatusOK {
		t.Fatalf("发给旧地址的验证码不该能确认新地址（换绑等于没有被证明）：%s", got.Body.String())
	}
	if me := env.meWith(t, access); !strings.Contains(me.Body.String(), "alice@example.com") {
		t.Fatalf("确认失败时邮箱不该变，/me 实际 %s", me.Body.String())
	}
}

// TestEmailChangeIsIdempotent 同键同体重放拿到首次的成功响应。
//
// 幂等的价值在"网络超时后重发"：码是单次使用的，第二次真跑会拿到
// OTP_INVALID，用户看到的是"我明明换绑成功了却说验证码无效"。
func TestEmailChangeIsIdempotent(t *testing.T) {
	env := newSettingsEnvWith(t, true)
	access, _ := env.register(t, "alice", "abcd1234")

	const key = "ec-idem-1"
	w, code := env.sendEmailOTP(t, access, "brand-new@example.com", key)
	if w.Code != http.StatusOK {
		t.Fatalf("发码应当 200，实际 %d（body=%s）", w.Code, w.Body.String())
	}
	// 同键同体再发一次：必须复用首次响应（含同一个 devCode）。
	w2, code2 := env.sendEmailOTP(t, access, "brand-new@example.com", key)
	if w2.Code != http.StatusOK {
		t.Fatalf("同键重放应当 200，实际 %d（body=%s）", w2.Code, w2.Body.String())
	}
	if code2 != code {
		t.Fatalf("同键重放应当拿到首次响应（devCode 相同），实际 %q vs %q", code2, code)
	}
}

// TestEmailChangeDoesNotDisturbAdminBind 两条路**并存且互不干扰**。
//
// 管理员代办（`PATCH /api/admin/users/:id {email}`）不需要码，因为它由
// 管理员发起；自助那条必须码发到新地址。两者的判据完全不同，混成一条
// （比如让管理员路也要求码）会让"帮用户解绑"变成不可能。
func TestEmailChangeDoesNotDisturbAdminBind(t *testing.T) {
	env := newSettingsEnvWith(t, true)
	access, _ := env.register(t, "alice", "abcd1234")

	// 自助换绑走一遍，确认它自己那条路是通的。
	w, code := env.sendEmailOTP(t, access, "self@example.com", "")
	if w.Code != http.StatusOK {
		t.Fatalf("自助发码应当 200，实际 %d（body=%s）", w.Code, w.Body.String())
	}
	if got := env.confirmEmail(t, access, "self@example.com", code, ""); got.Code != http.StatusOK {
		t.Fatalf("自助确认应当 200，实际 %d（body=%s）", got.Code, got.Body.String())
	}

	// 管理员路：把邮箱改成另一个地址，**不需要任何验证码**。
	// 这里直接调 service 那一层（管理端路由的装配在 passwordreset_test.go 里
	// 已经覆盖过一次），判据是"它仍然可用、不被自助那条的冷却/幂等挡住"。
	admin := env.store.seed("admin", "admin", "active")
	_ = admin
	if err := env.svc.BindUserEmail(t.Context(), 0, 1, "admin-set@example.com", "203.0.113.9"); err != nil {
		t.Fatalf("管理员代办绑定邮箱应当成功（它是另一条路）：%v", err)
	}

	me := env.meWith(t, access)
	if !strings.Contains(me.Body.String(), "admin-set@example.com") {
		t.Fatalf("管理员代办后 /me 应当是管理员设的地址，实际 %s", me.Body.String())
	}
}
