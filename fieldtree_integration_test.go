package ason

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// The field tree's integration side: what SetFieldTree does once a tree is attached to a transformer. The tree
// itself is built and checked in internal/fieldtree; these are the tests that need the engine.

// treeRoot, inner and sample are copied from internal/fieldtree's own tests rather than shared: the two sides
// test the same shapes from opposite ends, and a test fixture that spans a package boundary would have to be
// exported from a _test.go file, which Go does not allow anyway.

type customScalar struct{ n int }

type embedded struct {
	Store    bool `json:"store,omitempty"`
	Untagged string
}

type treeInner struct {
	A int             `json:"a"`
	B []string        `json:"b"`
	M map[string]int  `json:"m"`
	R json.RawMessage `json:"r"`
	X map[string]any  `json:"x"`
	Y []treeLeaf      `json:"y"`
}

type treeLeaf struct {
	N float64 `json:"n"`
	S string  `json:"s"`
}

type treeRoot struct {
	Name  string      `json:"name"`
	Inner treeInner   `json:"inner"`
	Ptr   *treeInner  `json:"ptr"`
	List  []treeInner `json:"list"`
	Free  any         `json:"free"`
}

type inner struct {
	A int `json:"a"`
}

type sample struct {
	embedded
	Str      string                 `json:"str,omitempty"`
	Num      float64                `json:"num,omitempty"`
	Int      int                    `json:"int,omitempty"`
	Flag     bool                   `json:"flag,omitempty"`
	Strs     []string               `json:"strs,omitempty"`
	Objs     []inner                `json:"objs,omitempty"`
	Obj      map[string]interface{} `json:"obj,omitempty"`
	MapInt   map[string]int         `json:"map_int,omitempty"`
	Struct   inner                  `json:"struct,omitempty"`
	Ptr      *inner                 `json:"ptr,omitempty"`
	Any      interface{}            `json:"any,omitempty"`
	Raw      json.RawMessage        `json:"raw,omitempty"`
	Custom   customScalar           `json:"custom,omitempty"`
	Bytes    []byte                 `json:"bytes,omitempty"`
	Skipped  string                 `json:"-"`
	unexport string
}

func TestSetFieldTreeNeverRejectsWhatUnmarshalAccepts(t *testing.T) {
	tree := FieldTreeOf(&treeRoot{}, 6)
	rnd := rand.New(rand.NewSource(5))

	var gen func(d int) string
	gen = func(d int) string {
		if d > 3 {
			return []string{`1`, `"s"`, `true`, `null`, `1.5`, `[]`, `{}`}[rnd.Intn(7)]
		}
		switch rnd.Intn(4) {
		case 0:
			return []string{`1`, `"s"`, `true`, `null`, `[1,2]`, `{"k":1}`, `"é€😀"`, `"a\"b"`}[rnd.Intn(8)]
		case 1:
			return "[" + gen(d+1) + "," + gen(d+1) + "]"
		default:
			keys := []string{"name", "inner", "ptr", "list", "free", "a", "b", "m", "r", "x", "y", "n", "s", "zz"}
			var b strings.Builder
			b.WriteByte('{')
			for i := 0; i < 1+rnd.Intn(3); i++ {
				if i > 0 {
					b.WriteByte(',')
				}
				fmt.Fprintf(&b, "%q:%s", keys[rnd.Intn(len(keys))], gen(d+1))
			}
			b.WriteByte('}')
			return b.String()
		}
	}

	falseReject, caught, checked := 0, 0, 0
	for i := 0; i < 20000; i++ {
		doc := `{"name":"x",` + strings.TrimPrefix(gen(1), "{")
		if !json.Valid([]byte(doc)) {
			continue
		}
		checked++
		var into treeRoot
		unmarshalOK := json.Unmarshal([]byte(doc), &into) == nil

		for _, chunk := range []int{1, 13, 4096} {
			tr := NewTransformer(BaseProtocol{})
			tr.SetFieldTypes(FieldTypesOf(&treeRoot{}))
			tr.SetFieldTree(tree)
			var out []byte
			for j := 0; j < len(doc); j += chunk {
				k := j + chunk
				if k > len(doc) {
					k = len(doc)
				}
				tr.Write([]byte(doc[j:k]))
				out = append(out, tr.Out()...)
			}
			out = append(out, tr.Finish()...)
			bad, why := tr.Unsupported()

			if unmarshalOK && bad {
				falseReject++
				if falseReject <= 3 {
					t.Errorf("误拒（分块 %d）：%s\n  原因 %s", chunk, doc, why)
				}
				break
			}
			if !bad {
				// 放行的请求，输出必须与输入逐字节相同（校验只读不改）
				if string(out) != doc {
					t.Fatalf("校验改动了输出（分块 %d）：\n 输入 %q\n 输出 %q", chunk, doc, string(out))
				}
			}
			if !unmarshalOK && bad && chunk == 1 {
				caught++
			}
		}
	}
	if falseReject > 0 {
		t.Fatalf("误拒 %d 例 —— 有这个数就不能上线", falseReject)
	}
	t.Logf("%d 例：误拒 0，其中 Unmarshal 拒绝且引擎也拒绝 %d 例", checked, caught)
}

// 嵌套里的类型错误要真的被抓到，而且透传的字节不能被改动。
func TestSetFieldTreeCatchesNestedMismatch(t *testing.T) {
	tree := FieldTreeOf(&treeRoot{}, 6)
	bad := []string{
		`{"inner":{"a":"x"}}`,
		`{"inner":{"b":[1]}}`,
		`{"inner":{"m":{"k":"s"}}}`,
		`{"inner":{"y":[{"n":"x"}]}}`,
		`{"list":[{"a":"x"}]}`,
		`{"ptr":{"a":[1,2]}}`,
	}
	for _, doc := range bad {
		var into treeRoot
		if json.Unmarshal([]byte(doc), &into) == nil {
			t.Fatalf("用例失效，Unmarshal 居然接受了：%s", doc)
		}
		for _, chunk := range []int{1, 7, 4096} {
			tr := NewTransformer(BaseProtocol{})
			tr.SetFieldTree(tree)
			for j := 0; j < len(doc); j += chunk {
				k := j + chunk
				if k > len(doc) {
					k = len(doc)
				}
				tr.Write([]byte(doc[j:k]))
				tr.Out()
			}
			tr.Finish()
			if bad, _ := tr.Unsupported(); !bad {
				t.Errorf("没抓到（分块 %d）：%s", chunk, doc)
			}
		}
	}
}

// 超过校验缓冲上限的容器要放行，不能因为"太大看不完"就拒绝。
func TestSetFieldTreeAcceptsOversizeContainer(t *testing.T) {
	tree := FieldTreeOf(&treeRoot{}, 6)
	doc := `{"inner":{"m":{"k":"` + strings.Repeat("y", 80<<10) + `"}}}`
	tr := NewTransformer(BaseProtocol{})
	tr.SetFieldTree(tree)
	tr.Write([]byte(doc))
	tr.Finish()
	if bad, why := tr.Unsupported(); bad {
		t.Fatalf("超过上限的容器被拒了：%s", why)
	}
}

// 树里本来就带着根字段自己的类型，所以设了树就不该再要求调用方设一遍扁平表。
// 这是 API 定型时合并的一处：两个入口做同一件事，调用方漏设一个就是静默地少了一层校验。
func TestSetFieldTreeImpliesRootTypes(t *testing.T) {
	tree := FieldTreeOf(&treeRoot{}, 6)

	// 只设树，根级类型错误也要抓到
	tr := NewTransformer(BaseProtocol{})
	tr.SetFieldTree(tree)
	tr.Write([]byte(`{"name":123}`)) // name 是 string
	tr.Finish()
	if bad, _ := tr.Unsupported(); !bad {
		t.Fatal("只设树时，根级类型错误没被抓到")
	}

	// 调用方自己设过扁平表时，不覆盖它
	custom := map[string]FieldTypes{"name": TypeAny}
	tr2 := NewTransformer(BaseProtocol{})
	tr2.SetFieldTypes(custom)
	tr2.SetFieldTree(tree)
	tr2.Write([]byte(`{"name":123}`))
	tr2.Finish()
	if bad, why := tr2.Unsupported(); bad {
		t.Fatalf("调用方显式设的表被树覆盖了：%s", why)
	}
}
func TestFieldTypesMatchEncodingJSON(t *testing.T) {
	types := FieldTypesOf(&sample{})
	if len(types) == 0 {
		t.Fatal("没有推导出任何字段")
	}

	values := map[string]string{
		"字符串":   `"s"`,
		"数字":    `1`,
		"小数":    `1.5`,
		"真":     `true`,
		"空":     `null`,
		"对象":    `{"a":1}`,
		"数组":    `[1,2]`,
		"字符串数组": `["x"]`,
	}

	fields := make([]string, 0, len(types))
	for k := range types {
		fields = append(fields, k)
	}

	falseReject, falseAccept := 0, 0
	for _, f := range fields {
		for vn, v := range values {
			doc := fmt.Sprintf(`{"pad":"%s","%s":%s}`, strings.Repeat("x", 32), f, v)

			var s sample
			unmarshalOK := json.Unmarshal([]byte(doc), &s) == nil

			tr := NewTransformer(BaseProtocol{})
			tr.SetFieldTypes(types)
			tr.Write([]byte(doc))
			tr.Finish()
			bad, why := tr.Unsupported()
			asonOK := !bad

			switch {
			case unmarshalOK && !asonOK:
				falseReject++
				t.Errorf("误拒：字段 %s = %s（%s），Unmarshal 接受而本实现拒绝：%s", f, vn, v, why)
			case !unmarshalOK && asonOK:
				// 允许的方向：文档里说明了本实现比 Unmarshal 宽的两处
				falseAccept++
			}
		}
	}
	t.Logf("共 %d 组，宽松放行 %d 组（整数收到小数、大小写不敏感匹配），误拒 %d 组",
		len(fields)*len(values), falseAccept, falseReject)
}

// 宽松的那几处必须是已知的，不能是随手漏掉的。逐条钉住。
func TestFieldTypesKnownLooseCases(t *testing.T) {
	types := FieldTypesOf(&sample{})
	loose := []struct {
		name string
		doc  string
	}{
		{"整数字段收到小数", `{"int":1.5}`},
		{"大小写不敏感的字段名", `{"INT":"x"}`},
	}
	for _, c := range loose {
		var s sample
		if json.Unmarshal([]byte(c.doc), &s) == nil {
			t.Fatalf("%s：这一条应当是 Unmarshal 拒绝的，用例失效了", c.name)
		}
		tr := NewTransformer(BaseProtocol{})
		tr.SetFieldTypes(types)
		tr.Write([]byte(c.doc))
		tr.Finish()
		if bad, _ := tr.Unsupported(); bad {
			t.Fatalf("%s：本实现拒绝了，说明文档里的宽松说明该更新了", c.name)
		}
	}
}

// 推导出来的表本身要对。
func TestFieldTypesOnlyChecksRootLevel(t *testing.T) {
	tr := NewTransformer(BaseProtocol{})
	tr.SetFieldTypes(map[string]FieldTypes{"num": TypeNumber})
	tr.Write([]byte(`{"a":{"num":"这是嵌套层的，不该被查"},"num":1}`))
	tr.Finish()
	if bad, why := tr.Unsupported(); bad {
		t.Fatalf("嵌套层的同名字段被误判了：%s", why)
	}
}

// 表里没有的字段一律放行；空位集合只接受 null。
func TestFieldTypesUnknownAndEmptySet(t *testing.T) {
	tr := NewTransformer(BaseProtocol{})
	tr.SetFieldTypes(map[string]FieldTypes{"only_null": 0})
	tr.Write([]byte(`{"not_in_table":{"anything":[1,2]},"only_null":null}`))
	tr.Finish()
	if bad, why := tr.Unsupported(); bad {
		t.Fatalf("不该拒绝：%s", why)
	}

	tr2 := NewTransformer(BaseProtocol{})
	tr2.SetFieldTypes(map[string]FieldTypes{"only_null": 0})
	tr2.Write([]byte(`{"only_null":1}`))
	tr2.Finish()
	if bad, _ := tr2.Unsupported(); !bad {
		t.Fatal("空位集合应当只接受 null")
	}
}
