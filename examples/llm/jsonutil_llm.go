package llm

import "strconv"

// gjsonBool reproduces gjson.Result.Bool(): the true literal, non-zero numbers, strings accepted by ParseBool.
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

// lower: ASCII lowercase (gjson's true/false check is case-insensitive).
func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
