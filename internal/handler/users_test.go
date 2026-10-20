package handler

// 公开档案与本人资料编辑的验收面（wayfinder #33）。
//
// 装配策略与 admin_test.go 一致：**真实**的 service.AccountService + 真实 Signer，
// 只把持久化换成内存用户表。于是"封禁 → 公开档案 404"这条链路上除 SQL 以外的
// 每一层都是生产代码（状态判定、字段白名单、错误映射全部真跑）。
//
// 三组判据，对应票面三条验收：
//  1. 未登录可读公开档案；PATCH /api/me 未登录 401；
//  2. 响应体字段白名单有单测钉住（加字段必须改测试）；
//  3. 不存在与被封禁的统一口径（都 404，同一个响应）。

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"ProjectionRoom/internal/service/auth"
	"ProjectionRoom/internal/store"
)

// publicProfileKeys 是 GET /api/users/:id 的**完整**字段白名单。
//
// 这个列表是本票验收第 2 条的全部内容：加字段必须改这里，而改这里是一次
// 有意识的动作（会让人先问"这个字段该公开吗"）。反过来，用
// `len(body) == N` 这种断言挡不住"删一个加一个"。
//
// 与 service.PublicProfile 的结构体标签逐字对应；少一个（例如 email）会在这里
// 立刻红，而不是等到某天有人从档案接口里读到别人的邮箱。
var publicProfileKeys = []string{
	"id",
	"username",
	"displayName",
	"role",
	"createdAt",
}

// forbiddenPublicKeys 是**必须缺席**的字段：本人档案里有、公开档案里绝不该有。
//
// 单列一份而不是"靠上面的白名单隐含"：白名单断言的是"集合相等"，而这条断言的是
// "这几个具体的东西不在"。两者的失败信息完全不同 —— 前者说"字段集变了"，
// 后者说"你把别人的邮箱/封禁状态/活跃时间公开了"。后者是安全事故，值得一条自己的用例。
var forbiddenPublicKeys = []string{
	"email",
	"emailVerified",
	"status",
	"lastSeenAt",
	"tokenVersion",
	"bannedReason",
	"passwordHash",
	"avatarChar", // 见 service.PublicProfile 的说明：显示层派生归前端
	"avatarColor",
}

// TestPublicProfileFieldWhitelist 钉住公开档案的字段白名单（验收第 2 条）。
func TestPublicProfileFieldWhitelist(t *testing.T) {
	env := newAdminEnv(t, nil)

	w := doJSON(t, env.engine, http.MethodGet, "/api/users/"+strconv.FormatInt(env.user.ID, 10), "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("公开档案应 200：got %d %s", w.Code, w.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是 JSON 对象：%v（%s）", err, w.Body.String())
	}

	got := make([]string, 0, len(body))
	for k := range body {
		got = append(got, k)
	}
	sort.Strings(got)

	want := append([]string(nil), publicProfileKeys...)
	sort.Strings(want)

	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("公开档案的字段集变了：\n  实际 = %v\n  白名单 = %v\n"+
			"加字段前先问：这个字段该公开吗？改了就把 publicProfileKeys 一起改。", got, want)
	}

	// 反向断言：本人的敏感字段一个都不能出现在公开档案里。
	raw := w.Body.String()
	for _, key := range forbiddenPublicKeys {
		if _, ok := body[key]; ok {
			t.Errorf("公开档案里出现了 %q —— 那是本人档案的字段，不该对他人可见", key)
		}
		// 也查一遍原始字节：某些字段可能以嵌套对象的形式漏出来
		//（例如把整个 user 塞进 "profile" 里），而 map 顶层断言看不出来。
		if strings.Contains(raw, `"`+key+`"`) {
			t.Errorf("响应字节里出现了 %q：%s", key, raw)
		}
	}

	// 值的正确性：这三个是最容易被写反的（id/username/role）。
	if id, _ := body["id"].(float64); int64(id) != env.user.ID {
		t.Errorf("id 应为 %d：got %v", env.user.ID, body["id"])
	}
	if body["username"] != env.user.Username {
		t.Errorf("username 应为 %q：got %v", env.user.Username, body["username"])
	}
	if body["role"] != store.RoleUser {
		t.Errorf("role 应为 %q：got %v", store.RoleUser, body["role"])
	}
	if s, _ := body["createdAt"].(string); s == "" {
		t.Error("createdAt 不应为空：公开档案页要显示加入时间")
	}
}

// TestPublicProfileAnonymousReadable 覆盖验收第 1 条的前半：
// 未登录也能读公开档案（它是帖子作者名、好友搜索结果的落点）。
func TestPublicProfileAnonymousReadable(t *testing.T) {
	env := newAdminEnv(t, nil)

	// 不带 Authorization。
	w := doJSON(t, env.engine, http.MethodGet, "/api/users/"+strconv.FormatInt(env.admin.ID, 10), "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("公开档案应免登录可读：got %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"username":"root"`) {
		t.Errorf("响应里应有 username：%s", w.Body.String())
	}
}

// TestPublicProfileMissingAndBannedBoth404 是本票的核心安全判据。
//
// 不存在与被封禁必须**字节级同形**：任何差异（状态码、错误码、文案）都是一条
// 存在性探测通道，而公开档案是免登录端点 —— 那正是最容易被批量扫的入口。
func TestPublicProfileMissingAndBannedBoth404(t *testing.T) {
	env := newAdminEnv(t, nil)

	// ① 一个从来不存在的 id。
	missing := doJSON(t, env.engine, http.MethodGet, "/api/users/999999", "", "")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("不存在的用户应 404：got %d %s", missing.Code, missing.Body.String())
	}

	// ② 一个真实存在但被封禁的 id。直接改状态（绕过 BanUser 的 token_version 自增：
	// 本用例验的是**读**路径，不需要那张 token 失效）。
	//
	// ⚠ seed 内部**自己加锁**（见 admin_test.go）：外面不能再套一层 s.mu.Lock()，
	// 否则 sync.Mutex 不可重入 → 整个用例挂死（第一次跑就踩到了，超时 5 分钟）。
	banned := env.st.seed("banned_one", store.RoleUser, store.StatusBanned)

	got := doJSON(t, env.engine, http.MethodGet, "/api/users/"+strconv.FormatInt(banned.ID, 10), "", "")
	if got.Code != http.StatusNotFound {
		t.Fatalf("被封禁用户的公开档案也应 404：got %d %s", got.Code, got.Body.String())
	}

	// ③ 两者**逐字相同** —— 这是判据本身，不是附加检查。
	if missing.Body.String() != got.Body.String() {
		t.Fatalf("不存在与被封禁的响应体必须完全相同（否则是存在性探测通道）：\n"+
			"  不存在 = %s\n  被封禁 = %s", missing.Body.String(), got.Body.String())
	}
}

// TestPublicProfileRejectsNonPositiveID 覆盖"id 不是正整数 → 400"。
//
// 与 404 分开是刻意的：前者是**调用方**的问题（前端拼错了 URL），
// 后者是**资源**的问题。混成 404 会让排查方向从一开始就错。
func TestPublicProfileRejectsNonPositiveID(t *testing.T) {
	env := newAdminEnv(t, nil)

	for _, id := range []string{"0", "-1", "abc", "1.5"} {
		w := doJSON(t, env.engine, http.MethodGet, "/api/users/"+id, "", "")
		if w.Code != http.StatusBadRequest {
			t.Errorf("id=%q 应 400（调用方参数错，不是资源不存在）：got %d %s", id, w.Code, w.Body.String())
		}
	}
}

// TestPatchMeRequiresAuth 覆盖验收第 1 条的后半：PATCH /api/me 未登录 401。
func TestPatchMeRequiresAuth(t *testing.T) {
	env := newAdminEnv(t, nil)

	w := doJSON(t, env.engine, http.MethodPatch, "/api/me", `{"displayName":"新名字"}`, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("未登录改昵称应 401：got %d %s", w.Code, w.Body.String())
	}
}

// TestPatchMeUpdatesDisplayName 是正常路径：改完立刻回读、响应带权威行。
func TestPatchMeUpdatesDisplayName(t *testing.T) {
	env := newAdminEnv(t, nil)

	w := doJSON(t, env.engine, http.MethodPatch, "/api/me", `{"displayName":"月夜猫"}`, env.userToken)
	if w.Code != http.StatusOK {
		t.Fatalf("改昵称应 200：got %d %s", w.Code, w.Body.String())
	}

	var body struct {
		User struct {
			ID          int64  `json:"id"`
			DisplayName string `json:"displayName"`
		} `json:"user"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是预期形状：%v（%s）", err, w.Body.String())
	}
	if body.User.DisplayName != "月夜猫" {
		t.Errorf("响应里的昵称应是新值：got %q", body.User.DisplayName)
	}
	if body.User.ID != env.user.ID {
		t.Errorf("改的应当是当前账号 %d：got %d", env.user.ID, body.User.ID)
	}

	// 回读：库里真的变了（而不是只回了一个"看起来对"的响应）。
	// 用 admin_test.go 的 `user()` 辅助方法读副本 —— 自己写 mu.Lock 容易与
	// seed 的内部加锁叠成死锁（sync.Mutex 不可重入）。
	after, ok := env.st.user(env.user.ID)
	if !ok {
		t.Fatalf("用户 %d 应当还在", env.user.ID)
	}
	if after.DisplayName != "月夜猫" {
		t.Errorf("库里昵称应已更新：got %q", after.DisplayName)
	}

	// 而且公开档案里也跟着变 —— 这两处读的是同一行，但它们是两条独立的出口，
	// 断言一次能挡住"公开档案读了缓存"这类回归。
	pub := doJSON(t, env.engine, http.MethodGet, "/api/users/"+strconv.FormatInt(env.user.ID, 10), "", "")
	if !strings.Contains(pub.Body.String(), `"displayName":"月夜猫"`) {
		t.Errorf("公开档案应显示新昵称：%s", pub.Body.String())
	}
}

// TestPatchMeSanitizesControlAndBidi 验 §7.3 的清洗真的走到了（零宽/Bidi 会被去掉）。
//
// 这条用例的价值不在"清洗函数对不对"（那个在 service 层有单测），而在于
// **HTTP 层确实把原始输入交给了清洗**，而不是自己先 TrimSpace 一遍再传。
func TestPatchMeSanitizesControlAndBidi(t *testing.T) {
	env := newAdminEnv(t, nil)

	// 中间夹一个零宽空格 U+200B 与一个 Bidi 覆盖 U+202E。
	payload := `{"displayName":"\u6708\u200b\u202e\u591c\u732b"}`
	w := doJSON(t, env.engine, http.MethodPatch, "/api/me", payload, env.userToken)
	if w.Code != http.StatusOK {
		t.Fatalf("改昵称应 200：got %d %s", w.Code, w.Body.String())
	}

	after, ok := env.st.user(env.user.ID)
	if !ok {
		t.Fatalf("用户 %d 应当还在", env.user.ID)
	}
	if after.DisplayName != "月夜猫" {
		t.Errorf("零宽与 Bidi 字符应被清洗掉：got %q", after.DisplayName)
	}
}

// TestPatchMeFallsBackToUsernameOnEmpty 验"清洗后为空 → 回落成用户名"（§7.3）。
//
// 它是"昵称不能为空"这条规则的实际形态：拒绝空串会让"我就想恢复默认名"
// 变成一个做不到的事，而回落成用户名正好是那个默认值。
func TestPatchMeFallsBackToUsernameOnEmpty(t *testing.T) {
	env := newAdminEnv(t, nil)

	// 全是零宽字符：清洗后是空串。
	w := doJSON(t, env.engine, http.MethodPatch, "/api/me", `{"displayName":"\u200b\u200b\u200b"}`, env.userToken)
	if w.Code != http.StatusOK {
		t.Fatalf("清洗后为空应回落成用户名（200），而不是 400：got %d %s", w.Code, w.Body.String())
	}

	after, ok := env.st.user(env.user.ID)
	if !ok {
		t.Fatalf("用户 %d 应当还在", env.user.ID)
	}
	if after.DisplayName != after.Username {
		t.Errorf("应回落成用户名 %q：got %q", after.Username, after.DisplayName)
	}
}

// TestPatchMeRejectsEmptyBody 验"什么都没传 → 400"。
//
// 为什么不让它成功：一次"什么都没改"的写请求如果回 200，前端提示与审计都会
// 失去意义（"保存成功"但没有任何变化是最难解释的交互）。
func TestPatchMeRejectsEmptyBody(t *testing.T) {
	env := newAdminEnv(t, nil)

	w := doJSON(t, env.engine, http.MethodPatch, "/api/me", `{}`, env.userToken)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("空 body 应 400：got %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "displayName") {
		t.Errorf("400 的文案应指出缺哪个字段：%s", w.Body.String())
	}
}

// TestPatchMeRejectsOverlongDisplayName 验超长昵称的 400 与专用错误码。
//
// 用 DISPLAY_NAME_INVALID 而不是 BAD_REQUEST：设置页只有这一个可编辑字段，
// 专用码让前端能把错误定位到那个输入框。
func TestPatchMeRejectsOverlongDisplayName(t *testing.T) {
	env := newAdminEnv(t, nil)

	// 远超清洗前的宽松上限（4 × 32 = 128 个 rune）。
	long := strings.Repeat("字", maxDisplayNamePatchRunes+1)
	payload, _ := json.Marshal(map[string]string{"displayName": long})
	w := doJSON(t, env.engine, http.MethodPatch, "/api/me", string(payload), env.userToken)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("超长昵称应 400：got %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), CodeDisplayNameInvalid) {
		t.Errorf("错误码应是 %s：%s", CodeDisplayNameInvalid, w.Body.String())
	}
}

// TestPatchMeForbiddenWhenBanned 验"被封禁的本人改昵称 → 403"。
//
// 与公开档案的"被封禁 → 404"形成对照，这不是矛盾：
//   - 公开档案是**别人**在读，封禁要让这个账号不可见（404，避免探测）；
//   - PATCH /api/me 是**本人**在写，他已经带着合法 token 过了 RequireAuth，
//     对他隐瞒"你被封了"没有意义 —— 403 才是可操作的答复。
func TestPatchMeForbiddenWhenBanned(t *testing.T) {
	env := newAdminEnv(t, nil)

	// 造一个已封禁的账号，并用**它自己**的有效 token 发请求。
	// seed 内部自己加锁，外面不能再套一层（sync.Mutex 不可重入）。
	banned := env.st.seed("banned_patch", store.RoleUser, store.StatusBanned)

	// 用与生产同一条路径签发 token（handlerTestSecret 是包级常量，见 auth_test.go）。
	signer, err := auth.NewSigner(handlerTestSecret, 15*time.Minute)
	if err != nil {
		t.Fatalf("构造 Signer 失败：%v", err)
	}
	tok, _, err := signer.Issue(banned.ID, banned.Role, banned.TokenVersion)
	if err != nil {
		t.Fatalf("签发被封禁账号的 token 失败：%v", err)
	}

	w := doJSON(t, env.engine, http.MethodPatch, "/api/me", `{"displayName":"想改个名"}`, tok)
	// RequireAuth 会先过 VerifyAccessToken，而它判 status != active → ErrUserBanned → 403。
	// 所以这里的 403 来自中间件（这正是期望的顺序：封禁的账号连"进入处理器"都不该发生）。
	if w.Code != http.StatusForbidden {
		t.Fatalf("被封禁账号改昵称应 403：got %d %s", w.Code, w.Body.String())
	}
}

// TestPatchMeCannotTouchOtherUsers 验"改昵称只改自己"（不变量 I3 的直接推论）。
//
// 做法：A 改昵称，B 的昵称必须一字不变。这不是"测一个显然的事"——
// 如果哪天有人把 userID 从 token 改成"请求体里的 id"，这条用例会立刻红，
// 而那正是最危险的一类回归（越权写）。
func TestPatchMeCannotTouchOtherUsers(t *testing.T) {
	env := newAdminEnv(t, nil)

	other := env.st.seed("other_guy", store.RoleUser, store.StatusActive)
	before, ok := env.st.user(other.ID)
	if !ok {
		t.Fatalf("用户 %d 应当存在", other.ID)
	}

	// 用 alice 的 token 改昵称，并在 body 里塞一个 targetId（服务端应当完全忽略它）。
	payload := `{"displayName":"alice 的新名字","id":` + strconv.FormatInt(other.ID, 10) + `}`
	w := doJSON(t, env.engine, http.MethodPatch, "/api/me", payload, env.userToken)
	if w.Code != http.StatusOK {
		t.Fatalf("改自己的昵称应 200：got %d %s", w.Code, w.Body.String())
	}

	alice, _ := env.st.user(env.user.ID)
	after, _ := env.st.user(other.ID)

	if alice.DisplayName != "alice 的新名字" {
		t.Errorf("自己的昵称应已更新：got %q", alice.DisplayName)
	}
	if after.DisplayName != before.DisplayName {
		t.Errorf("别人的昵称不该被改动：%q → %q（请求体里的 id 必须被忽略）",
			before.DisplayName, after.DisplayName)
	}
}
