package storage

// OwnsURL / isObjectKey 的单元测试（票 #74）。
//
// 这两个函数是「头像地址必须是本桶发的」这条规矩的唯一执行点，而它的输入
// 完全来自请求体 —— 所以它是本包里最该被逐变体钉死的地方。
//
// 不需要真 MinIO：Client 的字段都是同包的，直接构造即可（本文件在 package
// storage 内，不是 storage_test）。这正是把判据写成纯函数形状的收益。

import (
	"strings"
	"testing"
)

// newTestClient 造一个只用于 URL 判定的客户端（不连任何服务）。
func newTestClient(baseURL string) *Client {
	return &Client{
		cfg:    Config{Endpoint: "127.0.0.1:9000", Bucket: "pr-files", PublicBaseURL: baseURL},
		bucket: "pr-files",
	}
}

// TestIsObjectKeyAcceptsGeneratedShape 钉住 newObjectKey 会产出的形状。
func TestIsObjectKeyAcceptsGeneratedShape(t *testing.T) {
	ok := []string{
		"2026/11/" + strings.Repeat("a", 32) + ".jpg",
		"2026/01/" + strings.Repeat("0", 32) + ".png",
		"1999/12/" + strings.Repeat("f", 32) + ".gif",
		"2026/11/" + strings.Repeat("9", 32) + ".webp",
	}
	for _, key := range ok {
		if !isObjectKey(key) {
			t.Errorf("应接受（这是 newObjectKey 会产出的形状）：%q", key)
		}
	}
}

// TestIsObjectKeyRejectsEverythingElse 是这条判据的主体：把所有"看起来像"
// 但不该被接受的东西逐个钉住。
func TestIsObjectKeyRejectsEverythingElse(t *testing.T) {
	cases := []struct{ key, why string }{
		// —— 段数与穿越 ——
		{"", "空 key"},
		{"2026", "只有年"},
		{"2026/11", "只有年月"},
		{"2026/11/aa/bb.jpg", "多一段"},
		{"../../etc/passwd", "穿越到别处"},
		{"2026/11/../../../other-bucket/x.jpg", "穿越到别的桶"},
		{"/2026/11/" + strings.Repeat("a", 32) + ".jpg", "绝对路径（前导斜杠）"},
		{"2026/11/" + strings.Repeat("a", 32) + ".jpg/", "尾随斜杠"},
		{"2026//" + strings.Repeat("a", 32) + ".jpg", "空月份"},

		// —— 年月形状 ——
		{"26/11/" + strings.Repeat("a", 32) + ".jpg", "年份两位"},
		{"20266/11/" + strings.Repeat("a", 32) + ".jpg", "年份五位"},
		{"2026/1/" + strings.Repeat("a", 32) + ".jpg", "月份一位"},
		{"2026/ab/" + strings.Repeat("a", 32) + ".jpg", "月份非数字"},
		{"20x6/11/" + strings.Repeat("a", 32) + ".jpg", "年份非数字"},

		// —— 随机段 ——
		{"2026/11/" + strings.Repeat("a", 31) + ".jpg", "hex 短一位"},
		{"2026/11/" + strings.Repeat("a", 33) + ".jpg", "hex 长一位"},
		{"2026/11/" + strings.Repeat("A", 32) + ".jpg", "大写 hex（我们只产出小写）"},
		{"2026/11/" + strings.Repeat("g", 32) + ".jpg", "非 hex 字母"},
		{"2026/11/" + strings.Repeat("a", 32), "没有扩展名"},
		{"2026/11/.jpg", "只有扩展名"},

		// —— 扩展名白名单 ——
		{"2026/11/" + strings.Repeat("a", 32) + ".svg", "SVG 刻意不在白名单（可内嵌脚本）"},
		{"2026/11/" + strings.Repeat("a", 32) + ".bmp", "非白名单格式"},
		{"2026/11/" + strings.Repeat("a", 32) + ".jpeg", "白名单存的是 .jpg 不是 .jpeg"},
		{"2026/11/" + strings.Repeat("a", 32) + ".JPG", "扩展名大小写敏感"},
	}
	for _, c := range cases {
		if isObjectKey(c.key) {
			t.Errorf("应拒绝（%s）：%q", c.why, c.key)
		}
	}
}

// TestOwnsURLAcceptsOwnBucket 是正常路径：本桶 + 合法 key。
func TestOwnsURLAcceptsOwnBucket(t *testing.T) {
	c := newTestClient("https://cdn.example.com")
	u := "https://cdn.example.com/pr-files/2026/11/" + strings.Repeat("a", 32) + ".jpg"
	if !c.OwnsURL(u) {
		t.Errorf("本桶的合法 URL 应通过：%q", u)
	}

	// 尾部/首部空白要能容错：前端可能从别处复制来带空白的字符串。
	if !c.OwnsURL("  " + u + "\n") {
		t.Error("两侧空白应被 TrimSpace 掉")
	}
}

// TestOwnsURLRejectsForeignOrigins 是安全核心：**别的域**即使路径长得一样也不行。
//
// 这条用例存在的原因很具体：只判 "包含 /pr-files/" 的实现会让
// `https://evil.com/x/pr-files/2026/11/<hex>.jpg` 通过 —— 攻击者因此可以把自己的
// 头像指向任意第三方地址，于是每一个看到他的用户都向那个地址发一次 GET
// （带 Referer 与真实 IP）。决议 #54 §8 否决"外链直填"就是否决这个。
func TestOwnsURLRejectsForeignOrigins(t *testing.T) {
	c := newTestClient("https://cdn.example.com")
	key := "2026/11/" + strings.Repeat("a", 32) + ".jpg"

	bad := []struct{ url, why string }{
		{"https://evil.com/pr-files/" + key, "完全不同的域"},
		{"https://evil.com/x/pr-files/" + key, "路径里含 /pr-files/ 但域不是我们"},
		{"https://cdn.example.com.evil.com/pr-files/" + key, "后缀伪装（前缀不是我们的）"},
		{"http://cdn.example.com/pr-files/" + key, "scheme 不同"},
		{"https://cdn.example.com/other-bucket/" + key, "别的桶"},
		{"https://cdn.example.com/pr-files/", "空 key"},
		{"https://cdn.example.com/pr-files", "没有尾斜杠（不成 key 形状）"},
		{"", "空串"},
		{"   ", "全空白"},
		{"not a url", "不是 URL"},
		{"https://cdn.example.com/pr-files/../../other/" + key, "穿越"},
	}
	for _, b := range bad {
		if c.OwnsURL(b.url) {
			t.Errorf("应拒绝（%s）：%q", b.why, b.url)
		}
	}
}

// TestOwnsURLNilAndFallbackBase 覆盖两条边：
//   - nil 客户端（对象存储未配置）一律 false，而不是 panic；
//   - 没配 PublicBaseURL 时退回 Endpoint 拼出的地址（本地开发直连 MinIO）。
func TestOwnsURLNilAndFallbackBase(t *testing.T) {
	var nilClient *Client
	if nilClient.OwnsURL("https://cdn.example.com/pr-files/2026/11/" + strings.Repeat("a", 32) + ".jpg") {
		t.Error("nil 客户端应返回 false（未配置对象存储）")
	}

	// PublicBaseURL 为空 → publicURL 退回 scheme://Endpoint。
	fallback := newTestClient("")
	key := "2026/11/" + strings.Repeat("a", 32) + ".jpg"
	if !fallback.OwnsURL("http://127.0.0.1:9000/pr-files/" + key) {
		t.Error("没配 PublicBaseURL 时应接受 scheme://Endpoint 形态")
	}
	if fallback.OwnsURL("https://127.0.0.1:9000/pr-files/" + key) {
		t.Error("UseSSL=false 时 https 形态不该通过")
	}
}

// TestOwnsURLTrailingSlashInBase 钉住 PublicBaseURL 带尾斜杠时的归一化。
//
// 运维很容易把 `https://cdn.example.com/` 写进配置；若不 TrimSuffix，
// 拼出来的前缀会是 `...com//pr-files/`，于是**自己发的 URL 全部被判非法** ——
// 症状是"上传成功但一保存头像就 400"，而错误信息里看不出是配置问题。
func TestOwnsURLTrailingSlashInBase(t *testing.T) {
	c := newTestClient("https://cdn.example.com/")
	u := "https://cdn.example.com/pr-files/2026/11/" + strings.Repeat("a", 32) + ".jpg"
	if !c.OwnsURL(u) {
		t.Errorf("PublicBaseURL 带尾斜杠时应仍然通过：%q", u)
	}
}
