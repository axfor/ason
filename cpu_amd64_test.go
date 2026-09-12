//go:build !goexperiment.simd && amd64 && !purego

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

// The AVX2 function called directly, not through the dispatch, so that its instructions are executed on any machine
// that reports AVX2 rather than only when some other test happens to feed a long enough string. The first version of
// this file built its constants with VPBROADCASTB from a general register, which is an AVX-512 encoding: on a runner
// with AVX2 and no AVX-512 the first instruction of the function was illegal, and it was an unrelated test that
// happened to reach it. A direct call makes that coverage deliberate.
func TestAVX2FunctionExecutesWhenAvailable(t *testing.T) {
	if !hasAVX2 {
		t.Skip("no usable AVX2 on this machine; the SSE2 path is covered by the shared contract test")
	}
	for _, n := range []int{32, 64, 129, 1000, 70000} {
		p := append([]byte(nil), bytes.Repeat([]byte("z"), n)...)
		p = append(p, '"')
		// The wide scan steps 32 bytes at a time and returns where it stopped when fewer than 32 remain, so it lands
		// exactly on the quote only when the content is a multiple of 32. Otherwise it must stop short of it, never
		// past it, and the caller's narrower loops finish the tail.
		got := scanStringBodyAVX2(p, 0)
		switch {
		case got > n:
			t.Fatalf("n=%d: overshot the quote, stopped at %d", n, got)
		case n%32 == 0 && got != n:
			t.Fatalf("n=%d (a whole number of vectors): stopped at %d, want the quote at %d", n, got, n)
		case n-got >= 32:
			t.Fatalf("n=%d: stopped at %d with %d bytes still to scan, which is a full vector or more", n, got, n-got)
		}
		for pos := 0; pos < n; pos++ {
			q := append([]byte(nil), p...)
			q[pos] = '\\'
			got := scanStringBodyAVX2(q, 0)
			if got > pos {
				t.Fatalf("n=%d pos=%d: overshot the terminator, stopped at %d", n, pos, got)
			}
			// A terminator inside the first vector must be found exactly; past that the scan may stop at a vector
			// boundary before it, as long as it did not leave a whole vector unscanned.
			if pos < 32 && n >= 32 && got != pos {
				t.Fatalf("n=%d pos=%d: got %d want %d", n, pos, got, pos)
			}
			if pos-got >= 32 {
				t.Fatalf("n=%d pos=%d: stopped at %d, leaving a full vector before the terminator", n, pos, got)
			}
			if pos > 64 {
				break // enough positions per length; the contract test covers the rest exhaustively
			}
		}
	}
}
