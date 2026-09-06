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

// 黄金差分：testdata/*.jsonl.gz 由 Higress ai-proxy 的官方（整体缓冲）实现一次性生成，每条记录是
// 输入 + 配置 + 官方输出（或官方失败）。这里用同样的配置构造流式协议，在多种分块尺寸下运行，
// 按 Higress 差分 harness 的规则逐字段比对：数字按字面量比（UseNumber），只有 tools 的 parameters 子树
// 按数值比（官方经 map 往返会重排 key 与数字，流式原样透传，语义相同）。
//
// 允许的例外与 harness 一致：
//   - 官方失败而流式放行：只允许官方失败原因是"被丢弃字段的类型错误"（记录里 lenient=true）；
//   - 官方成功而流式判定不支持：只允许已知的回落原因（重复 key、官方会跳过的 part、http 图片、透传路径的 developer role 等）。

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

// ---- 与官方一致的配置 ----

var (
	oaiMapping        = map[string]string{"m1": "mapped-1", "gpt-*": "g", "~^re(.*)$": "x$1", "*": "fallback"}
	vMapping          = map[string]string{"m1": "qwen3.6-plus-x", "k": "kimi-k2.6", "gpt-*": "g", "*": "fallback"}
	qwenNativeMapping = map[string]string{"m1": "qwen3.6-plus-x", "k": "kimi-k2.6", "vl": "qwen-vl-max", "gpt-*": "g", "*": "fallback"}
	gemSafety         = map[string]string{"HARM_CATEGORY_HATE_SPEECH": "BLOCK_NONE", "HARM_CATEGORY_HARASSMENT": "BLOCK_ONLY_HIGH"}
	geminiThinking    = map[string]bool{"gemini-2.5-pro": true, "gemini-2.5-flash": true, "gemini-2.5-flash-lite": true}
)

// getMappedModel 复刻官方的 modelMapping 规则：精确 → 前缀（k*）→ 正则（~re，可带 $1）→ 通配 * → 原样。
func getMappedModel(model string, mapping map[string]string) string {
	if v, ok := mapping[model]; ok {
		return v
	}
	keys := make([]string, 0, len(mapping))
	for k := range mapping {
		keys = append(keys, k)
	}
	sort.Strings(keys) // 官方按 map 顺序遍历；这些映射表里的模式互不重叠，顺序不影响结果
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

// allowedFallback：官方成功而流式判定不支持时，各套件允许的已知原因。
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
		return has("重复的 key", "官方会跳过", "panic")
	case "gemini", "qwen_native":
		return has("重复的 key", "panic")
	case "openai":
		return chat && hasDeveloper(rec.In)
	default: // 变体
		return chat && hasDeveloper(rec.In) || has("不是对象", "messages 不是数组", "重复 key", "重复的 key")
	}
}

// ---- 比对（与 harness 相同）----

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
			out += fmt.Sprintf("[%s] 官方=%s 流式=%s  ", k, va, vb)
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
		return nil, false, "输出不是合法 JSON: " + err.Error()
	}
	return m, true, ""
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
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
					t.Fatalf("%s/%s: 黄金输出不是合法 JSON: %v", suite, rec.Name, err)
				}
			}
			for _, cs := range []int{1, 7, 64, 4096} {
				str, sok, why := runStream(newFor(rec), rec.In, cs)
				if !rec.OK {
					if cs == 1 {
						offFail++
					}
					if sok && !rec.Lenient {
						t.Fatalf("%s/%s chunk=%d: 官方失败但流式放行\n输入 %s", suite, rec.Name, cs, trunc(rec.In, 300))
					}
					if sok && cs == 1 {
						lenient++
					}
					continue
				}
				if rec.NeedFetch {
					if sok {
						t.Fatalf("%s/%s chunk=%d: 含 http 图片应回落却放行", suite, rec.Name, cs)
					}
					continue
				}
				if !sok {
					if !allowedFallback(rec, why) {
						t.Fatalf("%s/%s chunk=%d: 意外回落: %s\n输入 %s", suite, rec.Name, cs, why, trunc(rec.In, 300))
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
					t.Fatalf("%s/%s chunk=%d 与官方不一致: %s\n输入 %s", suite, rec.Name, cs, d, trunc(rec.In, 300))
				}
				if cs == 1 {
					same++
				}
			}
		}
		t.Logf("%-12s %d 条：一致 %d，官方失败 %d（其中丢弃字段类型错误、流式放行 %d），已知回落 %d，不一致 0", suite, len(recs), same, offFail, lenient, fallback)
	}
}
