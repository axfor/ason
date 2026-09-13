package ason

import (
	"encoding/json"
	"strconv"
)

// What happens once the scan loop has identified something: deciding whether a value may start (valueStart),
// applying the hook's Action to it (apply), and the region machinery that carries a subtree through to the output
// -- begin/emit/end, the run prefix, the capped append that enforces the budget, and the replay of a deferred
// key/value. Plus the two key helpers, hashKey and decodeKey, which is where the only encoding/json call in the
// engine lives (a key with escapes, decoded once and cached).
//
// Split out of engine.go, which had grown to 1858 lines. Same package, same API.

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
// emitRegionAt handles p[from:to] inside a region, committing at the window if the run crosses it.
func (t *Transformer) emitRegionAt(p []byte, from, to int) {
	// The commit window is a count of bytes scanned, so a run that crosses it is split there: what is still inside
	// the window is held as before, and everything past it is released like any other committed output. Without the
	// split a chunk larger than the window -- a whole body delivered in one piece -- is held whole, which both
	// breaks the ceiling the window promises and makes the writer copy the chunk into a buffer its own size.
	if t.sink != nil && !t.committed && t.replaying == 0 {
		if room := t.commitBytes() - (int(t.scanBase) + from); room < to-from {
			if room > 0 {
				t.emitRun(p, from, from+room)
				from += room
			}
			t.committed = true
			t.syncFlushRun()
		}
	}
	t.emitRun(p, from, to)
}

// emitRun hands p[from:to] to the region's target; the offset lets the writer keep the pass-through run virtual.
func (t *Transformer) emitRun(p []byte, from, to int) {
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
	// The replayed bytes are not the chunk the writer is counting through: their offsets are not comparable with it
	// and they live in a buffer that is reused, so nothing of them is handed over as a view.
	through := t.w.flushRun
	t.w.flushRun = nil
	t.scan(kv.Raw)
	if t.st == sInScalar {
		t.scan([]byte{' '}) // a separator to terminate a scalar
	}
	t.w.flushRun = through
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
