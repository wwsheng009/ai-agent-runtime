package memorystore

import (
	"errors"
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
	ID             string   `json:"id"`
	Scope          Scope    `json:"scope"`
	ProjectKey     string   `json:"project_key"`
	Kind           string   `json:"kind"`
	Status         Status   `json:"status"`
	Text           string   `json:"text"`
	Title          string   `json:"title,omitempty"`
	Summary        string   `json:"summary,omitempty"`
	CanonicalID    string   `json:"canonical_id,omitempty"`
	SupersededBy   string   `json:"superseded_by,omitempty"`
	Archived       bool     `json:"archived"`
	NormalizedText string   `json:"normalized_text,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	Confidence     float64  `json:"confidence,omitempty"`
	Sensitivity    string   `json:"sensitivity,omitempty"`
	Pinned         bool     `json:"pinned"`
	RetentionClass string   `json:"retention_class,omitempty"`
	SourceKind     string   `json:"source_kind,omitempty"`
	// SourceType and SourceTrust describe the evidence channel, not the
	// extracted memory itself. SourceQuote is a bounded, redacted snippet from
	// that channel and must never be populated with Record.Text as a fallback.
	SourceType              string                 `json:"source_type,omitempty"`
	SourceTrust             string                 `json:"source_trust,omitempty"`
	SourceQuote             string                 `json:"-"`
	SourceSessionID         string                 `json:"source_session_id,omitempty"`
	SourceSessionGeneration int64                  `json:"source_session_generation,omitempty"`
	SourceTurnID            string                 `json:"source_turn_id,omitempty"`
	SourceWindow            string                 `json:"source_window,omitempty"`
	SourceRefs              []string               `json:"source_refs,omitempty"`
	SourceHash              string                 `json:"source_hash,omitempty"`
	SourceRevision          int64                  `json:"source_revision,omitempty"`
	SourceUpdatedAt         time.Time              `json:"source_updated_at,omitempty"`
	SourceObservedAt        time.Time              `json:"source_observed_at,omitempty"`
	SourceMessageIDs        []string               `json:"source_message_ids,omitempty"`
	ContentHash             string                 `json:"content_hash,omitempty"`
	SupersedesID            string                 `json:"supersedes_id,omitempty"`
	Revision                int64                  `json:"revision"`
	CreatedAt               time.Time              `json:"created_at"`
	UpdatedAt               time.Time              `json:"updated_at"`
	LastUsedAt              time.Time              `json:"last_used_at,omitempty"`
	UseCount                int                    `json:"use_count,omitempty"`
	ValidFrom               time.Time              `json:"valid_from,omitempty"`
	ValidUntil              time.Time              `json:"valid_until,omitempty"`
	Freshness               Freshness              `json:"freshness_status"`
	LastVerifiedAt          time.Time              `json:"last_verified_at,omitempty"`
	RevalidateAfter         time.Time              `json:"revalidate_after,omitempty"`
	StaleReason             string                 `json:"stale_reason,omitempty"`
	DeletedAt               time.Time              `json:"deleted_at,omitempty"`
	Metadata                map[string]interface{} `json:"metadata,omitempty"`
}

// ConflictReviewPair identifies the higher-priority and competing records in
// a conflict that must be reviewed instead of automatically superseded.
type ConflictReviewPair struct {
	PreferredID string
	CompetingID string
}

// RecordSource is the durable provenance row for one memory record. Quote is
// intentionally separate from Record.Text so callers can audit the evidence
// without confusing it with the extracted claim.
type RecordSource struct {
	ID                string    `json:"id"`
	MemoryID          string    `json:"memory_id"`
	SourceKind        string    `json:"source_kind"`
	SourceID          string    `json:"source_id"`
	SourceType        string    `json:"source_type,omitempty"`
	SourceTrust       string    `json:"source_trust,omitempty"`
	SessionID         string    `json:"session_id,omitempty"`
	SessionGeneration int64     `json:"source_session_generation,omitempty"`
	TurnID            string    `json:"turn_id,omitempty"`
	SourceWindow      string    `json:"source_window,omitempty"`
	MessageIDs        []string  `json:"message_ids,omitempty"`
	SourceHash        string    `json:"source_hash,omitempty"`
	SourceRevision    int64     `json:"source_revision,omitempty"`
	SourceUpdatedAt   time.Time `json:"source_updated_at,omitempty"`
	SourceObservedAt  time.Time `json:"source_observed_at,omitempty"`
	Quote             string    `json:"quote,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
}

// ExtractionDiagnostic is bounded operator-visible evidence about why an
// extractor ignored a candidate. It deliberately contains no provider error,
// prompt, or raw transcript payload.
type ExtractionDiagnostic struct {
	ID                string    `json:"id"`
	ProjectKey        string    `json:"project_key,omitempty"`
	JobID             string    `json:"job_id"`
	SessionID         string    `json:"session_id,omitempty"`
	SessionGeneration int64     `json:"session_generation,omitempty"`
	TurnID            string    `json:"turn_id,omitempty"`
	Reason            string    `json:"reason"`
	Summary           string    `json:"summary,omitempty"`
	SourceIDs         []string  `json:"source_ids,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
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
	ID       string `json:"id"`
	Reason   string `json:"reason"`
	Category string `json:"category,omitempty"`
}

type JobStatus string

const (
	JobStatusPending   JobStatus = "pending"
	JobStatusRunning   JobStatus = "running"
	JobStatusSucceeded JobStatus = "succeeded"
	JobStatusFailed    JobStatus = "failed"
	JobStatusCancelled JobStatus = "cancelled"
	// JobStatusDeadLetter is terminal and intentionally not claimable.  The
	// dead_letter_* columns remain the source of truth for the operator-facing
	// reason/timestamp, while the status makes aggregation and filtering
	// explicit.
	JobStatusDeadLetter JobStatus = "dead_letter"
)

// Memory job priorities are assigned when work is enqueued, rather than
// inferred at claim time. Keep the values spaced so future job kinds can be
// inserted without changing existing ordering.
const (
	JobPriorityPhase1ExtractV2   = 400
	JobPriorityPhase1Extract     = 350
	JobPriorityCoreRebuild       = 325
	JobPrioritySummaryRebuild    = 300
	JobPriorityTurnRollup        = 250
	JobPriorityPhase2Consolidate = 200
	JobPriorityReindex           = 100
	JobPriorityDreaming          = 0
)

// DefaultMemoryJobTimeout is the absolute execution window assigned to new
// jobs that do not provide an explicit deadline. It is deliberately separate
// from the worker lease: a lease may be reclaimed and renewed, but a deadline
// must never move forward when a worker heartbeats.
const DefaultMemoryJobTimeout = 30 * time.Minute

// DefaultJobPriority returns the priority assigned to a newly-created job when
// its caller did not provide an explicit value. Existing rows are never
// rewritten, so this remains an additive default for new work only.
func DefaultJobPriority(kind string) int {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "phase1_extract/v2":
		return JobPriorityPhase1ExtractV2
	case "phase1_extract":
		return JobPriorityPhase1Extract
	case "core_rebuild":
		return JobPriorityCoreRebuild
	case "summary_rebuild":
		return JobPrioritySummaryRebuild
	case "turn_window_rollup":
		return JobPriorityTurnRollup
	case "phase2_consolidate":
		return JobPriorityPhase2Consolidate
	case "reindex":
		return JobPriorityReindex
	case "dreaming":
		return JobPriorityDreaming
	default:
		return 0
	}
}

type MemoryJob struct {
	ID                   string    `json:"id"`
	Kind                 string    `json:"kind"`
	JobKey               string    `json:"job_key"`
	Scope                Scope     `json:"scope"`
	ProjectKey           string    `json:"project_key"`
	SessionID            string    `json:"session_id,omitempty"`
	SessionGeneration    int64     `json:"session_generation,omitempty"`
	CaptureReceiptID     string    `json:"capture_receipt_id,omitempty"`
	SourceStartSeq       int64     `json:"source_start_seq,omitempty"`
	SourceEndSeq         int64     `json:"source_end_seq,omitempty"`
	SourceRevision       int64     `json:"source_revision,omitempty"`
	SourceHash           string    `json:"source_hash,omitempty"`
	AvailableAt          time.Time `json:"available_at,omitempty"`
	DeadlineAt           time.Time `json:"deadline_at,omitempty"`
	HeartbeatAt          time.Time `json:"heartbeat_at,omitempty"`
	DeferredUntil        time.Time `json:"deferred_until,omitempty"`
	DeadLetterAt         time.Time `json:"dead_letter_at,omitempty"`
	DeadLetterReason     string    `json:"dead_letter_reason,omitempty"`
	RepairOnly           bool      `json:"repair_only,omitempty"`
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
	SessionID         string
	SessionGeneration int64
	TurnID            string
	SourceWindow      string
	SourceKind        string
	SourceType        string
	SourceTrust       string
	SourceQuote       string
	SourceRefs        []string
	ProjectKey        string
	Kind              string
	Text              string
	Title             string
	Summary           string
	CanonicalID       string
	Archived          bool
	Tags              []string
	Confidence        float64
	Sensitivity       string
	SourceHash        string
	SourceRevision    int64
	SourceUpdatedAt   time.Time
	SourceMessageIDs  []string
	RetentionClass    string
	RevalidateAfter   time.Time
	Model             string
	TokenUsage        map[string]interface{}
	Metadata          map[string]interface{}
}

type Selection struct {
	Summary        *Summary
	Core           *CoreMemory
	Stable         []Record
	Recall         []Record
	Revision       int64
	TokenEstimate  int
	CategoryBudget map[string]int `json:"category_budget,omitempty"`
	CategoryUsed   map[string]int `json:"category_used,omitempty"`
	IDs            []string
	Dropped        []DroppedRecord
	CandidateCount int
	FilteredCount  int
}

type RecordFilter struct {
	Scope                   Scope
	ProjectKey              string
	Status                  Status
	Kind                    string
	Tag                     string
	Query                   string
	SourceKind              string
	SourceSessionID         string
	SourceSessionGeneration *int64
	// PinnedOnly selects stable/pinned records without changing the default
	// active-record query semantics.
	PinnedOnly       bool
	IncludeArchived  bool
	IncludeCandidate bool
	// AsOf selects the latest immutable snapshot committed at or before this
	// instant, then applies the snapshot's valid_from/valid_until interval.
	AsOf   *time.Time
	Limit  int
	Cursor string
}

type RecordPage struct {
	Items      []Record
	NextCursor string
	Count      int
}

type RecordPatch struct {
	Text         *string
	Title        *string
	Summary      *string
	CanonicalID  *string
	SupersededBy *string
	Archived     *bool
	Tags         *[]string
	Pinned       *bool
	Status       *Status
}

type StatusReport struct {
	Mode                          string                    `json:"mode"`
	Scope                         string                    `json:"scope"`
	DBPath                        string                    `json:"db_path,omitempty"`
	SchemaVersion                 int                       `json:"schema_version"`
	Total                         int                       `json:"total"`
	ManualCount                   int                       `json:"manual_count"`
	ByStatus                      map[string]int            `json:"by_status,omitempty"`
	ByFreshness                   map[string]int            `json:"by_freshness,omitempty"`
	SummaryRevision               int64                     `json:"summary_revision,omitempty"`
	SummaryStatus                 string                    `json:"summary_status,omitempty"`
	SummaryFreshness              string                    `json:"summary_freshness,omitempty"`
	PinnedCount                   int                       `json:"pinned_count,omitempty"`
	InjectionMode                 string                    `json:"injection_mode,omitempty"`
	Jobs                          map[string]int            `json:"jobs,omitempty"`
	JobsByKind                    map[string]map[string]int `json:"jobs_by_kind,omitempty"`
	TurnDigests                   map[string]int            `json:"turn_digests,omitempty"`
	TurnWindows                   map[string]int            `json:"turn_windows,omitempty"`
	OldestPendingDigestAgeSeconds int64                     `json:"oldest_pending_digest_age_seconds,omitempty"`
	PendingDigestSessions         int                       `json:"pending_digest_sessions,omitempty"`
	PendingDigestBacklogBatches   int                       `json:"pending_digest_backlog_batches,omitempty"`
	RecoveredDigestSessions       int64                     `json:"recovered_digest_sessions,omitempty"`
	Fallback                      bool                      `json:"fallback"`
	FallbackReason                string                    `json:"fallback_reason,omitempty"`
	// SearchBackend is deliberately explicit even before FTS5 is available:
	// "legacy_jsonl" describes Mode=off and "like_fallback" describes the
	// current v2 bounded keyword implementation.  A future FTS implementation
	// may return "fts5" without changing the status contract.
	SearchBackend              string          `json:"search_backend,omitempty"`
	ExtractionJobCounts        map[string]int  `json:"extraction_job_counts,omitempty"`
	PendingExtractionJobs      int             `json:"pending_extraction_jobs,omitempty"`
	PendingExtractionSessions  int             `json:"pending_extraction_sessions,omitempty"`
	OldestPendingJobAgeSeconds int64           `json:"oldest_pending_job_age_seconds,omitempty"`
	DeadLetterJobs             int             `json:"dead_letter_jobs,omitempty"`
	MissingDeadlineJobs        int             `json:"missing_deadline_jobs,omitempty"`
	LastExtractionAt           string          `json:"last_extraction_at,omitempty"`
	ExtractionJobs             []JobDiagnostic `json:"extraction_jobs,omitempty"`
}

type JobDiagnostic struct {
	Kind             string `json:"kind"`
	ID               string `json:"id"`
	JobKey           string `json:"job_key,omitempty"`
	SessionID        string `json:"session_id,omitempty"`
	Status           string `json:"status"`
	Priority         int    `json:"priority"`
	Attempt          int    `json:"attempt"`
	CreatedAt        string `json:"created_at,omitempty"`
	AvailableAt      string `json:"available_at,omitempty"`
	LeaseUntil       string `json:"lease_until,omitempty"`
	TimeoutAt        string `json:"timeout_at,omitempty"`
	DeadLetterAt     string `json:"dead_letter_at,omitempty"`
	DeadLetterReason string `json:"dead_letter_reason,omitempty"`
	LastError        string `json:"last_error,omitempty"`
}

type JobDiagnosticFilter struct {
	Kind       string
	Status     string
	SessionID  string
	ProjectKey string
	OlderThan  time.Duration
	Limit      int
}

type JobCountGroups struct {
	Counts        map[string]int `json:"counts"`
	OmittedGroups int            `json:"omitted_groups,omitempty"`
	OmittedJobs   int            `json:"omitted_jobs,omitempty"`
}

// JobOperatorSummary is a bounded, read-only aggregate for memory job
// operations. It intentionally reports counts and ages, not raw job errors or
// secret-bearing diagnostics.
type JobOperatorSummary struct {
	TotalJobs           int `json:"total_jobs"`
	PendingDueJobs      int `json:"pending_due_jobs"`
	PendingDeferredJobs int `json:"pending_deferred_jobs"`
	MissingDeadlineJobs int `json:"missing_deadline_jobs,omitempty"`
	// LeasedJobs counts running rows with a lease that has not expired.
	LeasedJobs int `json:"leased_jobs"`
	// RetryableJobs counts failed rows and scheduled retry rows that still have
	// automatic attempts remaining; dead-letter rows are reported separately.
	RetryableJobs int `json:"retryable_jobs"`
	// BlockedJobs counts deferred pending rows and every running row, including
	// rows whose lease has expired and need reclaim/recovery.
	BlockedJobs             int            `json:"blocked_jobs"`
	DeadLetterJobs          int            `json:"dead_letter_jobs"`
	OldestPendingAgeSeconds int64          `json:"oldest_pending_age_seconds,omitempty"`
	ByKind                  JobCountGroups `json:"by_kind"`
	ByStatus                JobCountGroups `json:"by_status"`
	ByPriority              JobCountGroups `json:"by_priority"`
}

// JobDeadlineRepairOptions controls the bounded migration of historical
// non-terminal jobs that predate the absolute deadline column. DryRun never
// changes jobs or audit rows.
type JobDeadlineRepairOptions struct {
	Limit  int
	DryRun bool
	Reason string
}

type JobDeadlineRepairItem struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	Status      JobStatus `json:"status"`
	Attempt     int       `json:"attempt"`
	MaxAttempts int       `json:"max_attempts"`
	CreatedAt   time.Time `json:"created_at"`
	DeadlineAt  time.Time `json:"deadline_at"`
	Applied     bool      `json:"applied"`
	Skipped     bool      `json:"skipped,omitempty"`
	Reason      string    `json:"reason,omitempty"`
}

type JobDeadlineRepairResult struct {
	DryRun   bool                    `json:"dry_run"`
	Scanned  int                     `json:"scanned"`
	Eligible int                     `json:"eligible"`
	Updated  int                     `json:"updated"`
	Skipped  int                     `json:"skipped"`
	Items    []JobDeadlineRepairItem `json:"items"`
}

// DigestReplayClassification is a safe, non-diagnostic outcome for replay
// planning. It intentionally avoids including provider errors or transcript
// content in operator reports.
type DigestReplayClassification string

const (
	DigestReplayEligible           DigestReplayClassification = "eligible"
	DigestReplayApplied            DigestReplayClassification = "replayed"
	DigestReplayAlreadyQueued      DigestReplayClassification = "already_queued"
	DigestReplayAlreadyProcessed   DigestReplayClassification = "already_processed"
	DigestReplayStaleGeneration    DigestReplayClassification = "stale_generation"
	DigestReplayMissingReceipt     DigestReplayClassification = "missing_receipt"
	DigestReplayInvalidIdentity    DigestReplayClassification = "invalid_identity"
	DigestReplayBlockedMaintenance DigestReplayClassification = "blocked_by_maintenance"
	DigestReplayNotOrphaned        DigestReplayClassification = "not_orphaned"
)

// DigestReplayResult is both a dry-run preview and the result of an attempted
// replay. The reason is a fixed classification message, never a raw runtime
// or provider diagnostic.
type DigestReplayResult struct {
	DigestID          string                     `json:"digest_id"`
	JobID             string                     `json:"job_id,omitempty"`
	SessionID         string                     `json:"session_id,omitempty"`
	SessionGeneration int64                      `json:"session_generation,omitempty"`
	CaptureReceiptID  string                     `json:"capture_receipt_id,omitempty"`
	Classification    DigestReplayClassification `json:"classification"`
	Reason            string                     `json:"reason,omitempty"`
	DryRun            bool                       `json:"dry_run"`
}

var (
	ErrJobNotFound            = errors.New("memorystore: job not found")
	ErrJobConflict            = errors.New("memorystore: job state conflict")
	ErrDigestNotFound         = errors.New("memorystore: digest not found")
	ErrDigestReplayIneligible = errors.New("memorystore: digest is not eligible for replay")
)

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
