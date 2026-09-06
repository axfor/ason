package ason

import (
	"strings"
	"testing"
)

// errAt 分块喂入并返回错误详情（nil = 放行）。
func errAt(tr *Transformer, in string, chunk int) *Error {
	feedAll(tr, in, chunk)
	return tr.Err()
}

// 错误分类、偏移与路径：与分块方式无关，偏移指向出错的字节。
func TestErrorCodesOffsetsPaths(t *testing.T) {
	cases := []struct {
		name string
		in   string
		mk   func() *Transformer
		code Code
		off  int64
		path string
	}{
		{"字面量", `{"a":1,"b":tru}`, base, ErrSyntax, 14, "b"},
		{"多余逗号", `{"a":1,}`, base, ErrSyntax, 7, ""},
		{"根后内容", `{"a":1} x`, base, ErrTrailing, 8, ""},
		{"根形状", ` [1]`, base, ErrRoot, 1, ""},
		{"截断", `{"a":[1,2`, base, ErrIncomplete, 9, "a"}, // 路径仍指向没结束的值
		{"截断字面量", `{"a":tru`, base, ErrSyntax, 8, "a"},
		{"区域里的括号", `{"a":{"x":[1}}`, base, ErrSyntax, 12, "a"},
		{"深层控制字符", "{\"m\":[{\"c\":\"x\x01\"}]}", enter, ErrSyntax, 13, "m[0]"}, // 元素是 Pass 区域：路径到最深的派发点
		{"重复 key", `{"a":1,"b":{"z":1,"z":2}}`, dupBail, ErrDuplicateKey, 21, "b.z"},
		{"协议 Bail 在 OnValue", `{"a":1,"model":"bad","z":2}`, valueBail, ErrUnsupported, 20, "model"},
		{"协议 BailCode 在 OnKey", `{"a":1,"nope":1}`, keyBailCode, ErrMisuse, 13, "nope"},
		{"Tail 里 Bail", `{"a":1}`, tailBail, ErrUnsupported, 7, ""},
		{"上限", `{"big":"` + strings.Repeat("y", 100) + `"}`, capSmall, ErrLimit, 71, "big"},
		{"UTF-8", "{\"s\":\"ab\xC0\x80\"}", utf8On, ErrSyntax, 8, "s"},
	}
	for _, c := range cases {
		for _, cs := range chunkSizes(len(c.in)) {
			tr := c.mk()
			e := errAt(tr, c.in, cs)
			if e == nil {
				t.Fatalf("%s chunk=%d: 应判定不支持", c.name, cs)
			}
			if e.Code != c.code || e.Offset != c.off || e.Path != c.path {
				t.Fatalf("%s chunk=%d: 得到 %s/%d/%q (%s)，期望 %s/%d/%q", c.name, cs, e.Code, e.Offset, e.Path, e.Msg, c.code, c.off, c.path)
			}
			bad, why := tr.Unsupported()
			if !bad || why != e.Error() || !strings.HasPrefix(why, e.Msg) {
				t.Fatalf("%s: Unsupported() 文案 %q 与 Err().Error() %q 不一致", c.name, why, e.Error())
			}
		}
	}
}

func base() *Transformer  { return NewTransformer(BaseProtocol{}) }
func enter() *Transformer { return NewTransformer(&dupProto{}) }
func dupBail() *Transformer {
	tr := NewTransformer(&dupProto{})
	tr.SetDupKeys(DupKeysBail)
	return tr
}
func utf8On() *Transformer {
	tr := NewTransformer(BaseProtocol{})
	tr.SetValidateUTF8(true)
	return tr
}
func valueBail() *Transformer {
	return NewKeyProbeTransformer(KeyProbeOptions{Keys: map[string]int{"model": 1024},
		OnKey: func(t *Transformer, k string, raw []byte) ([]byte, bool) {
			if string(raw) == `"bad"` {
				t.Bail("model 不可用")
			}
			return nil, false
		}})
}
func capSmall() *Transformer {
	return NewKeyProbeTransformer(KeyProbeOptions{Keys: map[string]int{"big": 64},
		OnKey: func(t *Transformer, k string, raw []byte) ([]byte, bool) { return nil, false }})
}

type keyBailProto struct{ BaseProtocol }

func (keyBailProto) OnKey(t *Transformer) Action {
	if t.Last() == "nope" {
		return BailCode(ErrMisuse, "nope is not allowed")
	}
	return Pass()
}
func keyBailCode() *Transformer { return NewTransformer(keyBailProto{}) }

type tailBailProto struct{ BaseProtocol }

func (tailBailProto) Tail(t *Transformer) { t.Bail("tail says no") }
func tailBail() *Transformer              { return NewTransformer(tailBailProto{}) }

// 正常结束：Err 为 nil，Unsupported 为 false 且文案为空。
func TestErrNilWhenOK(t *testing.T) {
	tr := base()
	if out, ok, _ := feedAll(tr, `{"a":1}`, 2); !ok || out != `{"a":1}` || tr.Err() != nil {
		t.Fatal("正常输入不应有错误")
	}
	if bad, why := tr.Unsupported(); bad || why != "" {
		t.Fatal("Unsupported 应为 false 且文案为空")
	}
}

// 第一次判定的原因保留，之后的 Bail 不覆盖。
func TestFirstErrorWins(t *testing.T) {
	tr := base()
	tr.BailErr(ErrLimit, "first")
	tr.Bail("second")
	if e := tr.Err(); e.Code != ErrLimit || e.Msg != "first" {
		t.Fatalf("得到 %+v", e)
	}
}

// Defer 回放里判定不支持：偏移是外层触发回放的位置，不是回放缓冲里的相对位置。
func TestErrorOffsetDuringReplay(t *testing.T) {
	in := `{"a":{"x":1},"b":2,"a2":3}`
	for _, cs := range chunkSizes(len(in)) {
		tr := NewTransformer(&replayBailProto{})
		e := errAt(tr, in, cs)
		if e == nil || e.Code != ErrUnsupported || e.Path != "a" {
			t.Fatalf("chunk=%d: %+v", cs, e)
		}
		if e.Offset != int64(len(in)) { // 根闭合时回放：闭合括号已消费，偏移是已消费的字节数
			t.Fatalf("chunk=%d: 偏移 %d", cs, e.Offset)
		}
	}
}

type replayBailProto struct {
	BaseProtocol
	released bool
}

func (p *replayBailProto) OnKey(t *Transformer) Action {
	if t.Depth() == 1 && t.Last() == "a" {
		if p.released {
			return Bail("a rejected on replay")
		}
		return Defer(1 << 10)
	}
	return Pass()
}
func (p *replayBailProto) OnLeave(t *Transformer) {
	if t.Depth() == 0 {
		p.released = true
		t.ReleaseNow()
	}
}

func TestCodeString(t *testing.T) {
	if ErrSyntax.String() != "syntax" || ErrDuplicateKey.String() != "duplicate_key" || Code(99).String() != "code(99)" {
		t.Fatal("Code.String")
	}
	e := &Error{Code: ErrSyntax, Msg: "unexpected comma", Offset: 5}
	if e.Error() != "unexpected comma at byte 5" {
		t.Fatalf("%q", e.Error())
	}
	e.Path = "messages[2].content"
	if e.Error() != "unexpected comma at byte 5 in messages[2].content" {
		t.Fatalf("%q", e.Error())
	}
}
