//go:build (!arm64 && !amd64) || purego

package ason

// No vector scan on this build -- wasip1 among them, where the toolchain emits no SIMD at all -- so the
// word-at-a-time loop is the whole implementation and scanStringBodyVec is never called.
const vectorStringScan = false

func scanStringBodyVec(p []byte, i int) int { return i }
