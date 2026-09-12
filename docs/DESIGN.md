# ason design notes

## The problem

Rewriting JSON usually means parsing the whole document into objects, changing it, and serialising it again.
That requires buffering all of it, so memory is concurrency times document size. ason makes the rewrite a
**single pass over the byte stream**: it buffers only where a protocol genuinely needs it, always under a
declared bound, and forwards the rest untouched.

Three constraints shape everything else:

1. **Bytes the transform does not touch come out unchanged** — whitespace, key order and escapes included. An
   in-place rewrite is byte-identical to an `sjson`-style edit.
2. **No assumptions about field order.** A shape that cannot be decided until a later field arrives is held by
   a bounded `Defer` and replayed once the information is there; outgrowing the bound is reported as
   unsupported rather than silently buffered.
3. **Being unable to handle something must leave a way out.** No output is released before the commit point,
   so until then the caller still holds the original bytes and can take another route — typically buffering
   the document and using the code the streaming path was meant to replace.

## Layers

```
input bytes ──► scanner (engine.go) ──► actions ──► lazy writer (writer.go) ──► output bytes
                     │  OnKey / OnElem / OnStart / OnValue / OnPrefix / OnLeave / Tail
                     ▼
                Protocol (implemented by the caller)
```

**The scanner** builds no object tree. It does two things: inside a container the protocol has entered — a
*dispatch frame* — every key and element raises a callback; and once the protocol has chosen an action, the
stretch from the value's first byte to its end becomes a *region*, whose bytes flow to the output, to a
buffer, or nowhere, according to that action. Inside a region only structure and string state are tracked,
and nothing is dispatched.

**The actions** are listed in the README. `Probe` lets a protocol see the value's kind — string, object,
array, null, bool, number — before deciding, so a rule like "null means absent, any other wrong type is an
error" is one `switch`.

**The writer** opens levels lazily. `Enter` only records the level, without writing the opening bracket; the
first child written opens it together with every unopened ancestor, commas included. A level that closes
without ever being written to materialises as `[]` or `{}` — unless it was `Lazy`, in which case nothing is
written at all. A protocol can also build output levels of its own with `PushObj` / `PushArr` / `Pop`, which
together with `Enter().Flat()` maps one input container onto several nested output ones.

**Formatting is preserved.** Whitespace around keys in a dispatch frame, between a value and its comma,
before a closing bracket, and after the root all reach the output as they were. A `Pass` region is byte
pass-through by construction.

**Sub-hooks.** `Enter().Via(hook)` hands every callback inside a subtree to another Protocol — including the
levels that hook enters itself, and its own `Defer` replays — and `OnLeave` on the container returns to the
originator. A reusable piece therefore needs no forwarding clause in each of the five callbacks.

## Grammar checking

The scanner validates JSON byte by byte with the same rejection surface as `encoding/json`. Fuzzing asserts
that in both directions: what the standard library rejects, ason rejects; what it accepts, ason accepts.
Dispatch frames and regions are held to the same standard.

- **Structure**: a missing key, colon or value; a trailing comma; mismatched bracket kinds; content after the
  root; a root that is not an allowed shape (an object by default, `SetRoot` also allows an array or either).
  A region follows the same grammar: its phase encodes what kind of container is open, so a comma, colon,
  string or scalar is one table lookup, and only a bracket touches the small bit stack.
- **Scalars**: `null`, `true` and `false` matched exactly; numbers by the grammar, through a nine-state
  machine that rejects `01`, `1.`, `1e`, `+1`, `.5` and `NaN`.
- **Strings and keys**: the only valid escapes are `\" \\ \/ \b \f \n \r \t \uXXXX`; a control character below
  0x20 is rejected.
- **Whitespace** is space, tab, carriage return and newline, nothing else.
- **UTF-8 validity is not checked by default**, because `encoding/json` does not reject invalid UTF-8 either —
  it replaces it. `SetValidateUTF8(true)` applies RFC 3629: overlong encodings, surrogates, code points above
  U+10FFFF, and stray or missing continuation bytes are rejected. A sequence contained in one chunk is
  verified with a single table lookup; one split across chunks goes through a byte-at-a-time state machine.
- **Duplicate keys** in a dispatch frame follow `SetDupKeys`: dispatched as usual by default, reported as
  unsupported under `DupKeysBail`, or only the first one dispatched under `DupKeysFirst`, which is gjson's
  semantics.

Checking uses constant state and buffers nothing. String bodies are scanned eight bytes at a time with SWAR
tests for the quote, the backslash and control characters; long strings and base64 run at about 4.8 GB/s on
one core of an Apple-silicon laptop, whether the 1MB body arrives in 16KB chunks or in one piece.

## Types the caller's own decoder would have checked

A caller that would otherwise have unmarshalled the document into a struct loses that unmarshal's type
checking, because the transform only judges the fields it actually reads. `SetFieldTree` takes the struct's
shape — `FieldTreeOf` derives it by reflection rather than having anyone write it out, since a hand-kept table
drifts from the struct the moment a field is added — and the scanner then rejects a value whose type the
struct says it cannot have, at the root and inside containers alike.

Two properties keep this from turning working requests into failures. Only containers the tree has an opinion
about leave the region fast path, and only a bounded copy of each is kept; outgrowing it stops the check
rather than failing the request, so a large field is never rejected for being large. And the mapping errs
towards accepting throughout: a type with its own `UnmarshalJSON`, an interface, a name two fields share, all
take everything. Rejecting a document the unmarshal would have accepted turns a passing request into a failing
one; accepting one it would have rejected only leaves the check where it already was.

### The marshal side of the same tree

A buffered path sometimes edits a document *through* the struct: unmarshal, change a field, marshal back. That
round trip changes the document's shape even where nothing was edited -- keys the struct has no field for are
gone, the zero values of `omitempty` fields are gone, fields the document never had appear as their zero values,
numbers come out the way `json.Marshal` writes them. A streaming caller that has to produce the same bytes needs
the marshal side of every field, so the tree records it: what the Go value is (`FieldKind`: a string, a number,
a bool, a struct, a pointer, a slice, a map, an interface, or a type that marshals itself and cannot be
described), whether the tag says `omitempty`, whether a number field is an integer type (the decode rejects a
fraction there, which the type check lets through on purpose), and `ZeroJSON`, what the marshal writes for the
field's zero value. Nothing in the engine reads these; they exist for a protocol that reproduces the round trip.

## The commit point, and why it exists

`Out()` returns nothing until `CommitBytes` (64KB by default, `SetCommitBytes` to change it) have been
scanned. Output accumulates; the caller still holds the original bytes.

**What the window buys is the ability to take back "I cannot handle this".** A streaming transform can
discover mid-document that it has met a shape it does not implement, a duplicate key, a value of the wrong
type, or a bound it would have to exceed. There are two situations to be in at that moment:

- **Nothing has been released.** The caller drops the transformer and takes the original bytes elsewhere,
  typically to the buffered implementation the streaming path was meant to replace. The result is identical
  to not having tried.
- **Bytes are already out.** They cannot be recalled. The caller can only fail the operation or, for a
  pass-through protocol, forward the rest unchanged.

The commit point is the boundary between the two, so the window size is a trade: a larger one sees more of the
document while a clean retreat is still possible, a smaller one holds fewer bytes per concurrent stream. That
gives it a property worth stating plainly:

> **With the window set to X, every document of X bytes or less behaves exactly as it would have without the
> streaming path at all** — it is fully scanned before the commit point is reached, so anything unsupported is
> still found in time.

Note that the engine enforces this rather than leaving it to the caller: `Out()` withholding output before the
commit point is what makes the retreat safe, and a caller cannot opt out of it.

A caller can, however, give the retreat up early. Often it knows before the window fills that it will never
take it — it has seen every field its headers depend on, and would fail rather than fall back on anything
found after that. Holding a window's worth of bytes past that moment is memory spent on an option nobody will
exercise. `CommitNow` releases output from that moment on, exactly as if the window had just filled; the
window then bounds only the documents where that moment never comes. On a gateway whose clients all put
`model` first, that is the first chunk of every request.

Details of an unsupported document are in `Err()`: a `Code` classifying it — grammar, truncation, root shape,
content after the root, duplicate key, a bound exceeded, deferred items left over, a shape the protocol does
not support, an action used wrongly — a message, and the input offset and path where it was decided. All three
are independent of how the input was chunked. `Unsupported()` returns that message; classify by `Code`, never
by matching the text. A protocol's own `Bail` reason is kept verbatim, and `BailCode` can classify it.

`Finish()` is called on the last chunk: it runs `Tail`, closes the root, and hands over whatever output is
left. `Out()` transfers ownership of the buffer instead of copying it, so what accumulated before the commit
point is released with it, and a stream's live memory under high concurrency is tens of kilobytes.

## Waiting for something outside the document

A protocol sometimes needs a value the document does not carry: the OpenAI → Gemini conversion inlines http(s)
images the buffered code downloads once it has the whole body. A single-pass scanner has no natural place for
that, so the engine offers one: `Suspend`, called from a callback, stops the scan at the next byte boundary. The
callback finishes, the unconsumed rest of the chunk is kept inside the transformer, and `Write` returns with
`Suspended` true. The protocol then owns the output until `Resume`: it writes the value from outside any
callback -- in slices, calling `Flush` between them so each leaves through the sink before the next is built --
and `Resume` scans the kept bytes and carries on, possibly suspending again. Nothing changes for the memory
bound: the slice is the only new holding, the kept bytes are at most one chunk, and the input that arrives
while the protocol waits is the caller's to hold, which for a proxy means the host's buffer rather than the
plugin's memory.

`Write` and `Finish` while suspended, and `Suspend` during a Defer replay or from `Tail`, are misuses
(`ErrMisuse`): a suspension waits for the world, and the replay and the tail are moments with no world to wait for.

## What is tested here, and what you should test

This repository's tests cover the engine itself: the action and replay rules; formatting fidelity down to the
whitespace before a comma and the newline after the root; rejection of invalid literals, escapes and control
characters at every chunk size; invariance to chunk size over random documents; that malformed input never
panics; deep nesting and multi-megabyte strings; and `KeyProbe` matching sjson byte for byte.

A protocol built on ason should be tested differentially: run the same input through the streaming protocol
and through whatever implementation it replaces, then compare field by field. Use three chunk sizes — one
byte, a small prime, and one larger than the whole document — because the bugs that survive a single chunking
are exactly the ones that depend on where a boundary falls.
