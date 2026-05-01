package commands

import (
	"encoding/json"
	"strconv"
	"strings"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const chatRuntimeContextTokenCount = "aicli_token_count"

func applyChatTokenUsage(session *ChatSession, usage *runtimetypes.TokenUsage) {
	if session == nil || usage == nil || usage.TotalTokens <= 0 {
		return
	}
	session.TokenCount += usage.TotalTokens
	if session.Interaction != nil {
		session.Interaction.RefreshStatus("")
	}
}

func resetChatTurnTokenUsage(session *ChatSession) {
	if session == nil {
		return
	}
	session.ContextTokenCount = 0
	session.ContextWindowTokenCount = 0
	session.TurnContextTokenCount = 0
}

func resetChatConversationTokenUsage(session *ChatSession) {
	if session == nil {
		return
	}
	resetChatTurnTokenUsage(session)
	session.TokenCount = 0
}

func applyChatContextTokens(session *ChatSession, promptTokens int, windowTokens int, forceRefresh bool) {
	if session == nil {
		return
	}
	changed := false
	if promptTokens > 0 && session.ContextTokenCount != promptTokens {
		session.ContextTokenCount = promptTokens
		changed = true
	}
	if windowTokens > 0 && session.ContextWindowTokenCount != windowTokens {
		session.ContextWindowTokenCount = windowTokens
		changed = true
	}
	if (changed || forceRefresh) && session.Interaction != nil {
		session.Interaction.RefreshStatus("")
	}
}

func applyChatTurnContextTokens(session *ChatSession, promptTokens int, windowTokens int, forceRefresh bool) {
	if session == nil {
		return
	}
	changed := false
	if promptTokens > 0 {
		session.ContextTokenCount = promptTokens
		session.TurnContextTokenCount += promptTokens
		changed = true
	}
	if windowTokens > 0 && session.ContextWindowTokenCount != windowTokens {
		session.ContextWindowTokenCount = windowTokens
		changed = true
	}
	if (changed || forceRefresh) && session.Interaction != nil {
		session.Interaction.RefreshStatus("")
	}
}

func applyChatTurnContextTokensFromMessages(session *ChatSession, messages []runtimetypes.Message, forceRefresh bool) int {
	promptTokens := countChatContextTokensForMessages(session, messages)
	applyChatTurnContextTokens(session, promptTokens, 0, forceRefresh)
	return promptTokens
}

func countChatContextTokensForMessages(session *ChatSession, messages []runtimetypes.Message) int {
	if len(messages) == 0 {
		return 0
	}
	llmRuntime, err := buildSharedChatAutoCompactRuntime(session)
	if err == nil && llmRuntime != nil {
		if count := llmRuntime.CountMessagesTokens(messages); count > 0 {
			return count
		}
	}
	return countSharedChatMessagesTokens(messages)
}

func restoreChatTokenCount(session *ChatSession, runtimeSession *runtimechat.Session) {
	if session == nil {
		return
	}
	if count, ok := runtimeSessionContextInt(runtimeSession, chatRuntimeContextTokenCount); ok {
		session.TokenCount = count
	}
}

func runtimeSessionContextInt(session *runtimechat.Session, key string) (int, bool) {
	if session == nil {
		return 0, false
	}
	value, ok := session.GetContext(key)
	if !ok || value == nil {
		return 0, false
	}

	switch typed := value.(type) {
	case int:
		return typed, true
	case int8:
		return int(typed), true
	case int16:
		return int(typed), true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case uint:
		return int(typed), true
	case uint8:
		return int(typed), true
	case uint16:
		return int(typed), true
	case uint32:
		return int(typed), true
	case uint64:
		return int(typed), true
	case float32:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed), true
		}
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			return parsed, true
		}
	}
	return 0, false
}
