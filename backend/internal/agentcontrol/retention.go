package agentcontrol

import (
	"context"
	"strings"
	"time"
)

// Registry retention (plan §P2-9 "TTL 删除走对账兜底").
//
// Terminal rows (closed/stale) never hold spawn quota and never appear as
// active children, so they are diagnostics: the durable registry keeps them so
// an operator can see what happened, and this retention turns that archive into
// a bounded one. Two tables grow with every spawn/close cycle — the rows
// themselves and the append-only wake-event log that wakes parent watchers —
// and neither had a pruning path before, so a long-lived deployment converged
// its quota but never its disk.
//
// Semantics shared by both hosts:
//   - one window governs rows and their wake events, because a wake event is
//     only meaningful while the row it describes is still around;
//   - the wake-event half is age-based over the whole append-only log rather
//     than only over the events of the pruned rows: every identity change
//     appends an event (a no-op projection refresh does not, see
//     agentRecordsShareWakeState), so a long-lived active child would otherwise
//     grow that table with its lifetime. A consumer that resumes from an
//     after_seq whose events already aged out may therefore see a truncated
//     history — the window is the retention contract for that log, and the row
//     half is what stays strictly terminal-only;
//   - 0 means the built-in default, a negative window means "keep forever"
//     (operator opt-out), a positive window is used as-is;
//   - only rows whose closed_at is older than the window are deleted, so a
//     purge can never touch a live child, and the deletion is age-based instead
//     of mode-based: reconcile observe/enforce gates drift convergence, not
//     housekeeping of rows that were already terminal before the pass started.

// DefaultTerminalRetention is how long a terminal registry row (and the wake
// events it produced) is kept when the host does not configure a window.
const DefaultTerminalRetention = 30 * 24 * time.Hour

// terminalPurgeBatch bounds a single DELETE so a neglected registry converges
// over several passes instead of holding one long write transaction (and its
// lock) on the shared SQLite file.
const terminalPurgeBatch = 512

// terminalPurgeMaxBatches bounds the work of one pass; the remainder is left to
// the next cadence, which keeps the reconcile pass predictable under load.
const terminalPurgeMaxBatches = 8

// NormalizeTerminalRetention maps host configuration onto the purge window:
// 0 uses DefaultTerminalRetention, a negative value disables the purge, and a
// positive value is used as-is.
func NormalizeTerminalRetention(window time.Duration) time.Duration {
	switch {
	case window == 0:
		return DefaultTerminalRetention
	case window < 0:
		return 0
	default:
		return window
	}
}

// ParseTerminalRetention parses a host override: a Go duration, "default"/"0"
// for the built-in window, or "off"/"none"/"-1" to keep terminal rows forever.
// ok=false reports an unparsable value so hosts keep the next precedence level
// instead of silently changing retention.
func ParseTerminalRetention(raw string) (time.Duration, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, false
	}
	switch strings.ToLower(trimmed) {
	case "off", "none", "-1":
		return -1, true
	case "default", "0":
		return 0, true
	}
	parsed, err := time.ParseDuration(trimmed)
	if err != nil {
		return 0, false
	}
	if parsed < 0 {
		return -1, true
	}
	return parsed, true
}

// TerminalPurgeStore is the writer half of registry retention. Stores that
// cannot prune simply do not implement it, and PurgeTerminalAgentRecords then
// reports Supported=false instead of failing the pass.
type TerminalPurgeStore interface {
	// PurgeAgentControlTerminalAgents deletes up to limit rows whose closed_at
	// is strictly older than closedBefore, oldest first.
	PurgeAgentControlTerminalAgents(ctx context.Context, closedBefore time.Time, limit int) (int64, error)
	// PurgeAgentControlAgentWakeEvents deletes up to limit wake events whose
	// created_at is strictly older than createdBefore, oldest first.
	PurgeAgentControlAgentWakeEvents(ctx context.Context, createdBefore time.Time, limit int) (int64, error)
}

// TerminalPurgePolicy is the host-resolved retention for one pass.
type TerminalPurgePolicy struct {
	// Now is the pass clock; zero uses time.Now().UTC().
	Now time.Time
	// Retention is the normalized window (NormalizeTerminalRetention): <= 0
	// disables the purge before any store call.
	Retention time.Duration
	// BatchLimit caps the rows deleted per statement; <= 0 uses the package
	// default.
	BatchLimit int
	// MaxBatches caps how many batches one pass issues; <= 0 uses the package
	// default.
	MaxBatches int
}

// TerminalPurgeOutcome reports what one purge pass did. Supported stays false
// when the store cannot prune, so hosts can render "retention unsupported"
// separately from "nothing was old enough".
type TerminalPurgeOutcome struct {
	Supported  bool  `json:"supported"`
	Rows       int64 `json:"rows,omitempty"`
	WakeEvents int64 `json:"wake_events,omitempty"`
	Batches    int   `json:"batches,omitempty"`
	// WakePruneMode/WakePruneCandidates report the wake-event governance half
	// (retention.go, H3) when the host folds it into the same pass: the mode it
	// ran in ("observe"/"enforce") and the bounded candidate set it reported
	// (observe) or removed (enforce). An empty mode means the host did not wire
	// that half, which is why it is a separate field instead of a count.
	WakePruneMode       string `json:"wake_prune_mode,omitempty"`
	WakePruneCandidates int64  `json:"wake_prune_candidates,omitempty"`
	// FirstError mirrors ReclaimOutcome: the pass keeps going where it can, and
	// the first failure is what the host surfaces.
	FirstError string `json:"first_error,omitempty"`
}

// PurgeTerminalAgentRecords deletes long-terminal registry rows and the wake
// events they produced. It is a no-op (and reports Supported=false) when the
// window is disabled or the store cannot prune, so hosts can call it
// unconditionally in every pass.
func PurgeTerminalAgentRecords(ctx context.Context, store AgentRegistryStore, policy TerminalPurgePolicy) (TerminalPurgeOutcome, error) {
	outcome := TerminalPurgeOutcome{}
	if ctx == nil {
		ctx = context.Background()
	}
	if policy.Retention <= 0 {
		return outcome, nil
	}
	purgeStore, ok := store.(TerminalPurgeStore)
	if !ok || purgeStore == nil {
		return outcome, nil
	}
	outcome.Supported = true
	now := policy.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	cutoff := now.Add(-policy.Retention)
	limit := policy.BatchLimit
	if limit <= 0 {
		limit = terminalPurgeBatch
	}
	maxBatches := policy.MaxBatches
	if maxBatches <= 0 {
		maxBatches = terminalPurgeMaxBatches
	}
	for batch := 0; batch < maxBatches; batch++ {
		rows, err := purgeStore.PurgeAgentControlTerminalAgents(ctx, cutoff, limit)
		if err != nil {
			outcome.FirstError = err.Error()
			return outcome, err
		}
		wakeEvents, err := purgeStore.PurgeAgentControlAgentWakeEvents(ctx, cutoff, limit)
		if err != nil {
			outcome.FirstError = err.Error()
			return outcome, err
		}
		outcome.Rows += rows
		outcome.WakeEvents += wakeEvents
		outcome.Batches++
		if rows < int64(limit) && wakeEvents < int64(limit) {
			return outcome, nil
		}
	}
	return outcome, nil
}

// Wake-event governance (plan P1-2 / H3).
//
// The registry's wake log is append-only and, before this, was only bounded by
// age: PurgeTerminalAgentRecords removes aged rows, but a single long-lived hot
// agent can append an unbounded number of non-terminal rows in the meantime
// (measured: 1,239,083 rows / 541 MB for 924 identity rows). This section adds
// the second bound — a per-agent cap on ACTIVE (non-terminal) wake rows — and a
// pass that converges the backlog with observe/enforce semantics.
//
// It reuses the existing retention entry point instead of adding a parallel
// scheduler: hosts call PruneAgentWakeEvents from the same reconcile hook that
// already runs PurgeTerminalAgentRecords (Reconciler.Purge), and the
// ReconcileMode of that pass decides observe vs enforce.
//
// Durable semantics: only wake-event rows are affected. The identity rows and
// the existing agent.reclaimed / quota-reclaim paths are untouched, and the
// age-based whole-log purge (PurgeAgentControlAgentWakeEvents) keeps its
// current window and semantics.

// DefaultMaxActiveWakeEventsPerAgent is how many non-terminal wake events one
// agent keeps when the host does not configure a cap. A wake event is a change
// feed entry: a watcher that falls further behind than this many changes for
// one agent can no longer replay it one-by-one, which is why the cap is set
// well above any interactive catch-up need (64) instead of near zero.
const DefaultMaxActiveWakeEventsPerAgent = 64

// DefaultClosedWakeRetention is how long a closed/stale wake event is kept by
// PruneAgentWakeEvents when the host does not configure a shorter window. The
// 1.13M closed rows measured on a real registry are diagnostics: they explain
// how an agent ended, and that story is only useful for a fraction of the 30-day
// identity-row window, so the default is deliberately shorter.
const DefaultClosedWakeRetention = 7 * 24 * time.Hour

// NormalizeMaxActiveWakeEventsPerAgent maps host configuration onto the
// per-agent cap: 0 uses DefaultMaxActiveWakeEventsPerAgent, a negative value
// disables the cap (keep the append-only log unbounded), and a positive value
// is used as-is.
func NormalizeMaxActiveWakeEventsPerAgent(limit int) int {
	switch {
	case limit == 0:
		return DefaultMaxActiveWakeEventsPerAgent
	case limit < 0:
		return 0
	default:
		return limit
	}
}

// NormalizeClosedWakeRetention maps host configuration onto the closed-row
// window: 0 uses DefaultClosedWakeRetention, a negative value disables that
// half, and a positive value is used as-is.
func NormalizeClosedWakeRetention(window time.Duration) time.Duration {
	switch {
	case window == 0:
		return DefaultClosedWakeRetention
	case window < 0:
		return 0
	default:
		return window
	}
}

// AgentWakePruneStore is the writer half of wake-event governance. Stores that
// cannot prune simply do not implement it, and PruneAgentWakeEvents then reports
// Supported=false instead of failing the pass.
//
// The Count/Delete pairs are deliberately symmetric: observe reports exactly
// what enforce would remove for the same policy and budget, so an operator can
// approve a pass before it deletes anything.
type AgentWakePruneStore interface {
	// CountAgentControlClosedWakeEvents reports how many closed/stale wake
	// events older than createdBefore the matching delete would remove, oldest
	// first, bounded by limit.
	CountAgentControlClosedWakeEvents(ctx context.Context, createdBefore time.Time, limit int) (int64, error)
	// DeleteAgentControlClosedWakeEvents deletes up to limit closed/stale wake
	// events older than createdBefore, oldest first.
	DeleteAgentControlClosedWakeEvents(ctx context.Context, createdBefore time.Time, limit int) (int64, error)
	// CountAgentControlActiveWakeOverflow reports how many non-terminal wake
	// events older than the newest keepPerAgent rows of their own agent the
	// matching delete would remove, bounded by limit.
	CountAgentControlActiveWakeOverflow(ctx context.Context, keepPerAgent int, limit int) (int64, error)
	// DeleteAgentControlActiveWakeOverflow deletes up to limit non-terminal wake
	// events older than the newest keepPerAgent rows of their own agent.
	DeleteAgentControlActiveWakeOverflow(ctx context.Context, keepPerAgent int, limit int) (int64, error)
}

// AgentWakePrunePolicy is the host-resolved wake-event governance for one pass.
type AgentWakePrunePolicy struct {
	// Now is the pass clock; zero uses time.Now().UTC().
	Now time.Time
	// Mode is the reconcile mode: anything other than ReconcileModeEnforce only
	// reports. The zero value is observe, so a caller that forgets to set it
	// never deletes.
	Mode ReconcileMode
	// ClosedRetention is the normalized window for closed/stale wake events
	// (NormalizeClosedWakeRetention): <= 0 disables that half.
	ClosedRetention time.Duration
	// PerAgentLimit is the normalized per-agent cap on non-terminal wake events
	// (NormalizeMaxActiveWakeEventsPerAgent): <= 0 disables that half.
	PerAgentLimit int
	// BatchLimit caps the rows removed per statement; <= 0 uses the package
	// default.
	BatchLimit int
	// MaxBatches caps how many batches one pass issues; <= 0 uses the package
	// default.
	MaxBatches int
}

// AgentWakePruneOutcome reports what one wake-event pass did (observe: what it
// would do). Supported stays false when the store cannot prune, so hosts can
// render "wake retention unsupported" separately from "nothing matched".
type AgentWakePruneOutcome struct {
	Supported bool   `json:"supported"`
	Mode      string `json:"mode,omitempty"`
	// ClosedCandidates / OverflowCandidates are the bounded sets observe
	// reported (and enforce removed) for the two halves.
	ClosedCandidates   int64 `json:"closed_candidates,omitempty"`
	OverflowCandidates int64 `json:"overflow_candidates,omitempty"`
	// ClosedDeleted / OverflowDeleted are zero in observe mode.
	ClosedDeleted   int64 `json:"closed_deleted,omitempty"`
	OverflowDeleted int64 `json:"overflow_deleted,omitempty"`
	Batches         int   `json:"batches,omitempty"`
	// FirstError mirrors ReclaimOutcome: the pass keeps going where it can, and
	// the first failure is what the host surfaces.
	FirstError string `json:"first_error,omitempty"`
}

// PruneAgentWakeEvents converges the wake-event log with observe/enforce
// semantics. Observe (the default: any Mode other than ReconcileModeEnforce)
// only counts what the same policy would remove and writes nothing; enforce
// deletes exactly those bounded candidate sets.
//
// It is meant to be called from the existing retention hook (Reconciler.Purge,
// next to PurgeTerminalAgentRecords) so there is no second scheduler. Default
// callers stay in observe mode: the zero-value policy has Mode == "" and both
// windows defaulted, so wiring this into a pass cannot delete anything until an
// operator explicitly opts into ReconcileModeEnforce. Existing
// agent.reclaimed / quota-reclaim semantics are untouched — this function only
// touches agent_control_agent_wake_events rows.
func PruneAgentWakeEvents(ctx context.Context, store AgentRegistryStore, policy AgentWakePrunePolicy) (AgentWakePruneOutcome, error) {
	outcome := AgentWakePruneOutcome{}
	if ctx == nil {
		ctx = context.Background()
	}
	pruneStore, ok := store.(AgentWakePruneStore)
	if !ok || pruneStore == nil {
		return outcome, nil
	}
	outcome.Supported = true
	mode := ReconcileModeObserve
	if policy.Mode == ReconcileModeEnforce {
		mode = ReconcileModeEnforce
	}
	outcome.Mode = string(mode)
	enforce := mode == ReconcileModeEnforce
	now := policy.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	limit := policy.BatchLimit
	if limit <= 0 {
		limit = terminalPurgeBatch
	}
	maxBatches := policy.MaxBatches
	if maxBatches <= 0 {
		maxBatches = terminalPurgeMaxBatches
	}
	// Observe counts the same bounded budget enforce would delete in one pass,
	// so the two modes report the same number for the same policy.
	budget := limit * maxBatches

	if window := NormalizeClosedWakeRetention(policy.ClosedRetention); window > 0 {
		cutoff := now.Add(-window)
		if enforce {
			deleted, batches, err := runAgentWakePruneBatches(ctx, limit, maxBatches, func(batchCtx context.Context, batchLimit int) (int64, error) {
				return pruneStore.DeleteAgentControlClosedWakeEvents(batchCtx, cutoff, batchLimit)
			})
			outcome.ClosedDeleted = deleted
			outcome.Batches += batches
			if err != nil {
				outcome.FirstError = err.Error()
				return outcome, err
			}
		} else {
			count, err := pruneStore.CountAgentControlClosedWakeEvents(ctx, cutoff, budget)
			if err != nil {
				outcome.FirstError = err.Error()
				return outcome, err
			}
			outcome.ClosedCandidates = count
		}
	}

	if keep := NormalizeMaxActiveWakeEventsPerAgent(policy.PerAgentLimit); keep > 0 {
		if enforce {
			deleted, batches, err := runAgentWakePruneBatches(ctx, limit, maxBatches, func(batchCtx context.Context, batchLimit int) (int64, error) {
				return pruneStore.DeleteAgentControlActiveWakeOverflow(batchCtx, keep, batchLimit)
			})
			outcome.OverflowDeleted = deleted
			outcome.Batches += batches
			if err != nil {
				outcome.FirstError = err.Error()
				return outcome, err
			}
		} else {
			count, err := pruneStore.CountAgentControlActiveWakeOverflow(ctx, keep, budget)
			if err != nil {
				outcome.FirstError = err.Error()
				return outcome, err
			}
			outcome.OverflowCandidates = count
		}
	}

	if enforce {
		// Enforce removes the reported candidates, so the outcome carries the
		// same numbers an observe pass would have reported.
		outcome.ClosedCandidates = outcome.ClosedDeleted
		outcome.OverflowCandidates = outcome.OverflowDeleted
	}
	return outcome, nil
}

// runAgentWakePruneBatches runs bounded delete batches until one comes back
// short (the half is converged) or the batch budget is exhausted. The remainder
// is left to the next cadence, which keeps the pass predictable under load.
func runAgentWakePruneBatches(ctx context.Context, limit int, maxBatches int, remove func(ctx context.Context, limit int) (int64, error)) (int64, int, error) {
	var deleted int64
	batches := 0
	for batch := 0; batch < maxBatches; batch++ {
		rows, err := remove(ctx, limit)
		if err != nil {
			return deleted, batches, err
		}
		deleted += rows
		batches++
		if rows < int64(limit) {
			break
		}
	}
	return deleted, batches, nil
}
