package ason

// validateAgainst checks a captured JSON value against a FieldTree.
//
// It answers one question only: does any value inside have a type this tree says it cannot have. Everything
// else -- an unknown key, a shape the tree says nothing about, a document it cannot walk -- is accepted. The
// asymmetry is deliberate: rejecting a document the caller's own unmarshal would have taken turns a passing
// request into a failing one, while accepting one it would have rejected only leaves the check where it
// already was.
//
// The bytes were produced by the scanner, so they are well-formed JSON; the walker still returns true on
// anything it does not expect rather than guessing.
func validateAgainst(b []byte, t *FieldTree) bool {
	v := validator{b: b}
	ok := v.value(t)
	return ok
}

type validator struct {
	b []byte
	i int
}

func (v *validator) ws() {
	for v.i < len(v.b) {
		switch v.b[v.i] {
		case ' ', '\t', '\n', '\r':
			v.i++
		default:
			return
		}
	}
}

// value checks the value starting at the cursor and leaves the cursor just past it.
func (v *validator) value(t *FieldTree) bool {
	v.ws()
	if v.i >= len(v.b) {
		return true
	}
	c := v.b[v.i]
	var kind ValueKind
	switch {
	case c == '{':
		kind = KindObject
	case c == '[':
		kind = KindArray
	case c == '"':
		kind = KindString
	case c == 't' || c == 'f':
		kind = KindBool
	case c == 'n':
		kind = KindNull
	default:
		kind = KindNumber
	}
	if t != nil && !t.Any && kind != KindNull && t.Types&typeBit(kind) == 0 {
		return false
	}
	switch kind {
	case KindObject:
		return v.object(t)
	case KindArray:
		return v.array(t)
	default:
		v.scalar()
		return true
	}
}

func (v *validator) object(t *FieldTree) bool {
	v.i++ // '{'
	for {
		v.ws()
		if v.i >= len(v.b) {
			return true
		}
		if v.b[v.i] == '}' {
			v.i++
			return true
		}
		if v.b[v.i] == ',' {
			v.i++
			continue
		}
		if v.b[v.i] != '"' {
			return true // not a shape this walker knows: accept
		}
		key, ok := v.key()
		if !ok {
			return true
		}
		v.ws()
		if v.i < len(v.b) && v.b[v.i] == ':' {
			v.i++
		}
		var sub *FieldTree
		if t != nil && !t.Any {
			if t.Keys != nil {
				// Indexing with string(bytes) directly keeps the key out of the heap: the compiler
				// looks it up without materialising the string. Binding it to a variable first would
				// allocate once per key, which on a container of many keys is most of the cost.
				sub = t.Keys[string(key)] // an unknown key leaves sub nil: nothing is claimed about it
			} else {
				sub = t.Elem // a map: every value takes the same subtree
			}
		}
		if !v.value(sub) {
			return false
		}
	}
}

func (v *validator) array(t *FieldTree) bool {
	v.i++ // '['
	var sub *FieldTree
	if t != nil && !t.Any {
		sub = t.Elem
	}
	for {
		v.ws()
		if v.i >= len(v.b) {
			return true
		}
		if v.b[v.i] == ']' {
			v.i++
			return true
		}
		if v.b[v.i] == ',' {
			v.i++
			continue
		}
		if !v.value(sub) {
			return false
		}
	}
}

// key reads a quoted key and returns the raw bytes between the quotes. They are returned as bytes, not a
// string, so the caller can index the map with string(...) directly and keep the key off the heap. A key with
// escapes is returned as-is: a field name that needs escaping is not a name any struct tag carries, so the
// lookup misses and the value is accepted.
func (v *validator) key() ([]byte, bool) {
	start := v.i + 1
	v.i++
	for v.i < len(v.b) {
		switch v.b[v.i] {
		case '\\':
			v.i += 2
			continue
		case '"':
			k := v.b[start:v.i]
			v.i++
			return k, true
		}
		v.i++
	}
	return nil, false
}

// scalar walks past a string, number, or literal.
func (v *validator) scalar() {
	if v.i < len(v.b) && v.b[v.i] == '"' {
		v.i++
		for v.i < len(v.b) {
			if v.b[v.i] == '\\' {
				v.i += 2
				continue
			}
			if v.b[v.i] == '"' {
				v.i++
				return
			}
			v.i++
		}
		return
	}
	for v.i < len(v.b) {
		switch v.b[v.i] {
		case ',', '}', ']', ' ', '\t', '\n', '\r':
			return
		}
		v.i++
	}
}
