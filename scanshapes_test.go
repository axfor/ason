package ason

import (
	"strings"
	"testing"
)

// The scanner's hot loops are where a change is most likely to pass the ordinary tests and still shift a byte
// somewhere: whitespace kept verbatim, an escape at a chunk boundary, a control character that must be rejected.
// These documents are fed at every chunk size through both output paths, and the two have to agree with each other
// and with the input for the bytes that pass through untouched.
func scanShapeDocs() []string {
	return []string{
		`{"a":1}`,
		"{\n\t\"a\"  :  [ 1 , 2 , { \"b\" : \"c\" } ] ,\n  \"d\" : { }\n}\n",
		`{"s":"with \"escapes\" and \\ and é and \t tabs and é"}`,
		`{"deep":[[[[[[[[[[{"x":[1,2,3]}]]]]]]]]]]}`,
		`{"ws":   "v"   ,   "n":  -1.5e10  ,  "t":  true  ,  "z":  null   }`,
		`{"model":"p/m","tools":[` + strings.Repeat(`{"type":"function","function":{"name":"f","parameters":{"type":"object","properties":{"a":{"type":"string"}}}}},`, 400) + `{"last":1}]}`,
		`{"parts":[` + strings.Repeat(`{"type":"text","text":"hello there"},`, 800) + `{"type":"text","text":"end"}]}`,
		"{\"pad\":\"" + strings.Repeat("y", 200<<10) + "\",\"after\":{\"k\":[1,2]}}",
	}
}

// A passthrough transform reproduces its input byte for byte, whatever the chunk size and whichever output path the
// caller takes. Anything the scanner's loops get wrong shows up here as a diff rather than as a plausible-looking
// document.
func TestScanShapesArePassedThroughUnchanged(t *testing.T) {
	for n, in := range scanShapeDocs() {
		for _, cs := range []int{1, 2, 7, 64, 4096, 65536, len(in)} {
			if cs > len(in) {
				cs = len(in)
			}
			got, ok, why := feedAll(NewTransformer(BaseProtocol{}), in, cs)
			if !ok {
				t.Fatalf("doc %d chunk=%d: %s", n, cs, why)
			}
			if got != in {
				t.Fatalf("doc %d chunk=%d: output differs from input (%d vs %d bytes)", n, cs, len(got), len(in))
			}
			sunk, ok2, why2 := feedSink(NewTransformer(BaseProtocol{}), in, cs)
			if !ok2 || sunk != in {
				t.Fatalf("doc %d chunk=%d: the sink path differs (%s, %d vs %d bytes)", n, cs, why2, len(sunk), len(in))
			}
		}
	}
}

// The same documents with a rewrite in them: the scanner's loops also decide where the output stops being a copy of
// the input, so a transform that renames one field has to produce the same bytes at every chunk size.
func TestScanShapesRewriteIdenticallyAtEveryChunkSize(t *testing.T) {
	for n, in := range scanShapeDocs() {
		want, ok, why := feedAll(NewTransformer(dropAndRenameProto{}), in, len(in))
		if !ok {
			t.Fatalf("doc %d whole: %s", n, why)
		}
		for _, cs := range []int{1, 2, 7, 64, 4096, 65536} {
			got, ok2, why2 := feedAll(NewTransformer(dropAndRenameProto{}), in, cs)
			if !ok2 || got != want {
				t.Fatalf("doc %d chunk=%d differs from whole-chunk output (%s, %d vs %d bytes)", n, cs, why2, len(got), len(want))
			}
			sunk, ok3, why3 := feedSink(NewTransformer(dropAndRenameProto{}), in, cs)
			if !ok3 || sunk != want {
				t.Fatalf("doc %d chunk=%d: sink differs (%s, %d vs %d bytes)", n, cs, why3, len(sunk), len(want))
			}
		}
	}
}

// Control characters inside a string are a rejection, and the scanner must reject them wherever the chunk boundary
// falls -- including right before the offending byte.
func TestScanRejectsControlCharactersAtEveryBoundary(t *testing.T) {
	in := "{\"a\":\"x\ty\"}" // a raw tab inside a string
	for cs := 1; cs <= len(in); cs++ {
		_, ok, _ := feedAll(NewTransformer(BaseProtocol{}), in, cs)
		if ok {
			t.Fatalf("chunk=%d: a raw control character in a string was accepted", cs)
		}
	}
}
