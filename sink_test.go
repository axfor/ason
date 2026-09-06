package ason

import (
	"bytes"
	"strings"
	"testing"
)

// feedSink collects the output through a sink; the result must be byte-identical to the Out() path.
func feedSink(tr *Transformer, in string, chunk int) (string, bool, string) {
	var sb bytes.Buffer
	tr.SetSink(func(b []byte) { sb.Write(b) })
	for i := 0; i < len(in); i += chunk {
		j := i + chunk
		if j > len(in) {
			j = len(in)
		}
		tr.Write([]byte(in[i:j]))
		if out := tr.Out(); out != nil {
			panic("Out() should return nothing once a sink is set")
		}
	}
	if out := tr.Finish(); out != nil {
		panic("Finish() should return nothing once a sink is set")
	}
	if u, why := tr.Unsupported(); u {
		return "", false, why
	}
	return sb.String(), true, ""
}

func TestSinkMatchesOut(t *testing.T) {
	big := `{"model":"p/m","messages":[{"role":"user","content":"` + strings.Repeat("x", 300<<10) + `"}],"n":1}` + "\n"
	ins := []string{`{"a":1}`, "{\n \"a\" : [ 1 , 2 ] ,\n \"b\" : { }\n}\n", big}
	mk := []func() *Transformer{
		func() *Transformer { return NewTransformer(BaseProtocol{}) },
		func() *Transformer {
			return NewKeyProbeTransformer(KeyProbeOptions{Keys: map[string]int{"model": 1024},
				OnKey: func(t *Transformer, k string, raw []byte) ([]byte, bool) { return []byte(`"R"`), true }})
		},
	}
	for _, in := range ins {
		for _, m := range mk {
			for _, cs := range []int{1, 7, 4096, 16384, len(in)} {
				want, ok1, _ := feedAll(m(), in, cs)
				got, ok2, why := feedSink(m(), in, cs)
				if ok1 != ok2 || got != want {
					t.Fatalf("chunk=%d: sink and Out differ (ok %v/%v %s)\n got %d bytes\nwant %d bytes", cs, ok1, ok2, why, len(got), len(want))
				}
			}
		}
	}
}

// A bail before the commit point: the sink must not receive a single byte; after it, what was handed over cannot be taken back but nothing more is delivered.
func TestSinkRespectsCommitPoint(t *testing.T) {
	early := `{"a":1,"b":tru}`
	var got bytes.Buffer
	tr := NewTransformer(BaseProtocol{})
	tr.SetSink(func(b []byte) { got.Write(b) })
	tr.Write([]byte(early))
	tr.Finish()
	if got.Len() != 0 {
		t.Fatalf("bailed before the commit point, yet the sink received %d bytes", got.Len())
	}
	late := `{"pad":"` + strings.Repeat("y", 100<<10) + `","b":tru}`
	got.Reset()
	tr = NewTransformer(BaseProtocol{})
	tr.SetSink(func(b []byte) { got.Write(b) })
	for i := 0; i < len(late); i += 4096 {
		j := i + 4096
		if j > len(late) {
			j = len(late)
		}
		tr.Write([]byte(late[i:j]))
	}
	tr.Finish()
	if bad, _ := tr.Unsupported(); !bad || !tr.Committed() {
		t.Fatal("should bail after the commit point")
	}
	if got.Len() == 0 || !strings.HasPrefix(late, got.String()) {
		t.Fatalf("the %d bytes delivered after the commit should be a prefix of the input", got.Len())
	}
}

// The sink path no longer allocates an output buffer per chunk: the allocation count of a 1MB stream does not depend on the chunk count.
func TestSinkReusesBuffer(t *testing.T) {
	in := []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("z", 1<<20) + `"}]}`)
	n := testing.AllocsPerRun(3, func() {
		tr := NewTransformer(BaseProtocol{})
		tr.SetSink(func([]byte) {})
		for i := 0; i < len(in); i += 16384 {
			j := i + 16384
			if j > len(in) {
				j = len(in)
			}
			tr.Write(in[i:j])
		}
		tr.Finish()
	})
	if n > 24 { // pre-commit buffer growth + one chunk-sized reusable buffer + frames / path (17 measured); per-chunk allocation over 64 chunks would far exceed this
		t.Fatalf("the sink path allocates %.0f times per stream, should be independent of the chunk count", n)
	}
}

func BenchmarkSinkPassthrough1MB(b *testing.B) {
	in := []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("z", 1<<20) + `"}]}`)
	b.SetBytes(int64(len(in)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tr := NewTransformer(BaseProtocol{})
		tr.SetSink(func([]byte) {})
		for j := 0; j < len(in); j += 16384 {
			k := j + 16384
			if k > len(in) {
				k = len(in)
			}
			tr.Write(in[j:k])
		}
		tr.Finish()
	}
}

func BenchmarkOutPassthrough1MB(b *testing.B) {
	in := []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("z", 1<<20) + `"}]}`)
	b.SetBytes(int64(len(in)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tr := NewTransformer(BaseProtocol{})
		for j := 0; j < len(in); j += 16384 {
			k := j + 16384
			if k > len(in) {
				k = len(in)
			}
			tr.Write(in[j:k])
			tr.Out()
		}
		tr.Finish()
	}
}
