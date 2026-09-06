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

// 合法的多字节 UTF-8（含 4 字节 emoji）在开启校验后原样透传，任何分块方式都一样。
func TestUTF8ValidPassthrough(t *testing.T) {
	ins := []string{
		`{"名字":"张三","emoji":"😀🎉","mix":"a€b中c𝄞d","ctrl":"é\n"}`,
		"{\"k\\u4e2d\":\"é中\U0001F600\"}",
		`{"a":{"深":["层","值",{"键":"值"}]}}`,
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

// 非法序列：过长编码、代理对、超出 U+10FFFF、非法首字节、孤立续字节、被引号/反斜杠/输入结尾截断。
func TestUTF8InvalidRejected(t *testing.T) {
	bad := map[string]string{
		"过长 2 字节 C0 80":    "{\"s\":\"\xC0\x80\"}",
		"过长 2 字节 C1 BF":    "{\"s\":\"\xC1\xBF\"}",
		"过长 3 字节 E0 80 80": "{\"s\":\"\xE0\x80\x80\"}",
		"过长 4 字节 F0 80":    "{\"s\":\"\xF0\x80\x80\x80\"}",
		"代理对 ED A0 80":     "{\"s\":\"\xED\xA0\x80\"}",
		"代理对 ED BF BF":     "{\"s\":\"\xED\xBF\xBF\"}",
		"超出 U+10FFFF":      "{\"s\":\"\xF4\x90\x80\x80\"}",
		"首字节 F5":           "{\"s\":\"\xF5\x80\x80\x80\"}",
		"首字节 FF":           "{\"s\":\"\xFF\"}",
		"孤立续字节":            "{\"s\":\"a\x80b\"}",
		"续字节不足后接引号":        "{\"s\":\"\xE4\xB8\"}",
		"续字节不足后接反斜杠":       "{\"s\":\"\xE4\xB8\\n\"}",
		"续字节不足后接 ASCII":    "{\"s\":\"\xE4\xB8x\"}",
		"续字节不足后接新序列":       "{\"s\":\"\xE4\xB8\xE4\xB8\xAD\"}",
		"key 里过长编码":        "{\"\xC0\x80\":1}",
		"key 里孤立续字节":       "{\"a\x80\":1}",
		"key 里截断":          "{\"\xE4\xB8\":1}",
		"key 里代理对":         "{\"\xED\xA0\x80\":1}",
		"Skip 区域里":         "{\"skip\":{\"s\":\"\xC0\x80\"}}",
		"Pass 深层里":         "{\"a\":[1,{\"b\":\"\xFF\"}]}",
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
					t.Fatalf("%s chunk=%d: 非法 UTF-8 被放行了", name, cs)
				}
			}
		}
		// 默认不校验：与 encoding/json 的 Valid 一致（只查文法，不查编码）
		tr := NewTransformer(BaseProtocol{})
		got, ok, why := feedAll(tr, in, len(in))
		if !ok || got != in {
			t.Fatalf("%s 默认模式应透传: ok=%v %s", name, ok, why)
		}
		if !json.Valid([]byte(in)) {
			t.Fatalf("%s: 前提不成立，encoding/json 也拒绝它", name)
		}
	}
}

// 输入在序列中间结束：Finish 必须判定不支持，而不是把半个字符吐出去。
func TestUTF8TruncatedAtEOF(t *testing.T) {
	tr := NewTransformer(BaseProtocol{})
	tr.SetValidateUTF8(true)
	tr.Write([]byte("{\"s\":\"\xE4\xB8"))
	tr.Out()
	tr.Finish()
	if u, _ := tr.Unsupported(); !u {
		t.Fatal("序列中间结束的输入被放行了")
	}
}

// 随机字节流：开启校验时的判定必须与 utf8.Valid 完全一致，且与分块无关。
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
				t.Fatalf("%q chunk=%d: 判定 %v，utf8.Valid=%v (%s)", b, cs, ok, want, why)
			}
			if ok && got != in {
				t.Fatalf("%q: 输出被改动", b)
			}
		}
	}
}

// 重复 key 策略：First 只派发第一个；Bail 判定不支持；默认照常派发。
func TestDupKeysPolicy(t *testing.T) {
	enterAll := &dupProto{}
	cases := []struct {
		name string
		in   string
		pol  DupKeys
		want string // "" = 期望不支持
	}{
		{"默认透传", `{"a":1,"a":2}`, DupKeysPass, `{"a":1,"a":2}`},
		{"First 顶层", `{"a":1,"a":2,"b":3}`, DupKeysFirst, `{"a":1,"b":3}`},
		{"First 末尾", `{"b":3,"a":1,"a":2}`, DupKeysFirst, `{"b":3,"a":1}`},
		{"First 三次", `{"a":1,"a":{"x":[1]},"a":"s"}`, DupKeysFirst, `{"a":1}`},
		{"First 带空白", "{ \"a\" : 1 , \"a\" : 2 }", DupKeysFirst, "{ \"a\" : 1 }"},
		{"First 只查派发帧", `{"a":1,"a":2,"o":{"c":1,"c":2}}`, DupKeysFirst, `{"a":1,"o":{"c":1}}`},
		{"First 转义同名", `{"a":1,"a":2}`, DupKeysFirst, `{"a":1}`},
		{"Bail", `{"a":1,"a":2}`, DupKeysBail, ""},
		{"Bail 深层", `{"o":{"c":1,"c":2}}`, DupKeysBail, ""},
		{"Bail 无重复", `{"a":1,"b":{"a":1}}`, DupKeysBail, `{"a":1,"b":{"a":1}}`},
	}
	for _, c := range cases {
		for _, cs := range chunkSizes(len(c.in)) {
			tr := NewTransformer(enterAll)
			tr.SetDupKeys(c.pol)
			got, ok, why := feedAll(tr, c.in, cs)
			if c.want == "" {
				if ok {
					t.Fatalf("%s chunk=%d: 应判定不支持，却输出 %q", c.name, cs, got)
				}
				if !strings.Contains(why, "重复的 key") {
					t.Fatalf("%s: 原因不对: %s", c.name, why)
				}
				continue
			}
			if !ok || got != c.want {
				t.Fatalf("%s chunk=%d: ok=%v %s\n got %q\nwant %q", c.name, cs, ok, why, got, c.want)
			}
		}
	}
	// 兼容：DupKeyBail 字段等价于 DupKeysBail
	tr := NewTransformer(enterAll)
	tr.DupKeyBail = true
	if _, ok, _ := feedAll(tr, `{"a":1,"a":2}`, 3); ok {
		t.Fatal("DupKeyBail 字段失效")
	}
}

// dupProto 对每个对象值都 Enter（让深层也成为派发帧）。
type dupProto struct{ BaseProtocol }

func (dupProto) OnKey(t *Transformer) Action { return Enter().Lenient() }

// First 策略与 Capture 改写组合：只有第一个被改写，后面的同名 key 被丢弃（gjson 取首个的语义）。
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

// Defer 回放不会把自己当成重复 key。
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
	if t.Depth() == 0 { // 根闭合：路径仍指向容器本身
		p.released = true
		t.ReleaseNow()
	}
}

// 根形状：默认只接受对象；RootArray 只接受数组；RootAny 两者都接受。数组根按下标派发。
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
		{"默认拒绝数组", arr, RootObject, false},
		{"默认接受对象", obj, RootObject, true},
		{"RootArray 接受数组", arr, RootArray, true},
		{"RootArray 拒绝对象", obj, RootArray, false},
		{"RootAny 数组", arr, RootAny, true},
		{"RootAny 对象", obj, RootAny, true},
		{"RootAny 标量", `1`, RootAny, false},
		{"RootAny 字符串", `"x"`, RootAny, false},
		{"数组根带空白", ws, RootAny, true},
		{"空数组根", `[]`, RootArray, true},
		{"数组根后多余内容", `[1] 2`, RootArray, false},
		{"数组根末尾逗号", `[1,]`, RootArray, false},
		{"数组根未闭合", `[1`, RootArray, false},
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
				t.Fatalf("%s chunk=%d: 透传应逐字节一致\n got %q\nwant %q", c.name, cs, got, c.in)
			}
		}
	}
}

// 数组根的元素经 OnElem 派发：下标可读，Enter 进对象元素改写内部 key，Skip 整个元素时分隔符正确。
func TestRootArrayDispatch(t *testing.T) {
	in := `[{"model":"a","x":1},{"model":"b"},3,{"model":"c","y":[1]}]`
	want := `[{"model":"R","x":1},{"model":"R"},{"model":"R","y":[1]}]` // 下标 2 的标量被 Skip
	for _, cs := range chunkSizes(len(in)) {
		p := &rootArrProto{}
		tr := NewTransformer(p)
		tr.SetRoot(RootArray)
		got, ok, why := feedAll(tr, in, cs)
		if !ok || got != want {
			t.Fatalf("chunk=%d: ok=%v %s\n got %q\nwant %q", cs, ok, why, got, want)
		}
		if p.seen != "0,1,2,3" {
			t.Fatalf("chunk=%d: OnElem 下标序列 %q", cs, p.seen)
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

// 区域（Pass / Skip / Capture）里的文法与派发帧一视同仁：随机结构垃圾的判定必须与 encoding/json 完全一致。
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
		{"Pass 区域", func() *Transformer { return NewTransformer(BaseProtocol{}) }},
		{"派发帧", func() *Transformer { return NewTransformer(enterAll) }},
		{"Skip 区域", func() *Transformer { return NewKeyProbeTransformer(skipA) }},
		{"Capture 区域", func() *Transformer { return NewKeyProbeTransformer(capA) }},
	}
	seen := map[string]bool{}
	for i := 0; i < 20000; i++ {
		var sb strings.Builder
		n := r.Intn(8) + 1
		for k := 0; k < n; k++ {
			sb.WriteString(toks[r.Intn(len(toks))])
		}
		in := `{"a":` + sb.String() + `}`
		if r.Intn(4) == 0 { // 也测“垃圾在第二个字段”的位置
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
					t.Fatalf("%s chunk=%d: %q 判定 %v (%s)，encoding/json=%v", m.name, cs, in, ok, why, want)
				}
				if ok && m.name != "Skip 区域" && got != in {
					t.Fatalf("%s: %q 输出被改动: %q", m.name, in, got)
				}
			}
		}
	}
}

// 区域里的典型错误：每一条 encoding/json 都拒绝，流式也必须拒绝——不分它落在 Pass 还是 Skip 区域。
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
			t.Fatalf("前提不成立，encoding/json 接受 %q", in)
		}
		for _, cs := range chunkSizes(len(in)) {
			if _, ok, _ := feedAll(NewTransformer(BaseProtocol{}), in, cs); ok {
				t.Fatalf("Pass 区域放行了 %q (chunk=%d)", in, cs)
			}
			if _, ok, _ := feedAll(NewKeyProbeTransformer(skipA), in, cs); ok {
				t.Fatalf("Skip 区域放行了 %q (chunk=%d)", in, cs)
			}
		}
	}
	for _, in := range good {
		for _, cs := range chunkSizes(len(in)) {
			got, ok, why := feedAll(NewTransformer(BaseProtocol{}), in, cs)
			if !ok || got != in {
				t.Fatalf("合法输入被拒绝或改动 %q chunk=%d: %v %s %q", in, cs, ok, why, got)
			}
		}
	}
}
