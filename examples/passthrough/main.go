// 透传：BaseProtocol 对一切返回 Pass，没动的字节一个不改（空白、顺序、转义都保留）。
//
//	printf '{\n  "id" : 1 ,\n  "items" : [ { "k" : "v" } ]\n}\n' | go run ./examples/passthrough
package main

import (
	"github.com/axfor/ason"
	"github.com/axfor/ason/examples/internal/run"
)

func main() {
	run.Main(ason.NewTransformer(ason.BaseProtocol{}), "{\n  \"id\" : 1 ,\n  \"items\" : [ { \"k\" : \"v\" } ]\n}\n")
}
