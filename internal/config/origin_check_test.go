package config

import (
	"strings"
	"testing"
)

// 「PR_PUBLIC_BASE_URL 的 host 必须在 PR_ALLOWED_ORIGINS 里」这道闸的用例。
//
// 它锁定的是一条**判定**：对外基地址的 host 与浏览器发 Origin 时的 host
// 必然是同一个值（页面就从基地址那儿打开）。两者不一致不是"配置风格问题"，
// 而是"这个实例的页面连不上自己的 /ws"——必须在启动期拦下，不能等用户点开页面。
//
// 线上实证（2026-11-02）：新域名上线后只改了 nginx，没改 PR_ALLOWED_ORIGINS，
// 于是 /ws 与 /ws/user 全部 403，观众进不了房、实时通道无限重连。

// TestPublicBaseURLAllowedMatchesHandlerSemantics 锁定与 handler.checkOrigin
// 一致的模式语义：含 `://` 比 scheme+host，否则只比 host[:port]，且大小写不敏感。
//
// 为什么必须一致：启动期的判定若比运行期的判据更宽，用户会看到"启动说没问题、
// 页面照样 403"；若更严，则会拦下本来能用的配置。两边是同一件事的两处实现。
func TestPublicBaseURLAllowedMatchesHandlerSemantics(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		allowed []string
		wantOK  bool
	}{
		{
			name:    "scheme 形态完全一致",
			baseURL: "https://pr.example.com",
			allowed: []string{"https://pr.example.com"},
			wantOK:  true,
		},
		{
			name:    "只写 host 也算命中（与 checkOrigin 一致）",
			baseURL: "https://pr.example.com",
			allowed: []string{"pr.example.com"},
			wantOK:  true,
		},
		{
			name:    "带端口：白名单写 scheme 形态",
			baseURL: "http://127.0.0.1:8888",
			allowed: []string{"http://127.0.0.1:8888"},
			wantOK:  true,
		},
		{
			name:    "带端口：白名单写裸 host:port（默认值就是这个形态）",
			baseURL: "http://127.0.0.1:8888",
			allowed: []string{"127.0.0.1:5173", "127.0.0.1:8888"},
			wantOK:  true,
		},
		{
			name:    "大小写不敏感",
			baseURL: "https://PR.Example.COM",
			allowed: []string{"https://pr.example.com"},
			wantOK:  true,
		},
		{
			name:    "通配模式（*.example.com）",
			baseURL: "https://app.example.com",
			allowed: []string{"*.example.com"},
			wantOK:  true,
		},
		{
			name:    "白名单里有多项，命中任意一项即可",
			baseURL: "https://pr.example.com",
			allowed: []string{"http://a.example", "127.0.0.1:5173", "pr.example.com"},
			wantOK:  true,
		},
		{
			// 这正是线上那次的形态：白名单只有旧 IP，基地址换成了新域名。
			name:    "白名单只有旧 IP、基地址是新域名 → 不命中",
			baseURL: "http://yachiyocat.top",
			allowed: []string{"http://60.205.212.25", "http://60.205.212.25:80"},
			wantOK:  false,
		},
		{
			name:    "端口不一致不算命中",
			baseURL: "http://127.0.0.1:8888",
			allowed: []string{"127.0.0.1:5173"},
			wantOK:  false,
		},
		{
			name:    "scheme 不一致不算命中（http 与 https 是两个来源）",
			baseURL: "https://pr.example.com",
			allowed: []string{"http://pr.example.com"},
			wantOK:  false,
		},
		{
			// 解析不出 host 时不属于本闸的职责（applyAuthEnv 已保证带 scheme）；
			// 放行以免把"格式写错"与"白名单漏配"混进同一句报错里。
			name:    "解析不出 host 时放行",
			baseURL: "https://",
			allowed: []string{"pr.example.com"},
			wantOK:  true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			origin, ok := publicBaseURLAllowed(c.baseURL, c.allowed)
			if ok != c.wantOK {
				t.Fatalf("publicBaseURLAllowed(%q, %v) = %v, 期望 %v", c.baseURL, c.allowed, ok, c.wantOK)
			}
			if !ok && !strings.Contains(origin, "://") {
				t.Fatalf("不命中时应回带 scheme 的原文（供直接抄进白名单），实际 %q", origin)
			}
		})
	}
}

// TestRequireCompleteRejectsPublicBaseURLNotInAllowedOrigins 是这道闸的回归哨兵。
//
// 断言三件事，缺一不可：
//  1. 这种情况必须**拒绝启动**（而不是运行期 403）；
//  2. 报错里点名 PR_ALLOWED_ORIGINS 与 PR_PUBLIC_BASE_URL（拒绝启动的全部价值
//     在于错误信息说清该补哪一项）；
//  3. 报错里给出**可直接抄进白名单的那一项原文** —— 用户不该自己去推
//     "该写 http://host 还是 host:port"（这两种形态在白名单里都合法，选错就还是 403）。
func TestRequireCompleteRejectsPublicBaseURLNotInAllowedOrigins(t *testing.T) {
	completeEnv(t)
	// 线上那次的形态：白名单只写了旧 IP，基地址换成了新域名。
	t.Setenv(envPublicBaseURL, "http://yachiyocat.top")
	t.Setenv(envAllowedOrigins, "http://60.205.212.25,http://60.205.212.25:80")

	_, err := Load()
	if err == nil {
		t.Fatal("基地址的 host 不在白名单里时必须拒绝启动（否则运行期 /ws 一律 403）")
	}
	msg := err.Error()
	for _, want := range []string{"PR_ALLOWED_ORIGINS", "PR_PUBLIC_BASE_URL", "403", "http://yachiyocat.top"} {
		if !strings.Contains(msg, want) {
			// 这里刻意断言"可抄的原文"而不是只断言失败：
			// 只报"不在白名单里"等于让用户自己猜该写哪一种形态。
			t.Fatalf("报错里应包含 %q，实际：%s", want, msg)
		}
	}
}

// TestRequireCompletePassesWhenBaseURLHostIsAllowed 是反向确认：
// 把该补的那一项补上之后必须能通过，否则这道闸会把正确的配置也拦下。
func TestRequireCompletePassesWhenBaseURLHostIsAllowed(t *testing.T) {
	completeEnv(t)
	t.Setenv(envPublicBaseURL, "http://yachiyocat.top")
	t.Setenv(envAllowedOrigins, "http://60.205.212.25,http://yachiyocat.top")

	if _, err := Load(); err != nil {
		t.Fatalf("白名单已覆盖基地址的 host，不该失败：%v", err)
	}
}

// TestRequireCompleteOriginCheckSkippedWhenIncomplete 锁定"缺项只报一次"的顺序问题：
// PR_PUBLIC_BASE_URL 为空时只该报"为空"，不该顺带报一句"host 不在白名单里"——
// 后者在基地址为空时毫无意义，只会让用户去查一个不相干的变量。
func TestRequireCompleteOriginCheckSkippedWhenIncomplete(t *testing.T) {
	completeEnv(t)
	unsetEnv(t, envPublicBaseURL)

	_, err := Load()
	if err == nil {
		t.Fatal("缺 PR_PUBLIC_BASE_URL 时应拒绝启动")
	}
	if strings.Contains(err.Error(), "PR_ALLOWED_ORIGINS") {
		t.Fatalf("基地址为空时不该再报白名单问题（会把人引向不相干的变量）：%s", err.Error())
	}
}
