package ason

import (
	"strings"
	"testing"
)

type captureProto struct{ BaseProtocol }

func (captureProto) OnKey(t *Transformer) Action {
	if t.Depth() == 1 && t.Last() == "big" {
		return Capture(0) // no per-item cap
	}
	return Pass()
}
func (captureProto) OnValue(t *Transformer, raw []byte) { t.W().KeyRaw(t.KeyRaw()); t.W().Raw(raw) }

// Total budget: the budget still holds when the per-item cap is unlimited.
func TestBudgetCapsCapture(t *testing.T) {
	in := `{"a":1,"big":"` + strings.Repeat("y", 200<<10) + `"}`
	tr := NewTransformer(captureProto{})
	tr.SetBudget(64 << 10)
	_, ok, why := feedAll(tr, in, 4096)
	if ok || tr.Err().Code != ErrLimit {
		t.Fatalf("should bail on budget overflow: ok=%v why=%s", ok, why)
	}
	// fine when the budget is large enough
	tr = NewTransformer(captureProto{})
	tr.SetBudget(1 << 20)
	out, ok, why := feedAll(tr, in, 4096)
	if !ok || out != in {
		t.Fatalf("should pass with a sufficient budget: ok=%v why=%s", ok, why)
	}
}

// The budget also covers Defer holds and the output kept before the commit point.
func TestBudgetCoversDeferAndPreCommit(t *testing.T) {
	in := `{"content":"` + strings.Repeat("y", 100<<10) + `","role":"user"}`
	tr := NewTransformer(&roleProtoForBudget{})
	tr.SetBudget(32 << 10)
	if _, ok, why := feedAll(tr, in, 4096); ok || tr.Err().Code != ErrLimit {
		t.Fatalf("a Defer hold over budget should bail: ok=%v why=%s", ok, why)
	}
	// passthrough, but the pre-commit output counts against the budget too: 64KB window, 16KB budget → the output kept before the window overflows
	tr = NewTransformer(BaseProtocol{})
	tr.SetBudget(16 << 10)
	if _, ok, why := feedAll(tr, in, 4096); ok || tr.Err().Code != ErrLimit {
		t.Fatalf("pre-commit output over budget should bail: ok=%v why=%s", ok, why)
	}
}

type roleProtoForBudget struct {
	BaseProtocol
	role string
}

func (p *roleProtoForBudget) OnKey(t *Transformer) Action {
	switch t.Last() {
	case "role":
		return Capture(64)
	case "content":
		if p.role == "" {
			return Defer(0)
		}
		return Pass()
	}
	return Pass()
}
func (p *roleProtoForBudget) OnValue(t *Transformer, raw []byte) {
	p.role, _ = JSONUnquote(raw)
	t.W().KeyRaw(t.KeyRaw())
	t.W().Raw(raw)
	t.Release()
}

// Buffered reflects the current hold and returns to zero after replay / delivery; the commit window is per transformer.
func TestBufferedAndCommitWindow(t *testing.T) {
	tr := NewTransformer(BaseProtocol{})
	tr.SetCommitBytes(1024)
	in := `{"a":"` + strings.Repeat("y", 4000) + `"}`
	tr.Write([]byte(in[:512]))
	if tr.Committed() || len(tr.Out()) != 0 || tr.Buffered() == 0 {
		t.Fatalf("no commit expected at 512B, and the pre-commit output should count in Buffered: committed=%v buffered=%d", tr.Committed(), tr.Buffered())
	}
	tr.Write([]byte(in[512:2048]))
	if !tr.Committed() || len(tr.Out()) == 0 {
		t.Fatal("should commit with output once past the 1KB window")
	}
	if tr.Buffered() != 0 {
		t.Fatalf("passthrough holds no buffer after the commit: %d", tr.Buffered())
	}
	tr.Write([]byte(in[2048:]))
	tr.Out()
	tr.Finish()
	if bad, why := tr.Unsupported(); bad {
		t.Fatal(why)
	}
}
