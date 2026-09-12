//go:build (arm64 || amd64) && !purego

package ason

import (
	"bytes"
	"math/rand"
	"testing"
)

// The vector scan -- NEON on arm64, SSE2 on amd64 -- is an accelerator in front of scanStringBody's contract, not a
// second definition of it, so what it owes the reference is narrower than equality: it may stop short (leaving the tail to the loops after it) but
// it must never run past the first terminator, never stop with a full sixteen bytes still to look at, and never
// return a position that is not a terminator. Checked at every alignment and length around the vector width, with
// each of the three terminators at each position, and then on random bytes including every high byte.
func TestVectorScanHoldsTheContract(t *testing.T) {
	check := func(p []byte, start int) {
		got := scanStringBodyVec(p, start)
		want := scanStringBodyRef(p, start)
		if got > want {
			t.Fatalf("overshot: len=%d start=%d got %d want<=%d\n%q", len(p), start, got, want, p)
		}
		if got < want && len(p)-got >= 16 {
			t.Fatalf("stopped early with a full word left: len=%d start=%d got %d want %d\n%q", len(p), start, got, want, p)
		}
		if got < len(p) && got == want {
			if c := p[got]; !(c == '"' || c == '\\' || c < 0x20) {
				t.Fatalf("returned a non-terminator %#x at %d", c, got)
			}
		}
	}
	for _, term := range []byte{'"', '\\', 0x00, 0x1f, 0x0a} {
		for length := 0; length <= 80; length++ {
			for pos := 0; pos < length; pos++ {
				p := bytes.Repeat([]byte("x"), length)
				p[pos] = term
				for start := 0; start <= length; start++ {
					check(p, start)
				}
			}
		}
	}
	r := rand.New(rand.NewSource(0xbeef))
	for n := 0; n < 20000; n++ {
		p := make([]byte, r.Intn(200))
		for i := range p {
			p[i] = byte(r.Intn(256))
		}
		start := 0
		if len(p) > 0 {
			start = r.Intn(len(p) + 1)
		}
		check(p, start)
	}
}
