package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 注销冷静期的 HTTP 面（决议 #51 §5 / 票 #73）。
//
// 本文件用**真的** service（与 account_settings_test.go 同一套夹具）：
// 这里要判的是"发起之后对外还剩什么"，而可见性判据（档案 404、搜索不到）
// 全在 service/store 里 —— 用假 service 会让判据退化成"我调用了那个方法"。
//
// 三条 HTTP 面的判断：
//
//  1. **发起的门**：要登录、要当前密码；错密码是 400 PASSWORD_MISMATCH
//     （不是 401 —— 那会让前端以为会话过期而把人踢去登录页）。
//  2. **幂等且不延期**：重复发起拿到同一个到期日（否则每刷一次页面就多 30 天）。
//  3. **撤销的门是反的**：不要密码；没申请过时回 409 DELETION_NOT_PENDING
//     （不是 400 —— 请求体没错，是状态不一致；前端据此收横幅而不是标红输入框）。

// deletionResp 是三个端点的共同响应形状。
type deletionResp struct {
	Pending     bool    `json:"pending"`
	RequestedAt *string `json:"requestedAt"`
	Deadline    *string `json:"deadline"`
}

// requestDeletion 打一次发起端点。
func (e *settingsEnv) requestDeletion(t *testing.T, access, password, idemKey string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"currentPassword":"` + password + `"}`
	return e.doAuthed(t, http.MethodPost, "/api/me/deletion", access, body, idemKey)
}

// cancelDeletion 打一次撤销端点（DELETE 没有请求体）。
func (e *settingsEnv) cancelDeletion(t *testing.T, access string) *httptest.ResponseRecorder {
	t.Helper()
	return e.doAuthed(t, http.MethodDelete, "/api/me/deletion", access, "", "")
}

// getDeletion 读一次注销状态。
func (e *settingsEnv) getDeletion(t *testing.T, access string) *httptest.ResponseRecorder {
	t.Helper()
	return e.doAuthed(t, http.MethodGet, "/api/me/deletion", access, "", "")
}

// decodeDeletion 解出状态体（解析失败直接判红，附带原始 body 便于定位）。
func decodeDeletion(t *testing.T, w *httptest.ResponseRecorder) deletionResp {
	t.Helper()
	var out deletionResp
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析注销状态失败：%v（body=%s）", err, w.Body.String())
	}
	return out
}

// 三个端点都要求登录。
func TestDeletionRequiresAuth(t *testing.T) {
	env := newSettingsEnvWith(t, false)

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"读状态", http.MethodGet, "/api/me/deletion", ""},
		{"发起", http.MethodPost, "/api/me/deletion", `{"currentPassword":"abcd1234"}`},
		{"撤销", http.MethodDelete, "/api/me/deletion", ""},
	}
	for _, tc := range cases {
		w := env.doAuthed(t, tc.method, tc.path, "", tc.body, "")
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s：匿名应 401，实际 %d（body=%s）", tc.name, w.Code, w.Body.String())
		}
	}
}

// 发起成功：pending 为真、带起点与到期日；`/me` 里也带上了 deletionRequestedAt。
func TestRequestDeletionSucceeds(t *testing.T) {
	env := newSettingsEnvWith(t, false)
	access, _ := env.register(t, "delreq", "abcd1234")

	// 发起前：pending 为假，两个时间都是 null。
	before := decodeDeletion(t, env.getDeletion(t, access))
	if before.Pending || before.RequestedAt != nil || before.Deadline != nil {
		t.Fatalf("发起前应当是干净的，实得 %+v", before)
	}

	w := env.requestDeletion(t, access, "abcd1234", "")
	if w.Code != http.StatusOK {
		t.Fatalf("发起应 200，实际 %d（body=%s）", w.Code, w.Body.String())
	}
	got := decodeDeletion(t, w)
	if !got.Pending {
		t.Fatalf("发起后 pending 应为真，实得 %+v", got)
	}
	if got.RequestedAt == nil || got.Deadline == nil {
		t.Fatalf("发起后应带起点与到期日，实得 %+v", got)
	}
	if *got.Deadline <= *got.RequestedAt {
		t.Fatalf("到期日必须晚于起点：%s <= %s", *got.Deadline, *got.RequestedAt)
	}

	// `/me` 也带上这个字段（前端顶部横幅的另一条来源）。
	meW := env.meWith(t, access)
	if meW.Code != http.StatusOK {
		t.Fatalf("/me 应 200，实际 %d", meW.Code)
	}
	// `/me` 的形状是 {"user": {...}}（外面包一层，因为同一响应里还有 unread）。
	var me struct {
		User struct {
			DeletionRequestedAt *string `json:"deletionRequestedAt"`
		} `json:"user"`
	}
	if err := json.Unmarshal(meW.Body.Bytes(), &me); err != nil {
		t.Fatalf("解析 /me 失败：%v", err)
	}
	if me.User.DeletionRequestedAt == nil {
		t.Fatalf("/me 应带上 deletionRequestedAt（body=%s）", meW.Body.String())
	}
}

// 错当前密码：400 + PASSWORD_MISMATCH，且**状态没变**。
//
// 这是"会话被劫持不足以注销别人账号"的实际执行点。断言必须包含"状态没变"：
// 只看状态码的话，一个先落库再报错的实现也能通过。
func TestRequestDeletionRejectsWrongPassword(t *testing.T) {
	env := newSettingsEnvWith(t, false)
	access, _ := env.register(t, "delwrong", "abcd1234")

	w := env.requestDeletion(t, access, "not-my-password", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("错密码应 400，实际 %d（body=%s）", w.Code, w.Body.String())
	}
	if body := w.Body.String(); !contains(body, CodePasswordMismatch) {
		t.Fatalf("错密码应带 %s，实得 %s", CodePasswordMismatch, body)
	}

	// 状态没变。
	after := decodeDeletion(t, env.getDeletion(t, access))
	if after.Pending {
		t.Fatal("错密码不该把账号置入冷静期")
	}
}

// 没填当前密码：400 BAD_REQUEST（不是 500）。
//
// 这条是"漏登记入参哨兵"的回归哨兵：那个错误没进 accountErrorResponse 时
// 会落到 default 分支变成 500，症状是"少填一栏"被显示成"服务端内部错误"。
func TestRequestDeletionRejectsMissingPassword(t *testing.T) {
	env := newSettingsEnvWith(t, false)
	access, _ := env.register(t, "delmissing", "abcd1234")

	for _, body := range []string{`{}`, `{"currentPassword":""}`, `{"currentPassword":"   "}`} {
		w := env.doAuthed(t, http.MethodPost, "/api/me/deletion", access, body, "")
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body=%s 应 400，实际 %d（body=%s）", body, w.Code, w.Body.String())
		}
	}
}

// 幂等且**不延期**：重复发起拿到同一个到期日。
//
// 不延期的意义：用户可能反复点确认（或前端刷新时又发一次），
// 每次都把起点覆盖成 now 的话，冷静期永远不会结束。
func TestRequestDeletionIsIdempotentAndDoesNotExtend(t *testing.T) {
	env := newSettingsEnvWith(t, false)
	access, _ := env.register(t, "delidem", "abcd1234")

	first := env.requestDeletion(t, access, "abcd1234", "")
	if first.Code != http.StatusOK {
		t.Fatalf("第一次发起应 200，实际 %d（body=%s）", first.Code, first.Body.String())
	}
	firstState := decodeDeletion(t, first)

	second := env.requestDeletion(t, access, "abcd1234", "")
	if second.Code != http.StatusOK {
		t.Fatalf("第二次发起应 200（幂等），实际 %d（body=%s）", second.Code, second.Body.String())
	}
	secondState := decodeDeletion(t, second)

	if *firstState.RequestedAt != *secondState.RequestedAt {
		t.Fatalf("重复发起不该改动起点：%s vs %s", *firstState.RequestedAt, *secondState.RequestedAt)
	}
	if *firstState.Deadline != *secondState.Deadline {
		t.Fatalf("重复发起不该推后到期日：%s vs %s", *firstState.Deadline, *secondState.Deadline)
	}
}

// 撤销：不要密码，成功后 pending 变假；再撤一次是 409 DELETION_NOT_PENDING。
func TestCancelDeletion(t *testing.T) {
	env := newSettingsEnvWith(t, false)
	access, _ := env.register(t, "delcancel", "abcd1234")

	if w := env.requestDeletion(t, access, "abcd1234", ""); w.Code != http.StatusOK {
		t.Fatalf("发起应 200，实际 %d（body=%s）", w.Code, w.Body.String())
	}

	w := env.cancelDeletion(t, access)
	if w.Code != http.StatusOK {
		t.Fatalf("撤销应 200，实际 %d（body=%s）", w.Code, w.Body.String())
	}
	if got := decodeDeletion(t, w); got.Pending {
		t.Fatalf("撤销后 pending 应为假，实得 %+v", got)
	}

	// 再撤一次：状态不一致是 409，不是 400 —— 请求体没错（DELETE 甚至没有体）。
	again := env.cancelDeletion(t, access)
	if again.Code != http.StatusConflict {
		t.Fatalf("重复撤销应 409，实际 %d（body=%s）", again.Code, again.Body.String())
	}
	if body := again.Body.String(); !contains(body, CodeDeletionNotPending) {
		t.Fatalf("重复撤销应带 %s，实得 %s", CodeDeletionNotPending, body)
	}
}

// 发起之后**当前会话仍然可用**（冷静期内必须能登录看横幅、点撤销）。
//
// 这是本票最容易写错的一条：看到"注销"就想顺手把会话全蹬掉。蹬掉的直接后果
// 是用户再也登不进来，横幅上的「撤销」按钮永远点不到，30 天后数据真删。
func TestDeletionKeepsSessionUsable(t *testing.T) {
	env := newSettingsEnvWith(t, false)
	access, refreshCookie := env.register(t, "delsession", "abcd1234")

	if w := env.requestDeletion(t, access, "abcd1234", ""); w.Code != http.StatusOK {
		t.Fatalf("发起应 200，实际 %d（body=%s）", w.Code, w.Body.String())
	}

	// access token 仍然有效。
	if w := env.meWith(t, access); w.Code != http.StatusOK {
		t.Fatalf("冷静期内 access token 应仍然有效，实际 %d（body=%s）", w.Code, w.Body.String())
	}
	// refresh 也仍然可用（能续期才叫"还能登录"）。
	if w := env.refreshWith(t, refreshCookie); w.Code != http.StatusOK {
		t.Fatalf("冷静期内 refresh 应仍然可用，实际 %d（body=%s）", w.Code, w.Body.String())
	}
}

// 注销之后公开档案 404（与封禁合流成同一个"用户不存在"）。
func TestDeletionHidesPublicProfile(t *testing.T) {
	env := newSettingsEnvWith(t, false)
	access, _ := env.register(t, "delhide", "abcd1234")

	// 找到自己的 id（注册响应里的 user.id）。
	meW := env.meWith(t, access)
	var me struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(meW.Body.Bytes(), &me); err != nil {
		t.Fatalf("解析 /me 失败：%v", err)
	}

	// 但本文件的 env 只注册了 account 路由，没有注册 `GET /api/users/:id`
	//（那条在 registerUserProfileRoutes 里）。所以这里判的是 service 层的
	// 公开档案门 —— 直接用 svc，不绕 HTTP（HTTP 面的可见性由 verify-profile
	// 与 store 层的用例覆盖）。
	if _, err := env.svc.PublicProfile(t.Context(), me.User.ID); err != nil {
		t.Fatalf("注销前公开档案应可读：%v", err)
	}
	if w := env.requestDeletion(t, access, "abcd1234", ""); w.Code != http.StatusOK {
		t.Fatalf("发起应 200，实际 %d（body=%s）", w.Code, w.Body.String())
	}
	if _, err := env.svc.PublicProfile(t.Context(), me.User.ID); err == nil {
		t.Fatal("注销后公开档案应当不可读（404 口径）")
	}

	// 撤销后回来。
	if w := env.cancelDeletion(t, access); w.Code != http.StatusOK {
		t.Fatalf("撤销应 200，实际 %d（body=%s）", w.Code, w.Body.String())
	}
	if _, err := env.svc.PublicProfile(t.Context(), me.User.ID); err != nil {
		t.Fatalf("撤销后公开档案应重新可读：%v", err)
	}
}

// contains 是本地小工具（避免为一个调用引 strings）。
func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
