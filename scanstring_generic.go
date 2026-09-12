//go:build (!arm64 && !amd64 && (!wasm || !goexperiment.simd)) || purego

package ason

// No vector scan on this build -- the purego tag, or an architecture with no implementation -- so the
// word-at-a-time loop is the whole implementation and scanStringBodyVec is never called.
const vectorStringScan = false

func scanStringBodyVec(p []byte, i int) int { return i }
