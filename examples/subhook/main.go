// Sub-hook: Enter().Via(hook) hands the callbacks of a whole subtree to another Protocol; OnLeave of the container goes back to the issuer.
// Here every key inside the meta subtree gets the prefix x_, including deeper objects the sub-hook enters itself.
//
//	echo '{"meta":{"a":1,"b":{"c":2}},"d":3}' | go run ./examples/subhook
package main

import (
	"github.com/axfor/ason"
	"github.com/axfor/ason/examples/internal/run"
)

type prefixKeys struct {
	ason.BaseProtocol
	prefix string
}

func (h *prefixKeys) OnKey(t *ason.Transformer) ason.Action {
	if t.Depth() == 2 { // direct children of meta: rename; object values are entered (still handled by this hook)
		return ason.Probe()
	}
	return ason.Pass().As(h.prefix + t.Last())
}

func (h *prefixKeys) OnStart(t *ason.Transformer, kind ason.ValueKind) ason.Action {
	if kind == ason.KindObject {
		return ason.Enter().As(h.prefix + t.Last())
	}
	return ason.Pass().As(h.prefix + t.Last())
}

type viaProto struct {
	ason.BaseProtocol
	meta prefixKeys
}

func (p *viaProto) OnKey(t *ason.Transformer) ason.Action {
	if t.Depth() == 1 && t.Last() == "meta" {
		return ason.Probe()
	}
	return ason.Pass()
}

func (p *viaProto) OnStart(t *ason.Transformer, kind ason.ValueKind) ason.Action {
	return ason.Enter().Via(&p.meta)
}

func main() {
	run.Main(ason.NewTransformer(&viaProto{meta: prefixKeys{prefix: "x_"}}), `{"meta":{"a":1,"b":{"c":2}},"d":3}`)
}
