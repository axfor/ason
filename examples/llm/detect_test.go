package llm

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func detect(in string) (bool, string, string) {
	tr := New()
	var out []byte
	for i := 0; i < len(in); i += 4096 {
		j := i + 4096
		if j > len(in) {
			j = len(in)
		}
		tr.Write([]byte(in[i:j]))
		out = append(out, tr.Out()...)
	}
	out = append(out, tr.Finish()...)
	bad, why := tr.Unsupported()
	return bad, why, string(out)
}

// Which shapes must Bail and which must pass. Equivalence of the passing output with the buffered path is the differential tests' job; here only valid JSON is checked.
func TestUnsupportedDetection(t *testing.T) {
	mustBail := []struct{ name, in string }{
		{"claude_content_blocks", `{"model":"m","messages":[{"role":"user","content":"U","claude_content_blocks":[]}]}`},
		{"duplicate key", `{"model":"m","model":"n","messages":[{"role":"user","content":"U"}]}`},
		{"empty messages", `{"model":"m","messages":[]}`},
		{"no messages", `{"model":"m"}`},
		{"no model", `{"messages":[{"role":"user","content":"U"}]}`},
		{"non-string role", `{"model":"m","messages":[{"role":1,"content":"U"}]}`},
		{"non-integer max_tokens", `{"model":"m","max_tokens":1.5,"messages":[{"role":"user","content":"U"}]}`},
		{"stop is a string", `{"model":"m","stop":"END","messages":[{"role":"user","content":"U"}]}`},
		{"root is not an object", `[1,2]`},
		{"truncated", `{"model":"m","messages":[{"role":"user","content":"U"}`},
	}
	for _, c := range mustBail {
		bad, why, _ := detect(c.in)
		if !bad {
			t.Errorf("%s: should be unsupported, but passed", c.name)
		} else {
			fmt.Printf("  ✓ %-22s rejected: %s\n", c.name, why)
		}
	}

	mustPass := []struct{ name, in string }{
		{"plain text chat", `{"model":"m","messages":[{"role":"system","content":"S"},{"role":"user","content":"U"}]}`},
		{"several messages", `{"model":"m","messages":[{"role":"user","content":"A"},{"role":"assistant","content":"B"}]}`},
		{"fields out of order", `{"messages":[{"content":"U","role":"user"}],"model":"m"}`},
		{"large content", `{"model":"m","messages":[{"role":"user","content":"` + strings.Repeat("y", 100000) + `"}]}`},
		{"stop array", `{"model":"m","stop":["END"],"messages":[{"role":"user","content":"U"}]}`},
		{"no Claude counterpart", `{"model":"m","user":"u1","seed":42,"n":1,"messages":[{"role":"user","content":"U"}]}`},
		{"nested object fields", `{"model":"m","metadata":{"k":"v"},"response_format":{"type":"json_object"},"messages":[{"role":"user","content":"U"}]}`},
		{"multimodal content array", `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA","detail":"high"}}]}]}`},
		{"message tool_calls", `{"model":"m","messages":[{"role":"assistant","content":"x","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{\"a\":1}"}}]}]}`},
		{"tool role merging", `{"model":"m","messages":[{"role":"tool","tool_call_id":"c1","content":"r1"},{"role":"tool","tool_call_id":"c2","content":"r2"},{"role":"user","content":"U"}]}`},
		{"top-level tools", `{"model":"m","tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}],"tool_choice":"auto","messages":[{"role":"user","content":"U"}]}`},
		{"unknown fields", `{"model":"m","totally_unknown_x":"v","messages":[{"role":"user","content":"U","name":"n1"}]}`},
		{"null content", `{"model":"m","messages":[{"role":"assistant","content":null}]}`},
		{"developer role", `{"model":"m","messages":[{"role":"developer","content":"D"},{"role":"user","content":"U"}]}`},
		{"reasoning", `{"model":"m","reasoning_effort":"high","messages":[{"role":"user","content":"U"}]}`},
	}
	for _, c := range mustPass {
		bad, why, out := detect(c.in)
		if bad {
			t.Errorf("%s: should pass, but was unsupported: %s", c.name, why)
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(out), &m); err != nil {
			t.Errorf("%s: output is not valid JSON: %v\n%s", c.name, err, out)
			continue
		}
		fmt.Printf("  ✓ %-22s streamed\n", c.name)
	}
}
