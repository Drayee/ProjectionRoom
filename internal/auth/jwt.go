// Package auth 是账号层的凭据原语：口令哈希、access token（HS256 JWT）与
// WebSocket 一次性票据。它不认识 HTTP，也不碰数据库。
//
// 文件布局：
//
//	password.go  bcrypt 口令哈希与口令策略
//	jwt.go       access token 的签发与校验
//	ticket.go    WebSocket 一次性票据（内存、单次使用）
//
// 为什么把凭据独立成一个包：它与"账号当前是什么状态"是两件事。access token 里的
// role/tv 只是**签发那一刻的快照**，授权前必须与数据库里的 users.token_version
// 比对（封禁 / 改密 / 登出全部设备都靠它即时失效，见 docs/ACCOUNTS.md §5）。
// 分开之后，"token 里的 role 是 admin 所以这个人现在是管理员"这种误用就不会发生。
package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// MinSecretBytes 是 HS256 密钥的最小长度（字节）。
//
// 32 字节 = 256 bit，与 HS256 的输出宽度一致：更短的密钥让离线爆破从"不可能"
// 变成"值得一试"，而密钥一旦可猜，攻击者就能给任意 userID 签发管理员 token。
// 与 config.MinJWTSecretBytes 必须一致，password_test.go 里有断言钉住。
const MinSecretBytes = 32

// 校验失败的两种结果。
//
// 为什么把"过期"从"无效"里分出来：前端对这两者的处理完全不同——过期应当静默
// 走一次刷新，而"无效"要么是攻击，要么是密钥轮换/客户端存了脏 token，必须重新登录。
// 混成一个错误就只能让前端一律踢人。
var (
	ErrExpiredToken = errors.New("auth: access token 已过期")
	ErrInvalidToken = errors.New("auth: access token 无效")
)

// Claims 是 access token 的载荷。
//
// 它只是签发那一刻的快照：UserID 送进 usecase 查库，Role/TokenVersion 与库里的
// 当前值比对后才算授权事实。任何一处直接拿 Role 放行、或跳过 TokenVersion 比对，
// 都会让封禁/改密/登出全部设备失去即时性（ACCOUNTS §5）。
type Claims struct {
	UserID       int64
	Role         string
	TokenVersion int
	JTI          string
	IssuedAt     time.Time
	ExpiresAt    time.Time
}

// Signer 按固定 TTL 签发并校验 HS256 access token。
//
// 它持有密钥，但**不是**授权依据：Verify 只回答"这串 token 是本服务签发的、
// 结构完整且未过期"，不回答"这个账号现在还能不能用"。
type Signer struct {
	secret []byte
	ttl    time.Duration
}

// NewSigner 构造签发器。secret 少于 MinSecretBytes 或 ttl <= 0 时报错。
//
// 为什么不给这两个参数兜底值：密钥太短与 TTL 非正都是配置错误，而它们的症状
// 分别是"全体可被伪造"和"token 一签发就过期"——比启动失败难查得多。宁可拒绝启动。
func NewSigner(secret string, ttl time.Duration) (*Signer, error) {
	if len(secret) < MinSecretBytes {
		return nil, fmt.Errorf("auth: JWT 密钥至少需要 %d 字节，当前 %d 字节", MinSecretBytes, len(secret))
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("auth: access token 有效期必须为正，当前 %s", ttl)
	}
	// 复制一份：调用方拿到的字符串来自配置，之后可能被复用/修改。
	return &Signer{secret: []byte(secret), ttl: ttl}, nil
}

// Issue 为 userID 签发 access token，并同时返回解码后的载荷。
//
// 返回 claims 是为了让调用方能直接用 JTI 写审计、用 ExpiresAt 决定 cookie/响应里的
// 过期时间，而不必自己再解一遍 token（那样会多一条"解出来的和签进去的不一致"的路径）。
// 为此这里把时间戳**截到秒**（JWT 的 NumericDate 精度就是秒）：不截的话 Issue 返回的
// ExpiresAt 带纳秒、而 token 里带的是整秒，同一个问题会有两个答案。
// 截断只会让 token 早过期（最多 1 秒），方向是安全的；副作用是 TTL 小于 1 秒时
// 签发即过期，而真实的 PR_ACCESS_TTL 是分钟量级，不受影响。
func (s *Signer) Issue(userID int64, role string, tokenVersion int) (string, Claims, error) {
	if userID <= 0 {
		return "", Claims{}, fmt.Errorf("auth: userID 必须为正数，当前 %d", userID)
	}
	if role == "" {
		return "", Claims{}, errors.New("auth: role 不能为空")
	}
	// jti 是"这一枚 token 的身份"，用于审计与后续的按 token 撤销；它只需要唯一，
	// 不需要抵抗猜测（短期凭证，且每次登录都换新的）。
	jti, err := newOpaqueToken(jtiBytes)
	if err != nil {
		return "", Claims{}, fmt.Errorf("auth: 生成 jti 失败: %w", err)
	}

	now := time.Now().Truncate(time.Second)
	claims := Claims{
		UserID:       userID,
		Role:         role,
		TokenVersion: tokenVersion,
		JTI:          jti,
		IssuedAt:     now,
		ExpiresAt:    now.Add(s.ttl),
	}

	// sub 用十进制字符串：JWT 规范把 sub 定义成 StringOrURI，写成 JSON 数字会被
	// 一部分库读成 float64（大 id 直接丢精度），而我们这里还要跨版本读回同一个值。
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":  strconv.FormatInt(userID, 10),
		"role": role,
		"tv":   tokenVersion,
		"jti":  jti,
		"iat":  jwt.NewNumericDate(claims.IssuedAt),
		"exp":  jwt.NewNumericDate(claims.ExpiresAt),
	})

	signed, err := tok.SignedString(s.secret)
	if err != nil {
		// 走到这里只可能是"算法与密钥类型不匹配"，属于编码错误，不是用户可见错误。
		return "", Claims{}, fmt.Errorf("auth: 签发 access token 失败: %w", err)
	}
	return signed, claims, nil
}

// Verify 校验算法、签名、exp 与必需 claim，通过后返回载荷。
//
// 拒绝算法混淆（alg 混淆 / alg:none）靠三道互相独立的闸门，任何一道单独成立
// 都不足以放行：
//
//  1. WithValidMethods 把可接受算法**写死**成 ["HS256"]，header 里的 alg 不在
//     这个集合（含 "none"）直接失败——用 header 里攻击者可控的字段去挑算法，
//     正是这类漏洞的根因；
//  2. keyfunc 里再按 jwt.SigningMethodHS256 的**方法对象**比对，而不是比对
//     alg 字符串，避免"字符串相等但实现不同"的构造；
//  3. 交给库的密钥恒为我们的 []byte。HS256 之外的方法（HS384/RS256）在第 1、2
//     道就被拒，因此不存在"把 RSA 公钥当 HMAC 密钥用"的那条经典路径。
//
// 错误只有两种身份：过期是 ErrExpiredToken，其余一律 ErrInvalidToken。底层原因
// 用 %v 拼进文本（便于排障），但不作为可 errors.Is 的目标——否则调用方会开始
// 依赖 jwt/v5 的错误类型，未来换实现就全线崩。
func (s *Signer) Verify(token string) (Claims, error) {
	if token == "" {
		return Claims{}, fmt.Errorf("%w: token 为空", ErrInvalidToken)
	}

	parsed, err := jwt.Parse(token,
		func(t *jwt.Token) (any, error) {
			if t.Method != jwt.SigningMethodHS256 {
				return nil, fmt.Errorf("非预期的签名算法 %v", t.Header["alg"])
			}
			return s.secret, nil
		},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
	)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return Claims{}, fmt.Errorf("%w: %v", ErrExpiredToken, err)
		}
		return Claims{}, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}

	mc, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return Claims{}, fmt.Errorf("%w: 载荷类型异常", ErrInvalidToken)
	}
	return claimsFromMap(mc)
}

// claimsFromMap 把已验签的载荷映射成 Claims，缺字段或不合法一律拒绝。
//
// 为什么必需 claim 要逐个检查而不是"缺了就当零值"：role 空值会让上层把用户当成
// 无角色，tv 的零值会与"库里的 0"意外相等，两者都是**静默**的权限错误；
// 而 JTI 缺失会让审计缺一环。宁可整体判无效，让客户端重新登录。
func claimsFromMap(mc jwt.MapClaims) (Claims, error) {
	sub, _ := mc["sub"].(string)
	if sub == "" {
		return Claims{}, fmt.Errorf("%w: 缺少 sub", ErrInvalidToken)
	}
	userID, err := strconv.ParseInt(sub, 10, 64)
	if err != nil || userID <= 0 {
		return Claims{}, fmt.Errorf("%w: sub 不是合法的用户 id（%q）", ErrInvalidToken, sub)
	}

	role, _ := mc["role"].(string)
	if role == "" {
		return Claims{}, fmt.Errorf("%w: 缺少 role", ErrInvalidToken)
	}

	tv, ok := claimInt(mc["tv"])
	if !ok {
		return Claims{}, fmt.Errorf("%w: 缺少或非法的 tv", ErrInvalidToken)
	}

	jti, _ := mc["jti"].(string)
	if jti == "" {
		return Claims{}, fmt.Errorf("%w: 缺少 jti", ErrInvalidToken)
	}

	iat, err := mc.GetIssuedAt()
	if err != nil || iat == nil {
		return Claims{}, fmt.Errorf("%w: 缺少或非法的 iat", ErrInvalidToken)
	}
	exp, err := mc.GetExpirationTime()
	if err != nil || exp == nil {
		return Claims{}, fmt.Errorf("%w: 缺少或非法的 exp", ErrInvalidToken)
	}

	return Claims{
		UserID:       userID,
		Role:         role,
		TokenVersion: tv,
		JTI:          jti,
		IssuedAt:     iat.Time,
		ExpiresAt:    exp.Time,
	}, nil
}

// claimInt 把 JSON 数字型 claim 转成 int。
//
// 为什么要同时认 float64 与 json.Number：encoding/json 默认把数字解成 float64，
// 而一旦有人给 parser 加上 WithJSONNumber，tv 就变成 json.Number。只认其中一种的
// 后果是"所有 token 突然全部无效"（全站掉线），所以这里显式覆盖两种表示，
// 并在非整数/越界时拒绝（tv 是撤销机制的比较对象，不能容忍静默取整）。
func claimInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		if n != math.Trunc(n) || n < math.MinInt32 || n > math.MaxInt32 {
			return 0, false
		}
		return int(n), true
	case json.Number:
		i, err := strconv.ParseInt(n.String(), 10, 32)
		if err != nil {
			return 0, false
		}
		return int(i), true
	default:
		return 0, false
	}
}
