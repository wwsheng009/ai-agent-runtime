package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

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
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const emptyTerminalAssistantResponseError = "upstream model returned an empty reply: no text and no tool calls"
const repeatedSemanticToolCallNoticeThreshold = 4
const explorationStallNoticeThreshold = 12
const defaultPromptPreflightAutoCompactRatio = 0.9

var errReActRunTimeout = stderrors.New("ReAct run duration limit reached")

// LoopReActConfig ReAct 循环配置
type LoopReActConfig struct {
	MaxSteps             int                   `yaml:"maxSteps"`
	MaxToolCalls         int                   `yaml:"maxToolCalls"`
	MaxRunDuration       time.Duration         `yaml:"maxRunDuration"`
	MaxExplorationSteps  int                   `yaml:"maxExplorationSteps"`
	MaxRepeatedToolCalls int                   `yaml:"maxRepeatedToolCalls"`
	EnableThought        bool                  `yaml:"enableThought"`
	EnableToolCalls      bool                  `yaml:"enableToolCalls"`
	EnableParallelTools  bool                  `yaml:"enableParallelTools"`
	MaxParallelToolCalls int                   `yaml:"maxParallelToolCalls"`
	Verbose              bool                  `yaml:"verbose"`
	Temperature          float64               `yaml:"temperature"`
	Provider             string                `yaml:"provider"`
	Model                string                `yaml:"model"`
	ReasoningEffort      string                `yaml:"reasoningEffort"`
	Thinking             *types.ThinkingConfig `yaml:"thinking"`
	StopOnSuccess        bool                  `yaml:"stopOnSuccess"`
	MaxIterations        int                   `yaml:"maxIterations"`
}

// ReActLoop ReAct 循环（Reasoning + Acting）
type ReActLoop struct {
	agent                        *Agent
	llmRuntime                   *llm.LLMRuntime
	config                       *LoopReActConfig
	parallelToolCallsUnsupported atomic.Bool
	reasoningEffortUnsupported   atomic.Bool
	thinkingUnsupported          atomic.Bool
	temperatureUnsupported       atomic.Bool
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

type historySessionContextReader interface {
	GetContext(key string) (interface{}, bool)
}

// NewReActLoop 创建 ReAct 循环
func NewReActLoop(agent *Agent, llmRuntime *llm.LLMRuntime, config *LoopReActConfig) *ReActLoop {
	if config == nil {
		config = &LoopReActConfig{
			MaxSteps:             0,
			EnableThought:        true,
			EnableToolCalls:      true,
			EnableParallelTools:  true,
			MaxParallelToolCalls: 4,
			Verbose:              false,
			Temperature:          0.7,
			StopOnSuccess:        true,
			MaxIterations:        10,
		}
	}
	config.MaxSteps = NormalizeMaxSteps(config.MaxSteps)
	if config.MaxParallelToolCalls <= 0 {
		config.MaxParallelToolCalls = 1
	}
	if parallelToolsDisabledByEnv() {
		config.EnableParallelTools = false
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

func (loop *ReActLoop) applyRememberedProviderRequestDowngrades(req *llm.LLMRequest) {
	if loop == nil || req == nil {
		return
	}
	if loop.reasoningEffortUnsupported.Load() {
		req.ReasoningEffort = ""
	}
	if loop.thinkingUnsupported.Load() {
		req.Thinking = nil
	}
	if loop.temperatureUnsupported.Load() {
		req.Temperature = 0
	}
}

func (loop *ReActLoop) downgradeUnsupportedProviderRequest(req *llm.LLMRequest, err error) string {
	if loop == nil || req == nil || err == nil {
		return ""
	}
	if req.Metadata[llm.MetadataKeyParallelToolCalls] == true && llm.IsUnsupportedRequestParameter(err, llm.MetadataKeyParallelToolCalls) {
		loop.parallelToolCallsUnsupported.Store(true)
		delete(req.Metadata, llm.MetadataKeyParallelToolCalls)
		delete(req.Metadata, "max_parallel_tool_calls")
		return llm.MetadataKeyParallelToolCalls
	}
	if strings.TrimSpace(req.ReasoningEffort) != "" &&
		(llm.IsUnsupportedRequestParameter(err, "reasoning_effort") || llm.IsUnsupportedRequestParameter(err, "reasoning")) {
		loop.reasoningEffortUnsupported.Store(true)
		req.ReasoningEffort = ""
		return "reasoning_effort"
	}
	if req.Thinking != nil && llm.IsUnsupportedRequestParameter(err, "thinking") {
		loop.thinkingUnsupported.Store(true)
		req.Thinking = nil
		return "thinking"
	}
	if req.Temperature != 0 && llm.IsUnsupportedRequestParameter(err, "temperature") {
		loop.temperatureUnsupported.Store(true)
		req.Temperature = 0
		return "temperature"
	}
	return ""
}

func (loop *ReActLoop) requestProvider() string {
	if loop != nil && loop.config != nil {
		if provider := strings.TrimSpace(loop.config.Provider); provider != "" {
			return provider
		}
	}
	if loop != nil && loop.agent != nil && loop.agent.config != nil {
		return strings.TrimSpace(loop.agent.config.Provider)
	}
	return ""
}

func (loop *ReActLoop) requestModel() string {
	if loop != nil && loop.config != nil {
		if model := strings.TrimSpace(loop.config.Model); model != "" {
			return model
		}
	}
	if loop != nil && loop.agent != nil && loop.agent.config != nil {
		return strings.TrimSpace(loop.agent.config.Model)
	}
	return ""
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

	ctx = toolctx.WithGoalID(ctx, sessionGoalID(session))
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

	ctx = toolctx.WithGoalID(ctx, sessionGoalID(session))
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

func sessionGoalID(session HistorySession) string {
	reader, ok := session.(historySessionContextReader)
	if !ok {
		return ""
	}
	raw, ok := reader.GetContext("aicli.goal")
	if !ok || raw == nil {
		return ""
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return ""
	}
	var owner struct {
		GoalID string `json:"goal_id"`
	}
	if err := json.Unmarshal(data, &owner); err != nil {
		return ""
	}
	return strings.TrimSpace(owner.GoalID)
}

func (loop *ReActLoop) run(ctx context.Context, prompt string, options loopRunOptions) (result *Result, runErr error) {
	defer func() {
		ensureAgentResultContract(result, prompt)
	}()
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
	var observations []types.Observation
	currentCtx := ensureTurnToolSurfaceSnapshot(ctx)
	if loop.config.MaxRunDuration > 0 {
		var runCancel context.CancelFunc
		currentCtx, runCancel = context.WithTimeoutCause(currentCtx, loop.config.MaxRunDuration, errReActRunTimeout)
		defer runCancel()
	}

	defer func() {
		startTime.StopTimer()
		loop.agent.SetRunning(false)
		if result == nil {
			return
		}
		if result.Steps <= 0 {
			result.Steps = loop.agent.state.CurrentStep
		}
		if result.Duration.GetDuration() <= 0 {
			result.Duration = *startTime
		}
		if runErr != nil && strings.TrimSpace(result.Error) == "" {
			result.Error = runErr.Error()
		}
		if stderrors.Is(context.Cause(currentCtx), errReActRunTimeout) {
			result.LimitReached = true
			result.LimitReason = "run_timeout"
		} else if (stderrors.Is(runErr, context.Canceled) || stderrors.Is(runErr, context.DeadlineExceeded)) && result.LimitReason == "" {
			result.LimitReason = "canceled"
		}
		if len(result.Observations) == 0 && len(observations) > 0 {
			result.Observations = observations
		}
		if runErr != nil && strings.TrimSpace(result.Output) == "" &&
			(stderrors.Is(runErr, context.Canceled) || stderrors.Is(runErr, context.DeadlineExceeded)) {
			result.Output = fmt.Sprintf("当前运行已停止；已保留 %d 条工具观察，可从现有会话继续。", len(result.Observations))
		}
		result.State = loop.agent.GetState()
	}()

	result = &Result{
		State: loop.agent.GetState(),
	}
	totalUsage := &types.TokenUsage{}

	// 构建初始对话历史
	history := cloneMessageHistory(options.History)
	if options.IncludePrompt {
		history = append(history, *types.NewUserMessage(prompt))
	}
	history = mergeConfiguredSystemPrompt(history, loop.agent.config.SystemPrompt)
	builder := NewMessageBuilder(history)
	// Keep a compact prompt view separate from the full durable audit history.
	// Once preflight compacts a long active turn, later model calls build on that
	// view instead of reprocessing the same raw replay on every step.
	promptBuilder := NewMessageBuilder(history)

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
	sessionCompactionRecoveryStep := 0
	sessionCompactionRecoveryInputs := make(map[string]struct{})
	lastToolPromptFingerprint := ""
	repeatedToolPromptFingerprint := 0
	lastSemanticToolFingerprint := ""
	repeatedSemanticToolCalls := 0
	consecutiveExplorationSteps := 0
	totalToolCalls := 0

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
		if err := currentCtx.Err(); err != nil {
			result.Success = false
			result.Error = err.Error()
			result.LimitReached = stderrors.Is(context.Cause(currentCtx), errReActRunTimeout)
			if result.LimitReached {
				result.LimitReason = "run_timeout"
			} else {
				result.LimitReason = "canceled"
			}
			result.Steps = step - 1
			result.Observations = observations
			result.Duration = *startTime
			result.Usage = totalUsage.Clone()
			return result, err
		}

		if loop.config.Verbose {
			fmt.Printf("[Step %d] Starting ReAct iteration\n", step)
		}
		if step == 1 {
			compactedHistory, compactUsage, compacted, compactErr := loop.tryActiveTurnSemanticCompaction(
				currentCtx,
				sessionID,
				traceID,
				step,
				promptBuilder.Messages(),
				0,
				nil,
				compactruntime.PhasePreTurn,
				"active_turn_start_context_limit",
			)
			if compactErr != nil {
				if ctxErr := currentCtx.Err(); ctxErr != nil {
					return result, ctxErr
				}
			} else if compacted {
				promptBuilder = NewMessageBuilder(compactedHistory)
				totalUsage.Add(compactUsage)
				result.Usage = totalUsage.Clone()
				if options.BudgetTokens > 0 && compactUsage != nil {
					remainingBudget -= compactUsage.TotalTokens
				}
			}
		}

		// 1. Think: LLM 推理决定下一步行动
		thought, action, usage, err := loop.think(currentCtx, traceID, sessionID, step, prompt, promptBuilder.Messages(), observations, options.ToolWhitelist, remainingBudget)
		if err != nil {
			recoveryHistory := builder.Messages()
			recoveryMetadata := map[string]interface{}(nil)
			recoveryKind := ""
			if preflightErr, ok := AsPromptPreflightError(err); ok {
				if replacement := preflightErr.CloneReplacementHistory(); len(replacement) > 0 {
					recoveryHistory = replacement
				}
				recoveryMetadata = preflightErr.Metadata()
				recoveryKind = "prompt preflight"
			} else if llm.IsContextWindowError(err) {
				recoveryMetadata = map[string]interface{}{
					"reason":         "provider_context_window_recovery",
					"provider_error": err.Error(),
				}
				recoveryKind = "provider context window"
			}
			recoveryFingerprint := promptMessageFingerprint(recoveryHistory)
			if sessionCompactionRecoveryStep != step {
				sessionCompactionRecoveryStep = step
				clear(sessionCompactionRecoveryInputs)
			}
			if recoveryKind != "" && markSessionCompactionRecoveryInput(sessionCompactionRecoveryInputs, recoveryFingerprint) {
				recoveredHistory, recovered, recoveryErr := loop.trySessionCompactionRecovery(currentCtx, sessionID, traceID, step, recoveryHistory, recoveryMetadata)
				if recoveryErr != nil {
					loop.agent.AddError(fmt.Sprintf("session compaction recovery failed after %s error: %v", recoveryKind, recoveryErr))
				} else if recovered && compactionRecoveryMadeProgress(loop.llmRuntime, recoveryHistory, recoveredHistory) {
					if options.PersistHistory != nil {
						if persistErr := options.PersistHistory(recoveredHistory); persistErr != nil {
							loop.agent.AddError(fmt.Sprintf("persist compacted history after %s error failed: %v", recoveryKind, persistErr))
							result.Error = err.Error()
							result.Usage = totalUsage.Clone()
							result.State = loop.agent.GetState()
							return result, fmt.Errorf("%w: persisted compacted history failed: %v", err, persistErr)
						}
					}
					builder = NewMessageBuilder(recoveredHistory)
					promptBuilder = NewMessageBuilder(recoveredHistory)
					step--
					continue
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
		if len(action.promptHistory) > 0 {
			promptBuilder = NewMessageBuilder(action.promptHistory)
		}
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
		if loop.config.MaxToolCalls > 0 && totalToolCalls+len(action.ToolCalls) > loop.config.MaxToolCalls {
			result.Success = false
			result.LimitReached = true
			result.LimitReason = "tool_calls"
			result.ToolCallLimit = loop.config.MaxToolCalls
			result.Output = toolCallLimitReachedMessage(loop.config.MaxToolCalls)
			result.Error = result.Output
			result.Steps = step
			result.Observations = observations
			result.Duration = *startTime
			result.Usage = totalUsage.Clone()
			builder.AppendAssistantAction(result.Output, nil, nil, nil)
			if err := persistBuilderHistory(builder, options.PersistHistory); err != nil {
				return nil, err
			}
			return result, nil
		}
		repeatedSemanticAdvisory := ""
		if fingerprint := semanticToolCallFingerprint(action.ToolCalls); fingerprint != "" {
			if fingerprint == lastSemanticToolFingerprint {
				repeatedSemanticToolCalls++
			} else {
				lastSemanticToolFingerprint = fingerprint
				repeatedSemanticToolCalls = 1
			}
			if repeatedSemanticToolCalls == repeatedSemanticToolCallNoticeThreshold {
				loop.agent.emitRuntimeEvent("tool_loop.repeated_semantic_call_observed", sessionID, "", map[string]interface{}{
					"trace_id":              traceID,
					"step":                  step,
					"tool_call_fingerprint": fingerprint,
					"repeat_count":          repeatedSemanticToolCalls,
					"tool_call_count":       len(action.ToolCalls),
				})
			}
			if repeatedSemanticToolCalls >= 2 {
				repeatedSemanticAdvisory = repeatedSemanticToolCallAdvisory(repeatedSemanticToolCalls)
			}
		} else {
			lastSemanticToolFingerprint = ""
			repeatedSemanticToolCalls = 0
		}
		consecutiveExplorationSteps = nextExplorationStallCount(consecutiveExplorationSteps, action.ToolCalls)
		if consecutiveExplorationSteps == explorationStallNoticeThreshold {
			loop.agent.emitRuntimeEvent("tool_loop.exploration_stall_observed", sessionID, "", map[string]interface{}{
				"trace_id":                   traceID,
				"step":                       step,
				"consecutive_readonly_steps": consecutiveExplorationSteps,
				"tool_call_count":            len(action.ToolCalls),
			})
		}
		if consecutiveExplorationSteps >= explorationStallNoticeThreshold {
			repeatedSemanticAdvisory = joinRuntimeAdvisories(
				repeatedSemanticAdvisory,
				explorationStallAdvisory(consecutiveExplorationSteps),
			)
		}
		if loop.config.MaxExplorationSteps > 0 && consecutiveExplorationSteps >= loop.config.MaxExplorationSteps {
			result.Success = false
			result.LimitReached = true
			result.LimitReason = "exploration_stall"
			result.Output = explorationLimitReachedMessage(loop.config.MaxExplorationSteps)
			result.Error = result.Output
			result.Steps = step
			result.Observations = observations
			result.Duration = *startTime
			result.Usage = totalUsage.Clone()
			builder.AppendAssistantAction(result.Output, nil, nil, nil)
			if err := persistBuilderHistory(builder, options.PersistHistory); err != nil {
				return nil, err
			}
			return result, nil
		}
		if loop.config.MaxRepeatedToolCalls > 0 && repeatedSemanticToolCalls >= loop.config.MaxRepeatedToolCalls {
			result.Success = false
			result.LimitReached = true
			result.LimitReason = "repeated_tool_calls"
			result.Output = repeatedToolLimitReachedMessage(loop.config.MaxRepeatedToolCalls)
			result.Error = result.Output
			result.Steps = step
			result.Observations = observations
			result.Duration = *startTime
			result.Usage = totalUsage.Clone()
			builder.AppendAssistantAction(result.Output, nil, nil, nil)
			if err := persistBuilderHistory(builder, options.PersistHistory); err != nil {
				return nil, err
			}
			return result, nil
		}
		totalToolCalls += len(action.ToolCalls)
		if fingerprint := actionPromptFingerprint(action); fingerprint != "" {
			if fingerprint == lastToolPromptFingerprint {
				repeatedToolPromptFingerprint++
			} else {
				lastToolPromptFingerprint = fingerprint
				repeatedToolPromptFingerprint = 1
			}
			if repeatedToolPromptFingerprint == 2 {
				loop.agent.emitRuntimeEvent("tool_loop.repeated_prompt_observed", sessionID, "", map[string]interface{}{
					"trace_id":           traceID,
					"step":               step,
					"prompt_fingerprint": fingerprint,
					"repeat_count":       repeatedToolPromptFingerprint,
					"tool_call_count":    len(action.ToolCalls),
				})
			}
		} else {
			lastToolPromptFingerprint = ""
			repeatedToolPromptFingerprint = 0
		}

		// 2. Act: 执行工具调用
		normalizedCalls := builder.AppendAssistantAction(action.Content, action.ToolCalls, action.Reasoning, action.MessageMetadata)
		promptBuilder.AppendAssistantAction(action.Content, normalizedCalls, action.Reasoning, action.MessageMetadata)
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
		observationStart := len(observations)
		observations = loop.observe(currentCtx, toolResults, observations, step)
		if manager := loop.agent.GetContextManager(); manager != nil && observationStart < len(observations) {
			workspaceID := ""
			if loop.agent.config != nil {
				workspaceID = optionString(loop.agent.config.Options, "workspace_id")
			}
			manager.RecordObservations(currentCtx, contextmgr.FactObservationInput{
				TraceID: traceID, WorkspaceID: workspaceID, SessionID: sessionID,
				GoalID: toolctx.GoalID(currentCtx), Observations: observations[observationStart:],
			})
		}

		// 4. 更新对话历史
		toolPayloads := toolResultsToPayloads(toolResults, repeatedSemanticAdvisory)
		builder.AppendToolResults(normalizedCalls, toolPayloads)
		promptBuilder.AppendToolResults(normalizedCalls, toolPayloads)
		if err := persistBuilderHistory(builder, options.PersistHistory); err != nil {
			return nil, err
		}

		compactedHistory, compactUsage, compacted, compactErr := loop.tryActiveTurnSemanticCompaction(
			currentCtx,
			sessionID,
			traceID,
			step,
			promptBuilder.Messages(),
			action.toolSchemaTokens,
			usage,
			compactruntime.PhaseMidTurn,
			"context_limit",
		)
		if compactErr != nil {
			if ctxErr := currentCtx.Err(); ctxErr != nil {
				return result, ctxErr
			}
		} else if compacted {
			promptBuilder = NewMessageBuilder(compactedHistory)
			totalUsage.Add(compactUsage)
			result.Usage = totalUsage.Clone()
			if options.BudgetTokens > 0 && compactUsage != nil {
				remainingBudget -= compactUsage.TotalTokens
			}
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

func toolCallLimitReachedMessage(maxCalls int) string {
	if maxCalls <= 0 {
		return "当前运行未配置工具调用上限。"
	}
	return fmt.Sprintf("已达到 maxToolCalls=%d 的工具调用上限，当前运行已停止并保留已有结果。", maxCalls)
}

func explorationLimitReachedMessage(maxSteps int) string {
	return fmt.Sprintf("连续只读探索达到 %d 轮且没有形成新的行动信号，当前运行已停止并保留已有证据。", maxSteps)
}

func repeatedToolLimitReachedMessage(maxCalls int) string {
	return fmt.Sprintf("同一语义工具调用连续重复达到 %d 次，当前运行已停止以避免无效循环。", maxCalls)
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
		contextBudget := resolveContextBuildPromptBudget(loop.llmRuntime, loop.agent, loop.config)
		built := manager.Build(ctx, contextmgr.BuildInput{
			TraceID:                  traceID,
			WorkspaceID:              optionString(loop.agent.config.Options, "workspace_id"),
			SessionID:                sessionID,
			GoalID:                   toolctx.GoalID(ctx),
			TaskID:                   taskID,
			TeamID:                   teamID,
			Profile:                  profileContext,
			Goal:                     goal,
			History:                  history,
			Memory:                   loop.agent.GetMemory(),
			Observations:             observations,
			CountTokens:              countTokens,
			PromptBudget:             contextBudget.PromptBudget,
			PromptBudgetSource:       contextBudget.BudgetSource,
			PromptBudgetSourceDetail: contextBudget.BudgetSourceDetail,
		})
		managedHistory = built.Messages
		action.Metadata = map[string]interface{}{
			"context": built.Metadata,
		}
	}
	var availableTools []types.ToolDefinition
	if loop.config.EnableToolCalls {
		// Freeze the tool surface once for the active turn. Re-compacting on later
		// steps would rewrite the tools prefix mid-session and break provider
		// prompt caching / tool-call continuity. Use the fixed turn prompt budget
		// (not step remainingBudget / first-step message size) so the frozen
		// surface remains budget-safe as active-turn history grows.
		toolsFrozen := false
		if snapshot, ok := TurnToolSurfaceSnapshotFromContext(ctx); ok && snapshot != nil {
			if _, cached, loadErr := snapshot.LoadTurnToolSurface(ctx); loadErr != nil {
				return "", nil, nil, loadErr
			} else {
				toolsFrozen = cached
			}
		}
		availableTools, err = loop.getAvailableTools(ctx, goal, toolWhitelist)
		if err != nil {
			return "", nil, nil, err
		}
		if !toolsFrozen {
			toolTokensBefore := estimateToolDefinitionTokens(loop.llmRuntime, availableTools)
			availableTools = loop.freezeToolSurfaceForTurn(availableTools)
			toolTokensAfter := estimateToolDefinitionTokens(loop.llmRuntime, availableTools)
			if snapshot, ok := TurnToolSurfaceSnapshotFromContext(ctx); ok && snapshot != nil {
				if saveErr := snapshot.SaveTurnToolSurface(ctx, availableTools); saveErr != nil {
					return "", nil, nil, saveErr
				}
			}
			toolFingerprint := ToolDefinitionsFingerprint(availableTools)
			if toolTokensAfter > 0 && toolTokensAfter < toolTokensBefore {
				loop.agent.emitRuntimeEvent("context.tool_schema.compacted", sessionID, "", map[string]interface{}{
					"trace_id":                 traceID,
					"step":                     step,
					"tool_count":               len(availableTools),
					"tool_schema_before":       toolTokensBefore,
					"tool_schema_after":        toolTokensAfter,
					"tool_surface_fingerprint": toolFingerprint,
					"reason":                   "turn_freeze",
				})
			} else if toolFingerprint != "" {
				loop.agent.emitRuntimeEvent("context.tool_schema.frozen", sessionID, "", map[string]interface{}{
					"trace_id":                 traceID,
					"step":                     step,
					"tool_count":               len(availableTools),
					"tool_schema_tokens":       toolTokensAfter,
					"tool_surface_fingerprint": toolFingerprint,
					"reason":                   "turn_freeze",
				})
			}
		}
	}
	var preflightMetadata map[string]interface{}
	managedHistory, preflightMetadata, err = loop.enforcePromptPreflightWithTools(traceID, sessionID, step, managedHistory, availableTools, remainingBudget)
	if err != nil {
		return "", nil, nil, err
	}
	action.promptHistory = reusablePromptHistory(history, managedHistory)
	action.toolSchemaTokens = estimateToolDefinitionTokens(loop.llmRuntime, availableTools)

	requestProvider := loop.requestProvider()
	requestModel := loop.requestModel()

	// 构建请求
	req := &llm.LLMRequest{
		Provider:        requestProvider,
		Model:           requestModel,
		Messages:        managedHistory,
		Tools:           availableTools,
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
	loop.applyRememberedProviderRequestDowngrades(req)
	if loop.config.EnableParallelTools && loop.config.MaxParallelToolCalls > 1 && len(availableTools) > 1 && !loop.parallelToolCallsUnsupported.Load() {
		req.Metadata[llm.MetadataKeyParallelToolCalls] = true
		req.Metadata["max_parallel_tool_calls"] = loop.config.MaxParallelToolCalls
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
	if parallel, _ := req.Metadata[llm.MetadataKeyParallelToolCalls].(bool); parallel {
		requestPayload[llm.MetadataKeyParallelToolCalls] = true
		requestPayload["max_parallel_tool_calls"] = loop.config.MaxParallelToolCalls
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
	if surface := summarizeToolSurface(req.Tools); len(surface) > 0 {
		req.Metadata["executor_path"] = "actor"
		requestPayload["executor_path"] = "actor"
		if fingerprint := ToolDefinitionsFingerprint(req.Tools); fingerprint != "" {
			surface["fingerprint"] = fingerprint
			req.Metadata["tool_surface_fingerprint"] = fingerprint
			requestPayload["tool_surface_fingerprint"] = fingerprint
		}
		req.Metadata["tool_surface"] = surface
		requestPayload["tool_surface"] = surface
	}
	if availability := summarizeToolAvailability(req.Tools); len(availability) > 0 {
		req.Metadata["tool_availability"] = cloneInterfaceMap(availability)
		requestPayload["tool_availability"] = availability
	}
	if fingerprint := promptMessageFingerprint(req.Messages); fingerprint != "" {
		req.Metadata["prompt_fingerprint"] = fingerprint
		requestPayload["prompt_fingerprint"] = fingerprint
		if action.Metadata == nil {
			action.Metadata = map[string]interface{}{}
		}
		action.Metadata["prompt_fingerprint"] = fingerprint
	}
	loop.agent.emitRuntimeEvent("llm.request.started", sessionID, "", requestPayload)
	response, err := loop.llmRuntime.Call(callCtx, req)
	for err != nil {
		parameter := loop.downgradeUnsupportedProviderRequest(req, err)
		if parameter == "" {
			break
		}
		eventType := "llm.request_parameter.downgraded"
		if parameter == llm.MetadataKeyParallelToolCalls {
			eventType = "llm.parallel_tool_calls.downgraded"
		}
		loop.agent.emitRuntimeEvent(eventType, sessionID, "", map[string]interface{}{
			"trace_id": traceID, "step": step, "provider": req.Provider, "model": req.Model,
			"parameter": parameter, "error": err.Error(),
		})
		response, err = loop.llmRuntime.Call(callCtx, req)
	}
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
		if response.Usage.PromptTokens > 0 {
			ratio := float64(response.Usage.CachedTokens) / float64(response.Usage.PromptTokens)
			finishedPayload["usage_cache_hit_ratio"] = ratio
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
	if plan := loop.buildParallelToolBatchPlan(toolCalls, toolWhitelist); plan != nil {
		return loop.runParallelToolBatch(ctx, traceID, sessionID, step, depth, toolCalls, plan), nil
	}
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
		callCtx := promoteTeamRunContext(toolCallContext(ctx, toolCalls, tc.ID, results[:i], loop.agent, sessionID, depth), results[:i])
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
		// Do not freeze yet: the caller freezes a turn-stable surface after the
		// first budget-aware compaction so later steps never rewrite tools.
		return loop.computeAvailableTools(ctx, goal, toolWhitelist)
	}
	return loop.computeAvailableTools(ctx, goal, toolWhitelist)
}

func (loop *ReActLoop) computeAvailableTools(ctx context.Context, goal string, toolWhitelist []string) ([]types.ToolDefinition, error) {
	allowed := whitelistSet(toolWhitelist)
	tools := make([]types.ToolDefinition, 0, 8)
	seen := make(map[string]bool)

	if loop.agent.mcpManager != nil {
		for _, mt := range loop.agent.mcpManager.ListTools() {
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
			definition := spawnSubagentsToolDefinition()
			if !seen[definition.Name] {
				seen[definition.Name] = true
				tools = append(tools, definition)
			}
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

	tools = optimizeModelToolSurface(tools)
	if len(allowed) == 0 {
		tools = projectSimpleGoalToolSurface(goal, tools)
	}
	tools = loop.compactToolSurfaceToBudget(tools)
	sortToolDefinitionsByName(tools)
	return tools, nil
}

func projectSimpleGoalToolSurface(goal string, tools []types.ToolDefinition) []types.ToolDefinition {
	allowed := simpleGoalToolNames(goal)
	if len(allowed) == 0 || len(tools) == 0 {
		return tools
	}
	projected := make([]types.ToolDefinition, 0, len(allowed))
	for _, definition := range tools {
		if allowed[strings.ToLower(strings.TrimSpace(definition.Name))] {
			projected = append(projected, definition)
		}
	}
	if len(projected) == 0 {
		return tools
	}
	return projected
}

func simpleGoalToolNames(goal string) map[string]bool {
	normalized := strings.ToLower(strings.TrimSpace(goal))
	if normalized == "" || len([]rune(normalized)) > 120 || strings.ContainsAny(normalized, "\n;&|") {
		return nil
	}
	fields := strings.Fields(normalized)
	if len(fields) == 0 || len(fields) > 8 {
		return nil
	}
	first := fields[0]
	switch {
	case first == "ls" || first == "dir" || strings.HasPrefix(normalized, "list files") ||
		strings.HasPrefix(normalized, "list directory") || strings.HasPrefix(normalized, "列出文件") ||
		strings.HasPrefix(normalized, "列出目录"):
		return map[string]bool{"ls": true, "glob": true}
	case strings.HasPrefix(normalized, "read file") || strings.HasPrefix(normalized, "view file") ||
		strings.HasPrefix(normalized, "show file") || strings.HasPrefix(normalized, "读取文件") ||
		strings.HasPrefix(normalized, "查看文件"):
		return map[string]bool{"view": true}
	case strings.HasPrefix(normalized, "find file") || strings.HasPrefix(normalized, "search files") ||
		strings.HasPrefix(normalized, "grep ") || strings.HasPrefix(normalized, "搜索文件") ||
		strings.HasPrefix(normalized, "查找文件"):
		return map[string]bool{"grep": true, "glob": true}
	case normalized == "pwd" || normalized == "date" || normalized == "whoami" || normalized == "git status":
		return map[string]bool{"bash": true, "execute_shell_command": true}
	default:
		return nil
	}
}

func optimizeModelToolSurface(tools []types.ToolDefinition) []types.ToolDefinition {
	if len(tools) == 0 {
		return nil
	}
	hasBash := false
	for _, definition := range tools {
		if strings.EqualFold(strings.TrimSpace(definition.Name), "bash") {
			hasBash = true
			break
		}
	}

	optimized := make([]types.ToolDefinition, 0, len(tools))
	for _, definition := range tools {
		name := strings.ToLower(strings.TrimSpace(definition.Name))
		if hasBash && name == "execute_shell_command" {
			continue
		}
		item := types.ToolDefinition{
			Name:        definition.Name,
			Description: definition.Description,
			Parameters:  cloneInterfaceMap(definition.Parameters),
			Metadata:    cloneInterfaceMap(definition.Metadata),
		}
		if name == "grep" {
			item.Description = "搜索文件内容。优先用 patterns + paths 在一次调用中批量搜索相关目标；高级 ripgrep 选项放入 rg_args。结果统一为 path:line[:column]: text。"
			item.Parameters = compactGrepParametersForModel(item.Parameters)
		}
		optimized = append(optimized, item)
	}
	return optimized
}

func compactGrepParametersForModel(parameters map[string]interface{}) map[string]interface{} {
	properties, _ := parameters["properties"].(map[string]interface{})
	if len(properties) == 0 {
		return parameters
	}
	commonNames := []string{
		"pattern", "patterns", "regexp", "pattern_file", "pattern_files",
		"path", "paths", "glob", "include", "exclude", "file_type", "type", "type_not",
		"fixed_strings", "literal", "ignore_case", "case_sensitive", "smart_case",
		"word", "word_regexp", "line_regexp", "invert_match", "only_matching",
		"context", "before_context", "after_context", "max_count", "max_depth", "max_filesize",
		"files_with_matches", "files_without_match", "count", "count_matches",
		"hidden", "no_ignore", "ignore_file", "pcre2", "multiline", "rg_args",
	}
	compactProperties := make(map[string]interface{}, len(commonNames))
	for _, name := range commonNames {
		if schema, ok := properties[name]; ok {
			compactProperties[name] = schema
		}
	}
	compacted := map[string]interface{}{
		"type":                 "object",
		"properties":           compactProperties,
		"additionalProperties": false,
	}
	if required, ok := parameters["required"]; ok {
		compacted["required"] = required
	}
	return compacted
}

func (loop *ReActLoop) compactToolSurfaceToBudget(tools []types.ToolDefinition) []types.ToolDefinition {
	return loop.compactToolSurfaceForPrompt(nil, tools, 0)
}

// freezeToolSurfaceForTurn selects a turn-stable tool surface before the first
// model call. It uses the fixed turn prompt budget and reserves most of that
// budget for active-turn message growth, so later steps do not need to rewrite
// tools under tighter prompt pressure.
func (loop *ReActLoop) freezeToolSurfaceForTurn(tools []types.ToolDefinition) []types.ToolDefinition {
	if loop == nil || loop.llmRuntime == nil || len(tools) == 0 {
		return tools
	}
	budget := resolveContextBuildPromptBudget(loop.llmRuntime, loop.agent, loop.config)
	if budget.PromptBudget <= 0 {
		return tools
	}
	before := estimateToolDefinitionTokens(loop.llmRuntime, tools)
	if before <= 0 {
		return tools
	}
	// Keep tools to at most ~1/4 of the turn prompt budget so active-turn replay
	// growth and preflight message compaction still have room later in the turn.
	toolBudget := budget.PromptBudget / 4
	if toolBudget < 1 {
		toolBudget = 1
	}
	if before <= toolBudget {
		return tools
	}
	compacted := compactToolDefinitionAnnotations(tools)
	if after := estimateToolDefinitionTokens(loop.llmRuntime, compacted); after > 0 && after < before {
		return compacted
	}
	return tools
}

func (loop *ReActLoop) compactToolSurfaceForPrompt(messages []types.Message, tools []types.ToolDefinition, remainingBudget int) []types.ToolDefinition {
	if loop == nil || loop.llmRuntime == nil || len(tools) == 0 {
		return tools
	}
	budget := resolvePromptPreflightBudget(loop.llmRuntime, loop.agent, loop.config, remainingBudget)
	if budget.PromptBudget <= 0 {
		return tools
	}
	before := estimateToolDefinitionTokens(loop.llmRuntime, tools)
	messageTokens := estimatePromptMessageTokens(loop.llmRuntime, messages)
	if before+messageTokens < budget.PromptBudget {
		return tools
	}
	compacted := compactToolDefinitionAnnotations(tools)
	if after := estimateToolDefinitionTokens(loop.llmRuntime, compacted); after > 0 && after < before {
		return compacted
	}
	return tools
}

func compactToolDefinitionAnnotations(tools []types.ToolDefinition) []types.ToolDefinition {
	compacted := cloneToolDefinitions(tools)
	for index := range compacted {
		compacted[index].Description = truncateToolDescription(compacted[index].Description, 160)
		compacted[index].Parameters = stripToolSchemaAnnotations(compacted[index].Parameters)
	}
	return compacted
}

func stripToolSchemaAnnotations(schema map[string]interface{}) map[string]interface{} {
	if len(schema) == 0 {
		return schema
	}
	cleaned, _ := stripToolSchemaValue(schema).(map[string]interface{})
	return cleaned
}

func stripToolSchemaValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		cleaned := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "description", "title", "examples", "example", "$comment", "deprecated", "default":
				continue
			default:
				cleaned[key] = stripToolSchemaValue(item)
			}
		}
		return cleaned
	case []interface{}:
		cleaned := make([]interface{}, len(typed))
		for index, item := range typed {
			cleaned[index] = stripToolSchemaValue(item)
		}
		return cleaned
	case []string:
		return append([]string(nil), typed...)
	default:
		return typed
	}
}

func truncateToolDescription(description string, limit int) string {
	trimmed := strings.TrimSpace(description)
	if limit <= 0 || trimmed == "" {
		return ""
	}
	runes := []rune(trimmed)
	if len(runes) <= limit {
		return trimmed
	}
	return strings.TrimSpace(string(runes[:limit])) + "..."
}

func sortToolDefinitionsByName(tools []types.ToolDefinition) {
	sort.SliceStable(tools, func(i, j int) bool {
		left := strings.TrimSpace(tools[i].Name)
		right := strings.TrimSpace(tools[j].Name)
		if left == right {
			return tools[i].Description < tools[j].Description
		}
		return left < right
	})
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
	Content          string                 `json:"content" yaml:"content"`
	ToolCalls        []types.ToolCall       `json:"toolCalls,omitempty" yaml:"toolCalls,omitempty"`
	Thought          string                 `json:"thought,omitempty" yaml:"thought,omitempty"`
	Reasoning        *types.ReasoningBlock  `json:"reasoning,omitempty" yaml:"reasoning,omitempty"`
	Metadata         map[string]interface{} `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	MessageMetadata  types.Metadata         `json:"messageMetadata,omitempty" yaml:"messageMetadata,omitempty"`
	promptHistory    []types.Message
	toolSchemaTokens int
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

func toolResultsToPayloads(results []toolExecutionResult, advisory string) []ToolResultPayload {
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
	if len(payloads) > 0 && strings.TrimSpace(advisory) != "" {
		last := len(payloads) - 1
		payloads[last].Content = strings.TrimSpace(payloads[last].Content) + "\n\n" + strings.TrimSpace(advisory)
		payloads[last].Metadata["semantic_repeat_advisory"] = true
	}
	return payloads
}

func repeatedSemanticToolCallAdvisory(repeatCount int) string {
	if repeatCount < 2 {
		return ""
	}
	return fmt.Sprintf(
		"Runtime advisory: the same semantic tool request has run %d consecutive times. Execution was not blocked. If the evidence is unchanged, reuse it and change the query, batch related inputs, or proceed to the next task step instead of repeating the call.",
		repeatCount,
	)
}

func nextExplorationStallCount(current int, calls []types.ToolCall) int {
	if len(calls) == 0 {
		return 0
	}
	for _, call := range calls {
		if !isExplorationOnlyToolCall(call) {
			return 0
		}
	}
	return current + 1
}

func isExplorationOnlyToolCall(call types.ToolCall) bool {
	switch strings.ToLower(strings.TrimSpace(call.Name)) {
	case "view", "grep", "glob", "ls", "fetch", "web_search", "sourcegraph", "list_mcp_resources":
		return true
	case "bash", "execute_shell_command":
		return !toolCallDeclaresMutatedPaths(call.Args)
	default:
		return false
	}
}

func toolCallDeclaresMutatedPaths(args map[string]interface{}) bool {
	if len(args) == 0 {
		return false
	}
	switch values := args["mutated_paths"].(type) {
	case []string:
		for _, value := range values {
			if strings.TrimSpace(value) != "" {
				return true
			}
		}
	case []interface{}:
		for _, value := range values {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				return true
			}
		}
	}
	return false
}

func explorationStallAdvisory(stepCount int) string {
	if stepCount < explorationStallNoticeThreshold {
		return ""
	}
	return fmt.Sprintf(
		"Runtime advisory: %d consecutive tool rounds have only inspected or checked state without a declared workspace mutation. Execution remains unrestricted. Reuse the evidence already collected: state the current root-cause hypothesis, implement the smallest justified fix, and verify it. If the task is read-only or already solved, stop calling tools and return the conclusion. Do not repeat searches merely to reconfirm unchanged evidence.",
		stepCount,
	)
}

func joinRuntimeAdvisories(values ...string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, "\n\n")
}

func toolCallContext(ctx context.Context, toolCalls []types.ToolCall, currentToolCallID string, completed []toolExecutionResult, agent *Agent, sessionID string, depth int) context.Context {
	if batch, ok := ToolBatchContextFromContext(ctx); ok && len(batch.ToolCalls) > 0 {
		ctx = WithToolBatchContext(ctx, batch.ToolCalls, currentToolCallID, batch.CompletedToolMessages)
	} else {
		ctx = WithToolBatchContext(ctx, toolCalls, currentToolCallID, toolMessagesFromResults(completed))
	}
	if strings.TrimSpace(sessionID) != "" {
		ctx = toolctx.WithSessionID(ctx, sessionID)
	}
	ctx = toolctx.WithAgentDepth(ctx, depth)
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

func mergeConfiguredSystemPrompt(history []types.Message, systemPrompt string) []types.Message {
	desired := strings.TrimSpace(systemPrompt)
	if desired == "" {
		return cloneMessageHistory(history)
	}
	if len(history) == 0 {
		return []types.Message{*types.NewSystemMessage(desired)}
	}

	first := history[0]
	existing := strings.TrimSpace(first.Content)
	if first.Role == "system" && (existing == desired || (existing != "" && strings.HasPrefix(desired, existing+"\n"))) {
		merged := cloneMessageHistory(history)
		merged[0].Content = desired
		return merged
	}

	merged := make([]types.Message, 0, len(history)+1)
	merged = append(merged, *types.NewSystemMessage(desired))
	merged = append(merged, cloneMessageHistory(history)...)
	return merged
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

func reusablePromptHistory(source, managed []types.Message) []types.Message {
	if len(managed) == 0 {
		return nil
	}

	persistedContext := make(map[string]int)
	for _, message := range source {
		stage := strings.TrimSpace(message.Metadata.GetString("context_stage", ""))
		if stage == "" {
			continue
		}
		persistedContext[promptContextMessageKey(message, stage)]++
	}

	reusable := make([]types.Message, 0, len(managed))
	for _, message := range managed {
		stage := strings.TrimSpace(message.Metadata.GetString("context_stage", ""))
		if stage != "" {
			key := promptContextMessageKey(message, stage)
			if persistedContext[key] <= 0 {
				continue
			}
			persistedContext[key]--
		}
		reusable = append(reusable, *message.Clone())
	}
	return reusable
}

func promptContextMessageKey(message types.Message, stage string) string {
	return strings.Join([]string{message.Role, stage, message.ToolCallID, message.Content}, "\x00")
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

func actionPromptFingerprint(action *AgentAction) string {
	if action == nil || len(action.Metadata) == 0 {
		return ""
	}
	return strings.TrimSpace(stringValue(action.Metadata["prompt_fingerprint"]))
}

func semanticToolCallFingerprint(calls []types.ToolCall) string {
	if len(calls) == 0 {
		return ""
	}
	type semanticToolCall struct {
		Name string                 `json:"name"`
		Args map[string]interface{} `json:"arguments,omitempty"`
	}
	payload := make([]semanticToolCall, 0, len(calls))
	for _, call := range calls {
		name := strings.ToLower(strings.TrimSpace(call.Name))
		if name == "" || semanticToolCallRepeatExempt(name) {
			return ""
		}
		payload = append(payload, semanticToolCall{Name: name, Args: call.Args})
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", sum[:])
}

func semanticToolCallRepeatExempt(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "background_task", "wait_agent", "read_agent", "list_agents", "get_agents", "get_goal", "read_goal":
		return true
	default:
		return false
	}
}

func promptMessageFingerprint(messages []types.Message) string {
	if len(messages) == 0 {
		return ""
	}
	type fingerprintToolCall struct {
		ID   string                 `json:"id,omitempty"`
		Name string                 `json:"name,omitempty"`
		Args map[string]interface{} `json:"arguments,omitempty"`
	}
	type fingerprintMessage struct {
		Role         string                `json:"role,omitempty"`
		Content      string                `json:"content,omitempty"`
		ContentParts []types.ContentPart   `json:"content_parts,omitempty"`
		ToolCalls    []fingerprintToolCall `json:"tool_calls,omitempty"`
		ToolCallID   string                `json:"tool_call_id,omitempty"`
	}
	payload := make([]fingerprintMessage, 0, len(messages))
	for _, message := range messages {
		item := fingerprintMessage{
			Role:         message.Role,
			Content:      message.Content,
			ContentParts: append([]types.ContentPart(nil), message.ContentParts...),
			ToolCallID:   message.ToolCallID,
		}
		if len(message.ToolCalls) > 0 {
			item.ToolCalls = make([]fingerprintToolCall, 0, len(message.ToolCalls))
			for _, call := range message.ToolCalls {
				item.ToolCalls = append(item.ToolCalls, fingerprintToolCall{
					ID:   call.ID,
					Name: call.Name,
					Args: call.Args,
				})
			}
		}
		payload = append(payload, item)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", sum[:])
}

func markSessionCompactionRecoveryInput(seen map[string]struct{}, fingerprint string) bool {
	if seen == nil || strings.TrimSpace(fingerprint) == "" {
		return false
	}
	if _, exists := seen[fingerprint]; exists {
		return false
	}
	seen[fingerprint] = struct{}{}
	return true
}

func compactionRecoveryMadeProgress(runtime *llm.LLMRuntime, before, after []types.Message) bool {
	if len(after) == 0 || promptMessageFingerprint(before) == promptMessageFingerprint(after) {
		return false
	}
	if len(after) < len(before) {
		return true
	}
	if runtime == nil {
		return false
	}
	beforeTokens := estimatePromptMessageTokens(runtime, before)
	afterTokens := estimatePromptMessageTokens(runtime, after)
	return beforeTokens > 0 && afterTokens > 0 && afterTokens < beforeTokens
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

func estimateToolDefinitionTokens(runtime *llm.LLMRuntime, tools []types.ToolDefinition) int {
	if runtime == nil || len(tools) == 0 {
		return 0
	}
	encoded, err := json.Marshal(tools)
	if err != nil {
		return 0
	}
	return runtime.CountTokens(string(encoded))
}

func estimatePromptMessageTokens(runtime *llm.LLMRuntime, messages []types.Message) int {
	if runtime == nil || len(messages) == 0 {
		return 0
	}
	return runtime.CountMessagesTokens(messages)
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
	return loop.enforcePromptPreflightWithTools(traceID, sessionID, step, messages, nil, remainingBudget)
}

func (loop *ReActLoop) enforcePromptPreflightWithTools(traceID, sessionID string, step int, messages []types.Message, tools []types.ToolDefinition, remainingBudget int) ([]types.Message, map[string]interface{}, error) {
	if len(messages) == 0 || loop == nil || loop.llmRuntime == nil {
		return messages, nil, nil
	}

	budget := resolvePromptPreflightBudget(loop.llmRuntime, loop.agent, loop.config, remainingBudget)
	if budget.PromptBudget <= 0 {
		return messages, nil, nil
	}

	countPromptMessages := func(input []types.Message) int {
		return estimatePromptMessageTokens(loop.llmRuntime, input)
	}
	messageTokensBefore := countPromptMessages(messages)
	toolSchemaTokens := estimateToolDefinitionTokens(loop.llmRuntime, tools)
	promptTokensBefore := messageTokensBefore + toolSchemaTokens
	preflightMetadata := budget.Metadata()
	if preflightMetadata == nil {
		preflightMetadata = map[string]interface{}{}
	}
	preflightMetadata["prompt_tokens_before"] = promptTokensBefore
	preflightMetadata["message_tokens_before"] = messageTokensBefore
	preflightMetadata["tool_schema_tokens"] = toolSchemaTokens
	preflightMetadata["tool_count"] = len(tools)
	if promptTokensBefore <= 0 || promptTokensBefore <= budget.PromptBudget {
		return messages, preflightMetadata, nil
	}

	startedPayload := budget.Metadata()
	if startedPayload == nil {
		startedPayload = map[string]interface{}{}
	}
	startedPayload["trace_id"] = traceID
	startedPayload["step"] = step
	startedPayload["prompt_tokens"] = promptTokensBefore
	startedPayload["message_tokens"] = messageTokensBefore
	startedPayload["tool_schema_tokens"] = toolSchemaTokens
	startedPayload["tool_count"] = len(tools)
	startedPayload["message_count"] = len(messages)
	startedPayload["remaining_budget"] = remainingBudget
	startedPayload["active_turn_replay"] = true
	loop.agent.emitRuntimeEvent("context.preflight.started", sessionID, "", startedPayload)

	messageBudget := budget.PromptBudget - toolSchemaTokens
	if messageBudget <= 0 {
		preflightMetadata["active_turn_compacted"] = false
		failure := buildPromptPreflightFailure(
			"tool_schema_exceeds_budget",
			messages,
			promptTokensBefore,
			budget.PromptBudget,
		)
		failureErr := newPromptPreflightError(budget, failure, promptTokensBefore, false, nil)
		failedPayload := cloneInterfaceMap(startedPayload)
		failedPayload["active_turn_compacted"] = false
		for key, value := range failureErr.Metadata() {
			failedPayload[key] = value
			preflightMetadata[key] = value
		}
		loop.agent.emitRuntimeEvent("context.preflight.failed", sessionID, "", failedPayload)
		return nil, preflightMetadata, failureErr
	}
	compactedMessages, compacted := historyguard.CompactActiveTurnReplayWithCounter(
		messages,
		historyguard.DefaultActiveTurnReplayMaxBytes,
		messageBudget,
		countPromptMessages,
	)
	preflightMetadata["message_count_before"] = len(messages)

	if compacted {
		messageTokensAfter := countPromptMessages(compactedMessages)
		promptTokensAfter := messageTokensAfter + toolSchemaTokens
		preflightMetadata["active_turn_compacted"] = true
		preflightMetadata["active_turn_prompt_only"] = true
		preflightMetadata["prompt_tokens_after"] = promptTokensAfter
		preflightMetadata["message_tokens_after"] = messageTokensAfter
		preflightMetadata["message_count_after"] = len(compactedMessages)

		compactedPayload := budget.Metadata()
		if compactedPayload == nil {
			compactedPayload = map[string]interface{}{}
		}
		compactedPayload["trace_id"] = traceID
		compactedPayload["step"] = step
		compactedPayload["prompt_tokens_before"] = promptTokensBefore
		compactedPayload["prompt_tokens_after"] = promptTokensAfter
		compactedPayload["message_tokens_before"] = messageTokensBefore
		compactedPayload["message_tokens_after"] = messageTokensAfter
		compactedPayload["tool_schema_tokens"] = toolSchemaTokens
		compactedPayload["tool_count"] = len(tools)
		compactedPayload["message_count_before"] = len(messages)
		compactedPayload["message_count_after"] = len(compactedMessages)
		compactedPayload["remaining_budget"] = remainingBudget
		compactedPayload["prompt_only"] = true
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
		failureErr := newPromptPreflightError(budget, failure, promptTokensAfter, true, nil)
		failedPayload := budget.Metadata()
		if failedPayload == nil {
			failedPayload = map[string]interface{}{}
		}
		failedPayload["trace_id"] = traceID
		failedPayload["step"] = step
		failedPayload["prompt_tokens"] = promptTokensAfter
		failedPayload["message_tokens"] = messageTokensAfter
		failedPayload["tool_schema_tokens"] = toolSchemaTokens
		failedPayload["tool_count"] = len(tools)
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
	failedPayload["message_tokens"] = messageTokensBefore
	failedPayload["tool_schema_tokens"] = toolSchemaTokens
	failedPayload["tool_count"] = len(tools)
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
	provider = loop.requestProvider()
	model = loop.requestModel()

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
		SessionID:             sessionID,
		TaskID:                sessionID,
		Provider:              provider,
		Model:                 model,
		Mode:                  compactruntime.ModeLocal,
		Force:                 true,
		History:               cloneMessageHistory(history),
		ReplacementTokenLimit: compactRecoveryMessageTokenLimit(budgetMetadata),
		Phase:                 compactruntime.PhasePreTurn,
		CountTokens: func(messages []types.Message) int {
			return estimatePromptMessageTokens(loop.llmRuntime, messages)
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

func (loop *ReActLoop) tryActiveTurnSemanticCompaction(ctx context.Context, sessionID, traceID string, step int, history []types.Message, toolSchemaTokens int, observedUsage *types.TokenUsage, phase, reason string) ([]types.Message, *types.TokenUsage, bool, error) {
	if loop == nil || loop.agent == nil || loop.llmRuntime == nil || len(history) == 0 {
		return nil, nil, false, nil
	}

	budget := resolveContextBuildPromptBudget(loop.llmRuntime, loop.agent, loop.config)
	if budget.PromptBudget <= 0 {
		return nil, nil, false, nil
	}
	messageTokens := estimatePromptMessageTokens(loop.llmRuntime, history)
	promptTokens := messageTokens + maxIntValue(0, toolSchemaTokens)
	if promptTokens <= budget.PromptBudget || !activeTurnSemanticCompactionEligible(budget, observedUsage) {
		return nil, nil, false, nil
	}

	replacementTokenLimit := budget.PromptBudget - maxIntValue(0, toolSchemaTokens)
	if replacementTokenLimit <= 0 {
		replacementTokenLimit = 1
	}
	provider := loop.requestProvider()
	model := loop.requestModel()
	phase = firstNonEmptyTrimmed(phase, compactruntime.PhaseMidTurn)
	reason = firstNonEmptyTrimmed(reason, "context_limit")
	eventPrefix := "context.mid_turn_compact"
	if phase == compactruntime.PhasePreTurn {
		eventPrefix = "context.pre_turn_compact"
	}
	startedPayload := budget.Metadata()
	if startedPayload == nil {
		startedPayload = map[string]interface{}{}
	}
	startedPayload["session_id"] = sessionID
	startedPayload["trace_id"] = traceID
	startedPayload["step"] = step
	startedPayload["phase"] = phase
	startedPayload["mode"] = compactruntime.ModeAuto
	startedPayload["reason"] = reason
	startedPayload["provider"] = provider
	startedPayload["model"] = model
	startedPayload["message_count"] = len(history)
	startedPayload["message_tokens"] = messageTokens
	startedPayload["tool_schema_tokens"] = toolSchemaTokens
	startedPayload["prompt_tokens"] = promptTokens
	startedPayload["replacement_token_limit"] = replacementTokenLimit
	startedPayload["prompt_only"] = true
	startedPayload["durable_history_replaced"] = false
	loop.agent.emitRuntimeEvent(eventPrefix+".started", sessionID, "", startedPayload)

	taskID := sessionID
	if loop.agent.config != nil {
		taskID = firstNonEmptyTrimmed(optionString(loop.agent.config.Options, "task_id"), taskID)
	}
	runtime := compactruntime.New(loop.llmRuntime, loop.agent.GetContextManager())
	compactRequest := compactruntime.Request{
		SessionID:             sessionID,
		TaskID:                taskID,
		Provider:              provider,
		Model:                 model,
		Mode:                  compactruntime.ModeAuto,
		Force:                 true,
		History:               cloneMessageHistory(history),
		ReplacementTokenLimit: replacementTokenLimit,
		Phase:                 phase,
		CountTokens: func(messages []types.Message) int {
			return estimatePromptMessageTokens(loop.llmRuntime, messages)
		},
		ObservedTokens:    promptTokens,
		HasObservedTokens: true,
	}
	result, status, err := runtime.MaybeCompact(ctx, compactRequest)
	if status.Mode == compactruntime.ModeRemote && (err != nil || result == nil || len(result.ReplacementHistory) == 0) && ctx.Err() == nil {
		fallbackPayload := cloneInterfaceMap(startedPayload)
		fallbackPayload["from_mode"] = compactruntime.ModeRemote
		fallbackPayload["to_mode"] = compactruntime.ModeLocal
		fallbackPayload["reason"] = firstNonEmptyTrimmed(status.Reason, "remote_compaction_unavailable")
		if err != nil {
			fallbackPayload["error"] = err.Error()
		}
		loop.agent.emitRuntimeEvent(eventPrefix+".fallback", sessionID, "", fallbackPayload)
		compactRequest.Mode = compactruntime.ModeLocal
		result, status, err = runtime.MaybeCompact(ctx, compactRequest)
	}
	if err != nil {
		failedPayload := cloneInterfaceMap(startedPayload)
		failedPayload["reason"] = firstNonEmptyTrimmed(status.Reason, "semantic_compaction_failed")
		failedPayload["error"] = err.Error()
		loop.agent.emitRuntimeEvent(eventPrefix+".failed", sessionID, "", failedPayload)
		return nil, nil, false, err
	}
	if result == nil || len(result.ReplacementHistory) == 0 || !compactionRecoveryMadeProgress(loop.llmRuntime, history, result.ReplacementHistory) {
		skippedPayload := cloneInterfaceMap(startedPayload)
		skippedPayload["reason"] = firstNonEmptyTrimmed(status.Reason, "replacement_did_not_reduce_context")
		loop.agent.emitRuntimeEvent(eventPrefix+".skipped", sessionID, "", skippedPayload)
		return nil, nil, false, nil
	}

	completedPayload := cloneInterfaceMap(startedPayload)
	completedPayload["mode"] = firstNonEmptyTrimmed(result.Mode, status.Mode)
	completedPayload["token_after"] = result.TokenAfter
	completedPayload["message_count_after"] = len(result.ReplacementHistory)
	completedPayload["compacted_messages"] = result.CompactedMessages
	completedPayload["usage_source"] = result.UsageSource
	summarySource := midTurnCompactionSummarySource(result)
	completedPayload["summary_source"] = summarySource
	completedPayload["semantic_checkpoint"] = summarySource != "deterministic_fallback"
	if summarySource == "deterministic_fallback" {
		completedPayload["reason"] = "deterministic_checkpoint_fallback_installed"
	} else {
		completedPayload["reason"] = "semantic_checkpoint_installed"
	}
	if len(result.CheckpointIDs) > 0 {
		completedPayload["checkpoint_ids"] = append([]string(nil), result.CheckpointIDs...)
		completedPayload["checkpoint_id"] = result.CheckpointIDs[len(result.CheckpointIDs)-1]
	}
	if result.Usage != nil {
		completedPayload["usage_prompt_tokens"] = result.Usage.PromptTokens
		completedPayload["usage_completion_tokens"] = result.Usage.CompletionTokens
		completedPayload["usage_total_tokens"] = result.Usage.TotalTokens
	}
	loop.agent.emitRuntimeEvent(eventPrefix+".completed", sessionID, "", completedPayload)
	return cloneMessageHistory(result.ReplacementHistory), result.Usage, true, nil
}

func activeTurnSemanticCompactionEligible(budget promptPreflightBudget, observedUsage *types.TokenUsage) bool {
	if budget.ModelCapabilityAutoCompactTokenLimit > 0 || budget.ModelCapabilityMaxContextTokens > 0 {
		return true
	}
	return observedUsage != nil && observedUsage.PromptTokens > 0 && budget.ProviderContextLimit > 0
}

func midTurnCompactionSummarySource(result *compactruntime.Result) string {
	if result == nil {
		return "unknown"
	}
	if strings.EqualFold(strings.TrimSpace(result.Mode), compactruntime.ModeRemote) {
		return "remote"
	}
	for index := len(result.ReplacementHistory) - 1; index >= 0; index-- {
		message := result.ReplacementHistory[index]
		if strings.EqualFold(strings.TrimSpace(message.Metadata.GetString("context_stage", "")), "compaction") {
			return firstNonEmptyTrimmed(message.Metadata.GetString("summary_source", ""), "provider")
		}
	}
	return "provider"
}

func maxIntValue(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func compactRecoveryMessageTokenLimit(metadata map[string]interface{}) int {
	limit := firstPositiveBudgetMetadataInt(metadata["prompt_budget"])
	if limit <= 0 {
		return 0
	}
	if toolTokens := intValue(metadata["tool_schema_tokens"]); toolTokens > 0 {
		limit -= toolTokens
	}
	if limit <= 0 {
		return 1
	}
	return limit
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
	case "tool_schema_exceeds_budget":
		failure.Reason = "tool definitions exceed the prompt budget"
		failure.Detail = fmt.Sprintf("messages plus tool definitions require %d tokens, exceeding prompt budget %d before the provider request is sent", promptTokens, promptBudget)
		failure.SuggestedAction = "请缩小工具白名单、精简工具 Schema，或提高 prompt 预算。"
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

func resolveContextBuildPromptBudget(runtime *llm.LLMRuntime, agent *Agent, loopConfig *LoopReActConfig) promptPreflightBudget {
	return resolvePromptPreflightBudget(runtime, agent, loopConfig, 0)
}

func resolvePromptPreflightBudget(runtime *llm.LLMRuntime, agent *Agent, loopConfig *LoopReActConfig, remainingBudget int) promptPreflightBudget {
	budget := promptPreflightBudget{}

	managerBudget := 0
	hasManagerBudget := false
	explicitContextBudget, hasExplicitContextBudget := promptPreflightContextBudgetOverride(agent)
	if agent != nil {
		if manager := agent.GetContextManager(); manager != nil && manager.Budget.MaxPromptTokens > 0 {
			managerBudget = manager.Budget.MaxPromptTokens
			hasManagerBudget = true
		}
	}
	if hasExplicitContextBudget {
		budget.addCandidate(
			"context_max_prompt_tokens",
			explicitContextBudget,
			"runtime context maxPromptTokens",
		)
	}

	hasResolvedPromptLimit := budget.PromptBudget > 0
	if hasExplicitContextBudget {
		hasResolvedPromptLimit = true
	}

	addFallbackPromptBudget := func() {
		source, value, detail := resolvePromptFallbackBudget(agent, managerBudget, hasManagerBudget)
		if value <= 0 {
			return
		}
		budget.addCandidate(source, value, detail)
	}

	resolvedProvider, resolvedModel := resolvePromptPreflightProviderModel(runtime, agent, loopConfig)
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
		capabilityAddedPromptLimit := false
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
				capabilityAddedPromptLimit = true
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
				capabilityAddedPromptLimit = true
			}
		}
		if (!ok || !capabilityAddedPromptLimit) && budget.ProviderContextLimit > 0 {
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

func resolvePromptFallbackBudget(agent *Agent, managerBudget int, hasManagerBudget bool) (string, int, string) {
	if agent != nil && agent.config != nil {
		if value, ok := contextOptionInt(agent.config.Options, "context_fallback_max_prompt_tokens"); ok {
			return "context_fallback_max_prompt_tokens", value, "runtime context fallbackMaxPromptTokens"
		}
	}
	if hasManagerBudget && managerBudget > contextmgr.DefaultFallbackMaxPromptTokens {
		return "context_max_prompt_tokens", managerBudget, "context manager budget max_prompt_tokens fallback"
	}
	return "default_context_fallback_max_prompt_tokens", contextmgr.DefaultFallbackMaxPromptTokens, "contextmgr.DefaultFallbackMaxPromptTokens"
}

func hasPromptPreflightContextBudgetOverride(agent *Agent) bool {
	_, ok := promptPreflightContextBudgetOverride(agent)
	return ok
}

func promptPreflightContextBudgetOverride(agent *Agent) (int, bool) {
	if agent == nil || agent.config == nil || len(agent.config.Options) == 0 {
		return 0, false
	}
	return contextOptionInt(agent.config.Options, "context_max_prompt_tokens")
}

func resolvePromptPreflightProviderModel(runtime *llm.LLMRuntime, agent *Agent, loopConfig *LoopReActConfig) (string, string) {
	providerName := ""
	model := ""
	if loopConfig != nil {
		providerName = strings.TrimSpace(loopConfig.Provider)
		model = strings.TrimSpace(loopConfig.Model)
	}
	if agent != nil && agent.config != nil {
		if providerName == "" {
			providerName = strings.TrimSpace(agent.config.Provider)
		}
		if model == "" {
			model = strings.TrimSpace(agent.config.Model)
		}
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
		reasoningEffort := stringValue(item["reasoning_effort"])
		thinkingEffort := stringValue(item["thinking_effort"])
		routeWarnings := []string(nil)
		if strings.TrimSpace(reasoningEffort) == "" && strings.TrimSpace(thinkingEffort) != "" {
			reasoningEffort = thinkingEffort
			routeWarnings = append(routeWarnings, "thinking_effort_alias_used")
		}
		task := SubagentTask{
			ID:                  stringValue(item["id"]),
			Role:                stringValue(item["role"]),
			Goal:                stringValue(item["goal"]),
			Difficulty:          stringValue(item["difficulty"]),
			DifficultyRationale: stringValue(item["difficulty_rationale"]),
			Provider:            stringValue(item["provider"]),
			Model:               stringValue(item["model"]),
			ReasoningEffort:     reasoningEffort,
			RouteWarnings:       routeWarnings,
			BudgetTokens:        intValue(item["budget_tokens"]),
			TimeoutSec:          intValue(item["timeout"]),
			ReadOnly:            boolValue(item["read_only"]),
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
		Description: "Spawn isolated subagents for parallel subtasks. Use only when tasks are independent or when hard/expert work benefits from isolated research, writing, or verification. Include difficulty and difficulty_rationale for every child task when known. Leave provider/model empty unless explicitly requested; runtime routing maps difficulty to local provider/model configuration.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"agents": map[string]interface{}{
					"type": "array",
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"id":                   map[string]interface{}{"type": "string"},
							"role":                 map[string]interface{}{"type": "string"},
							"goal":                 map[string]interface{}{"type": "string"},
							"difficulty":           map[string]interface{}{"type": "string", "enum": []string{"easy", "normal", "hard", "expert"}, "description": "Estimated task difficulty. Local runtime treats this as a routing hint."},
							"difficulty_rationale": map[string]interface{}{"type": "string", "description": "Short reason for the difficulty rating."},
							"provider":             map[string]interface{}{"type": "string", "description": "Optional provider hint. The local runtime may ignore it unless explicitly allowed."},
							"reasoning_effort":     map[string]interface{}{"type": "string", "enum": []string{"low", "medium", "high"}, "description": "Optional reasoning effort hint. The local runtime validates it against local policy."},
							"thinking_effort":      map[string]interface{}{"type": "string", "description": "Deprecated alias for reasoning_effort."},
							"tools_whitelist":      map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
							"depends_on":           map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
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

func summarizeToolSurface(tools []types.ToolDefinition) map[string]interface{} {
	return runtimeskill.BuildToolSurfaceSummary(tools)
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
