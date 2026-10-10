package service

import (
	"errors"
	"regexp"
	"strings"
)

// 邮箱的**规范形**与校验（账号按邮箱找回一侧的产品规则）。
//
// 为什么这一层负责正则、而 store 只负责形状：store 关心的是"这一列能不能装下、
// 要不要小写"；"什么算一个合法邮箱"是产品规则 —— 它决定向用户回什么错误，
// 也决定哪些字符串会进入发信链路（进而进入 SMTP 的 RCPT TO 头部）。
// 两个职责分开，才不会出现"store 里塞了一个邮件规则"的越界。

const (
	// MaxEmailLen 与 DDL 的 varchar(254) 一致（RFC 5321 对邮件地址长度的规定值）。
	MaxEmailLen = 254
	// maxEmailLocalLen 是 local-part 的上限（RFC 5321 §4.5.3.1.1 规定 64）。
	maxEmailLocalLen = 64
)

// emailPattern 是邮箱的白名单。
//
// 刻意**不比 RFC 5322 更宽**。能通过 RFC 5322 的字符串里有大量现实中不可能存在的
// 形态（带注释的地址、引号包裹的 local-part、域名里的转义），而放过它们只会让
// "用户填错了邮箱、信却发不出去"变成一条更难查的路径。这个白名单收窄到 ASCII 的常见形态：
//
//	local-part：字母/数字与 . _ % + - （不能以点或符号开头/结尾）
//	domain：标签由字母/数字与 - 组成且不以 - 开头结尾，TLD 至少 2 个字母
//
// 它同时是**邮件头注入的第二道防线**：这个字符集里根本没有 CR / LF / 空格 / 引号，
// 所以一个通过了它的地址不可能在 `To:` 头里塞进第二个头部字段。
var emailPattern = regexp.MustCompile(
	`^[A-Za-z0-9]([A-Za-z0-9._%+\-]*[A-Za-z0-9_%+\-])?@([A-Za-z0-9]([A-Za-z0-9\-]*[A-Za-z0-9])?\.)+[A-Za-z]{2,}$`)

// ErrBadEmail 表示邮箱格式不合法（长度超限、含控制字符/空白、不合白名单）。
//
// 它对外的文案与"账号不存在"完全不同，且**不构成枚举面**：这个判据在查库之前
// 就成立，与库里有没有这个邮箱毫无关系。
var ErrBadEmail = errors.New("account: 邮箱格式不正确")

// CanonicalEmail 校验并归一化邮箱：返回**全小写**的规范形，非法时返回 ErrBadEmail。
//
// 归一化的两条：
//  1. 去首尾空白 + 全小写 —— 与 username 全小写存储同口径，也让
//     users_email_lower_key 这个函数索引的语义（大小写不敏感的唯一性）在写入侧先成立；
//  2. 控制字符与零宽字符（StripControl）**直接拒**，而不是静默剥离。
//     静默剥离会让"用户以为填了 A、服务端存了 B"这件事完全没有痕迹，
//     而邮箱是身份的一部分（"同一个邮箱 = 同一个账号"正是找回功能的全部依据）。
//
// 归一化的结果与 store.canonicalEmail 对同一输入的结果**逐字相同**
// （两者都做"去空白 + 全小写 + 去控制字符"，只是本函数在出现这些字符时选择拒绝）——
// 这是刻意的：校验层与存储层对"规范形"必须只有一种理解。
func CanonicalEmail(email string) (string, error) {
	trimmed := strings.TrimSpace(email)
	if trimmed == "" {
		return "", ErrBadEmail
	}
	// 含空白（含内部空格）或任何控制/零宽字符 → 拒。
	if strings.ContainsAny(trimmed, " \t\r\n\v\f") || trimmed != StripControl(trimmed) {
		return "", ErrBadEmail
	}
	addr := strings.ToLower(trimmed)
	if len(addr) > MaxEmailLen {
		return "", ErrBadEmail
	}
	local, domain, ok := strings.Cut(addr, "@")
	if !ok || local == "" || domain == "" || len(local) > maxEmailLocalLen {
		return "", ErrBadEmail
	}
	if !emailPattern.MatchString(addr) {
		return "", ErrBadEmail
	}
	return addr, nil
}
