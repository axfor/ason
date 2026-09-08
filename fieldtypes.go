package ason

import (
	"encoding"
	"encoding/json"
	"reflect"
	"strings"
)

// FieldTypesOf derives the SetFieldTypes table from the struct a caller would otherwise have unmarshalled the
// document into, so the streaming check and that unmarshal reject the same documents. Deriving it beats writing
// it out: a hand-kept table drifts from the struct the moment a field is added, and the drift is silent.
//
// v is a struct or a pointer to one. Names come from the json tag when there is one, otherwise from the field,
// exactly as encoding/json takes them; embedded structs without a tag are flattened the same way.
//
// The mapping errs towards accepting. Anything encoding/json does not judge by type alone -- a type with its own
// UnmarshalJSON or UnmarshalText, an interface, a name two fields share -- accepts every type, so the check
// never rejects a document the unmarshal would have taken. What it does not reproduce: encoding/json also
// matches field names case-insensitively, and it rejects a fractional number for an integer field. Both make
// this check accept a little more than the unmarshal, never less.
func FieldTypesOf(v any) map[string]FieldTypes {
	rt := reflect.TypeOf(v)
	for rt != nil && rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	if rt == nil || rt.Kind() != reflect.Struct {
		return nil
	}
	m := map[string]FieldTypes{}
	seen := map[string]bool{}
	collectFields(rt, m, seen)
	return m
}

func collectFields(rt reflect.Type, m map[string]FieldTypes, seen map[string]bool) {
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			// Embedded without a name: encoding/json flattens its fields into this level.
			et := f.Type
			for et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				collectFields(et, m, seen)
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		if seen[name] {
			m[name] = TypeAny // two fields share the name: encoding/json has precedence rules, do not guess
			continue
		}
		seen[name] = true
		m[name] = jsonTypesOf(f.Type)
	}
}

// jsonTypesOf reports which JSON types encoding/json accepts for a Go type. Null is accepted for every type and
// carries no bit.
func jsonTypesOf(rt reflect.Type) FieldTypes {
	// A type that decodes itself decides what it takes; so does anything reached through an interface.
	if implementsUnmarshaler(rt) {
		return TypeAny
	}
	for rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
		if implementsUnmarshaler(rt) {
			return TypeAny
		}
	}
	switch rt.Kind() {
	case reflect.String:
		return TypeString
	case reflect.Bool:
		return TypeBool
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return TypeNumber
	case reflect.Slice:
		if rt.Elem().Kind() == reflect.Uint8 {
			return TypeArray | TypeString // []byte also takes a base64 string
		}
		return TypeArray
	case reflect.Array:
		return TypeArray
	case reflect.Map, reflect.Struct:
		return TypeObject
	}
	return TypeAny // interfaces and anything else: do not judge by type
}

var (
	jsonUnmarshaler = reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()
	textUnmarshaler = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()
)

func implementsUnmarshaler(rt reflect.Type) bool {
	if rt.Kind() == reflect.Interface {
		return true
	}
	pt := reflect.PointerTo(rt)
	return rt.Implements(jsonUnmarshaler) || pt.Implements(jsonUnmarshaler) ||
		rt.Implements(textUnmarshaler) || pt.Implements(textUnmarshaler)
}
