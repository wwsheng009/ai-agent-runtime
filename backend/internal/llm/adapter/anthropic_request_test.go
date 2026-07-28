package adapter

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	anthropictypes "github.com/wwsheng009/ai-agent-runtime/internal/types/anthropic"
)

func TestAnthropicBuildRequest_MovesInstructionMessagesToSystem(t *testing.T) {
	a := &AnthropicAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model: "claude-3-7-sonnet",
		Messages: []map[string]interface{}{
			{"role": "system", "content": "Base guardrail"},
			{"role": "developer", "content": "Tool guidance"},
			{"role": "user", "content": "hello"},
		},
		Stream: false,
	})

	if req["system"] != "Base guardrail\n\nTool guidance" {
		t.Fatalf("unexpected anthropic system instructions: %#v", req["system"])
	}
	messages, ok := req["messages"].([]map[string]interface{})
	if !ok {
		t.Fatalf("expected anthropic messages array, got %#v", req["messages"])
	}
	if len(messages) != 1 {
		t.Fatalf("expected only user message after extraction, got %#v", messages)
	}
	if messages[0]["role"] != "user" {
		t.Fatalf("expected user role after extraction, got %#v", messages[0]["role"])
	}
}

func TestAnthropicBuildRequest_RepairsAssistantFirstCompactedHistory(t *testing.T) {
	a := &AnthropicAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model: "claude-opus-5",
		Messages: []map[string]interface{}{
			{
				"role": "assistant",
				"content": []interface{}{
					map[string]interface{}{"type": "thinking", "thinking": "inspect the logs"},
					map[string]interface{}{
						"type":  "tool_use",
						"id":    "toolu_1",
						"name":  "shell",
						"input": map[string]interface{}{"command": "Get-ChildItem"},
					},
				},
			},
			{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{
						"type":        "tool_result",
						"tool_use_id": "toolu_1",
						"content":     "ok",
					},
				},
			},
		},
	})

	messages, ok := req["messages"].([]map[string]interface{})
	if !ok {
		t.Fatalf("expected anthropic messages array, got %#v", req["messages"])
	}
	if len(messages) != 3 {
		t.Fatalf("expected user anchor plus retained tool replay, got %#v", messages)
	}
	if messages[0]["role"] != "user" || messages[0]["content"] != anthropicCompactedHistoryUserAnchor {
		t.Fatalf("expected neutral user anchor before assistant-first history, got %#v", messages[0])
	}
	if messages[1]["role"] != "assistant" || messages[2]["role"] != "user" {
		t.Fatalf("expected retained assistant/tool_result adjacency, got %#v", messages)
	}
	resultBlocks, ok := messages[2]["content"].([]interface{})
	if !ok || len(resultBlocks) != 1 {
		t.Fatalf("expected one retained tool_result block, got %#v", messages[2]["content"])
	}
	result, ok := resultBlocks[0].(map[string]interface{})
	if !ok || result["tool_use_id"] != "toolu_1" {
		t.Fatalf("expected tool_result for toolu_1 immediately after tool_use, got %#v", resultBlocks)
	}
}

func TestAnthropicBuildRequest_OmitsEmptySystemField(t *testing.T) {
	a := &AnthropicAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model: "claude-3-7-sonnet",
		Messages: []map[string]interface{}{
			{"role": "user", "content": "hello"},
		},
		Stream: false,
	})

	if _, exists := req["system"]; exists {
		t.Fatalf("did not expect system field, got %#v", req["system"])
	}
}

func TestAnthropicBuildRequest_KeepsTurnContextDeveloperOutOfSystem(t *testing.T) {
	a := &AnthropicAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model: "claude-3-7-sonnet",
		Messages: []map[string]interface{}{
			{"role": "system", "content": "Base guardrail"},
			{"role": "user", "content": "check application logs"},
			{"role": "developer", "content": "Persistent goal.\n\nkeep the prefix stable"},
			{"role": "assistant", "content": "I will inspect logs."},
		},
		Stream: false,
	})

	if req["system"] != "Base guardrail" {
		t.Fatalf("expected only leading system in top-level system, got %#v", req["system"])
	}
	messages, ok := req["messages"].([]map[string]interface{})
	if !ok {
		t.Fatalf("expected anthropic messages array, got %#v", req["messages"])
	}
	if len(messages) != 3 {
		t.Fatalf("expected user + residual goal + assistant, got %#v", messages)
	}
	if messages[0]["role"] != "user" || messages[0]["content"] != "check application logs" {
		t.Fatalf("unexpected first message: %#v", messages[0])
	}
	if messages[1]["role"] != "user" || messages[1]["content"] != "Persistent goal.\n\nkeep the prefix stable" {
		t.Fatalf("expected residual developer goal as user message, got %#v", messages[1])
	}
	if messages[2]["role"] != "assistant" {
		t.Fatalf("expected assistant trailing message, got %#v", messages[2])
	}
}

func TestAnthropicBuildRequest_SetsTemperatureWhenNoThinking(t *testing.T) {
	a := &AnthropicAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:       "claude-sonnet-4-6",
		Messages:    []map[string]interface{}{{"role": "user", "content": "hello"}},
		Temperature: 0.7,
	})

	temp, ok := req["temperature"].(float64)
	if !ok {
		t.Fatalf("expected temperature in request, got %#v", req["temperature"])
	}
	if temp != 0.7 {
		t.Fatalf("expected temperature 0.7, got %v", temp)
	}
}

func TestAnthropicBuildRequest_OmitsTemperatureWhenThinkingEnabled(t *testing.T) {
	a := &AnthropicAdapter{}
	budget := 8192
	req := a.BuildRequest(RequestConfig{
		Model:       "claude-sonnet-4-6",
		Messages:    []map[string]interface{}{{"role": "user", "content": "hello"}},
		Temperature: 0.7,
		Thinking: &anthropictypes.Thinking{
			Type:         "enabled",
			BudgetTokens: &budget,
		},
	})

	if _, exists := req["temperature"]; exists {
		t.Fatalf("expected temperature to be omitted when thinking is enabled, got %#v", req["temperature"])
	}
}

func TestAnthropicBuildRequest_OmitsTemperatureWhenZero(t *testing.T) {
	a := &AnthropicAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:       "claude-sonnet-4-6",
		Messages:    []map[string]interface{}{{"role": "user", "content": "hello"}},
		Temperature: 0,
	})

	if _, exists := req["temperature"]; exists {
		t.Fatalf("expected temperature to be omitted when zero, got %#v", req["temperature"])
	}
}

func TestAnthropicBuildRequest_AdaptiveThinkingGeneratesCorrectBody(t *testing.T) {
	a := &AnthropicAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:           "claude-opus-4-6",
		Messages:        []map[string]interface{}{{"role": "user", "content": "hello"}},
		ReasoningEffort: "high",
		ReasoningEffortBudgets: map[string]int{
			"high": 0, // 0 budget signals adaptive mode
		},
	})

	rawThinking, ok := req["thinking"]
	if !ok {
		t.Fatalf("expected thinking in request")
	}
	thinking, ok := rawThinking.(*anthropictypes.Thinking)
	if !ok {
		t.Fatalf("expected thinking struct, got %T", rawThinking)
	}
	if thinking.Type != "adaptive" {
		t.Fatalf("expected thinking type adaptive, got %q", thinking.Type)
	}
	// Nested effort under thinking.adaptive is rejected by Anthropic gateways.
	// Effort must live only in output_config.
	if thinking.Effort != "" {
		t.Fatalf("expected wire thinking.effort empty for adaptive, got %q", thinking.Effort)
	}
	if thinking.BudgetTokens != nil {
		t.Fatalf("expected wire thinking.budget_tokens nil for adaptive, got %#v", thinking.BudgetTokens)
	}

	// Check output_config
	rawConfig, ok := req["output_config"]
	if !ok {
		t.Fatalf("expected output_config in request for adaptive thinking")
	}
	config, ok := rawConfig.(map[string]interface{})
	if !ok {
		t.Fatalf("expected output_config map, got %T", rawConfig)
	}
	if config["effort"] != "high" {
		t.Fatalf("expected output_config.effort high, got %v", config["effort"])
	}

	// Defense-in-depth: even if a caller still embeds Effort on the struct,
	// JSON must not emit thinking.effort for adaptive.
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	thinkingJSON, ok := decoded["thinking"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected thinking object in JSON, got %#v", decoded["thinking"])
	}
	if _, hasEffort := thinkingJSON["effort"]; hasEffort {
		t.Fatalf("JSON must omit thinking.effort for adaptive, got %#v", thinkingJSON)
	}
	if thinkingJSON["type"] != "adaptive" {
		t.Fatalf("expected JSON thinking.type=adaptive, got %#v", thinkingJSON["type"])
	}
}

func TestAnthropicBuildRequest_ToolChoiceIsPropagated(t *testing.T) {
	a := &AnthropicAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:      "claude-sonnet-4-6",
		Messages:   []map[string]interface{}{{"role": "user", "content": "hello"}},
		ToolChoice: map[string]interface{}{"type": "auto"},
	})

	tc, ok := req["tool_choice"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected tool_choice in request, got %#v", req["tool_choice"])
	}
	if tc["type"] != "auto" {
		t.Fatalf("expected tool_choice type auto, got %v", tc["type"])
	}
}

func TestAnthropicBuildRequest_NormalizesNoneToolChoiceWithoutDroppingTools(t *testing.T) {
	a := &AnthropicAdapter{}
	tools := []map[string]interface{}{{
		"name":         "view",
		"input_schema": map[string]interface{}{"type": "object"},
	}}
	req := a.BuildRequest(RequestConfig{
		Model:      "claude-sonnet-4-6",
		Messages:   []map[string]interface{}{{"role": "user", "content": "summarize"}},
		Functions:  tools,
		ToolChoice: "none",
	})

	if got := req["tools"]; !reflect.DeepEqual(got, tools) {
		t.Fatalf("expected frozen tools to be retained, got %#v", got)
	}
	choice, ok := req["tool_choice"].(map[string]interface{})
	if !ok || choice["type"] != "none" {
		t.Fatalf("expected anthropic tool_choice {type:none}, got %#v", req["tool_choice"])
	}
}

func TestAnthropicBuildAssistantMessage_NormalizesToolUseBlocks(t *testing.T) {
	a := &AnthropicAdapter{}
	msg := a.BuildAssistantMessage("", []map[string]interface{}{
		{
			"type": "tool_use",
			"id":   "call-1",
			"name": "view",
			"input": map[string]interface{}{
				"file_path": "README.md",
			},
		},
	}, "")

	toolCalls, ok := msg["tool_calls"].([]map[string]interface{})
	if !ok {
		t.Fatalf("expected normalized tool_calls slice, got %T", msg["tool_calls"])
	}
	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolCalls))
	}
	fn, _ := toolCalls[0]["function"].(map[string]interface{})
	if fn["name"] != "view" {
		t.Fatalf("unexpected function name: %#v", fn["name"])
	}
	args, _ := fn["arguments"].(string)
	if !strings.Contains(args, `"file_path":"README.md"`) {
		t.Fatalf("expected file_path in normalized arguments, got %q", args)
	}
}

func TestAnthropicBuildAssistantMessage_UnwrapsRawToolUseInput(t *testing.T) {
	a := &AnthropicAdapter{}
	msg := a.BuildAssistantMessage("", []map[string]interface{}{
		{
			"type": "tool_use",
			"id":   "call-raw",
			"name": "edit",
			"input": map[string]interface{}{
				"_raw": `{"file_path":"E:/projects/app/main.go","old_string":"old","new_string":"new"}`,
			},
		},
	}, "")

	toolCalls, ok := msg["tool_calls"].([]map[string]interface{})
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("expected 1 normalized tool call, got %T %#v", msg["tool_calls"], msg["tool_calls"])
	}
	fn, _ := toolCalls[0]["function"].(map[string]interface{})
	argsJSON, _ := fn["arguments"].(string)
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		t.Fatalf("decode arguments: %v", err)
	}
	if got, _ := args["file_path"].(string); got != "E:/projects/app/main.go" {
		t.Fatalf("expected unwrapped file_path, got %q in %s", got, argsJSON)
	}
	if _, exists := args["_raw"]; exists {
		t.Fatalf("did not expect _raw after normalization: %s", argsJSON)
	}
}

func TestAnthropicHandleResponse_StreamUnwrapsRawToolArguments(t *testing.T) {
	a := &AnthropicAdapter{}
	sse := strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"mimo-v2.5-pro","content":[]}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_1","name":"bash","input":{}}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"_raw\":\"{\\\"command\\\":\\\"git status\\\",\\\"workdir\\\":\\\"E:/projects/app\\\"}\"}"}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":0}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")

	msg, err := a.HandleResponse(true, strings.NewReader(sse), StreamCallbacks{})
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	toolCalls, ok := msg["tool_calls"].([]map[string]interface{})
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %T %#v", msg["tool_calls"], msg["tool_calls"])
	}
	fn, _ := toolCalls[0]["function"].(map[string]interface{})
	argsJSON, _ := fn["arguments"].(string)
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		t.Fatalf("decode arguments: %v", err)
	}
	if got, _ := args["command"].(string); got != "git status" {
		t.Fatalf("expected unwrapped command, got %q in %s", got, argsJSON)
	}
	if got, _ := args["workdir"].(string); got != "E:/projects/app" {
		t.Fatalf("expected unwrapped workdir, got %q in %s", got, argsJSON)
	}
}

func TestAnthropicBuildRequest_StopSequencesArePropagated(t *testing.T) {
	a := &AnthropicAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:         "claude-sonnet-4-6",
		Messages:      []map[string]interface{}{{"role": "user", "content": "hello"}},
		StopSequences: []string{"STOP", "END"},
	})

	ss, ok := req["stop_sequences"].([]string)
	if !ok {
		t.Fatalf("expected stop_sequences in request, got %#v", req["stop_sequences"])
	}
	if len(ss) != 2 || ss[0] != "STOP" || ss[1] != "END" {
		t.Fatalf("expected [STOP, END], got %v", ss)
	}
}

func TestAnthropicBuildRequest_StopSequencesFromMetadata(t *testing.T) {
	a := &AnthropicAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:    "claude-sonnet-4-6",
		Messages: []map[string]interface{}{{"role": "user", "content": "hello"}},
		Metadata: map[string]interface{}{
			"stop_sequences": []interface{}{"HALT"},
		},
	})

	ss, ok := req["stop_sequences"].([]interface{})
	if !ok {
		t.Fatalf("expected stop_sequences from metadata, got %#v", req["stop_sequences"])
	}
	if len(ss) != 1 || ss[0] != "HALT" {
		t.Fatalf("expected [HALT], got %v", ss)
	}
}

func TestAnthropicBuildRequest_DefaultMaxTokens16384(t *testing.T) {
	a := &AnthropicAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:    "claude-sonnet-4-6",
		Messages: []map[string]interface{}{{"role": "user", "content": "hello"}},
	})

	if req["max_tokens"] != 16384 {
		t.Fatalf("expected default max_tokens 16384, got %v", req["max_tokens"])
	}
}
