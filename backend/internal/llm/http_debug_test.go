package llm

import "testing"

func TestBuildHTTPDebugRequestMetadataAddsStableFingerprints(t *testing.T) {
	requestBody := map[string]interface{}{
		"model": "gpt-5",
		"input": []map[string]interface{}{
			{
				"type": "message",
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type": "input_text",
						"text": "hello",
					},
				},
			},
		},
		"tools": []map[string]interface{}{
			{
				"type": "function",
				"name": "tool_a",
			},
			{
				"type": "function",
				"name": "tool_b",
			},
		},
		"instructions":     "system prompt",
		"prompt_cache_key": "session-1",
	}

	metaFirst := buildHTTPDebugRequestMetadata(map[string]interface{}{"trace_id": "trace-1", "prompt_layout": "[base/system]\nSystem prompt"}, "codex", requestBody)
	metaSecond := buildHTTPDebugRequestMetadata(map[string]interface{}{"trace_id": "trace-2", "prompt_layout": "[base/system]\nSystem prompt"}, "codex", requestBody)

	debugFirst, ok := metaFirst["_request_debug"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected request debug metadata, got %#v", metaFirst["_request_debug"])
	}
	debugSecond, ok := metaSecond["_request_debug"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected request debug metadata, got %#v", metaSecond["_request_debug"])
	}

	for _, key := range []string{
		"request_sha256",
		"cache_surface_sha256",
		"input_sha256",
		"tools_sha256",
		"instructions_sha256",
		"prompt_layout_sha256",
	} {
		first, _ := debugFirst[key].(string)
		second, _ := debugSecond[key].(string)
		if first == "" || second == "" {
			t.Fatalf("expected %s to be populated, got %q / %q", key, first, second)
		}
		if first != second {
			t.Fatalf("expected %s to be stable, got %q / %q", key, first, second)
		}
	}

	if debugFirst["input_count"] != 1 {
		t.Fatalf("expected input_count=1, got %#v", debugFirst["input_count"])
	}
	if debugFirst["tool_count"] != 2 {
		t.Fatalf("expected tool_count=2, got %#v", debugFirst["tool_count"])
	}
	if debugFirst["prompt_cache_key"] != "session-1" {
		t.Fatalf("expected prompt_cache_key=session-1, got %#v", debugFirst["prompt_cache_key"])
	}
	if debugFirst["prompt_layout_length"] != len("[base/system]\nSystem prompt") {
		t.Fatalf("expected prompt_layout_length to be populated, got %#v", debugFirst["prompt_layout_length"])
	}
}

func TestBuildHTTPDebugRequestMetadataDetectsToolOrderChanges(t *testing.T) {
	base := map[string]interface{}{
		"messages": []map[string]interface{}{
			{
				"role":    "user",
				"content": "hello",
			},
		},
		"tools": []map[string]interface{}{
			{"name": "tool_a"},
			{"name": "tool_b"},
		},
	}
	reordered := map[string]interface{}{
		"messages": []map[string]interface{}{
			{
				"role":    "user",
				"content": "hello",
			},
		},
		"tools": []map[string]interface{}{
			{"name": "tool_b"},
			{"name": "tool_a"},
		},
	}

	baseDebug := buildHTTPDebugRequestDiagnostics("openai", base)
	reorderedDebug := buildHTTPDebugRequestDiagnostics("openai", reordered)

	if baseDebug["messages_sha256"] != reorderedDebug["messages_sha256"] {
		t.Fatalf("expected message fingerprint to stay stable, got %#v / %#v", baseDebug["messages_sha256"], reorderedDebug["messages_sha256"])
	}
	if baseDebug["tools_sha256"] == reorderedDebug["tools_sha256"] {
		t.Fatalf("expected tool fingerprint to change when order changes")
	}
	if baseDebug["cache_surface_sha256"] == reorderedDebug["cache_surface_sha256"] {
		t.Fatalf("expected cache surface fingerprint to change when tool order changes")
	}
}
