package ason

import (
	"encoding/json"
	"strings"
	"testing"
)

// suspendProto: the value of "fetch" is dropped and the scan suspends there; the test writes the replacement from
// outside the callbacks before resuming, the way a protocol inlines a value it has to fetch.
type suspendProto struct {
	BaseProtocol
	pending  string
	fromTail bool
}

func (p *suspendProto) OnKey(t *Transformer) Action {
	if t.Depth() == 1 && t.Last() == "fetch" {
		return Prefix(1 << 10)
	}
	return Pass()
}

func (p *suspendProto) OnPrefix(t *Transformer, raw []byte, complete bool) (Action, int) {
	p.pending = string(raw)
	t.Suspend()
	return Skip(), 0
}

func (p *suspendProto) Tail(t *Transformer) {
	if p.fromTail {
		t.Suspend()
	}
}

// resolve writes what the fetched value stands for, then resumes.
func resolve(t *Transformer, p *suspendProto, slices int) {
	w := t.W()
	w.Key("fetched")
	w.RawString(`{"url":`)
	w.JSONString(p.pending)
	w.RawString(`,"data":"`)
	for i := 0; i < slices; i++ {
		w.RawString(strings.Repeat("x", 8))
		t.Flush()
	}
	w.RawString(`"}`)
	p.pending = ""
}

func TestSuspendResume(t *testing.T) {
	in := `{"a":1,"fetch":"http://h/one","b":[1,2,{"c":"d"}],"fetch2":"keep","z":null}`
	want := map[string]any{"a": 1.0, "fetched": map[string]any{"url": "http://h/one", "data": strings.Repeat("x", 24)}, "b": []any{1.0, 2.0, map[string]any{"c": "d"}}, "fetch2": "keep", "z": nil}
	for chunk := 1; chunk <= len(in)+1; chunk++ {
		p := &suspendProto{}
		tr := NewTransformer(p)
		tr.SetCommitBytes(1)
		var out []byte
		tr.SetSink(func(b []byte) { out = append(out, b...) })
		suspensions := 0
		for i := 0; i < len(in); i += chunk {
			j := i + chunk
			if j > len(in) {
				j = len(in)
			}
			tr.Write([]byte(in[i:j]))
			for tr.Suspended() {
				suspensions++
				if p.pending != "http://h/one" {
					t.Fatalf("chunk %d: pending %q", chunk, p.pending)
				}
				resolve(tr, p, 3)
				tr.Resume()
			}
			if bad, why := tr.Unsupported(); bad {
				t.Fatalf("chunk %d: bailed: %s", chunk, why)
			}
		}
		tr.Finish()
		if bad, why := tr.Unsupported(); bad {
			t.Fatalf("chunk %d: bailed at finish: %s", chunk, why)
		}
		if suspensions != 1 {
			t.Fatalf("chunk %d: %d suspensions", chunk, suspensions)
		}
		var got map[string]any
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("chunk %d: bad output %q: %v", chunk, out, err)
		}
		gj, _ := json.Marshal(got)
		wj, _ := json.Marshal(want)
		if string(gj) != string(wj) {
			t.Fatalf("chunk %d: got %s want %s", chunk, gj, wj)
		}
	}
}

func TestSuspendKeepsOrderAndAlignment(t *testing.T) {
	in := `{"fetch":"u","k":"v"}`
	p := &suspendProto{}
	tr := NewTransformer(p)
	tr.SetCommitBytes(1)
	tr.Write([]byte(in))
	if !tr.Suspended() {
		t.Fatal("not suspended")
	}
	if tr.Aligned() {
		t.Fatal("a suspended transformer holds bytes and cannot be aligned")
	}
	if got := string(tr.Out()); got != `` { // the lazy writer has not opened the root yet: nothing was written before the suspension
		t.Fatalf("output before the suspension: %q", got)
	}
	resolve(tr, p, 1)
	tr.Resume()
	if tr.Suspended() {
		t.Fatal("still suspended")
	}
	out := string(tr.Out()) + string(tr.Finish())
	if out != `{"fetched":{"url":"u","data":"xxxxxxxx"},"k":"v"}` {
		t.Fatalf("got %q", out)
	}
}

func TestSuspendMisuse(t *testing.T) {
	// Write while suspended
	p := &suspendProto{}
	tr := NewTransformer(p)
	tr.Write([]byte(`{"fetch":"u",`))
	if !tr.Suspended() {
		t.Fatal("not suspended")
	}
	tr.Write([]byte(`"k":1}`))
	if e := tr.Err(); e == nil || e.Code != ErrMisuse {
		t.Fatalf("Write while suspended: %v", e)
	}
	// Finish while suspended
	p = &suspendProto{}
	tr = NewTransformer(p)
	tr.Write([]byte(`{"fetch":"u"}`))
	if !tr.Suspended() {
		t.Fatal("not suspended")
	}
	tr.Finish()
	if e := tr.Err(); e == nil || e.Code != ErrMisuse {
		t.Fatalf("Finish while suspended: %v", e)
	}
	// Suspend from Tail
	p = &suspendProto{fromTail: true}
	tr = NewTransformer(p)
	tr.Write([]byte(`{"k":1}`))
	tr.Finish()
	if e := tr.Err(); e == nil || e.Code != ErrMisuse {
		t.Fatalf("Suspend in Tail: %v", e)
	}
	// Resume without a suspension is a no-op
	tr = NewTransformer(&suspendProto{})
	tr.Write([]byte(`{"k":`))
	tr.Resume()
	tr.Write([]byte(`1}`))
	if out := string(tr.Finish()); out != `{"k":1}` {
		t.Fatalf("got %q", out)
	}
}

func TestSuspendAtChunkEnd(t *testing.T) {
	// the suspension lands exactly on a chunk boundary: nothing is held, the next Write after Resume carries on
	p := &suspendProto{}
	tr := NewTransformer(p)
	tr.SetCommitBytes(1)
	tr.Write([]byte(`{"fetch":"u"`))
	if !tr.Suspended() || len(tr.held) != 0 {
		t.Fatalf("suspended=%v held=%q", tr.Suspended(), tr.held)
	}
	resolve(tr, p, 1)
	tr.Resume()
	tr.Write([]byte(`,"k":2}`))
	out := string(tr.Out()) + string(tr.Finish())
	if out != `{"fetched":{"url":"u","data":"xxxxxxxx"},"k":2}` {
		t.Fatalf("got %q", out)
	}
}
