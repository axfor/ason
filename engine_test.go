package ason

import (
	"encoding/json"
	"strings"
	"testing"
)

// A minimal protocol that exercises the framework mechanics:
// keep → Pass; drop → Skip; ren → rename; wrap → wrap; cap → Capture then rewrite;
// late → Defer until sig arrives; box → Enter (Lazy); img → Prefix.
type probeProto struct {
	sigSeen bool
	tail    string
}

func (p *probeProto) OnKey(t *Transformer) Action {
	if t.Depth() == 1 {
		switch t.Last() {
		case "drop":
			return Skip()
		case "ren":
			return Pass().As("renamed")
		case "wrap":
			return Pass().Wrap([]byte(`{"v":`), []byte(`}`))
		case "cap":
			return Capture(64)
		case "late":
			if !p.sigSeen {
				return Defer(1 << 10)
			}
			return Pass().As("late_after_sig")
		case "sig":
			return Capture(16)
		case "box":
			return Enter().Lazy()
		case "arr":
			return Enter()
		case "img":
			return Prefix(8)
		case "inner":
			return Pass().Inner().Wrap([]byte(`"<`), []byte(`>"`))
		}
		return Pass()
	}
	if t.Depth() == 2 && t.Key(0) == "box" {
		return Skip() // nothing is kept inside box → Lazy should make it vanish entirely
	}
	return Pass()
}
func (p *probeProto) OnElem(t *Transformer) Action               { return Pass() }
func (p *probeProto) OnStart(t *Transformer, k ValueKind) Action { return Pass() }
func (p *probeProto) OnValue(t *Transformer, raw []byte) {
	switch t.Last() {
	case "cap":
		t.W().Key("cap_x2")
		t.W().Byte('[')
		t.W().Raw(raw)
		t.W().Byte(',')
		t.W().Raw(raw)
		t.W().Byte(']')
	case "sig":
		p.sigSeen = true
		t.Release()
	}
}
func (p *probeProto) OnPrefix(t *Transformer, raw []byte, complete bool) (Action, int) {
	// the first 3 bytes are a "protocol header", dropped; the rest streams out unchanged
	if len(raw) < 3 {
		return Bail("too short"), 0
	}
	t.W().Key("img_body")
	return Pass().Wrap([]byte(`"`), []byte(`"`)), 3
}
func (p *probeProto) OnLeave(t *Transformer) {}
func (p *probeProto) Tail(t *Transformer) {
	if p.tail != "" {
		t.W().Key("tail")
		t.W().JSONString(p.tail)
	}
}

func runProbe(t *testing.T, in string, chunk int, tail string) (map[string]any, string) {
	tr := NewTransformer(&probeProto{tail: tail})
	var sb strings.Builder
	for i := 0; i < len(in); i += chunk {
		j := i + chunk
		if j > len(in) {
			j = len(in)
		}
		tr.Write([]byte(in[i:j]))
		sb.Write(tr.Out())
	}
	sb.Write(tr.Finish())
	if bad, why := tr.Unsupported(); bad {
		return nil, why
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(sb.String()), &m); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, sb.String())
	}
	return m, sb.String()
}

func TestEngineActions(t *testing.T) {
	in := `{"keep":{"a":[1,"x",{"b":null}]},"drop":[1,2,{"z":3}],"ren":"r","wrap":[1,2],"cap":"c",` +
		`"late":{"deep":[true]},"sig":1,"box":{"k":"v"},"arr":[],"img":"HDRpayload","inner":"a\"b","n":-1.5e3}`
	for _, cs := range []int{1, 2, 3, 7, 4096} {
		m, out := runProbe(t, in, cs, "T")
		if m == nil {
			t.Fatalf("chunk=%d unexpected fallback: %s", cs, out)
		}
		want := map[string]string{
			"keep":           `{"a":[1,"x",{"b":null}]}`,
			"renamed":        `"r"`,
			"wrap":           `{"v":[1,2]}`,
			"cap_x2":         `["c","c"]`,
			"late_after_sig": `{"deep":[true]}`,
			"arr":            `[]`,
			"img_body":       `"payload"`,
			"inner":          `"<a\"b>"`,
			"n":              `-1.5e3`,
			"tail":           `"T"`,
		}
		_ = want
		if _, has := m["drop"]; has {
			t.Errorf("chunk=%d drop should be dropped", cs)
		}
		if _, has := m["box"]; has {
			t.Errorf("chunk=%d the empty Lazy container should vanish entirely", cs)
		}
		if _, has := m["sig"]; has {
			t.Errorf("chunk=%d a Captured value must not be written directly", cs)
		}
		if m["renamed"] != "r" || m["img_body"] != "payload" || m["inner"] != `<a"b>` || m["tail"] != "T" {
			t.Errorf("chunk=%d wrong output: %s", cs, out)
		}
		if a, _ := m["arr"].([]any); a == nil {
			t.Errorf("chunk=%d a non-Lazy empty container should materialize as []: %s", cs, out)
		}
		late, _ := m["late_after_sig"].(map[string]any)
		if late == nil {
			t.Errorf("chunk=%d the Deferred value should be replayed after sig: %s", cs, out)
		}
		// order: late is replayed after sig, so it comes after renamed/wrap
		if strings.Index(out, `"late_after_sig"`) < strings.Index(out, `"wrap"`) {
			t.Errorf("chunk=%d wrong replay order: %s", cs, out)
		}
	}
}

func TestEngineSyntaxBails(t *testing.T) {
	// Every syntax error is detected, inside dispatch frames as well as inside Pass regions.
	for _, in := range []string{`[1]`, `{"a":1,}`, `{"a" 1}`, `{"a":1}x`, `{"a":{"b":1}`, `{,"a":1}`, `{"a":1 "b":2}`, `{"a":}`} {
		tr := NewTransformer(&probeProto{})
		tr.Write([]byte(in))
		tr.Finish()
		if bad, _ := tr.Unsupported(); !bad {
			t.Errorf("%q should Bail", in)
		}
	}
}

func TestDeferOverflowBails(t *testing.T) {
	in := `{"late":"` + strings.Repeat("x", 2048) + `","sig":1}`
	tr := NewTransformer(&probeProto{})
	tr.Write([]byte(in))
	tr.Finish()
	if bad, why := tr.Unsupported(); !bad || tr.Err().Code != ErrLimit {
		t.Errorf("a Defer above its cap should Bail: %v %s", bad, why)
	}
}

type releaseOnEnterProto struct{ probeProto }

func (p *releaseOnEnterProto) OnKey(t *Transformer) Action {
	if t.Depth() == 1 && t.Last() == "go" {
		p.sigSeen = true
		t.Release()
		return Enter()
	}
	return p.probeProto.OnKey(t)
}

func TestReleaseFromEnteringCallback(t *testing.T) {
	in := `{"late":1,"go":{"x":1},"keep":2}`
	tr := NewTransformer(&releaseOnEnterProto{})
	tr.Write([]byte(in))
	out := string(tr.Finish())
	if bad, why := tr.Unsupported(); bad {
		t.Fatalf("unexpected fallback: %s", why)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("invalid JSON: %v %s", err, out)
	}
	if m["late_after_sig"] != float64(1) || m["keep"] != float64(2) {
		t.Errorf("the Defer items should be replayed once this frame is back: %s", out)
	}
}

// A protocol that neither replays nor drops its Defer items: the engine must Bail rather than swallow them silently.
type forgetfulProto struct{ probeProto }

func (p *forgetfulProto) OnValue(t *Transformer, raw []byte) {} // no Release any more

func TestLeftoverDeferredBails(t *testing.T) {
	tr := NewTransformer(&forgetfulProto{})
	tr.Write([]byte(`{"late":{"important":true},"sig":1,"keep":2}`))
	tr.Finish()
	if bad, why := tr.Unsupported(); !bad || tr.Err().Code != ErrLeftoverDefer {
		t.Errorf("leftover Defer items should Bail: %v %s", bad, why)
	}
}

// The separator added when replaying a scalar must not leak as whitespace before the closing bracket.
func TestReplayScalarNoWhitespaceLeak(t *testing.T) {
	tr := NewTransformer(&probeProto{})
	tr.Write([]byte(`{"late":1,"sig":1}`))
	out := string(tr.Finish())
	if out != `{"late_after_sig":1}` {
		t.Errorf("extra whitespace after the replay: %q", out)
	}
}

// After a buffer-overflow Bail, truncated data must not reach the protocol callbacks.
type panicOnValueProto struct{ probeProto }

func (p *panicOnValueProto) OnValue(t *Transformer, raw []byte) {
	if t.Last() == "cap" && len(raw) < 100 {
		panic("the protocol received a truncated buffer")
	}
}

func TestNoCallbackAfterBail(t *testing.T) {
	tr := NewTransformer(&panicOnValueProto{})
	tr.Write([]byte(`{"cap":"` + strings.Repeat("x", 100) + `"}`))
	tr.Finish()
	if bad, _ := tr.Unsupported(); !bad {
		t.Error("exceeding the Capture cap should Bail")
	}
}
