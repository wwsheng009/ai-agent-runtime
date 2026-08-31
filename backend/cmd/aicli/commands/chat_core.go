package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/compactruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/contextreconcile"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const chatcoreReasoningMetadataKey = "chatcore_reasoning_content"

// sharedChatDefaultAutoCompactRatio is the single CLI fallback ratio used when a
// model capability does not declare AutoCompactTokenLimit / AutoCompactRatio.
// Keep aligned with agent preflight and compactruntime defaults (0.85).
const sharedChatDefaultAutoCompactRatio = 0.85
const sharedChatDefaultContextWindowTokens = 256000

// chatSessionImageArtifactDir returns the session-local directory for
// persisting image attachment copies. Returns empty string if unavailable.
func chatSessionImageArtifactDir(session *ChatSession) string {
	if session == nil {
		return ""
	}
	if session.RuntimeSession != nil && strings.TrimSpace(session.SessionDir) != "" {
		return filepath.Join(session.SessionDir, session.RuntimeSession.ID+".artifacts", "images")
	}
	if strings.TrimSpace(session.SessionDir) != "" {
		return filepath.Join(session.SessionDir, "images")
	}
	return ""
}

var executeToolLoop = runtimechatcore.ExecuteToolLoop
var autoCompactSharedChatHistory = maybeAutoCompactSharedChatHistory

type aicliChatExecutor interface {
	Execute(ctx context.Context, session *ChatSession, prompt string) (string, error)
	RuntimeDescriptor() aicliRuntimeExecutorDescriptor
	ToolAvailable(session *ChatSession, toolName string) bool
}

// runtimeServerURLProvider 是 aicliChatExecutor 的可选扩展接口：返回当前
// 连接的 runtime-server 基础地址（含 scheme 与 host，末尾不带 /）。
// 仅 HTTP 传输模式的 executor 需要实现；非 server 模式不必实现。
type runtimeServerURLProvider interface {
	RuntimeServerURL() string
}

type aicliRuntimeExecutorDescriptor struct {
	Core          runtimechat.RuntimeCoreDescriptor `json:"core"`
	Transport     string                            `json:"transport"`
	RuntimeEvents bool                              `json:"runtime_events"`
}

const (
	aicliRuntimeTransportInProcess = "in_process"
	aicliRuntimeTransportHTTP      = "http"
	aicliRuntimeTransportLegacy    = "legacy_in_process"
)

func newAICLIActorRuntimeDescriptor(transport string) aicliRuntimeExecutorDescriptor {
	return aicliRuntimeExecutorDescriptor{
		Core:          runtimechat.SessionActorRuntimeCore(),
		Transport:     strings.TrimSpace(transport),
		RuntimeEvents: true,
	}
}

func (d aicliRuntimeExecutorDescriptor) unifiedActorRuntime() bool {
	return runtimechat.IsSessionActorRuntimeCore(d.Core) && strings.TrimSpace(d.Transport) != "" && d.RuntimeEvents
}

type aicliGoalContinuationExecutor interface {
	ContinueGoal(ctx context.Context, session *ChatSession) (string, error)
}

type aicliSharedChatExecutor struct{}

type sharedChatExecuteOptions struct {
	ContinuationPrompt string
}

type sharedChatAutoCompactReport struct {
	Result *compactruntime.Result
	Status compactruntime.Status
}

type sharedChatPromptBudget struct {
	ActiveTurnMaxTokens                  int
	BudgetSource                         string
	BudgetSourceDetail                   string
	ResolvedProvider                     string
	ResolvedModel                        string
	ProviderContextLimit                 int
	ModelCapabilityMaxContextTokens      int
	ModelCapabilityAutoCompactRatio      float64
	ModelCapabilityAutoCompactTokenLimit int
}

func newAICLISharedChatExecutor() aicliChatExecutor {
	return &aicliSharedChatExecutor{}
}

func ensureChatExecutor(session *ChatSession) (aicliChatExecutor, error) {
	if session == nil {
		return nil, fmt.Errorf("chat session is nil")
	}
	if session.ChatExecutor == nil {
		if session.ActorFirstReady && session.LocalRuntimeHost != nil {
			session.ChatExecutor = newAICLIActorChatExecutor()
		} else {
			return nil, fmt.Errorf("chat executor is not initialized")
		}
	}
	descriptor := session.ChatExecutor.RuntimeDescriptor()
	if !descriptor.unifiedActorRuntime() {
		return nil, fmt.Errorf("chat executor does not implement the unified SessionActor runtime contract: core=%s contract=%d transport=%s",
			descriptor.Core.Name, descriptor.Core.ContractVersion, descriptor.Transport)
	}
	return session.ChatExecutor, nil
}

func (e *aicliSharedChatExecutor) RuntimeDescriptor() aicliRuntimeExecutorDescriptor {
	return aicliRuntimeExecutorDescriptor{
		Core: runtimechat.RuntimeCoreDescriptor{
			Name:            "legacy_tool_loop",
			ContractVersion: 0,
			Lifecycle:       "turn_scoped",
			StateAuthority:  "chat_history",
			EventProtocol:   "tool_loop_events",
		},
		Transport:     aicliRuntimeTransportLegacy,
		RuntimeEvents: false,
	}
}

func (e *aicliSharedChatExecutor) Execute(ctx context.Context, session *ChatSession, prompt string) (string, error) {
	return e.execute(ctx, session, prompt, sharedChatExecuteOptions{})
}

func (e *aicliSharedChatExecutor) ContinueGoal(ctx context.Context, session *ChatSession) (string, error) {
	return e.execute(ctx, session, "", sharedChatExecuteOptions{ContinuationPrompt: goalAutoContinuationPrompt})
}

func (e *aicliSharedChatExecutor) execute(ctx context.Context, session *ChatSession, prompt string, opts sharedChatExecuteOptions) (string, error) {
	if session == nil {
		return "", fmt.Errorf("chat session is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = generatedImageToolContext(ctx, session)

	history := cloneRuntimeMessages(session.Messages)
	if len(history) == 0 && session.RuntimeSession != nil && len(session.RuntimeSession.History) > 0 {
		history = cloneRuntimeMessages(session.RuntimeSession.History)
	}
	isGoalContinuation := strings.TrimSpace(opts.ContinuationPrompt) != ""
	if isGoalContinuation {
		history = append(history, goalContinuationInstructionMessage(opts.ContinuationPrompt))
	}

	var selection *aicliFunctionSelection
	var exposureDetails *skillExposureDetails
	var exposureReport *aicliFunctionExposureReport
	if catalog := ensureFunctionCatalog(session); catalog != nil && catalog.Registry() != nil {
		selection, exposureDetails = stableSharedFunctionSelectionForRequest(session, prompt)
		exposureReport = buildFunctionExposureReport(catalog, prompt, selection, exposureDetails)
		if session.SkillsDebug {
			printfDirectInteractiveOutput(session, "\n%s\n", formatSkillExposureDebug(exposureReport))
		}
	}

	renderer := newAICLIEventRenderer(session)
	var currentScope aicliLogScope
	scopePrompt := prompt
	provider := &aicliProviderTurnExecutor{
		session:        session,
		exposureReport: exposureReport,
		nextScope: func() aicliLogScope {
			currentScope = nextLogScope(session, scopePrompt)
			scopePrompt = ""
			return currentScope
		},
	}
	toolExec := &aicliToolExecutor{
		session: session,
		scopeProvider: func() aicliLogScope {
			return currentScope
		},
	}
	if !isGoalContinuation {
		if compactedHistory, compactReport, compactErr := autoCompactSharedChatHistory(ctx, session, history); compactErr != nil {
			emitSharedChatAutoCompactEvent(renderer, compactReport, compactErr)
			writeSessionDebugInfo(session, formatSharedChatAutoCompactDebug(compactReport, compactErr), true)
		} else {
			history = compactedHistory
			if compactReport != nil && compactReport.Result != nil {
				emitSharedChatAutoCompactEvent(renderer, compactReport, nil)
				writeSessionDebugInfo(session, formatSharedChatAutoCompactDebug(compactReport, nil), true)
			}
		}
	}
	promptBudget := resolveSharedChatPromptBudget(session)
	historyCompactor := buildSharedChatPromptPreflightCompactor(session, renderer)
	if isGoalContinuation {
		historyCompactor = nil
	}
	toolLoopMetadata := buildToolLoopRequestMetadataFromExposureReport(exposureReport)
	if session.DisableTools {
		if toolLoopMetadata == nil {
			toolLoopMetadata = make(map[string]interface{})
		}
		toolLoopMetadata[runtimellm.MetadataKeyDisableTools] = true
		toolLoopMetadata["tool_choice"] = "none"
	}
	if session.RetryConfig.DisableRetries {
		if toolLoopMetadata == nil {
			toolLoopMetadata = make(map[string]interface{})
		}
		toolLoopMetadata[runtimellm.MetadataKeyDisableRetries] = true
	}

	loopResult, err := executeToolLoop(ctx, runtimechatcore.ToolLoopRequest{
		Prompt:                               prompt,
		ExplicitImagePaths:                   session.ImagePaths,
		ImageArtifactDir:                     chatSessionImageArtifactDir(session),
		History:                              history,
		ActiveTurnMaxTokens:                  promptBudget.ActiveTurnMaxTokens,
		CountTokens:                          countSharedChatMessagesTokens,
		PromptBudgetSource:                   promptBudget.BudgetSource,
		PromptBudgetDetail:                   promptBudget.BudgetSourceDetail,
		ResolvedProvider:                     promptBudget.ResolvedProvider,
		ResolvedModel:                        promptBudget.ResolvedModel,
		ModelCapabilityMaxContextTokens:      promptBudget.ModelCapabilityMaxContextTokens,
		ModelCapabilityAutoCompactRatio:      promptBudget.ModelCapabilityAutoCompactRatio,
		ModelCapabilityAutoCompactTokenLimit: promptBudget.ModelCapabilityAutoCompactTokenLimit,
		HistoryCompactor:                     historyCompactor,
		Metadata:                             toolLoopMetadata,
		Stream:                               session.Stream,
		Tools:                                toolDefinitionsFromSelection(selection),
		Provider:                             provider,
		ToolExecutor:                         toolExec,
		EventSink:                            renderer.Handle,
	})
	if err != nil {
		if preflightErr, ok := agent.AsPromptPreflightError(err); ok {
			if replacement := preflightErr.CloneReplacementHistory(); len(replacement) > 0 {
				replacement = stripGoalContinuationInstructionMessages(replacement)
				if replaceErr := replaceRuntimeMessages(session, replacement); replaceErr != nil {
					err = fmt.Errorf("%w: 应用 prompt preflight 恢复历史失败: %v", err, replaceErr)
				} else {
					warnIfChatSessionSyncFails(session, "shared chatcore preflight recovery sync", syncRuntimeSessionFromChat(session))
					preflightErr.ReplacementHistoryApplied = true
				}
			}
			return "", humanizeActorExecutorError(session, err)
		}
		return "", err
	}
	if loopResult == nil || loopResult.Response == nil {
		return "", fmt.Errorf("共享 chatcore 未返回结果")
	}

	resultHistory := stripGoalContinuationInstructionMessages(loopResult.History)
	if err := replaceRuntimeMessages(session, resultHistory); err != nil {
		return "", fmt.Errorf("共享 chat history 更新失败: %w", err)
	}
	if applied := applyChatContextTokensFromUsage(session, loopResult.Response.Usage, promptBudget.ModelCapabilityMaxContextTokens, true); applied <= 0 {
		applyChatContextTokensFromMessages(session, resultHistory, promptBudget.ModelCapabilityMaxContextTokens, true)
	}
	warnIfChatSessionSyncFails(session, "shared chatcore sync", syncRuntimeSessionFromChat(session))
	warnIfChatSessionSyncFails(session, "shared chatcore post-turn reconcile", runPostTurnReconcilersAndSync(session))

	if session.Logger != nil && len(loopResult.Response.ToolExecutions) > 0 {
		callSummaries := make([]aicliToolExecutionCallSummary, 0, len(loopResult.Response.ToolExecutions))
		successCount := 0
		errorCount := 0
		for _, exec := range loopResult.Response.ToolExecutions {
			source, kind := compactToolExecutionMetadata(exec.Metadata)
			summary := aicliToolExecutionCallSummary{
				ToolCallID: exec.ToolCallID,
				Function:   exec.ToolName,
				Success:    exec.Success,
				ToolSource: source,
				OutputKind: kind,
			}
			applyToolExecutionOutputCaptureMetadata(&summary, exec.Metadata)
			applyToolExecutionShellMetadata(&summary, exec.Metadata)
			if exec.Success {
				successCount++
				summary.ResultPreview = truncateOutputPreview(exec.Output, maxToolResultPreviewLines, maxToolResultPreviewBytes)
				summary.ResultBytes = len(exec.Output)
			} else {
				errorCount++
				summary.Error = exec.Error
			}
			callSummaries = append(callSummaries, summary)
		}
		summary := buildToolExecutionSummary(callSummaries, successCount, errorCount)
		session.Logger.LogToolExecutionSummary(currentScope, summary)
		writeSessionDebugInfo(session, formatToolExecutionSummaryDebug(summary), true)
	}

	var finalMessage *runtimetypes.Message
	if count := len(resultHistory); count > 0 {
		finalMessage = &resultHistory[count-1]
	}
	renderer.Finalize(loopResult.Response, finalMessage)

	return loopResult.Response.Output, nil
}

func resolveSharedChatActiveTurnPromptBudget(session *ChatSession) int {
	return resolveSharedChatPromptBudget(session).ActiveTurnMaxTokens
}

func resolveSharedChatPromptBudget(session *ChatSession) sharedChatPromptBudget {
	budget := sharedChatPromptBudget{}
	if session == nil {
		return budget
	}
	budget.ResolvedProvider = strings.TrimSpace(session.ProviderName)
	if budget.ResolvedProvider == "" {
		budget.ResolvedProvider = strings.TrimSpace(session.Provider.Protocol)
	}
	budget.ResolvedModel = strings.TrimSpace(session.Model)
	capability, ok := runtimellm.ResolveModelCapabilitySpec(session.Model, session.Provider.ModelCapabilities)
	if ok {
		budget.ModelCapabilityMaxContextTokens = capability.MaxContextTokens
		budget.ModelCapabilityAutoCompactTokenLimit = capability.AutoCompactTokenLimit
		if capability.AutoCompactTokenLimit > 0 {
			budget.ActiveTurnMaxTokens = capability.AutoCompactTokenLimit
			budget.BudgetSource = "model_capability_auto_compact_token_limit"
			budget.BudgetSourceDetail = "model capability auto-compact token limit"
			return budget
		}
		if capability.MaxContextTokens > 0 {
			ratio := capability.AutoCompactRatio
			ratioDetail := fmt.Sprintf("model capability auto-compact ratio %.2f", capability.AutoCompactRatio)
			if ratio <= 0 || ratio >= 1 {
				ratio = sharedChatDefaultAutoCompactRatio
				ratioDetail = fmt.Sprintf("fallback auto-compact ratio %.2f", sharedChatDefaultAutoCompactRatio)
			}
			budget.ModelCapabilityAutoCompactRatio = ratio
			limit := int(math.Floor(float64(capability.MaxContextTokens) * ratio))
			if limit <= 0 || limit > capability.MaxContextTokens {
				budget.ActiveTurnMaxTokens = capability.MaxContextTokens
				budget.BudgetSource = "model_capability_max_context_tokens"
				budget.BudgetSourceDetail = fmt.Sprintf("model capability max_context_tokens=%d", capability.MaxContextTokens)
				return budget
			}
			budget.ActiveTurnMaxTokens = limit
			budget.BudgetSource = "model_capability_auto_compact_ratio"
			budget.BudgetSourceDetail = fmt.Sprintf("%s over max_context_tokens=%d", ratioDetail, capability.MaxContextTokens)
			return budget
		}
	}
	budget.ProviderContextLimit = sharedChatDefaultContextWindowTokens
	limit := int(math.Floor(float64(budget.ProviderContextLimit) * sharedChatDefaultAutoCompactRatio))
	if limit <= 0 || limit > budget.ProviderContextLimit {
		limit = budget.ProviderContextLimit
	}
	budget.ActiveTurnMaxTokens = limit
	budget.BudgetSource = "default_context_window_default_ratio"
	budget.BudgetSourceDetail = fmt.Sprintf("floor(default context window * %.2f)", sharedChatDefaultAutoCompactRatio)
	return budget
}

func countSharedChatMessagesTokens(messages []runtimetypes.Message) int {
	total := 0
	for _, message := range messages {
		total += estimateSharedChatTokenCount(message.Role)
		total += estimateSharedChatTokenCount(message.Content)
		total += estimateSharedChatTokenCount(message.ToolCallID)
		total += 4
		for _, call := range message.ToolCalls {
			total += estimateSharedChatTokenCount(call.ID)
			total += estimateSharedChatTokenCount(call.Name)
			if len(call.Args) == 0 {
				continue
			}
			if payload, err := json.Marshal(call.Args); err == nil {
				total += estimateSharedChatTokenCount(string(payload))
			} else {
				total += estimateSharedChatTokenCount(fmt.Sprintf("%v", call.Args))
			}
		}
	}
	return total
}

func estimateSharedChatTokenCount(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	tokens := len([]rune(text)) / 4
	if tokens <= 0 {
		return 1
	}
	return tokens
}

func maybeAutoCompactSharedChatHistory(ctx context.Context, session *ChatSession, history []runtimetypes.Message) ([]runtimetypes.Message, *sharedChatAutoCompactReport, error) {
	if session == nil || len(history) == 0 {
		return history, nil, nil
	}
	llmRuntime, err := buildSharedChatAutoCompactRuntime(session)
	if err != nil {
		return history, nil, err
	}
	if llmRuntime == nil {
		return history, nil, nil
	}

	sessionID := ""
	if session.RuntimeSession != nil {
		sessionID = strings.TrimSpace(session.RuntimeSession.ID)
	}
	result, status, err := compactruntime.New(llmRuntime, nil).MaybeCompact(ctx, compactruntime.Request{
		SessionID:         sessionID,
		TaskID:            sessionID,
		Provider:          strings.TrimSpace(session.ProviderName),
		Model:             strings.TrimSpace(session.Model),
		History:           history,
		Phase:             compactruntime.PhasePreTurn,
		CountTokens:       llmRuntime.CountMessagesTokens,
		ObservedTokens:    resolveChatContextSnapshotTokens(session, history),
		HasObservedTokens: true,
		Tools:             stableSharedToolDefinitions(session),
	})
	report := &sharedChatAutoCompactReport{
		Result: result,
		Status: status,
	}
	if err != nil || result == nil || len(result.ReplacementHistory) == 0 {
		applyChatCompactContextUsage(session, result, status, false)
		return history, report, err
	}
	if session.RuntimeSession != nil {
		replacement, reconciliation := contextreconcile.Reconcile(
			result.ReplacementHistory,
			runtimechat.CanonicalContextSnapshot(session.RuntimeSession),
		)
		result.ReplacementHistory = replacement
		result.Reconciliation = &reconciliation
		result.TokenAfter = llmRuntime.CountMessagesTokens(replacement)
	}

	// Capture root title before history rewrite so the compaction summary cannot
	// become the display title when syncRuntimeSessionFromChat replaces history.
	rootTitleHint := ""
	parentSessionID := ""
	if session.RuntimeSession != nil {
		rootTitleHint = session.RuntimeSession.CompactRootTitleCandidate()
		parentSessionID = strings.TrimSpace(session.RuntimeSession.ID)
	}
	if err := replaceRuntimeMessages(session, result.ReplacementHistory); err != nil {
		return history, report, fmt.Errorf("共享 chat 自动压缩结果更新失败: %w", err)
	}
	// History rewrites must never reuse the previous provider cache
	// generation. The actor-first path does this inside
	// replaceSessionHistoryAndAdvancePromptCacheEpoch; the shared-chat
	// fallback advances the same durable session context so any request
	// built after this point derives the new epoch-scoped cache key.
	advanceSharedChatPromptCacheEpoch(session)
	if session.RuntimeSession != nil {
		// Shared-chat fallback path does not go through actor.Compact; apply the
		// same in-place compact title lineage used by the actor-first path.
		session.RuntimeSession.ApplyCompactTitleLineage(parentSessionID, rootTitleHint)
	}
	applyChatCompactContextUsage(session, result, status, true)
	warnIfChatSessionSyncFails(session, "shared chat auto compact sync", syncRuntimeSessionFromChat(session))
	refreshChatTitleMetadata(session)
	return cloneSharedChatRuntimeMessages(result.ReplacementHistory), report, nil
}

// chatSessionPromptCacheEpoch reads the durable prompt-cache generation that
// the actor-first path maintains under agent.PromptCacheEpochSessionContextKey.
func chatSessionPromptCacheEpoch(session *ChatSession) int {
	if session == nil || session.RuntimeSession == nil {
		return 0
	}
	epoch := chatContextIntValue(session.RuntimeSession.Metadata.Context, agent.PromptCacheEpochSessionContextKey)
	if epoch < 0 {
		return 0
	}
	return epoch
}

// advanceSharedChatPromptCacheEpoch marks a rewritten prompt history as a new
// provider cache generation. It mirrors the actor-side rule
// (replaceSessionHistoryAndAdvancePromptCacheEpoch): compaction / restore /
// rollback history rewrites must never associate the rewritten prefix with the
// previous generation's cache key.
func advanceSharedChatPromptCacheEpoch(session *ChatSession) int {
	if session == nil || session.RuntimeSession == nil {
		return 0
	}
	next := chatSessionPromptCacheEpoch(session) + 1
	session.RuntimeSession.SetContext(agent.PromptCacheEpochSessionContextKey, next)
	return next
}

// sharedChatPromptCacheKeyForEpoch mirrors agent.promptCacheKeyForEpoch so both
// request shapes derive the same cache key for a given session/generation:
// append-only turns keep the bare session key, rewritten history moves to
// "sessionID#prompt-cache-epoch-N".
func sharedChatPromptCacheKeyForEpoch(sessionID string, epoch int) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || epoch <= 0 {
		return sessionID
	}
	return fmt.Sprintf("%s#prompt-cache-epoch-%d", sessionID, epoch)
}

// chatContextIntValue reads a numeric value from session context, tolerating
// the JSON round-trip shapes (float64) and in-memory int shapes (int/int64).
func chatContextIntValue(ctx map[string]interface{}, key string) int {
	if ctx == nil {
		return 0
	}
	raw, ok := ctx[key]
	if !ok || raw == nil {
		return 0
	}
	switch v := raw.(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float32:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

func applyChatCompactContextUsage(session *ChatSession, result *compactruntime.Result, status compactruntime.Status, forceRefresh bool) {
	if session == nil {
		return
	}
	windowTokens := status.MaxContextTokens
	if result != nil {
		session.TurnContextTokenCount = 0
		contextTokens := result.TokenAfter
		if contextTokens <= 0 && len(result.ReplacementHistory) > 0 {
			contextTokens = countChatContextTokensForMessages(session, result.ReplacementHistory)
		}
		if contextTokens > 0 {
			applyChatContextTokensReset(session, contextTokens, windowTokens, forceRefresh)
			return
		}
		if forceRefresh && session.Interaction != nil {
			session.Interaction.RefreshStatus("")
		}
		return
	}
	if windowTokens > 0 && session.ContextWindowTokenCount != windowTokens {
		session.ContextWindowTokenCount = windowTokens
		if forceRefresh && session.Interaction != nil {
			session.Interaction.RefreshStatus("")
		}
	}
}

func buildSharedChatPromptPreflightCompactor(session *ChatSession, renderer *aicliEventRenderer) runtimechatcore.HistoryCompactor {
	if session == nil {
		return nil
	}
	llmRuntime, err := buildSharedChatAutoCompactRuntime(session)
	if err != nil || llmRuntime == nil {
		return nil
	}
	compactor := compactruntime.New(llmRuntime, nil)

	sessionID := ""
	if session.RuntimeSession != nil {
		sessionID = strings.TrimSpace(session.RuntimeSession.ID)
	}
	providerName := strings.TrimSpace(session.ProviderName)
	model := strings.TrimSpace(session.Model)

	return func(ctx context.Context, history []runtimetypes.Message) ([]runtimetypes.Message, bool, error) {
		if len(history) == 0 {
			return history, false, nil
		}
		result, status, err := compactor.MaybeCompact(ctx, compactruntime.Request{
			SessionID:         sessionID,
			TaskID:            sessionID,
			Provider:          providerName,
			Model:             model,
			Mode:              compactruntime.ModeLocal,
			Force:             true,
			History:           history,
			Phase:             "mid_turn",
			CountTokens:       countSharedChatMessagesTokens,
			ObservedTokens:    resolveChatContextSnapshotTokens(session, history),
			HasObservedTokens: true,
			Tools:             stableSharedToolDefinitions(session),
		})
		report := &sharedChatAutoCompactReport{
			Result: result,
			Status: status,
		}
		if err != nil {
			emitSharedChatAutoCompactEvent(renderer, report, err)
			writeSessionDebugInfo(session, formatSharedChatAutoCompactDebug(report, err), true)
			return history, false, err
		}
		if result == nil || len(result.ReplacementHistory) == 0 {
			return history, false, nil
		}
		emitSharedChatAutoCompactEvent(renderer, report, nil)
		writeSessionDebugInfo(session, formatSharedChatAutoCompactDebug(report, nil), true)
		return cloneSharedChatRuntimeMessages(result.ReplacementHistory), true, nil
	}
}

func buildSharedChatAutoCompactRuntime(session *ChatSession) (*runtimellm.LLMRuntime, error) {
	if session == nil {
		return nil, nil
	}
	providerType := strings.TrimSpace(session.Provider.GetType())
	if providerType == "" {
		return nil, nil
	}

	providerName := strings.TrimSpace(session.ProviderName)
	if providerName == "" {
		providerName = "shared-chat-provider"
	}
	defaultModel := strings.TrimSpace(session.Model)
	if defaultModel == "" {
		defaultModel = strings.TrimSpace(session.Provider.DefaultModel)
	}
	maxRetries := runtimellm.ProviderMaxRetriesFromAgentConfig(session.Config)
	// Auto-compact must not inherit "unlimited" retries (config nil / -1).
	// Local compact already disables per-request retries and falls back to a
	// deterministic summary; keep the runtime/provider caps finite too.
	if maxRetries < 0 {
		maxRetries = 0
	}
	retryTuning := runtimellm.RetryTuningFromAgentConfig(session.Config)
	retryRules := runtimellm.RetryRulesFromAgentConfig(session.Config)

	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{
		DefaultProvider: providerName,
		DefaultModel:    defaultModel,
		MaxRetries:      maxRetries,
		RetryTuning:     retryTuning,
		RetryRules:      retryRules,
	})
	provider, err := runtimellm.NewProvider(&runtimellm.ProviderConfig{
		Type:                    providerType,
		APIKey:                  session.Provider.GetAPIKey(),
		BaseURL:                 session.Provider.BaseURL,
		APIPath:                 session.Provider.APIPath,
		CompatibilityProfile:    session.Provider.Compatibility.Profile,
		Timeout:                 session.Provider.Timeout,
		MaxRetries:              maxRetries,
		MaxTransportRetries:     runtimellm.ProviderMaxTransportRetriesFromAgentConfig(session.Config),
		RetryTuning:             retryTuning,
		RetryRules:              retryRules,
		DefaultModel:            strings.TrimSpace(session.Provider.DefaultModel),
		SupportedModels:         append([]string(nil), session.Provider.SupportedModels...),
		ModelMappings:           cloneStringMap(session.Provider.ModelMappings),
		ModelCapabilities:       cloneProviderModelCapabilities(session.Provider.ModelCapabilities),
		EnableImageGeneration:   session.Provider.EnableImageGeneration,
		Headers:                 effectiveChatProviderHeaders(session),
		HeaderMappings:          cloneStringMap(session.Provider.HeaderMappings),
		HeaderMappingRules:      cloneHeaderMappingRules(session.Provider.HeaderMappingRules),
		SupportsMaxOutputTokens: session.Provider.SupportsMaxOutputTokens,
		Proxy:                   session.Provider.Proxy.Clone(),
		RequestsPerMinute:       session.Provider.RequestsPerMinute,
		StreamReadTimeout:       runtimellm.ProviderStreamReadTimeoutFromAgentConfig(session.Config),
		ResponseHeaderTimeout:   runtimellm.ProviderResponseHeaderTimeoutFromAgentConfig(session.Config),
	})
	if err != nil {
		return nil, err
	}
	if err := llmRuntime.RegisterProvider(providerName, provider); err != nil {
		return nil, err
	}

	aliases := []string{session.Model, session.Provider.DefaultModel}
	aliases = append(aliases, session.Provider.SupportedModels...)
	for _, alias := range aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			continue
		}
		_ = llmRuntime.RegisterProviderAlias(alias, providerName)
	}
	return llmRuntime, nil
}

func cloneSharedChatRuntimeMessages(messages []runtimetypes.Message) []runtimetypes.Message {
	if len(messages) == 0 {
		return nil
	}
	cloned := make([]runtimetypes.Message, len(messages))
	for index := range messages {
		cloned[index] = *messages[index].Clone()
	}
	return cloned
}

func formatSharedChatAutoCompactDebug(report *sharedChatAutoCompactReport, err error) string {
	if report == nil {
		if err == nil {
			return ""
		}
		return fmt.Sprintf("[context-debug] shared auto-compact failed: %v", err)
	}
	if err != nil {
		statusReason := strings.TrimSpace(report.Status.Reason)
		if statusReason == "" {
			statusReason = "unknown"
		}
		return fmt.Sprintf("[context-debug] shared auto-compact failed reason=%s error=%v", statusReason, err)
	}
	if report.Result == nil {
		return ""
	}
	return fmt.Sprintf(
		"[context-debug] shared auto-compact applied mode=%s token_before=%d token_after=%d compacted_messages=%d history_messages=%d",
		report.Result.Mode,
		report.Result.TokenBefore,
		report.Result.TokenAfter,
		report.Result.CompactedMessages,
		len(report.Result.ReplacementHistory),
	)
}

func emitSharedChatAutoCompactEvent(renderer *aicliEventRenderer, report *sharedChatAutoCompactReport, err error) {
	if renderer == nil {
		return
	}
	event, ok := sharedChatAutoCompactChatEvent(report, err)
	if !ok {
		return
	}
	renderer.Handle(event)
}

func sharedChatAutoCompactChatEvent(report *sharedChatAutoCompactReport, err error) (runtimechatcore.ChatEvent, bool) {
	if report == nil {
		return runtimechatcore.ChatEvent{}, false
	}
	metadata := map[string]interface{}{
		"category": "context",
		"name":     "shared_auto_compact",
	}
	if err != nil {
		if reason := strings.TrimSpace(report.Status.Reason); reason != "" {
			metadata["reason"] = reason
		}
		return runtimechatcore.ChatEvent{
			Type:     runtimechatcore.EventWarning,
			Content:  formatSharedChatAutoCompactWarning(report, err),
			Metadata: metadata,
		}, true
	}
	if report.Result == nil {
		return runtimechatcore.ChatEvent{}, false
	}
	metadata["mode"] = report.Result.Mode
	metadata["token_before"] = report.Result.TokenBefore
	metadata["token_after"] = report.Result.TokenAfter
	metadata["compacted_messages"] = report.Result.CompactedMessages
	metadata["history_messages"] = len(report.Result.ReplacementHistory)
	if reconciliation := report.Result.Reconciliation; reconciliation != nil {
		metadata["context_drift_count"] = reconciliation.DriftCount
		metadata["context_correction_made"] = reconciliation.CorrectionMade
		metadata["context_corrections"] = reconciliation.Corrections
	}
	return runtimechatcore.ChatEvent{
		Type:     runtimechatcore.EventWarning,
		Content:  formatSharedChatAutoCompactApplied(report.Result),
		Metadata: metadata,
	}, true
}

func formatSharedChatAutoCompactApplied(result *compactruntime.Result) string {
	if result == nil {
		return ""
	}
	return fmt.Sprintf(
		"[context] shared auto-compact applied mode=%s token %d -> %d compacted_messages=%d history_messages=%d",
		firstNonEmptyChatValue(strings.TrimSpace(result.Mode), compactruntime.ModeLocal),
		result.TokenBefore,
		result.TokenAfter,
		result.CompactedMessages,
		len(result.ReplacementHistory),
	)
}

func formatSharedChatAutoCompactWarning(report *sharedChatAutoCompactReport, err error) string {
	if err == nil {
		return ""
	}
	reason := "unknown"
	if report != nil && strings.TrimSpace(report.Status.Reason) != "" {
		reason = strings.TrimSpace(report.Status.Reason)
	}
	return fmt.Sprintf("[context] shared auto-compact failed reason=%s error=%v", reason, err)
}

func renderSharedChatWarningEvent(event runtimechatcore.ChatEvent) string {
	if event.Type != runtimechatcore.EventWarning {
		return ""
	}
	if len(event.Metadata) == 0 {
		return ""
	}
	if !strings.EqualFold(strings.TrimSpace(firstNonEmptyString(event.Metadata["category"])), "context") {
		return ""
	}
	return strings.TrimSpace(event.Content)
}

type aicliEventRenderer struct {
	session        *ChatSession
	transcript     *aicliTranscriptRenderer
	streamBuffer   strings.Builder
	streamLines    int
	reasoningOpen  bool
	spinnerCleared bool
}

func newAICLIEventRenderer(session *ChatSession) *aicliEventRenderer {
	return &aicliEventRenderer{
		session:    session,
		transcript: newAICLITranscriptRenderer(session),
	}
}

func (r *aicliEventRenderer) Handle(event runtimechatcore.ChatEvent) {
	if r == nil || r.session == nil {
		return
	}
	if r.session.ExecEventBridge != nil {
		r.session.ExecEventBridge.HandleChatCoreEvent(event)
	}
	// TerminalSession ownership survives coordinator teardown. The runtime event
	// bridge above must still observe the semantic event, but this compatibility
	// renderer must not reopen its stdout branches afterwards.
	if unifiedInteractiveOutputMustFailClosed(r.session) {
		return
	}
	switch event.Type {
	case runtimechatcore.EventPlanning:
		if !r.session.Stream || !shouldRenderChatReasoning(r.session) {
			return
		}
		if event.Content == "" {
			return
		}
		r.clearSpinner()
		if r.session.Interaction != nil {
			r.session.Interaction.RenderReasoningDelta(&runtimetypes.ReasoningBlock{
				Format:     "stream_delta",
				Summary:    event.Content,
				Streamable: true,
				Visibility: runtimetypes.ReasoningVisibilitySummary,
			})
			r.reasoningOpen = true
			return
		}
		if !r.reasoningOpen {
			fmt.Println("\n--- Thinking ---")
			r.reasoningOpen = true
		}
		fmt.Print(event.Content)
	case runtimechatcore.EventResult:
		if !r.session.Stream || !shouldRenderInteractiveOutput(r.session) {
			return
		}
		if event.Content == "" {
			return
		}
		r.clearSpinner()
		if r.session.Interaction != nil {
			if r.reasoningOpen {
				// Feed the first result while reasoning is still open. The
				// coordinator can then retain the provisional assistant source
				// until its final presentation (plain vs Markdown) is known,
				// instead of flushing an irreversible plain "• " prefix before
				// a later result chunk reveals Markdown.
				r.streamBuffer.WriteString(event.Content)
				r.streamLines += strings.Count(event.Content, "\n")
				r.session.Interaction.RenderAssistantDelta(event.Content)
				r.session.Interaction.FinalizeReasoningDelta()
				r.reasoningOpen = false
				return
			}
			r.streamBuffer.WriteString(event.Content)
			r.streamLines += strings.Count(event.Content, "\n")
			r.session.Interaction.RenderAssistantDelta(event.Content)
			return
		}
		if r.reasoningOpen {
			fmt.Print("\n--- End Thinking ---\n\n")
			r.reasoningOpen = false
		}
		r.streamBuffer.WriteString(event.Content)
		r.streamLines += strings.Count(event.Content, "\n")
		fmt.Print(event.Content)
	case runtimechatcore.EventTool:
		if !shouldRenderInteractiveOutput(r.session) {
			return
		}
		if event.Stage == "batch_start" {
			r.flushAssistantTurnForToolBatch()
		}
		r.transcript.RenderToolEvent(event)
	case runtimechatcore.EventWarning:
		if !shouldRenderInteractiveOutput(r.session) {
			return
		}
		if event.Content == "" {
			return
		}
		r.clearSpinner()
		if rendered := renderSharedChatWarningEvent(event); rendered != "" {
			r.transcript.RenderSupplement(rendered)
			return
		}
		r.transcript.RenderSupplement(fmt.Sprintf("⚠ %s", event.Content))
	}
}

func (r *aicliEventRenderer) Finalize(response *runtimechatcore.ChatResult, finalMessage *runtimetypes.Message) {
	if r == nil || r.session == nil {
		return
	}
	if unifiedInteractiveOutputMustFailClosed(r.session) {
		return
	}

	reasoningBlock := finalReasoningBlock(finalMessage)
	if reasoningBlock != nil && shouldRenderChatReasoning(r.session) && !r.session.Stream {
		r.clearSpinner()
		r.transcript.RenderReasoning(reasoningBlock)
	}

	if r.session.Stream && shouldRenderInteractiveOutput(r.session) {
		if r.session.Interaction != nil {
			if r.reasoningOpen {
				r.session.Interaction.FinalizeReasoningDelta()
				r.reasoningOpen = false
			}
			if response != nil && (strings.TrimSpace(response.Output) != "" || r.streamBuffer.Len() > 0) {
				if !r.session.Interaction.CompleteAssistantResponse(response.Output) {
					r.session.Interaction.FinalizeAssistantDelta()
				}
			} else {
				r.session.Interaction.FinalizeAssistantDelta()
			}
			return
		}
		if r.reasoningOpen {
			fmt.Print("\n--- End Thinking ---\n\n")
			r.reasoningOpen = false
		}
		content := r.streamBuffer.String()
		if content != "" && r.session.Formatter != nil && r.session.Formatter.IsMarkdown(content) {
			fmt.Printf("\033[%dF", r.streamLines+1)
			fmt.Printf("\033[J")
			fmt.Printf("%s\n\n", r.session.Formatter.Format(content))
		} else if r.spinnerCleared {
			fmt.Println()
		}
		return
	}

	r.clearSpinner()
}

func finalReasoningBlock(finalMessage *runtimetypes.Message) *runtimetypes.ReasoningBlock {
	if finalMessage == nil || finalMessage.Metadata == nil {
		return nil
	}
	if block := runtimetypes.GetReasoningBlock(finalMessage.Metadata); block != nil {
		return block
	}
	if reasoning := finalMessage.Metadata.GetString(chatcoreReasoningMetadataKey, ""); strings.TrimSpace(reasoning) != "" {
		return &runtimetypes.ReasoningBlock{
			Summary:    strings.TrimSpace(reasoning),
			Visibility: runtimetypes.ReasoningVisibilitySummary,
		}
	}
	return nil
}

func (r *aicliEventRenderer) clearSpinner() {
	if r.spinnerCleared {
		return
	}
	if r.session == nil || r.session.NoInteractive {
		r.spinnerCleared = true
		return
	}
	if r.session.Interaction != nil {
		r.session.Interaction.ClearThinking()
		r.spinnerCleared = true
		return
	}
	if unifiedInteractiveOutputMustFailClosed(r.session) {
		r.spinnerCleared = true
		return
	}
	fmt.Print("\r   \r")
	r.spinnerCleared = true
}

func (r *aicliEventRenderer) flushAssistantTurnForToolBatch() {
	if r == nil || r.session == nil {
		return
	}
	r.clearSpinner()
	if r.session.Interaction != nil {
		if r.reasoningOpen {
			r.session.Interaction.FinalizeReasoningDelta()
			r.reasoningOpen = false
		}
		r.session.Interaction.FinalizeAssistantDelta()
		r.resetAssistantStreamState()
		return
	}
	if unifiedInteractiveOutputMustFailClosed(r.session) {
		r.resetAssistantStreamState()
		return
	}
	if r.reasoningOpen {
		fmt.Print("\n--- End Thinking ---\n\n")
		r.reasoningOpen = false
	}
	r.resetAssistantStreamState()
}

func (r *aicliEventRenderer) resetAssistantStreamState() {
	if r == nil {
		return
	}
	r.streamBuffer.Reset()
	r.streamLines = 0
}

func shouldRenderInteractiveOutput(session *ChatSession) bool {
	return session != nil && !session.NoInteractive && !session.JSONOutput
}

func chatEventInt(event runtimechatcore.ChatEvent, key string) int {
	if event.Metadata == nil {
		return 0
	}
	switch value := event.Metadata[key].(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}
