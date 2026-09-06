package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

// Streaming conversion protocol OpenAI → Claude.
//
// Derived line by line from the buffered claude.go buildClaudeTextGenRequest. Principles:
//   - long things (messages[].content text, image base64) stream straight through with only a wrapper at each end;
//   - small things (model / role / tools / tool_calls / thinking config) are Captured and run through encoding/json with
//     the same structs as the buffered path, reproducing its omitempty and field shapes byte for byte;
//   - decisions that depend on later fields (content waiting for role, assistant content waiting for tool_calls, a part
//     waiting for type) use a bounded Defer and Bail past the cap: no guessing.
//   - fields absent from the buffered structs are dropped silently there, so they are Skipped here; that matches the buffered path.
//
// Known differences from the buffered path (all more lenient; none produces a valid request with different meaning):
//   - a Skipped field with an invalid type makes the buffered Unmarshal fail as a whole, not here;
//   - an invalid image data URL is skipped with a log line by the buffered path; here it Bails.

type ClaudeOptions struct {
	// MapModel reproduces the buffered mapModel: an error when model is empty or maps to empty. nil = unchanged.
	MapModel func(model string) (string, error)
	// ClaudeCodeMode mirrors the provider setting claudeCodeMode: system becomes an array with cache_control.
	ClaudeCodeMode bool
}

const (
	claudeDefaultMaxTokens        = 4096
	claudeMinThinkingBudgetTokens = 1024
	claudeCodeSystemPrompt        = "You are Claude Code, Anthropic's official CLI for Claude."

	// roleWaitCap: how much content to hold when it arrives before role; past it Bail rather than guess the role.
	roleWaitCap = 64 << 10
	// assistantWaitCap: assistant content has to wait for tool_calls before its shape is known.
	assistantWaitCap = 1 << 20
	// partWaitCap: text / image_url held while type arrives late in a multimodal part.
	partWaitCap = 8 << 20
	smallCap    = 64 << 10
	toolsCap    = 4 << 20
	// systemCap: system content must move to the top level as a whole. The buffered path caps the request body at 100MB
	// (ai-proxy defaultMaxBodyBytes); this matches it, so streaming does not let a single field grow without bound.
	systemCap    = 100 << 20
	urlPrefixWin = 512
)

// ---- small structs aligned field by field with the buffered path (only for rewriting Captured values) ----

type oaiTool struct {
	Type     string      `json:"type"`
	Function oaiFunction `json:"function"`
}
type oaiFunction struct {
	Description string                 `json:"description,omitempty"`
	Name        string                 `json:"name"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
}
type oaiToolCall struct {
	Index    int             `json:"index"`
	Id       string          `json:"id,omitempty"`
	Type     string          `json:"type"`
	Function oaiFunctionCall `json:"function"`
}
type oaiFunctionCall struct {
	Id        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}
type oaiToolChoice struct {
	Type     string      `json:"type"`
	Function oaiFunction `json:"function"`
}
type claudeTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	InputSchema map[string]interface{} `json:"input_schema,omitempty"`
}
type claudeToolChoice struct {
	Type                   string `json:"type"`
	Name                   string `json:"name,omitempty"`
	DisableParallelToolUse bool   `json:"disable_parallel_tool_use,omitempty"`
}
type claudeThinkingConfig struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
	Display      string `json:"display,omitempty"`
}
type claudeOutputConfig struct {
	Effort string          `json:"effort,omitempty"`
	Format json.RawMessage `json:"format,omitempty"`
}
type claudeToolUse struct {
	Type  string                  `json:"type"`
	Id    string                  `json:"id,omitempty"`
	Name  string                  `json:"name,omitempty"`
	Input *map[string]interface{} `json:"input,omitempty"`
}
type claudeToolResult struct {
	Type      string `json:"type"`
	ToolUseId string `json:"tool_use_id,omitempty"`
	Content   string `json:"content"`
}

type claudePart struct {
	typ     string
	typSeen bool
	dead    bool // the buffered path skips this part
	urlSeen bool
	file    []byte
}

type claudeMsg struct {
	role           string
	roleSeen       bool
	roleWritten    bool
	contentSeen    bool
	contentWritten bool
	toolCallId     string
	toolCalls      []byte // raw bytes; nil = not seen
	toolText       string // text content of a tool role message
	finalizing     bool
	part           claudePart
}

type claudeProto struct {
	opt ClaudeOptions

	model         string
	modelSeen     bool
	maxTok        int
	maxCompletion int
	stream        bool
	streamSeen    bool
	temp, topP    []byte
	stopN         int // number of stop array elements (streamed through, only counted)
	reasonEffort  string
	reasonMax     int
	thinkingRaw   []byte
	outputRaw     []byte
	tools         ToolsHook
	toolChoiceRaw []byte
	parallelTC    *bool

	messagesSeen   bool
	inputMsgs      int
	msgCount       int
	msgLevel       int
	openToolResult bool
	sysSeen        bool
	sys            string // decoded text (when content is an array)
	sysRaw         []byte // the raw JSON literal when content is a string, written verbatim
	m              claudeMsg
}

// NewClaude builds the OpenAI → Claude transformer.
func NewClaude(opt ClaudeOptions) *Transformer {
	if opt.MapModel == nil {
		// minimal semantics of the buffered mapModel: a missing model fails
		opt.MapModel = func(m string) (string, error) {
			if m == "" {
				return "", errors.New("missing model in request")
			}
			return m, nil
		}
	}
	p := &claudeProto{opt: opt, tools: ToolsHook{ParamsKey: "input_schema"}}
	t := NewTransformer(p)
	t.DupKeyBail = true // buffered struct decoding is last-wins, which streaming cannot reproduce: fall back
	return t
}

// New keeps the old constructor: a Claude transformer with default options.
func New() *Transformer { return NewClaude(ClaudeOptions{}) }

func (p *claudeProto) Prelude() Prelude {
	return Prelude{Model: p.model, ModelSeen: p.modelSeen, Stream: p.stream, StreamSeen: p.streamSeen}
}

// ---- dispatch ----

func (p *claudeProto) OnKey(t *Transformer) Action {
	switch t.Depth() {
	case 1:
		return p.topKey(t)
	case 3:
		return p.msgKey(t)
	case 5:
		return p.partKey(t)
	case 6:
		return p.imageKey(t)
	}
	return Bail("unexpected path: " + t.PathString())
}

func (p *claudeProto) OnElem(t *Transformer) Action {
	if t.Depth() == 2 && t.Key(0) == "stop" {
		return Probe()
	}
	switch t.Depth() {
	case 2, 4: // messages[i] / messages[i].content[j]
		return Probe()
	}
	return Bail("unexpected array: " + t.PathString())
}

func (p *claudeProto) OnStart(t *Transformer, kind ValueKind) Action {
	w := t.W()
	if t.Depth() == 1 && t.Last() == "tools" {
		switch kind {
		case KindNull: // buffered Tools is nil: not written
			return Skip()
		case KindArray:
			return Enter().Lazy().Via(&p.tools) // empty array: omitted by omitempty; the inside goes to the sub-hook
		}
		return Bail("tools is not an array, the buffered struct decoding fails")
	}
	if t.Key(0) == "stop" {
		switch t.Depth() {
		case 1:
			switch kind {
			case KindArray:
				return Enter().As("stop_sequences").Lazy() // empty array: omitempty
			case KindNull:
				return Skip()
			}
			return Bail("stop is not an array of strings")
		case 2:
			if kind != KindString {
				return Bail("stop is not an array of strings")
			}
			p.stopN++
			return Pass()
		}
	}
	switch t.Depth() {
	case 1: // messages
		if kind != KindArray {
			return Bail("messages is not an array")
		}
		p.msgLevel = w.Level() + 1
		return Enter().Lazy() // all-system: the buffered path writes null, added by Tail
	case 2: // messages[i]
		if kind != KindObject {
			return Bail("message is not an object")
		}
		p.m = claudeMsg{}
		p.inputMsgs++
		return Enter().Lazy() // system / merged tool messages produce no element
	case 3: // messages[i].content (role known, ordinary message)
		switch kind {
		case KindString:
			p.writeRole(t)
			p.m.contentWritten = true
			return Pass()
		case KindArray:
			p.writeRole(t)
			p.m.contentWritten = true
			return Enter()
		}
		// object / scalar / null: the buffered ParseContent yields empty → "content":[], added in OnLeave
		return Skip()
	case 4: // messages[i].content[j]
		if kind != KindObject {
			return Skip() // buffered path: non-map elements are skipped
		}
		p.m.part = claudePart{}
		return Enter().Lazy() // a part the buffered path skips leaves no trace
	case 5:
		pt := &p.m.part
		switch t.Last() {
		case "text":
			if kind != KindString {
				pt.dead = true // buffered path: the whole part is skipped when text is not a string
				return Skip()
			}
			w.Key("type")
			w.RawString(`"text"`)
			return Prefix(1) // buffered Text has omitempty: no text key for an empty string, so peek first

		case "image_url":
			if kind != KindObject {
				pt.dead = true
				return Skip()
			}
			return Enter().Flat()
		}
	}
	return Bail("unexpected Probe: " + t.PathString())
}

func (p *claudeProto) topKey(t *Transformer) Action {
	switch t.Last() {
	case "model":
		return Capture(4 << 10)
	case "messages", "tools", "stop":
		return Probe() // array: stream element by element
	case "max_tokens", "max_completion_tokens", "reasoning_max_tokens", "temperature", "top_p":
		return Capture(64)
	case "stream", "parallel_tool_calls":
		return Capture(16)
	case "reasoning_effort":
		return Capture(256)
	case "claude_thinking":
		return Capture(4 << 10)
	case "claude_output_config":
		return Capture(smallCap)
	case "tool_choice":
		return Capture(4 << 10)
	}
	// no Claude counterpart, or not defined in the buffered chatCompletionRequest: dropped there as well
	return Skip()
}

func (p *claudeProto) msgKey(t *Transformer) Action {
	m := &p.m
	switch t.Last() {
	case "role":
		return Capture(256)
	case "content":
		if !m.roleSeen {
			return Defer(roleWaitCap)
		}
		m.contentSeen = true
		switch m.role {
		case "system", "developer":
			return Capture(systemCap) // must move to the top-level system as a whole; the cap matches the buffered body limit
		case "tool":
			return Capture(assistantWaitCap)
		case "assistant":
			if !m.finalizing {
				return Defer(assistantWaitCap) // the shape depends on whether tool_calls is present
			}
		}
		return Probe()
	case "tool_calls":
		return Capture(assistantWaitCap)
	case "tool_call_id":
		return Capture(4 << 10)
	case "claude_content_blocks":
		return Bail("claude_content_blocks needs a struct round trip, not reproduced by streaming")
	}
	return Skip() // name / audio / refusal / reasoning* / function_call ...: not read by the buffered path
}

func (p *claudeProto) partKey(t *Transformer) Action {
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
	case "file":
		if !pt.typSeen {
			return Defer(smallCap)
		}
		if pt.typ != "file" {
			return Skip()
		}
		return Capture(smallCap)
	}
	return Skip() // cache_control / input_audio / unknown: not read by the buffered path
}

func (p *claudeProto) imageKey(t *Transformer) Action {
	if t.Last() == "url" {
		p.m.part.urlSeen = true
		return Prefix(urlPrefixWin)
	}
	return Skip() // detail and the like
}

// ---- values complete ----

func (p *claudeProto) OnValue(t *Transformer, raw []byte) {
	switch t.Depth() {
	case 1:
		p.topValue(t, raw)
	case 3:
		p.msgValue(t, raw)
	case 5:
		p.partValue(t, raw)
	}
}

func (p *claudeProto) topValue(t *Transformer, raw []byte) {
	isNull := string(raw) == "null"
	switch t.Last() {
	case "model":
		s, ok := jsonUnquote(raw)
		if !ok {
			t.Bail("model is not a string")
			return
		}
		p.model, p.modelSeen = s, true
	case "max_tokens", "max_completion_tokens", "reasoning_max_tokens":
		if isNull {
			return
		}
		if !isIntLiteral(raw) {
			t.Bail(t.Last() + " is not an integer")
			return
		}
		n := atoi(raw)
		switch t.Last() {
		case "max_tokens":
			p.maxTok = n
		case "max_completion_tokens":
			p.maxCompletion = n
		default:
			p.reasonMax = n
		}
	case "stream":
		switch string(raw) {
		case "true":
			p.stream = true
		case "false", "null":
			p.stream = false
		default:
			t.Bail("stream is not a boolean")
			return
		}
		p.streamSeen = !isNull
	case "parallel_tool_calls":
		switch string(raw) {
		case "true":
			v := true
			p.parallelTC = &v
		case "false":
			v := false
			p.parallelTC = &v
		case "null":
		default:
			t.Bail("parallel_tool_calls is not a boolean")
		}
	case "temperature", "top_p":
		if isNull {
			return
		}
		if !isNumLiteral(raw) {
			t.Bail(t.Last() + " is not a number")
			return
		}
		// the buffered path stores a float64 and Marshals it again: 1e0 → 1, 0.70 → 0.7. Same route here, byte-aligned
		var f float64
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Bail(t.Last() + " is not a number")
			return
		}
		cp, _ := json.Marshal(f)
		if t.Last() == "temperature" {
			p.temp = cp
		} else {
			p.topP = cp
		}
	case "reasoning_effort":
		if isNull {
			return
		}
		s, ok := jsonUnquote(raw)
		if !ok {
			t.Bail("reasoning_effort is not a string")
			return
		}
		p.reasonEffort = s
	case "claude_thinking":
		if !isNull {
			p.thinkingRaw = append([]byte(nil), raw...)
		}
	case "claude_output_config":
		if !isNull {
			p.outputRaw = append([]byte(nil), raw...)
		}
	case "tool_choice":
		if !isNull {
			p.toolChoiceRaw = append([]byte(nil), raw...)
		}
	}
}

func (p *claudeProto) msgValue(t *Transformer, raw []byte) {
	m := &p.m
	switch t.Last() {
	case "role":
		s, ok := jsonUnquote(raw)
		if !ok {
			t.Bail("role is not a string")
			return
		}
		m.role, m.roleSeen = s, true
		// buffered path: system messages are skipped with continue and do not affect "the previous output message",
		// so tool_result merging can reach across a system message.
		if p.openToolResult && s != "tool" && s != "system" && s != "developer" {
			p.closeToolResult(t)
		}
		if s != "assistant" && len(t.Deferred()) > 0 {
			t.Release() // content arrived first: the role is known now, replay
		}
	case "content":
		// only the content of system / developer / tool messages is Captured here
		switch m.role {
		case "system", "developer":
			p.sysSeen = true
			if len(raw) > 0 && raw[0] == '"' {
				p.sysRaw = append([]byte(nil), raw...) // string: not decoded, written verbatim
				p.sys = ""
			} else {
				p.sysRaw = nil
				p.sys = stringContent(t, raw)
			}
		case "tool":
			m.toolText = toolResultText(t, raw)
		}
	case "tool_calls":
		if string(raw) != "null" {
			m.toolCalls = append([]byte(nil), raw...)
		}
	case "tool_call_id":
		s, ok := jsonUnquote(raw)
		if !ok {
			t.Bail("tool_call_id is not a string")
			return
		}
		m.toolCallId = s
	}
}

func (p *claudeProto) partValue(t *Transformer, raw []byte) {
	pt := &p.m.part
	switch t.Last() {
	case "type":
		pt.typSeen = true
		s, ok := jsonUnquote(raw)
		if !ok {
			pt.dead = true // buffered switch contentMap["type"] matches nothing
			t.DropDeferred()
			return
		}
		pt.typ = s
		switch s {
		case "text", "image_url", "file":
			if len(t.Deferred()) > 0 {
				t.Release()
			}
		default:
			pt.dead = true // input_audio is explicitly unsupported by the buffered path; the rest matches nothing
			t.DropDeferred()
		}
	case "file":
		pt.file = append([]byte(nil), raw...)
	}
}

// OnPrefix: the prefix window of image_url.url. Reproduces the buffered splitting of data: URLs.
func (p *claudeProto) OnPrefix(t *Transformer, raw []byte, complete bool) (Action, int) {
	w := t.W()
	if t.Depth() == 5 && t.Last() == "text" {
		if complete && len(raw) == 0 {
			return Skip(), 0 // {"type":"text"}, consistent with the buffered omitempty
		}
		w.Key("text")
		return Pass().Wrap(lit0, lit0), 0
	}
	dec, off := unescapePrefix(raw)
	if !bytes.HasPrefix(dec, lit1) {
		w.Key("type")
		w.RawString(`"image"`)
		w.Key("source")
		if complete && len(dec) == 0 {
			w.RawString(`{"type":"url"}`) // Url omitempty
			return Skip(), 0
		}
		return Pass().Wrap(lit2, lit3), 0
	}
	semi := bytes.IndexByte(dec, ';')
	if semi < 0 {
		if complete {
			return Bail("invalid image url format, the buffered path skips this part"), 0
		}
		return Bail("data URL header exceeds the prefix window"), 0
	}
	media := string(dec[5:semi])
	rest := dec[semi+1:]
	resumeDec := semi + 1
	if !complete && len(rest) < len("base64,") {
		return Bail("data URL header cut at the window boundary"), 0
	}
	if bytes.HasPrefix(rest, lit4) {
		resumeDec += len("base64,")
	}
	resume := off[resumeDec]
	dataEmpty := complete && resume >= len(raw)
	if !complete && resume >= len(raw) {
		return Bail("data URL payload cut at the window boundary"), 0
	}
	w.Key("type")
	w.RawString(`"image"`)
	w.Key("source")
	var pre []byte
	pre = append(pre, `{"type":"base64"`...)
	if media != "" {
		pre = append(pre, `,"media_type":`...)
		pre = appendJSONString(pre, media)
	}
	if dataEmpty {
		w.Raw(pre)
		w.Byte('}')
		return Skip(), 0
	}
	pre = append(pre, `,"data":"`...)
	return Pass().Wrap(pre, lit3), resume
}

// ---- containers closing ----

func (p *claudeProto) OnLeave(t *Transformer) {
	w := t.W()
	if t.Key(0) == "tools" || t.Key(0) == "stop" {
		return // array closed: nothing to do (the inside is handled by the sub-hook)
	}
	switch t.Depth() {
	case 1: // messages
		if p.openToolResult {
			p.closeToolResult(t)
		}
		p.messagesSeen = true
		if p.inputMsgs == 0 {
			t.Bail("no message found in the request body")
		}
	case 2: // messages[i]
		p.finishMessage(t)
	case 3: // messages[i].content array: the buffered path always materializes []
		w.Open()
	case 4: // part
		pt := &p.m.part
		if pt.dead || !pt.typSeen {
			t.DropDeferred()
			return
		}
		if pt.typ == "file" && pt.file != nil {
			var obj map[string]interface{}
			if err := json.Unmarshal(pt.file, &obj); err != nil {
				t.Bail("file is not an object")
				return
			}
			id, ok := obj["file_id"].(string)
			if !ok {
				t.Bail("file.file_id missing, the buffered path panics")
				return
			}
			w.Key("type")
			w.RawString(`"file"`)
			w.Key("source")
			w.RawString(`{"type":"url"`)
			if id != "" {
				w.RawString(`,"file_id":`)
				w.JSONString(id)
			}
			w.Byte('}')
		}
	case 5: // image_url
		if !p.m.part.urlSeen {
			t.Bail("image_url.url missing, the buffered path panics")
		}
	}
}

func (p *claudeProto) writeRole(t *Transformer) {
	if p.m.roleWritten {
		return
	}
	p.m.roleWritten = true
	p.msgCount++
	w := t.W()
	w.Key("role")
	w.JSONString(p.m.role)
}

func (p *claudeProto) closeToolResult(t *Transformer) {
	t.W().RawString("]}")
	p.openToolResult = false
}

// finishMessage finalizes a message when it closes.
func (p *claudeProto) finishMessage(t *Transformer) {
	m := &p.m
	w := t.W()
	if !m.roleSeen {
		m.role, m.roleSeen = "", true // buffered Role zero value
		if p.openToolResult {
			p.closeToolResult(t)
		}
	}
	m.finalizing = true
	switch m.role {
	case "system", "developer":
		if len(t.Deferred()) > 0 {
			t.ReleaseNow()
		}
		if !m.contentSeen {
			p.sysSeen, p.sys, p.sysRaw = true, "", nil
		}
		return
	case "tool":
		if len(t.Deferred()) > 0 {
			t.ReleaseNow()
			if t.Dead() {
				return
			}
		}
		blk, _ := json.Marshal(claudeToolResult{Type: "tool_result", ToolUseId: m.toolCallId, Content: m.toolText})
		if p.openToolResult {
			w.Byte(',')
			w.Raw(blk)
			return
		}
		if !w.ElemAt(p.msgLevel) {
			t.Bail("tool message written at an unexpected position")
			return
		}
		w.RawString(`{"role":"user","content":[`)
		w.Raw(blk)
		p.openToolResult = true
		p.msgCount++
		return
	case "assistant":
		if m.toolCalls != nil {
			var tcs []oaiToolCall
			if err := json.Unmarshal(m.toolCalls, &tcs); err != nil {
				t.Bail("tool_calls failed to decode: " + err.Error())
				return
			}
			if len(tcs) > 0 {
				p.writeAssistantWithTools(t, tcs)
				return
			}
		}
	}
	// ordinary message (user / assistant without tool_calls / other roles)
	if len(t.Deferred()) > 0 {
		t.ReleaseNow()
		if t.Dead() {
			return
		}
	}
	p.writeRole(t)
	if !m.contentWritten {
		w.Key("content")
		w.RawString("[]")
	}
}

func (p *claudeProto) writeAssistantWithTools(t *Transformer, tcs []oaiToolCall) {
	w := t.W()
	p.writeRole(t)
	w.Key("content")
	w.Byte('[')
	first := true
	for _, kv := range t.Deferred() {
		if kv.Key != "content" {
			continue
		}
		// buffered path: IsStringContent && StringContent != ""
		if len(kv.Raw) > 2 && kv.Raw[0] == '"' {
			w.RawString(`{"type":"text","text":`)
			w.Raw(kv.Raw)
			w.Byte('}')
			first = false
		}
	}
	t.DropDeferred()
	for _, tc := range tcs {
		var input map[string]interface{}
		if tc.Function.Arguments != "" {
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &input); err != nil {
				input = make(map[string]interface{})
			}
		} else {
			input = make(map[string]interface{})
		}
		blk, _ := json.Marshal(claudeToolUse{Type: "tool_use", Id: tc.Id, Name: tc.Function.Name, Input: &input})
		if !first {
			w.Byte(',')
		}
		w.Raw(blk)
		first = false
	}
	w.Byte(']')
}

// ---- tail ----

func (p *claudeProto) Tail(t *Transformer) {
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
	if mapped != "" {
		w.Key("model")
		w.JSONString(mapped)
	}
	if p.msgCount == 0 {
		w.Key("messages")
		w.RawString("null") // buffered nil slice
	}
	// system
	if p.opt.ClaudeCodeMode {
		w.Key("system")
		w.RawString(`[{"type":"text"`)
		switch {
		case !p.sysSeen:
			w.RawString(`,"text":`)
			w.JSONString(claudeCodeSystemPrompt)
		case p.sysRaw != nil:
			if string(p.sysRaw) != `""` { // Text omitempty
				w.RawString(`,"text":`)
				w.Raw(p.sysRaw)
			}
		case p.sys != "":
			w.RawString(`,"text":`)
			w.JSONString(p.sys)
		}
		w.RawString(`,"cache_control":{"type":"ephemeral"}}]`)
	} else if p.sysSeen {
		w.Key("system")
		if p.sysRaw != nil {
			w.Raw(p.sysRaw)
		} else {
			w.JSONString(p.sys)
		}
	}
	maxTokens := p.maxTok
	if p.maxCompletion > 0 {
		maxTokens = p.maxCompletion
	}
	if maxTokens == 0 {
		maxTokens = claudeDefaultMaxTokens
	}
	w.Key("max_tokens")
	w.Int(maxTokens)
	if p.stream {
		w.Key("stream")
		w.RawString("true")
	}
	if p.temp != nil && !isZeroNum(p.temp) {
		w.Key("temperature")
		w.Raw(p.temp)
	}
	if p.topP != nil && !isZeroNum(p.topP) {
		w.Key("top_p")
		w.Raw(p.topP)
	}
	// thinking
	var thinking *claudeThinkingConfig
	if p.thinkingRaw != nil {
		var cfg claudeThinkingConfig
		if err := json.Unmarshal(p.thinkingRaw, &cfg); err != nil {
			t.Bail("claude_thinking failed to decode")
			return
		}
		thinking = &cfg
	}
	if thinking == nil && (p.reasonEffort != "" || p.reasonMax > 0) {
		var budget int
		if p.reasonMax > 0 {
			budget = p.reasonMax
		} else {
			switch p.reasonEffort {
			case "low":
				budget = 1024
			case "medium":
				budget = 8192
			case "high":
				budget = 16384
			default:
				budget = 8192
			}
		}
		if budget < claudeMinThinkingBudgetTokens {
			budget = claudeMinThinkingBudgetTokens
		}
		if budget >= maxTokens {
			budget = maxTokens - 1
		}
		if budget >= claudeMinThinkingBudgetTokens {
			thinking = &claudeThinkingConfig{Type: "enabled", BudgetTokens: budget}
		}
	}
	// tool_choice
	if p.toolChoiceRaw != nil {
		p.writeToolChoice(t, thinking)
		if t.Dead() {
			return
		}
	}
	if thinking != nil {
		b, _ := json.Marshal(thinking)
		w.Key("thinking")
		w.Raw(b)
	}
	if p.outputRaw != nil {
		var cfg claudeOutputConfig
		if err := json.Unmarshal(p.outputRaw, &cfg); err != nil {
			t.Bail("claude_output_config failed to decode")
			return
		}
		b, _ := json.Marshal(&cfg)
		w.Key("output_config")
		w.Raw(b)
	}
}

func (p *claudeProto) writeToolChoice(t *Transformer, thinking *claudeThinkingConfig) {
	w := t.W()
	var any interface{}
	if err := json.Unmarshal(p.toolChoiceRaw, &any); err != nil {
		t.Bail("tool_choice failed to decode")
		return
	}
	if any == nil {
		return
	}
	parallel := true
	if p.parallelTC != nil {
		parallel = *p.parallelTC
	}
	hasThinking := thinking != nil && thinking.Type != "" && thinking.Type != "disabled"

	var tcStr string
	var tcObj *oaiToolChoice
	switch v := any.(type) {
	case string:
		tcStr = v
	default:
		b, err := json.Marshal(any)
		if err == nil {
			var parsed oaiToolChoice
			if json.Unmarshal(b, &parsed) == nil {
				tcObj = &parsed
			}
		}
	}
	choiceType := tcStr
	if choiceType == "" && tcObj != nil {
		choiceType = tcObj.Type
	}
	var out *claudeToolChoice
	if !hasThinking && tcObj != nil && tcObj.Type == "function" && tcObj.Function.Name != "" {
		out = &claudeToolChoice{Name: tcObj.Function.Name, Type: "tool", DisableParallelToolUse: !parallel}
	} else if choiceType != "" {
		switch choiceType {
		case "required":
			choiceType = "any"
		case "function":
			choiceType = "auto"
		}
		if hasThinking && (choiceType == "any" || choiceType == "tool") {
			choiceType = "auto"
		}
		out = &claudeToolChoice{Type: choiceType}
		if choiceType != "none" {
			out.DisableParallelToolUse = !parallel
		}
	}
	if out != nil {
		b, _ := json.Marshal(out)
		w.Key("tool_choice")
		w.Raw(b)
	}
}

// ---- reproductions of the buffered StringContent / ParseContent (only for small Captured values) ----

// stringContent reproduces chatMessage.StringContent: a string as is; an array joins its text parts, each followed by "\n"; anything else "".
func stringContent(t *Transformer, raw []byte) string {
	if len(raw) > 0 && raw[0] == '"' {
		s, ok := jsonUnquote(raw)
		if !ok {
			t.Bail("invalid content string")
		}
		return s
	}
	if len(raw) > 0 && raw[0] == '[' {
		var items []interface{}
		if err := json.Unmarshal(raw, &items); err != nil {
			t.Bail("invalid content array")
			return ""
		}
		var sb strings.Builder
		for _, it := range items {
			m, ok := it.(map[string]interface{})
			if !ok {
				continue
			}
			if m["type"] == "text" {
				if s, ok := m["text"].(string); ok {
					sb.WriteString(s)
					sb.WriteString("\n")
				}
			}
		}
		return sb.String()
	}
	return ""
}

// toolResultText reproduces the tool role branch: a string as is; otherwise the text parts of ParseContent joined with "\n".
func toolResultText(t *Transformer, raw []byte) string {
	if len(raw) > 0 && raw[0] == '"' {
		s, ok := jsonUnquote(raw)
		if !ok {
			t.Bail("invalid content string")
		}
		return s
	}
	if len(raw) > 0 && raw[0] == '[' {
		var items []interface{}
		if err := json.Unmarshal(raw, &items); err != nil {
			t.Bail("invalid content array")
			return ""
		}
		var parts []string
		for _, it := range items {
			m, ok := it.(map[string]interface{})
			if !ok {
				continue
			}
			if m["type"] == "text" {
				if s, ok := m["text"].(string); ok {
					parts = append(parts, s)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func atoi(b []byte) int {
	n, neg := 0, false
	for i, c := range b {
		if i == 0 && c == '-' {
			neg = true
			continue
		}
		if i == 0 && c == '+' {
			continue
		}
		n = n*10 + int(c-'0')
	}
	if neg {
		return -n
	}
	return n
}
