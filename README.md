# ason

**Streaming JSON transformation for proxies and gateways.** ason rewrites a JSON document
*while it is still arriving*, chunk by chunk, with memory that does not depend on the document size.
Bytes the transform does not touch are forwarded verbatim — whitespace, key order and escapes included —
so an in-place rewrite is byte-identical to `sjson`; bytes it does touch (rename, move, wrap, drop, rewrite)
are produced without ever materialising the document.

It was built for [Higress](https://github.com/alibaba/higress) `ai-proxy`, where request bodies of several
megabytes (long contexts, base64 attachments) used to be buffered whole before protocol conversion. With ason
the gateway's memory stays flat at 2000 concurrent 1MB requests; the engine is protocol-agnostic and this
repository contains only the engine, examples and tests. The LLM protocol hooks live with the plugins that use them.

```
go get github.com/axfor/ason
```

## How it works

A `Protocol` is a set of callbacks. The scanner walks the byte stream and, for every key or array element of
a container the protocol has *entered*, asks the protocol for an **action**:

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
| `Bail(reason)` | unsupported: the caller falls back to its buffered path | — |

The writer builds output **lazily**: a container that never receives a write leaves no trace; the protocol can
also push its own output levels (`PushObj` / `PushArr` / `Pop`) to move an input container into a nested
output shape. Output is released only after a **commit point** (`CommitBytes`, 64KB of input), so a caller
that holds the request headers until then can still fall back cleanly when the protocol bails early.

The scanner validates JSON with the same rejection surface as `encoding/json` (literals, number grammar,
escapes, control characters, whitespace), byte by byte, with constant state. String bodies are scanned eight
bytes at a time; throughput is around 2.5 GB/s on a laptop core for long strings and base64.

## Examples

`example_test.go` is the tour: passthrough, rename, in-place rewrite (`KeyProbe`), redaction with `Prefix`,
restructuring `messages` into `input.messages`, deferred replay when `content` arrives before `role`,
sub-hooks with `Enter().Via`, streaming a `data:` URL body into another shape, and strict validation.
`cmd/ason-demo` runs a few of them on stdin:

```
echo '{"messages":[{"role":"user","content":"hi"}],"model":"openai/gpt-4o"}' | go run ./cmd/ason-demo -demo rewrite -chunk 7
```

## Tests

`go test ./...` — engine actions and replay rules, format fidelity (no byte changes on untouched input,
`sjson`-identical rewrites), strict literal / escape / whitespace rejection at every chunk size, chunk-size
invariance on random documents, garbage input never panics, deep nesting and multi-megabyte strings.
The design notes are in [docs/DESIGN.md](docs/DESIGN.md).

## 中文说明

ason 是面向代理 / 网关的流式 JSON 转换引擎：文档边到达边改写，内存与文档大小无关；没动的字节一个不改
（空白、顺序、转义都保留，原位改写与 sjson 逐字节一致），改动的部分（改名、搬家、加壳、丢弃、替换）也不需要物化整份文档。
它来自 Higress ai-proxy 的请求体流式转换：几 MB 的长上下文与 base64 附件原来要整体缓冲后才能做协议转换，
用 ason 之后网关在 2000 并发 × 1MB 请求下内存持平。本仓库只有协议无关的引擎、示例与测试；LLM 协议的 hooks 在使用它的插件里。

协议就是一组回调：扫描器对每个 key / 数组元素向协议要一个动作（Pass / Skip / Enter / Probe / Observe / Capture / Defer / Prefix / Bail），
写出器惰性建层，输出在 64KB 提交点之后才下发——调用方在此之前扣住请求头，协议判定不支持时可以干净回落。
扫描器按 `encoding/json` 的拒绝面逐字节校验，常数状态。示例见 `example_test.go`，设计见 `docs/DESIGN.md`。
