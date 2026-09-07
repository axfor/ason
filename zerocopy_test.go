package ason

import (
	"bytes"
	"strings"
	"testing"
)

// Unchanged 必须与"输出逐字节等于输入"完全一致：随机协议、随机分块下不允许出现假阳性。
func TestUnchangedMatchesReality(t *testing.T) {
	big := strings.Repeat("y", 300<<10)
	ins := []string{
		`{"model":"p/m","messages":[{"role":"user","content":"` + big + `"}],"n":1}`,
		"{\n  \"model\" : \"p/m\" ,\n  \"messages\" : [ { \"role\" : \"user\" , \"content\" : \"" + big + "\" } ]\n}\n",
		`{"a":[1,2,{"b":null}],"c":"` + big + `","d":true}`,
	}
	mk := map[string]func() *Transformer{
		"透传": func() *Transformer { return NewTransformer(BaseProtocol{}) },
		"改写": func() *Transformer {
			return NewKeyProbeTransformer(KeyProbeOptions{Keys: map[string]int{"model": 1024}, OnKey: func(t *Transformer, k string, raw []byte) ([]byte, bool) { return []byte(`"R"`), true }})
		},
		"丢弃": func() *Transformer {
			return NewKeyProbeTransformer(KeyProbeOptions{Keys: map[string]int{"n": 64}, OnKey: func(t *Transformer, k string, raw []byte) ([]byte, bool) { return nil, false }})
		},
		"只观察": func() *Transformer {
			return NewKeyProbeTransformer(KeyProbeOptions{Keys: map[string]int{"model": 1024}, Observe: true})
		},
	}
	for _, in := range ins {
		for name, f := range mk {
			for _, cs := range []int{4096, 16384, 65536} {
				tr := f()
				var got bytes.Buffer
				unchangedBytes := 0
				for i := 0; i < len(in); i += cs {
					j := i + cs
					if j > len(in) {
						j = len(in)
					}
					chunk := in[i:j]
					tr.Write([]byte(chunk))
					out := tr.Out()
					if tr.Unchanged() {
						// 契约：调用方可以直接放行自己的输入
						if string(out) != chunk {
							t.Fatalf("%s chunk=%d: Unchanged 为真但输出与输入不同\n in  %q\n out %q", name, cs, trunc(chunk), trunc(string(out)))
						}
						unchangedBytes += len(chunk)
					}
					got.Write(out)
				}
				got.Write(tr.Finish())
				if bad, why := tr.Unsupported(); bad {
					t.Fatalf("%s chunk=%d: 意外回落 %s", name, cs, why)
				}
				if name == "透传" || name == "只观察" {
					if got.String() != in {
						t.Fatalf("%s chunk=%d: 透传输出不保真", name, cs)
					}
					if unchangedBytes == 0 && len(in) > 3*cs {
						t.Fatalf("%s chunk=%d: 大请求体里一个 Unchanged 都没有，零拷贝路径失效", name, cs)
					}
				}
			}
		}
	}
}

func trunc(s string) string {
	if len(s) > 60 {
		return s[:60] + "..."
	}
	return s
}

// 固定缓冲：跨流复用，按请求分配趋近零。
func TestFixedOutBufferNoPerStreamAlloc(t *testing.T) {
	in := []byte(`{"model":"m","messages":[{"role":"user","content":"` + strings.Repeat("z", 1<<20) + `"}]}`)
	buf := make([]byte, 0, 128<<10)
	n := testing.AllocsPerRun(5, func() {
		tr := NewTransformer(BaseProtocol{})
		tr.SetOutBuffer(buf)
		tr.SetSink(func([]byte) {})
		for i := 0; i < len(in); i += 16384 {
			j := i + 16384
			if j > len(in) {
				j = len(in)
			}
			tr.Write(in[i:j])
		}
		tr.Finish()
	})
	if n > 24 {
		t.Fatalf("固定缓冲下每条流仍分配 %.0f 次（应只剩帧 / 路径 / key 缓存）", n)
	}
}

// 固定缓冲交出的切片在下一次 Write 之前有效，之后被复用。
func TestFixedOutBufferReuse(t *testing.T) {
	buf := make([]byte, 0, 64<<10)
	tr := NewTransformer(BaseProtocol{})
	tr.SetOutBuffer(buf)
	in := `{"a":"` + strings.Repeat("x", 80<<10) + `","b":1}`
	var total int
	for i := 0; i < len(in); i += 32768 {
		j := i + 32768
		if j > len(in) {
			j = len(in)
		}
		tr.Write([]byte(in[i:j]))
		total += len(tr.Out())
	}
	total += len(tr.Finish())
	if bad, why := tr.Unsupported(); bad {
		t.Fatal(why)
	}
	if total != len(in) {
		t.Fatalf("输出总量 %d，输入 %d", total, len(in))
	}
}

// 支持的用法：一块缓冲按顺序复用给多条流，每条流的输出都必须正确。
// （不支持的用法是并发交错共享——输出会在提交点前互相覆盖，文档已写明。）
func TestFixedOutBufferSequentialReuse(t *testing.T) {
	buf := make([]byte, 0, 128<<10)
	for i := 0; i < 5; i++ {
		in := `{"id":` + string(rune('0'+i)) + `,"pad":"` + strings.Repeat("x", 70<<10) + `"}`
		tr := NewTransformer(BaseProtocol{})
		tr.SetOutBuffer(buf)
		var got bytes.Buffer
		for j := 0; j < len(in); j += 16384 {
			k := j + 16384
			if k > len(in) {
				k = len(in)
			}
			tr.Write([]byte(in[j:k]))
			got.Write(tr.Out())
		}
		got.Write(tr.Finish())
		if bad, why := tr.Unsupported(); bad {
			t.Fatalf("第 %d 条流回落: %s", i, why)
		}
		if got.String() != in {
			t.Fatalf("第 %d 条流输出不保真（复用缓冲被污染？）", i)
		}
	}
}
