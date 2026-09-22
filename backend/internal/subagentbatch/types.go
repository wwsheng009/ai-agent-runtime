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

	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/uniqid"
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
	// TaskFailedWithResult is the terminal state of a task that failed overall
	// but still produced a usable deliverable (a non-empty result capsule was
	// persisted before the failure). It exists so a parent never has to choose
	// between "status says failed" and "the payload is retrievable": the status
	// itself tells the parent to read the result instead of re-dispatching the
	// same work. Renderers that predate this enum must fall back to "failed"
	// (see supervision.ReadResultPayload guidance for the model-facing form).
	TaskFailedWithResult TaskStatus = "failed_with_result"
	TaskCanceled         TaskStatus = "canceled"
	TaskTimedOut         TaskStatus = "timed_out"
	TaskSkipped          TaskStatus = "skipped"
)

// Terminal reports whether a task status can no longer transition.
func (s TaskStatus) Terminal() bool {
	switch s {
	case TaskSucceeded, TaskFailed, TaskFailedWithResult, TaskCanceled, TaskTimedOut, TaskSkipped:
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
	ID   string `json:"id,omitempty"`
	Role string `json:"role,omitempty"`
	// TaskType/TaskSubject are the optional v4 routing fields (plan §6.4 B-1):
	// TaskType is the closed-enum routing category, TaskSubject is audit-only.
	// Role stays the orchestration axis; both fields are omitempty so specs
	// written before this change decode byte-identically.
	TaskType              string      `json:"task_type,omitempty"`
	TaskSubject           string      `json:"task_subject,omitempty"`
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
		// 熵源不可用时退化为 uniqid（时间戳+进程内序号+进程随机后缀）：
		// 纯 UnixNano 在同一时钟 tick 内会生成相同 batch id，durable 主键
		// 撞键后第二个批次会被静默丢弃。
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s", prefix, uniqid.Token())))
		return prefix + "_" + hex.EncodeToString(sum[:8])
	}
	return prefix + "_" + hex.EncodeToString(nonce)
}

// --- Task record ---

// SubagentTaskRecord is the durable per-task record (plan §4.3 SubagentTaskRecord).
type SubagentTaskRecord struct {
	TaskID         string   `json:"task_id,omitempty"`
	BatchID        string   `json:"batch_id,omitempty"`
	ParentTaskID   string   `json:"parent_task_id,omitempty"`
	DependencyIDs  []string `json:"dependency_ids,omitempty"`
	ChildSessionID string   `json:"child_session_id,omitempty"`
	Role           string   `json:"role,omitempty"`
	// TaskType/TaskSubject mirror the subagent_tasks.task_type/task_subject
	// columns (plan §6.4 B-2). Legacy rows migrated from the v1 schema read
	// back as empty strings, which keeps them byte-identical to today.
	TaskType    string     `json:"task_type,omitempty"`
	TaskSubject string     `json:"task_subject,omitempty"`
	Difficulty  string     `json:"difficulty,omitempty"`
	ReadOnly    bool       `json:"read_only,omitempty"`
	Status      TaskStatus `json:"status,omitempty"`
	OrderIndex  int        `json:"order_index,omitempty"`
	Attempt     int        `json:"attempt,omitempty"`

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
	TaskID string `json:"task_id,omitempty"`
	Role   string `json:"role,omitempty"`
	// TaskType/TaskSubject carry the routing category and audit subject of the
	// task that produced this capsule (plan §6.4 B-1). Optional; empty for
	// results produced before the field existed.
	TaskType    string      `json:"task_type,omitempty"`
	TaskSubject string      `json:"task_subject,omitempty"`
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
	BatchID        string      `json:"batch_id,omitempty"`
	Status         BatchStatus `json:"status,omitempty"`
	TaskCount      int         `json:"task_count,omitempty"`
	CompletedCount int         `json:"completed_count,omitempty"`
	FailedCount    int         `json:"failed_count,omitempty"`
	// FailedWithResultCount is a sub-count of FailedCount: tasks that failed
	// overall but whose result capsule is still retrievable. It is listed
	// separately so the parent can tell "lost work" from "work to pick up".
	FailedWithResultCount int               `json:"failed_with_result_count,omitempty"`
	CanceledCount         int               `json:"canceled_count,omitempty"`
	TimedOutCount         int               `json:"timed_out_count,omitempty"`
	ElapsedMillis         int64             `json:"elapsed_ms,omitempty"`
	ErrorClass            string            `json:"error_class,omitempty"`
	CriticalErrors        []string          `json:"critical_errors,omitempty"`
	TaskStatuses          map[string]string `json:"task_statuses,omitempty"`
	CreatedAt             time.Time         `json:"created_at,omitempty"`
	FinishedAt            time.Time         `json:"finished_at,omitempty"`
}

// TaskCounts is the full status breakdown of a task cohort. It is the single
// source of truth for every count that is surfaced to a parent or written into
// a batch row: read-side projections must derive counts from task rows through
// DeriveTaskCounts instead of trusting the batch count columns, which are only
// refreshed at batch creation and terminal convergence.
type TaskCounts struct {
	Total            int
	Queued           int
	Running          int
	Completed        int
	Failed           int
	FailedWithResult int
	Canceled         int
	TimedOut         int
	Skipped          int
}

// FailedTotal is the failure cohort the batch status decision cares about.
// failed_with_result is still a failure (the task did not do what was asked);
// it only differs in whether the payload survives.
func (c TaskCounts) FailedTotal() int { return c.Failed + c.FailedWithResult }

// Terminal returns the number of tasks that can no longer transition.
func (c TaskCounts) Terminal() int {
	return c.Completed + c.FailedTotal() + c.Canceled + c.TimedOut + c.Skipped
}

// DeriveTaskCounts counts every known status so that
// Queued+Running+Terminal always equals Total for a well-formed cohort. An
// unrecognized status is counted as queued rather than dropped: silently
// losing a row is exactly the drift this helper exists to prevent.
func DeriveTaskCounts(tasks []SubagentTaskRecord) TaskCounts {
	counts := TaskCounts{Total: len(tasks)}
	for _, t := range tasks {
		switch t.Status {
		case TaskRunning:
			counts.Running++
		case TaskSucceeded:
			counts.Completed++
		case TaskFailed:
			counts.Failed++
		case TaskFailedWithResult:
			counts.FailedWithResult++
		case TaskCanceled:
			counts.Canceled++
		case TaskTimedOut:
			counts.TimedOut++
		case TaskSkipped:
			counts.Skipped++
		default: // TaskPending, TaskReady, and any future non-terminal status.
			counts.Queued++
		}
	}
	return counts
}

// Counts returns the cohort counts for a set of task records. It is a
// compatibility wrapper over DeriveTaskCounts; new code should use
// DeriveTaskCounts and read the named fields.
func Counts(tasks []SubagentTaskRecord) (queued, running, completed, failed, canceled, timedOut int) {
	counts := DeriveTaskCounts(tasks)
	return counts.Queued, counts.Running, counts.Completed, counts.FailedTotal(), counts.Canceled, counts.TimedOut
}
