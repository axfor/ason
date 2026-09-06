// Passthrough: BaseProtocol returns Pass for everything; untouched bytes stay identical (whitespace, order and escapes preserved).
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
