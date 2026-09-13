package ason

// The Transformer itself and everything a caller sets on it before bytes arrive: duplicate-key policy, root kind,
// per-field type expectations, the field tree, the shared key cache, UTF-8 validation, the commit window and the
// memory budget. All of it is configuration -- read once per stream, not per chunk -- which is why it sits apart
// from the loop that runs per byte.
//
// Split out of engine.go, which had grown to 1858 lines. Same package, same API.

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
		// hand over the flat table as well. A tree from FieldTreeOf has it ready and every transformer shares it.
		if tr.rootTypes != nil {
			t.fieldTypes = tr.rootTypes
		} else {
			t.fieldTypes = keyTypes(tr)
		}
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
	t.syncFlushRun()
	if t.sink != nil {
		t.drain(t.lastChunk)
	}
}

// SetCommitBytes sets the commit window of this transformer (0 restores the package default CommitBytes). Must be called before the first Write.
func (t *Transformer) SetCommitBytes(n int) { t.commit = n }

// SetBudget caps the sum of every buffer (Capture / Observe / Prefix windows, Defer holds, output kept before the commit point);
// exceeding it bails ("buffer budget exceeded"). 0 = unlimited. This is the total bound on memory; the individual caps remain per-item limits.
func (t *Transformer) SetBudget(n int) { t.budget = n }
