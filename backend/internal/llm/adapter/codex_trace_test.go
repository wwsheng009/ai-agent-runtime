package adapter

import (
	"strings"
	"testing"
)

func TestValidateCodexToolCallTraceRejectsInvalidPairs(t *testing.T) {
	tests := []struct {
		name       string
		input      []map[string]interface{}
		wantMarker string
	}{
		{
			name: "missing output",
			input: []map[string]interface{}{
				{"type": "function_call", "call_id": "call_missing"},
			},
			wantMarker: "tool_output_missing",
		},
		{
			name: "orphan output",
			input: []map[string]interface{}{
				{"type": "function_call_output", "call_id": "call_orphan"},
			},
			wantMarker: "codex_tool_trace_invalid",
		},
		{
			name: "duplicate call",
			input: []map[string]interface{}{
				{"type": "function_call", "call_id": "call_duplicate"},
				{"type": "function_call", "call_id": "call_duplicate"},
				{"type": "function_call_output", "call_id": "call_duplicate"},
			},
			wantMarker: "codex_tool_trace_invalid",
		},
		{
			name: "duplicate output",
			input: []map[string]interface{}{
				{"type": "function_call", "call_id": "call_duplicate_output"},
				{"type": "function_call_output", "call_id": "call_duplicate_output"},
				{"type": "function_call_output", "call_id": "call_duplicate_output"},
			},
			wantMarker: "codex_tool_trace_invalid",
		},
		{
			name: "call and output type mismatch",
			input: []map[string]interface{}{
				{"type": "custom_tool_call", "call_id": "call_type_mismatch"},
				{"type": "function_call_output", "call_id": "call_type_mismatch"},
			},
			wantMarker: "codex_tool_trace_invalid",
		},
		{
			name: "missing call id",
			input: []map[string]interface{}{
				{"type": "function_call", "name": "shell"},
			},
			wantMarker: "codex_tool_trace_invalid",
		},
		{
			name: "empty item",
			input: []map[string]interface{}{
				{},
			},
			wantMarker: "codex_tool_trace_invalid",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateCodexToolCallTrace(test.input)
			if err == nil {
				t.Fatalf("expected validation error")
			}
			if !strings.Contains(err.Error(), test.wantMarker) {
				t.Fatalf("expected %q in error, got %q", test.wantMarker, err)
			}
		})
	}
}

func TestValidateCodexToolCallTraceAcceptsFunctionAndCustomPairs(t *testing.T) {
	input := []map[string]interface{}{
		{"type": "function_call", "call_id": "call_function"},
		{"type": "function_call_output", "call_id": "call_function", "output": "ok"},
		{"type": "custom_tool_call", "call_id": "call_custom"},
		{"type": "custom_tool_call_output", "call_id": "call_custom", "output": "ok"},
	}
	if err := ValidateCodexToolCallTrace(input); err != nil {
		t.Fatalf("expected valid function/custom pairs, got %v", err)
	}
}

func TestCodexAdapterValidateRequestUsesNormalizedCallID(t *testing.T) {
	arguments := `{"command":"pwd"}`
	callID := stableCodexCallID("shell", arguments)
	a := &CodexAdapter{}

	err := a.ValidateRequest(RequestConfig{
		Messages: []map[string]interface{}{
			{
				"role": "assistant",
				"tool_calls": []map[string]interface{}{
					{
						// The source history omits id; the adapter derives callID
						// before the final Responses input is validated.
						"type": "function",
						"function": map[string]interface{}{
							"name":      "shell",
							"arguments": arguments,
						},
					},
				},
			},
			{
				"role":         "tool",
				"tool_call_id": callID,
				"content":      "ok",
			},
		},
	})
	if err != nil {
		t.Fatalf("expected normalized call id to pair with output, got %v", err)
	}
}

func TestCodexAdapterValidateRequestRejectsUnpairedFunctionCall(t *testing.T) {
	a := &CodexAdapter{}
	err := a.ValidateRequest(RequestConfig{
		Messages: []map[string]interface{}{
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
		},
	})
	if err == nil || !strings.Contains(err.Error(), "tool_output_missing") {
		t.Fatalf("expected missing output validation error, got %v", err)
	}
}
