package ui

import (
	"errors"
	"sync"
	"time"
)

// ExecutorRecoveryDiagEntry captures one recovery-branch iteration of the
// executor. It is exported so the pprof diagnostics endpoint (and e2e drivers)
// can observe the recovery loop's per-iteration state — the exact signal that
// was previously invisible to CPU/goroutine profiles.
type ExecutorRecoveryDiagEntry struct {
	Seq                uint64 `json:"seq"`
	Branch             string `json:"branch"` // "scheduled" | "snapshot"
	Revision           uint64 `json:"revision"`
	RevisionAfter      uint64 `json:"revisionAfter"`
	Generation         uint64 `json:"generation"`
	TerminalEpoch      uint64 `json:"terminalEpoch"`
	ProjectionUnknown  bool   `json:"projectionUnknown"`
	ReconciliationReq  bool   `json:"reconciliationRequired"`
	BackoffEngaged     bool   `json:"backoffEngaged"`
	FullRepaint        bool   `json:"fullRepaint"`
	ScrollbackReset    bool   `json:"scrollbackReset"`
	FrameErr           string `json:"frameErr"`
	ObligationPending  bool   `json:"obligationPending"`
	ArmedBackoff       bool   `json:"armedBackoff"`
	Continued          bool   `json:"continued"`
}

// ExecutorRecoveryDiag is the JSON-exportable recovery-loop diagnostic of one
// TerminalSessionExecutor. It is a bounded ring buffer: at most the most recent
// diagRecoveryRingSize iterations are retained.
type ExecutorRecoveryDiag struct {
	Entries         []ExecutorRecoveryDiagEntry `json:"entries"`
	BackoffEngaged  uint64                      `json:"backoffEngaged"`
	ArmedBackoff    uint64                      `json:"armedBackoff"`
	TotalRecoveries uint64                      `json:"totalRecoveries"`
}

const diagRecoveryRingSize = 256

// ErrTerminalTransactionMissingResult guards the boundary between a claimed
// reducer effect and the physical session. A transaction with a claimed token
// must return a typed history result; treating a missing result as Deferred
// would risk an untracked terminal write.
var ErrTerminalTransactionMissingResult = errors.New("terminal transaction omitted claimed history result")

// terminalScrollbackResetBackoff bounds how frequently the executor may re-enter
// a full scrollback reset + replay after a failed or dropped reconciliation.
// A persistently failing physical writer would otherwise spin on
// reset -> replay-all -> fail -> reset forever; production pprof observed this
// as unbounded history replay plus GC pressure. The backoff yields the worker
// so the next explicit Request (from a real state change) retries instead of
// the executor burning CPU in a tight recovery loop.
//
// terminalScrollbackResetBackoffYield is the per-engaged-check sleep. It must
// be well below the window above so a failed-mode backoff does not consume the
// window while the worker is yielding (a Request that lands inside the window
// must still be rate-limited).
const (
	terminalScrollbackResetBackoff      = 100 * time.Millisecond
	terminalScrollbackResetBackoffYield = 10 * time.Millisecond
)

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

	diagMu   sync.Mutex
	diagSeq  uint64
	diagRing []ExecutorRecoveryDiagEntry
	// diagBackoffEngaged / diagArmedBackoff are monotonic counters exposed via
	// RecoveryDiag so e2e drivers can quantify how often the backoff engaged
	// and how often the executor armed it after a non-converging recovery.
	diagBackoffEngaged uint64
	diagArmedBackoff   uint64

	// lastResetAt / lastResetEpoch are the scrollback recovery progress guard.
	// A reconciliation that did not converge must not be re-armed on the next
	// worker cycle; these fields rate-limit full scrollback resets so a failing
	// writer cannot turn the executor into an unbounded reset+replay loop.
	lastResetAt    time.Time
	lastResetEpoch uint64
	// lastResetGeneration is the controller LayoutGeneration recorded AFTER the
	// executor published its own transaction outcome for the last scrollback
	// reset attempt. The backoff only engages when the layout generation has
	// NOT advanced since that settled generation (i.e., a non-progressing
	// loop). LayoutGeneration — not Revision — is the progress signal: a
	// transcript replay / streaming resume posts hundreds of actions per cycle
	// (advancing Revision ~240/cycle) that re-arm ProjectionUnknown and
	// ReconciliationRequired, so a revision-based guard can never match and the
	// executor busy-loops at ~2 cores. Only a real geometry/theme change
	// (Resize, SetThemeContextAction) advances LayoutGeneration and must run
	// the next recovery immediately.
	lastResetGeneration uint64
	// lastResetFailed distinguishes the two recovery-failure modes:
	//   - true:  the physical writer failed (Frame.Err). The writer may heal,
	//     so the guard is a bounded rate-limit window and a later retry is
	//     allowed after the window expires.
	//   - false: the flush succeeded but did NOT converge (ProjectionUnknown /
	//     ReconciliationRequired still pending, or a scrollback reset whose
	//     generation did not advance). Transcript replay re-arms the
	//     obligation every cycle (~238 actions/cycle, Revision +240) without
	//     ever advancing LayoutGeneration, so the guard must persist until a
	//     real geometry/theme change; a time window would expire before the
	//     next schedule read (cycle ~500ms >> 100ms window) and the executor
	//     would busy-loop at ~2 cores (observed: 439 arms, 0 engages).
	lastResetFailed bool
}

// NewTerminalSessionExecutor creates the bounded physical worker.
func NewTerminalSessionExecutor(controller *UIController, session *TerminalSession) *TerminalSessionExecutor {
	return &TerminalSessionExecutor{
		controller: controller,
		session:    session,
		diagRing:   make([]ExecutorRecoveryDiagEntry, 0, diagRecoveryRingSize),
	}
}

// RecoveryDiag returns a snapshot of the executor's recovery-loop ring buffer.
// It is safe to call from any goroutine (e.g. the pprof HTTP handler).
func (e *TerminalSessionExecutor) RecoveryDiag() ExecutorRecoveryDiag {
	if e == nil {
		return ExecutorRecoveryDiag{}
	}
	e.diagMu.Lock()
	defer e.diagMu.Unlock()
	entries := make([]ExecutorRecoveryDiagEntry, len(e.diagRing))
	copy(entries, e.diagRing)
	return ExecutorRecoveryDiag{
		Entries:         entries,
		BackoffEngaged:  e.diagBackoffEngaged,
		ArmedBackoff:    e.diagArmedBackoff,
		TotalRecoveries: e.diagSeq,
	}
}

// recordRecoveryDiag appends one recovery-branch iteration to the ring buffer.
func (e *TerminalSessionExecutor) recordRecoveryDiag(entry ExecutorRecoveryDiagEntry) {
	e.diagMu.Lock()
	defer e.diagMu.Unlock()
	e.diagSeq++
	entry.Seq = e.diagSeq
	if entry.BackoffEngaged {
		e.diagBackoffEngaged++
	}
	if entry.ArmedBackoff {
		e.diagArmedBackoff++
	}
	if len(e.diagRing) == diagRecoveryRingSize {
		copy(e.diagRing, e.diagRing[1:])
		e.diagRing[len(e.diagRing)-1] = entry
		return
	}
	e.diagRing = append(e.diagRing, entry)
}

// scrollbackResetBackoff reports whether the executor must yield before
// attempting another full scrollback reset. It engages when the controller
// layout generation has not advanced since the executor settled its own
// outcome posts for the last reset attempt — the signature of a
// non-progressing reset+replay loop. A real external geometry/theme change
// (new layout generation) always allows recovery.
//
// The engagement semantics depend on how the last reset failed:
//   - Writer failure (lastResetFailed=true): bounded rate-limit window. The
//     physical writer may heal, so after the window expires the same
//     generation may retry once.
//   - Success without convergence (lastResetFailed=false): NO wall-clock
//     window. A recovery cycle is dominated by WaitIdle draining the
//     transcript replay (~238 actions/cycle in production, ~500ms), so a time
//     window shorter than the cycle always expires before the next schedule
//     read and the guard never engages (observed: 439 arms, 0 engages,
//     executor pinned at ~2 cores). The generation discriminator alone is the
//     correct progress signal; the guard persists until LayoutGeneration
//     advances.
func (e *TerminalSessionExecutor) scrollbackResetBackoff(stateGeneration uint64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.lastResetAt.IsZero() || e.lastResetGeneration != stateGeneration {
		return false
	}
	if e.lastResetFailed {
		return time.Since(e.lastResetAt) < terminalScrollbackResetBackoff
	}
	return true
}

// recordScrollbackReset persists the progress-guard state after a scrollback
// reset attempt so the next worker cycle can rate-limit a non-converging
// recovery without blocking legitimate retries under a new generation.
// stateGeneration MUST be read after the executor has published its outcome
// and the actor has settled (see runOne): the settled generation is what the
// next cycle observes when no external geometry/theme change intervened.
func (e *TerminalSessionExecutor) recordScrollbackReset(epoch, stateGeneration uint64, failed bool) {
	e.mu.Lock()
	e.lastResetAt = time.Now()
	e.lastResetEpoch = epoch
	e.lastResetGeneration = stateGeneration
	e.lastResetFailed = failed
	e.mu.Unlock()
}

// armRecoveryBackoff decides whether to arm the scrollback-reset backoff guard
// after a recovery flush. It returns true when the guard was armed.
//
// Two distinct cases arm the guard:
//
//  1. FAILED flush (Frame.Err != nil): every failure posts
//     HistoryProjectionInvalidated, which advances the actor revision. We must
//     record the generation AFTER publishResult so the settled generation
//     matches the next no-progress cycle.
//
//  2. SUCCESSFUL flush that left the recovery obligation pending AND the layout
//     generation did NOT advance during the flush. A successful flush that posts
//     no outcome (viewport-only recovery that fails to prove FullRepaint or a
//     known projection) leaves the obligation in place with an unchanged
//     generation — the signature of a non-progressing reset+replay loop.
//     Recording startGeneration (which equals the settled generation here)
//     makes the next no-progress cycle match and the backoff yields the worker.
//
// A successful flush where the layout generation DID advance during the flush
// is genuine progress (e.g. a resize that raced the in-flight transaction) and
// must NOT arm the guard: the next pending generation recovery must run
// immediately.
//
//  3. SUCCESSFUL scrollback reset whose layout generation did NOT advance.
//     This is the reset+replay-loop signature the obligation check cannot see:
//     the executor's own HistoryProjectionRecovered / HistoryScrollbackReconciled
//     posts are reduced (WaitIdle) before this function runs, so
//     terminalHistoryRecoveryObligationPending() is already false even though
//     the reconcile handler replanned the entire transcript (memo misses on
//     every TerminalEpoch bump) and the next worker cycle re-enters recovery.
//     The successful flush genuinely reset the terminal (epoch advanced), but
//     the loop that follows is non-progressing; only a real geometry/theme
//     change (new layout generation) must run the next recovery immediately.
//
// NOTE: LayoutGeneration, not Revision, is the progress discriminator. A
// transcript replay / streaming resume posts hundreds of actions per executor
// cycle (observed ~240 Revision increments/cycle while LayoutGeneration stays
// constant), each re-arming ProjectionUnknown and ReconciliationRequired. A
// revision-based guard therefore never matches and the executor busy-loops at
// ~2 cores of CPU. Revision advance is NOT genuine progress; only a real
// geometry/theme change advances LayoutGeneration.
func (e *TerminalSessionExecutor) armRecoveryBackoff(result TerminalTransactionResult, startGeneration uint64) bool {
	if result.Frame.Err != nil {
		e.recordScrollbackReset(result.TerminalEpoch, e.controller.LayoutGeneration(), true)
		return true
	}
	if e.controller.terminalHistoryRecoveryObligationPending() && e.controller.LayoutGeneration() == startGeneration {
		e.recordScrollbackReset(result.TerminalEpoch, startGeneration, false)
		return true
	}
	if result.ScrollbackReset && e.controller.LayoutGeneration() == startGeneration {
		// Successful reset at an unchanged layout generation: the reconcile
		// handler replans the full transcript (TerminalEpoch memo miss) and the
		// next cycle is recoveryActionable again. Arm so the worker yields on
		// the next same-generation recovery instead of busy-looping at ~2 cores.
		e.recordScrollbackReset(result.TerminalEpoch, startGeneration, false)
		return true
	}
	return false
}

// frameErrString converts a frame error to a string for diag logging.
func frameErrString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
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
		// Scrollback-reset backoff: a failing writer must not turn the
		// executor into an unbounded reset+replay loop. If the last reset
		// happened within the backoff window AND the layout generation has not
		// advanced since then, yield the worker. A real geometry/theme change
		// (new layout generation) always allows recovery — the reset is a
		// genuine retry, not a loop.
		if e.scrollbackResetBackoff(schedule.stateGeneration) {
			e.recordRecoveryDiag(ExecutorRecoveryDiagEntry{
				Branch:         "scheduled",
				Revision:       schedule.stateRevision,
				BackoffEngaged: true,
			})
			// Yield the worker: with a persistent same-generation backoff, an
			// external replay that keeps re-arming the recovery obligation would
			// otherwise turn this into a tight Request() -> check -> false loop.
			// The sleep bounds that churn; only a real generation change breaks
			// the guard. Must stay well below terminalScrollbackResetBackoff
			// so a failed-mode window is not consumed during the yield itself.
			time.Sleep(terminalScrollbackResetBackoffYield)
			return false
		}

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
		reconciliation := snapshot.reconciliationRequired
		if reconciliation {
			plan = composeTerminalViewportScrollbackReconciliationPlan(snapshot.appState)
		}
		result := e.session.FlushTransaction(plan)
		continued := e.publishResult(plan.Frame.LayoutGeneration, nil, result)
		armed := e.armRecoveryBackoff(result, schedule.stateGeneration)
		e.recordRecoveryDiag(ExecutorRecoveryDiagEntry{
			Branch:            "scheduled",
			Revision:          schedule.stateRevision,
			RevisionAfter:     e.controller.Revision(),
			Generation:        plan.Frame.LayoutGeneration,
			TerminalEpoch:     result.TerminalEpoch,
			ProjectionUnknown: snapshot.projectionUnknown,
			ReconciliationReq: snapshot.reconciliationRequired,
			FullRepaint:       result.Frame.FullRepaint,
			ScrollbackReset:   result.ScrollbackReset,
			FrameErr:          frameErrString(result.Frame.Err),
			ObligationPending: e.controller.terminalHistoryRecoveryObligationPending(),
			ArmedBackoff:      armed,
			Continued:         continued,
		})
		return continued
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
		reconciliation := snapshot.reconciliationRequired
		if reconciliation {
			plan = composeTerminalViewportScrollbackReconciliationPlan(snapshot.appState)
		}
		result := e.session.FlushTransaction(plan)
		continued := e.publishResult(plan.Frame.LayoutGeneration, nil, result)
		armed := e.armRecoveryBackoff(result, schedule.stateGeneration)
		e.recordRecoveryDiag(ExecutorRecoveryDiagEntry{
			Branch:            "snapshot",
			Revision:          schedule.stateRevision,
			RevisionAfter:     e.controller.Revision(),
			Generation:        plan.Frame.LayoutGeneration,
			TerminalEpoch:     result.TerminalEpoch,
			ProjectionUnknown: snapshot.projectionUnknown,
			ReconciliationReq: snapshot.reconciliationRequired,
			FullRepaint:       result.Frame.FullRepaint,
			ScrollbackReset:   result.ScrollbackReset,
			FrameErr:          frameErrString(result.Frame.Err),
			ObligationPending: e.controller.terminalHistoryRecoveryObligationPending(),
			ArmedBackoff:      armed,
			Continued:         continued,
		})
		return continued
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
