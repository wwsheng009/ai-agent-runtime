package memorystore

import (
	"context"
	"time"
)

type EpisodeOutcome string

const (
	EpisodeOutcomeSuccess   EpisodeOutcome = "success"
	EpisodeOutcomeFailed    EpisodeOutcome = "failed"
	EpisodeOutcomePartial   EpisodeOutcome = "partial"
	EpisodeOutcomeBlocked   EpisodeOutcome = "blocked"
	EpisodeOutcomeCancelled EpisodeOutcome = "cancelled"
)

type Episode struct {
	ID             string         `json:"id"`
	MemoryID       string         `json:"memory_id"`
	TenantID       string         `json:"tenant_id,omitempty"`
	ProjectKey     string         `json:"project_key"`
	TaskID         string         `json:"task_id"`
	SessionID      string         `json:"session_id"`
	Problem        string         `json:"problem"`
	Actions        []string       `json:"actions"`
	Result         string         `json:"result,omitempty"`
	FailureReason  string         `json:"failure_reason,omitempty"`
	Outcome        EpisodeOutcome `json:"outcome"`
	StartedAt      time.Time      `json:"started_at,omitempty"`
	EndedAt        time.Time      `json:"ended_at,omitempty"`
	SourceEventIDs []string       `json:"source_event_ids"`
	CreatedAt      time.Time      `json:"created_at"`
}

type EpisodeInput struct {
	ID             string
	TenantID       string
	ProjectKey     string
	TaskID         string
	SessionID      string
	Problem        string
	Actions        []string
	Result         string
	FailureReason  string
	Outcome        EpisodeOutcome
	StartedAt      time.Time
	EndedAt        time.Time
	SourceEventIDs []string
}

type EpisodeFilter struct {
	TaskID    string
	SessionID string
	Outcome   EpisodeOutcome
	Limit     int
}

type PatchOperation string

const (
	PatchOperationAdd       PatchOperation = "add"
	PatchOperationUpdate    PatchOperation = "update"
	PatchOperationMerge     PatchOperation = "merge"
	PatchOperationSupersede PatchOperation = "supersede"
	PatchOperationDelete    PatchOperation = "delete"
	PatchOperationMoveScope PatchOperation = "move_scope"
	PatchOperationReindex   PatchOperation = "reindex"
)

type PatchRisk string

const (
	PatchRiskLow    PatchRisk = "low"
	PatchRiskMedium PatchRisk = "medium"
	PatchRiskHigh   PatchRisk = "high"
)

type PatchStatus string

const (
	PatchStatusProposed   PatchStatus = "proposed"
	PatchStatusApproved   PatchStatus = "approved"
	PatchStatusRejected   PatchStatus = "rejected"
	PatchStatusApplied    PatchStatus = "applied"
	PatchStatusRolledBack PatchStatus = "rolled_back"
	PatchStatusFailed     PatchStatus = "failed"
)

type MemoryPatch struct {
	ID                string                 `json:"id"`
	TenantID          string                 `json:"tenant_id,omitempty"`
	ProjectKey        string                 `json:"project_key"`
	ScopeKind         Scope                  `json:"scope_kind"`
	ScopeKey          string                 `json:"scope_key"`
	Operation         PatchOperation         `json:"operation"`
	TargetIDs         []string               `json:"target_ids,omitempty"`
	ProposedContent   string                 `json:"proposed_content,omitempty"`
	ProposedMetadata  map[string]interface{} `json:"proposed_metadata,omitempty"`
	Reason            string                 `json:"reason"`
	Evidence          []string               `json:"evidence"`
	RiskLevel         PatchRisk              `json:"risk_level"`
	Status            PatchStatus            `json:"status"`
	ExpectedRevisions map[string]int64       `json:"expected_revisions,omitempty"`
	AppliedRecordIDs  []string               `json:"applied_record_ids,omitempty"`
	AppliedRevisions  map[string]int64       `json:"applied_revisions,omitempty"`
	RollbackVersions  map[string]int64       `json:"rollback_versions,omitempty"`
	CreatedBy         string                 `json:"created_by"`
	ReviewedBy        string                 `json:"reviewed_by,omitempty"`
	ReviewNote        string                 `json:"review_note,omitempty"`
	Error             string                 `json:"error,omitempty"`
	CreatedAt         time.Time              `json:"created_at"`
	ReviewedAt        time.Time              `json:"reviewed_at,omitempty"`
	AppliedAt         time.Time              `json:"applied_at,omitempty"`
	RolledBackAt      time.Time              `json:"rolled_back_at,omitempty"`
}

type PatchInput struct {
	TenantID          string
	ProjectKey        string
	ScopeKind         Scope
	ScopeKey          string
	Operation         PatchOperation
	TargetIDs         []string
	ProposedContent   string
	ProposedMetadata  map[string]interface{}
	Reason            string
	Evidence          []string
	RiskLevel         PatchRisk
	ExpectedRevisions map[string]int64
	CreatedBy         string
}

type PatchFilter struct {
	Status PatchStatus
	Risk   PatchRisk
	Limit  int
}

type PatchApplyOptions struct {
	AutoApproveLowRisk bool
	Actor              string
}

type DreamRunStatus string

const (
	DreamRunQueued    DreamRunStatus = "queued"
	DreamRunRunning   DreamRunStatus = "running"
	DreamRunSucceeded DreamRunStatus = "succeeded"
	DreamRunFailed    DreamRunStatus = "failed"
	DreamRunPartial   DreamRunStatus = "partial"
)

type DreamRun struct {
	ID             string         `json:"id"`
	TenantID       string         `json:"tenant_id,omitempty"`
	ProjectKey     string         `json:"project_key"`
	Status         DreamRunStatus `json:"status"`
	WindowStart    time.Time      `json:"window_start,omitempty"`
	WindowEnd      time.Time      `json:"window_end,omitempty"`
	InputCount     int            `json:"input_count"`
	PatchCount     int            `json:"patch_count"`
	TokenCost      int            `json:"token_cost"`
	ReflectionText string         `json:"reflection_text,omitempty"`
	Model          string         `json:"model,omitempty"`
	Shadow         bool           `json:"shadow,omitempty"`
	Error          string         `json:"error,omitempty"`
	StartedAt      time.Time      `json:"started_at,omitempty"`
	FinishedAt     time.Time      `json:"finished_at,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
}

type DreamRunOptions struct {
	Limit     int
	CreatedBy string
	MaxTokens int
	Analyzer  DreamAnalyzer
	Shadow    bool
	Force     bool
}

// DreamAnalysis is one bounded, sanitized reflection produced from a set of
// durable Episodes. The store still owns token truncation and patch creation.
type DreamAnalysis struct {
	Text  string
	Model string
}

type DreamAnalyzer interface {
	AnalyzeDream(context.Context, []Episode) (DreamAnalysis, error)
}
