package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/capability"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
	"github.com/wwsheng009/ai-agent-runtime/internal/workspace"
	"github.com/google/uuid"
)

// OrchestrationMode 统一编排模式
type OrchestrationMode string

const (
	OrchestrationRoutePreferred   OrchestrationMode = "route_preferred"
	OrchestrationAgentOnly        OrchestrationMode = "agent_only"
	OrchestrationLLMOnly          OrchestrationMode = "llm_only"
	OrchestrationPlannerPreferred OrchestrationMode = "planner_preferred"
)

const (
	PatchDecisionPolicyStrict = "strict"
	PatchDecisionPolicyWarn   = "warn"
)

type PatchApproval struct {
	Approved bool   `json:"approved,omitempty" yaml:"approved,omitempty"`
	TicketID string `json:"ticket_id,omitempty" yaml:"ticket_id,omitempty"`
	Approver string `json:"approver,omitempty" yaml:"approver,omitempty"`
	Reason   string `json:"reason,omitempty" yaml:"reason,omitempty"`
}

// OrchestrationRequest 统一编排请求
type OrchestrationRequest struct {
	Prompt                     string
	History                    []types.Message
	Mode                       OrchestrationMode
	Provider                   string
	Model                      string
	ReasoningEffort            string
	Thinking                   *types.ThinkingConfig
	MaxTokens                  int
	Temperature                float64
	Context                    map[string]interface{}
	Workspace                  *workspace.WorkspaceContext
	ExecutePlannedSubagents    bool
	AllowWritePlannedSubagents bool
	PatchDecisionPolicy        string
	ApproveBlockedPatches      bool
	PatchApprovalNote          string
	PatchApproval              *PatchApproval
}

// OrchestrationResult 统一编排结果
type OrchestrationResult struct {
	Mode                           OrchestrationMode       `json:"mode"`
	Source                         string                  `json:"source"`
	RouteAttempted                 bool                    `json:"route_attempted"`
	RouteCandidates                []*skill.RouteResult    `json:"route_candidates,omitempty"`
	CapabilityCandidates           []*capability.Candidate `json:"capability_candidates,omitempty"`
	Capability                     *capability.Descriptor  `json:"capability,omitempty"`
	Plan                           *Plan                   `json:"plan,omitempty"`
	SubagentTasks                  []SubagentTask          `json:"subagent_tasks,omitempty"`
	PlanningAttempted              bool                    `json:"planning_attempted"`
	PlanningSource                 string                  `json:"planning_source,omitempty"`
	PlanningError                  string                  `json:"planning_error,omitempty"`
	SubagentExecutionRequested     bool                    `json:"subagent_execution_requested,omitempty"`
	SubagentExecutionEligible      bool                    `json:"subagent_execution_eligible,omitempty"`
	SubagentExecutionBlockedReason string                  `json:"subagent_execution_blocked_reason,omitempty"`
	SubagentExecutionAttempted     bool                    `json:"subagent_execution_attempted,omitempty"`
	SubagentExecutionError         string                  `json:"subagent_execution_error,omitempty"`
	PatchDecision                  string                  `json:"patch_decision,omitempty"`
	PatchDecisionReason            string                  `json:"patch_decision_reason,omitempty"`
	PatchDecisionRequired          bool                    `json:"patch_decision_required,omitempty"`
	PatchDecisionPolicy            string                  `json:"patch_decision_policy,omitempty"`
	PatchDecisionOverrideApplied   bool                    `json:"patch_decision_override_applied,omitempty"`
	PatchApproval                  *PatchApproval          `json:"patch_approval,omitempty"`
	SubagentResults                []SubagentResult        `json:"subagent_results,omitempty"`
	AgentResult                    *Result                 `json:"agent_result,omitempty"`
	LLMResponse                    *llm.LLMResponse        `json:"llm_response,omitempty"`
	FallbackReason                 string                  `json:"fallback_reason,omitempty"`
}

func normalizeOrchestrationMode(mode OrchestrationMode) OrchestrationMode {
	switch mode {
	case OrchestrationAgentOnly, OrchestrationLLMOnly, OrchestrationPlannerPreferred:
		return mode
	default:
		return OrchestrationRoutePreferred
	}
}

// CapabilityDescriptor 返回 Agent 自身的能力描述
func (a *Agent) CapabilityDescriptor() *capability.Descriptor {
	if a == nil || a.config == nil {
		return nil
	}

	return &capability.Descriptor{
		ID:           a.config.Name,
		Name:         a.config.Name,
		Kind:         capability.KindAgent,
		Description:  "Unified skill orchestration entry point",
		Capabilities: []string{"route", "execute", "orchestrate"},
		Metadata: map[string]interface{}{
			"provider":           a.config.Provider,
			"model":              a.config.Model,
			"max_steps":          a.config.MaxSteps,
			"default_max_tokens": a.config.DefaultMaxTokens,
			"enable_memory":      a.config.EnableMemory,
			"enable_planning":    a.config.EnablePlanning,
			"enable_reflection":  a.config.EnableSelfReflect,
		},
	}
}

// CapabilityDescriptors 返回 Agent 与已注册 Skill 的统一能力描述
func (a *Agent) CapabilityDescriptors() []*capability.Descriptor {
	descriptors := make([]*capability.Descriptor, 0, 1)
	if descriptor := a.CapabilityDescriptor(); descriptor != nil {
		descriptors = append(descriptors, descriptor)
	}
	if a == nil || a.skillRouter == nil || a.skillRouter.Registry() == nil {
		return descriptors
	}
	return append(descriptors, a.skillRouter.Registry().CapabilityDescriptors()...)
}

// ResolveRoutes 返回统一路由候选
func (a *Agent) ResolveRoutes(ctx context.Context, prompt string) []*skill.RouteResult {
	if a == nil || a.skillRouter == nil {
		return nil
	}
	return a.skillRouter.Route(ctx, prompt)
}

// ResolveCapabilityCandidates 返回统一能力候选
func (a *Agent) ResolveCapabilityCandidates(ctx context.Context, prompt string) []*capability.Candidate {
	return skill.RouteResultsToCapabilityCandidates(a.ResolveRoutes(ctx, prompt))
}

// Orchestrate 统一编排入口
func (a *Agent) Orchestrate(ctx context.Context, req *OrchestrationRequest) (*OrchestrationResult, error) {
	if a == nil {
		return nil, fmt.Errorf("agent is nil")
	}
	if req == nil {
		return nil, fmt.Errorf("orchestration request is required")
	}
	if req.Prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}

	mode := normalizeOrchestrationMode(req.Mode)
	result := &OrchestrationResult{Mode: mode}

	switch mode {
	case OrchestrationLLMOnly:
		llmResp, err := a.callLLM(ctx, req)
		if err != nil {
			return nil, err
		}
		result.Source = "llm_direct"
		result.LLMResponse = llmResp
		return result, nil

	case OrchestrationAgentOnly:
		agentResult, err := a.RunWithHistory(ctx, req.Prompt, req.History)
		if err != nil {
			return nil, err
		}
		result.Source = "agent_direct"
		result.AgentResult = agentResult
		result.Capability = selectedCapability(agentResult)
		return result, nil

	default:
		result.RouteAttempted = true
		result.RouteCandidates = a.ResolveRoutes(ctx, req.Prompt)
		result.CapabilityCandidates = skill.RouteResultsToCapabilityCandidates(result.RouteCandidates)
		if mode == OrchestrationPlannerPreferred {
			result.Plan, result.PlanningSource, result.PlanningError = a.buildPreferredPlan(ctx, req, result.RouteCandidates)
			result.SubagentTasks = BuildSubagentTasksFromPlan(result.Plan)
			result.PlanningAttempted = true
			if req.ExecutePlannedSubagents {
				result.SubagentExecutionRequested = true
				if err := ValidatePlannedSubagentExecution(result.SubagentTasks, a.GetToolExecutionPolicy(), req.AllowWritePlannedSubagents); err != nil {
					result.SubagentExecutionBlockedReason = err.Error()
				} else if len(result.SubagentTasks) > 0 {
					result.SubagentExecutionEligible = true
					result.SubagentExecutionAttempted = true
					agentResult, subagentResults, execErr := a.executePlannedSubagents(ctx, req, result.SubagentTasks)
					result.Source = "agent_planned_subagents"
					result.SubagentResults = subagentResults
					result.AgentResult = agentResult
					result.Capability = a.CapabilityDescriptor()
					result.PatchDecision, result.PatchDecisionReason, result.PatchDecisionRequired = evaluatePatchDecision(subagentResults)
					result.PatchDecisionPolicy = normalizePatchDecisionPolicy(req.PatchDecisionPolicy)
					applyPatchDecisionPolicy(result, req)
					a.emitPatchDecisionEvent(req, result)
					if execErr != nil {
						result.SubagentExecutionError = execErr.Error()
						if result.AgentResult == nil {
							result.AgentResult = plannedSubagentFailureResult(a, execErr, subagentResults)
						}
						return result, nil
					}
					return result, nil
				} else {
					result.SubagentExecutionBlockedReason = "no planned subagents available for execution"
				}
			}
		}

		agentResult, err := a.runWithPreparedRoutes(ctx, a.buildRequest(req.Prompt, req.History, false, req.Context, req.ReasoningEffort, req.Thinking), result.RouteCandidates)
		if err != nil {
			return nil, err
		}

		if shouldReturnAgentRoute(agentResult, a.llmRuntime) {
			result.Source = "agent_route"
			result.AgentResult = agentResult
			result.Capability = selectedCapability(agentResult)
			return result, nil
		}

		if a.llmRuntime == nil {
			result.Source = "agent_route"
			result.AgentResult = agentResult
			result.Capability = selectedCapability(agentResult)
			return result, nil
		}

		llmResp, err := a.callLLM(ctx, req)
		if err != nil {
			return nil, err
		}
		result.Source = "llm_fallback"
		result.LLMResponse = llmResp
		result.FallbackReason = "no_matching_skill"
		return result, nil
	}
}

func (a *Agent) executePlannedSubagents(ctx context.Context, req *OrchestrationRequest, tasks []SubagentTask) (*Result, []SubagentResult, error) {
	if a == nil {
		return nil, nil, fmt.Errorf("agent is nil")
	}
	if a.llmRuntime == nil {
		return nil, nil, fmt.Errorf("llm runtime is not configured")
	}
	scheduler := a.GetSubagentScheduler()
	if scheduler == nil {
		return nil, nil, fmt.Errorf("subagent scheduler is not configured")
	}

	traceID := "trace_" + uuid.NewString()
	parentSessionID := ""
	if req != nil && req.Context != nil {
		if value, ok := req.Context["session_id"].(string); ok {
			parentSessionID = value
		}
	}

	reports, err := scheduler.RunChildren(ctx, SubagentRunOptions{
		TraceID:         traceID,
		ParentSessionID: parentSessionID,
		Depth:           1,
	}, tasks)
	if err != nil {
		return nil, reports, err
	}

	output := renderSubagentResults(reports)
	observation := types.NewObservation("planned_subagents", "spawn_subagents").
		WithInput(map[string]interface{}{
			"planned_subagent_tasks": tasks,
		}).
		WithOutput(output).
		WithMetric("subagent_count", len(reports)).
		WithMetric("subagent_reports", reports)

	success := true
	errorsList := make([]string, 0)
	for _, report := range reports {
		if !report.Success {
			success = false
			if report.Error != "" {
				errorsList = append(errorsList, report.Error)
			} else if report.Summary != "" {
				errorsList = append(errorsList, report.Summary)
			}
		}
	}
	if success {
		observation.MarkSuccess()
	} else {
		observation.MarkFailure(strings.Join(errorsList, "; "))
	}

	return &Result{
		Success:      success,
		Output:       output,
		Steps:        1,
		Observations: []types.Observation{*observation},
		TraceID:      traceID,
		State:        a.GetState(),
		Usage:        aggregateSubagentUsage(reports),
		Error:        strings.Join(errorsList, "; "),
	}, reports, nil
}

func aggregateSubagentUsage(results []SubagentResult) *types.TokenUsage {
	if len(results) == 0 {
		return nil
	}
	total := &types.TokenUsage{}
	hasUsage := false
	for _, result := range results {
		if result.Usage == nil {
			continue
		}
		total.Add(result.Usage)
		hasUsage = true
	}
	if !hasUsage {
		return nil
	}
	return total
}

func evaluatePatchDecision(results []SubagentResult) (string, string, bool) {
	totalPatches := 0
	for _, report := range results {
		for _, patch := range report.Patches {
			totalPatches++
			if !patchIsApplied(patch) {
				target := strings.TrimSpace(patch.Path)
				if target == "" {
					target = "unknown_patch"
				}
				return "blocked", fmt.Sprintf("writer patch %s is not confirmed applied", target), true
			}
			if strings.TrimSpace(patch.VerificationStatus) != "verified" {
				target := strings.TrimSpace(patch.Path)
				if target == "" {
					target = "unknown_patch"
				}
				return "blocked", fmt.Sprintf("writer patch %s requires manual review", target), true
			}
		}
	}
	if totalPatches == 0 {
		return "not_required", "", false
	}
	return "approved", "all writer patches verified", true
}

func normalizePatchDecisionPolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case PatchDecisionPolicyWarn:
		return PatchDecisionPolicyWarn
	default:
		return PatchDecisionPolicyStrict
	}
}

func applyPatchDecisionPolicy(result *OrchestrationResult, req *OrchestrationRequest) {
	if result == nil || !result.PatchDecisionRequired || result.PatchDecision != "blocked" {
		return
	}

	approval := patchApprovalFromRequest(req)
	manualOverride := approval != nil && approval.Approved
	if manualOverride {
		originalReason := strings.TrimSpace(result.PatchDecisionReason)
		approvalNote := strings.TrimSpace(approval.Reason)
		if approvalNote != "" {
			result.PatchDecisionReason = fmt.Sprintf("manual override approved: %s", approvalNote)
		} else {
			result.PatchDecisionReason = "manual override approved"
		}
		if originalReason != "" {
			result.PatchDecisionReason += " (original: " + originalReason + ")"
		}
		result.PatchDecision = "approved_override"
		result.PatchDecisionOverrideApplied = true
		result.PatchApproval = approval
		result.SubagentExecutionBlockedReason = ""
		if strings.TrimSpace(result.SubagentExecutionError) == originalReason {
			result.SubagentExecutionError = ""
		}
		attachPatchDecisionNote(result.AgentResult, result.PatchDecision, result.PatchDecisionReason)
		forceAgentResultSuccess(result.AgentResult)
		return
	}

	if result.PatchDecisionPolicy == PatchDecisionPolicyWarn {
		attachPatchDecisionNote(result.AgentResult, result.PatchDecision, result.PatchDecisionReason)
		forceAgentResultSuccess(result.AgentResult)
		return
	}

	if result.SubagentExecutionBlockedReason == "" {
		result.SubagentExecutionBlockedReason = result.PatchDecisionReason
	}
	if result.SubagentExecutionError == "" {
		result.SubagentExecutionError = result.PatchDecisionReason
	}
	if result.AgentResult != nil {
		result.AgentResult.Success = false
		if strings.TrimSpace(result.AgentResult.Error) == "" {
			result.AgentResult.Error = result.PatchDecisionReason
		} else if !strings.Contains(result.AgentResult.Error, result.PatchDecisionReason) {
			result.AgentResult.Error += "; " + result.PatchDecisionReason
		}
	}
	attachPatchDecisionNote(result.AgentResult, result.PatchDecision, result.PatchDecisionReason)
}

func patchApprovalFromRequest(req *OrchestrationRequest) *PatchApproval {
	if req == nil {
		return nil
	}
	if req.PatchApproval != nil {
		approval := *req.PatchApproval
		approval.TicketID = strings.TrimSpace(approval.TicketID)
		approval.Approver = strings.TrimSpace(approval.Approver)
		approval.Reason = strings.TrimSpace(approval.Reason)
		return &approval
	}
	if !req.ApproveBlockedPatches && strings.TrimSpace(req.PatchApprovalNote) == "" {
		return nil
	}
	return &PatchApproval{
		Approved: req.ApproveBlockedPatches,
		Reason:   strings.TrimSpace(req.PatchApprovalNote),
	}
}

func attachPatchDecisionNote(result *Result, decision, reason string) {
	if result == nil {
		return
	}
	line := "Patch decision: " + strings.TrimSpace(decision)
	if strings.TrimSpace(reason) != "" {
		line += ". " + strings.TrimSpace(reason)
	}
	if strings.TrimSpace(result.Output) == "" {
		result.Output = line
		return
	}
	if !strings.Contains(result.Output, "Patch decision:") {
		result.Output += "\n" + line
	}
}

func forceAgentResultSuccess(result *Result) {
	if result == nil {
		return
	}
	result.Success = true
	result.Error = ""
}

func (a *Agent) emitPatchDecisionEvent(req *OrchestrationRequest, result *OrchestrationResult) {
	if a == nil || result == nil || !result.PatchDecisionRequired {
		return
	}

	traceID := ""
	if result.AgentResult != nil {
		traceID = strings.TrimSpace(result.AgentResult.TraceID)
	}
	sessionID := ""
	if req != nil && req.Context != nil {
		if value, ok := req.Context["session_id"].(string); ok {
			sessionID = strings.TrimSpace(value)
		}
	}
	patchCount, verifiedCount, needsReviewCount, unverifiedCount := patchDecisionCounts(result.SubagentResults)
	appliedCount := patchAppliedCount(result.SubagentResults)
	payload := map[string]interface{}{
		"trace_id":                          traceID,
		"patch_decision":                    result.PatchDecision,
		"patch_decision_reason":             result.PatchDecisionReason,
		"patch_decision_required":           result.PatchDecisionRequired,
		"patch_decision_policy":             result.PatchDecisionPolicy,
		"patch_decision_override":           result.PatchDecisionOverrideApplied,
		"subagent_count":                    len(result.SubagentResults),
		"patch_count":                       patchCount,
		"applied_patch_count":               appliedCount,
		"verified_patch_count":              verifiedCount,
		"needs_review_patch_count":          needsReviewCount,
		"unverified_patch_count":            unverifiedCount,
		"subagent_execution_attempted":      result.SubagentExecutionAttempted,
		"subagent_execution_blocked":        strings.TrimSpace(result.SubagentExecutionBlockedReason) != "",
		"subagent_execution_blocked_reason": result.SubagentExecutionBlockedReason,
	}
	if result.PatchApproval != nil {
		payload["patch_approval"] = map[string]interface{}{
			"approved":  result.PatchApproval.Approved,
			"ticket_id": result.PatchApproval.TicketID,
			"approver":  result.PatchApproval.Approver,
			"reason":    result.PatchApproval.Reason,
		}
	}
	a.emitRuntimeEvent("patch.decision", sessionID, "spawn_subagents", payload)
}

func patchDecisionCounts(results []SubagentResult) (int, int, int, int) {
	total := 0
	verified := 0
	needsReview := 0
	unverified := 0
	for _, report := range results {
		for _, patch := range report.Patches {
			total++
			switch strings.TrimSpace(patch.VerificationStatus) {
			case "verified":
				verified++
			case "needs_review":
				needsReview++
			default:
				unverified++
			}
		}
	}
	return total, verified, needsReview, unverified
}

func patchAppliedCount(results []SubagentResult) int {
	total := 0
	for _, report := range results {
		for _, patch := range report.Patches {
			if patchIsApplied(patch) {
				total++
			}
		}
	}
	return total
}

func plannedSubagentFailureResult(agent *Agent, execErr error, results []SubagentResult) *Result {
	output := ""
	if len(results) > 0 {
		output = renderSubagentResults(results)
	} else if execErr != nil {
		output = execErr.Error()
	}
	observations := make([]types.Observation, 0, 1)
	if len(results) > 0 {
		observation := types.NewObservation("planned_subagents", "spawn_subagents").
			WithOutput(output).
			WithMetric("subagent_count", len(results)).
			WithMetric("subagent_reports", results)
		if execErr != nil {
			observation.MarkFailure(execErr.Error())
		} else {
			observation.MarkSuccess()
		}
		observations = append(observations, *observation)
	}
	errorText := ""
	if execErr != nil {
		errorText = execErr.Error()
	}
	state := AgentState{}
	if agent != nil {
		state = agent.GetState()
	}
	return &Result{
		Success:      false,
		Output:       output,
		Steps:        1,
		Observations: observations,
		State:        state,
		Usage:        aggregateSubagentUsage(results),
		Error:        errorText,
	}
}

func (a *Agent) buildPreferredPlan(ctx context.Context, req *OrchestrationRequest, routes []*skill.RouteResult) (*Plan, string, string) {
	if a == nil || a.planner == nil {
		return nil, "", "planner_not_configured"
	}
	if req != nil {
		prevProvider := a.planner.provider
		prevModel := a.planner.model
		prevReasoningEffort := a.planner.reasoningEffort
		prevThinking := types.CloneThinkingConfig(a.planner.thinking)
		if strings.TrimSpace(req.Provider) != "" {
			a.planner.provider = strings.TrimSpace(req.Provider)
		}
		if strings.TrimSpace(req.Model) != "" {
			a.planner.model = strings.TrimSpace(req.Model)
		}
		a.planner.reasoningEffort = strings.TrimSpace(req.ReasoningEffort)
		a.planner.thinking = types.CloneThinkingConfig(req.Thinking)
		defer func() {
			a.planner.provider = prevProvider
			a.planner.model = prevModel
			a.planner.reasoningEffort = prevReasoningEffort
			a.planner.thinking = prevThinking
		}()
	}

	if len(routes) > 0 && routes[0] != nil && routes[0].Skill != nil && routes[0].Skill.Workflow != nil {
		plan, err := a.planner.CreatePlanFromWorkflow(routes[0].Skill.Workflow)
		if err != nil {
			return nil, "workflow", err.Error()
		}
		if req != nil && req.Prompt != "" {
			plan.Goal = req.Prompt
		}
		return plan, "workflow", ""
	}

	if a.mcpManager != nil {
		tools := a.mcpManager.ListTools()
		if len(tools) > 0 {
			plan, err := a.planner.CreatePlan(ctx, req.Prompt, tools)
			if err != nil {
				return nil, "tool_catalog", err.Error()
			}
			return plan, "tool_catalog", ""
		}
	}

	return nil, "", "no_plannable_route_or_tool"
}

// PreviewPlan 生成计划预览，不执行 LLM fallback
func (a *Agent) PreviewPlan(ctx context.Context, req *OrchestrationRequest, routes []*skill.RouteResult) (*Plan, string, string) {
	if a == nil || req == nil {
		return nil, "", "planner_not_configured"
	}
	return a.buildPreferredPlan(ctx, req, routes)
}

func shouldReturnAgentRoute(result *Result, runtime *llm.LLMRuntime) bool {
	if result == nil {
		return false
	}
	if runtime == nil {
		return true
	}
	return result.Skill != "" || result.Success || result.Output != "No matching skill found for the request"
}

func selectedCapability(result *Result) *capability.Descriptor {
	if result == nil || result.Skill == "" {
		return nil
	}
	return &capability.Descriptor{
		ID:   result.Skill,
		Name: result.Skill,
		Kind: capability.KindSkill,
	}
}

func (a *Agent) callLLM(ctx context.Context, req *OrchestrationRequest) (*llm.LLMResponse, error) {
	if a.llmRuntime == nil {
		return nil, fmt.Errorf("llm runtime is not configured")
	}

	provider := req.Provider
	if provider == "" {
		provider = a.config.Provider
	}
	model := req.Model
	if model == "" {
		model = a.config.Model
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = a.config.DefaultMaxTokens
	}
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	temperature := req.Temperature
	if temperature == 0 && a.config.Temperature != 0 {
		temperature = a.config.Temperature
	}

	messages := make([]types.Message, 0, len(req.History)+2)
	if a.config.SystemPrompt != "" {
		messages = append(messages, *types.NewSystemMessage(a.config.SystemPrompt))
	}
	if req.Workspace != nil && req.Workspace.Summary != "" {
		messages = append(messages, *types.NewSystemMessage("Workspace context: " + req.Workspace.Summary))
	}
	messages = append(messages, req.History...)
	messages = append(messages, *types.NewUserMessage(req.Prompt))

	return a.llmRuntime.Call(ctx, &llm.LLMRequest{
		Provider:        provider,
		Model:           model,
		Messages:        messages,
		MaxTokens:       maxTokens,
		Temperature:     temperature,
		ReasoningEffort: req.ReasoningEffort,
		Thinking:        types.CloneThinkingConfig(req.Thinking),
	})
}
