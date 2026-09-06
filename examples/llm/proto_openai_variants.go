package llm

import (
	"encoding/json"
	"strconv"
)

// ---- Qwen compatible mode (qwenEnableCompatible, on by default) ----
//
// Buffered TransformRequestBodyHeaders, compatible branch: rewrite only when model is present; when the mapped model is
// non-empty, some message has a non-empty reasoning_content and the model supports it, add preserve_thinking:true.
// Accept / isStreaming are not set (this buffered branch does not call defaultTransformRequestBody).

type QwenVariant struct {
	// SupportsPreserveThinking reproduces qwenSupportsPreserveThinking.
	SupportsPreserveThinking func(model string) bool
	ptRaw                    []byte
	ptSeen                   bool
}

func (v *QwenVariant) TopKey(t *Transformer, key string) (Action, bool) {
	if key == "preserve_thinking" && !v.ptSeen {
		return Capture(64), true
	}
	return Action{}, false
}

func (v *QwenVariant) TopValue(t *Transformer, key string, raw []byte) {
	if key == "preserve_thinking" {
		v.ptSeen = true
		v.ptRaw = append([]byte(nil), raw...)
	}
}

func (v *QwenVariant) NeedReasoningScan() bool { return true }

func (v *QwenVariant) Tail(t *Transformer, st *OpenAIState) {
	w := t.W()
	if st.ModelSeen && st.Mapped != "" && st.ReasoningSeen && v.SupportsPreserveThinking != nil && v.SupportsPreserveThinking(st.Mapped) {
		w.Key("preserve_thinking")
		w.RawString("true")
		return
	}
	if v.ptSeen {
		w.Key("preserve_thinking")
		w.Raw(v.ptRaw)
	}
}

// ---- Zhipu (chat completion) ----
//
// Buffered path: a non-empty reasoning_effort → thinking is replaced as a whole by {"type":"enabled"} and reasoning_effort deleted;
// any message with a non-empty reasoning_content → thinking.clear_thinking = false.

type ZhipuVariant struct {
	effortRaw  []byte
	effortSeen bool
	thinkRaw   []byte
	thinkSeen  bool
}

func (v *ZhipuVariant) TopKey(t *Transformer, key string) (Action, bool) {
	switch key {
	case "reasoning_effort":
		if !v.effortSeen {
			return Capture(256), true
		}
	case "thinking":
		if !v.thinkSeen {
			return Capture(4 << 10), true
		}
	}
	return Action{}, false
}

func (v *ZhipuVariant) TopValue(t *Transformer, key string, raw []byte) {
	switch key {
	case "reasoning_effort":
		v.effortSeen = true
		v.effortRaw = append([]byte(nil), raw...)
	case "thinking":
		v.thinkSeen = true
		v.thinkRaw = append([]byte(nil), raw...)
	}
}

func (v *ZhipuVariant) NeedReasoningScan() bool { return true }

func (v *ZhipuVariant) Tail(t *Transformer, st *OpenAIState) {
	w := t.W()
	var thinking []byte
	if v.thinkSeen {
		thinking = v.thinkRaw
	}
	if v.effortSeen && gjsonStringNonEmpty(v.effortRaw) {
		thinking = lit10 // sjson replaces it with a map as a whole
	} else if v.effortSeen {
		w.Key("reasoning_effort")
		w.Raw(v.effortRaw)
	}
	if st.ReasoningSeen {
		var err error
		thinking, err = setObjectKey(thinking, "clear_thinking", lit11)
		if err != nil {
			t.Bail("thinking is not an object, sjson's handling not reproduced")
			return
		}
	}
	if thinking != nil {
		w.Key("thinking")
		w.Raw(thinking)
	}
}

// ---- OpenRouter (chat completion) ----
//
// Buffered path: when reasoning_max_tokens exists and Int() != 0 → delete reasoning_effort, set reasoning.max_tokens,
// delete reasoning_max_tokens; otherwise unchanged.

type OpenRouterVariant struct {
	effortRaw  []byte
	effortSeen bool
	rmtRaw     []byte
	rmtSeen    bool
	reasonRaw  []byte
	reasonSeen bool
}

func (v *OpenRouterVariant) TopKey(t *Transformer, key string) (Action, bool) {
	switch key {
	case "reasoning_effort":
		if !v.effortSeen {
			return Capture(256), true
		}
	case "reasoning_max_tokens":
		if !v.rmtSeen {
			return Capture(64), true
		}
	case "reasoning":
		if !v.reasonSeen {
			return Capture(4 << 10), true
		}
	}
	return Action{}, false
}

func (v *OpenRouterVariant) TopValue(t *Transformer, key string, raw []byte) {
	cp := append([]byte(nil), raw...)
	switch key {
	case "reasoning_effort":
		v.effortSeen, v.effortRaw = true, cp
	case "reasoning_max_tokens":
		v.rmtSeen, v.rmtRaw = true, cp
	case "reasoning":
		v.reasonSeen, v.reasonRaw = true, cp
	}
}

func (v *OpenRouterVariant) NeedReasoningScan() bool { return false }

func (v *OpenRouterVariant) Tail(t *Transformer, st *OpenAIState) {
	w := t.W()
	n := int64(0)
	if v.rmtSeen {
		n = gjsonInt(v.rmtRaw)
	}
	if !v.rmtSeen || n == 0 {
		if v.effortSeen {
			w.Key("reasoning_effort")
			w.Raw(v.effortRaw)
		}
		if v.rmtSeen {
			w.Key("reasoning_max_tokens")
			w.Raw(v.rmtRaw)
		}
		if v.reasonSeen {
			w.Key("reasoning")
			w.Raw(v.reasonRaw)
		}
		return
	}
	var reasoning []byte
	if v.reasonSeen {
		reasoning = v.reasonRaw
	}
	reasoning, err := setObjectKey(reasoning, "max_tokens", []byte(strconv.FormatInt(n, 10)))
	if err != nil {
		t.Bail("reasoning is not an object, sjson's handling not reproduced")
		return
	}
	w.Key("reasoning")
	w.Raw(reasoning)
}

// setObjectKey reproduces sjson setting "obj.key": creates obj when missing, otherwise replaces / appends the key.
func setObjectKey(obj []byte, key string, val []byte) ([]byte, error) {
	if obj == nil || string(obj) == "null" {
		return []byte(`{"` + key + `":` + string(val) + `}`), nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(obj, &m); err != nil || m == nil {
		return nil, errNotObject
	}
	m[key] = json.RawMessage(val)
	return json.Marshal(m)
}

type notObjectError struct{}

func (notObjectError) Error() string { return "not an object" }

var errNotObject error = notObjectError{}

// gjsonInt reproduces the main branches of gjson.Result.Int(): numbers truncated to integers, strings parsed as integers, true is 1.
func gjsonInt(raw []byte) int64 {
	if len(raw) == 0 {
		return 0
	}
	switch raw[0] {
	case 't':
		return 1
	case '"':
		s, ok := jsonUnquote(raw)
		if !ok {
			return 0
		}
		return gjsonParseInt(s) // gjson only accepts plain digits (optional minus sign) in strings, anything else is 0
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		if f, err := strconv.ParseFloat(string(raw), 64); err == nil {
			return int64(f) // safeInt / standard conversion: truncation
		}
	}
	return 0
}

func gjsonParseInt(s string) int64 {
	var n int64
	i := 0
	neg := false
	if len(s) > 0 && s[0] == '-' {
		neg = true
		i++
	}
	if i == len(s) {
		return 0
	}
	for ; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0
		}
		n = n*10 + int64(s[i]-'0')
	}
	if neg {
		return -n
	}
	return n
}
