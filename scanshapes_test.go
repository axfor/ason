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

// Numbers inside a region are the densest value a request body has -- a schema's arrays of small integers -- and the
// scanner has a fast path for the common shape (a digit followed immediately by a structural character). These assert
// the fast path cannot differ from the state machine: the same documents at every chunk size, digits against each
// closing byte, numbers the grammar must reject, and numbers nested deeper than the region's own level.
func TestRegionNumbersAreScannedIdentically(t *testing.T) {
	docs := []string{
		`{"v":[1,2,3,4,5,6,7,8,9,0]}`,
		`{"v":[1],"w":{"x":2},"y":3}`,
		`{"v":[[1,2],[3,[4,5]],{"a":[6]}]}`,
		`{"v":[0,-1,1.5,-0.25,1e3,1E-3,-1.5e+10,12345678901234567890]}`,
		`{"v":[1 , 2 ,3	,4]}`, // whitespace around the closing byte, including a tab
		`{"a":1}`,
		`{"a":-0}`,
	}
	for n, in := range docs {
		for _, cs := range []int{1, 2, 3, 7, 64, len(in)} {
			if cs > len(in) {
				cs = len(in)
			}
			got, ok, why := feedAll(NewTransformer(BaseProtocol{}), in, cs)
			if !ok {
				t.Fatalf("doc %d chunk=%d rejected: %s", n, cs, why)
			}
			if got != in {
				t.Fatalf("doc %d chunk=%d: %q != %q", n, cs, got, in)
			}
			sunk, ok2, why2 := feedSink(NewTransformer(BaseProtocol{}), in, cs)
			if !ok2 || sunk != in {
				t.Fatalf("doc %d chunk=%d sink: %s / %q", n, cs, why2, sunk)
			}
		}
	}
}

// The numbers the grammar rejects must stay rejected, at every chunk boundary: a fast path that accepts a digit
// without looking at what follows would let these through.
func TestRegionRejectsBadNumbers(t *testing.T) {
	bad := []string{
		`{"v":[01]}`, `{"v":[1.]}`, `{"v":[.5]}`, `{"v":[-]}`, `{"v":[1e]}`, `{"v":[1e+]}`,
		`{"v":[--1]}`, `{"v":[1..2]}`, `{"v":[0x1]}`, `{"v":[1 2]}`, `{"v":[+1]}`, `{"v":[1,]}`,
	}
	for _, in := range bad {
		for cs := 1; cs <= len(in); cs++ {
			if _, ok, _ := feedAll(NewTransformer(BaseProtocol{}), in, cs); ok {
				t.Fatalf("%q accepted at chunk=%d", in, cs)
			}
		}
	}
}

// The fast path may only finish a number whose closing byte is in the same chunk. These place a digit at the very end
// of a chunk, so the byte that ends it arrives in the next one and the state machine has to carry the value across.
func TestRegionNumbersSplitAtEveryBoundary(t *testing.T) {
	docs := []string{
		`{"v":[1,2,3],"w":4}`,
		`{"v":[1,22,333,4444],"w":{"x":[5,6]}}`,
		`{"v":[0,1,0,9],"w":-1}`,
		`{"v":[1.5,2,3e4,5],"w":[6,7.25]}`,
	}
	for n, in := range docs {
		// Every single split point, so each number in turn is the last byte of a chunk.
		for cut := 1; cut < len(in); cut++ {
			tr := NewTransformer(BaseProtocol{})
			var sb strings.Builder
			tr.SetSink(func(b []byte) { sb.Write(b) })
			tr.Write([]byte(in[:cut]))
			tr.Write([]byte(in[cut:]))
			tr.Finish()
			if bad, why := tr.Unsupported(); bad {
				t.Fatalf("doc %d cut=%d rejected: %s", n, cut, why)
			}
			if sb.String() != in {
				t.Fatalf("doc %d cut=%d: %q != %q", n, cut, sb.String(), in)
			}
		}
	}
}
