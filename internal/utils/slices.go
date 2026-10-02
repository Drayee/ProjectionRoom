package utils

// SameStrings 判断两个字符串切片是否逐元素相同（顺序敏感）。
func SameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// RemoveString 返回去掉首个匹配项的新切片（不改动入参）。
func RemoveString(list []string, target string) []string {
	out := make([]string, 0, len(list))
	for _, item := range list {
		if item != target {
			out = append(out, item)
		}
	}
	return out
}
