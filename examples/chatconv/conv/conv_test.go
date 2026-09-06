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

// ---- 参照实现：整体缓冲后按同样的规则转换（差分测试的对照物）----

func reference(in []byte, opt Options) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(in))
	dec.UseNumber()
	var src map[string]any
	if err := dec.Decode(&src); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("根后有内容")
	}
	out := map[string]any{}
	model := ""
	if v, ok := src["model"]; ok {
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("model 不是字符串")
		}
		model = s
	}
	out["model"] = opt.MapModel(model)
	if v, ok := src["max_tokens"]; ok {
		n, ok := v.(json.Number)
		if !ok || strings.ContainsAny(n.String(), ".eE") {
			return nil, errors.New("max_tokens 不是整数")
		}
		out["max_tokens"] = n
	} else {
		out["max_tokens"] = json.Number(fmt.Sprint(opt.DefaultMaxTokens))
	}
	var system []string
	if v, ok := src["messages"]; ok {
		arr, ok := v.([]any)
		if !ok {
			return nil, errors.New("messages 不是数组")
		}
		msgs := []any{}
		for _, e := range arr {
			m, ok := e.(map[string]any)
			if !ok {
				return nil, errors.New("消息不是对象")
			}
			rv, ok := m["role"]
			if !ok {
				return nil, errors.New("消息没有 role")
			}
			role, ok := rv.(string)
			if !ok {
				return nil, errors.New("role 不是字符串")
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
								continue // 没有 text：什么都没写，Lazy 层丢弃
							}
							s, ok := tv.(string)
							if !ok {
								return nil, errors.New("text 不是字符串")
							}
							parts = append(parts, map[string]any{"type": "text", "text": s})
						case "image_url":
							iv, ok := pm["image_url"]
							if !ok {
								continue
							}
							im, ok := iv.(map[string]any)
							if !ok {
								return nil, errors.New("image_url 不是对象")
							}
							uv, ok := im["url"]
							if !ok {
								continue
							}
							u, ok := uv.(string)
							if !ok {
								return nil, errors.New("url 不是字符串")
							}
							if !strings.HasPrefix(u, "data:") {
								return nil, errors.New("图片不是 data URL")
							}
							semi := strings.IndexByte(u, ';')
							comma := strings.IndexByte(u, ',')
							if semi < 0 || comma < 0 || comma < semi {
								return nil, errors.New("data URL 格式不对")
							}
							parts = append(parts, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": u[5:semi], "data": u[comma+1:]}})
						}
					}
					om["content"] = parts
				default:
					return nil, errors.New("content 既不是字符串也不是数组")
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
			return nil, errors.New("stop 不是数组")
		}
		for _, e := range arr {
			if _, ok := e.(string); !ok {
				return nil, errors.New("stop 元素不是字符串")
			}
		}
		if len(arr) > 0 {
			out["stop_sequences"] = arr
		}
	}
	if v, ok := src["tools"]; ok && v != nil {
		arr, ok := v.([]any)
		if !ok {
			return nil, errors.New("tools 不是数组")
		}
		tools := []any{}
		for _, e := range arr {
			tm, ok := e.(map[string]any)
			if !ok {
				return nil, errors.New("tools 元素不是对象")
			}
			ot := map[string]any{"name": ""}
			if fv, ok := tm["function"]; ok {
				fm, ok := fv.(map[string]any)
				if !ok {
					return nil, errors.New("function 不是对象")
				}
				if nv, ok := fm["name"]; ok {
					s, ok := nv.(string)
					if !ok {
						return nil, errors.New("name 不是字符串")
					}
					ot["name"] = s
				}
				if dv, ok := fm["description"]; ok {
					ot["description"] = dv
				}
				if pv, ok := fm["parameters"]; ok {
					pm, ok := pv.(map[string]any)
					if !ok {
						return nil, errors.New("parameters 不是对象")
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

// ---- 驱动 ----

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
		return nil, false, "输出不是合法 JSON: " + err.Error() + " :: " + string(out)
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
				t.Fatalf("%s chunk=%d: 参照失败(%v)但流式放行了: %v\n输入 %s", name, cs, rerr, got, in)
			}
			continue
		}
		if !ok {
			if strings.Contains(why, "缓冲超过上限") {
				continue // 有界前瞻：值先于决定其形状的字段到达且超过暂存上限，只能回落——设计上的已知回落
			}
			t.Fatalf("%s chunk=%d: 参照成功但流式判定不支持: %s\n输入 %s", name, cs, why, in)
		}
		if !reflect.DeepEqual(got, ref) {
			g, _ := json.Marshal(got)
			r, _ := json.Marshal(ref)
			t.Fatalf("%s chunk=%d 不一致\n输入 %s\n流式 %s\n参照 %s", name, cs, in, g, r)
		}
	}
}

// ---- 手写场景（引擎的每条路径各至少一例）----

func TestScenarios(t *testing.T) {
	big := strings.Repeat("y", 100<<10)
	cases := map[string]string{
		"最简":              `{"model":"demo/m","messages":[{"role":"user","content":"U"}]}`,
		"system上提":        `{"model":"m","messages":[{"role":"system","content":"S1"},{"role":"user","content":"U"},{"role":"system","content":"S2"}]}`,
		"system数组":        `{"model":"m","messages":[{"role":"system","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}]}`,
		"全是system":        `{"model":"m","messages":[{"role":"system","content":"S"}]}`,
		"content先于role":   `{"model":"m","messages":[{"content":"U","role":"user"},{"content":"S","role":"system"}]}`,
		"content先于role大":  `{"model":"m","messages":[{"content":"` + big[:50<<10] + `","role":"user"}]}`,
		"parts":           `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}]}`,
		"parts类型在后":       `{"model":"m","messages":[{"role":"user","content":[{"text":"a","type":"text"},{"image_url":{"url":"data:image/png;base64,QUJD"},"type":"image_url"}]}]}`,
		"图片":              `{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + strings.Repeat("/9j/", 20000) + `","detail":"low"}}]}]}`,
		"图片无base64标记":     `{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:text/plain;charset=utf-8,hello"}}]}]}`,
		"未知part":          `{"model":"m","messages":[{"role":"user","content":[{"type":"audio","data":"x"},{"type":"text","text":"a"},{"text":"no type"}]}]}`,
		"part非对象":         `{"model":"m","messages":[{"role":"user","content":[1,"x",null,{"type":"text","text":"a"}]}]}`,
		"role与字段不符":       `{"model":"m","messages":[{"role":"user","content":[{"type":"text","image_url":{"url":"x"},"text":"a"}]}]}`,
		"无content":        `{"model":"m","messages":[{"role":"assistant"},{"role":"user","content":"U"}]}`,
		"stop":            `{"model":"m","messages":[{"role":"user","content":"U"}],"stop":["a","b\n"],"max_tokens":5}`,
		"stop空与null":      `{"model":"m","messages":[],"stop":[],"tools":null}`,
		"tools":           `{"model":"m","messages":[],"tools":[{"type":"function","function":{"name":"f","description":"d","parameters":{"type":"object","properties":{"a":{"type":"string"}}}}},{"function":{"parameters":{},"name":"g"}},{"type":"function"}]}`,
		"tools乱序":         `{"tools":[{"function":{"parameters":{"z":[1,2.5e-3,null]},"description":null,"name":"f"},"type":"function"}],"messages":[{"role":"user","content":"U"}],"model":"m"}`,
		"丢弃字段":            `{"model":"m","temperature":0.5,"metadata":{"messages":[{"role":"x"}]},"messages":[{"role":"user","content":"U","name":"n","extra":[1,{"a":null}]}]}`,
		"缺model":          `{"messages":[{"role":"user","content":"U"}]}`,
		"缺messages":       `{"model":"m","max_tokens":12}`,
		"格式化":             "{\n  \"model\" : \"demo/m\" ,\n  \"messages\" : [ { \"role\" : \"user\" , \"content\" : \"U\" } ] ,\n  \"stop\" : [ \"a\" ]\n}\n",
		"转义与unicode":      `{"model":"mA","messages":[{"role":"user","content":"中😀\"\\\n\t"}]}`,
		"大文本":             `{"model":"m","messages":[{"role":"user","content":"` + big + `"}]}`,
		"错误：role缺失":       `{"model":"m","messages":[{"content":"U"}]}`,
		"错误：role数字":       `{"model":"m","messages":[{"role":1,"content":"U"}]}`,
		"错误：content null": `{"model":"m","messages":[{"role":"user","content":null}]}`,
		"错误：content对象":    `{"model":"m","messages":[{"role":"user","content":{"a":1}}]}`,
		"错误：stop数字":       `{"model":"m","messages":[],"stop":["a",1]}`,
		"错误：stop字符串":      `{"model":"m","messages":[],"stop":"a"}`,
		"错误：http图片":       `{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://x/y.png"}}]}]}`,
		"错误：max_tokens小数": `{"model":"m","messages":[],"max_tokens":1.5}`,
		"错误：model数字":      `{"model":1,"messages":[]}`,
		"错误：tools元素":      `{"model":"m","messages":[],"tools":[5]}`,
		"错误：parameters数组": `{"model":"m","messages":[],"tools":[{"function":{"name":"f","parameters":[]}}]}`,
		"错误：非法JSON":       `{"model":"m","messages":[{"role":"user","content":"U"}],"stream":tru}`,
		"错误：根后有内容":        `{"model":"m","messages":[]} x`,
	}
	for name, in := range cases {
		check(t, name, []byte(in))
	}
}

// ---- 随机差分：字段顺序、取值、形状全随机 ----

func TestFuzzDifferential(t *testing.T) {
	r := rand.New(rand.NewSource(20260906))
	pick := func(xs ...string) string { return xs[r.Intn(len(xs))] }
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	text := func() string {
		alph := []string{"a", "中", `"`, `\`, "\n", "\t", "é", "😀", " ", "<", "\x01"}
		n := r.Intn(30)
		var sb strings.Builder
		for i := 0; i < n; i++ {
			sb.WriteString(alph[r.Intn(len(alph))])
		}
		if r.Intn(20) == 0 {
			sb.WriteString(strings.Repeat("y", 70<<10)) // 越过提交点
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
			f = append(f, `"type":"text"`, `"text":`+pick(`1`, `null`, `["a"]`)) // 错误
		case 6:
			f = append(f, `"type":"image_url"`, `"image_url":`+pick(`{"url":"https://x/y.png"}`, `5`, `{"url":1}`, `{"detail":"low"}`)) // 错误或丢弃
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
			f = append(f, `"role":`+pick(`1`, `null`)) // 错误
		case 3: // 缺 role：错误
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
			f = append(f, `"content":`+pick(`null`, `7`, `{"a":1}`)) // system 以外是错误
		case 6: // 无 content
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
			fn = append(fn, `"parameters":`+pick(`[]`, `5`, `null`)) // 错误
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
		check(t, fmt.Sprintf("随机#%d", i), []byte(in))
	}
}

// 重复 key：目标是 struct 语义（后者覆盖前者），流式无法复刻，必须判定不支持而不是放行。
func TestDuplicateKeysBail(t *testing.T) {
	for _, in := range []string{
		`{"model":"a","model":"b","messages":[]}`,
		`{"model":"m","messages":[{"role":"user","role":"system","content":"U"}]}`,
	} {
		if _, ok, _ := stream([]byte(in), 3, opt); ok {
			t.Fatalf("重复 key 应判定不支持: %s", in)
		}
	}
}

// 提交点语义：64KB 之前 Out() 为空，之后逐块有输出；末块 Finish 一次交出剩余。
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
			t.Fatalf("提交点之前不该有输出 (at %d)", j)
		}
		if j >= ason.CommitBytes+8192 && len(out) == 0 && !seenOut {
			t.Fatalf("提交点之后应逐块有输出 (at %d)", j)
		}
		if len(out) > 0 {
			seenOut = true
		}
	}
	if fin := tr.Finish(); len(fin) == 0 {
		t.Fatal("Finish 应交出剩余输出")
	}
}
