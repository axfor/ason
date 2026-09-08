package ason

import (
	"encoding/json"
	"testing"
)

type ftInner struct {
	Index int    `json:"index"`
	Name  string `json:"name,omitempty"`
}

type ftCustom struct{ V string }

func (c ftCustom) MarshalJSON() ([]byte, error) { return []byte(`"custom"`), nil }

type ftRoot struct {
	Model    string                 `json:"model"`
	Temp     float64                `json:"temperature,omitempty"`
	Stream   bool                   `json:"stream,omitempty"`
	Content  any                    `json:"content,omitempty"`
	Opts     *ftInner               `json:"opts,omitempty"`
	Inner    ftInner                `json:"inner,omitempty"`
	Calls    []ftInner              `json:"calls,omitempty"`
	Meta     map[string]string      `json:"meta,omitempty"`
	Format   map[string]interface{} `json:"format,omitempty"`
	Custom   ftCustom               `json:"custom"`
	Ignored  string                 `json:"-"`
	unexport string
}

// The marshal-side facts: the kind of every field and its omitempty, and the zero value json.Marshal writes.
func TestFieldTreeMarshalSide(t *testing.T) {
	tree := FieldTreeOf(&ftRoot{}, 4)
	want := map[string]struct {
		kind FieldKind
		omit bool
	}{
		"model": {FieldString, false}, "temperature": {FieldNumber, true}, "stream": {FieldBool, true},
		"content": {FieldInterface, true}, "opts": {FieldPointer, true}, "inner": {FieldStruct, true},
		"calls": {FieldSlice, true}, "meta": {FieldMap, true}, "format": {FieldMap, true}, "custom": {FieldOther, false},
	}
	if len(tree.Keys) != len(want) {
		t.Fatalf("keys: %v", tree.SortedKeys())
	}
	for k, w := range want {
		c := tree.Keys[k]
		if c == nil || c.Kind != w.kind || c.Omit != w.omit {
			t.Fatalf("%s: got %+v want %+v", k, c, w)
		}
	}
	if !tree.Keys["inner"].Keys["index"].Int || tree.Keys["temperature"].Int {
		t.Fatal("integer-ness of number fields")
	}
	// the zero struct as json.Marshal writes it, from the tree and from the real thing
	z, ok := tree.Keys["inner"].ZeroJSON()
	real, _ := json.Marshal(ftInner{})
	if !ok || z != string(real) {
		t.Fatalf("zero inner: %q want %q", z, real)
	}
	if _, ok := tree.Keys["custom"].ZeroJSON(); ok {
		t.Fatal("a self-marshalling type has no zero the tree can write")
	}
	if z, _ := tree.Keys["calls"].ZeroJSON(); z != "null" {
		t.Fatalf("zero slice: %q", z)
	}
	if z, _ := tree.Keys["opts"].ZeroJSON(); z != "null" {
		t.Fatalf("zero pointer: %q", z)
	}
	// what omitempty leaves out of the real marshal, for reference
	b, _ := json.Marshal(ftRoot{Content: "", Calls: []ftInner{}, Meta: map[string]string{}})
	if string(b) != `{"model":"","content":"","inner":{"index":0},"custom":"custom"}` {
		t.Fatalf("reference marshal: %s", b)
	}
}
