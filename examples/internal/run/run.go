// Package run 是示例程序共用的驱动：从 stdin 按块读 JSON，边读边转换，写到 stdout。
// stdin 是终端时改用示例自带的样例输入，方便直接 go run。
package run

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/axfor/ason"
)

// Main 解析 -chunk 参数，驱动 tr 处理 stdin（或 sample），输出结果；判定不支持时打印原因并以 1 退出。
func Main(tr *ason.Transformer, sample string) {
	chunk := flag.Int("chunk", 7, "每次喂给转换器的字节数（故意很小，演示分块无关）")
	flag.Parse()
	var in io.Reader = os.Stdin
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		fmt.Fprintf(os.Stderr, "stdin 是终端，使用样例输入：%s\n", sample)
		in = strings.NewReader(sample)
	}
	buf := make([]byte, *chunk)
	for {
		n, err := in.Read(buf)
		if n > 0 {
			tr.Write(buf[:n])
			os.Stdout.Write(tr.Out()) // 提交点（64KB）之前这里是空的：输出攒着，原始字节仍在调用方手里
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
