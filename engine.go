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

// The scan loop, and only it. One pass over the bytes of a chunk, dispatching on the state left over from the
// previous chunk: every decision here is made per byte, which is why the configuration, the lifecycle and the
// region machinery live in files of their own. State types are in state.go, the Transformer and its setters in
// transformer.go, everything around the loop in lifecycle.go, and what happens after a value is recognised in
// region.go.

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
			// The scan stops at one of three bytes, and for a document of short strings the common one by far is the
			// closing quote -- one per string, where the other two are an escape or a syntax error. Testing for it
			// first spends one comparison per string instead of two.
			if c = p[i]; c != '"' {
				if c < 0x20 {
					t.BailCode(ErrSyntax, "control character in string")
					continue
				}
				t.esc = true // c == '\\'
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
					// No whitespace test before the switch: a request body has a structural character every few bytes
					// and, being machine-generated, usually no whitespace at all, so the table lookup was paid on
					// every one of them for nothing. Whitespace is a byte the switch does not name, so it is handled
					// in the default branch, which costs a document that does have whitespace one extra jump.
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
						if jsonSpace[c] {
							i++
							continue
						}
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
