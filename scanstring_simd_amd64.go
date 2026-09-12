//go:build goexperiment.simd && amd64 && !purego

package ason

import (
	"math/bits"
	"simd/archsimd"
)

// The same scan as the hand-written SSE2/AVX2 pair, expressed once through simd/archsimd. Where the standard
// library offers the operation, using it beats carrying assembly: there is no encoding left for this package to
// get wrong, which is how a VPBROADCASTB from a general register once became an AVX-512 instruction and SIGILLed
// a machine that only had AVX2.
//
// What the package does not do is pick an instruction set for you. It emits what the type says and documents what
// that needs -- "Asm: VPCMPEQB, CPU Feature: AVX2" on Uint8x32 -- and doc.go asks the caller to check first. So
// this file checks. One bit, not two, because there is no narrower path here: Uint8x16.Less and BroadcastUint8x16
// are both marked "Emulated, CPU Feature: AVX2", so the 16-byte loop needs exactly what the 32-byte loop needs and
// exists only to finish a tail of 16..31 bytes.
//
// That puts this implementation's floor at AVX2, above the SSE2 it replaces -- SSE2 being the amd64 baseline is
// why the assembly chose it. A machine without AVX2 gets the word-at-a-time scan here, which is correct and
// slower; a caller who wants vectors on such a machine should build without the experiment and get the assembly.
// arm64 and wasm have no such check to make: NEON is the arm64 baseline, and wasm's v128 is a module-level
// feature, so a runtime lacking it rejects the module rather than faulting on one instruction.
//
// It is behind goexperiment.simd because the package is: a caller who has not enabled the experiment gets the
// hand-written pair instead, with identical behaviour. arm64 and wasm reach the same operation through archsimd
// too, but extract the index differently: ToBits is defined only on amd64, so they use VUMINV and a store.
const vectorStringScan = true

// scanHasAVX2 is read once at start. archsimd offers the check; it does not perform it.
var scanHasAVX2 = archsimd.X86.AVX2()

func scanStringBodyVec(p []byte, i int) int {
	if !scanHasAVX2 {
		return i
	}
	q32 := archsimd.BroadcastUint8x32('"')
	b32 := archsimd.BroadcastUint8x32('\\')
	c32 := archsimd.BroadcastUint8x32(0x20)
	for len(p)-i >= 32 {
		v := archsimd.LoadUint8x32(p[i:])
		m := v.Equal(q32).Or(v.Equal(b32)).Or(v.Less(c32))
		if mb := m.ToBits(); mb != 0 {
			return i + bits.TrailingZeros32(mb)
		}
		i += 32
	}
	q16 := archsimd.BroadcastUint8x16('"')
	b16 := archsimd.BroadcastUint8x16('\\')
	c16 := archsimd.BroadcastUint8x16(0x20)
	for len(p)-i >= 16 {
		v := archsimd.LoadUint8x16(p[i:])
		m := v.Equal(q16).Or(v.Equal(b16)).Or(v.Less(c16))
		if mb := m.ToBits(); mb != 0 {
			return i + bits.TrailingZeros16(mb)
		}
		i += 16
	}
	return i
}
