package ui

import (
	"errors"
	"sync"
	"time"
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
	done      chan struct{}
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
	e.done = make(chan struct{})
	done := e.done
	e.wg.Add(1)
	e.mu.Unlock()
	go e.run(done)
}

// Close prevents new work and waits for the already-running worker. As with
// HistoryCommitExecutor, callers must not invoke it from a controller effect
// callback because that callback can be waiting for this worker's result post.
func (e *TerminalSessionExecutor) Close() {
	_ = e.CloseTimeout(0)
}

// CloseTimeout is the bounded form of Close. A false return means the worker
// still holds the physical writer; callers may abort the writer and wait once
// more before treating the session as abandoned.
func (e *TerminalSessionExecutor) CloseTimeout(timeout time.Duration) bool {
	if e == nil {
		return true
	}
	e.mu.Lock()
	e.closed = true
	done := e.done
	e.mu.Unlock()
	if done == nil {
		return true
	}
	if timeout <= 0 {
		e.wg.Wait()
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// WaitIdle is a deterministic test and controlled-shutdown helper.
func (e *TerminalSessionExecutor) WaitIdle() {
	if e != nil {
		e.wg.Wait()
	}
}

func (e *TerminalSessionExecutor) run(done chan struct{}) {
	defer e.wg.Done()
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

// finishWorker publishes running=false and closes the per-worker done channel
// in the same critical section. Request() after this point always observes a
// completed worker, so it can never reuse a channel that the exiting worker
// is still about to close.
func (e *TerminalSessionExecutor) finishWorker(done chan struct{}) {
	e.running = false
	if done != nil {
		e.done = nil
		close(done)
	}
}

// runOne returns true when reducer publication exposes immediate ordered work:
// either one history token was acknowledged or a successful scrollback reset
// replanned the canonical transcript under a fresh terminal epoch. Frame-only
// repaint, Deferred handoff, and errors otherwise stop the worker and wait for
// an explicit Request so a failing writer cannot turn the executor into a
// busy loop.
func (e *TerminalSessionExecutor) runOne() bool {
	if e == nil || e.controller == nil || e.session == nil {
		return false
	}
	e.controller.WaitIdle()
	schedule := e.controller.terminalSessionSchedule()
	if schedule.recoveryActionable {
		snapshot := e.controller.terminalSessionSnapshot(0)
		if !terminalSessionSnapshotRecoveryActionable(snapshot) {
			// The schedule changed after the first scalar read. Run one fresh
			// pass instead of relying on a possibly coalesced wake to flush it.
			latest := e.controller.terminalSessionSchedule()
			return latest.recoveryActionable || latest.pendingToken != 0 ||
				e.controller.terminalSessionHasActionableWork()
		}
		// A possibly written history range cannot be repaired by repainting only
		// the bottom viewport. Replace scrollback from semantic source first;
		// zero-byte viewport failures still use the cheaper repaint-only path.
		plan := composeTerminalViewportTransactionPlan(snapshot.appState, nil)
		if snapshot.reconciliationRequired {
			plan = composeTerminalViewportScrollbackReconciliationPlan(snapshot.appState)
		}
		result := e.session.FlushTransaction(plan)
		return e.publishResult(plan.Frame.LayoutGeneration, nil, result)
	}
	claimedToken := uint64(0)
	if schedule.pendingToken != 0 {
		if !e.controller.Post(BeginHistoryCommit{
			Token: schedule.pendingToken, LayoutGeneration: schedule.pendingGeneration,
		}) {
			return false
		}
		e.controller.WaitIdle()
		claimedToken = schedule.pendingToken
	}

	snapshot := e.controller.terminalSessionSnapshot(claimedToken)
	if claimedToken == 0 && terminalSessionSnapshotRecoveryActionable(snapshot) {
		plan := composeTerminalViewportTransactionPlan(snapshot.appState, nil)
		if snapshot.reconciliationRequired {
			plan = composeTerminalViewportScrollbackReconciliationPlan(snapshot.appState)
		}
		result := e.session.FlushTransaction(plan)
		return e.publishResult(plan.Frame.LayoutGeneration, nil, result)
	}
	if claimedToken != 0 && snapshot.claimed == nil {
		// BeginHistoryCommit is queued behind any reducer actions that raced the
		// scalar schedule read. If one of those actions replaced the candidate or
		// invalidated the projection, consume that actionable state immediately:
		// its wake may already have been coalesced into this running worker. A
		// frozen/leased queue intentionally exposes no pending token, while the
		// unchanged token can mean an older in-flight ordering fence; neither case
		// should spin the executor.
		latest := e.controller.terminalSessionSchedule()
		return terminalSessionClaimMissRequiresRetry(schedule, latest) ||
			e.controller.terminalSessionHasActionableWork()
	}
	plan := composeTerminalViewportTransactionPlan(snapshot.appState, snapshot.claimed, snapshot.bootstrap)
	result := e.session.FlushTransaction(plan)
	return e.publishResult(plan.Frame.LayoutGeneration, snapshot.claimed, result)
}

func terminalSessionClaimMissRequiresRetry(claimed, latest terminalSessionScheduleSnapshot) bool {
	if latest.recoveryActionable {
		return true
	}
	return latest.pendingToken != 0 &&
		(latest.pendingToken != claimed.pendingToken ||
			latest.pendingGeneration != claimed.pendingGeneration)
}

func terminalSessionSnapshotRecoveryActionable(snapshot terminalSessionControllerSnapshot) bool {
	return !snapshot.appState.Lease.Active && !snapshot.appState.HistoryEffects.Frozen &&
		(snapshot.projectionUnknown || snapshot.reconciliationRequired)
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
		return e.controller.terminalSessionHasActionableWork()
	}
	if historyAcknowledged && claimed != nil && e.controller.terminalSessionCommitAckedAndHasPending(claimed.Token) {
		return true
	}
	return e.controller.terminalSessionHasActionableWork()
}
