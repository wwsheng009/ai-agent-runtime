package agentcontrol

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// DefaultReconcileInterval is the low-frequency cadence recommended by plan
// §P2-9: drift is measured in abandoned sessions, so a pass every ten minutes
// keeps the durable active set honest without competing with live traffic.
const DefaultReconcileInterval = 10 * time.Minute

// MinReconcileInterval floors host configuration so a typo cannot turn the
// audit into a hot loop.
const MinReconcileInterval = time.Minute

// ParseReconcileMode normalizes a host-provided mode string. Unknown values
// fall back to observe, which is the safe default from plan §P2-9 兼容与风险.
func ParseReconcileMode(raw string) ReconcileMode {
	if strings.EqualFold(strings.TrimSpace(raw), string(ReconcileModeEnforce)) {
		return ReconcileModeEnforce
	}
	return ReconcileModeObserve
}

// NormalizeReconcileInterval applies the default and the floor.
func NormalizeReconcileInterval(interval time.Duration) time.Duration {
	if interval <= 0 {
		return DefaultReconcileInterval
	}
	if interval < MinReconcileInterval {
		return MinReconcileInterval
	}
	return interval
}

// Reconciler owns the periodic audit + convergence loop shared by the API
// runtime and the CLI local runtime, plus the last-report cache both hosts
// surface through `/debug` and the supervision HTTP API.
//
// List is a host callback instead of a bare store read because hosts refresh
// their projections first (CLI: materializeLocalAgentRegistry, API:
// materializeAgentControlAgentProjections) so a pass never audits a stale
// snapshot.
type Reconciler struct {
	Store    AgentRegistryStore
	List     func(ctx context.Context) ([]AgentRecord, error)
	Lookup   SessionBindingLookup
	Mode     ReconcileMode
	Interval time.Duration
	// Reclaim is the host-side half of the P2-8 eviction sweep (plan §P2-9
	// 方案 3): the host projects the audited rows into observations and closes
	// whatever the shared ReclaimPolicy selects. enforce=false means "evaluate
	// only" — the host must report Candidates without closing anything. A nil
	// hook keeps the pass audit-only, so hosts without a reclaim store behave
	// exactly as before.
	Reclaim func(ctx context.Context, records []AgentRecord, enforce bool, now time.Time) (ReclaimOutcome, error)
	// Purge is the host-side half of registry retention (retention.go): hosts
	// resolve the configured window and prune terminal rows plus their wake
	// events. A nil hook keeps the pass from deleting anything, and a disabled
	// window inside the hook is a no-op, so hosts can wire it unconditionally.
	Purge func(ctx context.Context, now time.Time) (TerminalPurgeOutcome, error)
	// Worktrees is the host-side half of worktree drift reconciliation
	// (isolation/worktree.ReconcileWorktrees, findings H11/H15): the host
	// resolves the repo root plus the worktree paths its live registry still
	// owns, then reports (and, in enforce mode, reclaims) orphan directories
	// and stale git registrations. A nil hook keeps the pass unchanged.
	Worktrees func(ctx context.Context, records []AgentRecord, enforce bool, now time.Time) (WorktreeReconcileOutcome, error)

	mu      sync.Mutex
	running bool
	last    ReconcileReport
	lastErr string
}

// WorktreeReconcileOutcome is the host-side summary of one worktree drift pass.
// Supported=false means the host has no worktree surface (no git repo, no
// durable store); the counters then mirror isolation/worktree.ReconcileReport.
type WorktreeReconcileOutcome struct {
	Supported     bool `json:"supported,omitempty"`
	Dirs          int  `json:"dirs,omitempty"`
	Orphans       int  `json:"orphans,omitempty"`
	StaleGit      int  `json:"stale_git,omitempty"`
	StaleRegistry int  `json:"stale_registry,omitempty"`
	Unmanaged     int  `json:"unmanaged,omitempty"`
	ReclaimedDirs int  `json:"reclaimed_dirs,omitempty"`
	ReclaimedGit  int  `json:"reclaimed_git,omitempty"`
	Failed        int  `json:"failed,omitempty"`
}

// IntervalOrDefault returns the effective cadence.
func (r *Reconciler) IntervalOrDefault() time.Duration {
	if r == nil {
		return DefaultReconcileInterval
	}
	return NormalizeReconcileInterval(r.Interval)
}

// ModeOrDefault returns the effective mode (observe unless enforce is set).
func (r *Reconciler) ModeOrDefault() ReconcileMode {
	if r == nil {
		return ReconcileModeObserve
	}
	if r.Mode == ReconcileModeEnforce {
		return ReconcileModeEnforce
	}
	return ReconcileModeObserve
}

// RunOnce performs a single audit + convergence pass and caches the outcome.
// Overlapping passes are rejected so a slow audit cannot stack up behind a
// short interval.
func (r *Reconciler) RunOnce(ctx context.Context) (ReconcileReport, error) {
	if r == nil {
		return ReconcileReport{Mode: string(ReconcileModeObserve)}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	if r.running {
		last, lastErr := r.last, r.lastErr
		r.mu.Unlock()
		return last, fmt.Errorf("reconcile pass already running: %s", lastErr)
	}
	r.running = true
	r.mu.Unlock()

	report, err := r.runPass(ctx)

	r.mu.Lock()
	r.running = false
	r.last = report
	if err != nil {
		r.lastErr = err.Error()
	} else {
		r.lastErr = ""
	}
	r.mu.Unlock()
	return report, err
}

func (r *Reconciler) runPass(ctx context.Context) (ReconcileReport, error) {
	// Placeholder: ReconcileAgentSessionConsistency stamps the report itself on
	// the success path (including ReconciledAt). The zero timestamp left by an
	// early List failure is what makes LastReport report "no completed pass".
	report := ReconcileReport{
		Mode: string(r.ModeOrDefault()),
	}
	store := r.Store
	var records []AgentRecord
	if r.List != nil {
		listed, err := r.List(ctx)
		if err != nil {
			return report, err
		}
		records = listed
	} else if store != nil {
		listed, err := store.ListAgentControlAgents(ctx, AgentFilter{IncludeClosed: true})
		if err != nil {
			return report, err
		}
		records = listed
	}
	report, err := ReconcileAgentSessionConsistency(ctx, store, records, r.Lookup, r.ModeOrDefault())
	if err != nil {
		return report, err
	}
	// Purge (retention) runs BEFORE reclaim so that terminal rows older than
	// the retention window are deleted from the store while reclaim can still
	// see the full audited set for diagnostics. This matters because purge only
	// ever touches rows that are already terminal — it never steals a row from
	// an active child — so running it first is both safe and keeps the record
	// set that QuotaChildren must scan (and pre-allocate for) as small as
	// possible. Putting purge after reclaim meant that an OOM during reclaim
	// left terminal rows un-purged, so every subsequent pass re-listed a
	// growing stack of closed rows and the process spiraled into OOM faster
	// (see QuotaChildren OOM: make([]AgentRecord, 0, len(records))).
	r.runPurgePass(ctx, &report)
	r.runReclaimPass(ctx, &report, records)
	r.runWorktreePass(ctx, &report, records)
	return report, nil
}

// runWorktreePass folds one host worktree drift sweep into the report. Like the
// reclaim/purge passes its failure is recorded instead of returned: a worktree
// base dir that cannot be read must not blank the identity-graph audit the
// hosts surface through /debug.
func (r *Reconciler) runWorktreePass(ctx context.Context, report *ReconcileReport, records []AgentRecord) {
	if r == nil || r.Worktrees == nil || report == nil {
		return
	}
	enforce := r.ModeOrDefault() == ReconcileModeEnforce
	outcome, err := r.Worktrees(ctx, records, enforce, time.Now().UTC())
	if err != nil {
		report.WorktreeError = err.Error()
		return
	}
	report.Worktrees = outcome
}

// runReclaimPass folds one host eviction sweep into the report. Its failure is
// recorded instead of returned: the audit result is what the hosts surface
// through `/debug`, and a store that refuses one eviction must not blank the
// drift report the operator is looking at.
func (r *Reconciler) runReclaimPass(ctx context.Context, report *ReconcileReport, records []AgentRecord) {
	if r == nil || r.Reclaim == nil || report == nil {
		return
	}
	enforce := r.ModeOrDefault() == ReconcileModeEnforce
	outcome, err := r.Reclaim(ctx, records, enforce, time.Now().UTC())
	report.ReclaimCandidates = outcome.Candidates
	report.Reclaimed = outcome.Reclaimed()
	report.ReclaimedRows = outcome.Rows
	report.ReclaimFailed = outcome.Failed
	report.ReclaimReasons = outcome.Reasons
	switch {
	case err != nil:
		report.ReclaimError = err.Error()
	case strings.TrimSpace(outcome.FirstError) != "":
		report.ReclaimError = strings.TrimSpace(outcome.FirstError)
	}
}

// runPurgePass folds registry retention into the report. Like the reclaim
// half, a failure is recorded instead of returned: the drift report the
// operator is looking at must not blank out because housekeeping failed.
func (r *Reconciler) runPurgePass(ctx context.Context, report *ReconcileReport) {
	if r == nil || r.Purge == nil || report == nil {
		return
	}
	outcome, err := r.Purge(ctx, time.Now().UTC())
	report.PurgedRows = outcome.Rows
	report.PurgedWakeEvents = outcome.WakeEvents
	report.WakePruneMode = outcome.WakePruneMode
	report.WakePruneCandidates = outcome.WakePruneCandidates
	switch {
	case err != nil:
		report.PurgeError = err.Error()
	case strings.TrimSpace(outcome.FirstError) != "":
		report.PurgeError = strings.TrimSpace(outcome.FirstError)
	}
}

// RunLoop blocks until ctx is done: one immediate pass (so drift left by an
// unclean shutdown converges without waiting a full interval) followed by the
// configured cadence. Errors are cached in the report so hosts can surface the
// last failure without an extra log sink.
func (r *Reconciler) RunLoop(ctx context.Context) {
	if r == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	interval := r.IntervalOrDefault()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	_, _ = r.RunOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = r.RunOnce(ctx)
		}
	}
}

// LastReport returns the cached report, the last error text and whether any
// pass has completed. Hosts use it for `/debug`, `/agents panel` and the
// supervision HTTP status payload.
func (r *Reconciler) LastReport() (ReconcileReport, string, bool) {
	if r == nil {
		return ReconcileReport{}, "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.last.ReconciledAt.IsZero() {
		return r.last, r.lastErr, false
	}
	return r.last, r.lastErr, true
}

// ReconcileSummary renders the host-facing line for the cached report, folding
// in the last error so an operator sees failures instead of a silent
// `reconcile=not_run`.
func (r *Reconciler) ReconcileSummary() string {
	report, lastErr, ok := r.LastReport()
	if !ok {
		if strings.TrimSpace(lastErr) != "" {
			return "reconcile=error detail=" + lastErr
		}
		return "reconcile=not_run"
	}
	summary := report.Summary()
	if strings.TrimSpace(lastErr) != "" {
		return summary + " last_error=" + lastErr
	}
	return summary
}
