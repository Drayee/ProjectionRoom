package config

import (
	"net/url"
	"path"
	"strings"
)

// publicBaseURLAllowed 判断 `PR_PUBLIC_BASE_URL` 的 host 是否被
// `PR_ALLOWED_ORIGINS` 覆盖；不覆盖时返回"应当补进白名单的那一项"。
//
// # 为什么要有这条校验
//
// 2026-11-02 线上实证：新域名 `yachiyocat.top` 上线后，服务器 `.env` 的
// `PR_ALLOWED_ORIGINS` 还停在旧的 IP 形态，于是**页面能正常打开**（静态资源
// 走的是路径，不判来源），但 `/ws?roomId=…` 与 `/ws/user?ticket=…` 一律 403：
// 观众进不了房、实时通道无限重连，而浏览器控制台里只有一行
// `WebSocket connection to 'ws://…' failed: Error during WebSocket handshake: Unexpected response code: 403`。
// 症状与真实原因（一个环境变量少写一项）之间隔了整条 nginx + 白名单链路，
// 排查成本极高，而它**在启动期就是可判定的**：对外基地址的 host 必然也是
// 浏览器发 Origin 时的 host。既然两者必须一致，就不该等到用户点开页面才发现。
//
// # 判据与 internal/handler 的 checkOrigin 同源
//
// 与 `internal/handler/ws.go` 的 `checkOrigin` 用同一套规则：
//   - 模式含 `://` 时与 `scheme://host` 比，否则只与 `host[:port]` 比；
//   - 支持 `path.Match` 的通配（`*.example.com`）；
//   - 大小写不敏感。
//
// 为什么不直接复用那份实现：`config` 是依赖树的叶子，`handler` 反过来依赖
// `config`，包一层 import 就成环了。这里只保留"字符串 + 通配"的最小实现，
// **两处必须一起改** —— 这点写在这里，也写在那边的注释里。
//
// 返回的第二个值只在 `ok=false` 时有意义：`scheme://host` 形态的原文，
// 直接抄进白名单即可修好（用户不需要自己推出该写哪一种形态）。
func publicBaseURLAllowed(baseURL string, allowed []string) (schemeOrigin string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" {
		// 解析不出 host 说明格式本身有问题 —— 那不是这条校验的职责
		// （applyAuthEnv 已保证它带 scheme），这里放行以免把两件事混在一句报错里。
		return "", true
	}
	schemeOrigin = strings.ToLower(u.Scheme + "://" + u.Host)
	target := strings.ToLower(u.Host)

	for _, raw := range allowed {
		pattern := strings.ToLower(strings.TrimSpace(raw))
		if pattern == "" {
			continue
		}
		candidate := target
		if strings.Contains(pattern, "://") {
			candidate = schemeOrigin
		}
		if matched, err := path.Match(pattern, candidate); err == nil && matched {
			return schemeOrigin, true
		}
	}
	return schemeOrigin, false
}
