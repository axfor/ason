package engine

// The scanner's internal state: what the byte loop is in the middle of (scanState), what kind of container a
// frame describes (frameKind, phase, regPhase), the frame itself, and the small types that hang off it -- a
// deferred key/value awaiting replay, a captured segment, where a region's bytes are headed. None of it is
// reachable from outside the package except KeyCache, which callers hand in to share decoded keys across streams.
//
// Split out of engine.go, which had grown to 1858 lines. Same package, same API.

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

// The tables below are indexed as tbl[ph&regPhaseMask] rather than tbl[ph]. The mask is what lets the compiler
// prove the index is in range and drop the bounds check -- five of them in scan, each a compare and a branch to
// panicBounds, replaced by one AND. It is only sound while every phase fits under the mask, so that is asserted at
// compile time: add a phase past rAComma without widening the tables and this stops building rather than folding a
// stray index onto the wrong entry, which is what a mask does instead of panicking.
const regPhaseMask = 15

var _ [regPhaseMask - int(rAComma)]struct{} // compile-time: rAComma <= regPhaseMask

var (
	// phase after the start of a string (key or value).
	regAfterStr = [16]regPhase{rKey0: rColon, rKey: rColon, rOValue: rOComma, rAValue0: rAComma, rAValue: rAComma, rAComma: rErr}
	// after the start of a scalar / container (containers are then overridden by regPush).
	regAfterVal = [16]regPhase{rOValue: rOComma, rAValue0: rAComma, rAValue: rAComma, rAComma: rErr}
	// after a comma.
	regAfterComma = [16]regPhase{rOComma: rKey, rAComma: rAValue}
	// can the container close: 0 = no (missing key / value, trailing comma), 1 = object may close, 2 = array may close.
	regClose = [16]uint8{rKey0: 1, rOComma: 1, rAValue0: 2, rAComma: 2}
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
