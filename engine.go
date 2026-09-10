// Package ason is a streaming cross-protocol JSON transformation framework:
//
//	layer 1 Scanner  - the protocol-agnostic byte-level scanner (this file), which turns the input into key / value / container events
//	layer 2 Protocol - one set of hand-written hooks per protocol (proto_*.go), returning an action for every event
//	layer 3 Guard    - commit point / fallback window (the committed / Bail semantics of this file plus the integration layer)
//
// The scanner builds no object tree. A value either streams to the output unchanged (Pass), is dropped (Skip),
// or enters a bounded buffer only when the protocol asks for it (Capture / Defer / Prefix).
// Memory is independent of the input size; it depends only on the few small values the protocol buffers.
package ason

import (
	"encoding/json"
	"strconv"
)

type scanState uint8

const (
	sIdle scanState = iota
	sInKey
	sInStr
	sInScalar
)

type frameKind uint8

const (
	fkObj frameKind = iota
	fkArr
)

type phase uint8

const (
	phKey   phase = iota // object: expecting a key (or })
	phColon              // object: expecting :
	phValue              // expecting a value (array: or ])
	phComma              // expecting , or a closing bracket
)

// Grammar phases inside a region: whether the current container is an object or an array is encoded in the phase, so the validity of a structural character and the next phase are one table lookup.
type regPhase uint8

const (
	rErr     regPhase = iota // lookup result: invalid
	rTop                     // region top level, expecting a value (start of the region)
	rKey0                    // object just opened: expecting a key or }
	rKey                     // object after a comma: expecting a key
	rColon                   // after a key: expecting :
	rOValue                  // after a colon: expecting a value
	rOComma                  // after a value in an object: expecting , or }
	rAValue0                 // array just opened: expecting a value or ]
	rAValue                  // array after a comma: expecting a value
	rAComma                  // after a value in an array: expecting , or ]
)

var (
	// phase after the start of a string (key or value).
	regAfterStr = [...]regPhase{rKey0: rColon, rKey: rColon, rOValue: rOComma, rAValue0: rAComma, rAValue: rAComma, rAComma: rErr}
	// after the start of a scalar / container (containers are then overridden by regPush).
	regAfterVal = [...]regPhase{rOValue: rOComma, rAValue0: rAComma, rAValue: rAComma, rAComma: rErr}
	// after a comma.
	regAfterComma = [...]regPhase{rOComma: rKey, rAComma: rAValue}
	// can the container close: 0 = no (missing key / value, trailing comma), 1 = object may close, 2 = array may close.
	regClose = [...]uint8{rKey0: 1, rOComma: 1, rAValue0: 2, rAComma: 2}
)

// frame is a container entered with Enter (a dispatch frame). Containers inside regions get no frame, only a depth count.
type frame struct {
	kind     frameKind
	ph       phase
	idx      int
	n        int  // number of completed values (object)
	flat     bool // no matching level on the output side
	lazy     bool // do not materialize an empty container on close
	seen     []string
	deferred []DeferredKV
	hook     Protocol // receiver of the callbacks inside this frame; nil = the main protocol
}

// DeferredKV is a raw key/value pair held by Defer.
type DeferredKV struct {
	Key    string
	KeyRaw []byte // [ws]"key"[ws]:[ws]
	Raw    []byte
}

type seg struct {
	k string
	i int // -1 = key segment
}

type regionTarget uint8

const (
	rtNone regionTarget = iota
	rtOut
	rtSkip
	rtCapture
	rtValidate // like rtOut for the output, but the bytes are also kept (bounded) and type-checked at the close
	rtDefer
	rtObserve
	rtPrefix
)

// keyCacheSize is the number of slots of the key intern cache: direct-mapped, a fixed 4KB, independent of how many distinct keys the document has;
// repeated keys (the vast majority of dispatches in normal documents) no longer allocate, and adversarial input with a flood of distinct keys cannot make it grow.
const keyCacheSize = 256

// KeyCache interns the keys a transformer meets, so a key seen before costs no allocation. One is created for
// each transformer by default, which means every request pays once for every distinct key it has -- and the
// keys of one request are the keys of the next. SetKeyCache shares one across transformers instead; after the
// first document on it, dispatching a key allocates nothing.
//
// A cache belongs to one goroutine at a time: transformers on one Envoy worker interleave but never run
// concurrently, which is the case it is for. Sharing one across goroutines is a data race.
type KeyCache [keyCacheSize]string

// NewKeyCache returns an empty cache to share with SetKeyCache.
func NewKeyCache() *KeyCache { return new(KeyCache) }

// CommitBytes is the commit window: no output is released before this many input bytes have been scanned.
// A bail before that point leaves the caller holding every raw byte, so it can fall back cleanly.
const CommitBytes = 64 << 10

// Transformer connects a Protocol to the scanner. Write feeds chunks, Out takes the releasable bytes.
type Transformer struct {
	proto Protocol
	w     Writer

	// DupKeyBail: bail on a duplicate key inside a dispatch frame.
	// Enable it when the target protocol decodes into structs (last wins); byte-passthrough protocols do not need it.
	DupKeyBail bool

	st     scanState
	esc    bool
	hexN   uint8 // hex digits still to read of a \u escape
	lit    litState
	depth  int
	frames []frame
	path   []seg
	keyBuf []byte
	keyEsc bool

	pend    Action
	pendSet bool

	// Whitespace around keys in dispatch frames is kept verbatim: kvRaw = [ws]"key"[ws]:[ws], elemWs = whitespace before an element.
	// Passthrough protocols rely on it for "untouched bytes stay identical", matching sjson's in-place rewrite.
	kvRaw       []byte
	wsRaw       []byte
	elemWs      []byte
	rootCloseWs []byte
	tailWs      []byte    // whitespace after the root object (a trailing newline, say): written verbatim in Finish
	keys        *KeyCache // key intern cache (direct-mapped, slot chosen by a hash of the key bytes); shared with SetKeyCache

	validateUTF8 bool
	u8           utf8State
	dup          DupKeys
	root         RootKind

	commit        int    // commit window; 0 = CommitBytes
	budget        int    // cap on the sum of all buffers; 0 = unlimited
	deferredBytes int    // bytes currently held by Defer items across all dispatch frames
	leadWs        []byte // whitespace before the root object: written verbatim when the root opens

	regOpen  bool
	regT     regionTarget
	regDepth int
	regInner bool
	regSuf   []byte
	regCap   int
	regKey   string
	capBuf   []byte
	// Grammar state inside regions: updated only on structural characters, data bytes never touch it. Like dispatch frames it rejects
	// mismatched bracket kinds, missing keys / colons / values and trailing commas: JSON inside a region must be acceptable to encoding/json too.
	regPh    regPhase
	regN     int    // number of container levels inside the region
	regKinds uint64 // bit-stack of container kinds for the first 64 levels (1 = array), level n at bit n
	regDeep  []bool // overflow stack beyond 64 levels (rare)

	wantRelease bool
	releaseAt   int // dispatch frame index where the replay was requested: consumed only at that frame's safe point, never by a child frame

	scanned     int
	committed   bool
	unsupported bool
	err         *Error
	sink        func([]byte)
	chunkLen    int   // length of the chunk handed to the current Write (for Unchanged)
	limitAt     int   // on a cap / budget overflow: how many bytes of this append still fit (locates the first byte that does not)
	scanBase    int64 // offset of the current Write chunk within the whole input
	replaying   int   // > 0: replaying Defer items (nested scan), error offsets take the outer position
	dead        bool
	rootSeen    bool
	rootDone    bool
	suspendReq  bool // a callback asked for a suspension: the scan stops at the next byte boundary
	suspended   bool // stopped by Suspend; held is the unconsumed rest of that Write's chunk, Resume scans it
	finishing   bool // inside Finish (Tail): a Suspend here is a misuse
	held        []byte
	lastChunk   int                   // length of the chunk the last Write scanned; drain's buffer-shrink heuristic wants it
	fieldTypes  map[string]FieldTypes // root-level field -> the JSON types it may have; nil disables the check
	fieldTree   *FieldTree            // recursive form; nil disables nested checking
	valSub      *FieldTree            // subtree for the region currently being validated
	valOver     bool                  // that region outgrew the validation buffer: accept it rather than judge it
	valEmit     bool                  // the validated region's bytes still go to the output (Pass) or are dropped (Skip)
}

// NewTransformer builds a transformer with the given protocol.
func NewTransformer(p Protocol) *Transformer {
	return &Transformer{proto: p}
}

// ---- public: Guard semantics ----

// DupKeys is the policy for duplicate keys inside a dispatch frame.
type DupKeys uint8

const (
	DupKeysPass  DupKeys = iota // default: no check, duplicates are dispatched as usual (passthrough semantics)
	DupKeysBail                 // bail (when the target has struct semantics, last wins, and streaming cannot reproduce that)
	DupKeysFirst                // dispatch only the first, later occurrences of the same key are Skipped automatically (gjson first-wins semantics)
)

// SetDupKeys sets the duplicate key policy (the DupKeyBail field is equivalent to DupKeysBail and kept for compatibility).
func (t *Transformer) SetDupKeys(d DupKeys) { t.dup = d }

// RootKind is the set of allowed root shapes.
type RootKind uint8

const (
	RootObject RootKind = iota // default
	RootArray
	RootAny // object or array
)

// SetRoot sets the allowed root shape. With an array root, depth 1 is an index and dispatches through OnElem.
func (t *Transformer) SetRoot(k RootKind) { t.root = k }

// FieldTypes is the set of JSON types a root-level field may have. Zero accepts nothing but null.
type FieldTypes uint8

const (
	TypeString FieldTypes = 1 << iota
	TypeNumber
	TypeBool
	TypeObject
	TypeArray
	// TypeAny accepts every type; use it for fields whose Go type is interface{} or json.RawMessage.
	TypeAny = TypeString | TypeNumber | TypeBool | TypeObject | TypeArray
)

// typeBit maps a scanned value kind to its FieldTypes bit. Null has none: it is always accepted.
func typeBit(k ValueKind) FieldTypes {
	switch k {
	case KindString:
		return TypeString
	case KindNumber:
		return TypeNumber
	case KindBool:
		return TypeBool
	case KindObject:
		return TypeObject
	case KindArray:
		return TypeArray
	}
	return 0
}

// SetFieldTypes rejects a root-level field whose value has a type the caller says it cannot have, so a caller
// that would otherwise have decoded the document into a fixed shape gets the same rejection while streaming.
//
// Only root-level fields are checked, and only those present in the map: anything else passes through. Null is
// accepted for every field, and a field whose entry has no bits set accepts only null -- both match what
// encoding/json does when unmarshalling into a struct. The check runs on the type of the value, not its
// contents, so a fractional number still reaches an integer field.
//
// A mismatch bails with ErrUnsupported, which before the commit point means the caller can still fall back.
func (t *Transformer) SetFieldTypes(m map[string]FieldTypes) { t.fieldTypes = m }

// SetFieldTree extends SetFieldTypes inside containers: a field whose own type is right can still hold a value
// of the wrong type, which the unmarshal rejects and a root-level check cannot see.
//
// Only the containers the tree has an opinion about are checked, and only up to valCapBytes of content each;
// past that the value is accepted rather than judged, so a large field never becomes a rejection. The bytes
// still pass through unchanged -- the check reads a copy, it does not rewrite anything. Containers the tree says
// nothing about keep the region fast path untouched.
//
// A mismatch bails with ErrUnsupported, which before the commit point still leaves the caller its fallback.
func (t *Transformer) SetFieldTree(tr *FieldTree) {
	t.fieldTree = tr
	if tr != nil && tr.Keys != nil && t.fieldTypes == nil {
		// The tree already carries the root fields' own types, so a caller that has a tree should not have to
		// hand over the flat table as well.
		m := make(map[string]FieldTypes, len(tr.Keys))
		for k, sub := range tr.Keys {
			if sub != nil {
				m[k] = sub.Types
			}
		}
		t.fieldTypes = m
	}
}

// valCapBytes bounds what one validated container may hold. The fields worth checking this way are small by
// nature -- metadata, logit_bias, response_format and the like -- so the bound is generous and rarely reached.
const valCapBytes = 64 << 10

// SetKeyCache shares a key intern cache with other transformers, so the keys one document taught it cost the
// next document nothing. Must be called before the first Write. See KeyCache for the ownership rule.
func (t *Transformer) SetKeyCache(c *KeyCache) { t.keys = c }

// SetValidateUTF8 enables UTF-8 validation of strings and keys (RFC 3629: overlong encodings, surrogates, code points above
// U+10FFFF, stray or missing continuation bytes are rejected, sequences split across chunks included). Off by default: encoding/json does not reject invalid UTF-8 either, it replaces it.
func (t *Transformer) SetValidateUTF8(on bool) { t.validateUTF8 = on }

// CommitNow releases output from here on, before the commit window has filled.
//
// The window exists to keep a retreat open: until it fills, nothing has been released and a caller that meets
// something it cannot handle can still fall back. A caller that knows it no longer needs that retreat -- it
// has seen every field its headers depend on, say, and would fail rather than fall back on anything found
// later -- can commit early, so an in-flight stream holds bytes only for as long as it has to. The window
// then acts as a ceiling for documents where that moment never comes.
//
// Output the transformer had accumulated is released as if the window had just filled: through the sink when
// one is set, otherwise from the next Out. A transformer that has already committed, or bailed, is unaffected.
func (t *Transformer) CommitNow() {
	if t.committed || t.unsupported || t.dead {
		return
	}
	if !t.checkBudget(0) {
		return
	}
	t.committed = true
	if t.sink != nil {
		t.drain(t.lastChunk)
	}
}

// SetCommitBytes sets the commit window of this transformer (0 restores the package default CommitBytes). Must be called before the first Write.
func (t *Transformer) SetCommitBytes(n int) { t.commit = n }

// SetBudget caps the sum of every buffer (Capture / Observe / Prefix windows, Defer holds, output kept before the commit point);
// exceeding it bails ("buffer budget exceeded"). 0 = unlimited. This is the total bound on memory; the individual caps remain per-item limits.
func (t *Transformer) SetBudget(n int) { t.budget = n }

// Buffered reports the number of buffered bytes currently held (for observation): captures, deferred items,
// and the output that has not been released yet. It does not include the root's closing token, which is held
// separately until Finish -- ask RootDone for that.
func (t *Transformer) Buffered() int {
	n := len(t.capBuf) + t.deferredBytes
	if !t.committed {
		n += len(t.w.buf)
	}
	return n
}

func (t *Transformer) commitBytes() int {
	if t.commit > 0 {
		return t.commit
	}
	return CommitBytes
}

// checkBudget is called wherever a buffer grows.
func (t *Transformer) checkBudget(extra int) bool {
	if t.budget > 0 && t.Buffered()+extra > t.budget {
		t.BailCode(ErrLimit, "buffer budget exceeded")
		return false
	}
	return true
}

// Committed reports whether the commit point has been passed. A bail after it cannot take back the bytes already released.
func (t *Transformer) Committed() bool { return t.committed }

// Dead reports whether the transformer has stopped (after a bail). Protocols can return early from callbacks on it.
func (t *Transformer) Dead() bool { return t.dead }

// RootDone reports whether the scanner has read the end of the root value. From that point on the output is
// only complete after Finish: the root's closing token is written there, after the protocol's Tail hook, along
// with any whitespace that followed the root. A caller that stops feeding the transformer and starts forwarding
// input verbatim must check this first -- once it is true, the bytes still held would be lost and the document
// would go out truncated.
func (t *Transformer) RootDone() bool { return t.rootDone }

// Aligned reports whether the output has caught up with the input: every byte consumed so far has been written
// out, or dropped by the protocol's own decision, and none is held back for a decision still to come -- a key
// being read, whitespace waiting for its comma, a value being captured, probed or deferred. Only then can a
// caller stop feeding the transformer and forward the input verbatim from the next byte on; RootDone must be
// false as well, because the root's closing token is written by Finish alone.
//
// It says nothing about what the protocol would still do to the bytes to come: the caller knows whether its
// own rewrites are behind this point.
//
// The check is conservative. It is true only inside a value that is passing straight through with nothing
// pending around it, which is where a chunk boundary lands in any document dominated by one large field, and
// false at every other point -- so a caller that finds it false keeps feeding and asks again after the next
// chunk. With a sink set the output is handed over at the end of every Write; without one the caller has to
// take it with Out first.
func (t *Transformer) Aligned() bool {
	if !t.committed || t.unsupported || t.dead || t.pendSet || t.replaying > 0 || t.deferredBytes > 0 || t.suspended {
		return false
	}
	if !t.regOpen || t.regT != rtOut || len(t.regSuf) > 0 || t.valSub != nil {
		return false
	}
	if t.w.vlen > 0 || len(t.w.buf) > 0 {
		return false // produced but not yet taken
	}
	for i := range t.w.frames {
		if f := &t.w.frames[i]; !f.opened || len(f.trail) > 0 {
			return false
		}
	}
	return true
}

// Unsupported reports whether input the transformer cannot handle was met. When true the output is unusable.
// The text is Err().Error(): reason + byte offset + path, ready for a log line; classify with Err().Code instead.
func (t *Transformer) Unsupported() (bool, string) {
	if t.err == nil {
		return false, ""
	}
	return true, t.err.Error()
}

// SetOutBuffer hands the transformer a buffer to build output in, owned by the caller and reused for every
// chunk and every stream. It removes the per-request allocation of the output buffer, which the allocation
// profile shows to be almost all of it; on wasip1 that matters more than the copy itself, because the garbage
// forces the Go runtime to collect while wasm linear memory never shrinks.
//
// Give it enough capacity for one commit window plus the largest chunk (128KB is a good default); a larger
// output still works, it just grows the slice once. Everything Out() or the sink hands back points into this
// buffer and stays valid only until the next Write / Finish. Must be called before the first Write.
//
// One buffer belongs to one transformer for its whole life: output accumulates across chunks until the commit
// point, so a buffer shared between concurrently running transformers would interleave their bytes. Share it
// only where streams are strictly sequential (a CLI, a single-stream worker), never across a proxy's in-flight
// requests.
func (t *Transformer) SetOutBuffer(b []byte) {
	t.w.buf = b[:0]
	t.w.fixed = true
}

// SetSink sets an output receiver. Once set, output produced past the commit point in each Write / Finish goes straight to the
// sink and the output buffer is reused instead of handed over: no allocation per chunk, and a stream no longer produces as much
// garbage as it has input. Meant for callers that consume immediately (writing to a host or a connection). The sink must consume b before returning; b is invalid afterwards.
// With a sink set Out() always returns nothing. Must be called before the first Write.
func (t *Transformer) SetSink(sink func(b []byte)) { t.sink = sink }

// Suspend, called from a protocol callback during Write, stops the scan at the next byte boundary: the callback finishes, the
// rest of the chunk is kept, and Write returns with Suspended true. The protocol may then write to the output from outside
// any callback (a value it had to fetch from elsewhere, say), and Resume scans the kept bytes and carries on.
//
// A suspension waits for something outside the document, so it is a misuse during a Defer replay or in Finish.
func (t *Transformer) Suspend() {
	if t.dead {
		return
	}
	if t.replaying > 0 || t.finishing {
		t.BailCode(ErrMisuse, "Suspend outside a Write callback")
		return
	}
	t.suspendReq = true
}

// Compact hands back the room the transformer is holding but does not need: the output buffer once its bytes have
// been taken, and the reference to the last chunk. For a scan that is about to wait -- for a fetch, or for a field the
// caller needs before it can release anything -- where what it keeps is multiplied by every stream in flight. The next
// write allocates again, sized to the chunk it gets.
func (t *Transformer) Compact() {
	if len(t.w.buf) == 0 && !t.w.fixed {
		t.w.buf = nil
	}
	t.w.release()
}

// Suspended reports whether the scan is stopped by Suspend. Write and Finish are misuses until Resume.
func (t *Transformer) Suspended() bool { return t.suspended }

// Resume continues a suspended scan with the bytes kept at the suspension. It may suspend again before they are used up.
func (t *Transformer) Resume() {
	if !t.suspended || t.dead {
		return
	}
	t.suspended = false
	held := t.held
	t.held = nil
	t.Write(held)
}

// Flush hands the releasable output to the sink now, outside a Write: for output produced while suspended, so a large
// value written in slices leaves the transformer slice by slice instead of piling up. Nothing happens without a sink or
// before the commit point.
func (t *Transformer) Flush() {
	if t.sink != nil && !t.dead {
		t.drain(t.lastChunk)
	}
}

// drain hands the releasable output to the sink (past the commit point, no bail).
func (t *Transformer) drain(chunk int) {
	if t.unsupported || !t.committed {
		return
	}
	if t.w.virt {
		if t.w.vlen > 0 {
			t.sink(t.w.vp[:t.w.vlen])
			t.w.vlen = 0
		}
		return
	}
	if len(t.w.buf) == 0 {
		return
	}
	t.sink(t.w.buf)
	if !t.w.fixed && cap(t.w.buf) > 2*chunk+4096 {
		t.w.buf = nil // do not keep the large pre-commit buffer; the next chunk allocates one of chunk size which is then reused
		t.w.hint = chunk
	} else {
		t.w.buf = t.w.buf[:0]
	}
}

// Out takes the releasable bytes. Returns nothing before the commit point, after a bail, or when a sink is set.
func (t *Transformer) Out() []byte {
	if t.unsupported || !t.committed || t.sink != nil {
		return nil
	}
	if t.w.virt { // never materialised: the output is a slice of the caller's chunk
		b := t.w.vp[:t.w.vlen]
		t.w.release() // handed over: keeping the chunk here would pin it until the next Write
		return b
	}
	t.w.release()
	if len(t.w.buf) == 0 {
		return nil
	}
	// Hand over ownership without copying or keeping capacity: the large pre-commit buffer (up to 128KB) becomes garbage right
	// away instead of being held by this stream to the end, which brings the live memory of an in-flight stream under high
	// concurrency from ~250KB down to a few dozen KB. The caller owns the slice; the next write allocates a new one.
	b := t.w.buf
	t.w.hint = len(b)
	if t.w.fixed {
		t.w.buf = b[:0] // caller-owned: valid until the next Write / Finish
	} else {
		t.w.buf = nil
	}
	return b
}

// Write feeds one chunk of input; chunks may be split at any byte boundary.
func (t *Transformer) Write(p []byte) {
	if t.dead {
		return
	}
	if t.suspended {
		t.BailCode(ErrMisuse, "Write while suspended")
		return
	}
	t.scanBase = int64(t.scanned)
	t.chunkLen = len(p)
	t.w.startChunk(p)
	t.scanned += len(p)
	t.lastChunk = len(p)
	t.w.hint = len(p) // size for the first real write; a chunk that stays virtual never allocates at all
	n := t.scan(p)
	if t.suspendReq {
		t.suspendReq = false
		if !t.dead {
			// Stopped inside the chunk: the bytes not consumed are kept (a copy: the caller owns p) and scanned again by Resume.
			t.suspended = true
			t.scanned = int(t.scanBase) + n
			t.held = append([]byte(nil), p[n:]...)
		}
	}
	t.fixOffset(int64(t.scanned))
	if !t.committed && !t.unsupported {
		if !t.checkBudget(0) {
			return
		}
		if t.scanned >= t.commitBytes() {
			t.committed = true
		}
	}
	if t.sink != nil {
		t.drain(len(p))
		t.w.release() // the sink has what this chunk produced; holding the caller's bytes past that pins them
	}
}

// Finish wraps up: calls the protocol's Tail and closes the root object.
// If it bails here, committed keeps its value; the integration layer uses that to choose between fallback and failure.
func (t *Transformer) Finish() []byte {
	if t.dead {
		return nil
	}
	if t.suspended {
		t.BailCode(ErrMisuse, "Finish while suspended")
		return nil
	}
	t.finishing = true
	defer func() { t.finishing = false }()
	t.scanBase = int64(t.scanned)
	if t.st == sInScalar {
		t.scan([]byte{' '})
		t.fixOffset(int64(t.scanned))
	}
	if !t.rootDone {
		t.BailCode(ErrIncomplete, "unexpected end of input")
		t.fixOffset(int64(t.scanned))
		return nil
	}
	t.proto.Tail(t)
	if t.unsupported {
		t.fixOffset(int64(t.scanned))
		return nil
	}
	t.w.ensureOpen(0)
	t.w.pop(t.rootCloseWs)
	t.w.buf = append(t.w.buf, t.tailWs...) // whitespace after the root (a trailing newline) is kept
	t.committed = true
	if t.sink != nil {
		t.drain(0)
		t.w.release()
		return nil
	}
	return t.Out()
}

// cur returns the callback receiver of the current frame (the sub-hook mounted with Via, or the main protocol).
func (t *Transformer) cur() Protocol {
	if f := t.top(); f != nil && f.hook != nil {
		return f.hook
	}
	return t.proto
}

// hookAt returns the callback receiver of frame i; the main protocol when i < 0 or the frame has no hook.
func (t *Transformer) hookAt(i int) Protocol {
	if i >= 0 && i < len(t.frames) && t.frames[i].hook != nil {
		return t.frames[i].hook
	}
	return t.proto
}

// ---- for protocols: path and output ----

// Protocol returns the protocol in use (the integration layer reads the Prelude through it).
func (t *Transformer) Protocol() Protocol { return t.proto }

// W is the output writer.
func (t *Transformer) W() *Writer { return &t.w }

// Depth is the number of path segments.
func (t *Transformer) Depth() int { return len(t.path) }

// Key is the key of segment level; "" when that segment is an array index.
func (t *Transformer) Key(level int) string {
	if level < 0 || level >= len(t.path) {
		return ""
	}
	return t.path[level].k
}

// Idx is the array index of segment level; -1 when that segment is a key.
func (t *Transformer) Idx(level int) int {
	if level < 0 || level >= len(t.path) {
		return -1
	}
	return t.path[level].i
}

// Last is the key of the last segment.
func (t *Transformer) Last() string { return t.Key(len(t.path) - 1) }

// PathString is for debugging: "messages[1].content".
func (t *Transformer) PathString() string {
	var b []byte
	for i, s := range t.path {
		if s.i >= 0 {
			b = append(b, '[')
			b = appendInt(b, s.i)
			b = append(b, ']')
		} else {
			if i > 0 {
				b = append(b, '.')
			}
			b = append(b, s.k...)
		}
	}
	return string(b)
}

// KeyRaw is the raw bytes of the current key (leading whitespace, quotes, colon and the whitespace around it). Protocols use it to keep the original formatting.
func (t *Transformer) KeyRaw() []byte { return t.kvRaw }

// Release asks to replay the Defer items of the current dispatch frame. The replay happens after the current callback returns, at
// the next safe point of the same frame (the end of the current value or the close of the frame); if the callback returned Enter into a child frame, the replay waits until this frame is back.
func (t *Transformer) Release() {
	t.wantRelease = true
	t.releaseAt = len(t.frames) - 1
}

// ReleaseNow replays the Defer items of the current dispatch frame synchronously. Only valid inside OnLeave, where the path still
// points at the container itself and the replayed keys attach below it correctly; inside OnValue use Release.
func (t *Transformer) ReleaseNow() { t.doRelease() }

// Deferred returns the Defer items of the current dispatch frame.
func (t *Transformer) Deferred() []DeferredKV {
	if f := t.top(); f != nil {
		return f.deferred
	}
	return nil
}

// DropDeferred discards the Defer items of the current dispatch frame.
func (t *Transformer) DropDeferred() {
	if f := t.top(); f != nil {
		t.forgetDeferred(f)
		f.deferred = nil
	}
}

// forgetDeferred subtracts the bytes of a frame's Defer items from the count (on replay or discard).
func (t *Transformer) forgetDeferred(f *frame) {
	for _, d := range f.deferred {
		t.deferredBytes -= len(d.KeyRaw) + len(d.Raw)
	}
}

// ---- scanner ----

// pushFrame pushes a dispatch frame, reusing the seen / deferred storage left in the slot instead of allocating per frame.
func (t *Transformer) pushFrame(nf frame) {
	if n := len(t.frames); n < cap(t.frames) {
		old := &t.frames[:n+1][n]
		nf.seen = old.seen[:0]
		nf.deferred = old.deferred[:0]
	}
	t.frames = append(t.frames, nf)
}

func (t *Transformer) top() *frame {
	if len(t.frames) == 0 {
		return nil
	}
	return &t.frames[len(t.frames)-1]
}

func (t *Transformer) scan(p []byte) int {
	rs := -1
	if t.regOpen {
		rs = 0
	}
	i := 0
	if t.replaying == 0 {
		defer func() { // on a bail, pin the offset to the failing byte (a bail inside a callback takes the current scan position)
			if t.dead {
				t.fixOffset(t.scanBase + int64(i))
			}
		}()
	}
scan:
	for i < len(p) && !t.dead {
		if t.suspendReq { // a callback asked to stop: everything from here on is kept for Resume
			break
		}
		c := p[i]
		switch t.st {
		case sInStr:
			if t.esc {
				t.esc = false
				switch escapeClass(c) {
				case 0:
					t.BailCode(ErrSyntax, "invalid escape in string")
					continue
				case 2:
					t.hexN = 4
				}
				i++
				continue
			}
			if t.hexN > 0 {
				if !isHexByte(c) {
					t.BailCode(ErrSyntax, "invalid \\u escape")
					continue
				}
				t.hexN--
				i++
				continue
			}
			var j int
			if t.validateUTF8 {
				j = t.scanStrUTF8(p, i)
				if t.dead {
					i = j // position of the invalid sequence
					continue
				}
			} else {
				j = scanStringBody(p, i)
			}
			if j == len(p) {
				i = j
				continue
			}
			i = j
			if p[i] < 0x20 {
				t.BailCode(ErrSyntax, "control character in string")
				continue
			}
			if p[i] == '\\' {
				t.esc = true
				i++
				continue
			}
			// unescaped quote: end of the string
			t.st = sIdle
			if t.regOpen && t.depth == t.regDepth {
				end := i + 1
				if t.regInner {
					end = i
				}
				rs = t.flush(p, rs, end)
				t.endRegion()
				t.afterValue()
			}
			i++
		case sInKey:
			if t.esc {
				t.esc = false
				switch escapeClass(c) {
				case 0:
					t.BailCode(ErrSyntax, "invalid escape in key")
					continue
				case 2:
					t.hexN = 4
				}
				t.keyBuf = append(t.keyBuf, c)
				t.kvRaw = append(t.kvRaw, c)
				i++
				continue
			}
			if t.hexN > 0 {
				if !isHexByte(c) {
					t.BailCode(ErrSyntax, "invalid \\u escape")
					continue
				}
				t.hexN--
				t.keyBuf = append(t.keyBuf, c)
				t.kvRaw = append(t.kvRaw, c)
				i++
				continue
			}
			if t.validateUTF8 {
				j := t.scanStrUTF8(p, i)
				if t.dead {
					i = j
					continue
				}
				if j > i {
					t.keyBuf = append(t.keyBuf, p[i:j]...)
					t.kvRaw = append(t.kvRaw, p[i:j]...)
					i = j
					continue
				}
			} else if j := scanStringBody(p, i); j > i { // append plain bytes as a run
				t.keyBuf = append(t.keyBuf, p[i:j]...)
				t.kvRaw = append(t.kvRaw, p[i:j]...)
				i = j
				continue
			}
			if c < 0x20 {
				t.BailCode(ErrSyntax, "control character in key")
				continue
			}
			if c == '\\' {
				t.esc = true
				t.keyEsc = true
				t.keyBuf = append(t.keyBuf, c)
				t.kvRaw = append(t.kvRaw, c)
				i++
				continue
			}
			// c == '"'
			t.st = sIdle
			t.kvRaw = append(t.kvRaw, c)
			t.onKeyDone()
			i++
		case sInScalar:
			if isScalarByte(c) {
				if t.lit.kind == KindNumber { // inlined table-driven DFA: numbers are the most common scalar inside regions
					for i < len(p) && isScalarByte(p[i]) {
						t.lit.num = numStep(t.lit.num, p[i])
						if t.lit.num == nsBad {
							t.BailCode(ErrSyntax, "invalid literal")
							continue scan
						}
						i++
					}
					continue
				}
				for i < len(p) && isScalarByte(p[i]) {
					if !t.lit.step(p[i]) {
						t.BailCode(ErrSyntax, "invalid literal")
						continue scan
					}
					i++
				}
				continue
			}
			if !t.lit.done() {
				t.BailCode(ErrSyntax, "incomplete literal")
				continue
			}
			t.st = sIdle
			if t.regOpen && t.depth == t.regDepth {
				rs = t.flush(p, rs, i)
				t.endRegion()
				t.afterValue()
			}
			// c is not consumed; back to sIdle to handle it
		case sIdle:
			if jsonSpace[c] {
				if !t.regOpen && t.rootSeen && !t.rootDone {
					t.wsRaw = append(t.wsRaw, c)
				} else if t.rootDone {
					t.tailWs = append(t.tailWs, c)
				} else if !t.rootSeen {
					t.leadWs = append(t.leadWs, c)
				}
				i++
				continue
			}
			if t.regOpen {
				// Inside a region: only track the grammar, no dispatch. A tight loop consumes structural characters and whitespace in one go
				// and returns to the outer state machine only for a string / scalar or when the region closes. The grammar phase lives in a local
				// and is written back on exit; it encodes the container kind, so commas / colons / strings / scalars never touch the stack, only brackets do.
				ph := t.regPh
				for i < len(p) {
					c = p[i]
					if jsonSpace[c] {
						i++
						continue
					}
					switch c {
					case '"':
						if ph = regAfterStr[ph]; ph == rErr {
							t.BailCode(ErrSyntax, "unexpected string")
							continue scan
						}
						t.regPh = ph
						t.st = sInStr
						t.esc = false
						i++
						continue scan
					case '{', '[':
						if regAfterVal[ph] == rErr {
							t.BailCode(ErrSyntax, "unexpected object or array")
							continue scan
						}
						t.depth++
						t.regPush(c == '[')
						ph = t.regPh
					case '}', ']':
						k := regClose[ph]
						if k == 0 {
							t.BailCode(ErrSyntax, "missing value or trailing comma before closing bracket")
							continue scan
						}
						if (c == ']') != (k == 2) {
							t.BailCode(ErrSyntax, "mismatched closing bracket")
							continue scan
						}
						ph = t.regPop()
						t.depth--
						if t.depth == t.regDepth {
							t.regPh = ph
							rs = t.flush(p, rs, i+1)
							t.endRegion()
							t.afterValue()
							i++
							continue scan
						}
					case ',':
						if ph = regAfterComma[ph]; ph == rErr {
							t.BailCode(ErrSyntax, "unexpected comma")
							continue scan
						}
					case ':':
						if ph != rColon {
							t.BailCode(ErrSyntax, "unexpected colon")
							continue scan
						}
						ph = rOValue
					default:
						if ph = regAfterVal[ph]; ph == rErr {
							t.BailCode(ErrSyntax, "unexpected literal")
							continue scan
						}
						if !t.lit.start(c) {
							t.BailCode(ErrSyntax, "invalid character")
							continue scan
						}
						t.regPh = ph
						t.st = sInScalar
						i++
						continue scan
					}
					i++
				}
				t.regPh = ph
				continue
			}
			if t.rootDone {
				t.BailCode(ErrTrailing, "data after root value")
				continue
			}
			f := t.top()
			if f == nil {
				// root
				isArr := c == '['
				if (c == '{' && t.root == RootArray) || (isArr && t.root == RootObject) || (c != '{' && c != '[') {
					switch t.root {
					case RootArray:
						t.BailCode(ErrRoot, "root is not an array")
					case RootAny:
						t.BailCode(ErrRoot, "root is not an object or array")
					default:
						t.BailCode(ErrRoot, "root is not an object")
					}
					continue
				}
				t.rootSeen = true
				t.depth = 1
				if isArr {
					t.pushFrame(frame{kind: fkArr, ph: phValue, idx: -1})
				} else {
					t.pushFrame(frame{kind: fkObj, ph: phKey, idx: -1})
				}
				t.w.buf = append(t.w.buf, t.leadWs...) // whitespace before the root is kept
				t.w.push("", nil, isArr)
				t.wsRaw = t.wsRaw[:0]
				i++
				continue
			}
			switch c {
			case '}', ']':
				if (c == '}') != (f.kind == fkObj) {
					t.BailCode(ErrSyntax, "mismatched closing bracket")
					continue
				}
				if f.kind == fkObj && (f.ph == phColon || f.ph == phValue) {
					t.BailCode(ErrSyntax, "missing value after key")
					continue
				}
				if f.kind == fkArr && f.ph == phValue && f.idx >= 0 {
					t.BailCode(ErrSyntax, "trailing comma in array")
					continue
				}
				if f.kind == fkObj && f.ph == phKey && f.n > 0 {
					t.BailCode(ErrSyntax, "trailing comma in object")
					continue
				}
				t.closeContainer()
				i++
			case ':':
				if f.kind != fkObj || f.ph != phColon {
					t.BailCode(ErrSyntax, "unexpected colon")
					continue
				}
				t.kvRaw = append(t.kvRaw, t.wsRaw...)
				t.kvRaw = append(t.kvRaw, ':')
				t.wsRaw = t.wsRaw[:0]
				f.ph = phValue
				i++
			case ',':
				if f.ph != phComma {
					t.BailCode(ErrSyntax, "unexpected comma")
					continue
				}
				t.w.trailWs(t.wsRaw) // whitespace between a value and its comma: parked on the output level, written verbatim with the next separator
				t.wsRaw = t.wsRaw[:0]
				if f.kind == fkObj {
					f.ph = phKey
				} else {
					f.ph = phValue
				}
				i++
			case '"':
				if f.kind == fkObj && f.ph == phKey {
					t.st = sInKey
					t.esc = false
					t.keyEsc = false
					t.keyBuf = t.keyBuf[:0]
					t.kvRaw = append(t.kvRaw[:0], t.wsRaw...)
					t.kvRaw = append(t.kvRaw, '"')
					t.wsRaw = t.wsRaw[:0]
					f.ph = phColon
					i++
					continue
				}
				if !t.valueStart(f, KindString) {
					continue
				}
				if t.regOpen {
					rs = i
					if t.regInner {
						rs = i + 1
					}
				}
				t.st = sInStr
				t.esc = false
				i++
			case '{', '[':
				kind := KindObject
				if c == '[' {
					kind = KindArray
				}
				if !t.valueStart(f, kind) {
					continue
				}
				if t.regOpen {
					rs = i
					t.regPush(kind == KindArray)
				}
				t.depth++
				i++
			default:
				if !t.lit.start(c) {
					t.BailCode(ErrSyntax, "invalid character")
					continue
				}
				if !t.valueStart(f, t.lit.kind) {
					continue
				}
				if t.regOpen {
					rs = i
				}
				t.st = sInScalar
				i++
			}
		}
	}
	if rs >= 0 && t.regOpen && !t.dead {
		t.flush(p, rs, len(p))
	}
	return i
}

// flush hands p[rs:end] to the region and returns the new rs (-1).
func (t *Transformer) flush(p []byte, rs, end int) int {
	if rs >= 0 && end > rs {
		t.emitRegionAt(p, rs, end)
		if t.dead && t.replaying == 0 { // cap / budget overflow: pin the offset to the first byte that did not fit
			t.fixOffset(t.scanBase + int64(rs) + int64(t.limitAt))
		}
	}
	return -1
}

// valueStart decides the action when the first byte of a value arrives inside a dispatch frame.
// Returns false when it bailed.
func (t *Transformer) valueStart(f *frame, kind ValueKind) bool {
	if f.kind == fkObj {
		if f.ph != phValue {
			t.BailCode(ErrSyntax, "unexpected value")
			return false
		}
		if t.fieldTypes != nil && len(t.path) == 1 && kind != KindNull {
			if want, ok := t.fieldTypes[t.path[0].k]; ok && want&typeBit(kind) == 0 {
				t.BailCode(ErrUnsupported, "field "+t.path[0].k+" cannot be "+kind.String())
				return false
			}
		}
		t.kvRaw = append(t.kvRaw, t.wsRaw...)
		t.wsRaw = t.wsRaw[:0]
	} else {
		if f.ph != phValue {
			t.BailCode(ErrSyntax, "missing comma between array elements")
			return false
		}
		// start of an array element
		f.idx++
		t.elemWs = append(t.elemWs[:0], t.wsRaw...)
		t.wsRaw = t.wsRaw[:0]
		t.path = append(t.path, seg{i: f.idx})
		t.pend = t.cur().OnElem(t)
		t.pendSet = true
		if t.dead {
			return false
		}
	}
	act := &t.pend // a pointer: Action is over a hundred bytes and passing it by value would copy it repeatedly on the dispatch hot path
	t.pendSet = false
	if act.kind == akProbe {
		t.pend = t.cur().OnStart(t, kind)
		if t.dead {
			return false
		}
	}
	return t.apply(f, act, kind)
}

// apply executes an action.
func (t *Transformer) apply(f *frame, act *Action, kind ValueKind) bool {
	isContainer := kind == KindObject || kind == KindArray
	switch act.kind {
	case akBail:
		t.BailCode(act.code, act.reason)
		return false
	case akProbe:
		t.BailCode(ErrMisuse, "Probe cannot be nested")
		return false
	case akEnter:
		if !isContainer {
			if !act.lenient {
				t.BailCode(ErrUnsupported, "expected an object or array")
				return false
			}
			act.kind = akPass
			return t.apply(f, act, kind)
		}
		nf := frame{ph: phKey, idx: -1, flat: act.flat, lazy: act.lazy}
		if kind == KindArray {
			nf.kind = fkArr
			nf.ph = phValue
		}
		nf.hook = act.via
		if nf.hook == nil {
			nf.hook = f.hook // a level the sub-hook entered itself stays with it
		}
		t.pushFrame(nf)
		if !act.flat {
			name := ""
			var raw []byte
			if f.kind == fkObj {
				name = act.key
				if name == "" {
					name = t.Last()
					raw = t.kvRaw // push copies it into the slot
				}
			} else {
				raw = t.elemWs
			}
			t.w.push(name, raw, kind == KindArray)
		}
		return true
	case akPass, akObserve:
		if act.inner && kind != KindString {
			t.BailCode(ErrUnsupported, "Inner requires a string value")
			return false
		}
		level := int(act.level)
		if level < 0 {
			level = t.w.Level()
		}
		var ok bool
		if f.kind == fkObj {
			if act.key == "" {
				ok = t.w.KeyRawAt(level, t.kvRaw)
			} else {
				ok = t.w.KeyAt(level, act.key)
			}
		} else {
			ok = t.w.ElemRawAt(level, t.elemWs)
		}
		if !ok {
			t.BailCode(ErrMisuse, "target output level already has an open child level")
			return false
		}
		if len(act.prefix) > 0 {
			t.w.Raw(act.prefix)
		}
		target := rtOut
		if act.kind == akObserve {
			target = rtObserve
		}
		if target == rtOut && isContainer && len(act.suffix) == 0 {
			if sub := t.validationSubtree(kind); sub != nil {
				t.beginValidation(act, sub, true)
				return true
			}
		}
		t.beginRegion(target, act)
		return true
	case akSkip:
		// A dropped field still has to be judged: the caller's unmarshal reads every known field, whether or
		// not the transform keeps it, and rejects the request when one holds the wrong type. Skipping the
		// bytes without looking is what left the streaming path more permissive than the buffered one.
		if isContainer {
			if sub := t.validationSubtree(kind); sub != nil {
				t.beginValidation(act, sub, false)
				return true
			}
		}
		t.beginRegion(rtSkip, act)
		return true
	case akCapture:
		t.beginRegion(rtCapture, act)
		return true
	case akDefer:
		if f.kind != fkObj {
			t.BailCode(ErrMisuse, "Defer applies only to object keys")
			return false
		}
		t.regKey = t.Last()
		t.beginRegion(rtDefer, act)
		return true
	case akPrefix:
		if kind != KindString {
			t.BailCode(ErrUnsupported, "Prefix requires a string value")
			return false
		}
		act.inner = true
		t.beginRegion(rtPrefix, act)
		return true
	}
	t.BailCode(ErrMisuse, "unknown action")
	return false
}

// regPush enters one container level inside a region: records its kind and switches the grammar phase to "expecting a key / value".
func (t *Transformer) regPush(isArr bool) {
	t.regN++
	if t.regN <= 64 {
		bit := uint64(1) << uint(t.regN-1)
		if isArr {
			t.regKinds |= bit
		} else {
			t.regKinds &^= bit
		}
	} else {
		t.regDeep = append(t.regDeep, isArr)
	}
	if isArr {
		t.regPh = rAValue0
	} else {
		t.regPh = rKey0
	}
}

// regPop closes one container level inside a region and returns the phase back in the parent (expecting , or } in an object, , or ] in an array).
func (t *Transformer) regPop() regPhase {
	t.regN--
	n := t.regN
	if n == 0 {
		return rTop
	}
	var parentArr bool
	if n <= 64 {
		parentArr = t.regKinds>>uint(n-1)&1 == 1
	} else {
		t.regDeep = t.regDeep[:n-64]
		parentArr = t.regDeep[n-65]
	}
	if parentArr {
		return rAComma
	}
	return rOComma
}

// validationSubtree returns the subtree to check this value against, or nil when there is nothing to check:
// no tree set, not a root-level field, the tree has no opinion about this field, or it says nothing about what
// is inside it -- in which case SetFieldTypes has already judged the value's own type and the region keeps its
// fast path untouched.
func (t *Transformer) validationSubtree(kind ValueKind) *FieldTree {
	if t.fieldTree == nil || t.fieldTree.Keys == nil || len(t.path) != 1 {
		return nil
	}
	sub, ok := t.fieldTree.Keys[t.path[0].k]
	if !ok || sub == nil || sub.Any {
		return nil
	}
	if sub.Keys == nil && sub.Elem == nil {
		return nil // nothing said about the members
	}
	if kind == KindObject && sub.Types&TypeObject == 0 {
		return nil // the value's own type is already wrong; SetFieldTypes reports that
	}
	if kind == KindArray && sub.Types&TypeArray == 0 {
		return nil
	}
	return sub
}

// beginValidation opens a region that keeps a bounded copy for the type check. emit says whether its bytes
// still reach the output: they do when the field was being passed through, they do not when it was dropped.
func (t *Transformer) beginValidation(act *Action, sub *FieldTree, emit bool) {
	t.beginRegion(rtValidate, act)
	t.regCap, t.valSub, t.valOver, t.valEmit = valCapBytes, sub, false, emit
}

func (t *Transformer) beginRegion(target regionTarget, act *Action) {
	t.regN = 0
	t.regDeep = t.regDeep[:0]
	t.regPh = rTop
	t.regOpen = true
	t.regT = target
	t.regDepth = t.depth
	t.regInner = act.inner
	t.regSuf = act.suffix
	t.regCap = int(act.cap)
	t.capBuf = t.capBuf[:0]
}

// emitRegion handles one run of raw bytes inside a region.
// emitRegionAt handles p[from:to] inside a region; the offset lets the writer keep the pass-through run virtual.
func (t *Transformer) emitRegionAt(p []byte, from, to int) {
	if t.regT == rtOut && len(t.regSuf) == 0 {
		t.w.passthroughAt(p, from, to)
		return
	}
	t.emitRegion(p[from:to])
}

func (t *Transformer) emitRegion(b []byte) {
	switch t.regT {
	case rtOut:
		t.w.Raw(b)
	case rtSkip:
	case rtCapture, rtDefer:
		t.capAppend(b)
	case rtObserve:
		t.w.Raw(b)
		t.capAppend(b)
	case rtValidate:
		if t.valEmit {
			t.w.Raw(b)
		}
		if !t.valOver {
			// A soft cap, unlike capAppend's: outgrowing it stops the check, it does not fail the request.
			if len(t.capBuf)+len(b) > t.regCap {
				t.valOver, t.capBuf = true, t.capBuf[:0]
			} else {
				t.capBuf = append(t.capBuf, b...)
			}
		}
	case rtPrefix:
		room := t.regCap - len(t.capBuf)
		if room >= len(b) {
			if !t.checkBudget(len(b)) {
				return
			}
			t.capBuf = append(t.capBuf, b...)
			return
		}
		t.capBuf = append(t.capBuf, b[:room]...)
		rest := b[room:]
		t.runPrefix(false)
		if t.dead {
			return
		}
		// bytes after the window are handled according to the new target
		t.emitRegion(rest)
	}
}

func (t *Transformer) capAppend(b []byte) {
	if t.regCap > 0 && len(t.capBuf)+len(b) > t.regCap {
		t.limitAt = t.regCap - len(t.capBuf)
		t.BailCode(ErrLimit, "capture limit exceeded")
		return
	}
	if t.budget > 0 && t.Buffered()+len(b) > t.budget {
		t.limitAt = t.budget - t.Buffered()
		if t.limitAt < 0 {
			t.limitAt = 0
		}
		t.BailCode(ErrLimit, "buffer budget exceeded")
		return
	}
	t.capBuf = append(t.capBuf, b...)
}

// runPrefix hands the prefix window to the protocol and switches the region target according to its answer.
func (t *Transformer) runPrefix(complete bool) {
	act, resume := t.cur().OnPrefix(t, t.capBuf, complete)
	if t.dead {
		return
	}
	if resume < 0 || resume > len(t.capBuf) {
		t.BailCode(ErrMisuse, "OnPrefix returned an invalid resume offset")
		return
	}
	switch act.kind {
	case akPass:
		if len(act.prefix) > 0 {
			t.w.Raw(act.prefix)
		}
		t.w.Raw(t.capBuf[resume:])
		t.regT = rtOut
		t.regSuf = act.suffix
	case akSkip:
		t.regT = rtSkip
		t.regSuf = nil
	case akBail:
		t.BailCode(act.code, act.reason)
		return
	default:
		t.BailCode(ErrMisuse, "OnPrefix must return Pass, Skip or Bail")
		return
	}
	t.capBuf = t.capBuf[:0]
}

// endRegion ends a region: delivers the buffer and writes the suffix.
func (t *Transformer) endRegion() {
	if t.dead {
		return // a Bail such as a buffer overflow already happened; do not hand truncated data to the protocol
	}
	switch t.regT {
	case rtOut:
		if len(t.regSuf) > 0 {
			t.w.Raw(t.regSuf)
		}
	case rtObserve:
		if len(t.regSuf) > 0 {
			t.w.Raw(t.regSuf)
		}
		t.cur().OnValue(t, t.capBuf)
	case rtCapture:
		t.cur().OnValue(t, t.capBuf)
	case rtValidate:
		if !t.valOver && t.valSub != nil {
			if !validateAgainst(t.capBuf, t.valSub) {
				t.BailCode(ErrUnsupported, "field "+t.path[0].k+" holds a value of the wrong type")
				return
			}
		}
		t.capBuf, t.valSub, t.valOver = t.capBuf[:0], nil, false
	case rtDefer:
		f := t.top()
		raw := make([]byte, len(t.capBuf))
		copy(raw, t.capBuf)
		f.deferred = append(f.deferred, DeferredKV{Key: t.regKey, KeyRaw: append([]byte(nil), t.kvRaw...), Raw: raw})
		t.deferredBytes += len(t.kvRaw) + len(raw)
	case rtPrefix:
		t.runPrefix(true)
		if t.dead {
			return
		}
		if t.regT == rtOut && len(t.regSuf) > 0 {
			t.w.Raw(t.regSuf)
		}
	}
	t.regOpen = false
	t.regT = rtNone
	t.regSuf = nil
	t.capBuf = t.capBuf[:0]
}

// onKeyDone: the key is complete, dispatch OnKey.
func (t *Transformer) onKeyDone() {
	var key string
	if t.keyEsc { // an escaped key: decode as JSON before dispatching (kvRaw still holds the original)
		k, ok := decodeKey(t.keyBuf)
		if !ok {
			t.BailCode(ErrSyntax, "invalid escape in key")
			return
		}
		key = k
	} else {
		if t.keys == nil {
			t.keys = NewKeyCache()
		}
		slot := &t.keys[hashKey(t.keyBuf)&(keyCacheSize-1)]
		if *slot == string(t.keyBuf) { // the comparison does not allocate
			key = *slot
		} else {
			key = string(t.keyBuf)
			*slot = key
		}
	}
	f := t.top()
	t.path = append(t.path, seg{k: key, i: -1})
	if t.DupKeyBail || t.dup != DupKeysPass {
		dup := false
		for _, s := range f.seen {
			if s == key {
				dup = true
				break
			}
		}
		if dup && (t.DupKeyBail || t.dup == DupKeysBail) {
			t.BailCode(ErrDuplicateKey, "duplicate key "+strconv.Quote(key))
			return
		}
		if !dup {
			f.seen = append(f.seen, key)
		} else { // DupKeysFirst: later occurrences of the same key are not dispatched, just dropped
			t.pend = Skip()
			t.pendSet = true
			return
		}
	}
	t.pend = t.cur().OnKey(t)
	t.pendSet = true
	if t.pend.kind == akBail {
		t.BailCode(t.pend.code, t.pend.reason)
	}
}

// afterValue: a value (scalar / string / container) ended inside a dispatch frame.
func (t *Transformer) afterValue() {
	f := t.top()
	if f == nil || t.dead {
		return
	}
	f.ph = phComma
	f.n++
	if len(t.path) > 0 {
		t.path = t.path[:len(t.path)-1]
	}
	if t.wantRelease && t.releaseAt == len(t.frames)-1 {
		t.wantRelease = false
		t.doRelease()
	}
}

// closeContainer closes a dispatch frame.
func (t *Transformer) closeContainer() {
	f := t.top()
	closeWs := append([]byte(nil), t.wsRaw...)
	t.wsRaw = t.wsRaw[:0]
	t.hookAt(len(t.frames) - 2).OnLeave(t) // the close goes back to whoever issued the Enter
	if t.dead {
		return
	}
	if t.wantRelease && t.releaseAt == len(t.frames)-1 {
		t.wantRelease = false
		t.doRelease()
		if t.dead {
			return
		}
	}
	if len(f.deferred) > 0 {
		// The protocol neither replayed nor explicitly dropped them: a protocol bug, and swallowing it silently would produce a request with different meaning.
		t.BailCode(ErrLeftoverDefer, "deferred items not released before the container closed")
		return
	}
	flat, lazy := f.flat, f.lazy
	t.frames = t.frames[:len(t.frames)-1]
	t.depth--
	if t.depth == 0 {
		t.rootDone = true
		t.rootCloseWs = closeWs
		return // the root output level is closed in Finish (after Tail)
	}
	if !flat {
		if !lazy {
			t.w.Open() // a container present in the input must exist in the output, even empty
		}
		t.w.pop(closeWs)
	}
	t.afterValue()
}

// doRelease replays the Defer items of the current dispatch frame: each goes through OnKey again and is handled according to the protocol state of this moment.
func (t *Transformer) doRelease() {
	f := t.top()
	if f == nil || len(f.deferred) == 0 {
		return
	}
	kvs := f.deferred
	t.forgetDeferred(f)
	f.deferred = nil
	for _, kv := range kvs {
		if t.dead {
			return
		}
		t.replayKV(kv)
	}
}

func (t *Transformer) replayKV(kv DeferredKV) {
	f := t.top()
	t.kvRaw = append(t.kvRaw[:0], kv.KeyRaw...)
	t.wsRaw = t.wsRaw[:0]
	t.path = append(t.path, seg{k: kv.Key, i: -1})
	t.pend = t.cur().OnKey(t)
	t.pendSet = true
	if t.pend.kind == akBail {
		t.BailCode(t.pend.code, t.pend.reason)
		return
	}
	f.ph = phValue
	t.replaying++
	t.scan(kv.Raw)
	if t.st == sInScalar {
		t.scan([]byte{' '}) // a separator to terminate a scalar
	}
	t.replaying--
	t.wsRaw = t.wsRaw[:0] // the padding space above is not part of the original
}

// scanStrUTF8 scans a string body with UTF-8 validation on: ASCII runs use SWAR, bytes >= 0x80 go one by one through the RFC 3629
// state machine (a sequence may span chunks, the state stays in t.u8). Returns the position of the next ASCII byte the caller must
// handle (quote / backslash / control character) or len(p); bails on an invalid sequence.
func (t *Transformer) scanStrUTF8(p []byte, i int) int {
	for i < len(p) {
		c := p[i]
		if c >= 0x80 {
			if t.u8.need == 0 {
				// start of a sequence that lies entirely in this chunk: validated with one table lookup, skipping the byte state machine
				f := utf8First[c]
				sz := int(f & 7)
				if sz == 0 {
					t.BailCode(ErrSyntax, "invalid UTF-8 sequence")
					return i
				}
				if i+sz <= len(p) {
					r := utf8Accept[f>>3]
					ok := p[i+1] >= r.lo && p[i+1] <= r.hi
					if sz >= 3 {
						ok = ok && p[i+2] >= 0x80 && p[i+2] <= 0xBF
					}
					if sz == 4 {
						ok = ok && p[i+3] >= 0x80 && p[i+3] <= 0xBF
					}
					if !ok {
						t.BailCode(ErrSyntax, "invalid UTF-8 sequence")
						return i
					}
					i += sz
					continue
				}
			}
			if !t.u8.step(c) { // a sequence split across chunks: byte by byte
				t.BailCode(ErrSyntax, "invalid UTF-8 sequence")
				return i
			}
			i++
			continue
		}
		if t.u8.need > 0 {
			t.BailCode(ErrSyntax, "invalid UTF-8 sequence")
			return i
		}
		i = scanStringBodyUTF8(p, i)
		if i == len(p) || p[i] < 0x80 {
			return i
		}
	}
	return i
}

// hashKey is the FNV-1a hash of the key bytes (keys are usually short; cheaper than a general map's hashing and probing).
func hashKey(b []byte) uint32 {
	h := uint32(2166136261)
	for _, c := range b {
		h = (h ^ uint32(c)) * 16777619
	}
	return h
}

// decodeKey decodes an escaped key. It is a separate function so the key variable in onKeyDone does not escape to the heap by having its address taken.
func decodeKey(raw []byte) (string, bool) {
	q := make([]byte, 0, len(raw)+2)
	q = append(q, '"')
	q = append(q, raw...)
	q = append(q, '"')
	var s string
	if err := json.Unmarshal(q, &s); err != nil {
		return "", false
	}
	return s, true
}
