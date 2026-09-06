package ason

import "testing"

// Passthrough (BaseProtocol: always Pass) must not change a single untouched byte, whitespace and indentation around keys included,
// matching sjson's in-place rewrite.
func TestPassthroughPreservesFormatting(t *testing.T) {
	in := "{\n  \"model\": \"llama3.1\",\n  \"messages\": [ {\"role\": \"user\", \"content\": \"Hi\"} ],\n  \"temperature\": 0.7,\n  \"stream\": true\n}"
	want := in
	for _, cs := range []int{1, 4, 4096} {
		tr := NewTransformer(BaseProtocol{})
		var out []byte
		for i := 0; i < len(in); i += cs {
			j := i + cs
			if j > len(in) {
				j = len(in)
			}
			tr.Write([]byte(in[i:j]))
			out = append(out, tr.Out()...)
		}
		out = append(out, tr.Finish()...)
		if bad, why := tr.Unsupported(); bad {
			t.Fatalf("chunk=%d unexpected fallback: %s", cs, why)
		}
		if string(out) != want {
			t.Errorf("chunk=%d formatting not preserved:\n got: %q\nwant: %q", cs, out, want)
		}
	}
}

// Whitespace between a value and its comma must be preserved as well (sjson's in-place rewrite keeps it).
func TestTrailingWhitespaceBeforeComma(t *testing.T) {
	in := "{\n  \"model\" : \"m\" ,\n  \"messages\" : [ 1 , { \"a\" : 1 , \"b\" : 2 } , 3 ] ,\n  \"stream\" : true\n}\n"
	for _, cs := range []int{1, 3, 4096} {
		out, ok, why := feedAll(NewTransformer(BaseProtocol{}), in, cs)
		if !ok {
			t.Fatalf("chunk=%d: %s", cs, why)
		}
		if out != in {
			t.Fatalf("chunk=%d passthrough not faithful:\n got  %q\n want %q", cs, out, in)
		}
	}
}

// Whitespace before the root object must be preserved as well.
func TestLeadingWhitespace(t *testing.T) {
	for _, in := range []string{" {}", "\n\t{\"a\":1}\n", "  \r\n{ }  "} {
		out, ok, why := feedAll(NewTransformer(BaseProtocol{}), in, 1)
		if !ok || out != in {
			t.Fatalf("%q: ok=%v why=%s out=%q", in, ok, why, out)
		}
	}
}
