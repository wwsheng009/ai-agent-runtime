package chatcore

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// ProviderTurnRequest describes the normalized provider request for one assistant turn.
type ProviderTurnRequest struct {
	Messages  []types.Message        `json:"messages,omitempty"`
	Tools     []types.ToolDefinition `json:"tools,omitempty"`
	Stream    bool                   `json:"stream,omitempty"`
	EventSink func(ChatEvent)        `json:"-"`
}

// ProviderTurnResponse describes the normalized provider response for one assistant turn.
type ProviderTurnResponse struct {
	Message *types.Message    `json:"message,omitempty"`
	Usage   *types.TokenUsage `json:"usage,omitempty"`
}

// ProviderTurnExecutor is the minimal provider surface required by the shared replay loop.
type ProviderTurnExecutor interface {
	Complete(ctx context.Context, req ProviderTurnRequest) (*ProviderTurnResponse, error)
}

// ToolExecutor is the minimal tool surface required by the shared replay loop.
type ToolExecutor interface {
	ExecuteTool(ctx context.Context, call types.ToolCall) ToolResult
}

// ToolResult captures the normalized output of one tool invocation.
type ToolResult struct {
	Content  string                 `json:"content,omitempty"`
	Error    string                 `json:"error,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// ToolLoopRequest describes the prepared inputs required to replay provider tool calls.
type ToolLoopRequest struct {
	Prompt       string
	History      []types.Message
	Provider     ProviderTurnExecutor
	Tools        []types.ToolDefinition
	ToolExecutor ToolExecutor
	EventSink    func(ChatEvent)
	Stream       bool
}

// ToolLoopResult returns the final assistant response plus the replayed history.
type ToolLoopResult struct {
	Response *ChatResult
	History  []types.Message
}

// ExecuteToolLoop replays provider tool calls until the provider returns a final assistant message.
func ExecuteToolLoop(ctx context.Context, req ToolLoopRequest) (*ToolLoopResult, error) {
	if req.Provider == nil {
		return nil, fmt.Errorf("provider is required")
	}

	history := cloneMessages(req.History)
	if req.Prompt != "" {
		history = append(history, *types.NewUserMessage(req.Prompt))
	}

	response := NewChatResult()

	for {
		turn, err := req.Provider.Complete(ctx, ProviderTurnRequest{
			Messages:  cloneMessages(history),
			Tools:     cloneToolDefinitions(req.Tools),
			Stream:    req.Stream,
			EventSink: req.EventSink,
		})
		if err != nil {
			return nil, err
		}
		if turn == nil || turn.Message == nil {
			return nil, fmt.Errorf("provider returned no message")
		}

		assistantMessage := cloneMessage(turn.Message)
		history = append(history, *assistantMessage)

		if len(assistantMessage.ToolCalls) == 0 {
			response.Output = assistantMessage.Content
			response.Usage = cloneUsage(turn.Usage)
			return &ToolLoopResult{
				Response: response,
				History:  cloneMessages(history),
			}, nil
		}
		if req.ToolExecutor == nil {
			return nil, fmt.Errorf("tool executor is required when provider requests tools")
		}

		emitChatEvent(req.EventSink, ChatEvent{
			Type:  EventTool,
			Stage: "batch_start",
			Metadata: map[string]interface{}{
				"call_count": len(assistantMessage.ToolCalls),
			},
		})

		successCount := 0
		errorCount := 0
		for _, call := range assistantMessage.ToolCalls {
			toolResult := req.ToolExecutor.ExecuteTool(ctx, call)
			execution := ToolExecutionSummary{
				ToolCallID: call.ID,
				ToolName:   call.Name,
				Output:     strings.TrimSpace(toolResult.Content),
				Error:      strings.TrimSpace(toolResult.Error),
				Success:    strings.TrimSpace(toolResult.Error) == "",
			}
			response.ToolExecutions = append(response.ToolExecutions, execution)
			if execution.Success {
				successCount++
			} else {
				errorCount++
			}

			history = append(history, *types.NewToolMessage(call.ID, renderToolMessage(toolResult)))

			emitChatEvent(req.EventSink, ChatEvent{
				Type:       EventTool,
				Stage:      "tool_result",
				ToolName:   call.Name,
				ToolCallID: call.ID,
				Arguments:  cloneInterfaceMap(call.Args),
				Output:     execution.Output,
				Error:      execution.Error,
				Success:    execution.Success,
			})
		}

		emitChatEvent(req.EventSink, ChatEvent{
			Type:  EventTool,
			Stage: "batch_end",
			Metadata: map[string]interface{}{
				"success_count": successCount,
				"error_count":   errorCount,
			},
		})
	}
}

func emitChatEvent(sink func(ChatEvent), event ChatEvent) {
	if sink != nil {
		sink(event)
	}
}

func renderToolMessage(result ToolResult) string {
	if content := strings.TrimSpace(result.Content); content != "" {
		return content
	}
	if errText := strings.TrimSpace(result.Error); errText != "" {
		return "Tool execution failed: " + errText
	}
	return ""
}

func cloneMessage(message *types.Message) *types.Message {
	if message == nil {
		return nil
	}

	cloned := &types.Message{
		Role:       message.Role,
		Content:    message.Content,
		ToolCallID: message.ToolCallID,
		Metadata:   message.Metadata.Clone(),
	}
	if len(message.ToolCalls) > 0 {
		cloned.ToolCalls = make([]types.ToolCall, len(message.ToolCalls))
		for index, call := range message.ToolCalls {
			cloned.ToolCalls[index] = types.ToolCall{
				ID:   call.ID,
				Name: call.Name,
				Args: cloneInterfaceMap(call.Args),
			}
		}
	}

	return cloned
}

func cloneMessages(input []types.Message) []types.Message {
	if len(input) == 0 {
		return nil
	}

	cloned := make([]types.Message, len(input))
	for index := range input {
		cloned[index] = *cloneMessage(&input[index])
	}

	return cloned
}

func cloneToolDefinitions(input []types.ToolDefinition) []types.ToolDefinition {
	if len(input) == 0 {
		return nil
	}

	cloned := make([]types.ToolDefinition, len(input))
	for index, tool := range input {
		cloned[index] = types.ToolDefinition{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  cloneInterfaceMap(tool.Parameters),
			Metadata:    cloneInterfaceMap(tool.Metadata),
		}
	}

	return cloned
}

func cloneInterfaceMap(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}

	cloned := make(map[string]interface{}, len(input))
	for key, value := range input {
		cloned[key] = cloneInterfaceValue(value)
	}

	return cloned
}

func cloneInterfaceValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneInterfaceMap(typed)
	case []interface{}:
		cloned := make([]interface{}, len(typed))
		for index, item := range typed {
			cloned[index] = cloneInterfaceValue(item)
		}
		return cloned
	default:
		return typed
	}
}

func cloneUsage(input *types.TokenUsage) *types.TokenUsage {
	if input == nil {
		return nil
	}
	cloned := *input
	return &cloned
}
