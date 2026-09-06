package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
)

// Streaming conversion protocol OpenAI → Gemini.
//
// Derived line by line from the buffered gemini.go buildGeminiChatRequest. The structural differences are larger than for Claude:
//   - messages → contents, assistant → model, and system messages move to the top-level system_instruction as a whole;
//   - each content becomes parts: a string → [{text}], image_url → inlineData (data: URLs are split after the prefix, the payload streams through);
//   - a set of top-level scalars is folded into generationConfig and written once in Tail;
//   - the request path depends on model and stream (applied by the integration layer before the headers are released).
//
// The buffered path fetches http(s) images asynchronously and inlines them; streaming cannot reproduce that and falls back.

type GeminiSafetySetting struct {
	Category  string `json:"category"`
	Threshold string `json:"threshold"`
}

type GeminiOptions struct {
	// MapModel reproduces the buffered mapModel: an error when model is empty or maps to empty.
	MapModel func(model string) (string, error)
	// ThinkingModel reproduces geminiThinkingModels[mapped model].
	ThinkingModel func(mapped string) bool
	// ThinkingBudget mirrors the setting geminiThinkingBudget.
	ThinkingBudget int64
	// SafetySettings mirrors the setting geminiSafetySetting (the buffered path iterates a map, so the order was never fixed).
	SafetySettings []GeminiSafetySetting
}

// output structs aligned field by field with the buffered path (only for Tail / small Captured values)
type geminiGenerationConfig struct {
	Temperature        float64               `json:"temperature,omitempty"`
	TopP               float64               `json:"topP,omitempty"`
	TopK               int64                 `json:"topK,omitempty"`
	Seed               int64                 `json:"seed,omitempty"`
	Logprobs           bool                  `json:"logprobs,omitempty"`
	MaxOutputTokens    int                   `json:"maxOutputTokens,omitempty"`
	CandidateCount     int                   `json:"candidateCount,omitempty"`
	StopSequences      []string              `json:"stopSequences,omitempty"`
	PresencePenalty    int64                 `json:"presencePenalty,omitempty"`
	FrequencyPenalty   int64                 `json:"frequencyPenalty,omitempty"`
	ResponseModalities []string              `json:"responseModalities,omitempty"`
	NegativePrompt     string                `json:"negativePrompt,omitempty"`
	ThinkingConfig     *geminiThinkingConfig `json:"thinkingConfig,omitempty"`
	MediaResolution    string                `json:"mediaResolution,omitempty"`
}
type geminiThinkingConfig struct {
	IncludeThoughts bool  `json:"includeThoughts,omitempty"`
	ThinkingBudget  int64 `json:"thinkingBudget,omitempty"`
}
type geminiTools struct {
	FunctionDeclarations any `json:"function_declarations,omitempty"`
}
type geminiPart struct {
	Text       string            `json:"text,omitempty"`
	InlineData *geminiInlineData `json:"inlineData,omitempty"`
}
type geminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type gemPart struct {
	typ     string
	typSeen bool
	dead    bool
	urlSeen bool
}

type gemMsg struct {
	role         string
	roleSeen     bool
	roleWritten  bool
	contentSeen  bool
	partsWritten bool
	finalizing   bool
	part         gemPart
}

type geminiProto struct {
	opt GeminiOptions

	model      string
	modelSeen  bool
	stream     bool
	streamSeen bool
	temp, topP float64
	maxTok     int
	presence   int64
	frequency  int64
	logprobs   bool
	modalities []string
	tools      ToolsHook

	messagesSeen bool
	inputMsgs    int
	sysSeen      bool
	sysParts     []byte // serialized parts array
	m            gemMsg
}

// NewGemini builds the OpenAI → Gemini transformer.
func NewGemini(opt GeminiOptions) *Transformer {
	if opt.MapModel == nil {
		opt.MapModel = func(m string) (string, error) {
			if m == "" {
				return "", errors.New("missing model in request")
			}
			return m, nil
		}
	}
	if opt.ThinkingModel == nil {
		opt.ThinkingModel = func(string) bool { return false }
	}
	p := &geminiProto{opt: opt, tools: ToolsHook{ParamsKey: "parameters"}}
	t := NewTransformer(p)
	t.DupKeyBail = true
	return t
}

func (p *geminiProto) Prelude() Prelude {
	return Prelude{Model: p.model, ModelSeen: p.modelSeen, Stream: p.stream, StreamSeen: p.streamSeen}
}

// ---- dispatch ----

func (p *geminiProto) OnKey(t *Transformer) Action {
	switch t.Depth() {
	case 1:
		switch t.Last() {
		case "model":
			return Capture(4 << 10)
		case "messages":
			return Probe()
		case "stream", "logprobs":
			return Capture(16)
		case "temperature", "top_p", "max_tokens", "presence_penalty", "frequency_penalty":
			return Capture(64)
		case "modalities":
			return Capture(4 << 10)
		case "tools":
			return Probe() // stream element by element
		}
		return Skip() // stop / seed / n / max_completion_tokens / tool_choice ...: not read by the buffered path
	case 3:
		m := &p.m
		switch t.Last() {
		case "role":
			return Capture(256)
		case "content":
			if !m.roleSeen {
				return Defer(roleWaitCap)
			}
			m.contentSeen = true
			if m.role == "system" {
				return Capture(systemCap) // moves to system_instruction as a whole; the cap matches the buffered body limit
			}
			return Probe()
		}
		return Skip() // tool_calls / name / ...: not read by the buffered path
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
		}
		return Skip()
	case 6:
		if t.Last() == "url" {
			p.m.part.urlSeen = true
			return Prefix(urlPrefixWin)
		}
		return Skip()
	}
	return Bail("unexpected path: " + t.PathString())
}

func (p *geminiProto) OnElem(t *Transformer) Action {
	switch t.Depth() {
	case 2, 4:
		return Probe()
	}
	return Bail("unexpected array: " + t.PathString())
}

func (p *geminiProto) OnStart(t *Transformer, kind ValueKind) Action {
	w := t.W()
	if t.Depth() == 1 && t.Last() == "tools" { // tools → tools:[{function_declarations:[...]}] (materialized whenever buffered Tools != nil, an empty array included)
		switch kind {
		case KindNull:
			return Skip()
		case KindArray:
			w.PushArr("tools")
			w.PushObj("")
			w.PushArr("function_declarations")
			return Enter().Flat().Via(&p.tools) // the inside goes to the sub-hook; on close control returns here to Pop
		}
		return Bail("tools is not an array, the buffered struct decoding fails")
	}
	switch t.Depth() {
	case 1: // messages → contents (buffered make(...,0): [] even when every message is system)
		if kind != KindArray {
			return Bail("messages is not an array")
		}
		return Enter().As("contents")
	case 2:
		if kind != KindObject {
			return Bail("message is not an object")
		}
		p.m = gemMsg{}
		p.inputMsgs++
		return Enter().Lazy() // system messages produce no element
	case 3: // content (non-system, role known)
		switch kind {
		case KindString:
			p.writeRole(t)
			p.m.partsWritten = true
			return Prefix(1) // buffered Text has omitempty: an empty string is [{}], so peek first
		case KindArray:
			p.writeRole(t)
			p.m.partsWritten = true
			return Enter().As("parts")
		}
		return Skip() // object / scalar / null: ParseContent yields empty → "parts":[]
	case 4:
		if kind != KindObject {
			return Skip()
		}
		p.m.part = gemPart{}
		return Enter().Lazy()
	case 5:
		pt := &p.m.part
		switch t.Last() {
		case "text":
			if kind != KindString {
				pt.dead = true
				return Skip()
			}
			return Prefix(1) // empty string → {} (Text omitempty)
		case "image_url":
			if kind != KindObject {
				pt.dead = true
				return Skip()
			}
			return Enter().Flat()
		}
	}
	_ = w
	return Bail("unexpected Probe: " + t.PathString())
}

// ---- values complete ----

func (p *geminiProto) OnValue(t *Transformer, raw []byte) {
	switch t.Depth() {
	case 1:
		p.topValue(t, raw)
	case 3:
		p.msgValue(t, raw)
	case 5:
		p.partValue(t, raw)
	}
}

func (p *geminiProto) topValue(t *Transformer, raw []byte) {
	isNull := string(raw) == "null"
	switch t.Last() {
	case "model":
		s, ok := jsonUnquote(raw)
		if !ok {
			t.Bail("model is not a string")
			return
		}
		p.model, p.modelSeen = s, true
	case "stream", "logprobs":
		switch string(raw) {
		case "true":
			if t.Last() == "stream" {
				p.stream = true
			} else {
				p.logprobs = true
			}
		case "false", "null":
		default:
			t.Bail(t.Last() + " is not a boolean")
			return
		}
		if t.Last() == "stream" {
			p.streamSeen = !isNull
		}
	case "temperature", "top_p", "presence_penalty", "frequency_penalty":
		if isNull {
			return
		}
		f, err := strconv.ParseFloat(string(raw), 64)
		if err != nil || !isNumLiteral(raw) {
			t.Bail(t.Last() + " is not a number")
			return
		}
		switch t.Last() {
		case "temperature":
			p.temp = f
		case "top_p":
			p.topP = f
		case "presence_penalty":
			p.presence = int64(f) // buffered int64(float64): truncation
		default:
			p.frequency = int64(f)
		}
	case "max_tokens":
		if isNull {
			return
		}
		if !isIntLiteral(raw) {
			t.Bail("max_tokens is not an integer")
			return
		}
		p.maxTok = atoi(raw)
	case "modalities":
		if isNull {
			return
		}
		var ss []string
		if err := json.Unmarshal(raw, &ss); err != nil {
			t.Bail("modalities is not an array of strings")
			return
		}
		p.modalities = ss
	}
}

func (p *geminiProto) msgValue(t *Transformer, raw []byte) {
	m := &p.m
	switch t.Last() {
	case "role":
		s, ok := jsonUnquote(raw)
		if !ok {
			t.Bail("role is not a string")
			return
		}
		m.role, m.roleSeen = s, true
		if len(t.Deferred()) > 0 {
			t.Release()
		}
	case "content": // only system content is Captured here
		parts, err := geminiPartsFromContent(raw)
		if err != nil {
			t.Bail(err.Error())
			return
		}
		p.sysSeen, p.sysParts = true, parts
	}
}

func (p *geminiProto) partValue(t *Transformer, raw []byte) {
	pt := &p.m.part
	if t.Last() != "type" {
		return
	}
	pt.typSeen = true
	s, ok := jsonUnquote(raw)
	if !ok {
		pt.dead = true
		t.DropDeferred()
		return
	}
	pt.typ = s
	switch s {
	case "text", "image_url":
		if len(t.Deferred()) > 0 {
			t.Release()
		}
	default:
		pt.dead = true // input_audio / file / unknown: skipped by the buffered gemini branch
		t.DropDeferred()
	}
}

// OnPrefix: the prefix window of image_url.url. Reproduces the buffered handleContentTypeImageUrl + baseStr2InlineData.
func (p *geminiProto) OnPrefix(t *Transformer, raw []byte, complete bool) (Action, int) {
	w := t.W()
	switch t.Depth() {
	case 3: // string content → parts:[{text}]; empty string → [{}]
		if complete && len(raw) == 0 {
			w.Key("parts")
			w.RawString(`[{}]`)
			return Skip(), 0
		}
		w.Key("parts")
		return Pass().Wrap(lit5, lit6), 0
	case 5: // text part
		if complete && len(raw) == 0 {
			w.Open() // {}
			return Skip(), 0
		}
		w.KeyRaw(t.KeyRaw())
		return Pass().Wrap(lit0, lit0), 0
	}
	dec, off := unescapePrefix(raw)
	if isHTTPURLPrefix(dec) {
		return Bail("the buffered path fetches http(s) images asynchronously and inlines them, not reproduced by streaming"), 0
	}
	if !bytes.HasPrefix(dec, lit1) {
		if !complete {
			return Bail("non-data: image string exceeds the prefix window"), 0
		}
		// buffered baseStr2InlineData: logs an error and yields an empty inlineData
		w.Key("inlineData")
		w.RawString(`{"mimeType":"","data":""}`)
		return Skip(), 0
	}
	semi := bytes.IndexByte(dec, ';')
	if semi < 0 {
		if !complete {
			return Bail("data URL header exceeds the prefix window"), 0
		}
		w.Open() // buffered path: a failed split returns nil → empty part {}
		return Skip(), 0
	}
	mime := string(dec[5:semi])
	rest := dec[semi+1:]
	resumeDec := semi + 1
	if !complete && len(rest) < len("base64,") {
		return Bail("data URL header cut at the window boundary"), 0
	}
	if bytes.HasPrefix(rest, lit4) {
		resumeDec += len("base64,")
	}
	resume := off[resumeDec]
	if !complete && resume >= len(raw) {
		return Bail("data URL payload cut at the window boundary"), 0
	}
	w.Key("inlineData")
	w.RawString(`{"mimeType":`)
	w.JSONString(mime)
	w.RawString(`,"data":"`)
	return Pass().Wrap(nil, lit3), resume
}

// ---- containers closing ----

func (p *geminiProto) OnLeave(t *Transformer) {
	w := t.W()
	if t.Depth() == 1 && t.Last() == "tools" {
		w.Open() // empty tools is materialized as [{"function_declarations":[]}] as well
		w.Pop()
		w.Pop()
		w.Pop()
		return
	}
	switch t.Depth() {
	case 1:
		p.messagesSeen = true
		if p.inputMsgs == 0 {
			t.Bail("no message found in the request body")
		}
	case 2:
		p.finishMessage(t)
	case 3: // parts array: always materialized by the buffered path
		w.Open()
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

func (p *geminiProto) writeRole(t *Transformer) {
	m := &p.m
	if m.roleWritten {
		return
	}
	m.roleWritten = true
	w := t.W()
	w.Open() // the element must exist even when role is empty (omitempty)
	role := m.role
	if role == "assistant" {
		role = "model"
	}
	if role != "" {
		w.Key("role")
		w.JSONString(role)
	}
}

func (p *geminiProto) finishMessage(t *Transformer) {
	m := &p.m
	w := t.W()
	if !m.roleSeen {
		m.role, m.roleSeen = "", true
	}
	m.finalizing = true
	if m.role == "system" {
		if len(t.Deferred()) > 0 {
			t.ReleaseNow()
			if t.Dead() {
				return
			}
		}
		if !m.contentSeen {
			p.sysSeen, p.sysParts = true, lit7
		}
		return
	}
	if len(t.Deferred()) > 0 {
		t.ReleaseNow()
		if t.Dead() {
			return
		}
	}
	p.writeRole(t)
	if !m.partsWritten {
		w.Key("parts")
		w.RawString("[]")
	}
}

// ---- tail ----

func (p *geminiProto) Tail(t *Transformer) {
	w := t.W()
	if !p.messagesSeen {
		t.Bail("no message found in the request body")
		return
	}
	mapped, err := p.opt.MapModel(p.model)
	if err != nil {
		t.Bail(err.Error())
		return
	}
	if p.sysSeen {
		w.Key("system_instruction")
		w.RawString(`{"parts":`)
		w.Raw(p.sysParts)
		w.Byte('}')
	}
	if len(p.opt.SafetySettings) > 0 {
		b, _ := json.Marshal(p.opt.SafetySettings)
		w.Key("safetySettings")
		w.Raw(b)
	}
	cfg := geminiGenerationConfig{
		Temperature:        p.temp,
		TopP:               p.topP,
		MaxOutputTokens:    p.maxTok,
		PresencePenalty:    p.presence,
		FrequencyPenalty:   p.frequency,
		Logprobs:           p.logprobs,
		ResponseModalities: p.modalities,
	}
	if p.opt.ThinkingModel(mapped) {
		cfg.ThinkingConfig = &geminiThinkingConfig{IncludeThoughts: true, ThinkingBudget: p.opt.ThinkingBudget}
	}
	b, _ := json.Marshal(cfg)
	w.Key("generationConfig")
	w.Raw(b)
}

// geminiPartsFromContent reproduces ParseContent + the gemini part mapping (for Captured system content).
func geminiPartsFromContent(raw []byte) ([]byte, error) {
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, errors.New("invalid system content")
	}
	parts := make([]geminiPart, 0)
	switch c := v.(type) {
	case string:
		parts = append(parts, geminiPart{Text: c})
	case []interface{}:
		for _, it := range c {
			m, ok := it.(map[string]interface{})
			if !ok {
				continue
			}
			switch m["type"] {
			case "text":
				if s, ok := m["text"].(string); ok {
					parts = append(parts, geminiPart{Text: s})
				}
			case "image_url":
				sub, ok := m["image_url"].(map[string]interface{})
				if !ok {
					continue
				}
				u, ok := sub["url"].(string)
				if !ok {
					return nil, errors.New("image_url.url missing, the buffered path panics")
				}
				if isHTTPURLPrefix([]byte(u)) {
					// the buffered path only fetches http images inside contents (countImageUrl does not scan system_instruction);
					// inside system they stay as {mimeType:"", data:url}
					parts = append(parts, geminiPart{InlineData: &geminiInlineData{Data: u}})
					continue
				}
				parts = append(parts, geminiPart{InlineData: inlineDataFromDataURL(u)})
			case "input_audio":
				sub, ok := m["input_audio"].(map[string]interface{})
				if ok {
					if _, ok := sub["data"].(string); !ok {
						return nil, errors.New("input_audio.data missing, the buffered path panics")
					}
					if _, ok := sub["format"].(string); !ok {
						return nil, errors.New("input_audio.format missing, the buffered path panics")
					}
				}
			case "file":
				sub, ok := m["file"].(map[string]interface{})
				if ok {
					if _, ok := sub["file_id"].(string); !ok {
						return nil, errors.New("file.file_id missing, the buffered path panics")
					}
				}
			}
		}
	}
	return json.Marshal(parts)
}

// inlineDataFromDataURL reproduces baseStr2InlineData: nil when the URL cannot be split (→ empty part).
func inlineDataFromDataURL(s string) *geminiInlineData {
	if len(s) >= 5 && s[:5] == "data:" {
		semi := -1
		for i := 0; i < len(s); i++ {
			if s[i] == ';' {
				semi = i
				break
			}
		}
		if semi < 0 {
			return nil
		}
		mime := s[5:semi]
		data := s[semi+1:]
		if len(data) >= 7 && data[:7] == "base64," {
			data = data[7:]
		}
		return &geminiInlineData{MimeType: mime, Data: data}
	}
	return &geminiInlineData{}
}

// isHTTPURLPrefix reproduces isUrl: scheme http / https after url.Parse (Parse lowercases the scheme).
func isHTTPURLPrefix(b []byte) bool {
	l := make([]byte, 0, 8)
	for i := 0; i < len(b) && i < 8; i++ {
		c := b[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		l = append(l, c)
	}
	return bytes.HasPrefix(l, lit8) || bytes.HasPrefix(l, lit9)
}
