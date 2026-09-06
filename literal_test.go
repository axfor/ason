package ason

import (
	"encoding/json"
	"strings"
	"testing"
)

// feedAll feeds the input in chunks and finishes; returns (output, passed, bail reason).
func feedAll(tr *Transformer, in string, chunk int) (string, bool, string) {
	var sb strings.Builder
	for i := 0; i < len(in); i += chunk {
		j := i + chunk
		if j > len(in) {
			j = len(in)
		}
		tr.Write([]byte(in[i:j]))
		sb.Write(tr.Out())
	}
	sb.Write(tr.Finish())
	if u, why := tr.Unsupported(); u {
		return "", false, why
	}
	return sb.String(), true, ""
}

// The scanner validates scalar literals / string escapes / whitespace strictly by the JSON grammar:
// input encoding/json rejects must be a bail in streaming (after the fallback the buffered path answers 400),
// whether it lands in a dispatch frame, a Skip region or a Pass region.
func TestStrictLiterals(t *testing.T) {
	bad := map[string]string{
		"nul at top level":                   `{"model":"m","stream":nul,"messages":[{"role":"user","content":"U"}]}`,
		"tru at top level":                   `{"model":"m","stream":tru,"messages":[{"role":"user","content":"U"}]}`,
		"truee":                              `{"model":"m","stream":truee,"messages":[{"role":"user","content":"U"}]}`,
		"leading zero":                       `{"model":"m","max_tokens":01,"messages":[{"role":"user","content":"U"}]}`,
		"1.":                                 `{"model":"m","temperature":1.,"messages":[{"role":"user","content":"U"}]}`,
		"1e":                                 `{"model":"m","temperature":1e,"messages":[{"role":"user","content":"U"}]}`,
		"1e+":                                `{"model":"m","temperature":1e+,"messages":[{"role":"user","content":"U"}]}`,
		"-":                                  `{"model":"m","temperature":-,"messages":[{"role":"user","content":"U"}]}`,
		"+1":                                 `{"model":"m","temperature":+1,"messages":[{"role":"user","content":"U"}]}`,
		".5":                                 `{"model":"m","temperature":.5,"messages":[{"role":"user","content":"U"}]}`,
		"NaN":                                `{"model":"m","temperature":NaN,"messages":[{"role":"user","content":"U"}]}`,
		"inside a Skip region":               `{"model":"m","zzz":{"a":[nul]},"messages":[{"role":"user","content":"U"}]}`,
		"number inside a Skip region":        `{"model":"m","zzz":{"a":1.e5},"messages":[{"role":"user","content":"U"}]}`,
		"inside a Pass region":               `{"model":"m","messages":[{"role":"user","content":"U","zzz":[+1]}]}`,
		"literal inside a Pass region":       `{"model":"m","messages":[{"role":"user","content":"U","zzz":{"a":fals}}]}`,
		"literal at the end":                 `{"model":"m","messages":[{"role":"user","content":"U"}],"zzz":tr}`,
		"invalid escape":                     `{"model":"m","messages":[{"role":"user","content":"a\x"}]}`,
		"short u escape":                     `{"model":"m","messages":[{"role":"user","content":"a\u12G4"}]}`,
		"u escape cut by a quote":            `{"model":"m","messages":[{"role":"user","content":"a\u123"}]}`,
		"control character":                  "{\"model\":\"m\",\"messages\":[{\"role\":\"user\",\"content\":\"a\x01b\"}]}",
		"control character in a Pass region": "{\"model\":\"m\",\"messages\":[{\"role\":\"user\",\"content\":\"U\",\"zzz\":\"a\nb\"}]}",
		"invalid key escape":                 `{"model":"m","messages":[{"role":"user","content":"U","z\q":1}]}`,
		"control character in a key":         "{\"model\":\"m\",\"messages\":[{\"role\":\"user\",\"content\":\"U\",\"z\x02\":1}]}",
		"non-JSON whitespace VT":             "{\"model\":\"m\",\x0b\"messages\":[{\"role\":\"user\",\"content\":\"U\"}]}",
		"non-JSON whitespace FF":             "{\"model\":\"m\",\"messages\":[\x0c{\"role\":\"user\",\"content\":\"U\"}]}",
		"non-JSON whitespace NUL":            "{\"model\":\"m\",\"messages\":[{\"role\":\"user\",\"content\":\"U\"}]\x00}",
	}
	good := map[string]string{
		"number edge cases": `{"model":"m","temperature":-0,"top_p":0.5E+1,"max_tokens":100,"zzz":{"a":-0.0e-0,"b":[1E2,0,-1,12.5e-7]},"messages":[{"role":"user","content":"U"}]}`,
		"escape edge cases": `{"model":"m","messages":[{"role":"user","content":"\u00e9\/\"\\\ud83d\ude00\b\f\n\r\t"}]}`,
		"key escape":        `{"model":"m","messages":[{"role":"user","content":"U","z\u0041\"":1}]}`,
		"JSON whitespace":   "{\t\"model\"\r:\n\"m\" ,\"messages\":[ {\"role\":\"user\",\"content\":\"U\"} ]\n}\n",
	}
	mk := map[string]func() *Transformer{
		"passthrough": func() *Transformer { return NewTransformer(BaseProtocol{}) },
		"capture":     func() *Transformer { return NewTransformer(&captureAllProto{}) },
	}
	for pn, newT := range mk {
		for name, in := range bad {
			if err := json.Unmarshal([]byte(in), new(map[string]any)); err == nil {
				t.Fatalf("case %q should be invalid JSON", name)
			}
			for _, cs := range []int{1, 2, 3, 7, 4096} {
				if out, ok, _ := feedAll(newT(), in, cs); ok {
					t.Errorf("[%s] %s chunk=%d: invalid input was passed: %s", pn, name, cs, out)
				}
			}
		}
		for name, in := range good {
			if err := json.Unmarshal([]byte(in), new(map[string]any)); err != nil {
				t.Fatalf("case %q should be valid JSON: %v", name, err)
			}
			for _, cs := range []int{1, 2, 3, 7, 4096} {
				out, ok, why := feedAll(newT(), in, cs)
				if !ok {
					t.Errorf("[%s] %s chunk=%d: valid input rejected: %s", pn, name, cs, why)
					continue
				}
				if !json.Valid([]byte(out)) {
					t.Errorf("[%s] %s chunk=%d: output is not valid JSON: %s", pn, name, cs, out)
				}
			}
		}
	}
}

// Scalar subkinds: null / bool / number can be told apart in OnStart, so a protocol can treat null as absent and other type mismatches as a fallback.
func TestScalarKinds(t *testing.T) {
	cases := []struct {
		in   string
		want ValueKind
	}{
		{`null`, KindNull}, {`true`, KindBool}, {`false`, KindBool},
		{`0`, KindNumber}, {`-1.5e3`, KindNumber}, {`"s"`, KindString}, {`{}`, KindObject}, {`[]`, KindArray},
	}
	for _, c := range cases {
		var got ValueKind
		p := &kindProbe{on: func(k ValueKind) { got = k }}
		tr := NewTransformer(p)
		_, ok, why := feedAll(tr, `{"k":`+c.in+`}`, 1)
		if !ok {
			t.Fatalf("%s: %s", c.in, why)
		}
		if got != c.want {
			t.Errorf("%s: kind=%v want %v", c.in, got, c.want)
		}
	}
}

type kindProbe struct {
	BaseProtocol
	on func(ValueKind)
}

func (p *kindProbe) OnKey(t *Transformer) Action { return Probe() }
func (p *kindProbe) OnStart(t *Transformer, kind ValueKind) Action {
	p.on(kind)
	return Pass()
}

// captureAllProto Captures every top-level value and writes it back unchanged, so literal validation also covers Capture regions.
type captureAllProto struct{ BaseProtocol }

func (captureAllProto) OnKey(t *Transformer) Action {
	if t.Depth() == 1 {
		return Capture(1 << 20)
	}
	return Pass()
}
func (captureAllProto) OnValue(t *Transformer, raw []byte) {
	w := t.W()
	w.KeyRaw(t.KeyRaw())
	w.Raw(raw)
}
