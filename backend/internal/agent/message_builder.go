package agent

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
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
	// identity 是 P1「创建时授予」（session-message-identity-protocol-20261017
	// §3 INV-1）的可选身份源：非 nil 时，本 builder 新创建的 assistant/tool 消息
	// 在构造处一次性铸 message_id/turn_id，不再依赖 persist 时的 substance 反推。
	// nil 时保持历史行为（由 ReplaceHistory 的继承层兜底），零行为变化。
	identity *BuilderIdentity
}

// BuilderIdentity 是一个 run 的身份上下文：turn_id 由 actor 在 run 边界授予，
// 序号在 builder 内单调递增。GrantMessage 把身份写进消息 metadata（已带 id 的
// 消息不覆盖）。
type BuilderIdentity struct {
	TurnID string
	seq    int
}

// NewBuilderIdentity 以 run 边界已知的 turn_id 构造身份源。
func NewBuilderIdentity(turnID string) *BuilderIdentity {
	return &BuilderIdentity{TurnID: strings.TrimSpace(turnID)}
}

// GrantMessage 在创建时授予身份：缺失 message_id 铸 msg_<uuid>；缺失 turn_id
// 盖本 run 的 turn_id。已带 id 的消息保持不变（重放/继承场景）。
func (bi *BuilderIdentity) GrantMessage(message *types.Message) {
	if bi == nil || message == nil {
		return
	}
	bi.seq++
	if types.MessageID(*message) == "" {
		if message.Metadata == nil {
			message.Metadata = types.NewMetadata()
		}
		message.Metadata.Set(types.MetadataKeyMessageID, "msg_"+strings.ReplaceAll(uuid.NewString(), "-", ""))
	}
	if types.TurnID(*message) == "" && bi.TurnID != "" {
		if message.Metadata == nil {
			message.Metadata = types.NewMetadata()
		}
		message.Metadata.Set(types.MetadataKeyTurnID, bi.TurnID)
	}
}

// NewMessageBuilder 创建一个新的 message builder。
func NewMessageBuilder(history []types.Message) *MessageBuilder {
	return NewMessageBuilderWithIdentity(history, nil)
}

// NewMessageBuilderWithIdentity 创建带身份源的 builder（INV-1 接线点）：run 起
// 点传入 NewBuilderIdentity(turnID)，本 run 新建的消息即获得创建时授予的身份。
func NewMessageBuilderWithIdentity(history []types.Message, identity *BuilderIdentity) *MessageBuilder {
	builder := &MessageBuilder{
		history:  make([]types.Message, 0, len(history)),
		identity: identity,
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
func (b *MessageBuilder) AppendAssistantAction(content string, toolCalls []types.ToolCall, reasoning *types.ReasoningBlock, metadata types.Metadata) []types.ToolCall {
	normalizedCalls := normalizeToolCalls(toolCalls)
	assistant := types.NewAssistantMessage(content)
	assistant.ToolCalls = normalizedCalls
	if len(metadata) > 0 {
		assistant.Metadata = metadata.Clone()
	}
	if reasoning != nil {
		types.SetReasoningBlock(assistant.Metadata, reasoning)
	}
	// P1 INV-1：创建时授予身份（identity 为 nil 时保持历史行为）。
	b.identity.GrantMessage(assistant)
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
		// P1 INV-1：创建时授予身份（identity 为 nil 时保持历史行为）。
		b.identity.GrantMessage(message)
		b.history = append(b.history, *message)
	}
	// 工具结果里的图片直通附件必须在整批 tool_result 之后注入 user 消息：
	// provider 协议只在 user 角色渲染 image block。
	b.appendImagePassthroughMessages(normalizedResults)
}

func normalizeToolCalls(toolCalls []types.ToolCall) []types.ToolCall {
	if len(toolCalls) == 0 {
		return nil
	}

	normalized := make([]types.ToolCall, len(toolCalls))
	for index, toolCall := range toolCalls {
		normalized[index] = toolCall
		if strings.TrimSpace(normalized[index].ID) == "" {
			normalized[index].ID = types.DeterministicToolCallID("toolcall_", index, normalized[index].Name, normalized[index].Args)
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
