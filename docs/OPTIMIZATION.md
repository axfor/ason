# ason 优化方案（v0.2 → v0.5）

## 零、深度洞察：数据告诉我们的事

下面每一条都来自这一轮的实测，不是推测；优化方案的取舍以此为据。

**1. 引擎已经不是热点，宿主边界才是。**
1MB 请求在 Envoy 里每请求约 14ms CPU，其中引擎扫描本身不到 0.5ms（字符串 / base64 4.8 GB/s）；
其余是宿主往 wasm 拷贝分块（`proxy_on_memory_allocate`）、再拷回、以及 Go 运行时的 GC。
结论：继续压扫描器的收益在网关里几乎看不见；有收益的是减少边界拷贝（零拷贝 key、直写 sink）和减少垃圾（更少的中间切片）。
结构密集体 0.69 GB/s 的短板是真实的，但只影响"tools 定义占主体"的请求。一度以为要靠 SWAR 化才能动它，后来证伪：
真实请求体的结构字符密度是 40%（平均每 2.5 字节一个），一次 8 字节必然命中多个，掩码提取的开销吃掉全部收益。
真正见效的是消除每字节的固定开销（见 M3 第 1 项）。

**2. 内存的上限不由引擎单独决定。**
`Out()` 交出所有权后，一条流在提交点之后的存活只有几十 KB；800 并发时网关多出的内存一半是 Envoy 每连接开销，
另一半曾经是 Go 运行时 GC 停摆造成的堆膨胀（与引擎无关，靠 wrapper 看门狗兜住）。
结论：引擎要给的是**可证明的上界**——单项 cap 不构成保证（一个协议有十几处 cap），必须有总预算（`SetBudget`）与可观测的 `Buffered()`，
再配合宿主侧的看门狗，链路上每一层的内存都有上界，"2000 并发内存持平"才是可复现的结论而不是一次实验。

**3. 保真类缺陷聚集在"协议不碰的边界"。**
黄金差分与模糊测试抓到的问题——`1e0` 格式化、逗号前空白、根前空白、根后换行、重复 key、`tools:[null]`、`parameters:5`——
没有一个在协议逻辑里，全在字面量 / 空白 / 重复 / 类型这些边界语义上。
结论：边界语义必须由引擎统一持有，协议不能有机会写错：`Probe` 报出 null/bool/number、严格校验、空白保真都已进引擎，
下一步把重复 key 策略、根形状、总预算也收进引擎（M2），协议只剩"我关心的 key 怎么写"。

**4. 顺序无关是通过"有界回放"换来的，回落是这个设计的一部分而不是失败。**
`Defer + Release` 让 content 先于 role 到达也能流式；上限之外只能回落。实测 Claude 套件 1230 条里已知回落 5 条、
Qwen 兼容 189 条（几乎都是 developer role 这类"目标形状要求 struct 往返"的形态）。
结论：回落率是运行指标而不是缺陷，窗口大小是部署策略而不是常量——所以 `CommitBytes` / 预算要按转换器可配（M2），
指标要按 `Error.Code` 分类（M1）。

**5. 写协议的成本在"派发"，不在"转换"。**
Claude 协议约一半代码是 `switch t.Depth()` 的路径分支；曾经的两类静默错误——漏转发某个回调、深度判断错——
一类已由 `Via` 消灭（引擎路由子树），另一类要由 Router 消灭（引擎路由路径）。
结论：**where 声明式、what 命令式**：路径匹配进引擎层，动作与写法仍手写，可读性与可验证性不倒退。

**6. 在同类里的位置。**
sjson / gjson：整体缓冲的原地改写与取值，保真但不流式；simdjson / sonic：极快的物化解析，不改写、不流式；
SAX / 事件式解析器：流式但只解析，没有写出器与保真；jq `--stream`、Envoy ext_proc：事件或整体，都不能"边到边改还能回落"。
ason 独有的是三件事的组合：流式改写 + 字节保真 + **提交点前可回落的事务式前缀**。最后一项是它能进代理数据面的原因，
也是文档里应当作为核心抽象来讲的东西。

**7. 风险。**
引擎核心 2000 行，一切新能力都应作为上层（Router、Stream、difftest、sse）而不是往核心里塞；
公开 API 尚未定型（错误类型、根形状、预算），越早定越好；文档全中文是对外采用的硬门槛。

---

目标：在不动"单遍流式、有界缓冲、字节保真、严格校验、提交点回落"这五条底座的前提下，
把**写协议的生产力**、**边界能力**、**性能上限**和**生态工具**做到同类库里最好。
每一项都给出 API 草图、语义、兼容性与验收标准；里程碑按依赖顺序排列，公开 API 的改动放在最前面。

不变的设计约束（任何优化都不能违反）：

1. 不建 DOM，不物化文档；协议只在明确要求的地方缓冲，且都有上限。
2. 没动的字节一个不改；原位改写与 sjson 逐字节一致。
3. 不对字段顺序做假设；需要后面的字段才能决定的形状用有界 `Defer` 回放。
4. 判定不支持有出路：提交点之前调用方能干净回落。
5. 零第三方依赖，`wasip1` 可构建。

---

## M1 · v0.2 —— 写协议的生产力

### 1.1 结构化错误与英文文案

现状：`Bail(reason string)` 只有一个中文字符串，没有位置；调用方（Higress guard、差分 harness）靠子串匹配区分回落原因。

设计：

```go
type Code uint8

const (
	ErrNone Code = iota
	ErrSyntax        // 不符合 JSON 文法（字面量、数字、转义、控制字符、结构）
	ErrRoot          // 根不是对象（或 Root 选项不允许的形状）
	ErrTrailing      // 根之后有非空白内容
	ErrDuplicateKey  // 派发帧里重复 key（DupKeys 策略为 Bail 时）
	ErrLimit         // 某个 Capture / Defer / Prefix 超过上限，或总预算超限
	ErrLeftoverDefer // 容器闭合时仍有未回放的 Defer 项
	ErrUnsupported   // 协议调用 Bail：它无法表达这个形状
)

type Error struct {
	Code   Code
	Msg    string // 英文；协议 Bail 的原文原样保留
	Offset int64  // 输入字节偏移（发生位置）
	Path   string // 发生时的路径（"messages[2].content"）
}

func (e *Error) Error() string

func (t *Transformer) Err() *Error              // nil 表示正常
func (t *Transformer) Unsupported() (bool, string) // 保留：等价于 Err() != nil, Err().Msg
func Bail(reason string) Action                  // 保留：Code = ErrUnsupported
func BailCode(code Code, reason string) Action   // 新增
func (t *Transformer) BailErr(code Code, reason string)
```

- 引擎内部所有 `Bail("中文")` 改为带 Code 的英文文案；`Offset` 取扫描器当前偏移，`Path` 取 `PathString()`。
- 兼容：`Unsupported()`、`Bail(string)` 不变；Higress guard 从子串匹配改为按 `Code` 归类（`ErrDuplicateKey` 等），
  差分 harness 的"允许的回落原因"同样改为 Code。协议自己的 Bail 文案（Higress 里是中文）不受影响。
- 验收：引擎测试里所有断言改为断言 Code；`examples/llm` 黄金差分零变化。

### 1.2 Router：声明式路径匹配

现状：协议是 `switch t.Depth()` + `t.Last()` 的手写分支，Claude 协议约一半代码是派发逻辑；漏写一个深度就是静默错误。

设计：建立在回调之上的一层，引擎不变。

```go
type Router struct{ /* 模式 trie */ }

func NewRouter() *Router

// 模式语法：段用 "/" 分隔；字面 key；"*" 任意一个 key 或下标；"#" 任意下标；"**" 任意深度的后缀。
// key 含 "/" 或 "*" 时用 Path() 构造器。
func (r *Router) Key(pattern string, h func(t *Transformer) Action) *Router
func (r *Router) Elem(pattern string, h func(t *Transformer) Action) *Router
func (r *Router) Start(pattern string, h func(t *Transformer, kind ValueKind) Action) *Router // Probe 之后
func (r *Router) Value(pattern string, h func(t *Transformer, raw []byte)) *Router          // Capture / Observe 到齐
func (r *Router) Prefix(pattern string, h func(t *Transformer, raw []byte, complete bool) (Action, int)) *Router
func (r *Router) Leave(pattern string, h func(t *Transformer)) *Router
func (r *Router) Default(a Action) *Router // 没有匹配时的动作（默认 Pass）

// 常用形态的一行写法
func (r *Router) Rename(pattern, name string) *Router      // Pass().As(name)
func (r *Router) Skip(pattern string) *Router
func (r *Router) Capture(pattern string, cap int, h func(t *Transformer, raw []byte)) *Router

func Path(segs ...any) string // Path("messages", Any, "content")；Any / AnyIndex / Rest 为哨兵

// 挂到协议上：Router 实现 Protocol；需要 Tail 等额外逻辑时嵌入它并覆盖
func (r *Router) Protocol() Protocol
```

实现要点：

- 模式编译成 trie；每个派发帧维护"当前可达的 trie 节点集合"（进入容器时推进，闭合时弹出），
  `OnKey` 只在集合里按 key 查一次，`**` 节点常驻。匹配代价与模式数量无关，O(1) 摊销。
- 多个模式同时命中时取最具体的（字面 > `*` > `**`），同级冲突在注册时报错。
- 与 `Via` 正交：子 hook 内部也可以用自己的 Router。

验收：用 Router 重写 `examples/chatconv`，行数减半、黄金/随机差分全过；文档给出"手写分支 vs Router"对照。

### 1.3 Trace：可观测的派发

```go
type Event struct {
	Kind   EventKind // Dispatch / Start / Region / Capture / Defer / Replay / Bail / Commit / Finish
	Offset int64
	Path   string
	Action string // "Pass.As(role)" 这样的可读形式
	Bytes  int    // 区域 / 捕获涉及的字节数
	Note   string
}

// 为 nil 时零开销（一次 nil 判断）
func (t *Transformer) SetTrace(fn func(Event))
```

- `Action` 增加 `String()`。
- 命令行 `ason-trace -example chatconv < body.json` 打印表格，协议作者第一时间能看到"每个 key 落到了哪个动作"。
- 验收：Trace 关闭时基准无差异；示例文档附一段 trace 输出。

### 1.4 英文 API 文档

导出标识符的注释全部英文（中文说明移到 docs/），pkg.go.dev 可读；`docs/DESIGN.md` 增加英文版。

---

## 进度（2026-09-06）

已完成并合入 main：

- 内存：key 驻留表、派发帧 / 写出帧槽位复用、每块只分配一次输出缓冲、协议热路径常量字节串提升为包级变量——
  Claude 转换 2000 个 tools 的 1MB 请求从 53102 次分配 / 2.46MB 降到 161 次 / 0.75MB；`SetCommitBytes`、`SetBudget`、`Buffered()`（2.2 节）。
- 性能：key 成段扫描、标量成段校验、区域内部紧凑循环——透传密集体 445 → 469 MB/s，Claude 密集 105 → 143 MB/s，长字符串 2.5–2.6 GB/s 不变。
  再往上要改短字符串的处理方式，收益递减，且引擎已不是网关里的瓶颈（洞察 1），暂停。
- 验证：所有改动都过了 7 套 7456 条黄金差分、场景差分与模糊。

第二批（正确性优先，按"正确性 > 流式性能 > 内存"重排后的第一步）：

- **区域文法**：新模糊目标发现 Pass / Skip / Capture 区域里只数括号深度、不校验文法——`{"a":{]}`、`{"a":{"x" 1}}`
  之类会被原样放行。现在区域内部与派发帧一样按文法走（阶段编码容器种类，查表转移，括号处动位栈），
  `FuzzPassthrough` 双向断言拒绝面与 `encoding/json` 一致（含 `1000e1000` 这种 Valid 接受、Unmarshal 溢出的边角）。
  代价：透传密集体 469 → 416 MB/s，贴近真实的 1MB 聊天体 1.71 → 1.65 GB/s，长字符串不变。
- **2.1 根形状**：`SetRoot(RootObject | RootArray | RootAny)`，数组根按下标经 `OnElem` 派发；多文档流（`Stream`）未做。
- **2.3 重复 key 策略**：`SetDupKeys(DupKeysPass | DupKeysBail | DupKeysFirst)`，`DupKeyBail` 字段保留；Defer 回放不会把自己算作重复。
- **2.4 UTF-8 校验**：`SetValidateUTF8(true)`，RFC 3629 全覆盖（过长、代理对、> U+10FFFF、孤立 / 缺失续字节、跨块序列），
  随机字节流与 `utf8.Valid` 逐一对照。整序列查表快路径：中文占一半的 1MB 体从 480 提到 955 MB/s。
- 测试：`strict_test.go`（UTF-8 / 重复 key / 根形状 / 区域文法差分，2 万条随机结构垃圾对照 `json.Valid`）、
  `FuzzStrictModes`；黄金差分、场景差分零变化。
- **1.1 结构化错误**：`Err() *Error{Code, Msg, Offset, Path}`，引擎内部全部改为带分类的英文文案；`Unsupported()` 文案变为
  `Err().Error()`（原因 + 偏移 + 路径），`Bail(string)` 保留为 `ErrUnsupported`，新增 `BailCode`。偏移在 scan 出口一次定位
  （`defer`，热路径零开销；Defer 回放里的错误取外层位置），与分块方式无关。引擎与示例的断言全部改为按 `Code`。
  比方案多了 `ErrIncomplete`（截断）与 `ErrMisuse`（用错动作），网关据此把"客户端断了"与"协议写错了"分开。
- **3.2 输出 sink**：`SetSink(func([]byte))`，提交点之后的输出在每次 Write 末尾交给回调，缓冲随后复用；提交前攒下的大缓冲
  在第一次交付后丢弃，之后每条流只持有一块块大小的缓冲。1MB / 16KB 分块：77 次 1.2MB → 17 次 168KB，2.7 → 4.2 GB/s。
  这是洞察 1 里"减少垃圾"的主项：网关侧每请求的输出垃圾从与输入等量降到常数。Higress 的 guard 已接入：转换器的 sink 追加到
  State 里跨块复用的缓冲，宿主同步拷贝返回的字节，两边都不再按块分配。
- **全部英文**：引擎、示例、chatconv、examples/llm 快照以及 Higress / wasm-go 里我们改动的部分，注释、错误文案、日志、测试名
  与断言文案全部改为英文（对外采用的硬门槛，洞察 7）；测试里用来覆盖多字节 UTF-8 的中文改为希腊文 / 西里尔文 / 欧元符号。
- **派发路径**：profile 显示派发密集体四分之一的时间在拷贝 136 字节的 `Action`（协议返回后又按值经过 valueStart / apply /
  beginRegion）和 key 驻留 map 的探测。现在 `apply` / `beginRegion` 取 `*Action`，字段收紧到 112 字节；驻留 map（上限 4096 项，
  多 key 文档下每条流几百 KB）换成 256 槽直接映射缓存（FNV-1a，固定 4KB）。5000 个不同 key：793KB → 148KB / 流，57 → 88 MB/s；
  Claude 密集 137 → 153 MB/s。剩下的拷贝是回调按值返回 `Action` 本身（公开 API），再往下要把 `Wrap` 改成 string 参数才能压到 64 字节以内，
  与 Higress hooks 一起改。

## M2 · v0.3 —— 边界能力

### 2.1 根形状与多文档流

```go
type RootKind uint8
const (
	RootObject RootKind = iota // 默认，兼容现状
	RootArray
	RootAny                    // 对象或数组
)
func (t *Transformer) SetRoot(RootKind)

// 多文档（NDJSON / 连续 JSON）：逐文档实例化协议，文档之间的空白原样保留
type Stream struct{ /* ... */ }
func NewStream(newProtocol func() Protocol, opts ...StreamOption) *Stream
func (s *Stream) Write(p []byte)
func (s *Stream) Out() []byte
func (s *Stream) Finish() []byte
func (s *Stream) Err() *Error
```

- 根数组：路径深度 1 是下标，`OnElem` 派发；其余语义不变。
- Stream：扫描到根闭合即完成一个文档，复位后开始下一个；某文档判定不支持则整个流停止（简单、可预期）。

### 2.2 每个转换器自己的窗口与总预算

```go
func (t *Transformer) SetCommitBytes(n int) // 0 = 包默认 64KB
func (t *Transformer) SetBudget(n int)      // 所有 Capture / Defer / Prefix 缓冲与提交前输出之和的上限；超限 → ErrLimit
func (t *Transformer) Buffered() int        // 当前持有的字节数（观测）
```

- 预算是"总量"约束，协议里各处的 cap 仍是"单项"约束；两者取严。
- Higress 的 model-router 可把窗口设为 16KB，减少提交前的持有。

### 2.3 重复 key 策略

```go
type DupKeys uint8
const (
	DupKeysPass DupKeys = iota // 默认：不检查（透传语义）
	DupKeysBail                // 现在的 DupKeyBail = true
	DupKeysFirst               // 取第一个，后面的同名 key 自动 Skip（gjson 语义）
)
func (t *Transformer) SetDupKeys(DupKeys)
```

`DupKeyBail` 字段保留为 `DupKeysBail` 的别名。KeyProbe / ai-statistics 里各自实现的"取第一个"收进引擎。

### 2.4 可选 UTF-8 校验

`SetValidateUTF8(true)`：字符串快路径遇到 ≥ 0x80 的字节才进入 UTF-8 DFA（Höhrmann 状态机，跨块保持状态），
非法序列 → `ErrSyntax`。默认关闭——encoding/json 本身不拒绝非法 UTF-8。

---

## M3 · v0.4 —— 性能上限

现状：长字符串 / base64 约 4.8 GB/s（SWAR），嵌套内容块约 0.84 GB/s，结构密集体约 0.69 GB/s。后者的成本不在某一处判断，
而在每字节都要付的固定开销：循环边界检查、取字节、状态分派。做之前先 profile，按收益排序：

1. **已完成 · 输出 sink**：`SetSink(func([]byte))` 把提交点之后的输出直接交给调用方，不再经 `Out()` 交接切片；
   提交点之前仍要缓冲（那是回落用的退路）。后来又往前走了一步：提交点之后**透传根本不复制**——够大的透传运行
   以输入视图的形式交给 sink，只有协议生成的字节才进缓冲，而缓冲按提交窗口而不是按分块定尺（`2a0b4ca`）。
   整块交付每请求分配 1,021,877 → 71,728 字节，长串 / base64 4.0 → 4.8 GB/s，分块档不变。
2. **已放弃 · 区域内的 SWAR 结构扫描**：原计划 8 字节一组找出 `" { } [ ] \`，预期结构密集体 1.5–2×。
   语料统计否决了它：`tools_1MB` 的结构字符占 40.2%（平均每 2.5 字节一个）、字符串平均长 5.6 字节、空白 0%，
   一次 8 字节读里必然有多个结构字符，逐位提取与分派的开销超过它省下的比较。SWAR 在长字符串里有效（`scanStringBody`
   就是），在结构密集处无效——因为那里根本没有"长段同类字节"可跳。
3. **已放弃 · 单字节整数快路径**：区域循环里"一位数字紧跟收尾字节"直接判定完整，跳过标量状态机与字面量 DFA。
   实现出来、正确性也站得住（2400 条随机数字文档在开关两个构建下逐字节一致、逐切点跨块测试、两个 fuzz 各 600–770 万次执行），
   微基准上单字节整数 +134%、人工构造的结构密集语料 +56%——但真实语料一分钱没赚：`tools_1MB` −0.4~0.0%、`parts_1MB` +0.0~1.3%。
   原因就在上面那份统计里：**真实请求体的标量占比是 0.0%**，LLM 请求体是字符串与嵌套结构，没有成片的小整数。
   代价是热路径上多一个四条件判断和一张表，故回退。留下的资产是那组数字语义测试与两个数字微基准（`BenchmarkNumbers`）。
4. **已放弃 · 用子切片引导边界检查消除**：把 `c = p[i]` 写成 `q := p[i:]; c := q[0]`，指望编译器凭"窗口非空"省掉检查。
   反而更糟：`engine.go` 的 BCE 点从 41 涨到 43，密集 tools 整块 690 → 653 MB/s、嵌套内容块 831 → 806、
   人工结构密集语料 254 → 242（各跌 3–5.4%）。切片头的构造本身有成本，而检查一个都没少。
   另一种写法——把循环条件改成循环体开头的显式守卫（`for {}` 加 `if i >= len(p) || t.dead { break }`）——同样不成立：
   它也没消除任何检查（`c := p[i]` 仍报 `Found IsInBounds`），profile 里守卫自己成了最热的两行（830ms + 690ms，
   而原来的两个循环头是 650ms + 740ms），只是把成本从循环头搬进了循环体。专项三档一度显示 +1~3.3%，
   但完整四形态矩阵（同参数、同批次）是密集 tools −4.1~−5.0%、嵌套内容块 −2.5~−4.7%，以矩阵为准，故回退。

   **这条路整体作废**：`scan` 里的 `i` 由字符串段、转义、整块区域等多条路径更新，Go 的边界检查消除在这种形态下
   证明不出范围，换写法只会挪动成本。要动它得先让 `i` 的更新路径收敛，那是另一次结构性改造，不在本轮。

5. **已放弃 · 跨流复用 Transformer（`Reset`）**：让调用方像复用缓冲那样复用 transformer，省掉每请求新建结构体与内部切片首次增长。
   设计做完了（80 个字段逐条归类：保留容量 11 条、必须置 nil 的 4 条别名或私有副本、清零 37 个状态位、保留 9 项配置、
   Writer 15 字段分三类），但按落地前提先量收益，量完否决：**池化驱动下每条流总共只有 7 次分配 / 1,256 字节**
   （逐次分配全采样，300 条流：`NewTransformer` 301 个对象 / 301KB 即每流 1 次 1KB，其余 6 次是 frames / path / keyBuf
   等切片首次增长与 key 处理）。400 条在飞流合计约 0.5MB，远低于 4MB 的 arena 台阶——省下它改变不了任何台阶，
   而代价是一个公开 API 加 80 个字段的逐字段正确性责任，跨流残留在压测里表现为偶发输出漂移，最难查。
   设计记录留在 ACGCluster 的 `designs/ason-Reset-设计.md`，若将来有"单 worker 上万并发流"这类形态再取用。

6. **已放弃（内联路线）· 让短串不付函数调用**：真实请求体字符串平均 5.6 字节，`scanStringBody` 的 8 字节 SWAR 主循环
   对 68% 的串根本进不去，却要付一次真实函数调用（实测 `cannot inline scanStringBody: cost 106 exceeds budget 80`）。
   试过把它拆成"可内联的短串头 + 独立 SWAR 尾"：第一版头部反而涨到 **132**（两个循环加位判断），
   压到极简单循环后仍是 **104**——而 SWAR 版 106、UTF8 版 114，三者都落在 104–114。
   结论是**编译器约束而非写法问题**：Go 的内联成本模型里，一个循环加每次迭代三次比较就吃掉大部分预算，
   "逐字节判三种终止符"的函数天生超预算。这条路与边界检查那条同理，不可达。
   那个"不依赖内联"的变体也试了，结果更糟，同样否决：让调用点先用一个"最多看 8 字节"的循环自己扫，
   扫不完再交给 SWAR 版——**短串 len=3 −35.6%、len=6 −30.5%、len=12 −43.4%、len=40 −23.2%，
   真实语料密集 tools −16.2%、嵌套内容块 −16.5~17.5%**（base64 与长串持平）。
   原因与直觉相反：原路径对一个 12 字节的串只调一次，改后变成每 8 字节调一次，**调用次数被放大而不是减少**，
   长串还要在两层循环间反复进出；base64 与长串不变，是因为它们的串长以万计，头部可忽略、SWAR 一进去就扫到底。
   **"消掉短串的函数调用开销"这个方向由此完全封口**：内联不可达（编译器预算），调用点自扫更糟（调用次数反增）。

7. **已放弃 · 热字段聚到同一 cache line**：`Transformer` 896 字节，扫描一个字节要触 `st/esc`、`depth`、
   `regOpen/regT/regDepth`、`regPh/regN/regKinds`、`dead/suspendReq`——实测横跨 **7 条 64 字节行**。
   把 30 个热字段整块提到结构体前部后，大小降到 864 字节、热字段只横跨 **3 条行**，语义零风险（仅重排）。
   但吞吐**两次测量方向相反**：第一次密集 tools +1.8~3.9%、嵌套内容块 +2.8%，同参数复测却是 tools −4.8%、
   parts −3.8%、人工结构密集 −5.0%。说明真实效应落在本机噪声带（±4%）以内，"7 → 3 条行"没有转化为可测变化；
   而代价是把 30 个字段搬离原有语义分组，可读性明显变差。按"两次相反即不足以支撑保留"回退。
   若将来要重提，得先有更低噪声的测量手段（固定频率、绑核、`perf` 级计数器），而不是再跑一遍同样的基准。

8. **已完成 · arm64 向量化字符串体扫描**：Go 不为任何架构自动向量化，所以用汇编写了 NEON 版（16 字节一步，
   对 `"` / `\` 用 `VCMEQ`、控制字符用 `VUSHR $5` 后与零比较；命中定位借标准库 `indexbyte_arm64.s` 的手法，
   但魔数换成 `0x08040201`，使每字节间隔 8 bit、位索引右移 3 位即字节索引——`0x40100401` 的间隔是 10，要除法）。
   Go 汇编器在 arm64 上不认 `VCMHI` / `VUMAXV`，可用集经查是 `VCMEQ / VCMTST / VAND / VORR / VUSHR / VADDV / VMOV / VRBIT`。

   **交接点由按串长的实测定出，不是猜的**：向量在 128 字节以下是亏的（8 字节 −19%、64 字节 −5%），
   128 字节起转正（+6%），之后单调放大（1KB +13%、4KB +34%、64KB +46%、1MB +175%）。
   所以不在入口判断——进入时只知道块内剩余多少，那与引号在哪无关——而是让 SWAR 先扫 128 字节，仍未命中才交给向量。
   最初写成"入口按块内剩余长度判断"，结果 1MB 整块交付时每个 5.6 字节的短串都照样进汇编，密集 tools 直接腰斩到 335 MB/s；
   阈值改 32 / 64 / 128 毫无差别，正是这个 bug 的征兆。

   **收益（arm64，SWAR → NEON 中位）**：base64 整块 4758 → **12101**（+154%）、长字符串 4760 → **12450**（+162%）；
   池化驱动下 4733 → **15059**（+218%）与 4377 → **15112**（+245%）。短档零代价：64 字节及以下在噪声内，
   密集 tools +4.8%、嵌套内容块 +6.2%、短串 len=6 +0.4%。

   **amd64 走 SSE2，同宽 16 字节，但写法更简单**：`PMOVMSKB` 直接把每个字节的最高位收成一个 bit，位索引就是字节
   索引，不需要 NEON 那套魔数；控制字符用 `PMINUB` 与 `0x1F` 取最小值后再 `PCMPEQB` 比对（相等即 `c <= 0x1F`），
   这样绕开 `PCMPGTB` 把 0x80 以上当负数的符号问题。**只用 SSE2、不做 AVX2**：AVX2 要运行时探测，而外部模块不能
   引用 `internal/cpu`，替代是引入 `golang.org/x/sys/cpu`（毁掉刚兑现的零依赖）或自写 CPUID 汇编；SSE2 是 amd64
   基线、无需探测，先拿这份确定的收益。AVX2 的 32 字节版留待以后，并记下它的代价。

   **wasm 拿不到这份收益**：Go 编译到 `wasip1` 不生成 SIMD（`runtime` 里零处 SIMD），所以网关侧仍走字运算版。
   分流是 `//go:build arm64 && !purego` / `amd64 && !purego` / `(!arm64 && !amd64) || purego`，用
   `go list -f '{{.GoFiles}}'` 逐目标验过（arm64→NEON、amd64→SSE2、wasip1→generic 且无 `.s`），
   `purego` 标签下两种架构都回到字运算版且数字回到基线。

   **SSE2 版的验证边界要写明**：开发机是 arm64，**SSE2 的逻辑没有在本机执行过**——只验证了能汇编、能编出 amd64
   测试二进制、分流选择正确。它的正确性由 CI 的 amd64 job 兑现（`ubuntu-latest` 跑 `go test -race ./...`，
   其中包含逐字节参考实现对照与"加速器契约"穷举测试，两者已改为架构中立、两边共用）。

   等价性靠两套穷举钉住：一个逐字节的参考实现（`scanstring_test.go`），以及汇编契约测试
   （`scanstring_neon_arm64_test.go`：可以停早、但绝不能越过首个终止符、不能在还剩满 16 字节时停、不能返回非终止符位置），
   三类终止符 × 长度 0–80 × 每位置 × 每起点，加两万条随机输入。

9. **零拷贝 key**（降级）：key 与其周围空白不跨块时，`kvRaw` 直接切片输入缓冲，跨块才拷贝。
   原本排在前面，理由是"派发帧密集的输入分配减半"；但写穿之后每请求只剩 15 次分配 / 71KB，
   而上面那份 profile 里与 `kvRaw` 有关的项一个都没进前 12。先做 SWAR，这一项等有证据再提。
10. **wasm 基准进 CI**：`GOOS=wasip1 GOARCH=wasm go test -c`，用 wazero 运行 `-test.bench`，把目标环境的数字放进 README。

验收：`bench_test.go` 三类输入的数字进 README；任何一项不能让黄金差分产生变化。

---

## M4 · v0.5 —— 生态

### 4.1 `ason/difftest`：通用差分 harness

把 `examples/llm/golden_test.go` 的做法做成包：

```go
type Suite struct {
	Name      string
	New       func(cfg map[string]any) *Transformer
	Reference func(in []byte, cfg map[string]any) (map[string]any, error) // 可选：在线参照
	Golden    string                                                         // 或离线黄金 JSONL(.gz)
	Chunks    []int
	Canon     func(m map[string]any)                                         // 比对前归一（如 tools 参数子树按数值）
	Lenient   func(refErr error) bool                                        // 参照失败但流式可放行
	Fallback  func(err *Error) bool                                          // 参照成功但允许的回落
}
func Run(t *testing.T, s Suite)
func WriteGolden(path string, s Suite, inputs []Case)  // 用参照生成黄金
```

### 4.2 `ason/gen`：随机 JSON 形状生成器

`gen.Spec` 描述字段、类型分布、顺序打乱、转义、大小；各套件共用，替换现在每处一份的生成器。

### 4.3 `ason/sse`：事件流伴侣

响应方向是一串小 JSON 事件（SSE）。`sse.Splitter` 处理 `data:` 多行、注释、`[DONE]`；`sse.Transform(factory)` 对每个事件
用一个新 Transformer 转换、保持帧边界。LLM 响应转换由此也能流式化。

### 4.4 文档

英文 README / DESIGN；"写一个协议"教程（以 chatconv 从手写分支到 Router 的演进为线索）；覆盖率与 pkg.go.dev 徽章。

---

## 兼容性与迁移

- v0.x 阶段每个里程碑一个 minor 版本；旧 API 保留至少一个版本并标 Deprecated。
- Higress 迁移点：M1 把 guard 与 harness 的回落判定改为 `Error.Code`；M2 让 model-router 设 16KB 窗口、ai-statistics 用 `DupKeysFirst`；
  M3 无 API 变化；M4 用 `difftest` 替换仓内差分 harness 的公共部分。
- 每个里程碑的验收都包含：`examples/llm` 黄金差分（7 套、7456 条、4 种分块）零变化。

## 顺序与估算

| 里程碑 | 内容 | 估算 |
|---|---|---|
| M1 v0.2 | ~~结构化错误 + 英文文案~~ → Router（chatconv 重写）→ Trace → 英文文档 | 剩 2 天 |
| M2 v0.3 | ~~根形状~~ / 多文档流 → ~~窗口与预算~~ → ~~重复 key 策略~~ → ~~UTF-8 选项~~ | 剩 0.5 天 |
| M3 v0.4 | 零拷贝 key → ~~sink~~ → wasm 基准 → （视 profile）区域 SWAR | 剩 1.5 天 |
| M4 v0.5 | difftest → gen → sse → 教程 | 3 天 |

M2 只剩多文档流，M1 的结构化错误已定型。Higress 侧待办：升级 ason 伪版本后，guard 与差分 harness 的回落判定改为按 `Code`。

---

# 零拷贝与规模化：v0.6 设计（2026-09-07）

## 零、剖析结果：钱花在哪

1MB 真实聊天体、16KB 分块、透传协议，`-memprofile` 的分配字节分布：

| 来源 | 占比 | 是什么 |
|---|---|---|
| `Writer.reserve` | **78.5%** | 每块输出缓冲的首次分配 |
| `Writer.Raw` | **20.2%** | 输出缓冲的扩容（把输入字节拷进来） |
| `onKeyDone` | 0.6% | key 驻留 |
| 其余 | <1% | 帧、路径、Capture |

**98.7% 的分配来自"把没改动的字节从输入拷到输出"。** 协议逻辑、key 处理、动作派发加起来不到 1%。
每 1MB 请求 645KB 分配、48 次分配，几乎就是一份输入的拷贝。

这解释了三件此前分开看的事实：wasm 堆有 160MB 固定底噪（GC 要在这些垃圾上工作）、
吞吐卡在 1.66 GB/s（memcpy 带宽的量级）、70KB 场景内存输给官方（多的正是这份拷贝与它的 GC 余量）。

## 一、洞察：输出的三种字节

把输出流按来源分成三类，比例悬殊：

| 类别 | 1MB 请求里的占比 | 现在的处理 | 应该的处理 |
|---|---|---|---|
| **透传字节**（协议没动的） | ~99.9% | 拷进输出缓冲 | **不拷，直接引用输入切片** |
| **生成字节**（改写值、包壳、新增字段） | ~0.1% | 拷进输出缓冲 | 拷进一小块 scratch（必要） |
| **缓冲字节**（Capture / Defer / Prefix） | 视协议，通常 0 | 拷进 capBuf | 拷（语义要求，且有界） |

进一步的洞察：**大多数分块里三类字节只有第一类**。1MB 的请求体，改写只发生在头部的 model、
尾部的新增字段，中间几十个分块一个字节都没改。对这些分块，正确的动作不是"高效地拷贝"，
而是**根本不产出输出**——让调用方原样放行原始字节。在网关里这等于不调用 `ReplaceHttpRequestBody`，
宿主缓冲原地放行，拷贝与分配同时归零。

## 二、设计：三层零拷贝

### 2.1 未改动分块直通（收益最大，改动最小）

Writer 增加一个"本块是否产出过与输入不同的字节"的标记。判定不靠比较，靠记账：
`Raw(b)` 时若 `b` 恰好是当前输入块的下一段连续切片（指针与长度相接），只推进游标不拷贝；
一旦出现生成字节、跳过字节、或乱序写入，本块标记为"已改动"，退回现有路径。

```go
// 新增的输出契约
type Output struct {
    Unchanged bool   // true：本块与输入逐字节相同，调用方直接放行原始字节
    Bytes     []byte // Unchanged 为 false 时的完整输出（所有权交给调用方）
}
func (t *Transformer) OutBlock() Output
```

调用方（Higress guard）拿到 `Unchanged` 就返回 `ActionContinue` 且不替换宿主缓冲——
这同时修掉一个已发现的链路缺陷：多个 wasm 插件串联时，替换宿主缓冲会破坏后一个插件正在累积的数据。

预期：1MB 请求的分配从 645KB 降到几 KB；吞吐上限从 memcpy 带宽提升到纯扫描带宽（长字符串 SWAR 已到 2.6 GB/s）。

### 2.2 向量化输出（有改动的分块）

有改动的分块也不必拼成一整块。Writer 维护段列表，透传段指向输入切片，生成段指向 scratch：

```go
type Segment struct { Data []byte; Owned bool }
func (t *Transformer) OutSegments() []Segment   // 按序拼接即为完整输出
```

宿主要求连续内存时（`ReplaceHttpRequestBody`）在最后一刻拼一次，长度已知、一次分配到位，
不再有 append 的倍增扩容；能逐段消费的调用方（写连接、写文件、SSE 下游）连这一次都省掉。

### 2.3 零拷贝 key 与原文

`kvRaw` / `keyBuf` 现有 21 处 append。key 不跨块时直接切片输入，跨块才回退到拷贝。
`KeyRaw()` 的契约本来就是"回调期间有效"，与此一致。占比虽小（0.6%），但它同时消除
`onKeyDone` 里最后一处按请求分配。

## 三、提交点：从"攒输出"到"两遍扫描"

现在提交窗口（64KB）内的输出必须攒着，这是引擎唯一的非流式点，也是每条在途流固定持有的部分。
原始字节本来就在宿主缓冲里，我们不必再存一份：

- 第一遍：只扫描、只判定，不产出任何输出（协议回调照常，但 Writer 处于 dry-run 模式）；
- 到提交点：从宿主重读这 64KB，第二遍产出输出并下发；
- 之后：正常单遍流式。

代价是提交窗口内多扫一遍（64KB 约 0.5ms CPU），换来每条在途流少持有一份 64KB 输出，
并且回落语义完全不变（提交点前依然一个字节没发出）。配合 2.1，提交点之后的分块多数直接透传，
**整条流的稳态持有量降到只有 Defer / Capture 的有界部分**。

## 四、动态编译：把协议配置编译成数据

现状是每个回调走一串 `switch t.Depth()` 与字符串比较。可以在配置加载时编译成跳转表：

```
(深度, key 哈希) → 动作槽位
```

- **预编译跳转表**：启动时把协议关心的路径展开成表，回调里一次哈希 + 一次查表，取代深度分支与字符串比较。
- **声明式规则**：改名、映射、包壳、丢弃、路径重写这几类占了新协议的八成工作量，做成规则后编译进同一张表，
  加一个供应商不必写 Go 代码；结构性差异大的协议（Claude 的 system 上提、Gemini 的 parts 重组）保留手写 hooks。
- **真 JIT 不可行**：wasm 没有可写可执行页；但 Envoy 支持热加载 WasmPlugin，等价于运行时可编程。

## 五、顺序与预期

| 项 | 预期收益 | 风险 | 估算 |
|---|---|---|---|
| 2.1 未改动分块直通 | 分配 −99%，吞吐 +50%~100%，顺带修链路缺陷 | 改公开输出契约 | 1 天 |
| 2.2 向量化输出 | 有改动分块的扩容归零 | 中 | 1 天 |
| 三、两遍扫描 | 每流稳态持有 −64KB，wasm 堆底噪显著下降 | 提交点逻辑重写，需重跑全部差分 | 1.5 天 |
| 2.3 零拷贝 key | 最后一处按请求分配 | 低 | 0.5 天 |
| 四、跳转表 + 声明式规则 | 派发成本下降；新协议无需写码 | 大，需重做协议层 | 3 天 |

验收标准不变：7 套 7456 条黄金差分零变化、模糊测试通过、网关端 104 例逐字段一致。

## 六、v0.6 零拷贝：实现与实测（2026-09-07）

已实现 2.1（调用方持有输出缓冲）、2.2 的记账式未改动信号、2.3 零拷贝 key；未做向量化输出（已下调）与跳转表（性能中性，属生产力项）。

**契约上的一处修正**：设计里写的是"每 VM 一个缓冲"，实现改成了每转换器一个。
一个 Envoy worker 会交错处理多条流，而转换器在提交点之前跨块累积输出，共享缓冲会把两条流写在一起。
`SetOutBuffer` 的文档写明一个缓冲属于一个转换器，`TestFixedOutBufferSequentialReuse` 守住顺序复用这条路径。

**微基准达到预期**：

| 指标 | 之前 | 之后 |
|---|---|---|
| 单请求分配量 | 645KB | 6.8KB |
| 单请求分配次数 | 48 | 17 |
| 长字符串扫描吞吐 | 2.6 GB/s | 4.1 GB/s |

**网关端是中性的**。在 Higress 上按 70KB × 800 与 1MB × 400 两个锚定点成对重复跑，
QPS、p99、Envoy 堆全部与不带这批改动的版本重叠。网关卡在 Envoy 的缓冲与网络上，
引擎的分配行为落在噪声以下。改动保留的理由是库自身的开销与 GC 压力真实下降，且在网关上不产生代价。

### 2.3 零拷贝 key 的落地（2026-09-08）

分配剖析把 `onKeyDone` 定位为每请求分配的三分之一：每个转换器各建一份 256 槽的 intern 缓存（4KB），
再为每个首次出现的 key 分配一次字符串——而一个网关上前后请求的 key 集合几乎相同。

`SetKeyCache` 让多个转换器共用一份缓存。首个文档之后，key 派发不再分配。
实测 chat 形态的请求：**每请求 19 次分配降到 14 次**，输出逐字节一致，差分语料无变化。
缓存属于单个 goroutine：一个 Envoy worker 上的流交错但不并发，正是它的适用场景；跨 goroutine 共享是数据竞争。

剩下的 14 次里最大的一块是输出缓冲的物化（`passthroughAt` 约 4 次/请求，每次约一个提交窗口），
那是虚拟透传被协议写入打断时发生的。

**试过让调用方改用 `SetOutBuffer` + 每 VM 空闲列表复用缓冲，网关实测回退了**：线性内存多 40–90MB，
GC 看门狗回收次数不变。原因是池化的缓冲是活的、每请求的缓冲是垃圾——池化把活跃集加上了 `并发 × 缓冲大小`，
而 GC 次数不变说明看门狗回收的根本不是这些缓冲。**分配次数和 GC 压力不是一回事**，这条要记住。
调用方保持 `SetSink`。

### 提交点自适应的落地与一处模型修正（2026-09-08）

`CommitNow` 让调用方在拿齐请求头所需字段后立刻提交，64KB 窗口只做上限。Higress 网关实测（成对、带预热）：
70KB × 800 吞吐不变（682 → 676 QPS），RSS 少 68MB，Envoy tcmalloc 少 63MB；1MB × 400 吞吐不变，RSS 少 51MB。
每一条流都提前提交了（计数 42625/42625）。

**省下的内存在 Envoy 侧，不在 wasm 侧**：tcmalloc 从 236 降到 174，wasm 线性内存只从 84 降到 80。
这修正了此前的模型——提交点之前调用方返回 `ActionPause`，原始字节留在 **Envoy 的解码缓冲**里，
不在 wasm 堆里，所以窗口的代价一直是 Envoy 内存。早先把 70KB 档 176MB 的 wasm 线性内存归因于
"在途 × 窗口"，方向对（随并发走）但位置错：那是每流在 wasm 里的其他工作集（宿主交给插件的分块拷贝、
转换器与帧、guard 状态），与窗口无关。**引擎里的任何分配优化都碰不到它**——三次实测（零拷贝引擎、
零拷贝 key、缓冲池）在网关上全部中性或负面，原因就在这里。
