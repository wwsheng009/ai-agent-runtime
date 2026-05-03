package commands

import (
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type chatHistoryEntry struct {
	Role    string
	Content string
}

func truncateAICLIMessages(session *ChatSession, keep int) {
	if session == nil {
		return
	}
	if keep <= 0 {
		session.Messages = nil
		return
	}
	if keep >= len(session.Messages) {
		return
	}
	truncated := make([]runtimetypes.Message, keep)
	for index := 0; index < keep; index++ {
		truncated[index] = *session.Messages[index].Clone()
	}
	session.Messages = truncated
}

func syncChatSystemPromptMessage(session *ChatSession) {
	if session == nil {
		return
	}
	prompt := strings.TrimSpace(composeChatSystemPromptWithGuidance(session))
	if prompt == "" {
		return
	}
	systemMessage := *runtimetypes.NewSystemMessage(prompt)
	if len(session.Messages) == 0 {
		replaceRuntimeMessages(session, []runtimetypes.Message{systemMessage})
		return
	}
	if strings.EqualFold(strings.TrimSpace(session.Messages[0].Role), "system") {
		replaced := make([]runtimetypes.Message, len(session.Messages))
		copy(replaced, session.Messages)
		updatedSystem := *replaced[0].Clone()
		updatedSystem.Content = prompt
		replaced[0] = updatedSystem
		replaceRuntimeMessages(session, replaced)
		return
	}
	messages := make([]runtimetypes.Message, 0, len(session.Messages)+1)
	messages = append(messages, systemMessage)
	messages = append(messages, session.Messages...)
	replaceRuntimeMessages(session, messages)
}

func appendRuntimeMessage(session *ChatSession, message runtimetypes.Message) {
	if session == nil {
		return
	}
	session.Messages = append(cloneRuntimeMessages(session.Messages), *message.Clone())
}

func replaceRuntimeMessages(session *ChatSession, messages []runtimetypes.Message) error {
	if session == nil {
		return nil
	}
	for _, message := range messages {
		if strings.TrimSpace(message.Role) == "" {
			return fmt.Errorf("message role cannot be empty")
		}
	}
	session.Messages = cloneRuntimeMessages(messages)
	return nil
}

func cloneRuntimeMessages(messages []runtimetypes.Message) []runtimetypes.Message {
	if len(messages) == 0 {
		return nil
	}
	cloned := make([]runtimetypes.Message, len(messages))
	for index := range messages {
		cloned[index] = *messages[index].Clone()
	}
	return cloned
}

func chatMessagesHaveConversation(messages []runtimetypes.Message) bool {
	for _, message := range messages {
		if !strings.EqualFold(strings.TrimSpace(message.Role), "system") {
			return true
		}
	}
	return false
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

	hiddenSystemPrompt := strings.TrimSpace(composeChatSystemPromptWithGuidance(session))
	rawSystemPrompt := strings.TrimSpace(session.SystemPromptText)
	entries := make([]chatHistoryEntry, 0, len(session.Messages))
	for _, message := range session.Messages {
		entry, ok := buildChatHistoryEntry(message, hiddenSystemPrompt, rawSystemPrompt)
		if !ok {
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}

func buildChatHistoryEntry(message runtimetypes.Message, hiddenSystemPrompt string, rawSystemPrompt string) (chatHistoryEntry, bool) {
	if strings.TrimSpace(message.Role) == "" {
		return chatHistoryEntry{}, false
	}

	role := strings.ToLower(strings.TrimSpace(message.Role))
	content := strings.TrimSpace(message.Content)
	toolSummary := chatHistoryToolSummary(message.ToolCalls)

	switch role {
	case "system":
		if content == "" || (hiddenSystemPrompt != "" && content == hiddenSystemPrompt) || (rawSystemPrompt != "" && content == rawSystemPrompt) {
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
		if toolCallID := strings.TrimSpace(message.ToolCallID); toolCallID != "" {
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

func chatHistoryToolSummary(toolCalls []runtimetypes.ToolCall) string {
	names := chatHistoryToolNames(toolCalls)
	if len(names) == 0 {
		return ""
	}
	return strings.Join(names, ", ")
}

func chatHistoryToolNames(toolCalls []runtimetypes.ToolCall) []string {
	if len(toolCalls) == 0 {
		return nil
	}
	names := make([]string, 0, len(toolCalls))
	for _, call := range toolCalls {
		if name := strings.TrimSpace(call.Name); name != "" {
			names = append(names, name)
		}
	}
	return names
}
