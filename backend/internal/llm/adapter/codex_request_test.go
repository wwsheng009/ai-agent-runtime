package adapter

import (
	"encoding/json"
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
	if !ok || len(tools) != 1 {
		t.Fatalf("expected the caller tool without adapter injection, got %T %v", req["tools"], req["tools"])
	}
	if tools[0]["name"] != "bash" {
		t.Fatalf("expected first tool bash, got %v", tools[0]["name"])
	}
	params, ok := tools[0]["parameters"].(map[string]interface{})
	if !ok || params == nil {
		t.Fatalf("expected parameters map, got %T", tools[0]["parameters"])
	}
	if params["additionalProperties"] != false {
		t.Fatalf("expected additionalProperties=false, got %v", params["additionalProperties"])
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
		Metadata: map[string]interface{}{"parallel_tool_calls": true},
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

func TestCodexBuildRequest_PropagatesParallelToolCalls(t *testing.T) {
	a := &CodexAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:    "gpt-5.4",
		Messages: []map[string]interface{}{{"role": "user", "content": "inspect files"}},
		Functions: []map[string]interface{}{
			{
				"type": "function", "name": "view",
				"parameters": map[string]interface{}{"type": "object"},
			},
		},
		Metadata: map[string]interface{}{"parallel_tool_calls": true},
	})

	if req["parallel_tool_calls"] != true {
		t.Fatalf("expected parallel_tool_calls=true, got %#v", req["parallel_tool_calls"])
	}
}

func TestCodexBuildRequest_PreservesMergedToolOrder(t *testing.T) {
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
	if !ok || len(tools) != 3 {
		t.Fatalf("expected 3 caller tools, got %T %v", req["tools"], req["tools"])
	}

	got := make([]string, 0, len(tools))
	for _, tool := range tools {
		name, _ := tool["name"].(string)
		got = append(got, name)
	}
	if joined := strings.Join(got, ","); joined != "write,bash,edit" {
		t.Fatalf("expected adapter to preserve caller order without injection, got %q", joined)
	}
}

func TestCodexBuildRequest_DoesNotSilentlyDeduplicateCallerTools(t *testing.T) {
	a := &CodexAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:    "gpt-5.2",
		Messages: []map[string]interface{}{{"role": "user", "content": "hello"}},
		Functions: []map[string]interface{}{
			{"type": "function", "name": "duplicate", "description": "first", "parameters": map[string]interface{}{"type": "object"}},
			{"type": "function", "name": "duplicate", "description": "second", "parameters": map[string]interface{}{"type": "object"}},
		},
	})

	tools, ok := req["tools"].([]map[string]interface{})
	if !ok || len(tools) != 2 {
		t.Fatalf("expected both caller tools to be preserved, got %T %#v", req["tools"], req["tools"])
	}
	if tools[0]["description"] != "first" || tools[1]["description"] != "second" {
		t.Fatalf("expected caller order and definitions intact, got %#v", tools)
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

func TestCodexBuildRequest_SetsServiceTierPriorityForFastMode(t *testing.T) {
	a := &CodexAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:    "gpt-5.2",
		Messages: []map[string]interface{}{{"role": "user", "content": "hello"}},
		Metadata: map[string]interface{}{
			"service_tier": "priority",
		},
	})
	if req["service_tier"] != "priority" {
		t.Fatalf("expected service_tier=priority, got %v", req["service_tier"])
	}

	// Legacy config name "fast" also maps to request value "priority".
	req = a.BuildRequest(RequestConfig{
		Model:    "gpt-5.2",
		Messages: []map[string]interface{}{{"role": "user", "content": "hello"}},
		Metadata: map[string]interface{}{
			"service_tier": "fast",
		},
	})
	if req["service_tier"] != "priority" {
		t.Fatalf("expected service_tier=priority for fast alias, got %v", req["service_tier"])
	}

	// default / empty should omit the field.
	req = a.BuildRequest(RequestConfig{
		Model:    "gpt-5.2",
		Messages: []map[string]interface{}{{"role": "user", "content": "hello"}},
		Metadata: map[string]interface{}{
			"service_tier": "default",
		},
	})
	if _, ok := req["service_tier"]; ok {
		t.Fatalf("expected service_tier omitted for default, got %v", req["service_tier"])
	}
	req = a.BuildRequest(RequestConfig{
		Model:    "gpt-5.2",
		Messages: []map[string]interface{}{{"role": "user", "content": "hello"}},
	})
	if _, ok := req["service_tier"]; ok {
		t.Fatalf("expected service_tier omitted when unset, got %v", req["service_tier"])
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

func TestCodexBuildRequest_ExactPrefixAcrossMultiStepToolTurn(t *testing.T) {
	// Simulates a frozen turn layout after context-snapshot fix:
	// leading system stays in instructions; frozen developer goal stays in
	// input after the user message; conversation traffic grows only by suffix.
	a := &CodexAdapter{}
	functions := []map[string]interface{}{
		{
			"type":        "function",
			"name":        "read_logs",
			"description": "read logs",
			"parameters":  map[string]interface{}{"type": "object"},
		},
	}

	step1 := a.BuildRequest(RequestConfig{
		Model: "gpt-5.4",
		Messages: []map[string]interface{}{
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": "check application logs"},
			{"role": "developer", "content": "Persistent goal.\n\nkeep the prefix stable"},
		},
		Stream:    false,
		Functions: functions,
		Metadata:  map[string]interface{}{"prompt_cache_key": "turn-1"},
	})

	step2 := a.BuildRequest(RequestConfig{
		Model: "gpt-5.4",
		Messages: []map[string]interface{}{
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": "check application logs"},
			{"role": "developer", "content": "Persistent goal.\n\nkeep the prefix stable"},
			{
				"role":    "assistant",
				"content": "I will inspect logs.",
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call-logs",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "read_logs",
							"arguments": `{"path":"app.log"}`,
						},
					},
				},
			},
			{"role": "tool", "tool_call_id": "call-logs", "content": "log line ok"},
		},
		Stream:    false,
		Functions: functions,
		Metadata:  map[string]interface{}{"prompt_cache_key": "turn-1"},
	})

	if step1["instructions"] != step2["instructions"] {
		t.Fatalf("instructions drifted across tool steps:\nstep1=%#v\nstep2=%#v", step1["instructions"], step2["instructions"])
	}
	instructions, _ := step1["instructions"].(string)
	if instructions != "You are a helpful assistant." {
		t.Fatalf("expected only leading system in instructions, got %#v", step1["instructions"])
	}
	if strings.Contains(instructions, "keep the prefix stable") {
		t.Fatalf("turn-context goal must not rewrite top-level instructions, got %#v", instructions)
	}
	if step1["prompt_cache_key"] != step2["prompt_cache_key"] {
		t.Fatalf("prompt_cache_key drifted: %#v vs %#v", step1["prompt_cache_key"], step2["prompt_cache_key"])
	}

	prevInput, okPrev := step1["input"].([]map[string]interface{})
	currInput, okCurr := step2["input"].([]map[string]interface{})
	if !okPrev || !okCurr {
		t.Fatalf("expected codex input arrays, got %T and %T", step1["input"], step2["input"])
	}
	if len(currInput) <= len(prevInput) {
		t.Fatalf("expected step2 input to grow by suffix only, prev=%d curr=%d", len(prevInput), len(currInput))
	}
	if len(prevInput) < 2 {
		t.Fatalf("expected user + developer goal in step1 input, got %#v", prevInput)
	}
	if prevInput[0]["role"] != "user" {
		t.Fatalf("expected first input role user, got %#v", prevInput[0])
	}
	if prevInput[1]["role"] != "developer" {
		t.Fatalf("expected frozen goal as developer input item, got %#v", prevInput[1])
	}
	for index := range prevInput {
		prevJSON, _ := json.Marshal(prevInput[index])
		currJSON, _ := json.Marshal(currInput[index])
		if string(prevJSON) != string(currJSON) {
			t.Fatalf("input item %d is not an exact prefix match:\nprev=%s\ncurr=%s", index, prevJSON, currJSON)
		}
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

func TestCodexBuildRequest_PreservesConfiguredAndCustomReasoningEffort(t *testing.T) {
	a := &CodexAdapter{}
	for _, effort := range []string{"max", " custom-effort "} {
		t.Run(strings.TrimSpace(effort), func(t *testing.T) {
			req := a.BuildRequest(RequestConfig{
				Model:           "gpt-5.2",
				Messages:        []map[string]interface{}{{"role": "user", "content": "test"}},
				ReasoningEffort: effort,
			})

			reasoning, ok := req["reasoning"].(map[string]interface{})
			if !ok {
				t.Fatalf("expected reasoning config for %q, got %#v", effort, req["reasoning"])
			}
			if got := reasoning["effort"]; got != strings.TrimSpace(effort) {
				t.Fatalf("expected reasoning effort %q to be preserved, got %#v", strings.TrimSpace(effort), got)
			}
		})
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
	if toolCalls[0]["type"] != "function" {
		t.Fatalf("expected function type, got %#v", toolCalls[0])
	}
	fn, ok := toolCalls[0]["function"].(map[string]interface{})
	if !ok || fn["name"] != "list_mcp_resources" {
		t.Fatalf("unexpected tool name: %#v", toolCalls[0])
	}
	if fn["arguments"] != "{}" {
		t.Fatalf("unexpected tool arguments: %#v", toolCalls[0])
	}
}

func TestCodexHandleResponse_StreamWithCustomToolCall(t *testing.T) {
	a := &CodexAdapter{}
	sseData := strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","response":{"id":"resp_custom","model":"gpt-5.4"}}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","item":{"type":"custom_tool_call","id":"item_1","call_id":"call_patch_1","name":"apply_patch","input":"","status":"in_progress"}}`,
		"",
		"event: response.custom_tool_call_input.delta",
		`data: {"type":"response.custom_tool_call_input.delta","item_id":"item_1","call_id":"call_patch_1","delta":"*** Begin Patch\n"}`,
		"",
		"event: response.custom_tool_call_input.delta",
		`data: {"type":"response.custom_tool_call_input.delta","item_id":"item_1","call_id":"call_patch_1","delta":"*** End Patch"}`,
		"",
		"event: response.output_item.done",
		`data: {"type":"response.output_item.done","item":{"type":"custom_tool_call","id":"item_1","call_id":"call_patch_1","name":"apply_patch","input":"*** Begin Patch\n*** End Patch","status":"completed"}}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_custom","status":"completed","stop_reason":"end_turn"}}`,
		"",
	}, "\n")

	msg, err := a.HandleResponse(true, strings.NewReader(sseData), StreamCallbacks{})
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	toolCalls, ok := msg["tool_calls"].([]map[string]interface{})
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("expected 1 custom tool call, got %T %#v", msg["tool_calls"], msg["tool_calls"])
	}
	if toolCalls[0]["type"] != "custom_tool_call" {
		t.Fatalf("expected custom_tool_call type, got %#v", toolCalls[0])
	}
	if toolCalls[0]["input"] != "*** Begin Patch\n*** End Patch" {
		t.Fatalf("unexpected custom tool input: %#v", toolCalls[0])
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
	fn, ok := toolCalls[4]["function"].(map[string]interface{})
	if !ok || fn["arguments"] != `{"command":"echo 5"}` {
		t.Fatalf("unexpected last tool arguments: %#v", toolCalls[4])
	}
}

func TestCodexHandleResponse_StreamMultiReasoningCompactedFinalDoesNotCorruptToolArgs(t *testing.T) {
	a := &CodexAdapter{}
	// Live stream keys tools by high output_index after multiple reasoning items.
	// response.completed often collapses those reasoning items and re-emits the
	// same tools at lower array offsets. Merging by those compacted offsets used
	// to collide with earlier stream slots and concatenate incompatible JSON.
	sseData := strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","response":{"id":"resp_compact","model":"gpt-5.6-sol"}}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_0","summary":[]}}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"reasoning","id":"rs_1","summary":[]}}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_view","call_id":"call_view","name":"view","arguments":""}}`,
		"",
		"event: response.function_call_arguments.delta",
		`data: {"type":"response.function_call_arguments.delta","output_index":2,"call_id":"call_view","delta":"{\"file_path\":\"a.go\"}"}`,
		"",
		"event: response.output_item.done",
		`data: {"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","id":"fc_view","call_id":"call_view","name":"view","arguments":"{\"file_path\":\"a.go\"}"}}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":3,"item":{"type":"function_call","id":"fc_grep","call_id":"call_grep","name":"grep","arguments":""}}`,
		"",
		"event: response.function_call_arguments.delta",
		`data: {"type":"response.function_call_arguments.delta","output_index":3,"call_id":"call_grep","delta":"{\"pattern\":\"TODO\"}"}`,
		"",
		"event: response.output_item.done",
		`data: {"type":"response.output_item.done","output_index":3,"item":{"type":"function_call","id":"fc_grep","call_id":"call_grep","name":"grep","arguments":"{\"pattern\":\"TODO\"}"}}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_compact","status":"completed","stop_reason":"tool_call","output":[{"type":"reasoning","id":"rs_merged","summary":[{"type":"summary_text","text":"plan"}]},{"type":"function_call","id":"fc_view","call_id":"call_view","name":"view","arguments":"{\"file_path\":\"a.go\"}"},{"type":"function_call","id":"fc_grep","call_id":"call_grep","name":"grep","arguments":"{\"pattern\":\"TODO\"}"}]}}`,
		"",
	}, "\n")

	msg, err := a.HandleResponse(true, strings.NewReader(sseData), StreamCallbacks{})
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	toolCalls, ok := msg["tool_calls"].([]map[string]interface{})
	if !ok || len(toolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %T %#v", msg["tool_calls"], msg["tool_calls"])
	}
	fn0, _ := toolCalls[0]["function"].(map[string]interface{})
	if toolCalls[0]["id"] != "call_view" || fn0["name"] != "view" {
		t.Fatalf("unexpected first tool call: %#v", toolCalls[0])
	}
	if fn0["arguments"] != `{"file_path":"a.go"}` {
		t.Fatalf("view arguments corrupted: %#v", toolCalls[0]["function"])
	}
	fn1, _ := toolCalls[1]["function"].(map[string]interface{})
	if toolCalls[1]["id"] != "call_grep" || fn1["name"] != "grep" {
		t.Fatalf("unexpected second tool call: %#v", toolCalls[1])
	}
	if fn1["arguments"] != `{"pattern":"TODO"}` {
		t.Fatalf("grep arguments corrupted: %#v", toolCalls[1]["function"])
	}
}

func TestApplyAuthoritativeCodexToolArguments_ReplacesIncompatibleJSON(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"file_path":"a.go"}`)
	applyAuthoritativeCodexToolArguments(&b, `{"pattern":"TODO"}`)
	if got := b.String(); got != `{"pattern":"TODO"}` {
		t.Fatalf("expected authoritative replacement, got %#v", got)
	}

	b.Reset()
	b.WriteString(`{"file`)
	applyAuthoritativeCodexToolArguments(&b, `{"file_path":"a.go"}`)
	if got := b.String(); got != `{"file_path":"a.go"}` {
		t.Fatalf("expected compatible prefix extension, got %#v", got)
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
	fn, ok := toolCalls[0]["function"].(map[string]interface{})
	if !ok || fn["name"] != "spawn_team" {
		t.Fatalf("unexpected tool name: %#v", toolCalls[0])
	}
	if _, ok := fn["arguments"].(string); !ok {
		t.Fatalf("expected string arguments, got %#v", toolCalls[0])
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

func TestCodexHandleResponse_StreamReturnsErrorForStandaloneErrorEvent(t *testing.T) {
	a := &CodexAdapter{}
	sseData := strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.4"}}`,
		"",
		"event: error",
		`data: {"type":"error","code":"internal_server_error","message":"connection reset by peer"}`,
		"",
	}, "\n")

	msg, err := a.handleCodexStreamResponse(strings.NewReader(sseData), StreamCallbacks{})
	if err == nil {
		t.Fatal("expected standalone error event to fail the stream")
	}
	if !strings.Contains(err.Error(), "connection reset by peer") {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, _ := msg["finish_reason"].(string); got != "failed" {
		t.Fatalf("expected finish_reason failed, got %#v", msg["finish_reason"])
	}
}

func TestCodexHandleResponse_StreamRecoversDoneEventsWithoutDeltas(t *testing.T) {
	a := &CodexAdapter{}
	var emittedText strings.Builder
	sseData := strings.Join([]string{
		"event: response.output_text.done",
		`data: {"type":"response.output_text.done","text":"Recovered text"}`,
		"",
		"event: response.function_call_arguments.done",
		`data: {"type":"response.function_call_arguments.done","output_index":1,"call_id":"call_1","name":"shell","arguments":"{\"command\":\"git status\"}"}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"status":"completed","stop_reason":"tool_call"}}`,
		"",
	}, "\n")

	msg, err := a.handleCodexStreamResponse(strings.NewReader(sseData), StreamCallbacks{
		OnText: func(delta string) { emittedText.WriteString(delta) },
	})
	if err != nil {
		t.Fatalf("handleCodexStreamResponse failed: %v", err)
	}
	if got := msg["content"]; got != "Recovered text" || emittedText.String() != "Recovered text" {
		t.Fatalf("expected recovered text, got content=%#v emitted=%q", got, emittedText.String())
	}
	toolCalls, ok := msg["tool_calls"].([]map[string]interface{})
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("expected one recovered tool call, got %T %#v", msg["tool_calls"], msg["tool_calls"])
	}
	if toolCalls[0]["id"] != "call_1" || toolCalls[0]["name"] != "shell" {
		t.Fatalf("unexpected recovered tool call: %#v", toolCalls[0])
	}
}

func TestCodexHandleResponse_StreamReadsNestedResponseError(t *testing.T) {
	a := &CodexAdapter{}
	sseData := strings.Join([]string{
		"event: response.failed",
		`data: {"type":"response.failed","response":{"status":"failed","error":{"code":"upstream_error","message":"nested provider failure"}}}`,
		"",
	}, "\n")

	_, err := a.handleCodexStreamResponse(strings.NewReader(sseData), StreamCallbacks{})
	if err == nil || !strings.Contains(err.Error(), "nested provider failure") {
		t.Fatalf("expected nested response error, got %v", err)
	}
}

func TestCodexHandleResponse_StreamEmitsImageProgressCallbacks(t *testing.T) {
	a := &CodexAdapter{}
	var phases []string
	var progressValues []float64
	var sanitizedIDs []string

	sseData := strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","response":{"id":"resp_image","model":"gpt-5.4"}}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"image_generation_call","id":"img:1","status":"in_progress","revised_prompt":"a tiny robot"}}`,
		"",
		"event: response.image_generation_call.partial_image",
		`data: {"type":"response.image_generation_call.partial_image","output_index":0,"index":1,"count":4,"item":{"type":"image_generation_call","id":"img:1","status":"in_progress","revised_prompt":"a tiny robot"}}`,
		"",
		"event: response.output_item.done",
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"image_generation_call","id":"img:1","status":"completed","revised_prompt":"a tiny robot"}}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_image","status":"completed","stop_reason":"end_turn"}}`,
		"",
	}, "\n")

	msg, err := a.HandleResponse(true, strings.NewReader(sseData), StreamCallbacks{
		OnImage: func(metadata map[string]interface{}) {
			if metadata == nil {
				t.Fatal("expected image metadata")
			}
			if phase, _ := metadata["phase"].(string); phase != "" {
				phases = append(phases, phase)
			}
			if value, ok := metadata["progress"].(float64); ok {
				progressValues = append(progressValues, value)
			}
			if value, _ := metadata["sanitized_id"].(string); value != "" {
				sanitizedIDs = append(sanitizedIDs, value)
			}
		},
	})
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	if msg["content"] != "" {
		t.Fatalf("expected empty content, got %#v", msg["content"])
	}
	if joined := strings.Join(phases, ","); joined != "started,partial,completed" {
		t.Fatalf("unexpected phases: %v", phases)
	}
	if len(progressValues) != 1 || progressValues[0] != 0.25 {
		t.Fatalf("unexpected progress values: %#v", progressValues)
	}
	if len(sanitizedIDs) != 3 || sanitizedIDs[0] != "img_1" {
		t.Fatalf("unexpected sanitized ids: %#v", sanitizedIDs)
	}
}

func TestCodexBuildRequest_DefaultsToNonStrictAndPreservesOptionalProperties(t *testing.T) {
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
			},
		},
	})

	tools := req["tools"].([]map[string]interface{})
	view := findToolByName(t, tools, "view")
	if view["strict"] != false {
		t.Fatalf("expected Codex-compatible strict=false default, got %v", view["strict"])
	}
	params := view["parameters"].(map[string]interface{})
	required := toStringSlice(t, params["required"])
	if len(required) != 1 || required[0] != "file_path" {
		t.Fatalf("expected only the declared field to remain required, got %v", required)
	}
	offset := params["properties"].(map[string]interface{})["offset"].(map[string]interface{})
	if offset["type"] != "integer" {
		t.Fatalf("expected optional integer schema to remain unchanged, got %v", offset["type"])
	}
}

func TestCodexBuildRequest_NonStrictOmitsEmptyRequiredKeyword(t *testing.T) {
	req := (&CodexAdapter{}).BuildRequest(RequestConfig{
		Model:    "gpt-5.2",
		Messages: []map[string]interface{}{{"role": "user", "content": "test"}},
		Functions: []map[string]interface{}{{
			"type":        "function",
			"name":        "shell",
			"description": "execute shell",
			"parameters": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{"command": map[string]interface{}{"type": "string"}},
				"required":   []string{},
			},
		}},
	})

	tool := findToolByName(t, req["tools"].([]map[string]interface{}), "shell")
	if tool["strict"] != false {
		t.Fatalf("expected strict=false, got %v", tool["strict"])
	}
	parameters := tool["parameters"].(map[string]interface{})
	if _, exists := parameters["required"]; exists {
		t.Fatalf("invalid required:null must be omitted, got %#v", parameters["required"])
	}
}

func TestCodexBuildRequest_StrictToolSanitizesOptionalPropertiesToNullableRequired(t *testing.T) {
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

func TestCodexBuildRequest_PreservesWrappedFunctionStrictFlag(t *testing.T) {
	req := (&CodexAdapter{}).BuildRequest(RequestConfig{
		Model:    "gpt-5.2",
		Messages: []map[string]interface{}{{"role": "user", "content": "test"}},
		Functions: []map[string]interface{}{{
			"type": "function",
			"function": map[string]interface{}{
				"name":   "inspect",
				"strict": true,
				"parameters": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"path":  map[string]interface{}{"type": "string"},
						"limit": map[string]interface{}{"type": "integer"},
					},
					"required": []string{"path"},
				},
			},
		}},
	})

	tool := findToolByName(t, req["tools"].([]map[string]interface{}), "inspect")
	if tool["strict"] != true {
		t.Fatalf("expected wrapped strict=true to be preserved, got %v", tool["strict"])
	}
	required := toStringSlice(t, tool["parameters"].(map[string]interface{})["required"])
	if len(required) != 2 {
		t.Fatalf("expected strict schema normalization, got required=%v", required)
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
				"strict":      true,
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

func TestCodexBuildRequest_NonStrictToolPreservesSkillRuntimeOpenObjects(t *testing.T) {
	a := &CodexAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:    "gpt-5.2",
		Messages: []map[string]interface{}{{"role": "user", "content": "test"}},
		Functions: []map[string]interface{}{
			{
				"type":        "function",
				"name":        "skill__alpha",
				"description": "skill",
				"strict":      false,
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
	if skillTool["strict"] != false {
		t.Fatalf("expected non-strict skill tool, got %v", skillTool["strict"])
	}
	params := skillTool["parameters"].(map[string]interface{})
	required := toStringSlice(t, params["required"])
	if len(required) != 1 || required[0] != "prompt" {
		t.Fatalf("expected only prompt to remain required, got %v", required)
	}
	props := params["properties"].(map[string]interface{})
	context := props["context"].(map[string]interface{})
	if context["type"] != "object" {
		t.Fatalf("expected context to remain an object, got %v", context["type"])
	}
	if _, exists := context["additionalProperties"]; exists {
		t.Fatalf("expected open context object to remain open, got %v", context["additionalProperties"])
	}
}

func TestCodexBuildRequest_NormalizesCompatibleNonStrictSchemaShapes(t *testing.T) {
	req := (&CodexAdapter{}).BuildRequest(RequestConfig{
		Model:    "gpt-5.2",
		Messages: []map[string]interface{}{{"role": "user", "content": "test"}},
		Functions: []map[string]interface{}{{
			"type":   "function",
			"name":   "compat_schema",
			"strict": false,
			"parameters": map[string]interface{}{
				"properties": map[string]interface{}{
					"tags":     map[string]interface{}{"type": "array"},
					"metadata": map[string]interface{}{"properties": map[string]interface{}{"label": map[string]interface{}{"type": "string"}}},
					"kind":     map[string]interface{}{"const": "file"},
					"enabled":  true,
				},
				"required": []string{"kind"},
				"$defs":    []interface{}{"malformed"},
			},
		}},
	})

	tool := findToolByName(t, req["tools"].([]map[string]interface{}), "compat_schema")
	params := tool["parameters"].(map[string]interface{})
	if params["type"] != "object" {
		t.Fatalf("expected object type inference, got %v", params["type"])
	}
	props := params["properties"].(map[string]interface{})
	tags := props["tags"].(map[string]interface{})
	if tags["items"].(map[string]interface{})["type"] != "string" {
		t.Fatalf("expected default array items, got %#v", tags["items"])
	}
	metadata := props["metadata"].(map[string]interface{})
	if metadata["type"] != "object" {
		t.Fatalf("expected nested object type inference, got %#v", metadata)
	}
	kind := props["kind"].(map[string]interface{})
	if kind["type"] != "string" {
		t.Fatalf("expected const schema to infer string, got %#v", kind)
	}
	enumValues, ok := kind["enum"].([]interface{})
	if !ok || len(enumValues) != 1 || enumValues[0] != "file" {
		t.Fatalf("expected const to become enum, got %#v", kind["enum"])
	}
	if props["enabled"].(map[string]interface{})["type"] != "string" {
		t.Fatalf("expected boolean schema form to degrade safely, got %#v", props["enabled"])
	}
	if _, exists := params["$defs"]; exists {
		t.Fatalf("expected malformed definitions to be dropped, got %#v", params["$defs"])
	}
}

func TestCodexBuildRequest_CompactsOversizedSchemaDescriptions(t *testing.T) {
	req := (&CodexAdapter{}).BuildRequest(RequestConfig{
		Model:    "gpt-5.2",
		Messages: []map[string]interface{}{{"role": "user", "content": "test"}},
		Functions: []map[string]interface{}{{
			"type": "function",
			"name": "large_schema",
			"parameters": map[string]interface{}{
				"type":        "object",
				"description": strings.Repeat("verbose ", 900),
				"properties": map[string]interface{}{
					"description": map[string]interface{}{"type": "string", "description": "user value"},
					"path":        map[string]interface{}{"type": "string", "description": "path value"},
				},
			},
		}},
	})

	tool := findToolByName(t, req["tools"].([]map[string]interface{}), "large_schema")
	params := tool["parameters"].(map[string]interface{})
	if _, exists := params["description"]; exists {
		t.Fatalf("expected oversized schema descriptions to be stripped")
	}
	props := params["properties"].(map[string]interface{})
	if _, exists := props["description"]; !exists {
		t.Fatalf("schema property named description must be preserved")
	}
	if _, exists := props["description"].(map[string]interface{})["description"]; exists {
		t.Fatalf("expected nested description annotation to be stripped")
	}
}

func TestCodexBuildRequest_PrunesUnreachableSchemaDefinitions(t *testing.T) {
	req := (&CodexAdapter{}).BuildRequest(RequestConfig{
		Model:    "gpt-5.2",
		Messages: []map[string]interface{}{{"role": "user", "content": "test"}},
		Functions: []map[string]interface{}{{
			"type": "function",
			"name": "refs",
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"user_name": map[string]interface{}{"$ref": "#/$defs/User/properties/name"},
				},
				"$defs": map[string]interface{}{
					"User": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"name":    map[string]interface{}{"type": "string"},
							"address": map[string]interface{}{"$ref": "#/$defs/Address"},
						},
					},
					"Address": map[string]interface{}{"type": "string"},
					"Unused":  map[string]interface{}{"type": "boolean"},
				},
			},
		}},
	})

	tool := findToolByName(t, req["tools"].([]map[string]interface{}), "refs")
	definitions := tool["parameters"].(map[string]interface{})["$defs"].(map[string]interface{})
	if _, exists := definitions["User"]; !exists {
		t.Fatalf("expected directly reachable definition")
	}
	if _, exists := definitions["Address"]; !exists {
		t.Fatalf("expected transitively reachable definition")
	}
	if _, exists := definitions["Unused"]; exists {
		t.Fatalf("expected unreachable definition to be pruned")
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
	if id, _ := input[1]["id"].(string); id == "" {
		t.Fatalf("expected reasoning item to carry a wire id, got %#v", input[1])
	}
	if input[2]["type"] != "function_call" || input[2]["name"] != "execute_shell_command" || input[2]["call_id"] != "call_1" {
		t.Fatalf("unexpected function_call item: %#v", input[2])
	}
	if id, _ := input[2]["id"].(string); id != "fc_1" {
		t.Fatalf("expected function_call item to carry id=fc_1, got %#v", input[2])
	}
	if input[3]["type"] != "function_call_output" || input[3]["call_id"] != "call_1" {
		t.Fatalf("unexpected function_call_output item: %#v", input[3])
	}
	if id, _ := input[3]["id"].(string); id != "fc_1" {
		t.Fatalf("expected function_call_output item to carry id=fc_1, got %#v", input[3])
	}
}

func TestCodexBuildRequest_FollowUpCustomToolCallUsesCustomOutputItems(t *testing.T) {
	a := &CodexAdapter{}
	assistant := a.BuildAssistantMessage(
		"",
		[]map[string]interface{}{
			{
				"id":        "call_patch_1",
				"type":      "custom_tool_call",
				"name":      "apply_patch",
				"arguments": "*** Begin Patch\n*** End Patch",
				"input":     "*** Begin Patch\n*** End Patch",
			},
		},
		"",
	)

	req := a.BuildRequest(RequestConfig{
		Model: "gpt-5.4",
		Messages: []map[string]interface{}{
			{"role": "user", "content": "修复文件"},
			assistant,
			{"role": "tool", "tool_call_id": "call_patch_1", "content": "补丁已应用"},
		},
		Stream: false,
	})

	input := req["input"].([]map[string]interface{})
	if len(input) != 3 {
		t.Fatalf("expected 3 input items, got %d: %#v", len(input), input)
	}
	if input[1]["type"] != "custom_tool_call" || input[1]["name"] != "apply_patch" {
		t.Fatalf("unexpected custom tool call item: %#v", input[1])
	}
	if id, _ := input[1]["id"].(string); id != "ctc_patch_1" {
		t.Fatalf("expected custom_tool_call item to carry id=ctc_patch_1, got %#v", input[1])
	}
	if input[2]["type"] != "custom_tool_call_output" || input[2]["call_id"] != "call_patch_1" {
		t.Fatalf("unexpected custom tool output item: %#v", input[2])
	}
	if id, _ := input[2]["id"].(string); id != "ctc_patch_1" {
		t.Fatalf("expected custom_tool_call_output item to carry id=ctc_patch_1, got %#v", input[2])
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

// TestCodexBuildRequest_ReplaysCanonicalizedOutputItemsWithIDs locks the
// Console Go wire contract: function_call / function_call_output items that
// were canonicalized (id/status/phase dropped) must be replayed with a
// mandatory fc_-prefixed id so the upstream does not reject the request with
// "messages[N]: missing field `id`" or "Invalid 'input[N].id'" (HTTP 400).
func TestCodexBuildRequest_ReplaysCanonicalizedOutputItemsWithIDs(t *testing.T) {
	a := &CodexAdapter{}

	// Simulate history that went through canonicalizeCodexOutputItems: the
	// response_output_items payload has no id/status/phase fields, exactly as
	// it is stored after reasoning metadata round-trips.
	assistant := map[string]interface{}{
		"role":    "assistant",
		"content": "I will inspect logs.",
		"response_output_items": []map[string]interface{}{
			{
				"type":    "reasoning",
				"summary": []map[string]interface{}{},
			},
			{
				"type":    "message",
				"role":    "assistant",
				"content": []map[string]interface{}{{"type": "output_text", "text": "I will inspect logs."}},
			},
			{
				"type":      "function_call",
				"call_id":   "call_1",
				"name":      "execute_shell_command",
				"arguments": `{"command":"git status -sb"}`,
			},
			{
				"type":    "function_call_output",
				"call_id": "call_1",
				"output":  "## main",
			},
		},
	}

	req := a.BuildRequest(RequestConfig{
		Model: "gpt-5.4",
		Messages: []map[string]interface{}{
			{"role": "user", "content": "查看git status"},
			assistant,
		},
		Stream: false,
	})

	input := req["input"].([]map[string]interface{})
	if len(input) != 5 {
		t.Fatalf("expected 5 input items, got %d: %#v", len(input), input)
	}
	if id, _ := input[1]["id"].(string); id == "" {
		t.Fatalf("expected replayed reasoning item to carry an id, got %#v", input[1])
	}
	if id, _ := input[3]["id"].(string); id != "fc_1" {
		t.Fatalf("expected replayed function_call item to carry id=fc_1, got %#v", input[3])
	}
	if id, _ := input[4]["id"].(string); id != "fc_1" {
		t.Fatalf("expected replayed function_call_output item to carry id=fc_1, got %#v", input[4])
	}

	// Stability: rebuilding the same history twice must produce identical ids
	// so prompt-cache prefixes stay byte-identical across steps.
	second := a.BuildRequest(RequestConfig{
		Model: "gpt-5.4",
		Messages: []map[string]interface{}{
			{"role": "user", "content": "查看git status"},
			assistant,
		},
		Stream: false,
	})
	secondInput := second["input"].([]map[string]interface{})
	if id, _ := input[1]["id"].(string); id != secondInput[1]["id"] {
		t.Fatalf("expected stable reasoning id across rebuilds, got %#v vs %#v", input[1]["id"], secondInput[1]["id"])
	}
	if id, _ := input[3]["id"].(string); id != secondInput[3]["id"] {
		t.Fatalf("expected stable function_call id across rebuilds, got %#v vs %#v", input[3]["id"], secondInput[3]["id"])
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

func TestCodexBuildRequest_ResponseFormatJSONSchemaChatStyle(t *testing.T) {
	a := &CodexAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:    "gpt-5.2",
		Messages: []map[string]interface{}{{"role": "user", "content": "parse this"}},
		Metadata: map[string]interface{}{
			"response_format": map[string]interface{}{
				"type": "json_schema",
				"json_schema": map[string]interface{}{
					"name":        "ticket",
					"description": "extracted ticket",
					"strict":      true,
					"schema": map[string]interface{}{
						"type":     "object",
						"required": []string{"title", "priority"},
					},
				},
			},
		},
	})

	text, ok := req["text"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected text config, got %T %#v", req["text"], req["text"])
	}
	format, ok := text["format"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected text.format, got %T %#v", text["format"], text["format"])
	}
	if format["type"] != "json_schema" || format["name"] != "ticket" || format["strict"] != true {
		t.Fatalf("unexpected format: %#v", format)
	}
	if format["description"] != "extracted ticket" {
		t.Fatalf("expected description preserved, got %#v", format)
	}
	schema, ok := format["schema"].(map[string]interface{})
	if !ok || schema["type"] != "object" {
		t.Fatalf("expected schema preserved, got %#v", format["schema"])
	}
}

func TestCodexBuildRequest_ResponseFormatJSONSchemaResponsesStyle(t *testing.T) {
	a := &CodexAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:    "gpt-5.2",
		Messages: []map[string]interface{}{{"role": "user", "content": "parse this"}},
		Metadata: map[string]interface{}{
			"response_format": map[string]interface{}{
				"type":   "json_schema",
				"name":   "sentiment",
				"strict": false,
				"schema": map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{"label": map[string]interface{}{"type": "string"}},
				},
			},
		},
	})

	format, ok := req["text"].(map[string]interface{})["format"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected text.format, got %#v", req["text"])
	}
	if format["type"] != "json_schema" || format["name"] != "sentiment" {
		t.Fatalf("unexpected format: %#v", format)
	}
	if _, exists := format["strict"]; !exists {
		t.Fatalf("expected strict false to be preserved, got %#v", format)
	}
}

func TestCodexBuildRequest_ResponseFormatJSONObjectAndTextIgnored(t *testing.T) {
	a := &CodexAdapter{}
	for _, format := range []map[string]interface{}{
		{"type": "json_object"},
		{"type": "text"},
		{"type": "json_schema", "schema": "not-an-object"},
	} {
		req := a.BuildRequest(RequestConfig{
			Model:    "gpt-5.2",
			Messages: []map[string]interface{}{{"role": "user", "content": "hi"}},
			Metadata: map[string]interface{}{"response_format": format},
		})
		if _, exists := req["text"]; exists {
			t.Fatalf("did not expect text config for %#v: %#v", format, req["text"])
		}
	}
}

func TestCodexBuildRequest_SamplingParamsOnlyWhenOptedIn(t *testing.T) {
	a := &CodexAdapter{}
	base := RequestConfig{
		Model:       "gpt-5.2",
		Messages:    []map[string]interface{}{{"role": "user", "content": "hi"}},
		Temperature: 0.7,
	}

	// 默认不发送采样参数(Codex CLI 协议无此字段)。
	req := a.BuildRequest(base)
	if _, exists := req["temperature"]; exists {
		t.Fatalf("did not expect temperature without supports_sampling: %#v", req["temperature"])
	}

	req = a.BuildRequest(RequestConfig{
		Model:       "gpt-5.2",
		Messages:    []map[string]interface{}{{"role": "user", "content": "hi"}},
		Temperature: 0.7,
		Metadata: map[string]interface{}{
			"supports_sampling": true,
			"top_p":             0.9,
		},
	})
	if req["temperature"] != 0.7 {
		t.Fatalf("expected temperature 0.7, got %#v", req["temperature"])
	}
	if req["top_p"] != 0.9 {
		t.Fatalf("expected top_p 0.9, got %#v", req["top_p"])
	}
}

func TestCodexBuildRequest_ResponseMetadataPassThroughFiltersInternalKeys(t *testing.T) {
	a := &CodexAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:    "gpt-5.2",
		Messages: []map[string]interface{}{{"role": "user", "content": "hi"}},
		Metadata: map[string]interface{}{
			"session_id":                 "sess_1",
			"supports_max_output_tokens": true,
			"response_format": map[string]interface{}{
				"type":   "json_schema",
				"name":   "x",
				"schema": map[string]interface{}{"type": "object"},
			},
			"codex_internal_flag": "secret",
			"user_tracking_id":    "track-123",
			"non_string_value":    42,
		},
	})

	metadata, ok := req["metadata"].(map[string]string)
	if !ok {
		t.Fatalf("expected metadata map, got %T %#v", req["metadata"], req["metadata"])
	}
	if metadata["user_tracking_id"] != "track-123" {
		t.Fatalf("expected user_tracking_id passed through, got %#v", metadata)
	}
	for _, forbidden := range []string{"session_id", "supports_max_output_tokens", "response_format", "codex_internal_flag", "non_string_value"} {
		if _, exists := metadata[forbidden]; exists {
			t.Fatalf("internal key %q leaked into upstream metadata: %#v", forbidden, metadata)
		}
	}
}

func TestCodexBuildRequest_MultimodalInputParts(t *testing.T) {
	a := &CodexAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model: "gpt-5.2",
		Messages: []map[string]interface{}{
			{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "input_text", "text": "analyze these"},
					{
						"type":      "input_image",
						"image_url": "data:image/png;base64,ZmFrZQ==",
						"detail":    "high",
					},
					{
						"type":      "input_image",
						"image_url": "data:image/png;base64,ZmFrZQ==",
						"detail":    "ultra", // 非法 detail 应被丢弃
					},
					{
						"type":    "input_file",
						"file_id": "file_abc123",
					},
					{
						"type":      "input_file",
						"filename":  "notes.txt",
						"file_data": "data:text/plain;base64,bm90ZXM=",
					},
					{
						"type":        "input_audio",
						"input_audio": "data:audio/wav;base64,UkVGRg==",
						"format":      "wav",
					},
				},
			},
		},
	})

	input := req["input"].([]map[string]interface{})
	parts, ok := input[0]["content"].([]map[string]interface{})
	if !ok {
		t.Fatalf("expected structured content parts, got %T %#v", input[0]["content"], input[0]["content"])
	}
	if len(parts) != 6 {
		t.Fatalf("expected 6 parts, got %d: %#v", len(parts), parts)
	}

	imageHigh := parts[1]
	if imageHigh["type"] != "input_image" || imageHigh["detail"] != "high" {
		t.Fatalf("expected input_image with detail high, got %#v", imageHigh)
	}
	imageInvalidDetail := parts[2]
	if _, exists := imageInvalidDetail["detail"]; exists {
		t.Fatalf("invalid detail must be dropped, got %#v", imageInvalidDetail)
	}
	if parts[3]["type"] != "input_file" || parts[3]["file_id"] != "file_abc123" {
		t.Fatalf("expected input_file with file_id, got %#v", parts[3])
	}
	if parts[4]["type"] != "input_file" || parts[4]["filename"] != "notes.txt" || parts[4]["file_data"] != "data:text/plain;base64,bm90ZXM=" {
		t.Fatalf("expected input_file with data, got %#v", parts[4])
	}
	if parts[5]["type"] != "input_audio" || parts[5]["format"] != "wav" || parts[5]["input_audio"] != "data:audio/wav;base64,UkVGRg==" {
		t.Fatalf("expected input_audio, got %#v", parts[5])
	}
}

func TestCodexBuildRequest_MaxOutputTokensExplicitMetadata(t *testing.T) {
	a := &CodexAdapter{}
	base := RequestConfig{
		Model:     "gpt-5.2",
		Messages:  []map[string]interface{}{{"role": "user", "content": "hi"}},
		MaxTokens: 2048,
	}

	// 字符串 "inf" 直接透传(官方 Responses API 语义:不设上限)。
	withInf := base
	withInf.Metadata = map[string]interface{}{"max_output_tokens": "inf"}
	req := a.BuildRequest(withInf)
	if req["max_output_tokens"] != "inf" {
		t.Fatalf("expected max_output_tokens=inf, got %#v", req["max_output_tokens"])
	}

	// 数字优先于 config.MaxTokens。
	withNum := base
	withNum.Metadata = map[string]interface{}{"max_output_tokens": 8192}
	req = a.BuildRequest(withNum)
	if req["max_output_tokens"] != 8192 {
		t.Fatalf("expected max_output_tokens=8192, got %#v", req["max_output_tokens"])
	}

	// 非法值(空串/0)回退到 config.MaxTokens。
	for _, bad := range []interface{}{"", 0, -5} {
		withBad := base
		withBad.Metadata = map[string]interface{}{"max_output_tokens": bad}
		req = a.BuildRequest(withBad)
		if req["max_output_tokens"] != 2048 {
			t.Fatalf("expected fallback 2048 for %#v, got %#v", bad, req["max_output_tokens"])
		}
	}
}

func TestCodexBuildRequest_StoreAndPreviousResponseID(t *testing.T) {
	a := &CodexAdapter{}
	base := RequestConfig{
		Model:    "gpt-5.2",
		Messages: []map[string]interface{}{{"role": "user", "content": "hi"}},
	}

	// 默认 stateless:store=false 且不发送 previous_response_id。
	req := a.BuildRequest(base)
	if req["store"] != false {
		t.Fatalf("expected default store=false, got %#v", req["store"])
	}
	if _, exists := req["previous_response_id"]; exists {
		t.Fatalf("did not expect previous_response_id by default: %#v", req["previous_response_id"])
	}

	// metadata 显式开启 store 并续接 previous response。
	withMeta := base
	withMeta.Metadata = map[string]interface{}{
		"store":                true,
		"previous_response_id": "resp_abc123",
	}
	req = a.BuildRequest(withMeta)
	if req["store"] != true {
		t.Fatalf("expected store=true, got %#v", req["store"])
	}
	if req["previous_response_id"] != "resp_abc123" {
		t.Fatalf("expected previous_response_id=resp_abc123, got %#v", req["previous_response_id"])
	}

	// store/previous_response_id 是适配器消费键,不得泄漏进上游 metadata。
	if md, ok := req["metadata"].(map[string]string); ok {
		if _, leaked := md["store"]; leaked {
			t.Fatalf("store leaked into metadata: %#v", md)
		}
		if _, leaked := md["previous_response_id"]; leaked {
			t.Fatalf("previous_response_id leaked into metadata: %#v", md)
		}
	}

	// 空 previous_response_id 视为未设置。
	withEmpty := base
	withEmpty.Metadata = map[string]interface{}{"previous_response_id": "  "}
	req = a.BuildRequest(withEmpty)
	if _, exists := req["previous_response_id"]; exists {
		t.Fatalf("did not expect empty previous_response_id: %#v", req["previous_response_id"])
	}
}

func TestNormalizeCodexInputPart_Video(t *testing.T) {
	// 公开 URL 形态。
	urlPart := normalizeCodexInputPart(map[string]interface{}{
		"type":      "input_video",
		"video_url": "https://example.com/clip.mp4",
	})
	if urlPart == nil || urlPart["type"] != "input_video" || urlPart["video_url"] != "https://example.com/clip.mp4" {
		t.Fatalf("expected input_video with video_url, got %#v", urlPart)
	}

	// base64 data URL 形态。
	dataPart := normalizeCodexInputPart(map[string]interface{}{
		"type":      "input_video",
		"filename":  "clip.mp4",
		"file_data": "data:video/mp4;base64,AAAA",
	})
	if dataPart == nil || dataPart["filename"] != "clip.mp4" || dataPart["file_data"] != "data:video/mp4;base64,AAAA" {
		t.Fatalf("expected input_video with file_data, got %#v", dataPart)
	}

	// 缺失内容时丢弃。
	for _, bad := range []map[string]interface{}{
		{"type": "input_video"},
		{"type": "input_video", "video_url": ""},
		{"type": "input_video", "filename": "clip.mp4", "file_data": "not-a-data-url"},
	} {
		if got := normalizeCodexInputPart(bad); got != nil {
			t.Fatalf("expected nil for %#v, got %#v", bad, got)
		}
	}
}

func TestNormalizeCodexInputPart_OutputTextKeepsAnnotations(t *testing.T) {
	annotations := []map[string]interface{}{
		{"type": "url_citation", "url": "https://example.com/doc", "title": "Doc", "start_index": 0, "end_index": 10},
	}

	// output_text 保留 annotations,便于带引用继续对话。
	part := normalizeCodexInputPart(map[string]interface{}{
		"type":        "output_text",
		"text":        "see the doc",
		"annotations": annotations,
	})
	if part == nil || part["type"] != "output_text" {
		t.Fatalf("expected output_text, got %#v", part)
	}
	got := decodeSliceOfMaps(part["annotations"])
	if len(got) != 1 || got[0]["url"] != "https://example.com/doc" {
		t.Fatalf("expected annotations preserved, got %#v", part["annotations"])
	}

	// summary_text 同样保留。
	part = normalizeCodexInputPart(map[string]interface{}{
		"type":        "summary_text",
		"text":        "summary",
		"annotations": annotations,
	})
	if part == nil || part["type"] != "output_text" {
		t.Fatalf("expected summary_text normalized to output_text, got %#v", part)
	}
	if len(decodeSliceOfMaps(part["annotations"])) != 1 {
		t.Fatalf("expected summary_text annotations preserved, got %#v", part["annotations"])
	}

	// input_text 不携带 annotations。
	part = normalizeCodexInputPart(map[string]interface{}{
		"type":        "input_text",
		"text":        "plain",
		"annotations": annotations,
	})
	if part == nil {
		t.Fatalf("expected input_text, got nil")
	}
	if _, exists := part["annotations"]; exists {
		t.Fatalf("did not expect annotations on input_text: %#v", part)
	}
}

func TestCodexStreamState_SortsOutOfOrderToolCalls(t *testing.T) {
	a := &CodexAdapter{}
	state := &CodexStreamState{
		ToolCalls:    make(map[int]*CodexToolCall),
		ToolItemKeys: make(map[string]int),
		OutputItems:  make(map[int]map[string]interface{}),
	}

	// 模拟并行工具调用:delta 事件乱序到达(index 1 先到,index 0 后到)。
	a.handleFunctionCallArgumentsDelta(state, map[string]interface{}{
		"index": 1, "delta": `{"query":"b"`,
	})
	a.handleFunctionCallArgumentsDelta(state, map[string]interface{}{
		"index": 0, "delta": `{"query":"a"`,
	})
	a.handleFunctionCallArgumentsDelta(state, map[string]interface{}{
		"index": 1, "delta": "}",
	})
	a.handleFunctionCallArgumentsDelta(state, map[string]interface{}{
		"index": 0, "delta": "}",
	})

	result := state.ToMap()
	toolCalls, ok := result["tool_calls"].([]map[string]interface{})
	if !ok || len(toolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %#v", result["tool_calls"])
	}
	// 输出必须按 output_index 升序,而非到达顺序。
	if toolCalls[0]["arguments"] != `{"query":"a"}` || toolCalls[1]["arguments"] != `{"query":"b"}` {
		t.Fatalf("expected index-sorted tool calls, got %#v", toolCalls)
	}
}

func TestCodexBuildRequest_StreamOptionsOnlyWhenStreaming(t *testing.T) {
	a := &CodexAdapter{}
	base := RequestConfig{
		Model:    "gpt-5.2",
		Messages: []map[string]interface{}{{"role": "user", "content": "hi"}},
		Metadata: map[string]interface{}{
			"stream_options": map[string]interface{}{"include_usage": true},
		},
	}

	// 流式请求透传 stream_options。
	streaming := base
	streaming.Stream = true
	req := a.BuildRequest(streaming)
	opts, ok := req["stream_options"].(map[string]interface{})
	if !ok || opts["include_usage"] != true {
		t.Fatalf("expected stream_options.include_usage=true, got %#v", req["stream_options"])
	}

	// 非流式请求不发送(官方只在流式时生效)。
	nonStreaming := base
	nonStreaming.Stream = false
	req = a.BuildRequest(nonStreaming)
	if _, exists := req["stream_options"]; exists {
		t.Fatalf("did not expect stream_options when non-streaming: %#v", req["stream_options"])
	}

	// stream_options 是消费键,不得泄漏进 metadata。
	if md, ok := req["metadata"].(map[string]string); ok {
		if _, leaked := md["stream_options"]; leaked {
			t.Fatalf("stream_options leaked into metadata: %#v", md)
		}
	}
}

func TestCodexBuildRequest_StopPassThrough(t *testing.T) {
	a := &CodexAdapter{}
	base := RequestConfig{
		Model:    "gpt-5.2",
		Messages: []map[string]interface{}{{"role": "user", "content": "hi"}},
	}

	// 单字符串。
	single := base
	single.Metadata = map[string]interface{}{"stop": "END"}
	req := a.BuildRequest(single)
	if req["stop"] != "END" {
		t.Fatalf("expected stop=END, got %#v", req["stop"])
	}

	// 字符串数组。
	list := base
	list.Metadata = map[string]interface{}{"stop": []string{"END", "STOP"}}
	req = a.BuildRequest(list)
	stops, ok := req["stop"].([]string)
	if !ok || len(stops) != 2 || stops[0] != "END" || stops[1] != "STOP" {
		t.Fatalf("expected stop array, got %#v", req["stop"])
	}

	// 空值不发送。
	empty := base
	empty.Metadata = map[string]interface{}{"stop": "  "}
	req = a.BuildRequest(empty)
	if _, exists := req["stop"]; exists {
		t.Fatalf("did not expect empty stop: %#v", req["stop"])
	}
}

func TestCodexBuildRequest_UserAndTruncationTopLevel(t *testing.T) {
	a := &CodexAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:    "gpt-5.2",
		Messages: []map[string]interface{}{{"role": "user", "content": "hi"}},
		Metadata: map[string]interface{}{
			"user":       "user_audit_001",
			"truncation": "auto",
		},
	})
	if req["user"] != "user_audit_001" {
		t.Fatalf("expected user=user_audit_001, got %#v", req["user"])
	}
	if req["truncation"] != "auto" {
		t.Fatalf("expected truncation=auto, got %#v", req["truncation"])
	}
	// 消费键不得泄漏进 metadata。
	if md, ok := req["metadata"].(map[string]string); ok {
		if _, leaked := md["user"]; leaked {
			t.Fatalf("user leaked into metadata: %#v", md)
		}
		if _, leaked := md["truncation"]; leaked {
			t.Fatalf("truncation leaked into metadata: %#v", md)
		}
	}

	// 非法 truncation 取值被丢弃。
	bad := a.BuildRequest(RequestConfig{
		Model:    "gpt-5.2",
		Messages: []map[string]interface{}{{"role": "user", "content": "hi"}},
		Metadata: map[string]interface{}{"truncation": "sometimes"},
	})
	if _, exists := bad["truncation"]; exists {
		t.Fatalf("did not expect invalid truncation: %#v", bad["truncation"])
	}
}

func TestCodexBuildRequest_ExtraBodyMerged(t *testing.T) {
	a := &CodexAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:     "gpt-5.2",
		Messages:  []map[string]interface{}{{"role": "user", "content": "hi"}},
		MaxTokens: 512,
		Metadata: map[string]interface{}{
			"extra_body": map[string]interface{}{
				"custom_field":      "custom_value",
				"max_output_tokens": 9999, // 已存在键,不覆盖
			},
		},
	})
	if req["custom_field"] != "custom_value" {
		t.Fatalf("expected extra_body custom_field merged, got %#v", req["custom_field"])
	}
	// 已存在的键不被 extra_body 覆盖。
	if req["max_output_tokens"] != 512 {
		t.Fatalf("expected max_output_tokens=512 preserved, got %#v", req["max_output_tokens"])
	}
	// extra_body 不得泄漏进 metadata。
	if md, ok := req["metadata"].(map[string]string); ok {
		if _, leaked := md["extra_body"]; leaked {
			t.Fatalf("extra_body leaked into metadata: %#v", md)
		}
	}
}
func TestCodexBuildRequest_NormalizesToolChoice(t *testing.T) {
	a := &CodexAdapter{}
	base := RequestConfig{
		Model:    "gpt-5.2",
		Messages: []map[string]interface{}{{"role": "user", "content": "hi"}},
		Functions: []map[string]interface{}{
			{
				"type":        "function",
				"name":        "ls",
				"description": "list files",
				"parameters":  map[string]interface{}{"type": "object"},
			},
		},
	}

	// Chat 嵌套风格 {"type":"function","function":{"name":"x"}} 展平为
	// Responses 形态 {"type":"function","name":"x"}。
	chatStyle := base
	chatStyle.ToolChoice = map[string]interface{}{
		"type":     "function",
		"function": map[string]interface{}{"name": "ls"},
	}
	req := a.BuildRequest(chatStyle)
	tc, ok := req["tool_choice"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected tool_choice object, got %#v", req["tool_choice"])
	}
	if tc["type"] != "function" || tc["name"] != "ls" {
		t.Fatalf("expected flattened tool_choice, got %#v", tc)
	}
	if _, nested := tc["function"]; nested {
		t.Fatalf("expected nested function key removed, got %#v", tc)
	}

	// Responses 原生风格原样透传。
	responsesStyle := base
	responsesStyle.ToolChoice = map[string]interface{}{"type": "function", "name": "ls"}
	req = a.BuildRequest(responsesStyle)
	tc, ok = req["tool_choice"].(map[string]interface{})
	if !ok || tc["name"] != "ls" || len(tc) != 2 {
		t.Fatalf("expected responses-style tool_choice passthrough, got %#v", req["tool_choice"])
	}

	// 字符串形态透传。
	strStyle := base
	strStyle.ToolChoice = "auto"
	req = a.BuildRequest(strStyle)
	if req["tool_choice"] != "auto" {
		t.Fatalf("expected tool_choice=auto, got %#v", req["tool_choice"])
	}
}

func TestCodexStream_OutputItemIDFallback(t *testing.T) {
	a := &CodexAdapter{}
	sseData := strings.Join([]string{
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_no_call_id","name":"ls","arguments":""}}`,
		"",
		"event: response.function_call_arguments.delta",
		`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"path\":\"/tmp\""}`,
		"",
		"event: response.function_call_arguments.delta",
		`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"}"}`,
		"",
		"event: response.output_item.done",
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_no_call_id","name":"ls","arguments":"{\"path\":\"/tmp\"}"}}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_fb","status":"completed","stop_reason":"tool_call"}}`,
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
	// 网关只给 item id 时,兜底用作 call_id,不允许空 id 泄漏到输出。
	if toolCalls[0]["id"] != "fc_no_call_id" {
		t.Fatalf("expected id fallback from item id, got %#v", toolCalls[0])
	}
}

func TestCodexBuildFunctionCallItem_MissingCallIDDerived(t *testing.T) {
	item := buildCodexFunctionCallItem(map[string]interface{}{
		"type":      "function_call",
		"name":      "ls",
		"arguments": `{"path":"/tmp"}`,
	})
	if item == nil {
		t.Fatal("expected non-nil item when name is present but call_id missing")
	}
	id, _ := item["id"].(string)
	if !strings.HasPrefix(id, "fc_") || len(id) <= len("fc_") {
		t.Fatalf("expected derived item id with fc_ prefix, got %#v", item["id"])
	}
	callID, _ := item["call_id"].(string)
	if !strings.HasPrefix(callID, "call_") || len(callID) <= len("call_") {
		t.Fatalf("expected derived call id with call_ prefix, got %#v", item["call_id"])
	}

	// 相同 name+arguments 必须派生相同 id(跨轮次重放稳定)。
	item2 := buildCodexFunctionCallItem(map[string]interface{}{
		"type":      "function_call",
		"name":      "ls",
		"arguments": `{"path":"/tmp"}`,
	})
	if item2["id"] != id {
		t.Fatalf("expected stable derived id across calls, got %#v vs %#v", item2["id"], id)
	}
	if item2["call_id"] != callID {
		t.Fatalf("expected stable derived call_id across calls, got %#v vs %#v", item2["call_id"], callID)
	}

	// 缺 name 仍丢弃(没有名字就无法执行工具)。
	nameless := buildCodexFunctionCallItem(map[string]interface{}{
		"type":      "function_call",
		"arguments": "{}",
	})
	if nameless != nil {
		t.Fatalf("expected nil when name missing, got %#v", nameless)
	}
}

func TestCodexEnsureInputItemID_NormalizesToolCallIDPrefix(t *testing.T) {
	cases := []struct {
		name       string
		itemType   string
		callID     string
		wantPrefix string
	}{
		{"function_call", "function_call", "call_1", "fc_"},
		{"function_call_output", "function_call_output", "call_1", "fc_"},
		{"function_call realistic", "function_call", "call_P4QjA9D8eJJwmXp9ImxEMtRg", "fc_"},
		{"custom_tool_call", "custom_tool_call", "call_patch_1", "ctc_"},
		{"custom_tool_call_output", "custom_tool_call_output", "call_patch_1", "ctc_"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := map[string]interface{}{
				"type":    tc.itemType,
				"call_id": tc.callID,
			}
			got := ensureCodexInputItemID(item)
			id, _ := got["id"].(string)
			if !strings.HasPrefix(id, tc.wantPrefix) {
				t.Fatalf("expected item id to start with %q, got %#v", tc.wantPrefix, got)
			}
			if got["call_id"] != tc.callID {
				t.Fatalf("expected call_id preserved, got %#v", got)
			}
		})
	}

	// Valid upstream item ids are preserved, while call_-prefixed ids are
	// rewritten even when the id field is already populated.
	valid := map[string]interface{}{
		"type": "function_call", "id": "fc_view", "call_id": "call_view",
		"name": "ls", "arguments": "{}",
	}
	if got := ensureCodexInputItemID(valid); got["id"] != "fc_view" {
		t.Fatalf("expected valid fc_ item id preserved, got %#v", got)
	}
	legacy := map[string]interface{}{
		"type": "function_call", "id": "call_P4QjA9D8eJJwmXp9ImxEMtRg", "call_id": "call_P4QjA9D8eJJwmXp9ImxEMtRg",
		"name": "ls", "arguments": "{}",
	}
	if got := ensureCodexInputItemID(legacy); got["id"] != "fc_P4QjA9D8eJJwmXp9ImxEMtRg" {
		t.Fatalf("expected call_ item id rewritten to fc_P4QjA9D8eJJwmXp9ImxEMtRg, got %#v", got)
	}
}

func TestCodexNonStreamResponse_CallIDFallback(t *testing.T) {
	a := &CodexAdapter{}

	// 只有 item id 没有 call_id:用 id 兜底。
	body := `{"id":"resp_ns","status":"completed","stop_reason":"tool_call","output":[{"type":"function_call","id":"fc_ns","name":"ls","arguments":"{}"}]}`
	msg, err := a.handleCodexNonStreamResponse(strings.NewReader(body), StreamCallbacks{})
	if err != nil {
		t.Fatalf("handleCodexNonStreamResponse failed: %v", err)
	}
	toolCalls, ok := msg["tool_calls"].([]map[string]interface{})
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %T %#v", msg["tool_calls"], msg["tool_calls"])
	}
	if toolCalls[0]["id"] != "fc_ns" {
		t.Fatalf("expected id fallback from item id, got %#v", toolCalls[0])
	}

	// call_id 与 id 全缺:派生稳定 id,不允许空 id 泄漏。
	body2 := `{"id":"resp_ns2","status":"completed","stop_reason":"tool_call","output":[{"type":"function_call","name":"grep","arguments":"{}"}]}`
	msg2, err := a.handleCodexNonStreamResponse(strings.NewReader(body2), StreamCallbacks{})
	if err != nil {
		t.Fatalf("handleCodexNonStreamResponse failed: %v", err)
	}
	toolCalls2, ok := msg2["tool_calls"].([]map[string]interface{})
	if !ok || len(toolCalls2) != 1 {
		t.Fatalf("expected 1 tool call, got %T %#v", msg2["tool_calls"], msg2["tool_calls"])
	}
	id2, _ := toolCalls2[0]["id"].(string)
	if !strings.HasPrefix(id2, "call_") {
		t.Fatalf("expected derived id when call_id/id missing, got %#v", toolCalls2[0])
	}
}

func TestCodexInput_ToolMessageNonStringContentSerialized(t *testing.T) {
	a := &CodexAdapter{}
	input := a.convertMessagesToCodexInput([]map[string]interface{}{
		{"role": "user", "content": "检查文件"},
		{
			"role": "assistant",
			"tool_calls": []map[string]interface{}{
				{
					"id":   "call_struct",
					"type": "function",
					"function": map[string]interface{}{
						"name":      "inspect",
						"arguments": `{"path":"a.go"}`,
					},
				},
			},
		},
		{
			"role":         "tool",
			"tool_call_id": "call_struct",
			"content": map[string]interface{}{
				"ok":     true,
				"lines":  []interface{}{"package main", "func main() {}"},
				"size":   42,
				"detail": nil,
			},
		},
	})
	if len(input) != 3 {
		t.Fatalf("expected 3 input items, got %d: %#v", len(input), input)
	}
	item := input[2]
	if item["type"] != "function_call_output" {
		t.Fatalf("expected function_call_output item, got %#v", input[2])
	}
	output, _ := item["output"].(string)
	if !strings.Contains(output, `"ok":true`) || !strings.Contains(output, `"size":42`) {
		t.Fatalf("expected JSON-serialized structured tool content, got %q", output)
	}
	if !strings.Contains(output, `"lines"`) || !strings.Contains(output, "func main") {
		t.Fatalf("expected nested array content serialized, got %q", output)
	}
	// call_id 兜底保持回环。
	if callID, _ := item["call_id"].(string); callID != "call_struct" {
		t.Fatalf("expected call_id preserved, got %#v", item)
	}
}

func TestCodexInput_MessageNamePreserved(t *testing.T) {
	a := &CodexAdapter{}
	input := a.convertMessagesToCodexInput([]map[string]interface{}{
		{"role": "user", "content": "你好", "name": "alice"},
		{"role": "assistant", "content": "收到", "name": "bob"},
		{"role": "developer", "content": "目标:重构", "name": "orchestrator"},
	})
	if len(input) != 3 {
		t.Fatalf("expected 3 input items, got %d: %#v", len(input), input)
	}
	wantNames := []string{"alice", "bob", "orchestrator"}
	for i, want := range wantNames {
		item := input[i]
		if got, _ := item["name"].(string); got != want {
			t.Fatalf("item %d: expected name %q, got %#v", i, want, item)
		}
	}
	// 无 name 时不应凭空出现空串 name。
	input2 := a.convertMessagesToCodexInput([]map[string]interface{}{
		{"role": "user", "content": "hi"},
	})
	if item := input2[0]; item["name"] != nil {
		t.Fatalf("expected no name key for unnamed message, got %#v", item)
	}
}

func TestCodexInput_AssistantContentPartsPreserved(t *testing.T) {
	a := &CodexAdapter{}
	input := a.convertMessagesToCodexInput([]map[string]interface{}{
		{"role": "user", "content": "继续"},
		{
			"role": "assistant",
			"name": "subagent",
			"content": []map[string]interface{}{
				{
					"type": "output_text",
					"text": "调查完成",
					"annotations": []map[string]interface{}{
						{"type": "file_citation", "file_id": "file_1", "index": 0},
					},
				},
				{
					"type":      "input_image",
					"image_url": "data:image/png;base64,AAAA",
					"detail":    "low",
				},
			},
		},
	})
	if len(input) != 2 {
		t.Fatalf("expected 2 input items, got %d: %#v", len(input), input)
	}
	item := input[1]
	if item["type"] != "message" || item["role"] != "assistant" {
		t.Fatalf("expected assistant message item, got %#v", input[1])
	}
	if name, _ := item["name"].(string); name != "subagent" {
		t.Fatalf("expected assistant name preserved, got %#v", item)
	}
	parts, ok := item["content"].([]map[string]interface{})
	if !ok || len(parts) != 2 {
		t.Fatalf("expected 2 content parts, got %#v", item["content"])
	}
	first := parts[0]
	if first["type"] != "output_text" {
		t.Fatalf("expected output_text part preserved, got %#v", parts[0])
	}
	annotations, ok := first["annotations"].([]map[string]interface{})
	if !ok || len(annotations) != 1 {
		t.Fatalf("expected annotations preserved, got %#v", first)
	}
	if annType, _ := annotations[0]["type"].(string); annType != "file_citation" {
		t.Fatalf("expected file_citation annotation, got %#v", annotations[0])
	}
	second := parts[1]
	if second["type"] != "input_image" {
		t.Fatalf("expected input_image part preserved, got %#v", parts[1])
	}
	if detail, _ := second["detail"].(string); detail != "low" {
		t.Fatalf("expected detail=low preserved, got %#v", second)
	}
}

func TestCodexInput_FunctionCallOutputPairsAdjacent(t *testing.T) {
	a := &CodexAdapter{}
	// 官方 Responses 要求 function_call 与对应 function_call_output 成对相邻:
	// 交错为 fc_1, fco_1, fc_2, fco_2,而不是 fc_1, fc_2, fco_1, fco_2。
	input := a.convertMessagesToCodexInput([]map[string]interface{}{
		{"role": "user", "content": "并行执行两个命令"},
		{
			"role": "assistant",
			"tool_calls": []map[string]interface{}{
				{
					"id":   "call_a",
					"type": "function",
					"function": map[string]interface{}{
						"name":      "shell",
						"arguments": `{"command":"git status"}`,
					},
				},
				{
					"id":   "call_b",
					"type": "function",
					"function": map[string]interface{}{
						"name":      "view",
						"arguments": `{"file_path":"a.go"}`,
					},
				},
			},
		},
		{"role": "tool", "tool_call_id": "call_a", "content": "M codex.go"},
		{"role": "tool", "tool_call_id": "call_b", "content": "package main"},
	})
	if len(input) != 5 {
		t.Fatalf("expected 5 input items, got %d: %#v", len(input), input)
	}
	// 期望交错顺序: user, fc_a, fco_a, fc_b, fco_b
	wantSequence := []string{
		"user", "function_call", "function_call_output",
		"function_call", "function_call_output",
	}
	for i, want := range wantSequence {
		item := input[i]
		if i == 0 {
			if item["role"] != want {
				t.Fatalf("item %d: expected role %q, got %#v", i, want, item)
			}
			continue
		}
		if item["type"] != want {
			t.Fatalf("item %d: expected type %q, got %#v", i, want, item)
		}
	}
	if callID := input[1]["call_id"]; callID != "call_a" {
		t.Fatalf("expected fc_a first, got %#v", input[1])
	}
	if callID := input[2]["call_id"]; callID != "call_a" {
		t.Fatalf("expected fco_a adjacent to fc_a, got %#v", input[2])
	}
	if callID := input[3]["call_id"]; callID != "call_b" {
		t.Fatalf("expected fc_b second, got %#v", input[3])
	}
	if callID := input[4]["call_id"]; callID != "call_b" {
		t.Fatalf("expected fco_b adjacent to fc_b, got %#v", input[4])
	}
}

func TestCodexInput_UnpairedFunctionCallAppendedAtEnd(t *testing.T) {
	a := &CodexAdapter{}
	// 历史中工具结果缺失时,未配对的 function_call 不应丢失;遇到下一条
	// 非 tool 消息时先 flush,保持 assistant(tool_calls) 之后的相对顺序。
	input := a.convertMessagesToCodexInput([]map[string]interface{}{
		{"role": "user", "content": "跑一下"},
		{
			"role": "assistant",
			"tool_calls": []map[string]interface{}{
				{
					"id":   "call_orphan",
					"type": "function",
					"function": map[string]interface{}{
						"name":      "shell",
						"arguments": `{"command":"echo hi"}`,
					},
				},
			},
		},
		{"role": "user", "content": "继续"},
	})
	if len(input) != 3 {
		t.Fatalf("expected 3 input items, got %d: %#v", len(input), input)
	}
	if input[1]["type"] != "function_call" || input[1]["name"] != "shell" {
		t.Fatalf("expected orphan function_call flushed before next message, got %#v", input[1])
	}
	if input[2]["role"] != "user" {
		t.Fatalf("expected trailing user message after flushed call, got %#v", input[2])
	}
}

func TestCodexNonStreamResponse_CustomToolCallIDFallback(t *testing.T) {
	a := &CodexAdapter{}
	// 非流式 custom_tool_call:call_id/id/input 全缺时派生稳定 id,
	// input/arguments 回退兜底,不允许整条调用丢失 id。
	body := `{"id":"resp_ctc","status":"completed","stop_reason":"tool_call","output":[{"type":"custom_tool_call","name":"apply_patch"}]}`
	msg, err := a.handleCodexNonStreamResponse(strings.NewReader(body), StreamCallbacks{})
	if err != nil {
		t.Fatalf("handleCodexNonStreamResponse failed: %v", err)
	}
	toolCalls, ok := msg["tool_calls"].([]map[string]interface{})
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("expected 1 custom tool call, got %T %#v", msg["tool_calls"], msg["tool_calls"])
	}
	tc := toolCalls[0]
	if tc["type"] != "custom_tool_call" {
		t.Fatalf("expected custom_tool_call type, got %#v", tc)
	}
	id, _ := tc["id"].(string)
	if !strings.HasPrefix(id, "call_") {
		t.Fatalf("expected derived id when call_id/id missing, got %#v", tc)
	}
	if input, _ := tc["input"].(string); input != "" {
		t.Fatalf("expected empty input fallback, got %#v", tc)
	}
	if args, _ := tc["arguments"].(string); args != "" {
		t.Fatalf("expected empty arguments fallback, got %#v", tc)
	}

	// arguments 承载输入时回退到 arguments。
	body2 := `{"id":"resp_ctc2","status":"completed","stop_reason":"tool_call","output":[{"type":"custom_tool_call","call_id":"call_patch","name":"apply_patch","arguments":"PATCH CONTENT"}]}`
	msg2, err := a.handleCodexNonStreamResponse(strings.NewReader(body2), StreamCallbacks{})
	if err != nil {
		t.Fatalf("handleCodexNonStreamResponse failed: %v", err)
	}
	toolCalls2 := msg2["tool_calls"].([]map[string]interface{})
	if input, _ := toolCalls2[0]["input"].(string); input != "PATCH CONTENT" {
		t.Fatalf("expected arguments fallback into input, got %#v", toolCalls2[0])
	}
	if id, _ := toolCalls2[0]["id"].(string); id != "call_patch" {
		t.Fatalf("expected call_id preserved as id, got %#v", toolCalls2[0])
	}
}

func TestCodexInput_ContentPartNamePreserved(t *testing.T) {
	a := &CodexAdapter{}
	// 官方 Responses content part(input_text/output_text/input_image/input_file/
	// input_audio/input_video)支持可选 name(多 agent 场景),归一化时不应丢弃。
	input := a.convertMessagesToCodexInput([]map[string]interface{}{
		{
			"role": "user",
			"content": []map[string]interface{}{
				{"type": "input_text", "text": "帮我查一下", "name": "planner"},
				{
					"type":      "input_image",
					"image_url": "data:image/png;base64,BBBB",
					"detail":    "high",
					"name":      "screenshot",
				},
				{
					"type":      "input_file",
					"filename":  "data.json",
					"file_data": "data:application/json;base64,e30=",
					"name":      "fixture",
				},
				{
					"type":        "input_audio",
					"input_audio": "data:audio/wav;base64,UklGRg==",
					"format":      "wav",
					"name":        "voice_note",
				},
				{
					"type":      "input_video",
					"video_url": "https://example.com/clip.mp4",
					"name":      "clip",
				},
			},
		},
	})
	if len(input) != 1 {
		t.Fatalf("expected 1 input item, got %d: %#v", len(input), input)
	}
	parts, ok := input[0]["content"].([]map[string]interface{})
	if !ok || len(parts) != 5 {
		t.Fatalf("expected 5 content parts, got %#v", input[0]["content"])
	}
	wantNames := []string{"planner", "screenshot", "fixture", "voice_note", "clip"}
	for i, want := range wantNames {
		part := parts[i]
		if got, _ := part["name"].(string); got != want {
			t.Fatalf("part %d (%v): expected name %q, got %#v", i, part["type"], want, part)
		}
	}
	// 无 name 的 part 不应凭空出现空串 name。
	input2 := a.convertMessagesToCodexInput([]map[string]interface{}{
		{
			"role": "user",
			"content": []map[string]interface{}{
				{"type": "input_text", "text": "hi"},
			},
		},
	})
	parts2 := input2[0]["content"].([]map[string]interface{})
	if parts2[0]["name"] != nil {
		t.Fatalf("expected no name key for unnamed part, got %#v", parts2[0])
	}
}

func TestCodexHandleResponse_StreamOutputItemAddedKeepsMessageMeta(t *testing.T) {
	a := &CodexAdapter{}
	// output_item.added 中 message item 的 name/status 应原样保留在
	// response_output_items 中,供后续回放使用(多 agent 场景)。
	sseData := strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","response":{"id":"resp_meta","model":"gpt-5.2-codex"}}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","role":"assistant","name":"subagent","status":"in_progress","content":[]}}`,
		"",
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","output_index":0,"delta":"Hello"}`,
		"",
		"event: response.output_item.done",
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","name":"subagent","status":"completed","content":[{"type":"output_text","text":"Hello"}]}}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_meta","status":"completed","stop_reason":"end_turn"}}`,
		"",
	}, "\n")

	msg, err := a.HandleResponse(true, strings.NewReader(sseData), StreamCallbacks{})
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	outputItems, ok := msg["response_output_items"].([]map[string]interface{})
	if !ok || len(outputItems) != 1 {
		t.Fatalf("expected 1 output item, got %T %#v", msg["response_output_items"], msg["response_output_items"])
	}
	item := outputItems[0]
	if name, _ := item["name"].(string); name != "subagent" {
		t.Fatalf("expected message name preserved in output items, got %#v", item)
	}
	if status, _ := item["status"].(string); status != "completed" {
		t.Fatalf("expected message status preserved in output items, got %#v", item)
	}
	if role, _ := item["role"].(string); role != "assistant" {
		t.Fatalf("expected message role preserved, got %#v", item)
	}
}

func TestCodexNonStreamResponse_MessageNameAndStatusPreserved(t *testing.T) {
	a := &CodexAdapter{}
	body := `{"id":"resp_ns_meta","status":"completed","stop_reason":"end_turn","output":[{"type":"message","role":"assistant","name":"subagent","status":"completed","content":[{"type":"output_text","text":"done"}]}]}`
	msg, err := a.handleCodexNonStreamResponse(strings.NewReader(body), StreamCallbacks{})
	if err != nil {
		t.Fatalf("handleCodexNonStreamResponse failed: %v", err)
	}
	rawItems, ok := msg["response_output_items"].([]interface{})
	if !ok || len(rawItems) != 1 {
		t.Fatalf("expected 1 output item, got %T %#v", msg["response_output_items"], msg["response_output_items"])
	}
	item, ok := rawItems[0].(map[string]interface{})
	if !ok {
		t.Fatalf("expected map output item, got %T %#v", rawItems[0], rawItems[0])
	}
	if name, _ := item["name"].(string); name != "subagent" {
		t.Fatalf("expected message name preserved in non-stream output items, got %#v", item)
	}
	if status, _ := item["status"].(string); status != "completed" {
		t.Fatalf("expected message status preserved in non-stream output items, got %#v", item)
	}
	if content, _ := msg["content"].(string); content != "done" {
		t.Fatalf("expected flat content extracted, got %#v", msg["content"])
	}
}

func TestCodexStream_UsageUpdatedEventMerged(t *testing.T) {
	a := &CodexAdapter{}
	// 官方 response.usage.updated 事件(2025-06 新增)在流式过程中多次触发,
	// 末尾携带完整 usage。即使 response.completed 快照不含 usage,也应合并。
	sseData := strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","response":{"id":"resp_usage","model":"gpt-5.2-codex"}}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","role":"assistant","content":[]}}`,
		"",
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","output_index":0,"delta":"hi"}`,
		"",
		"event: response.usage.updated",
		`data: {"type":"response.usage.updated","usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}`,
		"",
		"event: response.usage.updated",
		`data: {"type":"response.usage.updated","usage":{"input_tokens":12,"output_tokens":5,"total_tokens":17,"input_tokens_details":{"cached_tokens":8}}}`,
		"",
		"event: response.output_item.done",
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_usage","status":"completed","stop_reason":"end_turn"}}`,
		"",
	}, "\n")

	msg, err := a.HandleResponse(true, strings.NewReader(sseData), StreamCallbacks{})
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	usage, ok := msg["usage"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected usage in result, got %T %#v", msg["usage"], msg["usage"])
	}
	if input, _ := usage["input_tokens"].(int64); input != 12 {
		t.Fatalf("expected input_tokens=12, got %#v", usage["input_tokens"])
	}
	if output, _ := usage["output_tokens"].(int64); output != 5 {
		t.Fatalf("expected output_tokens=5, got %#v", usage["output_tokens"])
	}
	details, ok := usage["input_tokens_details"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected input_tokens_details preserved, got %#v", usage)
	}
	if cached, _ := details["cached_tokens"].(float64); cached != 8 {
		t.Fatalf("expected cached_tokens=8, got %#v", details)
	}
	if msg["sse_unknown_events"] != nil {
		t.Fatalf("usage.updated must not be recorded as unknown, got %#v", msg["sse_unknown_events"])
	}
}

func TestCodexStream_AnnotationDeltaMerged(t *testing.T) {
	a := &CodexAdapter{}
	// 官方 response.output_text.annotation.delta 事件流式补充 url_citation 的
	// url(added 事件中 url 为空),多段 delta 拼接后回填 annotation。
	sseData := strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","response":{"id":"resp_ann","model":"gpt-5.2-codex"}}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","role":"assistant","content":[]}}`,
		"",
		"event: response.content_part.added",
		`data: {"type":"response.content_part.added","output_index":0,"content_index":0,"part":{"type":"output_text","text":"see ref"}}`,
		"",
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","output_index":0,"delta":"see ref"}`,
		"",
		"event: response.output_text.annotation.added",
		`data: {"type":"response.output_text.annotation.added","output_index":0,"content_index":0,"annotation":{"index":0,"type":"url_citation","url":""}}`,
		"",
		"event: response.output_text.annotation.delta",
		`data: {"type":"response.output_text.annotation.delta","output_index":0,"content_index":0,"annotation_index":0,"delta":"https://example.com/a"}`,
		"",
		"event: response.output_text.annotation.delta",
		`data: {"type":"response.output_text.annotation.delta","output_index":0,"content_index":0,"annotation_index":0,"delta":"b/c.html"}`,
		"",
		"event: response.content_part.done",
		`data: {"type":"response.content_part.done","output_index":0,"content_index":0,"part":{"type":"output_text","text":"see ref","annotations":[{"index":0,"type":"url_citation","url":"https://example.com/ab/c.html"}]}}`,
		"",
		"event: response.output_item.done",
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"see ref","annotations":[{"index":0,"type":"url_citation","url":"https://example.com/ab/c.html"}]}]}}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_ann","status":"completed","stop_reason":"end_turn"}}`,
		"",
	}, "\n")

	msg, err := a.HandleResponse(true, strings.NewReader(sseData), StreamCallbacks{})
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	metadata := decodeMap(msg["metadata"])
	annotations, ok := metadata["annotations"].([]map[string]interface{})
	if !ok || len(annotations) != 1 {
		t.Fatalf("expected 1 annotation, got %T %#v", metadata["annotations"], metadata["annotations"])
	}
	if url, _ := annotations[0]["url"].(string); url != "https://example.com/ab/c.html" {
		t.Fatalf("expected delta-merged url, got %#v", annotations[0])
	}
	if msg["sse_unknown_events"] != nil {
		t.Fatalf("annotation.delta must not be recorded as unknown, got %#v", msg["sse_unknown_events"])
	}
}

func TestCodexStream_ItemSafetyEventsCollected(t *testing.T) {
	a := &CodexAdapter{}
	// 官方 response.item_safety.message.* 事件族携带内容安全过滤信息,
	// 应去重累积到结果 item_safety 字段,而不是静默丢弃。
	sseData := strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","response":{"id":"resp_safe","model":"gpt-5.2-codex"}}`,
		"",
		"event: response.item_safety.message.delta",
		`data: {"type":"response.item_safety.message.delta","item_id":"msg_1","output_index":0,"item_safety":[{"code":"R","reason":"violence"}]}`,
		"",
		"event: response.item_safety.message.done",
		`data: {"type":"response.item_safety.message.done","item_id":"msg_1","output_index":0,"item_safety":[{"code":"R","reason":"violence"}]}`,
		"",
		"event: response.item_safety.message.part.delta",
		`data: {"type":"response.item_safety.message.part.delta","item_id":"msg_1","output_index":0,"item_safety":[{"code":"R","reason":"hate"}]}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_safe","status":"completed","stop_reason":"end_turn"}}`,
		"",
	}, "\n")

	msg, err := a.HandleResponse(true, strings.NewReader(sseData), StreamCallbacks{})
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	metadata := decodeMap(msg["metadata"])
	items, ok := metadata["item_safety"].([]map[string]interface{})
	if !ok || len(items) != 2 {
		t.Fatalf("expected 2 deduped item_safety records, got %T %#v", metadata["item_safety"], metadata["item_safety"])
	}
	seen := map[string]bool{}
	for _, item := range items {
		seen[asCodexString(item["reason"])] = true
	}
	if !seen["violence"] || !seen["hate"] {
		t.Fatalf("expected both reasons, got %#v", items)
	}
	if msg["sse_unknown_events"] != nil {
		t.Fatalf("item_safety events must not be recorded as unknown, got %#v", msg["sse_unknown_events"])
	}
}

func TestCodexStream_AnnotationDeltaBeforeAddedMerged(t *testing.T) {
	a := &CodexAdapter{}
	// 顺序保护:delta 先于 annotation.added 到达时,added 后仍应回填。
	sseData := strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","response":{"id":"resp_ann2","model":"gpt-5.2-codex"}}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","role":"assistant","content":[]}}`,
		"",
		"event: response.output_text.annotation.delta",
		`data: {"type":"response.output_text.annotation.delta","output_index":0,"content_index":0,"annotation_index":0,"delta":"https://early.example/x"}`,
		"",
		"event: response.content_part.added",
		`data: {"type":"response.content_part.added","output_index":0,"content_index":0,"part":{"type":"output_text","text":"t","annotations":[{"index":0,"type":"url_citation","url":""}]}}`,
		"",
		"event: response.content_part.done",
		`data: {"type":"response.content_part.done","output_index":0,"content_index":0,"part":{"type":"output_text","text":"t","annotations":[{"index":0,"type":"url_citation","url":"https://early.example/x"}]}}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_ann2","status":"completed","stop_reason":"end_turn"}}`,
		"",
	}, "\n")

	msg, err := a.HandleResponse(true, strings.NewReader(sseData), StreamCallbacks{})
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	metadata := decodeMap(msg["metadata"])
	annotations, ok := metadata["annotations"].([]map[string]interface{})
	if !ok || len(annotations) != 1 {
		t.Fatalf("expected 1 annotation, got %T %#v", metadata["annotations"], metadata["annotations"])
	}
	if url, _ := annotations[0]["url"].(string); url != "https://early.example/x" {
		t.Fatalf("expected early delta merged after added, got %#v", annotations[0])
	}
}

func TestCodexToolCallInputString(t *testing.T) {
	// 官方 custom_tool_call.input 接受任意 JSON 值:字符串原样保留,
	// 对象/数组等结构化输入按 JSON 序列化,避免 fmt.Sprint 打出 Go map 垃圾串。
	cases := []struct {
		name string
		raw  interface{}
		want string
	}{
		{"string passthrough", "*** Begin Patch", "*** Begin Patch"},
		{"nil", nil, ""},
		{"object", map[string]interface{}{"command": "ls", "args": []interface{}{"-l"}}, `{"args":["-l"],"command":"ls"}`},
		{"array", []interface{}{"a", float64(1)}, `["a",1]`},
		{"number", float64(42), `42`},
		{"bool", true, `true`},
	}
	for _, tc := range cases {
		if got := codexToolCallInputString(tc.raw); got != tc.want {
			t.Fatalf("%s: expected %q, got %q", tc.name, tc.want, got)
		}
	}
}

func TestCodexNonStreamResponse_CustomToolCallObjectInput(t *testing.T) {
	a := &CodexAdapter{}
	// 非流式 custom_tool_call 的 input 为 JSON 对象(官方允许任意 JSON 值),
	// 应序列化为 JSON 字符串而不是 map 语法垃圾串;arguments 同步承载。
	body := `{"id":"resp_ctc_obj","status":"completed","stop_reason":"tool_call","output":[{"type":"custom_tool_call","id":"item_ctc_1","name":"execute_shell_command","input":{"command":"ls","args":["-l"]}}]}`
	msg, err := a.handleCodexNonStreamResponse(strings.NewReader(body), StreamCallbacks{})
	if err != nil {
		t.Fatalf("handleCodexNonStreamResponse failed: %v", err)
	}
	toolCalls, ok := msg["tool_calls"].([]map[string]interface{})
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("expected 1 custom tool call, got %T %#v", msg["tool_calls"], msg["tool_calls"])
	}
	tc := toolCalls[0]
	wantInput := `{"args":["-l"],"command":"ls"}`
	if input, _ := tc["input"].(string); input != wantInput {
		t.Fatalf("expected input serialized as JSON string, got %#v", tc["input"])
	}
	if args, _ := tc["arguments"].(string); args != wantInput {
		t.Fatalf("expected arguments mirror input, got %#v", tc["arguments"])
	}
	if id, _ := tc["id"].(string); id != "item_ctc_1" {
		t.Fatalf("expected item id preserved, got %#v", tc["id"])
	}
}

func TestCodexStream_CustomToolCallObjectInput(t *testing.T) {
	a := &CodexAdapter{}
	// 流式:custom_tool_call_input.done 与 output_item.done 快照中的 input
	// 均为 JSON 对象,应序列化为 JSON 字符串;delta 分段与对象快照不冲突。
	sseData := strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","response":{"id":"resp_ctc_obj","model":"gpt-5.2-codex"}}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","item":{"type":"custom_tool_call","id":"item_ctc_1","call_id":"call_ctc_1","name":"execute_shell_command","input":"","status":"in_progress"}}`,
		"",
		"event: response.custom_tool_call_input.delta",
		`data: {"type":"response.custom_tool_call_input.delta","item_id":"item_ctc_1","call_id":"call_ctc_1","delta":"{\"command\":"}`,
		"",
		"event: response.custom_tool_call_input.done",
		`data: {"type":"response.custom_tool_call_input.done","item_id":"item_ctc_1","call_id":"call_ctc_1","input":{"command":"ls","args":["-l"]}}`,
		"",
		"event: response.output_item.done",
		`data: {"type":"response.output_item.done","item":{"type":"custom_tool_call","id":"item_ctc_1","call_id":"call_ctc_1","name":"execute_shell_command","input":{"command":"ls","args":["-l"]},"status":"completed"}}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_ctc_obj","status":"completed","stop_reason":"end_turn"}}`,
		"",
	}, "\n")

	msg, err := a.HandleResponse(true, strings.NewReader(sseData), StreamCallbacks{})
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	toolCalls, ok := msg["tool_calls"].([]map[string]interface{})
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("expected 1 custom tool call, got %T %#v", msg["tool_calls"], msg["tool_calls"])
	}
	wantInput := `{"args":["-l"],"command":"ls"}`
	if got, _ := toolCalls[0]["input"].(string); got != wantInput {
		t.Fatalf("expected object input serialized as JSON, got %#v", toolCalls[0]["input"])
	}
	// 流式 custom_tool_call 与官方 output item 一致:只暴露 input,
	// 不生成多余的 arguments 键。
	if _, exists := toolCalls[0]["arguments"]; exists {
		t.Fatalf("expected no arguments key for streamed custom_tool_call, got %#v", toolCalls[0])
	}
}
