package team

import "strings"

const (
	FinalSummarySourceLead     = "lead"
	FinalSummarySourceFallback = "fallback"
)

const (
	FinalSummaryFallbackSessionsNotConfigured = "sessions_not_configured"
	FinalSummaryFallbackTeamNotAvailable      = "team_not_available"
	FinalSummaryFallbackLeadSessionMissing    = "lead_session_missing"
	FinalSummaryFallbackLeadSessionError      = "lead_session_error"
	FinalSummaryFallbackLeadOutputEmpty       = "lead_output_empty"
)

// FinalSummaryResult captures the final summary together with whether the lead
// summary path succeeded or the planner had to fall back to task-derived text.
type FinalSummaryResult struct {
	Summary        string
	SummarySource  string
	UsedFallback   bool
	FallbackReason string
	TraceID        string
	ErrorType      string
	ErrorMetadata  map[string]interface{}
	SessionError   error
}

func (r *FinalSummaryResult) CloneErrorMetadata() map[string]interface{} {
	if r == nil || len(r.ErrorMetadata) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(r.ErrorMetadata))
	for key, value := range r.ErrorMetadata {
		cloned[key] = value
	}
	return cloned
}

func (r *FinalSummaryResult) HasSessionError() bool {
	return r != nil && r.SessionError != nil
}

func (r *FinalSummaryResult) IsPromptPreflight() bool {
	return r != nil && strings.EqualFold(strings.TrimSpace(r.ErrorType), "prompt_preflight")
}
