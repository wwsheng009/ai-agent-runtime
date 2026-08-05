package ui

import (
	"errors"
	"sync"
)

// ErrTerminalTransactionMissingResult guards the boundary between a claimed
// reducer effect and the physical session. A transaction with a claimed token
// must return a typed history result; treating a missing result as Deferred
// would risk an untracked terminal write.
var ErrTerminalTransactionMissingResult = errors.New("terminal transaction omitted claimed history result")

// TerminalSessionExecutor is the bounded physical worker used by
// TerminalSessionPresenter. It claims one reducer-owned history token, derives
// one immutable AppState frame and commits the combined transaction. It never
// reads FixedBottomSurface state or accepts runtime callbacks directly; the
// presenter is its only production lifecycle/effect boundary.
//
// The executor is actor-safe: it claims one reducer-owned token, reads the
// resulting immutable AppState, performs one TerminalTransactionPlan write
// outside the actor, then posts the typed outcome. It also presents frame-only
// recovery transactions when the history queue is Unknown.
type TerminalSessionExecutor struct {
	controller *UIController
	session    *TerminalSession

	mu        sync.Mutex
	running   bool
	requested bool
	closed    bool
	wg        sync.WaitGroup
}

func NewTerminalSessionExecutor(controller *UIController, session *TerminalSession) *TerminalSessionExecutor {
	return &TerminalSessionExecutor{controller: controller, session: session}
}

// HandleEffect is the presenter-side effect adapter. It accepts only
// render/history wake intents and coalesces them into one worker; it does not
// inspect legacy surface state or write terminal bytes from the actor callback.
// TerminalSessionPresenter registers it only after the legacy surface writer
// has been fenced.
func (e *TerminalSessionExecutor) HandleEffect(effect Effect) {
	if e == nil || effect == nil {
		return
	}
	switch effect.(type) {
	case FlushEffect, HistoryCommitWakeEffect:
		e.Request()
	}
}

// Request coalesces a current-frame presentation request. It is safe from an
// effect callback because terminal work starts only after the active reducer
// action has completed.
func (e *TerminalSessionExecutor) Request() {
	if e == nil || e.controller == nil || e.session == nil {
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

// Close prevents new work and waits for the already-running worker. As with
// HistoryCommitExecutor, callers must not invoke it from a controller effect
// callback because that callback can be waiting for this worker's result post.
func (e *TerminalSessionExecutor) Close() {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()
	e.wg.Wait()
}

// WaitIdle is a deterministic test and controlled-shutdown helper.
func (e *TerminalSessionExecutor) WaitIdle() {
	if e != nil {
		e.wg.Wait()
	}
}

func (e *TerminalSessionExecutor) run() {
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

// runOne returns true only when one history token was acknowledged. That lets
// the next oldest token run after actor publication while a frame-only repaint,
// Deferred handoff, or any error waits for a later explicit wake instead of
// spinning on the terminal.
func (e *TerminalSessionExecutor) runOne() bool {
	if e == nil || e.controller == nil || e.session == nil {
		return false
	}
	e.controller.WaitIdle()
	state := e.controller.State()
	var claimed *HistoryCommit
	if pending := state.HistoryEffects.Pending(); len(pending) > 0 {
		candidate := pending[0]
		if !e.controller.Post(BeginHistoryCommit{
			Token: candidate.Token, LayoutGeneration: candidate.LayoutGeneration,
		}) {
			return false
		}
		e.controller.WaitIdle()
		state = e.controller.State()
		commit, ok := terminalSessionClaimedCommit(state, candidate.Token)
		if !ok {
			return false
		}
		claimed = &commit
	}

	plan := ComposeTerminalTransactionPlan(state.AppState, claimed)
	result := e.session.FlushTransaction(plan)
	return e.publishResult(plan.Frame.LayoutGeneration, claimed, result)
}

func terminalSessionClaimedCommit(state UIControllerState, token uint64) (HistoryCommit, bool) {
	for _, entry := range state.HistoryEffects.Entries() {
		if entry.Commit.Token == token && entry.State == HistoryCommitInFlight &&
			entry.Commit.LayoutGeneration == state.LayoutGeneration {
			return entry.Commit.Clone(), true
		}
	}
	return HistoryCommit{}, false
}

func (e *TerminalSessionExecutor) publishResult(generation uint64, claimed *HistoryCommit, result TerminalTransactionResult) bool {
	if e == nil || e.controller == nil {
		return false
	}
	historyAcknowledged := false
	if claimed != nil {
		history := result.History
		if history == nil {
			history = &HistoryCommitResult{Err: ErrTerminalTransactionMissingResult, MayHavePartiallyWritten: true}
		}
		switch {
		case history.Deferred && history.Err == nil && !history.MayHavePartiallyWritten:
			_ = e.controller.Post(HistoryCommitDeferred{Token: claimed.Token, LayoutGeneration: claimed.LayoutGeneration})
		case history.MayHavePartiallyWritten && history.Err == nil:
			_ = e.controller.Post(HistoryCommitFailed{
				Token: claimed.Token, LayoutGeneration: claimed.LayoutGeneration,
				Err: ErrHistoryCommitPartialWriteWithoutError, MayHavePartiallyWritten: true,
			})
		case history.Err != nil:
			_ = e.controller.Post(HistoryCommitFailed{
				Token: claimed.Token, LayoutGeneration: claimed.LayoutGeneration,
				Err: history.Err, MayHavePartiallyWritten: history.MayHavePartiallyWritten,
			})
		default:
			if e.controller.Post(HistoryCommitAcknowledged{
				Token: claimed.Token, Frame: history.Frame, LayoutGeneration: claimed.LayoutGeneration,
			}) {
				historyAcknowledged = true
			}
		}
	}

	if result.Frame.Err != nil {
		_ = e.controller.Post(HistoryProjectionInvalidated{LayoutGeneration: generation})
		e.controller.WaitIdle()
		return false
	}
	if result.Frame.Deferred {
		e.controller.WaitIdle()
		return false
	}
	if result.Frame.FullRepaint {
		_ = e.controller.Post(HistoryProjectionRecovered{LayoutGeneration: generation})
	}
	e.controller.WaitIdle()
	if !historyAcknowledged || claimed == nil {
		return false
	}
	state := e.controller.State()
	return historyCommitAcked(state, claimed.Token) && len(state.HistoryEffects.Pending()) > 0
}
