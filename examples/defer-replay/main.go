// 回放：body 先于 kind 到达时 Defer 起来（有上限），见到 kind 后 Release 重新派发——不对字段顺序做假设。
// kind 为 note 时 body 改名为 text，否则原样。
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
