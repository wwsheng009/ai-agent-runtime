package agent

import (
	"context"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type toolBatchContextKey struct{}

// ToolBatchContext captures the full assistant tool batch plus completed tool results
// before the currently executing tool call. It is used to persist crash-safe recovery
// state when a tool pauses for approval or user input mid-batch.
type ToolBatchContext struct {
	ToolCalls             []types.ToolCall
	CurrentToolCallID     string
	CompletedToolMessages []types.Message
}

// WithToolBatchContext annotates ctx with the current assistant tool batch.
func WithToolBatchContext(ctx context.Context, toolCalls []types.ToolCall, currentToolCallID string, completed []types.Message) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	payload := ToolBatchContext{
		CurrentToolCallID: currentToolCallID,
	}
	if len(toolCalls) > 0 {
		payload.ToolCalls = make([]types.ToolCall, len(toolCalls))
		for index := range toolCalls {
			payload.ToolCalls[index] = types.ToolCall{
				ID:   toolCalls[index].ID,
				Name: toolCalls[index].Name,
				Args: cloneInterfaceMap(toolCalls[index].Args),
			}
		}
	}
	if len(completed) > 0 {
		payload.CompletedToolMessages = make([]types.Message, len(completed))
		for index := range completed {
			payload.CompletedToolMessages[index] = *completed[index].Clone()
		}
	}
	return context.WithValue(ctx, toolBatchContextKey{}, payload)
}

// ToolBatchContextFromContext returns the tool batch metadata, if present.
func ToolBatchContextFromContext(ctx context.Context) (ToolBatchContext, bool) {
	if ctx == nil {
		return ToolBatchContext{}, false
	}
	payload, ok := ctx.Value(toolBatchContextKey{}).(ToolBatchContext)
	if !ok {
		return ToolBatchContext{}, false
	}
	cloned := ToolBatchContext{
		CurrentToolCallID: payload.CurrentToolCallID,
	}
	if len(payload.ToolCalls) > 0 {
		cloned.ToolCalls = make([]types.ToolCall, len(payload.ToolCalls))
		for index := range payload.ToolCalls {
			cloned.ToolCalls[index] = types.ToolCall{
				ID:   payload.ToolCalls[index].ID,
				Name: payload.ToolCalls[index].Name,
				Args: cloneInterfaceMap(payload.ToolCalls[index].Args),
			}
		}
	}
	if len(payload.CompletedToolMessages) > 0 {
		cloned.CompletedToolMessages = make([]types.Message, len(payload.CompletedToolMessages))
		for index := range payload.CompletedToolMessages {
			cloned.CompletedToolMessages[index] = *payload.CompletedToolMessages[index].Clone()
		}
	}
	return cloned, true
}
