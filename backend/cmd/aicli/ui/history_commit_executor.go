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
	wg        sync.WaitGroup
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
	e.wg.Add(1)
	e.mu.Unlock()
	go e.run()
}

// Close prevents new terminal work and waits for a worker that has already
// started. The caller must not invoke it from the UIController effect callback.
func (e *HistoryCommitExecutor) Close() {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()
	e.wg.Wait()
}

// WaitIdle is a deterministic test/controlled-teardown helper. It is not
// intended as a normal UI producer synchronization primitive.
func (e *HistoryCommitExecutor) WaitIdle() {
	if e != nil {
		e.wg.Wait()
	}
}

func (e *HistoryCommitExecutor) run() {
	defer e.wg.Done()
	for {
		e.mu.Lock()
		if e.closed {
			e.running = false
			e.mu.Unlock()
			return
		}
		e.requested = false
		e.mu.Unlock()

		if !e.runOne() {
			e.mu.Lock()
			if e.closed || !e.requested {
				e.running = false
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
	state := e.controller.State()
	commits := state.HistoryEffects.Pending()
	if len(commits) == 0 {
		return false
	}
	commit := commits[0]
	if !e.controller.Post(BeginHistoryCommit{
		Token:            commit.Token,
		LayoutGeneration: commit.LayoutGeneration,
	}) {
		return false
	}
	e.controller.WaitIdle()
	if !historyCommitClaimCurrent(e.controller.State(), commit) {
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
	return historyCommitAcked(e.controller.State(), commit.Token)
}

// commitHistory converts a terminal-side panic into the same conservative
// failure path as a short write. A sink can panic after it has emitted bytes,
// so recovery must assume an unknown physical projection rather than leaving
// the token permanently InFlight and the executor goroutine dead.
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

func historyCommitClaimCurrent(state UIControllerState, commit HistoryCommit) bool {
	if state.HistoryEffects.Frozen || state.HistoryEffects.ProjectionUnknown ||
		state.LayoutGeneration != commit.LayoutGeneration {
		return false
	}
	for _, entry := range state.HistoryEffects.Entries() {
		if entry.Commit.Token == commit.Token {
			return entry.State == HistoryCommitInFlight &&
				entry.Commit.LayoutGeneration == commit.LayoutGeneration
		}
	}
	return false
}

func historyCommitAcked(state UIControllerState, token uint64) bool {
	for _, entry := range state.HistoryEffects.Entries() {
		if entry.Commit.Token == token {
			return entry.State == HistoryCommitAcked
		}
	}
	return false
}
