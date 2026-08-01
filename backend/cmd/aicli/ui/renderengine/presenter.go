package renderengine

import (
	"io"
	"sync"
)

// Presenter owns the terminal write path (Stage A first version).
//
// It fixes the "one frame = one batch flush" discipline: every frame's ANSI
// output must be produced inside a single Flush call, wrapped in the
// existing terminal write lock and DEC 2026 synchronized-update framing so
// the emulator applies the whole repaint atomically. Later stages replace
// the closure body with ScreenModel diff output; the batching contract and
// the write statistics stay.
//
// Write statistics are diagnostic counters (flushes, per-frame Write calls,
// total bytes) that the Stage A acceptance criteria use to prove the frame
// path no longer emits line-by-line syscalls.
type Presenter struct {
	mu            sync.Mutex
	flushes       uint64
	lastFrameWr   int
	totalWrites   uint64
	totalBytes    uint64
}

// NewPresenter creates a Presenter with zeroed statistics.
func NewPresenter() *Presenter {
	return &Presenter{}
}

// Flush runs render inside one terminal write batch. render receives a
// counting writer so per-frame Write-call counts are tracked. Flush is safe
// to call from any goroutine; batches are serialized by the terminal write
// lock (the same lock every TUI write path already holds).
func (p *Presenter) Flush(w io.Writer, render func(w io.Writer)) {
	if render == nil {
		return
	}
	cw := &countingWriter{w: w}
	withTerminalWriteLock(func() {
		render(cw)
	})
	p.mu.Lock()
	p.flushes++
	p.lastFrameWr = cw.writes
	p.totalWrites += uint64(cw.writes)
	p.totalBytes += uint64(cw.bytes)
	p.mu.Unlock()
}

// FlushHoldingLock is Flush for callers that already hold the terminal write
// lock (the terminal write lock is non-reentrant, so nested Flush would
// deadlock). Callers MUST already be inside withTerminalWriteLock or on a
// path that guarantees exclusive write access. Statistics are identical to
// Flush.
func (p *Presenter) FlushHoldingLock(w io.Writer, render func(w io.Writer)) {
	if render == nil {
		return
	}
	cw := &countingWriter{w: w}
	render(cw)
	p.mu.Lock()
	p.flushes++
	p.lastFrameWr = cw.writes
	p.totalWrites += uint64(cw.writes)
	p.totalBytes += uint64(cw.bytes)
	p.mu.Unlock()
}

// FlushCount reports the total number of Flush batches issued.
func (p *Presenter) FlushCount() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.flushes
}

// LastFrameWriteCount reports the number of underlying Write calls in the
// most recent Flush batch (0 before the first Flush).
func (p *Presenter) LastFrameWriteCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastFrameWr
}

// TotalWriteCount reports the cumulative number of underlying Write calls.
func (p *Presenter) TotalWriteCount() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.totalWrites
}

// TotalBytes reports the cumulative number of bytes written through Flush.
func (p *Presenter) TotalBytes() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.totalBytes
}

// countingWriter counts Write calls and bytes.
type countingWriter struct {
	w      io.Writer
	writes int
	bytes  int
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.writes++
	c.bytes += n
	return n, err
}
