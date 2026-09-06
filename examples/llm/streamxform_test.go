package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// run feeds the input in chunks of the given size, simulating arbitrary TCP segmentation
func run(t *testing.T, in string, chunk int) string {
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

const basic = `{"model":"claude-3","max_tokens":100,"stream":true,"messages":[{"role":"system","content":"You are an assistant"},{"role":"user","content":"Hello"}]}`

func TestBasic(t *testing.T) {
	for _, cs := range []int{1, 3, 7, 16, 64, 4096} {
		got := run(t, basic, cs)
		var m map[string]any
		if err := json.Unmarshal([]byte(got), &m); err != nil {
			t.Fatalf("chunk=%d output is not valid JSON: %v\n%s", cs, err, got)
		}
		if m["model"] != "claude-3" {
			t.Errorf("chunk=%d model=%v", cs, m["model"])
		}
		if m["system"] != "You are an assistant" {
			t.Errorf("chunk=%d system=%v", cs, m["system"])
		}
		msgs, _ := m["messages"].([]any)
		if len(msgs) != 1 {
			t.Fatalf("chunk=%d messages count=%d, expected 1 (system extracted)", cs, len(msgs))
		}
		m0 := msgs[0].(map[string]any)
		if m0["role"] != "user" {
			t.Errorf("chunk=%d role=%v", cs, m0["role"])
		}
		if m0["content"] != "Hello" {
			t.Errorf("chunk=%d content=%v", cs, m0["content"])
		}
	}
}

// Escape sequences split across chunks must not break
func TestEscapeSplit(t *testing.T) {
	in := `{"model":"m","messages":[{"role":"user","content":"quote\" backslash\\ newline\n and ä é"}]}`
	for _, cs := range []int{1, 2, 5, 13} {
		got := run(t, in, cs)
		var m map[string]any
		if err := json.Unmarshal([]byte(got), &m); err != nil {
			t.Fatalf("chunk=%d invalid JSON: %v\n%s", cs, err, got)
		}
		msgs := m["messages"].([]any)
		want := "quote\" backslash\\ newline\n and ä é"
		if got := msgs[0].(map[string]any)["content"]; got != want {
			t.Errorf("chunk=%d content=%q want=%q", cs, got, want)
		}
	}
}

// Large content: memory must not grow with its length (output is taken as it is produced)
func TestLargeContentStreams(t *testing.T) {
	big := strings.Repeat("y", 1<<20)
	in := `{"model":"m","messages":[{"role":"user","content":"` + big + `"}]}`
	tr := New()
	maxHeld := 0
	var total int
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
	// With a commit point, CommitBytes accumulate before the first release: the price of the fallback window.
	// What matters is that it is bounded and does not grow with the content length.
	if maxHeld > CommitBytes+16*1024 {
		t.Errorf("held %d bytes at once, above the commit point bound (not streaming)", maxHeld)
	}
	if total < 1<<20 {
		t.Errorf("total output %d is smaller than the input content", total)
	}
}
