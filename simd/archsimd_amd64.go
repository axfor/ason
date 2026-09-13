//go:build goexperiment.simd && amd64 && !purego

package simd

import (
	"math/bits"

	"simd/archsimd"
)

// HasVector is true: this build has a vector scan.
const HasVector = true

// Width is the widest vector this build works in, in bytes. archsimd reaches 32 on amd64 through AVX2; whether the
// machine can run it is [HasWide].
const Width = 32

// HasWide reports whether the 32-byte operations may be used. archsimd offers the check and does not perform it --
// doc.go says "It is recommended to check for CPU features before using the corresponding vector operations" -- and
// the floor is AVX2 for the 16-byte operations too, because Uint8x16.Less and BroadcastUint8x16 are both marked
// "Emulated, CPU Feature: AVX2". So this one bit gates the whole archsimd path, and below it the caller falls back
// to the hand-written SSE2 scan.
var HasWide = archsimd.X86.AVX2()

// Uint8x16 is sixteen bytes in a vector register.
type Uint8x16 archsimd.Uint8x16

// Uint8x32 is thirty-two bytes in a vector register. Present only on amd64, and only usable when [HasWide].
type Uint8x32 archsimd.Uint8x32

// Mask8x16 is one boolean per byte of a [Uint8x16].
type Mask8x16 archsimd.Mask8x16

// Mask8x32 is one boolean per byte of a [Uint8x32].
type Mask8x32 archsimd.Mask8x32

// LoadUint8x16 loads sixteen bytes from the front of s, which must have at least that many.
func LoadUint8x16(s []uint8) Uint8x16 { return Uint8x16(archsimd.LoadUint8x16(s)) }

// LoadUint8x32 loads thirty-two bytes from the front of s, which must have at least that many.
func LoadUint8x32(s []uint8) Uint8x32 { return Uint8x32(archsimd.LoadUint8x32(s)) }

// BroadcastUint8x16 repeats x in all sixteen lanes.
//
// Asm: VPBROADCASTB, CPU Feature: AVX2
func BroadcastUint8x16(x uint8) Uint8x16 { return Uint8x16(archsimd.BroadcastUint8x16(x)) }

// BroadcastUint8x32 repeats x in all thirty-two lanes.
//
// Asm: VPBROADCASTB, CPU Feature: AVX2
func BroadcastUint8x32(x uint8) Uint8x32 { return Uint8x32(archsimd.BroadcastUint8x32(x)) }

// Equal reports, per lane, whether x equals y.
//
// Asm: VPCMPEQB, CPU Feature: AVX
func (x Uint8x16) Equal(y Uint8x16) Mask8x16 {
	return Mask8x16(archsimd.Uint8x16(x).Equal(archsimd.Uint8x16(y)))
}

// Less reports, per lane, whether x is below y, unsigned.
//
// Asm: VPCMPEQB+VPMINUB, CPU Feature: AVX2
func (x Uint8x16) Less(y Uint8x16) Mask8x16 {
	return Mask8x16(archsimd.Uint8x16(x).Less(archsimd.Uint8x16(y)))
}

// Equal reports, per lane, whether x equals y.
//
// Asm: VPCMPEQB, CPU Feature: AVX2
func (x Uint8x32) Equal(y Uint8x32) Mask8x32 {
	return Mask8x32(archsimd.Uint8x32(x).Equal(archsimd.Uint8x32(y)))
}

// Less reports, per lane, whether x is below y, unsigned.
//
// Asm: VPCMPEQB+VPMINUB, CPU Feature: AVX2
func (x Uint8x32) Less(y Uint8x32) Mask8x32 {
	return Mask8x32(archsimd.Uint8x32(x).Less(archsimd.Uint8x32(y)))
}

// Or is the lanewise disjunction of x and y.
//
// Asm: VPOR, CPU Feature: AVX
func (x Mask8x16) Or(y Mask8x16) Mask8x16 {
	return Mask8x16(archsimd.Mask8x16(x).Or(archsimd.Mask8x16(y)))
}

// Or is the lanewise disjunction of x and y.
//
// Asm: VPOR, CPU Feature: AVX2
func (x Mask8x32) Or(y Mask8x32) Mask8x32 {
	return Mask8x32(archsimd.Mask8x32(x).Or(archsimd.Mask8x32(y)))
}

// FirstSet is the index of the lowest set lane, or -1 when none is set. On amd64 archsimd hands out the mask as a
// bitmap, so this is a trailing-zero count.
//
// Asm: VPMOVMSKB+TZCNT, CPU Feature: AVX
func (x Mask8x16) FirstSet() int {
	if b := archsimd.Mask8x16(x).ToBits(); b != 0 {
		return bits.TrailingZeros16(b)
	}
	return -1
}

// FirstSet is the index of the lowest set lane, or -1 when none is set.
//
// Asm: VPMOVMSKB+TZCNT, CPU Feature: AVX2
func (x Mask8x32) FirstSet() int {
	if b := archsimd.Mask8x32(x).ToBits(); b != 0 {
		return bits.TrailingZeros32(b)
	}
	return -1
}

// ScanStringBody returns the index of the first byte that ends a JSON string body -- `"`, `\` or anything below
// 0x20 -- at or after i, or any earlier index it reached. Thirty-two bytes a step while that fits, then sixteen for
// the tail; below the AVX2 floor the hand-written SSE2 scan takes over, because archsimd has nothing narrower.
func ScanStringBody(p []byte, i int) int {
	if !HasWide {
		return scanStringBodySSE2(p, i)
	}
	// Constants outside the loops. FirstSet costs nothing extra on amd64 -- it is one VPMOVMSKB and a trailing-zero
	// count, with no constants of its own -- but arm64 and wasm build vectors in theirs, where going through the
	// method put those rebuilds inside the loop and cost 33%. Written the same way here so the three cannot drift.
	q32 := archsimd.BroadcastUint8x32('"')
	b32 := archsimd.BroadcastUint8x32('\\')
	c32 := archsimd.BroadcastUint8x32(0x20)
	for len(p)-i >= 32 {
		v := archsimd.LoadUint8x32(p[i:])
		if mb := v.Equal(q32).Or(v.Equal(b32)).Or(v.Less(c32)).ToBits(); mb != 0 {
			return i + bits.TrailingZeros32(mb)
		}
		i += 32
	}
	q16 := archsimd.BroadcastUint8x16('"')
	b16 := archsimd.BroadcastUint8x16('\\')
	c16 := archsimd.BroadcastUint8x16(0x20)
	for len(p)-i >= 16 {
		v := archsimd.LoadUint8x16(p[i:])
		if mb := v.Equal(q16).Or(v.Equal(b16)).Or(v.Less(c16)).ToBits(); mb != 0 {
			return i + bits.TrailingZeros16(mb)
		}
		i += 16
	}
	return i
}
