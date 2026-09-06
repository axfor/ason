package ason

import (
	"strings"
	"testing"
)

type captureProto struct{ BaseProtocol }

func (captureProto) OnKey(t *Transformer) Action {
	if t.Depth() == 1 && t.Last() == "big" {
		return Capture(0) // 单项不限
	}
	return Pass()
}
func (captureProto) OnValue(t *Transformer, raw []byte) { t.W().KeyRaw(t.KeyRaw()); t.W().Raw(raw) }

// 总预算：单项 cap 不限时，预算仍能兜住。
func TestBudgetCapsCapture(t *testing.T) {
	in := `{"a":1,"big":"` + strings.Repeat("y", 200<<10) + `"}`
	tr := NewTransformer(captureProto{})
	tr.SetBudget(64 << 10)
	_, ok, why := feedAll(tr, in, 4096)
	if ok || tr.Err().Code != ErrLimit {
		t.Fatalf("应因预算超限判定不支持: ok=%v why=%s", ok, why)
	}
	// 预算够时正常
	tr = NewTransformer(captureProto{})
	tr.SetBudget(1 << 20)
	out, ok, why := feedAll(tr, in, 4096)
	if !ok || out != in {
		t.Fatalf("预算充足应正常: ok=%v why=%s", ok, why)
	}
}

// 预算也覆盖 Defer 暂存与提交前攒着的输出。
func TestBudgetCoversDeferAndPreCommit(t *testing.T) {
	in := `{"content":"` + strings.Repeat("y", 100<<10) + `","role":"user"}`
	tr := NewTransformer(&roleProtoForBudget{})
	tr.SetBudget(32 << 10)
	if _, ok, why := feedAll(tr, in, 4096); ok || tr.Err().Code != ErrLimit {
		t.Fatalf("Defer 暂存超预算应判定不支持: ok=%v why=%s", ok, why)
	}
	// 透传但提交前的输出也计入预算：窗口 64KB、预算 16KB → 越过窗口前攒的输出超限
	tr = NewTransformer(BaseProtocol{})
	tr.SetBudget(16 << 10)
	if _, ok, why := feedAll(tr, in, 4096); ok || tr.Err().Code != ErrLimit {
		t.Fatalf("提交前输出超预算应判定不支持: ok=%v why=%s", ok, why)
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

// Buffered 反映当前持有量，回放/交付后归零；提交窗口可按转换器设置。
func TestBufferedAndCommitWindow(t *testing.T) {
	tr := NewTransformer(BaseProtocol{})
	tr.SetCommitBytes(1024)
	in := `{"a":"` + strings.Repeat("y", 4000) + `"}`
	tr.Write([]byte(in[:512]))
	if tr.Committed() || len(tr.Out()) != 0 || tr.Buffered() == 0 {
		t.Fatalf("512B 时不该提交，且提交前输出应计入 Buffered: committed=%v buffered=%d", tr.Committed(), tr.Buffered())
	}
	tr.Write([]byte(in[512:2048]))
	if !tr.Committed() || len(tr.Out()) == 0 {
		t.Fatal("越过 1KB 窗口后应提交并有输出")
	}
	if tr.Buffered() != 0 {
		t.Fatalf("提交后透传不持有任何缓冲: %d", tr.Buffered())
	}
	tr.Write([]byte(in[2048:]))
	tr.Out()
	tr.Finish()
	if bad, why := tr.Unsupported(); bad {
		t.Fatal(why)
	}
}
