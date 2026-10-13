package runtimeapi

import (
	"context"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimehost"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// buildSessionAgentForContext 组装会话执行所需的 agent 与选中 runtime 配置。
//
// E3.1b-2b 接缝：把 buildSessionActor 的「profile 解析结果 → agent.Config →
// apiAgent → 工具策略 → hooks/runtime services」装配抽成独立方法，便于后续以
// runtimehost.Deps 回调（公开类型）暴露给执行核。
//
// **行为零变化**：方法体与抽取前的内联块逐条一致；profileState 由调用方传入
// （避免二次解析）。
func (h *Handler) buildSessionAgentForContext(
	sessionID string,
	sessionCtx runtimehost.SessionContext,
	profileState *profileRuntimeState,
	runtimeConfig *runtimecfg.RuntimeConfig,
) (*agent.Agent, *runtimecfg.RuntimeConfig) {
	selectedConfig := runtimeConfig
	if profileState != nil && profileState.RuntimeConfig != nil {
		selectedConfig = profileState.RuntimeConfig
	}

	workspacePath := sessionCtx.WorkspacePath
	childAgentType := sessionCtx.ChildAgentType
	childDepth := sessionCtx.ChildDepth
	childReadOnly := sessionCtx.ChildReadOnly
	disableTools := sessionCtx.DisableTools
	streamRequested := sessionCtx.StreamRequested
	requestedReasoningEffort := sessionCtx.RequestedReasoningEffort

	agentProvider := resolveAgentProvider(profileState, selectedConfig, h.llmRuntime)
	if strings.TrimSpace(sessionCtx.RequestedProvider) != "" {
		agentProvider = strings.TrimSpace(sessionCtx.RequestedProvider)
	}
	agentModel := resolveAgentModel(profileState, selectedConfig, h.llmRuntime)
	if strings.TrimSpace(sessionCtx.RequestedModel) != "" {
		agentModel = strings.TrimSpace(sessionCtx.RequestedModel)
	}
	if strings.TrimSpace(agentModel) == "" {
		agentModel = defaultAgentModel(h.llmRuntime)
	}

	agentConfig := &agent.Config{
		Name:     firstNonEmptyString(strings.TrimSpace(childAgentType), "session-actor"),
		Provider: agentProvider,
		Model:    agentModel,
	}
	instructionMessages := buildRuntimeInstructionMessages(profileState, workspacePath, agentProvider)
	if systemPrompt := primarySystemInstructionContent(instructionMessages); systemPrompt != "" {
		agentConfig.SystemPrompt = systemPrompt
	} else if profileState != nil && strings.TrimSpace(profileState.PromptText) != "" {
		agentConfig.SystemPrompt = strings.TrimSpace(profileState.PromptText)
	}
	if agentConfig.MaxSteps < 0 {
		agentConfig.MaxSteps = 0
	} else if agentConfig.MaxSteps == 0 && selectedConfig != nil {
		agentConfig.MaxSteps = agent.NormalizeMaxSteps(selectedConfig.Agent.MaxMaxSteps)
	}
	if selectedConfig != nil {
		agentConfig.MaxToolCalls = selectedConfig.Agent.MaxToolCalls
		agentConfig.MaxRunDuration = selectedConfig.Agent.Timeout
		agentConfig.MaxExplorationSteps = selectedConfig.Agent.MaxExplorationSteps
		agentConfig.MaxRepeatedToolCalls = selectedConfig.Agent.MaxRepeatedToolCalls
		agentConfig.MaxRepeatedPollCalls = selectedConfig.Agent.MaxRepeatedPollCalls
		agentConfig.Options = contextOptionsFromRuntimeConfig(selectedConfig, h.knowledgeActivation)
	}
	if streamRequested || strings.TrimSpace(requestedReasoningEffort) != "" {
		if agentConfig.Options == nil {
			agentConfig.Options = make(map[string]interface{})
		}
		if streamRequested {
			agentConfig.Options["stream"] = true
		}
		if reasoningEffort := runtimetypes.NormalizeReasoningEffort(requestedReasoningEffort); reasoningEffort != "" {
			agentConfig.Options["reasoning_effort"] = reasoningEffort
		}
	}
	if workspacePath != "" {
		if agentConfig.Options == nil {
			agentConfig.Options = make(map[string]interface{})
		}
		agentConfig.Options["workspace_path"] = workspacePath
	}
	if profilePack := buildProfileContextPack(profileState); len(profilePack) > 0 {
		if agentConfig.Options == nil {
			agentConfig.Options = make(map[string]interface{})
		}
		agentConfig.Options["profile_context"] = cloneProfileContextValues(profilePack)
	}
	if profileState != nil && len(profileState.ContextValues) > 0 {
		if agentConfig.Options == nil {
			agentConfig.Options = make(map[string]interface{})
		}
		mergeProfileContextInto(agentConfig.Options, profileState.ContextValues)
	}
	if disableTools {
		if agentConfig.Options == nil {
			agentConfig.Options = make(map[string]interface{})
		}
		agentConfig.Options[llm.MetadataKeyDisableTools] = true
	}

	// Always construct the request catalog. disable_tools is an execution/choice
	// decision and must not remove definitions from an existing cache lane.
	sessionMCPManager := h.runtimeServerToolSurfaceForSession(context.Background(), sessionID, h.sessionBaseMCPManagerByID(context.Background(), sessionID), true)
	apiAgent := h.newAPIAgentWithRuntime(agentConfig, &agentRuntimeComponents{
		registry:        h.skillRegistry,
		embeddingRouter: h.embeddingRouter,
		mcpManager:      sessionMCPManager,
		llmRuntime:      h.llmRuntime,
	})
	if disableTools {
		apiAgent.SetToolExecutionPolicy(agent.NewToolExecutionPolicy([]string{}, false))
	} else {
		h.applyAgentExecutionPolicy(apiAgent, workspacePath, selectedConfig, profileStateToolPolicy(profileState))
	}
	if strings.TrimSpace(childAgentType) != "" {
		applyAPIChildAgentdefToolPolicy(apiAgent, childAgentType, workspacePath)
	}
	if childReadOnly {
		toolPolicy := apiAgent.GetToolExecutionPolicy()
		if toolPolicy == nil {
			toolPolicy = agent.NewToolExecutionPolicy(nil, true)
		} else {
			toolPolicy = toolPolicy.Clone()
			toolPolicy.ReadOnly = true
		}
		// 与 CLI 宿主同口径：只读边界只能收窄，父策略已带能力面时取交集。
		toolPolicy.SetCapabilityScope(toolPolicy.IntersectAllowedCapabilities(runtimepolicy.ReadOnlyChildCapabilities()))
		apiAgent.SetToolExecutionPolicy(toolPolicy)
	}
	applyAPIAgentChildDepthPolicy(apiAgent, childDepth, apiAgentDepthCeiling(selectedConfig))
	h.applyAgentHooks(apiAgent, selectedConfig)
	h.applyAgentRuntimeServices(apiAgent, selectedConfig)

	return apiAgent, selectedConfig
}

// resolveProfileStateForSessionContext 解析会话 profile 状态。
// 与原内联块同口径：解析失败不报错，返回 nil（降级为无 profile 行为）。
func (h *Handler) resolveProfileStateForSessionContext(sessionCtx runtimehost.SessionContext) *profileRuntimeState {
	if strings.TrimSpace(sessionCtx.ProfileRef) == "" {
		return nil
	}
	resolved, err := h.resolveProfileSessionState(sessionCtx.ProfileRef, sessionCtx.AgentID, sessionCtx.WorkspacePath)
	if err != nil {
		return nil
	}
	return resolved
}

// buildSessionLoopConfigFor 组装会话 loop 配置（E3.1b-2b 接缝）。
//
// 覆盖义务解析器接线、主 Agent 路由、completion requirement 与知识层观察接线。
// **行为零变化**：方法体与抽取前的内联块逐条一致；profileState 由调用方传入。
func (h *Handler) buildSessionLoopConfigFor(
	sessionID string,
	sessionCtx runtimehost.SessionContext,
	selectedConfig *runtimecfg.RuntimeConfig,
	profileState *profileRuntimeState,
) *agent.LoopReActConfig {
	workspacePath := sessionCtx.WorkspacePath
	childAgentType := sessionCtx.ChildAgentType
	childDepth := sessionCtx.ChildDepth
	childReadOnly := sessionCtx.ChildReadOnly

	loopConfig := buildSessionLoopConfig(selectedConfig, sessionCtx.RequestedReasoningEffort)
	// §6.12 结算判读器：携带 agent_session: 义务的挂起记录只有在 supervision
	// 控制面报出每个子会话终态后才会被 loop.settleParkedTurnOnRunEnd 结清。
	// 未装配 supervision store 时为 nil——记录保持挂起（保守方向），批量义务
	// 的结清规则不变。
	loopConfig.AgentSessionObligations = h.agentSessionObligationResolver()
	// §6.12 扩展：spawn_team 的 team: 义务同样需要 run 末的 durable 判读器
	// （team store 终态）；未接线时 team 义务保持保守不清账。
	loopConfig.TeamObligations = h.teamObligationResolver()
	// §6.1 宿主接线：主 Agent 路由只接主会话。child 标记（agent_type / depth /
	// read_only）与上面的子会话策略同口径——带任一标记的会话是子 Agent，走
	// aicli.subagents.routing，主 Agent 的开关不得改变其行为（§6.3 配置隔离）。
	applyAPISessionMainAgentRouting(loopConfig, h.resolveMainAgentRoutingForSession(sessionCtx.RoutingOverrideRaw, workspacePath),
		strings.TrimSpace(childAgentType) == "" && childDepth == 0 && !childReadOnly)
	applyAPISessionCompletionRequirement(loopConfig, profileState, childAgentType, sessionCtx.ChildCompletionRequirement, workspacePath)
	// Phase 1 交付 4：知识层 shadow 拦截（grep/view → exploration_attribution）。
	// 未注入观察器（知识层 off / 账本不可用）时 hook 为 nil，行为与无知识层一致。
	// W6：来源门禁与子代理判定同口径（agent_type / depth / read_only）——只读
	// 子代理 / 只读会话的观察零写入；可写子代理按 task 作用域写入。
	observationSubagent := strings.TrimSpace(childAgentType) != "" || childDepth > 0 || childReadOnly
	applyAPISessionToolObservation(loopConfig, h, sessionID,
		knowledge.ObservationSourceFor(observationSubagent, childReadOnly), sessionCtx.ObservationTaskID)
	return loopConfig
}
