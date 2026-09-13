package simd

// scanStringBodyRef is the definition every vector scan in this package is held to: one byte at a time, no
// cleverness. Slow on purpose -- the whole value of a reference implementation is that it is obviously right.
func scanStringBodyRef(p []byte, i int) int {
	for ; i < len(p); i++ {
		if c := p[i]; c == '"' || c == '\\' || c < 0x20 {
			return i
		}
	}
	return len(p)
}
