package llm

import (
	"fmt"
	"strings"
	"testing"
)

// Large content with a late role: the worst case, held up to the cap before degrading
func TestMemWithLateRole(t *testing.T) {
	for _, mb := range []int{1, 4, 16} {
		big := strings.Repeat("y", mb<<20)
		// role after content forces the bounded hold path
		in := `{"messages":[{"content":"` + big + `","role":"user"}],"model":"m"}`
		tr := New()
		maxHeld, total := 0, 0
		for i := 0; i < len(in); i += 4096 {
			j := i + 4096
			if j > len(in) {
				j = len(in)
			}
			tr.Write([]byte(in[i:j]))
			o := tr.Out()
			total += len(o)
			if len(o) > maxHeld {
				maxHeld = len(o)
			}
		}
		total += len(tr.Finish())
		fmt.Printf("  late role input %2dMB -> output %.2fMB | max held at once %d bytes (cap %d)\n",
			mb, float64(total)/1048576, maxHeld, roleWaitCap)
		if maxHeld > roleWaitCap+8192 {
			t.Errorf("%dMB: held %d above the hold cap, not bounded", mb, maxHeld)
		}
	}
}
