// Package run is the driver shared by the example programs: it reads JSON from stdin in chunks, transforms as it reads and writes to stdout.
// When stdin is a terminal the example's built-in sample input is used instead, so a plain go run works.
package run

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/axfor/ason"
)

// Main parses the -chunk flag, drives tr over stdin (or sample) and writes the result; on a bail it prints the reason and exits with 1.
func Main(tr *ason.Transformer, sample string) {
	chunk := flag.Int("chunk", 7, "bytes fed to the transformer per call (deliberately small, to show chunking does not matter)")
	flag.Parse()
	var in io.Reader = os.Stdin
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		fmt.Fprintf(os.Stderr, "stdin is a terminal, using the sample input: %s\n", sample)
		in = strings.NewReader(sample)
	}
	buf := make([]byte, *chunk)
	for {
		n, err := in.Read(buf)
		if n > 0 {
			tr.Write(buf[:n])
			os.Stdout.Write(tr.Out()) // empty before the commit point (64KB): output is kept back and the caller still holds the raw bytes
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	os.Stdout.Write(tr.Finish())
	if bad, why := tr.Unsupported(); bad {
		fmt.Fprintln(os.Stderr, "unsupported:", why)
		os.Exit(1)
	}
}
