package commands

import (
	"encoding/json"
	"strconv"
	"strings"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const chatRuntimeContextTokenCount = "aicli_token_count"
const chatRuntimeContextContextTokenCount = "aicli_context_token_count"
const chatRuntimeContextContextWindowTokenCount = "aicli_context_window_token_count"
const chatRuntimeContextTurnContextTokenCount = "aicli_turn_context_token_count"

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
	session.TurnContextTokenCount = 0
}

func resetChatConversationTokenUsage(session *ChatSession) {
	if session == nil {
		return
	}
	resetChatContextTokenUsage(session)
	session.TokenCount = 0
}

func resetChatContextTokenUsage(session *ChatSession) {
	if session == nil {
		return
	}
	session.ContextTokenCount = 0
	session.ContextWindowTokenCount = 0
	session.TurnContextTokenCount = 0
	session.providerContextTokenCount = 0
	session.providerContextWindowTokenCount = 0
}

func applyChatContextTokens(session *ChatSession, promptTokens int, windowTokens int, forceRefresh bool) {
	applyChatContextTokensLocked(session, promptTokens, windowTokens, forceRefresh, false)
}

func applyChatContextTokensLocked(session *ChatSession, promptTokens int, windowTokens int, forceRefresh bool, providerReported bool) {
	if session == nil {
		return
	}
	changed := false
	if !providerReported {
		session.providerContextTokenCount = 0
		session.providerContextWindowTokenCount = 0
	}
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

func applyChatContextTokensFromUsage(session *ChatSession, usage *runtimetypes.TokenUsage, windowTokens int, forceRefresh bool) int {
	if session == nil || usage == nil {
		return 0
	}
	contextTokens := usage.TotalTokens
	if contextTokens <= 0 {
		contextTokens = usage.PromptTokens + usage.CompletionTokens
	}
	if contextTokens <= 0 {
		return 0
	}
	if session.ContextTokenCount > 0 && contextTokens < session.ContextTokenCount {
		changed := false
		if windowTokens > 0 && session.ContextWindowTokenCount != windowTokens {
			session.ContextWindowTokenCount = windowTokens
			changed = true
		}
		if (changed || forceRefresh) && session.Interaction != nil {
			session.Interaction.RefreshStatus("")
		}
		return session.ContextTokenCount
	}
	session.providerContextTokenCount = contextTokens
	session.providerContextWindowTokenCount = windowTokens
	applyChatContextTokensLocked(session, contextTokens, windowTokens, forceRefresh, true)
	return contextTokens
}

func applyChatContextTokensFromMessages(session *ChatSession, messages []runtimetypes.Message, windowTokens int, forceRefresh bool) int {
	promptTokens := countChatContextTokensForMessages(session, messages)
	if promptTokens > 0 {
		applyChatContextTokens(session, promptTokens, windowTokens, forceRefresh)
		return promptTokens
	}
	if len(messages) == 0 {
		resetChatContextTokenUsage(session)
		if forceRefresh && session != nil && session.Interaction != nil {
			session.Interaction.RefreshStatus("")
		}
	}
	return 0
}

func refreshChatContextTokenSnapshotFromMessages(session *ChatSession, windowTokens int, forceRefresh bool) int {
	if session == nil {
		return 0
	}
	if !chatMessagesHaveConversation(session.Messages) {
		resetChatContextTokenUsage(session)
		if forceRefresh && session.Interaction != nil {
			session.Interaction.RefreshStatus("")
		}
		return 0
	}
	if windowTokens <= 0 {
		windowTokens = session.ContextWindowTokenCount
	}
	if windowTokens <= 0 {
		budget := resolveSharedChatPromptBudget(session)
		windowTokens = budget.ModelCapabilityMaxContextTokens
		if windowTokens <= 0 {
			windowTokens = budget.ProviderContextLimit
		}
		if windowTokens <= 0 {
			windowTokens = budget.ActiveTurnMaxTokens
		}
	}
	return applyChatContextTokensFromMessages(session, session.Messages, windowTokens, forceRefresh)
}

func resolveChatContextSnapshotTokens(session *ChatSession, fallbackMessages []runtimetypes.Message) int {
	if session == nil {
		return 0
	}
	if session.ContextTokenCount > 0 {
		return session.ContextTokenCount
	}
	messages := fallbackMessages
	if len(messages) == 0 && len(session.Messages) > 0 {
		messages = session.Messages
	}
	if len(messages) == 0 && session.RuntimeSession != nil && len(session.RuntimeSession.History) > 0 {
		messages = session.RuntimeSession.History
	}
	if !chatMessagesHaveConversation(messages) {
		return 0
	}
	return countChatContextTokensForMessages(session, messages)
}

func resolveChatObservedTokenUsage(session *ChatSession, fallbackMessages []runtimetypes.Message) int {
	if session == nil {
		return 0
	}
	if session.ContextTokenCount > 0 {
		return session.ContextTokenCount
	}
	return resolveChatContextSnapshotTokens(session, fallbackMessages)
}

func applyChatTurnContextTokens(session *ChatSession, promptTokens int, windowTokens int, forceRefresh bool) {
	if session == nil {
		return
	}
	changed := false
	if promptTokens > 0 {
		session.TurnContextTokenCount += promptTokens
		session.providerContextTokenCount = 0
		session.providerContextWindowTokenCount = 0
		if session.ContextTokenCount <= 0 || promptTokens > session.ContextTokenCount {
			session.ContextTokenCount = promptTokens
			changed = true
		}
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
	} else {
		session.TokenCount = 0
	}
}

func restoreChatContextTokenUsage(session *ChatSession, runtimeSession *runtimechat.Session) {
	if session == nil {
		return
	}
	resetChatContextTokenUsage(session)
	if count, ok := runtimeSessionContextInt(runtimeSession, chatRuntimeContextContextTokenCount); ok {
		session.ContextTokenCount = count
	}
	if count, ok := runtimeSessionContextInt(runtimeSession, chatRuntimeContextContextWindowTokenCount); ok {
		session.ContextWindowTokenCount = count
	}
	if count, ok := runtimeSessionContextInt(runtimeSession, chatRuntimeContextTurnContextTokenCount); ok {
		session.TurnContextTokenCount = count
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
