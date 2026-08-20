// Package subagentbatch defines the durable Batch/Task model, state machine
// and storage contract for asynchronous spawn_subagents background batches.
//
// This package is host-neutral: it has no dependency on the agent runtime, so
// the same durable lifecycle can be driven by an in-process worker, an
// orchestrator or a future runtime-server controller. Design contract:
// docs/plan/spawn-subagents-async-supervisor-plan.md section 4.3/6.
package subagentbatch

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SchemaVersion is the lifecycle event schema version emitted by this package.
const SchemaVersion = "subagent.batch.v1"

// --- Execution modes ---

// ExecutionMode controls how a batch couples to the parent ReAct turn.
type ExecutionMode string

const (
	// ExecutionModeWait preserves the legacy synchronous semantics: the parent
	// tool call blocks until the whole batch returns full reports.
	ExecutionModeWait ExecutionMode = "wait"
	// ExecutionModeBackground returns a batch handle immediately; child
	// lifecycle is delivered later through durable records and supervision
	// wake-up.
	ExecutionModeBackground ExecutionMode = "background"
)

// ParseExecutionMode validates and normalizes a mode string; empty defaults to
// wait (compatibility).
func ParseExecutionMode(value string) (ExecutionMode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "wait", "sync":
		return ExecutionModeWait, nil
	case "background", "async":
		return ExecutionModeBackground, nil
	default:
		return "", fmt.Errorf("subagentbatch: invalid execution_mode %q (expected wait|background)", value)
	}
}

// --- Batch status vocabulary ---

// BatchStatus is the durable batch lifecycle state.
type BatchStatus string

const (
	BatchQueued             BatchStatus = "queued"
	BatchRunning            BatchStatus = "running"
	BatchPartiallyCompleted BatchStatus = "partially_completed"
	BatchCompleted          BatchStatus = "completed"
	BatchFailed             BatchStatus = "failed"
	BatchCanceled           BatchStatus = "canceled"
	BatchTimedOut           BatchStatus = "timed_out"
	BatchOrphaned           BatchStatus = "orphaned"
)

// Terminal reports whether a batch status can no longer transition.
func (s BatchStatus) Terminal() bool {
	switch s {
	case BatchCompleted, BatchFailed, BatchCanceled, BatchTimedOut, BatchOrphaned:
		return true
	default:
		return false
	}
}

// --- Task status vocabulary ---

// TaskStatus is the durable per-task lifecycle state.
type TaskStatus string

const (
	TaskPending   TaskStatus = "pending"
	TaskReady     TaskStatus = "ready"
	TaskRunning   TaskStatus = "running"
	TaskSucceeded TaskStatus = "succeeded"
	TaskFailed    TaskStatus = "failed"
	TaskCanceled  TaskStatus = "canceled"
	TaskTimedOut  TaskStatus = "timed_out"
	TaskSkipped   TaskStatus = "skipped"
)

// Terminal reports whether a task status can no longer transition.
func (s TaskStatus) Terminal() bool {
	switch s {
	case TaskSucceeded, TaskFailed, TaskCanceled, TaskTimedOut, TaskSkipped:
		return true
	default:
		return false
	}
}

// --- Portable task spec (no agent runtime dependency) ---

// PatchSpec is the durable, portable representation of one applied/requested
// patch produced by a writer subagent.
type PatchSpec struct {
	Path               string   `json:"path,omitempty"`
	Diff               string   `json:"diff,omitempty"`
	Summary            string   `json:"summary,omitempty"`
	ApplyStatus        string   `json:"apply_status,omitempty"`
	AppliedBy          []string `json:"applied_by,omitempty"`
	VerificationStatus string   `json:"verification_status,omitempty"`
	VerifiedBy         []string `json:"verified_by,omitempty"`
	ArtifactRefs       []string `json:"artifact_refs,omitempty"`
}

// TaskSpec is the durable snapshot of one child task definition. Prompts and
// full tool arguments are intentionally NOT part of the lifecycle schema; they
// remain in the calling runtime.
type TaskSpec struct {
	ID                    string      `json:"id,omitempty"`
	Role                  string      `json:"role,omitempty"`
	Goal                  string      `json:"goal,omitempty"`
	Difficulty            string      `json:"difficulty,omitempty"`
	DifficultyRationale   string      `json:"difficulty_rationale,omitempty"`
	Provider              string      `json:"provider,omitempty"`
	Model                 string      `json:"model,omitempty"`
	ReasoningEffort       string      `json:"reasoning_effort,omitempty"`
	ToolsWhitelist        []string    `json:"tools_whitelist,omitempty"`
	DependsOn             []string    `json:"depends_on,omitempty"`
	Patches               []PatchSpec `json:"patches,omitempty"`
	BudgetTokens          int         `json:"budget_tokens,omitempty"`
	TimeoutSec            int         `json:"timeout_sec,omitempty"`
	ReadOnly              bool        `json:"read_only,omitempty"`
	CompletionRequirement string      `json:"completion_requirement,omitempty"`
}

// Epoch returns a displayable task id (stable for idempotency).
func (t TaskSpec) Epoch() string {
	return strings.TrimSpace(t.ID)
}

// --- Batch record ---

// SubagentBatch is the durable batch record (plan §4.3 SubagentBatch).
type SubagentBatch struct {
	BatchID          string        `json:"batch_id,omitempty"`
	RootScopeID      string        `json:"root_scope_id,omitempty"`
	ParentSessionID  string        `json:"parent_session_id,omitempty"`
	ParentTurnID     string        `json:"parent_turn_id,omitempty"`
	ParentToolCallID string        `json:"parent_tool_call_id,omitempty"`
	TraceID          string        `json:"trace_id,omitempty"`
	ExecutionMode    ExecutionMode `json:"execution_mode,omitempty"`
	Status           BatchStatus   `json:"status,omitempty"`
	IdempotencyKey   string        `json:"idempotency_key,omitempty"`

	TaskCount      int `json:"task_count,omitempty"`
	QueuedCount    int `json:"queued_count,omitempty"`
	RunningCount   int `json:"running_count,omitempty"`
	CompletedCount int `json:"completed_count,omitempty"`
	FailedCount    int `json:"failed_count,omitempty"`
	CanceledCount  int `json:"canceled_count,omitempty"`
	TimedOutCount  int `json:"timed_out_count,omitempty"`

	CreatedAt     time.Time  `json:"created_at,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	UpdatedAt     time.Time  `json:"updated_at,omitempty"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	BatchDeadline time.Time  `json:"batch_deadline,omitempty"`

	CancelRequestedAt *time.Time `json:"cancel_requested_at,omitempty"`
	CancelReason      string     `json:"cancel_reason,omitempty"`

	OwnerID      string    `json:"owner_id,omitempty"`
	FencingToken string    `json:"fencing_token,omitempty"`
	HeartbeatAt  time.Time `json:"heartbeat_at,omitempty"`

	ResultSummaryRef string `json:"result_summary_ref,omitempty"`
	// ResultSummary holds the inline, budget-bounded BatchSummary JSON when a
	// separate artifact is not configured.
	ResultSummary []byte `json:"-"`
	ErrorClass    string `json:"error_class,omitempty"`
	ErrorDetail   string `json:"error_detail,omitempty"`

	Version int64 `json:"version,omitempty"`
}

// NewID returns a durable batch id derived from a random nonce.
func NewID(prefix string) string {
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d", prefix, time.Now().UnixNano())))
		return prefix + "_" + hex.EncodeToString(sum[:8])
	}
	return prefix + "_" + hex.EncodeToString(nonce)
}

// --- Task record ---

// SubagentTaskRecord is the durable per-task record (plan §4.3 SubagentTaskRecord).
type SubagentTaskRecord struct {
	TaskID         string     `json:"task_id,omitempty"`
	BatchID        string     `json:"batch_id,omitempty"`
	ParentTaskID   string     `json:"parent_task_id,omitempty"`
	DependencyIDs  []string   `json:"dependency_ids,omitempty"`
	ChildSessionID string     `json:"child_session_id,omitempty"`
	Role           string     `json:"role,omitempty"`
	Difficulty     string     `json:"difficulty,omitempty"`
	ReadOnly       bool       `json:"read_only,omitempty"`
	Status         TaskStatus `json:"status,omitempty"`
	OrderIndex     int        `json:"order_index,omitempty"`
	Attempt        int        `json:"attempt,omitempty"`

	TaskDeadline   time.Time  `json:"task_deadline,omitempty"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	LastProgressAt *time.Time `json:"last_progress_at,omitempty"`

	Spec          []byte `json:"-"`
	ResultSummary []byte `json:"-"`
	ArtifactRef   string `json:"artifact_ref,omitempty"`
	ErrorClass    string `json:"error_class,omitempty"`
	ErrorCode     string `json:"error_code,omitempty"`
	Version       int64  `json:"version,omitempty"`
}

// SpecSummary returns a bounded, human/LLM digest of the task spec.
func (t SubagentTaskRecord) SpecSummary() string {
	return fmt.Sprintf("%s role=%s difficulty=%s read_only=%t budget=%d", t.TaskID, t.Role, t.Difficulty, t.ReadOnly, t.SpecBudgetTokens())
}

// SpecBudgetTokens extracts budget tokens from the encoded spec without
// decoding the whole payload.
func (t SubagentTaskRecord) SpecBudgetTokens() int {
	if len(t.Spec) == 0 {
		return 0
	}
	var spec TaskSpec
	if err := json.Unmarshal(t.Spec, &spec); err != nil {
		return 0
	}
	return spec.BudgetTokens
}

// --- Result capsule ---

// TaskResult is the durable, budget-bounded result capsule stored per task.
// Full child output must be referenced (ArtifactRef), never embedded here by
// default.
type TaskResult struct {
	TaskID      string      `json:"task_id,omitempty"`
	Role        string      `json:"role,omitempty"`
	SessionID   string      `json:"session_id,omitempty"`
	Success     bool        `json:"success"`
	Summary     string      `json:"summary,omitempty"`
	Findings    []string    `json:"findings,omitempty"`
	Patches     []PatchSpec `json:"patches,omitempty"`
	Error       string      `json:"error,omitempty"`
	UsageTotal  int         `json:"usage_total,omitempty"`
	ArtifactRef string      `json:"artifact_ref,omitempty"`
}

// BatchSummary is the coalesced digest used at batch terminal time and for
// parent preflight injection (plan §5.4).
type BatchSummary struct {
	BatchID        string            `json:"batch_id,omitempty"`
	Status         BatchStatus       `json:"status,omitempty"`
	TaskCount      int               `json:"task_count,omitempty"`
	CompletedCount int               `json:"completed_count,omitempty"`
	FailedCount    int               `json:"failed_count,omitempty"`
	CanceledCount  int               `json:"canceled_count,omitempty"`
	TimedOutCount  int               `json:"timed_out_count,omitempty"`
	ElapsedMillis  int64             `json:"elapsed_ms,omitempty"`
	ErrorClass     string            `json:"error_class,omitempty"`
	CriticalErrors []string          `json:"critical_errors,omitempty"`
	TaskStatuses   map[string]string `json:"task_statuses,omitempty"`
	CreatedAt      time.Time         `json:"created_at,omitempty"`
	FinishedAt     time.Time         `json:"finished_at,omitempty"`
}

// Counts returns the cohort counts for a set of task records.
func Counts(tasks []SubagentTaskRecord) (queued, running, completed, failed, canceled, timedOut int) {
	for _, t := range tasks {
		switch t.Status {
		case TaskPending, TaskReady:
			queued++
		case TaskRunning:
			running++
		case TaskSucceeded:
			completed++
		case TaskFailed:
			failed++
		case TaskCanceled:
			canceled++
		case TaskTimedOut:
			timedOut++
		}
	}
	return
}
