// Rename: OnKey returns Pass().As(newName). Only the top-level count is renamed; a nested key with the same name is untouched.
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
