// Package utils 收纳与业务无关的小工具：随机码、切片比较等。
//
// 判定标准：只有"被多个层用到、且不含任何领域知识"的东西才放这里。
// 有业务含义的东西留在 service，别把 utils 变成杂物间。
package utils

import (
	"crypto/rand"
	"fmt"
)

// RoomCodeAlphabet 去掉了容易听错的 I/O/0/1。
// 长度必须是 32 的幂次约束：256 % len == 0，否则取模会引入偏差（RandomCode 会校验）。
const RoomCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// RandomCode 生成 n 位随机码。
func RandomCode(alphabet string, n int) (string, error) {
	if n <= 0 {
		return "", fmt.Errorf("utils: 随机码长度必须为正（%d）", n)
	}
	if len(alphabet) == 0 || len(alphabet) > 256 {
		return "", fmt.Errorf("utils: 字母表长度非法（%d）", len(alphabet))
	}
	if 256%len(alphabet) != 0 {
		return "", fmt.Errorf("utils: 字母表长度 %d 不能整除 256，会产生取模偏差", len(alphabet))
	}

	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("utils: 读取随机源失败: %w", err)
	}
	for i := range buf {
		buf[i] = alphabet[int(buf[i])%len(alphabet)]
	}

	return string(buf), nil
}
