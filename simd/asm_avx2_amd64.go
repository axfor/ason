//go:build !goexperiment.simd && amd64 && !purego

package simd

// This file is what amd64 uses when archsimd is not compiled in at all -- the whole package is behind
// //go:build goexperiment.simd, and a library cannot require its callers to build with an environment variable
// set. It goes away when simd/archsimd leaves the experiment; see the gap list in doc.go.

// HasVector is true: this build has a vector scan, hand-written.
const HasVector = true

// Width is the widest vector this build works in, in bytes.
const Width = 32

// HasWide reports whether the 32-byte scan may be used, detected once at start from CPUID and XGETBV. Unlike the
// archsimd path, the 16-byte scan below it needs no feature test: SSE2 is the amd64 baseline.
var HasWide = hasAVX2

// scanStringBodyAVX2 is scanStringBodySSE2 doubled: thirty-two bytes at a time.
//
//go:noescape
func scanStringBodyAVX2(p []byte, i int) int

// ScanStringBody returns the index of the first byte that ends a JSON string body -- `"`, `\` or anything below
// 0x20 -- at or after i, or any earlier index it reached. The wide scan leaves a tail of up to 31 bytes, so the
// narrow one runs after it and the caller's word loop after that: each is an accelerator in front of the next.
func ScanStringBody(p []byte, i int) int {
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
