//go:build goexperiment.simd && amd64 && !purego

package ason

import (
	"math/bits"
	"simd/archsimd"
)

// The same scan as the hand-written SSE2/AVX2 pair, expressed once through simd/archsimd. Where the standard
// library offers the operation, using it beats carrying assembly: the width is chosen from the CPU by archsimd
// itself (Uint8x32 lowers to VPCMPEQB/VPMOVMSKB on Y registers when AVX2 is there), the tail is handled by
// LoadUint8x16Part, and there is no encoding for this package to get wrong -- which is how a VPBROADCASTB from a
// general register once became an AVX-512 instruction and SIGILLed a machine that only had AVX2.
//
// It is behind goexperiment.simd because the package is: a caller who has not enabled the experiment gets the
// hand-written pair instead, with identical behaviour. arm64 and wasm cannot use this route at all -- their
// Mask8x16 has no ToBits, so a comparison cannot be turned into a position -- and keep their own implementations.
const vectorStringScan = true

func scanStringBodyVec(p []byte, i int) int {
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
