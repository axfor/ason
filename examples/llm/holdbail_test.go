package llm

import (
	"strings"
	"testing"
)

// Very long content with a late role: must Bail, never guess user
func TestHoldOverflowBails(t *testing.T) {
	big := strings.Repeat("s", roleWaitCap+1024)
	in := `{"model":"m","messages":[{"content":"` + big + `","role":"system"}]}`
	tr := New()
	tr.Write([]byte(in))
	tr.Finish()
	bad, why := tr.Unsupported()
	if !bad {
		t.Fatal("should Bail when role is still unseen past roleWaitCap, but passed: system would be mistaken for user")
	}
	if !strings.Contains(why, "content") {
		t.Errorf("the Bail reason should point at content: %s", why)
	}
}
