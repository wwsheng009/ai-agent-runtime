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
	localFallbackUserRunes         = 1600
	localFallbackAssistantRunes    = 2000
	localFallbackToolRequestRunes  = 800
	localFallbackToolRunes         = 3600
	localFallbackFailureRunes      = 1200
	localFallbackPriorSummaryRunes = 3600
	localCompactionPrompt          = `You are performing a CONTEXT CHECKPOINT COMPACTION. Create a handoff summary for another LLM that will resume the task.

Include:
- Current progress and key decisions made
- Important context, constraints, or user preferences
- What remains to be done (clear next steps)
- Any critical data, examples, or references needed to continue

Be concise, structured, and focused on helping the next LLM seamlessly continue the work.`
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
	retainedRecent := selectCompactionRecentMessages(
		nonSystemMessages,
		req.KeepRecentMessages,
		counter,
		recentTokenLimit,
	)
	replacement := buildLocalReplacementHistory(systemMessages, retainedRecent, *summaryMessage)

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

	maxTokens, reasoningEffort := resolveCompactSummaryRequestSettings(a.llmRuntime, req.Provider, req.Model)
	maxTokens = fitLocalCompactSummaryMaxTokens(maxTokens, req, systemMessages, history, counter)
	request := buildLocalCompactionLLMRequest(req, systemMessages, history, maxTokens, reasoningEffort)
	if reason := localCompactionRequestBudgetFailure(a.llmRuntime, request, threshold); reason != "" {
		request = buildFittedLocalCompactionLLMRequest(a.llmRuntime, req, threshold, systemMessages, history, maxTokens, reasoningEffort, reason)
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
	assistantItems := make([]string, 0, 24)
	toolRequestItems := make([]string, 0, 12)
	toolItems := make([]string, 0, 32)
	failureItems := make([]string, 0, 12)
	toolNames := toolCallNamesByID(history)
	for _, message := range history {
		content := strings.TrimSpace(message.Content)
		switch message.Role {
		case "user":
			if strings.EqualFold(message.Metadata.GetString("context_stage", ""), "compaction") {
				if content != "" {
					priorSummaryItems = appendWithinRuneBudget(priorSummaryItems, summarizeCompactLine(content, 1200), 4, localFallbackPriorSummaryRunes)
				}
				continue
			}
			if content != "" {
				userItems = appendWithinRuneBudget(userItems, summarizeCompactLine(content, 260), 16, localFallbackUserRunes)
			}
		case "assistant":
			if content != "" {
				assistantItems = appendWithinRuneBudget(assistantItems, summarizeCompactLine(content, 260), 24, localFallbackAssistantRunes)
			}
			if len(message.ToolCalls) > 0 {
				names := make([]string, 0, len(message.ToolCalls))
				for _, call := range message.ToolCalls {
					if name := strings.TrimSpace(call.Name); name != "" {
						names = append(names, name)
					}
				}
				if len(names) > 0 {
					toolRequestItems = appendWithinRuneBudget(toolRequestItems, strings.Join(names, ", "), 12, localFallbackToolRequestRunes)
				}
			}
		case "tool":
			if content != "" {
				item := summarizeCompactLine(content, 300)
				if name := toolNames[strings.TrimSpace(message.ToolCallID)]; name != "" {
					item = name + ": " + item
				}
				toolItems = appendWithinRuneBudget(toolItems, item, 32, localFallbackToolRunes)
				if toolErr := strings.TrimSpace(message.Metadata.GetString("tool_error", "")); toolErr != "" {
					failure := summarizeCompactLine(toolErr, 300)
					if name := toolNames[strings.TrimSpace(message.ToolCallID)]; name != "" {
						failure = name + ": " + failure
					}
					failureItems = appendWithinRuneBudget(failureItems, failure, 12, localFallbackFailureRunes)
				}
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
		lines = append(lines, "Recent user requests:")
		for _, item := range userItems {
			lines = append(lines, "- "+item)
		}
	}
	if len(assistantItems) > 0 {
		lines = append(lines, "Assistant progress:")
		for _, item := range assistantItems {
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
	if len(failureItems) > 0 {
		lines = append(lines, "Recent tool failures to account for:")
		for _, item := range failureItems {
			lines = append(lines, "- "+item)
		}
	}
	lines = append(lines, "Next step: continue from the retained latest user request and use checkpoints or artifacts for full raw outputs when needed.")
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
	return &llm.LLMRequest{
		Provider:        strings.TrimSpace(req.Provider),
		Model:           strings.TrimSpace(req.Model),
		Messages:        buildLocalCompactionRequest(systemMessages, history),
		MaxTokens:       maxTokens,
		Temperature:     0,
		ReasoningEffort: strings.TrimSpace(reasoningEffort),
		Metadata: map[string]interface{}{
			llm.MetadataKeyInternalOperation: "compact",
			llm.MetadataKeyDisableTools:      true,
			llm.MetadataKeyDisableMetaTools:  true,
			"compact_mode":                   ModeLocal,
			"compact_phase":                  normalizedPhase(req.Phase),
			"session_id":                     strings.TrimSpace(req.SessionID),
		},
	}
}

// buildFittedLocalCompactionLLMRequest maps the omitted prefix locally and lets
// the provider reduce that checkpoint together with the largest safe recent
// suffix. It keeps oversized raw history off the wire while preserving one
// provider compaction pass whenever a reduced request can fit.
func buildFittedLocalCompactionLLMRequest(runtime *llm.LLMRuntime, req Request, threshold threshold, systemMessages, history []types.Message, maxTokens int, reasoningEffort, preflightReason string) *llm.LLMRequest {
	starts := safeLocalCompactionStarts(history)
	if len(starts) == 0 {
		return nil
	}

	buildCandidate := func(start int, systems []types.Message) *llm.LLMRequest {
		reduced := make([]types.Message, 0, len(history)-start+1)
		if start > 0 {
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
		if len(systems) == 0 && len(systemMessages) > 0 {
			candidate.Metadata["compact_system_messages_omitted"] = len(systemMessages)
		}
		return candidate
	}

	findFit := func(systems []types.Message) *llm.LLMRequest {
		last := buildCandidate(starts[len(starts)-1], systems)
		if localCompactionRequestBudgetFailure(runtime, last, threshold) != "" {
			return nil
		}
		low, high := 0, len(starts)-1
		for low < high {
			mid := low + (high-low)/2
			candidate := buildCandidate(starts[mid], systems)
			if localCompactionRequestBudgetFailure(runtime, candidate, threshold) == "" {
				high = mid
			} else {
				low = mid + 1
			}
		}
		return buildCandidate(starts[low], systems)
	}

	if fitted := findFit(systemMessages); fitted != nil {
		return fitted
	}
	return findFit(nil)
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
	if len(encoded) > localCompactMaxRequestBytes {
		return fmt.Sprintf("compact request skipped because serialized input is %d bytes (limit %d)", len(encoded), localCompactMaxRequestBytes)
	}

	inputTokens := runtime.CountMessagesTokens(request.Messages)
	if serializedTokens := runtime.CountTokens(string(encoded)); serializedTokens > inputTokens {
		inputTokens = serializedTokens
	}
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

func buildLocalReplacementHistory(systemMessages, retainedRecent []types.Message, summaryMessage types.Message) []types.Message {
	replacement := make([]types.Message, 0, len(systemMessages)+len(retainedRecent)+1)
	replacement = append(replacement, cloneMessages(systemMessages)...)
	replacement = append(replacement, *summaryMessage.Clone())
	replacement = append(replacement, cloneMessages(retainedRecent)...)
	return replacement
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
