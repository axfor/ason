//go:build !goexperiment.simd && arm64 && !purego

package ason

// scanStringBodyNEON scans 16 bytes at a time and returns the index of the first `"`, `\` or control character, or
// the index it stopped at when fewer than 16 bytes remain -- the caller finishes those. Implemented in assembly
// because Go emits no vector instructions of its own; `purego` and every other architecture keep the SWAR loop.
//
//go:noescape
func scanStringBodyNEON(p []byte, i int) int

// vectorStringScan says whether this build has a vector scan for string bodies, so the SWAR path can stay the
// single definition of the contract and the vector one is only ever an accelerator in front of it.
const vectorStringScan = true

func scanStringBodyVec(p []byte, i int) int { return scanStringBodyNEON(p, i) }
