package llm

import (
	"encoding/json"

	"github.com/axfor/ason"
)

// KeyProbeOptions adds Prelude reporting on top of ason.KeyProbeOptions: the values of ModelKey / StreamKey are reported to the integration layer.
type KeyProbeOptions struct {
	Keys      map[string]int
	OnKey     func(t *Transformer, key string, raw []byte) (replacement []byte, replace bool)
	Observe   bool
	ModelKey  string
	StreamKey string
}

type preludeProbe struct {
	*ason.KeyProbe
	opt KeyProbeOptions
	pre Prelude
}

// NewKeyProbe builds a top-level key probe with a Prelude (used by plugins such as model-router / ai-statistics).
func NewKeyProbe(opt KeyProbeOptions) *Transformer {
	p := &preludeProbe{opt: opt}
	p.KeyProbe = ason.NewKeyProbe(ason.KeyProbeOptions{Keys: opt.Keys, OnKey: opt.OnKey, Observe: opt.Observe})
	return NewTransformer(p)
}

func (p *preludeProbe) Prelude() Prelude { return p.pre }

func (p *preludeProbe) OnValue(t *Transformer, raw []byte) {
	k := t.Last()
	if k == p.opt.ModelKey {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			p.pre.Model, p.pre.ModelSeen = s, true
		}
	}
	if k == p.opt.StreamKey {
		var b bool
		if json.Unmarshal(raw, &b) == nil {
			p.pre.Stream, p.pre.StreamSeen = b, true
		}
	}
	p.KeyProbe.OnValue(t, raw)
}
