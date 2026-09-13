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

One document, four chunks: the engine scans, your hooks decide, the engine moves the bytes.

![How the engine and a protocol's hooks transform a document as it streams: the document arrives in four chunks, and inside one pass of the ason engine every field reaches the protocol as a hook call — OnKey answers Pass for model, Skip for debug, Capture for owner, whose value comes back through OnValue where the protocol writes "team":42, and Pass for the 512KB text value. The engine moves the bytes itself according to those answers. Numbered marks show how much output exists once each chunk has been read — chunk 2 carried only the skipped field, so the output did not grow — and the held line stays at zero except for the nine bytes of the captured value, "team/42", which the engine holds only until OnValue takes it.](docs/streaming.svg)

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
| `Bail(reason)` / `BailCode(code, reason)` | unsupported: the transformer stops and reports the reason (and a `Code`) | — |

The writer builds output **lazily**: a container that never receives a write leaves no trace; a protocol can
also push its own output levels (`PushObj` / `PushArr` / `Pop`) to move an input container into a nested
output shape. Output is released only after a **commit point** (`CommitBytes`, 64KB of input), so a caller
that keeps the original bytes until then can fall back to another strategy when the protocol bails early.

The scanner validates JSON with the same rejection surface as `encoding/json` (structure, literals, number
grammar, escapes, control characters, whitespace), byte by byte, with constant state — inside pass-through
regions as well as in dispatched frames, which the fuzz targets assert in both directions. String bodies are
scanned eight bytes at a time, and sixteen once a string turns out to be a long one: past 128 bytes the scan hands
the rest to a vector loop. By default that loop is assembly -- NEON on arm64, SSE2 on amd64, with AVX2 chosen at
run time from CPUID when the CPU and OS both allow it. Built with `GOEXPERIMENT=simd`, the same scan comes from
`simd/archsimd` instead, on arm64 and amd64 and also on wasm, which has no assembly path at all.
Through a sink, a 1MB body on arm64 with the default build: long strings at roughly **11.6 GB/s** per core and
base64 at 10.9 (11.7 and 12.0 with a buffer pool), nested content parts at roughly 0.75 GB/s, dense tool
definitions at roughly 0.60 GB/s. With `GOEXPERIMENT=simd` the long values go further -- **14.8** and 12.9 (15.5
and 15.4 pooled) -- and the two structural shapes stay where they are, within noise. wasm gains the most in
relative terms, having had no vector scan at all before Go 1.27: long strings go from 2.36 to **5.56 GB/s** and
base64 from 2.26 to 5.52, against a measured 4-6% cost on the structural shapes. Where there is no vector scan --
the `purego` build tag, or a wasm build without the experiment -- long strings come to roughly 4.8 GB/s on arm64,
and every other figure is within noise of the vector build, because what it accelerates is long values and
nothing else.

Per-transformer options: `SetCommitBytes` (commit window), `SetBudget` (cap on everything the engine may hold
for one document; `Buffered()` reports it), `SetRoot` (`RootObject` by default, `RootArray`, or `RootAny` —
array roots dispatch by index), `SetDupKeys` (`DupKeysPass`, `DupKeysBail`, or `DupKeysFirst` for gjson-style
first-wins), `SetValidateUTF8` (RFC 3629 validation of strings and keys, off by default like `encoding/json`;
whole sequences are checked with one table lookup, sequences split across chunks fall back to a byte DFA), and
`SetFieldTypes` / `SetFieldTree` (reject, while streaming, a value whose type the struct the caller would have
unmarshalled into cannot hold — derived from that struct with `FieldTypesOf` / `FieldTreeOf`). `SetKeyCache`
shares one key intern cache across transformers, and `SetOutBuffer` builds the output in a buffer the caller
owns.

`SetSink(func([]byte))` hands committed output to a callback instead of returning it from `Out()`, and reuses
the output buffer afterwards: a 1MB stream in 16KB chunks allocates 17 times and 154KB in total instead of 77
times and 1.2MB. Add `SetBufferPool` and a shared `SetKeyCache`, which is how a proxy worker drives it -- its
streams take turns, so one request's buffer and interned keys are the next one's -- and a 1MB stream of any
shape costs **7 allocations and 1,256 bytes**, whether it arrives in 16KB chunks or in one piece.

Past the commit point the bytes that are only passing through are not copied at all -- a run
large enough to be worth a hand-over goes to the sink as a view of the input -- so the same body delivered in
one piece, as a proxy usually gets it, is never copied into a buffer of its own size: without a pool it costs
the commit window once (70KB), and with one it costs the 1,256 bytes above. Use it when the output is
consumed immediately (written to a host or a connection); the slice is only valid inside the callback.

When the transformer stops, `Err()` returns an `*Error` with a `Code` (`ErrSyntax`, `ErrIncomplete`, `ErrRoot`,
`ErrTrailing`, `ErrDuplicateKey`, `ErrLimit`, `ErrLeftoverDefer`, `ErrUnsupported` for a protocol's own `Bail`,
`ErrMisuse` for an action used where it cannot apply), an English message, the input offset at which it was
detected, and the path at that point. `Unsupported()` still returns the boolean and a one-line text
(`unexpected comma at byte 512 in messages[2].content`); callers that need to classify use the code rather
than the text. Offsets and paths do not depend on how the input was chunked (the one exception is the budget
check on pre-commit output, which runs at the end of each `Write`).

## Architecture

Three layers, a single pass, no object tree:

![ason architecture: the scanner reads the input and asks the protocol for an action on every event, bytes travel straight from the scanner to the lazy writer, and the guard holds the output back until the commit point](docs/architecture.svg)

The scanner turns bytes into events and the protocol answers each one with an action — but the bytes never
travel through the protocol. They go straight from the scanner to the writer, which builds the output lazily;
the protocol writes only what it replaces or adds. Around all of it, the guard decides how long a decision can
still be taken back.

### Dispatch frames and regions

Everything else follows from one split. A container the protocol has *entered* is a **dispatch frame**: every
key and every element inside it becomes a callback. From the first byte of a value whose action is settled to
its last byte is a **region**: dispatch stops, the scanner only tracks grammar, and the bytes flow to one
target.

| Region target | Opened by | Where the bytes go |
|---|---|---|
| out | `Pass` | to the writer — a range of the input chunk, not copied while the run is still contiguous |
| skip | `Skip` | nowhere |
| capture | `Capture(cap)` | bounded buffer → `OnValue` |
| observe | `Observe(cap)` | the writer *and* a bounded buffer → `OnValue` |
| defer | `Defer(cap)` | bounded hold, re-dispatched on `Release` |
| prefix | `Prefix(cap)` | window → `OnPrefix`, whose answer picks the target for the rest |
| validate | `Pass` / `Skip` on a root-level container the field tree has an opinion about | as `Pass` / `Skip`, plus a bounded copy type-checked when the region closes |

Inside a region nothing is dispatched, no path segment is pushed, no key is interned: nesting is a counter plus
a 64-bit bit-stack of container kinds (a slice past 64 levels), so a subtree the protocol never asked about
costs one range copy and no allocation at all. That is where "memory does not depend on the document" actually
comes from — a frame exists only where the protocol asked for one, and everything else is a region.

### Constant state, whatever the chunking

The whole scanner is three small machines, all fixed size:

- **byte state** — idle / in-key / in-string / in-scalar, plus the escape flag and the `\u` digit counter;
- **frame phase** — key → colon → value → comma, one per dispatch frame;
- **region phase** — nine phases (plus an "invalid" result) that encode the enclosing container kind, so a
  comma, a colon, a string or a scalar is one table lookup and only a bracket touches the bit-stack.

Nothing in that state knows where a chunk ends, which is the property the chunk-invariance tests assert: the
same document fed at 1, 3, 7, 64 and 4096 bytes produces the same output, the same errors and the same offsets.
Keys are interned through a direct-mapped 256-slot cache (a fixed 4KB, whatever the document does), so the
repeated keys that make up almost every dispatch do not allocate, and a flood of distinct keys cannot grow it.
`SetKeyCache` shares one cache across transformers — the keys of one document are the keys of the next, so
after the first one a dispatch allocates nothing at all; a cache belongs to one goroutine at a time.

### The lazy writer

Entering a container registers an output **level** and writes nothing. The first write inside opens it — and
every ancestor still unopened — with the separator handled at each level, so protocol code never tracks commas
or field order. A level never written to materialises as `{}` / `[]` on close, or as nothing at all under
`Lazy`. `Flat` gives an input container no level of its own, and `PushObj` / `PushArr` / `Pop` let the protocol
build levels the input does not have; the two together move one input container into a nested output shape.
`At(level)` writes to an outer level, which works exactly as long as every level above it is still unopened —
once one is open it is a misuse (`ErrMisuse`), not a quietly misplaced field.

### Byte fidelity

Whitespace is not skipped, it is parked: before the root, inside the raw key run (`[ws]"key"[ws]:[ws]`), before
an array element, between a value and its comma (parked on the output level, written with the next separator),
before a closing bracket, and after the root. A `Pass` region is a byte range. Together that is "untouched bytes
stay untouched", the property the format tests check byte for byte against an `sjson` in-place rewrite.

### Zero-copy output

While a chunk is still one unbroken pass-through run the writer copies nothing — it extends a length into the
caller's chunk, and `Out()` hands back a slice of that chunk. The first byte that is not a straight
pass-through materialises the run and the writer returns to a real buffer. `Out()` then hands over the buffer's
ownership instead of copying it, so the pre-commit buffer becomes garbage immediately rather than being held
for the life of the stream. `SetOutBuffer` gives the transformer a caller-owned buffer to build in (one buffer
per transformer: output accumulates across chunks, so sharing one between concurrent streams interleaves them),
and `SetSink` drains at the end of every `Write` and reuses it.

### What is held, and what bounds it

`Pass`, `Skip` and `Enter` hold nothing. `Capture`, `Observe`, `Defer` and `Prefix` hold up to their own `cap`;
a validated container holds up to 64KB; output is held until the commit point. `Buffered()` is the sum of all
of it and `SetBudget` caps that sum — the per-item caps stay per item. Either limit bails with `ErrLimit`, and
the offset points at the first byte that did not fit, not at the end of the chunk that carried it.

### Deferred fields and replay

`Defer` holds the raw key run and the raw value. `Release` does not replay on the spot: it marks the frame, and
the replay happens at that frame's next safe point — the end of the current value, or the close of the frame —
never in the middle of a child frame, and never at a child's request. Each held pair then re-enters `OnKey` and
is scanned again, so it takes the action the protocol would take *now*, with everything the protocol has since
learned. Items still held when the container closes are `ErrLeftoverDefer`: a protocol that forgets them would
otherwise emit a document with a different meaning, so it fails loudly instead.

### Sub-hooks

`Enter().Via(hook)` hands every callback inside a subtree to another `Protocol`, including the levels that hook
enters itself and its own `Defer` replays; the container's `OnLeave` goes back to whoever issued the `Enter`, so
it can `Pop` what it pushed. Paths and depths stay absolute, so a reusable part does not need a forwarding
branch in each of the six callbacks.

### Suspending the scan

A protocol may need something the document does not carry -- an image it has to fetch and inline, say.
`t.Suspend()`, called from any callback during a `Write`, stops the scan at the next byte boundary: the
callback finishes, the rest of the chunk is kept, and `Write` returns with `Suspended()` true. Until `Resume()`
the protocol owns the output: it writes the value from outside any callback, in slices if it is large, and
`Flush()` hands each slice to the sink as it goes, so nothing piles up. `Resume()` scans the kept bytes and
carries on, and may suspend again. `Write` or `Finish` while suspended, or a `Suspend` during a Defer replay or
in `Tail`, is a misuse and bails with `ErrMisuse`.

### The commit window

No output is released before `CommitBytes` (64KB) of input has been scanned. Inside that window a `Bail` costs
nothing: the caller still holds every raw byte and can fall back to buffering the document and transforming it
some other way. A caller that knows it no longer needs the retreat — it has seen every field its decision
depends on — gives it up early with `CommitNow()`, and the window becomes a ceiling rather than a fixed cost.

Past the commit point, released bytes cannot be recalled, so a late bail is a failure — or, for a
passthrough-shaped protocol, forwarding the rest of the input verbatim. That is safe only where `Aligned()` is
true (every byte read so far has been written out or dropped, nothing held back for a decision still to come)
and `RootDone()` is still false, because the root's closing token is written by `Finish` alone — which also
runs `Tail` and writes the whitespace that followed the root.

### Errors

The `Code` / message / offset / path shape is described above; what the architecture adds is where the offset
points. A grammar error points at the failing byte, a cap or budget overflow at the first byte that did not
fit, a protocol's own `Bail` at the current scan position, and one raised in `Finish` at the total length.
Replays do not disturb any of it: while deferred items are re-scanned the offsets stay at the outer position,
so an error found during a replay still points into the original input rather than into the held copy.

### Checking the shape the caller would have unmarshalled

A streaming transform replaces a buffered one, and the buffered one ended in an `Unmarshal` that rejected
documents by type. Forwarding a document that unmarshal would have refused makes the streaming path the more
permissive of the two — so the caller can hand over the shape it would have decoded into and get the same
rejection while streaming:

```go
t.SetFieldTree(ason.FieldTreeOf(ChatRequest{}, 4)) // implies the root-level table
```

The tree also carries the marshal side of every field (`Kind`, `Omit`, `Int`, `ZeroJSON`), for a protocol that
reproduces a buffered path's unmarshal-and-marshal round trip; the engine itself does not read those.

The table is *derived* from the struct by reflection (`FieldTypesOf` for root fields, `FieldTreeOf` for the
recursive form), because a hand-kept one drifts from the struct the moment a field is added, and the drift is
silent. It errs towards accepting throughout: a type that decodes itself (`json.Unmarshaler`,
`encoding.TextUnmarshaler`), an interface and a name two fields share all accept everything, `null` is accepted
for every field, and `encoding/json`'s case-insensitive name matching and its rejection of a fractional number
for an integer field are not reproduced. Every gap makes the check accept a little more than the unmarshal,
never less.

Nested checking is bounded the same way: only containers the tree has an opinion about are checked, up to 64KB
of content each, and past that the value is accepted rather than judged — a large field never becomes a
rejection, and a container the tree says nothing about keeps the region fast path untouched. Fields the
protocol *drops* are checked too, because the caller's unmarshal reads a known field whether or not the
transform keeps it. A mismatch is `ErrUnsupported`, which before the commit point still leaves the fallback
open.

### Invariants

1. **Untouched bytes are not touched.** Pass-through is byte-identical — whitespace, key order and escapes
   included — so an in-place rewrite matches `sjson` exactly.
2. **No assumption about field order.** A shape that needs a later field is held with a bounded `Defer` and
   replayed once the information is there; past the bound it is unsupported, not a guess.
3. **"Unsupported" has a way out.** Output is withheld until the commit point, so the caller can still take
   another route cleanly.

## Examples

`examples/` holds one runnable program per technique — passthrough, rename, in-place rewrite (`KeyProbe`),
redaction with `Prefix`, restructuring `items` into `data.items`, deferred replay when a value arrives before
the field that decides its shape, sub-hooks with `Enter().Via`, streaming a `data:` URL payload into another
shape, read-only observation, and the commit-point fallback. Each reads stdin in small chunks:

```
echo '{"items":[{"k":"v"}],"owner":"team/42"}' | go run ./examples/rewrite -chunk 3
```

`doc_example_test.go` covers the same techniques as verified `Example` functions.

## Tests

`go test ./...` runs four layers, all on every push (see `.github/workflows/test.yml`, with `-race`,
Go 1.24 and stable, plus a short native fuzz job and a `wasip1` build):

- **engine**: actions and replay rules, format fidelity (no byte changes on untouched input,
  `sjson`-identical rewrites), strict literal / escape / whitespace rejection at every chunk size, chunk-size
  invariance on random documents, garbage input never panics, deep nesting and multi-megabyte strings;
- **examples**: every program under `examples/` is built and run with fixed input at three chunk sizes and
  compared with `examples/testdata/*.golden`; the `Example` functions in `doc_example_test.go` are verified too;
- **scenarios**: `examples/chatconv/conv` is a complete request-conversion protocol that uses every engine
  path (Capture, Defer / Release, Prefix, Via, Lazy levels, protocol-built levels, Tail). It is checked
  against a buffered reference implementation on 40 hand-written shapes and 3000 random documents
  (shuffled field order, escapes, multi-modal parts, tool shapes, malformed input) at chunk sizes 1 / 3 / 7 / 64 / 4096;
- **protocol suite** (`examples/llm`): the protocols Higress ai-proxy runs on this engine, with their
  behavioural tests and a golden differential — 7 suites, every hand-written case plus 1000 random requests
  each, compared field by field with the output of the reference (buffered) implementations at four chunk sizes;
- **fuzz**: `FuzzPassthrough` and `FuzzKeyProbe` (`go test -fuzz=FuzzPassthrough .`).

Design notes: [docs/DESIGN.md](docs/DESIGN.md). Roadmap and the reasoning behind it: [docs/OPTIMIZATION.md](docs/OPTIMIZATION.md).
