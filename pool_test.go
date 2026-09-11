package ason

import (
	"strings"
	"testing"
)

// A lent buffer goes back once the sink has taken what is in it: transformers written in turn share it, the output is
// the same as without a pool, and between writes a transformer holds none.
func TestBufferPoolIsSharedByTransformersWrittenInTurn(t *testing.T) {
	body := []byte(`{"model":"p/m1","messages":[{"role":"user","content":"` + strings.Repeat("y", 50<<10) + `"}],"stream":true}`)
	ref := func() string {
		tr := NewTransformer(renameModelProto{})
		tr.Write(body)
		return string(append(append([]byte(nil), tr.Out()...), tr.Finish()...))
	}()
	var pool [][]byte
	made, lent := 0, 0
	get := func(n int) []byte {
		lent++
		if k := len(pool); k > 0 {
			b := pool[k-1]
			pool = pool[:k-1]
			return b
		}
		made++
		return make([]byte, 0, n)
	}
	put := func(b []byte) { pool = append(pool, b) }
	const streams = 5
	trs := make([]*Transformer, streams)
	outs := make([][]byte, streams)
	for i := range trs {
		i := i
		trs[i] = NewTransformer(renameModelProto{})
		trs[i].SetCommitBytes(1)
		trs[i].SetBufferPool(get, put)
		trs[i].SetSink(func(b []byte) { outs[i] = append(outs[i], b...) })
	}
	for off := 0; off < len(body); off += 4096 {
		end := min(off+4096, len(body))
		for i, tr := range trs {
			tr.Write(body[off:end])
			if cap(tr.w.buf) != 0 {
				t.Fatalf("stream %d keeps a buffer of %d bytes between writes", i, cap(tr.w.buf))
			}
		}
	}
	for i, tr := range trs {
		tr.Finish()
		if got := string(outs[i]); got != ref {
			t.Fatalf("stream %d: output differs from the unpooled one (%d vs %d bytes)", i, len(got), len(ref))
		}
	}
	if lent == 0 || made > 2 {
		t.Fatalf("lent %d buffers, made %d: written in turn, the streams should share one or two", lent, made)
	}
}

// Compact keeps what a waiting scan has written, not the room of the buffer it was lent.
func TestCompactShrinksALentBuffer(t *testing.T) {
	var back [][]byte
	tr := NewTransformer(renameModelProto{})
	tr.SetBufferPool(func(n int) []byte { return make([]byte, 0, 256<<10) }, func(b []byte) { back = append(back, b) })
	tr.Write([]byte(`{"model":"p/m1","messages":[{"role":"user","content":"short"}`)) // before the commit point: output is held
	held := len(tr.w.buf)
	if held == 0 || cap(tr.w.buf) < 256<<10 {
		t.Fatalf("expected held output in a lent 256KB buffer, have len %d cap %d", held, cap(tr.w.buf))
	}
	tr.Compact()
	if len(tr.w.buf) != held || cap(tr.w.buf) != held {
		t.Fatalf("after Compact len %d cap %d, want both %d", len(tr.w.buf), cap(tr.w.buf), held)
	}
	if len(back) != 1 || cap(back[0]) != 256<<10 {
		t.Fatalf("the lent buffer should have gone back, got %d", len(back))
	}
	tr.Write([]byte(`],"stream":true}`))
	out := string(append(append([]byte(nil), tr.Out()...), tr.Finish()...))
	if !strings.Contains(out, `"model":"m1"`) || !strings.HasSuffix(out, `"stream":true}`) {
		t.Fatalf("output after Compact: %s", out)
	}
}

// Finish sizes the buffer it writes the tail into by the tail, not by the last chunk: no chunk comes in. It used to
// reserve as much as the last chunk -- a large object for every large request -- to write a closing bracket.
func TestFinishDoesNotReserveByTheLastChunk(t *testing.T) {
	for _, model := range []string{"p/m1", "q"} { // renamed, so the chunk was copied; untouched, so it stayed a view
		body := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"` + strings.Repeat("y", 256<<10) + `"}]}`)
		var asked []int
		tr := NewTransformer(renameModelProto{})
		tr.SetCommitBytes(1)
		tr.SetBufferPool(func(n int) []byte { asked = append(asked, n); return make([]byte, 0, n) }, func([]byte) {})
		tr.Write(body)
		tr.Out()
		asked = asked[:0]
		tr.Finish()
		for _, n := range asked {
			if n > 4<<10 {
				t.Fatalf("model %q: Finish asked for %dKB to write the tail of a %dKB request", model, n>>10, len(body)>>10)
			}
		}
	}
}
