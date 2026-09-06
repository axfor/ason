package llm

// The engine comes from github.com/axfor/ason (protocol-agnostic streaming JSON transformation: scanner, actions, lazy writer, strict validation).
// Its API is exposed in this package through aliases so the protocol files (proto_*.go, hook_tools.go) do not see the package boundary.

import "github.com/axfor/ason"

type (
	Transformer  = ason.Transformer
	Action       = ason.Action
	ValueKind    = ason.ValueKind
	Protocol     = ason.Protocol
	BaseProtocol = ason.BaseProtocol
	Writer       = ason.Writer
	DeferredKV   = ason.DeferredKV
	// Error / Code: details and classification of a bail (tr.Err()). The integration layer classifies fallbacks by Code, never by message text.
	Error = ason.Error
	Code  = ason.Code
)

const (
	ErrNone          = ason.ErrNone
	ErrSyntax        = ason.ErrSyntax
	ErrIncomplete    = ason.ErrIncomplete
	ErrRoot          = ason.ErrRoot
	ErrTrailing      = ason.ErrTrailing
	ErrDuplicateKey  = ason.ErrDuplicateKey
	ErrLimit         = ason.ErrLimit
	ErrLeftoverDefer = ason.ErrLeftoverDefer
	ErrUnsupported   = ason.ErrUnsupported
	ErrMisuse        = ason.ErrMisuse
)

const (
	KindString = ason.KindString
	KindObject = ason.KindObject
	KindArray  = ason.KindArray
	KindNull   = ason.KindNull
	KindBool   = ason.KindBool
	KindNumber = ason.KindNumber

	// CommitBytes is the commit window: no output is released before this many input bytes have been scanned.
	CommitBytes = ason.CommitBytes
)

var (
	NewTransformer = ason.NewTransformer
	Pass           = ason.Pass
	Skip           = ason.Skip
	Capture        = ason.Capture
	Observe        = ason.Observe
	Defer          = ason.Defer
	Enter          = ason.Enter
	Probe          = ason.Probe
	Prefix         = ason.Prefix
	Bail           = ason.Bail
	BailCode       = ason.BailCode
)

// helpers shared by the protocol implementations
func appendJSONString(dst []byte, s string) []byte { return ason.AppendJSONString(dst, s) }
func jsonUnquote(raw []byte) (string, bool)        { return ason.JSONUnquote(raw) }
func unescapePrefix(b []byte) ([]byte, []int)      { return ason.UnescapePrefix(b) }
func isIntLiteral(s []byte) bool                   { return ason.IsIntLiteral(s) }
func isNumLiteral(s []byte) bool                   { return ason.IsNumLiteral(s) }
func isZeroNum(s []byte) bool                      { return ason.IsZeroNum(s) }
