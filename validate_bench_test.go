package ason

import (
	"strings"
	"testing"
)

// 嵌套校验的代价必须量出来：它把"树有意见的顶层容器"从区域快路径挪到了逐字节复制 + 走树，
// 所以代价取决于那些容器占请求体的比例。真实请求里它们是小配置块，大头是 messages。
func benchBody() string {
	return `{"model":"m","metadata":{"a":"1","b":"2","c":"3"},"logit_bias":{"x":1,"y":2},` +
		`"response_format":{"type":"json_object"},"stream_options":{"include_usage":true},` +
		`"messages":[{"role":"user","content":"` + strings.Repeat("x", 70000) + `"}],"max_tokens":16}`
}

func runOnce(b *testing.B, withTree bool) {
	body := []byte(benchBody())
	tree := FieldTreeOf(&sample{}, 6)
	types := FieldTypesOf(&sample{})
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tr := NewTransformer(BaseProtocol{})
		tr.SetFieldTypes(types)
		if withTree {
			tr.SetFieldTree(tree)
		}
		for j := 0; j < len(body); j += 16 << 10 {
			k := j + 16<<10
			if k > len(body) {
				k = len(body)
			}
			tr.Write(body[j:k])
			tr.Out()
		}
		tr.Finish()
		if bad, why := tr.Unsupported(); bad {
			b.Fatal(why)
		}
	}
}

func BenchmarkRootTypesOnly(b *testing.B) { runOnce(b, false) }
func BenchmarkWithFieldTree(b *testing.B) { runOnce(b, true) }

// 最坏情况：被校验的容器本身就是请求体的大头（刚好不超过 64KB 上限，所以全程都在校验）。
// 真实请求里不会这样，但代价的上界要知道。
func worstBody() string {
	var b strings.Builder
	b.WriteString(`{"model":"m","obj":{`)
	for i := 0; b.Len() < 60<<10; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"k`)
		b.WriteString(strings.Repeat("x", 8))
		b.WriteString(`":{"n":1,"s":"v"}`)
	}
	b.WriteString(`},"messages":[{"role":"user","content":"hi"}]}`)
	return b.String()
}

func runWorst(b *testing.B, withTree bool) {
	body := []byte(worstBody())
	tree := FieldTreeOf(&sample{}, 6)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tr := NewTransformer(BaseProtocol{})
		if withTree {
			tr.SetFieldTree(tree)
		}
		tr.Write(body)
		tr.Finish()
	}
}

func BenchmarkWorstNoTree(b *testing.B)   { runWorst(b, false) }
func BenchmarkWorstWithTree(b *testing.B) { runWorst(b, true) }
