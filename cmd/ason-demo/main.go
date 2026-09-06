// ason-demo：从标准输入按块读 JSON，用选定的示例协议边读边转换，写到标准输出。
//
//	echo '{"model":"openai/gpt-4o","messages":[{"role":"user","content":"hi"}]}' | ason-demo -demo rewrite -chunk 7
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/axfor/ason"
)

type renameProto struct{ ason.BaseProtocol }

func (renameProto) OnKey(t *ason.Transformer) ason.Action {
	if t.Depth() == 1 && t.Last() == "max_tokens" {
		return ason.Pass().As("max_completion_tokens")
	}
	return ason.Pass()
}

type nestProto struct{ ason.BaseProtocol }

func (nestProto) OnKey(t *ason.Transformer) ason.Action {
	if t.Depth() == 1 && t.Last() == "messages" {
		return ason.Probe()
	}
	return ason.Pass()
}
func (nestProto) OnStart(t *ason.Transformer, kind ason.ValueKind) ason.Action {
	if kind != ason.KindArray {
		return ason.Bail("messages 不是数组")
	}
	t.W().PushObj("input")
	t.W().PushArr("messages")
	return ason.Enter().Flat()
}
func (nestProto) OnLeave(t *ason.Transformer) {
	if t.Depth() == 1 {
		t.W().Open()
		t.W().Pop()
		t.W().Pop()
	}
}

func main() {
	demo := flag.String("demo", "identity", "identity | rename | rewrite | nest")
	chunk := flag.Int("chunk", 4096, "读取块大小")
	flag.Parse()
	var tr *ason.Transformer
	switch *demo {
	case "identity":
		tr = ason.NewTransformer(ason.BaseProtocol{})
	case "rename":
		tr = ason.NewTransformer(renameProto{})
	case "nest":
		tr = ason.NewTransformer(nestProto{})
	case "rewrite":
		tr = ason.NewKeyProbeTransformer(ason.KeyProbeOptions{Keys: map[string]int{"model": 4096},
			OnKey: func(t *ason.Transformer, key string, raw []byte) ([]byte, bool) {
				if i := bytes.IndexByte(raw, '/'); i >= 0 {
					return append([]byte(`"`), raw[i+1:]...), true
				}
				return nil, false
			}})
	default:
		fmt.Fprintln(os.Stderr, "unknown demo:", *demo)
		os.Exit(2)
	}
	buf := make([]byte, *chunk)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			tr.Write(buf[:n])
			os.Stdout.Write(tr.Out())
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
