package llm

import (
	"encoding/json"
	"errors"
	"math"
)

// Streaming conversion OpenAI → the native Tongyi Qianwen DashScope protocol (qwenEnableCompatible=false).
//
// Derived line by line from the buffered qwen.go buildQwenTextGenerationRequest / chatMessage2QwenMessage:
//   - messages land in input.messages; role / name / reasoning_content / tool_calls of each message are copied, string content
//     streams through, array content is split by text / image into DashScope's multimodal shape (image URLs pass through undecoded);
//   - top-level scalars are folded into parameters and written once in Tail with the same struct as the buffered path (top_p clamped, incremental_output depends on tools);
//   - the request headers (Accept / X-DashScope-SSE) and path (qwen-vl uses the multimodal endpoint) depend on model and stream; the integration layer applies them before release.

type QwenNativeOptions struct {
	// MapModel reproduces the buffered mapModel: an error when model is empty or maps to empty.
	MapModel func(model string) (string, error)
	// SupportsPreserveThinking reproduces qwenSupportsPreserveThinking (applied to the mapped model).
	SupportsPreserveThinking func(model string) bool
	// EnableSearch mirrors the setting qwenEnableSearch.
	EnableSearch bool
	// DeveloperToSystem: the buffered handleRequestBody turns the developer role into system for providers that do not support it
	// (the whole request goes through a struct round trip there, but a DashScope request is struct-built anyway, so only role is visible).
	DeveloperToSystem bool
}

const (
	qwenResultFormatMessage = "message"
	qwenTopPMin             = 0.000001
	qwenTopPMax             = 0.999999
)

// aligned field by field with the buffered path (only for Tail / small Captured values)
type qwenParameters struct {
	ResultFormat      string    `json:"result_format,omitempty"`
	MaxTokens         int       `json:"max_tokens,omitempty"`
	RepetitionPenalty float64   `json:"repetition_penalty,omitempty"`
	N                 int       `json:"n,omitempty"`
	Seed              int       `json:"seed,omitempty"`
	Temperature       float64   `json:"temperature,omitempty"`
	TopP              float64   `json:"top_p,omitempty"`
	IncrementalOutput bool      `json:"incremental_output,omitempty"`
	EnableSearch      bool      `json:"enable_search,omitempty"`
	PreserveThinking  bool      `json:"preserve_thinking,omitempty"`
	Tools             []oaiTool `json:"tools,omitempty"`
}
type qwenToolCall struct {
	Index            int             `json:"index"`
	Id               string          `json:"id,omitempty"`
	Type             string          `json:"type"`
	Function         oaiFunctionCall `json:"function"`
	ThoughtSignature string          `json:"thought_signature,omitempty"`
	ExtraContent     map[string]any  `json:"extra_content,omitempty"`
}

type qwenPart struct {
	typ     string
	typSeen bool
	dead    bool
	urlSeen bool
}

type qwenMsg struct {
	roleSeen       bool
	contentSeen    bool
	contentWritten bool
	finalizing     bool
	part           qwenPart
}

type qwenProto struct {
	opt QwenNativeOptions

	model      string
	modelSeen  bool
	mapped     string
	stream     bool
	streamSeen bool
	maxTok, n  int
	seed       int
	temp, topP float64
	toolsRaw   []byte
	toolsN     int // -1 = tools is null or absent

	messagesSeen  bool
	inputMsgs     int
	reasoningSeen bool
	m             qwenMsg
}

// NewQwenNative builds the OpenAI → native DashScope transformer.
func NewQwenNative(opt QwenNativeOptions) *Transformer {
	if opt.MapModel == nil {
		opt.MapModel = func(m string) (string, error) {
			if m == "" {
				return "", errors.New("missing model in request")
			}
			return m, nil
		}
	}
	if opt.SupportsPreserveThinking == nil {
		opt.SupportsPreserveThinking = func(string) bool { return false }
	}
	p := &qwenProto{opt: opt, toolsN: -1}
	t := NewTransformer(p)
	t.DupKeyBail = true
	return t
}

func (p *qwenProto) Prelude() Prelude {
	return Prelude{Model: p.model, ModelSeen: p.modelSeen, Stream: p.stream, StreamSeen: p.streamSeen}
}

// IncrementalOutput reproduces the buffered parameters.incremental_output = streaming && no tools.
// The integration layer uses it after the whole body to write the incrementalStreaming context key (needed on the response side).
func (p *qwenProto) IncrementalOutput() bool { return p.stream && p.toolsN <= 0 }

// ---- dispatch ----

func (p *qwenProto) OnKey(t *Transformer) Action {
	switch t.Depth() {
	case 1:
		switch t.Last() {
		case "model":
			return Capture(4 << 10)
		case "messages":
			return Probe()
		case "stream":
			return Capture(16)
		case "max_tokens", "n", "seed", "temperature", "top_p":
			return Capture(64)
		case "tools":
			return Capture(toolsCap)
		}
		return Skip() // the buffered qwenTextGenParameters reads no other field
	case 3:
		m := &p.m
		switch t.Last() {
		case "role":
			return Capture(256)
		case "name":
			return Capture(4 << 10)
		case "reasoning_content":
			return Probe()
		case "tool_calls":
			return Capture(assistantWaitCap)
		case "content":
			m.contentSeen = true
			return Probe()
		}
		return Skip() // tool_call_id / audio / refusal ...: qwenMessage has no such fields
	case 5:
		pt := &p.m.part
		if pt.dead {
			return Skip()
		}
		switch t.Last() {
		case "type":
			return Capture(256)
		case "text":
			if !pt.typSeen {
				return Defer(partWaitCap)
			}
			if pt.typ != "text" {
				return Skip()
			}
			return Probe()
		case "image_url":
			if !pt.typSeen {
				return Defer(partWaitCap)
			}
			if pt.typ != "image_url" {
				return Skip()
			}
			return Probe()
		case "input_audio", "file":
			if !pt.typSeen {
				return Defer(smallCap)
			}
			if pt.typ != t.Last() {
				return Skip()
			}
			return Capture(smallCap)
		}
		return Skip()
	case 6:
		if t.Last() == "url" {
			p.m.part.urlSeen = true
			return Probe()
		}
		return Skip() // detail
	}
	return Bail("unexpected path: " + t.PathString())
}

func (p *qwenProto) OnElem(t *Transformer) Action {
	switch t.Depth() {
	case 2, 4:
		return Probe()
	}
	return Bail("unexpected array: " + t.PathString())
}

func (p *qwenProto) OnStart(t *Transformer, kind ValueKind) Action {
	w := t.W()
	switch t.Depth() {
	case 1: // messages → input.messages
		if kind != KindArray {
			return Bail("messages is not an array")
		}
		w.PushObj("input")
		w.PushArr("messages")
		return Enter().Flat()
	case 2:
		if kind != KindObject {
			return Bail("message is not an object")
		}
		p.m = qwenMsg{}
		p.inputMsgs++
		return Enter() // every message is written (system stays in place)
	case 3:
		switch t.Last() {
		case "content":
			switch kind {
			case KindString:
				p.m.contentWritten = true
				return Pass() // string: verbatim
			case KindArray:
				p.m.contentWritten = true
				return Enter().Lazy() // multimodal: map part by part; when no part lands the buffered path has null, handled in OnLeave
			}
			return Skip() // object / scalar / null: ParseContent yields nil → "content":null
		case "reasoning_content":
			if kind != KindString {
				return Bail("reasoning_content is not a string, the buffered struct decoding fails")
			}
			return Prefix(1) // only meaningful when non-empty (omitempty), and "seen non-empty" must be recorded
		}
	case 4:
		if kind != KindObject {
			return Skip() // the buffered ParseContent skips non-map elements
		}
		p.m.part = qwenPart{}
		return Enter().Lazy()
	case 5:
		pt := &p.m.part
		switch t.Last() {
		case "text":
			if kind != KindString {
				pt.dead = true
				return Skip()
			}
			return Prefix(1)
		case "image_url":
			if kind != KindObject {
				pt.dead = true
				return Skip()
			}
			return Enter().Flat()
		}
	case 6: // image_url.url → "image"
		if kind != KindString {
			return Bail("image_url.url is not a string, the buffered path panics")
		}
		return Prefix(1)
	}
	_ = w
	return Bail("unexpected Probe: " + t.PathString())
}

// ---- values complete ----

func (p *qwenProto) OnValue(t *Transformer, raw []byte) {
	w := t.W()
	isNull := string(raw) == "null"
	switch t.Depth() {
	case 1:
		switch t.Last() {
		case "model":
			s, ok := jsonUnquote(raw)
			if !ok {
				t.Bail("model is not a string")
				return
			}
			p.model, p.modelSeen = s, true
			mapped, err := p.opt.MapModel(s)
			if err != nil {
				t.Bail(err.Error())
				return
			}
			p.mapped = mapped
		case "stream":
			switch string(raw) {
			case "true":
				p.stream = true
			case "false", "null":
			default:
				t.Bail("stream is not a boolean")
				return
			}
			p.streamSeen = !isNull
		case "max_tokens", "n", "seed":
			if isNull {
				return
			}
			if !isIntLiteral(raw) {
				t.Bail(t.Last() + " is not an integer")
				return
			}
			v := atoi(raw)
			switch t.Last() {
			case "max_tokens":
				p.maxTok = v
			case "n":
				p.n = v
			default:
				p.seed = v
			}
		case "temperature", "top_p":
			if isNull {
				return
			}
			if !isNumLiteral(raw) {
				t.Bail(t.Last() + " is not a number")
				return
			}
			f, _ := parseFloat(raw)
			if t.Last() == "temperature" {
				p.temp = f
			} else {
				p.topP = f
			}
		case "tools":
			if isNull {
				return
			}
			var tools []oaiTool
			if err := json.Unmarshal(raw, &tools); err != nil {
				t.Bail("tools failed to decode: " + err.Error())
				return
			}
			p.toolsRaw = append([]byte(nil), raw...)
			p.toolsN = len(tools)
		}
	case 3:
		switch t.Last() {
		case "role":
			s, ok := jsonUnquote(raw)
			if !ok {
				t.Bail("role is not a string")
				return
			}
			p.m.roleSeen = true
			if s == "developer" && p.opt.DeveloperToSystem {
				s = "system"
			}
			w.Key("role")
			w.JSONString(s)
		case "name":
			s, ok := jsonUnquote(raw)
			if !ok {
				t.Bail("name is not a string")
				return
			}
			if s != "" { // omitempty
				w.Key("name")
				w.JSONString(s)
			}
		case "tool_calls":
			if isNull {
				return
			}
			var tcs []qwenToolCall
			if err := json.Unmarshal(raw, &tcs); err != nil {
				t.Bail("tool_calls failed to decode: " + err.Error())
				return
			}
			if len(tcs) > 0 { // omitempty
				b, _ := json.Marshal(tcs)
				w.Key("tool_calls")
				w.Raw(b)
			}
		}
	case 5:
		pt := &p.m.part
		switch t.Last() {
		case "type":
			pt.typSeen = true
			s, ok := jsonUnquote(raw)
			if !ok {
				pt.dead = true
				t.DropDeferred()
				return
			}
			pt.typ = s
			switch s {
			case "text", "image_url", "input_audio", "file":
				if len(t.Deferred()) > 0 {
					t.Release()
				}
			default:
				pt.dead = true // ParseContent's switch matches nothing → not added to the list
				t.DropDeferred()
			}
		case "input_audio", "file":
			// the buffered ParseContent asserts .(string) and panics when the field is missing; on success qwen gets an empty object {}
			var obj map[string]any
			if err := json.Unmarshal(raw, &obj); err != nil {
				pt.dead = true
				return
			}
			keys := []string{"file_id"}
			if t.Last() == "input_audio" {
				keys = []string{"data", "format"}
			}
			for _, k := range keys {
				if _, ok := obj[k].(string); !ok {
					t.Bail(t.Last() + "." + k + " missing, the buffered path panics")
					return
				}
			}
			w.Open() // {}
		}
	}
}

func (p *qwenProto) OnPrefix(t *Transformer, raw []byte, complete bool) (Action, int) {
	w := t.W()
	empty := complete && len(raw) == 0
	switch t.Depth() {
	case 3: // reasoning_content
		if empty {
			return Skip(), 0 // omitempty
		}
		p.reasoningSeen = true
		w.KeyRaw(t.KeyRaw())
		return Pass().Wrap(lit0, lit0), 0
	case 5: // part.text → {"text":...}
		if empty {
			w.Open() // {}
			return Skip(), 0
		}
		w.KeyRaw(t.KeyRaw())
		return Pass().Wrap(lit0, lit0), 0
	case 6: // image_url.url → {"image":...}
		if empty {
			w.Open()
			return Skip(), 0
		}
		w.Key("image")
		return Pass().Wrap(lit0, lit0), 0
	}
	return Bail("unexpected Prefix: " + t.PathString()), 0
}

// ---- containers closing ----

func (p *qwenProto) OnLeave(t *Transformer) {
	w := t.W()
	switch t.Depth() {
	case 1: // messages: buffered make(...,0); materialize [] then pop the two levels of input.messages together
		p.messagesSeen = true
		if p.inputMsgs == 0 {
			t.Bail("no message found in the request body")
			return
		}
		w.Open()
		w.Pop()
		w.Pop()
	case 2:
		m := &p.m
		m.finalizing = true
		if len(t.Deferred()) > 0 {
			t.ReleaseNow()
			if t.Dead() {
				return
			}
		}
		if !m.roleSeen { // Role has no omitempty
			w.Key("role")
			w.RawString(`""`)
		}
		if !m.contentWritten { // ParseContent yields nil → null; absent is nil as well
			w.Key("content")
			w.RawString("null")
		}
	case 3: // content array: buffered contents starts as nil and appends, nothing landed means null
		if !w.Opened(w.Level()) {
			w.KeyAt(w.Level()-1, "content")
			w.RawString("null")
		}
	case 4:
		if p.m.part.dead || !p.m.part.typSeen {
			t.DropDeferred()
		}
	case 5:
		if !p.m.part.urlSeen {
			t.Bail("image_url.url missing, the buffered path panics")
		}
	}
}

// ---- tail ----

func (p *qwenProto) Tail(t *Transformer) {
	w := t.W()
	if !p.messagesSeen {
		t.Bail("no message found in the request body")
		return
	}
	if !p.modelSeen {
		mapped, err := p.opt.MapModel("")
		if err != nil {
			t.Bail(err.Error())
			return
		}
		p.mapped = mapped
	}
	w.Key("model")
	w.JSONString(p.mapped)
	params := qwenParameters{
		ResultFormat:      qwenResultFormatMessage,
		MaxTokens:         p.maxTok,
		N:                 p.n,
		Seed:              p.seed,
		Temperature:       p.temp,
		TopP:              math.Max(qwenTopPMin, math.Min(p.topP, qwenTopPMax)),
		IncrementalOutput: p.IncrementalOutput(),
		EnableSearch:      p.opt.EnableSearch,
		PreserveThinking:  p.reasoningSeen && p.opt.SupportsPreserveThinking(p.mapped),
	}
	if p.toolsRaw != nil {
		_ = json.Unmarshal(p.toolsRaw, &params.Tools) // validated in OnValue
	}
	b, _ := json.Marshal(params)
	w.Key("parameters")
	w.Raw(b)
}

func parseFloat(raw []byte) (float64, error) {
	var f float64
	err := json.Unmarshal(raw, &f)
	return f, err
}
