package ui

import (
	"errors"
	"fmt"
	"sync"
)

var (
	ErrHistoryCommitPartialWriteWithoutError = errors.New("history commit reported a possible partial write without an error")
	ErrHistoryCommitSinkPanic                = errors.New("history commit sink panicked")
)

// HistoryCommitResult is the terminal-side outcome of one claimed history
// effect. Deferred is reserved for a transaction that proved it emitted no
// bytes; a writer error or short write must instead set Err so the reducer
// enters projection recovery.
type HistoryCommitResult struct {
	Frame                   uint64
	Err                     error
	MayHavePartiallyWritten bool
	Deferred                bool
	// Delivered is populated only by a single bootstrap transaction that wrote
	// several pending ranges in order. The UI reducer validates and advances
	// the batch atomically; TerminalSession never retains an effect ledger.
	Delivered []HistoryCommit
}

// HistoryCommitSink is the terminal-effect boundary used by the primary
// presenter. Implementations own the terminal transaction and must verify
// primary ownership/generation again immediately before writing. It receives
// an immutable effect snapshot and must never inspect historyWindow, a front
// buffer, or native scrollback as semantic input.
type HistoryCommitSink interface {
	CommitHistory(HistoryCommit) HistoryCommitResult
}

// HistoryCommitExecutor serializes terminal HistoryCommit delivery outside
// the UI actor. The actor remains the only AppState writer: this worker first
// posts BeginHistoryCommit, confirms that the reducer accepted the claim, then
// invokes exactly one terminal sink transaction and posts a typed result.
//
// It deliberately does not implement a FixedBottomSurface adapter. The legacy
// surface still writes its own historyWindow handoff, and connecting both
// would double-write native scrollback. The final primary presenter will own
// this sink when it replaces that legacy path in one cutover.
type HistoryCommitExecutor struct {
	controller *UIController
	sink       HistoryCommitSink

	mu        sync.Mutex
	running   bool
	requested bool
	closed    bool
	done      chan struct{}
}

func NewHistoryCommitExecutor(controller *UIController, sink HistoryCommitSink) *HistoryCommitExecutor {
	return &HistoryCommitExecutor{controller: controller, sink: sink}
}

// Request coalesces a presenter wake-up. It is safe to call synchronously from
// UIController's effect callback because all blocking actor waits happen in a
// dedicated worker goroutine after the current action has completed.
func (e *HistoryCommitExecutor) Request() {
	if e == nil || e.controller == nil || e.sink == nil {
		return
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.requested = true
	if e.running {
		e.mu.Unlock()
		return
	}
	e.running = true
	e.done = make(chan struct{})
	done := e.done
	e.mu.Unlock()
	go e.run(done)
}

// Close prevents new terminal work and waits for a worker that has already
// started. The caller must not invoke it from the UIController effect callback.
func (e *HistoryCommitExecutor) Close() {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.closed = true
	done := e.done
	e.mu.Unlock()
	if done != nil {
		<-done
	}
}

// WaitIdle is a deterministic test/controlled-teardown helper. It is not
// intended as a normal UI producer synchronization primitive.
func (e *HistoryCommitExecutor) WaitIdle() {
	if e != nil {
		e.waitWorkerIdle()
	}
}

// waitWorkerIdle mirrors TerminalSessionExecutor: the done channel is
// published and cleared under e.mu, so waiting on the current generation can
// never race a concurrent Add the way a sync.WaitGroup would.
func (e *HistoryCommitExecutor) waitWorkerIdle() {
	for {
		e.mu.Lock()
		done := e.done
		running := e.running
		e.mu.Unlock()
		if !running || done == nil {
			return
		}
		<-done
	}
}

func (e *HistoryCommitExecutor) finishWorker(done chan struct{}) {
	e.running = false
	if done != nil {
		e.done = nil
		close(done)
	}
}

func (e *HistoryCommitExecutor) run(done chan struct{}) {
	for {
		e.mu.Lock()
		if e.closed {
			e.finishWorker(done)
			e.mu.Unlock()
			return
		}
		e.requested = false
		e.mu.Unlock()

		if !e.runOne() {
			e.mu.Lock()
			if e.closed || !e.requested {
				e.finishWorker(done)
				e.mu.Unlock()
				return
			}
			e.mu.Unlock()
		}
	}
}

// runOne returns true only after a successful Ack, allowing the next oldest
// token to be consumed in order. Failure, freeze, Unknown, and a no-write
// defer intentionally stop the drain until a later reducer transition wakes
// the presenter again.
func (e *HistoryCommitExecutor) runOne() bool {
	if e == nil || e.controller == nil || e.sink == nil {
		return false
	}
	// A Request may be issued from an effect callback before the actor has
	// cleared inFlight. Waiting here keeps selection causally after the state
	// that caused the wake-up without ever blocking that actor goroutine.
	e.controller.WaitIdle()
	// The claim and its two confirmation reads are payload-free projections:
	// State() would detach every commit's render lines, and because the drain
	// calls runOne once per commit, that made a full drain quadratic in ledger
	// size.
	commit, ok := e.controller.PendingHistoryCommit()
	if !ok {
		return false
	}
	if !e.controller.Post(BeginHistoryCommit{
		Token:            commit.Token,
		LayoutGeneration: commit.LayoutGeneration,
	}) {
		return false
	}
	e.controller.WaitIdle()
	gate := e.controller.historyCommitGateOf(commit.Token)
	if gate.EntryFound && gate.WriteCursor == commit.Token && gate.EntryInvalidationPending {
		// The claim was invalidated before the payload crossed the writer:
		// release it with the zero-write proof instead of writing stale bytes.
		// The reducer resolves the pending invalidation as clean (no recovery).
		_ = e.controller.Post(HistoryCommitDeferred{
			Token:            commit.Token,
			LayoutGeneration: commit.LayoutGeneration,
		})
		e.controller.WaitIdle()
		return false
	}
	if !historyCommitClaimCurrent(gate, commit) {
		return false
	}

	result := e.commitHistory(commit)
	if result.Deferred && result.Err == nil && !result.MayHavePartiallyWritten {
		_ = e.controller.Post(HistoryCommitDeferred{
			Token:            commit.Token,
			LayoutGeneration: commit.LayoutGeneration,
		})
		e.controller.WaitIdle()
		return false
	}
	if result.MayHavePartiallyWritten && result.Err == nil {
		// A possible terminal prefix must never be acknowledged merely because
		// an adapter forgot to attach its writer error. The reducer's failure
		// path invalidates projection and prevents blind replay.
		result.Err = ErrHistoryCommitPartialWriteWithoutError
	}
	if result.Err != nil {
		_ = e.controller.Post(HistoryCommitFailed{
			Token:                   commit.Token,
			LayoutGeneration:        commit.LayoutGeneration,
			Err:                     result.Err,
			MayHavePartiallyWritten: result.MayHavePartiallyWritten,
		})
		e.controller.WaitIdle()
		return false
	}
	if !e.controller.Post(HistoryCommitAcknowledged{
		Token:            commit.Token,
		Frame:            result.Frame,
		LayoutGeneration: commit.LayoutGeneration,
	}) {
		return false
	}
	e.controller.WaitIdle()
	return historyCommitAcked(e.controller.historyCommitGateOf(commit.Token))
}

// commitHistory converts a terminal-side panic into the same conservative
// failure path as a short write. A sink can panic after it has emitted bytes,
// so recovery must assume an unknown physical projection rather than leaving
// the token permanently claimed and the executor goroutine dead.
func (e *HistoryCommitExecutor) commitHistory(commit HistoryCommit) (result HistoryCommitResult) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result = HistoryCommitResult{
				Err:                     fmt.Errorf("%w: %v", ErrHistoryCommitSinkPanic, recovered),
				MayHavePartiallyWritten: true,
			}
		}
	}()
	return e.sink.CommitHistory(commit)
}

// historyCommitClaimCurrent accepts the payload-free gate projection of the
// claimed token rather than a full state clone; see UIController.historyCommitGateOf.
func historyCommitClaimCurrent(gate historyCommitGate, commit HistoryCommit) bool {
	if gate.Frozen || gate.ProjectionUnknown ||
		gate.LayoutGeneration != commit.LayoutGeneration {
		return false
	}
	return gate.EntryFound && gate.EntryState == HistoryCommitQueued &&
		gate.WriteCursor == commit.Token &&
		gate.EntryGeneration == commit.LayoutGeneration
}

func historyCommitAcked(gate historyCommitGate) bool {
	return gate.EntryFound && gate.EntryState == HistoryCommitDelivered
}
