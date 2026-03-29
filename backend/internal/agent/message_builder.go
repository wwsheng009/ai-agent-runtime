package agent

import (
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/ai-gateway/ai-agent-runtime/internal/types"
)

// ToolResultPayload 是写入 tool_result 前的标准化负载。
type ToolResultPayload struct {
	ToolCallID string
	Content    string
	Metadata   types.Metadata
}

// MessageBuilder 负责构建并修复 tool_use/tool_result 历史。
type MessageBuilder struct {
	history []types.Message
}

// NewMessageBuilder 创建一个新的 message builder。
func NewMessageBuilder(history []types.Message) *MessageBuilder {
	builder := &MessageBuilder{
		history: make([]types.Message, 0, len(history)),
	}
	for _, message := range history {
		builder.history = append(builder.history, *message.Clone())
	}
	return builder
}

// Messages 返回已构建的消息历史。
func (b *MessageBuilder) Messages() []types.Message {
	messages := make([]types.Message, len(b.history))
	for index := range b.history {
		messages[index] = *b.history[index].Clone()
	}
	return messages
}

// Add 添加一条原始消息。
func (b *MessageBuilder) Add(message types.Message) {
	b.history = append(b.history, *message.Clone())
}

// AppendAssistantAction 追加 assistant 消息，并修复缺失的 tool ids。
func (b *MessageBuilder) AppendAssistantAction(content string, toolCalls []types.ToolCall) []types.ToolCall {
	normalizedCalls := normalizeToolCalls(toolCalls)
	assistant := types.NewAssistantMessage(content)
	assistant.ToolCalls = normalizedCalls
	b.history = append(b.history, *assistant)
	return normalizedCalls
}

// AppendToolResults 追加一组 tool_result，并自动补齐缺失结果。
func (b *MessageBuilder) AppendToolResults(toolCalls []types.ToolCall, results []ToolResultPayload) {
	normalizedCalls := normalizeToolCalls(toolCalls)
	normalizedResults := alignToolResults(normalizedCalls, results)

	for _, result := range normalizedResults {
		message := types.NewToolMessage(result.ToolCallID, result.Content)
		if len(result.Metadata) > 0 {
			message.Metadata = result.Metadata.Clone()
		}
		b.history = append(b.history, *message)
	}
}

func normalizeToolCalls(toolCalls []types.ToolCall) []types.ToolCall {
	if len(toolCalls) == 0 {
		return nil
	}

	normalized := make([]types.ToolCall, len(toolCalls))
	for index, toolCall := range toolCalls {
		normalized[index] = toolCall
		if strings.TrimSpace(normalized[index].ID) == "" {
			normalized[index].ID = "toolcall_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		}
		if normalized[index].Args == nil {
			normalized[index].Args = map[string]interface{}{}
		}
	}

	return normalized
}

func alignToolResults(toolCalls []types.ToolCall, results []ToolResultPayload) []ToolResultPayload {
	if len(toolCalls) == 0 && len(results) == 0 {
		return nil
	}

	normalized := make([]ToolResultPayload, 0, len(toolCalls))
	used := make([]bool, len(results))
	byID := make(map[string]int, len(results))

	for index, result := range results {
		if strings.TrimSpace(result.ToolCallID) == "" {
			continue
		}
		byID[result.ToolCallID] = index
	}

	for index, toolCall := range toolCalls {
		if resultIndex, ok := byID[toolCall.ID]; ok {
			used[resultIndex] = true
			normalized = append(normalized, normalizeToolResult(toolCall.ID, results[resultIndex]))
			continue
		}

		for candidateIndex := range results {
			if used[candidateIndex] {
				continue
			}
			used[candidateIndex] = true
			normalized = append(normalized, normalizeToolResult(toolCall.ID, results[candidateIndex]))
			goto matched
		}

		normalized = append(normalized, ToolResultPayload{
			ToolCallID: toolCall.ID,
			Content:    fmt.Sprintf("Tool execution failed: missing tool_result for call %s", toolCall.ID),
			Metadata: types.Metadata{
				"auto_repaired": true,
				"missing":       true,
				"tool_name":     toolCall.Name,
				"tool_index":    index,
			},
		})
	matched:
	}

	return normalized
}

func normalizeToolResult(toolCallID string, result ToolResultPayload) ToolResultPayload {
	normalized := ToolResultPayload{
		ToolCallID: toolCallID,
		Content:    strings.TrimSpace(result.Content),
		Metadata:   result.Metadata.Clone(),
	}
	if normalized.Metadata == nil {
		normalized.Metadata = types.NewMetadata()
	}
	if normalized.Content == "" {
		normalized.Content = fmt.Sprintf("Tool execution finished with no inline result for call %s", toolCallID)
		normalized.Metadata["auto_repaired"] = true
		normalized.Metadata["empty"] = true
	}
	return normalized
}
