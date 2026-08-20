package compactruntime

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	"github.com/wwsheng009/ai-agent-runtime/internal/contextmgr"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const (
	localSummaryCheckpointReason   = "history_window_summary_segment"
	localSegmentStartKey           = "segment_start"
	localSegmentEndKey             = "segment_end"
	localSummaryTextKey            = "summary_text"
	localSummaryHeading            = "Compacted context from earlier turns:"
	localRetainedRecentMaxTokens   = 20000
	localCompactDefaultMaxTokens   = 2048
	localCompactMaxRequestBytes    = 1024 * 1024
	localFallbackUserRunes         = 2200
	localFallbackAssistantRunes    = 2200
	localFallbackToolRequestRunes  = 1400
	localFallbackToolRunes         = 3600
	localFallbackFailureRunes      = 1800
	localFallbackConstraintRunes   = 1400
	localFallbackReferenceRunes    = 1800
	localFallbackRemainingRunes    = 1400
	localFallbackDurableRunes      = 2200
	localFallbackPriorSummaryRunes = 3600
	localCompactionPrompt          = `You are performing a CONTEXT CHECKPOINT COMPACTION. Create a handoff summary for another LLM that will resume the task.

Use this structure when the information exists:
1. Current goal / latest user objective
2. Constraints, preferences, and hard requirements
3. Key decisions and progress so far
4. Critical references (paths, commands, IDs, evidence)
5. Failures and pitfalls to avoid repeating
6. Remaining work and concrete next steps

Rules:
- Prefer durable facts over transient chatter.
- Preserve exact paths, commands, identifiers, error text, and decisions when present.
- Do not invent work that was not already in the history.
- Be concise, structured, and continuation-ready.`
)

type LocalAdapter struct {
	llmRuntime     *llm.LLMRuntime
	contextManager *contextmgr.Manager
}

type checkpointLister interface {
	ListCheckpoints(ctx context.Context, sessionID string, limit, offset int) ([]artifact.Checkpoint, error)
}

func (a *LocalAdapter) Compact(ctx context.Context, req Request, threshold threshold, counter TokenCounter) (*Result, string, error) {
	systemMessages, nonSystemMessages := splitMessages(req.History)
	if len(nonSystemMessages) == 0 {
		return nil, "no_non_system_history", nil
	}

	summaryMessage, checkpointIDs, usage, usageSource, err := a.buildSummaryMessage(ctx, req, threshold, systemMessages, nonSystemMessages, counter)
	if err != nil {
		return nil, "summary_generation_failed", err
	}
	if summaryMessage == nil {
		return nil, "summary_generation_empty", nil
	}

	recentTokenLimit := localRecentTokenLimit(req, systemMessages, *summaryMessage, counter)
	retainedDurable := selectCompactionDurableContext(nonSystemMessages, counter, recentTokenLimit)
	recentHistoryTokenLimit := recentTokenLimit
	if counter != nil {
		recentHistoryTokenLimit -= counter(retainedDurable)
		if recentHistoryTokenLimit <= 0 {
			recentHistoryTokenLimit = 1
		}
	}
	retainedRecent := selectCompactionRecentMessages(
		nonSystemMessages,
		req.KeepRecentMessages,
		counter,
		recentHistoryTokenLimit,
	)
	retainedRecent = stripDurableContextMessages(retainedRecent)
	replacement := buildLocalReplacementHistory(systemMessages, retainedRecent, retainedDurable, *summaryMessage)
	if normalizedPhase(req.Phase) == PhaseMidTurn {
		retainedUsers := selectCompactionUserMessages(nonSystemMessages, counter, recentTokenLimit)
		durableTokenLimit := recentTokenLimit
		if counter != nil {
			durableTokenLimit -= counter(retainedUsers)
		}
		retainedDurable := selectCompactionDurableContext(nonSystemMessages, counter, durableTokenLimit)
		replayTokenLimit := durableTokenLimit
		if counter != nil {
			replayTokenLimit -= counter(retainedDurable)
		}
		retainedReplay := selectCompactionRecentToolReplay(
			nonSystemMessages,
			req.KeepRecentMessages,
			counter,
			replayTokenLimit,
		)
		replacement = buildMidTurnReplacementHistory(systemMessages, retainedUsers, retainedDurable, retainedReplay, *summaryMessage)
		retainedRecent = append(cloneMessages(retainedUsers), cloneMessages(retainedDurable)...)
		retainedRecent = append(retainedRecent, cloneMessages(retainedReplay)...)
	}

	compactedMessages := len(nonSystemMessages) - len(retainedRecent)
	if compactedMessages < 0 {
		compactedMessages = 0
	}

	return &Result{
		Mode:               ModeLocal,
		Phase:              normalizedPhase(req.Phase),
		ResolvedProvider:   threshold.ResolvedProvider,
		ResolvedModel:      threshold.ResolvedModel,
		TriggerTokenLimit:  threshold.TriggerTokenLimit,
		MaxContextTokens:   threshold.MaxContextTokens,
		TokenBefore:        resolveObservedTokenCount(req, counter),
		TokenAfter:         counter(replacement),
		Usage:              usage,
		UsageSource:        usageSource,
		CompactedMessages:  compactedMessages,
		CheckpointIDs:      append([]string(nil), checkpointIDs...),
		ReplacementHistory: replacement,
	}, "", nil
}

func localRecentTokenLimit(req Request, systemMessages []types.Message, summaryMessage types.Message, counter TokenCounter) int {
	limit := localRetainedRecentMaxTokens
	if req.ReplacementTokenLimit <= 0 || counter == nil {
		return limit
	}
	fixed := append(cloneMessages(systemMessages), *summaryMessage.Clone())
	available := req.ReplacementTokenLimit - counter(fixed)
	if available <= 0 {
		return 1
	}
	if available < limit {
		return available
	}
	return limit
}

func (a *LocalAdapter) buildSummaryMessage(ctx context.Context, req Request, threshold threshold, systemMessages, history []types.Message, counter TokenCounter) (*types.Message, []string, *types.TokenUsage, string, error) {
	if message, checkpointID := a.findReusableSummaryCheckpoint(ctx, req.SessionID, req.Phase, history); message != nil {
		if checkpointID == "" {
			return message, nil, nil, "", nil
		}
		return message, []string{checkpointID}, nil, "", nil
	}
	if a == nil || a.llmRuntime == nil {
		return a.buildDeterministicSummaryMessage(ctx, req, history, "llm runtime is not configured")
	}

	summaryHistory := compactionSummaryHistory(req.Phase, history)
	maxTokens, reasoningEffort := resolveCompactSummaryRequestSettings(a.llmRuntime, req.Provider, req.Model)
	maxTokens = fitLocalCompactSummaryMaxTokens(maxTokens, req, systemMessages, summaryHistory, counter)
	request := buildLocalCompactionLLMRequest(req, systemMessages, summaryHistory, maxTokens, reasoningEffort)
	if reason := localCompactionRequestBudgetFailure(a.llmRuntime, request, threshold); reason != "" {
		request = buildFittedLocalCompactionLLMRequest(a.llmRuntime, req, threshold, systemMessages, summaryHistory, maxTokens, reasoningEffort, reason)
		if request == nil {
			return a.buildDeterministicSummaryMessage(ctx, req, history, reason)
		}
	}

	response, err := a.streamSummaryResponse(ctx, request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, nil, nil, "", ctxErr
		}
		return a.buildDeterministicSummaryMessage(ctx, req, history, err.Error())
	}

	summaryText := extractCompactSummaryText(response)
	if strings.TrimSpace(summaryText) == "" {
		return a.buildDeterministicSummaryMessage(ctx, req, history, "compact summary response is empty")
	}

	checkpointID := a.saveSummaryCheckpoint(ctx, req.SessionID, req.TaskID, history, summaryText)
	message := buildCompactionMessage(summaryText, checkpointID, 0, len(history), req.Phase)
	if message == nil {
		return nil, nil, nil, "", fmt.Errorf("failed to build compaction message")
	}
	message.Metadata["summary_source"] = "provider"
	var usage *types.TokenUsage
	if response != nil && response.Usage != nil {
		usage = response.Usage.Clone()
	}
	usageSource := ""
	if response != nil && response.Metadata != nil {
		usageSource = strings.TrimSpace(fmt.Sprintf("%v", response.Metadata["usage_source"]))
	}
	if checkpointID == "" {
		return message, nil, usage, usageSource, nil
	}
	return message, []string{checkpointID}, usage, usageSource, nil
}

func (a *LocalAdapter) streamSummaryResponse(ctx context.Context, request *llm.LLMRequest) (*llm.LLMResponse, error) {
	if a == nil || a.llmRuntime == nil {
		return nil, fmt.Errorf("llm runtime is not configured")
	}
	if request == nil {
		return nil, fmt.Errorf("compact summary request is nil")
	}

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	stream, err := a.llmRuntime.Stream(streamCtx, request)
	if err != nil {
		return nil, err
	}

	var text strings.Builder
	var reasoning strings.Builder
	metadata := map[string]interface{}{}
	for chunk := range stream {
		mergeCompactStreamMetadata(metadata, chunk.Metadata)

		switch chunk.Type {
		case llm.EventTypeText:
			text.WriteString(chunk.Content)
		case llm.EventTypeReasoning:
			reasoning.WriteString(chunk.Content)
		case llm.EventTypeToolCall, llm.EventTypeToolStart, llm.EventTypeToolEnd:
			return nil, fmt.Errorf("compact summary stream returned unexpected tool event: %s", chunk.Type)
		case llm.EventTypeError:
			if trimmed := strings.TrimSpace(chunk.Error); trimmed != "" {
				return nil, fmt.Errorf("%s", trimmed)
			}
		}

		if chunk.Done {
			if err := validateCompactStreamDone(chunk.Metadata); err != nil {
				return nil, err
			}
			return compactStreamResponse(request, text.String(), reasoning.String(), metadata), nil
		}
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return compactStreamResponse(request, text.String(), reasoning.String(), metadata), nil
}

func (a *LocalAdapter) buildDeterministicSummaryMessage(ctx context.Context, req Request, history []types.Message, reason string) (*types.Message, []string, *types.TokenUsage, string, error) {
	summaryText := buildDeterministicCompactSummary(history, reason)
	checkpointID := a.saveSummaryCheckpoint(ctx, req.SessionID, req.TaskID, history, summaryText)
	message := buildCompactionMessage(summaryText, checkpointID, 0, len(history), req.Phase)
	if message == nil {
		return nil, nil, nil, "", fmt.Errorf("failed to build deterministic compaction message")
	}
	message.Metadata["summary_source"] = "deterministic_fallback"
	if strings.TrimSpace(reason) != "" {
		message.Metadata["summary_fallback_reason"] = summarizeCompactLine(reason, 240)
	}
	if checkpointID == "" {
		return message, nil, nil, "deterministic_fallback", nil
	}
	return message, []string{checkpointID}, nil, "deterministic_fallback", nil
}

func buildDeterministicCompactSummary(history []types.Message, reason string) string {
	lines := []string{
		"Fallback summary generated locally because provider compact summary was unavailable.",
	}
	if trimmed := strings.TrimSpace(reason); trimmed != "" {
		lines = append(lines, "Fallback reason: "+summarizeCompactLine(trimmed, 240))
	}

	userItems := make([]string, 0, 16)
	priorSummaryItems := make([]string, 0, 4)
	durableItems := make([]string, 0, 8)
	decisionItems := make([]string, 0, 16)
	progressItems := make([]string, 0, 24)
	constraintItems := make([]string, 0, 12)
	referenceItems := make([]string, 0, 20)
	remainingItems := make([]string, 0, 12)
	toolRequestItems := make([]string, 0, 16)
	toolItems := make([]string, 0, 32)
	failureItems := make([]string, 0, 16)
	toolNames := toolCallNamesByID(history)
	for _, message := range history {
		content := strings.TrimSpace(message.Content)
		stage := strings.ToLower(strings.TrimSpace(message.Metadata.GetString("context_stage", "")))
		if stage == "compaction" {
			if content != "" {
				priorSummaryItems = appendWithinRuneBudget(priorSummaryItems, summarizeCompactLine(content, 1200), 4, localFallbackPriorSummaryRunes)
			}
			continue
		}
		if isDurableCompactStage(stage) {
			if content != "" {
				label := durableCompactStageLabel(stage)
				durableItems = appendWithinRuneBudget(durableItems, label+": "+summarizeCompactLine(content, 420), 8, localFallbackDurableRunes)
				for _, item := range extractCompactConstraintLines(content) {
					constraintItems = appendWithinRuneBudget(constraintItems, item, 12, localFallbackConstraintRunes)
				}
				for _, item := range extractCompactRemainingLines(content) {
					remainingItems = appendWithinRuneBudget(remainingItems, item, 12, localFallbackRemainingRunes)
				}
			}
			continue
		}
		if stage != "" {
			// Skip transient projections (workspace/recall/etc.); they are
			// re-injected by context build after compact.
			continue
		}
		switch message.Role {
		case "user", "developer":
			if content != "" {
				userItems = appendWithinRuneBudget(userItems, summarizeCompactLine(content, 360), 16, localFallbackUserRunes)
				for _, item := range extractCompactConstraintLines(content) {
					constraintItems = appendWithinRuneBudget(constraintItems, item, 12, localFallbackConstraintRunes)
				}
				for _, item := range extractCompactRemainingLines(content) {
					remainingItems = appendWithinRuneBudget(remainingItems, item, 12, localFallbackRemainingRunes)
				}
				for _, item := range extractCompactReferenceLinesFromText(content) {
					referenceItems = appendWithinRuneBudget(referenceItems, item, 16, localFallbackReferenceRunes)
				}
			}
		case "assistant":
			if content != "" {
				summary := summarizeCompactLine(content, 320)
				if looksLikeCompactDecision(content) {
					decisionItems = appendWithinRuneBudget(decisionItems, summary, 12, localFallbackAssistantRunes)
				} else {
					progressItems = appendWithinRuneBudget(progressItems, summary, 20, localFallbackAssistantRunes)
				}
				for _, item := range extractCompactConstraintLines(content) {
					constraintItems = appendWithinRuneBudget(constraintItems, item, 12, localFallbackConstraintRunes)
				}
				for _, item := range extractCompactRemainingLines(content) {
					remainingItems = appendWithinRuneBudget(remainingItems, item, 12, localFallbackRemainingRunes)
				}
				for _, item := range extractCompactReferenceLinesFromText(content) {
					referenceItems = appendWithinRuneBudget(referenceItems, item, 16, localFallbackReferenceRunes)
				}
				if looksLikeCompactFailure(content) {
					failureItems = appendWithinRuneBudget(failureItems, summary, 12, localFallbackFailureRunes)
				}
			}
			if len(message.ToolCalls) > 0 {
				names := make([]string, 0, len(message.ToolCalls))
				for _, call := range message.ToolCalls {
					if name := strings.TrimSpace(call.Name); name != "" {
						names = append(names, name)
					}
					for _, item := range extractCompactReferenceLinesFromToolCall(call) {
						referenceItems = appendWithinRuneBudget(referenceItems, item, 16, localFallbackReferenceRunes)
					}
				}
				if len(names) > 0 {
					toolRequestItems = appendWithinRuneBudget(toolRequestItems, strings.Join(names, ", "), 12, localFallbackToolRequestRunes)
				}
			}
		case "tool":
			if content != "" {
				item := summarizeCompactLine(content, 360)
				if name := toolNames[strings.TrimSpace(message.ToolCallID)]; name != "" {
					item = name + ": " + item
				}
				toolItems = appendWithinRuneBudget(toolItems, item, 32, localFallbackToolRunes)
				for _, itemRef := range extractCompactReferenceLinesFromText(content) {
					referenceItems = appendWithinRuneBudget(referenceItems, itemRef, 16, localFallbackReferenceRunes)
				}
			}
			toolErr := strings.TrimSpace(message.Metadata.GetString("tool_error", ""))
			if toolErr == "" && looksLikeCompactFailure(content) {
				toolErr = content
			}
			if toolErr != "" {
				failure := summarizeCompactLine(toolErr, 360)
				if name := toolNames[strings.TrimSpace(message.ToolCallID)]; name != "" {
					failure = name + ": " + failure
				}
				failureItems = appendWithinRuneBudget(failureItems, failure, 12, localFallbackFailureRunes)
			}
		}
	}

	if len(priorSummaryItems) > 0 {
		lines = append(lines, "Prior compacted context:")
		for _, item := range priorSummaryItems {
			lines = append(lines, "- "+item)
		}
	}
	if len(userItems) > 0 {
		lines = append(lines, "Current goal / recent user requests:")
		for _, item := range userItems {
			lines = append(lines, "- "+item)
		}
	}
	if len(durableItems) > 0 {
		lines = append(lines, "Durable session context:")
		for _, item := range durableItems {
			lines = append(lines, "- "+item)
		}
	}
	if len(constraintItems) > 0 {
		lines = append(lines, "Constraints and preferences:")
		for _, item := range constraintItems {
			lines = append(lines, "- "+item)
		}
	}
	if len(decisionItems) > 0 {
		lines = append(lines, "Key decisions:")
		for _, item := range decisionItems {
			lines = append(lines, "- "+item)
		}
	}
	if len(progressItems) > 0 {
		lines = append(lines, "Assistant progress:")
		for _, item := range progressItems {
			lines = append(lines, "- "+item)
		}
	}
	if len(toolRequestItems) > 0 {
		lines = append(lines, "Recent tool requests:")
		for _, item := range toolRequestItems {
			lines = append(lines, "- "+item)
		}
	}
	if len(toolItems) > 0 {
		lines = append(lines, "Tool outcomes:")
		for _, item := range toolItems {
			lines = append(lines, "- "+item)
		}
	}
	if len(referenceItems) > 0 {
		lines = append(lines, "Critical references:")
		for _, item := range referenceItems {
			lines = append(lines, "- "+item)
		}
	}
	if len(failureItems) > 0 {
		lines = append(lines, "Recent tool failures to account for:")
		for _, item := range failureItems {
			lines = append(lines, "- "+item)
		}
	}
	if len(remainingItems) > 0 {
		lines = append(lines, "Remaining work:")
		for _, item := range remainingItems {
			lines = append(lines, "- "+item)
		}
	} else {
		lines = append(lines, "Next step: continue from the retained latest user request and use checkpoints or artifacts for full raw outputs when needed.")
	}
	return ensureSummaryHeading(strings.Join(lines, "\n"))
}

func extractCompactSummaryText(response *llm.LLMResponse) string {
	if response == nil {
		return ""
	}

	summaryText := strings.TrimSpace(response.Content)
	if summaryText == "" {
		summaryText = strings.TrimSpace(response.Reasoning)
	}
	if summaryText == "" && len(response.Metadata) > 0 {
		if reasoning, ok := response.Metadata["reasoning_content"].(string); ok {
			summaryText = strings.TrimSpace(reasoning)
		}
	}
	return ensureSummaryHeading(summaryText)
}

func buildLocalCompactionLLMRequest(req Request, systemMessages, history []types.Message, maxTokens int, reasoningEffort string) *llm.LLMRequest {
	tools := cloneToolDefinitions(req.Tools)
	metadata := map[string]interface{}{
		llm.MetadataKeyInternalOperation: "compact",
		// Compact already falls back to a deterministic summary. Unbounded
		// provider/runtime retries can hang mid-turn (and unit tests) when the
		// stream fails or returns a non-SSE body.
		llm.MetadataKeyDisableRetries: true,
		"compact_mode":                ModeLocal,
		"compact_phase":               normalizedPhase(req.Phase),
		// Keep local compact on the same Codex/OpenAI cache route as chat
		// turns. session_id alone is usually enough for adapters, but set
		// prompt_cache_key explicitly so other protocols/debug surfaces see it.
		"session_id":       strings.TrimSpace(req.SessionID),
		"prompt_cache_key": strings.TrimSpace(req.SessionID),
	}
	if len(tools) > 0 {
		// Preserve the chat tools prefix for provider prompt-cache hits, but
		// force tool_choice=none so the compact summary never executes tools.
		metadata[llm.MetadataKeyDisableTools] = false
		metadata[llm.MetadataKeyDisableMetaTools] = false
		metadata["tool_choice"] = "none"
		metadata["compact_tools_retained"] = true
		metadata["compact_tool_count"] = len(tools)
	} else {
		metadata[llm.MetadataKeyDisableTools] = true
		metadata[llm.MetadataKeyDisableMetaTools] = true
	}
	return &llm.LLMRequest{
		Provider:        strings.TrimSpace(req.Provider),
		Model:           strings.TrimSpace(req.Model),
		Messages:        buildLocalCompactionRequest(systemMessages, history),
		Tools:           tools,
		MaxTokens:       maxTokens,
		Temperature:     0,
		ReasoningEffort: strings.TrimSpace(reasoningEffort),
		Metadata:        metadata,
	}
}

// buildFittedLocalCompactionLLMRequest maps the omitted prefix locally and lets
// the provider reduce that checkpoint together with the largest safe recent
// suffix. It keeps oversized raw history off the wire while preserving one
// provider compaction pass whenever a reduced request can fit.
//
// To keep the provider prompt-cache prefix stable across turns, the fitted
// request keeps the original leading user request verbatim in front of the
// locally-mapped summary whenever the budget allows; only when the anchor
// cannot fit does the request fall back to the summary-first layout.
func buildFittedLocalCompactionLLMRequest(runtime *llm.LLMRuntime, req Request, threshold threshold, systemMessages, history []types.Message, maxTokens int, reasoningEffort, preflightReason string) *llm.LLMRequest {
	starts := safeLocalCompactionStarts(history)
	if len(starts) == 0 {
		return nil
	}

	buildCandidate := func(start int, systems []types.Message, withAnchor bool) *llm.LLMRequest {
		reduced := make([]types.Message, 0, len(history)-start+2)
		if start > 0 {
			if withAnchor {
				if anchor := leadingCompactionAnchorUserMessage(history, start); anchor != nil {
					reduced = append(reduced, *anchor)
				}
			}
			prefix := types.NewUserMessage(buildDeterministicCompactSummary(history[:start], preflightReason))
			prefix.Metadata["context_stage"] = "compaction"
			prefix.Metadata["summary_source"] = "deterministic_prefix_map"
			prefix.Metadata["omitted_message_count"] = start
			reduced = append(reduced, *prefix)
		}
		reduced = append(reduced, cloneMessages(history[start:])...)
		candidate := buildLocalCompactionLLMRequest(req, systems, reduced, maxTokens, reasoningEffort)
		candidate.Metadata["compact_input_reduced"] = true
		candidate.Metadata["compact_omitted_messages"] = start
		candidate.Metadata["compact_retained_messages"] = len(history) - start
		candidate.Metadata["compact_preflight_reason"] = summarizeCompactLine(preflightReason, 240)
		if start > 0 && withAnchor {
			candidate.Metadata["compact_anchor_retained"] = true
		}
		if len(systems) == 0 && len(systemMessages) > 0 {
			candidate.Metadata["compact_system_messages_omitted"] = len(systemMessages)
		}
		return candidate
	}

	findFit := func(systems []types.Message, withAnchor bool) *llm.LLMRequest {
		last := buildCandidate(starts[len(starts)-1], systems, withAnchor)
		if localCompactionRequestBudgetFailure(runtime, last, threshold) != "" {
			return nil
		}
		low, high := 0, len(starts)-1
		for low < high {
			mid := low + (high-low)/2
			candidate := buildCandidate(starts[mid], systems, withAnchor)
			if localCompactionRequestBudgetFailure(runtime, candidate, threshold) == "" {
				high = mid
			} else {
				low = mid + 1
			}
		}
		return buildCandidate(starts[low], systems, withAnchor)
	}

	// Prefer the anchor-keeping layout (stable prefix), then degrade in order:
	// with system -> without system -> summary-only (legacy) layouts.
	if fitted := findFit(systemMessages, true); fitted != nil {
		return fitted
	}
	if fitted := findFit(nil, true); fitted != nil {
		return fitted
	}
	if fitted := findFit(systemMessages, false); fitted != nil {
		return fitted
	}
	return findFit(nil, false)
}

// leadingCompactionAnchorUserMessage returns a clone of the first plain user
// message inside the truncated prefix history[:start]. It is kept verbatim at
// the head of a fitted compact request so the provider prefix cache still sees
// the same "system + original user request" leading block that ordinary turns
// carry, instead of diverging at the very first (summary) message. Injected
// stage messages (previous compaction summaries / durable context) are skipped.
func leadingCompactionAnchorUserMessage(history []types.Message, start int) *types.Message {
	limit := start
	if limit > len(history) {
		limit = len(history)
	}
	for index := 0; index < limit; index++ {
		message := &history[index]
		if !strings.EqualFold(strings.TrimSpace(message.Role), "user") {
			continue
		}
		if strings.TrimSpace(message.Metadata.GetString("context_stage", "")) != "" {
			continue
		}
		return message.Clone()
	}
	return nil
}

func safeLocalCompactionStarts(history []types.Message) []int {
	starts := make([]int, 0, len(history)+1)
	for index, message := range history {
		if index == 0 || !strings.EqualFold(strings.TrimSpace(message.Role), "tool") {
			starts = append(starts, index)
		}
	}
	if len(starts) == 0 || starts[len(starts)-1] != len(history) {
		starts = append(starts, len(history))
	}
	return starts
}

func localCompactionRequestBudgetFailure(runtime *llm.LLMRuntime, request *llm.LLMRequest, threshold threshold) string {
	if runtime == nil || request == nil {
		return ""
	}
	encoded, err := json.Marshal(request.Messages)
	if err != nil {
		return fmt.Sprintf("compact request could not be measured safely: %v", err)
	}
	requestBytes := len(encoded)
	toolSchemaTokens := 0
	if len(request.Tools) > 0 {
		if toolsEncoded, toolsErr := json.Marshal(request.Tools); toolsErr == nil {
			requestBytes += len(toolsEncoded)
			toolSchemaTokens = runtime.CountTokens(string(toolsEncoded))
		}
	}
	if requestBytes > localCompactMaxRequestBytes {
		return fmt.Sprintf("compact request skipped because serialized input is %d bytes (limit %d)", requestBytes, localCompactMaxRequestBytes)
	}

	inputTokens := runtime.CountMessagesTokens(request.Messages)
	if serializedTokens := runtime.CountTokens(string(encoded)); serializedTokens > inputTokens {
		inputTokens = serializedTokens
	}
	inputTokens += toolSchemaTokens
	inputBudget := localCompactionInputBudget(runtime, request, threshold)
	if inputBudget > 0 && inputTokens > inputBudget {
		return fmt.Sprintf("compact request skipped because estimated input is %d tokens (budget %d)", inputTokens, inputBudget)
	}
	return ""
}

func localCompactionInputBudget(runtime *llm.LLMRuntime, request *llm.LLMRequest, threshold threshold) int {
	maxContextTokens := threshold.MaxContextTokens
	if maxContextTokens <= 0 && runtime != nil && request != nil {
		_, _, capability, ok := llm.ResolveRuntimeModelCapability(runtime, request.Provider, request.Model)
		if ok {
			maxContextTokens = capability.MaxContextTokens
		}
	}
	if maxContextTokens <= 0 || request == nil {
		return 0
	}
	inputBudget := maxContextTokens - request.MaxTokens
	if inputBudget <= 0 {
		return 1
	}
	return inputBudget
}

func compactStreamResponse(request *llm.LLMRequest, content, reasoning string, metadata map[string]interface{}) *llm.LLMResponse {
	response := &llm.LLMResponse{
		Content:   content,
		Reasoning: reasoning,
	}
	if request != nil {
		response.Model = strings.TrimSpace(request.Model)
	}
	if len(metadata) > 0 {
		response.Metadata = metadata
	}
	return response
}

func mergeCompactStreamMetadata(target, source map[string]interface{}) {
	if target == nil || len(source) == 0 {
		return
	}
	for key, value := range source {
		target[key] = value
	}
}

func validateCompactStreamDone(metadata map[string]interface{}) error {
	finishReason := strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v", metadata["finish_reason"])))
	switch finishReason {
	case "", "stop", "end_turn":
		return nil
	case "tool_calls", "tool_call", "function_call":
		return fmt.Errorf("compact summary stream finished with unexpected tool request: %s", finishReason)
	default:
		return nil
	}
}

func buildLocalCompactionRequest(systemMessages, history []types.Message) []types.Message {
	request := make([]types.Message, 0, len(systemMessages)+len(history)+1)
	request = append(request, cloneMessages(systemMessages)...)
	request = append(request, cloneMessages(history)...)
	request = append(request, *types.NewUserMessage(localCompactionPrompt))
	return request
}

func compactionSummaryHistory(phase string, history []types.Message) []types.Message {
	if normalizedPhase(phase) != PhaseMidTurn {
		return cloneMessages(history)
	}
	filtered := make([]types.Message, 0, len(history))
	for _, message := range history {
		stage := strings.TrimSpace(message.Metadata.GetString("context_stage", ""))
		if strings.EqualFold(strings.TrimSpace(message.Role), "user") && stage != "" && !strings.EqualFold(stage, "compaction") {
			continue
		}
		filtered = append(filtered, *message.Clone())
	}
	return filtered
}

type localRetentionUnit []types.Message

// buildLocalRetentionUnits removes stale compact projections and treats one
// assistant tool request plus all of its results as an indivisible unit.
func buildLocalRetentionUnits(messages []types.Message) []localRetentionUnit {
	units := make([]localRetentionUnit, 0, len(messages))
	for index := 0; index < len(messages); {
		message := messages[index]
		if isCompactionMessage(message) {
			index++
			continue
		}
		role := strings.ToLower(strings.TrimSpace(message.Role))
		if role == "tool" {
			index++
			continue
		}
		if role != "assistant" || len(message.ToolCalls) == 0 {
			units = append(units, localRetentionUnit{*message.Clone()})
			index++
			continue
		}

		expected := make(map[string]bool, len(message.ToolCalls))
		valid := true
		for _, call := range message.ToolCalls {
			id := strings.TrimSpace(call.ID)
			if id == "" || expected[id] {
				valid = false
				continue
			}
			expected[id] = true
		}
		seen := make(map[string]bool, len(expected))
		end := index + 1
		for end < len(messages) && strings.EqualFold(strings.TrimSpace(messages[end].Role), "tool") {
			result := messages[end]
			id := strings.TrimSpace(result.ToolCallID)
			if isCompactionMessage(result) || id == "" || !expected[id] || seen[id] {
				valid = false
			} else {
				seen[id] = true
			}
			end++
		}
		if valid && len(seen) == len(expected) {
			units = append(units, localRetentionUnit(cloneMessages(messages[index:end])))
		}
		index = end
	}
	return units
}

func isCompactionMessage(message types.Message) bool {
	return strings.EqualFold(strings.TrimSpace(message.Metadata.GetString("context_stage", "")), "compaction")
}

func selectCompactionRecentMessages(messages []types.Message, keepRecent int, counter TokenCounter, maxTokens int) []types.Message {
	units := buildLocalRetentionUnits(messages)
	if len(units) == 0 {
		return nil
	}

	start := len(units)
	for index := len(units) - 1; index >= 0; index-- {
		candidate := flattenLocalRetentionUnits(units[index:])
		if keepRecent > 0 && len(candidate) > keepRecent && start < len(units) {
			break
		}
		if counter != nil && maxTokens > 0 && counter(candidate) > maxTokens {
			break
		}
		start = index
	}

	activeUserUnit := -1
	for index := len(units) - 1; index >= 0; index-- {
		if len(units[index]) == 1 && strings.EqualFold(strings.TrimSpace(units[index][0].Role), "user") {
			activeUserUnit = index
			break
		}
	}
	if activeUserUnit < 0 || activeUserUnit >= start {
		return flattenLocalRetentionUnits(units[start:])
	}

	// The active request is pinned even when it alone exceeds the recent token
	// budget. Drop whole older suffix units until the combined replay fits.
	pinned := cloneMessages(units[activeUserUnit])
	for start < len(units) {
		candidate := append(cloneMessages(pinned), flattenLocalRetentionUnits(units[start:])...)
		if localRetentionFits(candidate, keepRecent, counter, maxTokens) {
			return candidate
		}
		start++
	}
	return pinned
}

func localRetentionFits(messages []types.Message, keepRecent int, counter TokenCounter, maxTokens int) bool {
	if keepRecent > 0 && len(messages) > keepRecent {
		return false
	}
	return counter == nil || maxTokens <= 0 || counter(messages) <= maxTokens
}

func flattenLocalRetentionUnits(units []localRetentionUnit) []types.Message {
	count := 0
	for _, unit := range units {
		count += len(unit)
	}
	flattened := make([]types.Message, 0, count)
	for _, unit := range units {
		flattened = append(flattened, cloneMessages(unit)...)
	}
	return flattened
}

func buildLocalReplacementHistory(systemMessages, retainedRecent, retainedDurable []types.Message, summaryMessage types.Message) []types.Message {
	replacement := make([]types.Message, 0, len(systemMessages)+len(retainedRecent)+len(retainedDurable)+1)
	replacement = append(replacement, cloneMessages(systemMessages)...)
	replacement = append(replacement, *summaryMessage.Clone())
	// Place durable world-state after the summary and before retained raw
	// history so the next turn can treat it as an authoritative prefix rather
	// than reconstructing it from compacted prose alone.
	replacement = append(replacement, cloneMessages(retainedDurable)...)
	replacement = append(replacement, cloneMessages(retainedRecent)...)
	return replacement
}

// Mid-turn replacement follows Codex's context-window shape: canonical
// instructions, independently retained real user requests, the latest durable
// world-state snapshot, recent complete tool replay, and one latest semantic
// checkpoint as the final item. Durable context remains between the user request
// and its replay so the next context build can reuse it instead of reconstructing
// authoritative state from compacted prose.
func buildMidTurnReplacementHistory(systemMessages, retainedUsers, retainedDurable, retainedReplay []types.Message, summaryMessage types.Message) []types.Message {
	replacement := make([]types.Message, 0, len(systemMessages)+len(retainedUsers)+len(retainedDurable)+len(retainedReplay)+1)
	replacement = append(replacement, cloneMessages(systemMessages)...)
	replacement = append(replacement, cloneMessages(retainedUsers)...)
	replacement = append(replacement, cloneMessages(retainedDurable)...)
	replacement = append(replacement, cloneMessages(retainedReplay)...)
	replacement = append(replacement, *summaryMessage.Clone())
	return replacement
}

func selectCompactionUserMessages(messages []types.Message, counter TokenCounter, maxTokens int) []types.Message {
	selected := make([]types.Message, 0, 4)
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if !isRealCompactionUserMessage(message) {
			continue
		}
		candidate := append([]types.Message{*message.Clone()}, selected...)
		if counter != nil && maxTokens > 0 && counter(candidate) > maxTokens {
			if len(selected) == 0 {
				return []types.Message{*message.Clone()}
			}
			break
		}
		selected = candidate
	}
	return selected
}

func isRealCompactionUserMessage(message types.Message) bool {
	if !strings.EqualFold(strings.TrimSpace(message.Role), "user") {
		return false
	}
	return strings.TrimSpace(message.Metadata.GetString("context_stage", "")) == ""
}

// selectCompactionDurableContext keeps the newest authoritative projection for
// each durable stage. Selection is priority-aware under pressure, while output
// order follows the source history so turn semantics remain deterministic.
func selectCompactionDurableContext(messages []types.Message, counter TokenCounter, maxTokens int) []types.Message {
	if maxTokens <= 0 || len(messages) == 0 {
		return nil
	}
	latest := make(map[string]int)
	for index := len(messages) - 1; index >= 0; index-- {
		stage := strings.ToLower(strings.TrimSpace(messages[index].Metadata.GetString("context_stage", "")))
		if !isDurableCompactStage(stage) {
			continue
		}
		if _, exists := latest[stage]; !exists {
			latest[stage] = index
		}
	}
	priorities := []string{"active_goal", "todo_state", "fact_ledger", "team", "project_memory", "observation"}
	selectedIndexes := make(map[int]bool, len(latest))
	selected := make([]types.Message, 0, len(latest))
	for _, stage := range priorities {
		index, exists := latest[stage]
		if !exists {
			continue
		}
		candidate := append(cloneMessages(selected), *messages[index].Clone())
		if counter != nil && counter(candidate) > maxTokens {
			continue
		}
		selected = candidate
		selectedIndexes[index] = true
	}
	if len(selectedIndexes) == 0 {
		return nil
	}
	ordered := make([]types.Message, 0, len(selectedIndexes))
	for index := range messages {
		if selectedIndexes[index] {
			ordered = append(ordered, *messages[index].Clone())
		}
	}
	return ordered
}

func stripDurableContextMessages(messages []types.Message) []types.Message {
	if len(messages) == 0 {
		return nil
	}
	filtered := make([]types.Message, 0, len(messages))
	for _, message := range messages {
		stage := strings.ToLower(strings.TrimSpace(message.Metadata.GetString("context_stage", "")))
		if isDurableCompactStage(stage) {
			continue
		}
		filtered = append(filtered, *message.Clone())
	}
	return filtered
}

func mergeDurableWorldState(phase string, source, replacement []types.Message, counter TokenCounter, maxTokens int) []types.Message {
	durable := selectCompactionDurableContext(source, counter, maxTokens)
	if len(durable) == 0 {
		return cloneMessages(replacement)
	}
	result := stripDurableContextMessages(replacement)
	insertAt := durableWorldStateInsertIndex(phase, result)
	merged := make([]types.Message, 0, len(result)+len(durable))
	merged = append(merged, cloneMessages(result[:insertAt])...)
	merged = append(merged, cloneMessages(durable)...)
	merged = append(merged, cloneMessages(result[insertAt:])...)
	return merged
}

func durableWorldStateInsertIndex(phase string, messages []types.Message) int {
	if len(messages) == 0 {
		return 0
	}
	if normalizedPhase(phase) == PhaseMidTurn {
		lastUser := -1
		for index, message := range messages {
			if isRealCompactionUserMessage(message) {
				lastUser = index
			}
		}
		if lastUser >= 0 {
			return lastUser + 1
		}
		// Keep provider checkpoint last when no real user remains.
		for index := len(messages) - 1; index >= 0; index-- {
			if isCompactionMessage(messages[index]) {
				return index
			}
		}
		return len(messages)
	}
	// Pre-turn: place durable world-state after the summary and before retained
	// raw history, matching the local replacement shape.
	for index, message := range messages {
		if isCompactionMessage(message) {
			return index + 1
		}
	}
	if strings.EqualFold(strings.TrimSpace(messages[0].Role), "system") {
		return 1
	}
	return 0
}

func selectCompactionRecentToolReplay(messages []types.Message, keepRecent int, counter TokenCounter, maxTokens int) []types.Message {
	if maxTokens <= 0 {
		return nil
	}
	units := buildLocalRetentionUnits(messages)
	selected := make([]localRetentionUnit, 0, 2)
	for index := len(units) - 1; index >= 0; index-- {
		unit := units[index]
		if len(unit) < 2 || !strings.EqualFold(strings.TrimSpace(unit[0].Role), "assistant") || len(unit[0].ToolCalls) == 0 {
			continue
		}
		candidateUnits := append([]localRetentionUnit{unit}, selected...)
		candidate := flattenLocalRetentionUnits(candidateUnits)
		if keepRecent > 0 && len(candidate) > keepRecent {
			break
		}
		if counter != nil && counter(candidate) > maxTokens {
			break
		}
		selected = candidateUnits
	}
	return flattenLocalRetentionUnits(selected)
}

func ensureSummaryHeading(summary string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(summary), strings.ToLower(localSummaryHeading)) {
		return summary
	}
	return localSummaryHeading + "\n" + summary
}

func toolCallNamesByID(history []types.Message) map[string]string {
	names := make(map[string]string)
	for _, message := range history {
		for _, call := range message.ToolCalls {
			id := strings.TrimSpace(call.ID)
			name := strings.TrimSpace(call.Name)
			if id != "" && name != "" {
				names[id] = name
			}
		}
	}
	return names
}

func isDurableCompactStage(stage string) bool {
	switch strings.ToLower(strings.TrimSpace(stage)) {
	case "active_goal", "todo_state", "team", "fact_ledger", "project_memory", "observation":
		return true
	default:
		return false
	}
}

func durableCompactStageLabel(stage string) string {
	switch strings.ToLower(strings.TrimSpace(stage)) {
	case "active_goal":
		return "active goal"
	case "todo_state":
		return "todos"
	case "team":
		return "team"
	case "fact_ledger":
		return "fact ledger"
	case "project_memory":
		return "project memory"
	case "observation":
		return "observations"
	default:
		return strings.TrimSpace(stage)
	}
}

func looksLikeCompactDecision(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	return strings.Contains(lower, "decision:") ||
		strings.Contains(lower, "conclusion:") ||
		strings.Contains(lower, "most likely") ||
		strings.Contains(lower, "we should") ||
		strings.Contains(lower, "decided") ||
		strings.Contains(lower, "root cause") ||
		strings.Contains(lower, "结论") ||
		strings.Contains(lower, "决定")
}

func looksLikeCompactFailure(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	return strings.Contains(lower, "failed") ||
		strings.Contains(lower, "failure") ||
		strings.Contains(lower, "error") ||
		strings.Contains(lower, "denied") ||
		strings.Contains(lower, "panic") ||
		strings.Contains(lower, "timeout") ||
		strings.Contains(lower, "traceback") ||
		strings.Contains(lower, "exit code") ||
		strings.Contains(lower, "失败") ||
		strings.Contains(lower, "错误")
}

func extractCompactConstraintLines(text string) []string {
	lines := splitCompactContentLines(text)
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "must ") ||
			strings.Contains(lower, "must not") ||
			strings.Contains(lower, "do not") ||
			strings.Contains(lower, "don't") ||
			strings.Contains(lower, "never ") ||
			strings.Contains(lower, "only ") ||
			strings.Contains(lower, "constraint") ||
			strings.Contains(lower, "preference") ||
			strings.Contains(lower, "require") ||
			strings.Contains(lower, "必须") ||
			strings.Contains(lower, "不要") ||
			strings.Contains(lower, "禁止") ||
			strings.Contains(lower, "只能") {
			out = append(out, summarizeCompactLine(line, 280))
		}
	}
	return out
}

func extractCompactRemainingLines(text string) []string {
	lines := splitCompactContentLines(text)
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "todo") ||
			strings.Contains(lower, "next step") ||
			strings.Contains(lower, "remaining") ||
			strings.Contains(lower, "still need") ||
			strings.Contains(lower, "follow up") ||
			strings.Contains(lower, "待办") ||
			strings.Contains(lower, "下一步") ||
			strings.Contains(lower, "剩余") ||
			strings.HasPrefix(lower, "- [ ]") ||
			strings.HasPrefix(lower, "* [ ]") {
			out = append(out, summarizeCompactLine(line, 280))
		}
	}
	return out
}

func extractCompactReferenceLinesFromToolCall(call types.ToolCall) []string {
	name := strings.TrimSpace(call.Name)
	if len(call.Args) == 0 {
		return nil
	}
	keys := []string{
		"path", "file_path", "filepath", "file", "cwd", "workdir",
		"command", "cmd", "url", "query", "pattern", "target",
		"session_id", "team_id", "goal_id", "id",
	}
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		value, ok := call.Args[key]
		if !ok || value == nil {
			continue
		}
		text := strings.TrimSpace(fmt.Sprintf("%v", value))
		if text == "" {
			continue
		}
		item := key + "=" + summarizeCompactLine(text, 220)
		if name != "" {
			item = name + ": " + item
		}
		out = append(out, item)
	}
	return out
}

func extractCompactReferenceLinesFromText(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	candidates := make([]string, 0, 8)
	for _, token := range strings.Fields(text) {
		token = strings.Trim(token, "`,\"'()[]{}<>")
		if token == "" {
			continue
		}
		if looksLikeCompactReferenceToken(token) {
			candidates = append(candidates, summarizeCompactLine(token, 220))
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	// Keep a small unique set so noise from long dumps does not dominate.
	seen := make(map[string]struct{}, len(candidates))
	out := make([]string, 0, 8)
	for _, item := range candidates {
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
		if len(out) >= 8 {
			break
		}
	}
	return out
}

func looksLikeCompactReferenceToken(token string) bool {
	if strings.Contains(token, "://") {
		return true
	}
	if strings.ContainsAny(token, `/\`) && (strings.Contains(token, ".") || strings.Contains(token, "/") || strings.Contains(token, `\`)) {
		// Paths and path-like identifiers.
		if len(token) >= 3 {
			return true
		}
	}
	if strings.HasPrefix(token, "evt_") || strings.HasPrefix(token, "goal_") || strings.HasPrefix(token, "session-") || strings.HasPrefix(token, "team-") {
		return true
	}
	return false
}

func splitCompactContentLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	raw := strings.Split(text, "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		if trimmed := strings.TrimSpace(text); trimmed != "" {
			return []string{trimmed}
		}
	}
	return out
}

func appendWithinRuneBudget(items []string, item string, maxItems, maxRunes int) []string {
	item = strings.TrimSpace(item)
	if item == "" || maxItems <= 0 || maxRunes <= 0 {
		return items
	}
	items = append(items, item)
	usedRunes := 0
	start := len(items)
	for index := len(items) - 1; index >= 0 && len(items)-index <= maxItems; index-- {
		itemRunes := len([]rune(items[index]))
		if usedRunes+itemRunes > maxRunes {
			break
		}
		usedRunes += itemRunes
		start = index
	}
	if start == len(items) {
		return []string{summarizeCompactLine(item, maxRunes)}
	}
	return append([]string(nil), items[start:]...)
}

func summarizeCompactLine(text string, limit int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))), " ")
	if text == "" || limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

func resolveCompactSummaryRequestSettings(runtime *llm.LLMRuntime, providerName, model string) (int, string) {
	maxTokens := localCompactDefaultMaxTokens
	reasoningEffort := ""
	if runtime == nil {
		return maxTokens, reasoningEffort
	}

	_, _, capability, ok := llm.ResolveRuntimeModelCapability(runtime, providerName, model)
	if !ok {
		return maxTokens, reasoningEffort
	}

	if resolvedMaxTokens, resolvedReasoningEffort := llm.CompactSummarySettings(capability); resolvedMaxTokens > 0 {
		maxTokens = resolvedMaxTokens
		if strings.TrimSpace(resolvedReasoningEffort) != "" {
			reasoningEffort = strings.TrimSpace(resolvedReasoningEffort)
		}
	}

	return maxTokens, reasoningEffort
}

func fitLocalCompactSummaryMaxTokens(configured int, req Request, systemMessages, history []types.Message, counter TokenCounter) int {
	if configured <= 0 {
		configured = localCompactDefaultMaxTokens
	}
	if req.ReplacementTokenLimit <= 0 || counter == nil {
		return configured
	}

	reserved := cloneMessages(systemMessages)
	for index := len(history) - 1; index >= 0; index-- {
		if strings.EqualFold(strings.TrimSpace(history[index].Role), "user") && !isCompactionMessage(history[index]) {
			reserved = append(reserved, *history[index].Clone())
			break
		}
	}
	remaining := req.ReplacementTokenLimit - counter(reserved)
	if remaining <= 0 {
		return 1
	}
	target := remaining / 2
	if target < 64 {
		target = 64
		if remaining < target {
			target = remaining
		}
	}
	if target < configured {
		return target
	}
	return configured
}

func buildCompactionMessage(summaryText, checkpointID string, segmentStart, segmentEnd int, phase string) *types.Message {
	summaryText = strings.TrimSpace(summaryText)
	if summaryText == "" {
		return nil
	}
	message := types.NewUserMessage(summaryText)
	message.Metadata["context_stage"] = "compaction"
	message.Metadata["compact_mode"] = ModeLocal
	message.Metadata["compact_phase"] = normalizedPhase(phase)
	message.Metadata[localSegmentStartKey] = segmentStart
	message.Metadata[localSegmentEndKey] = segmentEnd
	if strings.TrimSpace(checkpointID) != "" {
		message.Metadata["checkpoint_id"] = strings.TrimSpace(checkpointID)
	}
	return message
}

func (a *LocalAdapter) findReusableSummaryCheckpoint(ctx context.Context, sessionID, phase string, history []types.Message) (*types.Message, string) {
	store := summaryCheckpointStore(a.contextManager)
	if store == nil || strings.TrimSpace(sessionID) == "" || len(history) == 0 {
		return nil, ""
	}

	checkpoints := listSummaryCheckpoints(ctx, store, sessionID)
	if len(checkpoints) == 0 {
		return nil, ""
	}
	expectedHash := hashHistory(history)
	for _, checkpoint := range checkpoints {
		if strings.TrimSpace(checkpoint.Reason) != localSummaryCheckpointReason {
			continue
		}
		start, end, ok := checkpointRange(checkpoint)
		if !ok || start != 0 || end != len(history) {
			continue
		}
		if checkpoint.HistoryHash != expectedHash {
			continue
		}
		summaryText := strings.TrimSpace(summaryTextFromCheckpoint(checkpoint))
		if summaryText == "" {
			continue
		}
		return buildCompactionMessage(summaryText, checkpoint.ID, start, end, phase), strings.TrimSpace(checkpoint.ID)
	}
	return nil, ""
}

func (a *LocalAdapter) saveSummaryCheckpoint(ctx context.Context, sessionID, taskID string, history []types.Message, summaryText string) string {
	store := summaryCheckpointStore(a.contextManager)
	if store == nil || strings.TrimSpace(sessionID) == "" || len(history) == 0 {
		return ""
	}

	checkpoint := artifact.Checkpoint{
		SessionID:    strings.TrimSpace(sessionID),
		TaskID:       firstNonEmpty(strings.TrimSpace(taskID), strings.TrimSpace(sessionID)),
		Reason:       localSummaryCheckpointReason,
		HistoryHash:  hashHistory(history),
		MessageCount: len(history),
		Metadata: map[string]interface{}{
			"source_messages":    len(history),
			localSummaryTextKey:  summaryText,
			localSegmentStartKey: 0,
			localSegmentEndKey:   len(history),
		},
	}
	checkpointID, err := store.SaveCheckpoint(ctx, checkpoint)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(checkpointID)
}

func summaryCheckpointStore(manager *contextmgr.Manager) contextmgr.LedgerStore {
	if manager == nil || manager.Ledger == nil {
		return nil
	}
	return manager.Ledger
}

func listSummaryCheckpoints(ctx context.Context, store contextmgr.LedgerStore, sessionID string) []artifact.Checkpoint {
	if store == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	if lister, ok := store.(checkpointLister); ok {
		if checkpoints, err := lister.ListCheckpoints(ctx, sessionID, 64, 0); err == nil {
			return checkpoints
		}
	}
	if checkpoint, err := store.LatestCheckpoint(ctx, sessionID); err == nil && checkpoint != nil {
		return []artifact.Checkpoint{*checkpoint}
	}
	return nil
}

func summaryTextFromCheckpoint(checkpoint artifact.Checkpoint) string {
	if len(checkpoint.Metadata) == 0 {
		return ""
	}
	value, ok := checkpoint.Metadata[localSummaryTextKey]
	if !ok || value == nil {
		return ""
	}
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func checkpointRange(checkpoint artifact.Checkpoint) (int, int, bool) {
	if checkpoint.MessageCount <= 0 {
		return 0, 0, false
	}
	start, hasStart := intValue(checkpoint.Metadata, localSegmentStartKey)
	end, hasEnd := intValue(checkpoint.Metadata, localSegmentEndKey)
	if !hasStart {
		start = 0
	}
	if !hasEnd {
		end = start + checkpoint.MessageCount
	}
	if end-start != checkpoint.MessageCount || start < 0 || end <= start {
		return 0, 0, false
	}
	return start, end, true
}

func intValue(metadata map[string]interface{}, key string) (int, bool) {
	if len(metadata) == 0 {
		return 0, false
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float32:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

func splitMessages(history []types.Message) ([]types.Message, []types.Message) {
	systemMessages := make([]types.Message, 0, 1)
	nonSystemMessages := make([]types.Message, 0, len(history))
	for _, message := range history {
		if message.Role == "system" {
			systemMessages = append(systemMessages, *message.Clone())
			continue
		}
		nonSystemMessages = append(nonSystemMessages, *message.Clone())
	}
	return systemMessages, nonSystemMessages
}

func cloneMessages(messages []types.Message) []types.Message {
	cloned := make([]types.Message, len(messages))
	for index := range messages {
		cloned[index] = *messages[index].Clone()
	}
	return cloned
}

func cloneToolDefinitions(tools []types.ToolDefinition) []types.ToolDefinition {
	if len(tools) == 0 {
		return nil
	}
	cloned := make([]types.ToolDefinition, len(tools))
	for index, tool := range tools {
		cloned[index] = tool
		if len(tool.Parameters) > 0 {
			encoded, err := json.Marshal(tool.Parameters)
			if err == nil {
				var parameters map[string]interface{}
				if json.Unmarshal(encoded, &parameters) == nil {
					cloned[index].Parameters = parameters
				}
			}
		}
		if len(tool.Metadata) > 0 {
			metadata := make(map[string]interface{}, len(tool.Metadata))
			for key, value := range tool.Metadata {
				metadata[key] = value
			}
			cloned[index].Metadata = metadata
		}
	}
	return cloned
}

func hashHistory(messages []types.Message) string {
	parts := make([]string, 0, len(messages)*2)
	for _, message := range messages {
		parts = append(parts, message.Role)
		parts = append(parts, strings.TrimSpace(message.Content))
	}
	sum := sha1.Sum([]byte(strings.Join(parts, "\n")))
	return fmt.Sprintf("%x", sum[:])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
