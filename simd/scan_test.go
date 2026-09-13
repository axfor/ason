package simd

import (
	"bytes"
	"math/rand"
	"testing"
)

// ScanStringBody is an accelerator in front of the caller's own loop, so what it owes the reference is narrower
// than equality: it may stop short, but it must never run past the first terminator, never stop with a full
// vector's worth still to look at, and never return a position that is not a terminator. This test is
// architecture-neutral on purpose -- it is the only thing in this package that covers the archsimd arm64 and wasm
// implementations, which have no assembly and therefore no architecture-specific test of their own.
func TestScanStringBodyHoldsTheContract(t *testing.T) {
	if !HasVector {
		t.Skip("no vector scan in this build")
	}
	check := func(p []byte, start int) {
		got := ScanStringBody(p, start)
		want := scanStringBodyRef(p, start)
		if got > want {
			t.Fatalf("overshot: len=%d start=%d got %d want<=%d\n%q", len(p), start, got, want, p)
		}
		if got < want && len(p)-got >= Width {
			t.Fatalf("stopped with a full vector left: len=%d start=%d got %d want %d width=%d\n%q",
				len(p), start, got, want, Width, p)
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
	for length := 0; length <= 80; length++ {
		p := bytes.Repeat([]byte("x"), length)
		for start := 0; start <= length; start++ {
			check(p, start)
		}
	}
	r := rand.New(rand.NewSource(0xa50f))
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

// HasVector false must still answer legally: the cheapest legal answer is i unchanged.
func TestNoVectorReturnsInputUnchanged(t *testing.T) {
	if HasVector {
		t.Skip("this build has a vector scan")
	}
	p := []byte(`abcdefghijklmnopqrstuvwxyz"tail`)
	for i := 0; i <= len(p); i++ {
		if got := ScanStringBody(p, i); got != i {
			t.Fatalf("i=%d: got %d, want %d", i, got, i)
		}
	}
}
