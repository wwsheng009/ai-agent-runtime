package supervision

import (
	"strings"
	"time"
)

// Execution run kinds (doc 5.3). P3 covers agent_run; team kinds are reserved
// for P4/P5 but the model already carries the shared fields.
const (
	RunKindAgentRun      = "agent_run"
	RunKindTeamLoop      = "team_loop"
	RunKindTeamTaskAttempt = "team_task_attempt"
)

// Execution run workflows.
const (
	RunWorkflowSpawnAgent = "spawn_agent"
	RunWorkflowSpawnTeam  = "spawn_team"
)

// Execution run statuses (doc 5.3 / 7.3 watchdog state machine).
const (
	RunStatusQueued          = "queued"
	RunStatusRunning         = "running"
	RunStatusWaitingApproval = "waiting_approval"
	RunStatusWaitingInput    = "waiting_input"
	RunStatusCancelRequested = "cancel_requested"
	RunStatusCanceling       = "canceling"
	RunStatusSucceeded       = "succeeded"
	RunStatusFailed          = "failed"
	RunStatusCanceled        = "canceled"
	RunStatusTimedOut        = "timed_out"
	RunStatusOrphaned        = "orphaned"
	RunStatusSuperseded      = "superseded"
)

// SupervisionPolicy is the default child run policy reported with the
// spawn result (doc 7.1).
const SupervisionPolicyDefault = "interrupt_then_fail"

// runTerminalStatuses lists statuses that no longer accept progress or
// deadline transitions. Terminal writes are idempotent (doc 5.3 rule 5).
var runTerminalStatuses = map[string]struct{}{
	RunStatusSucceeded:  {},
	RunStatusFailed:     {},
	RunStatusCanceled:   {},
	RunStatusTimedOut:   {},
	RunStatusOrphaned:   {},
	RunStatusSuperseded: {},
}

// RunStatusTerminal reports whether the status is a terminal supervision
// state.
func RunStatusTerminal(status string) bool {
	_, ok := runTerminalStatuses[strings.TrimSpace(status)]
	return ok
}

// RunStatusActive reports whether the status still accepts progress/heartbeat
// updates (queued..canceling are live; waiting_* are live but use their own
// deadline).
func RunStatusActive(status string) bool {
	status = strings.TrimSpace(status)
	if RunStatusTerminal(status) {
		return false
	}
	return status != "" && status != RunStatusInvalid
}

// RunStatusInvalid is the fallback for unknown status strings.
const RunStatusInvalid = "invalid"

// ExecutionRun is the durable execution record (doc 5.3). It is intentionally
// storage-neutral: the supervision store persists it, while the host keeps
// session/actor state as the execution container.
type ExecutionRun struct {
	RunID               string
	Kind                string
	Workflow            string
	RootSessionID       string
	ParentSessionID     string
	ParentRunID         string
	SessionID           string
	AgentID             string
	Attempt             int
	Status              string
	OwnerID             string
	OwnerLeaseUntil     *time.Time
	StartedAt           time.Time
	LastHeartbeatAt     time.Time
	LastProgressAt      time.Time
	ProgressSeq         int64
	ExecutionDeadlineAt *time.Time
	ProgressDeadlineAt  *time.Time
	ApprovalDeadlineAt  *time.Time
	CancelRequestedAt   *time.Time
	CancelDeadlineAt    *time.Time
	CancelSource        string
	FinishedAt          *time.Time
	MaxAttempts         int
	FencingToken        int64
	Version             int64
	ResultRef           string
	ErrorCode           string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// Normalize returns a stable ExecutionRun shape for storage and comparison.
func (r ExecutionRun) Normalize() ExecutionRun {
	r.RunID = strings.TrimSpace(r.RunID)
	r.Kind = strings.TrimSpace(r.Kind)
	if r.Kind == "" {
		r.Kind = RunKindAgentRun
	}
	r.Workflow = strings.TrimSpace(r.Workflow)
	r.RootSessionID = strings.TrimSpace(r.RootSessionID)
	r.ParentSessionID = strings.TrimSpace(r.ParentSessionID)
	r.ParentRunID = strings.TrimSpace(r.ParentRunID)
	r.SessionID = strings.TrimSpace(r.SessionID)
	r.AgentID = strings.TrimSpace(r.AgentID)
	r.OwnerID = strings.TrimSpace(r.OwnerID)
	r.Status = strings.TrimSpace(r.Status)
	if r.Status == "" {
		r.Status = RunStatusQueued
	}
	r.CancelSource = strings.TrimSpace(r.CancelSource)
	r.ResultRef = strings.TrimSpace(r.ResultRef)
	r.ErrorCode = strings.TrimSpace(r.ErrorCode)
	if r.Attempt <= 0 {
		r.Attempt = 1
	}
	if r.MaxAttempts <= 0 {
		r.MaxAttempts = 1
	}
	if r.Version <= 0 {
		r.Version = 1
	}
	return r
}

// Terminal reports whether the run has reached a terminal supervision state.
func (r ExecutionRun) Terminal() bool {
	return RunStatusTerminal(r.Status)
}

// Active reports whether the run still accepts progress updates.
func (r ExecutionRun) Active() bool {
	return RunStatusActive(r.Status)
}

// formatRunTime serializes a time for the SQLite TEXT columns.
func formatRunTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// formatRunTimePtr serializes an optional time.
func formatRunTimePtr(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// runTimeSQL returns a SQL parameter for an optional time: NULL when unset so
// `IS NULL` predicates match (empty strings would not).
func runTimeSQL(t *time.Time) interface{} {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// parseRunTime parses a time from the SQLite TEXT columns.
func parseRunTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}
	}
	return t
}

// parseRunTimePtr parses an optional time.
func parseRunTimePtr(raw string) *time.Time {
	t := parseRunTime(raw)
	if t.IsZero() {
		return nil
	}
	return &t
}
