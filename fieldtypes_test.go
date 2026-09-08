package ason

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type embedded struct {
	Store    bool `json:"store,omitempty"`
	Untagged string
}

type inner struct {
	A int `json:"a"`
}

type customScalar struct{ n int }

func (c *customScalar) UnmarshalJSON(b []byte) error { return nil }

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

// 每种 JSON 值都试一遍，判定必须和 json.Unmarshal 一致。
// 危险的方向只有一个：把 Unmarshal 会接受的文档判成非法 —— 那会拦掉正常流量。
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
func TestFieldTypesOfShape(t *testing.T) {
	m := FieldTypesOf(sample{})
	want := map[string]FieldTypes{
		"str":      TypeString,
		"num":      TypeNumber,
		"int":      TypeNumber,
		"flag":     TypeBool,
		"strs":     TypeArray,
		"objs":     TypeArray,
		"obj":      TypeObject,
		"map_int":  TypeObject,
		"struct":   TypeObject,
		"ptr":      TypeObject,
		"any":      TypeAny,
		"raw":      TypeAny, // json.RawMessage 自己实现了 UnmarshalJSON
		"custom":   TypeAny, // 同上
		"bytes":    TypeArray | TypeString,
		"store":    TypeBool,   // 内嵌结构体被摊平
		"Untagged": TypeString, // 无 tag 时用字段名
	}
	for k, w := range want {
		if got, ok := m[k]; !ok {
			t.Errorf("字段 %s 没有推导出来", k)
		} else if got != w {
			t.Errorf("字段 %s 推导为 %d，应为 %d", k, got, w)
		}
	}
	for _, k := range []string{"-", "Skipped", "unexport", "embedded"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s 不该出现在表里", k)
		}
	}
}

// 只查根级：嵌套层里同名字段的类型不该被误判。
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
