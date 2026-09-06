package llm

import "encoding/json"

// Streaming passthrough protocol of the OpenAI-compatible family.
//
// Mirrors the buffered defaultTransformRequestBody + normalizeOpenAiRequestBody + convertDeveloperRoleToSystem:
// bytes pass through, with only a few touches: rewrite model, add stream_options.include_usage when required,
// and fall back on a developer role (that buffered path re-serializes the whole request through a struct, which streaming cannot reproduce).
//
// Qwen compatible mode / Zhipu / OpenRouter each add a little logic of their own on top, plugged in as a hand-written Variant
// (proto_openai_variants.go): they are code, not a rule table.
type OpenAIOptions struct {
	// MapModel reproduces getMappedModel: returns the input unchanged when no mapping matches, never fails.
	MapModel func(model string) string
	// ModelOnlyIfPresent: the buffered path only rewrites with sjson when model is present (Qwen compatible mode);
	// the default path adds "model":<result of mapping the empty string> when it is missing.
	ModelOnlyIfPresent bool
	// DetectStream: chat / videos / video_remix need to read stream (the side effects are applied by the integration layer).
	DetectStream bool
	// NormalizeUsage: chat / completion with usage statistics enabled add include_usage when stream is true.
	NormalizeUsage bool
	// DeveloperRoleSupported false means a developer role falls back.
	DeveloperRoleSupported bool
	// CheckMessages: only chat needs to enter messages and check role.
	CheckMessages bool
	// Variant: provider-specific logic; nil = pure default path.
	Variant OpenAIVariant
}

// OpenAIVariant is the provider-specific logic of one member of the OpenAI-compatible family.
type OpenAIVariant interface {
	// TopKey decides the action for a top-level key; ok=false hands it to the base protocol.
	TopKey(t *Transformer, key string) (Action, bool)
	// TopValue receives the top-level values the Variant Captured itself.
	TopValue(t *Transformer, key string, raw []byte)
	// NeedReasoningScan: whether it needs to know if any messages[].reasoning_content is non-empty.
	NeedReasoningScan() bool
	// Tail writes the provider-specific fields at the end.
	Tail(t *Transformer, base *OpenAIState)
}

// OpenAIState is the state the base protocol exposes to a Variant.
type OpenAIState struct {
	ModelSeen     bool
	Model         string // original model (valid when ModelSeen)
	Mapped        string // mapped model (valid when ModelSeen)
	ReasoningSeen bool   // some message has a non-empty reasoning_content (gjson String() != "")
}

type openaiProto struct {
	opt OpenAIOptions
	st  OpenAIState

	stream         bool
	streamSeen     bool
	streamOpts     []byte
	streamOptsSeen bool
	scanReasoning  bool
	msgRCSeen      bool     // reasoning_content already seen in the current message (gjson only looks at the first)
	movedTop       []string // top-level keys Captured and moved to the end: a second occurrence cannot keep the last-wins order
}

// NewOpenAI builds the transformer of the OpenAI-compatible passthrough protocol.
func NewOpenAI(opt OpenAIOptions) *Transformer {
	if opt.MapModel == nil {
		opt.MapModel = func(m string) string { return m }
	}
	p := &openaiProto{opt: opt}
	if opt.Variant != nil {
		p.scanReasoning = opt.Variant.NeedReasoningScan()
	}
	t := NewTransformer(p)
	t.DupKeyBail = false // sjson semantics: only the first duplicate key is touched, the rest stay verbatim
	return t
}

func (p *openaiProto) Prelude() Prelude {
	return Prelude{Model: p.st.Model, ModelSeen: p.st.ModelSeen, Stream: p.stream, StreamSeen: p.streamSeen && p.opt.DetectStream}
}

func (p *openaiProto) enterMessages() bool {
	return (p.opt.CheckMessages && !p.opt.DeveloperRoleSupported) || p.scanReasoning
}

// moved records a top-level key that was moved to the end of the output.
// sjson touches only the first of duplicate keys and keeps the rest verbatim, so the later ones still come after it;
// once the first has been moved to the end, the last-wins order is reversed, and such input can only fall back.
func (p *openaiProto) moved(t *Transformer, key string, a Action) Action {
	if a.IsCapture() {
		p.movedTop = append(p.movedTop, key)
	}
	return a
}

func (p *openaiProto) OnKey(t *Transformer) Action {
	switch t.Depth() {
	case 1:
		k := t.Last()
		for _, m := range p.movedTop {
			if m == k {
				return Bail("top-level duplicate key " + k + " was already captured, the last-wins order cannot be kept")
			}
		}
		if p.opt.Variant != nil {
			if a, ok := p.opt.Variant.TopKey(t, k); ok {
				return p.moved(t, k, a)
			}
		}
		switch t.Last() {
		case "model":
			if p.st.ModelSeen {
				return Pass() // sjson only rewrites the first
			}
			return Capture(4 << 10)
		case "stream":
			// the side effects (Accept / isStreaming) need DetectStream, the include_usage decision needs NormalizeUsage;
			// either of them means the value of stream has to be looked at.
			if (!p.opt.DetectStream && !p.opt.NormalizeUsage) || p.streamSeen {
				return Pass()
			}
			return Observe(256)
		case "stream_options":
			if !p.opt.NormalizeUsage || p.streamOptsSeen {
				return Pass()
			}
			return p.moved(t, k, Capture(16<<10))
		case "messages":
			if p.enterMessages() {
				if p.scanReasoning {
					return Probe() // gjson's Array() has special semantics for non-arrays: fall back on those
				}
				return Enter().Lenient()
			}
			return Pass()
		}
		return Pass()
	case 3:
		switch t.Last() {
		case "role":
			if p.opt.CheckMessages && !p.opt.DeveloperRoleSupported {
				return Observe(256)
			}
		case "reasoning_content":
			if p.scanReasoning && !p.msgRCSeen {
				p.msgRCSeen = true
				return Probe()
			}
		}
		return Pass()
	}
	return Pass()
}

func (p *openaiProto) OnElem(t *Transformer) Action {
	if t.Depth() == 2 {
		p.msgRCSeen = false
		return Enter().Lenient()
	}
	return Pass()
}

func (p *openaiProto) OnStart(t *Transformer, kind ValueKind) Action {
	switch t.Depth() {
	case 1: // messages
		if kind != KindArray {
			return Bail("messages is not an array, gjson Array() semantics not reproduced")
		}
		return Enter()
	case 3: // reasoning_content: only whether it is non-empty matters, nothing is buffered
		switch kind {
		case KindString:
			return Prefix(1)
		case KindNull, KindBool, KindNumber:
			return Observe(64)
		default:
			p.st.ReasoningSeen = true // String() of an object / array is its raw text, non-empty
			return Pass()
		}
	}
	return Pass()
}

func (p *openaiProto) OnValue(t *Transformer, raw []byte) {
	switch t.Depth() {
	case 1:
		k := t.Last()
		if p.opt.Variant != nil {
			switch k {
			case "model", "stream", "stream_options":
			default:
				p.opt.Variant.TopValue(t, k, raw)
				return
			}
		}
		switch k {
		case "model":
			// gjson.String() renders a non-string as text and sjson writes it back as a string, changing the type.
			// Such input is extremely rare; fall back to the buffered path instead of reproducing it here.
			s, ok := jsonUnquote(raw)
			if !ok {
				t.Bail("model is not a string")
				return
			}
			p.st.ModelSeen = true
			p.st.Model = s
			p.st.Mapped = p.opt.MapModel(s)
			w := t.W()
			w.KeyRaw(t.KeyRaw()) // keep the original spelling of "model":, as sjson's in-place rewrite does
			w.JSONString(p.st.Mapped)
		case "stream":
			p.streamSeen = true
			p.stream = gjsonBool(raw)
		case "stream_options":
			p.streamOptsSeen = true
			p.streamOpts = append([]byte(nil), raw...)
		}
	case 3:
		switch t.Last() {
		case "role":
			if s, ok := jsonUnquote(raw); ok && s == "developer" {
				t.Bail("developer role: the buffered path re-serializes the whole request through a struct, not reproduced by streaming")
			}
		case "reasoning_content":
			if string(raw) != "null" {
				p.st.ReasoningSeen = true
			}
		}
	}
}

func (p *openaiProto) OnPrefix(t *Transformer, raw []byte, complete bool) (Action, int) {
	if t.Depth() == 3 && t.Last() == "reasoning_content" {
		if !(complete && len(raw) == 0) {
			p.st.ReasoningSeen = true
		}
		t.W().KeyRaw(t.KeyRaw())
		return Pass().Wrap(lit0, lit0), 0
	}
	return Bail("unexpected Prefix: " + t.PathString()), 0
}

func (p *openaiProto) OnLeave(t *Transformer) {}

func (p *openaiProto) Tail(t *Transformer) {
	w := t.W()
	if !p.st.ModelSeen && !p.opt.ModelOnlyIfPresent {
		// sjson.SetBytes adds a model when it is missing (the result of mapping the empty string)
		w.Key("model")
		w.JSONString(p.opt.MapModel(""))
	}
	if p.opt.Variant != nil {
		p.opt.Variant.Tail(t, &p.st)
		if t.Dead() {
			return
		}
	}
	if !p.opt.NormalizeUsage {
		return
	}
	if !p.stream {
		if p.streamOptsSeen {
			w.Key("stream_options")
			w.Raw(p.streamOpts)
		}
		return
	}
	if !p.streamOptsSeen {
		w.Key("stream_options")
		w.RawString(`{"include_usage":true}`)
		return
	}
	// stream_options present: only append when include_usage is missing (gjson Exists semantics)
	var m map[string]json.RawMessage
	if err := json.Unmarshal(p.streamOpts, &m); err != nil || m == nil {
		t.Bail("stream_options is not an object, sjson's handling not reproduced")
		return
	}
	w.Key("stream_options")
	if _, has := m["include_usage"]; has {
		w.Raw(p.streamOpts)
		return
	}
	body := p.streamOpts[:len(p.streamOpts)-1] // drop the closing }
	w.Raw(body)
	if len(m) > 0 {
		w.Byte(',')
	}
	w.RawString(`"include_usage":true}`)
}

// gjsonStringNonEmpty reproduces gjson.Result.String() != "": only null and "" are empty.
func gjsonStringNonEmpty(raw []byte) bool {
	return len(raw) > 0 && string(raw) != "null" && string(raw) != `""`
}
