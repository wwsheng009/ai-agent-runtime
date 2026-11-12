package memorystore

import (
	"strings"
	"time"
)

// V2 long-term memory types. The legacy Note/JSONL surface remains in store.go
// so existing callers continue to compile and keep their behavior when
// Mode=off.

type Scope string

const (
	ScopeProject Scope = "project"
	ScopeUser    Scope = "user"
	ScopeProfile Scope = "profile"
)

type Status string

const (
	StatusCandidate  Status = "candidate"
	StatusActive     Status = "active"
	StatusSuperseded Status = "superseded"
	StatusDeleted    Status = "deleted"
	StatusExpired    Status = "expired"
)

// RetentionClasses mirrors memory.retention.classes. Kinds omitted from every
// list fall back to standard. "manual" is always permanent regardless of this
// mapping because user-authored notes must not be auto-expired.
type RetentionClasses struct {
	Permanent []string
	Standard  []string
	Ephemeral []string
}

// RetentionPolicy is the store-side lifecycle policy. Zero day/count values
// disable that dimension; eligible records are marked expired, not physically
// deleted, except for tombstones that have passed DeletedDays.
type RetentionPolicy struct {
	Enabled          bool
	CandidateDays    int
	EphemeralDays    int
	DeletedDays      int
	AuditDays        int
	MaxRecords       int
	ActiveUnusedDays int
	Classes          RetentionClasses
}

// RetentionSweepResult reports one bounded lifecycle pass.
type RetentionSweepResult struct {
	Reclassified       int64 `json:"reclassified"`
	CandidatesExpired  int64 `json:"candidates_expired"`
	ActiveExpired      int64 `json:"active_expired"`
	MaxRecordsExpired  int64 `json:"max_records_expired"`
	TombstonesDeleted  int64 `json:"tombstones_deleted"`
	EventsDeleted      int64 `json:"events_deleted"`
	UsageDeleted       int64 `json:"usage_deleted"`
	SuppressionsPurged int64 `json:"suppressions_purged"`
}

// Configured reports whether an explicit kind mapping was supplied.
func (c RetentionClasses) Configured() bool {
	return len(c.Permanent) > 0 || len(c.Standard) > 0 || len(c.Ephemeral) > 0
}

// ClassForKind returns permanent/standard/ephemeral for a record kind. Listed
// kinds win in that order; unlisted kinds fall back to standard.
func (c RetentionClasses) ClassForKind(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	for _, candidate := range c.Permanent {
		if strings.ToLower(strings.TrimSpace(candidate)) == kind {
			return "permanent"
		}
	}
	for _, candidate := range c.Ephemeral {
		if strings.ToLower(strings.TrimSpace(candidate)) == kind {
			return "ephemeral"
		}
	}
	for _, candidate := range c.Standard {
		if strings.ToLower(strings.TrimSpace(candidate)) == kind {
			return "standard"
		}
	}
	return "standard"
}

type Freshness string

const (
	FreshnessFresh      Freshness = "fresh"
	FreshnessStale      Freshness = "stale"
	FreshnessUnverified Freshness = "unverified"
)

type BlockKind string

const (
	BlockStable BlockKind = "stable"
	BlockRecall BlockKind = "recall"
)

type SessionMode string

const (
	SessionModeEnabled  SessionMode = "enabled"
	SessionModeDisabled SessionMode = "disabled"
	SessionModePolluted SessionMode = "polluted"
)

type Record struct {
	ID               string                 `json:"id"`
	Scope            Scope                  `json:"scope"`
	ProjectKey       string                 `json:"project_key"`
	Kind             string                 `json:"kind"`
	Status           Status                 `json:"status"`
	Text             string                 `json:"text"`
	NormalizedText   string                 `json:"normalized_text,omitempty"`
	Tags             []string               `json:"tags,omitempty"`
	Confidence       float64                `json:"confidence,omitempty"`
	Sensitivity      string                 `json:"sensitivity,omitempty"`
	Pinned           bool                   `json:"pinned"`
	RetentionClass   string                 `json:"retention_class,omitempty"`
	SourceKind       string                 `json:"source_kind,omitempty"`
	SourceSessionID  string                 `json:"source_session_id,omitempty"`
	SourceTurnID     string                 `json:"source_turn_id,omitempty"`
	SourceWindow     string                 `json:"source_window,omitempty"`
	SourceRefs       []string               `json:"source_refs,omitempty"`
	SourceHash       string                 `json:"source_hash,omitempty"`
	SourceRevision   int64                  `json:"source_revision,omitempty"`
	SourceUpdatedAt  time.Time              `json:"source_updated_at,omitempty"`
	SourceObservedAt time.Time              `json:"source_observed_at,omitempty"`
	SourceMessageIDs []string               `json:"source_message_ids,omitempty"`
	ContentHash      string                 `json:"content_hash,omitempty"`
	SupersedesID     string                 `json:"supersedes_id,omitempty"`
	Revision         int64                  `json:"revision"`
	CreatedAt        time.Time              `json:"created_at"`
	UpdatedAt        time.Time              `json:"updated_at"`
	LastUsedAt       time.Time              `json:"last_used_at,omitempty"`
	UseCount         int                    `json:"use_count,omitempty"`
	ValidFrom        time.Time              `json:"valid_from,omitempty"`
	ValidUntil       time.Time              `json:"valid_until,omitempty"`
	Freshness        Freshness              `json:"freshness_status"`
	LastVerifiedAt   time.Time              `json:"last_verified_at,omitempty"`
	RevalidateAfter  time.Time              `json:"revalidate_after,omitempty"`
	StaleReason      string                 `json:"stale_reason,omitempty"`
	DeletedAt        time.Time              `json:"deleted_at,omitempty"`
	Metadata         map[string]interface{} `json:"metadata,omitempty"`
}

type Summary struct {
	ID             string                 `json:"id"`
	Scope          Scope                  `json:"scope"`
	ProjectKey     string                 `json:"project_key"`
	SummaryKind    string                 `json:"summary_kind"`
	Revision       int64                  `json:"revision"`
	Status         string                 `json:"status"`
	Freshness      Freshness              `json:"freshness_status"`
	Text           string                 `json:"text"`
	TokenEstimate  int                    `json:"token_estimate"`
	SourceCount    int                    `json:"source_count"`
	InputWatermark string                 `json:"input_watermark,omitempty"`
	GeneratedAt    time.Time              `json:"generated_at"`
	Model          string                 `json:"model,omitempty"`
	ContentHash    string                 `json:"content_hash,omitempty"`
	RecordIDs      []string               `json:"record_ids,omitempty"`
	Metadata       map[string]interface{} `json:"metadata,omitempty"`
}

type DroppedRecord struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

type JobStatus string

const (
	JobStatusPending   JobStatus = "pending"
	JobStatusRunning   JobStatus = "running"
	JobStatusSucceeded JobStatus = "succeeded"
	JobStatusFailed    JobStatus = "failed"
	JobStatusCancelled JobStatus = "cancelled"
)

type MemoryJob struct {
	ID                   string    `json:"id"`
	Kind                 string    `json:"kind"`
	JobKey               string    `json:"job_key"`
	Scope                Scope     `json:"scope"`
	ProjectKey           string    `json:"project_key"`
	Status               JobStatus `json:"status"`
	Priority             int       `json:"priority"`
	WorkerID             string    `json:"worker_id,omitempty"`
	LeaseUntil           time.Time `json:"lease_until,omitempty"`
	Attempt              int       `json:"attempt"`
	MaxAttempts          int       `json:"max_attempts"`
	RetryAt              time.Time `json:"retry_at,omitempty"`
	StartedAt            time.Time `json:"started_at,omitempty"`
	FinishedAt           time.Time `json:"finished_at,omitempty"`
	LastError            string    `json:"last_error,omitempty"`
	InputWatermark       string    `json:"input_watermark,omitempty"`
	LastSuccessWatermark string    `json:"last_success_watermark,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type ClaimJobOptions struct {
	WorkerID   string
	Kind       string
	JobKey     string
	ProjectKey string
	Lease      time.Duration
	Limit      int
	Now        time.Time
}

type Candidate struct {
	SessionID        string
	TurnID           string
	SourceWindow     string
	SourceKind       string
	SourceRefs       []string
	ProjectKey       string
	Kind             string
	Text             string
	Tags             []string
	Confidence       float64
	Sensitivity      string
	SourceHash       string
	SourceRevision   int64
	SourceUpdatedAt  time.Time
	SourceMessageIDs []string
	RetentionClass   string
	RevalidateAfter  time.Time
	Model            string
	TokenUsage       map[string]interface{}
	Metadata         map[string]interface{}
}

type Selection struct {
	Summary        *Summary
	Stable         []Record
	Recall         []Record
	Revision       int64
	TokenEstimate  int
	IDs            []string
	Dropped        []DroppedRecord
	CandidateCount int
	FilteredCount  int
}

type RecordFilter struct {
	Scope            Scope
	ProjectKey       string
	Status           Status
	Kind             string
	Tag              string
	Query            string
	SourceSessionID  string
	IncludeCandidate bool
	Limit            int
	Cursor           string
}

type RecordPage struct {
	Items      []Record
	NextCursor string
	Count      int
}

type RecordPatch struct {
	Text   *string
	Tags   *[]string
	Pinned *bool
	Status *Status
}

type StatusReport struct {
	Mode                          string         `json:"mode"`
	Scope                         string         `json:"scope"`
	DBPath                        string         `json:"db_path,omitempty"`
	SchemaVersion                 int            `json:"schema_version"`
	Total                         int            `json:"total"`
	ByStatus                      map[string]int `json:"by_status,omitempty"`
	ByFreshness                   map[string]int `json:"by_freshness,omitempty"`
	SummaryRevision               int64          `json:"summary_revision,omitempty"`
	SummaryStatus                 string         `json:"summary_status,omitempty"`
	SummaryFreshness              string         `json:"summary_freshness,omitempty"`
	PinnedCount                   int            `json:"pinned_count,omitempty"`
	InjectionMode                 string         `json:"injection_mode,omitempty"`
	Jobs                          map[string]int `json:"jobs,omitempty"`
	TurnDigests                   map[string]int `json:"turn_digests,omitempty"`
	TurnWindows                   map[string]int `json:"turn_windows,omitempty"`
	OldestPendingDigestAgeSeconds int64          `json:"oldest_pending_digest_age_seconds,omitempty"`
	PendingDigestSessions         int            `json:"pending_digest_sessions,omitempty"`
	PendingDigestBacklogBatches   int            `json:"pending_digest_backlog_batches,omitempty"`
	RecoveredDigestSessions       int64          `json:"recovered_digest_sessions,omitempty"`
	Fallback                      bool           `json:"fallback"`
	FallbackReason                string         `json:"fallback_reason,omitempty"`
}

type RecallTrace struct {
	ID              string
	SessionID       string
	TurnID          string
	ProjectKey      string
	QueryHash       string
	QueryPreview    string
	SummaryRevision int64
	CandidateCount  int
	FilteredCount   int
	SelectedIDs     []string
	Dropped         []DroppedRecord
	TokenEstimate   int
	TokenBudget     int
	BudgetSource    string
	Injected        bool
	CreatedAt       time.Time
}

// V2Selector is the optional contextmgr integration surface. Keeping the
// legacy ProjectMemoryStore interface untouched lets old fakes/tests continue
// to work while v2-enabled stores expose the richer selection.
type V2Selector interface {
	SelectForInjectV2(opts InjectOptions) (Selection, error)
	V2InjectionEnabled() bool
}
