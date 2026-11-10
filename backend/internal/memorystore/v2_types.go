package memorystore

import "time"

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
	ID               string
	Scope            Scope
	ProjectKey       string
	Kind             string
	Status           Status
	Text             string
	NormalizedText   string
	Tags             []string
	Confidence       float64
	Pinned           bool
	RetentionClass   string
	SourceKind       string
	SourceSessionID  string
	SourceTurnID     string
	SourceRefs       []string
	SourceHash       string
	SourceRevision   int64
	SourceUpdatedAt  time.Time
	SourceObservedAt time.Time
	SourceMessageIDs []string
	ContentHash      string
	SupersedesID     string
	Revision         int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
	LastUsedAt       time.Time
	UseCount         int
	ValidFrom        time.Time
	ValidUntil       time.Time
	Freshness        Freshness
	LastVerifiedAt   time.Time
	RevalidateAfter  time.Time
	StaleReason      string
	DeletedAt        time.Time
	Metadata         map[string]interface{}
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
	WorkerID string
	Kind     string
	JobKey   string
	Lease    time.Duration
	Limit    int
	Now      time.Time
}

type Candidate struct {
	SessionID        string
	TurnID           string
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
}

type Selection struct {
	Summary       *Summary
	Stable        []Record
	Recall        []Record
	Revision      int64
	TokenEstimate int
	IDs           []string
	Dropped       []DroppedRecord
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
	Mode             string
	Scope            string
	DBPath           string
	SchemaVersion    int
	Total            int
	ByStatus         map[string]int
	ByFreshness      map[string]int
	SummaryRevision  int64
	SummaryStatus    string
	SummaryFreshness string
	Jobs             map[string]int
	Fallback         bool
	FallbackReason   string
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
