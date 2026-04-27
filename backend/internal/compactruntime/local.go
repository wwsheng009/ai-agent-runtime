package compactruntime

import (
	"context"
	"crypto/sha1"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	"github.com/wwsheng009/ai-agent-runtime/internal/contextmgr"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const (
	localSummaryCheckpointReason = "history_window_summary_segment"
	localSegmentStartKey         = "segment_start"
	localSegmentEndKey           = "segment_end"
	localSummaryTextKey          = "summary_text"
	localSummaryHeading          = "Compacted context from earlier turns:"
	localRetainedUserMaxTokens   = 20000
	localCompactDefaultMaxTokens = 2048
	localCompactionPrompt        = `You are performing a CONTEXT CHECKPOINT COMPACTION. Create a handoff summary for another LLM that will resume the task.

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

	summaryMessage, checkpointIDs, usage, usageSource, err := a.buildSummaryMessage(ctx, req, systemMessages, nonSystemMessages)
	if err != nil {
		return nil, "summary_generation_failed", err
	}
	if summaryMessage == nil {
		return nil, "summary_generation_empty", nil
	}

	retainedUsers := selectCompactionUserMessages(nonSystemMessages, counter, localRetainedUserMaxTokens)
	replacement := buildLocalReplacementHistory(systemMessages, nonSystemMessages, retainedUsers, *summaryMessage)

	compactedMessages := len(nonSystemMessages) - len(retainedUsers)
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
		TokenBefore:        counter(req.History),
		TokenAfter:         counter(replacement),
		Usage:              usage,
		UsageSource:        usageSource,
		CompactedMessages:  compactedMessages,
		CheckpointIDs:      append([]string(nil), checkpointIDs...),
		ReplacementHistory: replacement,
	}, "", nil
}

func (a *LocalAdapter) buildSummaryMessage(ctx context.Context, req Request, systemMessages, history []types.Message) (*types.Message, []string, *types.TokenUsage, string, error) {
	if message, checkpointID := a.findReusableSummaryCheckpoint(ctx, req.SessionID, req.Phase, history); message != nil {
		if checkpointID == "" {
			return message, nil, nil, "", nil
		}
		return message, []string{checkpointID}, nil, "", nil
	}
	if a == nil || a.llmRuntime == nil {
		return nil, nil, nil, "", fmt.Errorf("llm runtime is not configured")
	}

	maxTokens, reasoningEffort := resolveCompactSummaryRequestSettings(a.llmRuntime, req.Provider, req.Model)

	response, err := a.llmRuntime.Call(ctx, &llm.LLMRequest{
		Provider:        strings.TrimSpace(req.Provider),
		Model:           strings.TrimSpace(req.Model),
		Messages:        buildLocalCompactionRequest(systemMessages, history),
		MaxTokens:       maxTokens,
		Temperature:     0,
		ReasoningEffort: reasoningEffort,
		Metadata: map[string]interface{}{
			"internal_operation": "compact",
			"compact_mode":       ModeLocal,
			"compact_phase":      normalizedPhase(req.Phase),
			"session_id":         strings.TrimSpace(req.SessionID),
		},
	})
	if err != nil {
		return nil, nil, nil, "", err
	}

	summaryText := extractCompactSummaryText(response)
	if strings.TrimSpace(summaryText) == "" {
		return nil, nil, nil, "", fmt.Errorf("compact summary response is empty")
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

func buildLocalCompactionRequest(systemMessages, history []types.Message) []types.Message {
	request := make([]types.Message, 0, len(systemMessages)+len(history)+1)
	request = append(request, cloneMessages(systemMessages)...)
	request = append(request, cloneMessages(history)...)
	request = append(request, *types.NewUserMessage(localCompactionPrompt))
	return request
}

func selectCompactionUserMessages(messages []types.Message, counter TokenCounter, maxTokens int) []types.Message {
	userMessages := collectRetainedUserMessages(messages)
	if len(userMessages) == 0 {
		return nil
	}
	if counter == nil || maxTokens <= 0 {
		return userMessages
	}

	selectedStart := len(userMessages)
	for index := len(userMessages) - 1; index >= 0; index-- {
		candidate := cloneMessages(userMessages[index:])
		if counter(candidate) > maxTokens {
			break
		}
		selectedStart = index
	}
	if selectedStart < len(userMessages) {
		return cloneMessages(userMessages[selectedStart:])
	}
	return cloneMessages(userMessages[len(userMessages)-1:])
}

func collectRetainedUserMessages(messages []types.Message) []types.Message {
	if len(messages) == 0 {
		return nil
	}
	retained := make([]types.Message, 0, len(messages))
	for _, message := range messages {
		if message.Role != "user" {
			continue
		}
		if strings.EqualFold(message.Metadata.GetString("context_stage", ""), "compaction") {
			continue
		}
		retained = append(retained, *message.Clone())
	}
	return retained
}

func buildLocalReplacementHistory(systemMessages, nonSystemMessages, retainedUsers []types.Message, summaryMessage types.Message) []types.Message {
	replacement := make([]types.Message, 0, len(systemMessages)+len(retainedUsers)+1)
	replacement = append(replacement, cloneMessages(systemMessages)...)
	if shouldKeepTrailingUserAfterSummary(nonSystemMessages, retainedUsers) {
		replacement = append(replacement, cloneMessages(retainedUsers[:len(retainedUsers)-1])...)
		replacement = append(replacement, *summaryMessage.Clone())
		replacement = append(replacement, *retainedUsers[len(retainedUsers)-1].Clone())
		return replacement
	}
	replacement = append(replacement, cloneMessages(retainedUsers)...)
	replacement = append(replacement, *summaryMessage.Clone())
	return replacement
}

func shouldKeepTrailingUserAfterSummary(nonSystemMessages, retainedUsers []types.Message) bool {
	if len(nonSystemMessages) == 0 || len(retainedUsers) == 0 {
		return false
	}
	last := nonSystemMessages[len(nonSystemMessages)-1]
	if last.Role != "user" {
		return false
	}
	if strings.EqualFold(last.Metadata.GetString("context_stage", ""), "compaction") {
		return false
	}
	return true
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

func resolveCompactSummaryRequestSettings(runtime *llm.LLMRuntime, providerName, model string) (int, string) {
	maxTokens := localCompactDefaultMaxTokens
	reasoningEffort := "none"
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
