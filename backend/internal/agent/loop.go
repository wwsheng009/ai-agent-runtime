package agent

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	runtimecheckpoint "github.com/wwsheng009/ai-agent-runtime/internal/checkpoint"
	"github.com/wwsheng009/ai-agent-runtime/internal/compactruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/contextmgr"
	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/historyguard"
	runtimehooks "github.com/wwsheng009/ai-agent-runtime/internal/hooks"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/output"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimeprompt "github.com/wwsheng009/ai-agent-runtime/internal/prompt"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const emptyTerminalAssistantResponseError = "upstream model returned an empty reply: no text and no tool calls"
const defaultPromptPreflightAutoCompactRatio = 0.9

// LoopReActConfig ReAct 循环配置
type LoopReActConfig struct {
	MaxSteps        int                   `yaml:"maxSteps"`
	EnableThought   bool                  `yaml:"enableThought"`
	EnableToolCalls bool                  `yaml:"enableToolCalls"`
	Verbose         bool                  `yaml:"verbose"`
	Temperature     float64               `yaml:"temperature"`
	ReasoningEffort string                `yaml:"reasoningEffort"`
	Thinking        *types.ThinkingConfig `yaml:"thinking"`
	StopOnSuccess   bool                  `yaml:"stopOnSuccess"`
	MaxIterations   int                   `yaml:"maxIterations"`
}

// ReActLoop ReAct 循环（Reasoning + Acting）
type ReActLoop struct {
	agent      *Agent
	llmRuntime *llm.LLMRuntime
	config     *LoopReActConfig
}

type toolExecutionResult struct {
	Call     types.ToolCall
	Output   interface{}
	Error    string
	Envelope *output.Envelope
}

type richToolCaller interface {
	CallToolWithMeta(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, map[string]interface{}, error)
}

type toolSourceResolver interface {
	ResolveToolSource(toolName string) string
}

type loopRunOptions struct {
	TraceID        string
	SessionID      string
	History        []types.Message
	IncludePrompt  bool
	Depth          int
	BudgetTokens   int
	ToolWhitelist  []string
	PersistHistory func([]types.Message) error
}

// HistorySession 描述 ReActLoop 运行时需要的最小 session 能力。
type HistorySession interface {
	SessionID() string
	GetMessages() []types.Message
	LastMessage() *types.Message
	ReplaceHistory([]types.Message)
}

// NewReActLoop 创建 ReAct 循环
func NewReActLoop(agent *Agent, llmRuntime *llm.LLMRuntime, config *LoopReActConfig) *ReActLoop {
	if config == nil {
		config = &LoopReActConfig{
			MaxSteps:        0,
			EnableThought:   true,
			EnableToolCalls: true,
			Verbose:         false,
			Temperature:     0.7,
			StopOnSuccess:   true,
			MaxIterations:   10,
		}
	}
	config.MaxSteps = NormalizeMaxSteps(config.MaxSteps)
	if agent != nil && agent.llmRuntime == nil && llmRuntime != nil {
		agent.llmRuntime = llmRuntime
	}

	return &ReActLoop{
		agent:      agent,
		llmRuntime: llmRuntime,
		config:     config,
	}
}

// Run 执行 ReAct 循环
func (loop *ReActLoop) Run(ctx context.Context, prompt string) (*Result, error) {
	return loop.run(ctx, prompt, loopRunOptions{
		TraceID:       "trace_" + uuid.NewString(),
		IncludePrompt: true,
	})
}

// RunWithSession 使用 session 的历史作为热上下文，并在每轮后回写。
func (loop *ReActLoop) RunWithSession(ctx context.Context, prompt string, session HistorySession) (*Result, error) {
	if session == nil {
		return nil, errors.New(errors.ErrValidationFailed, "session is nil")
	}

	includePrompt := true
	if last := session.LastMessage(); last != nil && last.Role == "user" && last.Content == prompt {
		includePrompt = false
	}

	return loop.run(ctx, prompt, loopRunOptions{
		TraceID:       "trace_" + uuid.NewString(),
		SessionID:     session.SessionID(),
		History:       session.GetMessages(),
		IncludePrompt: includePrompt,
		PersistHistory: func(messages []types.Message) error {
			session.ReplaceHistory(stripSystemMessages(messages))
			return nil
		},
	})
}

// ContinueWithSession resumes execution from the existing session history without appending a new user prompt.
func (loop *ReActLoop) ContinueWithSession(ctx context.Context, session HistorySession) (*Result, error) {
	if session == nil {
		return nil, errors.New(errors.ErrValidationFailed, "session is nil")
	}

	return loop.run(ctx, "", loopRunOptions{
		TraceID:       "trace_" + uuid.NewString(),
		SessionID:     session.SessionID(),
		History:       session.GetMessages(),
		IncludePrompt: false,
		PersistHistory: func(messages []types.Message) error {
			session.ReplaceHistory(stripSystemMessages(messages))
			return nil
		},
	})
}

func (loop *ReActLoop) run(ctx context.Context, prompt string, options loopRunOptions) (*Result, error) {
	if loop.agent == nil {
		return nil, errors.New(errors.ErrValidationFailed, "agent is nil")
	}

	if loop.llmRuntime == nil {
		return nil, errors.New(errors.ErrValidationFailed, "LLM runtime is nil")
	}

	loop.agent.SetRunning(true)
	loop.agent.ClearErrors()
	loop.agent.state.CurrentStep = 0

	startTime := types.NewDuration()

	defer func() {
		loop.agent.SetRunning(false)
		startTime.StopTimer()
	}()

	result := &Result{
		State: loop.agent.GetState(),
	}
	totalUsage := &types.TokenUsage{}

	// 构建初始对话历史
	history := cloneMessageHistory(options.History)
	if options.IncludePrompt {
		history = append(history, *types.NewUserMessage(prompt))
	}
	builder := NewMessageBuilder(history)
	loop.configureBuilderActiveTurnCompaction(builder, options.BudgetTokens)

	// 添加系统提示词
	if loop.agent.config.SystemPrompt != "" && !hasLeadingSystemPrompt(history, loop.agent.config.SystemPrompt) {
		systemMsg := types.NewSystemMessage(loop.agent.config.SystemPrompt)
		builder = NewMessageBuilder(append([]types.Message{*systemMsg}, history...))
		loop.configureBuilderActiveTurnCompaction(builder, options.BudgetTokens)
	}

	var observations []types.Observation
	hadToolFailure := false
	failureMessages := make([]string, 0, 4)
	sessionID := options.SessionID
	if sessionID == "" {
		sessionID = "react_" + uuid.NewString()
	}
	traceID := options.TraceID
	if traceID == "" {
		traceID = "trace_" + uuid.NewString()
	}
	result.TraceID = traceID
	if err := persistBuilderHistory(builder, options.PersistHistory); err != nil {
		return nil, err
	}
	remainingBudget := options.BudgetTokens
	currentCtx := ensureTurnToolSurfaceSnapshot(ctx)
	sessionCompactionRecoveryStep := 0

	// ReAct 循环：Think - Act - Observe
	for step := 1; !stepExceedsLimit(loop.config.MaxSteps, step); step++ {
		loop.agent.state.CurrentStep = step
		if options.BudgetTokens > 0 && remainingBudget <= 0 {
			result.Success = false
			result.Error = fmt.Sprintf("token budget exceeded after %d step(s)", step-1)
			result.Usage = totalUsage.Clone()
			result.Observations = observations
			result.Steps = step - 1
			result.Duration = *startTime
			return result, nil
		}

		if loop.config.Verbose {
			fmt.Printf("[Step %d] Starting ReAct iteration\n", step)
		}

		// 1. Think: LLM 推理决定下一步行动
		thought, action, usage, err := loop.think(currentCtx, traceID, sessionID, step, prompt, builder.Messages(), observations, options.ToolWhitelist, remainingBudget)
		if err != nil {
			if preflightErr, ok := AsPromptPreflightError(err); ok && options.PersistHistory != nil {
				recoveryHistory := builder.Messages()
				if replacement := preflightErr.CloneReplacementHistory(); len(replacement) > 0 {
					recoveryHistory = replacement
				}
				if sessionCompactionRecoveryStep != step {
					recoveredHistory, recovered, recoveryErr := loop.trySessionCompactionRecovery(ctx, sessionID, traceID, step, recoveryHistory, preflightErr.Metadata())
					if recoveryErr != nil {
						loop.agent.AddError(fmt.Sprintf("session compaction recovery failed: %v", recoveryErr))
					} else if recovered && len(recoveredHistory) > 0 {
						if persistErr := options.PersistHistory(recoveredHistory); persistErr != nil {
							loop.agent.AddError(fmt.Sprintf("persist compacted history after prompt preflight failed: %v", persistErr))
							result.Error = err.Error()
							result.Usage = totalUsage.Clone()
							result.State = loop.agent.GetState()
							return result, fmt.Errorf("%w: persisted compacted history failed: %v", err, persistErr)
						}
						builder = NewMessageBuilder(recoveredHistory)
						loop.configureBuilderActiveTurnCompaction(builder, options.BudgetTokens)
						sessionCompactionRecoveryStep = step
						continue
					}
				}
				if replacement := preflightErr.CloneReplacementHistory(); len(replacement) > 0 {
					if persistErr := options.PersistHistory(replacement); persistErr != nil {
						loop.agent.AddError(fmt.Sprintf("persist compacted history after prompt preflight failed: %v", persistErr))
						result.Error = err.Error()
						result.Usage = totalUsage.Clone()
						result.State = loop.agent.GetState()
						return result, fmt.Errorf("%w: persisted compacted history failed: %v", err, persistErr)
					}
					preflightErr.ReplacementHistoryApplied = true
				}
			}
			loop.agent.AddError(fmt.Sprintf("think failed: %v", err))
			result.Error = err.Error()
			result.Usage = totalUsage.Clone()
			result.State = loop.agent.GetState()
			return result, err
		}
		totalUsage.Add(usage)
		result.Usage = totalUsage.Clone()
		if options.BudgetTokens > 0 && usage != nil {
			remainingBudget -= usage.TotalTokens
		}

		if loop.config.Verbose {
			fmt.Printf("[Step %d] Thought: %s\n", step, thought)
		}

		// 检查是否已经完成（没有工具调用）
		if len(action.ToolCalls) == 0 {
			if loop.config.Verbose {
				fmt.Printf("[Step %d] No tool calls, finishing\n", step)
			}
			if strings.TrimSpace(action.Content) == "" {
				err := fmt.Errorf(emptyTerminalAssistantResponseError)
				loop.agent.AddError(err.Error())
				result.Success = false
				result.Error = err.Error()
				result.Steps = step
				result.Observations = observations
				result.Duration = *startTime
				result.Usage = totalUsage.Clone()
				result.State = loop.agent.GetState()
				return result, err
			}
			builder.AppendAssistantAction(action.Content, nil, action.Reasoning, action.MessageMetadata)
			if err := persistBuilderHistory(builder, options.PersistHistory); err != nil {
				return nil, err
			}

			result.Success = !hadToolFailure
			result.Output = action.Content
			result.Reasoning = action.Reasoning
			result.Steps = step
			result.Observations = observations
			result.Duration = *startTime
			result.Usage = totalUsage.Clone()
			if hadToolFailure && len(failureMessages) > 0 {
				result.Error = joinFailureMessages(failureMessages)
			}

			// 记录到记忆
			if loop.agent.config.EnableMemory && len(observations) > 0 {
				for _, obs := range observations {
					loop.agent.memory.Add(obs)
				}
			}

			return result, nil
		}

		// 2. Act: 执行工具调用
		normalizedCalls := builder.AppendAssistantAction(action.Content, action.ToolCalls, action.Reasoning, action.MessageMetadata)
		historySnapshot := builder.Messages()
		toolResults, err := loop.act(currentCtx, traceID, sessionID, step, options.Depth, historySnapshot, normalizedCalls, options.ToolWhitelist)
		if err != nil {
			loop.agent.AddError(fmt.Sprintf("act failed: %v", err))
			hadToolFailure = true
			failureMessages = append(failureMessages, err.Error())

			// 记录失败观察
			obs := types.NewObservation(fmt.Sprintf("step_%d", step), "execution")
			obs.MarkFailure(err.Error())
			observations = append(observations, *obs)

			result.Error = err.Error()
			result.Observations = observations
			result.Usage = totalUsage.Clone()
			result.State = loop.agent.GetState()

			// 单次失败不立即返回，让 LLM 决定下一步
			if hasRemainingStepBudget(loop.config.MaxSteps, step) {
				result.Success = false
				continue
			}

			return result, err
		}
		for _, toolResult := range toolResults {
			if strings.TrimSpace(toolResult.Error) == "" {
				continue
			}
			hadToolFailure = true
			failureMessages = append(failureMessages, toolResult.Error)
		}

		// 3. Observe: 记录执行结果
		currentCtx = promoteTeamRunContext(currentCtx, toolResults)
		observations = loop.observe(currentCtx, toolResults, observations, step)

		// 4. 更新对话历史
		loop.configureBuilderActiveTurnCompaction(builder, remainingBudget)
		builder.AppendToolResults(normalizedCalls, toolResultsToPayloads(toolResults))
		if err := persistBuilderHistory(builder, options.PersistHistory); err != nil {
			return nil, err
		}

		if loop.config.Verbose {
			fmt.Printf("[Step %d] Completed %d tool calls\n", step, len(toolResults))
		}
	}

	result.Success = false
	result.LimitReached = true
	result.StepLimit = NormalizeMaxSteps(loop.config.MaxSteps)
	result.Output = stepLimitReachedMessage(result.StepLimit)
	result.Steps = result.StepLimit
	result.Observations = observations
	result.Duration = *startTime
	result.Usage = totalUsage.Clone()
	builder.AppendAssistantAction(result.Output, nil, nil, nil)
	if err := persistBuilderHistory(builder, options.PersistHistory); err != nil {
		return nil, err
	}
	if hadToolFailure && len(failureMessages) > 0 {
		result.Error = joinFailureMessages(append(failureMessages, result.Output))
	} else {
		result.Error = result.Output
	}
	result.State = loop.agent.GetState()

	return result, nil
}

func stepExceedsLimit(maxSteps int, step int) bool {
	maxSteps = NormalizeMaxSteps(maxSteps)
	return maxSteps > 0 && step > maxSteps
}

func hasRemainingStepBudget(maxSteps int, step int) bool {
	maxSteps = NormalizeMaxSteps(maxSteps)
	return maxSteps == 0 || step < maxSteps
}

func stepLimitReachedMessage(maxSteps int) string {
	maxSteps = NormalizeMaxSteps(maxSteps)
	if maxSteps <= 0 {
		return "当前运行未配置步数上限。"
	}
	return fmt.Sprintf("已达到 maxSteps=%d 的执行上限，当前轮次已停止。未配置、0 或负数表示不限制。", maxSteps)
}

// think 思考阶段：让 LLM 决定下一步行动
func (loop *ReActLoop) think(ctx context.Context, traceID, sessionID string, step int, goal string, history []types.Message, observations []types.Observation, toolWhitelist []string, remainingBudget int) (thought string, action *AgentAction, usage *types.TokenUsage, err error) {
	action = &AgentAction{}
	managedHistory := history
	countTokens := func(messages []types.Message) int {
		if loop == nil || loop.llmRuntime == nil {
			return 0
		}
		return loop.llmRuntime.CountMessagesTokens(messages)
	}
	manager := loop.agent.GetContextManager()
	if manager != nil {
		taskID := sessionID
		teamID := ""
		var profileContext map[string]interface{}
		if loop.agent != nil && loop.agent.config != nil && loop.agent.config.Options != nil {
			if value := optionString(loop.agent.config.Options, "task_id"); value != "" {
				taskID = value
			}
			teamID = optionString(loop.agent.config.Options, "team_id")
			profileContext = optionMap(loop.agent.config.Options, "profile_context")
		}
		built := manager.Build(ctx, contextmgr.BuildInput{
			TraceID:      traceID,
			SessionID:    sessionID,
			TaskID:       taskID,
			TeamID:       teamID,
			Profile:      profileContext,
			Goal:         goal,
			History:      history,
			Memory:       loop.agent.GetMemory(),
			Observations: observations,
			CountTokens:  countTokens,
		})
		managedHistory = built.Messages
		action.Metadata = map[string]interface{}{
			"context": built.Metadata,
		}
	}
	var preflightMetadata map[string]interface{}
	managedHistory, preflightMetadata, err = loop.enforcePromptPreflight(traceID, sessionID, step, managedHistory, remainingBudget)
	if err != nil {
		return "", nil, nil, err
	}

	// 构建请求
	req := &llm.LLMRequest{
		Provider:        loop.agent.config.Provider,
		Model:           loop.agent.config.Model,
		Messages:        managedHistory,
		MaxTokens:       resolveLoopMaxTokens(loop.agent.config.DefaultMaxTokens, remainingBudget),
		Temperature:     loop.config.Temperature,
		ReasoningEffort: loop.config.ReasoningEffort,
		Thinking:        types.CloneThinkingConfig(loop.config.Thinking),
		Metadata: map[string]interface{}{
			"trace_id":         traceID,
			"session_id":       sessionID,
			"remaining_budget": remainingBudget,
		},
	}
	if len(preflightMetadata) > 0 {
		req.Metadata["context_preflight"] = cloneInterfaceMap(preflightMetadata)
	}
	if sessionID != "" {
		req.Metadata["prompt_cache_key"] = sessionID
	}
	promptLayoutSummary := ""
	promptLayoutLength := 0
	totalMessageChars := 0
	instructionTokens := 0
	totalMessageTokens := 0
	var promptLayoutLayers []string
	var promptLayoutSources []string
	if layout := runtimeprompt.RenderInstructionMessagesLayout(managedHistory); layout != "" {
		req.Metadata["prompt_layout"] = layout
	}
	var tokenCountFunc func(string) int
	if provider, pErr := loop.llmRuntime.GetProvider(req.Provider); pErr == nil && provider != nil {
		tokenCountFunc = provider.CountTokens
	}
	layoutInfo := summarizePromptLayoutForEvent(managedHistory, tokenCountFunc)
	if layoutInfo.Summary != "" {
		promptLayoutSummary = layoutInfo.Summary
		promptLayoutLength = layoutInfo.InstructionChars
		totalMessageChars = layoutInfo.TotalChars
		instructionTokens = layoutInfo.InstructionTokens
		totalMessageTokens = layoutInfo.TotalTokens
		promptLayoutLayers = append([]string(nil), layoutInfo.Layers...)
		promptLayoutSources = append([]string(nil), layoutInfo.Sources...)
	}
	if outputDir := generatedImageOutputDirForAgentSession(loop.agent, sessionID); outputDir != "" {
		req.Metadata[llm.MetadataKeyGeneratedImageOutputDir] = outputDir
	}
	if loop.agent != nil && loop.agent.config != nil && boolValue(optionValue(loop.agent.config.Options, "stream")) {
		req.Stream = true
	}
	if !req.Stream && loop.llmRuntime != nil {
		_, _, capability, ok := llm.ResolveRuntimeModelCapability(loop.llmRuntime, req.Provider, req.Model)
		if ok && capability.NativeTools.ImageGeneration {
			hasText := false
			hasImage := false
			for _, modality := range capability.InputModalities {
				switch strings.ToLower(strings.TrimSpace(modality)) {
				case "text":
					hasText = true
				case "image":
					hasImage = true
				}
			}
			// Codex 图片生成只在流式响应里稳定暴露 image_generation_call。
			if hasText && hasImage {
				req.Stream = true
			}
		}
	}
	callCtx := ctx
	streamedReasoning := false
	if req.Stream {
		callCtx = llm.WithStreamReporter(ctx, func(chunk llm.StreamChunk) {
			switch chunk.Type {
			case llm.EventTypeText:
				if chunk.Content == "" {
					return
				}
				loop.agent.emitRuntimeEvent("assistant_delta", sessionID, "", map[string]interface{}{
					"trace_id": traceID,
					"content":  chunk.Content,
					"delta":    chunk.Content,
				})
			case llm.EventTypeReasoning:
				if chunk.Content == "" {
					return
				}
				streamedReasoning = true
				reasoning := &types.ReasoningBlock{
					Provider:   req.Provider,
					Summary:    chunk.Content,
					Format:     "stream_delta",
					Streamable: true,
					Visibility: types.ReasoningVisibilitySummary,
				}
				loop.agent.emitRuntimeEvent("assistant.reasoning", sessionID, "", map[string]interface{}{
					"trace_id":  traceID,
					"step":      step,
					"reasoning": reasoning.ToMap(),
				})
			case llm.EventTypeImage:
				if len(chunk.Metadata) == 0 {
					return
				}
				loop.agent.emitRuntimeEvent("assistant.image_progress", sessionID, "", map[string]interface{}{
					"trace_id": traceID,
					"step":     step,
					"image":    chunk.Metadata,
				})
			}
		})
	}
	callCtx = llm.WithRetryEventReporter(callCtx, loop.runtimeRetryEventReporter(traceID, sessionID, step, req))

	// 添加工具定义（如果启用了工具调用）
	if loop.config.EnableToolCalls {
		// 获取可用工具
		tools, err := loop.getAvailableTools(ctx, goal, toolWhitelist)
		if err != nil {
			return "", nil, nil, err
		}
		req.Tools = tools
	}

	// 调用 LLM
	requestPayload := map[string]interface{}{
		"trace_id":         traceID,
		"step":             step,
		"model":            req.Model,
		"provider":         req.Provider,
		"message_count":    len(req.Messages),
		"tool_count":       len(req.Tools),
		"remaining_budget": remainingBudget,
	}
	if len(preflightMetadata) > 0 {
		requestPayload["context_preflight"] = cloneInterfaceMap(preflightMetadata)
	}
	if totalMessageTokens > 0 {
		requestPayload["context_prompt_tokens"] = totalMessageTokens
	}
	if budgetValue, ok := preflightMetadata["prompt_budget"]; ok {
		if value := intValue(budgetValue); value > 0 {
			requestPayload["prompt_budget"] = value
		}
	}
	if sourceValue, ok := preflightMetadata["budget_source"]; ok {
		if value := strings.TrimSpace(stringValue(sourceValue)); value != "" {
			requestPayload["budget_source"] = value
		}
	}
	if sourceDetailValue, ok := preflightMetadata["budget_source_detail"]; ok {
		if value := strings.TrimSpace(stringValue(sourceDetailValue)); value != "" {
			requestPayload["budget_source_detail"] = value
		}
	}
	if candidates, ok := preflightMetadata["budget_candidates"].(map[string]interface{}); ok && len(candidates) > 0 {
		requestPayload["budget_candidates"] = cloneInterfaceMap(candidates)
	}
	if windowValue, ok := preflightMetadata["model_capability_max_context_tokens"]; ok {
		if value := intValue(windowValue); value > 0 {
			requestPayload["context_window_tokens"] = value
		}
	} else if windowValue, ok := preflightMetadata["provider_context_limit"]; ok {
		if value := intValue(windowValue); value > 0 {
			requestPayload["context_window_tokens"] = value
		}
	}
	if promptLayoutSummary != "" {
		requestPayload["prompt_layout_summary"] = promptLayoutSummary
	}
	if promptLayoutLength > 0 {
		requestPayload["prompt_layout_length"] = promptLayoutLength
	}
	if totalMessageChars > 0 {
		requestPayload["total_message_chars"] = totalMessageChars
	}
	if instructionTokens > 0 {
		requestPayload["instruction_tokens"] = instructionTokens
	}
	if totalMessageTokens > 0 {
		requestPayload["total_tokens"] = totalMessageTokens
	}
	if len(promptLayoutLayers) > 0 {
		requestPayload["prompt_layers"] = promptLayoutLayers
	}
	if len(promptLayoutSources) > 0 {
		requestPayload["prompt_sources"] = promptLayoutSources
	}
	if availability := summarizeToolAvailability(req.Tools); len(availability) > 0 {
		req.Metadata["tool_availability"] = cloneInterfaceMap(availability)
		requestPayload["tool_availability"] = availability
	}
	loop.agent.emitRuntimeEvent("llm.request.started", sessionID, "", requestPayload)
	response, err := loop.llmRuntime.Call(callCtx, req)
	if err != nil {
		finishedPayload := map[string]interface{}{
			"trace_id": traceID,
			"step":     step,
			"model":    req.Model,
			"provider": req.Provider,
			"success":  false,
			"error":    err.Error(),
		}
		if totalMessageTokens > 0 {
			finishedPayload["context_prompt_tokens"] = totalMessageTokens
		}
		if budgetValue, ok := preflightMetadata["prompt_budget"]; ok {
			if value := intValue(budgetValue); value > 0 {
				finishedPayload["prompt_budget"] = value
			}
		}
		if sourceValue, ok := preflightMetadata["budget_source"]; ok {
			if value := strings.TrimSpace(stringValue(sourceValue)); value != "" {
				finishedPayload["budget_source"] = value
			}
		}
		if sourceDetailValue, ok := preflightMetadata["budget_source_detail"]; ok {
			if value := strings.TrimSpace(stringValue(sourceDetailValue)); value != "" {
				finishedPayload["budget_source_detail"] = value
			}
		}
		if candidates, ok := preflightMetadata["budget_candidates"].(map[string]interface{}); ok && len(candidates) > 0 {
			finishedPayload["budget_candidates"] = cloneInterfaceMap(candidates)
		}
		if windowValue, ok := preflightMetadata["model_capability_max_context_tokens"]; ok {
			if value := intValue(windowValue); value > 0 {
				finishedPayload["context_window_tokens"] = value
			}
		} else if windowValue, ok := preflightMetadata["provider_context_limit"]; ok {
			if value := intValue(windowValue); value > 0 {
				finishedPayload["context_window_tokens"] = value
			}
		}
		loop.agent.emitRuntimeEvent("llm.request.finished", sessionID, "", finishedPayload)
		return "", nil, nil, err
	}
	finishedPayload := map[string]interface{}{
		"trace_id":        traceID,
		"step":            step,
		"model":           req.Model,
		"provider":        req.Provider,
		"success":         true,
		"tool_call_count": len(response.ToolCalls),
	}
	if totalMessageTokens > 0 {
		finishedPayload["context_prompt_tokens"] = totalMessageTokens
	}
	if budgetValue, ok := preflightMetadata["prompt_budget"]; ok {
		if value := intValue(budgetValue); value > 0 {
			finishedPayload["prompt_budget"] = value
		}
	}
	if sourceValue, ok := preflightMetadata["budget_source"]; ok {
		if value := strings.TrimSpace(stringValue(sourceValue)); value != "" {
			finishedPayload["budget_source"] = value
		}
	}
	if sourceDetailValue, ok := preflightMetadata["budget_source_detail"]; ok {
		if value := strings.TrimSpace(stringValue(sourceDetailValue)); value != "" {
			finishedPayload["budget_source_detail"] = value
		}
	}
	if candidates, ok := preflightMetadata["budget_candidates"].(map[string]interface{}); ok && len(candidates) > 0 {
		finishedPayload["budget_candidates"] = cloneInterfaceMap(candidates)
	}
	if windowValue, ok := preflightMetadata["model_capability_max_context_tokens"]; ok {
		if value := intValue(windowValue); value > 0 {
			finishedPayload["context_window_tokens"] = value
		}
	} else if windowValue, ok := preflightMetadata["provider_context_limit"]; ok {
		if value := intValue(windowValue); value > 0 {
			finishedPayload["context_window_tokens"] = value
		}
	}
	if response != nil && response.Usage != nil {
		finishedPayload["usage_prompt_tokens"] = response.Usage.PromptTokens
		finishedPayload["usage_completion_tokens"] = response.Usage.CompletionTokens
		finishedPayload["usage_total_tokens"] = response.Usage.TotalTokens
		if response.Usage.CachedTokens > 0 {
			finishedPayload["usage_cached_tokens"] = response.Usage.CachedTokens
		}
		if response.Usage.ReasoningTokens > 0 {
			finishedPayload["usage_reasoning_tokens"] = response.Usage.ReasoningTokens
		}
	}
	if response != nil && response.Metadata != nil {
		if source := strings.TrimSpace(stringValue(response.Metadata["usage_source"])); source != "" {
			finishedPayload["usage_source"] = source
		}
	}
	loop.agent.emitRuntimeEvent("llm.request.finished", sessionID, "", finishedPayload)

	// 解析响应
	action.Content = response.Content
	action.ToolCalls = response.ToolCalls
	action.Reasoning = response.ReasoningBlock
	if len(response.Metadata) > 0 {
		action.MessageMetadata = types.NewMetadata()
		for key, value := range response.Metadata {
			action.MessageMetadata[key] = value
		}
	}
	if action.Reasoning == nil && strings.TrimSpace(response.Reasoning) != "" {
		action.Reasoning = &types.ReasoningBlock{
			Provider:   req.Provider,
			Summary:    strings.TrimSpace(response.Reasoning),
			Visibility: types.ReasoningVisibilitySummary,
		}
	}
	if action.Reasoning != nil {
		if strings.TrimSpace(action.Reasoning.Provider) == "" {
			action.Reasoning.Provider = req.Provider
		}
		if !streamedReasoning {
			loop.agent.emitRuntimeEvent("assistant.reasoning", sessionID, "", map[string]interface{}{
				"trace_id":  traceID,
				"step":      step,
				"reasoning": action.Reasoning.ToMap(),
			})
		}
	}
	thought = "Based on the context, I'll " + action.Content

	if len(response.ToolCalls) > 0 {
		thought += fmt.Sprintf(" and use %d tool(s).", len(response.ToolCalls))
	} else {
		thought += " to provide the final answer."
	}

	return thought, action, response.Usage, nil
}

// act 行动阶段：执行工具调用
func (loop *ReActLoop) act(ctx context.Context, traceID, sessionID string, step int, depth int, historySnapshot []types.Message, toolCalls []types.ToolCall, toolWhitelist []string) ([]toolExecutionResult, error) {
	results := make([]toolExecutionResult, len(toolCalls))
	gateway := loop.agent.GetOutputGateway()
	allowedTools := whitelistSet(toolWhitelist)
	var pendingCheckpoints map[string]*runtimecheckpoint.PendingCheckpoint
	checkpointMgr := loop.agent.GetCheckpointManager()
	historyCount := len(historySnapshot)

	for i, tc := range toolCalls {
		if loop.config.Verbose {
			fmt.Printf("  Executing tool: %s with args: %v\n", tc.Name, tc.Args)
		}

		result := toolExecutionResult{Call: tc}
		metadata := map[string]interface{}{
			"step":     step,
			"trace_id": traceID,
		}
		if source := resolveToolSourceForRequest(loop.agent, tc.Name); source != "" {
			metadata[toolresult.SourceKey] = source
		}
		callCtx := promoteTeamRunContext(toolCallContext(ctx, toolCalls, tc.ID, results[:i], loop.agent, sessionID), results[:i])
		loop.agent.emitRuntimeEvent("tool.requested", sessionID, tc.Name, toolRequestedEventPayload(tc, step, traceID, toolRequestedEventSourcePayload(loop.agent, tc.Name)))
		if err := loop.agent.runPreToolUseHooks(ctx, sessionID, tc); err != nil {
			result.Error = err.Error()
			loop.emitToolDenied(sessionID, tc, step, traceID, "hook", result.Error, nil)
			envelope, gatewayErr := gateway.Process(ctx, output.RawToolResult{
				SessionID:  sessionID,
				ToolName:   tc.Name,
				ToolCallID: tc.ID,
				Step:       step,
				Error:      result.Error,
				Metadata:   metadata,
			})
			if gatewayErr != nil && envelope != nil {
				envelope.Metadata["gateway_error"] = gatewayErr.Error()
			}
			result.Envelope = envelope
			loop.emitToolReduced(sessionID, tc, step, traceID, result, nil)
			results[i] = result
			loop.agent.runPostToolUseHooks(ctx, sessionID, result)
			continue
		}

		if hookManager := loop.agent.GetHookManager(); hookManager != nil {
			decision, hookErr := hookManager.Dispatch(ctx, runtimehooks.EventPreToolUse, map[string]interface{}{
				"tool_name":  tc.Name,
				"tool_call":  tc.ID,
				"session_id": sessionID,
				"trace_id":   traceID,
				"args":       tc.Args,
			})
			if hookErr != nil {
				result.Error = hookErr.Error()
				loop.emitToolDenied(sessionID, tc, step, traceID, "hook", result.Error, nil)
				envelope, gatewayErr := gateway.Process(ctx, output.RawToolResult{
					SessionID:  sessionID,
					ToolName:   tc.Name,
					ToolCallID: tc.ID,
					Step:       step,
					Error:      result.Error,
					Metadata:   metadata,
				})
				if gatewayErr != nil && envelope != nil {
					envelope.Metadata["gateway_error"] = gatewayErr.Error()
				}
				result.Envelope = envelope
				loop.emitToolReduced(sessionID, tc, step, traceID, result, nil)
				results[i] = result
				loop.agent.runPostToolUseHooks(ctx, sessionID, result)
				continue
			}
			if decision.Action == runtimehooks.DecisionBlock {
				result.Error = strings.TrimSpace(decision.Message)
				if result.Error == "" {
					result.Error = "hook blocked tool"
				}
				loop.emitToolDenied(sessionID, tc, step, traceID, "hook", result.Error, nil)
				envelope, gatewayErr := gateway.Process(ctx, output.RawToolResult{
					SessionID:  sessionID,
					ToolName:   tc.Name,
					ToolCallID: tc.ID,
					Step:       step,
					Error:      result.Error,
					Metadata:   metadata,
				})
				if gatewayErr != nil && envelope != nil {
					envelope.Metadata["gateway_error"] = gatewayErr.Error()
				}
				result.Envelope = envelope
				loop.emitToolReduced(sessionID, tc, step, traceID, result, nil)
				results[i] = result
				loop.agent.runPostToolUseHooks(ctx, sessionID, result)
				continue
			}
			if decision.Action == runtimehooks.DecisionModify && len(decision.PatchedPayload) > 0 {
				patched, patchErr := runtimepolicy.ApplyPatchedArgs(tc.Args, decision.PatchedPayload)
				if patchErr != nil {
					result.Error = patchErr.Error()
					loop.emitToolDenied(sessionID, tc, step, traceID, "hook", result.Error, nil)
					envelope, gatewayErr := gateway.Process(ctx, output.RawToolResult{
						SessionID:  sessionID,
						ToolName:   tc.Name,
						ToolCallID: tc.ID,
						Step:       step,
						Error:      result.Error,
						Metadata:   metadata,
					})
					if gatewayErr != nil && envelope != nil {
						envelope.Metadata["gateway_error"] = gatewayErr.Error()
					}
					result.Envelope = envelope
					loop.emitToolReduced(sessionID, tc, step, traceID, result, nil)
					results[i] = result
					loop.agent.runPostToolUseHooks(ctx, sessionID, result)
					continue
				}
				tc.Args = patched
				result.Call.Args = patched
			}
			mergeHookMetadata(metadata, decision.Message, decision.ExtraContext)
		}

		if len(allowedTools) > 0 && !allowedTools[tc.Name] {
			result.Error = fmt.Sprintf("tool not allowed for this agent: %s", tc.Name)
			loop.emitToolDenied(sessionID, tc, step, traceID, "tool_whitelist", result.Error, nil)
			envelope, gatewayErr := gateway.Process(ctx, output.RawToolResult{
				SessionID:  sessionID,
				ToolName:   tc.Name,
				ToolCallID: tc.ID,
				Step:       step,
				Error:      result.Error,
				Metadata:   metadata,
			})
			if gatewayErr != nil && envelope != nil {
				envelope.Metadata["gateway_error"] = gatewayErr.Error()
			}
			result.Envelope = envelope
			loop.emitToolReduced(sessionID, tc, step, traceID, result, nil)
			results[i] = result
			loop.agent.runPostToolUseHooks(ctx, sessionID, result)
			continue
		}
		engine := loop.agent.GetPermissionEngine()
		if engine == nil {
			if policy := loop.agent.GetToolExecutionPolicy(); policy != nil {
				if err := policy.AllowTool(tc.Name); err != nil {
					result.Error = err.Error()
					loop.emitToolDenied(sessionID, tc, step, traceID, classifyDeniedPolicy(result.Error), result.Error, nil)
					envelope, gatewayErr := gateway.Process(ctx, output.RawToolResult{
						SessionID:  sessionID,
						ToolName:   tc.Name,
						ToolCallID: tc.ID,
						Step:       step,
						Error:      result.Error,
						Metadata:   metadata,
					})
					if gatewayErr != nil && envelope != nil {
						envelope.Metadata["gateway_error"] = gatewayErr.Error()
					}
					result.Envelope = envelope
					loop.emitToolReduced(sessionID, tc, step, traceID, result, nil)
					results[i] = result
					loop.agent.runPostToolUseHooks(ctx, sessionID, result)
					continue
				}
			}
		}

		if broker := loop.agent.GetToolBroker(); broker != nil && broker.IsBrokerTool(tc.Name) {
			if engine != nil {
				decision, evalErr := engine.Evaluate(callCtx, runtimepolicy.EvalRequest{
					SessionID:  sessionID,
					TraceID:    traceID,
					ToolCallID: tc.ID,
					ToolName:   tc.Name,
					Args:       tc.Args,
					Mode:       permissionModeFromContext(ctx),
				})
				if evalErr != nil {
					result.Error = evalErr.Error()
					loop.emitToolDenied(sessionID, tc, step, traceID, "permission_engine", result.Error, nil)
					envelope, gatewayErr := gateway.Process(ctx, output.RawToolResult{
						SessionID:  sessionID,
						ToolName:   tc.Name,
						ToolCallID: tc.ID,
						Step:       step,
						Error:      result.Error,
						Metadata:   metadata,
					})
					if gatewayErr != nil && envelope != nil {
						envelope.Metadata["gateway_error"] = gatewayErr.Error()
					}
					result.Envelope = envelope
					loop.emitToolReduced(sessionID, tc, step, traceID, result, nil)
					results[i] = result
					loop.agent.runPostToolUseHooks(ctx, sessionID, result)
					continue
				}
				mergeHookMetadata(metadata, decision.HookMessage, decision.HookContext)
				if decision.Type == runtimepolicy.DecisionDeny {
					result.Error = decision.Reason
					loop.emitToolDenied(sessionID, tc, step, traceID, "permission_engine", result.Error, nil)
					envelope, gatewayErr := gateway.Process(ctx, output.RawToolResult{
						SessionID:  sessionID,
						ToolName:   tc.Name,
						ToolCallID: tc.ID,
						Step:       step,
						Error:      result.Error,
						Metadata:   metadata,
					})
					if gatewayErr != nil && envelope != nil {
						envelope.Metadata["gateway_error"] = gatewayErr.Error()
					}
					result.Envelope = envelope
					loop.emitToolReduced(sessionID, tc, step, traceID, result, nil)
					results[i] = result
					loop.agent.runPostToolUseHooks(ctx, sessionID, result)
					continue
				}
				if len(decision.PatchedArgs) > 0 {
					patched, patchErr := runtimepolicy.ApplyPatchedArgs(tc.Args, decision.PatchedArgs)
					if patchErr != nil {
						result.Error = patchErr.Error()
						loop.emitToolDenied(sessionID, tc, step, traceID, "permission_engine", result.Error, nil)
						envelope, gatewayErr := gateway.Process(ctx, output.RawToolResult{
							SessionID:  sessionID,
							ToolName:   tc.Name,
							ToolCallID: tc.ID,
							Step:       step,
							Error:      result.Error,
							Metadata:   metadata,
						})
						if gatewayErr != nil && envelope != nil {
							envelope.Metadata["gateway_error"] = gatewayErr.Error()
						}
						result.Envelope = envelope
						loop.emitToolReduced(sessionID, tc, step, traceID, result, nil)
						results[i] = result
						loop.agent.runPostToolUseHooks(ctx, sessionID, result)
						continue
					}
					tc.Args = patched
					result.Call.Args = patched
				}
			}

			var (
				rawOutput interface{}
				rawMeta   map[string]interface{}
				callErr   error
			)
			rawOutput, rawMeta, callErr = broker.ExecuteToolCall(callCtx, sessionID, tc)
			recordToolExecutionOutcome(&result, metadata, rawOutput, rawMeta, callErr)

			envelope, gatewayErr := gateway.Process(ctx, output.RawToolResult{
				SessionID:  sessionID,
				ToolName:   tc.Name,
				ToolCallID: tc.ID,
				Step:       step,
				Content:    result.Output,
				Error:      result.Error,
				Metadata:   metadata,
			})
			if gatewayErr != nil && envelope != nil {
				envelope.Metadata["gateway_error"] = gatewayErr.Error()
			}
			result.Envelope = envelope
			loop.agent.emitRuntimeEvent("tool.completed", sessionID, tc.Name, toolCompletedEventPayload(result, step, traceID, map[string]interface{}{
				"awaiting_model": i == len(toolCalls)-1 && hasRemainingStepBudget(loop.config.MaxSteps, step),
			}))
			loop.emitToolReduced(sessionID, tc, step, traceID, result, nil)
			results[i] = result
			loop.agent.runPostToolUseHooks(ctx, sessionID, result)
			continue
		}

		if tc.Name == "spawn_subagents" {
			if engine != nil {
				decision, evalErr := engine.Evaluate(callCtx, runtimepolicy.EvalRequest{
					SessionID:  sessionID,
					TraceID:    traceID,
					ToolCallID: tc.ID,
					ToolName:   tc.Name,
					Args:       tc.Args,
					Mode:       permissionModeFromContext(ctx),
				})
				if evalErr != nil {
					result.Error = evalErr.Error()
					loop.emitToolDenied(sessionID, tc, step, traceID, "permission_engine", result.Error, nil)
					envelope, gatewayErr := gateway.Process(ctx, output.RawToolResult{
						SessionID:  sessionID,
						ToolName:   tc.Name,
						ToolCallID: tc.ID,
						Step:       step,
						Error:      result.Error,
						Metadata:   metadata,
					})
					if gatewayErr != nil && envelope != nil {
						envelope.Metadata["gateway_error"] = gatewayErr.Error()
					}
					result.Envelope = envelope
					loop.emitToolReduced(sessionID, tc, step, traceID, result, nil)
					results[i] = result
					loop.agent.runPostToolUseHooks(ctx, sessionID, result)
					continue
				}
				mergeHookMetadata(metadata, decision.HookMessage, decision.HookContext)
				if decision.Type == runtimepolicy.DecisionDeny {
					result.Error = decision.Reason
					loop.emitToolDenied(sessionID, tc, step, traceID, "permission_engine", result.Error, nil)
					envelope, gatewayErr := gateway.Process(ctx, output.RawToolResult{
						SessionID:  sessionID,
						ToolName:   tc.Name,
						ToolCallID: tc.ID,
						Step:       step,
						Error:      result.Error,
						Metadata:   metadata,
					})
					if gatewayErr != nil && envelope != nil {
						envelope.Metadata["gateway_error"] = gatewayErr.Error()
					}
					result.Envelope = envelope
					loop.emitToolReduced(sessionID, tc, step, traceID, result, nil)
					results[i] = result
					loop.agent.runPostToolUseHooks(ctx, sessionID, result)
					continue
				}
				if len(decision.PatchedArgs) > 0 {
					patched, patchErr := runtimepolicy.ApplyPatchedArgs(tc.Args, decision.PatchedArgs)
					if patchErr != nil {
						result.Error = patchErr.Error()
						loop.emitToolDenied(sessionID, tc, step, traceID, "permission_engine", result.Error, nil)
						envelope, gatewayErr := gateway.Process(ctx, output.RawToolResult{
							SessionID:  sessionID,
							ToolName:   tc.Name,
							ToolCallID: tc.ID,
							Step:       step,
							Error:      result.Error,
							Metadata:   metadata,
						})
						if gatewayErr != nil && envelope != nil {
							envelope.Metadata["gateway_error"] = gatewayErr.Error()
						}
						result.Envelope = envelope
						loop.emitToolReduced(sessionID, tc, step, traceID, result, nil)
						results[i] = result
						loop.agent.runPostToolUseHooks(ctx, sessionID, result)
						continue
					}
					tc.Args = patched
					result.Call.Args = patched
				}
			}

			subtasks, decodeErr := decodeSubagentTasks(tc.Args)
			if decodeErr != nil {
				result.Error = decodeErr.Error()
				loop.emitToolDenied(sessionID, tc, step, traceID, "subagent_decode", result.Error, nil)
			} else {
				scheduler := loop.agent.GetSubagentScheduler()
				if scheduler == nil {
					result.Error = "subagent scheduler is not configured"
					loop.emitToolDenied(sessionID, tc, step, traceID, "subagent_scheduler", result.Error, nil)
				} else {
					loop.agent.emitRuntimeEvent("subagent.batch.started", sessionID, tc.Name, map[string]interface{}{
						"tool_call_id": tc.ID,
						"step":         step,
						"trace_id":     traceID,
					})
					reports, runErr := scheduler.RunChildren(ctx, SubagentRunOptions{
						TraceID:          traceID,
						ParentSessionID:  sessionID,
						ParentToolCallID: tc.ID,
						Depth:            depth + 1,
					}, subtasks)
					loop.agent.emitRuntimeEvent("subagent.batch.completed", sessionID, tc.Name, map[string]interface{}{
						"tool_call_id":   tc.ID,
						"step":           step,
						"trace_id":       traceID,
						"success":        runErr == nil,
						"subagent_count": len(reports),
						"error":          errorString(runErr),
					})
					if runErr != nil {
						result.Error = runErr.Error()
						loop.emitToolDenied(sessionID, tc, step, traceID, "subagent_scheduler", result.Error, nil)
					} else {
						result.Output = renderSubagentResults(reports)
						metadata["subagent_count"] = len(reports)
						metadata["subagent_reports"] = reports
					}
				}
			}

			envelope, gatewayErr := gateway.Process(ctx, output.RawToolResult{
				SessionID:  sessionID,
				ToolName:   tc.Name,
				ToolCallID: tc.ID,
				Step:       step,
				Content:    result.Output,
				Error:      result.Error,
				Metadata:   metadata,
			})
			if gatewayErr != nil && envelope != nil {
				envelope.Metadata["gateway_error"] = gatewayErr.Error()
			}
			result.Envelope = envelope
			loop.emitToolReduced(sessionID, tc, step, traceID, result, map[string]interface{}{
				"subagent_count": metadata["subagent_count"],
			})
			results[i] = result
			loop.agent.runPostToolUseHooks(ctx, sessionID, result)
			continue
		}

		if loop.agent.mcpManager == nil {
			result.Error = "mcp manager is nil"
			loop.emitToolDenied(sessionID, tc, step, traceID, "runtime", result.Error, nil)
			envelope, gatewayErr := gateway.Process(ctx, output.RawToolResult{
				SessionID:  sessionID,
				ToolName:   tc.Name,
				ToolCallID: tc.ID,
				Step:       step,
				Error:      result.Error,
				Metadata:   metadata,
			})
			if gatewayErr != nil && envelope != nil {
				envelope.Metadata["gateway_error"] = gatewayErr.Error()
			}
			result.Envelope = envelope
			loop.emitToolReduced(sessionID, tc, step, traceID, result, nil)
			results[i] = result
			loop.agent.runPostToolUseHooks(ctx, sessionID, result)
			continue
		}

		// 查找工具
		toolInfo, err := loop.agent.mcpManager.FindTool(tc.Name)
		if err != nil {
			result.Error = fmt.Sprintf("tool not found: %s", tc.Name)
			loop.emitToolDenied(sessionID, tc, step, traceID, "tool_lookup", result.Error, nil)
			envelope, gatewayErr := gateway.Process(ctx, output.RawToolResult{
				SessionID:  sessionID,
				ToolName:   tc.Name,
				ToolCallID: tc.ID,
				Step:       step,
				Error:      result.Error,
				Metadata:   metadata,
			})
			if gatewayErr != nil && envelope != nil {
				envelope.Metadata["gateway_error"] = gatewayErr.Error()
			}
			result.Envelope = envelope
			loop.emitToolReduced(sessionID, tc, step, traceID, result, nil)
			results[i] = result
			loop.agent.runPostToolUseHooks(ctx, sessionID, result)
			continue
		}
		metadata["mcp_name"] = toolInfo.MCPName
		metadata["trust_level"] = toolInfo.MCPTrustLevel
		metadata["execution_mode"] = toolInfo.ExecutionMode
		if engine != nil {
			decision, evalErr := engine.Evaluate(callCtx, runtimepolicy.EvalRequest{
				SessionID:  sessionID,
				TraceID:    traceID,
				ToolCallID: tc.ID,
				ToolName:   tc.Name,
				ToolInfo:   &toolInfo,
				Args:       tc.Args,
				Mode:       permissionModeFromContext(ctx),
			})
			if evalErr != nil {
				result.Error = evalErr.Error()
				loop.emitToolDenied(sessionID, tc, step, traceID, "permission_engine", result.Error, metadata)
				envelope, gatewayErr := gateway.Process(ctx, output.RawToolResult{
					SessionID:  sessionID,
					ToolName:   tc.Name,
					ToolCallID: tc.ID,
					Step:       step,
					Error:      result.Error,
					Metadata:   metadata,
				})
				if gatewayErr != nil && envelope != nil {
					envelope.Metadata["gateway_error"] = gatewayErr.Error()
				}
				result.Envelope = envelope
				loop.emitToolReduced(sessionID, tc, step, traceID, result, map[string]interface{}{
					"mcp_name":       toolInfo.MCPName,
					"execution_mode": toolInfo.ExecutionMode,
				})
				results[i] = result
				loop.agent.runPostToolUseHooks(ctx, sessionID, result)
				continue
			}
			mergeHookMetadata(metadata, decision.HookMessage, decision.HookContext)
			if decision.Type == runtimepolicy.DecisionDeny {
				result.Error = decision.Reason
				loop.emitToolDenied(sessionID, tc, step, traceID, "permission_engine", result.Error, metadata)
				envelope, gatewayErr := gateway.Process(ctx, output.RawToolResult{
					SessionID:  sessionID,
					ToolName:   tc.Name,
					ToolCallID: tc.ID,
					Step:       step,
					Error:      result.Error,
					Metadata:   metadata,
				})
				if gatewayErr != nil && envelope != nil {
					envelope.Metadata["gateway_error"] = gatewayErr.Error()
				}
				result.Envelope = envelope
				loop.emitToolReduced(sessionID, tc, step, traceID, result, map[string]interface{}{
					"mcp_name":       toolInfo.MCPName,
					"execution_mode": toolInfo.ExecutionMode,
				})
				results[i] = result
				loop.agent.runPostToolUseHooks(ctx, sessionID, result)
				continue
			}
			if len(decision.PatchedArgs) > 0 {
				patched, patchErr := runtimepolicy.ApplyPatchedArgs(tc.Args, decision.PatchedArgs)
				if patchErr != nil {
					result.Error = patchErr.Error()
					loop.emitToolDenied(sessionID, tc, step, traceID, "permission_engine", result.Error, metadata)
					envelope, gatewayErr := gateway.Process(ctx, output.RawToolResult{
						SessionID:  sessionID,
						ToolName:   tc.Name,
						ToolCallID: tc.ID,
						Step:       step,
						Error:      result.Error,
						Metadata:   metadata,
					})
					if gatewayErr != nil && envelope != nil {
						envelope.Metadata["gateway_error"] = gatewayErr.Error()
					}
					result.Envelope = envelope
					loop.emitToolReduced(sessionID, tc, step, traceID, result, map[string]interface{}{
						"mcp_name":       toolInfo.MCPName,
						"execution_mode": toolInfo.ExecutionMode,
					})
					results[i] = result
					loop.agent.runPostToolUseHooks(ctx, sessionID, result)
					continue
				}
				tc.Args = patched
				result.Call.Args = patched
			}
		} else if policy := loop.agent.GetToolExecutionPolicy(); policy != nil {
			if err := policy.AllowToolCall(toolInfo, tc.Args); err != nil {
				result.Error = err.Error()
				loop.emitToolDenied(sessionID, tc, step, traceID, classifyDeniedPolicy(result.Error), result.Error, metadata)
				envelope, gatewayErr := gateway.Process(ctx, output.RawToolResult{
					SessionID:  sessionID,
					ToolName:   tc.Name,
					ToolCallID: tc.ID,
					Step:       step,
					Error:      result.Error,
					Metadata:   metadata,
				})
				if gatewayErr != nil && envelope != nil {
					envelope.Metadata["gateway_error"] = gatewayErr.Error()
				}
				result.Envelope = envelope
				loop.emitToolReduced(sessionID, tc, step, traceID, result, map[string]interface{}{
					"mcp_name":       toolInfo.MCPName,
					"execution_mode": toolInfo.ExecutionMode,
				})
				results[i] = result
				loop.agent.runPostToolUseHooks(ctx, sessionID, result)
				continue
			}
		}

		// 调用工具
		if checkpointMgr != nil && (runtimepolicy.IsWriteLikeToolName(tc.Name) || runtimepolicy.IsShellLikeToolName(tc.Name) || runtimepolicy.HasMutationHints(tc.Args)) {
			pending, _ := checkpointMgr.BeforeMutation(ctx, sessionID, tc.Name, tc.ID, tc.Args)
			if pending != nil {
				pending.MessageCount = historyCount
				pending.Conversation = cloneMessages(historySnapshot)
				if pendingCheckpoints == nil {
					pendingCheckpoints = make(map[string]*runtimecheckpoint.PendingCheckpoint, 1)
				}
				pendingCheckpoints[tc.ID] = pending
			}
		}
		var (
			rawOutput interface{}
			rawMeta   map[string]interface{}
		)
		if caller, ok := loop.agent.mcpManager.(richToolCaller); ok {
			rawOutput, rawMeta, err = caller.CallToolWithMeta(ctx, toolInfo.MCPName, tc.Name, tc.Args)
		} else {
			rawOutput, err = loop.agent.mcpManager.CallTool(ctx, toolInfo.MCPName, tc.Name, tc.Args)
		}
		recordToolExecutionOutcome(&result, metadata, rawOutput, rawMeta, err)

		envelope, gatewayErr := gateway.Process(ctx, output.RawToolResult{
			SessionID:  sessionID,
			ToolName:   tc.Name,
			ToolCallID: tc.ID,
			Step:       step,
			Content:    result.Output,
			Error:      result.Error,
			Metadata:   metadata,
		})
		if gatewayErr != nil && envelope != nil {
			envelope.Metadata["gateway_error"] = gatewayErr.Error()
		}
		result.Envelope = envelope
		loop.agent.emitRuntimeEvent("tool.completed", sessionID, tc.Name, toolCompletedEventPayload(result, step, traceID, map[string]interface{}{
			"awaiting_model": i == len(toolCalls)-1 && hasRemainingStepBudget(loop.config.MaxSteps, step),
		}))
		if pendingCheckpoints != nil {
			if pending := pendingCheckpoints[tc.ID]; pending != nil {
				meta := map[string]interface{}{}
				for key, value := range metadata {
					meta[key] = value
				}
				if result.Envelope != nil && len(result.Envelope.Metadata) > 0 {
					for key, value := range result.Envelope.Metadata {
						meta[key] = value
					}
				}
				checkpointID, checkpointErr := checkpointMgr.AfterMutation(ctx, pending, meta, result.Error)
				if checkpointID != "" {
					if hookMgr := loop.agent.GetHookManager(); hookMgr != nil {
						payload := map[string]interface{}{
							"session_id":    sessionID,
							"tool_name":     tc.Name,
							"tool_call_id":  tc.ID,
							"checkpoint_id": checkpointID,
							"trace_id":      traceID,
						}
						if checkpointErr != nil {
							payload["error"] = checkpointErr.Error()
						}
						hookMgr.DispatchAsync(ctx, runtimehooks.EventCheckpointCreated, payload)
					}
				}
				delete(pendingCheckpoints, tc.ID)
			}
		}
		loop.emitToolReduced(sessionID, tc, step, traceID, result, map[string]interface{}{
			"mcp_name":       toolInfo.MCPName,
			"execution_mode": toolInfo.ExecutionMode,
		})
		results[i] = result
		loop.agent.runPostToolUseHooks(ctx, sessionID, result)
	}

	return results, nil
}

func (loop *ReActLoop) emitToolDenied(sessionID string, tc types.ToolCall, step int, traceID, policy, reason string, extra map[string]interface{}) {
	if loop == nil || loop.agent == nil {
		return
	}
	payload := map[string]interface{}{
		"tool_call_id": tc.ID,
		"step":         step,
		"policy":       policy,
		"reason":       reason,
		"trace_id":     traceID,
	}
	for key, value := range extra {
		if key == "" {
			continue
		}
		payload[key] = value
	}
	loop.agent.emitRuntimeEvent("tool.denied", sessionID, tc.Name, payload)
}

func (loop *ReActLoop) emitToolReduced(sessionID string, tc types.ToolCall, step int, traceID string, result toolExecutionResult, extra map[string]interface{}) {
	if loop == nil || loop.agent == nil {
		return
	}
	payload := map[string]interface{}{
		"tool_call_id": tc.ID,
		"step":         step,
		"error":        result.Error,
		"trace_id":     traceID,
	}
	if result.Envelope != nil {
		if reducer, ok := result.Envelope.Metadata["reducer"]; ok {
			payload["reducer"] = reducer
		}
		if count := len(result.Envelope.ArtifactIDs); count > 0 {
			payload["artifact_ref_count"] = count
		}
	}
	for key, value := range extra {
		if key == "" {
			continue
		}
		payload[key] = value
	}
	loop.agent.emitRuntimeEvent("tool.reduced", sessionID, tc.Name, payload)
}

func (loop *ReActLoop) runtimeRetryEventReporter(traceID, sessionID string, step int, req *llm.LLMRequest) llm.RetryEventReporter {
	if loop == nil || loop.agent == nil {
		return nil
	}
	requestProvider := ""
	requestModel := ""
	if req != nil {
		requestProvider = strings.TrimSpace(req.Provider)
		requestModel = strings.TrimSpace(req.Model)
	}
	return func(event llm.RetryEvent) {
		payload := map[string]interface{}{
			"trace_id": traceID,
			"step":     step,
			"source":   strings.TrimSpace(event.Source),
		}
		if provider := firstNonEmptyTrimmed(event.Provider, requestProvider); provider != "" {
			payload["provider"] = provider
		}
		if protocol := strings.TrimSpace(event.Protocol); protocol != "" {
			payload["protocol"] = protocol
		}
		if model := firstNonEmptyTrimmed(event.Model, requestModel); model != "" {
			payload["model"] = model
		}
		if event.Attempt > 0 {
			payload["attempt"] = event.Attempt
		}
		if event.MaxAttempts > 0 {
			payload["max_attempts"] = event.MaxAttempts
		}
		if reason := strings.TrimSpace(event.RetryReason); reason != "" {
			payload["retry_reason"] = reason
		}
		if event.RetryDelayMS > 0 {
			payload["retry_delay_ms"] = event.RetryDelayMS
		}
		if errText := strings.TrimSpace(event.Error); errText != "" {
			payload["error"] = errText
		}
		loop.agent.emitRuntimeEvent("llm.retry", sessionID, "", payload)
	}
}

func toolRequestedEventSourcePayload(agent *Agent, toolName string) map[string]interface{} {
	source := resolveToolSourceForRequest(agent, toolName)
	if source == "" {
		return nil
	}
	return map[string]interface{}{
		toolresult.SourceKey: source,
	}
}

func firstNonEmptyTrimmed(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func resolveToolSourceForRequest(agent *Agent, toolName string) string {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return ""
	}
	if toolName == "list_mcp_resources" {
		return toolresult.SourceMeta
	}
	if agent != nil {
		if broker := agent.GetToolBroker(); broker != nil && broker.IsBrokerTool(toolName) {
			return toolresult.SourceBroker
		}
		if resolver, ok := agent.mcpManager.(toolSourceResolver); ok {
			if source := toolresult.NormalizeSource(resolver.ResolveToolSource(toolName)); source != "" {
				return source
			}
		}
		if agent.mcpManager != nil {
			if info, err := agent.mcpManager.FindTool(toolName); err == nil {
				if strings.TrimSpace(info.MCPName) != "" {
					return toolresult.SourceMCP
				}
				return toolresult.SourceToolkit
			}
		}
	}
	return ""
}

func classifyDeniedPolicy(reason string) string {
	lower := strings.ToLower(strings.TrimSpace(reason))
	switch {
	case strings.Contains(lower, "sandbox"):
		return "sandbox"
	case strings.Contains(lower, "read-only"):
		return "read_only"
	case strings.Contains(lower, "untrusted"):
		return "trust_level"
	case strings.Contains(lower, "remote mcp"):
		return "execution_mode"
	case strings.Contains(lower, "not allowed"):
		return "allowlist"
	default:
		return "tool_policy"
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// observe 观察阶段：记录执行结果
func (loop *ReActLoop) observe(ctx context.Context, toolResults []toolExecutionResult, observations []types.Observation, step int) []types.Observation {

	for i, result := range toolResults {
		toolName := result.Call.Name
		if toolName == "" {
			toolName = fmt.Sprintf("tool_%d", i)
		}

		obs := types.NewObservation(fmt.Sprintf("step_%d_tool_%d", step, i), toolName)
		obs.WithInput(result.Call.Args)

		if result.Envelope != nil {
			obs.WithOutput(result.Envelope.Render())
			if reducer, ok := result.Envelope.Metadata["reducer"]; ok {
				obs.WithMetric("reducer", reducer)
			}
			if rawBytes, ok := result.Envelope.Metadata["raw_bytes"]; ok {
				obs.WithMetric("raw_bytes", rawBytes)
			}
			if len(result.Envelope.ArtifactIDs) > 0 {
				obs.WithMetric("artifact_refs", result.Envelope.ArtifactIDs)
			}
			if subagentCount, ok := result.Envelope.Metadata["subagent_count"]; ok {
				obs.WithMetric("subagent_count", subagentCount)
			}
			if subagentReports, ok := result.Envelope.Metadata["subagent_reports"]; ok {
				obs.WithMetric("subagent_reports", subagentReports)
			}
			if toolMetadata, ok := result.Envelope.Metadata["tool_metadata"]; ok {
				obs.WithMetric("tool_metadata", toolMetadata)
			}
		}

		if result.Error != "" {
			obs.MarkFailure(result.Error)
		} else {
			obs.MarkSuccess()
		}

		observations = append(observations, *obs)
	}

	return observations
}

// getAvailableTools 获取可用工具列表
func (loop *ReActLoop) getAvailableTools(ctx context.Context, goal string, toolWhitelist []string) ([]types.ToolDefinition, error) {
	if snapshot, ok := TurnToolSurfaceSnapshotFromContext(ctx); ok && snapshot != nil {
		tools, cached, err := snapshot.LoadTurnToolSurface(ctx)
		if err != nil {
			return nil, err
		}
		if cached {
			return cloneToolDefinitions(tools), nil
		}
		tools, err = loop.computeAvailableTools(ctx, goal, toolWhitelist)
		if err != nil {
			return nil, err
		}
		if err := snapshot.SaveTurnToolSurface(ctx, tools); err != nil {
			return nil, err
		}
		return cloneToolDefinitions(tools), nil
	}
	return loop.computeAvailableTools(ctx, goal, toolWhitelist)
}

func (loop *ReActLoop) computeAvailableTools(ctx context.Context, goal string, toolWhitelist []string) ([]types.ToolDefinition, error) {
	allowed := whitelistSet(toolWhitelist)
	tools := make([]types.ToolDefinition, 0, 8)
	seen := make(map[string]bool)

	if loop.agent.mcpManager != nil {
		selectedTools := loop.agent.GetToolCatalog().Search(goal, 6)
		if len(selectedTools) == 0 {
			selectedTools = loop.agent.mcpManager.ListTools()
		}
		for _, mt := range selectedTools {
			if len(allowed) > 0 && !allowed[mt.Name] {
				continue
			}
			if policy := loop.agent.GetToolExecutionPolicy(); policy != nil && policy.AllowToolInfo(mt) != nil {
				continue
			}
			if seen[mt.Name] {
				continue
			}
			seen[mt.Name] = true
			definition := types.ToolDefinition{
				Name:        mt.Name,
				Description: mt.Description,
				Parameters:  normalizeToolParameters(mt.InputSchema),
			}
			if source := resolveToolSourceForRequest(loop.agent, mt.Name); source != "" {
				definition.Metadata = map[string]interface{}{
					toolresult.SourceKey: source,
				}
			}
			tools = append(tools, definition)
		}
	}

	if scheduler := loop.agent.GetSubagentScheduler(); scheduler != nil {
		if (len(allowed) == 0 || allowed["spawn_subagents"]) &&
			(loop.agent.GetToolExecutionPolicy() == nil || loop.agent.GetToolExecutionPolicy().AllowsDefinition("spawn_subagents")) {
			tools = append(tools, spawnSubagentsToolDefinition())
		}
	}

	if broker := loop.agent.GetToolBroker(); broker != nil {
		for _, def := range broker.DefinitionsForContext(ctx) {
			if len(allowed) > 0 && !allowed[def.Name] {
				continue
			}
			if policy := loop.agent.GetToolExecutionPolicy(); policy != nil && !policy.AllowsDefinition(def.Name) {
				continue
			}
			if seen[def.Name] {
				continue
			}
			seen[def.Name] = true
			tools = append(tools, def)
		}
	}

	return tools, nil
}

func promoteTeamRunContext(ctx context.Context, results []toolExecutionResult) context.Context {
	promoted := ctx
	for _, result := range results {
		promoted = promoteTeamRunContextFromResult(promoted, result)
	}
	return promoted
}

func promoteTeamRunContextFromResult(ctx context.Context, result toolExecutionResult) context.Context {
	if strings.TrimSpace(result.Error) != "" || !strings.EqualFold(strings.TrimSpace(result.Call.Name), toolbroker.ToolSpawnTeam) {
		return ctx
	}
	teamID, taskID := spawnTeamContextIDs(result)
	if teamID == "" {
		return ctx
	}

	meta, ok := team.GetRunMeta(ctx)
	if ok && meta != nil {
		meta = meta.Clone()
	} else {
		meta = &team.RunMeta{}
	}
	if meta.Team == nil {
		meta.Team = &team.TeamRunMeta{}
	}
	meta.Team.TeamID = teamID
	if strings.TrimSpace(meta.Team.AgentID) == "" {
		meta.Team.AgentID = "lead"
	}
	if taskID != "" {
		meta.Team.CurrentTaskID = taskID
	}
	return team.WithRunMeta(ctx, meta)
}

func spawnTeamContextIDs(result toolExecutionResult) (teamID string, taskID string) {
	switch output := result.Output.(type) {
	case toolbroker.SpawnTeamResult:
		teamID = strings.TrimSpace(output.TeamID)
		if len(output.TaskIDs) == 1 {
			taskID = strings.TrimSpace(output.TaskIDs[0])
		}
	case *toolbroker.SpawnTeamResult:
		if output != nil {
			teamID = strings.TrimSpace(output.TeamID)
			if len(output.TaskIDs) == 1 {
				taskID = strings.TrimSpace(output.TaskIDs[0])
			}
		}
	}

	if result.Envelope != nil {
		if teamID == "" {
			if rawMeta, ok := result.Envelope.Metadata["tool_metadata"].(map[string]interface{}); ok {
				teamID = strings.TrimSpace(stringValue(rawMeta["team_id"]))
				if taskID == "" {
					taskID = strings.TrimSpace(stringValue(rawMeta["task_id"]))
				}
			}
		}
	}

	if teamID == "" {
		teamID = strings.TrimSpace(stringValue(result.Call.Args["team_id"]))
	}
	if taskID == "" {
		taskID = firstSpawnTaskID(result.Call.Args)
	}
	return teamID, taskID
}

func firstSpawnTaskID(args map[string]interface{}) string {
	if len(args) == 0 {
		return ""
	}
	rawTasks, ok := args["tasks"]
	if !ok {
		return ""
	}
	switch tasks := rawTasks.(type) {
	case []interface{}:
		if len(tasks) != 1 {
			return ""
		}
		entry, ok := tasks[0].(map[string]interface{})
		if !ok {
			return ""
		}
		return strings.TrimSpace(stringValue(entry["id"]))
	case []map[string]interface{}:
		if len(tasks) != 1 {
			return ""
		}
		return strings.TrimSpace(stringValue(tasks[0]["id"]))
	default:
		return ""
	}
}

func normalizeToolParameters(schema map[string]interface{}) map[string]interface{} {
	if len(schema) == 0 {
		return map[string]interface{}{
			"type":                 "object",
			"additionalProperties": false,
		}
	}

	normalized := make(map[string]interface{}, len(schema)+1)
	for key, value := range schema {
		normalized[key] = value
	}
	if _, ok := normalized["type"]; !ok {
		normalized["type"] = "object"
	}
	if paramType, ok := normalized["type"].(string); ok && paramType == "object" {
		if _, ok := normalized["additionalProperties"]; !ok {
			normalized["additionalProperties"] = false
		}
	}
	return normalized
}

// AgentAction Agent 行动
type AgentAction struct {
	Content         string                 `json:"content" yaml:"content"`
	ToolCalls       []types.ToolCall       `json:"toolCalls,omitempty" yaml:"toolCalls,omitempty"`
	Thought         string                 `json:"thought,omitempty" yaml:"thought,omitempty"`
	Reasoning       *types.ReasoningBlock  `json:"reasoning,omitempty" yaml:"reasoning,omitempty"`
	Metadata        map[string]interface{} `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	MessageMetadata types.Metadata         `json:"messageMetadata,omitempty" yaml:"messageMetadata,omitempty"`
}

// Stop 停止循环
func (loop *ReActLoop) Stop() {
	if loop.agent != nil {
		loop.agent.SetRunning(false)
	}
}

// IsRunning 检查是否正在运行
func (loop *ReActLoop) IsRunning() bool {
	if loop.agent == nil {
		return false
	}
	return loop.agent.IsRunning()
}

func toolResultsToPayloads(results []toolExecutionResult) []ToolResultPayload {
	payloads := make([]ToolResultPayload, 0, len(results))
	for _, result := range results {
		payload := ToolResultPayload{
			ToolCallID: result.Call.ID,
			Metadata:   types.NewMetadata(),
		}
		payload.Content = output.RenderToolResultContentForModel(result.Output, result.Error, result.Envelope)
		if result.Envelope != nil {
			for key, value := range result.Envelope.Metadata {
				payload.Metadata[key] = value
			}
			if len(result.Envelope.ArtifactIDs) > 0 {
				payload.Metadata["artifact_refs"] = append([]string(nil), result.Envelope.ArtifactIDs...)
			}
		}
		if result.Error != "" {
			payload.Metadata["tool_error"] = result.Error
		}
		payloads = append(payloads, payload)
	}
	return payloads
}

func toolCallContext(ctx context.Context, toolCalls []types.ToolCall, currentToolCallID string, completed []toolExecutionResult, agent *Agent, sessionID string) context.Context {
	if batch, ok := ToolBatchContextFromContext(ctx); ok && len(batch.ToolCalls) > 0 {
		ctx = WithToolBatchContext(ctx, batch.ToolCalls, currentToolCallID, batch.CompletedToolMessages)
	} else {
		ctx = WithToolBatchContext(ctx, toolCalls, currentToolCallID, toolMessagesFromResults(completed))
	}
	if strings.TrimSpace(sessionID) != "" {
		ctx = toolctx.WithSessionID(ctx, sessionID)
	}
	if outputDir := generatedImageOutputDirForAgentSession(agent, sessionID); strings.TrimSpace(outputDir) != "" {
		ctx = toolctx.WithGeneratedImageOutputDir(ctx, outputDir)
	}
	return ctx
}

func toolMessagesFromResults(results []toolExecutionResult) []types.Message {
	if len(results) == 0 {
		return nil
	}
	messages := make([]types.Message, 0, len(results))
	for _, result := range results {
		if message := toolExecutionResultMessage(result); message != nil {
			messages = append(messages, *message)
		}
	}
	return messages
}

func toolExecutionResultMessage(result toolExecutionResult) *types.Message {
	if strings.TrimSpace(result.Call.ID) == "" {
		return nil
	}
	message := types.NewToolMessage(result.Call.ID, "")
	message.Content = output.RenderToolResultContentForModel(result.Output, result.Error, result.Envelope)
	if result.Envelope != nil {
		if len(result.Envelope.Metadata) > 0 {
			message.Metadata = types.NewMetadata()
			for key, value := range result.Envelope.Metadata {
				message.Metadata[key] = value
			}
		}
		if len(result.Envelope.ArtifactIDs) > 0 {
			if message.Metadata == nil {
				message.Metadata = types.NewMetadata()
			}
			message.Metadata["artifact_refs"] = append([]string(nil), result.Envelope.ArtifactIDs...)
		}
	}
	if strings.TrimSpace(message.Content) == "" {
		return nil
	}
	if message.Metadata == nil {
		message.Metadata = types.NewMetadata()
	}
	if strings.TrimSpace(result.Error) != "" {
		message.Metadata["tool_error"] = result.Error
	}
	return message
}

func persistBuilderHistory(builder *MessageBuilder, persist func([]types.Message) error) error {
	if builder == nil || persist == nil {
		return nil
	}
	return persist(builder.Messages())
}

func hasLeadingSystemPrompt(history []types.Message, systemPrompt string) bool {
	if len(history) == 0 {
		return false
	}
	first := history[0]
	return first.Role == "system" && first.Content == systemPrompt
}

func hasSystemPrompt(history []types.Message, systemPrompt string) bool {
	for _, msg := range history {
		if msg.Role == "system" && msg.Content == systemPrompt {
			return true
		}
	}
	return false
}

func hasLeadingSystemPromptInMessages(messages []types.Message, systemPrompt string) bool {
	return hasLeadingSystemPrompt(messages, systemPrompt)
}

func cloneMessageHistory(history []types.Message) []types.Message {
	cloned := make([]types.Message, len(history))
	for index := range history {
		cloned[index] = *history[index].Clone()
	}
	return cloned
}

func stripSystemMessages(history []types.Message) []types.Message {
	filtered := make([]types.Message, 0, len(history))
	for _, message := range history {
		if message.Role == "system" {
			continue
		}
		filtered = append(filtered, *message.Clone())
	}
	return filtered
}

func whitelistSet(values []string) map[string]bool {
	if len(values) == 0 {
		return nil
	}
	set := make(map[string]bool, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		set[value] = true
	}
	return set
}

func cloneInterfaceMap(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

func cloneMessages(input []types.Message) []types.Message {
	if len(input) == 0 {
		return nil
	}
	cloned := make([]types.Message, len(input))
	for i := range input {
		cloned[i] = *input[i].Clone()
	}
	return cloned
}

func cloneToolDefinitions(input []types.ToolDefinition) []types.ToolDefinition {
	if len(input) == 0 {
		return nil
	}
	cloned := make([]types.ToolDefinition, len(input))
	for index, tool := range input {
		cloned[index] = types.ToolDefinition{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  cloneInterfaceMap(tool.Parameters),
			Metadata:    cloneInterfaceMap(tool.Metadata),
		}
	}
	return cloned
}

func mergeHookMetadata(metadata map[string]interface{}, message string, context map[string]string) {
	if len(metadata) == 0 {
		return
	}
	message = strings.TrimSpace(message)
	if message != "" {
		if existing, ok := metadata["hook_message"].(string); ok && strings.TrimSpace(existing) != "" {
			metadata["hook_message"] = strings.TrimSpace(existing) + "\n" + message
		} else {
			metadata["hook_message"] = message
		}
	}
	if len(context) == 0 {
		return
	}
	target, ok := metadata["hook_context"].(map[string]interface{})
	if !ok || target == nil {
		target = make(map[string]interface{}, len(context))
		metadata["hook_context"] = target
	}
	for key, value := range context {
		target[key] = value
	}
}

func (loop *ReActLoop) configureBuilderActiveTurnCompaction(builder *MessageBuilder, remainingBudget int) {
	if builder == nil {
		return
	}
	if loop == nil || loop.llmRuntime == nil {
		builder.SetActiveTurnReplayCompaction(historyguard.DefaultActiveTurnReplayMaxBytes, 0, nil)
		return
	}
	budget := resolvePromptPreflightBudget(loop.llmRuntime, loop.agent, remainingBudget)
	if budget.PromptBudget > 0 {
		builder.SetActiveTurnReplayCompaction(
			historyguard.DefaultActiveTurnReplayMaxBytes,
			budget.PromptBudget,
			loop.llmRuntime.CountMessagesTokens,
		)
		return
	}
	builder.SetActiveTurnReplayCompaction(historyguard.DefaultActiveTurnReplayMaxBytes, 0, nil)
}

type promptPreflightBudget struct {
	PromptBudget                         int
	BudgetSource                         string
	BudgetSourceDetail                   string
	BudgetCandidates                     map[string]interface{}
	ResolvedProvider                     string
	ResolvedModel                        string
	ProviderContextLimit                 int
	ProviderOutputLimit                  int
	ModelCapabilityMaxContextTokens      int
	ModelCapabilityAutoCompactRatio      float64
	ModelCapabilityAutoCompactTokenLimit int
}

func (budget promptPreflightBudget) Metadata() map[string]interface{} {
	metadata := map[string]interface{}{}
	if budget.PromptBudget > 0 {
		metadata["prompt_budget"] = budget.PromptBudget
	}
	if budget.BudgetSource != "" {
		metadata["budget_source"] = budget.BudgetSource
	}
	if budget.BudgetSourceDetail != "" {
		metadata["budget_source_detail"] = budget.BudgetSourceDetail
	}
	if len(budget.BudgetCandidates) > 0 {
		metadata["budget_candidates"] = cloneInterfaceMap(budget.BudgetCandidates)
	}
	if budget.ResolvedProvider != "" {
		metadata["resolved_provider"] = budget.ResolvedProvider
	}
	if budget.ResolvedModel != "" {
		metadata["resolved_model"] = budget.ResolvedModel
	}
	if budget.ProviderContextLimit > 0 {
		metadata["provider_context_limit"] = budget.ProviderContextLimit
	}
	if budget.ProviderOutputLimit > 0 {
		metadata["provider_output_limit"] = budget.ProviderOutputLimit
	}
	if budget.ModelCapabilityMaxContextTokens > 0 {
		metadata["model_capability_max_context_tokens"] = budget.ModelCapabilityMaxContextTokens
	}
	if budget.ModelCapabilityAutoCompactRatio > 0 {
		metadata["model_capability_auto_compact_ratio"] = budget.ModelCapabilityAutoCompactRatio
	}
	if budget.ModelCapabilityAutoCompactTokenLimit > 0 {
		metadata["model_capability_auto_compact_token_limit"] = budget.ModelCapabilityAutoCompactTokenLimit
	}
	if len(metadata) == 0 {
		return nil
	}
	return metadata
}

func (budget *promptPreflightBudget) addCandidate(source string, value int, detail string) {
	if budget == nil || value <= 0 {
		return
	}
	if budget.BudgetCandidates == nil {
		budget.BudgetCandidates = make(map[string]interface{})
	}
	budget.BudgetCandidates[source] = value
	if budget.PromptBudget <= 0 || value < budget.PromptBudget {
		budget.PromptBudget = value
		budget.BudgetSource = source
		budget.BudgetSourceDetail = detail
	}
}

type promptPreflightFailure struct {
	Code                          string
	Reason                        string
	Detail                        string
	SuggestedAction               string
	CanRetryAfterCompaction       bool
	ActiveTurnMessageCount        int
	LatestReplayBlockMessageCount int
}

func (failure promptPreflightFailure) Metadata() map[string]interface{} {
	metadata := map[string]interface{}{
		"failure_reason_code": failure.Code,
		"failure_reason":      failure.Reason,
	}
	if failure.Detail != "" {
		metadata["failure_reason_detail"] = failure.Detail
	}
	if failure.SuggestedAction != "" {
		metadata["suggested_action"] = failure.SuggestedAction
	}
	metadata["can_retry_after_compaction"] = failure.CanRetryAfterCompaction
	if failure.ActiveTurnMessageCount > 0 {
		metadata["active_turn_message_count"] = failure.ActiveTurnMessageCount
	}
	if failure.LatestReplayBlockMessageCount > 0 {
		metadata["latest_replay_block_message_count"] = failure.LatestReplayBlockMessageCount
	}
	return metadata
}

func newPromptPreflightError(
	budget promptPreflightBudget,
	failure promptPreflightFailure,
	promptTokens int,
	activeTurnCompacted bool,
	replacementHistory []types.Message,
) *PromptPreflightError {
	return &PromptPreflightError{
		PromptTokens:                         promptTokens,
		PromptBudget:                         budget.PromptBudget,
		BudgetSource:                         budget.BudgetSource,
		BudgetSourceDetail:                   budget.BudgetSourceDetail,
		ResolvedProvider:                     budget.ResolvedProvider,
		ResolvedModel:                        budget.ResolvedModel,
		ProviderContextLimit:                 budget.ProviderContextLimit,
		ProviderOutputLimit:                  budget.ProviderOutputLimit,
		ModelCapabilityMaxContextTokens:      budget.ModelCapabilityMaxContextTokens,
		ModelCapabilityAutoCompactRatio:      budget.ModelCapabilityAutoCompactRatio,
		ModelCapabilityAutoCompactTokenLimit: budget.ModelCapabilityAutoCompactTokenLimit,
		Code:                                 failure.Code,
		Reason:                               failure.Reason,
		Detail:                               failure.Detail,
		SuggestedAction:                      failure.SuggestedAction,
		CanRetryAfterCompaction:              failure.CanRetryAfterCompaction,
		ActiveTurnCompacted:                  activeTurnCompacted,
		ActiveTurnMessageCount:               failure.ActiveTurnMessageCount,
		LatestReplayBlockMessageCount:        failure.LatestReplayBlockMessageCount,
		ReplacementHistory:                   cloneMessageHistory(replacementHistory),
	}
}

func (loop *ReActLoop) enforcePromptPreflight(traceID, sessionID string, step int, messages []types.Message, remainingBudget int) ([]types.Message, map[string]interface{}, error) {
	if len(messages) == 0 || loop == nil || loop.llmRuntime == nil {
		return messages, nil, nil
	}

	budget := resolvePromptPreflightBudget(loop.llmRuntime, loop.agent, remainingBudget)
	if budget.PromptBudget <= 0 {
		return messages, nil, nil
	}

	promptTokensBefore := loop.llmRuntime.CountMessagesTokens(messages)
	if promptTokensBefore <= 0 || promptTokensBefore <= budget.PromptBudget {
		return messages, nil, nil
	}

	startedPayload := budget.Metadata()
	if startedPayload == nil {
		startedPayload = map[string]interface{}{}
	}
	startedPayload["trace_id"] = traceID
	startedPayload["step"] = step
	startedPayload["prompt_tokens"] = promptTokensBefore
	startedPayload["message_count"] = len(messages)
	startedPayload["remaining_budget"] = remainingBudget
	startedPayload["active_turn_replay"] = true
	loop.agent.emitRuntimeEvent("context.preflight.started", sessionID, "", startedPayload)

	compactedMessages, compacted := historyguard.CompactActiveTurnReplayWithCounter(
		messages,
		historyguard.DefaultActiveTurnReplayMaxBytes,
		budget.PromptBudget,
		loop.llmRuntime.CountMessagesTokens,
	)

	preflightMetadata := budget.Metadata()
	if preflightMetadata == nil {
		preflightMetadata = map[string]interface{}{}
	}
	preflightMetadata["prompt_tokens_before"] = promptTokensBefore
	preflightMetadata["message_count_before"] = len(messages)

	if compacted {
		promptTokensAfter := loop.llmRuntime.CountMessagesTokens(compactedMessages)
		preflightMetadata["active_turn_compacted"] = true
		preflightMetadata["prompt_tokens_after"] = promptTokensAfter
		preflightMetadata["message_count_after"] = len(compactedMessages)

		compactedPayload := budget.Metadata()
		if compactedPayload == nil {
			compactedPayload = map[string]interface{}{}
		}
		compactedPayload["trace_id"] = traceID
		compactedPayload["step"] = step
		compactedPayload["prompt_tokens_before"] = promptTokensBefore
		compactedPayload["prompt_tokens_after"] = promptTokensAfter
		compactedPayload["message_count_before"] = len(messages)
		compactedPayload["message_count_after"] = len(compactedMessages)
		compactedPayload["remaining_budget"] = remainingBudget
		loop.agent.emitRuntimeEvent("context.preflight.compacted", sessionID, "", compactedPayload)
		if promptTokensAfter <= budget.PromptBudget {
			return compactedMessages, preflightMetadata, nil
		}

		failure := buildPromptPreflightFailure(
			"prompt_still_exceeds_budget_after_compaction",
			compactedMessages,
			promptTokensAfter,
			budget.PromptBudget,
		)
		failureErr := newPromptPreflightError(budget, failure, promptTokensAfter, true, compactedMessages)
		failedPayload := budget.Metadata()
		if failedPayload == nil {
			failedPayload = map[string]interface{}{}
		}
		failedPayload["trace_id"] = traceID
		failedPayload["step"] = step
		failedPayload["prompt_tokens"] = promptTokensAfter
		failedPayload["message_count"] = len(compactedMessages)
		failedPayload["remaining_budget"] = remainingBudget
		failedPayload["active_turn_compacted"] = true
		failedPayload["prompt_tokens_before"] = promptTokensBefore
		failedPayload["message_count_before"] = len(messages)
		for key, value := range failureErr.Metadata() {
			failedPayload[key] = value
			preflightMetadata[key] = value
		}
		loop.agent.emitRuntimeEvent("context.preflight.failed", sessionID, "", failedPayload)
		return nil, preflightMetadata, failureErr
	}

	preflightMetadata["active_turn_compacted"] = false
	failure := buildPromptPreflightFailure(
		"active_turn_not_compactable",
		messages,
		promptTokensBefore,
		budget.PromptBudget,
	)
	failureErr := newPromptPreflightError(budget, failure, promptTokensBefore, false, nil)
	failedPayload := budget.Metadata()
	if failedPayload == nil {
		failedPayload = map[string]interface{}{}
	}
	failedPayload["trace_id"] = traceID
	failedPayload["step"] = step
	failedPayload["prompt_tokens"] = promptTokensBefore
	failedPayload["message_count"] = len(messages)
	failedPayload["remaining_budget"] = remainingBudget
	failedPayload["active_turn_compacted"] = false
	for key, value := range failureErr.Metadata() {
		failedPayload[key] = value
		preflightMetadata[key] = value
	}
	loop.agent.emitRuntimeEvent("context.preflight.failed", sessionID, "", failedPayload)
	return nil, preflightMetadata, failureErr
}

func (loop *ReActLoop) trySessionCompactionRecovery(ctx context.Context, sessionID, traceID string, step int, history []types.Message, budgetMetadata map[string]interface{}) ([]types.Message, bool, error) {
	if loop == nil || loop.agent == nil || loop.llmRuntime == nil || len(history) == 0 {
		return nil, false, nil
	}

	runtime := compactruntime.New(loop.llmRuntime, loop.agent.GetContextManager())
	provider := ""
	model := ""
	if loop.agent.config != nil {
		provider = loop.agent.config.Provider
		model = loop.agent.config.Model
	}

	startedPayload := map[string]interface{}{
		"session_id":    sessionID,
		"trace_id":      traceID,
		"step":          step,
		"phase":         compactruntime.PhasePreTurn,
		"mode":          compactruntime.ModeLocal,
		"reason":        "prompt_preflight_recovery",
		"provider":      provider,
		"model":         model,
		"message_count": len(history),
		"token_before":  loop.llmRuntime.CountMessagesTokens(history),
	}
	for key, value := range budgetMetadata {
		if value == nil {
			continue
		}
		startedPayload[key] = value
	}
	if window := firstPositiveBudgetMetadataInt(
		budgetMetadata["context_window_tokens"],
		budgetMetadata["max_context_tokens"],
		budgetMetadata["model_capability_max_context_tokens"],
		budgetMetadata["provider_context_limit"],
	); window > 0 {
		startedPayload["context_window_tokens"] = window
	}
	loop.agent.emitRuntimeEvent("session_compact_started", sessionID, "", startedPayload)

	result, status, err := runtime.MaybeCompact(ctx, compactruntime.Request{
		SessionID: sessionID,
		TaskID:    sessionID,
		Provider:  provider,
		Model:     model,
		Mode:      compactruntime.ModeLocal,
		Force:     true,
		History:   cloneMessageHistory(history),
		Phase:     compactruntime.PhasePreTurn,
		CountTokens: func(messages []types.Message) int {
			return loop.llmRuntime.CountMessagesTokens(messages)
		},
	})
	if err != nil {
		failedPayload := cloneInterfaceMap(startedPayload)
		failedPayload["reason"] = firstNonEmptyTrimmed(status.Reason, "session_compaction_failed")
		failedPayload["error"] = err.Error()
		failedPayload["trigger_token_limit"] = status.TriggerTokenLimit
		failedPayload["max_context_tokens"] = status.MaxContextTokens
		loop.agent.emitRuntimeEvent("session_compact_failed", sessionID, "", failedPayload)
		return nil, false, err
	}
	if result == nil || len(result.ReplacementHistory) == 0 {
		skippedPayload := cloneInterfaceMap(startedPayload)
		skippedPayload["reason"] = firstNonEmptyTrimmed(status.Reason, "no_replacement_history")
		skippedPayload["trigger_token_limit"] = status.TriggerTokenLimit
		skippedPayload["max_context_tokens"] = status.MaxContextTokens
		loop.agent.emitRuntimeEvent("session_compact_skipped", sessionID, "", skippedPayload)
		return nil, false, nil
	}

	completedPayload := cloneInterfaceMap(startedPayload)
	completedPayload["mode"] = firstNonEmptyTrimmed(result.Mode, status.Mode)
	completedPayload["reason"] = firstNonEmptyTrimmed(status.Reason, "recovered")
	completedPayload["token_after"] = result.TokenAfter
	completedPayload["compacted_messages"] = result.CompactedMessages
	completedPayload["message_count_after"] = len(result.ReplacementHistory)
	completedPayload["trigger_token_limit"] = result.TriggerTokenLimit
	completedPayload["max_context_tokens"] = result.MaxContextTokens
	if result.Usage != nil {
		completedPayload["usage_prompt_tokens"] = result.Usage.PromptTokens
		completedPayload["usage_completion_tokens"] = result.Usage.CompletionTokens
		completedPayload["usage_total_tokens"] = result.Usage.TotalTokens
		if result.Usage.CachedTokens > 0 {
			completedPayload["usage_cached_tokens"] = result.Usage.CachedTokens
		}
		if result.Usage.ReasoningTokens > 0 {
			completedPayload["usage_reasoning_tokens"] = result.Usage.ReasoningTokens
		}
	}
	if result.UsageSource != "" {
		completedPayload["usage_source"] = result.UsageSource
	}
	if len(result.CheckpointIDs) > 0 {
		completedPayload["checkpoint_ids"] = append([]string(nil), result.CheckpointIDs...)
		completedPayload["checkpoint_id"] = result.CheckpointIDs[len(result.CheckpointIDs)-1]
	}
	loop.agent.emitRuntimeEvent("session_compact_completed", sessionID, "", completedPayload)

	return cloneMessageHistory(result.ReplacementHistory), true, nil
}

func firstPositiveBudgetMetadataInt(values ...interface{}) int {
	for _, value := range values {
		if number := intValue(value); number > 0 {
			return number
		}
	}
	return 0
}

func buildPromptPreflightFailure(code string, messages []types.Message, promptTokens, promptBudget int) promptPreflightFailure {
	failure := promptPreflightFailure{
		Code:                          strings.TrimSpace(code),
		ActiveTurnMessageCount:        activeTurnMessageCount(messages),
		LatestReplayBlockMessageCount: latestReplayBlockMessageCount(messages),
	}

	switch failure.Code {
	case "active_turn_not_compactable":
		failure.Reason = "active-turn replay cannot be compacted further"
		failure.Detail = fmt.Sprintf("prompt tokens %d exceed budget %d, and no earlier replay block remains available for compaction", promptTokens, promptBudget)
		failure.SuggestedAction = "请减少更早历史、提高 prompt 预算，或开启新的用户轮次。"
	case "prompt_still_exceeds_budget_after_compaction":
		failure.Reason = "prompt budget still exceeded after active-turn compaction"
		failure.Detail = fmt.Sprintf("prompt tokens %d still exceed budget %d after compacting older replay in the current turn", promptTokens, promptBudget)
		failure.SuggestedAction = "请继续收缩上下文层、提高预算，或从新的轮次继续。"
	default:
		failure.Reason = "prompt exceeds budget before send"
		failure.Detail = fmt.Sprintf("prompt tokens %d exceed budget %d before the provider request is sent", promptTokens, promptBudget)
		failure.SuggestedAction = "请减少 prompt 尺寸或降低上下文保留。"
	}

	return failure
}

func resolvePromptPreflightBudget(runtime *llm.LLMRuntime, agent *Agent, remainingBudget int) promptPreflightBudget {
	budget := promptPreflightBudget{}

	managerBudget := 0
	hasManagerBudget := false
	explicitContextBudget := hasPromptPreflightContextBudgetOverride(agent)
	if agent != nil {
		if manager := agent.GetContextManager(); manager != nil && manager.Budget.MaxPromptTokens > 0 {
			managerBudget = manager.Budget.MaxPromptTokens
			hasManagerBudget = true
		}
	}
	if hasManagerBudget && explicitContextBudget {
		budget.addCandidate(
			"context_max_prompt_tokens",
			managerBudget,
			"context manager budget max_prompt_tokens",
		)
	}

	hasResolvedPromptLimit := budget.PromptBudget > 0
	if hasManagerBudget && explicitContextBudget {
		hasResolvedPromptLimit = true
	}

	defaultPromptBudget := contextmgr.DefaultBudget().MaxPromptTokens
	addFallbackPromptBudget := func() {
		if hasManagerBudget {
			budget.addCandidate(
				"context_max_prompt_tokens",
				managerBudget,
				"context manager budget max_prompt_tokens fallback",
			)
			return
		}
		budget.addCandidate(
			"default_context_max_prompt_tokens",
			defaultPromptBudget,
			"contextmgr.DefaultBudget().MaxPromptTokens fallback",
		)
	}

	resolvedProvider, resolvedModel := resolvePromptPreflightProviderModel(runtime, agent)
	budget.ResolvedProvider = resolvedProvider
	budget.ResolvedModel = resolvedModel

	if provider := resolvePromptPreflightProvider(runtime, resolvedProvider, resolvedModel); provider != nil {
		if caps := provider.GetCapabilities(); caps != nil {
			budget.ProviderContextLimit = caps.MaxContextTokens
			budget.ProviderOutputLimit = caps.MaxOutputTokens
		}
	}

	if runtime != nil {
		resolvedCapabilityProvider, resolvedCapabilityModel, capability, ok := llm.ResolveRuntimeModelCapability(runtime, resolvedProvider, resolvedModel)
		if ok {
			if resolvedCapabilityProvider != "" {
				budget.ResolvedProvider = resolvedCapabilityProvider
			}
			if resolvedCapabilityModel != "" {
				budget.ResolvedModel = resolvedCapabilityModel
			}
			budget.ModelCapabilityMaxContextTokens = capability.MaxContextTokens
			budget.ModelCapabilityAutoCompactTokenLimit = capability.AutoCompactTokenLimit
			if capability.AutoCompactTokenLimit > 0 {
				value := capability.AutoCompactTokenLimit
				if capability.MaxContextTokens > 0 && value > capability.MaxContextTokens {
					value = capability.MaxContextTokens
				}
				budget.addCandidate(
					"model_capability_auto_compact_token_limit",
					value,
					"provider/model capability auto_compact_token_limit",
				)
				hasResolvedPromptLimit = true
			} else if capability.MaxContextTokens > 0 {
				ratio := capability.AutoCompactRatio
				if ratio <= 0 || ratio >= 1 {
					ratio = defaultPromptPreflightAutoCompactRatio
				}
				budget.ModelCapabilityAutoCompactRatio = ratio
				value := int(math.Floor(float64(capability.MaxContextTokens) * ratio))
				if value <= 0 || value > capability.MaxContextTokens {
					value = capability.MaxContextTokens
				}
				budget.addCandidate(
					"model_capability_context_ratio",
					value,
					fmt.Sprintf("floor(model capability max_context_tokens * %.2f)", ratio),
				)
				hasResolvedPromptLimit = true
			}
		} else if budget.ProviderContextLimit > 0 {
			value := int(math.Floor(float64(budget.ProviderContextLimit) * defaultPromptPreflightAutoCompactRatio))
			if value <= 0 || value > budget.ProviderContextLimit {
				value = budget.ProviderContextLimit
			}
			budget.addCandidate(
				"provider_context_limit_default_ratio",
				value,
				fmt.Sprintf("floor(provider max_context_tokens * %.2f)", defaultPromptPreflightAutoCompactRatio),
			)
			hasResolvedPromptLimit = true
		}
	}

	if !hasResolvedPromptLimit {
		addFallbackPromptBudget()
	}

	if remainingBudget > 0 {
		budget.addCandidate(
			"remaining_budget",
			remainingBudget,
			"remaining token budget for current run",
		)
	}

	return budget
}

func hasPromptPreflightContextBudgetOverride(agent *Agent) bool {
	if agent == nil || agent.config == nil || len(agent.config.Options) == 0 {
		return false
	}
	if _, ok := contextOptionInt(agent.config.Options, "context_max_prompt_tokens"); ok {
		return true
	}
	if profile, ok := agent.config.Options["context_profile"].(string); ok && strings.TrimSpace(profile) != "" {
		return true
	}
	return false
}

func resolvePromptPreflightProviderModel(runtime *llm.LLMRuntime, agent *Agent) (string, string) {
	providerName := ""
	model := ""
	if agent != nil && agent.config != nil {
		providerName = strings.TrimSpace(agent.config.Provider)
		model = strings.TrimSpace(agent.config.Model)
	}
	if runtime != nil {
		if resolved := runtime.ResolveProviderName(providerName); resolved != "" {
			providerName = resolved
		}
		if providerName == "" {
			providerName = runtime.ResolveProviderName(model)
		}
		if providerName == "" {
			providerName = strings.TrimSpace(runtime.DefaultProvider())
		}
		if model == "" {
			model = strings.TrimSpace(runtime.DefaultModel())
		}
	}
	return strings.TrimSpace(providerName), strings.TrimSpace(model)
}

func resolvePromptPreflightProvider(runtime *llm.LLMRuntime, providerName, model string) llm.Provider {
	if runtime == nil {
		return nil
	}
	if providerName != "" {
		if provider, err := runtime.GetProvider(providerName); err == nil && provider != nil {
			return provider
		}
	}
	if model != "" {
		if provider, err := runtime.GetProvider(model); err == nil && provider != nil {
			return provider
		}
	}
	return nil
}

func latestActiveTurnUserIndex(messages []types.Message) int {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == "user" {
			return index
		}
	}
	return -1
}

func activeTurnMessageCount(messages []types.Message) int {
	userIndex := latestActiveTurnUserIndex(messages)
	if userIndex < 0 || userIndex >= len(messages)-1 {
		return 0
	}
	return len(messages) - userIndex - 1
}

func latestReplayBlockMessageCount(messages []types.Message) int {
	userIndex := latestActiveTurnUserIndex(messages)
	if userIndex < 0 || userIndex >= len(messages)-1 {
		return 0
	}
	start := latestActiveTurnReplayBlockStart(messages, userIndex)
	if start < userIndex+1 || start > len(messages) {
		return 0
	}
	return len(messages) - start
}

func latestActiveTurnReplayBlockStart(messages []types.Message, userIndex int) int {
	if userIndex < 0 || userIndex >= len(messages)-1 {
		return len(messages)
	}

	index := len(messages) - 1
	for index > userIndex && messages[index].Role == "tool" {
		index--
	}
	if index <= userIndex {
		return userIndex + 1
	}
	if messages[index].Role == "assistant" && len(messages[index].ToolCalls) > 0 {
		return index
	}
	return index
}

func resolveLoopMaxTokens(defaultMaxTokens int, remainingBudget int) int {
	maxTokens := defaultMaxTokens
	if remainingBudget > 0 && (maxTokens <= 0 || remainingBudget < maxTokens) {
		maxTokens = remainingBudget
	}
	return maxTokens
}

func generatedImageOutputDirForAgentSession(agent *Agent, sessionID string) string {
	sessionID = sanitizeGeneratedImageSessionID(sessionID)
	if sessionID == "" {
		return ""
	}
	if artifactStorePath := resolveAgentArtifactStorePath(agent); artifactStorePath != "" {
		return filepath.Join(filepath.Dir(artifactStorePath), "generated-images", sessionID)
	}
	return filepath.Join(os.TempDir(), "ai-agent-runtime", "generated-images", sessionID)
}

func resolveAgentArtifactStorePath(agent *Agent) string {
	if agent == nil || agent.config == nil {
		return ""
	}
	if path := strings.TrimSpace(agent.config.ArtifactStorePath); path != "" {
		return path
	}
	if len(agent.config.Options) == 0 {
		return ""
	}
	if path, ok := agent.config.Options["artifact_store_path"].(string); ok {
		return strings.TrimSpace(path)
	}
	return ""
}

func sanitizeGeneratedImageSessionID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var builder strings.Builder
	for _, ch := range value {
		switch {
		case ch >= 'a' && ch <= 'z':
			builder.WriteRune(ch)
		case ch >= 'A' && ch <= 'Z':
			builder.WriteRune(ch)
		case ch >= '0' && ch <= '9':
			builder.WriteRune(ch)
		case ch == '-' || ch == '_':
			builder.WriteRune(ch)
		default:
			builder.WriteByte('_')
		}
	}
	return strings.Trim(builder.String(), "_")
}

func decodeSubagentTasks(args map[string]interface{}) ([]SubagentTask, error) {
	rawTasks, ok := args["agents"]
	if !ok {
		return nil, fmt.Errorf("spawn_subagents missing agents")
	}

	items, ok := rawTasks.([]interface{})
	if !ok {
		return nil, fmt.Errorf("spawn_subagents agents must be an array")
	}

	tasks := make([]SubagentTask, 0, len(items))
	for index, rawItem := range items {
		item, ok := rawItem.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("spawn_subagents agent %d is invalid", index)
		}
		task := SubagentTask{
			ID:           stringValue(item["id"]),
			Role:         stringValue(item["role"]),
			Goal:         stringValue(item["goal"]),
			Model:        stringValue(item["model"]),
			BudgetTokens: intValue(item["budget_tokens"]),
			TimeoutSec:   intValue(item["timeout"]),
			ReadOnly:     boolValue(item["read_only"]),
		}
		task.ToolsWhitelist = stringSliceValue(item["tools_whitelist"])
		task.DependsOn = stringSliceValue(item["depends_on"])
		task.PatchContext = filePatchSliceValue(item["patches"])
		if task.ID == "" {
			task.ID = fmt.Sprintf("subagent_%d", index+1)
		}
		if task.Goal == "" {
			return nil, fmt.Errorf("spawn_subagents agent %d missing goal", index)
		}
		tasks = append(tasks, task)
	}

	return tasks, nil
}

func renderSubagentResults(results []SubagentResult) string {
	if len(results) == 0 {
		return "No subagent results were produced."
	}

	lines := []string{"Subagent reports:"}
	for _, result := range results {
		status := "failed"
		if result.Success {
			status = "succeeded"
		}
		line := fmt.Sprintf("- %s (%s): %s", firstNonEmptySubagentValue(result.ID, "subagent"), status, result.Summary)
		lines = append(lines, line)
		for _, finding := range result.Findings {
			lines = append(lines, "  finding: "+finding)
		}
		for _, patch := range result.Patches {
			patchLine := "  patch"
			if patch.Path != "" {
				patchLine += ": " + patch.Path
			}
			if patch.Summary != "" {
				patchLine += " - " + patch.Summary
			}
			if patch.ApplyStatus != "" {
				patchLine += " [apply=" + patch.ApplyStatus + "]"
			}
			if len(patch.AppliedBy) > 0 {
				patchLine += " by " + strings.Join(patch.AppliedBy, ", ")
			}
			if patch.VerificationStatus != "" {
				patchLine += " [verify=" + patch.VerificationStatus + "]"
			}
			if len(patch.VerifiedBy) > 0 {
				patchLine += " via " + strings.Join(patch.VerifiedBy, ", ")
			}
			lines = append(lines, patchLine)
		}
		if result.Error != "" {
			lines = append(lines, "  error: "+result.Error)
		}
	}
	return strings.Join(lines, "\n")
}

func joinFailureMessages(messages []string) string {
	if len(messages) == 0 {
		return ""
	}
	ordered := make([]string, 0, len(messages))
	seen := make(map[string]bool, len(messages))
	for _, message := range messages {
		message = strings.TrimSpace(message)
		if message == "" || seen[message] {
			continue
		}
		seen[message] = true
		ordered = append(ordered, message)
	}
	return strings.Join(ordered, "; ")
}

func optionString(options map[string]interface{}, key string) string {
	if len(options) == 0 {
		return ""
	}
	raw, ok := options[key]
	if !ok {
		return ""
	}
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case fmt.Stringer:
		return strings.TrimSpace(value.String())
	default:
		return ""
	}
}

func optionMap(options map[string]interface{}, key string) map[string]interface{} {
	if len(options) == 0 {
		return nil
	}
	raw, ok := options[key]
	if !ok {
		return nil
	}
	value, ok := raw.(map[string]interface{})
	if !ok || len(value) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(value))
	for childKey, childValue := range value {
		cloned[childKey] = cloneOptionValue(childValue)
	}
	return cloned
}

func optionValue(options map[string]interface{}, key string) interface{} {
	if len(options) == 0 {
		return nil
	}
	value, ok := options[key]
	if !ok {
		return nil
	}
	return value
}

func permissionModeFromContext(ctx context.Context) runtimepolicy.Mode {
	meta, ok := team.GetRunMeta(ctx)
	if !ok || meta == nil {
		return ""
	}
	return runtimepolicy.Mode(strings.TrimSpace(meta.PermissionMode))
}

func cloneOptionValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		cloned := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			cloned[key] = cloneOptionValue(item)
		}
		return cloned
	case []interface{}:
		cloned := make([]interface{}, len(typed))
		for index, item := range typed {
			cloned[index] = cloneOptionValue(item)
		}
		return cloned
	case []string:
		cloned := make([]string, len(typed))
		copy(cloned, typed)
		return cloned
	default:
		return typed
	}
}

func spawnSubagentsToolDefinition() types.ToolDefinition {
	return types.ToolDefinition{
		Name:        "spawn_subagents",
		Description: "Spawn isolated subagents for parallel subtasks. Use only when tasks are independent.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"agents": map[string]interface{}{
					"type": "array",
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"id":              map[string]interface{}{"type": "string"},
							"role":            map[string]interface{}{"type": "string"},
							"goal":            map[string]interface{}{"type": "string"},
							"tools_whitelist": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
							"depends_on":      map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
							"patches": map[string]interface{}{
								"type": "array",
								"items": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"path":    map[string]interface{}{"type": "string"},
										"summary": map[string]interface{}{"type": "string"},
										"diff":    map[string]interface{}{"type": "string"},
									},
								},
							},
							"model":         map[string]interface{}{"type": "string"},
							"budget_tokens": map[string]interface{}{"type": "integer"},
							"timeout":       map[string]interface{}{"type": "integer"},
							"read_only":     map[string]interface{}{"type": "boolean"},
						},
						"required": []string{"goal"},
					},
				},
			},
			"required": []string{"agents"},
		},
	}
}

func summarizeToolAvailability(tools []types.ToolDefinition) map[string]interface{} {
	if len(tools) == 0 {
		return nil
	}
	requiresActiveTeamRun := make([]string, 0, 4)
	deferredTools := make([]string, 0, 4)
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" || len(tool.Metadata) == 0 {
			continue
		}
		if availability, _ := tool.Metadata["availability"].(string); strings.EqualFold(strings.TrimSpace(availability), "requires_active_team_run") {
			requiresActiveTeamRun = append(requiresActiveTeamRun, name)
		}
		if deferred, _ := tool.Metadata["defer_loading"].(bool); deferred {
			deferredTools = append(deferredTools, name)
		}
	}
	if len(requiresActiveTeamRun) == 0 && len(deferredTools) == 0 {
		return nil
	}
	summary := make(map[string]interface{}, 2)
	if len(requiresActiveTeamRun) > 0 {
		summary["requires_active_team_run"] = requiresActiveTeamRun
	}
	if len(deferredTools) > 0 {
		summary["deferred_tools"] = deferredTools
	}
	return summary
}

func stringValue(value interface{}) string {
	text, _ := value.(string)
	return text
}

func intValue(value interface{}) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float32:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func boolValue(value interface{}) bool {
	flag, _ := value.(bool)
	return flag
}

func stringSliceValue(value interface{}) []string {
	items, ok := value.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok && text != "" {
			out = append(out, text)
		}
	}
	return out
}

func filePatchSliceValue(value interface{}) []FilePatch {
	items, ok := value.([]interface{})
	if !ok {
		return nil
	}
	patches := make([]FilePatch, 0, len(items))
	for _, item := range items {
		patchMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		patch := FilePatch{
			Path:               stringValue(patchMap["path"]),
			Summary:            stringValue(patchMap["summary"]),
			Diff:               stringValue(patchMap["diff"]),
			ApplyStatus:        stringValue(patchMap["apply_status"]),
			AppliedBy:          stringSliceValue(patchMap["applied_by"]),
			ArtifactRefs:       stringSliceValue(patchMap["artifact_refs"]),
			VerificationStatus: stringValue(patchMap["verification_status"]),
			VerifiedBy:         stringSliceValue(patchMap["verified_by"]),
		}
		if patch.Path == "" && patch.Summary == "" && patch.Diff == "" {
			continue
		}
		patches = append(patches, patch)
	}
	return patches
}

func firstNonEmptySubagentValue(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
