package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/modelrouting"
	"github.com/wwsheng009/ai-agent-runtime/internal/providerhealth"
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
	// ForkHistory 是 fork 请求的一次性父级已完成轮次快照（P2-1'）。
	// fresh 任务恒为 nil。scheduler 在 loop.run 注入，不经子会话持久化。
	ForkHistory []types.Message
}

type ChildAgentFactory struct{}

func (f ChildAgentFactory) Build(ctx context.Context, req ChildBuildRequest) (ChildAgentSpec, error) {
	_ = ctx
	parent := req.Parent
	task := applyAgentdefTaskDefaults(req.Parent, req.Task)
	parentConfig := parent.GetConfig()

	resolver := modelrouting.Resolver{
		Config:  req.Config.Routing,
		Catalog: modelrouting.NewRuntimeCatalog(parent.llmRuntime),
		// 动态健康源：与本进程记录 LLM 成败的注册表是同一份（见 agent.loop），
		// 因此这里读到的是真实的上游表现，而不是配置里手写的标注。
		Health: providerhealth.Default(),
	}
	// Parent reasoning effort must be resolved for both routing states: the
	// disabled branch of the resolver is documented to inherit parent
	// provider/reasoning, and the child loop request reads its effort solely
	// from LoopReActConfig (loop.go builds LLMRequest.ReasoningEffort from
	// loop.config), never from agent Config.Options.
	parentReasoning := types.ResolveReasoningEffort("", parentConfig.Options)
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

	// Resolve the tool surface through the same helper the scheduler uses, so
	// the direct-child path cannot hand the child a policy whose allowlist is
	// empty or holds names the runtime no longer serves.
	requestedReadOnly := task.ReadOnly
	resolved, childPolicy, err := resolveChildToolSurface(parent, task)
	if err != nil {
		return ChildAgentSpec{}, err
	}
	task = resolved
	promptTask := task
	// The prompt must describe the effective inherited boundary, not only the
	// child's requested flag. A read-only parent cannot be widened by omitting
	// read_only on a nested child request.
	if childPolicy != nil && childPolicy.ReadOnly {
		if !requestedReadOnly && task.ReadOnlySource == "" {
			task.ReadOnlySource = "parent_tool_execution_policy"
		}
		filteredTools, removedTools := filterReadOnlyTools(task.ToolsWhitelist)
		if len(removedTools) > 0 {
			task.ToolsWhitelist = filteredTools
			task.ReadOnlyFilteredTools = append(task.ReadOnlyFilteredTools, removedTools...)
			// Re-derive after filtering so the child policy and its capability
			// scope describe the same effective tool surface.
			childPolicy = agentChildPolicy(parent, task)
		}
		childConfig.Options["read_only"] = true
		source := task.ReadOnlySource
		if source == "" {
			source = SpawnSubagentsToolName + ".read_only"
		}
		childConfig.Options["read_only_source"] = source
	} else if task.ReadOnly {
		filteredTools, removedTools := filterReadOnlyTools(task.ToolsWhitelist)
		if len(removedTools) > 0 {
			task.ToolsWhitelist = filteredTools
			task.ReadOnlyFilteredTools = append(task.ReadOnlyFilteredTools, removedTools...)
			childPolicy = agentChildPolicy(parent, task)
		}
		childConfig.Options["read_only"] = true
		source := task.ReadOnlySource
		if source == "" {
			source = SpawnSubagentsToolName + ".read_only"
		}
		childConfig.Options["read_only_source"] = source
	}
	promptTask = taskWithRouteDecision(task, decision)
	childConfig.SystemPrompt = parent.GetPromptBuilder().BuildSubagentPrompt(parentConfig, promptTask)

	// Fork context (P2-1'): when the task requests context=fork (capability
	// granted at admission), the child's initial history is a one-time frozen
	// snapshot of the parent's completed-turn prefix. Constraints: dialogue
	// only (parent system messages stripped), frozen at build time (no live
	// sharing), and the whole snapshot is refused when it exceeds the token
	// ceiling — never silently truncated. Fork equivalence with fresh on a
	// first-turn parent matches the DSH reference behavior.
	forkHistory, forkErr := buildForkHistoryForTask(req.Options, task, parent.llmRuntime, req.Config.ForkMaxSnapshotTokens)
	if forkErr != nil {
		return ChildAgentSpec{}, forkErr
	}

	childAgent := NewAgentWithLLM(&childConfig, parent.mcpManager, parent.llmRuntime)
	childSchedulerConfig := childSubagentSchedulerConfig(parent.GetSubagentScheduler(), req.Config)
	childAgent.SetSubagentScheduler(NewSubagentScheduler(childAgent, childSchedulerConfig))
	if childPolicy == nil {
		childPolicy = NewToolExecutionPolicy(task.ToolsWhitelist, task.ReadOnly)
	}
	if childSchedulerConfig.DelegationPolicy == DelegationPolicyDisabled {
		childPolicy.BlockDelegation = true
		childConfig.Options["delegation_policy"] = DelegationPolicyDisabled
	} else {
		childConfig.Options["delegation_policy"] = DelegationPolicyEnabled
	}
	childAgent.SetEventBus(parent.GetEventBus())
	childAgent.SetPromptBuilder(parent.GetPromptBuilder())
	childAgent.inheritToolHooksFrom(parent)
	childAgent.SetToolExecutionPolicy(childPolicy)

	effectiveMaxSteps := childConfig.MaxSteps
	if task.MaxTurns > 0 && (effectiveMaxSteps <= 0 || task.MaxTurns < effectiveMaxSteps) {
		effectiveMaxSteps = task.MaxTurns
	}
	loopConfig := &LoopReActConfig{
		MaxSteps:              effectiveMaxSteps,
		MaxToolCalls:          childConfig.MaxToolCalls,
		MaxRunDuration:        childConfig.MaxRunDuration,
		MaxExplorationSteps:   childConfig.MaxExplorationSteps,
		MaxRepeatedToolCalls:  childConfig.MaxRepeatedToolCalls,
		MaxRepeatedPollCalls:  childConfig.MaxRepeatedPollCalls,
		EnableThought:         true,
		EnableToolCalls:       true,
		Temperature:           childConfig.Temperature,
		ReasoningEffort:       decision.ReasoningEffort,
		CompletionRequirement: NormalizeCompletionRequirement(task.CompletionRequirement),
	}

	return ChildAgentSpec{
		Agent:       childAgent,
		Runtime:     parent.llmRuntime,
		LoopConfig:  loopConfig,
		SessionID:   buildSubagentSessionID(task.ID),
		Config:      childConfig,
		Task:        task,
		Decision:    decision,
		ForkHistory: forkHistory,
	}, nil
}

// buildForkHistoryForTask computes the fork snapshot for a task requesting
// context=fork; fresh tasks return nil. The snapshot source is the host-side
// parent history (SubagentRunOptions.ParentHistory) — the agent object itself
// holds no conversation history, so a fork request without a host-provided
// history is an actionable error, never a silent fresh fallback. Oversize
// snapshots fail the child build the same way (G5: refuse, don't truncate);
// maxSnapshotTokens 0 selects the runtime default.
func buildForkHistoryForTask(options SubagentRunOptions, task SubagentTask, runtime *llm.LLMRuntime, maxSnapshotTokens int) ([]types.Message, error) {
	if strings.TrimSpace(task.Context) != SubagentContextFork {
		return nil, nil
	}
	if len(options.ParentHistory) == 0 {
		return nil, fmt.Errorf(
			"task %s requests context=fork but the host did not provide a parent history snapshot; next_action=retry_without_context_field_or_wire_parent_history_in_the_spawn_path",
			task.ID,
		)
	}
	return BuildForkHistory(options.ParentHistory, forkSnapshotRuntimeTokens(runtime), maxSnapshotTokens)
}

func childSubagentSchedulerConfig(parentScheduler *SubagentScheduler, config SubagentSchedulerConfig) SubagentSchedulerConfig {
	if parentScheduler == nil || !parentScheduler.NestedDelegationOptIn() {
		config.DelegationPolicy = DelegationPolicyDisabled
	}
	// P1-4/H12: a depth-N fan-out must stay inside the same process-wide
	// budget as its parent — without inheritance every nested scheduler would
	// silently get its own ceiling again. The parent's limiter wins unless the
	// caller already supplied one explicitly.
	if parentScheduler != nil && config.GlobalLimiter == nil {
		config.GlobalLimiter = parentScheduler.config.GlobalLimiter
	}
	// The writer fence must span the whole process for the same reason: a
	// nested scheduler that built its own arbiter would silently reopen the
	// cross-batch writer race the arbiter exists to close.
	if parentScheduler != nil && config.WriterArbiter == nil {
		config.WriterArbiter = parentScheduler.config.WriterArbiter
	}
	return config
}

func subagentTaskRouteHint(task SubagentTask) modelrouting.TaskHint {
	timeout := time.Duration(0)
	if task.TimeoutSec > 0 {
		timeout = time.Duration(task.TimeoutSec) * time.Second
	}
	return modelrouting.TaskHint{
		ID:                  task.ID,
		Role:                task.Role,
		TaskType:            task.TaskType,
		TaskSubject:         task.TaskSubject,
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
	if decision.TaskType != "" {
		task.TaskType = decision.TaskType
	}
	task.TaskSubject = decision.TaskSubject
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
		"task_type":              decision.TaskType,
		"task_subject":           decision.TaskSubject,
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
