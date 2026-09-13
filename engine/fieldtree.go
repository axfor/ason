package engine

import "github.com/axfor/ason/fieldtree"

// The field tree is built by reflection over a caller's struct and knows nothing about the scanner, so it lives in
// its own package. These aliases let the engine spell the types without a qualifier, which is what the code has
// always done; ason re-publishes them again from here, and all three names are the same type.

// FieldTree is the shape a caller's struct expects, walked by the scanner to decide what a value may be.
type FieldTree = fieldtree.FieldTree

// FieldKind is what a field's Go type accepts.
type FieldKind = fieldtree.FieldKind

// FieldTypes is the set of JSON types a field accepts, as a bit set.
type FieldTypes = fieldtree.FieldTypes

// The bits of a FieldKind. An alias does not carry its type's constants, so they are forwarded by name.
const (
	FieldOther     = fieldtree.FieldOther
	FieldString    = fieldtree.FieldString
	FieldNumber    = fieldtree.FieldNumber
	FieldBool      = fieldtree.FieldBool
	FieldStruct    = fieldtree.FieldStruct
	FieldPointer   = fieldtree.FieldPointer
	FieldSlice     = fieldtree.FieldSlice
	FieldMap       = fieldtree.FieldMap
	FieldInterface = fieldtree.FieldInterface
)

// The bits of a FieldTypes. TypeAny accepts every type; use it for fields whose Go type is interface{} or
// json.RawMessage.
const (
	TypeString = fieldtree.TypeString
	TypeNumber = fieldtree.TypeNumber
	TypeBool   = fieldtree.TypeBool
	TypeObject = fieldtree.TypeObject
	TypeArray  = fieldtree.TypeArray
	TypeAny    = fieldtree.TypeAny
)

// FieldTreeOf builds a FieldTree from a value by reflection, to the given depth.
func FieldTreeOf(v any, depth int) *FieldTree { return fieldtree.FieldTreeOf(v, depth) }

// FieldTypesOf reports which JSON types each field of v accepts, keyed by JSON name.
func FieldTypesOf(v any) map[string]FieldTypes { return fieldtree.FieldTypesOf(v) }
