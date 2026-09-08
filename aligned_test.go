package ason

import (
	"strings"
	"testing"
)

// renameModelProto rewrites the root-level model and passes everything else: the shape of a prefix rewrite.
type renameModelProto struct{ BaseProtocol }

func (renameModelProto) OnKey(t *Transformer) Action {
	if t.Depth() == 1 && t.Last() == "model" {
		return Capture(64)
	}
	return Pass()
}

func (renameModelProto) OnValue(t *Transformer, raw []byte) {
	if t.Depth() == 1 && t.Last() == "model" {
		t.W().Key("model")
		t.W().Raw([]byte(`"m1"`))
	}
}

// Aligned is the contract a caller relies on to stop feeding the transformer and forward the rest of the input
// verbatim. So at every split where it is true -- once the protocol's own rewrite is behind the split, which is
// the caller's side of the bargain -- the output so far plus the raw remainder has to equal the output of
// transforming the whole document; and it has to be true somewhere, or the contract is useless.
func TestAlignedMeansTheRestCanGoVerbatim(t *testing.T) {
	bodies := []struct {
		name   string
		body   []byte
		commit int
	}{
		{"fields after a large one, window commit",
			[]byte(`{"model":"p/m1","messages":[{"role":"user","content":"` + strings.Repeat("y", 70<<10) + `"}] , "stream" : true , "max_tokens":16}` + "\n"), 0},
		{"model after messages, commit on the first byte",
			[]byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("y", 3000) + `"}] , "model" : "p/m1" , "stream":true}`), 1},
	}
	for _, c := range bodies {
		rewriteDone := strings.Index(string(c.body), `"p/m1"`) + len(`"p/m1"`)
		for _, useSink := range []bool{true, false} {
			ref := func() string {
				tr := NewTransformer(renameModelProto{})
				tr.Write(c.body)
				return string(append(append([]byte(nil), tr.Out()...), tr.Finish()...))
			}()
			aligned := 0
			for cut := 1; cut < len(c.body); cut++ {
				tr := NewTransformer(renameModelProto{})
				if c.commit > 0 {
					tr.SetCommitBytes(c.commit)
				}
				var out []byte
				if useSink {
					tr.SetSink(func(b []byte) { out = append(out, b...) })
				}
				tr.Write(c.body[:cut])
				if !useSink {
					out = append(out, tr.Out()...)
				}
				if !tr.Committed() && tr.Aligned() {
					t.Fatalf("%s sink=%v cut=%d: aligned before the commit point", c.name, useSink, cut)
				}
				if !tr.Aligned() || cut < rewriteDone {
					continue // before the rewrite Aligned is about held bytes only, not about what the protocol will still change
				}
				if tr.RootDone() {
					t.Fatalf("%s sink=%v cut=%d: aligned once the root is done", c.name, useSink, cut)
				}
				aligned++
				if got := string(out) + string(c.body[cut:]); got != ref {
					t.Fatalf("%s sink=%v cut=%d: aligned, but output+rest differs from the whole transform:\n got %q\nwant %q",
						c.name, useSink, cut, tail(got), tail(ref))
				}
			}
			if aligned == 0 {
				t.Fatalf("%s sink=%v: Aligned was never true", c.name, useSink)
			}
		}
	}
}

func tail(s string) string {
	if len(s) > 80 {
		return "..." + s[len(s)-80:]
	}
	return s
}
