// Package ason is a streaming cross-protocol JSON transformation framework:
//
//	layer 1 Scanner  - the protocol-agnostic byte-level scanner, which turns the input into key / value / container events
//	layer 2 Protocol - one set of hand-written hooks per protocol, returning an action for every event
//	layer 3 Guard    - commit point / fallback window
//
// The scanner builds no object tree. A value either streams to the output unchanged (Pass), is dropped (Skip),
// or enters a bounded buffer only when the protocol asks for it (Capture / Defer / Prefix). Memory is independent
// of the input size; it depends only on the few small values the protocol buffers.
//
// # Where the code is
//
// This file is the package's entire surface. The engine is in the engine subpackage, the reflection that builds a
// FieldTree is in fieldtree, and the vector scan is in simd; everything they export is re-published here under the
// names it has always had. The types are aliases, not wrappers, so a *Transformer from here and one from engine
// are the same type and the methods come across untouched.
//
// Constants are the exception -- a type alias does not carry them -- so each is forwarded by name below. That is
// not pedantry: getting it wrong once already made ason.FieldString stop compiling for callers.
package ason

import "github.com/axfor/ason/engine"

// Types. Aliases, so values cross the boundary unchanged and every method comes with them.
type (
	Action          = engine.Action
	BaseProtocol    = engine.BaseProtocol
	Code            = engine.Code
	DeferredKV      = engine.DeferredKV
	DupKeys         = engine.DupKeys
	Error           = engine.Error
	FieldKind       = engine.FieldKind
	FieldTree       = engine.FieldTree
	FieldTypes      = engine.FieldTypes
	KeyCache        = engine.KeyCache
	KeyProbe        = engine.KeyProbe
	KeyProbeOptions = engine.KeyProbeOptions
	Protocol        = engine.Protocol
	RootKind        = engine.RootKind
	Transformer     = engine.Transformer
	ValueKind       = engine.ValueKind
	Writer          = engine.Writer
)

// Constants, forwarded one by one because an alias does not bring them along.
const (
	CommitBytes      = engine.CommitBytes
	DupKeysBail      = engine.DupKeysBail
	DupKeysFirst     = engine.DupKeysFirst
	DupKeysPass      = engine.DupKeysPass
	ErrDuplicateKey  = engine.ErrDuplicateKey
	ErrIncomplete    = engine.ErrIncomplete
	ErrLeftoverDefer = engine.ErrLeftoverDefer
	ErrLimit         = engine.ErrLimit
	ErrMisuse        = engine.ErrMisuse
	ErrNone          = engine.ErrNone
	ErrRoot          = engine.ErrRoot
	ErrSyntax        = engine.ErrSyntax
	ErrTrailing      = engine.ErrTrailing
	ErrUnsupported   = engine.ErrUnsupported
	FieldBool        = engine.FieldBool
	FieldInterface   = engine.FieldInterface
	FieldMap         = engine.FieldMap
	FieldNumber      = engine.FieldNumber
	FieldOther       = engine.FieldOther
	FieldPointer     = engine.FieldPointer
	FieldSlice       = engine.FieldSlice
	FieldString      = engine.FieldString
	FieldStruct      = engine.FieldStruct
	KindArray        = engine.KindArray
	KindBool         = engine.KindBool
	KindNull         = engine.KindNull
	KindNumber       = engine.KindNumber
	KindObject       = engine.KindObject
	KindString       = engine.KindString
	RootAny          = engine.RootAny
	RootArray        = engine.RootArray
	RootObject       = engine.RootObject
	TypeAny          = engine.TypeAny
	TypeArray        = engine.TypeArray
	TypeBool         = engine.TypeBool
	TypeNumber       = engine.TypeNumber
	TypeObject       = engine.TypeObject
	TypeString       = engine.TypeString
)

// AppendJSONString encodes a string by the rules of encoding/json (including the HTML-safe escapes of < > &).
// Byte-identical to the Marshal output of encoding/json.
func AppendJSONString(dst []byte, s string) []byte { return engine.AppendJSONString(dst, s) }

func Bail(reason string) Action { return engine.Bail(reason) }

// BailCode is Bail with a classification.
func BailCode(code Code, reason string) Action { return engine.BailCode(code, reason) }

func Capture(cap int) Action { return engine.Capture(cap) }

func Defer(cap int) Action { return engine.Defer(cap) }

func Enter() Action { return engine.Enter() }

// FieldTreeOf builds a FieldTree from a value by reflection, to the given depth.
func FieldTreeOf(v any, depth int) *FieldTree { return engine.FieldTreeOf(v, depth) }

// FieldTypesOf reports which JSON types each field of v accepts, keyed by JSON name.
func FieldTypesOf(v any) map[string]FieldTypes { return engine.FieldTypesOf(v) }

// IsIntLiteral reports whether the literal is one encoding/json can decode into an int.
func IsIntLiteral(s []byte) bool { return engine.IsIntLiteral(s) }

func IsNumLiteral(s []byte) bool { return engine.IsNumLiteral(s) }

// IsZeroNum reports whether a number literal is zero (0 / 0.0 / 0e0 ...), reproducing omitempty semantics.
func IsZeroNum(s []byte) bool { return engine.IsZeroNum(s) }

// JSONUnquote decodes a JSON string literal (quotes included).
// Invalid input returns ok=false.
func JSONUnquote(raw []byte) (string, bool) { return engine.JSONUnquote(raw) }

// NewKeyCache returns an empty cache to share with SetKeyCache.
func NewKeyCache() *KeyCache { return engine.NewKeyCache() }

// NewKeyProbe builds the probe protocol (for NewTransformer, or use NewKeyProbeTransformer).
func NewKeyProbe(opt KeyProbeOptions) *KeyProbe { return engine.NewKeyProbe(opt) }

// NewKeyProbeTransformer is shorthand for NewTransformer(NewKeyProbe(opt)).
func NewKeyProbeTransformer(opt KeyProbeOptions) *Transformer {
	return engine.NewKeyProbeTransformer(opt)
}

// NewTransformer builds a transformer with the given protocol.
func NewTransformer(p Protocol) *Transformer { return engine.NewTransformer(p) }

func Observe(cap int) Action { return engine.Observe(cap) }

func Pass() Action { return engine.Pass() }

func Prefix(cap int) Action { return engine.Prefix(cap) }

func Probe() Action { return engine.Probe() }

func Skip() Action { return engine.Skip() }

// UnescapePrefix decodes a string prefix while recording the raw offset of every decoded byte,
// for "decide on the decoded bytes, resume from the raw bytes" situations (a data: URL header, say).
// An incomplete escape sequence at the end is not decoded; rawOff carries one extra sentinel pointing at the raw offset where
// decoding stopped, so the raw bytes from rawOff[len(dec)] on can be forwarded unchanged.
func UnescapePrefix(b []byte) (dec []byte, rawOff []int) { return engine.UnescapePrefix(b) }
