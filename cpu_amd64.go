//go:build !goexperiment.simd && amd64 && !purego

package ason

// CPUID and XGETBV, so the package can see whether AVX2 is usable without importing anything: internal/cpu is out
// of reach for a module outside the standard library, and golang.org/x/sys/cpu would be a dependency where there
// are none. Two instructions and twenty lines of assembly cost less than that.
//
//go:noescape
func cpuid(eaxArg, ecxArg uint32) (eax, ebx, ecx, edx uint32)

//go:noescape
func xgetbv() (eax, edx uint32)

// hasAVX2 is settled once at start: the CPU has to report AVX2, and the operating system has to say it saves the
// wide registers -- OSXSAVE in CPUID leaf 1, then XCR0 bits 1 and 2 for the SSE and AVX state. Without that second
// half a process can execute an AVX2 instruction and lose the top half of a register across a context switch.
var hasAVX2 = detectAVX2()

func detectAVX2() bool {
	maxID, _, _, _ := cpuid(0, 0)
	if maxID < 7 {
		return false
	}
	_, _, ecx1, _ := cpuid(1, 0)
	const osxsave = 1 << 27
	if ecx1&osxsave == 0 {
		return false
	}
	xcr0, _ := xgetbv()
	if xcr0&(1<<1) == 0 || xcr0&(1<<2) == 0 {
		return false // the OS does not preserve XMM/YMM state
	}
	_, ebx7, _, _ := cpuid(7, 0)
	const avx2 = 1 << 5
	return ebx7&avx2 != 0
}
