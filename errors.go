package ason

import "strconv"

// Code 把"为什么停下"分成几类，调用方按它决定回落、失败还是报告协议缺陷，不用匹配文案。
type Code uint8

const (
	ErrNone          Code = iota
	ErrSyntax             // 不符合 JSON 文法：字面量、数字、转义、控制字符、结构、（开启校验时）UTF-8
	ErrIncomplete         // Finish 时输入还没到根闭合
	ErrRoot               // 根不是 SetRoot 允许的形状
	ErrTrailing           // 根之后还有非空白内容
	ErrDuplicateKey       // 派发帧里重复 key（DupKeysBail / DupKeyBail）
	ErrLimit              // Capture / Observe / Defer / Prefix 超过上限，或总预算超限
	ErrLeftoverDefer      // 容器闭合时仍有未回放也未丢弃的 Defer 项
	ErrUnsupported        // 协议表达不了这个形状：协议自己 Bail，或 Enter / Inner / Prefix 遇到的值类型不对
	ErrMisuse             // 协议用错了 API：Probe 嵌套、Defer 用在数组元素上、OnPrefix 返回非法动作 / 偏移等
)

var codeNames = [...]string{"none", "syntax", "incomplete", "root", "trailing", "duplicate_key", "limit", "leftover_defer", "unsupported", "misuse"}

func (c Code) String() string {
	if int(c) < len(codeNames) {
		return codeNames[c]
	}
	return "code(" + strconv.Itoa(int(c)) + ")"
}

// Error 是判定不支持的完整描述。Msg 是英文；协议 Bail 的原文原样保留。
type Error struct {
	Code   Code
	Msg    string
	Offset int64  // 判定时已消费的输入字节数：文法错误指向出错的字节，回调里判定的是当前扫描位置，Finish 里判定的是总长度
	Path   string // 发生时的路径，如 messages[2].content；根上为空
}

func (e *Error) Error() string {
	s := e.Msg + " at byte " + strconv.FormatInt(e.Offset, 10)
	if e.Path != "" {
		s += " in " + e.Path
	}
	return s
}

// Err 返回判定不支持的详情；nil 表示正常。
func (t *Transformer) Err() *Error { return t.err }

// BailCode 与 Bail 相同，但带分类。
func BailCode(code Code, reason string) Action {
	return Action{kind: akBail, level: -1, reason: reason, code: code}
}

// BailErr 由协议或框架调用：判定不支持，停止扫描。第一次调用的原因保留，之后的忽略。
func (t *Transformer) BailErr(code Code, reason string) {
	if !t.unsupported {
		t.unsupported = true
		t.err = &Error{Code: code, Msg: reason, Offset: -1, Path: t.PathString()}
	}
	t.dead = true
}

// Bail 由协议调用：判定不支持（ErrUnsupported），停止扫描。
func (t *Transformer) Bail(reason string) { t.BailErr(ErrUnsupported, reason) }

// fixOffset 把还没定位的错误定在 off。
func (t *Transformer) fixOffset(off int64) {
	if t.err != nil && t.err.Offset < 0 {
		t.err.Offset = off
	}
}
