//go:build !goexperiment.simd && !purego

#include "textflag.h"

// func scanStringBodyNEON(p []byte, i int) int
//
// The inner loop of scanStringBody in NEON: 16 bytes per iteration where a uint64 holds 8. It stops at the same
// three bytes as the Go version -- `"` (0x22), `\` (0x5C) and anything below 0x20 -- and returns the index of the
// first one. When fewer than 16 bytes remain it returns the index it reached, leaving that tail to the caller.
//
// Only instructions the Go assembler accepts on arm64 are used. A match is located the way the standard library's
// indexbyte_arm64.s does it -- a per-lane mask, then the 64-bit halves out through VMOV, then RBIT + CLZ for the
// first set bit -- with one change: the mask is 0x08040201 rather than 0x40100401, which puts the four bytes of a
// 32-bit lane eight bits apart instead of ten, so the bit index is the byte index shifted by three and no division
// is needed. (Measured: with 0x40100401 the bits land at 0, 10, 20, 30 within a lane.)
// A control character is found without an unsigned compare, which the assembler has no mnemonic for: c>>5 == 0
// holds exactly when c < 0x20, so VUSHR by 5 and compare against zero.
//
// R0 p_base, R1 p_len, R3 i
TEXT ·scanStringBodyNEON(SB), NOSPLIT, $0-40
	MOVD p_base+0(FP), R0
	MOVD p_len+8(FP), R1
	MOVD i+24(FP), R3

	MOVD $0x22, R4
	VMOV R4, V1.B16              // '"' in every lane
	MOVD $0x5C, R4
	VMOV R4, V2.B16              // '\\' in every lane
	VMOVI $0, V3.B16             // zero, for the control-character test
	MOVD $0x08040201, R5
	VMOV R5, V5.S4               // one bit per byte, eight bits apart, so the bit index is the byte index times 8

loop:
	ADD  $16, R3, R6
	CMP  R6, R1
	BLT  done                    // fewer than 16 bytes left
	ADD  R0, R3, R7
	VLD1 (R7), [V0.B16]

	VCMEQ V1.B16, V0.B16, V16.B16    // == '"'
	VCMEQ V2.B16, V0.B16, V17.B16    // == '\\'
	VUSHR $5, V0.B16, V18.B16        // c >> 5
	VCMEQ V3.B16, V18.B16, V18.B16   // (c>>5) == 0, i.e. c < 0x20
	VORR V17.B16, V16.B16, V16.B16
	VORR V18.B16, V16.B16, V16.B16   // any of the three, 0xFF per matching lane

	VAND V5.B16, V16.B16, V16.B16    // one distinct bit per lane
	VMOV V16.D[0], R8
	VMOV V16.D[1], R9
	ORR  R9, R8, R10
	CBZ  R10, next                   // no lane matched
	CBZ  R8, high

	RBIT R8, R11
	CLZ  R11, R11                    // bit index of the first match in the low half
	LSR  $3, R11, R11                // eight bits per byte in the syndrome
	ADD  R3, R11, R3
	MOVD R3, ret+32(FP)
	RET

high:
	RBIT R9, R11
	CLZ  R11, R11
	LSR  $3, R11, R11
	ADD  $8, R11, R11                // the high half covers bytes 8..15
	ADD  R3, R11, R3
	MOVD R3, ret+32(FP)
	RET

next:
	MOVD R6, R3
	B    loop

done:
	MOVD R3, ret+32(FP)
	RET
