package llm

import (
	"encoding/json"

	"github.com/axfor/ason"
)

// KeyProbeOptions 在 ason.KeyProbeOptions 之上加上 Prelude 报告：把 ModelKey / StreamKey 的值报告给集成层。
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

// NewKeyProbe 构造带 Prelude 的顶层 key 探针（model-router / ai-statistics 这类插件用）。
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
