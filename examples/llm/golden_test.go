package llm

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Golden differential: testdata/*.jsonl.gz was generated once by the buffered implementation of Higress ai-proxy; each record
// holds input + configuration + buffered output (or a buffered failure). The streaming protocol is built with the same
// configuration, run at several chunk sizes and compared field by field by the rules of the Higress differential harness: numbers by
// literal (UseNumber), except the tools parameters subtree by numeric value (the buffered map round trip reorders keys and reformats numbers; streaming passes it through verbatim with the same meaning).
//
// Allowed exceptions match the harness:
//   - buffered failed but streaming passed: only when the buffered failure is a type error in a dropped field (lenient=true in the record);
//   - buffered succeeded but streaming bailed: only known fallback reasons (duplicate keys, parts the buffered path skips, http images, a developer role on the passthrough path, ...).

type goldenRec struct {
	Suite     string         `json:"suite"`
	Name      string         `json:"name"`
	In        string         `json:"in"`
	Cfg       map[string]any `json:"cfg"`
	OK        bool           `json:"ok"`
	Out       string         `json:"out,omitempty"`
	Lenient   bool           `json:"lenient,omitempty"`
	NeedFetch bool           `json:"needFetch,omitempty"`
}

func loadGolden(t *testing.T, suite string) []goldenRec {
	t.Helper()
	f, err := os.Open("testdata/" + suite + ".jsonl.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(gz)
	var recs []goldenRec
	for dec.More() {
		var r goldenRec
		if err := dec.Decode(&r); err != nil {
			t.Fatal(err)
		}
		recs = append(recs, r)
	}
	return recs
}

// ---- configuration matching the buffered path ----

var (
	oaiMapping        = map[string]string{"m1": "mapped-1", "gpt-*": "g", "~^re(.*)$": "x$1", "*": "fallback"}
	vMapping          = map[string]string{"m1": "qwen3.6-plus-x", "k": "kimi-k2.6", "gpt-*": "g", "*": "fallback"}
	qwenNativeMapping = map[string]string{"m1": "qwen3.6-plus-x", "k": "kimi-k2.6", "vl": "qwen-vl-max", "gpt-*": "g", "*": "fallback"}
	gemSafety         = map[string]string{"HARM_CATEGORY_HATE_SPEECH": "BLOCK_NONE", "HARM_CATEGORY_HARASSMENT": "BLOCK_ONLY_HIGH"}
	geminiThinking    = map[string]bool{"gemini-2.5-pro": true, "gemini-2.5-flash": true, "gemini-2.5-flash-lite": true}
)

// getMappedModel reproduces the buffered modelMapping rules: exact → prefix (k*) → regexp (~re, $1 allowed) → wildcard * → unchanged.
func getMappedModel(model string, mapping map[string]string) string {
	if v, ok := mapping[model]; ok {
		return v
	}
	keys := make([]string, 0, len(mapping))
	for k := range mapping {
		keys = append(keys, k)
	}
	sort.Strings(keys) // the buffered path iterates a map; the patterns in these tables do not overlap, so the order does not matter
	for _, k := range keys {
		v := mapping[k]
		if k == "*" {
			continue
		}
		if strings.HasSuffix(k, "*") {
			if strings.HasPrefix(model, strings.TrimSuffix(k, "*")) {
				return v
			}
		}
		if strings.HasPrefix(k, "~") {
			re := regexp.MustCompile(strings.TrimPrefix(k, "~"))
			if re.MatchString(model) {
				return re.ReplaceAllString(model, v)
			}
		}
	}
	if v, ok := mapping["*"]; ok {
		return v
	}
	return model
}

func qwenSupportsPreserveThinking(model string) bool {
	return model == "qwen3.6-max-preview" || strings.HasPrefix(model, "qwen3.6-plus") || model == "kimi-k2.6"
}

func gemMapping(m string) string {
	if m == "m1" {
		return "gemini-2.5-flash"
	}
	return m
}

func hasDeveloper(in string) bool {
	var v struct {
		Messages []struct {
			Role any `json:"role"`
		} `json:"messages"`
	}
	if json.Unmarshal([]byte(in), &v) != nil {
		return false
	}
	for _, m := range v.Messages {
		if m.Role == "developer" {
			return true
		}
	}
	return false
}

func newFor(rec goldenRec) *Transformer {
	cfg := rec.Cfg
	b := func(k string) bool { v, _ := cfg[k].(bool); return v }
	switch rec.Suite {
	case "claude":
		return NewClaude(ClaudeOptions{ClaudeCodeMode: b("cc")})
	case "openai":
		chat := b("chat")
		return NewOpenAI(OpenAIOptions{
			MapModel:       func(m string) string { return getMappedModel(m, oaiMapping) },
			DetectStream:   chat,
			NormalizeUsage: chat,
			CheckMessages:  chat,
		})
	case "qwen_compat":
		chat := b("chat")
		return NewOpenAI(OpenAIOptions{
			MapModel:           func(m string) string { return getMappedModel(m, vMapping) },
			ModelOnlyIfPresent: true,
			NormalizeUsage:     chat,
			CheckMessages:      chat,
			Variant:            &QwenVariant{SupportsPreserveThinking: qwenSupportsPreserveThinking},
		})
	case "zhipu", "openrouter":
		chat := b("chat")
		var vv OpenAIVariant
		if chat {
			if rec.Suite == "zhipu" {
				vv = &ZhipuVariant{}
			} else {
				vv = &OpenRouterVariant{}
			}
		}
		return NewOpenAI(OpenAIOptions{
			MapModel:       func(m string) string { return getMappedModel(m, vMapping) },
			DetectStream:   chat,
			NormalizeUsage: chat,
			CheckMessages:  chat,
			Variant:        vv,
		})
	case "gemini":
		var ss []GeminiSafetySetting
		if b("safety") {
			for k, v := range gemSafety {
				ss = append(ss, GeminiSafetySetting{Category: k, Threshold: v})
			}
			sort.Slice(ss, func(i, j int) bool { return ss[i].Category < ss[j].Category })
		}
		budget, _ := cfg["budget"].(float64)
		return NewGemini(GeminiOptions{
			MapModel: func(m string) (string, error) {
				if m == "" {
					return "", fmt.Errorf("missing model in request")
				}
				return gemMapping(m), nil
			},
			ThinkingModel:  func(m string) bool { return geminiThinking[m] },
			ThinkingBudget: int64(budget),
			SafetySettings: ss,
		})
	case "qwen_native":
		return NewQwenNative(QwenNativeOptions{
			MapModel: func(m string) (string, error) {
				if m == "" {
					return "", fmt.Errorf("missing model in request")
				}
				return getMappedModel(m, qwenNativeMapping), nil
			},
			SupportsPreserveThinking: qwenSupportsPreserveThinking,
			EnableSearch:             b("search"),
			DeveloperToSystem:        true,
		})
	}
	panic("unknown suite " + rec.Suite)
}

// allowedFallback: the known reasons each suite allows when the buffered path succeeded but streaming bailed.
func allowedFallback(rec goldenRec, why string) bool {
	chat, _ := rec.Cfg["chat"].(bool)
	has := func(ss ...string) bool {
		for _, s := range ss {
			if strings.Contains(why, s) {
				return true
			}
		}
		return false
	}
	switch rec.Suite {
	case "claude":
		return has("duplicate key", "the buffered path skips", "panic")
	case "gemini", "qwen_native":
		return has("duplicate key", "panic")
	case "openai":
		return chat && hasDeveloper(rec.In)
	default: // variants
		return chat && hasDeveloper(rec.In) || has("is not an object", "messages is not an array", "duplicate key")
	}
}

// ---- comparison (same as the harness) ----

func decodeMap(b []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

func canonNumbers(v any) any {
	switch x := v.(type) {
	case json.Number:
		if f, err := x.Float64(); err == nil {
			return f
		}
		return x
	case map[string]any:
		for k, e := range x {
			x[k] = canonNumbers(e)
		}
	case []any:
		for i, e := range x {
			x[i] = canonNumbers(e)
		}
	}
	return v
}

func diffMaps(a, b map[string]any) string {
	canonNumbers(a["tools"])
	canonNumbers(b["tools"])
	if pa, ok := a["parameters"].(map[string]any); ok {
		canonNumbers(pa["tools"])
	}
	if pb, ok := b["parameters"].(map[string]any); ok {
		canonNumbers(pb["tools"])
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) == string(jb) {
		return ""
	}
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	out := ""
	for k := range keys {
		va, _ := json.Marshal(a[k])
		vb, _ := json.Marshal(b[k])
		if string(va) != string(vb) {
			out += fmt.Sprintf("[%s] buffered=%s streaming=%s  ", k, va, vb)
		}
	}
	return out
}

func runStream(tr *Transformer, in string, chunk int) (map[string]any, bool, string) {
	var out []byte
	for i := 0; i < len(in); i += chunk {
		j := i + chunk
		if j > len(in) {
			j = len(in)
		}
		tr.Write([]byte(in[i:j]))
		out = append(out, tr.Out()...)
	}
	out = append(out, tr.Finish()...)
	if bad, why := tr.Unsupported(); bad {
		return nil, false, why
	}
	m, err := decodeMap(out)
	if err != nil {
		return nil, false, "output is not valid JSON: " + err.Error()
	}
	return m, true, ""
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "..."
}

func TestGolden(t *testing.T) {
	suites := []string{"claude", "openai", "qwen_compat", "zhipu", "openrouter", "gemini", "qwen_native"}
	for _, suite := range suites {
		recs := loadGolden(t, suite)
		same, offFail, lenient, fallback := 0, 0, 0, 0
		for _, rec := range recs {
			var off map[string]any
			if rec.OK {
				var err error
				if off, err = decodeMap([]byte(rec.Out)); err != nil {
					t.Fatalf("%s/%s: golden output is not valid JSON: %v", suite, rec.Name, err)
				}
			}
			for _, cs := range []int{1, 7, 64, 4096} {
				str, sok, why := runStream(newFor(rec), rec.In, cs)
				if !rec.OK {
					if cs == 1 {
						offFail++
					}
					if sok && !rec.Lenient {
						t.Fatalf("%s/%s chunk=%d: buffered failed but streaming passed\ninput %s", suite, rec.Name, cs, trunc(rec.In, 300))
					}
					if sok && cs == 1 {
						lenient++
					}
					continue
				}
				if rec.NeedFetch {
					if sok {
						t.Fatalf("%s/%s chunk=%d: an http image should fall back but passed", suite, rec.Name, cs)
					}
					continue
				}
				if !sok {
					if !allowedFallback(rec, why) {
						t.Fatalf("%s/%s chunk=%d: unexpected fallback: %s\ninput %s", suite, rec.Name, cs, why, trunc(rec.In, 300))
					}
					if cs == 1 {
						fallback++
					}
					continue
				}
				if suite == "gemini" {
					if ss, ok := str["safetySettings"].([]any); ok {
						sort.Slice(ss, func(i, j int) bool {
							return ss[i].(map[string]any)["category"].(string) < ss[j].(map[string]any)["category"].(string)
						})
					}
				}
				if d := diffMaps(off, str); d != "" {
					t.Fatalf("%s/%s chunk=%d differs from the buffered path: %s\ninput %s", suite, rec.Name, cs, d, trunc(rec.In, 300))
				}
				if cs == 1 {
					same++
				}
			}
		}
		t.Logf("%-12s %d records: identical %d, buffered failed %d (of which dropped-field type errors passed by streaming %d), known fallbacks %d, mismatches 0", suite, len(recs), same, offFail, lenient, fallback)
	}
}
