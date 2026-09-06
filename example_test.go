package ason_test

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/axfor/ason"
)

// feed 分块喂入并收尾，返回输出与是否判定不支持。
func feed(tr *ason.Transformer, in string, chunk int) (string, string) {
	var out bytes.Buffer
	for i := 0; i < len(in); i += chunk {
		j := i + chunk
		if j > len(in) {
			j = len(in)
		}
		tr.Write([]byte(in[i:j]))
		out.Write(tr.Out())
	}
	out.Write(tr.Finish())
	if bad, why := tr.Unsupported(); bad {
		return "", why
	}
	return out.String(), ""
}

// 透传：BaseProtocol 对一切返回 Pass，没动的字节一个不改（空白、顺序、转义都保留）。
func Example_passthrough() {
	in := "{\n  \"model\" : \"m\" ,\n  \"messages\" : [ { \"role\" : \"user\" } ]\n}\n"
	out, _ := feed(ason.NewTransformer(ason.BaseProtocol{}), in, 3)
	fmt.Println(out == in)
	// Output: true
}

// 改名：OnKey 返回 Pass().As(新名)。
type renameProto struct{ ason.BaseProtocol }

func (renameProto) OnKey(t *ason.Transformer) ason.Action {
	if t.Depth() == 1 && t.Last() == "max_tokens" {
		return ason.Pass().As("max_completion_tokens")
	}
	return ason.Pass()
}

func Example_rename() {
	out, _ := feed(ason.NewTransformer(renameProto{}), `{"model":"m","max_tokens":10,"n":{"max_tokens":1}}`, 5)
	fmt.Println(out)
	// Output: {"model":"m","max_completion_tokens":10,"n":{"max_tokens":1}}
}

// 原位改写：KeyProbe 捕获顶层 key，回调里决定替换值；其余字节直通。
func Example_rewrite() {
	tr := ason.NewKeyProbeTransformer(ason.KeyProbeOptions{
		Keys: map[string]int{"model": 4096},
		OnKey: func(t *ason.Transformer, key string, raw []byte) ([]byte, bool) {
			if i := bytes.IndexByte(raw, '/'); i >= 0 { // "provider/model" → "model"
				return append([]byte(`"`), raw[i+1:]...), true
			}
			return nil, false
		},
	})
	out, _ := feed(tr, `{"messages":[{"role":"user","content":"hi"}],"model":"openai/gpt-4o"}`, 7)
	fmt.Println(out)
	fmt.Println(string(tr.Protocol().(*ason.KeyProbe).Captured()["model"]))
	// Output:
	// {"messages":[{"role":"user","content":"hi"}],"model":"gpt-4o"}
	// "openai/gpt-4o"
}

// 脱敏：Prefix(n) 只攒字符串的前 n 字节，写出前缀后 Skip 掉剩余部分——长字符串不进内存。
type redactProto struct{ ason.BaseProtocol }

func (redactProto) OnKey(t *ason.Transformer) ason.Action {
	if t.Last() == "api_key" {
		return ason.Prefix(4)
	}
	return ason.Pass()
}
func (redactProto) OnPrefix(t *ason.Transformer, raw []byte, complete bool) (ason.Action, int) {
	w := t.W()
	w.KeyRaw(t.KeyRaw())
	w.RawString(`"` + string(raw) + `***"`)
	return ason.Skip(), 0
}

func Example_redact() {
	out, _ := feed(ason.NewTransformer(redactProto{}), `{"api_key":"sk-1234567890abcdef","user":"u"}`, 4)
	fmt.Println(out)
	// Output: {"api_key":"sk-1***","user":"u"}
}

// 重组：一个输入容器落到多层嵌套输出——messages 变成 input.messages。
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
	w := t.W()
	w.PushObj("input")
	w.PushArr("messages")
	return ason.Enter().Flat() // 元素直接落进自建的 input.messages
}
func (nestProto) OnLeave(t *ason.Transformer) {
	if t.Depth() == 1 {
		w := t.W()
		w.Open() // 空数组也物化
		w.Pop()
		w.Pop()
	}
}

func Example_restructure() {
	out, _ := feed(ason.NewTransformer(nestProto{}), `{"model":"m","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"}],"stream":true}`, 6)
	fmt.Println(out)
	// Output: {"model":"m","input":{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"}]},"stream":true}
}

// 回放：content 先于 role 到达时 Defer 起来，见到 role 后 Release 重新派发——不对字段顺序做假设。
type roleProto struct {
	ason.BaseProtocol
	role string
}

func (p *roleProto) OnKey(t *ason.Transformer) ason.Action {
	switch t.Last() {
	case "role":
		return ason.Capture(64)
	case "content":
		if p.role == "" {
			return ason.Defer(1 << 20) // 还不知道 role：暂存（有上限）
		}
		if p.role == "system" {
			return ason.Pass().As("instruction")
		}
		return ason.Pass()
	}
	return ason.Pass()
}
func (p *roleProto) OnValue(t *ason.Transformer, raw []byte) {
	if t.Last() == "role" {
		p.role, _ = ason.JSONUnquote(raw)
		t.W().KeyRaw(t.KeyRaw())
		t.W().Raw(raw)
		t.Release() // 当前值结束后回放 Defer 的 content
	}
}

func Example_deferReplay() {
	out, _ := feed(ason.NewTransformer(&roleProto{}), `{"content":"be brief","role":"system"}`, 1)
	fmt.Println(out)
	// Output: {"role":"system","instruction":"be brief"}
}

// 子 hook：Enter().Via(hook) 把整棵子树的回调交给另一个 Protocol；容器闭合的 OnLeave 回到发起方。
type prefixKeys struct {
	ason.BaseProtocol
	prefix string
}

func (h *prefixKeys) OnKey(t *ason.Transformer) ason.Action {
	return ason.Pass().As(h.prefix + t.Last())
}

type viaProto struct {
	ason.BaseProtocol
	meta prefixKeys
}

func (p *viaProto) OnKey(t *ason.Transformer) ason.Action {
	if t.Depth() == 1 && t.Last() == "meta" {
		return ason.Probe()
	}
	return ason.Pass()
}
func (p *viaProto) OnStart(t *ason.Transformer, kind ason.ValueKind) ason.Action {
	return ason.Enter().Via(&p.meta)
}

func Example_subHook() {
	out, _ := feed(ason.NewTransformer(&viaProto{meta: prefixKeys{prefix: "x_"}}), `{"meta":{"a":1,"b":{"c":2}},"d":3}`, 2)
	fmt.Println(out)
	// Output: {"meta":{"x_a":1,"x_b":{"c":2}},"d":3}
}

// 附件流式：data URL 只看前缀拆出 mime，base64 主体直通到另一个形状里，永不进内存。
type dataURLProto struct{ ason.BaseProtocol }

func (dataURLProto) OnKey(t *ason.Transformer) ason.Action {
	if t.Last() == "image" {
		return ason.Prefix(128)
	}
	return ason.Pass()
}
func (dataURLProto) OnPrefix(t *ason.Transformer, raw []byte, complete bool) (ason.Action, int) {
	dec, off := ason.UnescapePrefix(raw)
	if !bytes.HasPrefix(dec, []byte("data:")) {
		return ason.Bail("不是 data URL"), 0
	}
	comma := bytes.IndexByte(dec, ',')
	if comma < 0 {
		return ason.Bail("data URL 头超出窗口"), 0
	}
	mime := strings.TrimSuffix(string(dec[5:comma]), ";base64")
	w := t.W()
	w.Key("image")
	w.RawString(`{"mime":"` + mime + `","data":"`)
	return ason.Pass().Wrap(nil, []byte(`"}`)), off[comma+1] // 从逗号后的原始偏移续传
}

func Example_dataURL() {
	body := `{"image":"data:image/png;base64,` + strings.Repeat("iVBORw0KGgo=", 100) + `","n":1}`
	out, _ := feed(ason.NewTransformer(dataURLProto{}), body, 16)
	fmt.Println(strings.HasPrefix(out, `{"image":{"mime":"image/png","data":"iVBORw0KGgo=`), strings.HasSuffix(out, `"},"n":1}`), len(out))
	// Output: true true 1246
}

// 严格校验：与 encoding/json 同样的拒绝面，任何位置的语法错误都会判定不支持。
func Example_strict() {
	_, why := feed(ason.NewTransformer(ason.BaseProtocol{}), `{"a":1,"b":tru}`, 1)
	fmt.Println(why)
	// Output: 不完整的标量字面量
}
