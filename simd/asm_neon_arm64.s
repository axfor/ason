//go:build !purego

#include "textflag.h"

// func scanStringBodyNEON(p []byte, i int) int
//
// The inner loop of scanStringBody in NEON: 16 bytes per iteration where a uint64 holds 8. It stops at the same
// three bytes as the Go version -- `"` (0x22), `\` (0x5C) and anything below 0x20 -- and returns the index of the
// first one. When fewer than 16 bytes remain it returns the index it reached, leaving that tail to the caller.
//
// This used to avoid VCMHI and VUMINV on the belief that the Go assembler had neither, and located a match the way
// the standard library's indexbyte_arm64.s does -- a magic mask, the 64-bit halves out through VMOV, then RBIT and
// CLZ. That belief was wrong: obj/arm64 carries AVCMHI and AVUMINV in a.out.go, their names in anames.go, and
// encoding cases in asm7.go, and archsimd's own output disassembles to exactly these mnemonics. So the sequence
// below is now the same one the compiler emits through archsimd: an unsigned compare for c < 0x20, and a
// horizontal minimum over "this lane's index where it matched, 0xFF where it did not" to find the first match.
//
// R0 p_base, R1 p_len, R3 i
TEXT ·scanStringBodyNEON(SB), NOSPLIT, $0-40
	MOVD p_base+0(FP), R0
	MOVD p_len+8(FP), R1
	MOVD i+24(FP), R3

	// A negative i would be added to the base pointer and loaded from, which reads out of bounds and returns a
	// plausible-looking index rather than failing. The archsimd version cannot do that -- it indexes p[i:] and the
	// compiler's bounds check panics -- so this one bit of defence is what makes the two implementations equally
	// safe, and it costs one compare on a path taken once per long string.
	CMP  $0, R3
	BLT  done

	MOVD $0x22, R4
	VMOV R4, V1.B16              // '"' in every lane
	MOVD $0x5C, R4
	VMOV R4, V2.B16              // '\\' in every lane
	VMOVI $0x20, V3.B16          // 0x20 in every lane, for the unsigned compare below
	VMOVI $0xFF, V4.B16          // the miss value: no lane index can reach it
	MOVD $lanes<>(SB), R5
	VLD1 (R5), [V5.B16]          // 0,1,2,...,15 -- each lane's own index

loop:
	ADD  $16, R3, R6
	CMP  R6, R1
	BLT  done                    // fewer than 16 bytes left
	ADD  R0, R3, R7
	VLD1 (R7), [V0.B16]

	VCMEQ V1.B16, V0.B16, V16.B16    // == '"'
	VCMEQ V2.B16, V0.B16, V17.B16    // == '\\'
	VCMHI V0.B16, V3.B16, V18.B16    // 0x20 > c, unsigned, i.e. c < 0x20
	VORR V17.B16, V16.B16, V16.B16
	VORR V18.B16, V16.B16, V16.B16   // any of the three, 0xFF per matching lane

	VMOV V4.B16, V19.B16             // start from 0xFF in every lane
	VBIT V16.B16, V5.B16, V19.B16    // where the mask is set, take this lane's index
	VUMINV V19.B16, V20              // the smallest of those is the first match, 0xFF if none
	VMOV V20.B[0], R8
	CMP  $0xFF, R8
	BEQ  next                        // no lane matched
	ADD  R3, R8, R3
	MOVD R3, ret+32(FP)
	RET

next:
	MOVD R6, R3
	B    loop

done:
	MOVD R3, ret+32(FP)
	RET

// lanes is each lane's own index, so that a horizontal minimum over "index where matched, 0xFF where not" is the
// position of the first match.
DATA lanes<>+0(SB)/8, $0x0706050403020100
DATA lanes<>+8(SB)/8, $0x0F0E0D0C0B0A0908
GLOBL lanes<>(SB), RODATA|NOPTR, $16
