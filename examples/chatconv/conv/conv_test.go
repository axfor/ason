package conv

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/axfor/ason"
)

// ---- reference implementation: buffer everything, then convert by the same rules (the oracle of the differential test) ----

func reference(in []byte, opt Options) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(in))
	dec.UseNumber()
	var src map[string]any
	if err := dec.Decode(&src); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("data after root")
	}
	out := map[string]any{}
	model := ""
	if v, ok := src["model"]; ok {
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("model is not a string")
		}
		model = s
	}
	out["model"] = opt.MapModel(model)
	if v, ok := src["max_tokens"]; ok {
		n, ok := v.(json.Number)
		if !ok || strings.ContainsAny(n.String(), ".eE") {
			return nil, errors.New("max_tokens is not an integer")
		}
		out["max_tokens"] = n
	} else {
		out["max_tokens"] = json.Number(fmt.Sprint(opt.DefaultMaxTokens))
	}
	var system []string
	if v, ok := src["messages"]; ok {
		arr, ok := v.([]any)
		if !ok {
			return nil, errors.New("messages is not an array")
		}
		msgs := []any{}
		for _, e := range arr {
			m, ok := e.(map[string]any)
			if !ok {
				return nil, errors.New("message is not an object")
			}
			rv, ok := m["role"]
			if !ok {
				return nil, errors.New("message without role")
			}
			role, ok := rv.(string)
			if !ok {
				return nil, errors.New("role is not a string")
			}
			if role == "system" {
				if c, ok := m["content"]; ok {
					system = append(system, refSystemText(c))
				}
				continue
			}
			om := map[string]any{"role": role}
			if c, ok := m["content"]; ok {
				switch cv := c.(type) {
				case string:
					om["content"] = []any{map[string]any{"type": "text", "text": cv}}
				case []any:
					parts := []any{}
					for _, pe := range cv {
						pm, ok := pe.(map[string]any)
						if !ok {
							continue
						}
						typ, _ := pm["type"].(string)
						switch typ {
						case "text":
							tv, ok := pm["text"]
							if !ok {
								continue // no text: nothing written, the Lazy level drops it
							}
							s, ok := tv.(string)
							if !ok {
								return nil, errors.New("text is not a string")
							}
							parts = append(parts, map[string]any{"type": "text", "text": s})
						case "image_url":
							iv, ok := pm["image_url"]
							if !ok {
								continue
							}
							im, ok := iv.(map[string]any)
							if !ok {
								return nil, errors.New("image_url is not an object")
							}
							uv, ok := im["url"]
							if !ok {
								continue
							}
							u, ok := uv.(string)
							if !ok {
								return nil, errors.New("url is not a string")
							}
							if !strings.HasPrefix(u, "data:") {
								return nil, errors.New("image is not a data URL")
							}
							semi := strings.IndexByte(u, ';')
							comma := strings.IndexByte(u, ',')
							if semi < 0 || comma < 0 || comma < semi {
								return nil, errors.New("malformed data URL")
							}
							parts = append(parts, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": u[5:semi], "data": u[comma+1:]}})
						}
					}
					om["content"] = parts
				default:
					return nil, errors.New("content is neither a string nor an array")
				}
			}
			msgs = append(msgs, om)
		}
		out["messages"] = msgs
	}
	if len(system) > 0 {
		out["system"] = strings.Join(system, "\n")
	}
	if v, ok := src["stop"]; ok && v != nil {
		arr, ok := v.([]any)
		if !ok {
			return nil, errors.New("stop is not an array")
		}
		for _, e := range arr {
			if _, ok := e.(string); !ok {
				return nil, errors.New("stop element is not a string")
			}
		}
		if len(arr) > 0 {
			out["stop_sequences"] = arr
		}
	}
	if v, ok := src["tools"]; ok && v != nil {
		arr, ok := v.([]any)
		if !ok {
			return nil, errors.New("tools is not an array")
		}
		tools := []any{}
		for _, e := range arr {
			tm, ok := e.(map[string]any)
			if !ok {
				return nil, errors.New("tools element is not an object")
			}
			ot := map[string]any{"name": ""}
			if fv, ok := tm["function"]; ok {
				fm, ok := fv.(map[string]any)
				if !ok {
					return nil, errors.New("function is not an object")
				}
				if nv, ok := fm["name"]; ok {
					s, ok := nv.(string)
					if !ok {
						return nil, errors.New("name is not a string")
					}
					ot["name"] = s
				}
				if dv, ok := fm["description"]; ok {
					ot["description"] = dv
				}
				if pv, ok := fm["parameters"]; ok {
					pm, ok := pv.(map[string]any)
					if !ok {
						return nil, errors.New("parameters is not an object")
					}
					if len(pm) > 0 {
						ot["input_schema"] = pm
					}
				}
			}
			tools = append(tools, ot)
		}
		if len(tools) > 0 {
			out["tools"] = tools
		}
	}
	return out, nil
}

func refSystemText(c any) string {
	switch cv := c.(type) {
	case string:
		return cv
	case []any:
		var sb strings.Builder
		for _, pe := range cv {
			if pm, ok := pe.(map[string]any); ok && pm["type"] == "text" {
				if s, ok := pm["text"].(string); ok {
					sb.WriteString(s)
				}
			}
		}
		return sb.String()
	}
	return ""
}

// ---- driver ----

func stream(in []byte, chunk int, opt Options) (map[string]any, bool, string) {
	tr := New(opt)
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
	if bad, why := tr.Unsupported(); bad {
		return nil, false, why
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, false, "output is not valid JSON: " + err.Error() + " :: " + string(out)
	}
	return m, true, ""
}

var opt = Options{MapModel: func(m string) string { return strings.TrimPrefix(m, "demo/") }, DefaultMaxTokens: 1024}

func check(t *testing.T, name string, in []byte) {
	t.Helper()
	ref, rerr := reference(in, opt)
	for _, cs := range []int{1, 3, 7, 64, 4096} {
		got, ok, why := stream(in, cs, opt)
		if rerr != nil {
			if ok {
				t.Fatalf("%s chunk=%d: reference failed (%v) but streaming passed: %v\ninput %s", name, cs, rerr, got, in)
			}
			continue
		}
		if !ok {
			if strings.Contains(why, "capture limit exceeded") {
				continue // bounded lookahead: a value arrived before the field that decides its shape and exceeded the hold cap; a fallback by design
			}
			t.Fatalf("%s chunk=%d: reference succeeded but streaming bailed: %s\ninput %s", name, cs, why, in)
		}
		if !reflect.DeepEqual(got, ref) {
			g, _ := json.Marshal(got)
			r, _ := json.Marshal(ref)
			t.Fatalf("%s chunk=%d mismatch\ninput %s\nstreaming %s\nreference %s", name, cs, in, g, r)
		}
	}
}

// ---- hand-written scenarios (at least one per engine path) ----

func TestScenarios(t *testing.T) {
	big := strings.Repeat("y", 100<<10)
	cases := map[string]string{
		"minimal":                      `{"model":"demo/m","messages":[{"role":"user","content":"U"}]}`,
		"system hoisted":               `{"model":"m","messages":[{"role":"system","content":"S1"},{"role":"user","content":"U"},{"role":"system","content":"S2"}]}`,
		"system array":                 `{"model":"m","messages":[{"role":"system","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}]}`,
		"all system":                   `{"model":"m","messages":[{"role":"system","content":"S"}]}`,
		"content before role":          `{"model":"m","messages":[{"content":"U","role":"user"},{"content":"S","role":"system"}]}`,
		"content before role, large":   `{"model":"m","messages":[{"content":"` + big[:50<<10] + `","role":"user"}]}`,
		"parts":                        `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}]}`,
		"part type last":               `{"model":"m","messages":[{"role":"user","content":[{"text":"a","type":"text"},{"image_url":{"url":"data:image/png;base64,QUJD"},"type":"image_url"}]}]}`,
		"image":                        `{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + strings.Repeat("/9j/", 20000) + `","detail":"low"}}]}]}`,
		"image without base64 marker":  `{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:text/plain;charset=utf-8,hello"}}]}]}`,
		"unknown part":                 `{"model":"m","messages":[{"role":"user","content":[{"type":"audio","data":"x"},{"type":"text","text":"a"},{"text":"no type"}]}]}`,
		"non-object part":              `{"model":"m","messages":[{"role":"user","content":[1,"x",null,{"type":"text","text":"a"}]}]}`,
		"field not matching the type":  `{"model":"m","messages":[{"role":"user","content":[{"type":"text","image_url":{"url":"x"},"text":"a"}]}]}`,
		"no content":                   `{"model":"m","messages":[{"role":"assistant"},{"role":"user","content":"U"}]}`,
		"stop":                         `{"model":"m","messages":[{"role":"user","content":"U"}],"stop":["a","b\n"],"max_tokens":5}`,
		"empty stop and null":          `{"model":"m","messages":[],"stop":[],"tools":null}`,
		"tools":                        `{"model":"m","messages":[],"tools":[{"type":"function","function":{"name":"f","description":"d","parameters":{"type":"object","properties":{"a":{"type":"string"}}}}},{"function":{"parameters":{},"name":"g"}},{"type":"function"}]}`,
		"tools shuffled":               `{"tools":[{"function":{"parameters":{"z":[1,2.5e-3,null]},"description":null,"name":"f"},"type":"function"}],"messages":[{"role":"user","content":"U"}],"model":"m"}`,
		"dropped fields":               `{"model":"m","temperature":0.5,"metadata":{"messages":[{"role":"x"}]},"messages":[{"role":"user","content":"U","name":"n","extra":[1,{"a":null}]}]}`,
		"no model":                     `{"messages":[{"role":"user","content":"U"}]}`,
		"no messages":                  `{"model":"m","max_tokens":12}`,
		"formatted":                    "{\n  \"model\" : \"demo/m\" ,\n  \"messages\" : [ { \"role\" : \"user\" , \"content\" : \"U\" } ] ,\n  \"stop\" : [ \"a\" ]\n}\n",
		"escapes and unicode":          `{"model":"mA","messages":[{"role":"user","content":"€😀\"\\\n\t"}]}`,
		"large text":                   `{"model":"m","messages":[{"role":"user","content":"` + big + `"}]}`,
		"error: role missing":          `{"model":"m","messages":[{"content":"U"}]}`,
		"error: role number":           `{"model":"m","messages":[{"role":1,"content":"U"}]}`,
		"error: content null":          `{"model":"m","messages":[{"role":"user","content":null}]}`,
		"error: content object":        `{"model":"m","messages":[{"role":"user","content":{"a":1}}]}`,
		"error: stop number":           `{"model":"m","messages":[],"stop":["a",1]}`,
		"error: stop string":           `{"model":"m","messages":[],"stop":"a"}`,
		"error: http image":            `{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://x/y.png"}}]}]}`,
		"error: fractional max_tokens": `{"model":"m","messages":[],"max_tokens":1.5}`,
		"error: model number":          `{"model":1,"messages":[]}`,
		"error: tools element":         `{"model":"m","messages":[],"tools":[5]}`,
		"error: parameters array":      `{"model":"m","messages":[],"tools":[{"function":{"name":"f","parameters":[]}}]}`,
		"error: invalid JSON":          `{"model":"m","messages":[{"role":"user","content":"U"}],"stream":tru}`,
		"error: data after root":       `{"model":"m","messages":[]} x`,
	}
	for name, in := range cases {
		check(t, name, []byte(in))
	}
}

// ---- random differential: field order, values and shapes all random ----

func TestFuzzDifferential(t *testing.T) {
	r := rand.New(rand.NewSource(20260906))
	pick := func(xs ...string) string { return xs[r.Intn(len(xs))] }
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	text := func() string {
		alph := []string{"a", "€", `"`, `\`, "\n", "\t", "é", "😀", " ", "<", "\x01"}
		n := r.Intn(30)
		var sb strings.Builder
		for i := 0; i < n; i++ {
			sb.WriteString(alph[r.Intn(len(alph))])
		}
		if r.Intn(20) == 0 {
			sb.WriteString(strings.Repeat("y", 70<<10)) // past the commit point
		}
		return sb.String()
	}
	part := func() string {
		var f []string
		switch r.Intn(7) {
		case 0, 1:
			f = append(f, `"type":"text"`, `"text":`+q(text()))
		case 2:
			f = append(f, `"type":"image_url"`, `"image_url":{"url":`+q("data:image/"+pick("png", "jpeg")+pick(";base64", "")+","+strings.Repeat("QUJD", r.Intn(3000)))+`,"detail":"low"}`)
		case 3:
			f = append(f, `"type":"audio"`, `"data":"x"`)
		case 4:
			f = append(f, `"text":"no type"`)
		case 5:
			f = append(f, `"type":"text"`, `"text":`+pick(`1`, `null`, `["a"]`)) // error
		case 6:
			f = append(f, `"type":"image_url"`, `"image_url":`+pick(`{"url":"https://x/y.png"}`, `5`, `{"url":1}`, `{"detail":"low"}`)) // error or dropped
		}
		r.Shuffle(len(f), func(a, b int) { f[a], f[b] = f[b], f[a] })
		return "{" + strings.Join(f, ",") + "}"
	}
	message := func() string {
		var f []string
		switch r.Intn(10) {
		case 0:
			f = append(f, `"role":"system"`)
		case 1:
			f = append(f, `"role":"assistant"`)
		case 2:
			f = append(f, `"role":`+pick(`1`, `null`)) // error
		case 3: // role missing: error
		default:
			f = append(f, `"role":"user"`)
		}
		switch r.Intn(8) {
		case 0, 1, 2:
			f = append(f, `"content":`+q(text()))
		case 3, 4:
			var ps []string
			for i := 0; i < r.Intn(4); i++ {
				ps = append(ps, part())
			}
			if r.Intn(4) == 0 {
				ps = append(ps, pick(`1`, `"x"`, `null`))
			}
			f = append(f, `"content":[`+strings.Join(ps, ",")+`]`)
		case 5:
			f = append(f, `"content":`+pick(`null`, `7`, `{"a":1}`)) // an error except for system
		case 6: // no content
		case 7:
			f = append(f, `"content":`+q(text()), `"name":"n"`, `"extra":{"content":[1]}`)
		}
		r.Shuffle(len(f), func(a, b int) { f[a], f[b] = f[b], f[a] })
		return "{" + strings.Join(f, ",") + "}"
	}
	tool := func() string {
		var fn []string
		if r.Intn(6) != 0 {
			fn = append(fn, `"name":`+pick(`"f"`, `"g"`, `1`))
		}
		if r.Intn(2) == 0 {
			fn = append(fn, `"description":`+pick(q(text()), `null`, `""`, `{"x":1}`))
		}
		switch r.Intn(5) {
		case 0:
			fn = append(fn, `"parameters":{}`)
		case 1:
			fn = append(fn, `"parameters":{"type":"object","properties":{"a":{"type":"number","enum":[1,2.5]}},"required":["a"]}`)
		case 2:
			fn = append(fn, `"parameters":`+pick(`[]`, `5`, `null`)) // error
		}
		r.Shuffle(len(fn), func(a, b int) { fn[a], fn[b] = fn[b], fn[a] })
		var f []string
		if r.Intn(8) != 0 {
			f = append(f, `"function":{`+strings.Join(fn, ",")+`}`)
		}
		if r.Intn(3) != 0 {
			f = append(f, `"type":"function"`)
		}
		r.Shuffle(len(f), func(a, b int) { f[a], f[b] = f[b], f[a] })
		if r.Intn(25) == 0 {
			return pick(`5`, `null`, `[]`)
		}
		return "{" + strings.Join(f, ",") + "}"
	}
	for i := 0; i < 3000; i++ {
		var f []string
		if r.Intn(20) != 0 {
			f = append(f, `"model":`+pick(`"demo/m"`, `"m"`, `""`, `1`, `null`))
		}
		if r.Intn(2) == 0 {
			f = append(f, `"max_tokens":`+pick(`5`, `0`, `4096`, `1.5`, `"5"`, `-1`))
		}
		if r.Intn(10) != 0 {
			var ms []string
			for k := 0; k < r.Intn(5); k++ {
				ms = append(ms, message())
			}
			f = append(f, `"messages":`+pick("["+strings.Join(ms, ",")+"]", "["+strings.Join(ms, " , ")+"]", `{}`, `null`))
		}
		if r.Intn(3) == 0 {
			f = append(f, `"stop":`+pick(`["a"]`, `["a","b"]`, `[]`, `null`, `["a",1]`, `"a"`, `[ "a" , "b" ]`))
		}
		if r.Intn(3) == 0 {
			var ts []string
			for k := 0; k < r.Intn(4); k++ {
				ts = append(ts, tool())
			}
			f = append(f, `"tools":`+pick("["+strings.Join(ts, ",")+"]", `null`, `[]`, `{}`))
		}
		if r.Intn(3) == 0 {
			f = append(f, `"temperature":0.5`, `"metadata":{"messages":[{"role":"x"}],"model":1}`, `"unknown":[1,{"a":[]}]`)
		}
		r.Shuffle(len(f), func(a, b int) { f[a], f[b] = f[b], f[a] })
		sep := pick(",", " , ", ",\n  ")
		in := "{" + strings.Join(f, sep) + "}" + pick("", "\n")
		check(t, fmt.Sprintf("random#%d", i), []byte(in))
	}
}

// Duplicate keys: the target has struct semantics (last wins), which streaming cannot reproduce; it must bail rather than pass.
func TestDuplicateKeysBail(t *testing.T) {
	for _, in := range []string{
		`{"model":"a","model":"b","messages":[]}`,
		`{"model":"m","messages":[{"role":"user","role":"system","content":"U"}]}`,
	} {
		if _, ok, _ := stream([]byte(in), 3, opt); ok {
			t.Fatalf("a duplicate key should bail: %s", in)
		}
	}
}

// Commit point semantics: Out() is empty before 64KB and non-empty per chunk after; Finish hands over the rest at once on the last chunk.
func TestCommitPoint(t *testing.T) {
	in := []byte(`{"model":"m","messages":[{"role":"user","content":"` + strings.Repeat("y", 200<<10) + `"}]}`)
	tr := New(opt)
	seenOut := false
	for i := 0; i < len(in); i += 4096 {
		j := i + 4096
		if j > len(in) {
			j = len(in)
		}
		tr.Write(in[i:j])
		out := tr.Out()
		if j < ason.CommitBytes && len(out) > 0 {
			t.Fatalf("no output expected before the commit point (at %d)", j)
		}
		if j >= ason.CommitBytes+8192 && len(out) == 0 && !seenOut {
			t.Fatalf("output expected per chunk after the commit point (at %d)", j)
		}
		if len(out) > 0 {
			seenOut = true
		}
	}
	if fin := tr.Finish(); len(fin) == 0 {
		t.Fatal("Finish should hand over the remaining output")
	}
}
