package engine

import "encoding/json"

// Fixtures the field-tree benchmarks build a tree from. Duplicated rather than shared with the ason package's own
// tests: a fixture declared in a _test.go file cannot be exported across a package boundary, so each side keeps
// its own copy of the shapes it exercises.

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

type customScalar struct{ n int }

type embedded struct {
	Store    bool `json:"store,omitempty"`
	Untagged string
}

type inner struct {
	A int `json:"a"`
}
