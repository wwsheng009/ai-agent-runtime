package historyguard

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const DefaultActiveTurnReplayMaxBytes = 24 * 1024

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
	if preserveStart <= userIndex+1 || preserveStart > len(messages) {
		return messages, false
	}

	compacted := buildActiveTurnReplaySummary(messages[userIndex+1 : preserveStart])
	if compacted == nil {
		return messages, false
	}
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

	next := make([]types.Message, 0, len(messages)-(preserveStart-(userIndex+1))+1)
	next = append(next, cloneMessages(messages[:userIndex+1])...)
	next = append(next, *compacted)
	next = append(next, cloneMessages(messages[preserveStart:])...)
	if counter != nil && maxTokens > 0 && userIndex+1 < len(next) {
		if promptTokensAfter := counter(next); promptTokensAfter > 0 {
			next[userIndex+1].Metadata["prompt_tokens_after"] = promptTokensAfter
		}
	}
	return next, true
}

func buildActiveTurnReplaySummary(messages []types.Message) *types.Message {
	if len(messages) == 0 {
		return nil
	}

	assistantItems := make([]string, 0, 4)
	toolItems := make([]string, 0, 6)
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
					assistantItems = appendLimited(assistantItems, "assistant requested tools: "+strings.Join(names, ", "), 4)
				}
			} else if content != "" {
				assistantItems = appendLimited(assistantItems, summarizeLine(content, 180), 4)
			}
		case "tool":
			if content != "" {
				toolItems = appendLimited(toolItems, summarizeLine(content, 200), 6)
			}
		}
	}

	lines := []string{"Compacted earlier tool replay in current turn:"}
	if len(assistantItems) > 0 {
		lines = append(lines, "Assistant actions:")
		for _, item := range assistantItems {
			lines = append(lines, "- "+item)
		}
	}
	if len(toolItems) > 0 {
		lines = append(lines, "Tool outcomes:")
		for _, item := range toolItems {
			lines = append(lines, "- "+item)
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
