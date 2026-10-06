package ice

// stickyHosts 是"粘性"STUN 主机：只要本轮还能应答，就始终占一个下发名额。
//
// 为什么单独点名这两台：它们是默认列表里少数同时具备 A 与 AAAA 记录的服务器，
// 也就是**唯一能给出 IPv6 srflx 候选**的来源。客户端拿 srflx 里的 IPv6 地址做 IPv6
// 直连（同运营商、双栈环境下这条路径通常比 IPv4 打洞更稳），一旦它们被分数挤掉，
// 整条 IPv6 直连链路就消失了——这与"候选越少越快"的直觉正好相反。
// 所以这里用"能力优先"覆盖纯"延迟优先"：它们慢一点也留着。
//
// 反过来说，如果它们**本轮不应答**，就如实不选入：那说明服务端此刻看不到它们，
// 硬下发只会让浏览器白等一轮超时。
var stickyHosts = []string{"stun.l.google.com", "stun.cloudflare.com"}

// IsSticky 判断一条 STUN URL 是否属于粘性主机（按主机名比较，端口无关）。
func IsSticky(rawURL string) bool {
	host := hostOf(rawURL)
	if host == "" {
		return false
	}
	for _, h := range stickyHosts {
		if host == h {
			return true
		}
	}
	return false
}
