package handler

// 头像频控与管理端重置（票 #75）的 HTTP 面。
//
// 本文件只判**协议面**：429 的码与文案、resetAvatar 的三态与 200 回包。
// 领域规则（额度在写库前扣、清除不吃额度、先置空再删对象）在
// service/avatar_quota_test.go 里判 —— 那里能用假时钟观测跨日，
// HTTP 层只能靠"连打六次"来间接观察。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"ProjectionRoom/internal/config"
)

// newQuotaEnv 造一台"限速闸门足够宽"的管理端测试服务。
//
// 为什么要放宽：`PATCH /api/me` 挂的是登录桶（10/min、burst 5，见 users.go
// 的 writeLimiter）。本文件的用例要连打十几二十次 PATCH，正常配置下前 5 次之后
// 就会被**按 IP 的**闸门拦成 429 —— 那与本票要判的"按用户的每日 5 次额度"
// 是两件不同的事，混在一起会让失败信息指向错误的原因（实测第一次跑就是
// "第 6 次重设同一张应 200，实际 429"，而那个 429 来自 IP 桶）。
//
// 刻意不改成"少发几次"：额度恰好是 5，判"第 6 次 429"至少要发 6 次，
// 再叠加"重设不吃额度"那几条就必须超过 burst。
func newQuotaEnv(t *testing.T) *adminEnv {
	t.Helper()
	return newAdminEnv(t, func(cfg *config.Config) {
		cfg.IPC.LoginPerMinute = 6000
		cfg.IPC.LoginBurst = 1000
	})
}

// patchMeAvatar 发一次 PATCH /api/me（设头像），返回响应。
func patchMeAvatar(t *testing.T, env *adminEnv, url string) int {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"avatarUrl": url})
	if err != nil {
		t.Fatalf("marshal：%v", err)
	}
	w := doJSON(t, env.engine, http.MethodPatch, "/api/me", string(payload), env.userToken)
	return w.Code
}

// TestPatchMeAvatarQuotaReturns429 判"第 6 次改头像 → 429"。
//
// 这是决议 #54 §3 的每日 5 次限额在协议面上的唯一痕迹。三次要点：
//   - 状态码必须是 429（不是 400/403）：它承载"稍后重试"的语义；
//   - 码必须是 model.CodeRateLimited，前端已有一处按这个码分诊；
//   - 文案必须能被人读懂（带天数与"明天"），不能只回一个码。
func TestPatchMeAvatarQuotaReturns429(t *testing.T) {
	env := newQuotaEnv(t)

	for i := 0; i < 5; i++ {
		url := avatarHandlerURL(string(rune('a' + i)))
		if code := patchMeAvatar(t, env, url); code != http.StatusOK {
			t.Fatalf("第 %d 次改头像应 200，实际 %d", i+1, code)
		}
	}

	payload, _ := json.Marshal(map[string]any{"avatarUrl": avatarHandlerURL("z")})
	w := doJSON(t, env.engine, http.MethodPatch, "/api/me", string(payload), env.userToken)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("第 6 次应当 429，实际 %d（%s）", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "RATE_LIMITED") {
		t.Fatalf("429 应带 RATE_LIMITED：%s", w.Body.String())
	}
	// 文案要能说明"为什么"与"什么时候能再试"。
	body := w.Body.String()
	if !strings.Contains(body, "明天") && !strings.Contains(body, "次数") {
		t.Fatalf("429 的文案应说明额度与何时能再试：%s", body)
	}

	// 被拒之后头像必须停在第五张 —— "被拒绝"不该顺手把第六张写进去。
	after, ok := env.st.user(env.user.ID)
	if !ok {
		t.Fatalf("用户 %d 应当还在", env.user.ID)
	}
	if after.AvatarURL == nil || *after.AvatarURL != avatarHandlerURL("e") {
		t.Fatalf("被拒之后头像应停在第 5 张，实际 %v", after.AvatarURL)
	}
}

// TestPatchMeAvatarQuotaIgnoresRepeats 判"重设同一张不吃额度"。
//
// 前端每次进设置页都会重挂、网络层也会重试同一个 PATCH。如果它们吃额度，
// 用户"什么都没改"就会用掉今天的机会 —— 这是最容易被忽略的一个副作用。
func TestPatchMeAvatarQuotaIgnoresRepeats(t *testing.T) {
	env := newQuotaEnv(t)
	url := avatarHandlerURL("a")

	for i := 0; i < 8; i++ {
		if code := patchMeAvatar(t, env, url); code != http.StatusOK {
			t.Fatalf("第 %d 次重设同一张应 200，实际 %d", i+1, code)
		}
	}

	// 于是还有 4 次真正的变更额度可用。
	for i := 0; i < 4; i++ {
		next := avatarHandlerURL(string(rune('b' + i)))
		if code := patchMeAvatar(t, env, next); code != http.StatusOK {
			t.Fatalf("重设之后的第 %d 次变更应 200，实际 %d", i+1, code)
		}
	}
	// 第 6 次变更才 429。
	payload, _ := json.Marshal(map[string]any{"avatarUrl": avatarHandlerURL("z")})
	w := doJSON(t, env.engine, http.MethodPatch, "/api/me", string(payload), env.userToken)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("第 6 次真变更应 429，实际 %d（%s）", w.Code, w.Body.String())
	}
}

// TestPatchMeClearDoesNotConsumeQuota 判"清除头像不吃额度"。
//
// 回落到字母头是降低风险的方向（违规图的紧急处置正是这条路），
// 给它设计数等于让用户在"想撤掉这张图"和"留到明天"之间选后者。
func TestPatchMeClearDoesNotConsumeQuota(t *testing.T) {
	env := newQuotaEnv(t)

	if code := patchMeAvatar(t, env, avatarHandlerURL("a")); code != http.StatusOK {
		t.Fatalf("设头像应 200，实际 %d", code)
	}
	// 连清 8 次都不该吃额度。
	clearBody := `{"avatarUrl":null}`
	for i := 0; i < 8; i++ {
		w := doJSON(t, env.engine, http.MethodPatch, "/api/me", clearBody, env.userToken)
		if w.Code != http.StatusOK {
			t.Fatalf("第 %d 次清除应 200，实际 %d（%s）", i+1, w.Code, w.Body.String())
		}
	}
	// 仍然还有 4 次变更额度。
	for i := 0; i < 4; i++ {
		if code := patchMeAvatar(t, env, avatarHandlerURL(string(rune('b'+i)))); code != http.StatusOK {
			t.Fatalf("清除之后的第 %d 次变更应 200，实际 %d", i+1, code)
		}
	}
}

// TestAdminResetAvatarClearsIt 判定违规头像的一键处置。
//
// 三件事：回包里的用户已经没有 avatarUrl（管理端表格立刻更新）、
// 库里的值确实空了（他人视角也回字母头）、审计里有 user.avatar.reset。
func TestAdminResetAvatarClearsIt(t *testing.T) {
	env := newQuotaEnv(t)
	url := avatarHandlerURL("a")

	if code := patchMeAvatar(t, env, url); code != http.StatusOK {
		t.Fatalf("先设一张头像：%d", code)
	}
	stored, _ := env.st.user(env.user.ID)
	if stored.AvatarURL == nil {
		t.Fatal("前置失败：头像没落库")
	}

	target := "/api/admin/users/" + strconv.FormatInt(env.user.ID, 10)
	w := doJSON(t, env.engine, http.MethodPatch, target, `{"resetAvatar":true}`, env.adminToken)
	if w.Code != http.StatusOK {
		t.Fatalf("重置头像应 200，实际 %d（%s）", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), url) {
		t.Errorf("回包里不该再出现被清掉的地址：%s", w.Body.String())
	}

	after, ok := env.st.user(env.user.ID)
	if !ok {
		t.Fatalf("用户 %d 应当还在", env.user.ID)
	}
	if after.AvatarURL != nil {
		t.Fatalf("库里必须置空（他人视角立刻回字母头），实际 %v", *after.AvatarURL)
	}

	// 审计（异步单写，用 waitUntil 等它落地）。
	waitFor(t, "写 user.avatar.reset 审计", func() bool {
		return len(env.st.auditsOf("user.avatar.reset")) == 1
	})
}

// TestAdminResetAvatarIsIdempotent 判"本来就没有头像"时仍然 200。
//
// 管理端面板会连点。第二次点必须是成功 + 再多一条 unchanged 审计，
// 而不是 404/409 —— 那个对象已经不在了，"再删一次"不是错误。
func TestAdminResetAvatarIsIdempotent(t *testing.T) {
	env := newQuotaEnv(t)
	target := "/api/admin/users/" + strconv.FormatInt(env.user.ID, 10)

	for i := 0; i < 2; i++ {
		w := doJSON(t, env.engine, http.MethodPatch, target, `{"resetAvatar":true}`, env.adminToken)
		if w.Code != http.StatusOK {
			t.Fatalf("第 %d 次重置应 200（幂等），实际 %d（%s）", i+1, w.Code, w.Body.String())
		}
	}
	waitFor(t, "两次重置各写一条审计", func() bool {
		return len(env.st.auditsOf("user.avatar.reset")) == 2
	})
	// 第二次要标 unchanged，否则事后复盘分不清"处置了一张图"与"点了个空"。
	if !env.st.auditDetailContains("user.avatar.reset", `"unchanged":true`) {
		t.Fatal("对没有头像的用户重置，审计里应标 unchanged=true")
	}
}

// TestAdminResetAvatarUnknownUser 判目标不存在 → 404。
func TestAdminResetAvatarUnknownUser(t *testing.T) {
	env := newQuotaEnv(t)
	w := doJSON(t, env.engine, http.MethodPatch, "/api/admin/users/99999", `{"resetAvatar":true}`, env.adminToken)
	if w.Code != http.StatusNotFound {
		t.Fatalf("不存在的账号应 404，实际 %d（%s）", w.Code, w.Body.String())
	}
}

// TestAdminResetAvatarFalseIsNoop 判 `resetAvatar:false` 不被当成"要重置"。
//
// 用 `*bool` 而不是 `bool` 的全部意义在这里：`false` 是**显式不重置**，
// 与"没传这个键"在语义上都是"不动头像"，但前者至少说明调用方想过这件事。
// 两种都不该清掉头像 —— 也不该被"全空"判定拒掉（`false` 不是 nil）。
func TestAdminResetAvatarFalseIsNoop(t *testing.T) {
	env := newQuotaEnv(t)
	url := avatarHandlerURL("a")
	if code := patchMeAvatar(t, env, url); code != http.StatusOK {
		t.Fatalf("先设一张头像：%d", code)
	}

	target := "/api/admin/users/" + strconv.FormatInt(env.user.ID, 10)
	w := doJSON(t, env.engine, http.MethodPatch, target, `{"resetAvatar":false}`, env.adminToken)
	if w.Code != http.StatusOK {
		t.Fatalf("resetAvatar:false 应当 200（它是一次合法的空操作），实际 %d（%s）", w.Code, w.Body.String())
	}
	after, _ := env.st.user(env.user.ID)
	if after.AvatarURL == nil || *after.AvatarURL != url {
		t.Fatalf("resetAvatar:false 不该动头像，实际 %v", after.AvatarURL)
	}
}

// TestAdminResetAvatarAlongsideOtherFields 判"同一次 patch 里重置头像 + 改状态"。
//
// 管理端的确认条是"一次提交一组动作"，所以多字段组合是常态；
// 重置排在状态之前（它没有可填错的参数，越早生效越好）。
func TestAdminResetAvatarAlongsideOtherFields(t *testing.T) {
	env := newQuotaEnv(t)
	url := avatarHandlerURL("a")
	if code := patchMeAvatar(t, env, url); code != http.StatusOK {
		t.Fatalf("先设一张头像：%d", code)
	}

	target := "/api/admin/users/" + strconv.FormatInt(env.user.ID, 10)
	body := `{"resetAvatar":true,"status":"banned","reason":"违规头像"}`
	w := doJSON(t, env.engine, http.MethodPatch, target, body, env.adminToken)
	if w.Code != http.StatusOK {
		t.Fatalf("组合 patch 应 200，实际 %d（%s）", w.Code, w.Body.String())
	}

	after, _ := env.st.user(env.user.ID)
	if after.AvatarURL != nil {
		t.Errorf("头像应被重置，实际 %v", *after.AvatarURL)
	}
	if after.Status != "banned" {
		t.Errorf("状态应为 banned，实际 %q", after.Status)
	}
	waitFor(t, "两个动作各写一条审计", func() bool {
		return len(env.st.auditsOf("user.avatar.reset")) == 1 && len(env.st.auditsOf("user.ban")) >= 1
	})
}

// 保住 bytes / fmt 的引用（本文件用它们构造与诊断请求体）。
var (
	_ = bytes.NewReader
	_ = fmt.Sprintf
)
