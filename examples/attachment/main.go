// 附件流式：data URL 只看前缀（一个小窗口）拆出 mime，base64 主体直通到另一个形状里，永不进内存。
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
		return ason.Bail("不是 data URL"), 0
	}
	comma := bytes.IndexByte(dec, ',')
	if comma < 0 {
		return ason.Bail("data URL 头超出窗口"), 0
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
