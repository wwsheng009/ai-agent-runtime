// Package memoryruntime implements the asynchronous Agent Memory v2 write
// path. M2 scope is deliberately narrow: select eligible sessions, read a
// bounded canonical transcript, extract structured candidates, redact secrets,
// and persist candidates. It never promotes candidates to active memory or
// injects them into prompts.
package memoryruntime

import (
	"context"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

type Trigger string

const (
	TriggerIdle       Trigger = "idle"
	TriggerSessionEnd Trigger = "session_end"
	TriggerStartup    Trigger = "startup"
	TriggerBoth       Trigger = "both"
	TriggerManual     Trigger = "manual"
)

type Config struct {
	Mode                       string
	GenerationDisabled         bool
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
	CitationEnabled            bool
	ExposureSampling           float64
	Lease                      time.Duration
	WorkerID                   string
	ExtractionModel            string
	ExtractionProvider         string
	ExtractionReasoningEffort  string
	ConsolidationModel         string
	ConsolidationProvider      string
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
	ID             string
	UserID         string
	WorkspacePath  string
	State          string
	IsSubagent     bool
	Ephemeral      bool
	UpdatedAt      time.Time
	LastActivityAt time.Time
	CanonicalCount int
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
	Reason  string
	Summary string
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

type SessionSource interface {
	ListSessions(ctx context.Context, workspace string, limit int) ([]SessionRef, error)
	ReadTranscript(ctx context.Context, session SessionRef, maxMessages int) (Transcript, error)
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
