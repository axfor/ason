//go:build !goexperiment.simd && amd64 && !purego

package ason

// scanStringBodySSE2 scans 16 bytes at a time and returns the index of the first `"`, `\` or control character, or
// the index it stopped at when fewer than 16 bytes remain -- the caller finishes those. In assembly because Go
// emits no vector instructions of its own; SSE2 needs no feature test, so `purego` is the only way to opt out.
//
//go:noescape
func scanStringBodySSE2(p []byte, i int) int

// scanStringBodyAVX2 is the same, 32 bytes at a time, used when the CPU and the operating system both say AVX2 is
// usable. Detected once at start (see cpu_amd64.go); without it the SSE2 version runs, which every amd64 has.
//
//go:noescape
func scanStringBodyAVX2(p []byte, i int) int

// vectorStringScan says whether this build has a vector scan for string bodies, so the word-at-a-time loop stays
// the single definition of the contract and the vector one is only ever an accelerator in front of it.
const vectorStringScan = true

// scanStringBodyVec dispatches on what the machine turned out to have. The wide version leaves a tail of up to 31
// bytes, so the narrow one runs after it and the word loop after that: each is an accelerator in front of the next,
// and the contract belongs to the word loop alone.
func scanStringBodyVec(p []byte, i int) int {
	if hasAVX2 {
		i = scanStringBodyAVX2(p, i)
		// It either found a terminator or ran out of room for another 32 bytes. When it found one there is nothing
		// left to look at, and handing that position to the narrow scan would load a vector and compute a mask only
		// to return the same index again.
		if i < len(p) {
			if c := p[i]; c == '"' || c == '\\' || c < 0x20 {
				return i
			}
		}
	}
	return scanStringBodySSE2(p, i)
}
