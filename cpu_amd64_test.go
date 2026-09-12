//go:build amd64 && !purego

package ason

import (
	"bytes"
	"strings"
	"testing"
)

// What the machine turned out to have, printed so a CI log is evidence that the detection ran and which branch the
// vector scan took. A machine without AVX2 is not a failure -- the SSE2 path is the fallback and is exercised
// instead -- but it has to be visible, or "detected at start" is an untested claim.
func TestAVX2DetectionIsVisible(t *testing.T) {
	maxID, _, _, _ := cpuid(0, 0)
	_, _, ecx1, _ := cpuid(1, 0)
	xcr0, _ := xgetbv()
	var ebx7 uint32
	if maxID >= 7 {
		_, ebx7, _, _ = cpuid(7, 0)
	}
	t.Logf("cpuid max leaf %d, OSXSAVE %v, XCR0 %#x, leaf7 EBX %#x -> hasAVX2 %v",
		maxID, ecx1&(1<<27) != 0, xcr0, ebx7, hasAVX2)
	if hasAVX2 {
		t.Log("the vector scan takes the 32-byte AVX2 path, then the 16-byte SSE2 tail")
	} else {
		t.Log("no usable AVX2 here: the vector scan is the 16-byte SSE2 one")
	}
}

// Whichever branch the detection chose, a long string still has to scan to the same place as the reference. This is
// the AVX2 path's own check: with AVX2 present the input is long enough that scanStringBodyAVX2 does the work.
func TestAVX2PathMatchesReference(t *testing.T) {
	for _, n := range []int{31, 32, 33, 63, 64, 65, 127, 128, 129, 1000, 4096} {
		for _, term := range []byte{'"', '\\', 0x00, 0x1f} {
			for pos := 0; pos < n; pos++ {
				p := append([]byte(`{"k":"`), bytes.Repeat([]byte("x"), n)...)
				p = append(p, '"', '}')
				body := 6 // after {"k":"
				p[body+pos] = term
				if got, want := scanStringBody(p, body), scanStringBodyRef(p, body); got != want {
					t.Fatalf("n=%d term=%#x pos=%d: got %d want %d", n, term, pos, got, want)
				}
			}
		}
	}
	// And a long clean string: the scan must land on the closing quote, not before or after it.
	in := []byte(`{"k":"` + strings.Repeat("y", 100000) + `"}`)
	if got, want := scanStringBody(in, 6), 6+100000; got != want {
		t.Fatalf("clean 100KB string: got %d want %d", got, want)
	}
}
