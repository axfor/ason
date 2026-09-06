package ason

// KeyProbe is a passthrough protocol that only cares about a few top-level keys: the whole value of a matching key is collected
// and handed to a callback (which may replace it in place), every other byte passes through unchanged (whitespace, order and escapes untouched, like sjson's in-place rewrite).
//
// When a key appears more than once only the first occurrence triggers the callback, the rest stay verbatim, as gjson reads the first and sjson rewrites the first.
// Callers that need more state embed *KeyProbe and override OnValue (do their own work, then delegate),
// for example to note "field X has been seen" as values complete.
type KeyProbeOptions struct {
	// Keys: the top-level keys to capture and their byte caps (exceeding a cap bails).
	Keys map[string]int
	// OnKey is called when a value is complete. raw is the raw JSON text of the value; returning (replacement, true) replaces it in
	// place, otherwise it is written back unchanged. The callback may bail with t.Bail.
	OnKey func(t *Transformer, key string, raw []byte) (replacement []byte, replace bool)
	// Observe: observe only, no rewriting. Key and value pass through unchanged and the callback gets a copy (the return value of OnKey is ignored).
	Observe bool
}

// KeyProbe implements Protocol.
type KeyProbe struct {
	BaseProtocol
	Opt  KeyProbeOptions
	seen map[string]bool
	vals map[string][]byte
}

// NewKeyProbe builds the probe protocol (for NewTransformer, or use NewKeyProbeTransformer).
func NewKeyProbe(opt KeyProbeOptions) *KeyProbe {
	return &KeyProbe{Opt: opt, seen: map[string]bool{}, vals: map[string][]byte{}}
}

// NewKeyProbeTransformer is shorthand for NewTransformer(NewKeyProbe(opt)).
func NewKeyProbeTransformer(opt KeyProbeOptions) *Transformer {
	return NewTransformer(NewKeyProbe(opt))
}

// Captured returns the raw values of the captured top-level keys (the first occurrence of each), available after the scan.
func (p *KeyProbe) Captured() map[string][]byte { return p.vals }

func (p *KeyProbe) OnKey(t *Transformer) Action {
	if t.Depth() != 1 {
		return Pass()
	}
	k := t.Last()
	cap, want := p.Opt.Keys[k]
	if !want || p.seen[k] {
		return Pass()
	}
	p.seen[k] = true
	if p.Opt.Observe {
		return Observe(cap)
	}
	return Capture(cap)
}

func (p *KeyProbe) OnValue(t *Transformer, raw []byte) {
	k := t.Last()
	p.vals[k] = append([]byte(nil), raw...)
	if p.Opt.Observe {
		if p.Opt.OnKey != nil {
			p.Opt.OnKey(t, k, raw)
		}
		return
	}
	out := raw
	if p.Opt.OnKey != nil {
		if r, ok := p.Opt.OnKey(t, k, raw); ok {
			out = r
		}
	}
	w := t.W()
	w.KeyRaw(t.KeyRaw())
	w.Raw(out)
}
