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
	Seq                 uint64 `json:"seq"`
	AtUnixMs            int64  `json:"atUnixMs"` // wall-clock stamp of the iteration
	Branch              string `json:"branch"`   // "scheduled" | "snapshot"
	Revision            uint64 `json:"revision"`
	RevisionAfter       uint64 `json:"revisionAfter"`
	Generation          uint64 `json:"generation"`
	TerminalEpoch       uint64 `json:"terminalEpoch"`
	ProjectionUnknown   bool   `json:"projectionUnknown"`
	ReconciliationReq   bool   `json:"reconciliationRequired"`
	BackoffEngaged      bool   `json:"backoffEngaged"`
	FlushedWhileBackoff bool   `json:"flushedWhileBackoff"`
	// HandoffWhileBackoff marks a cycle that, with the scrollback-reset backoff
	// engaged in success mode, still claimed and delivered pending history
	// commits (a normal handoff WITHOUT the scrollback reset). A growing count
	// proves the active band keeps committing new messages into the unified
	// renderer even while the recovery obligation is parked at an unchanged
	// layout generation.
	HandoffWhileBackoff bool   `json:"handoffWhileBackoff"`
	FullRepaint         bool   `json:"fullRepaint"`
	ScrollbackReset     bool   `json:"scrollbackReset"`
	FrameErr            string `json:"frameErr"`
	ObligationPending   bool   `json:"obligationPending"`
	ArmedBackoff        bool   `json:"armedBackoff"`
	Continued           bool   `json:"continued"`
}

// ExecutorRecoveryDiag is the JSON-exportable recovery-loop diagnostic of one
// TerminalSessionExecutor. It is a bounded ring buffer: at most the most recent
// diagRecoveryRingSize iterations are retained.
type ExecutorRecoveryDiag struct {
	Entries        []ExecutorRecoveryDiagEntry `json:"entries"`
	BackoffEngaged uint64                      `json:"backoffEngaged"`
	ArmedBackoff   uint64                      `json:"armedBackoff"`
	// FlushesWhileBackoff counts plain viewport flushes performed while the
	// scrollback-reset backoff was engaged. A growing count proves the bottom
	// surface (prompt input) is still being rendered under backoff — the guard
	// suppresses scrollback resets only, not live rendering.
	FlushesWhileBackoff uint64 `json:"flushesWhileBackoff"`
	// HandoffsWhileBackoff counts pending history commits delivered while the
	// scrollback-reset backoff was engaged (success mode). The backoff must
	// suppress the expensive reset+replay only — new message content still has
	// to cross into native scrollback, otherwise post-resume messages never
	// render.
	HandoffsWhileBackoff uint64 `json:"handoffsWhileBackoff"`
	// ClaimMissReleases counts claim-miss cycles that posted an explicit release
	// for a claimed token: an accepted claim is returned to Pending and rebased
	// onto the current layout generation, while a refused claim is a safe
	// reducer no-op. Each posted release prevents (or retries) the
	// stranded-InFlight deadlock; see TerminalSessionExecutor.releaseClaimMiss.
	ClaimMissReleases uint64 `json:"claimMissReleases"`
	TotalRecoveries   uint64 `json:"totalRecoveries"`
	// GeneratedAtUnixMs is when this snapshot was assembled; it lets a caller
	// compute whether the loop is still advancing between two polls.
	GeneratedAtUnixMs int64 `json:"generatedAtUnixMs"`
	// Diagnosis is a derived verdict that encodes the debugging lesson learned
	// from the reported replay loop: a backoff that is armed but never engaged
	// (ArmedBackoff>0, BackoffEngaged==0) is a dead guard, not a working one.
	// Single-counter reads are misleading; the verdict compares both.
	//
	// SCOPE: since start. It consumes the monotonic lifetime counters, so it is
	// a historical verdict: an executor that stormed an hour ago and converged
	// still reads "backoff_engaged" forever. Poll WindowDiagnosis for the
	// current state; keep Diagnosis for "what has this executor ever done".
	Diagnosis string `json:"diagnosis"` // "idle" | "healthy" | "backoff_engaged" | "backoff_engaged_handing_off" | "dead_guard"; since start
	// WindowDiagnosis is the CURRENT-state verdict over the retained window
	// only (the most recent executorDiagWindowEntries entries, none older than
	// executorDiagWindowDuration before the newest). It answers "is the
	// executor healthy right now?" without lifetime history contaminating the
	// answer. "idle" means no entry landed inside the window (a quiet
	// executor), which is distinct from "healthy" (recoveries are running and
	// making progress).
	WindowDiagnosis string `json:"windowDiagnosis"`
	// WindowEntries is how many retained entries fell inside the window the
	// verdict was computed over.
	WindowEntries int `json:"windowEntries"`
	// WindowSpanMs is the span covered by those entries (newest - oldest inside
	// the window); 0 when fewer than two entries landed in the window.
	WindowSpanMs int64 `json:"windowSpanMs"`
	// WindowAgeMs is how long ago the newest retained entry happened; -1 when
	// the ring is empty. A large value together with WindowDiagnosis=="idle" is
	// a deliberately quiet executor, not a blind spot.
	WindowAgeMs int64 `json:"windowAgeMs"`
	// WindowRecoveriesPerSec is the recovery rate over the retained window
	// (entries[0].AtUnixMs .. entries[last].AtUnixMs). A continuously growing
	// rate under an unchanged generation is the loop signature.
	WindowRecoveriesPerSec float64 `json:"windowRecoveriesPerSec"`
	// GenerationAdvancesInWindow is how many distinct layout generations were
	// observed across the retained entries. 1 with many recoveries means the
	// loop is re-arming at a frozen generation (no real progress).
	GenerationAdvancesInWindow int `json:"generationAdvancesInWindow"`
	// FrameErrorsInWindow counts how many retained iterations ended with a
	// physical writer error. Combined with the diagnosis it distinguishes a
	// dead-guard loop (no errors, armed but never engaged) from a genuinely
	// failing writer (errors on every iteration).
	FrameErrorsInWindow int `json:"frameErrorsInWindow"`
	// ScrollbackResetsInWindow counts how many retained iterations performed a
	// full scrollback reset+replay. A high count under one generation is the
	// visible replay-loop signature.
	ScrollbackResetsInWindow int `json:"scrollbackResetsInWindow"`
	// LastGeneration is the layout generation of the most recent entry.
	LastGeneration uint64 `json:"lastGeneration"`
}

const diagRecoveryRingSize = 256

// The WindowDiagnosis window: the most recent executorDiagWindowEntries
// iterations, further restricted to iterations no older than
// executorDiagWindowDuration before the newest one (and before the snapshot
// wall clock — a stale ring reports "idle", never a fossilized verdict). The
// two bounds are deliberately generous relative to one recovery cycle
// (~500ms in production): the window must cover a real burst, not a single
// cycle.
const (
	executorDiagWindowEntries  = 64
	executorDiagWindowDuration = 60 * time.Second
)

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
	// terminalScrollbackResetRetryWindow bounds how long a SUCCESS-mode
	// (flush-ok-but-non-converging) backoff stays engaged before the executor
	// is allowed one more same-generation scrollback reset. It must be
	// comfortably above one recovery cycle (WaitIdle drains the transcript
	// replay, ~500ms in production): a window below the cycle always expires
	// before the next schedule read and the guard never engages (the original
	// 100ms failure mode, 439 arms / 0 engages at ~2 cores). A window above
	// the cycle both rate-limits the loop AND guarantees a bounded retry, so
	// a reconciliation that was dropped because ProjectionUnknown was still
	// set when its first barrier arrived gets a second chance once the
	// projection has recovered.
	terminalScrollbackResetRetryWindow = 2 * time.Second
	// terminalScrollbackResetMaxRetries caps consecutive same-generation
	// success-mode resets. Once the budget is exhausted the guard parks until
	// a real geometry/theme change (new LayoutGeneration); a genuinely
	// non-converging obligation must not re-run the expensive reset+replay at
	// the retry cadence forever.
	terminalScrollbackResetMaxRetries = 3
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
	// lastControllerTicket is the ack ticket of the newest action this executor
	// posted and the controller accepted (admitted or merged). The runOne and
	// publishResult fences wait for it instead of polling the controller queue:
	// "my last post has been applied" is exactly the state fence those call
	// sites need, and it cannot be pinned by unrelated producers.
	lastControllerTicket uint64

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
	// diagFlushedWhileBackoff counts how many times the executor performed a
	// plain viewport flush while the scrollback-reset backoff was engaged.
	// A growing count proves prompt-input rendering stays live under backoff
	// (the fix for the post-resume frozen input area), distinct from the
	// suppressed scrollback resets.
	diagFlushedWhileBackoff uint64
	// diagHandoffWhileBackoff counts how many pending history commits were
	// delivered while the scrollback-reset backoff was engaged (success mode).
	// A growing count proves the active band keeps committing new messages to
	// the unified renderer even while recovery is parked.
	diagHandoffWhileBackoff uint64
	// diagClaimMissReleases counts claim-miss cycles that posted an explicit
	// HistoryCommitDeferred release (most often because a Resize/Theme drained
	// by the ticket fence advanced the layout generation between markInFlight
	// and the snapshot). Without the release, an accepted claim stays InFlight
	// forever: terminalSessionSchedule scans Pending only, and
	// hasOlderPendingOrInFlight rejects every later claim behind it, silently
	// deadlocking the whole handoff queue (live 2026-10-05: in-flight=1, oldest
	// pending frozen, delivery journal frozen, pending growing on an idle
	// session). A refused claim posts the same no-op release; the counter
	// therefore measures release attempts, not confirmed rebases.
	diagClaimMissReleases uint64

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
	//     obligation every cycle (~238 actions/cycle, Revision +240). The
	//     guard engages for a bounded retry window (terminalScrollbackResetRetryWindow)
	//     to rate-limit the loop while still allowing a bounded number of
	//     same-generation retries, so a reconciliation whose first barrier was
	//     dropped while ProjectionUnknown was set gets a second chance after
	//     the projection recovers. After terminalScrollbackResetMaxRetries
	//     consecutive non-converging attempts the guard parks until a real
	//     geometry/theme change.
	lastResetFailed bool
	// lastResetSuccessRetries counts consecutive non-converging same-generation
	// success-mode resets, consumed from the retry budget. Reset to 0 on
	// generation change (new LayoutGeneration) or on a writer-failure record.
	lastResetSuccessRetries int
	// now is the wall clock behind the reset-backoff windows above. It exists as
	// a seam because those windows are wall-clock gated: an end-to-end driver
	// that loops external wakes observes a host-speed-dependent number of
	// retries (each engaged wake yields for 10ms, so a loaded host expires the
	// 100ms failed-mode window mid-loop and the same test flips between 2 and 3
	// extra writes). Tests freeze it to assert the guard's retry count instead
	// of the scheduler's speed. Production leaves it nil and nowTime falls back
	// to time.Now; the field is guarded by mu and only read with mu held.
	now func() time.Time
}

// NewTerminalSessionExecutor creates the bounded physical worker.
func NewTerminalSessionExecutor(controller *UIController, session *TerminalSession) *TerminalSessionExecutor {
	return &TerminalSessionExecutor{
		controller: controller,
		session:    session,
		diagRing:   make([]ExecutorRecoveryDiagEntry, 0, diagRecoveryRingSize),
	}
}

// nowTime returns the executor wall clock. Callers MUST hold e.mu: every
// production read happens inside scrollbackResetBackoff / recordScrollbackReset,
// and setNowFunc writes the field under the same mutex.
func (e *TerminalSessionExecutor) nowTime() time.Time {
	if e.now != nil {
		return e.now()
	}
	return time.Now()
}

// setNowFunc overrides the wall clock used by the scrollback-reset backoff
// windows. Tests use it to keep the retry count deterministic instead of
// racing real time; nil restores time.Now. It must be called while the worker
// is idle (the mutex serializes the field, not the surrounding test logic).
func (e *TerminalSessionExecutor) setNowFunc(now func() time.Time) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.now = now
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
	nowUnixMs := time.Now().UnixMilli()
	d := ExecutorRecoveryDiag{
		Entries:                    entries,
		BackoffEngaged:             e.diagBackoffEngaged,
		ArmedBackoff:               e.diagArmedBackoff,
		FlushesWhileBackoff:        e.diagFlushedWhileBackoff,
		HandoffsWhileBackoff:       e.diagHandoffWhileBackoff,
		ClaimMissReleases:          e.diagClaimMissReleases,
		TotalRecoveries:            e.diagSeq,
		GeneratedAtUnixMs:          nowUnixMs,
		GenerationAdvancesInWindow: executorDiagGenerationAdvances(entries),
	}
	for _, en := range entries {
		if en.FrameErr != "" {
			d.FrameErrorsInWindow++
		}
		if en.ScrollbackReset {
			d.ScrollbackResetsInWindow++
		}
	}
	if len(entries) > 0 {
		d.LastGeneration = entries[len(entries)-1].Generation
		if dt := entries[len(entries)-1].AtUnixMs - entries[0].AtUnixMs; dt > 0 && len(entries) > 1 {
			d.WindowRecoveriesPerSec = float64(len(entries)-1) / (float64(dt) / 1000.0)
		}
	}
	d.Diagnosis = executorDiagDiagnosis(d)
	window := executorDiagWindowStateFor(entries, nowUnixMs)
	d.WindowDiagnosis = window.Diagnosis
	d.WindowEntries = window.Entries
	d.WindowSpanMs = window.SpanMs
	d.WindowAgeMs = window.AgeMs
	return d
}

// executorDiagWindowState is the CURRENT-state verdict plus the shape of the
// window it was computed over. Keeping the shape next to the verdict makes a
// windowed verdict self-explaining: "idle" with WindowAgeMs=4m is a quiet
// executor, "idle" with WindowAgeMs=0 is a blind spot worth investigating.
type executorDiagWindowState struct {
	Diagnosis string
	Entries   int
	SpanMs    int64
	AgeMs     int64
}

// executorDiagWindowStateFor computes the windowed verdict for a snapshot taken
// at nowUnixMs.
func executorDiagWindowStateFor(entries []ExecutorRecoveryDiagEntry, nowUnixMs int64) executorDiagWindowState {
	state := executorDiagWindowState{AgeMs: -1}
	if len(entries) == 0 {
		state.Diagnosis = executorDiagWindowDiagnosis(nil)
		return state
	}
	newest := entries[len(entries)-1].AtUnixMs
	age := nowUnixMs - newest
	if age < 0 {
		age = 0
	}
	state.AgeMs = age
	window := executorDiagWindow(entries, nowUnixMs)
	state.Entries = len(window)
	state.Diagnosis = executorDiagWindowDiagnosis(window)
	if n := len(window); n > 0 {
		state.SpanMs = window[n-1].AtUnixMs - window[0].AtUnixMs
	}
	return state
}

// executorDiagWindow selects the retained entries that back WindowDiagnosis:
// the most recent executorDiagWindowEntries iterations, further restricted to
// iterations no older than executorDiagWindowDuration relative to BOTH the
// newest entry and the snapshot wall clock. A ring whose newest iteration is
// older than a whole window is quiet — it yields nil (and therefore the "idle"
// verdict) rather than replaying a stale state as CURRENT.
func executorDiagWindow(entries []ExecutorRecoveryDiagEntry, nowUnixMs int64) []ExecutorRecoveryDiagEntry {
	if len(entries) == 0 {
		return nil
	}
	windowMs := int64(executorDiagWindowDuration / time.Millisecond)
	newest := entries[len(entries)-1].AtUnixMs
	if nowUnixMs-newest > windowMs {
		return nil
	}
	start := len(entries) - executorDiagWindowEntries
	if start < 0 {
		start = 0
	}
	cutoff := newest - windowMs
	for start < len(entries) && entries[start].AtUnixMs < cutoff {
		start++
	}
	return entries[start:]
}

// executorDiagWindowDiagnosis derives the current-state verdict from the
// retained window only. It mirrors executorDiagDiagnosis — same five verdicts,
// same production lesson (armed-but-never-engaged is a dead guard) — but
// consumes counters accumulated INSIDE the window, so history cannot
// masquerade as the present:
//
//	idle                          no entry landed inside the window
//	dead_guard                    armed at least once, never engaged, in-window
//	backoff_engaged_handing_off   engaged and still committing new messages
//	backoff_engaged               the newest in-window iteration is throttled
//	healthy                       recoveries are running and progressing
func executorDiagWindowDiagnosis(window []ExecutorRecoveryDiagEntry) string {
	if len(window) == 0 {
		return "idle"
	}
	var armed, engaged, handoffs int
	for _, en := range window {
		if en.ArmedBackoff {
			armed++
		}
		if en.BackoffEngaged {
			engaged++
		}
		if en.HandoffWhileBackoff {
			handoffs++
		}
	}
	switch {
	case armed > 0 && engaged == 0:
		return "dead_guard"
	case engaged > 0 && handoffs > 0:
		return "backoff_engaged_handing_off"
	case window[len(window)-1].BackoffEngaged:
		return "backoff_engaged"
	default:
		return "healthy"
	}
}

// executorDiagGenerationAdvances counts distinct layout generations observed in
// the retained window. A value of 1 across many entries means the recovery loop
// is re-arming at a frozen generation — the signature of a non-progressing loop
// (the executor's transcript replay advances Revision, never LayoutGeneration).
func executorDiagGenerationAdvances(entries []ExecutorRecoveryDiagEntry) int {
	if len(entries) == 0 {
		return 0
	}
	seen := map[uint64]struct{}{}
	for _, en := range entries {
		seen[en.Generation] = struct{}{}
	}
	return len(seen)
}

// executorDiagDiagnosis derives a loop-health verdict from the two counters that
// single-read diagnostics mislead on. This encodes the production finding:
// armedBackoff=439 with backoffEngaged=0 was NOT a working rate-limit — it was
// a guard that never fired. The verdict surfaces that state directly.
func executorDiagDiagnosis(d ExecutorRecoveryDiag) string {
	if d.TotalRecoveries == 0 {
		return "idle"
	}
	// armed without a single engage is the dead-guard signature regardless of
	// how many recoveries ran (the production bug: 439 armed, 0 engaged).
	if d.ArmedBackoff > 0 && d.BackoffEngaged == 0 {
		return "dead_guard"
	}
	// Backoff engaged AND content still flowing: the guard is live and the
	// pending-commit handoff is delivering new messages (the fix for
	// post-resume messages never rendering). This is a parked-but-progressing
	// state, not a stall.
	if d.BackoffEngaged > 0 && d.HandoffsWhileBackoff > 0 {
		return "backoff_engaged_handing_off"
	}
	// Backoff has engaged at least once (or there is none armed). Look at the
	// most recent iteration: if it is currently throttled the guard is live;
	// otherwise the recovery either converged or is making genuine progress.
	if len(d.Entries) > 0 && d.Entries[len(d.Entries)-1].BackoffEngaged {
		return "backoff_engaged"
	}
	return "healthy"
}

// recordRecoveryDiag appends one recovery-branch iteration to the ring buffer.
func (e *TerminalSessionExecutor) recordRecoveryDiag(entry ExecutorRecoveryDiagEntry) {
	e.diagMu.Lock()
	defer e.diagMu.Unlock()
	e.diagSeq++
	entry.Seq = e.diagSeq
	entry.AtUnixMs = time.Now().UnixMilli()
	if entry.BackoffEngaged {
		e.diagBackoffEngaged++
	}
	if entry.ArmedBackoff {
		e.diagArmedBackoff++
	}
	if entry.FlushedWhileBackoff {
		e.diagFlushedWhileBackoff++
	}
	if entry.HandoffWhileBackoff {
		e.diagHandoffWhileBackoff++
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
//   - Success without convergence (lastResetFailed=false): bounded retry
//     window (terminalScrollbackResetRetryWindow). A recovery cycle is
//     dominated by WaitIdle draining the transcript replay (~238 actions/cycle
//     in production, ~500ms), so the window must be comfortably ABOVE the
//     cycle: a window shorter than the cycle always expires before the next
//     schedule read and the guard never engages (observed with the original
//     100ms window: 439 arms, 0 engages, executor pinned at ~2 cores). With a
//     window above the cycle the guard rate-limits the reset+replay loop AND
//     still expires, allowing a bounded retry (terminalScrollbackResetMaxRetries
//     consecutive attempts) so a reconciliation whose first barrier was
//     dropped while ProjectionUnknown was set can converge after the
//     projection recovers. After the budget is exhausted the guard parks
//     until a real geometry/theme change advances LayoutGeneration.
func (e *TerminalSessionExecutor) scrollbackResetBackoff(stateGeneration uint64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.lastResetAt.IsZero() || e.lastResetGeneration != stateGeneration {
		return false
	}
	if e.lastResetFailed {
		return e.nowTime().Sub(e.lastResetAt) < terminalScrollbackResetBackoff
	}
	// Success mode: engage for the retry window, park permanently once the
	// same-generation retry budget is exhausted.
	if e.lastResetSuccessRetries >= terminalScrollbackResetMaxRetries {
		return true
	}
	return e.nowTime().Sub(e.lastResetAt) < terminalScrollbackResetRetryWindow
}

// scrollbackResetSuccessMode reports whether the engaged backoff came from a
// successful-but-non-converging recovery (lastResetFailed=false). In that mode
// the physical writer is healthy and the executor may still perform plain
// viewport flushes to keep the bottom surface (prompt input) live while
// suppressing the expensive scrollback reset+replay. In failed mode the writer
// is broken and must not be touched until the bounded window expires.
func (e *TerminalSessionExecutor) scrollbackResetSuccessMode() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return !e.lastResetFailed
}

// recordScrollbackReset persists the progress-guard state after a scrollback
// reset attempt so the next worker cycle can rate-limit a non-converging
// recovery without blocking legitimate retries under a new generation.
// stateGeneration MUST be read after the executor has published its outcome
// and the actor has settled (see runOne): the settled generation is what the
// next cycle observes when no external geometry/theme change intervened.
func (e *TerminalSessionExecutor) recordScrollbackReset(epoch, stateGeneration uint64, failed bool) {
	e.mu.Lock()
	e.lastResetAt = e.nowTime()
	e.lastResetEpoch = epoch
	if e.lastResetGeneration != stateGeneration || failed {
		// New generation (first attempt or a real geometry/theme change) or a
		// writer-failure record: start a fresh retry budget. A failure does not
		// consume the success-mode budget; the writer heals independently.
		e.lastResetSuccessRetries = 0
	} else {
		// Another consecutive same-generation success-mode reset: consume one
		// retry-budget slot. The backoff then parks once the budget is spent.
		e.lastResetSuccessRetries++
	}
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

// terminalSessionControllerIdleWait 是执行器等控制器 ack 的单次上限。
//
// 执行器是生产侧循环，而控制器在持续负载下队列可能长期非空（历史规划 pass
// 与高频事件流叠加）：无界的 UIController.WaitIdle 会把执行器线程永久钉死，
// 现场表现为投递日志/scrollback 不再推进、TUI 假死（2026-10-04
// session_20260930210352_V5o7MDYL：publishResult 在 WaitIdle 上阻塞 33 分钟，
// executor.last_entry 冻结在 seq=831，render_output 冻结在 last_sequence=3233）。
// 原实现用 WaitIdleTimeout 的 1ms 轮询兜底；现在所有调用点改为等待执行器
// 自己最近一次 post 的 ack 票据（WaitActionApplied）——事件驱动、不受其它
// 生产者队列长度影响。超时后继续前进仍是安全的：所有调用点在超时路径上都
// 只依赖随后重新读取的快照，或队列里仍有待办（HasPending）；排队中的
// ack/reducer action 会按序应用并由对应的 reducer 分支自行唤醒执行器。
const terminalSessionControllerIdleWait = 2 * time.Second

// postControllerActionTracked 投递一个控制器 action 并记录被接受 post 的 ack
// 票据。仅当控制器拒绝（已关闭）时返回 false；合并 post 返回的是被合并槽位
// 提升后的票据，因此等待该票据观察到的正是携带本次 payload 的那次 apply。
func (e *TerminalSessionExecutor) postControllerActionTracked(action UIAction) bool {
	if e == nil || e.controller == nil || action == nil {
		return false
	}
	outcome, ticket := e.controller.PostTracked(action)
	if outcome == PostDropped {
		return false
	}
	e.lastControllerTicket = ticket
	return true
}

// waitLastControllerAction 有界等待执行器最近一次被接受的 post 完成 apply、
// 其 AppState 已发布——这正是原 waitControllerIdle 各调用点需要的状态栅栏。
// 没有待观察 post 时立即返回 true；false 表示超时，调用方按"可能仍需再跑
// 一轮"处理，绝不阻塞。
func (e *TerminalSessionExecutor) waitLastControllerAction() bool {
	if e == nil || e.controller == nil {
		return true
	}
	ticket := e.lastControllerTicket
	if ticket == 0 {
		return true
	}
	return e.controller.WaitActionApplied(ticket, terminalSessionControllerIdleWait)
}

// waitControllerAcceptedApplied 有界等待"调用时刻已接受的全部 action"完成
// apply——runOne 读 schedule 前的状态栅栏。种子快照/替换动作是异步 apply 的：
// 立即读 schedule 会在 pending token 出现前空跑一轮并退出，而后续唤醒只由
// 特定 action 的 reducer 转移触发（HistoryPlanWindowReady 等），一旦错过
// 就再也没有 claim 机会（全量负载下
// TestPrintVisibleChatHistory_UnifiedHandoffsOverflowedCanonicalHistory 稳定复现
// 17 个 entry 全 Pending）。与旧 WaitIdleTimeout 的区别：只等本次调用时刻
// 已接受的票据（finite 集合），不会被后续生产者的持续入队钉死，事件驱动无
// 1ms 轮询。
func (e *TerminalSessionExecutor) waitControllerAcceptedApplied() bool {
	if e == nil || e.controller == nil {
		return true
	}
	ticket := e.controller.LastAcceptedTicket()
	if ticket == 0 {
		return true
	}
	return e.controller.WaitActionApplied(ticket, terminalSessionControllerIdleWait)
}

// releaseClaimMiss returns an accepted-but-uncomposable history claim to the
// reducer as an explicit Deferred. This helper runs only on the claim-miss
// branch, which is reached before any transaction is composed or written, so
// "Deferred" (the terminal transaction did not start; zero bytes reached the
// host) is the exact semantic: the reducer returns the token to Pending and,
// when the layout generation advanced underneath the claim, rebases its
// payload onto the current generation.
//
// This is a liveness requirement, not an optimization. A claim whose write
// cursor stays set with no terminal result is still Pending, so the schedule
// keeps pointing at it and the ordering guard blocks every later claim: a
// single stranded claim would deadlock the whole handoff queue, and no aging
// watchdog exists to recover it afterwards. The post is unconditional because
// this helper cannot distinguish "the claim was refused" from "the cursor was
// set but the snapshot refused"; a Deferred for a token that does not hold the
// cursor is a safe no-op in the reducer (the deferInFlight error is ignored
// there).
func (e *TerminalSessionExecutor) releaseClaimMiss(token, generation uint64) {
	if e == nil || e.controller == nil || token == 0 {
		return
	}
	e.diagMu.Lock()
	e.diagClaimMissReleases++
	e.diagMu.Unlock()
	e.postControllerActionTracked(HistoryCommitDeferred{Token: token, LayoutGeneration: generation})
	// Drain so the rebase is visible to the caller's follow-up schedule read.
	// Bounded by terminalSessionControllerIdleWait like every other controller
	// wait on this worker.
	e.waitLastControllerAction()
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
	e.waitControllerAcceptedApplied()
	schedule := e.controller.terminalSessionSchedule()
	if schedule.recoveryActionable {
		// Scrollback-reset backoff: a failing writer must not turn the
		// executor into an unbounded reset+replay loop. If the last reset
		// happened within the backoff window AND the layout generation has not
		// advanced since then, yield the worker. A real geometry/theme change
		// (new layout generation) always allows recovery — the reset is a
		// genuine retry, not a loop.
		if e.scrollbackResetBackoff(schedule.stateGeneration) {
			// Backoff engaged. Two failure modes:
			//   - failed mode (lastResetFailed=true): the physical writer is
			//     broken; do not touch it until the bounded window expires.
			//   - success mode (lastResetFailed=false): the writer is healthy
			//     but the recovery obligation will not converge at this layout
			//     generation. A full scrollback reset would re-enter the
			//     reset+replay loop (the observed ~2-core busy loop), so that
			//     stays suppressed. Suppressing the reset must NOT suppress the
			//     handoff of new content: the active band commits new messages
			//     as pending history tokens, and if the executor never claims
			//     them the message never reaches the unified renderer's
			//     scrollback (post-resume messages render nothing). Claim and
			//     deliver pending tokens exactly like the normal path, just
			//     without the reset+replay. When no token is pending, perform a
			//     plain viewport transaction instead, which keeps prompt
			//     rendering live while still suppressing the expensive
			//     reset+replay. publishResult may also post
			//     HistoryProjectionRecovered when the viewport repaint proves
			//     the projection known, which heals the obligation and exits
			//     the guard naturally.
			if e.scrollbackResetSuccessMode() {
				if schedule.pendingToken != 0 {
					// New content exists: deliver it now. The scrollback reset
					// stays suppressed (it would re-enter the reset+replay
					// loop), but a pending commit is genuine forward progress —
					// the active band's stable prefix crossing into native
					// scrollback. Without this, new messages after `resume`
					// stall forever while the success-mode backoff persists at
					// an unchanged layout generation.
					if !e.postControllerActionTracked(BeginHistoryCommit{
						Token: schedule.pendingToken, LayoutGeneration: schedule.pendingGeneration,
					}) {
						return false
					}
					e.waitLastControllerAction()
					claimedToken := schedule.pendingToken
					snapshot := e.controller.terminalSessionSnapshot(claimedToken)
					if snapshot.claimed != nil {
						plan := composeTerminalViewportTransactionPlan(snapshot.appState, snapshot.claimed, snapshot.bootstrap)
						result := e.session.FlushTransaction(plan)
						continued := e.publishResult(plan.Frame.LayoutGeneration, snapshot.claimed, result)
						e.recordRecoveryDiag(ExecutorRecoveryDiagEntry{
							Branch:              "scheduled",
							Revision:            schedule.stateRevision,
							RevisionAfter:       e.controller.Revision(),
							Generation:          plan.Frame.LayoutGeneration,
							TerminalEpoch:       result.TerminalEpoch,
							ProjectionUnknown:   snapshot.projectionUnknown,
							ReconciliationReq:   snapshot.reconciliationRequired,
							BackoffEngaged:      true,
							HandoffWhileBackoff: true,
							FullRepaint:         result.Frame.FullRepaint,
							ScrollbackReset:     result.ScrollbackReset,
							FrameErr:            frameErrString(result.Frame.Err),
							ObligationPending:   e.controller.terminalHistoryRecoveryObligationPending(),
							Continued:           continued,
						})
						return continued
					}
					// The claim could not be composed: either the reducer refused
					// markInFlight, or the layout generation advanced after an
					// accepted claim (the token was rebased/invalidated). Release
					// an accepted claim explicitly so it cannot strand InFlight
					// and block every later claim via the ordering guard; then
					// fall through to the recovery-actionable re-check below
					// instead of spinning on the stale token.
					e.releaseClaimMiss(claimedToken, schedule.pendingGeneration)
				}
				snapshot := e.controller.terminalSessionSnapshot(0)
				if terminalSessionSnapshotRecoveryActionable(snapshot) {
					plan := composeTerminalViewportTransactionPlan(snapshot.appState, nil)
					// A known-projection viewport flush must not silently keep
					// an outstanding history obligation alive forever. This
					// branch uses the same recovery selector as the non-backoff
					// paths: an armed session-load replay may replace scrollback,
					// while every other obligation settles non-destructively and
					// lets HistoryReconciliationSettled clear it.
					plan = terminalHistoryRecoveryPlan(snapshot)
					result := e.session.FlushTransaction(plan)
					e.publishResult(plan.Frame.LayoutGeneration, nil, result)
					armed := e.armRecoveryBackoff(result, schedule.stateGeneration)
					e.recordRecoveryDiag(ExecutorRecoveryDiagEntry{
						Branch:              "scheduled",
						Revision:            schedule.stateRevision,
						RevisionAfter:       e.controller.Revision(),
						Generation:          plan.Frame.LayoutGeneration,
						TerminalEpoch:       result.TerminalEpoch,
						ProjectionUnknown:   snapshot.projectionUnknown,
						ReconciliationReq:   snapshot.reconciliationRequired,
						BackoffEngaged:      true,
						FlushedWhileBackoff: true,
						FullRepaint:         result.Frame.FullRepaint,
						ScrollbackReset:     result.ScrollbackReset,
						FrameErr:            frameErrString(result.Frame.Err),
						ObligationPending:   e.controller.terminalHistoryRecoveryObligationPending(),
						ArmedBackoff:        armed,
						Continued:           false,
					})
					return false
				}
			}
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
		// An outstanding history obligation is recovered by the same selector as
		// every other entry point: replay only when a load authorized it,
		// otherwise repaint the viewport from source and quarantine in place.
		plan := terminalHistoryRecoveryPlan(snapshot)
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
		if !e.postControllerActionTracked(BeginHistoryCommit{
			Token: schedule.pendingToken, LayoutGeneration: schedule.pendingGeneration,
		}) {
			return false
		}
		e.waitLastControllerAction()
		claimedToken = schedule.pendingToken
	}

	snapshot := e.controller.terminalSessionSnapshot(claimedToken)
	if claimedToken == 0 && terminalSessionSnapshotRecoveryActionable(snapshot) {
		plan := terminalHistoryRecoveryPlan(snapshot)
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
	if claimedToken == 0 && schedule.planIncomplete {
		// No pending token and no recovery obligation, but the transcript plan
		// still owes cells. The trigger that carries a budget-truncated plan
		// forward is the ack handler, and every gate that can block that single
		// attempt (projection unknown, an unresolved delivery, a settle that
		// quarantined a delivered batch) clears through a transition that is
		// not an ack — so the plan can be stranded with an empty queue while
		// the executor goes idle (live: 6622 cells / 291842 rows, next=1288,
		// acked=322, pending=0, projection known). Ask the reducer to continue
		// the plan; the stall guard (PlanStalled) keeps a plan that cannot
		// advance in this epoch from spinning this loop.
		if e.postControllerActionTracked(ContinueHistoryPlanAction{}) {
			return true
		}
	}
	if claimedToken != 0 && snapshot.claimed == nil {
		// BeginHistoryCommit is queued behind any reducer actions that raced the
		// scalar schedule read. If one of those actions replaced the candidate or
		// invalidated the projection, consume that actionable state immediately:
		// its wake may already have been coalesced into this running worker. A
		// frozen/leased queue intentionally exposes no pending token, while the
		// unchanged token can mean an older in-flight ordering fence; neither case
		// should spin the executor. An *accepted* claim (markInFlight succeeded)
		// whose detached batch could not be composed at the now-current layout
		// generation must be released explicitly; leaving it InFlight strands it
		// forever behind the Pending-only schedule scan and the ordering guard.
		e.releaseClaimMiss(claimedToken, schedule.pendingGeneration)
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

// terminalHistoryRecoveryPlan selects the physical transaction that resolves an
// outstanding history obligation (projectionUnknown or reconciliationRequired).
//
// Only an explicit reducer-armed authorization may replace native scrollback and
// replay history: a session load (/resume, /load, startup restore) requests it
// inside the replacement snapshot it publishes, so the grant is installed by the
// same reduction that installs the Scene it authorizes and is consumed by
// exactly one reset+reconcile. Every other
// obligation — a failed or partially written handoff, an invalidated in-flight
// token, a resize or theme change that raced a claim — is settled
// non-destructively: the visible frame is repainted from semantic source and the
// unprovable resident range is quarantined in place. Already delivered rows are
// never re-emitted, and no normal interaction clears native scrollback.
func terminalHistoryRecoveryPlan(snapshot terminalSessionControllerSnapshot) TerminalTransactionPlan {
	if snapshot.scrollbackReplayArmed {
		return composeTerminalViewportScrollbackReconciliationPlan(snapshot.appState)
	}
	plan := composeTerminalViewportTransactionPlan(snapshot.appState, nil)
	plan.SettleHistoryProjection = true
	return plan
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
			e.postControllerActionTracked(HistoryCommitDeferred{Token: claimed.Token, LayoutGeneration: claimed.LayoutGeneration})
		case history.Err != nil && result.Frame.Err != nil && !history.MayHavePartiallyWritten:
			// The terminal transaction was attempted, but the writer proved that
			// zero bytes reached the host. Keep the same token retryable. The frame
			// error below invalidates the viewport cache; after a source-backed
			// recovery, HistoryProjectionRecovered wakes this Pending handoff.
			e.postControllerActionTracked(HistoryCommitDeferred{Token: claimed.Token, LayoutGeneration: claimed.LayoutGeneration})
		case history.MayHavePartiallyWritten && history.Err == nil:
			e.postControllerActionTracked(HistoryCommitFailed{
				Token: claimed.Token, LayoutGeneration: claimed.LayoutGeneration,
				Err: ErrHistoryCommitPartialWriteWithoutError, MayHavePartiallyWritten: true,
			})
		case history.Err != nil:
			e.postControllerActionTracked(HistoryCommitFailed{
				Token: claimed.Token, LayoutGeneration: claimed.LayoutGeneration,
				Err: history.Err, MayHavePartiallyWritten: history.MayHavePartiallyWritten,
			})
		default:
			if len(history.Delivered) > 0 {
				historyAcknowledged = e.postControllerActionTracked(HistoryCommitsAcknowledged{
					Commits: history.Delivered, Frame: history.Frame, LayoutGeneration: claimed.LayoutGeneration,
				})
			} else if e.postControllerActionTracked(HistoryCommitAcknowledged{
				Token: claimed.Token, Frame: history.Frame, LayoutGeneration: claimed.LayoutGeneration,
			}) {
				historyAcknowledged = true
			}
		}
	}

	if result.Frame.Err != nil {
		e.postControllerActionTracked(HistoryProjectionInvalidated{LayoutGeneration: generation})
		e.waitLastControllerAction()
		return false
	}
	if result.Frame.Deferred {
		e.waitLastControllerAction()
		return false
	}
	// A bottom-viewport repaint is not proof that the independently owned top
	// history projection recovered. Publish the reducer barrier only when the
	// terminal owner confirms both facts; partial history writes remain
	// fail-closed until an explicit scrollback reconciliation.
	if result.Frame.FullRepaint && e.session.ProjectionState().HistoryKnown {
		e.postControllerActionTracked(HistoryProjectionRecovered{LayoutGeneration: generation})
	}
	// A non-destructive recovery proves the visible frame without replacing
	// scrollback. Publish the settle barrier so the reducer quarantines the
	// unprovable resident range and resumes ordered handoff; it is skipped while
	// an authorized replay is still pending, because that replay owns the
	// obligation and a settle must never race it.
	if result.SettledHistoryProjection && result.Frame.Err == nil && !result.Frame.Deferred {
		e.postControllerActionTracked(HistoryReconciliationSettled{LayoutGeneration: generation})
	}
	if result.ScrollbackReset && result.TerminalEpoch != 0 {
		e.postControllerActionTracked(HistoryScrollbackReconciled{
			LayoutGeneration: generation,
			TerminalEpoch:    result.TerminalEpoch,
		})
	}
	e.waitLastControllerAction()
	if result.ScrollbackReset {
		return e.controller.terminalSessionHasActionableWork()
	}
	if historyAcknowledged && claimed != nil && e.controller.terminalSessionCommitAckedAndHasPending(claimed.Token) {
		return true
	}
	return e.controller.terminalSessionHasActionableWork()
}
