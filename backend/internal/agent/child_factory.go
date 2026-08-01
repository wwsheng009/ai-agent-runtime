package agent

import (
	"context"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/modelrouting"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type ChildBuildRequest struct {
	Parent  *Agent
	Task    SubagentTask
	Options SubagentRunOptions
	Config  SubagentSchedulerConfig
}

type ChildAgentSpec struct {
	Agent      *Agent
	Runtime    *llm.LLMRuntime
	LoopConfig *LoopReActConfig
	SessionID  string
	Config     Config
	Task       SubagentTask
	Decision   modelrouting.RouteDecision
}

type ChildAgentFactory struct{}

func (f ChildAgentFactory) Build(ctx context.Context, req ChildBuildRequest) (ChildAgentSpec, error) {
	_ = ctx
	parent := req.Parent
	task := req.Task
	parentConfig := parent.GetConfig()

	resolver := modelrouting.Resolver{
		Config:  req.Config.Routing,
		Catalog: modelrouting.NewRuntimeCatalog(parent.llmRuntime),
	}
	parentReasoning := ""
	if modelrouting.RoutingEnabled(req.Config.Routing) {
		parentReasoning = types.ResolveReasoningEffort("", parentConfig.Options)
	}
	decision, err := resolver.Resolve(modelrouting.ParentDefaults{
		Provider:        parentConfig.Provider,
		Model:           parentConfig.Model,
		ReasoningEffort: parentReasoning,
		MaxTokens:       parentConfig.DefaultMaxTokens,
		Temperature:     float64Ptr(parentConfig.Temperature),
	}, subagentTaskRouteHint(task))
	if err != nil {
		return ChildAgentSpec{}, err
	}

	childConfig := *parentConfig
	childConfig.Name = firstNonEmptyString(task.ID, childConfig.Name+"-subagent")
	childConfig.Provider = decision.Provider
	childConfig.Model = decision.Model
	if decision.MaxTokens > 0 {
		childConfig.DefaultMaxTokens = decision.MaxTokens
	}
	if decision.Temperature != nil {
		childConfig.Temperature = *decision.Temperature
	}
	childConfig.Options = cloneAgentOptions(parentConfig.Options)
	applyRouteOptions(childConfig.Options, decision)
	if task.ReadOnly {
		childConfig.Options["read_only"] = true
		childConfig.Options["read_only_source"] = "spawn_subagents.read_only"
	}

	if task.ToolsWhitelist == nil {
		task.ToolsWhitelist = DefaultToolsForRole(task.Role)
	}
	childPolicy := parent.GetSubagentScheduler().childPolicy(task)
	promptTask := taskWithRouteDecision(task, decision)
	// The prompt must describe the effective inherited boundary, not only the
	// child's requested flag. A read-only parent cannot be widened by omitting
	// read_only on a nested child request.
	if childPolicy != nil && childPolicy.ReadOnly {
		promptTask.ReadOnly = true
		childConfig.Options["read_only"] = true
		if !task.ReadOnly {
			if _, exists := childConfig.Options["read_only_source"]; !exists {
				childConfig.Options["read_only_source"] = "parent_tool_execution_policy"
			}
		}
	}
	childConfig.SystemPrompt = parent.GetPromptBuilder().BuildSubagentPrompt(parentConfig, promptTask)

	childAgent := NewAgentWithLLM(&childConfig, parent.mcpManager, parent.llmRuntime)
	childAgent.SetSubagentScheduler(NewSubagentScheduler(childAgent, req.Config))
	childAgent.SetEventBus(parent.GetEventBus())
	childAgent.SetPromptBuilder(parent.GetPromptBuilder())
	childAgent.inheritToolHooksFrom(parent)
	childAgent.SetToolExecutionPolicy(childPolicy)

	loopConfig := &LoopReActConfig{
		MaxSteps:              childConfig.MaxSteps,
		MaxToolCalls:          childConfig.MaxToolCalls,
		MaxRunDuration:        childConfig.MaxRunDuration,
		MaxExplorationSteps:   childConfig.MaxExplorationSteps,
		MaxRepeatedToolCalls:  childConfig.MaxRepeatedToolCalls,
		EnableThought:         true,
		EnableToolCalls:       true,
		Temperature:           childConfig.Temperature,
		ReasoningEffort:       decision.ReasoningEffort,
		CompletionRequirement: NormalizeCompletionRequirement(task.CompletionRequirement),
	}

	return ChildAgentSpec{
		Agent:      childAgent,
		Runtime:    parent.llmRuntime,
		LoopConfig: loopConfig,
		SessionID:  buildSubagentSessionID(task.ID),
		Config:     childConfig,
		Task:       task,
		Decision:   decision,
	}, nil
}

func subagentTaskRouteHint(task SubagentTask) modelrouting.TaskHint {
	timeout := time.Duration(0)
	if task.TimeoutSec > 0 {
		timeout = time.Duration(task.TimeoutSec) * time.Second
	}
	return modelrouting.TaskHint{
		ID:                  task.ID,
		Role:                task.Role,
		Goal:                task.Goal,
		Difficulty:          task.Difficulty,
		DifficultyRationale: task.DifficultyRationale,
		Provider:            task.Provider,
		Model:               task.Model,
		ReasoningEffort:     task.ReasoningEffort,
		BudgetTokens:        task.BudgetTokens,
		Timeout:             timeout,
		ReadOnly:            task.ReadOnly,
		Warnings:            append([]string(nil), task.RouteWarnings...),
	}
}

func taskWithRouteDecision(task SubagentTask, decision modelrouting.RouteDecision) SubagentTask {
	task.Difficulty = decision.Difficulty
	task.DifficultyRationale = decision.DifficultyRationale
	task.Provider = decision.Provider
	task.Model = decision.Model
	task.ReasoningEffort = decision.ReasoningEffort
	task.RoutingSource = decision.Source
	task.RouteWarnings = append([]string(nil), decision.Warnings...)
	if decision.Timeout > 0 && task.TimeoutSec <= 0 {
		task.TimeoutSec = int(decision.Timeout / time.Second)
	}
	return task
}

func cloneAgentOptions(options map[string]interface{}) map[string]interface{} {
	if len(options) == 0 {
		return map[string]interface{}{}
	}
	cloned := make(map[string]interface{}, len(options)+4)
	for key, value := range options {
		cloned[key] = cloneOptionValue(value)
	}
	return cloned
}

func applyRouteOptions(options map[string]interface{}, decision modelrouting.RouteDecision) {
	if options == nil {
		return
	}
	if decision.Difficulty != "" {
		options["difficulty"] = decision.Difficulty
	}
	if decision.Source != "" {
		options["routing_source"] = decision.Source
	}
	if decision.ReasoningEffort != "" {
		options["reasoning_effort"] = decision.ReasoningEffort
	}
}

func routeAuditPayload(decision modelrouting.RouteDecision) map[string]interface{} {
	payload := map[string]interface{}{
		"difficulty":             decision.Difficulty,
		"difficulty_source":      decision.DifficultySource,
		"difficulty_rationale":   decision.DifficultyRationale,
		"route_provider":         decision.Provider,
		"route_model":            decision.Model,
		"route_reasoning_effort": decision.ReasoningEffort,
		"route_source":           decision.Source,
	}
	if len(decision.Warnings) > 0 {
		payload["route_warnings"] = append([]string(nil), decision.Warnings...)
	}
	if decision.FallbackUsed {
		payload["fallback_used"] = true
	}
	if decision.FallbackReason != "" {
		payload["fallback_reason"] = decision.FallbackReason
	}
	return payload
}

func mergeRouteAuditPayload(base map[string]interface{}, decision modelrouting.RouteDecision) map[string]interface{} {
	if base == nil {
		base = map[string]interface{}{}
	}
	for key, value := range routeAuditPayload(decision) {
		if text, ok := value.(string); ok && text == "" {
			continue
		}
		base[key] = value
	}
	return base
}

func float64Ptr(value float64) *float64 {
	copied := value
	return &copied
}
