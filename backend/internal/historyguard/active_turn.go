package historyguard

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const DefaultActiveTurnReplayMaxBytes = 24 * 1024

const (
	defaultLatestReplayToolResultMaxBytes = 4096
	latestReplayToolResultHeadRunes       = 1400
	latestReplayToolResultTailRunes       = 1000
)

func CompactActiveTurnReplay(messages []types.Message, maxBytes int) ([]types.Message, bool) {
	return CompactActiveTurnReplayWithCounter(messages, maxBytes, 0, nil)
}

func CompactActiveTurnReplayWithCounter(messages []types.Message, maxBytes int, maxTokens int, counter func([]types.Message) int) ([]types.Message, bool) {
	if len(messages) == 0 {
		return messages, false
	}
	if maxBytes <= 0 {
		maxBytes = DefaultActiveTurnReplayMaxBytes
	}

	userIndex := activeUserTurnStart(messages)
	if userIndex < 0 || userIndex >= len(messages)-1 {
		return messages, false
	}

	activeBytes := estimatedMessagesBytes(messages[userIndex:])
	overBytes := activeBytes > maxBytes
	activeTokens := 0
	totalTokens := 0
	overTokens := false
	overTotalTokens := false
	if counter != nil && maxTokens > 0 {
		activeTokens = counter(messages[userIndex:])
		totalTokens = counter(messages)
		overTokens = activeTokens > maxTokens
		overTotalTokens = totalTokens > maxTokens
	}
	if !overBytes && !overTokens && !overTotalTokens {
		return messages, false
	}

	preserveStart := latestReplayBlockStart(messages, userIndex)
	if preserveStart < userIndex+1 || preserveStart > len(messages) {
		return messages, false
	}

	current := messages
	compactedOlderReplay := false
	if preserveStart > userIndex+1 {
		compactStart := userIndex + 1
		if anchorStart := latestCompactionSummaryStart(messages, userIndex, preserveStart); anchorStart >= 0 {
			if anchorStart+1 < preserveStart {
				compactStart = anchorStart + 1
			} else {
				compactStart = preserveStart
			}
		}

		if compactStart < preserveStart {
			compacted := buildActiveTurnReplaySummary(messages[compactStart:preserveStart])
			if compacted != nil {
				if activeBytes > 0 {
					compacted.Metadata["active_turn_bytes_before"] = activeBytes
				}
				if activeTokens > 0 {
					compacted.Metadata["active_turn_tokens_before"] = activeTokens
				}
				if totalTokens > 0 {
					compacted.Metadata["prompt_tokens_before"] = totalTokens
				}
				if reason := joinCompactionReasons(overBytes, overTokens, overTotalTokens); reason != "" {
					compacted.Metadata["active_turn_compaction_reason"] = reason
				}

				next := make([]types.Message, 0, len(messages)-(preserveStart-compactStart)+1)
				next = append(next, cloneMessages(messages[:compactStart])...)
				next = append(next, *compacted)
				next = append(next, cloneMessages(messages[preserveStart:])...)
				if counter != nil && maxTokens > 0 && userIndex+1 < len(next) {
					if promptTokensAfter := counter(next); promptTokensAfter > 0 {
						next[userIndex+1].Metadata["prompt_tokens_after"] = promptTokensAfter
					}
				}
				current = next
				compactedOlderReplay = true
				if !messagesExceedActiveTurnBudget(current, maxBytes, maxTokens, counter) {
					return current, true
				}
			}
		}
	}

	reduced, reducedLatestReplay := reduceLatestReplayToolResults(current, maxBytes, maxTokens, counter)
	if reducedLatestReplay {
		return reduced, true
	}
	if compactedOlderReplay {
		return current, true
	}
	return messages, false
}

func latestCompactionSummaryStart(messages []types.Message, userIndex, preserveStart int) int {
	if userIndex < 0 || preserveStart <= userIndex+1 {
		return -1
	}
	for index := preserveStart - 1; index > userIndex; index-- {
		if !isActiveTurnCompactionSummary(messages[index]) {
			continue
		}
		if strings.TrimSpace(messages[index].Content) != "" {
			return index
		}
	}
	return -1
}

// HasActiveTurnCompactionSummary reports whether the current active user turn
// already contains a compacted replay summary that should be preserved as an
// anchor instead of being folded into another summary.
func HasActiveTurnCompactionSummary(messages []types.Message) bool {
	userIndex := activeUserTurnStart(messages)
	if userIndex < 0 || userIndex >= len(messages)-1 {
		return false
	}
	return latestCompactionSummaryStart(messages, userIndex, len(messages)) >= 0
}

func messagesExceedActiveTurnBudget(messages []types.Message, maxBytes int, maxTokens int, counter func([]types.Message) int) bool {
	userIndex := activeUserTurnStart(messages)
	if userIndex < 0 || userIndex >= len(messages)-1 {
		return false
	}
	if estimatedMessagesBytes(messages[userIndex:]) > maxBytes {
		return true
	}
	if counter == nil || maxTokens <= 0 {
		return false
	}
	return counter(messages[userIndex:]) > maxTokens || counter(messages) > maxTokens
}

func reduceLatestReplayToolResults(messages []types.Message, maxBytes int, maxTokens int, counter func([]types.Message) int) ([]types.Message, bool) {
	userIndex := activeUserTurnStart(messages)
	if userIndex < 0 || userIndex >= len(messages)-1 {
		return messages, false
	}
	preserveStart := latestReplayBlockStart(messages, userIndex)
	if preserveStart < userIndex+1 || preserveStart >= len(messages) {
		return messages, false
	}

	next := cloneMessages(messages)
	reducedCount := 0
	for index := preserveStart; index < len(next); index++ {
		if next[index].Role != "tool" {
			continue
		}
		reduced, changed := buildReducedToolResultContent(next[index].Content, defaultLatestReplayToolResultMaxBytes)
		if !changed {
			continue
		}
		if next[index].Metadata == nil {
			next[index].Metadata = types.NewMetadata()
		}
		next[index].Metadata["active_turn_tool_result_reduced"] = true
		next[index].Metadata["tool_result_bytes_before"] = len(next[index].Content)
		next[index].Metadata["tool_result_lines_before"] = countLines(next[index].Content)
		next[index].Content = reduced
		next[index].Metadata["tool_result_bytes_after"] = len(next[index].Content)
		reducedCount++
	}
	if reducedCount == 0 {
		return messages, false
	}

	if preserveStart < len(next) {
		if next[preserveStart].Metadata == nil {
			next[preserveStart].Metadata = types.NewMetadata()
		}
		next[preserveStart].Metadata["active_turn_latest_replay_reduced"] = true
		next[preserveStart].Metadata["active_turn_latest_replay_reduced_tools"] = reducedCount
		if counter != nil && maxTokens > 0 {
			if promptTokensAfter := counter(next); promptTokensAfter > 0 {
				next[preserveStart].Metadata["prompt_tokens_after"] = promptTokensAfter
			}
		}
	}
	return next, true
}

func buildReducedToolResultContent(content string, maxBytes int) (string, bool) {
	content = strings.TrimSpace(strings.ReplaceAll(content, "\r\n", "\n"))
	if content == "" || maxBytes <= 0 || len(content) <= maxBytes {
		return content, false
	}

	lineCount := countLines(content)
	head := firstRunes(content, latestReplayToolResultHeadRunes)
	tail := lastRunes(content, latestReplayToolResultTailRunes)
	refs := extractArtifactReferenceLines(content, 6)

	lines := []string{
		"Tool result content compacted for prompt budget.",
		fmt.Sprintf("Original bytes: %d", len(content)),
		fmt.Sprintf("Original lines: %d", lineCount),
		"Preserved excerpt:",
		"--- head ---",
		strings.TrimSpace(head),
	}
	if len(refs) > 0 {
		lines = append(lines, "Key reference lines:")
		for _, ref := range refs {
			lines = append(lines, "- "+ref)
		}
	}
	if strings.TrimSpace(tail) != "" && strings.TrimSpace(tail) != strings.TrimSpace(head) {
		lines = append(lines, "--- tail ---", strings.TrimSpace(tail))
	}

	reduced := strings.TrimSpace(strings.Join(lines, "\n"))
	if len(reduced) > maxBytes {
		if maxBytes <= 3 {
			reduced = truncateBytes(reduced, maxBytes)
		} else {
			reduced = truncateBytes(reduced, maxBytes-3) + "..."
		}
	}
	return reduced, true
}

func extractArtifactReferenceLines(content string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	lines := strings.Split(content, "\n")
	refs := make([]string, 0, limit)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		if !strings.Contains(lower, "artifact") && !strings.Contains(lower, "checkpoint") {
			continue
		}
		refs = appendLimited(refs, summarizeLine(trimmed, 220), limit)
		if len(refs) >= limit {
			return refs
		}
	}
	return refs
}

func isActiveTurnCompactionSummary(message types.Message) bool {
	if message.Metadata.GetBool("active_turn_compaction", false) {
		return true
	}
	content := strings.TrimSpace(message.Content)
	return strings.HasPrefix(content, "Compacted earlier tool replay in current turn:")
}

func buildActiveTurnReplaySummary(messages []types.Message) *types.Message {
	if len(messages) == 0 {
		return nil
	}

	assistantItems := make([]string, 0)
	toolItems := make([]string, 0)
	toolCalls := 0

	for _, message := range messages {
		content := strings.TrimSpace(message.Content)
		switch message.Role {
		case "assistant":
			if len(message.ToolCalls) > 0 {
				names := make([]string, 0, len(message.ToolCalls))
				for _, call := range message.ToolCalls {
					toolCalls++
					if strings.TrimSpace(call.Name) != "" {
						names = append(names, strings.TrimSpace(call.Name))
					}
				}
				if len(names) > 0 {
					assistantItems = append(assistantItems, "assistant requested tools: "+strings.Join(names, ", "))
				}
			} else if content != "" {
				assistantItems = append(assistantItems, summarizeLine(content, 180))
			}
		case "tool":
			if content != "" {
				toolItems = append(toolItems, summarizeLine(content, 200))
			}
		}
	}

	lines := []string{"Compacted earlier tool replay in current turn:"}
	if head, recent := splitSummaryItems(assistantItems, 4, 3); len(head) > 0 {
		lines = append(lines, "Assistant actions:")
		for _, item := range head {
			lines = append(lines, "- "+item)
		}
		if len(recent) > 0 {
			lines = append(lines, "Recent assistant actions:")
			for _, item := range recent {
				lines = append(lines, "- "+item)
			}
		}
	}
	if head, recent := splitSummaryItems(toolItems, 6, 4); len(head) > 0 {
		lines = append(lines, "Tool outcomes:")
		for _, item := range head {
			lines = append(lines, "- "+item)
		}
		if len(recent) > 0 {
			lines = append(lines, "Recent tool outcomes:")
			for _, item := range recent {
				lines = append(lines, "- "+item)
			}
		}
	}

	message := types.NewAssistantMessage(strings.Join(lines, "\n"))
	message.Metadata["active_turn_compaction"] = true
	message.Metadata["compacted_messages"] = len(messages)
	message.Metadata["compacted_tool_calls"] = toolCalls
	return message
}

func latestReplayBlockStart(messages []types.Message, userIndex int) int {
	if userIndex < 0 || userIndex >= len(messages)-1 {
		return len(messages)
	}

	index := len(messages) - 1
	for index > userIndex && isTrailingContextMessage(messages[index]) {
		index--
	}
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

func isTrailingContextMessage(message types.Message) bool {
	if strings.TrimSpace(message.Metadata.GetString("context_stage", "")) != "" {
		return true
	}
	content := strings.TrimSpace(message.Content)
	return strings.HasPrefix(content, "Relevant recalled artifacts:") ||
		strings.HasPrefix(content, "Recent observations:") ||
		strings.HasPrefix(content, "Workspace recall:")
}

func activeUserTurnStart(messages []types.Message) int {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == "user" {
			return index
		}
	}
	return -1
}

func estimatedMessagesBytes(messages []types.Message) int {
	total := 0
	for _, message := range messages {
		total += len(message.Content)
		total += len(message.Role) + len(message.ToolCallID) + 16
		for _, call := range message.ToolCalls {
			total += len(call.ID) + len(call.Name)
			if len(call.Args) == 0 {
				continue
			}
			if payload, err := json.Marshal(call.Args); err == nil {
				total += len(payload)
			} else {
				total += len(fmt.Sprintf("%v", call.Args))
			}
		}
	}
	return total
}

func cloneMessages(messages []types.Message) []types.Message {
	if len(messages) == 0 {
		return nil
	}
	cloned := make([]types.Message, len(messages))
	for index := range messages {
		cloned[index] = *messages[index].Clone()
	}
	return cloned
}

func appendLimited(items []string, item string, limit int) []string {
	if strings.TrimSpace(item) == "" || len(items) >= limit {
		return items
	}
	return append(items, item)
}

func splitSummaryItems(items []string, headLimit, recentLimit int) ([]string, []string) {
	if len(items) == 0 {
		return nil, nil
	}
	if headLimit <= 0 || len(items) <= headLimit {
		return append([]string(nil), items...), nil
	}
	head := append([]string(nil), items[:headLimit]...)
	if recentLimit <= 0 {
		return head, nil
	}
	recentStart := len(items) - recentLimit
	if recentStart < headLimit {
		recentStart = headLimit
	}
	if recentStart >= len(items) {
		return head, nil
	}
	return head, append([]string(nil), items[recentStart:]...)
}

func joinCompactionReasons(overBytes, overTokens, overTotalTokens bool) string {
	reasons := make([]string, 0, 3)
	if overBytes {
		reasons = append(reasons, "bytes")
	}
	if overTokens {
		reasons = append(reasons, "active_turn_tokens")
	}
	if overTotalTokens {
		reasons = append(reasons, "prompt_tokens")
	}
	return strings.Join(reasons, "+")
}

func summarizeLine(text string, limit int) string {
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

func countLines(text string) int {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

func firstRunes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

func lastRunes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[len(runes)-limit:])
}

func truncateBytes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	var builder strings.Builder
	builder.Grow(limit)
	written := 0
	for _, r := range text {
		part := string(r)
		if written+len(part) > limit {
			break
		}
		builder.WriteString(part)
		written += len(part)
	}
	return builder.String()
}
