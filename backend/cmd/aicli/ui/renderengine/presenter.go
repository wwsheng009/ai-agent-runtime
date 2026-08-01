package renderengine

import (
	"bytes"
	"io"
	"sync"
)

// Presenter owns the terminal write path.
//
// Each frame is assembled in memory and written to the target exactly once
// while holding the shared terminal write lock. The lock also supplies DEC
// 2026 synchronized-update framing when enabled by the live surface, so a
// repaint cannot be observed half way through.
//
// Write statistics are diagnostic counters used by frame-path acceptance
// tests: one Flush is one frame, and one non-empty frame performs one target
// Write regardless of how many writes the render callback requests.
type Presenter struct {
	mu          sync.Mutex
	flushes     uint64
	lastFrameWr int
	totalWrites uint64
	totalBytes  uint64
}

// NewPresenter creates a Presenter with zeroed statistics.
func NewPresenter() *Presenter {
	return &Presenter{}
}

// Flush runs render inside one terminal write batch. render receives an
// in-memory writer; Flush is safe to call from any goroutine and batches are
// serialized by the shared terminal write lock.
func (p *Presenter) Flush(w io.Writer, render func(w io.Writer)) error {
	if p == nil || w == nil || render == nil {
		return nil
	}
	var err error
	withTerminalWriteLock(func() {
		err = p.flushLocked(w, render)
	})
	return err
}

// FlushHoldingLock is Flush for callers that already hold the terminal write
// lock. The lock is non-reentrant, so callers MUST already be inside
// withTerminalWriteLock or another path that guarantees exclusive terminal
// access.
func (p *Presenter) FlushHoldingLock(w io.Writer, render func(w io.Writer)) error {
	if p == nil || w == nil || render == nil {
		return nil
	}
	return p.flushLocked(w, render)
}

// FlushHandoffHoldingLock emits one native-scrollback handoff as a presenter
// batch. Callers must already hold the shared terminal write lock, matching
// FlushHoldingLock's contract.
func (p *Presenter) FlushHandoffHoldingLock(w io.Writer, plan HandoffPlan) error {
	if p == nil || w == nil || plan.Empty() {
		return nil
	}
	return p.FlushHoldingLock(w, func(frame io.Writer) {
		_, _ = plan.WriteTo(frame)
	})
}

// flushLocked assembles one frame and performs at most one target Write. The
// caller must hold terminalWriteMu when using this helper.
func (p *Presenter) flushLocked(w io.Writer, render func(w io.Writer)) error {
	var frame bytes.Buffer
	render(&frame)
	if frame.Len() == 0 {
		p.recordFrame(0, 0)
		return nil
	}
	n, err := w.Write(frame.Bytes())
	// Retrying a short write would produce a second syscall and violate the
	// one-frame/one-write contract. The caller owns any error policy.
	if n < 0 {
		n = 0
	}
	if n > frame.Len() {
		n = frame.Len()
	}
	p.recordFrame(1, n)
	if err != nil {
		return err
	}
	if n != frame.Len() {
		return io.ErrShortWrite
	}
	return nil
}

func (p *Presenter) recordFrame(writes, bytes int) {
	p.mu.Lock()
	p.flushes++
	p.lastFrameWr = writes
	p.totalWrites += uint64(writes)
	p.totalBytes += uint64(bytes)
	p.mu.Unlock()
}

// FlushCount reports the total number of Flush batches issued.
func (p *Presenter) FlushCount() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.flushes
}

// LastFrameWriteCount reports the number of target Write calls in the most
// recent Flush batch (0 before the first Flush or for an empty frame).
func (p *Presenter) LastFrameWriteCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastFrameWr
}

// TotalWriteCount reports the cumulative number of target Write calls.
func (p *Presenter) TotalWriteCount() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.totalWrites
}

// TotalBytes reports the cumulative number of bytes accepted by target Write.
func (p *Presenter) TotalBytes() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.totalBytes
}
