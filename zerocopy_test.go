package ason

import (
	"bytes"
	"math/rand"
	"sort"
	"strings"
	"testing"
)

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

// RootDone marks the point after which the output is only complete once Finish has run: the root's closing
// token and any trailing whitespace are written there. A caller that switches to forwarding input verbatim
// has to stop at that boundary, so the signal has to be exact.
func TestRootDoneMarksTheHeldTail(t *testing.T) {
	// Past the commit point the transformer releases as it goes, so what it still holds when the root ends
	// is exactly the tail -- which is what a caller forwarding the rest verbatim would drop.
	body := []byte(`{"a":"` + strings.Repeat("y", 70<<10) + `","b":[1,2,3]}` + "\n")
	tr := NewTransformer(BaseProtocol{})
	tr.Write(body)
	if !tr.RootDone() {
		t.Fatal("the whole document was written, the root must be done")
	}
	released := len(tr.Out())
	if released == 0 {
		t.Fatal("past the commit point the transformer should have released something")
	}
	tail := tr.Finish()
	if bad, why := tr.Unsupported(); bad {
		t.Fatal(why)
	}
	if released+len(tail) != len(body) {
		t.Fatalf("released %d + finished %d != %d", released, len(tail), len(body))
	}
	if len(tail) == 0 {
		t.Fatal("RootDone was true but Finish added nothing: the signal would be pointless")
	}

	// Before the root ends nothing is held back that the remaining input would not produce anyway.
	for cut := 1; cut < len(body); cut++ {
		tr := NewTransformer(BaseProtocol{})
		tr.Write(body[:cut])
		if tr.RootDone() {
			continue
		}
		tr.Write(body[cut:])
		if !tr.RootDone() {
			t.Fatalf("cut=%d: the root should be done once the rest is written", cut)
		}
		break
	}
}

// 整条流式设计都压在这一条不变式上：一份文档无论怎么切分喂进来，
// 每次 Write 之后取走的字节，拼上 Finish 的字节，必须正好等于完整输出，一个不多一个不少。
// 提交点之后引擎才会边走边放，所以文档必须大于提交点才测得到这条路径；
// 调用方（Higress 的 guard）就是在这里丢过字节：它在引擎还扣着根闭合符号时停止喂入，
// 转去原样透传，结果发出去的请求体少了尾巴。
func TestReleasedPlusFinishedEqualsWholeOutputUnderAnySplit(t *testing.T) {
	body := []byte(`{"a":"` + strings.Repeat("y", 70<<10) + `","b":[1,2,{"c":null}],"d":1.5e3}` + "\n")

	whole := func() string {
		tr := NewTransformer(BaseProtocol{})
		tr.Write(body)
		out := string(tr.Out())
		return out + string(tr.Finish())
	}()
	if whole != string(body) {
		t.Fatalf("透传应当逐字节还原：得到 %d 字节，应为 %d", len(whole), len(body))
	}

	check := func(splits []int) {
		t.Helper()
		tr := NewTransformer(BaseProtocol{})
		var got []byte
		prev := 0
		for _, cut := range splits {
			tr.Write(body[prev:cut])
			got = append(got, tr.Out()...) // Out 取走已释放的部分，下一次 Write 从头开始
			prev = cut
		}
		got = append(got, tr.Finish()...)
		if bad, why := tr.Unsupported(); bad {
			t.Fatalf("splits=%v: %s", splits, why)
		}
		if string(got) != whole {
			t.Fatalf("splits=%v: 释放 %d + 收尾 = %d 字节，应为 %d；尾部 %q",
				splits, len(got), len(got), len(whole), got[max(0, len(got)-24):])
		}
	}

	// 两刀扫过提交点附近：这一带最容易让"跨过提交点"和"读到根结尾"撞在同一块里
	for cut := 60 << 10; cut < len(body); cut += 89 {
		check([]int{cut, len(body)})
	}
	// 随机切分兜底
	rnd := rand.New(rand.NewSource(11))
	for i := 0; i < 300; i++ {
		n := 1 + rnd.Intn(6)
		set := map[int]bool{}
		for j := 0; j < n; j++ {
			set[1+rnd.Intn(len(body)-1)] = true
		}
		var splits []int
		for c := range set {
			splits = append(splits, c)
		}
		sort.Ints(splits)
		check(append(splits, len(body)))
	}
}
