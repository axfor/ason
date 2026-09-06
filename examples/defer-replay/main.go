// Replay: when body arrives before kind it is Deferred (bounded); once kind is seen Release dispatches it again, with no assumption about field order.
// When kind is note, body is renamed to text; otherwise it stays as is.
//
//	echo '{"body":"be brief","kind":"note"}' | go run ./examples/defer-replay
package main

import (
	"github.com/axfor/ason"
	"github.com/axfor/ason/examples/internal/run"
)

type kindProto struct {
	ason.BaseProtocol
	kind string
}

func (p *kindProto) OnKey(t *ason.Transformer) ason.Action {
	switch t.Last() {
	case "kind":
		return ason.Capture(64)
	case "body":
		if p.kind == "" {
			return ason.Defer(1 << 20)
		}
		if p.kind == "note" {
			return ason.Pass().As("text")
		}
		return ason.Pass()
	}
	return ason.Pass()
}

func (p *kindProto) OnValue(t *ason.Transformer, raw []byte) {
	if t.Last() == "kind" {
		p.kind, _ = ason.JSONUnquote(raw)
		t.W().KeyRaw(t.KeyRaw())
		t.W().Raw(raw)
		t.Release()
	}
}

func main() {
	run.Main(ason.NewTransformer(&kindProto{}), `{"body":"be brief","kind":"note"}`)
}
