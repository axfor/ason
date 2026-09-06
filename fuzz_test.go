package ason

import (
	"encoding/json"
	"testing"
	"unicode/utf8"
)

// Native fuzzing: passthrough must be byte-identical for valid JSON; the rejection surface matches encoding/json (UTF-8 unchecked by default, like it); never panics.
func FuzzPassthrough(f *testing.F) {
	for _, s := range []string{
		`{}`, `{"a":1}`, `{"a":[1,2,{"b":null}],"c":"x\né"}`, "{\n \"a\" : [ ] ,\n \"b\" : { }\n}\n",
		`{"a":tru}`, `{"a":1,}`, `{"a":"\x"}`, `{"a":-0.5e+3,"b":01}`, `{"a":1} x`, `[1]`, ``, `{"a":"` + string([]byte{0xff}) + `"}`,
	} {
		f.Add([]byte(s), 3)
	}
	f.Fuzz(func(t *testing.T, in []byte, chunk int) {
		if chunk <= 0 || chunk > 4096 {
			chunk = 1 + (chunk&0x7fffffff)%4096
		}
		tr := NewTransformer(BaseProtocol{})
		var out []byte
		for i := 0; i < len(in); i += chunk {
			j := i + chunk
			if j > len(in) {
				j = len(in)
			}
			tr.Write(in[i:j])
			out = append(out, tr.Out()...)
		}
		out = append(out, tr.Finish()...)
		bad, _ := tr.Unsupported()
		valid := json.Valid(in) && firstByte(in) == '{' // grammar + the root must be an object; not Unmarshal (1000e1000 and the like fail on overflow)
		if valid && bad {
			t.Fatalf("valid object bailed: %q", in)
		}
		if !valid && !bad && len(in) < 10000 { // encoding/json has a nesting limit of 10000; very long inputs are not compared
			t.Fatalf("invalid input passed: %q", in)
		}
		if !bad && string(out) != string(in) {
			t.Fatalf("passthrough not faithful:\n in  %q\n out %q", in, out)
		}
	})
}

// KeyProbe rewriting never panics and the output is still valid JSON (when the input is).
func FuzzKeyProbe(f *testing.F) {
	for _, s := range []string{`{"k":"v","x":[1]}`, `{"x":{"k":1},"k":null}`, `{"k":"` + `A` + `"}`, `{"k":tru}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		tr := NewKeyProbeTransformer(KeyProbeOptions{Keys: map[string]int{"k": 1 << 16},
			OnKey: func(t *Transformer, key string, raw []byte) ([]byte, bool) { return []byte(`"R"`), true }})
		tr.Write(in)
		out := tr.Finish()
		if bad, _ := tr.Unsupported(); bad {
			return
		}
		var v map[string]any
		if json.Unmarshal(in, &v) == nil && !json.Valid(out) {
			t.Fatalf("output is not valid JSON:\n in  %q\n out %q", in, out)
		}
	})
}

// UTF-8 validation on: passes ⇔ (the default mode passes and utf8.Valid(in)), and the output stays byte-identical; array roots under RootAny likewise.
func FuzzStrictModes(f *testing.F) {
	for _, s := range []string{
		`{"a":"é€😀"}`, "{\"a\":\"\xC0\x80\"}", "{\"\xED\xA0\x80\":1}", "{\"a\":\"\xE4\xB8\"}",
		`[1,"s",{"a":[true]}]`, `[1,]`, `{"a":1,"a":2}`, "[\"\xFF\"]",
	} {
		f.Add([]byte(s), 3)
	}
	f.Fuzz(func(t *testing.T, in []byte, chunk int) {
		if chunk <= 0 || chunk > 4096 {
			chunk = 1 + (chunk&0x7fffffff)%4096
		}
		run := func(set func(*Transformer)) ([]byte, bool) {
			tr := NewTransformer(BaseProtocol{})
			set(tr)
			var out []byte
			for i := 0; i < len(in); i += chunk {
				j := i + chunk
				if j > len(in) {
					j = len(in)
				}
				tr.Write(in[i:j])
				out = append(out, tr.Out()...)
			}
			out = append(out, tr.Finish()...)
			bad, _ := tr.Unsupported()
			return out, !bad
		}
		plainOut, plainOK := run(func(*Transformer) {})
		anyOut, anyOK := run(func(tr *Transformer) { tr.SetRoot(RootAny) })
		u8Out, u8OK := run(func(tr *Transformer) { tr.SetRoot(RootAny); tr.SetValidateUTF8(true) })
		var v any
		valid := json.Unmarshal(in, &v) == nil
		_, isObj := v.(map[string]any)
		_, isArr := v.([]any)
		if valid && (isObj || isArr) && !anyOK {
			t.Fatalf("RootAny rejected a valid object/array: %q", in)
		}
		if anyOK && !(isObj || isArr) {
			t.Fatalf("RootAny passed a non-object/array: %q", in)
		}
		if plainOK && !anyOK {
			t.Fatalf("the default mode passed but RootAny rejected: %q", in)
		}
		if anyOK && string(anyOut) != string(in) || plainOK && string(plainOut) != string(in) {
			t.Fatalf("passthrough not faithful: %q", in)
		}
		if wantU8 := anyOK && utf8.Valid(in); wantU8 != u8OK {
			t.Fatalf("UTF-8 validation verdict %v, want %v: %q", u8OK, wantU8, in)
		}
		if u8OK && string(u8Out) != string(in) {
			t.Fatalf("UTF-8 mode passthrough not faithful: %q", in)
		}
	})
}

func firstByte(b []byte) byte {
	for _, c := range b {
		if !jsonSpace[c] {
			return c
		}
	}
	return 0
}
