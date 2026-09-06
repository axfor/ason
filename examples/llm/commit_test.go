package llm

import (
	"fmt"
	"strings"
	"testing"
)

// Unsupported shape found inside the commit window → the caller can fall back cleanly (no byte released)
func TestBailBeforeCommit(t *testing.T) {
	in := `{"model":"m","messages":[{"role":"user","content":"U","claude_content_blocks":[{"type":"text","text":"x"}]}]}`
	tr := New()
	emitted := 0
	for i := 0; i < len(in); i += 512 {
		j := i + 512
		if j > len(in) {
			j = len(in)
		}
		tr.Write([]byte(in[i:j]))
		emitted += len(tr.Out())
	}
	emitted += len(tr.Finish())
	bad, why := tr.Unsupported()
	fmt.Printf("  small request, unsupported shape: unsupported=%v released=%d bytes past commit=%v\n    reason: %s\n",
		bad, emitted, tr.Committed(), why)
	if !bad {
		t.Fatal("should be unsupported")
	}
	if emitted != 0 {
		t.Errorf("%d bytes released after the bail, the caller cannot fall back cleanly", emitted)
	}
	if tr.Committed() {
		t.Error("a small request should not pass the commit point")
	}
}

// Unsupported shape found after the commit point → bytes already released, the request can only fail
func TestBailAfterCommit(t *testing.T) {
	big := strings.Repeat("y", 200<<10) // 200KB, far beyond CommitBytes
	in := `{"model":"m","messages":[{"role":"user","content":"` + big +
		`"},{"role":"user","content":"x","claude_content_blocks":[{"type":"text","text":"x"}]}]}`
	tr := New()
	emitted := 0
	for i := 0; i < len(in); i += 4096 {
		j := i + 4096
		if j > len(in) {
			j = len(in)
		}
		tr.Write([]byte(in[i:j]))
		emitted += len(tr.Out())
	}
	emitted += len(tr.Finish())
	bad, why := tr.Unsupported()
	fmt.Printf("  large request, unsupported shape at the end: unsupported=%v released=%d bytes past commit=%v\n    reason: %s\n",
		bad, emitted, tr.Committed(), why)
	if !bad {
		t.Fatal("should be unsupported")
	}
	if !tr.Committed() {
		t.Error("a 200KB input should have passed the commit point")
	}
	if emitted == 0 {
		t.Error("bytes should have been released after the commit point")
	}
}
