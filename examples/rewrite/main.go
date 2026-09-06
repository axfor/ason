// In-place rewrite: KeyProbe captures top-level keys and the callback decides the replacement; every other byte passes through, byte-identical to sjson's in-place rewrite.
//
//	echo '{"items":[{"k":"v"}],"owner":"team/42"}' | go run ./examples/rewrite
package main

import (
	"bytes"
	"fmt"
	"os"

	"github.com/axfor/ason"
	"github.com/axfor/ason/examples/internal/run"
)

func main() {
	tr := ason.NewKeyProbeTransformer(ason.KeyProbeOptions{
		Keys: map[string]int{"owner": 4096},
		OnKey: func(t *ason.Transformer, key string, raw []byte) ([]byte, bool) {
			if i := bytes.IndexByte(raw, '/'); i >= 0 { // "team/42" → "42"
				return append([]byte(`"`), raw[i+1:]...), true
			}
			return nil, false
		},
	})
	run.Main(tr, `{"items":[{"k":"v"}],"owner":"team/42"}`)
	fmt.Fprintf(os.Stderr, "\ncaptured owner: %s\n", tr.Protocol().(*ason.KeyProbe).Captured()["owner"])
}
