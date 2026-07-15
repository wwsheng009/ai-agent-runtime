package agentresult

import (
	"fmt"
	"strings"
)

// Status is the canonical terminal state shared by child, teammate, and lead results.
type Status string

const (
	StatusSucceeded          Status = "succeeded"
	StatusPartiallyCompleted Status = "partially_completed"
	StatusBlocked            Status = "blocked"
	StatusFailed             Status = "failed"
	StatusTimedOut           Status = "timed_out"
	StatusCanceled           Status = "canceled"
	StatusOrphaned           Status = "orphaned"
)

type Finding struct {
	ID           string   `json:"id,omitempty" yaml:"id,omitempty"`
	Summary      string   `json:"summary" yaml:"summary"`
	EvidenceRefs []string `json:"evidence_refs,omitempty" yaml:"evidence_refs,omitempty"`
}

type Change struct {
	Path         string   `json:"path,omitempty" yaml:"path,omitempty"`
	Summary      string   `json:"summary,omitempty" yaml:"summary,omitempty"`
	Status       string   `json:"status,omitempty" yaml:"status,omitempty"`
	ArtifactRefs []string `json:"artifact_refs,omitempty" yaml:"artifact_refs,omitempty"`
	EvidenceRefs []string `json:"evidence_refs,omitempty" yaml:"evidence_refs,omitempty"`
}

type Artifact struct {
	ID          string `json:"id" yaml:"id"`
	Kind        string `json:"kind,omitempty" yaml:"kind,omitempty"`
	ContentType string `json:"content_type,omitempty" yaml:"content_type,omitempty"`
	URI         string `json:"uri,omitempty" yaml:"uri,omitempty"`
	SHA256      string `json:"sha256,omitempty" yaml:"sha256,omitempty"`
}

type Evidence struct {
	Ref           string `json:"ref" yaml:"ref"`
	Kind          string `json:"kind,omitempty" yaml:"kind,omitempty"`
	URI           string `json:"uri,omitempty" yaml:"uri,omitempty"`
	Summary       string `json:"summary,omitempty" yaml:"summary,omitempty"`
	SourceEventID string `json:"source_event_id,omitempty" yaml:"source_event_id,omitempty"`
}

type Error struct {
	Code         string   `json:"code,omitempty" yaml:"code,omitempty"`
	Message      string   `json:"message" yaml:"message"`
	Retryable    bool     `json:"retryable,omitempty" yaml:"retryable,omitempty"`
	EvidenceRefs []string `json:"evidence_refs,omitempty" yaml:"evidence_refs,omitempty"`
}

type Usage struct {
	InputTokens  int   `json:"input_tokens,omitempty" yaml:"input_tokens,omitempty"`
	OutputTokens int   `json:"output_tokens,omitempty" yaml:"output_tokens,omitempty"`
	TotalTokens  int   `json:"total_tokens,omitempty" yaml:"total_tokens,omitempty"`
	ToolCalls    int   `json:"tool_calls,omitempty" yaml:"tool_calls,omitempty"`
	DurationMS   int64 `json:"duration_ms,omitempty" yaml:"duration_ms,omitempty"`
}

// Result is the machine-readable result contract used across all agent roles.
type Result struct {
	Status             Status     `json:"status" yaml:"status"`
	Summary            string     `json:"summary" yaml:"summary"`
	Findings           []Finding  `json:"findings,omitempty" yaml:"findings,omitempty"`
	Changes            []Change   `json:"changes,omitempty" yaml:"changes,omitempty"`
	Artifacts          []Artifact `json:"artifacts,omitempty" yaml:"artifacts,omitempty"`
	Evidence           []Evidence `json:"evidence,omitempty" yaml:"evidence,omitempty"`
	RemainingWork      []string   `json:"remaining_work,omitempty" yaml:"remaining_work,omitempty"`
	Errors             []Error    `json:"errors,omitempty" yaml:"errors,omitempty"`
	Usage              Usage      `json:"usage" yaml:"usage"`
	TraceID            string     `json:"trace_id,omitempty" yaml:"trace_id,omitempty"`
	ExecutionEventRefs []string   `json:"execution_event_refs,omitempty" yaml:"execution_event_refs,omitempty"`
}

func (r Result) Validate() error {
	if !ValidStatus(r.Status) {
		return fmt.Errorf("invalid agent result status %q", r.Status)
	}
	if strings.TrimSpace(r.Summary) == "" {
		return fmt.Errorf("agent result summary is required")
	}
	for _, finding := range r.Findings {
		if strings.TrimSpace(finding.Summary) == "" {
			return fmt.Errorf("finding summary is required")
		}
	}
	for _, item := range r.Errors {
		if strings.TrimSpace(item.Message) == "" {
			return fmt.Errorf("agent result error message is required")
		}
	}
	return nil
}

func ValidStatus(status Status) bool {
	switch status {
	case StatusSucceeded, StatusPartiallyCompleted, StatusBlocked, StatusFailed,
		StatusTimedOut, StatusCanceled, StatusOrphaned:
		return true
	default:
		return false
	}
}
