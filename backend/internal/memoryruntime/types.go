// Package memoryruntime implements the asynchronous Agent Memory v2 write
// path. M2 scope is deliberately narrow: select eligible sessions, read a
// bounded canonical transcript, extract structured candidates, redact secrets,
// and persist candidates. It never promotes candidates to active memory or
// injects them into prompts.
package memoryruntime

import (
	"context"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

type Trigger string

const (
	TriggerIdle       Trigger = "idle"
	TriggerSessionEnd Trigger = "session_end"
	TriggerStartup    Trigger = "startup"
	TriggerBoth       Trigger = "both"
	TriggerManual     Trigger = "manual"
	// TriggerTurnThreshold is an active-session trigger. It is driven only by
	// durable canonical capture receipts; it must never infer completed turns
	// from a live transcript or streaming event.
	TriggerTurnThreshold Trigger = "turn_threshold"
	// TriggerTaskCompletion is the reserved G4 lifecycle hook. The hook accepts
	// only a durable task terminal event and may consume already-frozen capture
	// ranges, but it does not create/promote Episode memories by itself.
	TriggerTaskCompletion Trigger = "task_completion"
)

type Config struct {
	Mode               string
	GenerationDisabled bool
	// GenerationTrigger is copied from memory.generation.trigger. Keeping the
	// normalized value on the runtime lets post-commit hooks make the same
	// decision as the host scheduler without importing the config package.
	GenerationTrigger          string
	Workspace                  string
	ProjectKey                 string
	IncludeSubagents           bool
	RequireWorkspaceMatch      bool
	MinIdle                    time.Duration
	MaxRolloutAge              time.Duration
	MaxSessionsPerRun          int
	MaxSourceMessages          int
	MaxSourceBytes             int
	MaxToolOutputRunes         int
	MaxCandidatesPerSession    int
	MaxCandidateTextRunes      int
	MaxTagsPerCandidate        int
	MaxAttempts                int
	ConsolidationDisabled      bool
	ConsolidationInterval      time.Duration
	MaxConsolidationRecords    int
	MaxConsolidationUnusedDays int
	MinCandidateConfidence     float64
	SummaryTokenBudget         int
	// CoreRebuildTokenBudget and the two source limits keep automatic Core
	// generation bounded independently from project-summary consolidation.
	// Core rebuild only creates reviewable candidates; it never activates one.
	CoreRebuildTokenBudget    int
	CoreRebuildMaxRecords     int
	CoreRebuildMaxUsageRows   int
	CitationEnabled           bool
	ExposureSampling          float64
	Lease                     time.Duration
	WorkerID                  string
	ExtractionModel           string
	ExtractionProvider        string
	ExtractionReasoningEffort string
	ConsolidationModel        string
	ConsolidationProvider     string
	// Empty stage overrides inherit ExtractionReasoningEffort.
	ConsolidationReasoningEffort string
	WindowSummaryReasoningEffort string
	Summarizer                   ProjectSummaryGenerator

	// Lifecycle/freshness policy consumed by extraction and consolidation.
	// Zero-value Config preserves the historical hard-coded kind mapping.
	RetentionClasses             memorystore.RetentionClasses
	FreshnessDisabled            bool
	FreshnessDefaultTTL          time.Duration
	FreshnessPerKind             map[string]time.Duration
	FreshnessStaleBehavior       string
	FreshnessRevalidateBatchSize int
	RedactionConfigured          bool
	RedactionEnabled             bool
	RedactionDropOnSecret        bool

	// TurnDigest controls the runtime outbox written at turn end. It never
	// triggers extraction by itself; session-end/idle/startup recovery consumes
	// it through the canonical transcript path.
	TurnDigestEnabled              bool
	TurnDigestStoreText            bool
	TurnDigestMaxUserChars         int
	TurnDigestMaxAssistantChars    int
	TurnDigestRetentionDays        int
	TurnDigestMaxPendingPerSession int
	TurnDigestMaxTurnsPerBatch     int
	TurnDigestMaxBytesPerBatch     int
	TurnDigestMaxDigestsPerRun     int
	TurnDigestExtractEveryNTurns   int
	TurnDigestIdleExtractMinutes   int
	TurnDigestRecoveryOnStartup    bool
	TurnDigestWindowSummaryEnabled bool
	TurnDigestWindowTurns          int
	TurnDigestWindowRetentionDays  int
}

func (c Config) normalized() Config {
	if c.Mode == "" {
		c.Mode = "off"
	}
	c.GenerationTrigger = strings.ToLower(strings.TrimSpace(c.GenerationTrigger))
	if c.MaxSessionsPerRun <= 0 {
		c.MaxSessionsPerRun = 2
	}
	if c.MaxSourceMessages <= 0 {
		c.MaxSourceMessages = 200
	}
	if c.MaxSourceBytes <= 0 {
		c.MaxSourceBytes = 512 * 1024
	}
	if c.MaxToolOutputRunes <= 0 {
		c.MaxToolOutputRunes = 800
	}
	if c.MaxCandidatesPerSession <= 0 {
		c.MaxCandidatesPerSession = 20
	}
	if c.MaxCandidateTextRunes <= 0 {
		c.MaxCandidateTextRunes = 4000
	}
	if c.MaxTagsPerCandidate <= 0 {
		c.MaxTagsPerCandidate = 8
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 3
	}
	if c.MaxConsolidationRecords <= 0 {
		c.MaxConsolidationRecords = 256
	}
	if c.MaxConsolidationUnusedDays <= 0 {
		c.MaxConsolidationUnusedDays = 30
	}
	if c.MinCandidateConfidence <= 0 {
		c.MinCandidateConfidence = 0.7
	}
	if c.SummaryTokenBudget <= 0 {
		c.SummaryTokenBudget = 800
	}
	if c.CoreRebuildTokenBudget <= 0 {
		c.CoreRebuildTokenBudget = 1200
	}
	if c.CoreRebuildMaxRecords <= 0 {
		c.CoreRebuildMaxRecords = 32
	}
	if c.CoreRebuildMaxUsageRows <= 0 {
		c.CoreRebuildMaxUsageRows = 200
	}
	if c.ConsolidationInterval <= 0 {
		c.ConsolidationInterval = 24 * time.Hour
	}
	if c.Lease <= 0 {
		c.Lease = 5 * time.Minute
	}
	if c.MinIdle <= 0 {
		c.MinIdle = 6 * time.Hour
	}
	if c.MaxRolloutAge <= 0 {
		c.MaxRolloutAge = 30 * 24 * time.Hour
	}
	if c.ExtractionReasoningEffort == "" {
		c.ExtractionReasoningEffort = "low"
	}
	if c.TurnDigestMaxUserChars <= 0 {
		c.TurnDigestMaxUserChars = 8192
	}
	if c.TurnDigestMaxAssistantChars <= 0 {
		c.TurnDigestMaxAssistantChars = 16384
	}
	if c.TurnDigestRetentionDays <= 0 {
		c.TurnDigestRetentionDays = 7
	}
	if c.TurnDigestMaxPendingPerSession <= 0 {
		c.TurnDigestMaxPendingPerSession = 200
	}
	if c.TurnDigestMaxTurnsPerBatch <= 0 {
		c.TurnDigestMaxTurnsPerBatch = 20
	}
	if c.TurnDigestMaxBytesPerBatch <= 0 {
		c.TurnDigestMaxBytesPerBatch = 128 * 1024
	}
	if c.TurnDigestMaxDigestsPerRun <= 0 {
		c.TurnDigestMaxDigestsPerRun = 200
	}
	if c.TurnDigestExtractEveryNTurns <= 0 {
		c.TurnDigestExtractEveryNTurns = 50
	}
	if c.TurnDigestIdleExtractMinutes <= 0 {
		c.TurnDigestIdleExtractMinutes = 30
	}
	if c.TurnDigestWindowTurns <= 0 {
		c.TurnDigestWindowTurns = 100
	}
	if c.TurnDigestWindowRetentionDays <= 0 {
		c.TurnDigestWindowRetentionDays = 30
	}
	if c.FreshnessRevalidateBatchSize <= 0 {
		c.FreshnessRevalidateBatchSize = 50
	}
	if !c.RedactionConfigured {
		c.RedactionEnabled = true
		c.RedactionDropOnSecret = true
	}
	return c
}

type SessionRef struct {
	ID                string
	UserID            string
	SessionGeneration int64
	WorkspacePath     string
	State             string
	IsSubagent        bool
	Ephemeral         bool
	UpdatedAt         time.Time
	LastActivityAt    time.Time
	CanonicalCount    int
}

type TranscriptMessage struct {
	ID         string
	Seq        int
	Role       string
	Content    string
	ToolName   string
	ToolCallID string
	Truncated  bool
	// Fragment offsets are bytes in the redacted message, never secret-bearing
	// canonical text. A fragment alone is not a completed canonical message.
	FragmentOffset int
	FragmentTotal  int
}

type Transcript struct {
	SessionID     string
	ProjectKey    string
	WorkspacePath string
	Hash          string
	Revision      int64
	UpdatedAt     time.Time
	Messages      []TranscriptMessage
}

type Candidate struct {
	Kind             string
	Text             string
	Tags             []string
	Confidence       float64
	Sensitivity      string
	SourceMessageIDs []string
}

type IgnoredCandidate struct {
	Reason           string
	Summary          string
	SourceMessageIDs []string
}

type TokenUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

type ExtractionResult struct {
	Candidates []Candidate
	Ignored    []IgnoredCandidate
	Model      string
	Usage      TokenUsage
}

// ProjectSummaryInput is the bounded record set handed to an optional
// Consolidation summary generator. Records have already passed active/fresh,
// secret, private, pinned/manual, and unused-age filtering.
type ProjectSummaryInput struct {
	ProjectKey  string
	TokenBudget int
	Records     []memorystore.Record
}

type ProjectSummaryOutput struct {
	Text  string
	Model string
	Usage TokenUsage
}

type ProjectSummaryGenerator interface {
	SummarizeProject(ctx context.Context, input ProjectSummaryInput) (ProjectSummaryOutput, error)
}

type Extractor interface {
	Extract(ctx context.Context, session SessionRef, transcript Transcript) (ExtractionResult, error)
}

// TaskCompletionEvent is the narrow memory-runtime boundary for a durable
// AgentControl/team task terminal event. SourceEventID is required so an
// in-memory notification cannot be mistaken for a durable completion. The
// memory runtime intentionally keeps this payload bounded and does not depend
// on the team package. Episode persistence is deliberately outside this G0
// hook until the G4 schema/patch pipeline exists.
type TaskCompletionEvent struct {
	TenantID          string
	ProjectKey        string
	SessionID         string
	TaskID            string
	Outcome           string
	Problem           string
	Actions           []string
	Result            string
	FailureReason     string
	SourceEventIDs    []string
	SourceEventID     string
	Durable           bool
	TeamID            string
	ResultRef         string
	SessionGeneration int64
	StartedAt         time.Time
	EndedAt           time.Time
	Timestamp         time.Time
}

// TaskCompletionConsumer is the future G4 Episode boundary. Implementations
// must durably enqueue or persist the bounded event and may not infer task
// completion from a transcript.
type TaskCompletionConsumer interface {
	ConsumeTaskCompletion(context.Context, TaskCompletionEvent) error
}

type TaskCompletionResult struct {
	Trigger  Trigger
	Accepted bool
	Reason   string
}

type SessionSource interface {
	ListSessions(ctx context.Context, workspace string, limit int) ([]SessionRef, error)
	ReadTranscript(ctx context.Context, session SessionRef, maxMessages int) (Transcript, error)
}

// TurnCaptureReceiptSource is implemented by canonical stores that expose the
// durable turn-end handoff. It is intentionally an optional extension so old
// session backends remain usable for legacy digest recovery.
type TurnCaptureReceiptSource interface {
	ListTurnCaptureReceipts(context.Context, int) ([]chat.TurnCaptureReceipt, error)
	ListMaterializedTurnCaptureReceipts(context.Context, int) ([]chat.TurnCaptureReceipt, error)
	GetTurnCaptureReceipt(context.Context, string) (chat.TurnCaptureReceipt, error)
	ValidateTurnCaptureReceipt(context.Context, chat.TurnCaptureReceipt) error
	MarkTurnCaptureReceiptMaterialized(context.Context, string) error
	DeferTurnCaptureReceipt(context.Context, string, string, time.Time) error
}

// SessionTurnCaptureReceiptSource is an optional scoped listing extension used
// by session-end flushes so a busy global outbox cannot hide this session's
// receipts behind the page limit.
type SessionTurnCaptureReceiptSource interface {
	ListTurnCaptureReceiptsForSession(context.Context, string, int) ([]chat.TurnCaptureReceipt, error)
	ListMaterializedTurnCaptureReceiptsForSession(context.Context, string, int) ([]chat.TurnCaptureReceipt, error)
	HasTurnCaptureReceiptsForSession(context.Context, string) (bool, error)
}

// SessionTurnCaptureReceiptGenerationSource scopes receipt routing and
// session-end materialization to one canonical incarnation.
type SessionTurnCaptureReceiptGenerationSource interface {
	ListTurnCaptureReceiptsForSessionGeneration(context.Context, string, int64, int) ([]chat.TurnCaptureReceipt, error)
	HasTurnCaptureReceiptsForSessionGeneration(context.Context, string, int64) (bool, error)
}

// MaterializedTurnCaptureReceiptPageSource adds a keyset-paginated recovery
// listing. The page cursor is the last receipt returned by the previous page;
// it lets recovery skip already-completed materialized receipts without
// confusing canonical receipt state with extraction state.
type MaterializedTurnCaptureReceiptPageSource interface {
	ListMaterializedTurnCaptureReceiptsPage(context.Context, *chat.TurnCaptureReceipt, int) ([]chat.TurnCaptureReceipt, error)
	ListMaterializedTurnCaptureReceiptsForSessionPage(context.Context, string, *chat.TurnCaptureReceipt, int) ([]chat.TurnCaptureReceipt, error)
}

// SessionGenerationMaterializedTurnCaptureReceiptPageSource prevents receipts
// from an older incarnation from occupying a newer incarnation's recovery page.
type SessionGenerationMaterializedTurnCaptureReceiptPageSource interface {
	ListMaterializedTurnCaptureReceiptsForSessionGenerationPage(
		context.Context, string, int64, *chat.TurnCaptureReceipt, int,
	) ([]chat.TurnCaptureReceipt, error)
}

// TranscriptWindowSource is an optional extension for large sessions. It reads
// at most maxMessages from the beginning of the canonical sequence range.
// Callers must verify coverage; a partial page never proves the range complete.
type TranscriptWindowSource interface {
	ReadTranscriptWindow(ctx context.Context, session SessionRef, startSeq, endSeq, maxMessages int) (Transcript, error)
}

type SessionModeReader interface {
	GetSessionMode(ctx context.Context, sessionID string) (memorystore.SessionMode, error)
}

type RunResult struct {
	Trigger      Trigger
	Sessions     int
	Processed    int
	Skipped      int
	Candidates   int
	Stale        int
	Failed       int
	RolledUp     int
	RollupFailed int
	Ignored      []string
	Errors       []string
	// Completed is a durable extraction receipt, not an indication that source
	// loading succeeded. RunSessionWindow sets it only when every subwindow of
	// the requested range has a successful persisted job. RunSession covers its
	// bounded transcript page. Aggregate sweeps do not carry this receipt.
	Completed bool
	// SourceHash and the inclusive sequence bounds describe completed source
	// coverage. They remain empty/zero for running, failed, or backoff jobs.
	SourceHash     string
	SourceStartSeq int64
	SourceEndSeq   int64
}

type RollupResult struct {
	Examined int
	RolledUp int
	Skipped  int
	Failed   int
	Errors   []string
}

type ConsolidationResult struct {
	Promoted      int
	Superseded    int
	SummaryID     string
	Revision      int64
	SummaryText   string
	SummaryModel  string
	TokenCount    int
	Rebuilt       int
	RebuildJobs   int
	OpenQuestions int
}
