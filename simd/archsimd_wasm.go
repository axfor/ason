//go:build goexperiment.simd && wasm && !purego

package simd

import (
	"encoding/binary"
	"math/bits"

	"simd/archsimd"
)

// HasVector is true: this build has a vector scan. Before Go 1.27 no wasm build did -- the target emitted no vector
// instructions at all -- and archsimd reaching it is what changed that. Note that v128 is a module-level feature:
// a runtime without the SIMD proposal rejects the whole module rather than faulting on one instruction.
const HasVector = true

// Width is the widest vector this build works in, in bytes. v128 is 128-bit and there is nothing wider.
const Width = 16

// HasWide is false: there is no 32-byte type on wasm.
const HasWide = false

// Uint8x16 is sixteen bytes in a v128 value.
type Uint8x16 archsimd.Uint8x16

// Mask8x16 is one boolean per byte of a [Uint8x16].
type Mask8x16 archsimd.Mask8x16

// LoadUint8x16 loads sixteen bytes from the front of s, which must have at least that many.
//
// Asm: V128Load
func LoadUint8x16(s []uint8) Uint8x16 { return Uint8x16(archsimd.LoadUint8x16(s)) }

// BroadcastUint8x16 repeats x in all sixteen lanes.
//
// Asm: I8x16Splat
func BroadcastUint8x16(x uint8) Uint8x16 { return Uint8x16(archsimd.BroadcastUint8x16(x)) }

// Equal reports, per lane, whether x equals y.
//
// Asm: I8x16Eq
func (x Uint8x16) Equal(y Uint8x16) Mask8x16 {
	return Mask8x16(archsimd.Uint8x16(x).Equal(archsimd.Uint8x16(y)))
}

// Less reports, per lane, whether x is below y, unsigned. One instruction here, where NEON and SSE2 both need two.
//
// Asm: I8x16LtU
func (x Uint8x16) Less(y Uint8x16) Mask8x16 {
	return Mask8x16(archsimd.Uint8x16(x).Less(archsimd.Uint8x16(y)))
}

// Or is the lanewise disjunction of x and y.
//
// Asm: V128Or
func (x Mask8x16) Or(y Mask8x16) Mask8x16 {
	return Mask8x16(archsimd.Mask8x16(x).Or(archsimd.Mask8x16(y)))
}

// FirstSet is the index of the lowest set lane, or -1 when none is set.
//
// wasm has neither ToBits (amd64's VPMOVMSKB) nor ReduceMin (arm64's VUMINV), so the mask goes to memory once as
// sixteen 0xFF/0x00 bytes and comes back as two uint64s: the first non-zero one holds the hit, and its trailing
// zero count divided by eight is the lane. ToInt8x16 turns false into 0 and true into -1, and ToBits reinterprets
// those bits as unsigned, so a set lane reads as 0xFF. wasm is little-endian and binary.LittleEndian says so.
//
// The wasm assembler does have I8x16Bitmask; the day archsimd exposes it, this becomes a trailing-zero count on a
// uint16 like amd64's and the store disappears.
//
// Asm: V128Store
func (x Mask8x16) FirstSet() int {
	var hit [16]byte
	archsimd.Mask8x16(x).ToInt8x16().ToBits().StoreArray(&hit)
	if w := binary.LittleEndian.Uint64(hit[0:8]); w != 0 {
		return bits.TrailingZeros64(w) >> 3
	}
	if w := binary.LittleEndian.Uint64(hit[8:16]); w != 0 {
		return 8 + bits.TrailingZeros64(w)>>3
	}
	return -1
}

// ScanStringBody returns the index of the first byte that ends a JSON string body -- `"`, `\` or anything below
// 0x20 -- at or after i, or any earlier index it reached.
// Sixteen bytes a step -- v128 is 128-bit -- and the tail goes back to the caller.
func ScanStringBody(p []byte, i int) int {
	// Built once outside the loop, including the array the mask is stored through: going via FirstSet instead would
	// make all of it per-iteration, since they are locals of that method. On arm64 the same mistake measured 33% on
	// the long-value shapes. FirstSet remains a public operation for callers who scan once rather than in a loop.
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
