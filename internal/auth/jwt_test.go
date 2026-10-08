package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	testSecret  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	otherSecret = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"

	testTTL = 15 * time.Minute
)

// newTestSigner 构造用于正常路径的签发器（15 分钟 TTL，与 config 默认值一致）。
func newTestSigner(t *testing.T) *Signer {
	t.Helper()
	s, err := NewSigner(testSecret, testTTL)
	if err != nil {
		t.Fatalf("NewSigner() 报错：%v", err)
	}
	return s
}

// baseClaims 造一份"合法"的载荷，供各用例按需删/改字段。
func baseClaims() jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"sub":  "42",
		"role": "user",
		"tv":   3,
		"jti":  "jti-0001",
		"iat":  jwt.NewNumericDate(now.Add(-time.Minute)),
		"exp":  jwt.NewNumericDate(now.Add(time.Minute)),
	}
}

// signMap 用真密钥把任意载荷签成 token——用来模拟"密钥已泄漏"或"字段被改"的构造，
// 从而验证：即使签名合法，字段层面的缺失/非法也必须被拒。
func signMap(t *testing.T, secret string, mc jwt.MapClaims) string {
	t.Helper()
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, mc).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("构造测试 token 失败：%v", err)
	}
	return signed
}

func TestSignerIssueVerifyRoundTrip(t *testing.T) {
	s := newTestSigner(t)

	before := time.Now()
	token, claims, err := s.Issue(42, "host", 7)
	if err != nil {
		t.Fatalf("Issue() 报错：%v", err)
	}
	if strings.Count(token, ".") != 2 {
		t.Fatalf("JWT 应当是三段式，实际 %q", token)
	}
	if claims.UserID != 42 || claims.Role != "host" || claims.TokenVersion != 7 {
		t.Fatalf("Issue 返回的载荷不符：%+v", claims)
	}
	if claims.JTI == "" {
		t.Error("jti 不能为空（审计与按 token 撤销都依赖它）")
	}
	if d := claims.ExpiresAt.Sub(claims.IssuedAt); d != testTTL {
		t.Errorf("exp-iat 应当等于 TTL %s，实际 %s", testTTL, d)
	}
	if claims.IssuedAt.Before(before.Add(-time.Second)) {
		t.Errorf("iat 应当是签发时刻，实际 %s", claims.IssuedAt)
	}

	got, err := s.Verify(token)
	if err != nil {
		t.Fatalf("Verify() 报错：%v", err)
	}
	if got.UserID != claims.UserID || got.Role != claims.Role ||
		got.TokenVersion != claims.TokenVersion || got.JTI != claims.JTI {
		t.Errorf("往返后的载荷不一致：got %+v want %+v", got, claims)
	}
	if !got.ExpiresAt.Equal(claims.ExpiresAt) || !got.IssuedAt.Equal(claims.IssuedAt) {
		t.Errorf("往返后的时间不一致：got %+v want %+v", got, claims)
	}
}

// 同一载荷两次签发必须得到不同 jti：否则"按 token 追溯一次登录"就退化成按用户追溯。
func TestIssueGivesUniqueJTI(t *testing.T) {
	s := newTestSigner(t)
	seen := make(map[string]bool)
	for i := 0; i < 50; i++ {
		_, claims, err := s.Issue(1, "user", 0)
		if err != nil {
			t.Fatalf("Issue() 报错：%v", err)
		}
		if seen[claims.JTI] {
			t.Fatalf("jti 重复：%q", claims.JTI)
		}
		seen[claims.JTI] = true
	}
}

// 签名密钥由配置持有，而配置在进程重启后不变——所以另一台/另一个进程用同一密钥
// 签发的 token 必须能通过校验（这条是"access token 无状态"的定义）。
func TestVerifyAcceptsTokenFromAnotherSignerWithSameSecret(t *testing.T) {
	a, err := NewSigner(testSecret, testTTL)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewSigner(testSecret, testTTL)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := a.Issue(9, "user", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Verify(token); err != nil {
		t.Fatalf("同密钥的另一个签发器应当能校验：%v", err)
	}
}

func TestNewSignerRejectsBadConfig(t *testing.T) {
	cases := []struct {
		name   string
		secret string
		ttl    time.Duration
	}{
		{"密钥短一个字节", strings.Repeat("x", MinSecretBytes-1), time.Minute},
		{"密钥为空", "", time.Minute},
		{"TTL 为零", testSecret, 0},
		{"TTL 为负", testSecret, -time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewSigner(tc.secret, tc.ttl); err == nil {
				t.Fatalf("应当报错：secret=%d 字节, ttl=%s", len(tc.secret), tc.ttl)
			}
		})
	}

	// 边界值正例：正好 32 字节必须可用。
	if _, err := NewSigner(strings.Repeat("x", MinSecretBytes), time.Minute); err != nil {
		t.Fatalf("正好 %d 字节的密钥应当可用：%v", MinSecretBytes, err)
	}
}

func TestIssueRejectsBadInput(t *testing.T) {
	s := newTestSigner(t)
	if _, _, err := s.Issue(0, "user", 0); err == nil {
		t.Error("userID=0 应当报错")
	}
	if _, _, err := s.Issue(-1, "user", 0); err == nil {
		t.Error("userID 为负应当报错")
	}
	if _, _, err := s.Issue(1, "", 0); err == nil {
		t.Error("空 role 应当报错")
	}
}

// 过期必须与"无效"可区分：前端对前者的正确反应是静默刷新，对后者是重新登录。
func TestVerifyRejectsExpiredToken(t *testing.T) {
	t.Run("极短 TTL", func(t *testing.T) {
		// TTL 取 1 秒而不是毫秒：JWT 的时间戳精度是秒，更短的 TTL 会直接编码成
		// "签发即过期"，那样这条用例就不再证明"时间过去了才失效"。
		s, err := NewSigner(testSecret, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		token, claims, err := s.Issue(1, "user", 0)
		if err != nil {
			t.Fatal(err)
		}

		// 签发瞬间必须仍然可用——否则"延迟到期"这件事本身就没被验证。
		if _, err := s.Verify(token); err != nil {
			t.Fatalf("刚签发的 token 就不可用：%v", err)
		}

		// 睡到 exp 之后：exp 由秒级截断得来，最坏提前 1 秒，1.5 秒足够跨过去。
		time.Sleep(time.Until(claims.ExpiresAt) + 1500*time.Millisecond)

		_, err = s.Verify(token)
		if !errors.Is(err, ErrExpiredToken) {
			t.Fatalf("期望 ErrExpiredToken，实际 %v", err)
		}
		if errors.Is(err, ErrInvalidToken) {
			t.Error("过期不能被归类成 ErrInvalidToken（两者前端处理不同）")
		}
	})

	// 用真密钥手工构造一个 exp 已过去的 token：不依赖 sleep，作为上面那条的确定性补充。
	t.Run("手工构造的过期 token", func(t *testing.T) {
		mc := baseClaims()
		mc["iat"] = jwt.NewNumericDate(time.Now().Add(-10 * time.Minute))
		mc["exp"] = jwt.NewNumericDate(time.Now().Add(-5 * time.Minute))

		_, err := newTestSigner(t).Verify(signMap(t, testSecret, mc))
		if !errors.Is(err, ErrExpiredToken) {
			t.Fatalf("期望 ErrExpiredToken，实际 %v", err)
		}
	})

	// exp 缺失与 exp 过去都是"不能用"，但前者属于结构不合规。
	t.Run("缺少 exp", func(t *testing.T) {
		mc := baseClaims()
		delete(mc, "exp")

		_, err := newTestSigner(t).Verify(signMap(t, testSecret, mc))
		if !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("期望 ErrInvalidToken，实际 %v", err)
		}
	})
}

func TestVerifyRejectsFutureIssuedAt(t *testing.T) {
	mc := baseClaims()
	mc["iat"] = jwt.NewNumericDate(time.Now().Add(time.Hour))
	mc["exp"] = jwt.NewNumericDate(time.Now().Add(2 * time.Hour))

	_, err := newTestSigner(t).Verify(signMap(t, testSecret, mc))
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("iat 在未来必须被判无效，实际 %v", err)
	}
}

func TestVerifyRejectsTamperedSignature(t *testing.T) {
	s := newTestSigner(t)
	token, _, err := s.Issue(42, "user", 0)
	if err != nil {
		t.Fatal(err)
	}

	// 改最后一个字符（保证真的改掉，而不是"原样塞回"）。
	last := token[len(token)-1]
	repl := byte('A')
	if last == 'A' {
		repl = 'B'
	}
	tampered := token[:len(token)-1] + string(repl)

	if _, err := s.Verify(tampered); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("签名被篡改必须拒绝，实际 %v", err)
	}
}

func TestVerifyRejectsTamperedPayload(t *testing.T) {
	s := newTestSigner(t)
	token, _, err := s.Issue(42, "user", 0)
	if err != nil {
		t.Fatal(err)
	}

	// 把 role 改成 admin，签名保持原样——这是"想自助提权"的最短路径。
	// 载荷用 base64url 无填充编码，与 jwt/v5 的写出口径一致。
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token 不是三段式：%q", token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("解码载荷失败：%v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("解析载荷失败：%v", err)
	}
	payload["role"] = "admin"
	patched, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("重新编码载荷失败：%v", err)
	}
	forged := parts[0] + "." + base64.RawURLEncoding.EncodeToString(patched) + "." + parts[2]

	if _, err := s.Verify(forged); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("载荷被改写必须拒绝，实际 %v", err)
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	// 用另一把密钥签发的 token：签名算法完全合法，只有密钥不对。
	forged := signMap(t, otherSecret, baseClaims())

	if _, err := newTestSigner(t).Verify(forged); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("换密钥必须拒绝，实际 %v", err)
	}
}

// alg:none：header 声称不签名，签名段留空。这是最容易"顺手放行"的构造，
// 因为它看起来像"内部可信的 token"。
func TestVerifyRejectsAlgNone(t *testing.T) {
	payload, err := json.Marshal(baseClaims())
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString(payload)

	cases := []struct {
		name   string
		header string
		sig    string
	}{
		{"无签名", `{"alg":"none","typ":"JWT"}`, ""},
		{"带一个假签名", `{"alg":"none","typ":"JWT"}`, base64.RawURLEncoding.EncodeToString([]byte("x"))},
		{"大小写变体", `{"alg":"None","typ":"JWT"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token := base64.RawURLEncoding.EncodeToString([]byte(tc.header)) + "." + enc + "." + tc.sig
			if _, err := newTestSigner(t).Verify(token); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("alg:none 必须拒绝，实际 %v", err)
			}
		})
	}
}

// 算法混淆：header 换一个"同类但不同"的算法（HS384）再用同一把密钥签。
// 允许它就意味着"服务器用什么算法哈希"由客户端决定。
func TestVerifyRejectsAlgorithmConfusion(t *testing.T) {
	cases := []struct {
		name   string
		method jwt.SigningMethod
	}{
		{"HS384", jwt.SigningMethodHS384},
		{"HS512", jwt.SigningMethodHS512},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token, err := jwt.NewWithClaims(tc.method, baseClaims()).SignedString([]byte(testSecret))
			if err != nil {
				t.Fatalf("构造测试 token 失败：%v", err)
			}
			if _, err := newTestSigner(t).Verify(token); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("%s 必须被拒（算法写死为 HS256），实际 %v", tc.name, err)
			}
		})
	}
}

// 缺 claim / claim 非法：签名合法但载荷结构不完整，一律判无效而不是"补零值"。
func TestVerifyRejectsMissingOrInvalidClaims(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(mc jwt.MapClaims)
	}{
		{"缺少 sub", func(mc jwt.MapClaims) { delete(mc, "sub") }},
		{"sub 非数字", func(mc jwt.MapClaims) { mc["sub"] = "alice" }},
		{"sub 为 0", func(mc jwt.MapClaims) { mc["sub"] = "0" }},
		{"sub 为负", func(mc jwt.MapClaims) { mc["sub"] = "-7" }},
		{"sub 是数字类型", func(mc jwt.MapClaims) { mc["sub"] = 42 }},
		{"缺少 role", func(mc jwt.MapClaims) { delete(mc, "role") }},
		{"role 为空串", func(mc jwt.MapClaims) { mc["role"] = "" }},
		{"role 非字符串", func(mc jwt.MapClaims) { mc["role"] = 7 }},
		{"缺少 tv", func(mc jwt.MapClaims) { delete(mc, "tv") }},
		{"tv 非整数", func(mc jwt.MapClaims) { mc["tv"] = 1.5 }},
		{"tv 是字符串", func(mc jwt.MapClaims) { mc["tv"] = "3" }},
		{"缺少 jti", func(mc jwt.MapClaims) { delete(mc, "jti") }},
		{"jti 为空串", func(mc jwt.MapClaims) { mc["jti"] = "" }},
		{"缺少 iat", func(mc jwt.MapClaims) { delete(mc, "iat") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mc := baseClaims()
			tc.mutate(mc)

			_, err := newTestSigner(t).Verify(signMap(t, testSecret, mc))
			if !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("期望 ErrInvalidToken，实际 %v", err)
			}
			if errors.Is(err, ErrExpiredToken) {
				t.Errorf("结构不合规不应当被归类成过期：%v", err)
			}
		})
	}
}

// tv 为 0 是合法值（新用户、从未登出全部设备），不能与"缺失"混淆。
func TestVerifyAcceptsZeroTokenVersion(t *testing.T) {
	mc := baseClaims()
	mc["tv"] = 0

	claims, err := newTestSigner(t).Verify(signMap(t, testSecret, mc))
	if err != nil {
		t.Fatalf("tv=0 应当通过：%v", err)
	}
	if claims.TokenVersion != 0 {
		t.Errorf("tv 应为 0，实际 %d", claims.TokenVersion)
	}
}

func TestVerifyRejectsMalformedToken(t *testing.T) {
	s := newTestSigner(t)
	cases := []struct {
		name  string
		token string
	}{
		{"空串", ""},
		{"只有两段", "aaa.bbb"},
		{"非 base64", "!!!.???.###"},
		{"四段", "a.b.c.d"},
		{"随机文本", "hello world"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Verify(tc.token); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("畸形 token 必须拒绝，实际 %v", err)
			}
		})
	}
}
