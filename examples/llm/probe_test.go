package llm

import (
	"encoding/json"
	"fmt"
	"testing"
)

// probe only reports behaviour, no assertions: first see which shapes break
func probe(t *testing.T, name, in string) {
	t.Helper()
	got := xform(in, 4096)
	var m map[string]any
	err := json.Unmarshal([]byte(got), &m)
	status := "valid JSON"
	if err != nil {
		status = "invalid JSON: " + err.Error()
	}
	fmt.Printf("  %-22s %s\n    -> %s\n", name, status, got)
}

func TestProbeShapes(t *testing.T) {
	probe(t, "content is an array (multimodal)",
		`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	probe(t, "empty messages",
		`{"model":"m","messages":[]}`)
	probe(t, "message with tool_calls",
		`{"model":"m","messages":[{"role":"assistant","content":"x","tool_calls":[{"id":"c1","function":{"name":"f","arguments":"{}"}}]}]}`)
	probe(t, "unknown top-level object field",
		`{"model":"m","metadata":{"k":"v"},"messages":[{"role":"user","content":"U"}]}`)
	probe(t, "top-level tools array",
		`{"model":"m","tools":[{"type":"function","function":{"name":"f","description":"d"}}],"messages":[{"role":"user","content":"U"}]}`)
	probe(t, "null content",
		`{"model":"m","messages":[{"role":"assistant","content":null,"tool_calls":[]}]}`)
	probe(t, "several user messages",
		`{"model":"m","messages":[{"role":"user","content":"A"},{"role":"assistant","content":"B"},{"role":"user","content":"C"}]}`)
	probe(t, "no messages field",
		`{"model":"m","max_tokens":10}`)
	probe(t, "escape inside a key",
		`{"model":"m","messages":[{"role":"user","content":"a\"b"}]}`)
}
