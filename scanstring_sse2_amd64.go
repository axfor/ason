//go:build amd64 && !purego

package ason

// scanStringBodySSE2 scans 16 bytes at a time and returns the index of the first `"`, `\` or control character, or
// the index it stopped at when fewer than 16 bytes remain -- the caller finishes those. In assembly because Go
// emits no vector instructions of its own; SSE2 needs no feature test, so `purego` is the only way to opt out.
//
//go:noescape
func scanStringBodySSE2(p []byte, i int) int

// vectorStringScan says whether this build has a vector scan for string bodies, so the word-at-a-time loop stays
// the single definition of the contract and the vector one is only ever an accelerator in front of it.
const vectorStringScan = true

func scanStringBodyVec(p []byte, i int) int { return scanStringBodySSE2(p, i) }
