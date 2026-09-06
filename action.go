package ason

// Layer 2 interface: protocol hooks and actions.
//
// A protocol implementation is hand-written code, not a rule table: protocols differ in structure, not in field names.
// The framework only offers the set of actions "what to do with one key / element / value"; every decision belongs to the protocol code.

type actKind uint8

const (
	akPass    actKind = iota // rule 1: key+value written unchanged (may be renamed / wrapped), never buffered
	akSkip                   // rule 2: dropped up to the end of the value, never buffered
	akCapture                // rule 3: the whole value is buffered and handed to OnValue for rewriting (small values only)
	akObserve                // Pass plus a copy for OnValue (small values)
	akDefer                  // key+value held in a bounded buffer and replayed, with the protocol state of that moment, when the protocol calls Release
	akEnter                  // enter the container: child keys / elements keep going to the protocol
	akProbe                  // look at the value kind first: OnStart is called back
	akPrefix                 // strings: collect a prefix window for OnPrefix, then decide
	akBail                   // cannot handle it → fallback
)

// Action is the protocol's decision for one key / element / value.
// Built with a constructor plus chained modifiers; the zero value is meaningless.
type Action struct {
	kind    actKind
	code    Code     // Bail: classification, ErrUnsupported for Bail()
	inner   bool     // Pass: copy only the string content (without the quotes); Bail on a non-string
	flat    bool     // Enter: no matching level on the output side (the protocol decides what to write)
	lenient bool     // Enter: a non-container value is handled as Pass instead of a Bail
	lazy    bool     // Enter: if nothing was written inside by the close, do not materialize (drop); by default materialized as [] / {}
	level   int32    // -1 = the current output level; otherwise write to the given outer level (every level above it must still be unopened)
	cap     int32    // Capture/Observe/Defer/Prefix: byte cap, 0 = unlimited
	key     string   // Pass/Enter: output name; "" keeps the original key
	prefix  []byte   // Pass: written before the value
	suffix  []byte   // Pass: written after the value
	reason  string   // Bail
	via     Protocol // Enter: the callbacks inside this subtree go to it (a sub-hook); OnLeave of the container still goes to whoever issued the Enter
}

func Pass() Action           { return Action{kind: akPass, level: -1} }
func Skip() Action           { return Action{kind: akSkip, level: -1} }
func Capture(cap int) Action { return Action{kind: akCapture, level: -1, cap: clampCap(cap)} }
func Observe(cap int) Action { return Action{kind: akObserve, level: -1, cap: clampCap(cap)} }
func Defer(cap int) Action   { return Action{kind: akDefer, level: -1, cap: clampCap(cap)} }
func Enter() Action          { return Action{kind: akEnter, level: -1} }
func Probe() Action          { return Action{kind: akProbe, level: -1} }
func Prefix(cap int) Action  { return Action{kind: akPrefix, level: -1, cap: clampCap(cap)} }

// clampCap fits a cap into int32 (buffer caps are far below 2GB; larger values are treated as 2GB-1).
func clampCap(n int) int32 {
	if n > 1<<31-1 {
		return 1<<31 - 1
	}
	return int32(n)
}
func Bail(reason string) Action { return BailCode(ErrUnsupported, reason) }

// As renames (Pass / Enter).
func (a Action) As(key string) Action { a.key = key; return a }

// At writes to the given output level (Pass / Enter). Level numbers come from Writer.Level().
func (a Action) At(level int) Action { a.level = int32(level); return a }

// Inner writes only the string content, without quotes (Pass).
func (a Action) Inner() Action { a.inner = true; return a }

// Wrap adds bytes around the value (Pass). prefix/suffix should be static bytes that are not modified during the callback.
func (a Action) Wrap(prefix, suffix []byte) Action { a.prefix = prefix; a.suffix = suffix; return a }

// Flat: no level is created on the output side when entering (Enter).
func (a Action) Flat() Action { a.flat = true; return a }

// Lazy: if nothing was written by the time the entered container closes, not a single byte is written (Enter).
// For elements that may be dropped as a whole; the default materializes an empty container, matching the input.
func (a Action) Lazy() Action { a.lazy = true; return a }

// Via hands every callback inside this container (OnKey / OnElem / OnStart / OnValue / OnPrefix / OnLeave and Defer replays)
// to a sub-hook, which also owns the deeper levels it enters itself. OnLeave of the container itself goes back to whoever
// issued the Enter so it can finish up (Pop an output level it built, say). Paths and depths stay absolute.
func (a Action) Via(h Protocol) Action { a.via = h; return a }

// Lenient: Enter degrades to Pass on a non-container value instead of a Bail (Enter).
func (a Action) Lenient() Action { a.lenient = true; return a }

// IsCapture reports whether this is a Capture action (used by protocols that wrap another protocol's decision).
func (a Action) IsCapture() bool { return a.kind == akCapture }

// ValueKind is the kind of value reported to the protocol by the Probe callback.
type ValueKind uint8

const (
	KindString ValueKind = iota
	KindObject
	KindArray
	KindNull   // the literal null
	KindBool   // true / false
	KindNumber // a number
)

// IsScalar reports whether the kind is a scalar (null / bool / number; strings are KindString on their own).
func (k ValueKind) IsScalar() bool { return k >= KindNull }

// IsContainer reports whether the kind is an object or an array.
func (k ValueKind) IsContainer() bool { return k == KindObject || k == KindArray }

func (k ValueKind) String() string {
	switch k {
	case KindString:
		return "string"
	case KindObject:
		return "object"
	case KindArray:
		return "array"
	case KindNull:
		return "null"
	case KindBool:
		return "bool"
	case KindNumber:
		return "number"
	}
	return "?"
}

// Protocol is the complete streaming logic of one target protocol.
//
// Every callback happens synchronously during the scan; raw is only valid during the callback and must be copied to be kept.
// Inside a callback t gives access to the current path, the output (t.W()), replay requests (t.Release())
// and bailing (t.Bail()).
type Protocol interface {
	// OnKey: a key was scanned. t.Path already includes it.
	OnKey(t *Transformer) Action
	// OnElem: the start of an array element was scanned. t.Path already includes the index.
	OnElem(t *Transformer) Action
	// OnStart: after Probe, the first byte of the value arrived; the kind is reported and the final action is requested.
	OnStart(t *Transformer, kind ValueKind) Action
	// OnValue: a Capture / Observe value is complete.
	OnValue(t *Transformer, raw []byte)
	// OnPrefix: the Prefix window is full (complete=false) or the string ended inside the window (complete=true).
	// raw is the raw bytes of the string content (escapes included, quotes excluded).
	// Returns the follow-up action (Pass, Skip or Bail) and resume: raw[resume:] is written right after the prefix.
	OnPrefix(t *Transformer, raw []byte, complete bool) (Action, int)
	// OnLeave: an entered container closed. t.Path still points at the container.
	OnLeave(t *Transformer)
	// Tail: after the root object closed and before the closing } is written; where rule 4 puts "added fields go last".
	Tail(t *Transformer)
}

// BaseProtocol provides empty implementations of every callback (always Pass / no action).
// A protocol or sub-hook embeds it and overrides only the callbacks it cares about; a new protocol starts from "everything passes through".
type BaseProtocol struct{}

func (BaseProtocol) OnKey(*Transformer) Action                         { return Pass() }
func (BaseProtocol) OnElem(*Transformer) Action                        { return Pass() }
func (BaseProtocol) OnStart(*Transformer, ValueKind) Action            { return Pass() }
func (BaseProtocol) OnValue(*Transformer, []byte)                      {}
func (BaseProtocol) OnPrefix(*Transformer, []byte, bool) (Action, int) { return Pass(), 0 }
func (BaseProtocol) OnLeave(*Transformer)                              {}
func (BaseProtocol) Tail(*Transformer)                                 {}
