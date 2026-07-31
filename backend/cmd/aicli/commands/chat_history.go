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
	// Durable history stores a frozen environment-aware prefix without turn-
	// volatile goal guidance. Goal text is injected as a frozen turn-context
	// message via agent Options["active_goal_guidance"], never via SystemPrompt.
	prompt := strings.TrimSpace(composeDurableChatSystemPromptWithGuidance(session))
	if prompt == "" {
		return
	}
	systemMessage := *runtimetypes.NewSystemMessage(prompt)
	if len(session.Messages) == 0 {
		replaceRuntimeMessages(session, []runtimetypes.Message{systemMessage})
		return
	}
	if strings.EqualFold(strings.TrimSpace(session.Messages[0].Role), "system") {
		// Content-equality short-circuit: never rewrite historical prefix when
		// the composed system prompt is unchanged (session-frozen environment
		// snapshot makes environment churn a no-op across multi-turn sends).
		if strings.TrimSpace(session.Messages[0].Content) == prompt {
			return
		}
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
	session.Messages = append(session.Messages, *message.Clone())
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
	// History is already-final content. Settle any ClearPrompt layout debt
	// (pendingScrollDown / blank-row flag) BEFORE the first content write so
	// live surface compensation is not attached to transcript replay.
	settleInteractiveOutputLayout(session)
	// Replay is a pure content-plane operation: the replay renderer routes user
	// echo through RenderReplayedUserInput, which never restores the composer, so
	// replaying already-final history cannot grow the bottom reserve or bill
	// surface scroll compensation into the transcript. The caller re-shows the
	// prompt once after replay completes.
	renderer := newAICLIReplayTranscriptRenderer(session)
	if strings.TrimSpace(header) != "" {
		renderer.RenderSupplement(fmt.Sprintf("%s (%d 条消息):", strings.TrimSpace(header), len(messages)))
	}
	toolCalls := indexChatHistoryToolCalls(messages)
	for index := range messages {
		renderVisibleChatHistoryMessage(renderer, messages[index], toolCalls)
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

	// Hide both durable and outbound system prefixes. Goal guidance is injected as
	// turn-context messages (not system text); older sessions may still store
	// outbound-with-goal system text.
	hiddenSystemPrompt := strings.TrimSpace(composeDurableChatSystemPromptWithGuidance(session))
	outboundSystemPrompt := strings.TrimSpace(composeChatSystemPromptWithGuidance(session))
	rawSystemPrompt := strings.TrimSpace(session.SystemPromptText)
	messages := make([]runtimetypes.Message, 0, len(session.Messages))
	for _, message := range session.Messages {
		if !isVisibleChatHistoryMessage(session, message, hiddenSystemPrompt, rawSystemPrompt) {
			continue
		}
		if outboundSystemPrompt != "" &&
			strings.EqualFold(strings.TrimSpace(message.Role), "system") &&
			strings.TrimSpace(message.Content) == outboundSystemPrompt {
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
	// Frozen turn-context snapshots (fact/recall/goal/todo/etc.) are prompt-only
	// infrastructure and must not pollute the user-visible transcript.
	if message.Metadata.GetBool("context_snapshot", false) ||
		strings.TrimSpace(message.Metadata.GetString("context_stage", "")) != "" {
		return false
	}

	role := strings.ToLower(strings.TrimSpace(message.Role))
	content := strings.TrimSpace(message.Content)
	// Legacy defense: older sessions may have fact ledgers as assistant text with
	// stripped metadata. Hide the well-known ledger header so "继续" replays stay clean.
	if isLegacyFactLedgerTranscript(content) {
		return false
	}
	switch role {
	case "system":
		if content == "" || (hiddenSystemPrompt != "" && content == hiddenSystemPrompt) || (rawSystemPrompt != "" && content == rawSystemPrompt) {
			return false
		}
	case "developer":
		// Developer messages are prompt infrastructure, never user-visible chat.
		return false
	case "assistant":
		return content != "" || len(message.ToolCalls) > 0 || (chatReasoningOutputEnabled(session) && finalReasoningBlock(&message) != nil)
	case "tool":
		return content != "" || strings.TrimSpace(chatHistoryToolError(message)) != ""
	default:
		return content != ""
	}
	return true
}

const legacyFactLedgerHeader = "Verified fact ledger (authoritative over compacted prose):"

func isLegacyFactLedgerTranscript(content string) bool {
	trimmed := strings.TrimSpace(content)
	return strings.HasPrefix(trimmed, legacyFactLedgerHeader)
}

func renderVisibleChatHistoryMessage(renderer *aicliTranscriptRenderer, message runtimetypes.Message, toolCalls map[string]runtimetypes.ToolCall) {
	if renderer == nil {
		return
	}
	role := strings.ToLower(strings.TrimSpace(message.Role))
	content := message.Content
	switch role {
	case "assistant":
		renderer.RenderReasoning(finalReasoningBlock(&message))
		renderer.RenderAssistant(content)
		// P5.6: Running is viewport-only (ActiveBand). History/replay only
		// emits the final Completed cell once the matching tool message
		// arrives — never a Running row in scrollback.
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
