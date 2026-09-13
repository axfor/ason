// Package simd is the vector layer the scanner sits on. It exists to keep one rule in one place: prefer
// simd/archsimd wherever it can express the operation, and hand-write only what it cannot.
//
// # The goal is to disappear
//
// archsimd is an official package and will keep filling in. This package is therefore written to be deleted in
// pieces: every file that exists only because archsimd is missing something carries the condition under which it
// becomes redundant, so the gaps can be closed one at a time rather than re-litigated. What callers see --
// [HasVector] and [ScanStringBody] -- does not change when a gap closes, which is the point of having a layer here
// at all. The end state is this package forwarding to archsimd on every architecture, with no assembly left.
//
// # What is stable here and what is not
//
// [HasVector] and [ScanStringBody] are the supported surface: they keep their meaning as the gaps close, and the
// scan's contract (below) will not tighten or loosen underneath a caller.
//
// The vector types and their methods -- [Uint8x16], [Mask8x16], their loads, broadcasts and comparisons -- are
// not. They exist because [ScanStringBody] is built from them where archsimd is available, and they are shaped to
// mirror archsimd so the mirroring layer can be dropped. They are also not uniformly present: which of them exist
// at all depends on the architecture and on whether the build has archsimd, so code written against them will not
// compile everywhere. archsimd's own documentation asks callers not to expose its SIMD types in public APIs, and
// that request propagates through anything that forwards them, including this. Depend on the two names above;
// treat the rest as the implementation of them.
//
// # Why this is not in internal/
//
// Because the two supported names are worth reusing: any scanner that needs "find the first byte of interest in a
// long run" has the same problem and the same reason to prefer archsimd over assembly. The narrow surface is what
// makes that safe -- the unstable half is documented as unstable rather than hidden by the import path.
//
// Current gaps, each with the condition that closes it:
//
//   - Taking a position out of a comparison. Mask8x32.ToBits and Mask8x16.ToBits are defined only in
//     types_amd64.go. arm64 reduces instead (VUMINV) and wasm has neither, so [Mask8x16.FirstSet] is this
//     package's own abstraction, implemented three ways. Closes when ToBits exists on all three -- on wasm the
//     instruction is already there, I8x16Bitmask, just not exposed.
//   - The amd64 feature floor. archsimd gates its 128-bit operations on AVX2 -- Uint8x16.Less and
//     BroadcastUint8x16 are both marked "Emulated, CPU Feature: AVX2" -- but the instructions underneath are not
//     AVX2-only: Less expands to a sign-bit flip and a signed compare, which SSE2 has always had, and the AVX2
//     requirement arrives through BroadcastInt8x16's use of SetElem. The gate is conservative rather than
//     intrinsic, so hand-written SSE2 takes the same scan below it. Closes when archsimd's 128-bit operations
//     stop requiring AVX2.
//   - arm64 preferring assembly. Not a capability gap: archsimd expresses this scan on arm64 and emits the same
//     vector sequence, VCMEQ VCMHI VORR VBIT VUMINV. A cost gap -- its loop body is 22 instructions against the
//     assembly's 16, the six extra being the slice recomputation and bounds check around p[i:], a zero-extension
//     and a register move, all per sixteen bytes. Measured at 8-11% on long values, nothing faster anywhere.
//     Closes if the compiler stops needing those inside a loop whose bounds it has already proven; the full
//     accounting is in asm_neon_arm64.go.
//   - The experiment gate. The whole of archsimd is behind //go:build goexperiment.simd, and a library cannot
//     require its callers to build with an environment variable set, so the default build has no archsimd at all
//     and hand-written NEON and SSE2 carry it. Closes when simd/archsimd leaves the experiment.
//   - Architectures archsimd does not cover -- 386, riscv64, s390x, ppc64le, mips64, loong64 -- where
//     [HasVector] is false and the caller's word loop is the whole implementation. Closes per architecture as
//     archsimd grows types for them.
//
// # Why the hand-written side is not a drop-in for the archsimd side
//
// The two layers here are shaped differently on purpose. archsimd's fine-grained API is free because its methods
// are compiler intrinsics: LoadUint8x16 followed by Equal followed by Or emits three instructions and no calls.
// Assembly cannot be inlined, so expressing the same scan as calls to hand-written Load/Equal/Or primitives would
// pay a call per step and lose what the vectors bought. The fine-grained types therefore exist only where archsimd
// does, and [ScanStringBody] is the seam: built from those types where they exist, and one contiguous piece of
// assembly where they do not.
//
// # The contract
//
// [HasVector] says whether this build has any vector scan.
//
// [ScanStringBody] is named for the job it does rather than for a generic operation, and the name is the honest
// one: the three bytes it stops at -- `"` (0x22), `\` (0x5C), and anything below 0x20 -- are exactly the bytes
// that end the plain run of a JSON string body per RFC 8259. It is a scan for that grammar, not a general
// "find any of these three bytes" primitive, and it is not parameterised because the constants are what let the
// comparison vectors be built once outside the loop.
//
// It may stop early: it returns the index of the first such byte at or after i, or any earlier index it happened
// to reach -- when fewer than one vector's worth of bytes remain, for instance. That is deliberate. The caller's
// word-at-a-time loop owns the semantics and this package only ever accelerates, so stopping short is always a
// legal answer and the caller must be written to continue from wherever it stopped. When [HasVector] is false,
// ScanStringBody returns i unchanged, which is that same contract at its cheapest.
//
// Callers must not take the address of a vector value, put it in an aggregate, or let it escape to the heap;
// archsimd's own documentation asks the same, for the same reason.
//
// The types here are defined types (type Uint8x16 archsimd.Uint8x16) rather than structs wrapping a vector, which
// keeps them out of the aggregate the documentation warns about and converts for free.
//
// A word on what that did and did not buy, because I got this wrong once and the wrong version was in this comment.
// Moving the scan behind this package cost 33% on the long-value shapes (longstring 1MB whole: 70.85us with the
// loop written directly against archsimd, 94.56us through here). I assumed the struct wrapper was the cause, since
// the documentation warns against aggregates; changing it to a defined type recovered 0.4%, which is noise. The
// cause is elsewhere -- see the comment on ScanStringBody about where the comparison vectors are built.
package simd
