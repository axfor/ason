// Chat request conversion: see the conv package. In the sample, system is hoisted, content becomes parts, image data URLs become source and tools change shape.
//
//	go run ./examples/chatconv
package main

import (
	"strings"

	"github.com/axfor/ason/examples/chatconv/conv"
	"github.com/axfor/ason/examples/internal/run"
)

func main() {
	sample := `{"model":"demo/large","messages":[{"role":"system","content":"be brief"},{"content":"hi","role":"user"},` +
		`{"role":"user","content":[{"text":"look","type":"text"},{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgo="}}]}],` +
		`"stop":["END"],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}],"temperature":0.5}`
	run.Main(conv.New(conv.Options{MapModel: func(m string) string { return strings.TrimPrefix(m, "demo/") }}), sample)
}
