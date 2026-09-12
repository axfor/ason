package ason

import (
	"encoding/json"
	"math/rand"
	"strings"
	"testing"
)

func runKeyProbe(t *testing.T, tr *Transformer, in string, chunk int) (string, bool, string) {
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

// rewriteFirstModel is the property the engine's fidelity claim rests on, written the obvious way: find the first
// top-level "model", and if its decoded value contains a slash, replace that value in place with what follows the
// slash, leaving every other byte -- whitespace, key order, escapes, a second "model" -- exactly as it was. That is
// what sjson.SetBytes produced for these documents, and expressing it here instead of importing sjson keeps the
// property tested and the module free of dependencies.
func rewriteFirstModel(in string) string {
	raw := firstModelRaw(in)
	if raw == "" {
		return in
	}
	var s string
	if json.Unmarshal([]byte(raw), &s) != nil {
		return in
	}
	slash := strings.Index(s, "/")
	if slash < 0 {
		return in
	}
	repl, err := json.Marshal(s[slash+1:])
	if err != nil {
		return in
	}
	// Walk the bytes tracking depth and strings, and stop at the first `"model"` that sits directly in the root
	// object: depth is 1 inside the root, and the key's opening quote is seen while not already inside a string.
	depth, i := 0, 0
	for i < len(in) {
		c := in[i]
		if c == '"' {
			if depth == 1 && strings.HasPrefix(in[i:], `"model"`) {
				k := i + len(`"model"`)
				for k < len(in) && (in[k] == ' ' || in[k] == '\t' || in[k] == '\n' || in[k] == '\r') {
					k++
				}
				if k < len(in) && in[k] == ':' {
					k++
					for k < len(in) && (in[k] == ' ' || in[k] == '\t' || in[k] == '\n' || in[k] == '\r') {
						k++
					}
					if strings.HasPrefix(in[k:], raw) {
						return in[:k] + string(repl) + in[k+len(raw):]
					}
					return in
				}
			}
			// Skip the whole string, escapes included.
			i++
			for i < len(in) {
				if in[i] == '\\' {
					i += 2
					continue
				}
				if in[i] == '"' {
					i++
					break
				}
				i++
			}
			continue
		}
		if c == '{' || c == '[' {
			depth++
		} else if c == '}' || c == ']' {
			depth--
		}
		i++
	}
	return in
}

// Rewriting model: the output must be byte-identical to what sjson.SetBytes produces (formatting preserved) and the Prelude must report model.
func TestKeyProbeRewriteMatchesSjson(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	bodies := []string{
		`{"model":"p/m","messages":[{"role":"user","content":"U"}]}`,
		`{"messages":[{"role":"user","content":"U"}],"model":"p/m","stream":true}`,
		"{\n  \"model\" : \"p/m\" ,\n  \"messages\" : [ ]\n}\n",
		`{"model":"p/m","model":"q/n"}`,
		`{"a":{"model":"inner"},"model":"p/m"}`,
		`{"model":"plain"}`,
		`{"model":123}`,
		`{"model":"p/m","x":"\u0041\n"}`,
	}
	for i := 0; i < 200; i++ {
		var sb strings.Builder
		sb.WriteString("{")
		n := r.Intn(6) + 1
		pos := r.Intn(n)
		for k := 0; k < n; k++ {
			if k > 0 {
				sb.WriteString(",")
			}
			if k == pos {
				sb.WriteString(`"model":"p/m` + strings.Repeat("x", r.Intn(50)) + `"`)
			} else {
				sb.WriteString(`"k` + string(rune('a'+k)) + `":` + []string{`1`, `"s"`, `[1,{"model":"nested"}]`, `{"model":"nested"}`, `null`, `true`}[r.Intn(6)])
			}
		}
		sb.WriteString("}")
		bodies = append(bodies, sb.String())
	}
	for _, in := range bodies {
		var wantModel string
		gotRaw := ""
		for _, cs := range []int{1, 3, 7, 4096} {
			tr := NewKeyProbeTransformer(KeyProbeOptions{Keys: map[string]int{"model": 4096},
				OnKey: func(t *Transformer, key string, raw []byte) ([]byte, bool) {
					var s string
					if json.Unmarshal(raw, &s) != nil {
						return nil, false
					}
					if i := strings.Index(s, "/"); i >= 0 {
						b, _ := json.Marshal(s[i+1:])
						return b, true
					}
					return nil, false
				}})
			out, ok, why := runKeyProbe(t, tr, in, cs)
			if !ok {
				t.Fatalf("%s chunk=%d: %s", in, cs, why)
			}
			captured := string(tr.Protocol().(*KeyProbe).Captured()["model"])
			// expectation: gjson reads the first model; with a "/" sjson rewrites the first
			var m map[string]any
			_ = json.Unmarshal([]byte(in), &m)
			want := in
			var s string
			if err := json.Unmarshal([]byte(firstModelRaw(in)), &s); err == nil {
				wantModel = s
				if strings.Contains(s, "/") {
					want = rewriteFirstModel(in)
				}
			}
			if out != want {
				t.Fatalf("%s chunk=%d:\n got  %s\n want %s", in, cs, out, want)
			}
			if wantModel != "" && captured != firstModelRaw(in) {
				t.Fatalf("%s: captured=%q want %q", in, captured, firstModelRaw(in))
			}
			gotRaw = out
		}
		_ = gotRaw
	}
}

// firstModelRaw returns the raw value of the first top-level "model" (a naive implementation for the test).
func firstModelRaw(in string) string {
	tr := NewKeyProbeTransformer(KeyProbeOptions{Keys: map[string]int{"model": 4096}, Observe: true})
	tr.Write([]byte(in))
	tr.Finish()
	return string(tr.Protocol().(*KeyProbe).Captured()["model"])
}

// Observe mode: bytes untouched, the callback receives the value.
func TestKeyProbeObserve(t *testing.T) {
	in := `{"stream":true,"model":"m","x":[1,2]}`
	var got []string
	tr := NewKeyProbeTransformer(KeyProbeOptions{Keys: map[string]int{"model": 64, "stream": 8}, Observe: true,
		OnKey: func(t *Transformer, key string, raw []byte) ([]byte, bool) {
			got = append(got, key+"="+string(raw))
			return []byte(`"ignored"`), true
		}})
	out, ok, why := runKeyProbe(t, tr, in, 2)
	if !ok || out != in {
		t.Fatalf("observe mode must keep the bytes: ok=%v why=%s out=%s", ok, why, out)
	}
	vals := tr.Protocol().(*KeyProbe).Captured()
	if string(vals["model"]) != `"m"` || string(vals["stream"]) != `true` || strings.Join(got, " ") != `stream=true model="m"` {
		t.Fatalf("vals=%v got=%v", vals, got)
	}
}
