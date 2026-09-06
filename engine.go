// Package ason 是流式跨协议 JSON 转换框架：
//
//	层 1 Scanner  —— 协议无关的字节级扫描器（本文件），把输入切成 key / 值 / 容器事件
//	层 2 Protocol —— 每协议一套手写 hooks（proto_*.go），对每个事件返回动作
//	层 3 Guard    —— 提交点 / 回落窗口（本文件的 committed / Bail 语义 + 集成层）
//
// 扫描器不建对象树。值要么原样流向输出（Pass），要么丢弃（Skip），
// 要么在协议明确要求时才进入有界缓冲（Capture / Defer / Prefix）。
// 内存与输入大小无关，只与协议要求缓冲的那几个小值有关。
package ason

import (
	"encoding/json"
	"strconv"
)

type scanState uint8

const (
	sIdle scanState = iota
	sInKey
	sInStr
	sInScalar
)

type frameKind uint8

const (
	fkObj frameKind = iota
	fkArr
)

type phase uint8

const (
	phKey   phase = iota // 对象：期待 key（或 }）
	phColon              // 对象：期待 :
	phValue              // 期待值（数组：或 ]）
	phComma              // 期待 , 或闭合
)

// 区域内部的文法阶段：把"当前容器是对象还是数组"编码进阶段里，结构字符的合法性与后继阶段查表即得。
type regPhase uint8

const (
	rErr     regPhase = iota // 查表结果：非法
	rTop                     // 区域顶层，期待值（区域一开始）
	rKey0                    // 对象刚打开：期待 key 或 }
	rKey                     // 对象逗号之后：期待 key
	rColon                   // key 之后：期待 :
	rOValue                  // 冒号之后：期待值
	rOComma                  // 对象里值之后：期待 , 或 }
	rAValue0                 // 数组刚打开：期待值或 ]
	rAValue                  // 数组逗号之后：期待值
	rAComma                  // 数组里值之后：期待 , 或 ]
)

var (
	// 字符串开头之后的阶段（key 或值）。
	regAfterStr = [...]regPhase{rKey0: rColon, rKey: rColon, rOValue: rOComma, rAValue0: rAComma, rAValue: rAComma, rAComma: rErr}
	// 标量 / 容器开头之后（容器再由 regPush 覆盖）。
	regAfterVal = [...]regPhase{rOValue: rOComma, rAValue0: rAComma, rAValue: rAComma, rAComma: rErr}
	// 逗号之后。
	regAfterComma = [...]regPhase{rOComma: rKey, rAComma: rAValue}
	// 能否闭合：0 = 不能（缺 key / 缺值 / 多余逗号），1 = 对象可闭合，2 = 数组可闭合。
	regClose = [...]uint8{rKey0: 1, rOComma: 1, rAValue0: 2, rAComma: 2}
)

// frame 是一个 Enter 进来的容器（派发帧）。区域内部的容器不建帧，只计深度。
type frame struct {
	kind     frameKind
	ph       phase
	idx      int
	n        int  // 已完成的值个数（对象）
	flat     bool // 输出侧没有对应层
	lazy     bool // 闭合时不物化空容器
	seen     []string
	deferred []DeferredKV
	hook     Protocol // 这一帧内的回调接收方；nil = 主协议
}

// DeferredKV 是 Defer 暂存的一对 key/value 原始字节。
type DeferredKV struct {
	Key    string
	KeyRaw []byte // [空白]"key"[空白]:[空白]
	Raw    []byte
}

type seg struct {
	k string
	i int // -1 = key 段
}

type regionTarget uint8

const (
	rtNone regionTarget = iota
	rtOut
	rtSkip
	rtCapture
	rtDefer
	rtObserve
	rtPrefix
)

// internKeysMax 是 key 驻留表的上限：正常文档的不同 key 远少于此，对抗性输入（海量不同 key）不会让表无限增长。
const internKeysMax = 4096

// CommitBytes 是提交点窗口：扫描这么多输入字节之前不下发任何输出。
// 越过之前判定不支持，调用方仍持有全部原始字节，可以干净回落。
const CommitBytes = 64 << 10

// Transformer 把一个 Protocol 接到扫描器上。Write 逐块喂入，Out 取出可下发的字节。
type Transformer struct {
	proto Protocol
	w     Writer

	// DupKeyBail：派发帧内出现重复 key 时判定不支持。
	// 目标协议用 struct 解析（后者覆盖前者）时应开启；字节透传类协议不需要。
	DupKeyBail bool

	st     scanState
	esc    bool
	hexN   uint8 // \u 转义还需读取的 hex 位数
	lit    litState
	depth  int
	frames []frame
	path   []seg
	keyBuf []byte
	keyEsc bool

	pend    Action
	pendSet bool

	// 原样保留派发帧里 key 周围的空白：kvRaw = [空白]"key"[空白]:[空白]，elemWs = 元素前空白。
	// 透传类协议靠它做到"没动的字节一个不改"，效果与 sjson 的原地修改一致。
	kvRaw       []byte
	wsRaw       []byte
	elemWs      []byte
	rootCloseWs []byte
	tailWs      []byte            // 根对象之后的空白（如尾部换行）：Finish 时原样吐出
	keys        map[string]string // key 驻留表：重复出现的 key 不再分配（上限 internKeysMax 个）

	validateUTF8 bool
	u8           utf8State
	dup          DupKeys
	root         RootKind

	commit        int    // 提交点窗口；0 = CommitBytes
	budget        int    // 所有缓冲之和的上限；0 = 不限
	deferredBytes int    // 当前所有派发帧里 Defer 项占用的字节数
	leadWs        []byte // 根对象之前的空白：根打开时原样吐出

	regOpen  bool
	regT     regionTarget
	regDepth int
	regInner bool
	regSuf   []byte
	regCap   int
	regKey   string
	capBuf   []byte
	// 区域内部的文法状态：只在结构字符上更新，数据字节不经过它。与派发帧一样按 JSON 文法拒绝
	// 括号种类不配、缺 key / 缺冒号 / 缺值、多余逗号——区域里的 JSON 同样必须是 encoding/json 会接受的。
	regPh    regPhase
	regN     int    // 区域内容器层数
	regKinds uint64 // 前 64 层容器种类的位栈（1 = 数组），第 n 层在第 n 位
	regDeep  []bool // 超过 64 层时的溢出栈（罕见）

	wantRelease bool
	releaseAt   int // 请求回放时所在的派发帧号：只在该帧的安全点消费，进入子帧不会误消费

	scanned     int
	committed   bool
	unsupported bool
	err         *Error
	sink        func([]byte)
	limitAt     int   // 上限 / 预算超限时：本次追加里装得下的字节数（定位第一个装不下的字节）
	scanBase    int64 // 当前 Write 的块在整个输入里的起始偏移
	replaying   int   // > 0：正在回放 Defer 项（嵌套 scan），错误偏移取外层位置
	dead        bool
	rootSeen    bool
	rootDone    bool
}

// NewTransformer 用指定协议构造转换器。
func NewTransformer(p Protocol) *Transformer {
	return &Transformer{proto: p}
}

// ---- 对外：Guard 语义 ----

// DupKeys 是派发帧里重复 key 的策略。
type DupKeys uint8

const (
	DupKeysPass  DupKeys = iota // 默认：不检查，重复的 key 照常派发（透传语义）
	DupKeysBail                 // 判定不支持（目标是 struct 语义、后者覆盖前者、流式无法复刻时）
	DupKeysFirst                // 只派发第一个，后面的同名 key 自动 Skip（gjson 取首个的语义）
)

// SetDupKeys 设置重复 key 策略（DupKeyBail 字段等价于 DupKeysBail，保留兼容）。
func (t *Transformer) SetDupKeys(d DupKeys) { t.dup = d }

// RootKind 是允许的根形状。
type RootKind uint8

const (
	RootObject RootKind = iota // 默认
	RootArray
	RootAny // 对象或数组
)

// SetRoot 设置允许的根形状。根是数组时深度 1 是下标，经 OnElem 派发。
func (t *Transformer) SetRoot(k RootKind) { t.root = k }

// SetValidateUTF8 开启字符串与 key 的 UTF-8 校验（RFC 3629：拒绝过长编码、代理对、超出 U+10FFFF、
// 孤立或缺失的续字节，含跨块的序列）。默认关闭——encoding/json 不拒绝非法 UTF-8，只替换。
func (t *Transformer) SetValidateUTF8(on bool) { t.validateUTF8 = on }

// SetCommitBytes 设置本转换器的提交点窗口（0 恢复包默认 CommitBytes）。必须在第一次 Write 之前调用。
func (t *Transformer) SetCommitBytes(n int) { t.commit = n }

// SetBudget 设置所有缓冲（Capture / Observe / Prefix 窗口、Defer 暂存、提交前攒着的输出）之和的上限，
// 超过即判定不支持（"缓冲预算超限"）。0 = 不限。这是内存上界的总量保证；各处 cap 仍是单项约束。
func (t *Transformer) SetBudget(n int) { t.budget = n }

// Buffered 报告当前持有的缓冲字节数（观测用）。
func (t *Transformer) Buffered() int {
	n := len(t.capBuf) + t.deferredBytes
	if !t.committed {
		n += len(t.w.buf)
	}
	return n
}

func (t *Transformer) commitBytes() int {
	if t.commit > 0 {
		return t.commit
	}
	return CommitBytes
}

// checkBudget 在缓冲增长处调用。
func (t *Transformer) checkBudget(extra int) bool {
	if t.budget > 0 && t.Buffered()+extra > t.budget {
		t.BailErr(ErrLimit, "buffer budget exceeded")
		return false
	}
	return true
}

// Committed 报告是否已越过提交点。越过之后再判定不支持，已发出的字节收不回来。
func (t *Transformer) Committed() bool { return t.committed }

// Dead 报告转换器是否已停止（判定不支持之后）。协议在回调里可据此提前返回。
func (t *Transformer) Dead() bool { return t.dead }

// Unsupported 报告是否遇到了处理不了的输入。为 true 时输出不可用。
// 文案是 Err().Error()：原因 + 字节偏移 + 路径，适合直接进日志；按类别处理用 Err().Code。
func (t *Transformer) Unsupported() (bool, string) {
	if t.err == nil {
		return false, ""
	}
	return true, t.err.Error()
}

// SetSink 设置输出接收方。设了之后，每次 Write / Finish 里越过提交点后产生的输出直接交给 sink，
// 输出缓冲随后复用而不是交出所有权——每块不再分配一次、整条流不再制造与输入等量的垃圾，
// 适合能立即消费的调用方（写宿主、写连接）。sink 返回前必须消费完 b（拷贝或写出），返回后 b 失效。
// 设了 sink 之后 Out() 总是返回空。必须在第一次 Write 之前调用。
func (t *Transformer) SetSink(sink func(b []byte)) { t.sink = sink }

// drain 把可下发的输出交给 sink（提交点之后、未判定不支持时）。
func (t *Transformer) drain(chunk int) {
	if t.unsupported || !t.committed || len(t.w.buf) == 0 {
		return
	}
	t.sink(t.w.buf)
	if cap(t.w.buf) > 2*chunk+4096 {
		t.w.buf = nil // 提交前攒下的大缓冲不留着；下一块按块大小重新分配后一直复用
		t.w.hint = chunk
	} else {
		t.w.buf = t.w.buf[:0]
	}
}

// Out 取走可下发的字节。未越过提交点、或已判定不支持、或设了 sink 时返回空。
func (t *Transformer) Out() []byte {
	if t.unsupported || !t.committed || len(t.w.buf) == 0 || t.sink != nil {
		return nil
	}
	// 交出所有权，不拷贝也不保留容量：提交点前攒下的大缓冲（可达 128KB）随之变成垃圾，
	// 而不是被这条流持有到结束——高并发下每条在途流的存活内存由此从 ~250KB 降到几十 KB。
	// 调用方拿到的切片归它所有；下一次写入会重新分配。
	b := t.w.buf
	t.w.hint = len(b)
	t.w.buf = nil
	return b
}

// Write 喂入一块输入，可在任意字节边界切分。
func (t *Transformer) Write(p []byte) {
	if t.dead {
		return
	}
	t.scanBase = int64(t.scanned)
	t.scanned += len(p)
	t.w.reserve(len(p)) // 一块输出只分配一次缓冲（按上次交出的大小预留）
	t.scan(p)
	t.fixOffset(int64(t.scanned))
	if !t.committed && !t.unsupported {
		if !t.checkBudget(0) {
			return
		}
		if t.scanned >= t.commitBytes() {
			t.committed = true
		}
	}
	if t.sink != nil {
		t.drain(len(p))
	}
}

// Finish 收尾：调用协议 Tail，闭合根对象。
// 若在此判定不支持，committed 保持原值——集成层据此决定回落还是失败。
func (t *Transformer) Finish() []byte {
	if t.dead {
		return nil
	}
	t.scanBase = int64(t.scanned)
	if t.st == sInScalar {
		t.scan([]byte{' '})
		t.fixOffset(int64(t.scanned))
	}
	if !t.rootDone {
		t.BailErr(ErrIncomplete, "unexpected end of input")
		t.fixOffset(int64(t.scanned))
		return nil
	}
	t.proto.Tail(t)
	if t.unsupported {
		t.fixOffset(int64(t.scanned))
		return nil
	}
	t.w.ensureOpen(0)
	t.w.pop(t.rootCloseWs)
	t.w.buf = append(t.w.buf, t.tailWs...) // 根之后的空白（尾部换行）保真
	t.committed = true
	if t.sink != nil {
		t.drain(0)
		return nil
	}
	return t.Out()
}

// cur 返回当前帧的回调接收方（Via 挂载的子 hook，或主协议）。
func (t *Transformer) cur() Protocol {
	if f := t.top(); f != nil && f.hook != nil {
		return f.hook
	}
	return t.proto
}

// hookAt 返回第 i 帧的回调接收方；i < 0 或该帧未挂载时是主协议。
func (t *Transformer) hookAt(i int) Protocol {
	if i >= 0 && i < len(t.frames) && t.frames[i].hook != nil {
		return t.frames[i].hook
	}
	return t.proto
}

// ---- 对协议：路径与输出 ----

// Protocol 返回接入的协议（集成层用它取 Prelude）。
func (t *Transformer) Protocol() Protocol { return t.proto }

// W 输出器。
func (t *Transformer) W() *Writer { return &t.w }

// Depth 当前路径段数。
func (t *Transformer) Depth() int { return len(t.path) }

// Key 第 level 段的 key；该段是数组下标时返回 ""。
func (t *Transformer) Key(level int) string {
	if level < 0 || level >= len(t.path) {
		return ""
	}
	return t.path[level].k
}

// Idx 第 level 段的数组下标；该段是 key 时返回 -1。
func (t *Transformer) Idx(level int) int {
	if level < 0 || level >= len(t.path) {
		return -1
	}
	return t.path[level].i
}

// Last 最后一段的 key。
func (t *Transformer) Last() string { return t.Key(len(t.path) - 1) }

// PathString 调试用："messages[1].content"。
func (t *Transformer) PathString() string {
	var b []byte
	for i, s := range t.path {
		if s.i >= 0 {
			b = append(b, '[')
			b = appendInt(b, s.i)
			b = append(b, ']')
		} else {
			if i > 0 {
				b = append(b, '.')
			}
			b = append(b, s.k...)
		}
	}
	return string(b)
}

// KeyRaw 当前 key 的原始字节（含前导空白、引号、冒号及其周围空白）。协议想原样保留格式时用它。
func (t *Transformer) KeyRaw() []byte { return t.kvRaw }

// Release 请求回放当前派发帧里 Defer 的项。回放发生在当前回调返回后、同一帧的安全点
// （当前值结束或该帧闭合）；若回调返回 Enter 进入了子帧，回放推迟到回到本帧之后。
func (t *Transformer) Release() {
	t.wantRelease = true
	t.releaseAt = len(t.frames) - 1
}

// ReleaseNow 同步回放当前派发帧里 Defer 的项。只能在 OnLeave 里调用——
// 那时路径正指向容器本身，回放的 key 会正确地挂在它下面；在 OnValue 里要用 Release。
func (t *Transformer) ReleaseNow() { t.doRelease() }

// Deferred 查看当前派发帧里 Defer 的项。
func (t *Transformer) Deferred() []DeferredKV {
	if f := t.top(); f != nil {
		return f.deferred
	}
	return nil
}

// DropDeferred 丢弃当前派发帧里 Defer 的项。
func (t *Transformer) DropDeferred() {
	if f := t.top(); f != nil {
		t.forgetDeferred(f)
		f.deferred = nil
	}
}

// forgetDeferred 把一帧里 Defer 项的字节从计数里扣掉（回放或丢弃时）。
func (t *Transformer) forgetDeferred(f *frame) {
	for _, d := range f.deferred {
		t.deferredBytes -= len(d.KeyRaw) + len(d.Raw)
	}
}

// ---- 扫描器 ----

// pushFrame 压入派发帧，复用槽位里上一次留下的 seen / deferred 存储，不再按帧分配。
func (t *Transformer) pushFrame(nf frame) {
	if n := len(t.frames); n < cap(t.frames) {
		old := &t.frames[:n+1][n]
		nf.seen = old.seen[:0]
		nf.deferred = old.deferred[:0]
	}
	t.frames = append(t.frames, nf)
}

func (t *Transformer) top() *frame {
	if len(t.frames) == 0 {
		return nil
	}
	return &t.frames[len(t.frames)-1]
}

func (t *Transformer) scan(p []byte) {
	rs := -1
	if t.regOpen {
		rs = 0
	}
	i := 0
	if t.replaying == 0 {
		defer func() { // 判定不支持时把偏移定在出错的字节（回调里判定的取当前扫描位置）
			if t.dead {
				t.fixOffset(t.scanBase + int64(i))
			}
		}()
	}
scan:
	for i < len(p) && !t.dead {
		c := p[i]
		switch t.st {
		case sInStr:
			if t.esc {
				t.esc = false
				switch escapeClass(c) {
				case 0:
					t.BailErr(ErrSyntax, "invalid escape in string")
					continue
				case 2:
					t.hexN = 4
				}
				i++
				continue
			}
			if t.hexN > 0 {
				if !isHexByte(c) {
					t.BailErr(ErrSyntax, "invalid \\u escape")
					continue
				}
				t.hexN--
				i++
				continue
			}
			var j int
			if t.validateUTF8 {
				j = t.scanStrUTF8(p, i)
				if t.dead {
					i = j // 非法序列的位置
					continue
				}
			} else {
				j = scanStringBody(p, i)
			}
			if j == len(p) {
				i = j
				continue
			}
			i = j
			if p[i] < 0x20 {
				t.BailErr(ErrSyntax, "control character in string")
				continue
			}
			if p[i] == '\\' {
				t.esc = true
				i++
				continue
			}
			// 未转义的引号：字符串结束
			t.st = sIdle
			if t.regOpen && t.depth == t.regDepth {
				end := i + 1
				if t.regInner {
					end = i
				}
				rs = t.flush(p, rs, end)
				t.endRegion()
				t.afterValue()
			}
			i++
		case sInKey:
			if t.esc {
				t.esc = false
				switch escapeClass(c) {
				case 0:
					t.BailErr(ErrSyntax, "invalid escape in key")
					continue
				case 2:
					t.hexN = 4
				}
				t.keyBuf = append(t.keyBuf, c)
				t.kvRaw = append(t.kvRaw, c)
				i++
				continue
			}
			if t.hexN > 0 {
				if !isHexByte(c) {
					t.BailErr(ErrSyntax, "invalid \\u escape")
					continue
				}
				t.hexN--
				t.keyBuf = append(t.keyBuf, c)
				t.kvRaw = append(t.kvRaw, c)
				i++
				continue
			}
			if t.validateUTF8 {
				j := t.scanStrUTF8(p, i)
				if t.dead {
					i = j
					continue
				}
				if j > i {
					t.keyBuf = append(t.keyBuf, p[i:j]...)
					t.kvRaw = append(t.kvRaw, p[i:j]...)
					i = j
					continue
				}
			} else if j := scanStringBody(p, i); j > i { // 普通字节成段追加
				t.keyBuf = append(t.keyBuf, p[i:j]...)
				t.kvRaw = append(t.kvRaw, p[i:j]...)
				i = j
				continue
			}
			if c < 0x20 {
				t.BailErr(ErrSyntax, "control character in key")
				continue
			}
			if c == '\\' {
				t.esc = true
				t.keyEsc = true
				t.keyBuf = append(t.keyBuf, c)
				t.kvRaw = append(t.kvRaw, c)
				i++
				continue
			}
			// c == '"'
			t.st = sIdle
			t.kvRaw = append(t.kvRaw, c)
			t.onKeyDone()
			i++
		case sInScalar:
			if isScalarByte(c) {
				if t.lit.kind == KindNumber { // 内联的表驱动 DFA：数字是区域内最常见的标量
					for i < len(p) && isScalarByte(p[i]) {
						t.lit.num = numStep(t.lit.num, p[i])
						if t.lit.num == nsBad {
							t.BailErr(ErrSyntax, "invalid literal")
							continue scan
						}
						i++
					}
					continue
				}
				for i < len(p) && isScalarByte(p[i]) {
					if !t.lit.step(p[i]) {
						t.BailErr(ErrSyntax, "invalid literal")
						continue scan
					}
					i++
				}
				continue
			}
			if !t.lit.done() {
				t.BailErr(ErrSyntax, "incomplete literal")
				continue
			}
			t.st = sIdle
			if t.regOpen && t.depth == t.regDepth {
				rs = t.flush(p, rs, i)
				t.endRegion()
				t.afterValue()
			}
			// 不消费 c，回到 sIdle 处理
		case sIdle:
			if jsonSpace[c] {
				if !t.regOpen && t.rootSeen && !t.rootDone {
					t.wsRaw = append(t.wsRaw, c)
				} else if t.rootDone {
					t.tailWs = append(t.tailWs, c)
				} else if !t.rootSeen {
					t.leadWs = append(t.leadWs, c)
				}
				i++
				continue
			}
			if t.regOpen {
				// 区域内部：只跟踪文法，不派发。紧凑循环一口气吃掉结构字符与空白，
				// 只在进入字符串 / 标量或区域闭合时回到外层状态机。文法阶段放在局部变量里，退出时写回；
				// 阶段本身编码了当前容器的种类，逗号 / 冒号 / 字符串 / 标量都不用查栈，栈只在括号处动。
				ph := t.regPh
				for i < len(p) {
					c = p[i]
					if jsonSpace[c] {
						i++
						continue
					}
					switch c {
					case '"':
						if ph = regAfterStr[ph]; ph == rErr {
							t.BailErr(ErrSyntax, "unexpected string")
							continue scan
						}
						t.regPh = ph
						t.st = sInStr
						t.esc = false
						i++
						continue scan
					case '{', '[':
						if regAfterVal[ph] == rErr {
							t.BailErr(ErrSyntax, "unexpected object or array")
							continue scan
						}
						t.depth++
						t.regPush(c == '[')
						ph = t.regPh
					case '}', ']':
						k := regClose[ph]
						if k == 0 {
							t.BailErr(ErrSyntax, "missing value or trailing comma before closing bracket")
							continue scan
						}
						if (c == ']') != (k == 2) {
							t.BailErr(ErrSyntax, "mismatched closing bracket")
							continue scan
						}
						ph = t.regPop()
						t.depth--
						if t.depth == t.regDepth {
							t.regPh = ph
							rs = t.flush(p, rs, i+1)
							t.endRegion()
							t.afterValue()
							i++
							continue scan
						}
					case ',':
						if ph = regAfterComma[ph]; ph == rErr {
							t.BailErr(ErrSyntax, "unexpected comma")
							continue scan
						}
					case ':':
						if ph != rColon {
							t.BailErr(ErrSyntax, "unexpected colon")
							continue scan
						}
						ph = rOValue
					default:
						if ph = regAfterVal[ph]; ph == rErr {
							t.BailErr(ErrSyntax, "unexpected literal")
							continue scan
						}
						if !t.lit.start(c) {
							t.BailErr(ErrSyntax, "invalid character")
							continue scan
						}
						t.regPh = ph
						t.st = sInScalar
						i++
						continue scan
					}
					i++
				}
				t.regPh = ph
				continue
			}
			if t.rootDone {
				t.BailErr(ErrTrailing, "data after root value")
				continue
			}
			f := t.top()
			if f == nil {
				// 根
				isArr := c == '['
				if (c == '{' && t.root == RootArray) || (isArr && t.root == RootObject) || (c != '{' && c != '[') {
					switch t.root {
					case RootArray:
						t.BailErr(ErrRoot, "root is not an array")
					case RootAny:
						t.BailErr(ErrRoot, "root is not an object or array")
					default:
						t.BailErr(ErrRoot, "root is not an object")
					}
					continue
				}
				t.rootSeen = true
				t.depth = 1
				if isArr {
					t.pushFrame(frame{kind: fkArr, ph: phValue, idx: -1})
				} else {
					t.pushFrame(frame{kind: fkObj, ph: phKey, idx: -1})
				}
				t.w.buf = append(t.w.buf, t.leadWs...) // 根之前的空白保真
				t.w.push("", nil, isArr)
				t.wsRaw = t.wsRaw[:0]
				i++
				continue
			}
			switch c {
			case '}', ']':
				if (c == '}') != (f.kind == fkObj) {
					t.BailErr(ErrSyntax, "mismatched closing bracket")
					continue
				}
				if f.kind == fkObj && (f.ph == phColon || f.ph == phValue) {
					t.BailErr(ErrSyntax, "missing value after key")
					continue
				}
				if f.kind == fkArr && f.ph == phValue && f.idx >= 0 {
					t.BailErr(ErrSyntax, "trailing comma in array")
					continue
				}
				if f.kind == fkObj && f.ph == phKey && f.n > 0 {
					t.BailErr(ErrSyntax, "trailing comma in object")
					continue
				}
				t.closeContainer()
				i++
			case ':':
				if f.kind != fkObj || f.ph != phColon {
					t.BailErr(ErrSyntax, "unexpected colon")
					continue
				}
				t.kvRaw = append(t.kvRaw, t.wsRaw...)
				t.kvRaw = append(t.kvRaw, ':')
				t.wsRaw = t.wsRaw[:0]
				f.ph = phValue
				i++
			case ',':
				if f.ph != phComma {
					t.BailErr(ErrSyntax, "unexpected comma")
					continue
				}
				t.w.trailWs(t.wsRaw) // 值与逗号之间的空白：挂到输出层，写下一个分隔符时原样吐出
				t.wsRaw = t.wsRaw[:0]
				if f.kind == fkObj {
					f.ph = phKey
				} else {
					f.ph = phValue
				}
				i++
			case '"':
				if f.kind == fkObj && f.ph == phKey {
					t.st = sInKey
					t.esc = false
					t.keyEsc = false
					t.keyBuf = t.keyBuf[:0]
					t.kvRaw = append(t.kvRaw[:0], t.wsRaw...)
					t.kvRaw = append(t.kvRaw, '"')
					t.wsRaw = t.wsRaw[:0]
					f.ph = phColon
					i++
					continue
				}
				if !t.valueStart(f, KindString) {
					continue
				}
				if t.regOpen {
					rs = i
					if t.regInner {
						rs = i + 1
					}
				}
				t.st = sInStr
				t.esc = false
				i++
			case '{', '[':
				kind := KindObject
				if c == '[' {
					kind = KindArray
				}
				if !t.valueStart(f, kind) {
					continue
				}
				if t.regOpen {
					rs = i
					t.regPush(kind == KindArray)
				}
				t.depth++
				i++
			default:
				if !t.lit.start(c) {
					t.BailErr(ErrSyntax, "invalid character")
					continue
				}
				if !t.valueStart(f, t.lit.kind) {
					continue
				}
				if t.regOpen {
					rs = i
				}
				t.st = sInScalar
				i++
			}
		}
	}
	if rs >= 0 && t.regOpen && !t.dead {
		t.flush(p, rs, len(p))
	}
}

// flush 把 p[rs:end] 交给区域，返回新的 rs（-1）。
func (t *Transformer) flush(p []byte, rs, end int) int {
	if rs >= 0 && end > rs {
		t.emitRegion(p[rs:end])
		if t.dead && t.replaying == 0 { // 上限 / 预算超限：偏移定在第一个装不下的字节
			t.fixOffset(t.scanBase + int64(rs) + int64(t.limitAt))
		}
	}
	return -1
}

// valueStart 在派发帧里一个值的第一个字节到达时决定动作。
// 返回 false 表示已 Bail。
func (t *Transformer) valueStart(f *frame, kind ValueKind) bool {
	if f.kind == fkObj {
		if f.ph != phValue {
			t.BailErr(ErrSyntax, "unexpected value")
			return false
		}
		t.kvRaw = append(t.kvRaw, t.wsRaw...)
		t.wsRaw = t.wsRaw[:0]
	} else {
		if f.ph != phValue {
			t.BailErr(ErrSyntax, "missing comma between array elements")
			return false
		}
		// 数组元素开始
		f.idx++
		t.elemWs = append(t.elemWs[:0], t.wsRaw...)
		t.wsRaw = t.wsRaw[:0]
		t.path = append(t.path, seg{i: f.idx})
		t.pend = t.cur().OnElem(t)
		t.pendSet = true
		if t.dead {
			return false
		}
	}
	act := t.pend
	t.pendSet = false
	if act.kind == akProbe {
		act = t.cur().OnStart(t, kind)
		if t.dead {
			return false
		}
	}
	return t.apply(f, act, kind)
}

// apply 执行一个动作。
func (t *Transformer) apply(f *frame, act Action, kind ValueKind) bool {
	isContainer := kind == KindObject || kind == KindArray
	switch act.kind {
	case akBail:
		t.BailErr(act.code, act.reason)
		return false
	case akProbe:
		t.BailErr(ErrMisuse, "Probe cannot be nested")
		return false
	case akEnter:
		if !isContainer {
			if !act.lenient {
				t.BailErr(ErrUnsupported, "expected an object or array")
				return false
			}
			act.kind = akPass
			return t.apply(f, act, kind)
		}
		nf := frame{ph: phKey, idx: -1, flat: act.flat, lazy: act.lazy}
		if kind == KindArray {
			nf.kind = fkArr
			nf.ph = phValue
		}
		nf.hook = act.via
		if nf.hook == nil {
			nf.hook = f.hook // 子 hook 自己 Enter 的层仍归它
		}
		t.pushFrame(nf)
		if !act.flat {
			name := ""
			var raw []byte
			if f.kind == fkObj {
				name = act.key
				if name == "" {
					name = t.Last()
					raw = t.kvRaw // push 会拷贝进槽位
				}
			} else {
				raw = t.elemWs
			}
			t.w.push(name, raw, kind == KindArray)
		}
		return true
	case akPass, akObserve:
		if act.inner && kind != KindString {
			t.BailErr(ErrUnsupported, "Inner requires a string value")
			return false
		}
		level := act.level
		if level < 0 {
			level = t.w.Level()
		}
		var ok bool
		if f.kind == fkObj {
			if act.key == "" {
				ok = t.w.KeyRawAt(level, t.kvRaw)
			} else {
				ok = t.w.KeyAt(level, act.key)
			}
		} else {
			ok = t.w.ElemRawAt(level, t.elemWs)
		}
		if !ok {
			t.BailErr(ErrMisuse, "target output level already has an open child level")
			return false
		}
		if len(act.prefix) > 0 {
			t.w.Raw(act.prefix)
		}
		target := rtOut
		if act.kind == akObserve {
			target = rtObserve
		}
		t.beginRegion(target, act)
		return true
	case akSkip:
		t.beginRegion(rtSkip, act)
		return true
	case akCapture:
		t.beginRegion(rtCapture, act)
		return true
	case akDefer:
		if f.kind != fkObj {
			t.BailErr(ErrMisuse, "Defer applies only to object keys")
			return false
		}
		t.regKey = t.Last()
		t.beginRegion(rtDefer, act)
		return true
	case akPrefix:
		if kind != KindString {
			t.BailErr(ErrUnsupported, "Prefix requires a string value")
			return false
		}
		act.inner = true
		t.beginRegion(rtPrefix, act)
		return true
	}
	t.BailErr(ErrMisuse, "unknown action")
	return false
}

// regPush 区域内进入一层容器：记下种类，文法阶段切到"期待 key / 期待值"。
func (t *Transformer) regPush(isArr bool) {
	t.regN++
	if t.regN <= 64 {
		bit := uint64(1) << uint(t.regN-1)
		if isArr {
			t.regKinds |= bit
		} else {
			t.regKinds &^= bit
		}
	} else {
		t.regDeep = append(t.regDeep, isArr)
	}
	if isArr {
		t.regPh = rAValue0
	} else {
		t.regPh = rKey0
	}
}

// regPop 区域内闭合一层容器，返回回到父层后的阶段（父层是对象则期待 , 或 }，是数组则期待 , 或 ]）。
func (t *Transformer) regPop() regPhase {
	t.regN--
	n := t.regN
	if n == 0 {
		return rTop
	}
	var parentArr bool
	if n <= 64 {
		parentArr = t.regKinds>>uint(n-1)&1 == 1
	} else {
		t.regDeep = t.regDeep[:n-64]
		parentArr = t.regDeep[n-65]
	}
	if parentArr {
		return rAComma
	}
	return rOComma
}

func (t *Transformer) beginRegion(target regionTarget, act Action) {
	t.regN = 0
	t.regDeep = t.regDeep[:0]
	t.regPh = rTop
	t.regOpen = true
	t.regT = target
	t.regDepth = t.depth
	t.regInner = act.inner
	t.regSuf = act.suffix
	t.regCap = act.cap
	t.capBuf = t.capBuf[:0]
}

// emitRegion 处理区域内的一段原始字节。
func (t *Transformer) emitRegion(b []byte) {
	switch t.regT {
	case rtOut:
		t.w.Raw(b)
	case rtSkip:
	case rtCapture, rtDefer:
		t.capAppend(b)
	case rtObserve:
		t.w.Raw(b)
		t.capAppend(b)
	case rtPrefix:
		room := t.regCap - len(t.capBuf)
		if room >= len(b) {
			if !t.checkBudget(len(b)) {
				return
			}
			t.capBuf = append(t.capBuf, b...)
			return
		}
		t.capBuf = append(t.capBuf, b[:room]...)
		rest := b[room:]
		t.runPrefix(false)
		if t.dead {
			return
		}
		// 窗口之后的字节按新目标处理
		t.emitRegion(rest)
	}
}

func (t *Transformer) capAppend(b []byte) {
	if t.regCap > 0 && len(t.capBuf)+len(b) > t.regCap {
		t.limitAt = t.regCap - len(t.capBuf)
		t.BailErr(ErrLimit, "capture limit exceeded")
		return
	}
	if t.budget > 0 && t.Buffered()+len(b) > t.budget {
		t.limitAt = t.budget - t.Buffered()
		if t.limitAt < 0 {
			t.limitAt = 0
		}
		t.BailErr(ErrLimit, "buffer budget exceeded")
		return
	}
	t.capBuf = append(t.capBuf, b...)
}

// runPrefix 把前缀窗口交给协议，并按其返回切换区域目标。
func (t *Transformer) runPrefix(complete bool) {
	act, resume := t.cur().OnPrefix(t, t.capBuf, complete)
	if t.dead {
		return
	}
	if resume < 0 || resume > len(t.capBuf) {
		t.BailErr(ErrMisuse, "OnPrefix returned an invalid resume offset")
		return
	}
	switch act.kind {
	case akPass:
		if len(act.prefix) > 0 {
			t.w.Raw(act.prefix)
		}
		t.w.Raw(t.capBuf[resume:])
		t.regT = rtOut
		t.regSuf = act.suffix
	case akSkip:
		t.regT = rtSkip
		t.regSuf = nil
	case akBail:
		t.BailErr(act.code, act.reason)
		return
	default:
		t.BailErr(ErrMisuse, "OnPrefix must return Pass, Skip or Bail")
		return
	}
	t.capBuf = t.capBuf[:0]
}

// endRegion 区域结束：交付缓冲、写后缀。
func (t *Transformer) endRegion() {
	if t.dead {
		return // 缓冲超限等 Bail 已发生，不再把残缺数据交给协议
	}
	switch t.regT {
	case rtOut:
		if len(t.regSuf) > 0 {
			t.w.Raw(t.regSuf)
		}
	case rtObserve:
		if len(t.regSuf) > 0 {
			t.w.Raw(t.regSuf)
		}
		t.cur().OnValue(t, t.capBuf)
	case rtCapture:
		t.cur().OnValue(t, t.capBuf)
	case rtDefer:
		f := t.top()
		raw := make([]byte, len(t.capBuf))
		copy(raw, t.capBuf)
		f.deferred = append(f.deferred, DeferredKV{Key: t.regKey, KeyRaw: append([]byte(nil), t.kvRaw...), Raw: raw})
		t.deferredBytes += len(t.kvRaw) + len(raw)
	case rtPrefix:
		t.runPrefix(true)
		if t.dead {
			return
		}
		if t.regT == rtOut && len(t.regSuf) > 0 {
			t.w.Raw(t.regSuf)
		}
	}
	t.regOpen = false
	t.regT = rtNone
	t.regSuf = nil
	t.capBuf = t.capBuf[:0]
}

// onKeyDone key 闭合：派发 OnKey。
func (t *Transformer) onKeyDone() {
	var key string
	if t.keyEsc { // 带转义的 key：按 JSON 解码后再派发（原文仍由 kvRaw 保留）
		k, ok := decodeKey(t.keyBuf)
		if !ok {
			t.BailErr(ErrSyntax, "invalid escape in key")
			return
		}
		key = k
	} else if k, ok := t.keys[string(t.keyBuf)]; ok { // map 以 []byte 查找不分配
		key = k
	} else {
		key = string(t.keyBuf)
		if t.keys == nil {
			t.keys = make(map[string]string, 32)
		}
		if len(t.keys) < internKeysMax {
			t.keys[key] = key
		}
	}
	f := t.top()
	t.path = append(t.path, seg{k: key, i: -1})
	if t.DupKeyBail || t.dup != DupKeysPass {
		dup := false
		for _, s := range f.seen {
			if s == key {
				dup = true
				break
			}
		}
		if dup && (t.DupKeyBail || t.dup == DupKeysBail) {
			t.BailErr(ErrDuplicateKey, "duplicate key "+strconv.Quote(key))
			return
		}
		if !dup {
			f.seen = append(f.seen, key)
		} else { // DupKeysFirst：后面的同名 key 不派发，直接丢弃
			t.pend = Skip()
			t.pendSet = true
			return
		}
	}
	t.pend = t.cur().OnKey(t)
	t.pendSet = true
	if t.pend.kind == akBail {
		t.BailErr(t.pend.code, t.pend.reason)
	}
}

// afterValue 一个值（标量 / 字符串 / 容器）在派发帧里结束。
func (t *Transformer) afterValue() {
	f := t.top()
	if f == nil || t.dead {
		return
	}
	f.ph = phComma
	f.n++
	if len(t.path) > 0 {
		t.path = t.path[:len(t.path)-1]
	}
	if t.wantRelease && t.releaseAt == len(t.frames)-1 {
		t.wantRelease = false
		t.doRelease()
	}
}

// closeContainer 派发帧闭合。
func (t *Transformer) closeContainer() {
	f := t.top()
	closeWs := append([]byte(nil), t.wsRaw...)
	t.wsRaw = t.wsRaw[:0]
	t.hookAt(len(t.frames) - 2).OnLeave(t) // 闭合回到发起 Enter 的一方
	if t.dead {
		return
	}
	if t.wantRelease && t.releaseAt == len(t.frames)-1 {
		t.wantRelease = false
		t.doRelease()
		if t.dead {
			return
		}
	}
	if len(f.deferred) > 0 {
		// 协议既没回放也没显式丢弃：这是协议逻辑漏洞，静默吞掉会产出语义不同的请求。
		t.BailErr(ErrLeftoverDefer, "deferred items not released before the container closed")
		return
	}
	flat, lazy := f.flat, f.lazy
	t.frames = t.frames[:len(t.frames)-1]
	t.depth--
	if t.depth == 0 {
		t.rootDone = true
		t.rootCloseWs = closeWs
		return // 根的输出层在 Finish 里闭合（Tail 之后）
	}
	if !flat {
		if !lazy {
			t.w.Open() // 输入里存在的容器，输出里也要存在，哪怕是空的
		}
		t.w.pop(closeWs)
	}
	t.afterValue()
}

// doRelease 回放当前派发帧里的 Defer 项：每一项重新经过 OnKey，按此刻的协议状态处理。
func (t *Transformer) doRelease() {
	f := t.top()
	if f == nil || len(f.deferred) == 0 {
		return
	}
	kvs := f.deferred
	t.forgetDeferred(f)
	f.deferred = nil
	for _, kv := range kvs {
		if t.dead {
			return
		}
		t.replayKV(kv)
	}
}

func (t *Transformer) replayKV(kv DeferredKV) {
	f := t.top()
	t.kvRaw = append(t.kvRaw[:0], kv.KeyRaw...)
	t.wsRaw = t.wsRaw[:0]
	t.path = append(t.path, seg{k: kv.Key, i: -1})
	t.pend = t.cur().OnKey(t)
	t.pendSet = true
	if t.pend.kind == akBail {
		t.BailErr(t.pend.code, t.pend.reason)
		return
	}
	f.ph = phValue
	t.replaying++
	t.scan(kv.Raw)
	if t.st == sInScalar {
		t.scan([]byte{' '}) // 补一个分隔符收尾标量
	}
	t.replaying--
	t.wsRaw = t.wsRaw[:0] // 上面的补位空格不属于原文
}

// scanStrUTF8 开启 UTF-8 校验时的字符串体扫描：ASCII 段走 SWAR，≥ 0x80 的字节逐个过 RFC 3629 状态机
// （序列可以跨块，状态留在 t.u8）。返回下一个需要外层处理的 ASCII 字节位置（引号 / 反斜杠 / 控制字符）或 len(p)；
// 序列非法时 Bail。
func (t *Transformer) scanStrUTF8(p []byte, i int) int {
	for i < len(p) {
		c := p[i]
		if c >= 0x80 {
			if t.u8.need == 0 {
				// 序列开头且整个序列都在本块：查表一次验完，不进逐字节状态机
				f := utf8First[c]
				sz := int(f & 7)
				if sz == 0 {
					t.BailErr(ErrSyntax, "invalid UTF-8 sequence")
					return i
				}
				if i+sz <= len(p) {
					r := utf8Accept[f>>3]
					ok := p[i+1] >= r.lo && p[i+1] <= r.hi
					if sz >= 3 {
						ok = ok && p[i+2] >= 0x80 && p[i+2] <= 0xBF
					}
					if sz == 4 {
						ok = ok && p[i+3] >= 0x80 && p[i+3] <= 0xBF
					}
					if !ok {
						t.BailErr(ErrSyntax, "invalid UTF-8 sequence")
						return i
					}
					i += sz
					continue
				}
			}
			if !t.u8.step(c) { // 跨块的序列：逐字节
				t.BailErr(ErrSyntax, "invalid UTF-8 sequence")
				return i
			}
			i++
			continue
		}
		if t.u8.need > 0 {
			t.BailErr(ErrSyntax, "invalid UTF-8 sequence")
			return i
		}
		i = scanStringBodyUTF8(p, i)
		if i == len(p) || p[i] < 0x80 {
			return i
		}
	}
	return i
}

// decodeKey 解码带转义的 key。独立成函数是为了不让 onKeyDone 里的 key 变量因取地址而逃逸到堆上。
func decodeKey(raw []byte) (string, bool) {
	q := make([]byte, 0, len(raw)+2)
	q = append(q, '"')
	q = append(q, raw...)
	q = append(q, '"')
	var s string
	if err := json.Unmarshal(q, &s); err != nil {
		return "", false
	}
	return s, true
}
