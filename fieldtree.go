package ason

import "github.com/axfor/ason/internal/fieldtree"

// The field tree and the type map are built by reflection over a caller's struct, which is a self-contained job
// with no knowledge of the scanner -- so it lives in internal/fieldtree. These aliases keep it reachable under the
// names it has always had; nothing about the API changes.

// FieldTree is the shape a caller's struct expects, walked by the scanner to decide what a value may be.
type FieldTree = fieldtree.FieldTree

// FieldKind is what a field's Go type accepts.
type FieldKind = fieldtree.FieldKind

// FieldTreeOf builds a FieldTree from a value by reflection, to the given depth.
func FieldTreeOf(v any, depth int) *FieldTree { return fieldtree.FieldTreeOf(v, depth) }

// FieldTypesOf reports which JSON types each field of v accepts, keyed by JSON name.
func FieldTypesOf(v any) map[string]FieldTypes { return fieldtree.FieldTypesOf(v) }

// FieldTypes is the set of JSON types a field accepts, as a bit set.
type FieldTypes = fieldtree.FieldTypes

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

// The bits of a FieldKind. A type alias does not carry its type's constants, so they are forwarded by name --
// without these, ason.FieldString and the rest stop compiling for callers.
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
