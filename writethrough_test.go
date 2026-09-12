package ason

import (
	"runtime"
	"strings"
	"testing"
)

// dropAndRenameProto drops one root-level field and rewrites another: a dropped field is what makes the writer meet a
// gap, which is the point where a chunk stops being a view of its own input.
type dropAndRenameProto struct{ BaseProtocol }

func (dropAndRenameProto) OnKey(t *Transformer) Action {
	if t.Depth() == 1 {
		switch {
		case t.Last() == "model":
			return Capture(64)
		case strings.HasPrefix(t.Last(), "drop"):
			return Skip()
		}
	}
	return Pass()
}

func (dropAndRenameProto) OnValue(t *Transformer, raw []byte) {
	if t.Depth() == 1 && t.Last() == "model" {
		t.W().Key("model")
		t.W().Raw([]byte(`"m1"`))
	}
}

// denseInput is a document that is rewritten throughout: every other field is dropped, so the writer meets a gap every
// few hundred bytes and no pass-through run is ever large. It is the case the write-through must leave alone.
func denseInput() string {
	var b strings.Builder
	b.WriteString(`{"model":"p/m"`)
	for i := 0; i < 400; i++ {
		b.WriteString(`,"drop`)
		b.WriteString(strings.Repeat("d", 3))
		b.WriteString(`":"`)
		b.WriteString(strings.Repeat("q", 120))
		b.WriteString(`","keep`)
		b.WriteString(strings.Repeat("k", 3))
		b.WriteString(`":"`)
		b.WriteString(strings.Repeat("w", 120))
		b.WriteString(`"`)
	}
	b.WriteString("}")
	return b.String()
}

// Writing through must not change a byte of the output: the sink's bytes, in the order they arrive, are what the
// buffered path produces -- for a rewrite at the head, one at the tail of a large chunk, and a document rewritten
// throughout, at every chunk size including the whole body in one piece.
func TestWriteThroughMatchesTheBufferedOutput(t *testing.T) {
	big := strings.Repeat("x", 300<<10)
	ins := []string{
		`{"model":"p/m","dropped":{"a":[1,2,{"b":"c"}]},"messages":[{"role":"user","content":"` + big + `"}],"n":1}` + "\n",
		`{"messages":[{"role":"user","content":"` + big + `"}],"dropped":"` + strings.Repeat("z", 70<<10) + `","model":"p/m"}`,
		`{"a":"` + big + `","dropped":` + strings.Repeat("1", 100<<10) + `,"b":1}`,
		denseInput(),
	}
	for n, in := range ins {
		for _, cs := range []int{1, 7, 4096, 16384, 65536, len(in)} {
			want, ok1, why1 := feedAll(dropAndRenameProto{}.newTransformer(), in, cs)
			got, ok2, why2 := feedSink(dropAndRenameProto{}.newTransformer(), in, cs)
			if ok1 != ok2 || got != want {
				t.Fatalf("input %d chunk=%d: sink and Out differ (ok %v/%v, %s / %s)\n got %d bytes\nwant %d bytes",
					n, cs, ok1, ok2, why1, why2, len(got), len(want))
			}
		}
	}
}

func (p dropAndRenameProto) newTransformer() *Transformer { return NewTransformer(p) }

// A body delivered in one piece must not be copied into a buffer of its own: past the commit point the pass-through
// leaves as a view of the input, so the only buffer taken is sized by the commit window, not by the body.
func TestWriteThroughDoesNotCopyAWholeBody(t *testing.T) {
	body := []byte(`{"model":"p/m","dropped":{"x":1},"messages":[{"role":"user","content":"` + strings.Repeat("y", 1<<20) + `"}],"n":1}`)
	var asked []int
	pieces, out := 0, 0
	tr := NewTransformer(dropAndRenameProto{})
	tr.SetBufferPool(func(n int) []byte { asked = append(asked, n); return make([]byte, 0, n) }, func([]byte) {})
	tr.SetSink(func(b []byte) { pieces++; out += len(b) })
	tr.Write(body)
	tr.Finish()
	if bad, why := tr.Unsupported(); bad {
		t.Fatal(why)
	}
	for _, n := range asked {
		if n > CommitBytes {
			t.Fatalf("asked for %dKB of buffer to transform a %dKB body delivered in one piece", n>>10, len(body)>>10)
		}
	}
	if pieces < 2 {
		t.Fatalf("the body left in %d piece(s): the pass-through was copied instead of handed over", pieces)
	}
	if out < len(body)-4096 {
		t.Fatalf("the sink got %d bytes of a %d byte body", out, len(body))
	}
}

// The same case in bytes allocated: a megabyte body must not allocate anything near its own size.
func TestWriteThroughAllocatesFarLessThanTheBody(t *testing.T) {
	body := []byte(`{"model":"p/m","dropped":{"x":1},"messages":[{"role":"user","content":"` + strings.Repeat("y", 1<<20) + `"}],"n":1}`)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	tr := NewTransformer(dropAndRenameProto{})
	tr.SetSink(func(b []byte) {})
	tr.Write(body)
	tr.Finish()
	runtime.ReadMemStats(&after)
	if bad, why := tr.Unsupported(); bad {
		t.Fatal(why)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > uint64(len(body)/2) {
		t.Fatalf("a %dKB body allocated %dKB", len(body)>>10, grew>>10)
	}
}

// The window still holds: nothing reaches the sink before the commit point, whatever the chunk it arrives in.
func TestWriteThroughHoldsUntilTheCommitPoint(t *testing.T) {
	body := []byte(`{"model":"p/m","dropped":1,"a":"` + strings.Repeat("y", 1<<20) + `"}`)
	for _, cs := range []int{4096, 16384, len(body)} {
		got := 0
		tr := NewTransformer(dropAndRenameProto{})
		tr.SetSink(func(b []byte) { got += len(b) })
		for i := 0; i < len(body); i += cs {
			j := min(i+cs, len(body))
			scanned := j
			tr.Write(body[i:j])
			if scanned < CommitBytes && got != 0 {
				t.Fatalf("chunk=%d: %d bytes released with only %d scanned, window is %d", cs, got, scanned, CommitBytes)
			}
		}
	}
}

// The virtual run and the buffer are alternatives, never both held: a run is handed over or copied before anything is
// written into the buffer, and the buffer is handed over before a run restarts. drain() relies on it -- it takes the
// run and returns -- so a chunk holding both would leave bytes behind. The defensive len(buf) == 0 in materialise is
// not what keeps it true, this is.
func TestVirtualRunAndBufferAreNeverBothHeld(t *testing.T) {
	big := strings.Repeat("x", 300<<10)
	ins := []string{
		`{"model":"p/m","dropped":{"a":[1,2]},"messages":[{"role":"user","content":"` + big + `"}],"n":1}`,
		`{"a":"` + big + `","dropped":"` + strings.Repeat("z", 70<<10) + `","model":"p/m"}`,
		denseInput(),
	}
	for n, in := range ins {
		for _, cs := range []int{4096, 16384, 65536, len(in)} {
			tr := NewTransformer(dropAndRenameProto{})
			check := func(where string) {
				if tr.w.virt && len(tr.w.buf) > 0 {
					t.Fatalf("input %d chunk=%d at %s: a run of %d bytes and %d buffered bytes are both held",
						n, cs, where, tr.w.vlen-tr.w.vfrom, len(tr.w.buf))
				}
			}
			tr.SetSink(func(b []byte) { check("a hand-over") })
			for i := 0; i < len(in); i += cs {
				tr.Write([]byte(in[i:min(i+cs, len(in))]))
				check("the end of a chunk")
			}
			tr.Finish()
			check("Finish")
		}
	}
}
