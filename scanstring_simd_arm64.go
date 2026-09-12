//go:build goexperiment.simd && arm64 && !purego

package ason

import "simd/archsimd"

// The same scan as the hand-written NEON next door, expressed through simd/archsimd -- and shorter than it, because
// the package reaches two instructions the assembler does not expose. The assembler has no VCMHI, so the assembly
// tests c < 0x20 as VUSHR $5 followed by a compare against zero; and no VUMINV, so it turns a hit into an index with
// a magic mask, two VMOV lane reads and RBIT/CLZ. Through archsimd the compiler emits VCMHI for Less and VUMINV for
// ReduceMin directly, which is the whole argument for preferring the package where it can express the operation.
//
// There is no 32-byte vector here -- arm64 has none in archsimd -- so the step stays 16, as in the assembly.
const vectorStringScan = true

// scanLaneIndex is the lane's own position, so that ReduceMin over "position where a terminator sits, 0xFF where it
// does not" is the index of the first terminator. 0xFF is safe as the miss value because a lane index is 0..15.
var scanLaneIndex = [16]uint8{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}

func scanStringBodyVec(p []byte, i int) int {
	quote := archsimd.BroadcastUint8x16('"')
	esc := archsimd.BroadcastUint8x16('\\')
	ctrl := archsimd.BroadcastUint8x16(0x20)
	miss := archsimd.BroadcastUint8x16(0xFF)
	lanes := archsimd.LoadUint8x16Array(&scanLaneIndex)
	for len(p)-i >= 16 {
		v := archsimd.LoadUint8x16(p[i:])
		m := v.Equal(quote).Or(v.Equal(esc)).Or(v.Less(ctrl))
		if n := lanes.IfElse(m, miss).ReduceMin(); n != 0xFF {
			return i + int(n)
		}
		i += 16
	}
	return i
}
