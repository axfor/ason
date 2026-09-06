// Package conv is an example "chat request conversion" protocol: it converts one common chat request shape into another, streaming.
// It deliberately exercises every path of the engine (Capture / Defer replay / Prefix splitting a data URL / a Via sub-hook /
// Lazy levels / self-built output levels / fields added in Tail): it carries the engine scenario tests and serves as a reference for writing your own protocol.
//
// Input (source shape)                              Output (target shape)
//
//	model                                          model (through MapModel)
//	content of messages[].role == "system"         system: every system text joined with "\n" (omitted when there is none)
//	messages[].content string                      content:[{"type":"text","text":...}]
//	messages[].content array: text parts           {"type":"text","text":...}
//	                        image_url data URLs    {"type":"image","source":{"type":"base64","media_type":...,"data":...}}
//	max_tokens                                     max_tokens (default 1024)
//	stop                                           stop_sequences (an empty array is omitted; elements must be strings)
//	tools[].function{name,description,parameters}  tools[]{name,description,input_schema} (empty parameters omitted)
//	other top-level fields                         dropped
//
// Unsupported (Bail): messages not an array, a message without role, a non-string role, content that is null / a number / an object,
// a non-string stop element, an image that is not a data URL, a data URL header beyond the 512-byte window.
package conv

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/axfor/ason"
)

// Options are the protocol options.
type Options struct {
	// MapModel maps the source model to the target model; nil keeps it unchanged.
	MapModel func(string) string
	// DefaultMaxTokens is the default max_tokens.
	DefaultMaxTokens int
}

const (
	roleWaitCap = 64 << 10 // how much content to hold when it arrives before role
	systemCap   = 1 << 20  // cap on system content captured as a whole
	partWaitCap = 8 << 20  // how much to hold when a part's type arrives after its content (images)
	urlWindow   = 512      // window for the data URL header
	smallCap    = 4 << 10  // model / max_tokens / role / type
)

type proto struct {
	ason.BaseProtocol
	opt      Options
	tools    toolsHook
	model    string
	modelOK  bool
	maxTok   string // raw number text
	system   []string
	m        msg
	msgsSeen bool // the messages array appeared
	outMsgs  int  // (non-system) messages written
}

type msg struct {
	role       string
	roleSeen   bool
	system     bool
	partType   string
	partTypeOK bool
}

// New builds the transformer.
func New(opt Options) *ason.Transformer {
	if opt.DefaultMaxTokens == 0 {
		opt.DefaultMaxTokens = 1024
	}
	if opt.MapModel == nil {
		opt.MapModel = func(s string) string { return s }
	}
	p := &proto{opt: opt}
	tr := ason.NewTransformer(p)
	tr.DupKeyBail = true
	return tr
}

// ---- top level ----

func (p *proto) OnKey(t *ason.Transformer) ason.Action {
	switch t.Depth() {
	case 1:
		switch t.Last() {
		case "model", "max_tokens":
			return ason.Capture(smallCap)
		case "messages", "stop", "tools":
			return ason.Probe()
		}
		return ason.Skip()
	case 3: // messages[i].K
		return p.msgKey(t)
	case 5: // messages[i].content[j].K
		return p.partKey(t)
	case 6: // messages[i].content[j].image_url.K
		return p.urlKey(t)
	}
	return ason.Skip()
}

func (p *proto) OnElem(t *ason.Transformer) ason.Action {
	switch t.Depth() {
	case 2, 4: // messages[i] / stop[i] / content[j]
		return ason.Probe()
	}
	return ason.Skip()
}

func (p *proto) OnStart(t *ason.Transformer, kind ason.ValueKind) ason.Action {
	switch t.Depth() {
	case 1:
		switch t.Last() {
		case "messages":
			if kind != ason.KindArray {
				return ason.Bail("messages is not an array")
			}
			p.msgsSeen = true
			return ason.Enter().Lazy() // all system: Tail adds "messages":[]
		case "stop":
			switch kind {
			case ason.KindArray:
				return ason.Enter().As("stop_sequences").Lazy()
			case ason.KindNull:
				return ason.Skip()
			}
			return ason.Bail("stop is not an array")
		case "tools":
			switch kind {
			case ason.KindArray:
				return ason.Enter().Lazy().Via(&p.tools)
			case ason.KindNull:
				return ason.Skip()
			}
			return ason.Bail("tools is not an array")
		}
	case 2:
		if t.Key(0) == "stop" {
			if kind != ason.KindString {
				return ason.Bail("stop element is not a string")
			}
			return ason.Pass()
		}
		if kind != ason.KindObject {
			return ason.Bail("message is not an object")
		}
		p.m = msg{}
		return ason.Enter().Lazy() // system messages produce no element
	case 3: // content
		return p.contentStart(t, kind)
	case 4: // content[j]
		if kind != ason.KindObject {
			return ason.Skip()
		}
		p.m.partType, p.m.partTypeOK = "", false
		return ason.Enter().Lazy()
	case 5:
		return p.partStart(t, kind)
	}
	return ason.Skip()
}

func (p *proto) OnValue(t *ason.Transformer, raw []byte) {
	w := t.W()
	switch t.Depth() {
	case 1:
		switch t.Last() {
		case "model":
			s, ok := ason.JSONUnquote(raw)
			if !ok {
				t.Bail("model is not a string")
				return
			}
			p.model, p.modelOK = s, true
			w.Key("model")
			w.JSONString(p.opt.MapModel(s)) // write the mapped model in place
		case "max_tokens":
			if !ason.IsIntLiteral(raw) {
				t.Bail("max_tokens is not an integer")
				return
			}
			p.maxTok = string(raw)
		}
	case 3:
		switch t.Last() {
		case "role":
			s, ok := ason.JSONUnquote(raw)
			if !ok {
				t.Bail("role is not a string")
				return
			}
			p.m.role, p.m.roleSeen = s, true
			p.m.system = s == "system"
			if p.m.system {
				t.Release() // re-dispatch the held content under the system rule (into the system buffer)
				return
			}
			p.outMsgs++
			w.Key("role")
			w.Raw(raw)
			t.Release() // the held content can be written now
		case "content": // system content captured as a whole
			p.system = append(p.system, systemText(raw))
		}
	case 5:
		if t.Last() == "type" {
			s, _ := ason.JSONUnquote(raw)
			p.m.partType, p.m.partTypeOK = s, true
			t.Release()
		}
	}
}

func (p *proto) OnLeave(t *ason.Transformer) {
	switch t.Depth() {
	case 2:
		if t.Key(0) != "messages" {
			return
		}
		if !p.m.roleSeen {
			t.Bail("message without role")
		}
	case 4: // part closed: a part with a missing or unknown type is dropped (held content included)
		if !p.m.partTypeOK || (p.m.partType != "text" && p.m.partType != "image_url") {
			t.DropDeferred()
		}
	}
}

func (p *proto) Tail(t *ason.Transformer) {
	w := t.W()
	if !p.modelOK {
		w.Key("model")
		w.JSONString(p.opt.MapModel(""))
	}
	if p.msgsSeen && p.outMsgs == 0 {
		// only system messages (or none): the target shape still needs messages
		w.Key("messages")
		w.RawString("[]")
	}
	w.Key("max_tokens")
	if p.maxTok != "" {
		w.RawString(p.maxTok)
	} else {
		w.Int(p.opt.DefaultMaxTokens)
	}
	if len(p.system) > 0 {
		w.Key("system")
		w.JSONString(strings.Join(p.system, "\n"))
	}
}

// ---- messages ----

func (p *proto) msgKey(t *ason.Transformer) ason.Action {
	switch t.Last() {
	case "role":
		return ason.Capture(smallCap)
	case "content":
		if !p.m.roleSeen {
			return ason.Defer(roleWaitCap) // role unknown yet: hold
		}
		if p.m.system {
			return ason.Capture(systemCap)
		}
		return ason.Probe()
	}
	return ason.Skip()
}

func (p *proto) contentStart(t *ason.Transformer, kind ason.ValueKind) ason.Action {
	switch kind {
	case ason.KindString:
		return ason.Pass().Wrap([]byte(`[{"type":"text","text":`), []byte(`}]`))
	case ason.KindArray:
		return ason.Enter().As("content")
	}
	return ason.Bail("content is neither a string nor an array")
}

func (p *proto) partKey(t *ason.Transformer) ason.Action {
	switch t.Last() {
	case "type":
		return ason.Capture(smallCap)
	case "text", "image_url":
		if !p.m.partTypeOK {
			return ason.Defer(partWaitCap) // type has not arrived yet
		}
		if (t.Last() == "text") != (p.m.partType == "text") {
			return ason.Skip() // field that does not match the type
		}
		return ason.Probe()
	}
	return ason.Skip()
}

func (p *proto) partStart(t *ason.Transformer, kind ason.ValueKind) ason.Action {
	w := t.W()
	switch t.Last() {
	case "text":
		if kind != ason.KindString {
			return ason.Bail("text is not a string")
		}
		w.Key("type")
		w.RawString(`"text"`)
		return ason.Pass()
	case "image_url":
		if kind != ason.KindObject {
			return ason.Bail("image_url is not an object")
		}
		return ason.Enter().Flat()
	}
	return ason.Skip()
}

// depth 6: image_url.url
func (p *proto) OnPrefix(t *ason.Transformer, raw []byte, complete bool) (ason.Action, int) {
	dec, off := ason.UnescapePrefix(raw)
	if !bytes.HasPrefix(dec, []byte("data:")) {
		return ason.Bail("image is not a data URL"), 0
	}
	semi := bytes.IndexByte(dec, ';')
	comma := bytes.IndexByte(dec, ',')
	if semi < 0 || comma < 0 || comma < semi {
		if !complete {
			return ason.Bail("data URL header exceeds the window"), 0
		}
		return ason.Bail("malformed data URL"), 0
	}
	w := t.W()
	w.Key("type")
	w.RawString(`"image"`)
	w.Key("source")
	w.RawString(`{"type":"base64","media_type":`)
	w.JSONString(string(dec[5:semi]))
	w.RawString(`,"data":"`)
	return ason.Pass().Wrap(nil, []byte(`"}`)), off[comma+1]
}

// ---- sub-hook: tools ----

type toolsHook struct {
	ason.BaseProtocol
	nameSeen bool
	fnSeen   bool
}

func (h *toolsHook) OnElem(t *ason.Transformer) ason.Action { return ason.Probe() }

func (h *toolsHook) OnKey(t *ason.Transformer) ason.Action {
	switch t.Depth() {
	case 3:
		if t.Last() == "function" {
			return ason.Probe()
		}
	case 4:
		switch t.Last() {
		case "name", "description", "parameters":
			return ason.Probe()
		}
	default:
		return ason.Pass() // inside parameters: verbatim
	}
	return ason.Skip()
}

func (h *toolsHook) OnStart(t *ason.Transformer, kind ason.ValueKind) ason.Action {
	switch t.Depth() {
	case 2:
		if kind != ason.KindObject {
			return ason.Bail("tools element is not an object")
		}
		h.nameSeen, h.fnSeen = false, false
		return ason.Enter()
	case 3:
		if kind != ason.KindObject {
			return ason.Bail("function is not an object")
		}
		h.fnSeen = true
		return ason.Enter().Flat()
	case 4:
		switch t.Last() {
		case "name":
			if kind != ason.KindString {
				return ason.Bail("name is not a string")
			}
			h.nameSeen = true
			return ason.Pass()
		case "description":
			return ason.Pass()
		case "parameters":
			if kind != ason.KindObject {
				return ason.Bail("parameters is not an object")
			}
			return ason.Enter().As("input_schema").Lazy()
		}
	}
	return ason.Skip()
}

func (h *toolsHook) OnLeave(t *ason.Transformer) {
	if t.Depth() == 2 && (!h.fnSeen || !h.nameSeen) {
		t.W().Key("name")
		t.W().RawString(`""`)
	}
}

// the url at depth 6 is dispatched through the top-level OnKey: handled here
func (p *proto) urlKey(t *ason.Transformer) ason.Action {
	if t.Last() == "url" {
		return ason.Prefix(urlWindow)
	}
	return ason.Skip()
}

// systemText extracts the text of system content: a string as is; for an array, the parts whose type is text and whose text is a string, joined; the rest ignored.
func systemText(raw []byte) string {
	if s, ok := ason.JSONUnquote(raw); ok {
		return s
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return ""
	}
	var sb strings.Builder
	for _, it := range items {
		var part struct {
			Type string          `json:"type"`
			Text json.RawMessage `json:"text"`
		}
		if json.Unmarshal(it, &part) != nil || part.Type != "text" {
			continue
		}
		if s, ok := ason.JSONUnquote(part.Text); ok {
			sb.WriteString(s)
		}
	}
	return sb.String()
}
