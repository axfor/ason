// Package conv 是一个"聊天请求转换"示例协议：把一种常见的聊天请求形状流式转换成另一种。
// 它刻意用到了引擎的每一条路径——Capture / Defer 回放 / Prefix 拆 data URL / Via 子 hook /
// Lazy 层 / 自建输出层 / Tail 补字段——是引擎场景测试的载体，也是写自己协议时的参照。
//
// 输入（源形状）                                   输出（目标形状）
//
//	model                                          model（经 MapModel）
//	messages[].role == "system" 的 content          system：所有 system 文本用 "\n" 连接（没有则省略）
//	messages[].content 字符串                        content:[{"type":"text","text":…}]
//	messages[].content 数组：text 部分              {"type":"text","text":…}
//	                        image_url 的 data URL   {"type":"image","source":{"type":"base64","media_type":…,"data":…}}
//	max_tokens                                     max_tokens（缺省 1024）
//	stop                                           stop_sequences（空数组省略；元素必须是字符串）
//	tools[].function{name,description,parameters}  tools[]{name,description,input_schema}（空 parameters 省略）
//	其余顶层字段                                     丢弃
//
// 不支持（Bail）：messages 不是数组、消息没有 role、role 不是字符串、content 是 null / 数字 / 对象、
// stop 元素不是字符串、图片不是 data URL、data URL 头超出 512 字节窗口。
package conv

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/axfor/ason"
)

// Options 是协议选项。
type Options struct {
	// MapModel 把源 model 映射成目标 model；nil 表示原样。
	MapModel func(string) string
	// DefaultMaxTokens 缺省 max_tokens。
	DefaultMaxTokens int
}

const (
	roleWaitCap = 64 << 10 // content 先于 role 到达时最多暂存这么多
	systemCap   = 1 << 20  // system 内容整体捕获的上限
	partWaitCap = 8 << 20  // part 的 type 先于内容到达时最多暂存这么多（图片）
	urlWindow   = 512      // data URL 头的窗口
	smallCap    = 4 << 10  // model / max_tokens / role / type
)

type proto struct {
	ason.BaseProtocol
	opt      Options
	tools    toolsHook
	model    string
	modelOK  bool
	maxTok   string // 原始数字文本
	system   []string
	m        msg
	msgsSeen bool // messages 数组出现过
	outMsgs  int  // 写出的（非 system）消息数
}

type msg struct {
	role       string
	roleSeen   bool
	system     bool
	partType   string
	partTypeOK bool
}

// New 构造转换器。
func New(opt Options) *ason.Transformer {
	if opt.DefaultMaxTokens == 0 {
		opt.DefaultMaxTokens = 1024
	}
	if opt.MapModel == nil {
		opt.MapModel = func(s string) string { return s }
	}
	p := &proto{opt: opt}
	tr := ason.NewTransformer(p)
	tr.DupKeyBail = true
	return tr
}

// ---- 顶层 ----

func (p *proto) OnKey(t *ason.Transformer) ason.Action {
	switch t.Depth() {
	case 1:
		switch t.Last() {
		case "model", "max_tokens":
			return ason.Capture(smallCap)
		case "messages", "stop", "tools":
			return ason.Probe()
		}
		return ason.Skip()
	case 3: // messages[i].K
		return p.msgKey(t)
	case 5: // messages[i].content[j].K
		return p.partKey(t)
	case 6: // messages[i].content[j].image_url.K
		return p.urlKey(t)
	}
	return ason.Skip()
}

func (p *proto) OnElem(t *ason.Transformer) ason.Action {
	switch t.Depth() {
	case 2, 4: // messages[i] / stop[i] / content[j]
		return ason.Probe()
	}
	return ason.Skip()
}

func (p *proto) OnStart(t *ason.Transformer, kind ason.ValueKind) ason.Action {
	switch t.Depth() {
	case 1:
		switch t.Last() {
		case "messages":
			if kind != ason.KindArray {
				return ason.Bail("messages 不是数组")
			}
			p.msgsSeen = true
			return ason.Enter().Lazy() // 全是 system 时 Tail 补 "messages":[]
		case "stop":
			switch kind {
			case ason.KindArray:
				return ason.Enter().As("stop_sequences").Lazy()
			case ason.KindNull:
				return ason.Skip()
			}
			return ason.Bail("stop 不是数组")
		case "tools":
			switch kind {
			case ason.KindArray:
				return ason.Enter().Lazy().Via(&p.tools)
			case ason.KindNull:
				return ason.Skip()
			}
			return ason.Bail("tools 不是数组")
		}
	case 2:
		if t.Key(0) == "stop" {
			if kind != ason.KindString {
				return ason.Bail("stop 元素不是字符串")
			}
			return ason.Pass()
		}
		if kind != ason.KindObject {
			return ason.Bail("消息不是对象")
		}
		p.m = msg{}
		return ason.Enter().Lazy() // system 消息不产生元素
	case 3: // content
		return p.contentStart(t, kind)
	case 4: // content[j]
		if kind != ason.KindObject {
			return ason.Skip()
		}
		p.m.partType, p.m.partTypeOK = "", false
		return ason.Enter().Lazy()
	case 5:
		return p.partStart(t, kind)
	}
	return ason.Skip()
}

func (p *proto) OnValue(t *ason.Transformer, raw []byte) {
	w := t.W()
	switch t.Depth() {
	case 1:
		switch t.Last() {
		case "model":
			s, ok := ason.JSONUnquote(raw)
			if !ok {
				t.Bail("model 不是字符串")
				return
			}
			p.model, p.modelOK = s, true
			w.Key("model")
			w.JSONString(p.opt.MapModel(s)) // 原位写出映射后的 model
		case "max_tokens":
			if !ason.IsIntLiteral(raw) {
				t.Bail("max_tokens 不是整数")
				return
			}
			p.maxTok = string(raw)
		}
	case 3:
		switch t.Last() {
		case "role":
			s, ok := ason.JSONUnquote(raw)
			if !ok {
				t.Bail("role 不是字符串")
				return
			}
			p.m.role, p.m.roleSeen = s, true
			p.m.system = s == "system"
			if p.m.system {
				t.Release() // 让暂存的 content 按 system 规则重新派发（进 system 缓冲）
				return
			}
			p.outMsgs++
			w.Key("role")
			w.Raw(raw)
			t.Release() // 暂存的 content 现在知道怎么写了
		case "content": // system 的 content 整体捕获
			p.system = append(p.system, systemText(raw))
		}
	case 5:
		if t.Last() == "type" {
			s, _ := ason.JSONUnquote(raw)
			p.m.partType, p.m.partTypeOK = s, true
			t.Release()
		}
	}
}

func (p *proto) OnLeave(t *ason.Transformer) {
	switch t.Depth() {
	case 2:
		if t.Key(0) != "messages" {
			return
		}
		if !p.m.roleSeen {
			t.Bail("消息没有 role")
		}
	case 4: // part 闭合：type 缺失或未知的 part 丢弃（连同暂存的内容）
		if !p.m.partTypeOK || (p.m.partType != "text" && p.m.partType != "image_url") {
			t.DropDeferred()
		}
	}
}

func (p *proto) Tail(t *ason.Transformer) {
	w := t.W()
	if !p.modelOK {
		w.Key("model")
		w.JSONString(p.opt.MapModel(""))
	}
	if p.msgsSeen && p.outMsgs == 0 {
		// messages 里全是 system（或为空）：目标形状仍要有 messages
		w.Key("messages")
		w.RawString("[]")
	}
	w.Key("max_tokens")
	if p.maxTok != "" {
		w.RawString(p.maxTok)
	} else {
		w.Int(p.opt.DefaultMaxTokens)
	}
	if len(p.system) > 0 {
		w.Key("system")
		w.JSONString(strings.Join(p.system, "\n"))
	}
}

// ---- 消息 ----

func (p *proto) msgKey(t *ason.Transformer) ason.Action {
	switch t.Last() {
	case "role":
		return ason.Capture(smallCap)
	case "content":
		if !p.m.roleSeen {
			return ason.Defer(roleWaitCap) // 还不知道 role：暂存
		}
		if p.m.system {
			return ason.Capture(systemCap)
		}
		return ason.Probe()
	}
	return ason.Skip()
}

func (p *proto) contentStart(t *ason.Transformer, kind ason.ValueKind) ason.Action {
	switch kind {
	case ason.KindString:
		return ason.Pass().Wrap([]byte(`[{"type":"text","text":`), []byte(`}]`))
	case ason.KindArray:
		return ason.Enter().As("content")
	}
	return ason.Bail("content 既不是字符串也不是数组")
}

func (p *proto) partKey(t *ason.Transformer) ason.Action {
	switch t.Last() {
	case "type":
		return ason.Capture(smallCap)
	case "text", "image_url":
		if !p.m.partTypeOK {
			return ason.Defer(partWaitCap) // type 还没到
		}
		if (t.Last() == "text") != (p.m.partType == "text") {
			return ason.Skip() // 与 type 不符的字段
		}
		return ason.Probe()
	}
	return ason.Skip()
}

func (p *proto) partStart(t *ason.Transformer, kind ason.ValueKind) ason.Action {
	w := t.W()
	switch t.Last() {
	case "text":
		if kind != ason.KindString {
			return ason.Bail("text 不是字符串")
		}
		w.Key("type")
		w.RawString(`"text"`)
		return ason.Pass()
	case "image_url":
		if kind != ason.KindObject {
			return ason.Bail("image_url 不是对象")
		}
		return ason.Enter().Flat()
	}
	return ason.Skip()
}

// 深度 6：image_url.url
func (p *proto) OnPrefix(t *ason.Transformer, raw []byte, complete bool) (ason.Action, int) {
	dec, off := ason.UnescapePrefix(raw)
	if !bytes.HasPrefix(dec, []byte("data:")) {
		return ason.Bail("图片不是 data URL"), 0
	}
	semi := bytes.IndexByte(dec, ';')
	comma := bytes.IndexByte(dec, ',')
	if semi < 0 || comma < 0 || comma < semi {
		if !complete {
			return ason.Bail("data URL 头超出窗口"), 0
		}
		return ason.Bail("data URL 格式不对"), 0
	}
	w := t.W()
	w.Key("type")
	w.RawString(`"image"`)
	w.Key("source")
	w.RawString(`{"type":"base64","media_type":`)
	w.JSONString(string(dec[5:semi]))
	w.RawString(`,"data":"`)
	return ason.Pass().Wrap(nil, []byte(`"}`)), off[comma+1]
}

// ---- 子 hook：tools ----

type toolsHook struct {
	ason.BaseProtocol
	nameSeen bool
	fnSeen   bool
}

func (h *toolsHook) OnElem(t *ason.Transformer) ason.Action { return ason.Probe() }

func (h *toolsHook) OnKey(t *ason.Transformer) ason.Action {
	switch t.Depth() {
	case 3:
		if t.Last() == "function" {
			return ason.Probe()
		}
	case 4:
		switch t.Last() {
		case "name", "description", "parameters":
			return ason.Probe()
		}
	default:
		return ason.Pass() // parameters 内部原样
	}
	return ason.Skip()
}

func (h *toolsHook) OnStart(t *ason.Transformer, kind ason.ValueKind) ason.Action {
	switch t.Depth() {
	case 2:
		if kind != ason.KindObject {
			return ason.Bail("tools 元素不是对象")
		}
		h.nameSeen, h.fnSeen = false, false
		return ason.Enter()
	case 3:
		if kind != ason.KindObject {
			return ason.Bail("function 不是对象")
		}
		h.fnSeen = true
		return ason.Enter().Flat()
	case 4:
		switch t.Last() {
		case "name":
			if kind != ason.KindString {
				return ason.Bail("name 不是字符串")
			}
			h.nameSeen = true
			return ason.Pass()
		case "description":
			return ason.Pass()
		case "parameters":
			if kind != ason.KindObject {
				return ason.Bail("parameters 不是对象")
			}
			return ason.Enter().As("input_schema").Lazy()
		}
	}
	return ason.Skip()
}

func (h *toolsHook) OnLeave(t *ason.Transformer) {
	if t.Depth() == 2 && (!h.fnSeen || !h.nameSeen) {
		t.W().Key("name")
		t.W().RawString(`""`)
	}
}

// 深度 6 的 url 通过顶层 OnKey 派发：这里补上
func (p *proto) urlKey(t *ason.Transformer) ason.Action {
	if t.Last() == "url" {
		return ason.Prefix(urlWindow)
	}
	return ason.Skip()
}

// systemText 取 system 内容的文本：字符串直接用；数组则逐项取 type 为 text 且 text 是字符串的部分连接，其余忽略。
func systemText(raw []byte) string {
	if s, ok := ason.JSONUnquote(raw); ok {
		return s
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return ""
	}
	var sb strings.Builder
	for _, it := range items {
		var part struct {
			Type string          `json:"type"`
			Text json.RawMessage `json:"text"`
		}
		if json.Unmarshal(it, &part) != nil || part.Type != "text" {
			continue
		}
		if s, ok := ason.JSONUnquote(part.Text); ok {
			sb.WriteString(s)
		}
	}
	return sb.String()
}
