package ason

import (
	"encoding/json"
	"testing"
	"unicode/utf8"
)

// 原生模糊：透传对合法 JSON 必须逐字节相同；拒绝面与 encoding/json 一致（UTF-8 默认不查，与它相同）；永不 panic。
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
		valid := json.Valid(in) && firstByte(in) == '{' // 文法 + 根必须是对象；不用 Unmarshal（1000e1000 之类会因溢出而失败）
		if valid && bad {
			t.Fatalf("合法对象被判定不支持: %q", in)
		}
		if !valid && !bad && len(in) < 10000 { // encoding/json 自己有 10000 层的嵌套上限，超长输入不比
			t.Fatalf("非法输入被放行: %q", in)
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

// 开启 UTF-8 校验：放行 ⇔ (默认模式放行 且 utf8.Valid(in))，放行时输出仍逐字节相同；RootAny 下数组根同样成立。
func FuzzStrictModes(f *testing.F) {
	for _, s := range []string{
		`{"a":"é中😀"}`, "{\"a\":\"\xC0\x80\"}", "{\"\xED\xA0\x80\":1}", "{\"a\":\"\xE4\xB8\"}",
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
			t.Fatalf("RootAny 拒绝了合法的对象/数组: %q", in)
		}
		if anyOK && !(isObj || isArr) {
			t.Fatalf("RootAny 放行了非对象/数组: %q", in)
		}
		if plainOK && !anyOK {
			t.Fatalf("默认模式放行但 RootAny 拒绝: %q", in)
		}
		if anyOK && string(anyOut) != string(in) || plainOK && string(plainOut) != string(in) {
			t.Fatalf("透传不保真: %q", in)
		}
		if wantU8 := anyOK && utf8.Valid(in); wantU8 != u8OK {
			t.Fatalf("UTF-8 校验判定 %v，期望 %v: %q", u8OK, wantU8, in)
		}
		if u8OK && string(u8Out) != string(in) {
			t.Fatalf("UTF-8 模式透传不保真: %q", in)
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
