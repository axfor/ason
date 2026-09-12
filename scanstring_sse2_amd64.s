//go:build !goexperiment.simd && !purego

#include "textflag.h"

// func scanStringBodySSE2(p []byte, i int) int
//
// The inner loop of scanStringBody in SSE2: 16 bytes per iteration where a uint64 holds 8. It stops at the same
// three bytes as the Go version -- `"` (0x22), `\` (0x5C) and anything below 0x20 -- and returns the index of the
// first one. When fewer than 16 bytes remain it returns the index it reached, leaving that tail to the caller.
//
// SSE2 only, so no runtime feature test and no dependency for one: every amd64 has it. PMOVMSKB gives one bit per
// byte directly, so unlike the NEON version there is no per-lane mask to undo -- the bit index is the byte index.
// A control character is found without a signed compare: PMINUB against 0x1F equals the input exactly where the
// byte is <= 0x1F, which is c < 0x20 with unsigned semantics, where PCMPGTB would read 0x80 and up as negative.
//
// SI p_base, BX p_len, DI i
TEXT ·scanStringBodySSE2(SB), NOSPLIT, $0-40
	MOVQ p_base+0(FP), SI
	MOVQ p_len+8(FP), BX
	MOVQ i+24(FP), DI

	MOVQ $0x2222222222222222, AX
	MOVQ AX, X0
	PUNPCKLQDQ X0, X0            // '"' in all 16 bytes
	MOVQ $0x5C5C5C5C5C5C5C5C, AX
	MOVQ AX, X1
	PUNPCKLQDQ X1, X1            // '\\'
	MOVQ $0x1F1F1F1F1F1F1F1F, AX
	MOVQ AX, X2
	PUNPCKLQDQ X2, X2            // 0x1F, for the unsigned "at most" test

loop:
	LEAQ 16(DI), AX
	CMPQ AX, BX
	JA   done                    // fewer than 16 bytes left

	MOVOU (SI)(DI*1), X3
	MOVOU X3, X4
	PCMPEQB X0, X4               // == '"'
	MOVOU X3, X5
	PCMPEQB X1, X5               // == '\\'
	POR X5, X4
	MOVOU X3, X6
	PMINUB X2, X6                // min(c, 0x1F)
	PCMPEQB X3, X6               // == c, i.e. c <= 0x1F
	POR X6, X4

	PMOVMSKB X4, DX              // one bit per matching byte
	TESTL DX, DX
	JNZ  hit
	MOVQ AX, DI
	JMP  loop

hit:
	BSFL DX, DX                  // first set bit == first matching byte
	ADDQ DX, DI
	MOVQ DI, ret+32(FP)
	RET

done:
	MOVQ DI, ret+32(FP)
	RET
