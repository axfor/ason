package ason

import (
	"encoding/binary"
	"math/bits"
	"strconv"
	"unicode/utf8"
)

const hexDigits = "0123456789abcdef"

// AppendJSONString encodes a string by the rules of encoding/json (including the HTML-safe escapes of < > &).
// Byte-identical to the Marshal output of encoding/json.
func AppendJSONString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(s); {
		b := s[i]
		if b < utf8.RuneSelf {
			if safeSet[b] {
				i++
				continue
			}
			dst = append(dst, s[start:i]...)
			switch b {
			case '\\', '"':
				dst = append(dst, '\\', b)
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\r':
				dst = append(dst, '\\', 'r')
			case '\t':
				dst = append(dst, '\\', 't')
			default:
				// control characters and < > & become \u00XX
				dst = append(dst, '\\', 'u', '0', '0', hexDigits[b>>4], hexDigits[b&0xF])
			}
			i++
			start = i
			continue
		}
		c, size := utf8.DecodeRuneInString(s[i:])
		if c == utf8.RuneError && size == 1 {
			dst = append(dst, s[start:i]...)
			dst = append(dst, `�`...)
			i += size
			start = i
			continue
		}
		if c == ' ' || c == ' ' {
			dst = append(dst, s[start:i]...)
			dst = append(dst, '\\', 'u', '2', '0', '2', hexDigits[c&0xF])
			i += size
			start = i
			continue
		}
		i += size
	}
	dst = append(dst, s[start:]...)
	return append(dst, '"')
}

// safeSet matches the htmlSafeSet of encoding/json: these ASCII bytes are written as is.
var safeSet = func() [utf8.RuneSelf]bool {
	var s [utf8.RuneSelf]bool
	for b := 0x20; b < utf8.RuneSelf; b++ {
		s[b] = true
	}
	s['"'] = false
	s['\\'] = false
	s['<'] = false
	s['>'] = false
	s['&'] = false
	return s
}()

func appendInt(dst []byte, n int) []byte { return strconv.AppendInt(dst, int64(n), 10) }

// JSONUnquote decodes a JSON string literal (quotes included).
// Invalid input returns ok=false.
func JSONUnquote(raw []byte) (string, bool) {
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return "", false
	}
	return unescapeInner(raw[1 : len(raw)-1])
}

// unescapeInner decodes string content without the quotes.
func unescapeInner(b []byte) (string, bool) {
	// fast path: no escapes
	hasEsc := false
	for _, c := range b {
		if c == '\\' {
			hasEsc = true
			break
		}
	}
	if !hasEsc {
		return string(b), true
	}
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c != '\\' {
			out = append(out, c)
			continue
		}
		i++
		if i >= len(b) {
			return "", false
		}
		switch b[i] {
		case '"', '\\', '/':
			out = append(out, b[i])
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'u':
			if i+4 >= len(b) {
				return "", false
			}
			r, ok := hex4(b[i+1 : i+5])
			if !ok {
				return "", false
			}
			i += 4
			if r >= 0xD800 && r < 0xDC00 { // high surrogate, try to pair it
				if i+6 < len(b) && b[i+1] == '\\' && b[i+2] == 'u' {
					if r2, ok2 := hex4(b[i+3 : i+7]); ok2 && r2 >= 0xDC00 && r2 < 0xE000 {
						r = 0x10000 + (r-0xD800)<<10 + (r2 - 0xDC00)
						i += 6
					} else {
						r = utf8.RuneError
					}
				} else {
					r = utf8.RuneError
				}
			} else if r >= 0xDC00 && r < 0xE000 {
				r = utf8.RuneError
			}
			out = utf8.AppendRune(out, r)
		default:
			return "", false
		}
	}
	return string(out), true
}

func hex4(b []byte) (rune, bool) {
	var r rune
	for _, c := range b {
		r <<= 4
		switch {
		case c >= '0' && c <= '9':
			r |= rune(c - '0')
		case c >= 'a' && c <= 'f':
			r |= rune(c-'a') + 10
		case c >= 'A' && c <= 'F':
			r |= rune(c-'A') + 10
		default:
			return 0, false
		}
	}
	return r, true
}

// UnescapePrefix decodes a string prefix while recording the raw offset of every decoded byte,
// for "decide on the decoded bytes, resume from the raw bytes" situations (a data: URL header, say).
// An incomplete escape sequence at the end is not decoded; rawOff carries one extra sentinel pointing at the raw offset where
// decoding stopped, so the raw bytes from rawOff[len(dec)] on can be forwarded unchanged.
func UnescapePrefix(b []byte) (dec []byte, rawOff []int) {
	dec = make([]byte, 0, len(b))
	rawOff = make([]int, 0, len(b)+1)
	i := 0
	for i < len(b) {
		c := b[i]
		if c != '\\' {
			dec = append(dec, c)
			rawOff = append(rawOff, i)
			i++
			continue
		}
		if i+1 >= len(b) {
			break // incomplete escape: stop here
		}
		start := i
		var r rune
		consumed := 2
		switch b[i+1] {
		case '"', '\\', '/':
			r = rune(b[i+1])
		case 'b':
			r = '\b'
		case 'f':
			r = '\f'
		case 'n':
			r = '\n'
		case 'r':
			r = '\r'
		case 't':
			r = '\t'
		case 'u':
			if i+6 > len(b) {
				i = len(b) + 1 // marker: incomplete
				break
			}
			v, ok := hex4(b[i+2 : i+6])
			if !ok {
				v = utf8.RuneError
			}
			r = v
			consumed = 6
		default:
			r = utf8.RuneError
		}
		if i > len(b) {
			i = start
			break
		}
		var tmp [4]byte
		n := utf8.EncodeRune(tmp[:], r)
		for k := 0; k < n; k++ {
			dec = append(dec, tmp[k])
			rawOff = append(rawOff, start)
		}
		i = start + consumed
	}
	rawOff = append(rawOff, i) // sentinel
	return dec, rawOff
}

// IsZeroNum reports whether a number literal is zero (0 / 0.0 / 0e0 ...), reproducing omitempty semantics.
func IsZeroNum(s []byte) bool {
	f, err := strconv.ParseFloat(string(s), 64)
	return err == nil && f == 0
}

// IsIntLiteral reports whether the literal is one encoding/json can decode into an int.
func IsIntLiteral(s []byte) bool {
	_, err := strconv.ParseInt(string(s), 10, 64)
	return err == nil
}

func IsNumLiteral(s []byte) bool {
	_, err := strconv.ParseFloat(string(s), 64)
	return err == nil && len(s) > 0 && s[0] != '+' && s[0] != '.'
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

// ---- strict validation of scalar literals (as encoding/json: null/true/false matched exactly, numbers by the JSON grammar) ----

// numState is a state of the number grammar DFA.
type numState uint8

const (
	nsStart   numState = iota // expecting '-' or the first digit
	nsNeg                     // '-' read
	nsZero                    // the integer part is a single 0 (accepting)
	nsInt                     // non-zero integer part (accepting)
	nsDot                     // '.' read
	nsFrac                    // fraction digits (accepting)
	nsExp                     // e/E read
	nsExpSign                 // sign after e read
	nsExpDig                  // exponent digits (accepting)
	nsBad
)

// Table-driven number DFA: byte class × state → new state (nsBad = invalid).
type numClass uint8

const (
	ncOther numClass = iota
	ncZero           // '0'
	ncDigit          // '1'..'9'
	ncDot            // '.'
	ncExp            // 'e' / 'E'
	ncPlus           // '+'
	ncMinus          // '-'
	ncCount
)

var numClassOf = func() (t [256]numClass) {
	t['0'] = ncZero
	for c := '1'; c <= '9'; c++ {
		t[c] = ncDigit
	}
	t['.'] = ncDot
	t['e'], t['E'] = ncExp, ncExp
	t['+'] = ncPlus
	t['-'] = ncMinus
	return
}()

var numTrans = func() (t [nsBad][ncCount]numState) {
	for s := range t {
		for c := range t[s] {
			t[s][c] = nsBad
		}
	}
	t[nsStart][ncMinus] = nsNeg
	t[nsStart][ncZero] = nsZero
	t[nsStart][ncDigit] = nsInt
	t[nsNeg][ncZero] = nsZero
	t[nsNeg][ncDigit] = nsInt
	t[nsZero][ncDot] = nsDot
	t[nsZero][ncExp] = nsExp
	t[nsInt][ncZero], t[nsInt][ncDigit] = nsInt, nsInt
	t[nsInt][ncDot] = nsDot
	t[nsInt][ncExp] = nsExp
	t[nsDot][ncZero], t[nsDot][ncDigit] = nsFrac, nsFrac
	t[nsFrac][ncZero], t[nsFrac][ncDigit] = nsFrac, nsFrac
	t[nsFrac][ncExp] = nsExp
	t[nsExp][ncPlus], t[nsExp][ncMinus] = nsExpSign, nsExpSign
	t[nsExp][ncZero], t[nsExp][ncDigit] = nsExpDig, nsExpDig
	t[nsExpSign][ncZero], t[nsExpSign][ncDigit] = nsExpDig, nsExpDig
	t[nsExpDig][ncZero], t[nsExpDig][ncDigit] = nsExpDig, nsExpDig
	return
}()

func numStep(s numState, c byte) numState {
	if s >= nsBad {
		return nsBad
	}
	return numTrans[s][numClassOf[c]]
}

func numAccept(s numState) bool {
	return s == nsZero || s == nsInt || s == nsFrac || s == nsExpDig
}

// litState tracks a scalar literal being scanned.
type litState struct {
	kind  ValueKind
	first byte     // first byte (tells true from false)
	n     int      // bytes of null/true/false matched so far
	num   numState // number DFA
}

func litKindOf(c byte) (ValueKind, bool) {
	switch {
	case c == 'n':
		return KindNull, true
	case c == 't', c == 'f':
		return KindBool, true
	case c == '-', c >= '0' && c <= '9':
		return KindNumber, true
	}
	return 0, false
}

// start initializes with the first byte; returns false when it is invalid.
func (l *litState) start(c byte) bool {
	k, ok := litKindOf(c)
	if !ok {
		return false
	}
	l.kind, l.first, l.n, l.num = k, c, 0, nsStart
	return l.step(c)
}

func (l *litState) word(c byte) string {
	if l.kind == KindNull {
		return "null"
	}
	if c == 't' {
		return "true"
	}
	return "false"
}

// step feeds one byte; returns false on a grammar violation.
func (l *litState) step(c byte) bool {
	if l.kind == KindNumber {
		l.num = numStep(l.num, c)
		return l.num != nsBad
	}
	if l.n == 0 {
		l.n = 1
		return true
	}
	w := l.word(l.first)
	if l.n >= len(w) || c != w[l.n] {
		return false
	}
	l.n++
	return true
}

// done reports whether the literal is complete if it ends here.
func (l *litState) done() bool {
	if l.kind == KindNumber {
		return numAccept(l.num)
	}
	return l.n == len(l.word(l.first))
}

// vectorStringMin is how far the word-at-a-time loop gets through a string before handing the rest to the vector
// scan. Measured per string length on arm64: the vector scan loses below 128 bytes (-19% at 8, -5% at 64) and wins
// above it (+4% at 128, +10% at 4KB, +32% at 64KB, +129% at 1MB), so the handover is placed at 128 bytes of this
// string -- not of the chunk, which says nothing about where the closing quote is.
const vectorStringMin = 128

// scanStringBody skips the plain bytes of a string starting at p[i:] and returns the index of the first byte that needs
// handling (`"`, `\\` or a control character < 0x20), or len(p) when there is none.
// SWAR over 8 bytes at a time: strings / base64 make up the bulk of large requests, so this is the scanner's hottest path.
func scanStringBody(p []byte, i int) int {
	const lo = 0x0101010101010101
	const hi = 0x8080808080808080
	const qq = lo * '"'
	const bb = lo * '\\'
	const sp = lo * 0x20
	start := i
	for i+8 <= len(p) {
		x := binary.LittleEndian.Uint64(p[i:])
		xq := x ^ qq
		xb := x ^ bb
		m := ((xq - lo) & ^xq & hi) | ((xb - lo) & ^xb & hi) | ((x - sp) & ^x & hi)
		if m != 0 {
			// the borrow only propagates upward, so the lowest set byte is always a real hit
			return i + bits.TrailingZeros64(m)>>3
		}
		i += 8
		if vectorStringScan && i-start >= vectorStringMin {
			// Still going after this many bytes, so this is a long value -- a base64 payload, a long message -- and
			// the vector scan takes over for the rest of it: sixteen bytes a step instead of eight, measured at +32%
			// at 64KB and +129% at a megabyte. The length of the string is not known on entry (only how much of the
			// chunk is left, which says nothing about where the closing quote is), and a body's strings average six
			// bytes, so the word loop above is what decides: a short value never reaches this, and a long one pays
			// these few words once. It returns where it stopped when fewer than sixteen bytes remain.
			i = scanStringBodyVec(p, i)
			break
		}
	}
	for i < len(p) {
		c := p[i]
		if c == '"' || c == '\\' || c < 0x20 {
			return i
		}
		i++
	}
	return i
}

// scanStringBodyUTF8 is scanStringBody that also stops at the first byte >= 0x80 (used when UTF-8 validation is on).
func scanStringBodyUTF8(p []byte, i int) int {
	const lo = 0x0101010101010101
	const hi = 0x8080808080808080
	const qq = lo * '"'
	const bb = lo * '\\'
	const sp = lo * 0x20
	for i+8 <= len(p) {
		x := binary.LittleEndian.Uint64(p[i:])
		xq := x ^ qq
		xb := x ^ bb
		m := ((xq - lo) & ^xq & hi) | ((xb - lo) & ^xb & hi) | ((x - sp) & ^x & hi) | (x & hi)
		if m != 0 {
			return i + bits.TrailingZeros64(m)>>3
		}
		i += 8
	}
	for i < len(p) {
		c := p[i]
		if c == '"' || c == '\\' || c < 0x20 || c >= 0x80 {
			return i
		}
		i++
	}
	return i
}

// utf8First gives, per first byte, the sequence length (low 3 bits, 0 = invalid first byte) and the accepted range of the second byte (high bits, see utf8Accept).
var utf8First = func() (t [256]uint8) {
	for c := 0xC2; c <= 0xDF; c++ {
		t[c] = 2
	}
	t[0xE0] = 3 | 1<<3
	for c := 0xE1; c <= 0xEF; c++ {
		t[c] = 3
	}
	t[0xED] = 3 | 2<<3
	t[0xF0] = 4 | 3<<3
	for c := 0xF1; c <= 0xF3; c++ {
		t[c] = 4
	}
	t[0xF4] = 4 | 4<<3
	return
}()

// utf8Accept is the accepted range of the second byte: 0 = general, 1 = after E0 (no overlong), 2 = after ED (no surrogates),
// 3 = after F0 (no overlong), 4 = after F4 (<= U+10FFFF).
var utf8Accept = [5]struct{ lo, hi byte }{{0x80, 0xBF}, {0xA0, 0xBF}, {0x80, 0x9F}, {0x90, 0xBF}, {0x80, 0x8F}}

// utf8State is the RFC 3629 UTF-8 sequence validation state: need is the number of continuation bytes still expected, lo/hi the accepted range of the next byte.
type utf8State struct {
	need   uint8
	lo, hi byte
}

func (u *utf8State) step(c byte) bool {
	if u.need == 0 {
		switch {
		case c < 0x80:
			return true
		case c >= 0xC2 && c <= 0xDF:
			u.need, u.lo, u.hi = 1, 0x80, 0xBF
		case c == 0xE0:
			u.need, u.lo, u.hi = 2, 0xA0, 0xBF
		case c >= 0xE1 && c <= 0xEC, c == 0xEE, c == 0xEF:
			u.need, u.lo, u.hi = 2, 0x80, 0xBF
		case c == 0xED:
			u.need, u.lo, u.hi = 2, 0x80, 0x9F // no surrogates
		case c == 0xF0:
			u.need, u.lo, u.hi = 3, 0x90, 0xBF
		case c >= 0xF1 && c <= 0xF3:
			u.need, u.lo, u.hi = 3, 0x80, 0xBF
		case c == 0xF4:
			u.need, u.lo, u.hi = 3, 0x80, 0x8F // ≤ U+10FFFF
		default:
			return false // C0 C1 F5..FF and stray continuation bytes
		}
		return true
	}
	if c < u.lo || c > u.hi {
		return false
	}
	u.need--
	u.lo, u.hi = 0x80, 0xBF
	return true
}

// jsonSpace marks the 4 whitespace bytes the JSON grammar allows.
var jsonSpace = [256]bool{' ': true, '\t': true, '\n': true, '\r': true}

func isHexByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// escapeClass classifies the byte after a backslash: 0 invalid, 1 single-character escape, 2 \u (followed by 4 hex digits).
func escapeClass(c byte) uint8 {
	switch c {
	case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		return 1
	case 'u':
		return 2
	}
	return 0
}

func isScalarByte(c byte) bool {
	switch {
	case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		return true
	case c == '-', c == '+', c == '.':
		return true
	}
	return false
}
