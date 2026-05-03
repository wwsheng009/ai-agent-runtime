package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/compactruntime"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const chatcoreReasoningMetadataKey = "chatcore_reasoning_content"
const sharedChatDefaultAutoCompactRatio = 0.9
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
}

type aicliSharedChatExecutor struct{}

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

func ensureChatExecutor(session *ChatSession) aicliChatExecutor {
	if session == nil {
		return newAICLISharedChatExecutor()
	}
	if session.ChatExecutor == nil {
		if session.ActorFirstReady {
			session.ChatExecutor = newAICLIActorChatExecutor()
		} else {
			session.ChatExecutor = newAICLISharedChatExecutor()
		}
	}
	return session.ChatExecutor
}

func (e *aicliSharedChatExecutor) Execute(ctx context.Context, session *ChatSession, prompt string) (string, error) {
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

	var selection *aicliFunctionSelection
	var exposureDetails *skillExposureDetails
	var exposureReport *aicliFunctionExposureReport
	if !session.DisableTools {
		if catalog := ensureFunctionCatalog(session); catalog != nil && catalog.Registry() != nil {
			selection, exposureDetails = catalog.SelectRequestFunctions(session, prompt)
			exposureReport = buildFunctionExposureReport(catalog, prompt, selection, exposureDetails)
			if session.SkillsDebug {
				beginDirectInteractiveOutput(session)
				fmt.Printf("\n%s\n", formatSkillExposureDebug(exposureReport))
			}
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
	promptBudget := resolveSharedChatPromptBudget(session)

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
		HistoryCompactor:                     buildSharedChatPromptPreflightCompactor(session, renderer),
		Metadata:                             buildToolLoopRequestMetadataFromExposureReport(exposureReport),
		Stream:                               session.Stream,
		Tools:                                toolDefinitionsFromSelection(selection),
		Provider:                             provider,
		ToolExecutor:                         toolExec,
		EventSink:                            renderer.Handle,
	})
	if err != nil {
		if preflightErr, ok := agent.AsPromptPreflightError(err); ok {
			if replacement := preflightErr.CloneReplacementHistory(); len(replacement) > 0 {
				if replaceErr := replaceRuntimeMessages(session, replacement); replaceErr != nil {
					err = fmt.Errorf("%w: 应用 prompt preflight 恢复历史失败: %v", err, replaceErr)
				} else {
					applyChatContextTokensFromMessages(session, replacement, promptBudget.ModelCapabilityMaxContextTokens, true)
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

	if err := replaceRuntimeMessages(session, loopResult.History); err != nil {
		return "", fmt.Errorf("共享 chat history 更新失败: %w", err)
	}
	applyChatContextTokensFromMessages(session, loopResult.History, promptBudget.ModelCapabilityMaxContextTokens, true)
	warnIfChatSessionSyncFails(session, "shared chatcore sync", syncRuntimeSessionFromChat(session))

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
	if count := len(loopResult.History); count > 0 {
		finalMessage = &loopResult.History[count-1]
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
		ObservedTokens:    session.TokenCount,
		HasObservedTokens: true,
	})
	report := &sharedChatAutoCompactReport{
		Result: result,
		Status: status,
	}
	if err != nil || result == nil || len(result.ReplacementHistory) == 0 {
		applyChatCompactContextUsage(session, result, status, false)
		return history, report, err
	}

	if err := replaceRuntimeMessages(session, result.ReplacementHistory); err != nil {
		return history, report, fmt.Errorf("共享 chat 自动压缩结果更新失败: %w", err)
	}
	applyChatCompactContextUsage(session, result, status, true)
	warnIfChatSessionSyncFails(session, "shared chat auto compact sync", syncRuntimeSessionFromChat(session))
	return cloneSharedChatRuntimeMessages(result.ReplacementHistory), report, nil
}

func applyChatCompactContextUsage(session *ChatSession, result *compactruntime.Result, status compactruntime.Status, forceRefresh bool) {
	if session == nil {
		return
	}
	windowTokens := status.MaxContextTokens
	if result != nil {
		session.TokenCount = 0
		session.TurnContextTokenCount = 0
		contextTokens := result.TokenAfter
		if contextTokens <= 0 && len(result.ReplacementHistory) > 0 {
			contextTokens = countChatContextTokensForMessages(session, result.ReplacementHistory)
		}
		if contextTokens > 0 {
			applyChatContextTokens(session, contextTokens, windowTokens, forceRefresh)
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
			SessionID:   sessionID,
			TaskID:      sessionID,
			Provider:    providerName,
			Model:       model,
			Mode:        compactruntime.ModeLocal,
			Force:       true,
			History:     history,
			Phase:       "mid_turn",
			CountTokens: countSharedChatMessagesTokens,
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

	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{
		DefaultProvider: providerName,
		DefaultModel:    defaultModel,
	})
	provider, err := runtimellm.NewProvider(&runtimellm.ProviderConfig{
		Type:                    providerType,
		APIKey:                  session.Provider.GetAPIKey(),
		BaseURL:                 session.Provider.BaseURL,
		APIPath:                 session.Provider.APIPath,
		Timeout:                 session.Provider.Timeout,
		MaxRetries:              1,
		DefaultModel:            strings.TrimSpace(session.Provider.DefaultModel),
		SupportedModels:         append([]string(nil), session.Provider.SupportedModels...),
		ModelMappings:           cloneStringMap(session.Provider.ModelMappings),
		ModelCapabilities:       cloneProviderModelCapabilities(session.Provider.ModelCapabilities),
		Headers:                 cloneStringMap(session.Provider.Headers),
		HeaderMappings:          cloneStringMap(session.Provider.HeaderMappings),
		HeaderMappingRules:      cloneHeaderMappingRules(session.Provider.HeaderMappingRules),
		SupportsMaxOutputTokens: session.Provider.SupportsMaxOutputTokens,
		Proxy:                   session.Provider.Proxy.Clone(),
		RequestsPerMinute:       session.Provider.RequestsPerMinute,
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
	streamBuffer   strings.Builder
	streamLines    int
	reasoningOpen  bool
	spinnerCleared bool
}

func newAICLIEventRenderer(session *ChatSession) *aicliEventRenderer {
	return &aicliEventRenderer{session: session}
}

func (r *aicliEventRenderer) Handle(event runtimechatcore.ChatEvent) {
	if r == nil || r.session == nil {
		return
	}
	switch event.Type {
	case runtimechatcore.EventPlanning:
		if !r.session.Stream || !shouldRenderInteractiveOutput(r.session) {
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
				r.session.Interaction.FinalizeReasoningDelta()
				r.reasoningOpen = false
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
		rendered := renderSharedChatToolEvent(event)
		if strings.TrimSpace(rendered) == "" {
			return
		}
		if r.session.Interaction != nil {
			r.session.Interaction.RenderAsyncLine(rendered)
			return
		}
		fmt.Println(rendered)
	case runtimechatcore.EventWarning:
		if !shouldRenderInteractiveOutput(r.session) {
			return
		}
		if event.Content == "" {
			return
		}
		r.clearSpinner()
		if rendered := renderSharedChatWarningEvent(event); rendered != "" {
			if r.session.Interaction != nil {
				r.session.Interaction.RenderAsyncLine(rendered)
				return
			}
			fmt.Println(rendered)
			return
		}
		if r.session.Interaction != nil {
			r.session.Interaction.RenderAsyncLine(fmt.Sprintf("⚠ %s", event.Content))
			return
		}
		fmt.Printf("\n⚠ %s\n", event.Content)
	}
}

func (r *aicliEventRenderer) Finalize(response *runtimechatcore.ChatResult, finalMessage *runtimetypes.Message) {
	if r == nil || r.session == nil {
		return
	}

	reasoningBlock := finalReasoningBlock(finalMessage)
	if reasoningBlock != nil && shouldRenderInteractiveOutput(r.session) && !r.session.Stream {
		r.clearSpinner()
		lines := chatReasoningLines(reasoningBlock)
		if len(lines) > 0 {
			if r.session.Interaction != nil {
				r.session.Interaction.RenderAsyncLine(strings.Join(lines, "\n"))
			} else {
				fmt.Println(strings.Join(lines, "\n"))
			}
		}
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
			fmt.Printf("助手> %s\n\n", r.session.Formatter.Format(content))
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
