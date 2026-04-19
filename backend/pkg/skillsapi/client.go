package skillsapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Client struct {
	baseURL    string
	httpClient *http.Client
	adminToken string
	headers    http.Header
}

type Option func(*Client)

const agentChatEndpoint = "/api/agent/chat"

func WithHTTPClient(client *http.Client) Option {
	return func(c *Client) {
		if client != nil {
			c.httpClient = client
		}
	}
}

func WithAdminToken(token string) Option {
	return func(c *Client) {
		c.adminToken = strings.TrimSpace(token)
	}
}

func WithHeader(key, value string) Option {
	return func(c *Client) {
		if key == "" {
			return
		}
		c.headers.Set(key, value)
	}
}

func WithTenantID(tenantID string) Option {
	return WithHeader("X-Skills-Tenant", tenantID)
}

func WithProjectID(projectID string) Option {
	return WithHeader("X-Skills-Project", projectID)
}

func NewClient(baseURL string, opts ...Option) *Client {
	client := &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		headers: make(http.Header),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(client)
		}
	}
	return client
}

type APIError struct {
	StatusCode int                    `json:"status_code"`
	Message    string                 `json:"message"`
	Code       string                 `json:"code,omitempty"`
	Context    map[string]interface{} `json:"context,omitempty"`
	Body       string                 `json:"body,omitempty"`
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return fmt.Sprintf("runtime api returned %d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("runtime api returned %d", e.StatusCode)
}

func (e *APIError) HasCode(code string) bool {
	if e == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(e.Code), strings.TrimSpace(code))
}

func (e *APIError) ContextValue(key string) (interface{}, bool) {
	if e == nil || e.Context == nil {
		return nil, false
	}
	value, ok := e.Context[key]
	return value, ok
}

type APIErrorGovernance struct {
	Policy        string
	Scope         string
	MCPName       string
	MCPTrustLevel string
	ExecutionMode string
}

func (e *APIError) Governance() APIErrorGovernance {
	if e == nil {
		return APIErrorGovernance{}
	}
	return APIErrorGovernance{
		Policy:        contextStringValue(e.Context, "policy"),
		Scope:         contextStringValue(e.Context, "governance_scope"),
		MCPName:       contextStringValue(e.Context, "mcp_name"),
		MCPTrustLevel: contextStringValue(e.Context, "mcp_trust_level"),
		ExecutionMode: contextStringValue(e.Context, "execution_mode"),
	}
}

func (g APIErrorGovernance) Present() bool {
	return g.Policy != "" || g.Scope != "" || g.MCPName != "" || g.MCPTrustLevel != "" || g.ExecutionMode != ""
}

func (g APIErrorGovernance) IsSandboxPolicy() bool {
	return strings.EqualFold(g.Policy, "sandbox")
}

func (g APIErrorGovernance) IsMCPGovernance() bool {
	return strings.EqualFold(g.Scope, "mcp")
}

func (g APIErrorGovernance) IsRemoteMCP() bool {
	return strings.EqualFold(g.ExecutionMode, "remote_mcp")
}

func (g APIErrorGovernance) IsLocalMCP() bool {
	return strings.EqualFold(g.ExecutionMode, "local_mcp")
}

func (g APIErrorGovernance) IsTrustedRemote() bool {
	return strings.EqualFold(g.MCPTrustLevel, "trusted_remote")
}

func (g APIErrorGovernance) IsUntrustedRemote() bool {
	return strings.EqualFold(g.MCPTrustLevel, "untrusted_remote")
}

type SkillSource struct {
	Path       string `json:"path,omitempty"`
	Dir        string `json:"dir,omitempty"`
	Layer      string `json:"layer,omitempty"`
	PromptPath string `json:"prompt_path,omitempty"`
}

type Trigger struct {
	Type   string   `json:"type"`
	Values []string `json:"values,omitempty"`
	Weight float64  `json:"weight,omitempty"`
}

type WorkflowStep struct {
	ID        string                 `json:"id,omitempty"`
	Name      string                 `json:"name,omitempty"`
	Tool      string                 `json:"tool,omitempty"`
	Args      map[string]interface{} `json:"args,omitempty"`
	DependsOn []string               `json:"dependsOn,omitempty"`
	Condition string                 `json:"condition,omitempty"`
}

type Workflow struct {
	Steps []WorkflowStep `json:"steps,omitempty"`
}

type ContextConfig struct {
	Files       []string `json:"files,omitempty"`
	Environment []string `json:"environment,omitempty"`
	Symbols     []string `json:"symbols,omitempty"`
}

type Skill struct {
	Name         string        `json:"name"`
	Description  string        `json:"description,omitempty"`
	Version      string        `json:"version,omitempty"`
	Category     string        `json:"category,omitempty"`
	Capabilities []string      `json:"capabilities,omitempty"`
	Tags         []string      `json:"tags,omitempty"`
	Triggers     []Trigger     `json:"triggers,omitempty"`
	Tools        []string      `json:"tools,omitempty"`
	SystemPrompt string        `json:"systemPrompt,omitempty"`
	UserPrompt   string        `json:"userPrompt,omitempty"`
	Workflow     *Workflow     `json:"workflow,omitempty"`
	Context      ContextConfig `json:"context,omitempty"`
	Permissions  []string      `json:"permissions,omitempty"`
	Source       *SkillSource  `json:"source,omitempty"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type UsageScope struct {
	TenantID  string `json:"tenant_id,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
	UserID    string `json:"user_id,omitempty"`
	ScopeKey  string `json:"scope_key,omitempty"`
}

type ListSkillsParams struct {
	SourceLayer string
	SourceDir   string
}

type ListSkillsResponse struct {
	Skills []Skill `json:"skills"`
	Count  int     `json:"count"`
}

type SearchSkillsParams struct {
	Query       string
	Limit       int
	Category    string
	Mode        string
	SourceLayer string
	SourceDir   string
}

type SearchMatch struct {
	Skill     Skill   `json:"skill"`
	Score     float64 `json:"score,omitempty"`
	MatchedBy string  `json:"matched_by,omitempty"`
	Details   string  `json:"details,omitempty"`
}

type SearchSkillsResponse struct {
	Query         string        `json:"query"`
	Results       []Skill       `json:"results"`
	Matches       []SearchMatch `json:"matches"`
	Count         int           `json:"count"`
	Limit         int           `json:"limit"`
	RequestedMode string        `json:"requested_mode"`
	ResolvedMode  string        `json:"resolved_mode"`
	UsedEmbedding bool          `json:"used_embedding"`
}

type ExecuteSkillRequest struct {
	Prompt    string                 `json:"prompt"`
	Params    map[string]interface{} `json:"params,omitempty"`
	Context   map[string]interface{} `json:"context,omitempty"`
	History   []Message              `json:"history,omitempty"`
	Options   map[string]interface{} `json:"options,omitempty"`
	SessionID string                 `json:"session_id,omitempty"`
	UserID    string                 `json:"user_id,omitempty"`
	TenantID  string                 `json:"tenant_id,omitempty"`
	ProjectID string                 `json:"project_id,omitempty"`
}

type ExecuteSkillResponse struct {
	Skill     string                 `json:"skill"`
	Status    string                 `json:"status"`
	Result    map[string]interface{} `json:"result"`
	SessionID string                 `json:"session_id,omitempty"`

	decodedResultOnce sync.Once           `json:"-"`
	decodedResult     *ExecuteSkillResult `json:"-"`
	decodedResultErr  error               `json:"-"`
}

type Observation struct {
	Step      string                 `json:"step"`
	Tool      string                 `json:"tool"`
	Input     interface{}            `json:"input,omitempty"`
	Output    interface{}            `json:"output,omitempty"`
	Success   bool                   `json:"success"`
	Error     string                 `json:"error,omitempty"`
	Metrics   map[string]interface{} `json:"metrics,omitempty"`
	Timestamp time.Time              `json:"timestamp,omitempty"`
	Duration  map[string]interface{} `json:"duration,omitempty"`
}

type ObservationGovernance struct {
	MCPName       string
	MCPTrustLevel string
	ExecutionMode string
}

func (o Observation) Governance() ObservationGovernance {
	return ObservationGovernance{
		MCPName:       stringMetricValue(o.Metrics, "mcp_name"),
		MCPTrustLevel: stringMetricValue(o.Metrics, "mcp_trust_level"),
		ExecutionMode: stringMetricValue(o.Metrics, "execution_mode"),
	}
}

func (g ObservationGovernance) Present() bool {
	return g.MCPName != "" || g.MCPTrustLevel != "" || g.ExecutionMode != ""
}

func (g ObservationGovernance) IsRemoteMCP() bool {
	return strings.EqualFold(g.ExecutionMode, "remote_mcp")
}

func (g ObservationGovernance) IsLocalMCP() bool {
	return strings.EqualFold(g.ExecutionMode, "local_mcp")
}

func (g ObservationGovernance) IsTrustedRemote() bool {
	return strings.EqualFold(g.MCPTrustLevel, "trusted_remote")
}

func (g ObservationGovernance) IsUntrustedRemote() bool {
	return strings.EqualFold(g.MCPTrustLevel, "untrusted_remote")
}

type GovernanceSummary struct {
	MCPNames             []string
	LocalMCPCount        int
	RemoteMCPCount       int
	TrustedRemoteCount   int
	UntrustedRemoteCount int
}

func (g GovernanceSummary) HasMCP() bool {
	return len(g.MCPNames) > 0
}

func (g GovernanceSummary) UsesRemoteMCP() bool {
	return g.RemoteMCPCount > 0
}

func (g GovernanceSummary) UsesTrustedRemoteMCP() bool {
	return g.TrustedRemoteCount > 0
}

func (g GovernanceSummary) UsesUntrustedRemoteMCP() bool {
	return g.UntrustedRemoteCount > 0
}

type ExecuteSkillResult struct {
	Success      bool                   `json:"success"`
	Output       string                 `json:"output"`
	SkillName    string                 `json:"skillName,omitempty"`
	Skill        string                 `json:"skill,omitempty"`
	Observations []Observation          `json:"observations,omitempty"`
	Error        string                 `json:"error,omitempty"`
	ErrorCode    string                 `json:"error_code,omitempty"`
	ErrorContext map[string]interface{} `json:"error_context,omitempty"`
	Usage        map[string]interface{} `json:"usage,omitempty"`
}

func (r *ExecuteSkillResult) GovernanceSummary() GovernanceSummary {
	if r == nil {
		return GovernanceSummary{}
	}
	return buildGovernanceSummary(r.Observations)
}

func (r *ExecuteSkillResponse) DecodeResult() (*ExecuteSkillResult, error) {
	if r == nil || r.Result == nil {
		return nil, nil
	}
	r.decodedResultOnce.Do(func() {
		r.decodedResult, r.decodedResultErr = decodeMapPayload[ExecuteSkillResult](r.Result)
	})
	return r.decodedResult, r.decodedResultErr
}

type AgentChatRequest struct {
	Messages                   []Message `json:"messages"`
	SessionID                  string    `json:"session_id,omitempty"`
	UserID                     string    `json:"user_id,omitempty"`
	TenantID                   string    `json:"tenant_id,omitempty"`
	ProjectID                  string    `json:"project_id,omitempty"`
	WorkspacePath              string    `json:"workspace_path,omitempty"`
	MaxSteps                   int       `json:"max_steps,omitempty"`
	EnableRouting              bool      `json:"enable_routing,omitempty"`
	PlanningMode               string    `json:"planning_mode,omitempty"`
	ExecutePlannedSubagents    bool      `json:"execute_planned_subagents,omitempty"`
	AllowWritePlannedSubagents bool      `json:"allow_write_planned_subagents,omitempty"`
	Stream                     bool      `json:"stream,omitempty"`
}

type AgentChatResponse struct {
	SessionID string                 `json:"session_id,omitempty"`
	AgentID   string                 `json:"agent_id,omitempty"`
	Result    map[string]interface{} `json:"result"`
	Source    string                 `json:"source,omitempty"`
	Status    string                 `json:"status,omitempty"`

	decodedResultOnce sync.Once        `json:"-"`
	decodedResult     *AgentChatResult `json:"-"`
	decodedResultErr  error            `json:"-"`
}

type AgentChatResult struct {
	Kind            string                   `json:"kind,omitempty"`
	Source          string                   `json:"source,omitempty"`
	Success         bool                     `json:"success"`
	Output          string                   `json:"output,omitempty"`
	Model           string                   `json:"model,omitempty"`
	Skill           string                   `json:"skill,omitempty"`
	Steps           int                      `json:"steps,omitempty"`
	Observations    []Observation            `json:"observations,omitempty"`
	State           map[string]interface{}   `json:"state,omitempty"`
	Usage           map[string]interface{}   `json:"usage,omitempty"`
	Duration        map[string]interface{}   `json:"duration,omitempty"`
	Error           string                   `json:"error,omitempty"`
	Orchestration   map[string]interface{}   `json:"orchestration,omitempty"`
	Planning        map[string]interface{}   `json:"planning,omitempty"`
	SubagentSummary map[string]interface{}   `json:"subagent_summary,omitempty"`
	SubagentResults []SubagentResultSummary  `json:"subagent_results,omitempty"`
	Metadata        map[string]interface{}   `json:"metadata,omitempty"`
	ToolCalls       []map[string]interface{} `json:"tool_calls,omitempty"`
	Reasoning       string                   `json:"reasoning,omitempty"`

	decodedOrchestrationOnce sync.Once             `json:"-"`
	decodedOrchestration     *OrchestrationSummary `json:"-"`
	decodedOrchestrationErr  error                 `json:"-"`

	decodedPlanningOnce sync.Once        `json:"-"`
	decodedPlanning     *PlanningSummary `json:"-"`
	decodedPlanningErr  error            `json:"-"`

	decodedSubagentSummaryOnce sync.Once        `json:"-"`
	decodedSubagentSummary     *SubagentSummary `json:"-"`
	decodedSubagentSummaryErr  error            `json:"-"`

	decodedToolCallsOnce sync.Once        `json:"-"`
	decodedToolCalls     []ResultToolCall `json:"-"`
	decodedToolCallsErr  error            `json:"-"`

	decodedUsageOnce sync.Once    `json:"-"`
	decodedUsage     *ResultUsage `json:"-"`
	decodedUsageErr  error        `json:"-"`

	decodedDurationOnce sync.Once       `json:"-"`
	decodedDuration     *ResultDuration `json:"-"`
	decodedDurationErr  error           `json:"-"`

	decodedStateOnce sync.Once    `json:"-"`
	decodedState     *ResultState `json:"-"`
	decodedStateErr  error        `json:"-"`
}

func (r *AgentChatResult) GovernanceSummary() GovernanceSummary {
	if r == nil {
		return GovernanceSummary{}
	}
	return buildGovernanceSummary(r.Observations)
}

func (r *AgentChatResult) MetadataValue(key string) (interface{}, bool) {
	if r == nil || r.Metadata == nil {
		return nil, false
	}
	return mapValue(r.Metadata, key)
}

func (r *AgentChatResult) MetadataString(key string) string {
	if r == nil {
		return ""
	}
	return mapStringValue(r.Metadata, key)
}

func (r *AgentChatResult) MetadataBool(key string) (bool, bool) {
	if r == nil {
		return false, false
	}
	return mapBoolValue(r.Metadata, key)
}

func (r *AgentChatResult) MetadataInt(key string) (int, bool) {
	if r == nil {
		return 0, false
	}
	return mapIntValue(r.Metadata, key)
}

func (r *AgentChatResult) MetadataMap(key string) (map[string]interface{}, bool) {
	if r == nil {
		return nil, false
	}
	return mapMapValue(r.Metadata, key)
}

type RouteCandidate struct {
	Skill           string  `json:"skill,omitempty"`
	Score           float64 `json:"score,omitempty"`
	MatchedBy       string  `json:"matched_by,omitempty"`
	Details         string  `json:"details,omitempty"`
	Chosen          bool    `json:"chosen,omitempty"`
	SelectionReason string  `json:"selection_reason,omitempty"`
}

type CapabilityCandidate struct {
	Descriptor *CapabilityDescriptor `json:"descriptor,omitempty"`
	Score      float64               `json:"score,omitempty"`
	MatchedBy  string                `json:"matched_by,omitempty"`
	Details    string                `json:"details,omitempty"`
}

type FailedObservationDetail struct {
	Step       string `json:"step,omitempty"`
	Tool       string `json:"tool,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

type ObservationSummary struct {
	Count                     int                       `json:"count,omitempty"`
	Successful                int                       `json:"successful,omitempty"`
	Failed                    int                       `json:"failed,omitempty"`
	Tools                     []string                  `json:"tools,omitempty"`
	FailedTools               []string                  `json:"failed_tools,omitempty"`
	FailedDetails             []FailedObservationDetail `json:"failed_details,omitempty"`
	StepDurationsMS           map[string]int64          `json:"step_durations_ms,omitempty"`
	TotalDurationMS           int64                     `json:"total_duration_ms,omitempty"`
	MaxDurationMS             int64                     `json:"max_duration_ms,omitempty"`
	AverageDurationMS         int64                     `json:"average_duration_ms,omitempty"`
	SubagentBatches           int                       `json:"subagent_batches,omitempty"`
	SubagentCount             int                       `json:"subagent_count,omitempty"`
	SubagentSuccessful        int                       `json:"subagent_successful,omitempty"`
	SubagentFailed            int                       `json:"subagent_failed,omitempty"`
	SubagentRoles             []string                  `json:"subagent_roles,omitempty"`
	SubagentPatchCount        int                       `json:"subagent_patch_count,omitempty"`
	SubagentAppliedPatchCount int                       `json:"subagent_applied_patch_count,omitempty"`
	SubagentPatchPaths        []string                  `json:"subagent_patch_paths,omitempty"`
}

type SubagentSummary struct {
	Batches           int      `json:"batches,omitempty"`
	Count             int      `json:"count,omitempty"`
	Successful        int      `json:"successful,omitempty"`
	Failed            int      `json:"failed,omitempty"`
	Roles             []string `json:"roles,omitempty"`
	PatchCount        int      `json:"patch_count,omitempty"`
	AppliedPatchCount int      `json:"applied_patch_count,omitempty"`
	PatchPaths        []string `json:"patch_paths,omitempty"`
}

type OrchestrationSummary struct {
	Source                         string                `json:"source,omitempty"`
	RouteAttempted                 bool                  `json:"route_attempted"`
	RouteMatched                   bool                  `json:"route_matched"`
	CandidateCount                 int                   `json:"candidate_count,omitempty"`
	RouteCandidates                []RouteCandidate      `json:"route_candidates,omitempty"`
	Capability                     *CapabilityDescriptor `json:"capability,omitempty"`
	CapabilityCandidates           []CapabilityCandidate `json:"capability_candidates,omitempty"`
	FallbackReason                 string                `json:"fallback_reason,omitempty"`
	Skill                          string                `json:"skill,omitempty"`
	Model                          string                `json:"model,omitempty"`
	Success                        bool                  `json:"success"`
	Steps                          int                   `json:"steps,omitempty"`
	ToolCallCount                  int                   `json:"tool_call_count,omitempty"`
	ObservationSummary             *ObservationSummary   `json:"observation_summary,omitempty"`
	OutputPreview                  string                `json:"output_preview,omitempty"`
	PlanningAttempted              bool                  `json:"planning_attempted,omitempty"`
	PlanningSource                 string                `json:"planning_source,omitempty"`
	PlanStepCount                  int                   `json:"plan_step_count,omitempty"`
	SubagentTaskCount              int                   `json:"subagent_task_count,omitempty"`
	SubagentExecutionRequested     bool                  `json:"subagent_execution_requested,omitempty"`
	SubagentExecutionEligible      bool                  `json:"subagent_execution_eligible,omitempty"`
	SubagentExecutionBlockedReason string                `json:"subagent_execution_blocked_reason,omitempty"`
	SubagentExecutionAttempted     bool                  `json:"subagent_execution_attempted,omitempty"`
	PlanningError                  string                `json:"planning_error,omitempty"`
}

func (o *OrchestrationSummary) SelectedRoute() *RouteCandidate {
	if o == nil {
		return nil
	}
	for i := range o.RouteCandidates {
		if o.RouteCandidates[i].Chosen {
			return &o.RouteCandidates[i]
		}
	}
	return nil
}

func (o *OrchestrationSummary) HasPlanningError() bool {
	return o != nil && strings.TrimSpace(o.PlanningError) != ""
}

type PlanningStep struct {
	ID          string   `json:"id,omitempty"`
	Description string   `json:"description,omitempty"`
	Tool        string   `json:"tool,omitempty"`
	DependsOn   []string `json:"depends_on,omitempty"`
	Priority    int      `json:"priority,omitempty"`
}

type PlanningSubagentTask struct {
	ID             string   `json:"id,omitempty"`
	Role           string   `json:"role,omitempty"`
	Goal           string   `json:"goal,omitempty"`
	ToolsWhitelist []string `json:"tools_whitelist,omitempty"`
	DependsOn      []string `json:"depends_on,omitempty"`
	ReadOnly       bool     `json:"read_only,omitempty"`
}

type SubagentPatchSummary struct {
	Path               string   `json:"path,omitempty"`
	Summary            string   `json:"summary,omitempty"`
	Diff               string   `json:"diff,omitempty"`
	ApplyStatus        string   `json:"apply_status,omitempty"`
	AppliedBy          []string `json:"applied_by,omitempty"`
	ArtifactRefs       []string `json:"artifact_refs,omitempty"`
	VerificationStatus string   `json:"verification_status,omitempty"`
	VerifiedBy         []string `json:"verified_by,omitempty"`
}

type ResultToolCall struct {
	ID        string                 `json:"id,omitempty"`
	Name      string                 `json:"name,omitempty"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

type ResultUsage struct {
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens,omitempty"`
}

type ResultDuration struct {
	Start time.Time `json:"start,omitempty"`
	End   time.Time `json:"end,omitempty"`
}

type ResultState struct {
	CurrentStep int         `json:"currentStep,omitempty"`
	Running     bool        `json:"running"`
	Errors      []string    `json:"errors,omitempty"`
	Context     interface{} `json:"context,omitempty"`
}

func (d *ResultDuration) Elapsed() time.Duration {
	if d == nil || d.Start.IsZero() || d.End.IsZero() {
		return 0
	}
	return d.End.Sub(d.Start)
}

func (s *ResultState) HasErrors() bool {
	return s != nil && len(s.Errors) > 0
}

func (s *ResultState) ContextMap() (map[string]interface{}, bool) {
	if s == nil || s.Context == nil {
		return nil, false
	}
	values, ok := s.Context.(map[string]interface{})
	return values, ok
}

type SubagentResultSummary struct {
	ID               string                 `json:"id,omitempty"`
	Role             string                 `json:"role,omitempty"`
	SessionID        string                 `json:"session_id,omitempty"`
	ParentSessionID  string                 `json:"parent_session_id,omitempty"`
	ParentToolCallID string                 `json:"parent_tool_call_id,omitempty"`
	ReadOnly         bool                   `json:"read_only,omitempty"`
	BudgetTokens     int                    `json:"budget_tokens,omitempty"`
	Success          bool                   `json:"success"`
	Summary          string                 `json:"summary,omitempty"`
	Patches          []SubagentPatchSummary `json:"patches,omitempty"`
	Findings         []string               `json:"findings,omitempty"`
	Error            string                 `json:"error,omitempty"`
}

type PlanningSummary struct {
	Mode                           string                 `json:"mode,omitempty"`
	Attempted                      bool                   `json:"attempted"`
	PlanningSource                 string                 `json:"planning_source,omitempty"`
	PlanningError                  string                 `json:"planning_error,omitempty"`
	StepCount                      int                    `json:"step_count,omitempty"`
	SubagentTaskCount              int                    `json:"subagent_task_count,omitempty"`
	SubagentExecutionRequested     bool                   `json:"subagent_execution_requested,omitempty"`
	SubagentExecutionEligible      bool                   `json:"subagent_execution_eligible,omitempty"`
	SubagentExecutionBlockedReason string                 `json:"subagent_execution_blocked_reason,omitempty"`
	SubagentExecutionAttempted     bool                   `json:"subagent_execution_attempted,omitempty"`
	SubagentExecutionError         string                 `json:"subagent_execution_error,omitempty"`
	SubagentResultCount            int                    `json:"subagent_result_count,omitempty"`
	SubagentPatchCount             int                    `json:"subagent_patch_count,omitempty"`
	SubagentAppliedPatchCount      int                    `json:"subagent_applied_patch_count,omitempty"`
	SubagentVerifiedPatchCount     int                    `json:"subagent_verified_patch_count,omitempty"`
	SubagentNeedsReviewPatchCount  int                    `json:"subagent_needs_review_patch_count,omitempty"`
	SubagentUnverifiedPatchCount   int                    `json:"subagent_unverified_patch_count,omitempty"`
	Goal                           string                 `json:"goal,omitempty"`
	Steps                          []PlanningStep         `json:"steps,omitempty"`
	SubagentTasks                  []PlanningSubagentTask `json:"subagent_tasks,omitempty"`
}

func (p *PlanningSummary) HasError() bool {
	return p != nil && strings.TrimSpace(p.PlanningError) != ""
}

func (r *AgentChatResult) DecodeOrchestration() (*OrchestrationSummary, error) {
	if r == nil || r.Orchestration == nil {
		return nil, nil
	}
	r.decodedOrchestrationOnce.Do(func() {
		r.decodedOrchestration, r.decodedOrchestrationErr = decodeMapPayload[OrchestrationSummary](r.Orchestration)
	})
	return r.decodedOrchestration, r.decodedOrchestrationErr
}

func (r *AgentChatResult) DecodePlanning() (*PlanningSummary, error) {
	if r == nil || r.Planning == nil {
		return nil, nil
	}
	r.decodedPlanningOnce.Do(func() {
		r.decodedPlanning, r.decodedPlanningErr = decodeMapPayload[PlanningSummary](r.Planning)
	})
	return r.decodedPlanning, r.decodedPlanningErr
}

func (r *AgentChatResult) DecodeSubagentSummary() (*SubagentSummary, error) {
	if r == nil || r.SubagentSummary == nil {
		return nil, nil
	}
	r.decodedSubagentSummaryOnce.Do(func() {
		r.decodedSubagentSummary, r.decodedSubagentSummaryErr = decodeMapPayload[SubagentSummary](r.SubagentSummary)
	})
	return r.decodedSubagentSummary, r.decodedSubagentSummaryErr
}

func (r *AgentChatResult) DecodeToolCalls() ([]ResultToolCall, error) {
	if r == nil || r.ToolCalls == nil {
		return nil, nil
	}
	r.decodedToolCallsOnce.Do(func() {
		r.decodedToolCalls, r.decodedToolCallsErr = decodeSlicePayload[ResultToolCall](r.ToolCalls)
	})
	return r.decodedToolCalls, r.decodedToolCallsErr
}

func (r *AgentChatResult) DecodeUsage() (*ResultUsage, error) {
	if r == nil || r.Usage == nil {
		return nil, nil
	}
	r.decodedUsageOnce.Do(func() {
		r.decodedUsage, r.decodedUsageErr = decodeMapPayload[ResultUsage](r.Usage)
	})
	return r.decodedUsage, r.decodedUsageErr
}

func (r *AgentChatResult) DecodeDuration() (*ResultDuration, error) {
	if r == nil || r.Duration == nil {
		return nil, nil
	}
	r.decodedDurationOnce.Do(func() {
		r.decodedDuration, r.decodedDurationErr = decodeMapPayload[ResultDuration](r.Duration)
	})
	return r.decodedDuration, r.decodedDurationErr
}

func (r *AgentChatResult) DecodeState() (*ResultState, error) {
	if r == nil || r.State == nil {
		return nil, nil
	}
	r.decodedStateOnce.Do(func() {
		r.decodedState, r.decodedStateErr = decodeMapPayload[ResultState](r.State)
	})
	return r.decodedState, r.decodedStateErr
}

func (r *AgentChatResponse) DecodeResult() (*AgentChatResult, error) {
	if r == nil || r.Result == nil {
		return nil, nil
	}
	r.decodedResultOnce.Do(func() {
		r.decodedResult, r.decodedResultErr = decodeMapPayload[AgentChatResult](r.Result)
	})
	return r.decodedResult, r.decodedResultErr
}

type MutationPolicy struct {
	ReadOnly         bool `json:"read_only"`
	DisableImport    bool `json:"disable_import"`
	DisablePersist   bool `json:"disable_persist"`
	DisableReloadOps bool `json:"disable_reload_ops"`
	DisableHotReload bool `json:"disable_hot_reload"`
}

type MutationPolicyUpdateRequest struct {
	ReadOnly         *bool `json:"read_only,omitempty"`
	DisableImport    *bool `json:"disable_import,omitempty"`
	DisablePersist   *bool `json:"disable_persist,omitempty"`
	DisableReloadOps *bool `json:"disable_reload_ops,omitempty"`
	DisableHotReload *bool `json:"disable_hot_reload,omitempty"`
}

type GetMutationPolicyResponse struct {
	Policy MutationPolicy `json:"policy"`
}

type UpdateMutationPolicyResponse struct {
	Updated bool           `json:"updated"`
	Policy  MutationPolicy `json:"policy"`
}

type GovernancePersistence struct {
	MutationPolicyEnabled bool `json:"mutation_policy_enabled"`
	UsagePolicyEnabled    bool `json:"usage_policy_enabled"`
	AuthPolicyEnabled     bool `json:"auth_policy_enabled"`
	UsageLedgerEnabled    bool `json:"usage_ledger_enabled"`
}

type GovernanceSearchAdmin struct {
	AdminTokenConfigured bool `json:"admin_token_configured"`
	ReindexCooldownSecs  int  `json:"reindex_cooldown_seconds"`
}

type GetGovernancePolicyResponse struct {
	MutationPolicy MutationPolicy        `json:"mutation_policy"`
	UsagePolicy    UsagePolicyDetails    `json:"usage_policy"`
	AuthPolicy     AuthPolicy            `json:"auth_policy"`
	Persistence    GovernancePersistence `json:"persistence"`
	SearchAdmin    GovernanceSearchAdmin `json:"search_admin"`
}

type UsagePolicy struct {
	TrackingEnabled    bool `json:"tracking_enabled"`
	LedgerEnabled      bool `json:"ledger_enabled"`
	QuotaEnabled       bool `json:"quota_enabled"`
	DefaultMaxRequests int  `json:"default_max_requests"`
	DefaultMaxTokens   int  `json:"default_max_tokens"`
	TenantQuotaCount   int  `json:"tenant_quota_count,omitempty"`
	ProjectQuotaCount  int  `json:"project_quota_count,omitempty"`
	UserQuotaCount     int  `json:"user_quota_count,omitempty"`
}

type UsageQuotaLimitConfig struct {
	MaxRequests *int `json:"max_requests,omitempty"`
	MaxTokens   *int `json:"max_tokens,omitempty"`
}

type UsagePolicyDetails struct {
	TrackingEnabled    bool                             `json:"tracking_enabled"`
	LedgerEnabled      bool                             `json:"ledger_enabled"`
	QuotaEnabled       bool                             `json:"quota_enabled"`
	DefaultMaxRequests int                              `json:"default_max_requests"`
	DefaultMaxTokens   int                              `json:"default_max_tokens"`
	Tenants            map[string]UsageQuotaLimitConfig `json:"tenants"`
	Projects           map[string]UsageQuotaLimitConfig `json:"projects"`
	Users              map[string]UsageQuotaLimitConfig `json:"users"`
}

type AuthPolicy struct {
	Enabled             bool     `json:"enabled"`
	JWTClaimsEnabled    bool     `json:"jwt_claims_enabled"`
	JWTSecretConfigured bool     `json:"jwt_secret_configured"`
	TenantHeaders       []string `json:"tenant_headers"`
	ProjectHeaders      []string `json:"project_headers"`
	UserHeaders         []string `json:"user_headers"`
	RoleHeaders         []string `json:"role_headers"`
	TenantClaims        []string `json:"tenant_claims"`
	ProjectClaims       []string `json:"project_claims"`
	UserClaims          []string `json:"user_claims"`
	RoleClaims          []string `json:"role_claims"`
	AdminRoles          []string `json:"admin_roles"`
	APIKeyScopeCount    int      `json:"api_key_scope_count"`
}

type GetAuthPolicyResponse struct {
	Policy AuthPolicy `json:"policy"`
}

type AuthPolicyUpdateRequest struct {
	Replace          bool                  `json:"replace,omitempty"`
	Enabled          *bool                 `json:"enabled,omitempty"`
	JWTClaimsEnabled *bool                 `json:"jwt_claims_enabled,omitempty"`
	TenantHeaders    []string              `json:"tenant_headers,omitempty"`
	ProjectHeaders   []string              `json:"project_headers,omitempty"`
	UserHeaders      []string              `json:"user_headers,omitempty"`
	RoleHeaders      []string              `json:"role_headers,omitempty"`
	TenantClaims     []string              `json:"tenant_claims,omitempty"`
	ProjectClaims    []string              `json:"project_claims,omitempty"`
	UserClaims       []string              `json:"user_claims,omitempty"`
	RoleClaims       []string              `json:"role_claims,omitempty"`
	AdminRoles       []string              `json:"admin_roles,omitempty"`
	APIKeyScopes     map[string]UsageScope `json:"api_key_scopes,omitempty"`
}

type UpdateAuthPolicyResponse struct {
	Updated bool       `json:"updated"`
	Policy  AuthPolicy `json:"policy"`
}

type DeleteAuthPolicyEntryRequest struct {
	Field string `json:"field"`
	Key   string `json:"key"`
}

type DeleteAuthPolicyEntryResponse struct {
	Deleted bool       `json:"deleted"`
	Field   string     `json:"field"`
	Key     string     `json:"key"`
	Policy  AuthPolicy `json:"policy"`
}

type UsagePolicyUpdateRequest struct {
	Replace            bool                             `json:"replace,omitempty"`
	TrackingEnabled    *bool                            `json:"tracking_enabled,omitempty"`
	QuotaEnabled       *bool                            `json:"quota_enabled,omitempty"`
	DefaultMaxRequests *int                             `json:"default_max_requests,omitempty"`
	DefaultMaxTokens   *int                             `json:"default_max_tokens,omitempty"`
	Tenants            map[string]UsageQuotaLimitConfig `json:"tenants,omitempty"`
	Projects           map[string]UsageQuotaLimitConfig `json:"projects,omitempty"`
	Users              map[string]UsageQuotaLimitConfig `json:"users,omitempty"`
}

type GetUsagePolicyResponse struct {
	Policy UsagePolicyDetails `json:"policy"`
}

type UpdateUsagePolicyResponse struct {
	Updated bool               `json:"updated"`
	Policy  UsagePolicyDetails `json:"policy"`
}

type DeleteUsagePolicyEntryRequest struct {
	Level string `json:"level"`
	Key   string `json:"key"`
}

type DeleteUsagePolicyEntryResponse struct {
	Deleted bool               `json:"deleted"`
	Level   string             `json:"level"`
	Key     string             `json:"key"`
	Policy  UsagePolicyDetails `json:"policy"`
}

type SkillStats struct {
	Name        string `json:"name"`
	Category    string `json:"category"`
	CallCount   int    `json:"call_count"`
	SuccessRate int    `json:"success_rate"`
	AvgDuration int    `json:"avg_duration_ms"`
	SourceDir   string `json:"source_dir,omitempty"`
	SourcePath  string `json:"source_path,omitempty"`
	SourceLayer string `json:"source_layer,omitempty"`
}

type GetStatsResponse struct {
	Stats               []SkillStats           `json:"stats"`
	TotalSkills         int                    `json:"total_skills"`
	SkillDirs           []string               `json:"skill_dirs"`
	SourceSummary       map[string]int         `json:"source_summary"`
	MutationPolicy      MutationPolicy         `json:"mutation_policy"`
	UsagePolicy         UsagePolicy            `json:"usage_policy"`
	ScopeResolverPolicy AuthPolicy             `json:"scope_resolver_policy"`
	Search              map[string]interface{} `json:"search"`
	Embedding           map[string]interface{} `json:"embedding"`
	Runtime             RuntimeStatus          `json:"runtime"`
	Validation          RuntimeValidation      `json:"validation"`

	decodedSearchOnce    sync.Once             `json:"-"`
	decodedSearch        *SearchTelemetryStats `json:"-"`
	decodedSearchErr     error                 `json:"-"`
	decodedEmbeddingOnce sync.Once             `json:"-"`
	decodedEmbedding     *EmbeddingStatus      `json:"-"`
	decodedEmbeddingErr  error                 `json:"-"`
}

type SearchTelemetryStats struct {
	TotalRequests      int            `json:"total_requests"`
	TotalResults       int            `json:"total_results"`
	AverageResults     float64        `json:"average_results"`
	EmbeddingRequests  int            `json:"embedding_requests"`
	RequestedModeCount map[string]int `json:"requested_mode_count,omitempty"`
	ResolvedModeCount  map[string]int `json:"resolved_mode_count,omitempty"`
	LastQuery          string         `json:"last_query,omitempty"`
	LastRequestedMode  string         `json:"last_requested_mode,omitempty"`
	LastResolvedMode   string         `json:"last_resolved_mode,omitempty"`
	LastResultCount    int            `json:"last_result_count,omitempty"`
	LastUsedEmbedding  bool           `json:"last_used_embedding,omitempty"`
	ReindexCount       int            `json:"reindex_count,omitempty"`
	LastReindexStatus  string         `json:"last_reindex_status,omitempty"`
	LastReindexAt      time.Time      `json:"last_reindex_at,omitempty"`
}

func (s *SearchTelemetryStats) HasSearchTraffic() bool {
	return s != nil && s.TotalRequests > 0
}

func (s *SearchTelemetryStats) HasReindexHistory() bool {
	return s != nil && (s.ReindexCount > 0 || !s.LastReindexAt.IsZero())
}

type EmbeddingRouterStats struct {
	IndexSize int     `json:"indexSize"`
	Threshold float64 `json:"threshold"`
	TopK      int     `json:"topK"`
}

type EmbeddingStatus struct {
	Enabled bool                  `json:"enabled"`
	Stats   *EmbeddingRouterStats `json:"stats,omitempty"`
}

func (e *EmbeddingStatus) Indexed() bool {
	return e != nil && e.Stats != nil && e.Stats.IndexSize > 0
}

func (r *GetStatsResponse) DecodeSearch() (*SearchTelemetryStats, error) {
	if r == nil || r.Search == nil {
		return nil, nil
	}
	r.decodedSearchOnce.Do(func() {
		r.decodedSearch, r.decodedSearchErr = decodeMapPayload[SearchTelemetryStats](r.Search)
	})
	return r.decodedSearch, r.decodedSearchErr
}

func (r *GetStatsResponse) DecodeEmbedding() (*EmbeddingStatus, error) {
	if r == nil || r.Embedding == nil {
		return nil, nil
	}
	r.decodedEmbeddingOnce.Do(func() {
		r.decodedEmbedding, r.decodedEmbeddingErr = decodeMapPayload[EmbeddingStatus](r.Embedding)
	})
	return r.decodedEmbedding, r.decodedEmbeddingErr
}

type RuntimeProviderStatus struct {
	Name                 string    `json:"name"`
	Healthy              bool      `json:"healthy"`
	Status               string    `json:"status,omitempty"`
	Error                string    `json:"error,omitempty"`
	ConsecutiveFailures  int       `json:"consecutive_failures,omitempty"`
	ConsecutiveSuccesses int       `json:"consecutive_successes,omitempty"`
	LastCheck            time.Time `json:"last_check,omitempty"`
	LastSuccess          time.Time `json:"last_success,omitempty"`
	LastFailure          time.Time `json:"last_failure,omitempty"`
	SupportsTools        bool      `json:"supports_tools,omitempty"`
	SupportsStreaming    bool      `json:"supports_streaming,omitempty"`
	MaxContextTokens     int       `json:"max_context_tokens,omitempty"`
	MaxOutputTokens      int       `json:"max_output_tokens,omitempty"`
}

type RuntimeMCPStatus struct {
	Name          string    `json:"name"`
	Type          string    `json:"type"`
	TrustLevel    string    `json:"trust_level,omitempty"`
	ExecutionMode string    `json:"execution_mode,omitempty"`
	Enabled       bool      `json:"enabled"`
	Connected     bool      `json:"connected"`
	ToolCount     int       `json:"tool_count"`
	LastError     string    `json:"last_error,omitempty"`
	LastConnect   time.Time `json:"last_connect,omitempty"`
	HealthCheck   time.Time `json:"health_check,omitempty"`
}

func (s RuntimeMCPStatus) IsRemoteMCP() bool {
	return strings.EqualFold(s.ExecutionMode, "remote_mcp")
}

func (s RuntimeMCPStatus) IsLocalMCP() bool {
	return strings.EqualFold(s.ExecutionMode, "local_mcp")
}

func (s RuntimeMCPStatus) IsTrustedRemote() bool {
	return strings.EqualFold(s.TrustLevel, "trusted_remote")
}

func (s RuntimeMCPStatus) IsUntrustedRemote() bool {
	return strings.EqualFold(s.TrustLevel, "untrusted_remote")
}

type RuntimeMCPSummary struct {
	Names                []string
	LocalCount           int
	RemoteCount          int
	TrustedRemoteCount   int
	UntrustedRemoteCount int
	ConnectedCount       int
	DisconnectedCount    int
}

func (s RuntimeStatus) LocalMCPs() []RuntimeMCPStatus {
	return filterMCPStatuses(s.MCPs, func(item RuntimeMCPStatus) bool {
		return item.IsLocalMCP()
	})
}

func (s RuntimeStatus) RemoteMCPs() []RuntimeMCPStatus {
	return filterMCPStatuses(s.MCPs, func(item RuntimeMCPStatus) bool {
		return item.IsRemoteMCP()
	})
}

func (s RuntimeStatus) MCPSummary() RuntimeMCPSummary {
	if len(s.MCPs) == 0 {
		return RuntimeMCPSummary{}
	}

	summary := RuntimeMCPSummary{
		Names: make([]string, 0, len(s.MCPs)),
	}
	for _, mcp := range s.MCPs {
		if mcp.Name != "" {
			summary.Names = append(summary.Names, mcp.Name)
		}
		if mcp.IsLocalMCP() {
			summary.LocalCount++
		}
		if mcp.IsRemoteMCP() {
			summary.RemoteCount++
		}
		if mcp.IsTrustedRemote() {
			summary.TrustedRemoteCount++
		}
		if mcp.IsUntrustedRemote() {
			summary.UntrustedRemoteCount++
		}
		if mcp.Connected {
			summary.ConnectedCount++
		} else {
			summary.DisconnectedCount++
		}
	}
	sort.Strings(summary.Names)
	return summary
}

type RuntimeStatus struct {
	DefaultModel  string                  `json:"default_model"`
	Providers     []RuntimeProviderStatus `json:"providers"`
	ProviderCount int                     `json:"provider_count"`
	MCPs          []RuntimeMCPStatus      `json:"mcps"`
	MCPCount      int                     `json:"mcp_count"`
}

type GetRuntimeStatusResponse struct {
	Runtime RuntimeStatus `json:"runtime"`
}

type RuntimeHealth struct {
	Healthy            bool     `json:"healthy"`
	HealthyProviders   int      `json:"healthy_providers"`
	DegradedProviders  int      `json:"degraded_providers,omitempty"`
	UnhealthyProviders int      `json:"unhealthy_providers"`
	UnknownProviders   int      `json:"unknown_providers,omitempty"`
	ConnectedMCPs      int      `json:"connected_mcps"`
	DisconnectedMCPs   int      `json:"disconnected_mcps"`
	Issues             []string `json:"issues"`
}

type GetRuntimeHealthResponse struct {
	Runtime RuntimeStatus `json:"runtime"`
	Health  RuntimeHealth `json:"health"`
}

type RuntimeValidation struct {
	Healthy      bool                   `json:"healthy"`
	IssueCount   int                    `json:"issue_count"`
	WarningCount int                    `json:"warning_count"`
	Issues       []string               `json:"issues"`
	Warnings     []string               `json:"warnings"`
	SkillCount   int                    `json:"skill_count"`
	SkillDirs    []string               `json:"skill_dirs"`
	DefaultModel string                 `json:"default_model"`
	Config       map[string]interface{} `json:"config,omitempty"`
}

type GetRuntimeValidationResponse struct {
	Validation RuntimeValidation `json:"validation"`
}

type CapabilitySource struct {
	Path  string `json:"path,omitempty"`
	Dir   string `json:"dir,omitempty"`
	Layer string `json:"layer,omitempty"`
}

type CapabilityTrigger struct {
	Type   string   `json:"type"`
	Values []string `json:"values,omitempty"`
	Weight float64  `json:"weight,omitempty"`
}

type CapabilityDependency struct {
	Name        string                 `json:"name"`
	Kind        string                 `json:"kind"`
	Description string                 `json:"description,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

type CapabilityDescriptor struct {
	ID           string                 `json:"id"`
	Name         string                 `json:"name"`
	Kind         string                 `json:"kind"`
	Description  string                 `json:"description,omitempty"`
	Version      string                 `json:"version,omitempty"`
	Category     string                 `json:"category,omitempty"`
	Labels       []string               `json:"labels,omitempty"`
	Capabilities []string               `json:"capabilities,omitempty"`
	Triggers     []CapabilityTrigger    `json:"triggers,omitempty"`
	Dependencies []CapabilityDependency `json:"dependencies,omitempty"`
	Source       *CapabilitySource      `json:"source,omitempty"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
}

type GetCapabilitiesResponse struct {
	Capabilities []CapabilityDescriptor `json:"capabilities"`
	Count        int                    `json:"count"`
}

type ReloadRuntimeMCPsResponse struct {
	Reloaded bool          `json:"reloaded"`
	Runtime  RuntimeStatus `json:"runtime"`
	Health   RuntimeHealth `json:"health"`
}

type CreateSkillOptions struct {
	Persist   bool
	TargetDir string
}

type UpdateSkillOptions struct {
	Persist   *bool
	TargetDir string
}

type DeleteSkillOptions struct {
	DeleteFile bool
}

type ImportSkillsOptions struct {
	Persist   bool
	TargetDir string
}

type SkillMutationResponse struct {
	Message     string       `json:"message"`
	Skill       string       `json:"skill"`
	Source      *SkillSource `json:"source,omitempty"`
	FileDeleted bool         `json:"file_deleted,omitempty"`
}

type ExportSkillsResponse struct {
	Skills        []Skill        `json:"skills"`
	Count         int            `json:"count"`
	SourceSummary map[string]int `json:"source_summary"`
}

type ImportSkillsResponse struct {
	Imported  int           `json:"imported"`
	Persisted int           `json:"persisted,omitempty"`
	Failed    int           `json:"failed"`
	Errors    []interface{} `json:"errors"`
}

type ReloadSkillsRequest struct {
	Dir  string   `json:"dir,omitempty"`
	Dirs []string `json:"dirs,omitempty"`
}

type ReloadSkillsResponse struct {
	Message     string   `json:"message"`
	Status      string   `json:"status"`
	SkillDirs   []string `json:"skill_dirs"`
	TotalSkills int      `json:"total_skills"`
}

type HotReloadRequest struct {
	Dir        string   `json:"dir,omitempty"`
	Dirs       []string `json:"dirs,omitempty"`
	DebounceMS int      `json:"debounce_ms,omitempty"`
}

type HotReloadStatsResponse struct {
	Stats map[string]interface{} `json:"stats"`
}

type StartHotReloadResponse struct {
	Started bool                   `json:"started"`
	Dir     string                 `json:"dir,omitempty"`
	Dirs    []string               `json:"dirs,omitempty"`
	Stats   map[string]interface{} `json:"stats"`
}

type StopHotReloadResponse struct {
	Stopped bool                   `json:"stopped"`
	Stats   map[string]interface{} `json:"stats"`
}

type ReloadHotReloadResponse struct {
	Reloaded bool                   `json:"reloaded"`
	Stats    map[string]interface{} `json:"stats"`
}

type SearchStatsResponse struct {
	Search    map[string]interface{} `json:"search"`
	Embedding map[string]interface{} `json:"embedding"`

	decodedSearchOnce    sync.Once             `json:"-"`
	decodedSearch        *SearchTelemetryStats `json:"-"`
	decodedSearchErr     error                 `json:"-"`
	decodedEmbeddingOnce sync.Once             `json:"-"`
	decodedEmbedding     *EmbeddingStatus      `json:"-"`
	decodedEmbeddingErr  error                 `json:"-"`
}

func (r *SearchStatsResponse) DecodeSearch() (*SearchTelemetryStats, error) {
	if r == nil || r.Search == nil {
		return nil, nil
	}
	r.decodedSearchOnce.Do(func() {
		r.decodedSearch, r.decodedSearchErr = decodeMapPayload[SearchTelemetryStats](r.Search)
	})
	return r.decodedSearch, r.decodedSearchErr
}

func (r *SearchStatsResponse) DecodeEmbedding() (*EmbeddingStatus, error) {
	if r == nil || r.Embedding == nil {
		return nil, nil
	}
	r.decodedEmbeddingOnce.Do(func() {
		r.decodedEmbedding, r.decodedEmbeddingErr = decodeMapPayload[EmbeddingStatus](r.Embedding)
	})
	return r.decodedEmbedding, r.decodedEmbeddingErr
}

type ReindexSearchIndexResponse struct {
	Reindexed         bool                   `json:"reindexed,omitempty"`
	RetryAfterSeconds int                    `json:"retry_after_seconds,omitempty"`
	Search            map[string]interface{} `json:"search,omitempty"`
	Embedding         map[string]interface{} `json:"embedding,omitempty"`
	Error             string                 `json:"error,omitempty"`

	decodedSearchOnce    sync.Once             `json:"-"`
	decodedSearch        *SearchTelemetryStats `json:"-"`
	decodedSearchErr     error                 `json:"-"`
	decodedEmbeddingOnce sync.Once             `json:"-"`
	decodedEmbedding     *EmbeddingStatus      `json:"-"`
	decodedEmbeddingErr  error                 `json:"-"`
}

func (r *ReindexSearchIndexResponse) DecodeSearch() (*SearchTelemetryStats, error) {
	if r == nil || r.Search == nil {
		return nil, nil
	}
	r.decodedSearchOnce.Do(func() {
		r.decodedSearch, r.decodedSearchErr = decodeMapPayload[SearchTelemetryStats](r.Search)
	})
	return r.decodedSearch, r.decodedSearchErr
}

func (r *ReindexSearchIndexResponse) DecodeEmbedding() (*EmbeddingStatus, error) {
	if r == nil || r.Embedding == nil {
		return nil, nil
	}
	r.decodedEmbeddingOnce.Do(func() {
		r.decodedEmbedding, r.decodedEmbeddingErr = decodeMapPayload[EmbeddingStatus](r.Embedding)
	})
	return r.decodedEmbedding, r.decodedEmbeddingErr
}

type UsageStatsResponse struct {
	UserID          string                 `json:"user_id,omitempty"`
	TenantID        string                 `json:"tenant_id,omitempty"`
	ProjectID       string                 `json:"project_id,omitempty"`
	Scope           *UsageScope            `json:"scope,omitempty"`
	Scopes          []UsageScope           `json:"scopes,omitempty"`
	TrackingEnabled bool                   `json:"tracking_enabled"`
	Policy          UsagePolicy            `json:"policy"`
	Quota           map[string]interface{} `json:"quota"`
	Usage           map[string]interface{} `json:"usage"`
}

type GetUsageLedgerParams struct {
	Scope      UsageScope
	Entrypoint string
	Skill      string
	Success    *bool
	Since      *time.Time
	Limit      int
}

type UsageLedgerRecord struct {
	ID           string                 `json:"id"`
	RequestID    string                 `json:"request_id"`
	ModelID      string                 `json:"model_id"`
	ProviderID   string                 `json:"provider_id"`
	InputTokens  int                    `json:"input_tokens"`
	OutputTokens int                    `json:"output_tokens"`
	TotalTokens  int                    `json:"total_tokens"`
	MessageCount int                    `json:"message_count"`
	MaxTokens    int                    `json:"max_tokens"`
	Success      bool                   `json:"success"`
	StatusCode   int                    `json:"status_code"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt    string                 `json:"created_at"`
}

type GetUsageLedgerResponse struct {
	Records []UsageLedgerRecord    `json:"records"`
	Count   int                    `json:"count"`
	Filters map[string]interface{} `json:"filters"`
}

type ResetUsageStatsRequest struct {
	TenantID  string `json:"tenant_id,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
	UserID    string `json:"user_id,omitempty"`
}

type ResetUsageStatsResponse struct {
	Reset bool        `json:"reset"`
	Scope *UsageScope `json:"scope,omitempty"`
}

type SessionMetadata struct {
	Tags       []string               `json:"tags,omitempty"`
	Title      string                 `json:"title,omitempty"`
	Summary    string                 `json:"summary,omitempty"`
	TotalTurns int                    `json:"totalTurns,omitempty"`
	LastAgent  string                 `json:"lastAgent,omitempty"`
	LastSkill  string                 `json:"lastSkill,omitempty"`
	LastModel  string                 `json:"lastModel,omitempty"`
	CreatedBy  string                 `json:"createdBy,omitempty"`
	Context    map[string]interface{} `json:"context,omitempty"`
}

type Session struct {
	ID        string          `json:"id"`
	UserID    string          `json:"userId"`
	State     string          `json:"state"`
	History   []Message       `json:"history,omitempty"`
	Metadata  SessionMetadata `json:"metadata"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
	ExpiresAt *time.Time      `json:"expiresAt,omitempty"`
}

type CreateSessionRequest struct {
	UserID string `json:"user_id,omitempty"`
	Title  string `json:"title,omitempty"`
}

type CreateSessionResponse struct {
	Session Session `json:"session"`
}

type ListSessionsResponse struct {
	Sessions []Session `json:"sessions"`
	Count    int       `json:"count"`
	UserID   string    `json:"user_id"`
}

type GetSessionResponse struct {
	Session Session `json:"session"`
}

type DeleteSessionResponse struct {
	Deleted bool   `json:"deleted"`
	ID      string `json:"id"`
}

type SessionHistoryResponse struct {
	SessionID string    `json:"session_id"`
	History   []Message `json:"history"`
	Count     int       `json:"count"`
}

type SessionStatsResponse struct {
	UserID string                 `json:"user_id"`
	Stats  map[string]interface{} `json:"stats"`
}

type SearchSessionsRequest struct {
	UserID string   `json:"user_id,omitempty"`
	Tags   []string `json:"tags,omitempty"`
	State  string   `json:"state,omitempty"`
	Limit  int      `json:"limit,omitempty"`
	Offset int      `json:"offset,omitempty"`
}

type SearchSessionsResponse struct {
	Sessions []Session              `json:"sessions"`
	Count    int                    `json:"count"`
	Filters  map[string]interface{} `json:"filters"`
}

type UpdateSessionRequest struct {
	Title      *string                `json:"title,omitempty"`
	State      *string                `json:"state,omitempty"`
	TagsAdd    []string               `json:"tags_add,omitempty"`
	TagsRemove []string               `json:"tags_remove,omitempty"`
	Context    map[string]interface{} `json:"context,omitempty"`
}

type SessionStateChangeResponse struct {
	Session Session `json:"session"`
	State   string  `json:"state"`
}

type SessionApprovalRequest struct {
	ID        string          `json:"id"`
	SessionID string          `json:"session_id"`
	ToolName  string          `json:"tool_name"`
	ArgsJSON  json.RawMessage `json:"args_json,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	RiskLevel string          `json:"risk_level,omitempty"`
	ExpiresAt time.Time       `json:"expires_at,omitempty"`
}

type SessionQuestionRequest struct {
	ID          string     `json:"id"`
	SessionID   string     `json:"session_id"`
	Prompt      string     `json:"prompt"`
	Suggestions []string   `json:"suggestions,omitempty"`
	Required    bool       `json:"required"`
	CreatedAt   time.Time  `json:"created_at,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

type SessionRuntimeState struct {
	SessionID           string                  `json:"session_id"`
	Status              string                  `json:"status"`
	CurrentTurnID       string                  `json:"current_turn_id,omitempty"`
	CurrentCheckpointID string                  `json:"current_checkpoint_id,omitempty"`
	CurrentRunMeta      *SessionRunMeta         `json:"current_run_meta,omitempty"`
	PendingApproval     *SessionApprovalRequest `json:"pending_approval,omitempty"`
	PendingQuestion     *SessionQuestionRequest `json:"pending_question,omitempty"`
	HeadOffset          int64                   `json:"head_offset"`
	ActiveJobIDs        []string                `json:"active_job_ids,omitempty"`
	UpdatedAt           time.Time               `json:"updated_at"`
}

type SessionRuntimeStateResponse struct {
	State SessionRuntimeState `json:"state"`
}

type SessionRuntimeEvent struct {
	Type      string                 `json:"type"`
	TraceID   string                 `json:"trace_id,omitempty"`
	AgentName string                 `json:"agent_name,omitempty"`
	SessionID string                 `json:"session_id,omitempty"`
	ToolName  string                 `json:"tool_name,omitempty"`
	Payload   map[string]interface{} `json:"payload,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
}

type SessionRuntimeEventsResponse struct {
	Events []SessionRuntimeEvent `json:"events"`
	Count  int                   `json:"count"`
}

type SessionTeamRunMeta struct {
	TeamID        string `json:"team_id,omitempty"`
	AgentID       string `json:"agent_id,omitempty"`
	CurrentTaskID string `json:"current_task_id,omitempty"`
}

type SessionRunMeta struct {
	Team *SessionTeamRunMeta `json:"team,omitempty"`
}

type TeamTask struct {
	ID           string     `json:"id"`
	TeamID       string     `json:"team_id,omitempty"`
	ParentTaskID *string    `json:"parent_task_id,omitempty"`
	Title        string     `json:"title,omitempty"`
	Goal         string     `json:"goal,omitempty"`
	Inputs       []string   `json:"inputs,omitempty"`
	Status       string     `json:"status,omitempty"`
	Priority     int        `json:"priority,omitempty"`
	Assignee     *string    `json:"assignee,omitempty"`
	LeaseUntil   *time.Time `json:"lease_until,omitempty"`
	RetryCount   int        `json:"retry_count,omitempty"`
	ReadPaths    []string   `json:"read_paths,omitempty"`
	WritePaths   []string   `json:"write_paths,omitempty"`
	Deliverables []string   `json:"deliverables,omitempty"`
	Summary      string     `json:"summary,omitempty"`
	ResultRef    *string    `json:"result_ref,omitempty"`
	Version      int64      `json:"version,omitempty"`
	CreatedAt    *time.Time `json:"created_at,omitempty"`
	UpdatedAt    *time.Time `json:"updated_at,omitempty"`
}

type TeamTaskDependency struct {
	TaskID      string `json:"task_id"`
	DependsOnID string `json:"depends_on_id"`
}

type TeamRecord struct {
	ID            string     `json:"id"`
	WorkspaceID   string     `json:"workspace_id,omitempty"`
	LeadSessionID string     `json:"lead_session_id,omitempty"`
	Status        string     `json:"status,omitempty"`
	Strategy      string     `json:"strategy,omitempty"`
	MaxTeammates  int        `json:"max_teammates,omitempty"`
	MaxWriters    int        `json:"max_writers,omitempty"`
	CreatedAt     *time.Time `json:"created_at,omitempty"`
	UpdatedAt     *time.Time `json:"updated_at,omitempty"`
}

type TeammateRecord struct {
	ID            string     `json:"id"`
	TeamID        string     `json:"team_id"`
	Name          string     `json:"name,omitempty"`
	Profile       string     `json:"profile,omitempty"`
	SessionID     string     `json:"session_id,omitempty"`
	State         string     `json:"state,omitempty"`
	LastHeartbeat *time.Time `json:"last_heartbeat,omitempty"`
	Capabilities  []string   `json:"capabilities,omitempty"`
	CreatedAt     *time.Time `json:"created_at,omitempty"`
	UpdatedAt     *time.Time `json:"updated_at,omitempty"`
}

type TeamAssignment struct {
	Task     TeamTask       `json:"task"`
	Teammate TeammateRecord `json:"teammate"`
}

type TeamLeaseReclaim struct {
	Task               TeamTask   `json:"task"`
	PreviousAssignee   string     `json:"previous_assignee,omitempty"`
	PreviousLeaseUntil *time.Time `json:"previous_lease_until,omitempty"`
}

type CreateTeamRequest struct {
	ID            string `json:"id,omitempty"`
	WorkspaceID   string `json:"workspace_id,omitempty"`
	LeadSessionID string `json:"lead_session_id,omitempty"`
	Status        string `json:"status,omitempty"`
	Strategy      string `json:"strategy,omitempty"`
	MaxTeammates  int    `json:"max_teammates,omitempty"`
	MaxWriters    int    `json:"max_writers,omitempty"`
}

type CreateTeamResponse struct {
	Team TeamRecord `json:"team"`
}

type ListTeamsParams struct {
	Status      string
	Limit       int
	TeamIDs     []string
	WorkspaceID string
}

type ListTeamsResponse struct {
	Teams       []TeamRecord `json:"teams"`
	Count       int          `json:"count"`
	Limit       int          `json:"limit,omitempty"`
	TeamIDs     []string     `json:"team_ids,omitempty"`
	WorkspaceID string       `json:"workspace_id,omitempty"`
	Status      string       `json:"status,omitempty"`
}

type ListTeammatesParams struct {
	Limit int
	State string
}

type ListTeammatesResponse struct {
	Teammates []TeammateRecord `json:"teammates"`
	Count     int              `json:"count"`
	Limit     int              `json:"limit,omitempty"`
	State     *string          `json:"state,omitempty"`
}

type PlanTeamTasksRequest struct {
	Goal        string `json:"goal,omitempty"`
	AutoPersist bool   `json:"auto_persist,omitempty"`
}

type PlanTeamTasksResponse struct {
	TeamID          string               `json:"team_id"`
	Goal            string               `json:"goal"`
	AutoPersist     bool                 `json:"auto_persist"`
	Tasks           []TeamTask           `json:"tasks"`
	Dependencies    []TeamTaskDependency `json:"dependencies"`
	TaskCount       int                  `json:"task_count"`
	DependencyCount int                  `json:"dependency_count"`
	Summary         string               `json:"summary,omitempty"`
}

type GetTaskGraphParams struct {
	Status          []string
	Assignee        string
	ParentTaskID    string
	TaskIDs         []string
	IncludeExternal bool
	Limit           int
}

type GetTaskGraphResponse struct {
	Tasks               []TeamTask           `json:"tasks"`
	Count               int                  `json:"count"`
	Edges               []TeamTaskDependency `json:"edges"`
	EdgeCount           int                  `json:"edge_count"`
	MissingDependencies []string             `json:"missing_dependencies,omitempty"`
	TaskIDs             []string             `json:"task_ids,omitempty"`
	Limit               int                  `json:"limit,omitempty"`
	IncludeExternal     bool                 `json:"include_external,omitempty"`
	Status              []string             `json:"status,omitempty"`
	Assignee            *string              `json:"assignee,omitempty"`
	ParentTaskID        *string              `json:"parent_task_id,omitempty"`
}

type ReportTaskOutcomeRequest struct {
	TaskStatus string `json:"task_status,omitempty"`
	Summary    string `json:"summary,omitempty"`
	Blocker    string `json:"blocker,omitempty"`
	HandoffTo  string `json:"handoff_to,omitempty"`
	ResultRef  string `json:"result_ref,omitempty"`
	TeammateID string `json:"teammate_id,omitempty"`
	NotifyLead *bool  `json:"notify_lead,omitempty"`
	AutoReplan *bool  `json:"auto_replan,omitempty"`
}

type ReportTaskOutcomeResponse struct {
	Task                TeamTask             `json:"task"`
	MessageID           string               `json:"message_id,omitempty"`
	AutoReplan          bool                 `json:"auto_replan,omitempty"`
	ReplanError         string               `json:"replan_error,omitempty"`
	HandoffTo           string               `json:"handoff_to,omitempty"`
	PlannedTasks        []TeamTask           `json:"planned_tasks,omitempty"`
	PlannedDependencies []TeamTaskDependency `json:"planned_dependencies,omitempty"`
	PlannedSummary      string               `json:"planned_summary,omitempty"`
}

type GetTeamTaskOptions struct {
	IncludeDependencies bool
	IncludeDependents   bool
}

type GetTeamTaskResponse struct {
	Task         TeamTask `json:"task"`
	Dependencies []string `json:"dependencies,omitempty"`
	Dependents   []string `json:"dependents,omitempty"`
}

type ListTeamTasksParams struct {
	Status              []string
	Assignee            string
	ParentTaskID        string
	TaskIDs             []string
	IncludeDependencies bool
	IncludeDependents   bool
	Limit               int
}

type ListTeamTasksResponse struct {
	Tasks        []TeamTask          `json:"tasks"`
	Count        int                 `json:"count"`
	Limit        int                 `json:"limit,omitempty"`
	Status       []string            `json:"status,omitempty"`
	Assignee     *string             `json:"assignee,omitempty"`
	ParentTaskID *string             `json:"parent_task_id,omitempty"`
	TaskIDs      []string            `json:"task_ids,omitempty"`
	Dependencies map[string][]string `json:"dependencies,omitempty"`
	Dependents   map[string][]string `json:"dependents,omitempty"`
}

type TeamTaskDependenciesResponse struct {
	TaskID       string   `json:"task_id"`
	Dependencies []string `json:"dependencies"`
	Count        int      `json:"count"`
}

type TeamTaskDependentsResponse struct {
	TaskID     string   `json:"task_id"`
	Dependents []string `json:"dependents"`
	Count      int      `json:"count"`
}

type CreateTeamTaskRequest struct {
	ID           string   `json:"id,omitempty"`
	ParentTaskID string   `json:"parent_task_id,omitempty"`
	Title        string   `json:"title,omitempty"`
	Goal         string   `json:"goal,omitempty"`
	Status       string   `json:"status,omitempty"`
	Priority     int      `json:"priority,omitempty"`
	Assignee     string   `json:"assignee,omitempty"`
	Inputs       []string `json:"inputs,omitempty"`
	ReadPaths    []string `json:"read_paths,omitempty"`
	WritePaths   []string `json:"write_paths,omitempty"`
	Deliverables []string `json:"deliverables,omitempty"`
	Summary      string   `json:"summary,omitempty"`
	ResultRef    string   `json:"result_ref,omitempty"`
}

type CreateTeamTaskResponse struct {
	Task TeamTask `json:"task"`
}

type UpdateTeamTaskRequest struct {
	ParentTaskID *string   `json:"parent_task_id,omitempty"`
	Title        *string   `json:"title,omitempty"`
	Goal         *string   `json:"goal,omitempty"`
	Status       *string   `json:"status,omitempty"`
	Priority     *int      `json:"priority,omitempty"`
	Assignee     *string   `json:"assignee,omitempty"`
	Inputs       *[]string `json:"inputs,omitempty"`
	ReadPaths    *[]string `json:"read_paths,omitempty"`
	WritePaths   *[]string `json:"write_paths,omitempty"`
	Deliverables *[]string `json:"deliverables,omitempty"`
	Summary      *string   `json:"summary,omitempty"`
	ResultRef    *string   `json:"result_ref,omitempty"`
}

type UpdateTeamTaskResponse struct {
	Task TeamTask `json:"task"`
}

type AddTaskDependencyRequest struct {
	DependsOnID string `json:"depends_on_id"`
}

type AddTaskDependencyResponse struct {
	TaskID      string `json:"task_id"`
	DependsOnID string `json:"depends_on_id"`
}

type ClaimReadyTasksRequest struct {
	Limit int `json:"limit,omitempty"`
}

type ClaimReadyTasksResponse struct {
	Assignments []TeamAssignment `json:"assignments"`
	Count       int              `json:"count"`
}

type ReclaimExpiredTasksRequest struct {
	Limit  int        `json:"limit,omitempty"`
	DryRun bool       `json:"dry_run,omitempty"`
	AsOf   *time.Time `json:"as_of,omitempty"`
}

type ReclaimExpiredTasksResponse struct {
	TeamID    string             `json:"team_id"`
	AsOf      time.Time          `json:"as_of"`
	DryRun    bool               `json:"dry_run"`
	Reclaimed []TeamLeaseReclaim `json:"reclaimed"`
	Count     int                `json:"count"`
}

type MarkReadyTasksResponse struct {
	TeamID string `json:"team_id"`
	Count  int64  `json:"count"`
}

type ReplanTaskRequest struct {
	AutoPersist bool `json:"auto_persist,omitempty"`
}

type ReplanTaskResponse struct {
	TeamID          string               `json:"team_id"`
	FailedTask      string               `json:"failed_task"`
	AutoPersist     bool                 `json:"auto_persist"`
	Tasks           []TeamTask           `json:"tasks"`
	Dependencies    []TeamTaskDependency `json:"dependencies"`
	TaskCount       int                  `json:"task_count"`
	DependencyCount int                  `json:"dependency_count"`
	Summary         string               `json:"summary,omitempty"`
}

type TeamMailboxMessage struct {
	ID        string                 `json:"id"`
	TeamID    string                 `json:"team_id"`
	FromAgent string                 `json:"from_agent"`
	ToAgent   string                 `json:"to_agent"`
	TaskID    *string                `json:"task_id,omitempty"`
	Kind      string                 `json:"kind"`
	Body      string                 `json:"body"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt *time.Time             `json:"created_at,omitempty"`
	AckedAt   *time.Time             `json:"acked_at,omitempty"`
}

type SendTeamMailboxMessageRequest struct {
	FromAgent string                 `json:"from_agent,omitempty"`
	ToAgent   string                 `json:"to_agent,omitempty"`
	TaskID    string                 `json:"task_id,omitempty"`
	Kind      string                 `json:"kind,omitempty"`
	Body      string                 `json:"body,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

type SendTeamMailboxMessageResponse struct {
	Message       TeamMailboxMessage `json:"message"`
	DispatchError string             `json:"dispatch_error,omitempty"`
}

type ListTeamMailboxParams struct {
	FromAgent        string
	ToAgent          string
	TaskID           string
	ParentTaskID     string
	Kind             string
	AgentID          string
	UnreadOnly       bool
	MarkRead         bool
	IncludeBroadcast bool
	Since            *time.Time
	Limit            int
}

type ListTeamMailboxResponse struct {
	Messages     []TeamMailboxMessage   `json:"messages"`
	Count        int                    `json:"count"`
	ParentTaskID string                 `json:"parent_task_id,omitempty"`
	Limit        int                    `json:"limit,omitempty"`
	MarkedRead   bool                   `json:"marked_read,omitempty"`
	AgentID      string                 `json:"agent_id,omitempty"`
	Filters      map[string]interface{} `json:"filters,omitempty"`
}

type AckTeamMailboxMessageResponse struct {
	MessageID string `json:"message_id"`
	TeamID    string `json:"team_id"`
	AgentID   string `json:"agent_id,omitempty"`
}

type SessionRuntimeCommandRequest struct {
	Type         string          `json:"type"`
	Prompt       string          `json:"prompt,omitempty"`
	RunMeta      *SessionRunMeta `json:"run_meta,omitempty"`
	RequestID    string          `json:"request_id,omitempty"`
	Allow        *bool           `json:"allow,omitempty"`
	PatchedArgs  json.RawMessage `json:"patched_args,omitempty"`
	QuestionID   string          `json:"question_id,omitempty"`
	Answer       string          `json:"answer,omitempty"`
	CheckpointID string          `json:"checkpoint_id,omitempty"`
	Mode         string          `json:"mode,omitempty"`
}

type SessionRuntimeCommandResponse struct {
	Result map[string]interface{} `json:"result,omitempty"`
	OK     bool                   `json:"ok,omitempty"`
}

type SpawnSessionAgentRequest struct {
	ID          string `json:"id,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	Message     string `json:"message,omitempty"`
	AgentType   string `json:"agent_type,omitempty"`
	Model       string `json:"model,omitempty"`
	ForkContext *bool  `json:"fork_context,omitempty"`
}

type SendSessionAgentInputRequest struct {
	ID        string `json:"id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Message   string `json:"message,omitempty"`
	Interrupt *bool  `json:"interrupt,omitempty"`
}

type WaitSessionAgentsRequest struct {
	ID         string   `json:"id,omitempty"`
	SessionID  string   `json:"session_id,omitempty"`
	IDs        []string `json:"ids,omitempty"`
	SessionIDs []string `json:"session_ids,omitempty"`
	TimeoutMs  int      `json:"timeout_ms,omitempty"`
}

type ListSessionAgentEventsParams struct {
	AfterSeq int64
	Limit    int
	WaitMs   int
}

type SessionAgent struct {
	ID                 string `json:"id"`
	SessionID          string `json:"session_id"`
	ParentSessionID    string `json:"parent_session_id,omitempty"`
	AgentType          string `json:"agent_type,omitempty"`
	Status             string `json:"status"`
	Exists             bool   `json:"exists"`
	Created            bool   `json:"created,omitempty"`
	Queued             bool   `json:"queued,omitempty"`
	TimedOut           bool   `json:"timed_out,omitempty"`
	PendingApproval    bool   `json:"pending_approval,omitempty"`
	PendingQuestion    bool   `json:"pending_question,omitempty"`
	MessageCount       int    `json:"message_count,omitempty"`
	Output             string `json:"output,omitempty"`
	Error              string `json:"error,omitempty"`
	SessionState       string `json:"session_state,omitempty"`
	CurrentTurnID      string `json:"current_turn_id,omitempty"`
	PendingToolName    string `json:"pending_tool_name,omitempty"`
	PendingToolCallID  string `json:"pending_tool_call_id,omitempty"`
	LastMessageRole    string `json:"last_message_role,omitempty"`
	LastMessagePreview string `json:"last_message_preview,omitempty"`
}

type SessionAgentStatusResponse struct {
	Agent SessionAgent `json:"agent"`
}

type SessionAgentWaitResult struct {
	Agent            *SessionAgent  `json:"agent,omitempty"`
	Agents           []SessionAgent `json:"agents,omitempty"`
	MatchedID        string         `json:"matched_id,omitempty"`
	MatchedSessionID string         `json:"matched_session_id,omitempty"`
	TimedOut         bool           `json:"timed_out,omitempty"`
	ReadyCount       int            `json:"ready_count,omitempty"`
	PendingCount     int            `json:"pending_count,omitempty"`
}

type WaitSessionAgentsResponse struct {
	Result SessionAgentWaitResult `json:"result"`
}

type SessionAgentEvent struct {
	Seq       int64                  `json:"seq,omitempty"`
	Type      string                 `json:"type"`
	TraceID   string                 `json:"trace_id,omitempty"`
	SessionID string                 `json:"session_id,omitempty"`
	ToolName  string                 `json:"tool_name,omitempty"`
	AgentName string                 `json:"agent_name,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
	Payload   map[string]interface{} `json:"payload,omitempty"`
}

type SessionAgentEventsResult struct {
	SessionID string              `json:"session_id"`
	Events    []SessionAgentEvent `json:"events,omitempty"`
	Count     int                 `json:"count"`
	LatestSeq int64               `json:"latest_seq,omitempty"`
	TimedOut  bool                `json:"timed_out,omitempty"`
}

type ListSessionAgentEventsResponse struct {
	Result SessionAgentEventsResult `json:"result"`
}

type BackgroundJob struct {
	ID         string                 `json:"id"`
	SessionID  string                 `json:"session_id"`
	Kind       string                 `json:"kind,omitempty"`
	Command    string                 `json:"command,omitempty"`
	Cwd        string                 `json:"cwd,omitempty"`
	Priority   int                    `json:"priority,omitempty"`
	Status     string                 `json:"status"`
	Message    string                 `json:"message,omitempty"`
	CreatedAt  time.Time              `json:"created_at"`
	StartedAt  *time.Time             `json:"started_at,omitempty"`
	FinishedAt *time.Time             `json:"finished_at,omitempty"`
	ExitCode   *int                   `json:"exit_code,omitempty"`
	LogPath    string                 `json:"log_path,omitempty"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
}

type BackgroundJobListResponse struct {
	Jobs  []BackgroundJob `json:"jobs"`
	Count int             `json:"count"`
}

type BackgroundJobResponse struct {
	Job BackgroundJob `json:"job"`
}

type BackgroundJobEvent struct {
	Seq       int64                  `json:"seq"`
	JobID     string                 `json:"job_id"`
	Type      string                 `json:"type"`
	Payload   map[string]interface{} `json:"payload,omitempty"`
	CreatedAt time.Time              `json:"created_at"`
}

type BackgroundJobEventsResponse struct {
	Events []BackgroundJobEvent `json:"events"`
	Count  int                  `json:"count"`
}

type BackgroundJobOutput struct {
	JobID      string `json:"job_id"`
	Status     string `json:"status"`
	Output     string `json:"output,omitempty"`
	NextOffset int64  `json:"next_offset"`
	ExitCode   *int   `json:"exit_code,omitempty"`
}

type BackgroundJobOutputResponse struct {
	Output BackgroundJobOutput `json:"output"`
}

type CheckpointSummary struct {
	ID           string                 `json:"id"`
	SessionID    string                 `json:"session_id"`
	TaskID       string                 `json:"task_id,omitempty"`
	Reason       string                 `json:"reason,omitempty"`
	HistoryHash  string                 `json:"history_hash,omitempty"`
	MessageCount int                    `json:"message_count"`
	CreatedAt    time.Time              `json:"created_at"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
}

type CheckpointListResponse struct {
	Checkpoints []CheckpointSummary `json:"checkpoints"`
	Count       int                 `json:"count"`
}

type CheckpointRestoreResult struct {
	CheckpointID string                  `json:"checkpoint_id"`
	Mode         string                  `json:"mode"`
	AppliedPaths []string                `json:"applied_paths,omitempty"`
	Errors       []string                `json:"errors,omitempty"`
	Preview      []string                `json:"preview,omitempty"`
	PreviewFiles []CheckpointPreviewFile `json:"preview_files,omitempty"`
}

type CheckpointPreviewFile struct {
	Path     string `json:"path"`
	Change   string `json:"change"`
	DiffText string `json:"diff_text,omitempty"`
}

type CheckpointPreviewResponse struct {
	Result CheckpointRestoreResult `json:"result"`
}

type CheckpointFile struct {
	ID           string `json:"id"`
	CheckpointID string `json:"checkpoint_id"`
	Path         string `json:"path"`
	Op           string `json:"op"`
	BeforeBlobID string `json:"before_blob_id,omitempty"`
	AfterBlobID  string `json:"after_blob_id,omitempty"`
	BeforeHash   string `json:"before_hash,omitempty"`
	AfterHash    string `json:"after_hash,omitempty"`
	DiffText     string `json:"diff_text,omitempty"`
}

type CheckpointFilesResponse struct {
	Files []CheckpointFile `json:"files"`
	Count int              `json:"count"`
}

type ClearSessionHistoryResponse struct {
	SessionID string `json:"session_id"`
	Cleared   bool   `json:"cleared"`
}

type BatchSessionActionRequest struct {
	SessionIDs []string `json:"session_ids"`
}

type BatchSessionActionResponse struct {
	Action    string            `json:"action"`
	Processed []string          `json:"processed"`
	Count     int               `json:"count"`
	Failures  map[string]string `json:"failures"`
}

type Stream struct {
	body    io.ReadCloser
	scanner *bufio.Scanner
	closed  bool
}

type StreamEnvelopeMeta struct {
	Name          string    `json:"name"`
	SchemaVersion string    `json:"schema_version,omitempty"`
	Timestamp     time.Time `json:"timestamp,omitempty"`
	Sequence      int       `json:"sequence,omitempty"`
}

type StreamEvent struct {
	Event string
	Data  json.RawMessage
}

func (e *StreamEvent) Decode(v interface{}) error {
	if e == nil {
		return io.EOF
	}
	return json.Unmarshal(e.Data, v)
}

type RuntimeEvent struct {
	Type      string                 `json:"type"`
	TraceID   string                 `json:"trace_id,omitempty"`
	AgentName string                 `json:"agent_name,omitempty"`
	SessionID string                 `json:"session_id,omitempty"`
	ToolName  string                 `json:"tool_name,omitempty"`
	Payload   map[string]interface{} `json:"payload,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
}

type RuntimeEventsResponse struct {
	Events  []RuntimeEvent         `json:"events"`
	Count   int                    `json:"count"`
	Filters map[string]interface{} `json:"filters,omitempty"`
}

func (e *StreamEvent) DecodeEnvelopeMeta() (*StreamEnvelopeMeta, error) {
	if e == nil {
		return nil, io.EOF
	}
	var payload struct {
		EventMeta StreamEnvelopeMeta `json:"_event"`
	}
	if err := json.Unmarshal(e.Data, &payload); err != nil {
		return nil, err
	}
	return &payload.EventMeta, nil
}

type StreamMetaPayload struct {
	EventMeta     StreamEnvelopeMeta     `json:"_event"`
	SessionID     string                 `json:"session_id,omitempty"`
	AgentID       string                 `json:"agent_id,omitempty"`
	Source        string                 `json:"source,omitempty"`
	Kind          string                 `json:"kind,omitempty"`
	Status        string                 `json:"status,omitempty"`
	Model         string                 `json:"model,omitempty"`
	Orchestration map[string]interface{} `json:"orchestration,omitempty"`
	Planning      map[string]interface{} `json:"planning,omitempty"`

	decodedOrchestrationOnce sync.Once             `json:"-"`
	decodedOrchestration     *OrchestrationSummary `json:"-"`
	decodedOrchestrationErr  error                 `json:"-"`

	decodedPlanningOnce sync.Once        `json:"-"`
	decodedPlanning     *PlanningSummary `json:"-"`
	decodedPlanningErr  error            `json:"-"`
}

func (e *StreamEvent) DecodeMetaPayload() (*StreamMetaPayload, error) {
	if e == nil {
		return nil, io.EOF
	}
	var payload StreamMetaPayload
	if err := json.Unmarshal(e.Data, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

func (p *StreamMetaPayload) DecodeOrchestration() (*OrchestrationSummary, error) {
	if p == nil || p.Orchestration == nil {
		return nil, nil
	}
	p.decodedOrchestrationOnce.Do(func() {
		p.decodedOrchestration, p.decodedOrchestrationErr = decodeMapPayload[OrchestrationSummary](p.Orchestration)
	})
	return p.decodedOrchestration, p.decodedOrchestrationErr
}

func (p *StreamMetaPayload) DecodePlanning() (*PlanningSummary, error) {
	if p == nil || p.Planning == nil {
		return nil, nil
	}
	p.decodedPlanningOnce.Do(func() {
		p.decodedPlanning, p.decodedPlanningErr = decodeMapPayload[PlanningSummary](p.Planning)
	})
	return p.decodedPlanning, p.decodedPlanningErr
}

type StreamChunkPayload struct {
	EventMeta  StreamEnvelopeMeta     `json:"_event"`
	Index      int                    `json:"index,omitempty"`
	Type       string                 `json:"type,omitempty"`
	Content    string                 `json:"content,omitempty"`
	TotalChars int                    `json:"total_chars,omitempty"`
	Text       map[string]interface{} `json:"text,omitempty"`
	Reasoning  map[string]interface{} `json:"reasoning,omitempty"`
	Tool       map[string]interface{} `json:"tool,omitempty"`
	ToolCall   map[string]interface{} `json:"tool_call,omitempty"`
	Delta      map[string]interface{} `json:"delta,omitempty"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`

	decodedTextOnce sync.Once        `json:"-"`
	decodedText     *StreamTextBlock `json:"-"`
	decodedTextErr  error            `json:"-"`

	decodedReasoningOnce sync.Once             `json:"-"`
	decodedReasoning     *StreamReasoningBlock `json:"-"`
	decodedReasoningErr  error                 `json:"-"`

	decodedToolOnce sync.Once        `json:"-"`
	decodedTool     *StreamToolEvent `json:"-"`
	decodedToolErr  error            `json:"-"`

	decodedToolCallOnce sync.Once           `json:"-"`
	decodedToolCall     *StreamToolCallInfo `json:"-"`
	decodedToolCallErr  error               `json:"-"`

	decodedDeltaOnce sync.Once           `json:"-"`
	decodedDelta     *StreamToolCallInfo `json:"-"`
	decodedDeltaErr  error               `json:"-"`
}

func (e *StreamEvent) DecodeChunkPayload() (*StreamChunkPayload, error) {
	if e == nil {
		return nil, io.EOF
	}
	var payload StreamChunkPayload
	if err := json.Unmarshal(e.Data, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

func (p *StreamChunkPayload) MetadataValue(key string) (interface{}, bool) {
	if p == nil || p.Metadata == nil {
		return nil, false
	}
	return mapValue(p.Metadata, key)
}

func (p *StreamChunkPayload) MetadataString(key string) string {
	if p == nil {
		return ""
	}
	return mapStringValue(p.Metadata, key)
}

func (p *StreamChunkPayload) MetadataBool(key string) (bool, bool) {
	if p == nil {
		return false, false
	}
	return mapBoolValue(p.Metadata, key)
}

func (p *StreamChunkPayload) MetadataInt(key string) (int, bool) {
	if p == nil {
		return 0, false
	}
	return mapIntValue(p.Metadata, key)
}

func (p *StreamChunkPayload) MetadataMap(key string) (map[string]interface{}, bool) {
	if p == nil {
		return nil, false
	}
	return mapMapValue(p.Metadata, key)
}

type StreamTextBlock struct {
	Content    string `json:"content,omitempty"`
	TotalChars int    `json:"total_chars,omitempty"`
}

type StreamReasoningBlock struct {
	Content string `json:"content,omitempty"`
	Delta   string `json:"delta,omitempty"`
	Length  int    `json:"length,omitempty"`
}

type StreamToolCallInfo struct {
	ID        string                 `json:"id,omitempty"`
	Name      string                 `json:"name,omitempty"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

type StreamToolEvent struct {
	ID      string                 `json:"id,omitempty"`
	Name    string                 `json:"name,omitempty"`
	Args    map[string]interface{} `json:"args,omitempty"`
	Status  string                 `json:"status,omitempty"`
	Content string                 `json:"content,omitempty"`
}

func (p *StreamChunkPayload) DecodeText() (*StreamTextBlock, error) {
	if p == nil || p.Text == nil {
		return nil, nil
	}
	p.decodedTextOnce.Do(func() {
		p.decodedText, p.decodedTextErr = decodeMapPayload[StreamTextBlock](p.Text)
	})
	return p.decodedText, p.decodedTextErr
}

func (p *StreamChunkPayload) DecodeReasoning() (*StreamReasoningBlock, error) {
	if p == nil || p.Reasoning == nil {
		return nil, nil
	}
	p.decodedReasoningOnce.Do(func() {
		p.decodedReasoning, p.decodedReasoningErr = decodeMapPayload[StreamReasoningBlock](p.Reasoning)
	})
	return p.decodedReasoning, p.decodedReasoningErr
}

func (p *StreamChunkPayload) DecodeTool() (*StreamToolEvent, error) {
	if p == nil || p.Tool == nil {
		return nil, nil
	}
	p.decodedToolOnce.Do(func() {
		p.decodedTool, p.decodedToolErr = decodeMapPayload[StreamToolEvent](p.Tool)
	})
	return p.decodedTool, p.decodedToolErr
}

func (p *StreamChunkPayload) DecodeToolCall() (*StreamToolCallInfo, error) {
	if p == nil || p.ToolCall == nil {
		return nil, nil
	}
	p.decodedToolCallOnce.Do(func() {
		p.decodedToolCall, p.decodedToolCallErr = decodeMapPayload[StreamToolCallInfo](p.ToolCall)
	})
	return p.decodedToolCall, p.decodedToolCallErr
}

func (p *StreamChunkPayload) DecodeDelta() (*StreamToolCallInfo, error) {
	if p == nil || p.Delta == nil {
		return nil, nil
	}
	p.decodedDeltaOnce.Do(func() {
		p.decodedDelta, p.decodedDeltaErr = decodeMapPayload[StreamToolCallInfo](p.Delta)
	})
	return p.decodedDelta, p.decodedDeltaErr
}

type StreamPlanningPayload struct {
	EventMeta                      StreamEnvelopeMeta     `json:"_event"`
	Mode                           string                 `json:"mode,omitempty"`
	Attempted                      bool                   `json:"attempted"`
	PlanningSource                 string                 `json:"planning_source,omitempty"`
	PlanningError                  string                 `json:"planning_error,omitempty"`
	StepCount                      int                    `json:"step_count,omitempty"`
	SubagentTaskCount              int                    `json:"subagent_task_count,omitempty"`
	SubagentExecutionRequested     bool                   `json:"subagent_execution_requested,omitempty"`
	SubagentExecutionEligible      bool                   `json:"subagent_execution_eligible,omitempty"`
	SubagentExecutionBlockedReason string                 `json:"subagent_execution_blocked_reason,omitempty"`
	SubagentExecutionAttempted     bool                   `json:"subagent_execution_attempted,omitempty"`
	SubagentExecutionError         string                 `json:"subagent_execution_error,omitempty"`
	SubagentResultCount            int                    `json:"subagent_result_count,omitempty"`
	Goal                           string                 `json:"goal,omitempty"`
	Steps                          []PlanningStep         `json:"steps,omitempty"`
	SubagentTasks                  []PlanningSubagentTask `json:"subagent_tasks,omitempty"`
}

func (e *StreamEvent) DecodePlanningPayload() (*StreamPlanningPayload, error) {
	if e == nil {
		return nil, io.EOF
	}
	var payload StreamPlanningPayload
	if err := json.Unmarshal(e.Data, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

type StreamOrchestrationPayload struct {
	EventMeta StreamEnvelopeMeta `json:"_event"`
	OrchestrationSummary
}

func (e *StreamEvent) DecodeOrchestrationPayload() (*StreamOrchestrationPayload, error) {
	if e == nil {
		return nil, io.EOF
	}
	var payload StreamOrchestrationPayload
	if err := json.Unmarshal(e.Data, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

type StreamRoutePayload struct {
	EventMeta       StreamEnvelopeMeta `json:"_event"`
	Source          string             `json:"source,omitempty"`
	Skill           string             `json:"skill,omitempty"`
	RouteAttempted  bool               `json:"route_attempted"`
	RouteMatched    bool               `json:"route_matched"`
	CandidateCount  int                `json:"candidate_count,omitempty"`
	RouteCandidates []RouteCandidate   `json:"route_candidates,omitempty"`
}

func (e *StreamEvent) DecodeRoutePayload() (*StreamRoutePayload, error) {
	if e == nil {
		return nil, io.EOF
	}
	var payload StreamRoutePayload
	if err := json.Unmarshal(e.Data, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

func (p *StreamRoutePayload) SelectedRoute() *RouteCandidate {
	if p == nil {
		return nil
	}
	for i := range p.RouteCandidates {
		if p.RouteCandidates[i].Chosen {
			return &p.RouteCandidates[i]
		}
	}
	return nil
}

type StreamObservationPayload struct {
	EventMeta  StreamEnvelopeMeta     `json:"_event"`
	Index      int                    `json:"index,omitempty"`
	Step       string                 `json:"step"`
	Tool       string                 `json:"tool"`
	Input      interface{}            `json:"input,omitempty"`
	Output     interface{}            `json:"output,omitempty"`
	Success    bool                   `json:"success"`
	Error      string                 `json:"error,omitempty"`
	Metrics    map[string]interface{} `json:"metrics,omitempty"`
	DurationMS int64                  `json:"duration_ms,omitempty"`
}

func (e *StreamEvent) DecodeObservationPayload() (*StreamObservationPayload, error) {
	if e == nil {
		return nil, io.EOF
	}
	var payload StreamObservationPayload
	if err := json.Unmarshal(e.Data, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

type StreamResultPayload struct {
	EventMeta StreamEnvelopeMeta `json:"_event"`
	AgentChatResult
}

func (e *StreamEvent) DecodeResultPayload() (*StreamResultPayload, error) {
	if e == nil {
		return nil, io.EOF
	}
	var payload StreamResultPayload
	if err := json.Unmarshal(e.Data, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

type StreamDonePayload struct {
	EventMeta StreamEnvelopeMeta     `json:"_event"`
	SessionID string                 `json:"session_id,omitempty"`
	AgentID   string                 `json:"agent_id,omitempty"`
	Source    string                 `json:"source,omitempty"`
	Status    string                 `json:"status,omitempty"`
	Content   string                 `json:"content,omitempty"`
	Result    map[string]interface{} `json:"result,omitempty"`

	decodedResultOnce sync.Once        `json:"-"`
	decodedResult     *AgentChatResult `json:"-"`
	decodedResultErr  error            `json:"-"`
}

func (e *StreamEvent) DecodeDonePayload() (*StreamDonePayload, error) {
	if e == nil {
		return nil, io.EOF
	}
	var payload StreamDonePayload
	if err := json.Unmarshal(e.Data, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

func (p *StreamDonePayload) DecodeResult() (*AgentChatResult, error) {
	if p == nil || p.Result == nil {
		return nil, nil
	}
	p.decodedResultOnce.Do(func() {
		p.decodedResult, p.decodedResultErr = decodeMapPayload[AgentChatResult](p.Result)
	})
	return p.decodedResult, p.decodedResultErr
}

func stringMetricValue(metrics map[string]interface{}, key string) string {
	if len(metrics) == 0 || key == "" {
		return ""
	}
	return contextStringValue(metrics, key)
}

func mapValue(values map[string]interface{}, key string) (interface{}, bool) {
	if len(values) == 0 || key == "" {
		return nil, false
	}
	value, ok := values[key]
	if !ok || value == nil {
		return nil, false
	}
	return value, true
}

func mapStringValue(values map[string]interface{}, key string) string {
	if len(values) == 0 || key == "" {
		return ""
	}
	return contextStringValue(values, key)
}

func mapBoolValue(values map[string]interface{}, key string) (bool, bool) {
	value, ok := mapValue(values, key)
	if !ok {
		return false, false
	}
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
		if err == nil {
			return parsed, true
		}
	}
	return false, false
}

func mapIntValue(values map[string]interface{}, key string) (int, bool) {
	value, ok := mapValue(values, key)
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return typed, true
	case int8:
		return int(typed), true
	case int16:
		return int(typed), true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float32:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil {
			return int(parsed), true
		}
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err == nil {
			return parsed, true
		}
	}
	return 0, false
}

func mapMapValue(values map[string]interface{}, key string) (map[string]interface{}, bool) {
	value, ok := mapValue(values, key)
	if !ok {
		return nil, false
	}
	typed, ok := value.(map[string]interface{})
	return typed, ok
}

func contextStringValue(values map[string]interface{}, key string) string {
	if len(values) == 0 || key == "" {
		return ""
	}
	value, ok := values[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return fmt.Sprint(typed)
	}
}

func decodeMapPayload[T any](payload map[string]interface{}) (*T, error) {
	if payload == nil {
		return nil, nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return decodeRawPayload[T](data)
}

func decodeSlicePayload[T any](payload []map[string]interface{}) ([]T, error) {
	if payload == nil {
		return nil, nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return decodeRawSlicePayload[T](data)
}

func decodeRawPayload[T any](data []byte) (*T, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var decoded T
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	return &decoded, nil
}

func decodeRawSlicePayload[T any](data []byte) ([]T, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var decoded []T
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func buildGovernanceSummary(observations []Observation) GovernanceSummary {
	if len(observations) == 0 {
		return GovernanceSummary{}
	}

	names := make(map[string]struct{})
	summary := GovernanceSummary{}
	for _, obs := range observations {
		governance := obs.Governance()
		if !governance.Present() {
			continue
		}
		if governance.MCPName != "" {
			names[governance.MCPName] = struct{}{}
		}
		if governance.IsLocalMCP() {
			summary.LocalMCPCount++
		}
		if governance.IsRemoteMCP() {
			summary.RemoteMCPCount++
		}
		if governance.IsTrustedRemote() {
			summary.TrustedRemoteCount++
		}
		if governance.IsUntrustedRemote() {
			summary.UntrustedRemoteCount++
		}
	}

	if len(names) > 0 {
		summary.MCPNames = make([]string, 0, len(names))
		for name := range names {
			summary.MCPNames = append(summary.MCPNames, name)
		}
		sort.Strings(summary.MCPNames)
	}
	return summary
}

func filterMCPStatuses(items []RuntimeMCPStatus, keep func(RuntimeMCPStatus) bool) []RuntimeMCPStatus {
	if len(items) == 0 {
		return nil
	}
	filtered := make([]RuntimeMCPStatus, 0, len(items))
	for _, item := range items {
		if keep == nil || keep(item) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

type StreamErrorPayload struct {
	EventMeta StreamEnvelopeMeta `json:"_event"`
	Index     int                `json:"index,omitempty"`
	Message   string             `json:"message,omitempty"`
	Source    string             `json:"source,omitempty"`
}

func (e *StreamEvent) DecodeErrorPayload() (*StreamErrorPayload, error) {
	if e == nil {
		return nil, io.EOF
	}
	var payload StreamErrorPayload
	if err := json.Unmarshal(e.Data, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

type DecodedStreamEvent struct {
	Event         string
	Raw           *StreamEvent
	Meta          *StreamMetaPayload
	Chunk         *StreamChunkPayload
	Planning      *StreamPlanningPayload
	Orchestration *StreamOrchestrationPayload
	Route         *StreamRoutePayload
	Observation   *StreamObservationPayload
	Result        *StreamResultPayload
	Done          *StreamDonePayload
	Error         *StreamErrorPayload
}

type StreamHandlers struct {
	OnEvent         func(*DecodedStreamEvent) error
	OnMeta          func(*StreamMetaPayload) error
	OnChunk         func(*StreamChunkPayload) error
	OnPlanning      func(*StreamPlanningPayload) error
	OnOrchestration func(*StreamOrchestrationPayload) error
	OnRoute         func(*StreamRoutePayload) error
	OnObservation   func(*StreamObservationPayload) error
	OnResult        func(*StreamResultPayload) error
	OnDone          func(*StreamDonePayload) error
	OnError         func(*StreamErrorPayload) error
	OnUnknown       func(*DecodedStreamEvent) error
}

func (e *StreamEvent) DecodeTyped() (*DecodedStreamEvent, error) {
	if e == nil {
		return nil, io.EOF
	}
	decoded := &DecodedStreamEvent{
		Event: e.Event,
		Raw:   e,
	}
	switch e.Event {
	case "meta":
		payload, err := e.DecodeMetaPayload()
		decoded.Meta = payload
		return decoded, err
	case "chunk", "reasoning", "tool_call", "tool_start", "tool_end":
		payload, err := e.DecodeChunkPayload()
		decoded.Chunk = payload
		return decoded, err
	case "planning":
		payload, err := e.DecodePlanningPayload()
		decoded.Planning = payload
		return decoded, err
	case "orchestration":
		payload, err := e.DecodeOrchestrationPayload()
		decoded.Orchestration = payload
		return decoded, err
	case "route":
		payload, err := e.DecodeRoutePayload()
		decoded.Route = payload
		return decoded, err
	case "observation":
		payload, err := e.DecodeObservationPayload()
		decoded.Observation = payload
		return decoded, err
	case "result":
		payload, err := e.DecodeResultPayload()
		decoded.Result = payload
		return decoded, err
	case "done":
		payload, err := e.DecodeDonePayload()
		decoded.Done = payload
		return decoded, err
	case "error":
		payload, err := e.DecodeErrorPayload()
		decoded.Error = payload
		return decoded, err
	default:
		return decoded, nil
	}
}

func (s *Stream) Close() error {
	if s == nil || s.closed {
		return nil
	}
	s.closed = true
	if s.body != nil {
		return s.body.Close()
	}
	return nil
}

func (s *Stream) Next() (*StreamEvent, error) {
	if s == nil || s.closed {
		return nil, io.EOF
	}

	var eventName string
	var dataLines []string
	for s.scanner.Scan() {
		line := s.scanner.Text()
		if line == "" {
			if eventName == "" && len(dataLines) == 0 {
				continue
			}
			return &StreamEvent{
				Event: eventName,
				Data:  json.RawMessage(strings.Join(dataLines, "\n")),
			}, nil
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}

	if err := s.scanner.Err(); err != nil {
		return nil, err
	}
	_ = s.Close()
	if eventName != "" || len(dataLines) > 0 {
		return &StreamEvent{
			Event: eventName,
			Data:  json.RawMessage(strings.Join(dataLines, "\n")),
		}, nil
	}
	return nil, io.EOF
}

func (s *Stream) NextDecoded() (*DecodedStreamEvent, error) {
	event, err := s.Next()
	if err != nil {
		return nil, err
	}
	return event.DecodeTyped()
}

func (s *Stream) Consume(handlers StreamHandlers) error {
	for {
		decoded, err := s.NextDecoded()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			_ = s.Close()
			return err
		}

		if handlers.OnEvent != nil {
			if err := handlers.OnEvent(decoded); err != nil {
				_ = s.Close()
				return err
			}
		}

		var callbackErr error
		switch {
		case decoded.Meta != nil && handlers.OnMeta != nil:
			callbackErr = handlers.OnMeta(decoded.Meta)
		case decoded.Chunk != nil && handlers.OnChunk != nil:
			callbackErr = handlers.OnChunk(decoded.Chunk)
		case decoded.Planning != nil && handlers.OnPlanning != nil:
			callbackErr = handlers.OnPlanning(decoded.Planning)
		case decoded.Orchestration != nil && handlers.OnOrchestration != nil:
			callbackErr = handlers.OnOrchestration(decoded.Orchestration)
		case decoded.Route != nil && handlers.OnRoute != nil:
			callbackErr = handlers.OnRoute(decoded.Route)
		case decoded.Observation != nil && handlers.OnObservation != nil:
			callbackErr = handlers.OnObservation(decoded.Observation)
		case decoded.Result != nil && handlers.OnResult != nil:
			callbackErr = handlers.OnResult(decoded.Result)
		case decoded.Done != nil && handlers.OnDone != nil:
			callbackErr = handlers.OnDone(decoded.Done)
		case decoded.Error != nil && handlers.OnError != nil:
			callbackErr = handlers.OnError(decoded.Error)
		case handlers.OnUnknown != nil:
			callbackErr = handlers.OnUnknown(decoded)
		}
		if callbackErr != nil {
			_ = s.Close()
			return callbackErr
		}

		if decoded.Done != nil || decoded.Error != nil {
			return s.Close()
		}
	}
}

func (c *Client) ListSkills(ctx context.Context, params ListSkillsParams) (*ListSkillsResponse, error) {
	query := url.Values{}
	if params.SourceLayer != "" {
		query.Set("source_layer", params.SourceLayer)
	}
	if params.SourceDir != "" {
		query.Set("source_dir", params.SourceDir)
	}

	var response ListSkillsResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime/skills", query, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) GetSkill(ctx context.Context, name string) (*Skill, error) {
	var response Skill
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime/skills/"+url.PathEscape(name), nil, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) SearchSkills(ctx context.Context, params SearchSkillsParams) (*SearchSkillsResponse, error) {
	query := url.Values{}
	query.Set("q", params.Query)
	if params.Limit > 0 {
		query.Set("limit", strconv.Itoa(params.Limit))
	}
	if params.Category != "" {
		query.Set("category", params.Category)
	}
	if params.Mode != "" {
		query.Set("mode", params.Mode)
	}
	if params.SourceLayer != "" {
		query.Set("source_layer", params.SourceLayer)
	}
	if params.SourceDir != "" {
		query.Set("source_dir", params.SourceDir)
	}

	var response SearchSkillsResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime/skills/search", query, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) GetStats(ctx context.Context, params ListSkillsParams) (*GetStatsResponse, error) {
	query := url.Values{}
	if params.SourceLayer != "" {
		query.Set("source_layer", params.SourceLayer)
	}
	if params.SourceDir != "" {
		query.Set("source_dir", params.SourceDir)
	}

	var response GetStatsResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime/skills/stats", query, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) GetRuntimeStatus(ctx context.Context) (*GetRuntimeStatusResponse, error) {
	var response GetRuntimeStatusResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime/status", nil, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) GetRuntimeHealth(ctx context.Context) (*GetRuntimeHealthResponse, error) {
	var response GetRuntimeHealthResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime/health", nil, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) ReloadRuntimeMCPs(ctx context.Context) (*ReloadRuntimeMCPsResponse, error) {
	var response ReloadRuntimeMCPsResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/runtime/mcps/reload", nil, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) ValidateRuntime(ctx context.Context) (*GetRuntimeValidationResponse, error) {
	var response GetRuntimeValidationResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime/validate", nil, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) GetCapabilities(ctx context.Context) (*GetCapabilitiesResponse, error) {
	var response GetCapabilitiesResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime/capabilities", nil, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) ExecuteSkill(ctx context.Context, name string, req ExecuteSkillRequest) (*ExecuteSkillResponse, error) {
	var response ExecuteSkillResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/runtime/skills/"+url.PathEscape(name)+"/execute", nil, req, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) AgentChat(ctx context.Context, req AgentChatRequest) (*AgentChatResponse, error) {
	req.Stream = false
	var response AgentChatResponse
	if err := c.doJSON(ctx, http.MethodPost, agentChatEndpoint, nil, req, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) AgentChatStream(ctx context.Context, req AgentChatRequest) (*Stream, error) {
	req.Stream = true
	httpReq, err := c.newRequest(ctx, http.MethodPost, agentChatEndpoint, nil, req)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return nil, decodeAPIError(resp.StatusCode, body)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	return &Stream{
		body:    resp.Body,
		scanner: scanner,
	}, nil
}

func (c *Client) StreamSessionRuntimeEvents(ctx context.Context, sessionID string, afterSeq int64, pollMs int) (*Stream, error) {
	query := url.Values{}
	if afterSeq > 0 {
		query.Set("after", strconv.FormatInt(afterSeq, 10))
	}
	if pollMs > 0 {
		query.Set("poll_ms", strconv.Itoa(pollMs))
	}

	httpReq, err := c.newRequest(ctx, http.MethodGet, "/api/runtime/sessions/"+url.PathEscape(sessionID)+"/runtime/stream", query, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return nil, decodeAPIError(resp.StatusCode, body)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	return &Stream{
		body:    resp.Body,
		scanner: scanner,
	}, nil
}

func (c *Client) ListRuntimeEvents(ctx context.Context, filters map[string]string, limit int) (*RuntimeEventsResponse, error) {
	query := url.Values{}
	for key, value := range filters {
		if strings.TrimSpace(key) == "" {
			continue
		}
		query.Set(key, strings.TrimSpace(value))
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}

	var response RuntimeEventsResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime/events", query, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) CreateSkill(ctx context.Context, skill Skill, opts CreateSkillOptions) (*SkillMutationResponse, error) {
	query := url.Values{}
	if opts.Persist {
		query.Set("persist", "true")
	}
	if opts.TargetDir != "" {
		query.Set("target_dir", opts.TargetDir)
	}

	var response SkillMutationResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/runtime/skills", query, skill, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) UpdateSkill(ctx context.Context, name string, skill Skill, opts UpdateSkillOptions) (*SkillMutationResponse, error) {
	query := url.Values{}
	if opts.Persist != nil {
		query.Set("persist", strconv.FormatBool(*opts.Persist))
	}
	if opts.TargetDir != "" {
		query.Set("target_dir", opts.TargetDir)
	}

	var response SkillMutationResponse
	if err := c.doJSON(ctx, http.MethodPut, "/api/runtime/skills/"+url.PathEscape(name), query, skill, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) DeleteSkill(ctx context.Context, name string, opts DeleteSkillOptions) (*SkillMutationResponse, error) {
	query := url.Values{}
	if opts.DeleteFile {
		query.Set("delete_file", "true")
	}

	var response SkillMutationResponse
	if err := c.doJSON(ctx, http.MethodDelete, "/api/runtime/skills/"+url.PathEscape(name), query, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) ExportSkills(ctx context.Context, params ListSkillsParams) (*ExportSkillsResponse, error) {
	query := url.Values{}
	if params.SourceLayer != "" {
		query.Set("source_layer", params.SourceLayer)
	}
	if params.SourceDir != "" {
		query.Set("source_dir", params.SourceDir)
	}

	var response ExportSkillsResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime/skills/export", query, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) ImportSkills(ctx context.Context, skills []Skill) (*ImportSkillsResponse, error) {
	return c.ImportSkillsWithOptions(ctx, skills, ImportSkillsOptions{})
}

func (c *Client) ImportSkillsWithOptions(ctx context.Context, skills []Skill, opts ImportSkillsOptions) (*ImportSkillsResponse, error) {
	query := url.Values{}
	if opts.Persist {
		query.Set("persist", "true")
	}
	if opts.TargetDir != "" {
		query.Set("target_dir", opts.TargetDir)
	}

	var response ImportSkillsResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/runtime/skills/import", query, map[string]interface{}{"skills": skills}, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) ReloadSkills(ctx context.Context, req ReloadSkillsRequest) (*ReloadSkillsResponse, error) {
	var response ReloadSkillsResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/runtime/skills/reload", nil, req, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) StartHotReload(ctx context.Context, req HotReloadRequest) (*StartHotReloadResponse, error) {
	var response StartHotReloadResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/runtime/skills/hot-reload/start", nil, req, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) StopHotReload(ctx context.Context) (*StopHotReloadResponse, error) {
	var response StopHotReloadResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/runtime/skills/hot-reload/stop", nil, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) ReloadHotReload(ctx context.Context) (*ReloadHotReloadResponse, error) {
	var response ReloadHotReloadResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/runtime/skills/hot-reload/reload", nil, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) GetHotReloadStats(ctx context.Context) (*HotReloadStatsResponse, error) {
	var response HotReloadStatsResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime/skills/hot-reload/stats", nil, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) GetSearchStats(ctx context.Context) (*SearchStatsResponse, error) {
	var response SearchStatsResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime/skills/search/stats", nil, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) ReindexSearchIndex(ctx context.Context, force bool) (*ReindexSearchIndexResponse, error) {
	query := url.Values{}
	if force {
		query.Set("force", "true")
	}

	var response ReindexSearchIndexResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/runtime/skills/search/reindex", query, nil, &response); err != nil {
		if apiErr, ok := err.(*APIError); ok && apiErr.StatusCode == http.StatusTooManyRequests {
			_ = json.Unmarshal([]byte(apiErr.Body), &response)
			response.Error = apiErr.Message
			return &response, err
		}
		return nil, err
	}
	return &response, nil
}

func (c *Client) GetUsageStats(ctx context.Context, userID string) (*UsageStatsResponse, error) {
	return c.GetUsageStatsWithScope(ctx, UsageScope{UserID: userID})
}

func (c *Client) GetUsageStatsWithScope(ctx context.Context, scope UsageScope) (*UsageStatsResponse, error) {
	query := url.Values{}
	if scope.TenantID != "" {
		query.Set("tenant_id", scope.TenantID)
	}
	if scope.ProjectID != "" {
		query.Set("project_id", scope.ProjectID)
	}
	if scope.UserID != "" {
		query.Set("user_id", scope.UserID)
	}

	var response UsageStatsResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime/usage/stats", query, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) GetUsageLedger(ctx context.Context, params GetUsageLedgerParams) (*GetUsageLedgerResponse, error) {
	query := url.Values{}
	if params.Scope.TenantID != "" {
		query.Set("tenant_id", params.Scope.TenantID)
	}
	if params.Scope.ProjectID != "" {
		query.Set("project_id", params.Scope.ProjectID)
	}
	if params.Scope.UserID != "" {
		query.Set("user_id", params.Scope.UserID)
	}
	if params.Entrypoint != "" {
		query.Set("entrypoint", params.Entrypoint)
	}
	if params.Skill != "" {
		query.Set("skill", params.Skill)
	}
	if params.Success != nil {
		query.Set("success", strconv.FormatBool(*params.Success))
	}
	if params.Since != nil && !params.Since.IsZero() {
		query.Set("since", params.Since.Format(time.RFC3339))
	}
	if params.Limit > 0 {
		query.Set("limit", strconv.Itoa(params.Limit))
	}

	var response GetUsageLedgerResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime/usage/ledger", query, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) ResetUsageStats(ctx context.Context, req ResetUsageStatsRequest) (*ResetUsageStatsResponse, error) {
	var response ResetUsageStatsResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/runtime/usage/reset", nil, req, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) GetUsagePolicy(ctx context.Context) (*GetUsagePolicyResponse, error) {
	var response GetUsagePolicyResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime/usage/policy", nil, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) GetMutationPolicy(ctx context.Context) (*GetMutationPolicyResponse, error) {
	var response GetMutationPolicyResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime/mutation/policy", nil, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) GetGovernancePolicy(ctx context.Context) (*GetGovernancePolicyResponse, error) {
	var response GetGovernancePolicyResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime/governance/policy", nil, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) UpdateMutationPolicy(ctx context.Context, req MutationPolicyUpdateRequest) (*UpdateMutationPolicyResponse, error) {
	var response UpdateMutationPolicyResponse
	if err := c.doJSON(ctx, http.MethodPut, "/api/runtime/mutation/policy", nil, req, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) GetAuthPolicy(ctx context.Context) (*GetAuthPolicyResponse, error) {
	var response GetAuthPolicyResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime/auth/policy", nil, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) UpdateAuthPolicy(ctx context.Context, req AuthPolicyUpdateRequest) (*UpdateAuthPolicyResponse, error) {
	var response UpdateAuthPolicyResponse
	if err := c.doJSON(ctx, http.MethodPut, "/api/runtime/auth/policy", nil, req, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) DeleteAuthPolicyEntry(ctx context.Context, req DeleteAuthPolicyEntryRequest) (*DeleteAuthPolicyEntryResponse, error) {
	var response DeleteAuthPolicyEntryResponse
	if err := c.doJSON(ctx, http.MethodDelete, "/api/runtime/auth/policy", nil, req, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) UpdateUsagePolicy(ctx context.Context, req UsagePolicyUpdateRequest) (*UpdateUsagePolicyResponse, error) {
	var response UpdateUsagePolicyResponse
	if err := c.doJSON(ctx, http.MethodPut, "/api/runtime/usage/policy", nil, req, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) DeleteUsagePolicyEntry(ctx context.Context, req DeleteUsagePolicyEntryRequest) (*DeleteUsagePolicyEntryResponse, error) {
	var response DeleteUsagePolicyEntryResponse
	if err := c.doJSON(ctx, http.MethodDelete, "/api/runtime/usage/policy", nil, req, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) CreateSession(ctx context.Context, req CreateSessionRequest) (*CreateSessionResponse, error) {
	var response CreateSessionResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/runtime/sessions", nil, req, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) ListSessions(ctx context.Context, userID string) (*ListSessionsResponse, error) {
	query := url.Values{}
	if userID != "" {
		query.Set("user_id", userID)
	}

	var response ListSessionsResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime/sessions", query, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) GetSession(ctx context.Context, sessionID string) (*GetSessionResponse, error) {
	var response GetSessionResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime/sessions/"+url.PathEscape(sessionID), nil, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) UpdateSession(ctx context.Context, sessionID string, req UpdateSessionRequest) (*GetSessionResponse, error) {
	var response GetSessionResponse
	if err := c.doJSON(ctx, http.MethodPatch, "/api/runtime/sessions/"+url.PathEscape(sessionID), nil, req, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) DeleteSession(ctx context.Context, sessionID string) (*DeleteSessionResponse, error) {
	var response DeleteSessionResponse
	if err := c.doJSON(ctx, http.MethodDelete, "/api/runtime/sessions/"+url.PathEscape(sessionID), nil, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) GetSessionHistory(ctx context.Context, sessionID string) (*SessionHistoryResponse, error) {
	var response SessionHistoryResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime/sessions/"+url.PathEscape(sessionID)+"/history", nil, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) ClearSessionHistory(ctx context.Context, sessionID string) (*ClearSessionHistoryResponse, error) {
	var response ClearSessionHistoryResponse
	if err := c.doJSON(ctx, http.MethodDelete, "/api/runtime/sessions/"+url.PathEscape(sessionID)+"/history", nil, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) CreateTeam(ctx context.Context, req CreateTeamRequest) (*CreateTeamResponse, error) {
	var response CreateTeamResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/runtime/teams", nil, req, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) ListTeams(ctx context.Context, params ListTeamsParams) (*ListTeamsResponse, error) {
	query := url.Values{}
	if strings.TrimSpace(params.Status) != "" {
		query.Set("status", strings.TrimSpace(params.Status))
	}
	if params.Limit > 0 {
		query.Set("limit", strconv.Itoa(params.Limit))
	}
	if len(params.TeamIDs) > 0 {
		ids := make([]string, 0, len(params.TeamIDs))
		for _, id := range params.TeamIDs {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			ids = append(ids, id)
		}
		if len(ids) > 0 {
			query.Set("team_ids", strings.Join(ids, ","))
		}
	}
	if strings.TrimSpace(params.WorkspaceID) != "" {
		query.Set("workspace_id", strings.TrimSpace(params.WorkspaceID))
	}

	var response ListTeamsResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime/teams", query, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) ListTeammates(ctx context.Context, teamID string, params ListTeammatesParams) (*ListTeammatesResponse, error) {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return nil, fmt.Errorf("teamID is required")
	}

	query := url.Values{}
	if params.Limit > 0 {
		query.Set("limit", strconv.Itoa(params.Limit))
	}
	if strings.TrimSpace(params.State) != "" {
		query.Set("state", strings.TrimSpace(params.State))
	}

	var response ListTeammatesResponse
	endpoint := "/api/runtime/teams/" + url.PathEscape(teamID) + "/teammates"
	if err := c.doJSON(ctx, http.MethodGet, endpoint, query, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) PlanTeamTasks(ctx context.Context, teamID string, req PlanTeamTasksRequest) (*PlanTeamTasksResponse, error) {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return nil, fmt.Errorf("teamID is required")
	}

	var response PlanTeamTasksResponse
	endpoint := "/api/runtime/teams/" + url.PathEscape(teamID) + "/plan"
	if err := c.doJSON(ctx, http.MethodPost, endpoint, nil, req, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) GetTaskGraph(ctx context.Context, teamID string, params GetTaskGraphParams) (*GetTaskGraphResponse, error) {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return nil, fmt.Errorf("teamID is required")
	}

	query := url.Values{}
	if len(params.Status) > 0 {
		statuses := make([]string, 0, len(params.Status))
		for _, status := range params.Status {
			status = strings.TrimSpace(status)
			if status == "" {
				continue
			}
			statuses = append(statuses, status)
		}
		if len(statuses) > 0 {
			query.Set("status", strings.Join(statuses, ","))
		}
	}
	if strings.TrimSpace(params.Assignee) != "" {
		query.Set("assignee", strings.TrimSpace(params.Assignee))
	}
	if strings.TrimSpace(params.ParentTaskID) != "" {
		query.Set("parent_task_id", strings.TrimSpace(params.ParentTaskID))
	}
	if len(params.TaskIDs) > 0 {
		taskIDs := make([]string, 0, len(params.TaskIDs))
		for _, taskID := range params.TaskIDs {
			taskID = strings.TrimSpace(taskID)
			if taskID == "" {
				continue
			}
			taskIDs = append(taskIDs, taskID)
		}
		if len(taskIDs) > 0 {
			query.Set("task_ids", strings.Join(taskIDs, ","))
		}
	}
	if params.IncludeExternal {
		query.Set("include_external", "true")
	}
	if params.Limit > 0 {
		query.Set("limit", strconv.Itoa(params.Limit))
	}

	var response GetTaskGraphResponse
	endpoint := "/api/runtime/teams/" + url.PathEscape(teamID) + "/tasks/graph"
	if err := c.doJSON(ctx, http.MethodGet, endpoint, query, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) ClaimReadyTasks(ctx context.Context, teamID string, req ClaimReadyTasksRequest) (*ClaimReadyTasksResponse, error) {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return nil, fmt.Errorf("teamID is required")
	}

	var response ClaimReadyTasksResponse
	endpoint := "/api/runtime/teams/" + url.PathEscape(teamID) + "/tasks/claim"
	if err := c.doJSON(ctx, http.MethodPost, endpoint, nil, req, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) ReclaimExpiredTasks(ctx context.Context, teamID string, req ReclaimExpiredTasksRequest) (*ReclaimExpiredTasksResponse, error) {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return nil, fmt.Errorf("teamID is required")
	}

	var response ReclaimExpiredTasksResponse
	endpoint := "/api/runtime/teams/" + url.PathEscape(teamID) + "/tasks/reclaim"
	if err := c.doJSON(ctx, http.MethodPost, endpoint, nil, req, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) MarkReadyTasks(ctx context.Context, teamID string) (*MarkReadyTasksResponse, error) {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return nil, fmt.Errorf("teamID is required")
	}

	var response MarkReadyTasksResponse
	endpoint := "/api/runtime/teams/" + url.PathEscape(teamID) + "/tasks/ready"
	if err := c.doJSON(ctx, http.MethodPost, endpoint, nil, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) ReplanTask(ctx context.Context, teamID, taskID string, req ReplanTaskRequest) (*ReplanTaskResponse, error) {
	teamID = strings.TrimSpace(teamID)
	taskID = strings.TrimSpace(taskID)
	if teamID == "" {
		return nil, fmt.Errorf("teamID is required")
	}
	if taskID == "" {
		return nil, fmt.Errorf("taskID is required")
	}

	var response ReplanTaskResponse
	endpoint := "/api/runtime/teams/" + url.PathEscape(teamID) + "/tasks/" + url.PathEscape(taskID) + "/replan"
	if err := c.doJSON(ctx, http.MethodPost, endpoint, nil, req, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) ReportTaskOutcome(ctx context.Context, teamID, taskID string, req ReportTaskOutcomeRequest) (*ReportTaskOutcomeResponse, error) {
	return c.teamTaskOutcomeAction(ctx, teamID, taskID, "outcome", req)
}

func (c *Client) CompleteTask(ctx context.Context, teamID, taskID string, req ReportTaskOutcomeRequest) (*ReportTaskOutcomeResponse, error) {
	req.TaskStatus = "done"
	return c.ReportTaskOutcome(ctx, teamID, taskID, req)
}

func (c *Client) FailTask(ctx context.Context, teamID, taskID string, req ReportTaskOutcomeRequest) (*ReportTaskOutcomeResponse, error) {
	req.TaskStatus = "failed"
	return c.ReportTaskOutcome(ctx, teamID, taskID, req)
}

func (c *Client) BlockTask(ctx context.Context, teamID, taskID string, req ReportTaskOutcomeRequest) (*ReportTaskOutcomeResponse, error) {
	req.TaskStatus = "blocked"
	return c.ReportTaskOutcome(ctx, teamID, taskID, req)
}

func (c *Client) GetTeamTask(ctx context.Context, teamID, taskID string, opts GetTeamTaskOptions) (*GetTeamTaskResponse, error) {
	teamID = strings.TrimSpace(teamID)
	taskID = strings.TrimSpace(taskID)
	if teamID == "" {
		return nil, fmt.Errorf("teamID is required")
	}
	if taskID == "" {
		return nil, fmt.Errorf("taskID is required")
	}

	query := url.Values{}
	if opts.IncludeDependencies {
		query.Set("include_dependencies", "true")
	}
	if opts.IncludeDependents {
		query.Set("include_dependents", "true")
	}

	var response GetTeamTaskResponse
	endpoint := "/api/runtime/teams/" + url.PathEscape(teamID) + "/tasks/" + url.PathEscape(taskID)
	if err := c.doJSON(ctx, http.MethodGet, endpoint, query, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) ListTeamTasks(ctx context.Context, teamID string, params ListTeamTasksParams) (*ListTeamTasksResponse, error) {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return nil, fmt.Errorf("teamID is required")
	}

	query := url.Values{}
	if len(params.Status) > 0 {
		statuses := make([]string, 0, len(params.Status))
		for _, status := range params.Status {
			status = strings.TrimSpace(status)
			if status == "" {
				continue
			}
			statuses = append(statuses, status)
		}
		if len(statuses) > 0 {
			query.Set("status", strings.Join(statuses, ","))
		}
	}
	if strings.TrimSpace(params.Assignee) != "" {
		query.Set("assignee", strings.TrimSpace(params.Assignee))
	}
	if strings.TrimSpace(params.ParentTaskID) != "" {
		query.Set("parent_task_id", strings.TrimSpace(params.ParentTaskID))
	}
	if len(params.TaskIDs) > 0 {
		taskIDs := make([]string, 0, len(params.TaskIDs))
		for _, taskID := range params.TaskIDs {
			taskID = strings.TrimSpace(taskID)
			if taskID == "" {
				continue
			}
			taskIDs = append(taskIDs, taskID)
		}
		if len(taskIDs) > 0 {
			query.Set("task_ids", strings.Join(taskIDs, ","))
		}
	}
	if params.IncludeDependencies {
		query.Set("include_dependencies", "true")
	}
	if params.IncludeDependents {
		query.Set("include_dependents", "true")
	}
	if params.Limit > 0 {
		query.Set("limit", strconv.Itoa(params.Limit))
	}

	var response ListTeamTasksResponse
	endpoint := "/api/runtime/teams/" + url.PathEscape(teamID) + "/tasks"
	if err := c.doJSON(ctx, http.MethodGet, endpoint, query, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) ListTaskDependencies(ctx context.Context, teamID, taskID string) (*TeamTaskDependenciesResponse, error) {
	teamID = strings.TrimSpace(teamID)
	taskID = strings.TrimSpace(taskID)
	if teamID == "" {
		return nil, fmt.Errorf("teamID is required")
	}
	if taskID == "" {
		return nil, fmt.Errorf("taskID is required")
	}

	var response TeamTaskDependenciesResponse
	endpoint := "/api/runtime/teams/" + url.PathEscape(teamID) + "/tasks/" + url.PathEscape(taskID) + "/dependencies"
	if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) ListTaskDependents(ctx context.Context, teamID, taskID string) (*TeamTaskDependentsResponse, error) {
	teamID = strings.TrimSpace(teamID)
	taskID = strings.TrimSpace(taskID)
	if teamID == "" {
		return nil, fmt.Errorf("teamID is required")
	}
	if taskID == "" {
		return nil, fmt.Errorf("taskID is required")
	}

	var response TeamTaskDependentsResponse
	endpoint := "/api/runtime/teams/" + url.PathEscape(teamID) + "/tasks/" + url.PathEscape(taskID) + "/dependents"
	if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) CreateTeamTask(ctx context.Context, teamID string, req CreateTeamTaskRequest) (*CreateTeamTaskResponse, error) {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return nil, fmt.Errorf("teamID is required")
	}

	var response CreateTeamTaskResponse
	endpoint := "/api/runtime/teams/" + url.PathEscape(teamID) + "/tasks"
	if err := c.doJSON(ctx, http.MethodPost, endpoint, nil, req, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) UpdateTeamTask(ctx context.Context, teamID, taskID string, req UpdateTeamTaskRequest) (*UpdateTeamTaskResponse, error) {
	teamID = strings.TrimSpace(teamID)
	taskID = strings.TrimSpace(taskID)
	if teamID == "" {
		return nil, fmt.Errorf("teamID is required")
	}
	if taskID == "" {
		return nil, fmt.Errorf("taskID is required")
	}

	var response UpdateTeamTaskResponse
	endpoint := "/api/runtime/teams/" + url.PathEscape(teamID) + "/tasks/" + url.PathEscape(taskID)
	if err := c.doJSON(ctx, http.MethodPatch, endpoint, nil, req, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) AddTaskDependency(ctx context.Context, teamID, taskID, dependsOnID string) (*AddTaskDependencyResponse, error) {
	teamID = strings.TrimSpace(teamID)
	taskID = strings.TrimSpace(taskID)
	dependsOnID = strings.TrimSpace(dependsOnID)
	if teamID == "" {
		return nil, fmt.Errorf("teamID is required")
	}
	if taskID == "" {
		return nil, fmt.Errorf("taskID is required")
	}
	if dependsOnID == "" {
		return nil, fmt.Errorf("dependsOnID is required")
	}

	var response AddTaskDependencyResponse
	endpoint := "/api/runtime/teams/" + url.PathEscape(teamID) + "/tasks/" + url.PathEscape(taskID) + "/dependencies"
	if err := c.doJSON(ctx, http.MethodPost, endpoint, nil, AddTaskDependencyRequest{
		DependsOnID: dependsOnID,
	}, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) SendTeamMailboxMessage(ctx context.Context, teamID string, req SendTeamMailboxMessageRequest) (*SendTeamMailboxMessageResponse, error) {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return nil, fmt.Errorf("teamID is required")
	}

	var response SendTeamMailboxMessageResponse
	endpoint := "/api/runtime/teams/" + url.PathEscape(teamID) + "/mailbox"
	if err := c.doJSON(ctx, http.MethodPost, endpoint, nil, req, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) ListTeamMailbox(ctx context.Context, teamID string, params ListTeamMailboxParams) (*ListTeamMailboxResponse, error) {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return nil, fmt.Errorf("teamID is required")
	}

	query := url.Values{}
	if strings.TrimSpace(params.FromAgent) != "" {
		query.Set("from_agent", strings.TrimSpace(params.FromAgent))
	}
	if strings.TrimSpace(params.ToAgent) != "" {
		query.Set("to_agent", strings.TrimSpace(params.ToAgent))
	}
	if strings.TrimSpace(params.TaskID) != "" {
		query.Set("task_id", strings.TrimSpace(params.TaskID))
	}
	if strings.TrimSpace(params.ParentTaskID) != "" {
		query.Set("parent_task_id", strings.TrimSpace(params.ParentTaskID))
	}
	if strings.TrimSpace(params.Kind) != "" {
		query.Set("kind", strings.TrimSpace(params.Kind))
	}
	if strings.TrimSpace(params.AgentID) != "" {
		query.Set("agent_id", strings.TrimSpace(params.AgentID))
	}
	if params.UnreadOnly {
		query.Set("unread_only", "true")
	}
	if params.MarkRead {
		query.Set("mark_read", "true")
	}
	if params.IncludeBroadcast {
		query.Set("include_broadcast", "true")
	}
	if params.Since != nil && !params.Since.IsZero() {
		query.Set("since", params.Since.UTC().Format(time.RFC3339Nano))
	}
	if params.Limit > 0 {
		query.Set("limit", strconv.Itoa(params.Limit))
	}

	var response ListTeamMailboxResponse
	endpoint := "/api/runtime/teams/" + url.PathEscape(teamID) + "/mailbox"
	if err := c.doJSON(ctx, http.MethodGet, endpoint, query, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) AckTeamMailboxMessage(ctx context.Context, teamID, messageID, agentID string) (*AckTeamMailboxMessageResponse, error) {
	teamID = strings.TrimSpace(teamID)
	messageID = strings.TrimSpace(messageID)
	if teamID == "" {
		return nil, fmt.Errorf("teamID is required")
	}
	if messageID == "" {
		return nil, fmt.Errorf("messageID is required")
	}

	query := url.Values{}
	if strings.TrimSpace(agentID) != "" {
		query.Set("agent_id", strings.TrimSpace(agentID))
	}

	var response AckTeamMailboxMessageResponse
	endpoint := "/api/runtime/teams/" + url.PathEscape(teamID) + "/mailbox/" + url.PathEscape(messageID) + "/ack"
	if err := c.doJSON(ctx, http.MethodPost, endpoint, query, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) ListBackgroundJobs(ctx context.Context, sessionID, status string, limit, offset int) (*BackgroundJobListResponse, error) {
	query := url.Values{}
	if strings.TrimSpace(sessionID) != "" {
		query.Set("session_id", strings.TrimSpace(sessionID))
	}
	if strings.TrimSpace(status) != "" {
		query.Set("status", strings.TrimSpace(status))
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		query.Set("offset", strconv.Itoa(offset))
	}

	var response BackgroundJobListResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime/background/jobs", query, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) teamTaskOutcomeAction(ctx context.Context, teamID, taskID, action string, req ReportTaskOutcomeRequest) (*ReportTaskOutcomeResponse, error) {
	teamID = strings.TrimSpace(teamID)
	taskID = strings.TrimSpace(taskID)
	action = strings.TrimSpace(action)
	if teamID == "" {
		return nil, fmt.Errorf("teamID is required")
	}
	if taskID == "" {
		return nil, fmt.Errorf("taskID is required")
	}
	if action == "" {
		return nil, fmt.Errorf("action is required")
	}

	var response ReportTaskOutcomeResponse
	endpoint := "/api/runtime/teams/" + url.PathEscape(teamID) + "/tasks/" + url.PathEscape(taskID) + "/" + action
	if err := c.doJSON(ctx, http.MethodPost, endpoint, nil, req, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) GetBackgroundJob(ctx context.Context, jobID string) (*BackgroundJobResponse, error) {
	var response BackgroundJobResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime/background/jobs/"+url.PathEscape(jobID), nil, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) ListBackgroundJobEvents(ctx context.Context, jobID string, afterSeq int64, limit int) (*BackgroundJobEventsResponse, error) {
	query := url.Values{}
	if afterSeq > 0 {
		query.Set("after", strconv.FormatInt(afterSeq, 10))
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}

	var response BackgroundJobEventsResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime/background/jobs/"+url.PathEscape(jobID)+"/events", query, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) GetBackgroundJobOutput(ctx context.Context, jobID string, offset int64, limit int) (*BackgroundJobOutputResponse, error) {
	query := url.Values{}
	if offset > 0 {
		query.Set("offset", strconv.FormatInt(offset, 10))
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}

	var response BackgroundJobOutputResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime/background/jobs/"+url.PathEscape(jobID)+"/output", query, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) ListSessionCheckpoints(ctx context.Context, sessionID string, limit, offset int) (*CheckpointListResponse, error) {
	query := url.Values{}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		query.Set("offset", strconv.Itoa(offset))
	}

	var response CheckpointListResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime/sessions/"+url.PathEscape(sessionID)+"/checkpoints", query, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) GetCheckpointFiles(ctx context.Context, sessionID, checkpointID string) (*CheckpointFilesResponse, error) {
	var response CheckpointFilesResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime/sessions/"+url.PathEscape(sessionID)+"/checkpoints/"+url.PathEscape(checkpointID)+"/files", nil, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) PreviewSessionCheckpoint(ctx context.Context, sessionID, checkpointID, mode string) (*CheckpointPreviewResponse, error) {
	query := url.Values{}
	if strings.TrimSpace(mode) != "" {
		query.Set("mode", strings.TrimSpace(mode))
	}
	var response CheckpointPreviewResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/runtime/sessions/"+url.PathEscape(sessionID)+"/checkpoints/"+url.PathEscape(checkpointID)+"/preview", query, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) RestoreSessionCheckpoint(ctx context.Context, sessionID, checkpointID, mode string) (*SessionRuntimeCommandResponse, error) {
	query := url.Values{}
	if strings.TrimSpace(mode) != "" {
		query.Set("mode", strings.TrimSpace(mode))
	}
	var response SessionRuntimeCommandResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/runtime/sessions/"+url.PathEscape(sessionID)+"/checkpoints/"+url.PathEscape(checkpointID)+"/restore", query, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) GetSessionRuntimeState(ctx context.Context, sessionID string) (*SessionRuntimeStateResponse, error) {
	var response SessionRuntimeStateResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime/sessions/"+url.PathEscape(sessionID)+"/runtime", nil, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) ListSessionRuntimeEvents(ctx context.Context, sessionID string, afterSeq int64, limit int) (*SessionRuntimeEventsResponse, error) {
	query := url.Values{}
	if afterSeq > 0 {
		query.Set("after", strconv.FormatInt(afterSeq, 10))
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}

	var response SessionRuntimeEventsResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime/sessions/"+url.PathEscape(sessionID)+"/runtime/events", query, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) SubmitSessionRuntimeCommand(ctx context.Context, sessionID string, req SessionRuntimeCommandRequest) (*SessionRuntimeCommandResponse, error) {
	var response SessionRuntimeCommandResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/runtime/sessions/"+url.PathEscape(sessionID)+"/runtime/commands", nil, req, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) SpawnSessionAgent(ctx context.Context, parentSessionID string, req SpawnSessionAgentRequest) (*SessionAgentStatusResponse, error) {
	var response SessionAgentStatusResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/runtime/sessions/"+url.PathEscape(parentSessionID)+"/agents", nil, req, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) GetSessionAgentStatus(ctx context.Context, parentSessionID, agentID string) (*SessionAgentStatusResponse, error) {
	var response SessionAgentStatusResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime/sessions/"+url.PathEscape(parentSessionID)+"/agents/"+url.PathEscape(agentID), nil, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) SendSessionAgentInput(ctx context.Context, parentSessionID, agentID string, req SendSessionAgentInputRequest) (*SessionAgentStatusResponse, error) {
	var response SessionAgentStatusResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/runtime/sessions/"+url.PathEscape(parentSessionID)+"/agents/"+url.PathEscape(agentID)+"/input", nil, req, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) WaitSessionAgents(ctx context.Context, parentSessionID string, req WaitSessionAgentsRequest) (*WaitSessionAgentsResponse, error) {
	var response WaitSessionAgentsResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/runtime/sessions/"+url.PathEscape(parentSessionID)+"/agents/wait", nil, req, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) ListSessionAgentEvents(ctx context.Context, parentSessionID, agentID string, params ListSessionAgentEventsParams) (*ListSessionAgentEventsResponse, error) {
	query := url.Values{}
	if params.AfterSeq > 0 {
		query.Set("after_seq", strconv.FormatInt(params.AfterSeq, 10))
	}
	if params.Limit > 0 {
		query.Set("limit", strconv.Itoa(params.Limit))
	}
	if params.WaitMs > 0 {
		query.Set("wait_ms", strconv.Itoa(params.WaitMs))
	}

	var response ListSessionAgentEventsResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime/sessions/"+url.PathEscape(parentSessionID)+"/agents/"+url.PathEscape(agentID)+"/events", query, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) CloseSessionAgent(ctx context.Context, parentSessionID, agentID string) (*SessionAgentStatusResponse, error) {
	var response SessionAgentStatusResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/runtime/sessions/"+url.PathEscape(parentSessionID)+"/agents/"+url.PathEscape(agentID)+"/close", nil, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) ResumeSessionAgent(ctx context.Context, parentSessionID, agentID string) (*SessionAgentStatusResponse, error) {
	var response SessionAgentStatusResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/runtime/sessions/"+url.PathEscape(parentSessionID)+"/agents/"+url.PathEscape(agentID)+"/resume", nil, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) GetSessionStats(ctx context.Context, userID string) (*SessionStatsResponse, error) {
	query := url.Values{}
	if userID != "" {
		query.Set("user_id", userID)
	}

	var response SessionStatsResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime/sessions/stats", query, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) SearchSessions(ctx context.Context, req SearchSessionsRequest) (*SearchSessionsResponse, error) {
	var response SearchSessionsResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/runtime/sessions/search", nil, req, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) ArchiveSession(ctx context.Context, sessionID string) (*SessionStateChangeResponse, error) {
	return c.changeSessionState(ctx, sessionID, "archive")
}

func (c *Client) ActivateSession(ctx context.Context, sessionID string) (*SessionStateChangeResponse, error) {
	return c.changeSessionState(ctx, sessionID, "activate")
}

func (c *Client) CloseSession(ctx context.Context, sessionID string) (*SessionStateChangeResponse, error) {
	return c.changeSessionState(ctx, sessionID, "close")
}

func (c *Client) BatchDeleteSessions(ctx context.Context, sessionIDs []string) (*BatchSessionActionResponse, error) {
	return c.batchSessionAction(ctx, "delete", sessionIDs)
}

func (c *Client) BatchArchiveSessions(ctx context.Context, sessionIDs []string) (*BatchSessionActionResponse, error) {
	return c.batchSessionAction(ctx, "archive", sessionIDs)
}

func (c *Client) changeSessionState(ctx context.Context, sessionID, action string) (*SessionStateChangeResponse, error) {
	var response SessionStateChangeResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/runtime/sessions/"+url.PathEscape(sessionID)+"/"+action, nil, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) batchSessionAction(ctx context.Context, action string, sessionIDs []string) (*BatchSessionActionResponse, error) {
	var response BatchSessionActionResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/runtime/sessions/batch/"+action, nil, BatchSessionActionRequest{
		SessionIDs: append([]string(nil), sessionIDs...),
	}, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) doJSON(ctx context.Context, method, endpoint string, query url.Values, body interface{}, out interface{}) error {
	req, err := c.newRequest(ctx, method, endpoint, query, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeAPIError(resp.StatusCode, respBody)
	}
	if out == nil || len(respBody) == 0 {
		return nil
	}
	return json.Unmarshal(respBody, out)
}

func (c *Client) newRequest(ctx context.Context, method, endpoint string, query url.Values, body interface{}) (*http.Request, error) {
	target := c.baseURL + endpoint
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, values := range c.headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	if c.adminToken != "" {
		req.Header.Set("X-Skills-Admin-Token", c.adminToken)
	}
	return req, nil
}

func decodeAPIError(statusCode int, body []byte) error {
	apiErr := &APIError{
		StatusCode: statusCode,
		Body:       string(body),
	}

	var payload struct {
		Error   string                 `json:"error"`
		Code    string                 `json:"code"`
		Context map[string]interface{} `json:"context"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		apiErr.Message = payload.Error
		apiErr.Code = payload.Code
		apiErr.Context = payload.Context
	}
	return apiErr
}
