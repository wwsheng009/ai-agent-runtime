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

type Extractor interface {
	Extract(ctx context.Context, session SessionRef, transcript Transcript) (ExtractionResult, error)
}

type SessionSource interface {
	ListSessions(ctx context.Context, workspace string, limit int) ([]SessionRef, error)
	ReadTranscript(ctx context.Context, session SessionRef, maxMessages int) (Transcript, error)
}

type SessionModeReader interface {
	GetSessionMode(ctx context.Context, sessionID string) (memorystore.SessionMode, error)
}

type RunResult struct {
	Trigger    Trigger
	Sessions   int
	Processed  int
	Skipped    int
	Candidates int
	Stale      int
	Failed     int
	Ignored    []string
	Errors     []string
}

type ConsolidationResult struct {
	Promoted      int
	Superseded    int
	SummaryID     string
	Revision      int64
	SummaryText   string
	TokenCount    int
	Rebuilt       int
	RebuildJobs   int
	OpenQuestions int
}
