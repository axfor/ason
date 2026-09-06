package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func xform(in string, chunk int) string {
	tr := New()
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
	return sb.String()
}

// messages before the top-level scalars: allowed by the JSON spec, clients may serialize in any order
func TestMessagesBeforeScalars(t *testing.T) {
	in := `{"messages":[{"role":"user","content":"U"}],"model":"claude-3","max_tokens":100,"stream":true}`
	got := xform(in, 4096)
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, got)
	}
	if m["model"] != "claude-3" {
		t.Errorf("model lost: %v  (output %s)", m["model"], got)
	}
	if m["max_tokens"] != float64(100) {
		t.Errorf("max_tokens lost: %v", m["max_tokens"])
	}
	if m["stream"] != true {
		t.Errorf("stream lost: %v", m["stream"])
	}
}

// content before role inside a message object
func TestContentBeforeRole(t *testing.T) {
	in := `{"model":"m","messages":[{"content":"S","role":"system"},{"content":"U","role":"user"}]}`
	got := xform(in, 4096)
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, got)
	}
	if m["system"] != "S" {
		t.Errorf("system not extracted: %v  (output %s)", m["system"], got)
	}
	msgs, _ := m["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages count=%d, expected 1 (output %s)", len(msgs), got)
	}
	if msgs[0].(map[string]any)["role"] != "user" {
		t.Errorf("role lost: %v", msgs[0])
	}
}
