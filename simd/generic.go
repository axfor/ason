//go:build (!amd64 && !arm64 && (!wasm || !goexperiment.simd)) || purego

package simd

// No vector scan in this build: either `purego` was asked for, or this is an architecture with no implementation --
// 386, riscv64, s390x, ppc64le, mips64, loong64, and wasm without the experiment. The caller's word-at-a-time loop
// is then the entire implementation, which costs nothing to arrange because it owns the contract in every build.
const (
	HasVector = false
	Width     = 0
	HasWide   = false
)

// ScanStringBody hands the position straight back, which is the accelerator contract's cheapest legal answer.
func ScanStringBody(p []byte, i int) int { return i }
