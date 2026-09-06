// Observe: look but do not touch. Skip everything, Capture a few small fields, count the elements of items; the output is discarded and the caller forwards the raw bytes itself.
// This is the "statistics / audit" use: constant memory however large the document.
//
//	echo '{"owner":"team/42","items":[{"a":1},{"b":2},3]}' | go run ./examples/observe
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/axfor/ason"
)

type observer struct {
	ason.BaseProtocol
	owner string
	items int
}

func (o *observer) OnKey(t *ason.Transformer) ason.Action {
	if t.Depth() == 1 {
		switch t.Last() {
		case "owner":
			return ason.Capture(256)
		case "items":
			return ason.Enter().Lazy()
		}
	}
	return ason.Skip()
}

func (o *observer) OnElem(t *ason.Transformer) ason.Action {
	if t.Depth() == 2 {
		o.items++
	}
	return ason.Skip()
}

func (o *observer) OnValue(t *ason.Transformer, raw []byte) { o.owner, _ = ason.JSONUnquote(raw) }

func main() {
	chunk := flag.Int("chunk", 5, "bytes fed to the transformer per call")
	flag.Parse()
	o := &observer{}
	tr := ason.NewTransformer(o)
	in := io.Reader(os.Stdin)
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		in = readerOf(`{"owner":"team/42","items":[{"a":1},{"b":2},3]}`)
	}
	buf := make([]byte, *chunk)
	for {
		n, err := in.Read(buf)
		if n > 0 {
			os.Stdout.Write(buf[:n]) // forward verbatim
			tr.Write(buf[:n])
			tr.Out() // nobody wants the observer's output; take it so it does not accumulate
		}
		if err != nil {
			break
		}
	}
	tr.Finish()
	if bad, why := tr.Unsupported(); bad {
		fmt.Fprintln(os.Stderr, "\nstopped observing:", why)
		return
	}
	fmt.Fprintf(os.Stderr, "\nowner=%s items=%d\n", o.owner, o.items)
}

type strReader struct {
	s string
	i int
}

func (r *strReader) Read(p []byte) (int, error) {
	if r.i >= len(r.s) {
		return 0, io.EOF
	}
	n := copy(p, r.s[r.i:])
	r.i += n
	return n, nil
}

func readerOf(s string) io.Reader { return &strReader{s: s} }
