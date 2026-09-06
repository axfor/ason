// Restructuring: one input container lands in nested output levels; items becomes data.items.
// The protocol builds the output levels itself with PushObj / PushArr, then Enter().Flat() lets the elements land there directly; on close, Open materializes an empty array and Pop closes it.
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
		return ason.Bail("items is not an array")
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
