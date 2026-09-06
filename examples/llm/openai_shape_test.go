package llm

import (
	"encoding/json"
	"testing"
)

func TestOpenAIPassthroughShape(t *testing.T) {
	in := `{"messages":[{"role":"user","content":"U"}],"model":"gpt-4o","stream":true,"x":{"model":"inner"}}`
	tr := NewOpenAI(OpenAIOptions{MapModel: func(m string) string { return "M:" + m }, DetectStream: true, NormalizeUsage: true, CheckMessages: true})
	tr.Write([]byte(in))
	out := string(tr.Finish())
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("invalid JSON: %v %s", err, out)
	}
	if m["model"] != "M:gpt-4o" {
		t.Errorf("model not rewritten: %s", out)
	}
	if so, _ := m["stream_options"].(map[string]any); so == nil || so["include_usage"] != true {
		t.Errorf("stream_options.include_usage not added: %s", out)
	}
	if x, _ := m["x"].(map[string]any); x == nil || x["model"] != "inner" {
		t.Errorf("model inside a nested object must not be changed: %s", out)
	}
	if p, ok := tr.Protocol().(Preluder); !ok || !p.Prelude().Stream || !p.Prelude().StreamSeen {
		t.Errorf("Prelude did not report stream")
	}
	// the integration layer writes the context keys from the original model (the response-side model name and the Azure request path depend on it)
	if pre := tr.Protocol().(Preluder).Prelude(); !pre.ModelSeen || pre.Model != "gpt-4o" {
		t.Errorf("Prelude did not report the original model: %+v", pre)
	}
}

// ---- regression cases from review ----

// Release called from a callback that returns Enter: the replay must happen when this frame is back at a safe point, not be consumed by the child frame.
