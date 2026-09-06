// 重组：一个输入容器落到多层嵌套输出——items 变成 data.items。
// 协议用 PushObj / PushArr 自建输出层，再 Enter().Flat() 让元素直接落进去；闭合时 Open 物化空数组并 Pop。
//
//	echo '{"id":"m","items":[{"a":1},{"b":2}],"flag":true}' | go run ./examples/restructure
package main

import (
	"github.com/axfor/ason"
	"github.com/axfor/ason/examples/internal/run"
)

type nestProto struct{ ason.BaseProtocol }

func (nestProto) OnKey(t *ason.Transformer) ason.Action {
	if t.Depth() == 1 && t.Last() == "items" {
		return ason.Probe()
	}
	return ason.Pass()
}

func (nestProto) OnStart(t *ason.Transformer, kind ason.ValueKind) ason.Action {
	if kind != ason.KindArray {
		return ason.Bail("items 不是数组")
	}
	w := t.W()
	w.PushObj("data")
	w.PushArr("items")
	return ason.Enter().Flat()
}

func (nestProto) OnLeave(t *ason.Transformer) {
	if t.Depth() == 1 {
		w := t.W()
		w.Open()
		w.Pop()
		w.Pop()
	}
}

func main() {
	run.Main(ason.NewTransformer(nestProto{}), `{"id":"m","items":[{"a":1},{"b":2}],"flag":true}`)
}
