package ason

import (
	"encoding/json"
	"math/rand"
	"strings"
	"testing"
)

// 分块尺寸不变性：同一输入，任何分块方式的输出都相同。
func TestChunkSizeInvariance(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	gen := func() string {
		var sb strings.Builder
		sb.WriteString("{")
		n := r.Intn(8) + 1
		for i := 0; i < n; i++ {
			if i > 0 {
				sb.WriteString(" , ")
			}
			sb.WriteString(`"k` + string(rune('a'+i)) + `" : `)
			switch r.Intn(6) {
			case 0:
				sb.WriteString(`"` + strings.Repeat("x\\n\\u00e9", r.Intn(300)) + `"`)
			case 1:
				sb.WriteString(`[ 1 , 2.5e-3 , true , null , { "z" : [ ] } ]`)
			case 2:
				sb.WriteString(`{ "model" : "p/m" , "inner" : { "deep" : [ [ ] , { } ] } }`)
			case 3:
				sb.WriteString("-0")
			case 4:
				sb.WriteString(`"` + strings.Repeat("y", r.Intn(5000)) + `"`)
			case 5:
				sb.WriteString("false")
			}
		}
		sb.WriteString("}\n")
		return sb.String()
	}
	mk := []func() *Transformer{
		func() *Transformer { return NewTransformer(BaseProtocol{}) },
		func() *Transformer {
			return NewKeyProbeTransformer(KeyProbeOptions{Keys: map[string]int{"ka": 1 << 20, "kd": 1 << 20},
				OnKey: func(t *Transformer, k string, raw []byte) ([]byte, bool) { return []byte(`"R"`), true }})
		},
	}
	for i := 0; i < 300; i++ {
		in := gen()
		for _, m := range mk {
			ref, ok, why := feedAll(m(), in, len(in))
			if !ok {
				t.Fatalf("%s: %s", in, why)
			}
			for _, cs := range []int{1, 2, 3, 5, 7, 11, 64, 1000} {
				got, ok, why := feedAll(m(), in, cs)
				if !ok || got != ref {
					t.Fatalf("chunk=%d 输出与整体喂入不同 (ok=%v %s)\n in  %q\n ref %q\n got %q", cs, ok, why, in, ref, got)
				}
			}
			if !json.Valid([]byte(ref)) {
				t.Fatalf("输出不是合法 JSON: %q", ref)
			}
		}
	}
}

// 垃圾输入：随机字节、随机截断、随机篡改，任何协议都不能 panic，只能"输出"或"判定不支持"。
func TestGarbageNeverPanics(t *testing.T) {
	r := rand.New(rand.NewSource(9))
	base := `{"model":"m","messages":[{"role":"user","content":"hi\né"},{"role":"assistant","content":[{"type":"text","text":"x"}]}],"tools":[{"f":{"p":{"a":[1,2.5,null]}}}],"stream":true}`
	mk := []func() *Transformer{
		func() *Transformer { return NewTransformer(BaseProtocol{}) },
		func() *Transformer { return NewTransformer(&captureAllProto{}) },
		func() *Transformer {
			return NewKeyProbeTransformer(KeyProbeOptions{Keys: map[string]int{"model": 64, "tools": 4096}})
		},
	}
	for i := 0; i < 3000; i++ {
		b := []byte(base)
		switch r.Intn(4) {
		case 0: // 截断
			b = b[:r.Intn(len(b))]
		case 1: // 随机改一个字节
			b[r.Intn(len(b))] = byte(r.Intn(256))
		case 2: // 插入垃圾
			p := r.Intn(len(b))
			b = append(b[:p:p], append([]byte{byte(r.Intn(256)), byte(r.Intn(256))}, b[p:]...)...)
		case 3: // 纯随机
			b = make([]byte, r.Intn(64))
			r.Read(b)
		}
		for _, m := range mk {
			func() {
				defer func() {
					if e := recover(); e != nil {
						t.Fatalf("panic: %v\n输入 %q", e, b)
					}
				}()
				tr := m()
				cs := []int{1, 3, 17, 4096}[r.Intn(4)]
				for j := 0; j < len(b); j += cs {
					k := j + cs
					if k > len(b) {
						k = len(b)
					}
					tr.Write(b[j:k])
					tr.Out()
				}
				tr.Finish()
			}()
		}
	}
}

// 深层嵌套与超长字符串：常数状态，不随输入增长。
func TestDeepAndLong(t *testing.T) {
	deep := strings.Repeat(`{"a":[`, 2000) + `1` + strings.Repeat(`]}`, 2000)
	in := `{"d":` + deep + `,"s":"` + strings.Repeat("abcdefgh\\n", 200000) + `"}`
	out, ok, why := feedAll(NewTransformer(BaseProtocol{}), in, 4096)
	if !ok || out != in {
		t.Fatalf("ok=%v why=%s len=%d", ok, why, len(out))
	}
}
