package llm

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Enumerates several orderings of the top-level fields and the fields inside a message, checking the outputs are equivalent
func TestFieldOrderPermutations(t *testing.T) {
	cases := []struct{ name, in string }{
		{"canonical order", `{"model":"m","max_tokens":50,"stream":true,"messages":[{"role":"system","content":"S"},{"role":"user","content":"U"}]}`},
		{"messages first", `{"messages":[{"role":"system","content":"S"},{"role":"user","content":"U"}],"model":"m","max_tokens":50,"stream":true}`},
		{"messages in the middle", `{"model":"m","messages":[{"role":"system","content":"S"},{"role":"user","content":"U"}],"max_tokens":50,"stream":true}`},
		{"content before role", `{"model":"m","max_tokens":50,"stream":true,"messages":[{"content":"S","role":"system"},{"content":"U","role":"user"}]}`},
		{"both shuffled", `{"messages":[{"content":"S","role":"system"},{"content":"U","role":"user"}],"stream":true,"model":"m","max_tokens":50}`},
	}
	for _, c := range cases {
		for _, cs := range []int{1, 7, 4096} {
			got := xform(c.in, cs)
			var m map[string]any
			if err := json.Unmarshal([]byte(got), &m); err != nil {
				t.Fatalf("%s chunk=%d invalid JSON: %v\n%s", c.name, cs, err, got)
			}
			if m["model"] != "m" || m["max_tokens"] != float64(50) || m["stream"] != true {
				t.Errorf("%s chunk=%d wrong top-level fields: %s", c.name, cs, got)
			}
			if m["system"] != "S" {
				t.Errorf("%s chunk=%d system=%v: %s", c.name, cs, m["system"], got)
			}
			msgs, _ := m["messages"].([]any)
			if len(msgs) != 1 {
				t.Errorf("%s chunk=%d messages count=%d: %s", c.name, cs, len(msgs), got)
				continue
			}
			m0 := msgs[0].(map[string]any)
			if m0["role"] != "user" {
				t.Errorf("%s chunk=%d role=%v", c.name, cs, m0["role"])
			}
			txt := m0["content"]
			if txt != "U" {
				t.Errorf("%s chunk=%d text=%v", c.name, cs, txt)
			}
		}
		fmt.Printf("  %-14s ✓  %s\n", c.name, strings.TrimSpace(xform(c.in, 4096)))
	}
}
