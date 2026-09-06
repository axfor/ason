// 子 hook：Enter().Via(hook) 把整棵子树的回调交给另一个 Protocol；容器闭合的 OnLeave 回到发起方。
// 这里 meta 子树里的每个 key 都加前缀 x_，包括更深层由子 hook 自己 Enter 的对象。
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
	if t.Depth() == 2 { // meta 的直接子项：改名；对象值继续进入（仍由本 hook 处理）
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
