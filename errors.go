package ason

import "strconv"

// Code classifies "why it stopped" so callers can choose between fallback, failure and reporting a protocol defect without matching message text.
type Code uint8

const (
	ErrNone          Code = iota
	ErrSyntax             // not JSON grammar: literals, numbers, escapes, control characters, structure, UTF-8 (when validation is on)
	ErrIncomplete         // the input had not reached the root close at Finish
	ErrRoot               // the root is not a shape SetRoot allows
	ErrTrailing           // non-whitespace content after the root
	ErrDuplicateKey       // duplicate key inside a dispatch frame (DupKeysBail / DupKeyBail)
	ErrLimit              // a Capture / Observe / Defer / Prefix cap or the total budget was exceeded
	ErrLeftoverDefer      // Defer items neither replayed nor dropped when the container closed
	ErrUnsupported        // the protocol cannot express this shape: its own Bail, or Enter / Inner / Prefix meeting the wrong value kind
	ErrMisuse             // the protocol misused the API: nested Probe, Defer on an array element, OnPrefix returning an invalid action / offset, etc.
)

var codeNames = [...]string{"none", "syntax", "incomplete", "root", "trailing", "duplicate_key", "limit", "leftover_defer", "unsupported", "misuse"}

func (c Code) String() string {
	if int(c) < len(codeNames) {
		return codeNames[c]
	}
	return "code(" + strconv.Itoa(int(c)) + ")"
}

// Error is the full description of a bail. Msg is English; a protocol's own Bail text is kept as is.
type Error struct {
	Code   Code
	Msg    string
	Offset int64  // input bytes consumed at the time of the bail: grammar errors point at the failing byte, a bail inside a callback at the current scan position, one in Finish at the total length
	Path   string // path at that point, e.g. messages[2].content; empty at the root
}

func (e *Error) Error() string {
	s := e.Msg + " at byte " + strconv.FormatInt(e.Offset, 10)
	if e.Path != "" {
		s += " in " + e.Path
	}
	return s
}

// Err returns the details of the bail; nil when everything is fine.
func (t *Transformer) Err() *Error { return t.err }

// BailCode is Bail with a classification.
func BailCode(code Code, reason string) Action {
	return Action{kind: akBail, level: -1, reason: reason, code: code}
}

// BailErr is called by protocols or the framework: bail and stop scanning. The first reason is kept, later calls are ignored.
func (t *Transformer) BailErr(code Code, reason string) {
	if !t.unsupported {
		t.unsupported = true
		t.err = &Error{Code: code, Msg: reason, Offset: -1, Path: t.PathString()}
	}
	t.dead = true
}

// Bail is called by protocols: bail (ErrUnsupported) and stop scanning.
func (t *Transformer) Bail(reason string) { t.BailErr(ErrUnsupported, reason) }

// fixOffset pins an error that has no offset yet to off.
func (t *Transformer) fixOffset(off int64) {
	if t.err != nil && t.err.Offset < 0 {
		t.err.Offset = off
	}
}
