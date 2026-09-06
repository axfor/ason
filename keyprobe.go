package ason

// KeyProbe 是"只关心顶层若干 key"的透传型协议：命中的 key 整个值攒下来交给回调（可原位替换），
// 其余字节原样直通（空白、顺序、转义一个不改，效果与 sjson 的原地改写一致）。
//
// 同一个 key 出现多次时只有第一次触发回调，后面的原样保留——与 gjson 取首个、sjson 改首个一致。
// 需要更多状态的调用方可以嵌入 *KeyProbe 并覆盖 OnValue（先做自己的事再委托），
// 例如在值到齐时顺便记录"是否已见到某个字段"。
type KeyProbeOptions struct {
	// Keys：要捕获的顶层 key 及其字节上限（超出上限判定不支持）。
	Keys map[string]int
	// OnKey：值到齐时回调。raw 是值的原始 JSON 文本；返回 (replacement, true) 则用 replacement 原位替换，
	// 否则原样写回。回调内可用 t.Bail 判定不支持。
	OnKey func(t *Transformer, key string, raw []byte) (replacement []byte, replace bool)
	// Observe：只观察不改写。key 与值原样直通，回调只拿到副本（此时 OnKey 的返回值被忽略）。
	Observe bool
}

// KeyProbe 实现 Protocol。
type KeyProbe struct {
	BaseProtocol
	Opt  KeyProbeOptions
	seen map[string]bool
	vals map[string][]byte
}

// NewKeyProbe 构造探针协议（配合 NewTransformer 使用，或用 NewKeyProbeTransformer）。
func NewKeyProbe(opt KeyProbeOptions) *KeyProbe {
	return &KeyProbe{Opt: opt, seen: map[string]bool{}, vals: map[string][]byte{}}
}

// NewKeyProbeTransformer 是 NewTransformer(NewKeyProbe(opt)) 的简写。
func NewKeyProbeTransformer(opt KeyProbeOptions) *Transformer {
	return NewTransformer(NewKeyProbe(opt))
}

// Captured 返回已捕获的顶层 key 的原始值（第一次出现的那个），扫描结束后可用。
func (p *KeyProbe) Captured() map[string][]byte { return p.vals }

func (p *KeyProbe) OnKey(t *Transformer) Action {
	if t.Depth() != 1 {
		return Pass()
	}
	k := t.Last()
	cap, want := p.Opt.Keys[k]
	if !want || p.seen[k] {
		return Pass()
	}
	p.seen[k] = true
	if p.Opt.Observe {
		return Observe(cap)
	}
	return Capture(cap)
}

func (p *KeyProbe) OnValue(t *Transformer, raw []byte) {
	k := t.Last()
	p.vals[k] = append([]byte(nil), raw...)
	if p.Opt.Observe {
		if p.Opt.OnKey != nil {
			p.Opt.OnKey(t, k, raw)
		}
		return
	}
	out := raw
	if p.Opt.OnKey != nil {
		if r, ok := p.Opt.OnKey(t, k, raw); ok {
			out = r
		}
	}
	w := t.W()
	w.KeyRaw(t.KeyRaw())
	w.Raw(out)
}
