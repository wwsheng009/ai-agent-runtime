package chataloganalytics

import "time"

// Query filters chat-log analytics scans.
type Query struct {
	// From/To filter by session start time (inclusive/exclusive).
	From time.Time
	To   time.Time

	Provider  string
	Model     string
	Directory string // date partition or relative path prefix, e.g. "2026/07/27" or "2026/07"
	Project   string // normalized engineering project root, independent from the log date partition
	Status    string
	Query     string // free-text match against session_id/provider/model/directory/project

	// GroupBy controls summary breakdown: day | provider | model | directory | project | status
	GroupBy string

	Limit  int
	Offset int

	// MaxScan caps how many session directories are considered before filtering/sorting.
	// Zero uses the package default.
	MaxScan int
}

// SessionRollup is a coarse per-session usage record for list/global views.
type SessionRollup struct {
	SessionID             string    `json:"session_id"`
	RuntimeSessionID      string    `json:"runtime_session_id,omitempty"`
	Title                 string    `json:"title,omitempty"`
	TitleSource           string    `json:"title_source,omitempty"`
	Directory             string    `json:"directory"`
	Project               string    `json:"project,omitempty"`
	RelPath               string    `json:"-"`
	StartTime             time.Time `json:"start_time"`
	EndTime               time.Time `json:"end_time,omitempty"`
	LastObservedAt        time.Time `json:"last_observed_at,omitempty"`
	Status                string    `json:"status,omitempty"`
	Provider              string    `json:"provider,omitempty"`
	Protocol              string    `json:"protocol,omitempty"`
	Model                 string    `json:"model,omitempty"`
	BaseURL               string    `json:"-"`
	Stream                bool      `json:"stream,omitempty"`
	TotalRequests         int       `json:"total_requests"`
	TotalResponses        int       `json:"total_responses"`
	TotalToolCalls        int       `json:"total_tool_calls"`
	TotalTokens           int       `json:"total_tokens"`
	PromptTokens          int       `json:"prompt_tokens,omitempty"`
	CompletionTokens      int       `json:"completion_tokens,omitempty"`
	CachedTokens          int       `json:"cached_tokens,omitempty"`
	ReasoningTokens       int       `json:"reasoning_tokens,omitempty"`
	LLMRequests           int       `json:"llm_requests,omitempty"`
	LLMRequestsWithUsage  int       `json:"llm_requests_with_usage,omitempty"`
	LLMSuccesses          int       `json:"llm_successes,omitempty"`
	LLMErrors             int       `json:"llm_errors,omitempty"`
	TurnCount             int       `json:"turn_count"`
	FailedTurns           int       `json:"failed_turns"`
	RecoveredTurns        int       `json:"recovered_turns"`
	ToolResultsObserved   int       `json:"tool_results_observed"`
	ToolErrors            int       `json:"tool_errors"`
	AverageResponseTimeMs int64     `json:"average_response_time_ms,omitempty"`
	TotalDurationMs       int64     `json:"total_duration_ms,omitempty"`
	HasDebugUsage         bool      `json:"has_debug_usage,omitempty"`
	Source                string    `json:"source,omitempty"` // summary | debug | mixed
	UsageQuality          string    `json:"usage_quality"`
	UsageComplete         bool      `json:"usage_complete"`
	UsageCoverage         float64   `json:"usage_coverage"`
	Partial               bool      `json:"partial"`
	PartialReasons        []string  `json:"partial_reasons"`
	DroppedMessages       int       `json:"dropped_messages"`
	ReconciliationStatus  string    `json:"reconciliation_status"`
	ReconciliationDelta   int       `json:"reconciliation_delta"`
}

// TokenTotals aggregates token counters.
type TokenTotals struct {
	TotalTokens      int `json:"total_tokens"`
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens"`
}

// GlobalTotals is the overall rollup across matched sessions.
type GlobalTotals struct {
	Sessions              int   `json:"sessions"`
	TotalRequests         int   `json:"total_requests"`
	TotalResponses        int   `json:"total_responses"`
	TotalToolCalls        int   `json:"total_tool_calls"`
	LLMRequests           int   `json:"llm_requests"`
	LLMSuccesses          int   `json:"llm_successes"`
	LLMErrors             int   `json:"llm_errors"`
	Turns                 int   `json:"turns"`
	FailedTurns           int   `json:"failed_turns"`
	RecoveredTurns        int   `json:"recovered_turns"`
	ToolResultsObserved   int   `json:"tool_results_observed"`
	ToolErrors            int   `json:"tool_errors"`
	TotalDurationMs       int64 `json:"total_duration_ms"`
	AverageResponseTimeMs int64 `json:"average_response_time_ms,omitempty"`
	TokenTotals
}

// GroupBucket is one multi-dimension summary row.
type GroupBucket struct {
	Key                   string `json:"key"`
	Sessions              int    `json:"sessions"`
	TotalRequests         int    `json:"total_requests"`
	TotalResponses        int    `json:"total_responses"`
	TotalToolCalls        int    `json:"total_tool_calls"`
	LLMRequests           int    `json:"llm_requests"`
	LLMSuccesses          int    `json:"llm_successes"`
	LLMErrors             int    `json:"llm_errors"`
	Turns                 int    `json:"turns"`
	FailedTurns           int    `json:"failed_turns"`
	RecoveredTurns        int    `json:"recovered_turns"`
	ToolResultsObserved   int    `json:"tool_results_observed"`
	ToolErrors            int    `json:"tool_errors"`
	TotalDurationMs       int64  `json:"total_duration_ms"`
	AverageResponseTimeMs int64  `json:"average_response_time_ms,omitempty"`
	TokenTotals
}

const SchemaVersion = "runtime.analytics.v1"

// Coverage reports whether the aggregate can be interpreted as a complete
// lifetime total or only as the currently retained diagnostic window.
type Coverage struct {
	Sessions             int     `json:"sessions"`
	SessionsWithUsage    int     `json:"sessions_with_usage"`
	UsageSessionRate     float64 `json:"usage_session_rate"`
	LLMRequests          int     `json:"llm_requests"`
	LLMRequestsWithUsage int     `json:"llm_requests_with_usage"`
	UsageRequestRate     float64 `json:"usage_request_rate"`
	ToolResultsObserved  int     `json:"tool_results_observed"`
	DroppedMessages      int     `json:"dropped_messages"`
}

type DataWindow struct {
	From time.Time `json:"from,omitempty"`
	To   time.Time `json:"to,omitempty"`
}

// SummaryResult is returned by global multi-dimension aggregation.
type SummaryResult struct {
	SchemaVersion  string        `json:"schema_version"`
	GeneratedAt    time.Time     `json:"generated_at"`
	GroupBy        string        `json:"group_by"`
	Totals         GlobalTotals  `json:"totals"`
	Groups         []GroupBucket `json:"groups"`
	Scanned        int           `json:"scanned"`
	Matched        int           `json:"matched"`
	DataWindow     DataWindow    `json:"data_window"`
	Coverage       Coverage      `json:"coverage"`
	Partial        bool          `json:"partial"`
	PartialReasons []string      `json:"partial_reasons"`
}

// ListResult is returned by session list analytics.
type ListResult struct {
	SchemaVersion  string          `json:"schema_version"`
	GeneratedAt    time.Time       `json:"generated_at"`
	Sessions       []SessionRollup `json:"sessions"`
	Count          int             `json:"count"`
	Total          int             `json:"total"`
	Limit          int             `json:"limit"`
	Offset         int             `json:"offset"`
	Scanned        int             `json:"scanned"`
	Totals         GlobalTotals    `json:"totals"`
	DataWindow     DataWindow      `json:"data_window"`
	Coverage       Coverage        `json:"coverage"`
	Partial        bool            `json:"partial"`
	PartialReasons []string        `json:"partial_reasons"`
}

// DimensionsResult contains the finite values used by analytics filter controls.
type DimensionsResult struct {
	SchemaVersion string    `json:"schema_version"`
	GeneratedAt   time.Time `json:"generated_at"`
	Providers     []string  `json:"providers"`
	Models        []string  `json:"models"`
	Directories   []string  `json:"directories"`
	Projects      []string  `json:"projects"`
	Statuses      []string  `json:"statuses"`
}

// StepUsage is one LLM request_finished step from debug.log.
type StepUsage struct {
	StartedAt           time.Time `json:"started_at,omitempty"`
	Timestamp           time.Time `json:"timestamp,omitempty"`
	TraceID             string    `json:"trace_id,omitempty"`
	Step                int       `json:"step,omitempty"`
	Success             bool      `json:"success"`
	PromptTokens        int       `json:"prompt_tokens,omitempty"`
	CompletionTokens    int       `json:"completion_tokens,omitempty"`
	TotalTokens         int       `json:"total_tokens,omitempty"`
	CachedTokens        int       `json:"cached_tokens,omitempty"`
	CacheReadTokens     int       `json:"cache_read_tokens,omitempty"`
	CacheReadReported   bool      `json:"cache_read_reported,omitempty"`
	CacheHitRatio       float64   `json:"cache_hit_ratio,omitempty"`
	CacheStatus         string    `json:"cache_status,omitempty"`
	ReasoningTokens     int       `json:"reasoning_tokens,omitempty"`
	UsageSource         string    `json:"usage_source,omitempty"`
	UsageAvailable      bool      `json:"usage_available"`
	ErrorCategory       string    `json:"error_category,omitempty"`
	DurationMs          int64     `json:"duration_ms,omitempty"`
	ContextPromptTokens int       `json:"context_prompt_tokens,omitempty"`
	ContextWindowTokens int       `json:"context_window_tokens,omitempty"`
	PromptBudget        int       `json:"prompt_budget,omitempty"`
	ContextUtilization  float64   `json:"context_utilization,omitempty"`
}

type TurnUsage struct {
	TurnID                string      `json:"turn_id"`
	TraceID               string      `json:"trace_id"`
	Ordinal               int         `json:"ordinal"`
	StartedAt             time.Time   `json:"started_at,omitempty"`
	EndedAt               time.Time   `json:"ended_at,omitempty"`
	DurationMs            int64       `json:"duration_ms"`
	Outcome               string      `json:"outcome"`
	ErrorCategory         string      `json:"error_category,omitempty"`
	LLMRequests           int         `json:"llm_requests"`
	LLMSuccesses          int         `json:"llm_successes"`
	LLMErrors             int         `json:"llm_errors"`
	ToolResultsObserved   int         `json:"tool_results_observed"`
	ToolErrors            int         `json:"tool_errors"`
	Usage                 TokenTotals `json:"usage"`
	UsageQuality          string      `json:"usage_quality"`
	UsageCoverage         float64     `json:"usage_coverage"`
	MaxContextUtilization float64     `json:"max_context_utilization"`
}

type Diagnostic struct {
	Code          string  `json:"code"`
	Severity      string  `json:"severity"`
	Count         int     `json:"count"`
	Rate          float64 `json:"rate,omitempty"`
	TurnID        string  `json:"turn_id,omitempty"`
	ErrorCategory string  `json:"error_category,omitempty"`
}

// SessionUsageDetail is the per-session analytics payload.
type SessionUsageDetail struct {
	SchemaVersion   string         `json:"schema_version"`
	GeneratedAt     time.Time      `json:"generated_at"`
	Session         SessionRollup  `json:"session"`
	Steps           []StepUsage    `json:"steps"`
	StepCount       int            `json:"step_count"`
	Turns           []TurnUsage    `json:"turns"`
	Diagnostics     []Diagnostic   `json:"diagnostics"`
	ErrorCategories map[string]int `json:"error_categories"`
	Coverage        Coverage       `json:"coverage"`
	Partial         bool           `json:"partial"`
	PartialReasons  []string       `json:"partial_reasons"`
}
