package supervision

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ErrExecutionDeadlineRequired is returned by StartRun when the supervisor is
// configured to require an execution deadline (I2) and the run would be
// admitted without one. An obligation without a decidable deadline can never be
// joined, so such a dispatch must be rejected instead of parked.
var ErrExecutionDeadlineRequired = errors.New("supervision: execution deadline is required (I2)")

// ExecutionSupervisorConfig controls the child run watchdog (doc 10 config
// design). Mode observe only records decisions; enforce also sends interrupt
// and performs cancel/grace state transitions.
type ExecutionSupervisorConfig struct {
	Enabled bool
	// Mode is "observe" or "enforce". Empty defaults to "enforce" so the P3
	// acceptance (blocking provider interrupted at deadline) works out of the
	// box; operators can switch to observe for dry-run.
	Mode string
	// ScanInterval is the decision scan period.
	ScanInterval time.Duration
	// DefaultExecutionTimeout is the operator default for runs that do not
	// carry an explicit timeout. Zero means unbounded only when AllowUnbounded
	// is true; otherwise it maps to the documented default (30m).
	DefaultExecutionTimeout time.Duration
	// DefaultProgressTimeout is the operator default for progress stalls.
	DefaultProgressTimeout time.Duration
	// DefaultApprovalTimeout is the independent deadline for
	// waiting_approval/waiting_input (never the progress timeout).
	DefaultApprovalTimeout time.Duration
	// DefaultCancelGrace is how long a run may keep executing after an
	// interrupt request before it is fenced as orphaned.
	DefaultCancelGrace time.Duration
	// AllowUnbounded permits explicit timeout=0 to mean "no execution
	// deadline". When false, timeout=0 resolves to DefaultExecutionTimeout.
	AllowUnbounded bool
	// RequireExecutionDeadline enforces I2 at admission: a dispatch whose
	// execution deadline resolves to nil (only reachable with AllowUnbounded)
	// is rejected instead of admitted, because the run could never be judged
	// terminal by the watchdog ladder. The zero value keeps legacy behavior, so
	// hosts only opt in when their runs participate in the suspension/join
	// protocol (AC-C0-2e).
	RequireExecutionDeadline bool
	// StoreOutageGrace is the tolerated store failure window before the
	// scanner backs off (doc 10 retention/grace).
	StoreOutageGrace time.Duration

	// --- escalate-first soft threshold (change #1; plan §6.3, Q2/Q3, I8) ---

	// EscalateFirst switches the soft-threshold branch from "judge -> enforce"
	// to "judge -> report -> decide -> fallback" (plan §6.3). nil/true (the
	// default) escalates first; an explicit false restores the pre-#1 forced
	// branch, so a host can roll back without rolling back the binary.
	EscalateFirst *bool
	// StallEscalationMultiplier is the escalation threshold as a multiple of
	// the soft threshold (Q3): progress_age >= multiplier x soft escalates
	// instead of cancelling. Zero uses the documented default (2).
	StallEscalationMultiplier float64
	// DecisionWindow is the decision window W the parent gets after an
	// escalation (Q2). Zero derives 2 x DefaultProgressTimeout so a host that
	// only overrides the progress timeout keeps the documented ratio.
	DecisionWindow time.Duration
	// DecisionWindowMax is the wall-clock cap on the decision window (Q2: 2W).
	// Zero derives 2 x DecisionWindow. It bounds the runnable-clock deferral of
	// I8/EC-A3, so the fallback can never be postponed indefinitely.
	DecisionWindowMax time.Duration
	// ExecutionRunRetention / ExecutionRunPruneLimit bound the retention GC
	// (plan C4-2 / §6.12). Zero takes the shared operator defaults (7 days,
	// 200 rows per pass) so an unwired host still prunes instead of growing the
	// ledger without bound.
	ExecutionRunRetention  time.Duration
	ExecutionRunPruneLimit int
}

// DefaultExecutionSupervisorConfig returns the documented defaults (doc 10):
// enforce mode, 30m execution timeout, 5m progress timeout, 1h approval
// timeout, 15s cancel grace, 5s scan interval.
func DefaultExecutionSupervisorConfig() ExecutionSupervisorConfig {
	// The suspension/escalate-first knobs come from the shared operator config
	// (change #8) so both surfaces report one set of defaults (AC-P0-4a).
	suspension := DefaultConfig()
	return ExecutionSupervisorConfig{
		Enabled:                 true,
		Mode:                    "enforce",
		ScanInterval:            5 * time.Second,
		DefaultExecutionTimeout: 30 * time.Minute,
		DefaultProgressTimeout:  5 * time.Minute,
		DefaultApprovalTimeout:  1 * time.Hour,
		DefaultCancelGrace:      15 * time.Second,
		AllowUnbounded:          false,
		StoreOutageGrace:        2 * time.Minute,

		EscalateFirst:             suspension.EscalateFirst,
		StallEscalationMultiplier: suspension.StallEscalationMultiplier,
		DecisionWindow:            suspension.DecisionWindow,
		DecisionWindowMax:         suspension.DecisionWindowMax,
		ExecutionRunRetention:     suspension.ExecutionRunRetention,
		ExecutionRunPruneLimit:    suspension.ExecutionRunPruneLimit,
	}
}

// RunSpec is the host-neutral input for starting a supervised run (doc 5.3 /
// 7.1). Zero durations fall back to the supervisor defaults; explicit zero
// means unbounded only when AllowUnbounded is configured.
type RunSpec struct {
	Kind            string
	Workflow        string
	RootSessionID   string
	ParentSessionID string
	ParentRunID     string
	SessionID       string
	AgentID         string
	OwnerID         string
	// TurnID is the dispatching turn (AC-P0-3c). Resume keys on it, so hosts
	// must pass the turn that spawned this obligation.
	TurnID string
	// DeclaredBudget / DecisionWindowUntil are the per-obligation declarative
	// budget and the escalate-first decision window (§6.2 / I8).
	DeclaredBudget      time.Duration
	DecisionWindowUntil *time.Time
	ExecutionTimeout    time.Duration
	ProgressTimeout     time.Duration
	ApprovalTimeout     time.Duration
	CancelGrace         time.Duration
	MaxAttempts         int
}

// RunInterrupter is implemented by the host to interrupt a live child run
// (actor interrupt; no-op when no live actor exists).
type RunInterrupter interface {
	InterruptRun(ctx context.Context, run ExecutionRun) error
}

// CompletionDispatcher is implemented by the host to deliver a terminal
// completion outbox entry to the parent mailbox. It returns the parent
// mailbox sequence on success.
type CompletionDispatcher interface {
	DispatchCompletion(ctx context.Context, entry CompletionOutboxEntry) (int64, error)
}

// RunDecision is one scanner decision for a single run.
type RunDecision struct {
	RunID       string
	SessionID   string
	Status      string
	Decision    string // execution_timed_out | progress_stalled | approval_timeout | cancel_grace_expired | orphan_suspected
	ActionTaken string // none_observe | escalated | cancel_requested | interrupted | orphaned | forced_terminal
	Reason      string
}

// ExecutionSupervisorStats is a read-only operator snapshot (plan §P0-4 可见性).
// It exposes the effective config plus cheap in-process counters so that a host
// debug surface can report "is the watchdog running, how often did it scan,
// what did it decide last" without touching the store.
type ExecutionSupervisorStats struct {
	Enabled          bool
	Mode             string
	ScanInterval     time.Duration
	ExecutionTimeout time.Duration
	ProgressTimeout  time.Duration
	ApprovalTimeout  time.Duration
	CancelGrace      time.Duration
	StoreOutageGrace time.Duration
	// LoopRunning reports whether RunLoop is currently executing; a supervisor
	// that is only used for synchronous ScanOnce calls stays false.
	LoopRunning bool
	// Scans / Decisions / Enforced are cumulative since process start.
	Scans     int64
	Decisions int64
	Enforced  int64
	// LastScan* describe the most recent ScanOnce call, including failures.
	LastScanAt        time.Time
	LastScanError     string
	LastScanDecisions []RunDecision
	// LastDispatch* describe the most recent completion-outbox flush.
	LastDispatchAt        time.Time
	LastDispatchDelivered int
	LastDispatchFailed    int
	// Retention is the effective terminal-run retention window and
	// LastPrune* describe the most recent retention GC pass (plan C4-2).
	Retention        time.Duration
	PrunedTotal      int64
	LastPruneAt      time.Time
	LastPruneRemoved int64
	LastPruneError   string
}

// executionSupervisorState is the mutex-guarded counter block behind Stats.
type executionSupervisorState struct {
	loopRunning    bool
	scans          int64
	decisions      int64
	enforced       int64
	lastScanAt     time.Time
	lastScanError  string
	lastDecisions  []RunDecision
	lastDispatchAt time.Time
	delivered      int
	failed         int
	lastPruneAt    time.Time
	prunedTotal    int64
	lastPruned     int64
	lastPruneError string
}

// ExecutionSupervisor is the P3 child run watchdog: durable run records,
// deadline tracking, observe/enforce decision scan, interrupt + cancel grace,
// and the terminal completion outbox (doc 5.2 Durable Execution Supervisor).
type ExecutionSupervisor struct {
	Store       ExecutionRunStore
	StoreFull   Store // lifecycle projection store (same SQLite store)
	Wakes       *WakeScheduler
	Config      ExecutionSupervisorConfig
	Interrupter RunInterrupter
	Dispatcher  CompletionDispatcher
	// ParentRunnable reports whether the parent that owns a run can be woken
	// right now (I8, design doc EC-A3). The decision window does not burn down
	// while it returns false, bounded by the wall-clock cap
	// (ProgressDeadlineAt + DecisionWindowMax). It is the same predicate the
	// wake consumer uses (see ParentRunnable), so hosts hand both surfaces the
	// same closure; execution runs carry no team id, so the team argument is
	// empty. Nil means "always runnable", which degrades to plain wall-clock
	// measurement.
	ParentRunnable ParentRunnable
	// Now is injectable for tests. Nil uses time.Now().UTC().
	Now func() time.Time

	statsMu sync.Mutex
	stats   executionSupervisorState
}

// StartRun creates a durable run record and returns it. run_id is generated
// here (doc 7.1: run_<uuid>), and all effective deadline values are resolved
// and persisted so config changes cannot rewrite history (doc 10 rule 4).
func (s *ExecutionSupervisor) StartRun(ctx context.Context, spec RunSpec) (*ExecutionRun, error) {
	if s == nil || s.Store == nil {
		return nil, fmt.Errorf("execution supervisor store is required")
	}
	cfg := s.effectiveConfig()
	now := s.now()
	spec.SessionID = strings.TrimSpace(spec.SessionID)
	if spec.SessionID == "" {
		return nil, fmt.Errorf("session id is required")
	}
	run := ExecutionRun{
		RunID:               generateRunID(),
		Kind:                strings.TrimSpace(spec.Kind),
		Workflow:            strings.TrimSpace(spec.Workflow),
		RootSessionID:       strings.TrimSpace(spec.RootSessionID),
		ParentSessionID:     strings.TrimSpace(spec.ParentSessionID),
		ParentRunID:         strings.TrimSpace(spec.ParentRunID),
		SessionID:           spec.SessionID,
		AgentID:             firstNonEmpty(spec.AgentID, spec.SessionID),
		Attempt:             1,
		Status:              RunStatusQueued,
		OwnerID:             strings.TrimSpace(spec.OwnerID),
		TurnID:              strings.TrimSpace(spec.TurnID),
		DeclaredBudget:      spec.DeclaredBudget,
		DecisionWindowUntil: spec.DecisionWindowUntil,
		StartedAt:           now,
		LastHeartbeatAt:     now,
		LastProgressAt:      now,
		ProgressSeq:         0,
		MaxAttempts:         spec.MaxAttempts,
		FencingToken:        1,
		Version:             1,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	if run.Kind == "" {
		run.Kind = RunKindAgentRun
	}
	if run.Workflow == "" {
		run.Workflow = RunWorkflowSpawnAgent
	}
	if run.MaxAttempts <= 0 {
		run.MaxAttempts = 1
	}
	run.ExecutionDeadlineAt = resolveDeadline(spec.ExecutionTimeout, cfg.DefaultExecutionTimeout, cfg.AllowUnbounded, now)
	run.ProgressDeadlineAt = resolveDeadline(spec.ProgressTimeout, cfg.DefaultProgressTimeout, cfg.AllowUnbounded, now)
	run.ApprovalDeadlineAt = resolveDeadline(spec.ApprovalTimeout, cfg.DefaultApprovalTimeout, cfg.AllowUnbounded, now)
	if cfg.RequireExecutionDeadline && run.ExecutionDeadlineAt == nil {
		return nil, fmt.Errorf("%w: session=%s", ErrExecutionDeadlineRequired, run.SessionID)
	}
	created, err := s.Store.CreateExecutionRun(ctx, run)
	if err != nil {
		return nil, err
	}
	if !created {
		return nil, fmt.Errorf("run_id collision: %s", run.RunID)
	}
	return &run, nil
}

// RecordProgress records meaningful execution progress (doc 5.4). Events from
// response deltas, tool call start/end, approval/input transitions, and
// session end map here; supervisor scans and wait timeouts never do.
func (s *ExecutionSupervisor) RecordProgress(ctx context.Context, event RunProgressEvent) (bool, error) {
	if s == nil || s.Store == nil {
		return false, fmt.Errorf("execution supervisor store is required")
	}
	return s.Store.RecordExecutionProgress(ctx, event, s.now())
}

// CompleteRun maps a child terminal event to a supervision terminal status
// (doc 7.3 mapping table), writes the terminal transition idempotently, enqueues
// the completion outbox with the idempotency key
// "subagent_completion:<run_id>:<version>", and projects the terminal change
// to the parent lifecycle inbox.
func (s *ExecutionSupervisor) CompleteRun(ctx context.Context, runID, status, errorCode, resultRef string, payload interface{}) error {
	if s == nil || s.Store == nil {
		return fmt.Errorf("execution supervisor store is required")
	}
	runID = strings.TrimSpace(runID)
	status = strings.TrimSpace(status)
	now := s.now()
	ok, err := s.Store.MarkExecutionRunTerminal(ctx, runID, status, errorCode, resultRef, now)
	if err != nil {
		return fmt.Errorf("mark run terminal: %w", err)
	}
	run, err := s.Store.GetExecutionRun(ctx, runID)
	if err != nil {
		if ok {
			run = &ExecutionRun{RunID: runID, Status: status}
		} else {
			return err
		}
	}
	payloadJSON := "{}"
	if payload != nil {
		payloadJSON, err = MarshalOutboxPayloadJSON(payload)
		if err != nil {
			return fmt.Errorf("marshal completion payload: %w", err)
		}
	}
	version := run.Version
	if version <= 0 {
		version = 1
	}
	entry := CompletionOutboxEntry{
		OutboxID:        "outbox_" + runID + "_" + status,
		RunID:           runID,
		SessionID:       run.SessionID,
		ParentSessionID: run.ParentSessionID,
		RootSessionID:   run.RootSessionID,
		Status:          status,
		IdempotencyKey:  fmt.Sprintf("subagent_completion:%s:%d", runID, version),
		PayloadJSON:     payloadJSON,
		CreatedAt:       now,
	}
	if _, err := s.Store.EnqueueCompletionOutbox(ctx, entry); err != nil {
		return fmt.Errorf("enqueue completion outbox: %w", err)
	}
	s.projectTerminal(ctx, run, status, errorCode)
	return nil
}

// ScanOnce runs one decision pass over active runs (doc 5.5 health matrix)
// and returns the decisions taken. It also retries undelivered completion
// outbox entries.
func (s *ExecutionSupervisor) ScanOnce(ctx context.Context) ([]RunDecision, error) {
	if s == nil || s.Store == nil {
		return nil, fmt.Errorf("execution supervisor store is required")
	}
	now := s.now()
	runs, err := s.Store.ListActiveExecutionRuns(ctx, 200)
	if err != nil {
		s.recordScan(now, nil, err)
		return nil, err
	}
	var decisions []RunDecision
	for i := range runs {
		run := runs[i]
		decision := s.evaluateRun(ctx, &run, now)
		if decision != nil {
			decisions = append(decisions, *decision)
		}
	}
	var dispatchErr error
	if s.Dispatcher != nil {
		if err := s.DispatchPendingOutbox(ctx); err != nil {
			dispatchErr = err
		}
	}
	s.recordScan(now, decisions, dispatchErr)
	// Retention GC rides the scan (plan C4-2 / §6.12): it is opportunistic and
	// bounded, and a prune failure must not mask a healthy decision pass, so it
	// is recorded in Stats instead of replacing the scan result.
	s.pruneRetention(ctx, now)
	return decisions, dispatchErr
}

// RunLoop runs ScanOnce on the configured interval until ctx is canceled.
// Store failures back off by StoreOutageGrace instead of spinning.
func (s *ExecutionSupervisor) RunLoop(ctx context.Context) {
	if s == nil {
		return
	}
	s.setLoopRunning(true)
	defer s.setLoopRunning(false)
	cfg := s.effectiveConfig()
	interval := cfg.ScanInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	backoff := cfg.StoreOutageGrace
	if backoff <= 0 {
		backoff = 2 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := s.ScanOnce(ctx); err != nil {
				// Back off on store outage so a dead store does not spin.
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
			}
		}
	}
}

// DispatchPendingOutbox delivers undelivered completion outbox entries and
// records the parent mailbox sequence (doc 7.4).
func (s *ExecutionSupervisor) DispatchPendingOutbox(ctx context.Context) error {
	if s == nil || s.Store == nil || s.Dispatcher == nil {
		return nil
	}
	entries, err := s.Store.ListUndeliveredOutbox(ctx, 100)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	now := s.now()
	delivered, failed := 0, 0
	for _, entry := range entries {
		seq, err := s.Dispatcher.DispatchCompletion(ctx, entry)
		if err != nil {
			_, _ = s.Store.MarkOutboxFailed(ctx, entry.OutboxID, err.Error(), now)
			failed++
			continue
		}
		_, _ = s.Store.MarkOutboxDelivered(ctx, entry.OutboxID, seq, now)
		delivered++
	}
	s.recordDispatch(now, delivered, failed)
	return nil
}

// Stats returns a point-in-time snapshot of the watchdog configuration and
// counters for operator surfaces (/debug). A nil receiver yields the zero
// snapshot instead of panicking so callers can render "not wired" directly.
func (s *ExecutionSupervisor) Stats() ExecutionSupervisorStats {
	if s == nil {
		return ExecutionSupervisorStats{}
	}
	cfg := s.effectiveConfig()
	s.statsMu.Lock()
	state := s.stats
	s.statsMu.Unlock()
	decisions := make([]RunDecision, len(state.lastDecisions))
	copy(decisions, state.lastDecisions)
	return ExecutionSupervisorStats{
		Enabled:               cfg.Enabled,
		Mode:                  cfg.Mode,
		ScanInterval:          cfg.ScanInterval,
		ExecutionTimeout:      cfg.DefaultExecutionTimeout,
		ProgressTimeout:       cfg.DefaultProgressTimeout,
		ApprovalTimeout:       cfg.DefaultApprovalTimeout,
		CancelGrace:           cfg.DefaultCancelGrace,
		StoreOutageGrace:      cfg.StoreOutageGrace,
		LoopRunning:           state.loopRunning,
		Scans:                 state.scans,
		Decisions:             state.decisions,
		Enforced:              state.enforced,
		LastScanAt:            state.lastScanAt,
		LastScanError:         state.lastScanError,
		LastScanDecisions:     decisions,
		LastDispatchAt:        state.lastDispatchAt,
		LastDispatchDelivered: state.delivered,
		LastDispatchFailed:    state.failed,
		Retention:             cfg.ExecutionRunRetention,
		PrunedTotal:           state.prunedTotal,
		LastPruneAt:           state.lastPruneAt,
		LastPruneRemoved:      state.lastPruned,
		LastPruneError:        state.lastPruneError,
	}
}

// maxSupervisorStatsDecisions bounds the retained last-scan decisions so a
// stalled population of runs cannot grow the debug snapshot without limit.
const maxSupervisorStatsDecisions = 5

func (s *ExecutionSupervisor) recordScan(at time.Time, decisions []RunDecision, err error) {
	if s == nil {
		return
	}
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	s.stats.scans++
	s.stats.lastScanAt = at
	s.stats.lastScanError = ""
	if err != nil {
		s.stats.lastScanError = err.Error()
	}
	s.stats.decisions += int64(len(decisions))
	kept := decisions
	if len(kept) > maxSupervisorStatsDecisions {
		kept = kept[:maxSupervisorStatsDecisions]
	}
	s.stats.lastDecisions = append([]RunDecision(nil), kept...)
	for _, decision := range decisions {
		if strings.TrimSpace(decision.ActionTaken) != "" && decision.ActionTaken != "none_observe" {
			s.stats.enforced++
		}
	}
}

func (s *ExecutionSupervisor) recordDispatch(at time.Time, delivered, failed int) {
	if s == nil {
		return
	}
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	s.stats.lastDispatchAt = at
	s.stats.delivered = delivered
	s.stats.failed = failed
}

// executionRunPruneInterval throttles the retention GC. The retention window
// is measured in days, so running the bounded DELETE on every scan tick would
// be pure overhead; GC stays opportunistic and rides the first scan after the
// interval elapses.
const executionRunPruneInterval = time.Hour

// pruneRetention removes one bounded batch of retention-expired terminal runs
// (plan C4-2 / design §6.12). It never touches the rows the "must not affect"
// list protects — the store enforces those guards inside the DELETE — and it
// deliberately does not fail the scan: GC is opportunistic bookkeeping, while
// the scan's return value is the decision result the caller acts on.
func (s *ExecutionSupervisor) pruneRetention(ctx context.Context, now time.Time) {
	if s == nil || s.Store == nil || !s.pruneDue(now) {
		return
	}
	cfg := s.effectiveConfig()
	removed, err := s.Store.PruneExecutionRuns(ctx, ExecutionRunPrunePolicy{
		Now:       now,
		Retention: cfg.ExecutionRunRetention,
		Limit:     cfg.ExecutionRunPruneLimit,
	})
	s.recordPrune(now, removed, err)
}

func (s *ExecutionSupervisor) pruneDue(now time.Time) bool {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	if s.stats.lastPruneAt.IsZero() {
		return true
	}
	return now.Sub(s.stats.lastPruneAt) >= executionRunPruneInterval
}

func (s *ExecutionSupervisor) recordPrune(at time.Time, removed int64, err error) {
	if s == nil {
		return
	}
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	s.stats.lastPruneAt = at
	s.stats.lastPruned = removed
	s.stats.prunedTotal += removed
	s.stats.lastPruneError = ""
	if err != nil {
		s.stats.lastPruneError = err.Error()
	}
}

func (s *ExecutionSupervisor) setLoopRunning(running bool) {
	if s == nil {
		return
	}
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	s.stats.loopRunning = running
}

// evaluateRun applies the health matrix to a single active run and, for states
// the matrix cannot decide, the I10 watchdog fallback (design doc §16.4).
func (s *ExecutionSupervisor) evaluateRun(ctx context.Context, run *ExecutionRun, now time.Time) *RunDecision {
	decision, handled := s.evaluateRunLadder(ctx, run, now)
	if handled {
		return decision
	}
	return s.watchdogForceTerminal(ctx, run, now)
}

// evaluateRunLadder applies the health matrix to a single active run. The
// second return value reports whether the ladder judged this run: a handled run
// whose tier intentionally does nothing (the escalate-first silent/piggyback
// bands and the open decision window) must not fall through to the I10
// watchdog, otherwise the watchdog would force-terminal exactly the runs the
// escalation ladder is still watching.
func (s *ExecutionSupervisor) evaluateRunLadder(ctx context.Context, run *ExecutionRun, now time.Time) (*RunDecision, bool) {
	status := strings.TrimSpace(run.Status)
	decision := &RunDecision{
		RunID:     run.RunID,
		SessionID: run.SessionID,
		Status:    status,
	}

	// Cancel grace in flight: run was asked to stop but is still active past
	// its cancel deadline -> fence as orphaned (doc 7.3 step 6).
	if status == RunStatusCancelRequested || status == RunStatusCanceling {
		if run.CancelDeadlineAt != nil && !run.CancelDeadlineAt.IsZero() && !now.Before(*run.CancelDeadlineAt) {
			decision.Decision = "cancel_grace_expired"
			decision.Reason = "interrupt sent but run did not finish within cancel grace"
			if s.enforce() {
				if s.fenceOrphaned(ctx, run, now) {
					decision.ActionTaken = "orphaned"
				} else {
					decision.ActionTaken = "cancel_requested"
				}
			} else {
				decision.ActionTaken = "none_observe"
			}
			s.projectDecision(ctx, run, decision)
			return decision, true
		}
		return nil, false
	}

	// waiting_approval / waiting_input use their own deadline and must never
	// be killed by the ordinary progress timeout (doc 5.5 blocked but healthy).
	if status == RunStatusWaitingApproval || status == RunStatusWaitingInput {
		if run.ApprovalDeadlineAt != nil && !run.ApprovalDeadlineAt.IsZero() && !now.Before(*run.ApprovalDeadlineAt) {
			decision.Decision = "approval_timeout"
			decision.Reason = "approval/input deadline expired"
		} else {
			return nil, false
		}
	} else {
		switch {
		case run.ExecutionDeadlineAt != nil && !run.ExecutionDeadlineAt.IsZero() && !now.Before(*run.ExecutionDeadlineAt):
			decision.Decision = "execution_timed_out"
			decision.Reason = "execution deadline expired"
		case run.ProgressDeadlineAt != nil && !run.ProgressDeadlineAt.IsZero() && !now.Before(*run.ProgressDeadlineAt):
			if s.escalateFirstEnabled() {
				// Change #1: the soft threshold no longer cancels. It reports to
				// the parent and gives it a decision window before the fallback
				// fires (plan §6.3 judge -> report -> decide -> fallback).
				return s.evaluateProgressStall(ctx, run, now)
			}
			decision.Decision = "progress_stalled"
			decision.Reason = "no meaningful progress since progress deadline"
		default:
			// A window that expired while the run is no longer stalled can only
			// come from a concurrent extend_deadline (change #2): retire it so
			// the next stall escalates afresh instead of firing the fallback
			// without a report.
			s.retireExpiredDecisionWindow(ctx, run, now)
			// Orphan suspicion: owner lease expired (heartbeat stale). P3 only
			// observes and projects; enforcement requires host lease/session
			// confirmation and lands with the reclaim work in P4.
			if run.OwnerLeaseUntil != nil && !run.OwnerLeaseUntil.IsZero() && now.After(*run.OwnerLeaseUntil) {
				decision.Decision = "orphan_suspected"
				decision.Reason = "owner lease expired"
				decision.ActionTaken = "none_observe"
				s.projectDecision(ctx, run, decision)
				return decision, true
			}
			return nil, false
		}
	}

	if s.enforce() {
		return s.enforceRunCancel(ctx, run, decision, decision.Decision, now), true
	}
	decision.ActionTaken = "none_observe"
	s.projectDecision(ctx, run, decision)
	return decision, true
}

// enforceRunCancel applies the forced branch shared by the hard deadline and
// the escalate-first fallback: request cancel (CAS), interrupt the live actor
// and project the decision. source is the cancel source persisted on the run;
// it differs from decision.Decision for the fallback, where the condition is a
// stall but the cause of the forced cancel is the expired decision window.
func (s *ExecutionSupervisor) enforceRunCancel(ctx context.Context, run *ExecutionRun, decision *RunDecision, source string, now time.Time) *RunDecision {
	grace := s.effectiveConfig().DefaultCancelGrace
	if grace <= 0 {
		grace = 15 * time.Second
	}
	requested, err := s.Store.RequestExecutionCancel(ctx, run.RunID, source, grace, now)
	if err != nil {
		decision.Reason = decision.Reason + "; cancel request failed: " + err.Error()
		return decision
	}
	if !requested {
		// Another scanner/host already requested cancel; hand off.
		decision.ActionTaken = "cancel_requested"
		s.projectDecision(ctx, run, decision)
		return decision
	}
	decision.ActionTaken = "cancel_requested"
	run.Status = RunStatusCancelRequested
	run.CancelSource = source
	if s.Interrupter != nil {
		if err := s.Interrupter.InterruptRun(ctx, *run); err == nil {
			decision.ActionTaken = "interrupted"
		}
	}
	s.projectDecision(ctx, run, decision)
	return decision
}

// progressStalled reports whether the run is past its progress deadline, i.e.
// the soft threshold of the escalation ladder has fired.
func progressStalled(run *ExecutionRun, now time.Time) bool {
	return run != nil && run.ProgressDeadlineAt != nil && !run.ProgressDeadlineAt.IsZero() && !now.Before(*run.ProgressDeadlineAt)
}

// evaluateProgressStall implements the escalate-first soft-threshold ladder
// (change #1; plan §6.3, Q2/Q3, I8). The soft threshold (the progress deadline)
// is already crossed when this is called, so only the tiers above it are
// decided here, with soft = the run's progress-timeout duration:
//
//	piggyback  now < deadline + (multiplier-1) x soft -> nothing (the parent
//	              picks the stall up on its next natural turn; no wake is spent)
//	report     now >= that escalation instant -> escalated: critical +
//	              action_required, never a cancel
//	decide     decision window still open     -> nothing (no re-report/cancel)
//	fallback   window expired, no decision    -> forced cancel with
//	              CancelSource=decision_window_expired
//
// The hard execution deadline keeps its own branch above this one, so a run
// that is both stalled and past its hard deadline is still cut immediately
// (AC-P0-1d).
func (s *ExecutionSupervisor) evaluateProgressStall(ctx context.Context, run *ExecutionRun, now time.Time) (*RunDecision, bool) {
	cfg := s.effectiveConfig()
	soft := progressSoftThreshold(run, cfg)
	// The soft threshold itself is the progress deadline; the multiplier buys
	// one more soft window of piggybacking before the parent is bothered.
	escalateAt := run.ProgressDeadlineAt.Add(time.Duration((cfg.StallEscalationMultiplier - 1) * float64(soft)))
	if now.Before(escalateAt) {
		// Piggyback band: the stall is visible to the operator (alerts) and to
		// the parent's next turn, but it does not justify a wake or a cancel.
		return nil, true
	}
	if run.DecisionWindowUntil != nil && !run.DecisionWindowUntil.IsZero() {
		return s.evaluateDecisionWindow(ctx, run, now)
	}
	return s.escalateProgressStall(ctx, run, now)
}

// escalateProgressStall opens the decision window for a newly detected stall
// (the "report" tier). It never cancels: the parent owns the decision for the
// length of the window and the fallback only fires once the window is exhausted.
func (s *ExecutionSupervisor) escalateProgressStall(ctx context.Context, run *ExecutionRun, now time.Time) (*RunDecision, bool) {
	decision := &RunDecision{
		RunID:     run.RunID,
		SessionID: run.SessionID,
		Status:    run.Status,
		Decision:  "progress_stalled",
		Reason:    "progress stalled past the escalation threshold; waiting for an extend/cancel decision",
	}
	// Re-read before mutating: RecordExecutionProgress does not bump the row
	// version, so a full-row CAS from the scan snapshot could revert progress
	// that arrived after the scan listed this run. The fresh copy also decides
	// whether the stall is still current (AC-P0-1b).
	current, err := s.Store.GetExecutionRun(ctx, run.RunID)
	if err != nil {
		decision.Reason = "escalation deferred: " + err.Error()
		return decision, true
	}
	if current == nil || current.Terminal() {
		return nil, true
	}
	*run = *current
	if !progressStalled(run, now) {
		return nil, true
	}
	if run.DecisionWindowUntil != nil && !run.DecisionWindowUntil.IsZero() {
		// Another scanner/host opened the window between the scan snapshot and
		// the re-read: it owns the report, so stay quiet.
		return nil, true
	}
	if !s.enforce() {
		// Observe mode never mutates: the escalation is projected (and would
		// open the window in enforce mode) but no window is written.
		decision.ActionTaken = "none_observe"
		s.projectDecision(ctx, run, decision)
		return decision, true
	}
	until := now.Add(effectiveDecisionWindow(s.effectiveConfig()))
	updated := *run
	updated.DecisionWindowUntil = &until
	ok, err := s.Store.UpdateExecutionRunCAS(ctx, updated, run.Version)
	if err != nil {
		decision.Reason = "escalation deferred: " + err.Error()
		return decision, true
	}
	if !ok {
		// Lost the CAS: another scanner/host already escalated this run (or
		// moved it on). Adopt the winner's state instead of reporting twice.
		if latest, err := s.Store.GetExecutionRun(ctx, run.RunID); err == nil && latest != nil {
			*run = *latest
		}
		return nil, true
	}
	run.DecisionWindowUntil = &until
	run.Version = updated.Version
	decision.ActionTaken = "escalated"
	s.projectDecision(ctx, run, decision)
	return decision, true
}

// evaluateDecisionWindow handles the decide/fallback tiers of a run whose
// escalation window is already open.
func (s *ExecutionSupervisor) evaluateDecisionWindow(ctx context.Context, run *ExecutionRun, now time.Time) (*RunDecision, bool) {
	until := *run.DecisionWindowUntil
	if now.Before(until) {
		// Decide tier: the parent still owns the decision. Re-projecting every
		// scan would spam the inbox and burn the wake budget, so stay quiet.
		return nil, true
	}
	if !progressStalled(run, now) {
		// The window outlived its stall (a concurrent extend_deadline moved the
		// deadline, change #2): retire it so the next stall escalates afresh
		// instead of firing the fallback without a report.
		s.retireDecisionWindow(ctx, run)
		return nil, true
	}
	cfg := s.effectiveConfig()
	if !decisionWindowForEpisode(run, cfg) {
		// The window predates the current progress deadline, so it belongs to an
		// earlier stall episode: an extend_deadline moved the deadline since it
		// was opened (change #2, EC-A5). Retire it and let the next scan escalate
		// afresh instead of cancelling without a report.
		s.retireDecisionWindow(ctx, run)
		return nil, true
	}
	if s.deferDecisionWindow(ctx, run, now, cfg) {
		return nil, true
	}
	decision := &RunDecision{
		RunID:     run.RunID,
		SessionID: run.SessionID,
		Status:    run.Status,
		Decision:  "progress_stalled",
		Reason:    "decision window expired without an extend/cancel decision",
	}
	if s.enforce() {
		return s.enforceRunCancel(ctx, run, decision, "decision_window_expired", now), true
	}
	decision.ActionTaken = "none_observe"
	s.projectDecision(ctx, run, decision)
	return decision, true
}

// deferDecisionWindow implements the runnable-clock rule (I8, design doc
// EC-A3): while the parent cannot be woken (user input in flight, waiting
// approval, compacting) the decision window must not burn down, otherwise the
// fallback would fire exactly because nobody was able to look at the
// escalation. The window is pushed to now+W but never past the wall-clock cap
// ProgressDeadlineAt + DecisionWindowMax (Q2), so the fallback stays decidable
// even when the parent never becomes runnable again.
//
// It reports whether the window was (or would be, in observe mode) deferred.
// A nil ParentRunnable means "always runnable", which degrades to plain
// wall-clock measurement.
func (s *ExecutionSupervisor) deferDecisionWindow(ctx context.Context, run *ExecutionRun, now time.Time, cfg ExecutionSupervisorConfig) bool {
	if s == nil || run == nil || s.ParentRunnable == nil {
		return false
	}
	if s.ParentRunnable(ctx, run.RootSessionID, run.ParentSessionID, "") {
		return false
	}
	anchor := run.ProgressDeadlineAt
	if anchor == nil || anchor.IsZero() {
		return false
	}
	cap := anchor.Add(cfg.DecisionWindowMax)
	if !now.Before(cap) {
		return false
	}
	until := now.Add(effectiveDecisionWindow(cfg))
	if until.After(cap) {
		until = cap
	}
	if !until.After(*run.DecisionWindowUntil) {
		return true
	}
	if !s.enforce() {
		// Observe mode never mutates; staying silent keeps the dry run honest.
		return true
	}
	updated := *run
	updated.DecisionWindowUntil = &until
	ok, err := s.Store.UpdateExecutionRunCAS(ctx, updated, run.Version)
	if err != nil || !ok {
		// Losing the CAS is not a reason to fire the fallback: stay silent for
		// this scan and retry. The cap check above still bounds the deferral, so
		// a persistently failing store cannot postpone the fallback forever.
		return true
	}
	run.DecisionWindowUntil = &until
	run.Version = updated.Version
	return true
}

// decisionWindowForEpisode reports whether the open window was created for the
// run's current stall episode. The supervisor stamps a window at or after the
// progress deadline it escalated on, so an extend_deadline that moved the
// deadline (change #2) is detectable afterwards: the window start (until - W)
// then predates the current deadline, and the window must not be allowed to
// fire the fallback for a stall it never reported (EC-A5).
func decisionWindowForEpisode(run *ExecutionRun, cfg ExecutionSupervisorConfig) bool {
	if run == nil || run.DecisionWindowUntil == nil || run.DecisionWindowUntil.IsZero() {
		return false
	}
	if run.ProgressDeadlineAt == nil || run.ProgressDeadlineAt.IsZero() {
		return true
	}
	start := run.DecisionWindowUntil.Add(-effectiveDecisionWindow(cfg))
	return !start.Before(*run.ProgressDeadlineAt)
}

// retireDecisionWindow clears an escalation window that outlived its stall so
// the next stall escalates afresh instead of firing the fallback without a
// report. Callers only retire windows that already expired (or that provably
// belong to an earlier episode), so a window a dispatcher declared for a
// healthy run (RunSpec.DecisionWindowUntil) is left alone.
func (s *ExecutionSupervisor) retireDecisionWindow(ctx context.Context, run *ExecutionRun) {
	if s == nil || s.Store == nil || run == nil || !s.enforce() {
		return
	}
	if run.DecisionWindowUntil == nil || run.DecisionWindowUntil.IsZero() {
		return
	}
	updated := *run
	updated.DecisionWindowUntil = nil
	ok, err := s.Store.UpdateExecutionRunCAS(ctx, updated, run.Version)
	if err != nil || !ok {
		return
	}
	run.DecisionWindowUntil = nil
	run.Version = updated.Version
}

// retireExpiredDecisionWindow retires a window that has already expired while
// the run is no longer stalled (extend_deadline moved the deadline).
func (s *ExecutionSupervisor) retireExpiredDecisionWindow(ctx context.Context, run *ExecutionRun, now time.Time) {
	if run == nil || run.DecisionWindowUntil == nil || run.DecisionWindowUntil.IsZero() {
		return
	}
	if now.Before(*run.DecisionWindowUntil) {
		return
	}
	s.retireDecisionWindow(ctx, run)
}

// fenceOrphaned bumps the fencing token and marks the run orphaned so late
// writes can no longer win (doc 5.4 fencing rules).
func (s *ExecutionSupervisor) fenceOrphaned(ctx context.Context, run *ExecutionRun, now time.Time) bool {
	return s.fenceRun(ctx, run, "cancel_grace_expired", true, now)
}

// fenceRun is the shared fencing primitive (doc 5.4): it bumps the run's
// fencing token, writes the terminal verdict through the version CAS and
// converges the run's live-condition alerts. keepExistingSource preserves an
// earlier cancel source (the cancel-grace path reports the cancel that was
// already in flight); the restart path records its own verdict instead.
func (s *ExecutionSupervisor) fenceRun(ctx context.Context, run *ExecutionRun, source string, keepExistingSource bool, now time.Time) bool {
	current, err := s.Store.GetExecutionRun(ctx, run.RunID)
	if err != nil {
		return false
	}
	if current.Terminal() {
		return true
	}
	fenced := *current
	fenced.FencingToken = current.FencingToken + 1
	fenced.Status = RunStatusOrphaned
	fenced.FinishedAt = &now
	if keepExistingSource {
		fenced.CancelSource = firstNonEmpty(current.CancelSource, strings.TrimSpace(source))
	} else {
		fenced.CancelSource = firstNonEmpty(strings.TrimSpace(source), current.CancelSource)
	}
	ok, err := s.Store.UpdateExecutionRunCAS(ctx, fenced, current.Version)
	if err != nil {
		return false
	}
	if !ok {
		// CAS lost: re-check terminal state before giving up.
		again, err := s.Store.GetExecutionRun(ctx, run.RunID)
		return err == nil && again.Terminal()
	}
	run.FencingToken = fenced.FencingToken
	run.Status = fenced.Status
	run.FinishedAt = fenced.FinishedAt
	run.CancelSource = fenced.CancelSource
	// The run is terminal through fencing, so its live-condition alerts are
	// stale exactly as in projectTerminal (plan §4.3).
	s.convergeRunAlerts(ctx, &fenced, RunStatusOrphaned)
	return true
}

// C4-3 / AC-P3-3b：宿主重启时的"恢复 or orphaned"决策。
//
// 重启后账本里可能留着上一个进程的非终态 run。判据是 **worker 存活**：心跳 /
// 更新时间仍在宽限期内的行按"可恢复"保留（另一个宿主可能还活着，或本进程刚接手，
// 这就是恢复分支）；宽限期外仍无心跳的行不可恢复 —— 记 `orphaned`（终态）+ 提升
// fencing token + 投影 critical 告警，晚到写入因 version / token 已前进而被 CAS
// 拒绝（EC-B9）。
const (
	// DefaultRestartReconcileGrace mirrors the host startup recovery grace: a
	// run quieter than this cannot belong to a live worker.
	DefaultRestartReconcileGrace = 5 * time.Minute
	// DefaultRestartReconcileLimit bounds one restart pass.
	DefaultRestartReconcileLimit = 512
	// RunCancelSourceRestartUnrecoverable records why a restarted host fenced a
	// run: no live worker after the restart grace.
	RunCancelSourceRestartUnrecoverable = "restart_unrecoverable"
)

// RestartReconcilePolicy configures one restart pass.
type RestartReconcilePolicy struct {
	// Now is the decision clock. Zero uses the supervisor clock.
	Now time.Time
	// StaleAfter is the liveness grace. Zero uses DefaultRestartReconcileGrace.
	StaleAfter time.Duration
	// Limit bounds the pass. Zero uses DefaultRestartReconcileLimit.
	Limit int
	// Reason overrides the recorded CancelSource.
	Reason string
	// Recoverable is an optional host predicate: true means the host already
	// knows how to resume this run (for example its turn still has a durable
	// parked record), so the pass must keep it even with a stale heartbeat.
	Recoverable func(run *ExecutionRun) bool
}

// RestartReconcileReport summarizes one restart pass.
type RestartReconcileReport struct {
	Scanned        int
	Recovered      int
	Orphaned       int
	Failed         int
	Observed       int
	OrphanedRunIDs []string
}

// ReconcileRestart decides, once per host startup, which non-terminal runs the
// previous incarnation left behind can be recovered and which must be fenced as
// orphaned. It is bounded, CAS-guarded and idempotent: a second pass finds the
// fenced rows terminal and reports them as recovered without a second
// transition.
func (s *ExecutionSupervisor) ReconcileRestart(ctx context.Context, policy RestartReconcilePolicy) (RestartReconcileReport, error) {
	var report RestartReconcileReport
	if s == nil || s.Store == nil {
		return report, fmt.Errorf("execution supervisor store is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := policy.Now
	if now.IsZero() {
		now = s.now()
	}
	grace := policy.StaleAfter
	if grace <= 0 {
		grace = DefaultRestartReconcileGrace
	}
	limit := policy.Limit
	if limit <= 0 {
		limit = DefaultRestartReconcileLimit
	}
	reason := strings.TrimSpace(policy.Reason)
	if reason == "" {
		reason = RunCancelSourceRestartUnrecoverable
	}
	runs, err := s.Store.ListActiveExecutionRuns(ctx, limit)
	if err != nil {
		return report, err
	}
	enforce := s.enforce()
	for i := range runs {
		run := runs[i]
		report.Scanned++
		if policy.Recoverable != nil && policy.Recoverable(&run) {
			// The host can resume this run (its parked turn record is still
			// there): keep the ledger row and let the resume path drive it.
			report.Recovered++
			continue
		}
		last := run.LastHeartbeatAt
		if run.UpdatedAt.After(last) {
			last = run.UpdatedAt
		}
		if last.Add(grace).After(now) {
			// Inside the grace window: another host may still own this run, or
			// this process just took over. Recovery keeps it untouched.
			report.Recovered++
			continue
		}
		if !enforce {
			report.Observed++
			continue
		}
		if !s.fenceRun(ctx, &run, reason, false, now) {
			report.Failed++
			continue
		}
		report.Orphaned++
		report.OrphanedRunIDs = append(report.OrphanedRunIDs, run.RunID)
		s.projectRestartOrphan(ctx, &run, reason)
	}
	return report, nil
}

// projectRestartOrphan makes a restart verdict visible instead of ledger-only:
// an unrecoverable run fenced by a restarted host is critical + action_required
// for the parent that owns it (same contract as every non-healthy decision).
func (s *ExecutionSupervisor) projectRestartOrphan(ctx context.Context, run *ExecutionRun, reason string) {
	if s == nil || s.StoreFull == nil || run == nil {
		return
	}
	_, _ = ProjectLifecycle(ctx, s.StoreFull, s.Wakes, LifecycleProjection{
		RootScopeID:           run.RootSessionID,
		TargetParentSessionID: run.ParentSessionID,
		SubjectKind:           SubjectAgentRun,
		SubjectID:             run.RunID,
		EventType:             "run_orphaned",
		Severity:              SeverityCritical,
		SupervisionState:      SupervisionOrphaned,
		Reason:                reason,
		RecommendedAction:     "inspect run and decide cancel/retry",
	})
}

// watchdogForceTerminal is the I10 fallback (design doc §16.4): a run that is
// still active but whose decidable deadline is missing can never be judged
// terminal by the ordinary ladder, which would leave the parent turn parked
// forever. The watchdog judges it terminal in the same scan cycle and records
// the cancel source.
//
// It only runs for states evaluateRunLadder could not decide, so every path the
// ladder already handles (hard deadline -> interrupt -> cancel grace ->
// orphaned, approval timeout, healthy runs) keeps its exact behavior and is
// never double-judged (AC-C0-2d).
func (s *ExecutionSupervisor) watchdogForceTerminal(ctx context.Context, run *ExecutionRun, now time.Time) *RunDecision {
	status, source, reason := watchdogForcedTerminal(run)
	if status == "" {
		return nil
	}
	decision := &RunDecision{
		RunID:     run.RunID,
		SessionID: run.SessionID,
		Status:    run.Status,
		Decision:  source,
		Reason:    reason,
	}
	if s.enforce() {
		if s.forceTerminalRun(ctx, run, status, source, now) {
			decision.ActionTaken = "forced_terminal"
		} else {
			decision.ActionTaken = "cancel_requested"
		}
	} else {
		// Observe mode never mutates, but the forced judgment is still
		// projected so the operator sees what enforcement would have done.
		decision.ActionTaken = "none_observe"
	}
	// A forced judgment must never be silent: critical + unresolved makes the
	// parent treat it as action_required (design doc §16.4 rule 3).
	s.projectDecision(ctx, run, decision)
	return decision
}

// watchdogForcedTerminal maps an undecidable active run to the terminal status,
// cancel source and reason the watchdog must write. It returns empty values for
// every state that still has a usable deadline.
func watchdogForcedTerminal(run *ExecutionRun) (status, source, reason string) {
	if run == nil || run.Terminal() {
		return "", "", ""
	}
	switch strings.TrimSpace(run.Status) {
	case RunStatusCancelRequested, RunStatusCanceling:
		// Cancel was requested but no cancel deadline was persisted, so the
		// ladder's cancel_grace_expired branch can never fire.
		if run.CancelDeadlineAt == nil || run.CancelDeadlineAt.IsZero() {
			return RunStatusOrphaned, "cancel_deadline_missing",
				"cancel requested without a cancel deadline; forced terminal to keep join decidable"
		}
	case RunStatusWaitingApproval, RunStatusWaitingInput:
		// Blocked states are governed by their own deadline; without it the run
		// can wait forever.
		if run.ApprovalDeadlineAt == nil || run.ApprovalDeadlineAt.IsZero() {
			return RunStatusAbandoned, "approval_deadline_missing",
				"waiting state without an approval deadline; forced terminal to keep join decidable"
		}
	default:
		if run.ExecutionDeadlineAt == nil || run.ExecutionDeadlineAt.IsZero() {
			return RunStatusAbandoned, "deadline_missing",
				"run has no execution deadline; forced terminal to keep join decidable"
		}
	}
	return "", "", ""
}

// forceTerminalRun writes the watchdog's terminal judgment with the same CAS +
// fencing discipline as every other ledger write. It is idempotent: a run that
// is already terminal (for example because the hard threshold judged it first)
// is reported as written without a second transition.
func (s *ExecutionSupervisor) forceTerminalRun(ctx context.Context, run *ExecutionRun, status, source string, now time.Time) bool {
	if s == nil || s.Store == nil || run == nil {
		return false
	}
	current, err := s.Store.GetExecutionRun(ctx, run.RunID)
	if err != nil {
		return false
	}
	if current.Terminal() {
		return true
	}
	fenced := *current
	fenced.Status = status
	// The forced judgment's own source is recorded (instead of preserving an
	// earlier cancel source) so the ledger answers "why did this run become
	// terminal" with the watchdog verdict that actually decided it.
	fenced.CancelSource = firstNonEmpty(strings.TrimSpace(source), strings.TrimSpace(current.CancelSource))
	finished := now
	fenced.FinishedAt = &finished
	fenced.FencingToken = current.FencingToken + 1
	ok, err := s.Store.UpdateExecutionRunCAS(ctx, fenced, current.Version)
	if err != nil {
		return false
	}
	if !ok {
		// CAS lost: report success only when a concurrent writer already made
		// the run terminal, otherwise the decision stays an observation.
		again, err := s.Store.GetExecutionRun(ctx, run.RunID)
		return err == nil && again.Terminal()
	}
	run.Status = fenced.Status
	run.CancelSource = fenced.CancelSource
	run.FinishedAt = fenced.FinishedAt
	run.FencingToken = fenced.FencingToken
	// The forced judgment is a terminal transition, so the run's live-condition
	// alerts are stale by definition (same contract as projectTerminal).
	s.convergeRunAlerts(ctx, &fenced, status)
	return true
}

// projectDecision writes the lifecycle inbox notification for a non-healthy
// decision (doc 5.5: every non-healthy verdict must be idempotently
// projected).
func (s *ExecutionSupervisor) projectDecision(ctx context.Context, run *ExecutionRun, decision *RunDecision) {
	if s == nil || s.StoreFull == nil || run == nil || decision == nil {
		return
	}
	severity := SeverityCritical
	state := SupervisionCancelRequested
	switch decision.Decision {
	case "orphan_suspected":
		state = SupervisionOrphaned
		severity = SeverityCritical
	case "execution_timed_out", "approval_timeout":
		state = SupervisionTimedOut
	case "progress_stalled":
		state = SupervisionStalled
	case "cancel_grace_expired":
		state = SupervisionOrphaned
	}
	_, _ = ProjectLifecycle(ctx, s.StoreFull, s.Wakes, LifecycleProjection{
		RootScopeID:           run.RootSessionID,
		TargetParentSessionID: run.ParentSessionID,
		SubjectKind:           SubjectAgentRun,
		SubjectID:             run.RunID,
		EventType:             decision.Decision,
		Severity:              severity,
		SupervisionState:      state,
		Reason:                decision.Reason,
		RecommendedAction:     "inspect run and decide cancel/retry",
	})
}

// projectTerminal projects a terminal transition to the lifecycle inbox.
func (s *ExecutionSupervisor) projectTerminal(ctx context.Context, run *ExecutionRun, status, errorCode string) {
	if s == nil || s.StoreFull == nil || run == nil {
		return
	}
	severity := SeverityInfo
	state := SupervisionTerminated
	switch status {
	case RunStatusTimedOut:
		severity = SeverityCritical
		state = SupervisionTimedOut
	case RunStatusFailed, RunStatusOrphaned:
		severity = SeverityCritical
		state = SupervisionTerminated
	case RunStatusCanceled:
		severity = SeverityInfo
		state = SupervisionTerminated
	default:
		state = SupervisionTerminated
	}
	reason := "child run completed"
	if errorCode != "" {
		reason = "child run completed with error: " + errorCode
	}
	_, _ = ProjectLifecycle(ctx, s.StoreFull, s.Wakes, LifecycleProjection{
		RootScopeID:           run.RootSessionID,
		TargetParentSessionID: run.ParentSessionID,
		SubjectKind:           SubjectAgentRun,
		SubjectID:             run.RunID,
		EventType:             "run_" + status,
		Severity:              severity,
		SupervisionState:      state,
		Reason:                reason,
	})
	// Plan §4.3: the run reached a terminal state, so the alerts it projected
	// while live (progress_stalled and friends) are stale by definition. Without
	// this the parent keeps seeing a critical/action-required row for work that
	// already finished.
	s.convergeRunAlerts(ctx, run, status)
}

// convergeRunAlerts is the best-effort hook that retires a run's live-condition
// alerts once the run itself is terminal. A failure only leaves the stale row
// behind for the next scan or parent turn; the terminal projection is already
// durable, so it must not fail the completion path.
func (s *ExecutionSupervisor) convergeRunAlerts(ctx context.Context, run *ExecutionRun, status string) {
	if s == nil || s.StoreFull == nil || run == nil {
		return
	}
	_, _ = ConvergeRunAlerts(ctx, s.StoreFull, run.RootSessionID, run.RunID, status, s.now())
}

func (s *ExecutionSupervisor) enforce() bool {
	cfg := s.effectiveConfig()
	return strings.EqualFold(strings.TrimSpace(cfg.Mode), "enforce")
}

func (s *ExecutionSupervisor) effectiveConfig() ExecutionSupervisorConfig {
	cfg := s.Config
	defaults := DefaultExecutionSupervisorConfig()
	if cfg.ScanInterval <= 0 {
		cfg.ScanInterval = defaults.ScanInterval
	}
	if cfg.DefaultExecutionTimeout <= 0 && !cfg.AllowUnbounded {
		cfg.DefaultExecutionTimeout = defaults.DefaultExecutionTimeout
	}
	if cfg.DefaultProgressTimeout <= 0 && !cfg.AllowUnbounded {
		cfg.DefaultProgressTimeout = defaults.DefaultProgressTimeout
	}
	if cfg.DefaultApprovalTimeout <= 0 && !cfg.AllowUnbounded {
		cfg.DefaultApprovalTimeout = defaults.DefaultApprovalTimeout
	}
	if cfg.DefaultCancelGrace <= 0 {
		cfg.DefaultCancelGrace = defaults.DefaultCancelGrace
	}
	if cfg.StoreOutageGrace <= 0 {
		cfg.StoreOutageGrace = defaults.StoreOutageGrace
	}
	// escalate-first (change #1): zero values take the documented defaults, and
	// the decision window is derived from the *effective* progress timeout so a
	// host that only overrides that timeout keeps the documented 2x ratio (Q2).
	if cfg.StallEscalationMultiplier <= 0 {
		cfg.StallEscalationMultiplier = defaults.StallEscalationMultiplier
	}
	if cfg.DecisionWindow <= 0 {
		if cfg.DefaultProgressTimeout > 0 {
			cfg.DecisionWindow = 2 * cfg.DefaultProgressTimeout
		} else {
			cfg.DecisionWindow = defaults.DecisionWindow
		}
	}
	if cfg.DecisionWindowMax <= 0 {
		cfg.DecisionWindowMax = 2 * cfg.DecisionWindow
	}
	// C4-2 retention GC: zero keeps the documented defaults so a host that
	// never configures the knobs still prunes terminal rows.
	if cfg.ExecutionRunRetention <= 0 {
		cfg.ExecutionRunRetention = defaults.ExecutionRunRetention
	}
	if cfg.ExecutionRunPruneLimit <= 0 {
		cfg.ExecutionRunPruneLimit = defaults.ExecutionRunPruneLimit
	}
	return cfg
}

// escalateFirstEnabled reports whether the soft-threshold branch escalates
// before it enforces (change #1; plan §6.3). nil means on: the gray-release
// default is the new ladder, and only an explicit false restores the pre-#1
// forced cancel.
func (s *ExecutionSupervisor) escalateFirstEnabled() bool {
	if s == nil {
		return false
	}
	cfg := s.effectiveConfig()
	return cfg.EscalateFirst == nil || *cfg.EscalateFirst
}

// effectiveDecisionWindow returns the decision window actually used by the
// escalation ladder: W, capped by DecisionWindowMax so a stale configuration
// can never open a window longer than the documented wall-clock bound (Q2).
func effectiveDecisionWindow(cfg ExecutionSupervisorConfig) time.Duration {
	window := cfg.DecisionWindow
	if window <= 0 {
		window = DefaultConfig().DecisionWindow
	}
	if cfg.DecisionWindowMax > 0 && window > cfg.DecisionWindowMax {
		return cfg.DecisionWindowMax
	}
	return window
}

// progressSoftThreshold returns the run's progress-timeout duration, i.e. the
// soft threshold the escalation multiplier scales (Q3). It is derived from the
// persisted deadline minus StartedAt so it reflects the timeout the run was
// actually admitted with (a per-run ProgressTimeout wins over the operator
// default) and survives progress events, which move LastProgressAt but never
// the deadline. An extend_deadline (change #2) moves the deadline, so the soft
// threshold grows with the granted extension - the multiplier then buys that
// much more piggybacking before the parent is bothered again.
func progressSoftThreshold(run *ExecutionRun, cfg ExecutionSupervisorConfig) time.Duration {
	if run != nil && run.ProgressDeadlineAt != nil && !run.ProgressDeadlineAt.IsZero() && !run.StartedAt.IsZero() {
		if declared := run.ProgressDeadlineAt.Sub(run.StartedAt); declared > 0 {
			return declared
		}
	}
	if cfg.DefaultProgressTimeout > 0 {
		return cfg.DefaultProgressTimeout
	}
	return DefaultConfig().HeartbeatTimeout
}

func (s *ExecutionSupervisor) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

// resolveDeadline resolves the effective deadline for one timeout dimension:
// explicit spec value wins; zero falls back to the operator default; explicit
// zero means unbounded only when allowUnbounded is set (doc 10 rule 5 / 7.2
// rule 2).
func resolveDeadline(spec, fallback time.Duration, allowUnbounded bool, now time.Time) *time.Time {
	value := spec
	if value == 0 {
		if allowUnbounded {
			return nil
		}
		value = fallback
	}
	if value <= 0 {
		return nil
	}
	deadline := now.Add(value)
	return &deadline
}

// generateRunID builds a durable run id (doc 7.1: run_<uuid>).
func generateRunID() string {
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	return "run_" + time.Now().UTC().Format("20060102150405") + "_" + hex.EncodeToString(suffix)
}
