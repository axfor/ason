# ason

**A streaming JSON transformation framework for Go.** ason rewrites a JSON document *while it is still
arriving*: feed it byte chunks of any size, read the transformed output as it becomes available. Memory does
not depend on the document size — a protocol decides, key by key, what to forward, drop, rename, move, wrap,
capture or rewrite, and only the parts it explicitly asks to hold are buffered, each with an upper bound.

Bytes the transform does not touch are forwarded verbatim — whitespace, key order and escapes included — so
an in-place rewrite is byte-identical to an `sjson`-style edit; bytes it does touch are produced without ever
materialising the document. There is no dependency outside the standard library and it builds for
`GOOS=wasip1 GOARCH=wasm`.

```
go get github.com/axfor/ason
```

## How it works

A **protocol** is a set of callbacks. The scanner walks the byte stream and, for every key or array element
of a container the protocol has *entered*, asks the protocol for an **action**:

| Action | Effect | Buffering |
|---|---|---|
| `Pass` | key + value forwarded verbatim (`As` renames, `Wrap` adds prefix/suffix, `Inner` strips the quotes, `At` writes to an outer output level) | none |
| `Skip` | dropped | none |
| `Enter` | descend; children are dispatched too (`Lazy`: omit if nothing is written, `Flat`: no output level, `Lenient`: a scalar becomes `Pass`, `Via(hook)`: route the whole subtree to another Protocol) | none |
| `Probe` | look at the value type first (string / object / array / null / bool / number), then decide in `OnStart` | none |
| `Observe(cap)` | `Pass` plus a copy handed to `OnValue` | bounded |
| `Capture(cap)` | value collected and handed to `OnValue`; the protocol writes the replacement | bounded |
| `Defer(cap)` | key + value held and re-dispatched when the protocol calls `Release` (a field that arrives before the field that decides its shape) | bounded |
| `Prefix(cap)` | string: the first `cap` bytes go to `OnPrefix`, which decides how the rest streams (split a `data:` URL, redact, detect a scheme) | window only |
| `Bail(reason)` | unsupported: the transformer stops and reports the reason | — |

The writer builds output **lazily**: a container that never receives a write leaves no trace; a protocol can
also push its own output levels (`PushObj` / `PushArr` / `Pop`) to move an input container into a nested
output shape. Output is released only after a **commit point** (`CommitBytes`, 64KB of input), so a caller
that keeps the original bytes until then can fall back to another strategy when the protocol bails early.

The scanner validates JSON with the same rejection surface as `encoding/json` (structure, literals, number
grammar, escapes, control characters, whitespace), byte by byte, with constant state — inside pass-through
regions as well as in dispatched frames, which the fuzz targets assert in both directions. String bodies are
scanned eight bytes at a time; long strings and base64 payloads stream at roughly 2.5 GB/s per core, a
realistic 1MB chat body at roughly 1.6 GB/s, dense structure at roughly 420 MB/s, with a few hundred
allocations per megabyte regardless of how many keys the document has.

Per-transformer options: `SetCommitBytes` (commit window), `SetBudget` (cap on everything the engine may hold
for one document; `Buffered()` reports it), `SetRoot` (`RootObject` by default, `RootArray`, or `RootAny` —
array roots dispatch by index), `SetDupKeys` (`DupKeysPass`, `DupKeysBail`, or `DupKeysFirst` for gjson-style
first-wins), and `SetValidateUTF8` (RFC 3629 validation of strings and keys, off by default like `encoding/json`;
whole sequences are checked with one table lookup, sequences split across chunks fall back to a byte DFA).

## Examples

`examples/` holds one runnable program per technique — passthrough, rename, in-place rewrite (`KeyProbe`),
redaction with `Prefix`, restructuring `items` into `data.items`, deferred replay when a value arrives before
the field that decides its shape, sub-hooks with `Enter().Via`, streaming a `data:` URL payload into another
shape, read-only observation, and the commit-point fallback. Each reads stdin in small chunks:

```
echo '{"items":[{"k":"v"}],"owner":"team/42"}' | go run ./examples/rewrite -chunk 3
```

`example_test.go` covers the same techniques as verified `Example` functions.

## Tests

`go test ./...` runs four layers, all on every push (see `.github/workflows/test.yml`, with `-race`,
Go 1.24 and stable, plus a short native fuzz job and a `wasip1` build):

- **engine**: actions and replay rules, format fidelity (no byte changes on untouched input,
  `sjson`-identical rewrites), strict literal / escape / whitespace rejection at every chunk size, chunk-size
  invariance on random documents, garbage input never panics, deep nesting and multi-megabyte strings;
- **examples**: every program under `examples/` is built and run with fixed input at three chunk sizes and
  compared with `examples/testdata/*.golden`; the `Example` functions in `example_test.go` are verified too;
- **scenarios**: `examples/chatconv/conv` is a complete request-conversion protocol that uses every engine
  path (Capture, Defer / Release, Prefix, Via, Lazy levels, protocol-built levels, Tail). It is checked
  against a buffered reference implementation on 40 hand-written shapes and 3000 random documents
  (shuffled field order, escapes, multi-modal parts, tool shapes, malformed input) at chunk sizes 1 / 3 / 7 / 64 / 4096;
- **protocol suite** (`examples/llm`): the protocols Higress ai-proxy runs on this engine, with their
  behavioural tests and a golden differential — 7 suites, every hand-written case plus 1000 random requests
  each, compared field by field with the output of the reference (buffered) implementations at four chunk sizes;
- **fuzz**: `FuzzPassthrough` and `FuzzKeyProbe` (`go test -fuzz=FuzzPassthrough .`).

Design notes: [docs/DESIGN.md](docs/DESIGN.md). Roadmap and the reasoning behind it: [docs/OPTIMIZATION.md](docs/OPTIMIZATION.md).

## 中文说明

ason 是 Go 的通用流式 JSON 转换框架：文档边到达边改写，按任意大小的块喂入、随时取出已转换的输出，内存与文档大小无关。
协议逐个 key 决定直通、丢弃、改名、搬家、加壳、捕获或改写，只有它明确要求持有的部分才缓冲，且都有上限。
没动的字节一个不改（空白、顺序、转义都保留，原位改写与 sjson 逐字节一致），改动的部分也不需要物化整份文档。
无标准库以外的依赖，可编译到 `GOOS=wasip1 GOARCH=wasm`。

协议就是一组回调：扫描器对每个 key / 数组元素向协议要一个动作（Pass / Skip / Enter / Probe / Observe / Capture / Defer / Prefix / Bail），
写出器惰性建层，输出在 64KB 提交点之后才下发——调用方在此之前保留原始字节，协议判定不支持时可以换一条路。
扫描器按 `encoding/json` 的拒绝面逐字节校验，常数状态。示例见 `examples/`（每种技巧一个可运行程序）与 `example_test.go`，设计见 `docs/DESIGN.md`，优化方案与洞察见 `docs/OPTIMIZATION.md`。
