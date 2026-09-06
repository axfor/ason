// Redaction: Prefix(n) collects only the first n bytes of the string, writes the prefix and Skips the rest; a secret of any length never enters memory.
//
//	echo '{"api_key":"sk-1234567890abcdef","user":"u"}' | go run ./examples/redact
package main

import (
	"github.com/axfor/ason"
	"github.com/axfor/ason/examples/internal/run"
)

type redactProto struct{ ason.BaseProtocol }

func (redactProto) OnKey(t *ason.Transformer) ason.Action {
	if t.Last() == "api_key" {
		return ason.Prefix(4)
	}
	return ason.Pass()
}

func (redactProto) OnPrefix(t *ason.Transformer, raw []byte, complete bool) (ason.Action, int) {
	w := t.W()
	w.KeyRaw(t.KeyRaw())
	w.RawString(`"` + string(raw) + `***"`)
	return ason.Skip(), 0
}

func main() {
	run.Main(ason.NewTransformer(redactProto{}), `{"api_key":"sk-1234567890abcdef","user":"u"}`)
}
