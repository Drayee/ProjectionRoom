package auth

import (
	"crypto/md5" //nolint:gosec // 只用于校验旧项目的 MD5 口令哈希，不是新的哈希方案
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

// 口令策略与 bcrypt 代价的边界值。
//
// 为什么下限卡在 10、上限卡在 14：低于 10 的 bcrypt 在现代 GPU 上不再构成门槛，
// 而"降成本"最容易发生在性能压力下（登录变慢 → 有人把 cost 调成 4）；
// 高于 14 时单次校验要几百毫秒到一秒，登录接口会变成放大器（一个请求占满一个核）。
// 这两个值与 internal/config 的同名常量必须一致，password_test.go 里有断言钉住。
const (
	MinBcryptCost = 10
	MaxBcryptCost = 14

	// MinPasswordLength 是口令长度下限，按**字符数**（RuneCountInString）而不是
	// 字节数计算：否则一个 4 字汉字口令就有 12 字节，会以"够长"的名义通过，
	// 而它的熵远小于 8 个 ASCII 字符。
	MinPasswordLength = 8

	// MaxPasswordBytes 是口令的字节数上限，等于 bcrypt 的输入上限。
	// 为什么必须有这一条：bcrypt 对超过 72 字节的口令直接返回 ErrPasswordTooLong，
	// 没有上限时长口令会在**注册**这一步抛错（用户看到 500），而不是被策略挡在门口。
	MaxPasswordBytes = 72
)

// 口令策略的哨兵错误。
//
// 为什么一条规则一个哨兵，而不是一句笼统的"口令不合规"：注册时的失败必须
// 说清缺哪一条（ACCOUNTS §5 的"统一文案"只针对**登录**，那是为了不泄漏账号
// 是否存在），而 service 还要按原因分别计数。让调用方用 errors.Is 判断，
// 比解析错误字符串可靠。
var (
	ErrPasswordEmpty    = errors.New("auth: 口令不能为空（游客不需要口令，账号必须设置一个）")
	ErrPasswordTooShort = fmt.Errorf("auth: 口令至少需要 %d 个字符", MinPasswordLength)
	ErrPasswordTooLong  = fmt.Errorf("auth: 口令最多 %d 字节", MaxPasswordBytes)
	ErrPasswordNoLetter = errors.New("auth: 口令必须包含至少一个字母")
	ErrPasswordNoDigit  = errors.New("auth: 口令必须包含至少一个数字")
)

// HashPassword 用 bcrypt 生成口令哈希。
//
// cost 越界时直接报错，不走 bcrypt 的静默兜底：GenerateFromPassword 对小于
// MinCost 的值会**默默换成 10**，于是"配置写错了"表现为"哈希强度悄悄变了"，
// 而且没有任何日志能看出来。
//
// 这里不校验口令是否为空/是否够长：合法性由 ValidatePasswordPolicy 负责，
// 保持"策略"只有一个 owner（注册流程必须先过策略再落库）。
func HashPassword(plain string, cost int) (string, error) {
	if cost < MinBcryptCost || cost > MaxBcryptCost {
		return "", fmt.Errorf("auth: bcrypt cost %d 超出允许区间 [%d,%d]", cost, MinBcryptCost, MaxBcryptCost)
	}

	h, err := bcrypt.GenerateFromPassword([]byte(plain), cost)
	if err != nil {
		return "", fmt.Errorf("auth: 生成口令哈希失败: %w", err)
	}
	return string(h), nil
}

// VerifyPassword 判断明文口令是否与哈希匹配。
//
// 它只回答"这串明文能算出这个哈希"，不回答"这个账号现在能登录"——后者要查库
// （封禁状态、token_version 都在那里）。
//
// 这里不做任何前置短路（例如"hash 为空就直接 false"）：结论只由 bcrypt 内部的
// constant-time 比较给出，任何提前返回都会把"哈希是否畸形/口令是否为空"变成
// 一个可测量的时间差。存储的哈希畸形时 bcrypt 在解析阶段就返回错误，与
// "口令不匹配"走同一条 false 路径，调用方无法从耗时上区分这两者。
//
// ⚠ 本函数只认 bcrypt。旧项目（yachiyo）留下的 32 位十六进制 MD5 走
// IsLegacyMD5Hash + VerifyLegacyMD5 这条独立路径，由 service.Login 显式分流
// （分流与升级见 account.go 的 loginWithLegacyUpgrade）——不在这里做自动识别，
// 是为了让"这是一条会消失的兼容路径"在代码里显眼，而不是一把长期的万能钥匙。
func VerifyPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// legacyMD5HexLen 是旧项目口令哈希的形态：32 个十六进制字符（MD5 摘要）。
const legacyMD5HexLen = 32

// IsLegacyMD5Hash 判断库里存的哈希是否是旧项目的 MD5 形态。
//
// 判据刻意是"形状"而不是"来源标记列"：加一列 hash_scheme 要动 DDL、动
// schema 契约测试，而这条路径的目标是**随迁移完成而消失**，不值得为它留一份
// 永久的表结构债务。形状上的歧义可以排除：bcrypt 哈希以 "$2" 开头且长度 60，
// 与"恰好 32 个 hex 字符"不可能重叠。
//
// 大小写不敏感：迁移数据里同时存在小写与大写 hex（旧项目未归一化）。
func IsLegacyMD5Hash(hash string) bool {
	if len(hash) != legacyMD5HexLen {
		return false
	}
	for i := 0; i < len(hash); i++ {
		c := hash[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// VerifyLegacyMD5 判断明文口令的 MD5 摘要是否与库里的旧哈希一致。
//
// 这是**迁移兼容路径**，不是新的哈希方案：MD5 早已不构成口令存储的防线
// （GPU 上每秒可算数十亿次），它存在的唯一理由是"旧用户能拿原口令登进新系统"。
// 口令一经校验通过，service 层立刻把它换成 bcrypt（见 loginWithLegacyUpgrade），
// 所以每一行的 MD5 只会被读一次。
//
// 比较用 subtle.ConstantTimeCompare：这里比较的是两个 16 字节摘要，
// 时序差异带不出口令信息（摘要已把输入压成定长），但常量时间能免掉
// "逐字节短路"这种不必要的可观测量。
//
// 不做口令长度/策略校验：迁移来的口令本来就是按旧项目的弱策略存的，
// 在这里补校验等于让老用户"登不进来"——策略在**改密**时生效，见 otp.go。
func VerifyLegacyMD5(hash, plain string) bool {
	sum := md5.Sum([]byte(plain)) //nolint:gosec // 兼容旧哈希，不是新的哈希方案
	want, err := hex.DecodeString(strings.ToLower(hash))
	if err != nil || len(want) != len(sum) {
		return false
	}
	return subtle.ConstantTimeCompare(sum[:], want) == 1
}

// ValidatePasswordPolicy 检查口令是否满足最低要求：非空、至少 MinPasswordLength
// 个字符、不超过 bcrypt 的 72 字节上限、同时含字母与数字。
//
// 为什么刻意不要"必须含大写/特殊字符/不得包含用户名"这一套：它们换来的边际熵
// 远小于代价——用户会把口令写在便签上，或者改成 Password1! 这种可预测形态。
// 长度 + 两类字符是"用户不会反抗、又能挡住纯字典口令"的最低门槛；真正的防线
// 是登录限速与统一失败文案（ACCOUNTS §5）。
//
// 检查顺序（空 → 太短 → 太长 → 类别）是有意的：先把最短的那条路告诉用户，
// 让他改一次就能过，而不是改完长度再被告知缺数字。
func ValidatePasswordPolicy(plain string) error {
	if plain == "" {
		return ErrPasswordEmpty
	}
	if utf8.RuneCountInString(plain) < MinPasswordLength {
		return ErrPasswordTooShort
	}
	if len(plain) > MaxPasswordBytes {
		return ErrPasswordTooLong
	}

	var hasLetter, hasDigit bool
	for _, r := range plain {
		switch {
		case unicode.IsLetter(r):
			hasLetter = true
		case unicode.IsDigit(r):
			hasDigit = true
		}
	}
	// 用 unicode 类别而不是 ASCII 白名单：汉字属于 IsLetter，因此"汉字+数字"
	// 的口令能通过。门槛是长度与字符类别数量，不是字符集歧视。
	if !hasLetter {
		return ErrPasswordNoLetter
	}
	if !hasDigit {
		return ErrPasswordNoDigit
	}
	return nil
}
