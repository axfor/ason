//go:build !arm64 || purego

package ason

// No vector scan on this build (wasip1 among them: the toolchain emits no SIMD for wasm), so the SWAR loop is the
// whole implementation and scanStringBodyVec is never called.
const vectorStringScan = false

func scanStringBodyVec(p []byte, i int) int { return i }
