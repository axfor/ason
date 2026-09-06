package ason

import (
	"encoding/json"
	"testing"
)

// 原生模糊：透传对合法 JSON 必须逐字节相同；对非法输入只能"判定不支持"或原样（UTF-8 不查）；永不 panic。
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
		var v map[string]any
		valid := json.Unmarshal(in, &v) == nil && v != nil // 根必须是对象（"null" 也能解进 map，但不是对象）
		if valid && bad {
			t.Fatalf("合法对象被判定不支持: %q", in)
		}
		if !bad && string(out) != string(in) {
			t.Fatalf("透传不保真:\n in  %q\n out %q", in, out)
		}
	})
}

// KeyProbe 改写永不 panic，且输出仍是合法 JSON（输入合法时）。
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
			t.Fatalf("输出不是合法 JSON:\n in  %q\n out %q", in, out)
		}
	})
}
