package fieldtree

import (
	"encoding/json"
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
