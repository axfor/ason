// 脱敏：Prefix(n) 只攒字符串的前 n 字节，写出前缀后 Skip 掉剩余部分——不管多长的密钥都不进内存。
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
