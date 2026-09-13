//go:build !goexperiment.simd && !purego

#include "textflag.h"

// func scanStringBodyAVX2(p []byte, i int) int
//
// scanStringBodySSE2 doubled: 32 bytes per iteration. Same three stopping bytes, same contract -- returns the index
// of the first one, or the index it reached when fewer than 32 bytes remain, leaving that to the caller (whose SSE2
// and word loops finish it). VZEROUPPER before returning, so a caller that goes back to SSE code does not pay the
// transition penalty.
//
// SI p_base, BX p_len, DI i
TEXT ·scanStringBodyAVX2(SB), NOSPLIT, $0-40
	MOVQ p_base+0(FP), SI
	MOVQ p_len+8(FP), BX
	MOVQ i+24(FP), DI

	// VPBROADCASTB from a general register is an AVX-512BW instruction (the assembler encodes it with an EVEX
	// prefix), and this file must run on any machine that merely has AVX2 -- a runner without AVX-512 took SIGILL
	// on exactly that. The AVX2 form broadcasts from an xmm or from memory, so the byte goes through an xmm first.
	MOVL $0x22, AX
	MOVQ AX, X0
	VPBROADCASTB X0, Y0          // '"' in all 32 bytes
	MOVL $0x5C, AX
	MOVQ AX, X1
	VPBROADCASTB X1, Y1          // '\\'
	MOVL $0x1F, AX
	MOVQ AX, X2
	VPBROADCASTB X2, Y2          // 0x1F, for the unsigned "at most" test

loop:
	LEAQ 32(DI), AX
	CMPQ AX, BX
	JA   done                    // fewer than 32 bytes left

	VMOVDQU (SI)(DI*1), Y3
	VPCMPEQB Y0, Y3, Y4          // == '"'
	VPCMPEQB Y1, Y3, Y5          // == '\\'
	VPOR Y5, Y4, Y4
	VPMINUB Y2, Y3, Y6           // min(c, 0x1F)
	VPCMPEQB Y3, Y6, Y6          // == c, i.e. c <= 0x1F
	VPOR Y6, Y4, Y4

	VPMOVMSKB Y4, DX             // one bit per matching byte
	TESTL DX, DX
	JNZ  hit
	MOVQ AX, DI
	JMP  loop

hit:
	TZCNTL DX, DX                // first set bit == first matching byte
	ADDQ DX, DI
	VZEROUPPER
	MOVQ DI, ret+32(FP)
	RET

done:
	VZEROUPPER
	MOVQ DI, ret+32(FP)
	RET
