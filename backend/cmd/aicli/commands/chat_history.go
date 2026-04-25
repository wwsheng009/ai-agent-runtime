package commands

import (
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

type chatHistoryEntry struct {
	Role    string
	Content string
}

func printVisibleChatHistory(session *ChatSession, header string) int {
	entries := collectVisibleChatHistory(session)
	if len(entries) == 0 {
		return 0
	}
	if strings.TrimSpace(header) != "" {
		fmt.Printf("%s (%d 条消息):\n", strings.TrimSpace(header), len(entries))
	}
	for _, entry := range entries {
		printVisibleChatHistoryEntry(session, entry)
	}
	return len(entries)
}

func hasVisibleChatHistory(session *ChatSession) bool {
	return len(collectVisibleChatHistory(session)) > 0
}

func collectVisibleChatHistory(session *ChatSession) []chatHistoryEntry {
	if session == nil || len(session.Messages) == 0 {
		return nil
	}

	hiddenSystemPrompt := strings.TrimSpace(session.SystemPromptText)
	entries := make([]chatHistoryEntry, 0, len(session.Messages))
	for _, raw := range session.Messages {
		entry, ok := buildChatHistoryEntry(raw, hiddenSystemPrompt)
		if !ok {
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}

func buildChatHistoryEntry(raw map[string]interface{}, hiddenSystemPrompt string) (chatHistoryEntry, bool) {
	if len(raw) == 0 {
		return chatHistoryEntry{}, false
	}

	role := strings.ToLower(strings.TrimSpace(chatHistoryString(raw["role"])))
	if role == "" {
		return chatHistoryEntry{}, false
	}

	content := strings.TrimSpace(chatHistoryString(raw["content"]))
	toolSummary := chatHistoryToolSummary(raw["tool_calls"])

	switch role {
	case "system":
		if content == "" || (hiddenSystemPrompt != "" && content == hiddenSystemPrompt) {
			return chatHistoryEntry{}, false
		}
	case "assistant":
		switch {
		case content != "" && toolSummary != "":
			content += "\n调用工具: " + toolSummary
		case content == "" && toolSummary != "":
			content = "调用工具: " + toolSummary
		case content == "":
			return chatHistoryEntry{}, false
		}
	case "tool":
		if content == "" {
			return chatHistoryEntry{}, false
		}
		content = truncateOutputPreview(content, maxToolResultPreviewLines, maxToolResultPreviewBytes)
		if toolCallID := strings.TrimSpace(chatHistoryString(raw["tool_call_id"])); toolCallID != "" {
			content = fmt.Sprintf("[%s] %s", toolCallID, content)
		}
	default:
		if content == "" {
			return chatHistoryEntry{}, false
		}
		if role != "user" {
			content = fmt.Sprintf("[%s] %s", role, content)
			role = "system"
		}
	}

	return chatHistoryEntry{
		Role:    role,
		Content: content,
	}, true
}

func printVisibleChatHistoryEntry(session *ChatSession, entry chatHistoryEntry) {
	content := entry.Content
	switch entry.Role {
	case "assistant":
		if session != nil && session.Formatter != nil {
			content = session.Formatter.Format(content)
		}
		ui.DisplayAssistantMessage(content)
	case "tool":
		ui.DisplayToolMessage(content)
	case "system":
		ui.DisplaySystemMessage(content)
	default:
		ui.DisplayUserMessage(content)
	}
}

func chatHistoryString(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", typed)
	}
}

func chatHistoryToolSummary(raw interface{}) string {
	names := chatHistoryToolNames(raw)
	if len(names) == 0 {
		return ""
	}
	return strings.Join(names, ", ")
}

func chatHistoryToolNames(raw interface{}) []string {
	switch typed := raw.(type) {
	case []map[string]interface{}:
		names := make([]string, 0, len(typed))
		for _, item := range typed {
			if name := chatHistoryToolName(item); name != "" {
				names = append(names, name)
			}
		}
		return names
	case []interface{}:
		names := make([]string, 0, len(typed))
		for _, item := range typed {
			itemMap, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			if name := chatHistoryToolName(itemMap); name != "" {
				names = append(names, name)
			}
		}
		return names
	default:
		return nil
	}
}

func chatHistoryToolName(raw map[string]interface{}) string {
	if len(raw) == 0 {
		return ""
	}
	if function, ok := raw["function"].(map[string]interface{}); ok {
		if name := strings.TrimSpace(chatHistoryString(function["name"])); name != "" {
			return name
		}
	}
	return strings.TrimSpace(chatHistoryString(raw["name"]))
}
