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
	localSummaryCheckpointReason = "history_window_summary_segment"
	localSegmentStartKey         = "segment_start"
	localSegmentEndKey           = "segment_end"
	localSummaryTextKey          = "summary_text"
	localSummaryHeading          = "Compacted context from earlier turns:"
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

	windowStart := recentWindowStart(nonSystemMessages, req.KeepRecentMessages)
	if windowStart <= 0 {
		return nil, "recent_window_covers_history", nil
	}

	older := cloneMessages(nonSystemMessages[:windowStart])
	recent := cloneMessages(nonSystemMessages[windowStart:])
	if len(older) == 0 {
		return nil, "no_compaction_segment", nil
	}

	summaryMessage, checkpointIDs, err := a.buildSummaryMessage(ctx, req, older)
	if err != nil {
		return nil, "summary_generation_failed", err
	}
	if summaryMessage == nil {
		return nil, "summary_generation_empty", nil
	}

	replacement := make([]types.Message, 0, len(systemMessages)+len(recent)+1)
	replacement = append(replacement, cloneMessages(systemMessages)...)
	replacement = append(replacement, *summaryMessage)
	replacement = append(replacement, recent...)

	return &Result{
		Mode:               ModeLocal,
		Phase:              normalizedPhase(req.Phase),
		ResolvedProvider:   threshold.ResolvedProvider,
		ResolvedModel:      threshold.ResolvedModel,
		TriggerTokenLimit:  threshold.TriggerTokenLimit,
		MaxContextTokens:   threshold.MaxContextTokens,
		TokenBefore:        counter(req.History),
		TokenAfter:         counter(replacement),
		CompactedMessages:  len(older),
		CheckpointIDs:      append([]string(nil), checkpointIDs...),
		ReplacementHistory: replacement,
	}, "", nil
}

func (a *LocalAdapter) buildSummaryMessage(ctx context.Context, req Request, older []types.Message) (*types.Message, []string, error) {
	if message, checkpointID := a.findReusableSummaryCheckpoint(ctx, req.SessionID, req.Phase, older); message != nil {
		if checkpointID == "" {
			return message, nil, nil
		}
		return message, []string{checkpointID}, nil
	}
	if a == nil || a.llmRuntime == nil {
		return nil, nil, fmt.Errorf("llm runtime is not configured")
	}

	response, err := a.llmRuntime.Call(ctx, &llm.LLMRequest{
		Provider:    strings.TrimSpace(req.Provider),
		Model:       strings.TrimSpace(req.Model),
		Messages:    buildLocalSummaryPrompt(older),
		MaxTokens:   1024,
		Temperature: 0,
		Metadata: map[string]interface{}{
			"internal_operation": "compact",
			"compact_mode":       ModeLocal,
			"compact_phase":      normalizedPhase(req.Phase),
			"session_id":         strings.TrimSpace(req.SessionID),
		},
	})
	if err != nil {
		return nil, nil, err
	}

	summaryText := ensureSummaryHeading(response.Content)
	if strings.TrimSpace(summaryText) == "" {
		return nil, nil, fmt.Errorf("compact summary response is empty")
	}

	checkpointID := a.saveSummaryCheckpoint(ctx, req.SessionID, req.TaskID, older, summaryText)
	message := buildCompactionMessage(summaryText, checkpointID, 0, len(older), req.Phase)
	if message == nil {
		return nil, nil, fmt.Errorf("failed to build compaction message")
	}
	if checkpointID == "" {
		return message, nil, nil
	}
	return message, []string{checkpointID}, nil
}

func buildLocalSummaryPrompt(history []types.Message) []types.Message {
	return []types.Message{
		*types.NewSystemMessage("You are compacting earlier conversation history so an agent can continue working. Produce a concise plain-text handoff summary. Preserve user goals, constraints, key decisions, file paths, commands, tool outcomes, unresolved issues, and next steps. Do not invent facts. Do not include code fences."),
		*types.NewUserMessage("Summarize this earlier conversation history for continued execution:\n\n" + renderHistory(history)),
	}
}

func renderHistory(history []types.Message) string {
	if len(history) == 0 {
		return ""
	}
	lines := make([]string, 0, len(history)*4)
	for index, message := range history {
		lines = append(lines, fmt.Sprintf("[%d] role=%s", index+1, strings.TrimSpace(message.Role)))
		if strings.TrimSpace(message.Content) != "" {
			lines = append(lines, strings.TrimSpace(message.Content))
		}
		if len(message.ToolCalls) > 0 {
			for _, call := range message.ToolCalls {
				payload, _ := json.Marshal(call.Args)
				lines = append(lines, fmt.Sprintf("tool_call id=%s name=%s args=%s", strings.TrimSpace(call.ID), strings.TrimSpace(call.Name), strings.TrimSpace(string(payload))))
			}
		}
		if strings.TrimSpace(message.ToolCallID) != "" {
			lines = append(lines, "tool_result_for="+strings.TrimSpace(message.ToolCallID))
		}
		lines = append(lines, "")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
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

func buildCompactionMessage(summaryText, checkpointID string, segmentStart, segmentEnd int, phase string) *types.Message {
	summaryText = strings.TrimSpace(summaryText)
	if summaryText == "" {
		return nil
	}
	message := types.NewAssistantMessage(summaryText)
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

func (a *LocalAdapter) findReusableSummaryCheckpoint(ctx context.Context, sessionID, phase string, older []types.Message) (*types.Message, string) {
	store := summaryCheckpointStore(a.contextManager)
	if store == nil || strings.TrimSpace(sessionID) == "" || len(older) == 0 {
		return nil, ""
	}

	checkpoints := listSummaryCheckpoints(ctx, store, sessionID)
	if len(checkpoints) == 0 {
		return nil, ""
	}
	expectedHash := hashHistory(older)
	for _, checkpoint := range checkpoints {
		if strings.TrimSpace(checkpoint.Reason) != localSummaryCheckpointReason {
			continue
		}
		start, end, ok := checkpointRange(checkpoint)
		if !ok || start != 0 || end != len(older) {
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

func (a *LocalAdapter) saveSummaryCheckpoint(ctx context.Context, sessionID, taskID string, older []types.Message, summaryText string) string {
	store := summaryCheckpointStore(a.contextManager)
	if store == nil || strings.TrimSpace(sessionID) == "" || len(older) == 0 {
		return ""
	}

	checkpoint := artifact.Checkpoint{
		SessionID:    strings.TrimSpace(sessionID),
		TaskID:       firstNonEmpty(strings.TrimSpace(taskID), strings.TrimSpace(sessionID)),
		Reason:       localSummaryCheckpointReason,
		HistoryHash:  hashHistory(older),
		MessageCount: len(older),
		Metadata: map[string]interface{}{
			"source_messages":    len(older),
			localSummaryTextKey:  summaryText,
			localSegmentStartKey: 0,
			localSegmentEndKey:   len(older),
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

func recentWindowStart(messages []types.Message, count int) int {
	if count <= 0 || len(messages) <= count {
		return 0
	}
	start := len(messages) - count
	if start <= 0 || start >= len(messages) {
		return 0
	}
	start = adjustRecentWindowForToolBlock(messages, start)
	if turnStart := activeUserTurnStart(messages); turnStart >= 0 && turnStart < start {
		return turnStart
	}
	return start
}

func adjustRecentWindowForToolBlock(messages []types.Message, start int) int {
	if start <= 0 || start >= len(messages) {
		return 0
	}
	if messages[start].Role != "tool" {
		return start
	}
	blockStart := start
	for blockStart > 0 && messages[blockStart-1].Role == "tool" {
		blockStart--
	}
	if blockStart > 0 {
		previous := messages[blockStart-1]
		if previous.Role == "assistant" && len(previous.ToolCalls) > 0 {
			return blockStart - 1
		}
	}
	return blockStart
}

func activeUserTurnStart(messages []types.Message) int {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == "user" {
			return index
		}
	}
	return -1
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
