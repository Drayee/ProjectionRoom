package auth

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"ProjectionRoom/internal/config"

	"golang.org/x/crypto/bcrypt"
)

// testCost 用最低允许值（10）而不是 12：本文件验证的是"哈希与校验的语义"，
// 强度由 cost 参数本身决定；用 12 会让每次哈希多花约 200ms，
// 在"每个 cost 都跑一遍"的用例里变成几秒钟的无谓等待。
const testCost = MinBcryptCost

func TestHashPasswordAndVerify(t *testing.T) {
	const plain = "abcd1234"

	hash, err := HashPassword(plain, testCost)
	if err != nil {
		t.Fatalf("HashPassword() 报错：%v", err)
	}
	if hash == plain {
		t.Fatal("哈希不能等于明文")
	}
	if !VerifyPassword(hash, plain) {
		t.Error("正确口令应当通过")
	}
	if VerifyPassword(hash, "abcd12345") {
		t.Error("错误口令必须失败")
	}
	if VerifyPassword(hash, "") {
		t.Error("空口令不能匹配任何哈希")
	}
}

// 同一口令两次哈希必须不同（bcrypt 内置随机盐）。
// 这条守的是"库里两个相同口令的用户一眼可辨"这类信息泄漏。
func TestHashPasswordIsSalted(t *testing.T) {
	const plain = "abcd1234"
	h1, err := HashPassword(plain, testCost)
	if err != nil {
		t.Fatalf("HashPassword() 报错：%v", err)
	}
	h2, err := HashPassword(plain, testCost)
	if err != nil {
		t.Fatalf("HashPassword() 报错：%v", err)
	}
	if h1 == h2 {
		t.Fatal("同一口令两次哈希必须不同（盐未生效）")
	}
	if !VerifyPassword(h1, plain) || !VerifyPassword(h2, plain) {
		t.Error("两个哈希都应能校验通过")
	}
}

// 配置里允许的每一个 cost 都必须能走完"生成 → 校验"。
// 用 t.Parallel 让 5 个 cost 重叠执行，总耗时接近最慢的那个。
func TestHashPasswordAcceptsAllAllowedCosts(t *testing.T) {
	for cost := MinBcryptCost; cost <= MaxBcryptCost; cost++ {
		t.Run(fmt.Sprintf("cost=%d", cost), func(t *testing.T) {
			t.Parallel()

			const plain = "abcd1234"
			hash, err := HashPassword(plain, cost)
			if err != nil {
				t.Fatalf("HashPassword(cost=%d) 报错：%v", cost, err)
			}
			// 不回显 cost 本身，只证明"哈希里记的 cost 就是请求的 cost"：
			// bcrypt 对越界值会静默改用默认值，那正是我们要挡掉的形态。
			got, err := bcrypt.Cost([]byte(hash))
			if err != nil {
				t.Fatalf("bcrypt.Cost() 报错：%v", err)
			}
			if got != cost {
				t.Errorf("哈希里的 cost 应为 %d，实际 %d", cost, got)
			}
			if !VerifyPassword(hash, plain) {
				t.Errorf("cost=%d 生成的哈希应当能校验通过", cost)
			}
			if VerifyPassword(hash, plain+"x") {
				t.Errorf("cost=%d 的哈希不应当接受错误口令", cost)
			}
		})
	}
}

func TestHashPasswordRejectsCostOutOfRange(t *testing.T) {
	cases := []struct {
		name string
		cost int
	}{
		{"低于下限", MinBcryptCost - 1},
		{"高于上限", MaxBcryptCost + 1},
		{"零值", 0},
		{"负数", -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hash, err := HashPassword("abcd1234", tc.cost)
			if err == nil {
				t.Fatalf("cost=%d 应当报错，实际拿到哈希 %q", tc.cost, hash)
			}
			if hash != "" {
				t.Errorf("报错时不应返回哈希，实际 %q", hash)
			}
		})
	}
}

// 存储里出现非 bcrypt 内容（历史脏数据、被改写的行）时必须返回 false，
// 而不是 panic 或"真"。
func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	cases := []struct {
		name string
		hash string
	}{
		{"空哈希", ""},
		{"非 bcrypt 文本", "not-a-bcrypt-hash"},
		{"明文当哈希", "abcd1234"},
		{"前缀错误", "x$2a$10$abcdefghijklmnopqrstuv"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if VerifyPassword(tc.hash, "abcd1234") {
				t.Errorf("畸形哈希 %q 不应当校验通过", tc.hash)
			}
		})
	}
}

func TestValidatePasswordPolicy(t *testing.T) {
	longASCII := strings.Repeat("a1", 36) // 72 字节，正好等于上限
	tooLong := strings.Repeat("a1", 37)   // 74 字节

	cases := []struct {
		name    string
		plain   string
		wantErr error
		wantMsg string
	}{
		{"空口令", "", ErrPasswordEmpty, "空"},
		{"7 个字符含字母数字", "abc1234", ErrPasswordTooShort, "8 个字符"},
		{"纯字母 8 位", "abcdefgh", ErrPasswordNoDigit, "数字"},
		{"纯数字 8 位", "12345678", ErrPasswordNoLetter, "字母"},
		{"纯字母 20 位", "abcdefghijklmnopqrst", ErrPasswordNoDigit, "数字"},
		{"超出 72 字节", tooLong, ErrPasswordTooLong, "72 字节"},
		{"字母加数字 8 位", "abcd1234", nil, ""},
		{"数字在前", "1234abcd", nil, ""},
		{"72 字节边界", longASCII, nil, ""},
		{"汉字口令加数字", "汉字口令abcd1", nil, ""},
		{"重复字母加数字", "aaaaaaaa1", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePasswordPolicy(tc.plain)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("期望通过，实际报错：%v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("期望 %v，实际通过", tc.wantErr)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("期望 errors.Is(err, %v)，实际 %v", tc.wantErr, err)
			}
			// 错误信息必须说清缺哪一条——注册时要原样展示给用户。
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("错误信息应当含 %q，实际 %q", tc.wantMsg, err.Error())
			}
		})
	}
}

// 策略里那串数字在 internal/config 里还有一份（配置校验用的边界）。
// 两个 owner 一旦漂移，症状是"配置接受的值被策略拒绝"或反之——
// 这里用断言把它们钉在一起，谁改都得同时改另一处。
func TestBoundsAgreeWithConfig(t *testing.T) {
	if MinBcryptCost != config.MinBcryptCost || MaxBcryptCost != config.MaxBcryptCost {
		t.Fatalf("bcrypt cost 区间漂移：auth [%d,%d] vs config [%d,%d]",
			MinBcryptCost, MaxBcryptCost, config.MinBcryptCost, config.MaxBcryptCost)
	}
	if MinSecretBytes != config.MinJWTSecretBytes {
		t.Fatalf("JWT 密钥下限漂移：auth %d vs config %d", MinSecretBytes, config.MinJWTSecretBytes)
	}
	if DefaultTicketTTL != config.DefaultWSTicketTTL {
		t.Fatalf("票据 TTL 默认值漂移：auth %s vs config %s", DefaultTicketTTL, config.DefaultWSTicketTTL)
	}
}
