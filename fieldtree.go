package ason

import (
	"encoding"
	"encoding/json"
	"reflect"
	"sort"
)

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

	// The marshal side, for a caller that reproduces the round trip a buffered path makes (json.Unmarshal into
	// the struct, json.Marshal back out): what the field's Go value is, and whether the tag says omitempty.
	Kind FieldKind
	Omit bool
	Int  bool // a number field of an integer type: the decode rejects a fraction or an exponent

	// rootTypes is Keys' own types as a flat table, filled in by FieldTreeOf for the root and shared, read-only, by
	// every transformer the tree is set on: SetFieldTree used to build the same map for each one (1.8KB a stream
	// for a chat request, as long as the stream lives).
	rootTypes map[string]FieldTypes
}

// FieldKind is the class of Go value behind a field, as far as the round trip cares: what a JSON null decodes
// to, and what omitempty leaves out.
type FieldKind uint8

const (
	FieldOther     FieldKind = iota // a type that marshals itself, or one the tree does not classify: not reproducible
	FieldString                     // "" is the zero value
	FieldNumber                     // 0
	FieldBool                       // false
	FieldStruct                     // never omitted, null decodes to the zero struct
	FieldPointer                    // nil on null; omitted when nil
	FieldSlice                      // nil on null, omitted when nil or empty
	FieldMap                        // nil on null, omitted when nil or empty
	FieldInterface                  // nil on null only; omitted when nil
)

// ZeroJSON is what json.Marshal writes for the zero value of this field: what a field that is absent from the
// document, or null, comes out as when it is not omitted. ok is false for a kind that marshals itself.
func (t *FieldTree) ZeroJSON() (zero string, ok bool) {
	switch t.Kind {
	case FieldString:
		return `""`, true
	case FieldNumber:
		return "0", true
	case FieldBool:
		return "false", true
	case FieldPointer, FieldSlice, FieldMap, FieldInterface:
		return "null", true
	case FieldStruct:
		if t.Keys == nil {
			return "", false // deeper than the tree goes
		}
		b := []byte("{")
		first := true
		for _, k := range t.SortedKeys() {
			c := t.Keys[k]
			if c.Omit {
				continue
			}
			z, ok := c.ZeroJSON()
			if !ok {
				return "", false
			}
			if !first {
				b = append(b, ',')
			}
			first = false
			b = append(b, '"')
			b = append(b, k...)
			b = append(b, '"', ':')
			b = append(b, z...)
		}
		return string(append(b, '}')), true
	}
	return "", false
}

// SortedKeys returns the member names in a fixed order (the map has none).
func (t *FieldTree) SortedKeys() []string {
	keys := make([]string, 0, len(t.Keys))
	for k := range t.Keys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
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
	t := treeOf(rt, depth, map[reflect.Type]bool{})
	t.rootTypes = keyTypes(t)
	return t
}

// keyTypes is the flat table of the members' own types, or nil when the tree says nothing about members.
func keyTypes(tr *FieldTree) map[string]FieldTypes {
	if tr.Keys == nil {
		return nil
	}
	m := make(map[string]FieldTypes, len(tr.Keys))
	for k, sub := range tr.Keys {
		if sub != nil {
			m[k] = sub.Types
		}
	}
	return m
}

func treeOf(rt reflect.Type, depth int, onPath map[reflect.Type]bool) *FieldTree {
	kind := kindOf(rt)
	if implementsUnmarshaler(rt) {
		return &FieldTree{Types: TypeAny, Any: true, Kind: kind}
	}
	for rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
		if implementsUnmarshaler(rt) {
			return &FieldTree{Types: TypeAny, Any: true, Kind: kind}
		}
	}
	t := &FieldTree{Types: jsonTypesOf(rt), Kind: kind, Int: isIntKind(rt)}
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
		child := treeOf(f.Type, depth-1, onPath)
		child.Omit = hasOmitEmpty(tag)
		out[name] = child
	}
}

// kindOf classifies a field's Go type for the marshal side. A type that marshals itself is FieldOther whatever
// it is made of: the tree cannot say what it writes.
func kindOf(rt reflect.Type) FieldKind {
	if rt.Implements(jsonMarshaler) || reflect.PointerTo(rt).Implements(jsonMarshaler) ||
		rt.Implements(textMarshaler) || reflect.PointerTo(rt).Implements(textMarshaler) {
		return FieldOther
	}
	switch rt.Kind() {
	case reflect.Pointer:
		return FieldPointer
	case reflect.Interface:
		return FieldInterface
	case reflect.String:
		return FieldString
	case reflect.Bool:
		return FieldBool
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return FieldNumber
	case reflect.Slice:
		return FieldSlice
	case reflect.Array:
		return FieldOther // a fixed array is neither omitted nor nil: not classified
	case reflect.Map:
		return FieldMap
	case reflect.Struct:
		return FieldStruct
	}
	return FieldOther
}

func isIntKind(rt reflect.Type) bool {
	switch rt.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	}
	return false
}

func hasOmitEmpty(tag string) bool {
	_, opts, _ := cutComma(tag)
	for opts != "" {
		var o string
		o, opts, _ = cutComma(opts)
		if o == "omitempty" {
			return true
		}
	}
	return false
}

func cutComma(s string) (string, string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

var (
	jsonMarshaler = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	textMarshaler = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
)
