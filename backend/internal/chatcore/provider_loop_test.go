package chatcore

import (
	"context"
	"reflect"
	"testing"

	"github.com/ai-gateway/ai-agent-runtime/internal/types"
)

type fakeProviderTurnExecutor struct {
	responses []*ProviderTurnResponse
	requests  []ProviderTurnRequest
}

func (f *fakeProviderTurnExecutor) Complete(ctx context.Context, req ProviderTurnRequest) (*ProviderTurnResponse, error) {
	cloned := ProviderTurnRequest{
		Stream: req.Stream,
		Tools:  cloneToolDefinitions(req.Tools),
	}
	if len(req.Messages) > 0 {
		cloned.Messages = cloneMessages(req.Messages)
	}
	f.requests = append(f.requests, cloned)
	if len(f.responses) == 0 {
		return nil, nil
	}
	response := f.responses[0]
	f.responses = f.responses[1:]
	return response, nil
}

type fakeToolExecutor struct {
	results map[string]ToolResult
	calls   []types.ToolCall
}

func (f *fakeToolExecutor) ExecuteTool(ctx context.Context, call types.ToolCall) ToolResult {
	f.calls = append(f.calls, call)
	if result, ok := f.results[call.Name]; ok {
		return result
	}
	return ToolResult{Error: "missing fake tool result"}
}

func TestExecuteToolLoop_ReplaysToolCallsUntilFinalAssistantMessage(t *testing.T) {
	provider := &fakeProviderTurnExecutor{
		responses: []*ProviderTurnResponse{
			{
				Message: &types.Message{
					Role:    "assistant",
					Content: "Need a tool.",
					ToolCalls: []types.ToolCall{
						{
							ID:   "call_1",
							Name: "read_file",
							Args: map[string]interface{}{"path": "README.md"},
						},
					},
				},
			},
			{
				Message: types.NewAssistantMessage("Final answer"),
			},
		},
	}
	tools := &fakeToolExecutor{
		results: map[string]ToolResult{
			"read_file": {
				Content: "README contents",
			},
		},
	}

	result, err := ExecuteToolLoop(context.Background(), ToolLoopRequest{
		Prompt:   "Summarize the repo",
		History:  []types.Message{*types.NewSystemMessage("You are helpful.")},
		Provider: provider,
		Tools: []types.ToolDefinition{
			{
				Name:        "read_file",
				Description: "Read a file",
				Parameters: map[string]interface{}{
					"type": "object",
				},
			},
		},
		ToolExecutor: tools,
	})
	if err != nil {
		t.Fatalf("ExecuteToolLoop failed: %v", err)
	}
	if result == nil || result.Response == nil {
		t.Fatal("expected loop result and response")
	}
	if result.Response.Output != "Final answer" {
		t.Fatalf("unexpected final output: %+v", result.Response)
	}
	if len(result.Response.ToolExecutions) != 1 {
		t.Fatalf("expected one tool execution, got %+v", result.Response.ToolExecutions)
	}
	if !result.Response.ToolExecutions[0].Success || result.Response.ToolExecutions[0].Output != "README contents" {
		t.Fatalf("unexpected tool execution: %+v", result.Response.ToolExecutions[0])
	}
	if len(result.History) != 5 {
		t.Fatalf("expected 5 history messages, got %d: %#v", len(result.History), result.History)
	}
	if result.History[1].Role != "user" || result.History[1].Content != "Summarize the repo" {
		t.Fatalf("expected user prompt in history: %#v", result.History)
	}
	if result.History[2].Role != "assistant" || len(result.History[2].ToolCalls) != 1 {
		t.Fatalf("expected assistant tool-call message in history: %#v", result.History)
	}
	if result.History[3].Role != "tool" || result.History[3].ToolCallID != "call_1" || result.History[3].Content != "README contents" {
		t.Fatalf("expected tool message in history: %#v", result.History)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("expected 2 provider requests, got %d", len(provider.requests))
	}
	if len(provider.requests[1].Messages) != 4 {
		t.Fatalf("expected second provider request to include replayed tool history, got %#v", provider.requests[1].Messages)
	}
	if len(tools.calls) != 1 || tools.calls[0].Name != "read_file" {
		t.Fatalf("unexpected tool calls: %+v", tools.calls)
	}
}

func TestExecuteToolLoop_EmitsToolBatchEvents(t *testing.T) {
	provider := &fakeProviderTurnExecutor{
		responses: []*ProviderTurnResponse{
			{
				Message: &types.Message{
					Role:    "assistant",
					Content: "Need a tool.",
					ToolCalls: []types.ToolCall{
						{
							ID:   "call_1",
							Name: "read_file",
							Args: map[string]interface{}{"path": "README.md"},
						},
					},
				},
			},
			{
				Message: types.NewAssistantMessage("Done"),
			},
		},
	}
	tools := &fakeToolExecutor{
		results: map[string]ToolResult{
			"read_file": {
				Content: "README contents",
			},
		},
	}

	var events []ChatEvent
	_, err := ExecuteToolLoop(context.Background(), ToolLoopRequest{
		Prompt:       "Summarize the repo",
		Provider:     provider,
		ToolExecutor: tools,
		EventSink: func(event ChatEvent) {
			events = append(events, event)
		},
	})
	if err != nil {
		t.Fatalf("ExecuteToolLoop failed: %v", err)
	}

	stages := make([]string, 0, len(events))
	for _, event := range events {
		if event.Type != EventTool {
			continue
		}
		stages = append(stages, event.Stage)
	}
	if want := []string{"batch_start", "tool_result", "batch_end"}; !reflect.DeepEqual(stages, want) {
		t.Fatalf("unexpected tool event stages: got %v want %v (events=%+v)", stages, want, events)
	}
}
