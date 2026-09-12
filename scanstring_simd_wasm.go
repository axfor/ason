//go:build goexperiment.simd && wasm && !purego

package ason

import (
	"encoding/binary"
	"math/bits"

	"simd/archsimd"
)

// wasm reaches a vector scan through simd/archsimd, which matters more here than anywhere else: this is the shape
// the gateway runs, and until Go 1.27 it had no vector instructions at all. The v128 proposal is what archsimd
// lowers to -- V128Load, I8x16Splat, I8x16Eq, I8x16LtU, V128Or, V128Store -- and a build without the experiment
// gets the word-at-a-time scan instead, unchanged.
//
// Turning the comparison into an index takes a different route again. wasm has no ToBits (that is amd64's
// VPMOVMSKB) and no ReduceMin (that is arm64's VUMINV), so the mask goes to memory once as sixteen 0xFF/0x00
// bytes and is read back as two uint64s: the first non-zero one holds the hit, and its trailing zero count
// divided by eight is the lane. wasm is little-endian, and binary.LittleEndian says so explicitly.
const vectorStringScan = true

func scanStringBodyVec(p []byte, i int) int {
	quote := archsimd.BroadcastUint8x16('"')
	esc := archsimd.BroadcastUint8x16('\\')
	ctrl := archsimd.BroadcastUint8x16(0x20)
	var hit [16]byte
	for len(p)-i >= 16 {
		v := archsimd.LoadUint8x16(p[i:])
		m := v.Equal(quote).Or(v.Equal(esc)).Or(v.Less(ctrl))
		m.ToInt8x16().ToBits().StoreArray(&hit)
		if w := binary.LittleEndian.Uint64(hit[0:8]); w != 0 {
			return i + bits.TrailingZeros64(w)>>3
		}
		if w := binary.LittleEndian.Uint64(hit[8:16]); w != 0 {
			return i + 8 + bits.TrailingZeros64(w)>>3
		}
		i += 16
	}
	return i
}
