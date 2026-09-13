//go:build amd64 && !purego

package simd

// scanStringBodySSE2 scans sixteen bytes at a time and returns the index of the first `"`, `\` or control
// character, or the index it stopped at when fewer than sixteen bytes remain.
//
// This is the one piece of assembly that both configurations compile, and the reason is a feature floor rather
// than a missing operation. archsimd marks Uint8x16.Less and BroadcastUint8x16 "Emulated, CPU Feature: AVX2", so
// its whole 128-bit path is gated on AVX2 -- but the instructions underneath are not. Less expands (in
// compare_gen_amd64.go) to flipping the sign bit and comparing signed, three instructions that SSE2 has had all
// along; the AVX2 requirement arrives through BroadcastInt8x16, which is built from SetElem and broadcast1To2.
// The gate is conservative, not intrinsic. What this file does is take the same 16-byte scan below that gate,
// where SSE2 is the amd64 baseline and needs no feature test at all -- otherwise a machine with SSE2 and no AVX2
// would drop all the way to the caller's word-at-a-time loop. See the gap list in doc.go: it goes away when
// archsimd's 128-bit operations stop requiring AVX2.
//
//go:noescape
func scanStringBodySSE2(p []byte, i int) int
