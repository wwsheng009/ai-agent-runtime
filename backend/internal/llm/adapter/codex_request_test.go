package adapter

import (
	"strings"
	"testing"
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

func TestCodexBuildRequest_PreservesNativeImageGenerationToolAndDisablesParallelToolCalls(t *testing.T) {
	a := &CodexAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:    "gpt-5.4",
		Messages: []map[string]interface{}{{"role": "user", "content": "generate an image"}},
		Functions: []map[string]interface{}{
			{
				"type":          "image_generation",
				"output_format": "png",
			},
			{
				"type":        "function",
				"name":        "bash",
				"description": "run shell",
				"parameters": map[string]interface{}{
					"type": "object",
				},
			},
		},
	})

	if req["parallel_tool_calls"] != false {
		t.Fatalf("expected parallel_tool_calls=false, got %#v", req["parallel_tool_calls"])
	}

	tools, ok := req["tools"].([]map[string]interface{})
	if !ok {
		t.Fatalf("expected []map[string]interface{} tools, got %T", req["tools"])
	}

	var sawNative bool
	for _, tool := range tools {
		if tool["type"] == "image_generation" {
			sawNative = true
			if tool["output_format"] != "png" {
				t.Fatalf("expected output_format png, got %#v", tool["output_format"])
			}
		}
	}
	if !sawNative {
		t.Fatalf("expected native image_generation tool, got %#v", tools)
	}
}

func TestCodexBuildRequest_SortsMergedToolsByName(t *testing.T) {
	a := &CodexAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:    "gpt-5.2",
		Messages: []map[string]interface{}{{"role": "user", "content": "hello"}},
		Functions: []map[string]interface{}{
			{
				"type":        "function",
				"name":        "write",
				"description": "write file",
				"parameters": map[string]interface{}{
					"type": "object",
				},
			},
			{
				"type":        "function",
				"name":        "bash",
				"description": "run shell",
				"parameters": map[string]interface{}{
					"type": "object",
				},
			},
			{
				"type":        "function",
				"name":        "edit",
				"description": "edit file",
				"parameters": map[string]interface{}{
					"type": "object",
				},
			},
		},
	})

	tools, ok := req["tools"].([]map[string]interface{})
	if !ok || len(tools) != 4 {
		t.Fatalf("expected 4 tools, got %T %v", req["tools"], req["tools"])
	}

	got := make([]string, 0, len(tools))
	for _, tool := range tools {
		name, _ := tool["name"].(string)
		got = append(got, name)
	}
	if joined := strings.Join(got, ","); joined != "bash,edit,list_mcp_resources,write" {
		t.Fatalf("expected stable merged tool order, got %q", joined)
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
	if req["store"] != false {
		t.Fatalf("expected store=false, got %v", req["store"])
	}
}

func TestCodexBuildRequest_OmitsMaxOutputTokensWhenMetadataDisablesIt(t *testing.T) {
	a := &CodexAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:    "gpt-5.4",
		Messages: []map[string]interface{}{{"role": "user", "content": "hello"}},
		Metadata: map[string]interface{}{
			"supports_max_output_tokens": false,
		},
	})

	if _, exists := req["max_output_tokens"]; exists {
		t.Fatalf("did not expect max_output_tokens when metadata disables it: %#v", req["max_output_tokens"])
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

func TestCodexBuildRequest_PreservesStructuredUserInputParts(t *testing.T) {
	a := &CodexAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model: "gpt-5.2-codex",
		Messages: []map[string]interface{}{
			{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type": "input_text",
						"text": "look at this image",
					},
					{
						"type":      "input_image",
						"image_url": "data:image/png;base64,ZmFrZQ==",
					},
				},
			},
		},
	})

	input := req["input"].([]map[string]interface{})
	if len(input) != 1 {
		t.Fatalf("expected one input item, got %#v", input)
	}
	parts, ok := input[0]["content"].([]map[string]interface{})
	if !ok {
		t.Fatalf("expected structured content parts, got %T %#v", input[0]["content"], input[0]["content"])
	}
	if len(parts) != 2 {
		t.Fatalf("expected two structured content parts, got %#v", parts)
	}
	if parts[0]["type"] != "input_text" || parts[1]["type"] != "input_image" {
		t.Fatalf("unexpected structured parts: %#v", parts)
	}
	if parts[1]["image_url"] != "data:image/png;base64,ZmFrZQ==" {
		t.Fatalf("expected image data URL to be preserved, got %#v", parts[1]["image_url"])
	}
}

func TestCodexBuildRequest_AddsReasoningConfig(t *testing.T) {
	a := &CodexAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:           "gpt-5.2",
		Messages:        []map[string]interface{}{{"role": "user", "content": "test"}},
		MaxTokens:       2000,
		ReasoningEffort: "high",
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
	include, ok := req["include"].([]string)
	if !ok {
		rawInclude, ok := req["include"].([]interface{})
		if !ok {
			t.Fatalf("expected include list, got %T", req["include"])
		}
		include = make([]string, 0, len(rawInclude))
		for _, item := range rawInclude {
			include = append(include, item.(string))
		}
	}
	if len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Fatalf("expected reasoning.encrypted_content include, got %v", include)
	}
}

func TestCodexBuildRequest_OmitsReasoningConfigWithoutExplicitEffort(t *testing.T) {
	a := &CodexAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:    "gpt-5.2",
		Messages: []map[string]interface{}{{"role": "user", "content": "test"}},
	})

	if _, exists := req["reasoning"]; exists {
		t.Fatalf("did not expect reasoning config without explicit effort: %#v", req["reasoning"])
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

func TestCodexBuildRequest_UsesSessionMetadataForPromptCache(t *testing.T) {
	a := &CodexAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:    "gpt-5.2-codex",
		Messages: []map[string]interface{}{{"role": "user", "content": "test"}},
		Metadata: map[string]interface{}{
			"session_id": "session-123",
		},
	})

	if req["prompt_cache_key"] != "session-123" {
		t.Fatalf("expected prompt_cache_key=session-123, got %#v", req["prompt_cache_key"])
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

	msg, err := a.HandleResponse(true, strings.NewReader(sseData), StreamCallbacks{})
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

func TestCodexHandleResponse_StreamPreservesAllSparseIndexedToolCalls(t *testing.T) {
	a := &CodexAdapter{}
	sseData := strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","response":{"id":"resp_sparse","model":"gpt-5.4-mini"}}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","summary":[]}}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"message","role":"assistant","content":[]}}`,
		"",
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","output_index":1,"delta":"我继续查看剩余改动。"}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","call_id":"call_1","name":"execute_shell_command","arguments":""}}`,
		"",
		"event: response.output_item.done",
		`data: {"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","call_id":"call_1","name":"execute_shell_command","arguments":"{\"command\":\"echo 1\"}"}}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":3,"item":{"type":"function_call","call_id":"call_2","name":"execute_shell_command","arguments":""}}`,
		"",
		"event: response.output_item.done",
		`data: {"type":"response.output_item.done","output_index":3,"item":{"type":"function_call","call_id":"call_2","name":"execute_shell_command","arguments":"{\"command\":\"echo 2\"}"}}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":4,"item":{"type":"function_call","call_id":"call_3","name":"execute_shell_command","arguments":""}}`,
		"",
		"event: response.output_item.done",
		`data: {"type":"response.output_item.done","output_index":4,"item":{"type":"function_call","call_id":"call_3","name":"execute_shell_command","arguments":"{\"command\":\"echo 3\"}"}}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":5,"item":{"type":"function_call","call_id":"call_4","name":"execute_shell_command","arguments":""}}`,
		"",
		"event: response.output_item.done",
		`data: {"type":"response.output_item.done","output_index":5,"item":{"type":"function_call","call_id":"call_4","name":"execute_shell_command","arguments":"{\"command\":\"echo 4\"}"}}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":6,"item":{"type":"function_call","call_id":"call_5","name":"execute_shell_command","arguments":""}}`,
		"",
		"event: response.output_item.done",
		`data: {"type":"response.output_item.done","output_index":6,"item":{"type":"function_call","call_id":"call_5","name":"execute_shell_command","arguments":"{\"command\":\"echo 5\"}"}}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_sparse","status":"completed","stop_reason":"end_turn"}}`,
		"",
	}, "\n")

	msg, err := a.HandleResponse(true, strings.NewReader(sseData), StreamCallbacks{})
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	toolCalls, ok := msg["tool_calls"].([]map[string]interface{})
	if !ok || len(toolCalls) != 5 {
		t.Fatalf("expected 5 tool calls, got %T %#v", msg["tool_calls"], msg["tool_calls"])
	}
	if toolCalls[4]["id"] != "call_5" {
		t.Fatalf("expected last sparse-indexed tool call to be preserved, got %#v", toolCalls[4])
	}
	if toolCalls[4]["arguments"] != `{"command":"echo 5"}` {
		t.Fatalf("unexpected last tool arguments: %#v", toolCalls[4])
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

	msg, err := a.HandleResponse(true, strings.NewReader(sseData), StreamCallbacks{})
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

	msg, err := a.HandleResponse(false, strings.NewReader(jsonData), StreamCallbacks{})
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

func TestCodexHandleResponse_NonStreamPreservesReasoningOutputItems(t *testing.T) {
	a := &CodexAdapter{}
	jsonData := `{
		"id":"resp_1",
		"model":"gpt-5.2-codex",
		"stop_reason":"end_turn",
		"output":[
			{"type":"reasoning","summary":[{"type":"summary_text","text":"Thinking"}],"encrypted_content":"-"},
			{"type":"message","content":[{"type":"output_text","text":"Hello"}],"role":"assistant"}
		]
	}`

	msg, err := a.HandleResponse(false, strings.NewReader(jsonData), StreamCallbacks{})
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	outputItems, ok := msg["response_output_items"].([]map[string]interface{})
	if !ok || len(outputItems) != 2 {
		t.Fatalf("expected 2 response_output_items, got %T %#v", msg["response_output_items"], msg["response_output_items"])
	}
	if outputItems[0]["type"] != "reasoning" {
		t.Fatalf("expected first output item reasoning, got %#v", outputItems[0])
	}
	if outputItems[0]["encrypted_content"] != "-" {
		t.Fatalf("expected encrypted_content to be preserved, got %#v", outputItems[0]["encrypted_content"])
	}
}

func TestCodexHandleResponse_StreamPreservesReasoningOutputItems(t *testing.T) {
	a := &CodexAdapter{}
	sseData := strings.Join([]string{
		"event: response.created",
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.2-codex\"}}",
		"",
		"event: response.reasoning_summary_part.added",
		"data: {\"type\":\"response.reasoning_summary_part.added\",\"summary_index\":0}",
		"",
		"event: response.reasoning_summary_text.delta",
		"data: {\"type\":\"response.reasoning_summary_text.delta\",\"summary_index\":0,\"delta\":\"Thinking\"}",
		"",
		"event: response.output_item.added",
		"data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"reasoning\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"Thinking\"}],\"encrypted_content\":\"-\"}}",
		"",
		"event: response.output_item.done",
		"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"reasoning\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"Thinking\"}],\"encrypted_content\":\"-\"}}",
		"",
		"event: response.output_item.added",
		"data: {\"type\":\"response.output_item.added\",\"output_index\":1,\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}",
		"",
		"event: response.output_text.delta",
		"data: {\"type\":\"response.output_text.delta\",\"output_index\":1,\"delta\":\"Hello\"}",
		"",
		"event: response.output_item.done",
		"data: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hello\"}]}}",
		"",
		"event: response.completed",
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"stop_reason\":\"end_turn\"}}",
		"",
	}, "\n")

	msg, err := a.HandleResponse(true, strings.NewReader(sseData), StreamCallbacks{})
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	outputItems, ok := msg["response_output_items"].([]map[string]interface{})
	if !ok || len(outputItems) != 2 {
		t.Fatalf("expected 2 response_output_items, got %T %#v", msg["response_output_items"], msg["response_output_items"])
	}
	if outputItems[0]["type"] != "reasoning" {
		t.Fatalf("expected first output item reasoning, got %#v", outputItems[0])
	}
	if outputItems[0]["encrypted_content"] != "-" {
		t.Fatalf("expected encrypted_content to be preserved, got %#v", outputItems[0]["encrypted_content"])
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

	msg, err := a.HandleResponse(false, strings.NewReader(jsonData), StreamCallbacks{})
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

	msg, err := a.HandleResponse(false, strings.NewReader(sseData), StreamCallbacks{})
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	if msg["content"] != "Hello" {
		t.Fatalf("expected SSE fallback content Hello, got %#v", msg["content"])
	}
}

func TestCodexHandleResponse_StreamReturnsErrorOnFailedResponse(t *testing.T) {
	a := &CodexAdapter{}
	sseData := strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.2-codex"}}`,
		"",
		"event: error",
		`data: {"type":"error","code":"internal_server_error","message":"connection reset by peer"}`,
		"",
		"event: response.failed",
		`data: {"status":"failed","error":{"message":"no available resource: no available key/provider"}}`,
		"",
	}, "\n")

	msg, err := a.handleCodexStreamResponse(strings.NewReader(sseData), StreamCallbacks{})
	if err == nil {
		t.Fatal("expected stream failure error")
	}
	if !strings.Contains(err.Error(), "codex response failed") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "no available resource: no available key/provider") {
		t.Fatalf("expected provider failure in error, got %v", err)
	}
	if got, _ := msg["finish_reason"].(string); got != "failed" {
		t.Fatalf("expected finish_reason failed, got %#v", msg["finish_reason"])
	}
	if got, _ := msg["error"].(string); !strings.Contains(got, "no available resource: no available key/provider") {
		t.Fatalf("expected error field to contain provider failure, got %#v", msg["error"])
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
	view := findToolByName(t, tools, "view")
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
	todosTool := findToolByName(t, tools, "todos")
	params := todosTool["parameters"].(map[string]interface{})
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
	skillTool := findToolByName(t, tools, "skill__alpha")
	params := skillTool["parameters"].(map[string]interface{})
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

func TestCodexBuildAssistantMessage_PreservesReasoningDetails(t *testing.T) {
	a := &CodexAdapter{}
	msg := a.BuildAssistantMessage(
		"Hello",
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

	details, ok := msg["reasoning_details"].(map[string]interface{})
	if !ok || len(details) == 0 {
		t.Fatalf("expected reasoning_details, got %T %#v", msg["reasoning_details"], msg["reasoning_details"])
	}
	if details["summary"] != "**Checking git status**" {
		t.Fatalf("expected reasoning summary, got %#v", details["summary"])
	}
	meta, ok := details["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected reasoning metadata, got %T", details["metadata"])
	}
	outputItems, ok := meta["response_output_items"].([]map[string]interface{})
	if !ok || len(outputItems) != 3 {
		t.Fatalf("expected 3 response_output_items, got %T %#v", meta["response_output_items"], meta["response_output_items"])
	}
	if outputItems[0]["type"] != "reasoning" {
		t.Fatalf("expected first response output item reasoning, got %#v", outputItems[0])
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

func findToolByName(t *testing.T, tools []map[string]interface{}, name string) map[string]interface{} {
	t.Helper()
	for _, tool := range tools {
		toolName, _ := tool["name"].(string)
		if toolName == name {
			return tool
		}
	}
	t.Fatalf("expected tool %q in %#v", name, tools)
	return nil
}
