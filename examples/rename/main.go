// 改名：OnKey 返回 Pass().As(新名)。只改顶层的 count，嵌套里的同名 key 不动。
//
//	echo '{"id":"m","count":10,"n":{"count":1}}' | go run ./examples/rename
package main

import (
	"github.com/axfor/ason"
	"github.com/axfor/ason/examples/internal/run"
)

type renameProto struct{ ason.BaseProtocol }

func (renameProto) OnKey(t *ason.Transformer) ason.Action {
	if t.Depth() == 1 && t.Last() == "count" {
		return ason.Pass().As("total")
	}
	return ason.Pass()
}

func main() {
	run.Main(ason.NewTransformer(renameProto{}), `{"id":"m","count":10,"n":{"count":1}}`)
}
