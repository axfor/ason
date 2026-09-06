// Streaming an attachment: only the prefix of the data URL (a small window) is inspected to extract the mime type; the base64 payload streams into another shape and never enters memory.
//
//	echo '{"image":"data:image/png;base64,iVBORw0KGgo=","n":1}' | go run ./examples/attachment
package main

import (
	"bytes"
	"strings"

	"github.com/axfor/ason"
	"github.com/axfor/ason/examples/internal/run"
)

type dataURLProto struct{ ason.BaseProtocol }

func (dataURLProto) OnKey(t *ason.Transformer) ason.Action {
	if t.Last() == "image" {
		return ason.Prefix(128)
	}
	return ason.Pass()
}

func (dataURLProto) OnPrefix(t *ason.Transformer, raw []byte, complete bool) (ason.Action, int) {
	dec, off := ason.UnescapePrefix(raw)
	if !bytes.HasPrefix(dec, []byte("data:")) {
		return ason.Bail("not a data URL"), 0
	}
	comma := bytes.IndexByte(dec, ',')
	if comma < 0 {
		return ason.Bail("data URL header exceeds the window"), 0
	}
	mime := strings.TrimSuffix(string(dec[5:comma]), ";base64")
	w := t.W()
	w.Key("image")
	w.RawString(`{"mime":"` + mime + `","data":"`)
	return ason.Pass().Wrap(nil, []byte(`"}`)), off[comma+1]
}

func main() {
	run.Main(ason.NewTransformer(dataURLProto{}), `{"image":"data:image/png;base64,iVBORw0KGgo=","n":1}`)
}
