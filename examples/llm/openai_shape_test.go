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
		t.Fatalf("非法 JSON: %v %s", err, out)
	}
	if m["model"] != "M:gpt-4o" {
		t.Errorf("model 未改写: %s", out)
	}
	if so, _ := m["stream_options"].(map[string]any); so == nil || so["include_usage"] != true {
		t.Errorf("stream_options.include_usage 未补: %s", out)
	}
	if x, _ := m["x"].(map[string]any); x == nil || x["model"] != "inner" {
		t.Errorf("嵌套对象里的 model 不应被改: %s", out)
	}
	if p, ok := tr.Protocol().(Preluder); !ok || !p.Prelude().Stream || !p.Prelude().StreamSeen {
		t.Errorf("Prelude 未报告 stream")
	}
	// 集成层要用原始 model 写上下文键（响应侧的模型名、Azure 的请求路径都靠它）
	if pre := tr.Protocol().(Preluder).Prelude(); !pre.ModelSeen || pre.Model != "gpt-4o" {
		t.Errorf("Prelude 未报告原始 model: %+v", pre)
	}
}

// ---- 评审发现的回归用例 ----

// Release 在返回 Enter 的回调里被调用：回放必须发生在本帧回到安全点时，不能被子帧消费掉。
