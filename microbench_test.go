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

// numbersBody is what a tool schema's arrays of small integers look like: the scalar path runs its whole DFA for a
// single digit, once per element, and the profile shows that adding up in a document made mostly of them.
func numbersBody(n int) []byte {
	var b strings.Builder
	b.WriteString(`{"model":"m","rows":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"v":[1,2,3,4,5,6,7,8],"w":[0,9,1,8,2,7]}`)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

// Wide numbers for contrast: the same element count, values that actually exercise the DFA's states.
func wideNumbersBody(n int) []byte {
	var b strings.Builder
	b.WriteString(`{"model":"m","rows":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"v":[-1.5e10,12345678,0.125,-9876.5,1e-7,42,3.14159,-0.0],"w":[100000,2500,7,80,9000,1]}`)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

func BenchmarkNumbers(b *testing.B) {
	b.Run("single-digit", func(b *testing.B) { benchShape(b, numbersBody(9000)) })
	b.Run("wide", func(b *testing.B) { benchShape(b, wideNumbersBody(5000)) })
}

// oneStringBody is a document whose whole cost is a single string of the given length: it isolates where a vector
// scan starts to beat the word-at-a-time one, which a mixed corpus cannot show.
func oneStringBody(n int) []byte {
	return []byte(`{"k":"` + strings.Repeat("x", n) + `"}`)
}

// Lengths across the range that matters: below the vector width, around it, and up to where base64 payloads live.
func BenchmarkStringLen(b *testing.B) {
	for _, n := range []int{8, 16, 32, 64, 128, 256, 1024, 4096, 65536, 1 << 20} {
		in := oneStringBody(n)
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) { benchShape(b, in) })
	}
}
