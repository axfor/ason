package ason

import "reflect"

// FieldTree is the type a document may have at one path, and what its children may have.
//
// It is the recursive form of FieldTypes: the root-level check answers "may this field be an object", and a
// tree answers "and what may the values inside it be". Deriving it is the step that has to come before any
// nested checking, because the risk of nested checking is rejecting documents the unmarshal would accept, and
// that risk can only be measured against a table that is itself derived from the struct rather than written.
//
// Nothing in the engine consumes this yet. It exists so the differential evidence can be built first.
type FieldTree struct {
	Types FieldTypes            // what this value itself may be
	Keys  map[string]*FieldTree // object members by name; nil means "no opinion about members"
	Elem  *FieldTree            // array elements or map values; nil means "no opinion"
	Any   bool                  // this subtree decodes itself: accept everything below here
}

// FieldTreeOf derives the tree from the struct a caller would have unmarshalled into. Depth bounds the
// recursion: a struct that reaches itself would otherwise not terminate.
func FieldTreeOf(v any, depth int) *FieldTree {
	rt := reflect.TypeOf(v)
	for rt != nil && rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	if rt == nil {
		return nil
	}
	return treeOf(rt, depth, map[reflect.Type]bool{})
}

func treeOf(rt reflect.Type, depth int, onPath map[reflect.Type]bool) *FieldTree {
	if implementsUnmarshaler(rt) {
		return &FieldTree{Types: TypeAny, Any: true}
	}
	for rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
		if implementsUnmarshaler(rt) {
			return &FieldTree{Types: TypeAny, Any: true}
		}
	}
	t := &FieldTree{Types: jsonTypesOf(rt)}
	if depth <= 0 || onPath[rt] {
		return t // deep enough, or a cycle: keep the type, say nothing about the children
	}
	onPath[rt] = true
	defer delete(onPath, rt)

	switch rt.Kind() {
	case reflect.Struct:
		t.Keys = map[string]*FieldTree{}
		collectTree(rt, t.Keys, depth, onPath)
	case reflect.Slice, reflect.Array:
		if rt.Elem().Kind() != reflect.Uint8 {
			t.Elem = treeOf(rt.Elem(), depth-1, onPath)
		}
	case reflect.Map:
		t.Elem = treeOf(rt.Elem(), depth-1, onPath)
	}
	return t
}

func collectTree(rt reflect.Type, out map[string]*FieldTree, depth int, onPath map[reflect.Type]bool) {
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := cutComma(tag)
		if f.Anonymous && name == "" {
			et := f.Type
			for et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				collectTree(et, out, depth, onPath)
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		if _, dup := out[name]; dup {
			out[name] = &FieldTree{Types: TypeAny, Any: true} // shared name: do not guess
			continue
		}
		out[name] = treeOf(f.Type, depth-1, onPath)
	}
}

func cutComma(s string) (string, string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}
