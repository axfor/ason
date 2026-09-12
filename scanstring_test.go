package ason

import (
	"bytes"
	"math/rand"
	"testing"
)

// scanStringBodyRef is the obvious implementation: one byte at a time, no words, no vectors. Every faster path --
// the SWAR loop today, an assembly one tomorrow -- has to agree with it on every input, which is what these check.
func scanStringBodyRef(p []byte, i int) int {
	for ; i < len(p); i++ {
		if c := p[i]; c == '"' || c == '\\' || c < 0x20 {
			return i
		}
	}
	return i
}

// Every alignment and length around the word boundary, with the terminator at every position: a vectorised scan that
// is off by one lane, or that reads past the end of a short tail, fails here rather than in a document somewhere.
func TestScanStringBodyMatchesReference(t *testing.T) {
	for _, term := range []byte{'"', '\\', 0x00, 0x1f, 0x0a} {
		for length := 0; length <= 80; length++ {
			for pos := 0; pos < length; pos++ {
				p := bytes.Repeat([]byte("x"), length)
				p[pos] = term
				for start := 0; start <= length; start++ {
					if got, want := scanStringBody(p, start), scanStringBodyRef(p, start); got != want {
						t.Fatalf("term=%#x len=%d pos=%d start=%d: got %d want %d", term, length, pos, start, got, want)
					}
				}
			}
		}
	}
}

// No terminator at all: the scan must reach exactly len(p) from any start, never past it.
func TestScanStringBodyRunsToTheEnd(t *testing.T) {
	for length := 0; length <= 200; length++ {
		p := bytes.Repeat([]byte("y"), length)
		for start := 0; start <= length; start++ {
			if got := scanStringBody(p, start); got != length {
				t.Fatalf("len=%d start=%d: got %d want %d", length, start, got, length)
			}
		}
	}
}

// Random bytes, including every high byte and every control character, at random starts: the reference decides.
func TestScanStringBodyRandomAgreesWithReference(t *testing.T) {
	r := rand.New(rand.NewSource(0x5ca1ab1e))
	for n := 0; n < 20000; n++ {
		p := make([]byte, r.Intn(130))
		for i := range p {
			p[i] = byte(r.Intn(256))
		}
		start := 0
		if len(p) > 0 {
			start = r.Intn(len(p) + 1)
		}
		if got, want := scanStringBody(p, start), scanStringBodyRef(p, start); got != want {
			t.Fatalf("len=%d start=%d: got %d want %d\n%q", len(p), start, got, want, p)
		}
	}
}

// The UTF-8 variant stops at the first byte >= 0x80 as well; same contract, same reference plus that rule.
func TestScanStringBodyUTF8MatchesReference(t *testing.T) {
	ref := func(p []byte, i int) int {
		for ; i < len(p); i++ {
			if c := p[i]; c == '"' || c == '\\' || c < 0x20 || c >= 0x80 {
				return i
			}
		}
		return i
	}
	r := rand.New(rand.NewSource(0xf00d))
	for n := 0; n < 20000; n++ {
		p := make([]byte, r.Intn(130))
		for i := range p {
			p[i] = byte(r.Intn(256))
		}
		start := 0
		if len(p) > 0 {
			start = r.Intn(len(p) + 1)
		}
		if got, want := scanStringBodyUTF8(p, start), ref(p, start); got != want {
			t.Fatalf("len=%d start=%d: got %d want %d\n%q", len(p), start, got, want, p)
		}
	}
}
