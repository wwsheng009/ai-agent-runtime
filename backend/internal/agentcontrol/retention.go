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
