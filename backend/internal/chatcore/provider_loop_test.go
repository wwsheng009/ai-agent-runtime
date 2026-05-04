package chatcore

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type fakeProviderTurnExecutor struct {
	responses []*ProviderTurnResponse
	requests  []ProviderTurnRequest
}

func (f *fakeProviderTurnExecutor) Complete(ctx context.Context, req ProviderTurnRequest) (*ProviderTurnResponse, error) {
	cloned := ProviderTurnRequest{
		Stream:   req.Stream,
		Tools:    cloneToolDefinitions(req.Tools),
		Metadata: cloneInterfaceMap(req.Metadata),
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

func TestExecuteToolLoop_PropagatesRequestMetadataToProvider(t *testing.T) {
	provider := &fakeProviderTurnExecutor{
		responses: []*ProviderTurnResponse{
			{
				Message: types.NewAssistantMessage("Final answer"),
			},
		},
	}

	_, err := ExecuteToolLoop(context.Background(), ToolLoopRequest{
		Prompt:   "Summarize the repo",
		History:  []types.Message{*types.NewSystemMessage("You are helpful.")},
		Metadata: map[string]interface{}{"skill_exposure": map[string]interface{}{"mode": "prefer"}},
		Provider: provider,
	})
	if err != nil {
		t.Fatalf("ExecuteToolLoop failed: %v", err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("expected 1 provider request, got %d", len(provider.requests))
	}
	if provider.requests[0].Metadata == nil {
		t.Fatal("expected metadata to be propagated to provider")
	}
	exposure, ok := provider.requests[0].Metadata["skill_exposure"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected skill_exposure metadata map, got %#v", provider.requests[0].Metadata["skill_exposure"])
	}
	if got := exposure["mode"]; got != "prefer" {
		t.Fatalf("expected propagated exposure mode prefer, got %#v", got)
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
	var toolResultMetadata map[string]interface{}
	for _, event := range events {
		if event.Type != EventTool {
			continue
		}
		stages = append(stages, event.Stage)
		if event.Stage == "tool_result" {
			toolResultMetadata = event.Metadata
		}
	}
	if want := []string{"batch_start", "tool_requested", "tool_result", "batch_end"}; !reflect.DeepEqual(stages, want) {
		t.Fatalf("unexpected tool event stages: got %v want %v (events=%+v)", stages, want, events)
	}
	if toolResultMetadata != nil {
		t.Fatalf("expected empty metadata by default, got %+v", toolResultMetadata)
	}
}

func TestExecuteToolLoop_PropagatesToolResultMetadataToEvents(t *testing.T) {
	provider := &fakeProviderTurnExecutor{
		responses: []*ProviderTurnResponse{
			{
				Message: &types.Message{
					Role: "assistant",
					ToolCalls: []types.ToolCall{
						{ID: "call_1", Name: "remote_search", Args: map[string]interface{}{"query": "golang"}},
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
			"remote_search": {
				Content: "result 1\nresult 2\nresult 3",
				Metadata: map[string]interface{}{
					"tool_source": "mcp",
				},
			},
		},
	}

	var events []ChatEvent
	_, err := ExecuteToolLoop(context.Background(), ToolLoopRequest{
		Prompt:   "Search",
		Provider: provider,
		Tools: []types.ToolDefinition{
			{
				Name:        "remote_search",
				Description: "Search remotely",
				Parameters:  map[string]interface{}{"type": "object"},
				Metadata: map[string]interface{}{
					"tool_source": "mcp",
				},
			},
		},
		ToolExecutor: tools,
		EventSink: func(event ChatEvent) {
			events = append(events, event)
		},
	})
	if err != nil {
		t.Fatalf("ExecuteToolLoop failed: %v", err)
	}

	for _, event := range events {
		if event.Type == EventTool && event.Stage == "tool_requested" {
			if got := event.Metadata["tool_source"]; got != "mcp" {
				t.Fatalf("expected tool_requested metadata tool_source=mcp, got %#v", got)
			}
		}
		if event.Type == EventTool && event.Stage == "tool_result" {
			if got := event.Metadata["tool_source"]; got != "mcp" {
				t.Fatalf("expected tool_result metadata tool_source=mcp, got %#v", got)
			}
			return
		}
	}
	t.Fatal("expected tool_result event")
}

func TestExecuteToolLoop_PreservesToolResultMetadataInHistory(t *testing.T) {
	provider := &fakeProviderTurnExecutor{
		responses: []*ProviderTurnResponse{
			{
				Message: &types.Message{
					Role: "assistant",
					ToolCalls: []types.ToolCall{
						{ID: "call_1", Name: "background_task", Args: map[string]interface{}{"command": "git status"}},
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
			"background_task": {
				Content: "job_id=job-1\nstatus=queued",
				Metadata: map[string]interface{}{
					"tool_source": "broker",
					"output_kind": "text",
				},
			},
		},
	}

	result, err := ExecuteToolLoop(context.Background(), ToolLoopRequest{
		Prompt:       "Run git status later",
		Provider:     provider,
		ToolExecutor: tools,
	})
	if err != nil {
		t.Fatalf("ExecuteToolLoop failed: %v", err)
	}
	if result == nil {
		t.Fatal("expected result")
	}
	if len(result.History) < 3 {
		t.Fatalf("expected replayed history, got %#v", result.History)
	}
	toolMessage := result.History[len(result.History)-2]
	if toolMessage.Role != "tool" || toolMessage.ToolCallID != "call_1" {
		t.Fatalf("expected tool message before final assistant message, got %#v", toolMessage)
	}
	if got := toolMessage.Metadata["tool_source"]; got != "broker" {
		t.Fatalf("expected tool_source=broker in tool message metadata, got %#v", got)
	}
	if got := toolMessage.Metadata["output_kind"]; got != "text" {
		t.Fatalf("expected output_kind=text in tool message metadata, got %#v", got)
	}
	if len(result.Response.ToolExecutions) != 1 {
		t.Fatalf("expected one tool execution, got %+v", result.Response.ToolExecutions)
	}
	if got := result.Response.ToolExecutions[0].Metadata["tool_source"]; got != "broker" {
		t.Fatalf("expected tool execution metadata tool_source=broker, got %#v", got)
	}
}

func TestExecuteToolLoop_UsesPromptOnlyActiveTurnCompactionWhenToolLoopExpands(t *testing.T) {
	large := strings.Repeat("abcdefghijklmnopqrstuvwxyz0123456789", 450)
	provider := &fakeProviderTurnExecutor{
		responses: []*ProviderTurnResponse{
			{
				Message: &types.Message{
					Role: "assistant",
					ToolCalls: []types.ToolCall{
						{ID: "call_1", Name: "view", Args: map[string]interface{}{"file_path": "README.md"}},
					},
				},
			},
			{
				Message: &types.Message{
					Role: "assistant",
					ToolCalls: []types.ToolCall{
						{ID: "call_2", Name: "view", Args: map[string]interface{}{"file_path": "AGENTS.md"}},
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
			"view": {
				Content: "AGENTS " + large,
			},
		},
	}

	result, err := ExecuteToolLoop(context.Background(), ToolLoopRequest{
		Prompt:       "继续分析当前实现",
		Provider:     provider,
		ToolExecutor: tools,
	})
	if err != nil {
		t.Fatalf("ExecuteToolLoop failed: %v", err)
	}
	if result == nil || len(result.History) < 5 {
		t.Fatalf("expected replayed history, got %#v", result)
	}
	for _, message := range result.History {
		if message.Metadata.GetBool("active_turn_compaction", false) {
			t.Fatalf("did not expect active turn compaction to appear in final canonical history, got %#v", result.History)
		}
	}
	if result.History[2].Role != "tool" || result.History[2].ToolCallID != "call_1" {
		t.Fatalf("expected first raw tool result to remain in final history, got %#v", result.History)
	}
	if !strings.Contains(result.History[2].Content, "AGENTS ") {
		t.Fatalf("expected first raw tool content to remain in final history, got %q", result.History[2].Content)
	}
	if result.History[4].Role != "tool" || result.History[4].ToolCallID != "call_2" {
		t.Fatalf("expected second raw tool result to remain in final history, got %#v", result.History)
	}
	if len(provider.requests) != 3 {
		t.Fatalf("expected 3 provider requests, got %d", len(provider.requests))
	}
	var compactedInReplay bool
	for _, message := range provider.requests[2].Messages {
		if message.Metadata.GetBool("active_turn_compaction", false) {
			compactedInReplay = true
			break
		}
	}
	if !compactedInReplay {
		t.Fatalf("expected third provider request to include active turn compaction, got %#v", provider.requests[2].Messages)
	}
}

func TestExecuteToolLoop_UsesPromptOnlyActiveTurnCompactionWhenTokenBudgetExceeded(t *testing.T) {
	large := strings.Repeat("abcdefghijklmnopqrstuvwxyz0123456789", 40)
	provider := &fakeProviderTurnExecutor{
		responses: []*ProviderTurnResponse{
			{
				Message: &types.Message{
					Role: "assistant",
					ToolCalls: []types.ToolCall{
						{ID: "call_1", Name: "view", Args: map[string]interface{}{"file_path": "README.md"}},
					},
				},
			},
			{
				Message: &types.Message{
					Role: "assistant",
					ToolCalls: []types.ToolCall{
						{ID: "call_2", Name: "view", Args: map[string]interface{}{"file_path": "AGENTS.md"}},
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
			"view": {
				Content: "AGENTS " + large,
			},
		},
	}

	result, err := ExecuteToolLoop(context.Background(), ToolLoopRequest{
		Prompt:              "继续分析当前实现",
		Provider:            provider,
		ToolExecutor:        tools,
		ActiveTurnMaxBytes:  64 * 1024,
		ActiveTurnMaxTokens: 500,
		CountTokens: func(messages []types.Message) int {
			total := 0
			for _, message := range messages {
				total += len(message.Content) / 4
			}
			return total
		},
	})
	if err != nil {
		t.Fatalf("ExecuteToolLoop failed: %v", err)
	}
	if result == nil || len(result.History) < 5 {
		t.Fatalf("expected replayed history, got %#v", result)
	}
	for _, message := range result.History {
		if message.Metadata.GetBool("active_turn_compaction", false) {
			t.Fatalf("did not expect active turn compaction to appear in final canonical history, got %#v", result.History)
		}
	}
	if len(provider.requests) != 3 {
		t.Fatalf("expected 3 provider requests, got %d", len(provider.requests))
	}
	foundCompaction := false
	for _, message := range provider.requests[2].Messages {
		if message.Metadata.GetBool("active_turn_compaction", false) {
			foundCompaction = true
			if got := message.Metadata.GetString("active_turn_compaction_reason", ""); !strings.Contains(got, "tokens") {
				t.Fatalf("expected token compaction reason, got %#v", got)
			}
			break
		}
	}
	if !foundCompaction {
		t.Fatalf("expected third provider request to include prompt-only active turn compaction, got %#v", provider.requests[2].Messages)
	}
}

func TestExecuteToolLoop_FailsPromptPreflightBeforeProviderWhenHistoryCannotBeCompacted(t *testing.T) {
	provider := &fakeProviderTurnExecutor{}
	prompt := strings.Repeat("0123456789", 80)

	_, err := ExecuteToolLoop(context.Background(), ToolLoopRequest{
		Prompt:              prompt,
		History:             []types.Message{*types.NewSystemMessage("You are helpful.")},
		Provider:            provider,
		ActiveTurnMaxTokens: 40,
		CountTokens: func(messages []types.Message) int {
			total := 0
			for _, message := range messages {
				total += len(message.Content) / 4
			}
			return total
		},
		PromptBudgetSource: "model_capability_auto_compact_token_limit",
		ResolvedProvider:   "test-provider",
		ResolvedModel:      "test-model",
	})
	if err == nil {
		t.Fatal("expected prompt preflight failure")
	}
	preflightErr, ok := agent.AsPromptPreflightError(err)
	if !ok || preflightErr == nil {
		t.Fatalf("expected PromptPreflightError, got %v", err)
	}
	if preflightErr.Code != "active_turn_not_compactable" {
		t.Fatalf("expected active_turn_not_compactable, got %+v", preflightErr)
	}
	if preflightErr.PromptBudget != 40 {
		t.Fatalf("expected prompt budget 40, got %+v", preflightErr)
	}
	if preflightErr.ResolvedProvider != "test-provider" || preflightErr.ResolvedModel != "test-model" {
		t.Fatalf("expected provider/model metadata, got %+v", preflightErr)
	}
	if replacement := preflightErr.CloneReplacementHistory(); len(replacement) != 0 {
		t.Fatalf("expected no replacement history when replay cannot be compacted, got %#v", replacement)
	}
	if len(provider.requests) != 0 {
		t.Fatalf("expected no provider requests before preflight failure, got %d", len(provider.requests))
	}
}

func TestExecuteToolLoop_FailsPromptPreflightAfterCompactionStillExceedsBudget(t *testing.T) {
	large := strings.Repeat("abcdefghijklmnopqrstuvwxyz0123456789", 40)
	provider := &fakeProviderTurnExecutor{
		responses: []*ProviderTurnResponse{
			{
				Message: &types.Message{
					Role: "assistant",
					ToolCalls: []types.ToolCall{
						{ID: "call_1", Name: "view", Args: map[string]interface{}{"file_path": "README.md"}},
					},
				},
			},
			{
				Message: &types.Message{
					Role: "assistant",
					ToolCalls: []types.ToolCall{
						{ID: "call_2", Name: "view", Args: map[string]interface{}{"file_path": "AGENTS.md"}},
					},
				},
			},
		},
	}
	tools := &fakeToolExecutor{
		results: map[string]ToolResult{
			"view": {
				Content: "AGENTS " + large,
			},
		},
	}

	_, err := ExecuteToolLoop(context.Background(), ToolLoopRequest{
		Prompt:              "继续分析当前实现",
		Provider:            provider,
		ToolExecutor:        tools,
		ActiveTurnMaxBytes:  64 * 1024,
		ActiveTurnMaxTokens: 380,
		CountTokens: func(messages []types.Message) int {
			total := 0
			for _, message := range messages {
				total += len(message.Content) / 4
			}
			return total
		},
		PromptBudgetSource: "model_capability_auto_compact_ratio",
		ResolvedModel:      "test-model",
	})
	if err == nil {
		t.Fatal("expected prompt preflight failure after compaction")
	}
	preflightErr, ok := agent.AsPromptPreflightError(err)
	if !ok || preflightErr == nil {
		t.Fatalf("expected PromptPreflightError, got %v", err)
	}
	if preflightErr.Code != "prompt_still_exceeds_budget_after_compaction" {
		t.Fatalf("expected prompt_still_exceeds_budget_after_compaction, got %+v", preflightErr)
	}
	if !preflightErr.ActiveTurnCompacted {
		t.Fatalf("expected active turn to be marked compacted, got %+v", preflightErr)
	}
	replacement := preflightErr.CloneReplacementHistory()
	if len(replacement) != 0 {
		t.Fatalf("expected no replacement history for prompt-only active-turn compaction failure, got %#v", replacement)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("expected preflight to stop before third provider call, got %d requests", len(provider.requests))
	}
}

func TestExecuteToolLoop_UsesWholeHistoryCompactorToContinueAfterPromptPreflight(t *testing.T) {
	large := strings.Repeat("abcdefghijklmnopqrstuvwxyz0123456789", 40)
	provider := &fakeProviderTurnExecutor{
		responses: []*ProviderTurnResponse{
			{
				Message: &types.Message{
					Role: "assistant",
					ToolCalls: []types.ToolCall{
						{ID: "call_1", Name: "view", Args: map[string]interface{}{"file_path": "README.md"}},
					},
				},
			},
			{
				Message: &types.Message{
					Role: "assistant",
					ToolCalls: []types.ToolCall{
						{ID: "call_2", Name: "view", Args: map[string]interface{}{"file_path": "AGENTS.md"}},
					},
				},
			},
			{
				Message: types.NewAssistantMessage("Final answer after whole-history compaction"),
			},
		},
	}
	tools := &fakeToolExecutor{
		results: map[string]ToolResult{
			"view": {
				Content: "AGENTS " + large,
			},
		},
	}

	compactedHistory := []types.Message{
		*types.NewUserMessage("继续分析当前实现"),
		*types.NewUserMessage("Compacted context from earlier turns:\n- 已完成多轮文件查看\n- 继续输出最终结论"),
	}
	compactedHistory[1].Metadata["context_stage"] = "compaction"
	compactedHistory[1].Metadata["compact_mode"] = "local"

	compactCalls := 0
	result, err := ExecuteToolLoop(context.Background(), ToolLoopRequest{
		Prompt:              "继续分析当前实现",
		Provider:            provider,
		ToolExecutor:        tools,
		ActiveTurnMaxBytes:  64 * 1024,
		ActiveTurnMaxTokens: 380,
		CountTokens: func(messages []types.Message) int {
			total := 0
			for _, message := range messages {
				total += len(message.Content) / 4
			}
			return total
		},
		HistoryCompactor: func(ctx context.Context, history []types.Message) ([]types.Message, bool, error) {
			compactCalls++
			if len(history) == 0 {
				t.Fatal("expected oversized history to be passed into whole-history compactor")
			}
			return cloneMessages(compactedHistory), true, nil
		},
	})
	if err != nil {
		t.Fatalf("ExecuteToolLoop failed: %v", err)
	}
	if result == nil || result.Response == nil {
		t.Fatal("expected loop result after whole-history compaction")
	}
	if result.Response.Output != "Final answer after whole-history compaction" {
		t.Fatalf("unexpected final output: %+v", result.Response)
	}
	if compactCalls != 1 {
		t.Fatalf("expected whole-history compactor to run once, got %d", compactCalls)
	}
	if len(provider.requests) != 3 {
		t.Fatalf("expected third provider request after compaction, got %d requests", len(provider.requests))
	}
	if len(provider.requests[2].Messages) != len(compactedHistory) {
		t.Fatalf("expected third provider request to use compacted history, got %#v", provider.requests[2].Messages)
	}
	if stage := provider.requests[2].Messages[1].Metadata.GetString("context_stage", ""); stage != "compaction" {
		t.Fatalf("expected compaction marker in third provider request, got %#v", provider.requests[2].Messages)
	}
	if len(result.History) != len(compactedHistory)+1 {
		t.Fatalf("expected final history to keep compacted replacement plus final answer, got %#v", result.History)
	}
	if result.History[len(result.History)-1].Content != "Final answer after whole-history compaction" {
		t.Fatalf("unexpected final history: %#v", result.History)
	}
}

func TestExecuteToolLoop_AutoAttachesPromptImagesToHistory(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "diagram.png")
	writeChatcoreTinyPNG(t, imagePath)

	provider := &fakeProviderTurnExecutor{
		responses: []*ProviderTurnResponse{{
			Message: types.NewAssistantMessage("done"),
		}},
	}

	result, err := ExecuteToolLoop(context.Background(), ToolLoopRequest{
		Prompt:   "请分析这张图 " + imagePath,
		Provider: provider,
	})
	if err != nil {
		t.Fatalf("ExecuteToolLoop failed: %v", err)
	}
	if len(result.History) != 2 {
		t.Fatalf("expected user + assistant history, got %#v", result.History)
	}
	if !llm.MessageHasLocalInputImages(&result.History[0]) {
		t.Fatalf("expected first history message to include local input image metadata, got %+v", result.History[0].Metadata)
	}
	if len(provider.requests) != 1 || !llm.MessageHasLocalInputImages(&provider.requests[0].Messages[0]) {
		t.Fatalf("expected provider request to preserve local image metadata, got %#v", provider.requests)
	}
}

func TestExecuteToolLoop_ExplicitImagePathsAreAttachedToHistory(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "photo.png")
	writeChatcoreTinyPNG(t, imagePath)

	provider := &fakeProviderTurnExecutor{
		responses: []*ProviderTurnResponse{{
			Message: types.NewAssistantMessage("I see the image"),
		}},
	}

	result, err := ExecuteToolLoop(context.Background(), ToolLoopRequest{
		Prompt:             "请查看附件",
		ExplicitImagePaths: []string{imagePath},
		Provider:           provider,
	})
	if err != nil {
		t.Fatalf("ExecuteToolLoop failed: %v", err)
	}
	if len(result.History) != 2 {
		t.Fatalf("expected user + assistant history, got %#v", result.History)
	}
	if !llm.MessageHasLocalInputImages(&result.History[0]) {
		t.Fatalf("expected first history message to include local input image metadata, got %+v", result.History[0].Metadata)
	}
	images := llm.ExtractLocalInputImages(map[string]interface{}(result.History[0].Metadata))
	if len(images) == 0 {
		t.Fatal("expected at least one image in extracted metadata")
	}
	if images[0].Source != "explicit" {
		t.Fatalf("expected source=explicit, got %q", images[0].Source)
	}
}

func TestExecuteToolLoop_InvalidExplicitImagePathReturnsError(t *testing.T) {
	provider := &fakeProviderTurnExecutor{
		responses: []*ProviderTurnResponse{{
			Message: types.NewAssistantMessage("done"),
		}},
	}

	_, err := ExecuteToolLoop(context.Background(), ToolLoopRequest{
		Prompt:             "请查看附件",
		ExplicitImagePaths: []string{"/nonexistent/image.png"},
		Provider:           provider,
	})
	if err == nil {
		t.Fatal("expected error for invalid explicit image path")
	}
}

func writeChatcoreTinyPNG(t *testing.T, path string) {
	t.Helper()
	payload, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVQIHWP4////fwAJ+wP+qC1oAAAAAElFTkSuQmCC")
	if err != nil {
		t.Fatalf("decode tiny png: %v", err)
	}
	if err := os.WriteFile(path, payload, 0644); err != nil {
		t.Fatalf("write tiny png: %v", err)
	}
}
