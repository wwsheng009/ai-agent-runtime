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
