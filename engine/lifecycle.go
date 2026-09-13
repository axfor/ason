package engine

// A stream's life around the scan loop: what the caller may ask mid-flight (Committed, Dead, Aligned, Buffered,
// Unsupported), where output goes (SetOutBuffer, SetSink, the buffer pool), the suspend/resume and compaction path
// that lets a stalled stream hand its memory back, the drain and flush that move bytes out, Write and Finish at
// the two ends, and the accessors a protocol hook reads while inside a callback (Depth, Key, Idx, PathString).
//
// Split out of engine.go, which had grown to 1858 lines. Same package, same API.

// Buffered reports the number of buffered bytes currently held (for observation): captures, deferred items,
// and the output that has not been released yet. It does not include the root's closing token, which is held
// separately until Finish -- ask RootDone for that.
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

// checkBudget is called wherever a buffer grows.
func (t *Transformer) checkBudget(extra int) bool {
	if t.budget > 0 && t.Buffered()+extra > t.budget {
		t.BailCode(ErrLimit, "buffer budget exceeded")
		return false
	}
	return true
}

// Committed reports whether the commit point has been passed. A bail after it cannot take back the bytes already released.
func (t *Transformer) Committed() bool { return t.committed }

// Dead reports whether the transformer has stopped (after a bail). Protocols can return early from callbacks on it.
func (t *Transformer) Dead() bool { return t.dead }

// RootDone reports whether the scanner has read the end of the root value. From that point on the output is
// only complete after Finish: the root's closing token is written there, after the protocol's Tail hook, along
// with any whitespace that followed the root. A caller that stops feeding the transformer and starts forwarding
// input verbatim must check this first -- once it is true, the bytes still held would be lost and the document
// would go out truncated.
func (t *Transformer) RootDone() bool { return t.rootDone }

// Aligned reports whether the output has caught up with the input: every byte consumed so far has been written
// out, or dropped by the protocol's own decision, and none is held back for a decision still to come -- a key
// being read, whitespace waiting for its comma, a value being captured, probed or deferred. Only then can a
// caller stop feeding the transformer and forward the input verbatim from the next byte on; RootDone must be
// false as well, because the root's closing token is written by Finish alone.
//
// It says nothing about what the protocol would still do to the bytes to come: the caller knows whether its
// own rewrites are behind this point.
//
// The check is conservative. It is true only inside a value that is passing straight through with nothing
// pending around it, which is where a chunk boundary lands in any document dominated by one large field, and
// false at every other point -- so a caller that finds it false keeps feeding and asks again after the next
// chunk. With a sink set the output is handed over at the end of every Write; without one the caller has to
// take it with Out first.
func (t *Transformer) Aligned() bool {
	if !t.committed || t.unsupported || t.dead || t.pendSet || t.replaying > 0 || t.deferredBytes > 0 || t.suspended {
		return false
	}
	if !t.regOpen || t.regT != rtOut || len(t.regSuf) > 0 || t.valSub != nil {
		return false
	}
	if t.w.vlen > t.w.vfrom || len(t.w.buf) > 0 {
		return false // produced but not yet taken
	}
	for i := range t.w.frames {
		if f := &t.w.frames[i]; !f.opened || len(f.trail) > 0 {
			return false
		}
	}
	return true
}

// Unsupported reports whether input the transformer cannot handle was met. When true the output is unusable.
// The text is Err().Error(): reason + byte offset + path, ready for a log line; classify with Err().Code instead.
func (t *Transformer) Unsupported() (bool, string) {
	if t.err == nil {
		return false, ""
	}
	return true, t.err.Error()
}

// SetOutBuffer hands the transformer a buffer to build output in, owned by the caller and reused for every
// chunk and every stream. It removes the per-request allocation of the output buffer, which the allocation
// profile shows to be almost all of it; on wasip1 that matters more than the copy itself, because the garbage
// forces the Go runtime to collect while wasm linear memory never shrinks.
//
// Give it enough capacity for one commit window plus the largest chunk (128KB is a good default); a larger
// output still works, it just grows the slice once. Everything Out() or the sink hands back points into this
// buffer and stays valid only until the next Write / Finish. Must be called before the first Write.
//
// One buffer belongs to one transformer for its whole life: output accumulates across chunks until the commit
// point, so a buffer shared between concurrently running transformers would interleave their bytes. Share it
// only where streams are strictly sequential (a CLI, a single-stream worker), never across a proxy's in-flight
// requests.
func (t *Transformer) SetOutBuffer(b []byte) {
	t.w.buf = b[:0]
	t.w.fixed = true
	t.syncFlushRun() // everything handed over points into this buffer: nothing is written through past it
}

// SetSink sets an output receiver. Once set, output produced past the commit point in each Write / Finish goes straight to the
// sink and the output buffer is reused instead of handed over: no allocation per chunk, and a stream no longer produces as much
// garbage as it has input. Meant for callers that consume immediately (writing to a host or a connection). The sink must consume b before returning; b is invalid afterwards.
// With a sink set Out() always returns nothing. Must be called before the first Write.
func (t *Transformer) SetSink(sink func(b []byte)) { t.sink = sink; t.syncFlushRun() }

// syncFlushRun turns the writer's write-through on once there is a sink to take the bytes and the commit point is
// past: before it, the pass-through run is the retreat the caller can still fall back on, so it stays held.
func (t *Transformer) syncFlushRun() {
	if t.sink != nil && t.committed && !t.w.fixed {
		t.w.flushRun = t.sink
		return
	}
	t.w.flushRun = nil
}

// Suspend, called from a protocol callback during Write, stops the scan at the next byte boundary: the callback finishes, the
// rest of the chunk is kept, and Write returns with Suspended true. The protocol may then write to the output from outside
// any callback (a value it had to fetch from elsewhere, say), and Resume scans the kept bytes and carries on.
//
// A suspension waits for something outside the document, so it is a misuse during a Defer replay or in Finish.
func (t *Transformer) Suspend() {
	if t.dead {
		return
	}
	if t.replaying > 0 || t.finishing {
		t.BailCode(ErrMisuse, "Suspend outside a Write callback")
		return
	}
	t.suspendReq = true
}

// Compact hands back the room the transformer is holding but does not need: the output buffer once its bytes have
// been taken, and the reference to the last chunk. For a scan that is about to wait -- for a fetch, or for a field the
// caller needs before it can release anything -- where what it keeps is multiplied by every stream in flight. The next
// write allocates again, sized to the chunk it gets.
func (t *Transformer) Compact() {
	switch {
	case t.w.fixed:
	case len(t.w.buf) == 0:
		t.handBack()
		t.w.buf = nil
	case t.w.put != nil && cap(t.w.buf) > 2*len(t.w.buf)+4096:
		// A lent buffer with a little output in it: keep a copy of just that, so a scan that waits holds what it has
		// written rather than the room of whichever buffer it happened to be lent.
		b := append(make([]byte, 0, len(t.w.buf)), t.w.buf...)
		t.w.buf = t.w.buf[:0]
		t.handBack()
		t.w.buf = b
	}
	t.w.release()
}

// handBack returns an empty lent buffer to the pool.
func (t *Transformer) handBack() {
	if t.w.put != nil && !t.w.fixed && cap(t.w.buf) > 0 && len(t.w.buf) == 0 {
		t.w.put(t.w.buf)
		t.w.buf = nil
	}
}

// SetBufferPool lends the output buffer: get supplies one when there is output to write (n is the size wanted, a
// hint) and put takes it back once the sink has the bytes in it, instead of the transformer keeping one of its own
// between writes. Transformers that are written in turn -- the streams of one thread -- then share a few buffers,
// where each would otherwise hold one the size of its last chunk for as long as it lives. A buffer given with
// SetOutBuffer is not lent, and output taken with Out belongs to the caller and is not handed back.
func (t *Transformer) SetBufferPool(get func(n int) []byte, put func(b []byte)) {
	t.w.get, t.w.put = get, put
}

// Suspended reports whether the scan is stopped by Suspend. Write and Finish are misuses until Resume.
func (t *Transformer) Suspended() bool { return t.suspended }

// Resume continues a suspended scan with the bytes kept at the suspension. It may suspend again before they are used up.
func (t *Transformer) Resume() {
	if !t.suspended || t.dead {
		return
	}
	t.suspended = false
	held := t.held
	t.held = nil
	t.Write(held)
}

// Flush hands the releasable output to the sink now, outside a Write: for output produced while suspended, so a large
// value written in slices leaves the transformer slice by slice instead of piling up. Nothing happens without a sink or
// before the commit point.
func (t *Transformer) Flush() {
	if t.sink != nil && !t.dead {
		// Sized by what was just written, not by the chunk that last arrived: output produced outside a Write -- a
		// fetched value going out slice by slice -- has nothing to do with the size of the input chunk, and judging
		// the buffer against that would throw it away after every slice and allocate the next one from scratch.
		t.drain(len(t.w.buf))
	}
}

// drain hands the releasable output to the sink (past the commit point, no bail).
func (t *Transformer) drain(chunk int) {
	if t.unsupported || !t.committed {
		return
	}
	if t.w.virt {
		if t.w.vlen > t.w.vfrom {
			t.sink(t.w.vp[t.w.vfrom:t.w.vlen])
			t.w.vfrom = t.w.vlen
		}
		t.handBack()
		return
	}
	if len(t.w.buf) == 0 {
		t.handBack()
		return
	}
	t.sink(t.w.buf)
	if t.w.put != nil && !t.w.fixed {
		t.w.buf = t.w.buf[:0]
		t.handBack()
		t.w.hint = chunk
	} else if !t.w.fixed && cap(t.w.buf) > 2*chunk+4096 {
		t.w.buf = nil // do not keep the large pre-commit buffer; the next chunk allocates one of chunk size which is then reused
		t.w.hint = chunk
	} else {
		t.w.buf = t.w.buf[:0]
	}
}

// Out takes the releasable bytes. Returns nothing before the commit point, after a bail, or when a sink is set.
func (t *Transformer) Out() []byte {
	if t.unsupported || !t.committed || t.sink != nil {
		return nil
	}
	if t.w.virt { // never materialised: the output is a slice of the caller's chunk
		b := t.w.vp[t.w.vfrom:t.w.vlen]
		t.w.release() // handed over: keeping the chunk here would pin it until the next Write
		return b
	}
	t.w.release()
	if len(t.w.buf) == 0 {
		return nil
	}
	// Hand over ownership without copying or keeping capacity: the large pre-commit buffer (up to 128KB) becomes garbage right
	// away instead of being held by this stream to the end, which brings the live memory of an in-flight stream under high
	// concurrency from ~250KB down to a few dozen KB. The caller owns the slice; the next write allocates a new one.
	b := t.w.buf
	t.w.hint = len(b)
	if t.w.fixed {
		t.w.buf = b[:0] // caller-owned: valid until the next Write / Finish
	} else {
		t.w.buf = nil
	}
	return b
}

// Write feeds one chunk of input; chunks may be split at any byte boundary.
func (t *Transformer) Write(p []byte) {
	if t.dead {
		return
	}
	if t.suspended {
		t.BailCode(ErrMisuse, "Write while suspended")
		return
	}
	t.scanBase = int64(t.scanned)
	t.chunkLen = len(p)
	t.w.startChunk(p)
	t.scanned += len(p)
	t.lastChunk = len(p)
	// Size for the first real write; a chunk that stays virtual never allocates at all. With a sink the output does
	// not wait for the caller to take it, so the buffer never has to hold more than the commit window: a whole body
	// delivered in one piece used to size it by itself and allocate a megabyte to rewrite a field name.
	t.w.hint = len(p)
	if t.sink != nil && t.w.hint > t.commitBytes() {
		t.w.hint = t.commitBytes()
	}
	n := t.scan(p)
	if t.suspendReq {
		t.suspendReq = false
		if !t.dead {
			// Stopped inside the chunk: the bytes not consumed are kept (a copy: the caller owns p) and scanned again by Resume.
			t.suspended = true
			t.scanned = int(t.scanBase) + n
			t.held = append([]byte(nil), p[n:]...)
		}
	}
	t.fixOffset(int64(t.scanned))
	if !t.committed && !t.unsupported {
		if !t.checkBudget(0) {
			return
		}
		if t.scanned >= t.commitBytes() {
			t.committed = true
			t.syncFlushRun()
		}
	}
	if t.sink != nil {
		t.drain(len(p))
		t.w.release() // the sink has what this chunk produced; holding the caller's bytes past that pins them
	}
}

// Finish wraps up: calls the protocol's Tail and closes the root object.
// If it bails here, committed keeps its value; the integration layer uses that to choose between fallback and failure.
func (t *Transformer) Finish() []byte {
	if t.dead {
		return nil
	}
	if t.suspended {
		t.BailCode(ErrMisuse, "Finish while suspended")
		return nil
	}
	t.finishing = true
	defer func() { t.finishing = false }()
	// No chunk comes in: what Finish writes is the tail, a closing bracket or a few appended fields, so its buffer is
	// sized by that and not by the last chunk (a 1MB request that stayed virtual reserved 1MB to close its root).
	t.w.hint = 0
	t.scanBase = int64(t.scanned)
	if t.st == sInScalar {
		t.scan([]byte{' '})
		t.fixOffset(int64(t.scanned))
	}
	if !t.rootDone {
		t.BailCode(ErrIncomplete, "unexpected end of input")
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
	t.w.buf = append(t.w.buf, t.tailWs...) // whitespace after the root (a trailing newline) is kept
	t.committed = true
	t.syncFlushRun()
	if t.sink != nil {
		t.drain(0)
		t.w.release()
		return nil
	}
	return t.Out()
}

// cur returns the callback receiver of the current frame (the sub-hook mounted with Via, or the main protocol).
func (t *Transformer) cur() Protocol {
	if f := t.top(); f != nil && f.hook != nil {
		return f.hook
	}
	return t.proto
}

// hookAt returns the callback receiver of frame i; the main protocol when i < 0 or the frame has no hook.
func (t *Transformer) hookAt(i int) Protocol {
	if i >= 0 && i < len(t.frames) && t.frames[i].hook != nil {
		return t.frames[i].hook
	}
	return t.proto
}

// ---- for protocols: path and output ----

// Protocol returns the protocol in use (the integration layer reads the Prelude through it).
func (t *Transformer) Protocol() Protocol { return t.proto }

// W is the output writer.
func (t *Transformer) W() *Writer { return &t.w }

// Depth is the number of path segments.
func (t *Transformer) Depth() int { return len(t.path) }

// Key is the key of segment level; "" when that segment is an array index.
func (t *Transformer) Key(level int) string {
	if level < 0 || level >= len(t.path) {
		return ""
	}
	return t.path[level].k
}

// Idx is the array index of segment level; -1 when that segment is a key.
func (t *Transformer) Idx(level int) int {
	if level < 0 || level >= len(t.path) {
		return -1
	}
	return t.path[level].i
}

// Last is the key of the last segment.
func (t *Transformer) Last() string { return t.Key(len(t.path) - 1) }

// PathString is for debugging: "messages[1].content".
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

// KeyRaw is the raw bytes of the current key (leading whitespace, quotes, colon and the whitespace around it). Protocols use it to keep the original formatting.
func (t *Transformer) KeyRaw() []byte { return t.kvRaw }

// Release asks to replay the Defer items of the current dispatch frame. The replay happens after the current callback returns, at
// the next safe point of the same frame (the end of the current value or the close of the frame); if the callback returned Enter into a child frame, the replay waits until this frame is back.
func (t *Transformer) Release() {
	t.wantRelease = true
	t.releaseAt = len(t.frames) - 1
}

// ReleaseNow replays the Defer items of the current dispatch frame synchronously. Only valid inside OnLeave, where the path still
// points at the container itself and the replayed keys attach below it correctly; inside OnValue use Release.
func (t *Transformer) ReleaseNow() { t.doRelease() }

// Deferred returns the Defer items of the current dispatch frame.
func (t *Transformer) Deferred() []DeferredKV {
	if f := t.top(); f != nil {
		return f.deferred
	}
	return nil
}

// DropDeferred discards the Defer items of the current dispatch frame.
func (t *Transformer) DropDeferred() {
	if f := t.top(); f != nil {
		t.forgetDeferred(f)
		f.deferred = nil
	}
}

// forgetDeferred subtracts the bytes of a frame's Defer items from the count (on replay or discard).
func (t *Transformer) forgetDeferred(f *frame) {
	for _, d := range f.deferred {
		t.deferredBytes -= len(d.KeyRaw) + len(d.Raw)
	}
}

// ---- scanner ----

// pushFrame pushes a dispatch frame, reusing the seen / deferred storage left in the slot instead of allocating per frame.
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
