package llm

import "strconv"

// gjsonBool 复刻 gjson.Result.Bool()：true 字面量、非零数字、可被 ParseBool 的字符串。
func gjsonBool(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	switch raw[0] {
	case 't':
		return string(raw) == "true"
	case '"':
		s, ok := jsonUnquote(raw)
		if !ok {
			return false
		}
		b, err := strconv.ParseBool(lower(s))
		return err == nil && b
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		f, err := strconv.ParseFloat(string(raw), 64)
		return err == nil && f != 0
	}
	return false
}

// lower：ASCII 小写（gjson 的 true/false 判定不区分大小写）。
func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
