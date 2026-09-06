package ason

import (
	"bytes"
	"strings"
	"testing"
)

// feedSink 用 sink 收输出；与 Out() 路径的结果必须逐字节相同。
func feedSink(tr *Transformer, in string, chunk int) (string, bool, string) {
	var sb bytes.Buffer
	tr.SetSink(func(b []byte) { sb.Write(b) })
	for i := 0; i < len(in); i += chunk {
		j := i + chunk
		if j > len(in) {
			j = len(in)
		}
		tr.Write([]byte(in[i:j]))
		if out := tr.Out(); out != nil {
			panic("设了 sink 之后 Out() 应返回空")
		}
	}
	if out := tr.Finish(); out != nil {
		panic("设了 sink 之后 Finish() 应返回空")
	}
	if u, why := tr.Unsupported(); u {
		return "", false, why
	}
	return sb.String(), true, ""
}

func TestSinkMatchesOut(t *testing.T) {
	big := `{"model":"p/m","messages":[{"role":"user","content":"` + strings.Repeat("x", 300<<10) + `"}],"n":1}` + "\n"
	ins := []string{`{"a":1}`, "{\n \"a\" : [ 1 , 2 ] ,\n \"b\" : { }\n}\n", big}
	mk := []func() *Transformer{
		func() *Transformer { return NewTransformer(BaseProtocol{}) },
		func() *Transformer {
			return NewKeyProbeTransformer(KeyProbeOptions{Keys: map[string]int{"model": 1024},
				OnKey: func(t *Transformer, k string, raw []byte) ([]byte, bool) { return []byte(`"R"`), true }})
		},
	}
	for _, in := range ins {
		for _, m := range mk {
			for _, cs := range []int{1, 7, 4096, 16384, len(in)} {
				want, ok1, _ := feedAll(m(), in, cs)
				got, ok2, why := feedSink(m(), in, cs)
				if ok1 != ok2 || got != want {
					t.Fatalf("chunk=%d: sink 与 Out 不一致 (ok %v/%v %s)\n got %d 字节\nwant %d 字节", cs, ok1, ok2, why, len(got), len(want))
				}
			}
		}
	}
}

// 提交点之前判定不支持：sink 一个字节都不该收到；之后判定不支持：已交出的收不回，但之后不再交。
func TestSinkRespectsCommitPoint(t *testing.T) {
	early := `{"a":1,"b":tru}`
	var got bytes.Buffer
	tr := NewTransformer(BaseProtocol{})
	tr.SetSink(func(b []byte) { got.Write(b) })
	tr.Write([]byte(early))
	tr.Finish()
	if got.Len() != 0 {
		t.Fatalf("提交前判定不支持，sink 却收到 %d 字节", got.Len())
	}
	late := `{"pad":"` + strings.Repeat("y", 100<<10) + `","b":tru}`
	got.Reset()
	tr = NewTransformer(BaseProtocol{})
	tr.SetSink(func(b []byte) { got.Write(b) })
	for i := 0; i < len(late); i += 4096 {
		j := i + 4096
		if j > len(late) {
			j = len(late)
		}
		tr.Write([]byte(late[i:j]))
	}
	tr.Finish()
	if bad, _ := tr.Unsupported(); !bad || !tr.Committed() {
		t.Fatal("应在提交点之后判定不支持")
	}
	if got.Len() == 0 || !strings.HasPrefix(late, got.String()) {
		t.Fatalf("提交后交出的 %d 字节应是输入的前缀", got.Len())
	}
}

// sink 路径不再每块分配输出缓冲：整条 1MB 流的分配次数与块数无关。
func TestSinkReusesBuffer(t *testing.T) {
	in := []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("z", 1<<20) + `"}]}`)
	n := testing.AllocsPerRun(3, func() {
		tr := NewTransformer(BaseProtocol{})
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
	if n > 24 { // 提交前的缓冲增长 + 一次块大小的复用缓冲 + 帧 / 路径（实测 17）；64 块若每块分配会远超此数
		t.Fatalf("sink 路径每条流分配 %.0f 次，应与块数无关", n)
	}
}

func BenchmarkSinkPassthrough1MB(b *testing.B) {
	in := []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("z", 1<<20) + `"}]}`)
	b.SetBytes(int64(len(in)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tr := NewTransformer(BaseProtocol{})
		tr.SetSink(func([]byte) {})
		for j := 0; j < len(in); j += 16384 {
			k := j + 16384
			if k > len(in) {
				k = len(in)
			}
			tr.Write(in[j:k])
		}
		tr.Finish()
	}
}

func BenchmarkOutPassthrough1MB(b *testing.B) {
	in := []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("z", 1<<20) + `"}]}`)
	b.SetBytes(int64(len(in)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tr := NewTransformer(BaseProtocol{})
		for j := 0; j < len(in); j += 16384 {
			k := j + 16384
			if k > len(in) {
				k = len(in)
			}
			tr.Write(in[j:k])
			tr.Out()
		}
		tr.Finish()
	}
}
