package ason

import (
	"encoding/json"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"
)

func chunkSizes(n int) []int {
	cs := []int{1, 2, 3, 5, 7, 64, 4096}
	if n > 0 {
		cs = append(cs, n)
	}
	return cs
}

// Valid multibyte UTF-8 (4-byte emoji included) passes through unchanged with validation on, whatever the chunking.
func TestUTF8ValidPassthrough(t *testing.T) {
	ins := []string{
		`{"Grüße":"Привет","emoji":"😀🎉","mix":"a€bαc𝄞d","ctrl":"é\n"}`,
		"{\"k\\u20ac\":\"é€\U0001F600\"}",
		`{"a":{"γλώσσα":["αβγ","δεζ",{"ключ":"значение"}]}}`,
	}
	for _, in := range ins {
		for _, cs := range chunkSizes(len(in)) {
			tr := NewTransformer(BaseProtocol{})
			tr.SetValidateUTF8(true)
			got, ok, why := feedAll(tr, in, cs)
			if !ok || got != in {
				t.Fatalf("chunk=%d: ok=%v %s\n got %q\nwant %q", cs, ok, why, got, in)
			}
		}
	}
}

// Invalid sequences: overlong encodings, surrogates, above U+10FFFF, invalid first bytes, stray continuation bytes, cut by a quote / backslash / the end of input.
func TestUTF8InvalidRejected(t *testing.T) {
	bad := map[string]string{
		"overlong 2-byte C0 80":                  "{\"s\":\"\xC0\x80\"}",
		"overlong 2-byte C1 BF":                  "{\"s\":\"\xC1\xBF\"}",
		"overlong 3-byte E0 80 80":               "{\"s\":\"\xE0\x80\x80\"}",
		"overlong 4-byte F0 80":                  "{\"s\":\"\xF0\x80\x80\x80\"}",
		"surrogate ED A0 80":                     "{\"s\":\"\xED\xA0\x80\"}",
		"surrogate ED BF BF":                     "{\"s\":\"\xED\xBF\xBF\"}",
		"above U+10FFFF":                         "{\"s\":\"\xF4\x90\x80\x80\"}",
		"first byte F5":                          "{\"s\":\"\xF5\x80\x80\x80\"}",
		"first byte FF":                          "{\"s\":\"\xFF\"}",
		"stray continuation byte":                "{\"s\":\"a\x80b\"}",
		"missing continuation then quote":        "{\"s\":\"\xE4\xB8\"}",
		"missing continuation then backslash":    "{\"s\":\"\xE4\xB8\\n\"}",
		"missing continuation then ASCII":        "{\"s\":\"\xE4\xB8x\"}",
		"missing continuation then new sequence": "{\"s\":\"\xE4\xB8\xE4\xB8\xAD\"}",
		"overlong encoding in a key":             "{\"\xC0\x80\":1}",
		"stray continuation byte in a key":       "{\"a\x80\":1}",
		"truncated sequence in a key":            "{\"\xE4\xB8\":1}",
		"surrogate in a key":                     "{\"\xED\xA0\x80\":1}",
		"inside a Skip region":                   "{\"skip\":{\"s\":\"\xC0\x80\"}}",
		"deep inside a Pass region":              "{\"a\":[1,{\"b\":\"\xFF\"}]}",
	}
	prot := KeyProbeOptions{Keys: map[string]int{"skip": 1 << 20},
		OnKey: func(t *Transformer, k string, raw []byte) ([]byte, bool) { return nil, false }}
	for name, in := range bad {
		for _, cs := range chunkSizes(len(in)) {
			for _, mk := range []func() *Transformer{
				func() *Transformer { return NewTransformer(BaseProtocol{}) },
				func() *Transformer { return NewKeyProbeTransformer(prot) },
			} {
				tr := mk()
				tr.SetValidateUTF8(true)
				if _, ok, _ := feedAll(tr, in, cs); ok {
					t.Fatalf("%s chunk=%d: invalid UTF-8 was passed", name, cs)
				}
			}
		}
		// no validation by default: matches encoding/json Valid (grammar only, not encoding)
		tr := NewTransformer(BaseProtocol{})
		got, ok, why := feedAll(tr, in, len(in))
		if !ok || got != in {
			t.Fatalf("%s should pass through in the default mode: ok=%v %s", name, ok, why)
		}
		if !json.Valid([]byte(in)) {
			t.Fatalf("%s: premise does not hold, encoding/json rejects it too", name)
		}
	}
}

// Input ending in the middle of a sequence: Finish must bail rather than emit half a character.
func TestUTF8TruncatedAtEOF(t *testing.T) {
	tr := NewTransformer(BaseProtocol{})
	tr.SetValidateUTF8(true)
	tr.Write([]byte("{\"s\":\"\xE4\xB8"))
	tr.Out()
	tr.Finish()
	if u, _ := tr.Unsupported(); !u {
		t.Fatal("input ending in the middle of a sequence was passed")
	}
}

// Random byte streams: with validation on the verdict must match utf8.Valid exactly, independent of chunking.
func TestUTF8MatchesStdlib(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	alphabet := []byte{'a', 'z', 0x80, 0x8F, 0xA0, 0xBF, 0xC0, 0xC2, 0xDF, 0xE0, 0xE1, 0xED, 0xEF, 0xF0, 0xF1, 0xF4, 0xF5, 0xFF, 0x9F, 0x90}
	for i := 0; i < 3000; i++ {
		n := r.Intn(12) + 1
		b := make([]byte, n)
		for k := range b {
			b[k] = alphabet[r.Intn(len(alphabet))]
		}
		inKey := r.Intn(2) == 0
		var in string
		if inKey {
			in = "{\"" + string(b) + "\":1}"
		} else {
			in = "{\"s\":\"" + string(b) + "\"}"
		}
		want := utf8.Valid(b)
		for _, cs := range []int{1, 2, 3, len(in)} {
			tr := NewTransformer(BaseProtocol{})
			tr.SetValidateUTF8(true)
			got, ok, why := feedAll(tr, in, cs)
			if ok != want {
				t.Fatalf("%q chunk=%d: verdict %v, utf8.Valid=%v (%s)", b, cs, ok, want, why)
			}
			if ok && got != in {
				t.Fatalf("%q: output was modified", b)
			}
		}
	}
}

// Duplicate key policies: First dispatches only the first; Bail bails; the default dispatches as usual.
func TestDupKeysPolicy(t *testing.T) {
	enterAll := &dupProto{}
	cases := []struct {
		name string
		in   string
		pol  DupKeys
		want string // "" = expect a bail
	}{
		{"default passthrough", `{"a":1,"a":2}`, DupKeysPass, `{"a":1,"a":2}`},
		{"First top level", `{"a":1,"a":2,"b":3}`, DupKeysFirst, `{"a":1,"b":3}`},
		{"First at the end", `{"b":3,"a":1,"a":2}`, DupKeysFirst, `{"b":3,"a":1}`},
		{"First three times", `{"a":1,"a":{"x":[1]},"a":"s"}`, DupKeysFirst, `{"a":1}`},
		{"First with whitespace", "{ \"a\" : 1 , \"a\" : 2 }", DupKeysFirst, "{ \"a\" : 1 }"},
		{"First only in dispatch frames", `{"a":1,"a":2,"o":{"c":1,"c":2}}`, DupKeysFirst, `{"a":1,"o":{"c":1}}`},
		{"First same key escaped", `{"a":1,"a":2}`, DupKeysFirst, `{"a":1}`},
		{"Bail", `{"a":1,"a":2}`, DupKeysBail, ""},
		{"Bail deep", `{"o":{"c":1,"c":2}}`, DupKeysBail, ""},
		{"Bail without duplicates", `{"a":1,"b":{"a":1}}`, DupKeysBail, `{"a":1,"b":{"a":1}}`},
	}
	for _, c := range cases {
		for _, cs := range chunkSizes(len(c.in)) {
			tr := NewTransformer(enterAll)
			tr.SetDupKeys(c.pol)
			got, ok, why := feedAll(tr, c.in, cs)
			if c.want == "" {
				if ok {
					t.Fatalf("%s chunk=%d: should bail, but produced %q", c.name, cs, got)
				}
				if tr.Err().Code != ErrDuplicateKey {
					t.Fatalf("%s: wrong reason: %s", c.name, why)
				}
				continue
			}
			if !ok || got != c.want {
				t.Fatalf("%s chunk=%d: ok=%v %s\n got %q\nwant %q", c.name, cs, ok, why, got, c.want)
			}
		}
	}
	// compatibility: the DupKeyBail field is equivalent to DupKeysBail
	tr := NewTransformer(enterAll)
	tr.DupKeyBail = true
	if _, ok, _ := feedAll(tr, `{"a":1,"a":2}`, 3); ok {
		t.Fatal("the DupKeyBail field no longer works")
	}
}

// dupProto Enters every object value (so deeper levels become dispatch frames too).
type dupProto struct{ BaseProtocol }

func (dupProto) OnKey(t *Transformer) Action { return Enter().Lenient() }

// The First policy combined with a Capture rewrite: only the first is rewritten, later occurrences of the key are dropped (gjson first-wins semantics).
func TestDupKeysFirstWithCapture(t *testing.T) {
	in := `{"model":"a","x":1,"model":"b"}`
	for _, cs := range chunkSizes(len(in)) {
		tr := NewKeyProbeTransformer(KeyProbeOptions{Keys: map[string]int{"model": 1024},
			OnKey: func(t *Transformer, k string, raw []byte) ([]byte, bool) { return []byte(`"R"`), true }})
		tr.SetDupKeys(DupKeysFirst)
		got, ok, why := feedAll(tr, in, cs)
		if !ok || got != `{"model":"R","x":1}` {
			t.Fatalf("chunk=%d: ok=%v %s got %q", cs, ok, why, got)
		}
	}
}

// A Defer replay does not count itself as a duplicate key.
func TestDupKeysFirstWithDefer(t *testing.T) {
	in := `{"a":1,"b":2,"a":3}`
	for _, cs := range chunkSizes(len(in)) {
		tr := NewTransformer(&deferA{})
		tr.SetDupKeys(DupKeysFirst)
		got, ok, why := feedAll(tr, in, cs)
		if !ok || got != `{"b":2,"a":1}` {
			t.Fatalf("chunk=%d: ok=%v %s got %q", cs, ok, why, got)
		}
	}
}

type deferA struct {
	BaseProtocol
	released bool
}

func (p *deferA) OnKey(t *Transformer) Action {
	if t.Depth() == 1 && t.Last() == "a" && !p.released {
		return Defer(1 << 20)
	}
	return Pass()
}

func (p *deferA) OnLeave(t *Transformer) {
	if t.Depth() == 0 { // root closing: the path still points at the container itself
		p.released = true
		t.ReleaseNow()
	}
}

// Root shapes: the default accepts objects only; RootArray arrays only; RootAny both. Array roots dispatch by index.
func TestRootKind(t *testing.T) {
	arr := `[1,"s",{"a":[true,null]},[ ],{ }]`
	obj := `{"a":1}`
	ws := " \n[ 1 , 2 ]\n"
	cases := []struct {
		name string
		in   string
		root RootKind
		ok   bool
	}{
		{"default rejects an array", arr, RootObject, false},
		{"default accepts an object", obj, RootObject, true},
		{"RootArray accepts an array", arr, RootArray, true},
		{"RootArray rejects an object", obj, RootArray, false},
		{"RootAny array", arr, RootAny, true},
		{"RootAny object", obj, RootAny, true},
		{"RootAny scalar", `1`, RootAny, false},
		{"RootAny string", `"x"`, RootAny, false},
		{"array root with whitespace", ws, RootAny, true},
		{"empty array root", `[]`, RootArray, true},
		{"data after an array root", `[1] 2`, RootArray, false},
		{"trailing comma in an array root", `[1,]`, RootArray, false},
		{"unclosed array root", `[1`, RootArray, false},
	}
	for _, c := range cases {
		for _, cs := range chunkSizes(len(c.in)) {
			tr := NewTransformer(BaseProtocol{})
			tr.SetRoot(c.root)
			got, ok, why := feedAll(tr, c.in, cs)
			if ok != c.ok {
				t.Fatalf("%s chunk=%d: ok=%v (%s) got %q", c.name, cs, ok, why, got)
			}
			if ok && got != c.in {
				t.Fatalf("%s chunk=%d: passthrough should be byte-identical\n got %q\nwant %q", c.name, cs, got, c.in)
			}
		}
	}
}

// Elements of an array root dispatch through OnElem: the index is readable, Enter into object elements rewrites inner keys, and Skipping a whole element keeps the separators right.
func TestRootArrayDispatch(t *testing.T) {
	in := `[{"model":"a","x":1},{"model":"b"},3,{"model":"c","y":[1]}]`
	want := `[{"model":"R","x":1},{"model":"R"},{"model":"R","y":[1]}]` // the scalar at index 2 is Skipped
	for _, cs := range chunkSizes(len(in)) {
		p := &rootArrProto{}
		tr := NewTransformer(p)
		tr.SetRoot(RootArray)
		got, ok, why := feedAll(tr, in, cs)
		if !ok || got != want {
			t.Fatalf("chunk=%d: ok=%v %s\n got %q\nwant %q", cs, ok, why, got, want)
		}
		if p.seen != "0,1,2,3" {
			t.Fatalf("chunk=%d: OnElem index sequence %q", cs, p.seen)
		}
	}
}

type rootArrProto struct {
	BaseProtocol
	seen string
}

func (p *rootArrProto) OnElem(t *Transformer) Action {
	if t.Depth() == 1 {
		if p.seen != "" {
			p.seen += ","
		}
		p.seen += string(rune('0' + t.Idx(0)))
		if t.Idx(0) == 2 {
			return Skip()
		}
		return Enter().Lenient()
	}
	return Pass()
}

func (p *rootArrProto) OnKey(t *Transformer) Action {
	if t.Depth() == 2 && t.Last() == "model" {
		return Capture(1024)
	}
	return Pass()
}

func (p *rootArrProto) OnValue(t *Transformer, raw []byte) {
	t.W().KeyRaw(t.KeyRaw())
	t.W().Raw([]byte(`"R"`))
}

// Grammar inside regions (Pass / Skip / Capture) is treated like dispatch frames: the verdict on random structural garbage must match encoding/json exactly.
func TestRegionGrammarMatchesStdlib(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	toks := []string{"{", "}", "[", "]", ",", ":", `"k"`, `"v"`, "1", "-2.5e3", "true", "null", " ", "\n", "tru", "01"}
	enterAll := &dupProto{}
	skipA := KeyProbeOptions{Keys: map[string]int{"a": 1 << 20},
		OnKey: func(t *Transformer, k string, raw []byte) ([]byte, bool) { return nil, false }}
	capA := KeyProbeOptions{Keys: map[string]int{"a": 1 << 20},
		OnKey: func(t *Transformer, k string, raw []byte) ([]byte, bool) { return raw, true }}
	mk := []struct {
		name string
		fn   func() *Transformer
	}{
		{"Pass region", func() *Transformer { return NewTransformer(BaseProtocol{}) }},
		{"dispatch frame", func() *Transformer { return NewTransformer(enterAll) }},
		{"Skip region", func() *Transformer { return NewKeyProbeTransformer(skipA) }},
		{"Capture region", func() *Transformer { return NewKeyProbeTransformer(capA) }},
	}
	seen := map[string]bool{}
	for i := 0; i < 20000; i++ {
		var sb strings.Builder
		n := r.Intn(8) + 1
		for k := 0; k < n; k++ {
			sb.WriteString(toks[r.Intn(len(toks))])
		}
		in := `{"a":` + sb.String() + `}`
		if r.Intn(4) == 0 { // also test garbage in the second field
			in = `{"a":[1],"b":` + sb.String() + `}`
		}
		if seen[in] {
			continue
		}
		seen[in] = true
		want := json.Valid([]byte(in))
		for _, m := range mk {
			for _, cs := range []int{1, 3, len(in)} {
				got, ok, why := feedAll(m.fn(), in, cs)
				if ok != want {
					t.Fatalf("%s chunk=%d: %q verdict %v (%s), encoding/json=%v", m.name, cs, in, ok, why, want)
				}
				if ok && m.name != "Skip region" && got != in {
					t.Fatalf("%s: %q output was modified: %q", m.name, in, got)
				}
			}
		}
	}
}

// Typical errors inside regions: encoding/json rejects each of them and so must streaming, whether it lands in a Pass or a Skip region.
func TestRegionGrammarCases(t *testing.T) {
	bad := []string{
		`{"a":{]}`, `{"a":[}]}`, `{"a":{{}}}`, `{"a":{"x"}}`, `{"a":{"x":}}`, `{"a":{"x" 1}}`,
		`{"a":{"x":1,}}`, `{"a":[1,]}`, `{"a":[,1]}`, `{"a":[1 2]}`, `{"a":{1:2}}`, `{"a":{"x":1 "y":2}}`,
		`{"a":[:]}`, `{"a":{"x"::1}}`, `{"a":[1,,2]}`, `{"a":["x" "y"]}`, `{"a":{"x":1}}}`, `{"a":[[]]]}`,
		`{"a":[{"x":1]}`, `{"a":{"x":[1}}`, `{"a":{,}}`, `{"a":[}`, `{"a":{"x":1:2}}`, `{"a":{"x",1}}`,
		`{"a":{}{}}`, `{"a":[[]{}]}`, `{"a":[1{}]}`, `{"a":{"x":1}[]}`,
	}
	good := []string{
		`{"a":{}}`, `{"a":[]}`, `{"a":[[],{}]}`, `{"a":{"x":[1,{"y":null}],"z":""}}`, "{\"a\": { \"x\" : [ 1 , 2 ] } }",
		`{"a":[[[[[]]]]]}`, `{"a":{"x":{"y":{"z":{}}}}}`, `{"a":[1,"s",true,null,-0.5e2,{},[]]}`,
	}
	skipA := KeyProbeOptions{Keys: map[string]int{"a": 1 << 20},
		OnKey: func(t *Transformer, k string, raw []byte) ([]byte, bool) { return nil, false }}
	for _, in := range bad {
		if json.Valid([]byte(in)) {
			t.Fatalf("premise does not hold, encoding/json accepts %q", in)
		}
		for _, cs := range chunkSizes(len(in)) {
			if _, ok, _ := feedAll(NewTransformer(BaseProtocol{}), in, cs); ok {
				t.Fatalf("Pass region passed %q (chunk=%d)", in, cs)
			}
			if _, ok, _ := feedAll(NewKeyProbeTransformer(skipA), in, cs); ok {
				t.Fatalf("Skip region passed %q (chunk=%d)", in, cs)
			}
		}
	}
	for _, in := range good {
		for _, cs := range chunkSizes(len(in)) {
			got, ok, why := feedAll(NewTransformer(BaseProtocol{}), in, cs)
			if !ok || got != in {
				t.Fatalf("valid input rejected or modified %q chunk=%d: %v %s %q", in, cs, ok, why, got)
			}
		}
	}
}
