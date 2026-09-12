package ason

import (
	"fmt"
	"strings"
	"testing"
)

// A matrix of shapes and chunk sizes: what the engine costs per byte depends on both, and the gaps in the old
// benchmarks (base64-heavy bodies, tool definitions, deep nesting, one chunk against many) were where the claims
// in the README had no measurement of their own.
func matrixBodies() []struct {
	name string
	body []byte
} {
	b64 := strings.Repeat("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJ", 23000) // ~1MB of base64 payload
	long := strings.Repeat("z", 1<<20)
	var tools strings.Builder
	tools.WriteString(`{"model":"m","tools":[`)
	for i := 0; tools.Len() < 1<<20; i++ {
		if i > 0 {
			tools.WriteByte(',')
		}
		fmt.Fprintf(&tools, `{"type":"function","function":{"name":"f%d","description":"d%d","parameters":{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"number"}},"required":["a"]}}}`, i, i)
	}
	tools.WriteString(`],"messages":[{"role":"user","content":"hi"}]}`)
	var deep strings.Builder
	deep.WriteString(`{"model":"m","messages":[`)
	for i := 0; deep.Len() < 1<<20; i++ {
		if i > 0 {
			deep.WriteByte(',')
		}
		deep.WriteString(`{"role":"user","content":[{"type":"text","text":"hello there"},{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgo="}}]}`)
	}
	deep.WriteString(`]}`)
	return []struct {
		name string
		body []byte
	}{
		{"base64_1MB", []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + b64 + `"}}]}]}`)},
		{"longstring_1MB", []byte(`{"messages":[{"role":"user","content":"` + long + `"}]}`)},
		{"tools_1MB", []byte(tools.String())},
		{"parts_1MB", []byte(deep.String())},
	}
}

func BenchmarkMatrix(b *testing.B) {
	for _, body := range matrixBodies() {
		for _, chunk := range []int{16 << 10, 64 << 10, 1 << 30} {
			name := fmt.Sprintf("%s/chunk=%s", body.name, map[bool]string{true: "whole"}[chunk > len(body.body)])
			if chunk <= len(body.body) {
				name = fmt.Sprintf("%s/chunk=%dKB", body.name, chunk>>10)
			}
			in := body.body
			b.Run(name, func(b *testing.B) {
				b.SetBytes(int64(len(in)))
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					tr := NewTransformer(BaseProtocol{})
					tr.SetSink(func([]byte) {})
					for j := 0; j < len(in); j += chunk {
						k := j + chunk
						if k > len(in) {
							k = len(in)
						}
						tr.Write(in[j:k])
					}
					tr.Finish()
				}
			})
		}
	}
}
