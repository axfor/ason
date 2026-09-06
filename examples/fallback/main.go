// 提交点与回落：输出在 64KB 提交点之前不下发；协议在此之前判定不支持时，调用方还持有全部原始字节，
// 可以换一条路（这里演示：原样输出）。提交点之后判定不支持则只能失败。
//
//	echo '{"a":1,"b":tru}' | go run ./examples/fallback
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/axfor/ason"
)

func main() {
	chunk := flag.Int("chunk", 5, "每次喂给转换器的字节数")
	flag.Parse()
	tr := ason.NewTransformer(ason.BaseProtocol{})
	var held []byte // 提交点之前，调用方自己留着原始字节
	in := io.Reader(os.Stdin)
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		in = readerOf(`{"a":1,"b":tru}`)
	}
	buf := make([]byte, *chunk)
	for {
		n, err := in.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if !tr.Committed() {
				held = append(held, chunk...)
			}
			tr.Write(chunk)
			if bad, why := tr.Unsupported(); bad {
				if tr.Committed() {
					fmt.Fprintln(os.Stderr, "unsupported after the commit point, cannot recover:", why)
					os.Exit(1)
				}
				fmt.Fprintln(os.Stderr, "unsupported before the commit point, falling back to verbatim:", why)
				os.Stdout.Write(held)
				io.Copy(os.Stdout, in)
				return
			}
			if out := tr.Out(); len(out) > 0 { // 越过提交点后才会有输出
				held = nil
				os.Stdout.Write(out)
			}
		}
		if err != nil {
			break
		}
	}
	os.Stdout.Write(tr.Finish())
	if bad, why := tr.Unsupported(); bad {
		fmt.Fprintln(os.Stderr, "unsupported at finish, falling back to verbatim:", why)
		os.Stdout.Write(held)
	}
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
