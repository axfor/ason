//go:build arm64 && !purego

package simd

// arm64 uses this in both configurations, which is the one place the package prefers assembly to simd/archsimd, so
// the reason is worth stating precisely. It is not that archsimd cannot express the scan here -- it can, and did:
// Equal, Less, IfElse and ReduceMin give exactly the same instruction sequence this file emits, VCMEQ VCMHI VORR
// VBIT VUMINV, verified by disassembling both. It is that the loop body costs more when the compiler writes it.
// Counted from the compiler's own -S output: the archsimd loop is 22 instructions where this one is 16, and all six
// extra are scalar -- three to recompute the address of p[i:] where assembly needs one ADD (SUB, AND, ADD), two for
// the bounds check guarding it (CMP and a branch to panicBounds), one MOVBU to zero-extend ReduceMin's uint8, and
// one VMOV of register housekeeping. Per sixteen bytes, so roughly 390k extra instructions on a megabyte. Measured
// over ten runs: 8-11% on the long-value shapes, 0.9-6% on the structural ones, nothing faster.
//
// Worth being exact about what is not the cause, because the obvious answer is wrong and was written here first:
// the function preamble (stack check, frame pointer, morestack) and the inlining that archsimd's larger body
// prevents are one-time costs. On a megabyte this function is entered once and then loops 65536 times, so they
// cannot account for 8% of anything. The whole gap is inside the loop -- which is also why 38% more instructions
// show up as only 8-11% more time: the extras are cheap scalar work that issues alongside the vector ops.
//
// So this is a cost gap, not a capability gap. It closes if the compiler stops emitting a bounds check and an
// address recomputation inside a loop whose bounds it has already proven; until then the archsimd version of this
// scan lives in the package's history rather than its build.
//
// (An earlier version of this comment claimed the Go assembler had neither VCMHI nor VUMINV, which is why this file
// once used VUSHR against zero and a magic mask with RBIT/CLZ. That was wrong -- obj/arm64 has both, in a.out.go,
// anames.go and asm7.go -- and correcting it is what made this file 27-32% faster and worth preferring at all.)

// HasVector is true: this build has a vector scan, hand-written.
const HasVector = true

// Width is the widest vector this build works in, in bytes. NEON is 128-bit.
const Width = 16

// HasWide is false: there is no 32-byte vector on arm64. NEON is the arm64 baseline, so there is nothing to detect.
const HasWide = false

// scanStringBodyNEON scans sixteen bytes at a time and returns the index of the first `"`, `\` or control
// character, or the index it stopped at when fewer than sixteen bytes remain.
//
//go:noescape
func scanStringBodyNEON(p []byte, i int) int

// ScanStringBody returns the index of the first byte that ends a JSON string body -- `"`, `\` or anything below
// 0x20 -- at or after i, or any earlier index it reached.
func ScanStringBody(p []byte, i int) int { return scanStringBodyNEON(p, i) }
