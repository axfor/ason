package ason

import (
	"strings"
	"testing"
)

// errAt feeds the input in chunks and returns the error details (nil = passed).
func errAt(tr *Transformer, in string, chunk int) *Error {
	feedAll(tr, in, chunk)
	return tr.Err()
}

// Error codes, offsets and paths: independent of chunking, the offset points at the failing byte.
func TestErrorCodesOffsetsPaths(t *testing.T) {
	cases := []struct {
		name string
		in   string
		mk   func() *Transformer
		code Code
		off  int64
		path string
	}{
		{"literal", `{"a":1,"b":tru}`, base, ErrSyntax, 14, "b"},
		{"trailing comma", `{"a":1,}`, base, ErrSyntax, 7, ""},
		{"data after root", `{"a":1} x`, base, ErrTrailing, 8, ""},
		{"root shape", ` [1]`, base, ErrRoot, 1, ""},
		{"truncated", `{"a":[1,2`, base, ErrIncomplete, 9, "a"}, // the path still points at the unfinished value
		{"truncated literal", `{"a":tru`, base, ErrSyntax, 8, "a"},
		{"bracket inside a region", `{"a":{"x":[1}}`, base, ErrSyntax, 12, "a"},
		{"deep control character", "{\"m\":[{\"c\":\"x\x01\"}]}", enter, ErrSyntax, 13, "m[0]"}, // the element is a Pass region: the path reaches the deepest dispatch point
		{"duplicate key", `{"a":1,"b":{"z":1,"z":2}}`, dupBail, ErrDuplicateKey, 21, "b.z"},
		{"protocol Bail in OnValue", `{"a":1,"model":"bad","z":2}`, valueBail, ErrUnsupported, 20, "model"},
		{"protocol BailCode in OnKey", `{"a":1,"nope":1}`, keyBailCode, ErrMisuse, 13, "nope"},
		{"Bail in Tail", `{"a":1}`, tailBail, ErrUnsupported, 7, ""},
		{"cap", `{"big":"` + strings.Repeat("y", 100) + `"}`, capSmall, ErrLimit, 71, "big"},
		{"UTF-8", "{\"s\":\"ab\xC0\x80\"}", utf8On, ErrSyntax, 8, "s"},
	}
	for _, c := range cases {
		for _, cs := range chunkSizes(len(c.in)) {
			tr := c.mk()
			e := errAt(tr, c.in, cs)
			if e == nil {
				t.Fatalf("%s chunk=%d: should bail", c.name, cs)
			}
			if e.Code != c.code || e.Offset != c.off || e.Path != c.path {
				t.Fatalf("%s chunk=%d: got %s/%d/%q (%s), want %s/%d/%q", c.name, cs, e.Code, e.Offset, e.Path, e.Msg, c.code, c.off, c.path)
			}
			bad, why := tr.Unsupported()
			if !bad || why != e.Error() || !strings.HasPrefix(why, e.Msg) {
				t.Fatalf("%s: Unsupported() text %q differs from Err().Error() %q", c.name, why, e.Error())
			}
		}
	}
}

func base() *Transformer  { return NewTransformer(BaseProtocol{}) }
func enter() *Transformer { return NewTransformer(&dupProto{}) }
func dupBail() *Transformer {
	tr := NewTransformer(&dupProto{})
	tr.SetDupKeys(DupKeysBail)
	return tr
}
func utf8On() *Transformer {
	tr := NewTransformer(BaseProtocol{})
	tr.SetValidateUTF8(true)
	return tr
}
func valueBail() *Transformer {
	return NewKeyProbeTransformer(KeyProbeOptions{Keys: map[string]int{"model": 1024},
		OnKey: func(t *Transformer, k string, raw []byte) ([]byte, bool) {
			if string(raw) == `"bad"` {
				t.Bail("model unavailable")
			}
			return nil, false
		}})
}
func capSmall() *Transformer {
	return NewKeyProbeTransformer(KeyProbeOptions{Keys: map[string]int{"big": 64},
		OnKey: func(t *Transformer, k string, raw []byte) ([]byte, bool) { return nil, false }})
}

type keyBailProto struct{ BaseProtocol }

func (keyBailProto) OnKey(t *Transformer) Action {
	if t.Last() == "nope" {
		return BailCode(ErrMisuse, "nope is not allowed")
	}
	return Pass()
}
func keyBailCode() *Transformer { return NewTransformer(keyBailProto{}) }

type tailBailProto struct{ BaseProtocol }

func (tailBailProto) Tail(t *Transformer) { t.Bail("tail says no") }
func tailBail() *Transformer              { return NewTransformer(tailBailProto{}) }

// Normal end: Err is nil, Unsupported is false with an empty text.
func TestErrNilWhenOK(t *testing.T) {
	tr := base()
	if out, ok, _ := feedAll(tr, `{"a":1}`, 2); !ok || out != `{"a":1}` || tr.Err() != nil {
		t.Fatal("normal input should produce no error")
	}
	if bad, why := tr.Unsupported(); bad || why != "" {
		t.Fatal("Unsupported should be false with an empty text")
	}
}

// The first reason is kept; later Bails do not overwrite it.
func TestFirstErrorWins(t *testing.T) {
	tr := base()
	tr.BailCode(ErrLimit, "first")
	tr.Bail("second")
	if e := tr.Err(); e.Code != ErrLimit || e.Msg != "first" {
		t.Fatalf("got %+v", e)
	}
}

// A bail during a Defer replay: the offset is the outer position that triggered the replay, not a position inside the replay buffer.
func TestErrorOffsetDuringReplay(t *testing.T) {
	in := `{"a":{"x":1},"b":2,"a2":3}`
	for _, cs := range chunkSizes(len(in)) {
		tr := NewTransformer(&replayBailProto{})
		e := errAt(tr, in, cs)
		if e == nil || e.Code != ErrUnsupported || e.Path != "a" {
			t.Fatalf("chunk=%d: %+v", cs, e)
		}
		if e.Offset != int64(len(in)) { // replayed at the root close: the closing bracket is consumed, the offset is the number of bytes consumed
			t.Fatalf("chunk=%d: offset %d", cs, e.Offset)
		}
	}
}

type replayBailProto struct {
	BaseProtocol
	released bool
}

func (p *replayBailProto) OnKey(t *Transformer) Action {
	if t.Depth() == 1 && t.Last() == "a" {
		if p.released {
			return Bail("a rejected on replay")
		}
		return Defer(1 << 10)
	}
	return Pass()
}
func (p *replayBailProto) OnLeave(t *Transformer) {
	if t.Depth() == 0 {
		p.released = true
		t.ReleaseNow()
	}
}

func TestCodeString(t *testing.T) {
	if ErrSyntax.String() != "syntax" || ErrDuplicateKey.String() != "duplicate_key" || Code(99).String() != "code(99)" {
		t.Fatal("Code.String")
	}
	e := &Error{Code: ErrSyntax, Msg: "unexpected comma", Offset: 5}
	if e.Error() != "unexpected comma at byte 5" {
		t.Fatalf("%q", e.Error())
	}
	e.Path = "messages[2].content"
	if e.Error() != "unexpected comma at byte 5 in messages[2].content" {
		t.Fatalf("%q", e.Error())
	}
}
