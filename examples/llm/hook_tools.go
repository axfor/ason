package llm

// Reusable sub-hook: element-by-element streaming of the OpenAI `tools` array.
//
// The buffered Claude and Gemini paths both decode `tools[i].function` into the same function struct
// (description omitempty, name always present, parameters omitted when it is an empty map or nil) and wrap it their own way.
// This turns "pass elements through, keep the inside of parameters verbatim, omitempty semantics" into a part a protocol can mount:
// the protocol does `Enter().Via(&hook)` on the tools array and only decides the outer wrapping and the output name of parameters.
//
// The tools array itself is assumed at path depth 1 (t.Key(0) == "tools"), elements at depth 2, function at depth 3,
// function fields at depth 4 and the inside of parameters at depth ≥ 5. OnLeave of the array goes back to the protocol (Gemini pops the level it built).
type ToolsHook struct {
	BaseProtocol

	// ParamsKey is the output key of parameters (input_schema for Claude, parameters for Gemini).
	ParamsKey string

	elem struct {
		nameSeen bool
		fnSeen   bool
	}
}

// OnElem: tools[i]
func (h *ToolsHook) OnElem(t *Transformer) Action { return Probe() }

// OnKey: keys under tools (from depth 3)
func (h *ToolsHook) OnKey(t *Transformer) Action {
	switch t.Depth() {
	case 3: // tools[i].K: the buffered tool struct reads only function (type is not written)
		if t.Last() == "function" {
			return Probe()
		}
		return Skip()
	case 4: // tools[i].function.K
		switch t.Last() {
		case "name", "description", "parameters":
			return Probe()
		}
		return Skip()
	}
	return Pass() // inside parameters: verbatim
}

// OnStart: value kind decisions
func (h *ToolsHook) OnStart(t *Transformer, kind ValueKind) Action {
	switch t.Depth() {
	case 2: // tools[i]
		if kind == KindNull { // decoded as a zero-value struct by the buffered path, still written as an element
			w := t.W()
			w.Elem()
			w.RawString(`{"name":""}`)
			return Skip()
		}
		if kind != KindObject {
			return Bail("tools element is not an object, the buffered struct decoding fails")
		}
		h.elem.nameSeen, h.elem.fnSeen = false, false
		return Enter() // the buffered path always writes this element ("name":"" even when function is missing)
	case 3: // function
		switch kind {
		case KindObject:
			h.elem.fnSeen = true
			return Enter().Flat()
		case KindNull: // zero-value struct, same as missing
			return Skip()
		}
		return Bail("tools[].function is not an object, the buffered struct decoding fails")
	case 4:
		switch t.Last() {
		case "name":
			if kind != KindString {
				return Bail("tools[].function.name is not a string, the buffered struct decoding fails")
			}
			h.elem.nameSeen = true
			return Pass()
		case "description":
			if kind != KindString {
				return Bail("tools[].function.description is not a string, the buffered struct decoding fails")
			}
			return Prefix(1) // omitempty: an empty string is not written
		case "parameters":
			switch kind {
			case KindObject:
				return Enter().As(h.ParamsKey).Lazy() // empty object: omitted by omitempty
			case KindNull:
				return Skip()
			}
			return Bail("tools[].function.parameters is not an object, the buffered struct decoding fails")
		}
	}
	return Pass()
}

// OnPrefix: the empty-string decision for description
func (h *ToolsHook) OnPrefix(t *Transformer, raw []byte, complete bool) (Action, int) {
	if complete && len(raw) == 0 {
		return Skip(), 0
	}
	t.W().KeyRaw(t.KeyRaw())
	return Pass().Wrap(lit0, lit0), 0
}

// OnLeave: when tools[i] closes, add the name the buffered struct writes without omitempty
func (h *ToolsHook) OnLeave(t *Transformer) {
	if t.Depth() == 2 && (!h.elem.fnSeen || !h.elem.nameSeen) {
		w := t.W()
		w.Key("name")
		w.RawString(`""`)
	}
}
