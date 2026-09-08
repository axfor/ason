package ason

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// 类型树是嵌套校验的前置条件，而不是嵌套校验本身。
// 先证明它对任意深度、任意 JSON 值的判定都不严于 encoding/json —— 有了这个证据，才谈改行为。
// 顺序反了就是拿正常流量当试验场。

// walkTree 按树判定一份文档合不合法。这是"如果按树校验会怎样"的离线模拟，
// 引擎里并没有接它 —— 目的就是在不影响任何请求的前提下把证据先攒够。
func walkTree(t *FieldTree, v any) bool {
	if t == nil || t.Any {
		return true
	}
	var bit FieldTypes
	switch x := v.(type) {
	case nil:
		return true // null 对任何类型都合法，与 encoding/json 一致
	case string:
		bit = TypeString
	case float64:
		bit = TypeNumber
	case bool:
		bit = TypeBool
	case map[string]any:
		bit = TypeObject
		if t.Types&bit == 0 {
			return false
		}
		for k, vv := range x {
			if t.Keys != nil {
				if sub, ok := t.Keys[k]; ok && !walkTree(sub, vv) {
					return false
				}
				continue
			}
			if !walkTree(t.Elem, vv) {
				return false
			}
		}
		return true
	case []any:
		bit = TypeArray
		if t.Types&bit == 0 {
			return false
		}
		for _, vv := range x {
			if !walkTree(t.Elem, vv) {
				return false
			}
		}
		return true
	default:
		return true
	}
	return t.Types&bit != 0
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

// 判定不能严于 encoding/json：Unmarshal 接受的，树也必须接受。
func TestFieldTreeNeverStricterThanEncodingJSON(t *testing.T) {
	tree := FieldTreeOf(&treeRoot{}, 6)
	if tree == nil || tree.Keys == nil {
		t.Fatal("没有推导出树")
	}

	docs := []string{
		`{"name":"s"}`, `{"name":null}`, `{"inner":{"a":1}}`, `{"inner":{"a":null}}`,
		`{"inner":{"b":["x","y"]}}`, `{"inner":{"b":null}}`, `{"inner":{"m":{"k":1}}}`,
		`{"inner":{"r":{"anything":[1,"2",null]}}}`, `{"inner":{"r":"even a string"}}`,
		`{"inner":{"x":{"k":[1,2,{"deep":true}]}}}`, `{"inner":{"y":[{"n":1.5,"s":"a"}]}}`,
		`{"ptr":{"a":1}}`, `{"ptr":null}`, `{"list":[{"a":1},{"a":2}]}`,
		`{"free":{"whatever":[1,2]}}`, `{"free":"scalar"}`, `{"free":null}`,
		`{"unknown_field":{"deep":[1,2]}}`, `{}`,
		// 这些是 Unmarshal 会拒的，树也应当拒
		`{"name":1}`, `{"inner":{"a":"x"}}`, `{"inner":{"b":[1]}}`, `{"inner":{"m":{"k":"s"}}}`,
		`{"inner":{"y":[{"n":"x"}]}}`, `{"list":"x"}`, `{"ptr":1}`,
	}
	strictHits := 0
	for _, d := range docs {
		var into treeRoot
		unmarshalOK := json.Unmarshal([]byte(d), &into) == nil
		var generic any
		if json.Unmarshal([]byte(d), &generic) != nil {
			t.Fatalf("用例本身不是合法 JSON: %s", d)
		}
		treeOK := walkTree(tree, generic)
		if unmarshalOK && !treeOK {
			t.Errorf("误拒：Unmarshal 接受而树拒绝：%s", d)
		}
		if !unmarshalOK && !treeOK {
			strictHits++
		}
	}
	t.Logf("%d 个用例，其中 %d 个是 Unmarshal 拒绝且树也拒绝的（嵌套校验将来能拿到的部分）", len(docs), strictHits)
}

// 随机文档上跑同一条不变式，并顺带量出嵌套校验相对根级能多抓多少。
func TestFieldTreeOnRandomDocuments(t *testing.T) {
	tree := FieldTreeOf(&treeRoot{}, 6)
	flat := FieldTypesOf(&treeRoot{})
	rnd := rand.New(rand.NewSource(3))

	var gen func(d int) string
	gen = func(d int) string {
		if d > 3 {
			return []string{`1`, `"s"`, `true`, `null`, `1.5`}[rnd.Intn(5)]
		}
		switch rnd.Intn(4) {
		case 0:
			return []string{`1`, `"s"`, `true`, `null`, `[1,2]`, `{"k":1}`}[rnd.Intn(6)]
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

	falseReject, treeCaught, rootCaught := 0, 0, 0
	for i := 0; i < 20000; i++ {
		doc := "{\"name\":\"x\"," + strings.TrimPrefix(gen(1), "{")
		if !json.Valid([]byte(doc)) {
			continue
		}
		var into treeRoot
		unmarshalOK := json.Unmarshal([]byte(doc), &into) == nil
		var generic any
		if json.Unmarshal([]byte(doc), &generic) != nil {
			continue
		}
		treeOK := walkTree(tree, generic)
		if unmarshalOK && !treeOK {
			falseReject++
			if falseReject <= 3 {
				t.Errorf("误拒：%s", doc)
			}
			continue
		}
		if !unmarshalOK {
			if !treeOK {
				treeCaught++
			}
			// 根级能不能抓到：只看顶层字段的类型
			if m, ok := generic.(map[string]any); ok {
				for k, v := range m {
					want, known := flat[k]
					if !known {
						continue
					}
					var bit FieldTypes
					switch v.(type) {
					case string:
						bit = TypeString
					case float64:
						bit = TypeNumber
					case bool:
						bit = TypeBool
					case map[string]any:
						bit = TypeObject
					case []any:
						bit = TypeArray
					default:
						continue
					}
					if want&bit == 0 {
						rootCaught++
						break
					}
				}
			}
		}
	}
	if falseReject > 0 {
		t.Fatalf("误拒 %d 例 —— 有这个数就不能谈上线嵌套校验", falseReject)
	}
	t.Logf("Unmarshal 拒绝的用例里：根级校验能抓 %d 例，按树校验能抓 %d 例（%.1f 倍）",
		rootCaught, treeCaught, float64(treeCaught)/float64(max(rootCaught, 1)))
}

// 接进引擎之后的判定，必须和离线走树的判定一致，而且同样永不严于 encoding/json。
// 这是整套改动里唯一会把"本来能过的请求"变成"过不去"的地方，所以证据要按误拒来组织。
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
