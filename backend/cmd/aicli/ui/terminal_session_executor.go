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

// runOne returns true when reducer publication exposes immediate ordered work:
// either one history token was acknowledged or a successful scrollback reset
// replanned the canonical transcript under a fresh terminal epoch. Frame-only
// repaint, Deferred handoff, and errors otherwise wait for an explicit wake.
func (e *TerminalSessionExecutor) runOne() bool {
	if e == nil || e.controller == nil || e.session == nil {
		return false
	}
	e.controller.WaitIdle()
	state := e.controller.State()
	if state.HistoryEffects.ProjectionUnknown {
		// Projection recovery is viewport-only. Do it before claiming another
		// irreversible history range; HistoryProjectionRecovered will publish a
		// fresh wake when ordered Pending work may proceed.
		plan := ComposeTerminalTransactionPlan(state.AppState, nil)
		result := e.session.FlushTransaction(plan)
		return e.publishResult(plan.Frame.LayoutGeneration, nil, result)
	}
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

	bootstrap := terminalSessionBootstrapCommits(state, claimed)
	plan := ComposeTerminalTransactionPlan(state.AppState, claimed, bootstrap)
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

// terminalSessionBootstrapCommits returns the current oldest contiguous
// pending suffix together with the claimed first token. TerminalSession can
// insert this ordered batch directly above the inline viewport in one write;
// the reducer still validates and acknowledges every token atomically.
func terminalSessionBootstrapCommits(state UIControllerState, claimed *HistoryCommit) []HistoryCommit {
	if claimed == nil {
		return nil
	}
	commits := make([]HistoryCommit, 0)
	for _, entry := range state.HistoryEffects.Entries() {
		if entry.Commit.LayoutGeneration != state.LayoutGeneration {
			continue
		}
		if entry.Commit.Token == claimed.Token && entry.State == HistoryCommitInFlight {
			commits = append(commits, entry.Commit.Clone())
			continue
		}
		if len(commits) > 0 && entry.State == HistoryCommitPending {
			commits = append(commits, entry.Commit.Clone())
		}
	}
	if len(commits) == 0 || commits[0].Token != claimed.Token {
		return nil
	}
	return commits
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
		case history.Err != nil && result.Frame.Err != nil && !history.MayHavePartiallyWritten:
			// The terminal transaction was attempted, but the writer proved that
			// zero bytes reached the host. Keep the same token retryable. The frame
			// error below invalidates the viewport cache; after a source-backed
			// recovery, HistoryProjectionRecovered wakes this Pending handoff.
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
			if len(history.Delivered) > 0 {
				historyAcknowledged = e.controller.Post(HistoryCommitsAcknowledged{
					Commits: history.Delivered, Frame: history.Frame, LayoutGeneration: claimed.LayoutGeneration,
				})
			} else if e.controller.Post(HistoryCommitAcknowledged{
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
	// A bottom-viewport repaint is not proof that the independently owned top
	// history projection recovered. Publish the reducer barrier only when the
	// terminal owner confirms both facts; partial history writes remain
	// fail-closed until an explicit scrollback reconciliation.
	if result.Frame.FullRepaint && e.session.ProjectionState().HistoryKnown {
		_ = e.controller.Post(HistoryProjectionRecovered{LayoutGeneration: generation})
	}
	if result.ScrollbackReset && result.TerminalEpoch != 0 {
		_ = e.controller.Post(HistoryScrollbackReconciled{
			LayoutGeneration: generation,
			TerminalEpoch:    result.TerminalEpoch,
		})
	}
	e.controller.WaitIdle()
	if result.ScrollbackReset {
		return e.controller.State().HistoryEffects.HasPending()
	}
	if !historyAcknowledged || claimed == nil {
		return false
	}
	state := e.controller.State()
	return historyCommitAcked(state, claimed.Token) && len(state.HistoryEffects.Pending()) > 0
}
