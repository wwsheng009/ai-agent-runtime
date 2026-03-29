package agent

import (
	"context"
	"fmt"
	"strings"

	runtimecheckpoint "github.com/wwsheng009/ai-agent-runtime/internal/checkpoint"
	"github.com/wwsheng009/ai-agent-runtime/internal/contextmgr"
	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimehooks "github.com/wwsheng009/ai-agent-runtime/internal/hooks"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/output"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/google/uuid"
)

const emptyTerminalAssistantResponseError = "upstream model returned an empty reply: no text and no tool calls"

// LoopReActConfig ReAct 循环配置
type LoopReActConfig struct {
	MaxSteps        int     `yaml:"maxSteps"`
	EnableThought   bool    `yaml:"enableThought"`
	EnableToolCalls bool    `yaml:"enableToolCalls"`
	Verbose         bool    `yaml:"verbose"`
	Temperature     float64 `yaml:"temperature"`
	ReasoningEffort string  `yaml:"reasoningEffort"`
	Thinking        *types.ThinkingConfig `yaml:"thinking"`
	StopOnSuccess   bool    `yaml:"stopOnSuccess"`
	MaxIterations   int     `yaml:"maxIterations"`
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
			MaxSteps:        10,
			EnableThought:   true,
			EnableToolCalls: true,
			Verbose:         false,
			Temperature:     0.7,
			StopOnSuccess:   true,
			MaxIterations:   10,
		}
	}
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

	// 添加系统提示词
	if loop.agent.config.SystemPrompt != "" && !hasLeadingSystemPrompt(history, loop.agent.config.SystemPrompt) {
		systemMsg := types.NewSystemMessage(loop.agent.config.SystemPrompt)
		builder = NewMessageBuilder(append([]types.Message{*systemMsg}, history...))
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

	// ReAct 循环：Think - Act - Observe
	for step := 1; step <= loop.config.MaxSteps; step++ {
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
		thought, action, usage, err := loop.think(ctx, traceID, sessionID, prompt, builder.Messages(), observations, options.ToolWhitelist, remainingBudget)
		if err != nil {
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
			builder.AppendAssistantAction(action.Content, nil)
			if err := persistBuilderHistory(builder, options.PersistHistory); err != nil {
				return nil, err
			}

			result.Success = !hadToolFailure
			result.Output = action.Content
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
		normalizedCalls := builder.AppendAssistantAction(action.Content, action.ToolCalls)
		historySnapshot := builder.Messages()
		toolResults, err := loop.act(ctx, traceID, sessionID, step, options.Depth, historySnapshot, normalizedCalls, options.ToolWhitelist)
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
			if step < loop.config.MaxSteps {
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
		observations = loop.observe(ctx, toolResults, observations, step)

		// 4. 更新对话历史
		builder.AppendToolResults(normalizedCalls, toolResultsToPayloads(toolResults))
		if err := persistBuilderHistory(builder, options.PersistHistory); err != nil {
			return nil, err
		}

		if loop.config.Verbose {
			fmt.Printf("[Step %d] Completed %d tool calls\n", step, len(toolResults))
		}
	}

	result.Success = !hadToolFailure
	result.Output = "Reached maximum iterations. Consider increasing max steps."
	result.Steps = loop.config.MaxSteps
	result.Observations = observations
	result.Duration = *startTime
	result.Usage = totalUsage.Clone()
	if hadToolFailure && len(failureMessages) > 0 {
		result.Error = joinFailureMessages(failureMessages)
	}

	return result, nil
}

// think 思考阶段：让 LLM 决定下一步行动
func (loop *ReActLoop) think(ctx context.Context, traceID, sessionID, goal string, history []types.Message, observations []types.Observation, toolWhitelist []string, remainingBudget int) (thought string, action *AgentAction, usage *types.TokenUsage, err error) {
	action = &AgentAction{}
	managedHistory := history
	if manager := loop.agent.GetContextManager(); manager != nil {
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
			CountTokens: func(messages []types.Message) int {
				if loop.llmRuntime == nil {
					return 0
				}
				return loop.llmRuntime.CountMessagesTokens(messages)
			},
		})
		managedHistory = built.Messages
		action.Metadata = map[string]interface{}{
			"context": built.Metadata,
		}
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
	if loop.agent != nil && loop.agent.config != nil && boolValue(optionValue(loop.agent.config.Options, "stream")) {
		req.Stream = true
	}
	callCtx := ctx
	if req.Stream {
		callCtx = llm.WithStreamReporter(ctx, func(chunk llm.StreamChunk) {
			if chunk.Type != llm.EventTypeText || chunk.Content == "" {
				return
			}
			loop.agent.emitRuntimeEvent("assistant_delta", sessionID, "", map[string]interface{}{
				"trace_id": traceID,
				"content":  chunk.Content,
				"delta":    chunk.Content,
			})
		})
	}

	// 添加工具定义（如果启用了工具调用）
	if loop.config.EnableToolCalls {
		// 获取可用工具
		tools, err := loop.getAvailableTools(goal, toolWhitelist)
		if err != nil {
			return "", nil, nil, err
		}
		req.Tools = tools
	}

	// 调用 LLM
	loop.agent.emitRuntimeEvent("llm.request.started", sessionID, "", map[string]interface{}{
		"trace_id":         traceID,
		"model":            req.Model,
		"provider":         req.Provider,
		"message_count":    len(req.Messages),
		"tool_count":       len(req.Tools),
		"remaining_budget": remainingBudget,
	})
	response, err := loop.llmRuntime.Call(callCtx, req)
	if err != nil {
		loop.agent.emitRuntimeEvent("llm.request.finished", sessionID, "", map[string]interface{}{
			"trace_id": traceID,
			"model":    req.Model,
			"provider": req.Provider,
			"success":  false,
			"error":    err.Error(),
		})
		return "", nil, nil, err
	}
	loop.agent.emitRuntimeEvent("llm.request.finished", sessionID, "", map[string]interface{}{
		"trace_id":        traceID,
		"model":           req.Model,
		"provider":        req.Provider,
		"success":         true,
		"tool_call_count": len(response.ToolCalls),
	})

	// 解析响应
	action.Content = response.Content
	action.ToolCalls = response.ToolCalls
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
		loop.agent.emitRuntimeEvent("tool.requested", sessionID, tc.Name, map[string]interface{}{
			"tool_call_id": tc.ID,
			"step":         step,
			"trace_id":     traceID,
		})
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
				decision, evalErr := engine.Evaluate(ctx, runtimepolicy.EvalRequest{
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
			rawOutput, rawMeta, callErr = broker.ExecuteToolCall(ctx, sessionID, tc)
			if callErr != nil {
				result.Error = callErr.Error()
			} else {
				result.Output = rawOutput
				if len(rawMeta) > 0 {
					metadata["tool_metadata"] = cloneInterfaceMap(rawMeta)
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
			loop.agent.emitRuntimeEvent("tool.completed", sessionID, tc.Name, map[string]interface{}{
				"tool_call_id": tc.ID,
				"step":         step,
				"error":        result.Error,
				"trace_id":     traceID,
			})
			loop.emitToolReduced(sessionID, tc, step, traceID, result, nil)
			results[i] = result
			loop.agent.runPostToolUseHooks(ctx, sessionID, result)
			continue
		}

		if tc.Name == "spawn_subagents" {
			if engine != nil {
				decision, evalErr := engine.Evaluate(ctx, runtimepolicy.EvalRequest{
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
			decision, evalErr := engine.Evaluate(ctx, runtimepolicy.EvalRequest{
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
		if err != nil {
			result.Error = err.Error()
		} else {
			result.Output = rawOutput
			if len(rawMeta) > 0 {
				metadata["tool_metadata"] = cloneInterfaceMap(rawMeta)
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
		loop.agent.emitRuntimeEvent("tool.completed", sessionID, tc.Name, map[string]interface{}{
			"tool_call_id": tc.ID,
			"step":         step,
			"error":        result.Error,
			"trace_id":     traceID,
		})
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
func (loop *ReActLoop) getAvailableTools(goal string, toolWhitelist []string) ([]types.ToolDefinition, error) {
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
			tools = append(tools, types.ToolDefinition{
				Name:        mt.Name,
				Description: mt.Description,
				Parameters:  normalizeToolParameters(mt.InputSchema),
			})
		}
	}

	if scheduler := loop.agent.GetSubagentScheduler(); scheduler != nil {
		if (len(allowed) == 0 || allowed["spawn_subagents"]) &&
			(loop.agent.GetToolExecutionPolicy() == nil || loop.agent.GetToolExecutionPolicy().AllowsDefinition("spawn_subagents")) {
			tools = append(tools, spawnSubagentsToolDefinition())
		}
	}

	if broker := loop.agent.GetToolBroker(); broker != nil {
		for _, def := range broker.Definitions() {
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
	Content   string                 `json:"content" yaml:"content"`
	ToolCalls []types.ToolCall       `json:"toolCalls,omitempty" yaml:"toolCalls,omitempty"`
	Thought   string                 `json:"thought,omitempty" yaml:"thought,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty" yaml:"metadata,omitempty"`
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
		if result.Envelope != nil {
			payload.Content = result.Envelope.Render()
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

func resolveLoopMaxTokens(defaultMaxTokens int, remainingBudget int) int {
	maxTokens := defaultMaxTokens
	if remainingBudget > 0 && (maxTokens <= 0 || remainingBudget < maxTokens) {
		maxTokens = remainingBudget
	}
	return maxTokens
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
