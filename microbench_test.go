package ason

import (
	"fmt"
	"strings"
	"testing"
)

// The shapes above are 1MB bodies, which mix every cost together. These isolate the two the profile points at in a
// document of tool definitions: very many very short strings (11 thousand of them average 5.6 bytes in tools_1MB),
// and a structural character every 2.5 bytes with no whitespace at all between them.
func shortStringBody(n, strLen int) []byte {
	var b strings.Builder
	b.WriteString(`{"model":"m","tools":[`)
	v := strings.Repeat("x", strLen)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"name":"%s","desc":"%s"}`, v, v)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

// structuralBody is the opposite extreme: nesting and punctuation with almost no string content, which is what the
// region's tight loop costs per byte when there is nothing for the string scanner to do.
func structuralBody(n int) []byte {
	var b strings.Builder
	b.WriteString(`{"model":"m","tools":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"a":{"b":[1,2,3],"c":{"d":[4,5]}}}`)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

func benchShape(b *testing.B, in []byte) {
	b.SetBytes(int64(len(in)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tr := NewTransformer(BaseProtocol{})
		tr.SetSink(func([]byte) {})
		tr.Write(in)
		tr.Finish()
		if bad, why := tr.Unsupported(); bad {
			b.Fatal(why)
		}
	}
}

// Strings of 3, 6 and 12 bytes: below, across and above the 8-byte word the string scanner reads.
func BenchmarkShortStrings(b *testing.B) {
	for _, n := range []int{3, 6, 12, 40} {
		in := shortStringBody(12000, n)
		b.Run(fmt.Sprintf("len=%d", n), func(b *testing.B) { benchShape(b, in) })
	}
}

func BenchmarkStructural(b *testing.B) {
	in := structuralBody(12000)
	b.Run("nesting", func(b *testing.B) { benchShape(b, in) })
}
