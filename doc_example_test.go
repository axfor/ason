package ason_test

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/axfor/ason"
)

// feed feeds the input in chunks and finishes; returns the output and the bail reason.
func feed(tr *ason.Transformer, in string, chunk int) (string, string) {
	var out bytes.Buffer
	for i := 0; i < len(in); i += chunk {
		j := i + chunk
		if j > len(in) {
			j = len(in)
		}
		tr.Write([]byte(in[i:j]))
		out.Write(tr.Out())
	}
	out.Write(tr.Finish())
	if bad, why := tr.Unsupported(); bad {
		return "", why
	}
	return out.String(), ""
}

// Passthrough: BaseProtocol returns Pass for everything; untouched bytes stay identical (whitespace, order and escapes preserved).
func Example_passthrough() {
	in := "{\n  \"id\" : 1 ,\n  \"items\" : [ { \"k\" : \"v\" } ]\n}\n"
	out, _ := feed(ason.NewTransformer(ason.BaseProtocol{}), in, 3)
	fmt.Println(out == in)
	// Output: true
}

// Rename: OnKey returns Pass().As(newName). Only the top level is renamed; a nested key with the same name is untouched.
type renameProto struct{ ason.BaseProtocol }

func (renameProto) OnKey(t *ason.Transformer) ason.Action {
	if t.Depth() == 1 && t.Last() == "count" {
		return ason.Pass().As("total")
	}
	return ason.Pass()
}

func Example_rename() {
	out, _ := feed(ason.NewTransformer(renameProto{}), `{"id":"m","count":10,"n":{"count":1}}`, 5)
	fmt.Println(out)
	// Output: {"id":"m","total":10,"n":{"count":1}}
}

// In-place rewrite: KeyProbe captures top-level keys and the callback decides the replacement; every other byte passes through, and the captured raw values can be read afterwards.
func Example_rewrite() {
	tr := ason.NewKeyProbeTransformer(ason.KeyProbeOptions{
		Keys: map[string]int{"owner": 4096},
		OnKey: func(t *ason.Transformer, key string, raw []byte) ([]byte, bool) {
			if i := bytes.IndexByte(raw, '/'); i >= 0 { // "team/42" → "42"
				return append([]byte(`"`), raw[i+1:]...), true
			}
			return nil, false
		},
	})
	out, _ := feed(tr, `{"items":[{"k":"v"}],"owner":"team/42"}`, 7)
	fmt.Println(out)
	fmt.Println(string(tr.Protocol().(*ason.KeyProbe).Captured()["owner"]))
	// Output:
	// {"items":[{"k":"v"}],"owner":"42"}
	// "team/42"
}

// Redaction: Prefix(n) collects only the first n bytes of the string, writes the prefix and Skips the rest; a long string never enters memory.
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

func Example_redact() {
	out, _ := feed(ason.NewTransformer(redactProto{}), `{"api_key":"sk-1234567890abcdef","user":"u"}`, 4)
	fmt.Println(out)
	// Output: {"api_key":"sk-1***","user":"u"}
}

// Restructuring: one input container lands in nested output levels; items becomes data.items.
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
	return ason.Enter().Flat() // elements land directly in the self-built data.items
}
func (nestProto) OnLeave(t *ason.Transformer) {
	if t.Depth() == 1 {
		w := t.W()
		w.Open() // an empty array is materialized as well
		w.Pop()
		w.Pop()
	}
}

func Example_restructure() {
	out, _ := feed(ason.NewTransformer(nestProto{}), `{"id":"m","items":[{"a":1},{"b":2}],"flag":true}`, 6)
	fmt.Println(out)
	// Output: {"id":"m","data":{"items":[{"a":1},{"b":2}]},"flag":true}
}

// Replay: when body arrives before kind it is Deferred; once kind is seen Release dispatches it again, with no assumption about field order.
type kindProto struct {
	ason.BaseProtocol
	kind string
}

func (p *kindProto) OnKey(t *ason.Transformer) ason.Action {
	switch t.Last() {
	case "kind":
		return ason.Capture(64)
	case "body":
		if p.kind == "" {
			return ason.Defer(1 << 20) // kind unknown yet: hold (bounded)
		}
		if p.kind == "note" {
			return ason.Pass().As("text")
		}
		return ason.Pass()
	}
	return ason.Pass()
}
func (p *kindProto) OnValue(t *ason.Transformer, raw []byte) {
	if t.Last() == "kind" {
		p.kind, _ = ason.JSONUnquote(raw)
		t.W().KeyRaw(t.KeyRaw())
		t.W().Raw(raw)
		t.Release() // replay the Deferred body once the current value ends
	}
}

func Example_deferReplay() {
	out, _ := feed(ason.NewTransformer(&kindProto{}), `{"body":"be brief","kind":"note"}`, 1)
	fmt.Println(out)
	// Output: {"kind":"note","text":"be brief"}
}

// Sub-hook: Enter().Via(hook) hands the callbacks of a whole subtree to another Protocol; OnLeave of the container goes back to the issuer.
type prefixKeys struct {
	ason.BaseProtocol
	prefix string
}

func (h *prefixKeys) OnKey(t *ason.Transformer) ason.Action {
	return ason.Pass().As(h.prefix + t.Last())
}

type viaProto struct {
	ason.BaseProtocol
	meta prefixKeys
}

func (p *viaProto) OnKey(t *ason.Transformer) ason.Action {
	if t.Depth() == 1 && t.Last() == "meta" {
		return ason.Probe()
	}
	return ason.Pass()
}
func (p *viaProto) OnStart(t *ason.Transformer, kind ason.ValueKind) ason.Action {
	return ason.Enter().Via(&p.meta)
}

func Example_subHook() {
	out, _ := feed(ason.NewTransformer(&viaProto{meta: prefixKeys{prefix: "x_"}}), `{"meta":{"a":1,"b":{"c":2}},"d":3}`, 2)
	fmt.Println(out)
	// Output: {"meta":{"x_a":1,"x_b":{"c":2}},"d":3}
}

// Streaming an attachment: only the prefix of the data URL is inspected to extract the mime type; the base64 payload streams into another shape and never enters memory.
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
	return ason.Pass().Wrap(nil, []byte(`"}`)), off[comma+1] // resume from the raw offset after the comma
}

func Example_dataURL() {
	body := `{"image":"data:image/png;base64,` + strings.Repeat("iVBORw0KGgo=", 100) + `","n":1}`
	out, _ := feed(ason.NewTransformer(dataURLProto{}), body, 16)
	fmt.Println(strings.HasPrefix(out, `{"image":{"mime":"image/png","data":"iVBORw0KGgo=`), strings.HasSuffix(out, `"},"n":1}`), len(out))
	// Output: true true 1246
}

// Strict validation: the same rejection surface as encoding/json; a syntax error anywhere bails.
func Example_strict() {
	_, why := feed(ason.NewTransformer(ason.BaseProtocol{}), `{"a":1,"b":tru}`, 1)
	fmt.Println(why)
	// Output: incomplete literal at byte 14 in b
}
