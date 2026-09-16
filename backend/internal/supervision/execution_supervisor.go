package supervision

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

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
	// StoreOutageGrace is the tolerated store failure window before the
	// scanner backs off (doc 10 retention/grace).
	StoreOutageGrace time.Duration
}

// DefaultExecutionSupervisorConfig returns the documented defaults (doc 10):
// enforce mode, 30m execution timeout, 5m progress timeout, 1h approval
// timeout, 15s cancel grace, 5s scan interval.
func DefaultExecutionSupervisorConfig() ExecutionSupervisorConfig {
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
	}
}

// RunSpec is the host-neutral input for starting a supervised run (doc 5.3 /
// 7.1). Zero durations fall back to the supervisor defaults; explicit zero
// means unbounded only when AllowUnbounded is configured.
type RunSpec struct {
	Kind             string
	Workflow         string
	RootSessionID    string
	ParentSessionID  string
	ParentRunID      string
	SessionID        string
	AgentID          string
	OwnerID          string
	ExecutionTimeout time.Duration
	ProgressTimeout  time.Duration
	ApprovalTimeout  time.Duration
	CancelGrace      time.Duration
	MaxAttempts      int
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
	ActionTaken string // none_observe | cancel_requested | interrupted | orphaned
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
		RunID:           generateRunID(),
		Kind:            strings.TrimSpace(spec.Kind),
		Workflow:        strings.TrimSpace(spec.Workflow),
		RootSessionID:   strings.TrimSpace(spec.RootSessionID),
		ParentSessionID: strings.TrimSpace(spec.ParentSessionID),
		ParentRunID:     strings.TrimSpace(spec.ParentRunID),
		SessionID:       spec.SessionID,
		AgentID:         firstNonEmpty(spec.AgentID, spec.SessionID),
		Attempt:         1,
		Status:          RunStatusQueued,
		OwnerID:         strings.TrimSpace(spec.OwnerID),
		StartedAt:       now,
		LastHeartbeatAt: now,
		LastProgressAt:  now,
		ProgressSeq:     0,
		MaxAttempts:     spec.MaxAttempts,
		FencingToken:    1,
		Version:         1,
		CreatedAt:       now,
		UpdatedAt:       now,
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

func (s *ExecutionSupervisor) setLoopRunning(running bool) {
	if s == nil {
		return
	}
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	s.stats.loopRunning = running
}

// evaluateRun applies the health matrix to a single active run.
func (s *ExecutionSupervisor) evaluateRun(ctx context.Context, run *ExecutionRun, now time.Time) *RunDecision {
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
			return decision
		}
		return nil
	}

	// waiting_approval / waiting_input use their own deadline and must never
	// be killed by the ordinary progress timeout (doc 5.5 blocked but healthy).
	if status == RunStatusWaitingApproval || status == RunStatusWaitingInput {
		if run.ApprovalDeadlineAt != nil && !run.ApprovalDeadlineAt.IsZero() && !now.Before(*run.ApprovalDeadlineAt) {
			decision.Decision = "approval_timeout"
			decision.Reason = "approval/input deadline expired"
		} else {
			return nil
		}
	} else {
		switch {
		case run.ExecutionDeadlineAt != nil && !run.ExecutionDeadlineAt.IsZero() && !now.Before(*run.ExecutionDeadlineAt):
			decision.Decision = "execution_timed_out"
			decision.Reason = "execution deadline expired"
		case run.ProgressDeadlineAt != nil && !run.ProgressDeadlineAt.IsZero() && !now.Before(*run.ProgressDeadlineAt):
			decision.Decision = "progress_stalled"
			decision.Reason = "no meaningful progress since progress deadline"
		default:
			// Orphan suspicion: owner lease expired (heartbeat stale). P3 only
			// observes and projects; enforcement requires host lease/session
			// confirmation and lands with the reclaim work in P4.
			if run.OwnerLeaseUntil != nil && !run.OwnerLeaseUntil.IsZero() && now.After(*run.OwnerLeaseUntil) {
				decision.Decision = "orphan_suspected"
				decision.Reason = "owner lease expired"
				decision.ActionTaken = "none_observe"
				s.projectDecision(ctx, run, decision)
				return decision
			}
			return nil
		}
	}

	if s.enforce() {
		grace := s.effectiveConfig().DefaultCancelGrace
		if grace <= 0 {
			grace = 15 * time.Second
		}
		requested, err := s.Store.RequestExecutionCancel(ctx, run.RunID, decision.Decision, grace, now)
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
		run.CancelSource = decision.Decision
		if s.Interrupter != nil {
			if err := s.Interrupter.InterruptRun(ctx, *run); err == nil {
				decision.ActionTaken = "interrupted"
			}
		}
	} else {
		decision.ActionTaken = "none_observe"
	}
	s.projectDecision(ctx, run, decision)
	return decision
}

// fenceOrphaned bumps the fencing token and marks the run orphaned so late
// writes can no longer win (doc 5.4 fencing rules).
func (s *ExecutionSupervisor) fenceOrphaned(ctx context.Context, run *ExecutionRun, now time.Time) bool {
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
	fenced.CancelSource = firstNonEmpty(current.CancelSource, "cancel_grace_expired")
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
	// The run is terminal through fencing, so its live-condition alerts are
	// stale exactly as in projectTerminal (plan §4.3).
	s.convergeRunAlerts(ctx, &fenced, RunStatusOrphaned)
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
	return cfg
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
