package adapter

import (
	"strings"
	"testing"

	anthropictypes "github.com/ai-gateway/ai-agent-runtime/internal/types/anthropic"
)

func TestCodexBuildRequest_AddsToolChoice(t *testing.T) {
	a := &CodexAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:     "gpt-5.2",
		Messages:  []map[string]interface{}{},
		Stream:    true,
		MaxTokens: 2000,
		Functions: []map[string]interface{}{
			{
				"type":        "function",
				"name":        "bash",
				"description": "execute shell",
				"parameters": map[string]interface{}{
					"type": "object",
				},
				"strict": true,
			},
		},
	})

	if _, ok := req["tool_choice"]; !ok {
		t.Fatalf("expected tool_choice to be set")
	}
	if req["tool_choice"] != "auto" {
		t.Fatalf("expected tool_choice auto, got %v", req["tool_choice"])
	}

	tools, ok := req["tools"].([]map[string]interface{})
	if !ok || len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %T %v", req["tools"], req["tools"])
	}
	if tools[0]["name"] != "bash" {
		t.Fatalf("expected first tool bash, got %v", tools[0]["name"])
	}
	if tools[1]["name"] != "list_mcp_resources" {
		t.Fatalf("expected second tool list_mcp_resources, got %v", tools[1]["name"])
	}
	params, ok := tools[1]["parameters"].(map[string]interface{})
	if !ok || params == nil {
		t.Fatalf("expected parameters map, got %T", tools[1]["parameters"])
	}
	if params["additionalProperties"] != false {
		t.Fatalf("expected additionalProperties=false, got %v", params["additionalProperties"])
	}
	required, ok := params["required"].([]string)
	if !ok {
		rawRequired, ok := params["required"].([]interface{})
		if !ok {
			t.Fatalf("expected required list on MCP meta tool, got %T", params["required"])
		}
		required = make([]string, 0, len(rawRequired))
		for _, item := range rawRequired {
			required = append(required, item.(string))
		}
	}
	if len(required) != 2 {
		t.Fatalf("expected both MCP params to be required for codex, got %v", required)
	}
}

func TestCodexBuildRequest_UsesConfiguredStreamFlag(t *testing.T) {
	a := &CodexAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:    "gpt-5.2",
		Messages: []map[string]interface{}{{"role": "user", "content": "hello"}},
		Stream:   false,
	})

	if req["stream"] != false {
		t.Fatalf("expected stream=false, got %v", req["stream"])
	}
}

func TestCodexBuildRequest_MovesSystemMessagesToInstructions(t *testing.T) {
	a := &CodexAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model: "gpt-5.2-codex",
		Messages: []map[string]interface{}{
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": "hello"},
		},
		Stream: false,
	})

	if req["instructions"] != "You are a helpful assistant." {
		t.Fatalf("expected instructions to contain system prompt, got %#v", req["instructions"])
	}
	input := req["input"].([]map[string]interface{})
	if len(input) != 1 {
		t.Fatalf("expected only user input item after system extraction, got %d: %#v", len(input), input)
	}
	if input[0]["role"] != "user" {
		t.Fatalf("expected remaining input role user, got %#v", input[0]["role"])
	}
}

func TestCodexBuildRequest_MergesSystemAndDeveloperMessagesIntoInstructions(t *testing.T) {
	a := &CodexAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model: "gpt-5.2-codex",
		Messages: []map[string]interface{}{
			{"role": "system", "content": "System guardrails"},
			{"role": "developer", "content": "Developer guidance"},
			{"role": "user", "content": "hello"},
		},
		Stream: false,
	})

	if req["instructions"] != "System guardrails\n\nDeveloper guidance" {
		t.Fatalf("unexpected merged instructions: %#v", req["instructions"])
	}
	input := req["input"].([]map[string]interface{})
	if len(input) != 1 || input[0]["role"] != "user" {
		t.Fatalf("expected only user input item, got %#v", input)
	}
}

func TestCodexBuildRequest_AddsReasoningConfig(t *testing.T) {
	a := &CodexAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:           "gpt-5.2",
		Messages:        []map[string]interface{}{{"role": "user", "content": "test"}},
		MaxTokens:       2000,
		ReasoningEffort: " HIGH ",
	})

	reasoning, ok := req["reasoning"].(map[string]interface{})
	if !ok || reasoning == nil {
		t.Fatalf("expected reasoning config, got %T", req["reasoning"])
	}
	if reasoning["effort"] != "high" {
		t.Fatalf("expected reasoning effort high, got %v", reasoning["effort"])
	}
	if reasoning["summary"] != "auto" {
		t.Fatalf("expected reasoning summary auto, got %v", reasoning["summary"])
	}
}

func TestCodexBuildRequest_OmitsReasoningConfigWhenInvalid(t *testing.T) {
	a := &CodexAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:           "gpt-5.2",
		Messages:        []map[string]interface{}{{"role": "user", "content": "test"}},
		ReasoningEffort: "fast",
	})

	if _, exists := req["reasoning"]; exists {
		t.Fatalf("did not expect reasoning config for invalid effort: %#v", req["reasoning"])
	}
}

func TestCodexBuildRequest_DerivesReasoningFromAnthropicThinking(t *testing.T) {
	a := &CodexAdapter{}
	budget := 32000

	req := a.BuildRequest(RequestConfig{
		Model:    "claude-sonnet-4-6",
		Messages: []map[string]interface{}{{"role": "user", "content": "test"}},
		Thinking: &anthropictypes.Thinking{
			Type:         "enabled",
			BudgetTokens: &budget,
		},
	})

	reasoning, ok := req["reasoning"].(map[string]interface{})
	if !ok || reasoning == nil {
		t.Fatalf("expected reasoning config, got %T", req["reasoning"])
	}
	if reasoning["effort"] != "xhigh" {
		t.Fatalf("expected derived reasoning effort xhigh, got %v", reasoning["effort"])
	}
}

func TestCodexHandleResponse_StreamWithOutputIndexToolCall(t *testing.T) {
	a := &CodexAdapter{}
	sseData := strings.Join([]string{
		"event: response.created",
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.2-codex\"}}",
		"",
		"event: response.output_item.added",
		"data: {\"type\":\"response.output_item.added\",\"output_index\":1,\"item\":{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"list_mcp_resources\",\"arguments\":\"\"}}",
		"",
		"event: response.function_call_arguments.delta",
		"data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":1,\"delta\":\"{}\"}",
		"",
		"event: response.output_item.done",
		"data: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"list_mcp_resources\",\"arguments\":\"{}\"}}",
		"",
		"event: response.completed",
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"stop_reason\":\"end_turn\"}}",
		"",
	}, "\n")

	msg, err := a.HandleResponse(true, strings.NewReader(sseData), nil)
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	toolCalls, ok := msg["tool_calls"].([]map[string]interface{})
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %T %#v", msg["tool_calls"], msg["tool_calls"])
	}
	if toolCalls[0]["name"] != "list_mcp_resources" {
		t.Fatalf("unexpected tool name: %#v", toolCalls[0])
	}
	if toolCalls[0]["arguments"] != "{}" {
		t.Fatalf("unexpected tool arguments: %#v", toolCalls[0])
	}
}

func TestCodexHandleResponse_StreamReturnsStandardAssistantMessage(t *testing.T) {
	a := &CodexAdapter{}
	sseData := strings.Join([]string{
		"event: response.created",
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.2-codex\"}}",
		"",
		"event: response.output_item.added",
		"data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}",
		"",
		"event: response.output_text.delta",
		"data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"Hello\"}",
		"",
		"event: response.completed",
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"stop_reason\":\"end_turn\"}}",
		"",
	}, "\n")

	msg, err := a.HandleResponse(true, strings.NewReader(sseData), nil)
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	if msg["role"] != "assistant" {
		t.Fatalf("expected assistant role, got %#v", msg["role"])
	}
	if msg["content"] != "Hello" {
		t.Fatalf("expected content Hello, got %#v", msg["content"])
	}
	if _, exists := msg["reasoning_content"]; exists && msg["reasoning_content"] != "" {
		t.Fatalf("did not expect reasoning_content, got %#v", msg["reasoning_content"])
	}
}

func TestCodexHandleResponse_NonStreamReturnsStandardAssistantMessage(t *testing.T) {
	a := &CodexAdapter{}
	jsonData := `{
		"id":"resp_1",
		"model":"gpt-5.2-codex",
		"stop_reason":"end_turn",
		"output":[
			{"type":"reasoning","summary":[{"type":"summary_text","text":"Thinking"}]},
			{"type":"message","content":[{"type":"output_text","text":"Hello"}],"role":"assistant"}
		]
	}`

	msg, err := a.HandleResponse(false, strings.NewReader(jsonData), nil)
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	if msg["content"] != "Hello" {
		t.Fatalf("expected content Hello, got %#v", msg["content"])
	}
	if msg["reasoning_content"] != "Thinking" {
		t.Fatalf("expected reasoning_content Thinking, got %#v", msg["reasoning_content"])
	}
}

func TestCodexHandleResponse_NonStreamFunctionCallOnlyReturnsToolCall(t *testing.T) {
	a := &CodexAdapter{}
	jsonData := `{
		"id":"resp_1",
		"model":"gpt-5.2-codex",
		"stop_reason":"tool_call",
		"output":[
			{
				"type":"function_call",
				"call_id":"call_1",
				"name":"spawn_team",
				"arguments":"{\"teammates\":[{\"name\":\"executor\"}],\"tasks\":[{\"title\":\"task-1\",\"goal\":\"run the task\"}]}"
			}
		]
	}`

	msg, err := a.HandleResponse(false, strings.NewReader(jsonData), nil)
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	if msg["content"] != "" {
		t.Fatalf("expected empty content, got %#v", msg["content"])
	}
	toolCalls, ok := msg["tool_calls"].([]map[string]interface{})
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %T %#v", msg["tool_calls"], msg["tool_calls"])
	}
	if toolCalls[0]["name"] != "spawn_team" {
		t.Fatalf("unexpected tool name: %#v", toolCalls[0])
	}
	if _, ok := toolCalls[0]["arguments"].(string); !ok {
		t.Fatalf("expected string arguments, got %#v", toolCalls[0]["arguments"])
	}
}

func TestCodexHandleResponse_NonStreamAcceptsSSEPayload(t *testing.T) {
	a := &CodexAdapter{}
	sseData := strings.Join([]string{
		"event: response.created",
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.2-codex\"}}",
		"",
		"event: response.output_item.added",
		"data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}",
		"",
		"event: response.output_text.delta",
		"data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"Hello\"}",
		"",
		"event: response.completed",
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"stop_reason\":\"end_turn\"}}",
		"",
	}, "\n")

	msg, err := a.HandleResponse(false, strings.NewReader(sseData), nil)
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	if msg["content"] != "Hello" {
		t.Fatalf("expected SSE fallback content Hello, got %#v", msg["content"])
	}
}

func TestCodexBuildRequest_SanitizesOptionalPropertiesToNullableRequired(t *testing.T) {
	a := &CodexAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:    "gpt-5.2",
		Messages: []map[string]interface{}{{"role": "user", "content": "test"}},
		Functions: []map[string]interface{}{
			{
				"type":        "function",
				"name":        "view",
				"description": "查看文件内容",
				"parameters": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file_path": map[string]interface{}{"type": "string"},
						"offset":    map[string]interface{}{"type": "integer"},
						"limit":     map[string]interface{}{"type": "integer"},
					},
					"required": []string{"file_path"},
				},
				"strict": true,
			},
		},
	})

	tools := req["tools"].([]map[string]interface{})
	view := tools[0]
	params := view["parameters"].(map[string]interface{})
	required := toStringSlice(t, params["required"])
	if len(required) != 3 {
		t.Fatalf("expected all parameters required, got %v", required)
	}
	props := params["properties"].(map[string]interface{})
	offset := props["offset"].(map[string]interface{})
	offsetTypes := toStringSlice(t, offset["type"])
	if len(offsetTypes) != 2 || offsetTypes[0] != "integer" || offsetTypes[1] != "null" {
		t.Fatalf("expected optional integer to become nullable, got %v", offset["type"])
	}
}

func TestCodexBuildRequest_RemovesDefaultsAndSanitizesNestedSchemas(t *testing.T) {
	a := &CodexAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:    "gpt-5.2",
		Messages: []map[string]interface{}{{"role": "user", "content": "test"}},
		Functions: []map[string]interface{}{
			{
				"type":        "function",
				"name":        "todos",
				"description": "todo list",
				"parameters": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"count": map[string]interface{}{
							"type":    "integer",
							"default": 5,
						},
						"todos": map[string]interface{}{
							"type": "array",
							"items": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"status": map[string]interface{}{
										"type": "string",
										"enum": []interface{}{"pending", "completed"},
									},
								},
								"required": []string{"status"},
							},
						},
					},
					"required": []string{"todos"},
				},
			},
		},
	})

	tools := req["tools"].([]map[string]interface{})
	params := tools[0]["parameters"].(map[string]interface{})
	props := params["properties"].(map[string]interface{})

	count := props["count"].(map[string]interface{})
	if _, exists := count["default"]; exists {
		t.Fatalf("expected default to be removed, got %v", count["default"])
	}
	countTypes := toStringSlice(t, count["type"])
	if len(countTypes) != 2 || countTypes[1] != "null" {
		t.Fatalf("expected count to become nullable, got %v", count["type"])
	}

	todos := props["todos"].(map[string]interface{})
	itemSchema := todos["items"].(map[string]interface{})
	if itemSchema["additionalProperties"] != false {
		t.Fatalf("expected nested object additionalProperties=false, got %v", itemSchema["additionalProperties"])
	}
	status := itemSchema["properties"].(map[string]interface{})["status"].(map[string]interface{})
	enumValues, ok := status["enum"].([]interface{})
	if !ok {
		t.Fatalf("expected enum slice, got %T", status["enum"])
	}
	hasNull := false
	for _, value := range enumValues {
		if value == nil {
			hasNull = true
			break
		}
	}
	if hasNull {
		t.Fatalf("did not expect required nested enum to become nullable, got %v", enumValues)
	}
}

func TestCodexBuildRequest_SanitizesSkillRuntimeOpenObjects(t *testing.T) {
	a := &CodexAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:    "gpt-5.2",
		Messages: []map[string]interface{}{{"role": "user", "content": "test"}},
		Functions: []map[string]interface{}{
			{
				"type":        "function",
				"name":        "skill__alpha",
				"description": "skill",
				"parameters": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"prompt":  map[string]interface{}{"type": "string"},
						"context": map[string]interface{}{"type": "object", "description": "optional context"},
						"options": map[string]interface{}{"type": "object", "description": "optional options"},
					},
					"required": []string{"prompt"},
				},
			},
		},
	})

	tools := req["tools"].([]map[string]interface{})
	params := tools[0]["parameters"].(map[string]interface{})
	required := toStringSlice(t, params["required"])
	if len(required) != 3 {
		t.Fatalf("expected all skill params required, got %v", required)
	}
	props := params["properties"].(map[string]interface{})
	context := props["context"].(map[string]interface{})
	contextTypes := toStringSlice(t, context["type"])
	if len(contextTypes) != 2 || contextTypes[0] != "object" || contextTypes[1] != "null" {
		t.Fatalf("expected context object to become nullable, got %v", context["type"])
	}
	if context["additionalProperties"] != false {
		t.Fatalf("expected context additionalProperties=false, got %v", context["additionalProperties"])
	}
}

func TestCodexBuildRequest_FollowUpToolCallUsesOutputItems(t *testing.T) {
	a := &CodexAdapter{}
	assistant := a.BuildAssistantMessage(
		"",
		[]map[string]interface{}{
			{
				"id":   "call_1",
				"type": "function",
				"function": map[string]interface{}{
					"name":      "execute_shell_command",
					"arguments": `{"command":"git status -sb"}`,
				},
			},
		},
		"**Checking git status**",
	)

	req := a.BuildRequest(RequestConfig{
		Model: "gpt-5.2-codex",
		Messages: []map[string]interface{}{
			{"role": "user", "content": "查看git status"},
			assistant,
			{"role": "tool", "tool_call_id": "call_1", "content": "## main"},
		},
		Stream: false,
	})

	input := req["input"].([]map[string]interface{})
	if len(input) != 4 {
		t.Fatalf("expected 4 input items, got %d: %#v", len(input), input)
	}
	if input[0]["type"] != "message" || input[0]["role"] != "user" {
		t.Fatalf("unexpected first input item: %#v", input[0])
	}
	if input[1]["type"] != "reasoning" {
		t.Fatalf("expected reasoning item, got %#v", input[1])
	}
	if _, exists := input[1]["role"]; exists {
		t.Fatalf("reasoning item should not be a message: %#v", input[1])
	}
	if input[2]["type"] != "function_call" || input[2]["name"] != "execute_shell_command" {
		t.Fatalf("unexpected function_call item: %#v", input[2])
	}
	if input[3]["type"] != "function_call_output" || input[3]["call_id"] != "call_1" {
		t.Fatalf("unexpected function_call_output item: %#v", input[3])
	}
}

func toStringSlice(t *testing.T, raw interface{}) []string {
	t.Helper()
	switch typed := raw.(type) {
	case []string:
		return typed
	case []interface{}:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			s, ok := item.(string)
			if !ok {
				t.Fatalf("expected string slice element, got %T", item)
			}
			out = append(out, s)
		}
		return out
	default:
		t.Fatalf("expected string slice, got %T", raw)
		return nil
	}
}
