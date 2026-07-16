package commands

import (
	"fmt"
	"strings"

	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

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
	session.StatusMessageCount = countChatStatusMessages(session.Messages)
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
	session.StatusMessageCount = countChatStatusMessages(session.Messages)
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

func countChatStatusMessages(messages []runtimetypes.Message) int {
	count := 0
	for _, message := range messages {
		role := strings.TrimSpace(message.Role)
		if role != "" && !strings.EqualFold(role, "system") {
			count++
		}
	}
	return count
}

func printVisibleChatHistory(session *ChatSession, header string) int {
	messages := collectVisibleChatHistory(session)
	if len(messages) == 0 {
		return 0
	}
	renderer := newAICLITranscriptRenderer(session)
	if strings.TrimSpace(header) != "" {
		renderer.RenderSupplement(fmt.Sprintf("%s (%d 条消息):", strings.TrimSpace(header), len(messages)))
	}
	toolCalls := indexChatHistoryToolCalls(messages)
	toolMetadata := indexChatHistoryToolMetadata(messages)
	for index := range messages {
		renderVisibleChatHistoryMessage(renderer, messages[index], toolCalls, toolMetadata)
	}
	return len(messages)
}

func hasVisibleChatHistory(session *ChatSession) bool {
	return len(collectVisibleChatHistory(session)) > 0
}

func collectVisibleChatHistory(session *ChatSession) []runtimetypes.Message {
	if session == nil || len(session.Messages) == 0 {
		return nil
	}

	hiddenSystemPrompt := strings.TrimSpace(composeChatSystemPromptWithGuidance(session))
	rawSystemPrompt := strings.TrimSpace(session.SystemPromptText)
	messages := make([]runtimetypes.Message, 0, len(session.Messages))
	for _, message := range session.Messages {
		if !isVisibleChatHistoryMessage(session, message, hiddenSystemPrompt, rawSystemPrompt) {
			continue
		}
		messages = append(messages, *message.Clone())
	}
	return messages
}

func isVisibleChatHistoryMessage(session *ChatSession, message runtimetypes.Message, hiddenSystemPrompt string, rawSystemPrompt string) bool {
	if strings.TrimSpace(message.Role) == "" {
		return false
	}

	role := strings.ToLower(strings.TrimSpace(message.Role))
	content := strings.TrimSpace(message.Content)
	switch role {
	case "system":
		if content == "" || (hiddenSystemPrompt != "" && content == hiddenSystemPrompt) || (rawSystemPrompt != "" && content == rawSystemPrompt) {
			return false
		}
	case "assistant":
		return content != "" || len(message.ToolCalls) > 0 || (chatReasoningOutputEnabled(session) && finalReasoningBlock(&message) != nil)
	case "tool":
		return content != "" || strings.TrimSpace(chatHistoryToolError(message)) != ""
	default:
		return content != ""
	}
	return true
}

func renderVisibleChatHistoryMessage(renderer *aicliTranscriptRenderer, message runtimetypes.Message, toolCalls map[string]runtimetypes.ToolCall, toolMetadata map[string]map[string]interface{}) {
	if renderer == nil {
		return
	}
	role := strings.ToLower(strings.TrimSpace(message.Role))
	content := message.Content
	switch role {
	case "assistant":
		renderer.RenderReasoning(finalReasoningBlock(&message))
		renderer.RenderAssistant(content)
		for _, call := range message.ToolCalls {
			metadata := toolMetadata[strings.TrimSpace(call.ID)]
			renderer.RenderToolEvent(runtimechatcore.ChatEvent{
				Type:       runtimechatcore.EventTool,
				Stage:      "tool_requested",
				ToolName:   call.Name,
				ToolCallID: call.ID,
				Arguments:  cloneFunctionSchema(call.Args),
				Metadata:   cloneFunctionSchema(metadata),
			})
		}
	case "tool":
		call := toolCalls[strings.TrimSpace(message.ToolCallID)]
		toolName := firstNonEmptyChatValue(
			strings.TrimSpace(call.Name),
			chatHistoryToolNameFromMetadata(message.Metadata),
			strings.TrimSpace(message.ToolCallID),
			"tool",
		)
		output, toolErr := splitChatHistoryToolResult(message)
		renderer.RenderToolEvent(runtimechatcore.ChatEvent{
			Type:       runtimechatcore.EventTool,
			Stage:      "tool_result",
			ToolName:   toolName,
			ToolCallID: message.ToolCallID,
			Arguments:  cloneFunctionSchema(call.Args),
			Output:     output,
			Error:      toolErr,
			Success:    strings.TrimSpace(toolErr) == "",
			Metadata:   chatHistoryToolMetadataMap(message.Metadata),
		})
	case "system":
		renderer.RenderSystem(content)
	case "user":
		renderer.RenderUser(content)
	default:
		renderer.RenderSystem(fmt.Sprintf("[%s] %s", role, content))
	}
}

func indexChatHistoryToolCalls(messages []runtimetypes.Message) map[string]runtimetypes.ToolCall {
	indexed := make(map[string]runtimetypes.ToolCall)
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			if callID := strings.TrimSpace(call.ID); callID != "" {
				indexed[callID] = call
			}
		}
	}
	return indexed
}

func indexChatHistoryToolMetadata(messages []runtimetypes.Message) map[string]map[string]interface{} {
	indexed := make(map[string]map[string]interface{})
	for _, message := range messages {
		if !strings.EqualFold(strings.TrimSpace(message.Role), "tool") {
			continue
		}
		if callID := strings.TrimSpace(message.ToolCallID); callID != "" {
			indexed[callID] = chatHistoryToolMetadataMap(message.Metadata)
		}
	}
	return indexed
}

func chatHistoryToolNameFromMetadata(metadata runtimetypes.Metadata) string {
	return payloadStringValue(chatHistoryToolMetadataMap(metadata)["tool_name"])
}

func chatHistoryToolMetadataMap(metadata runtimetypes.Metadata) map[string]interface{} {
	flat := cloneFunctionSchema(map[string]interface{}(metadata))
	if len(flat) == 0 {
		return nil
	}

	var nested map[string]interface{}
	switch value := flat["tool_metadata"].(type) {
	case map[string]interface{}:
		nested = value
	case runtimetypes.Metadata:
		nested = map[string]interface{}(value)
	}
	for _, key := range []string{"tool_name", "tool_source", "tool_error", "error", "shell_type", "shell_path", "shell_display", "workdir", "cwd"} {
		if payloadStringValue(flat[key]) == "" && payloadStringValue(nested[key]) != "" {
			flat[key] = nested[key]
		}
	}
	if intPayloadValue(flat, "duration_ms") <= 0 && intPayloadValue(nested, "duration_ms") > 0 {
		flat["duration_ms"] = intPayloadValue(nested, "duration_ms")
	}
	return flat
}

func chatHistoryToolError(message runtimetypes.Message) string {
	metadata := chatHistoryToolMetadataMap(message.Metadata)
	for _, key := range []string{"tool_error", "error"} {
		if errText := strings.TrimSpace(payloadStringValue(metadata[key])); errText != "" {
			return errText
		}
	}
	const prefix = "Tool execution failed:"
	content := strings.TrimSpace(message.Content)
	if !strings.HasPrefix(content, prefix) {
		return ""
	}
	firstLine := strings.SplitN(strings.TrimSpace(strings.TrimPrefix(content, prefix)), "\n", 2)[0]
	return strings.TrimSpace(firstLine)
}

func splitChatHistoryToolResult(message runtimetypes.Message) (string, string) {
	content := strings.TrimSpace(message.Content)
	toolErr := chatHistoryToolError(message)
	if toolErr == "" {
		return content, ""
	}
	const failurePrefix = "Tool execution failed:"
	if strings.HasPrefix(content, failurePrefix) {
		if newline := strings.IndexByte(content, '\n'); newline >= 0 {
			content = strings.TrimSpace(content[newline+1:])
		} else {
			content = ""
		}
	}
	return content, toolErr
}
