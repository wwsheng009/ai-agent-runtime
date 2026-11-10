package llm

import (
	"bytes"
	"testing"
	"unicode/utf8"
)

func TestBoundHTTPDebugRawBodyRetainsEndsWithinFixedLimit(t *testing.T) {
	body := append(bytes.Repeat([]byte("h"), maxHTTPDebugRawBodyBytes), bytes.Repeat([]byte("t"), 4096)...)
	bounded := boundHTTPDebugRawBody(body)
	if len(bounded) != maxHTTPDebugRawBodyBytes {
		t.Fatalf("expected %d retained bytes, got %d", maxHTTPDebugRawBodyBytes, len(bounded))
	}
	if !bytes.HasPrefix(bounded, []byte("hhhh")) || !bytes.HasSuffix(bounded, []byte("tttt")) {
		t.Fatalf("expected bounded body to retain both head and tail")
	}
	if !bytes.Contains(bounded, httpDebugOmissionMarker) {
		t.Fatalf("expected omission marker in bounded body")
	}
}

func TestTruncateHTTPDebugBytesAvoidsFullConversionAndInvalidUTF8(t *testing.T) {
	data := append(bytes.Repeat([]byte("a"), 31), []byte("界tail")...)
	truncated := truncateHTTPDebugBytes(data, 33)
	if len(truncated) > 33 || !utf8.ValidString(truncated) {
		t.Fatalf("expected bounded valid UTF-8 preview, got %q", truncated)
	}
}

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

	metaFirst := buildHTTPDebugRequestMetadata(map[string]interface{}{"trace_id": "trace-1", "prompt_layout": "[base/system]\nSystem prompt"}, "codex", "opencode-console-go-2026-07", requestBody)
	metaSecond := buildHTTPDebugRequestMetadata(map[string]interface{}{"trace_id": "trace-2", "prompt_layout": "[base/system]\nSystem prompt"}, "codex", "opencode-console-go-2026-07", requestBody)

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
	if debugFirst["compatibility_profile"] != "opencode-console-go-2026-07" {
		t.Fatalf("expected compatibility_profile to be recorded, got %#v", debugFirst["compatibility_profile"])
	}
	if debugFirst["protocol"] != "codex" {
		t.Fatalf("expected protocol=codex, got %#v", debugFirst["protocol"])
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

	baseDebug := buildHTTPDebugRequestDiagnostics("openai", "", base)
	reorderedDebug := buildHTTPDebugRequestDiagnostics("openai", "", reordered)

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

func TestBuildHTTPDebugRequestDiagnosticsCacheSurfaceTracksEachField(t *testing.T) {
	base := map[string]interface{}{
		"messages":     []map[string]interface{}{{"role": "user", "content": "hello"}},
		"tools":        []map[string]interface{}{{"name": "tool_a"}},
		"instructions": "system prompt",
	}
	baseDebug := buildHTTPDebugRequestDiagnostics("openai", "", base)
	baseSurface, _ := baseDebug["cache_surface_sha256"].(string)
	if baseSurface == "" {
		t.Fatal("expected cache surface fingerprint")
	}

	changes := []struct {
		name string
		body map[string]interface{}
	}{
		{name: "messages", body: map[string]interface{}{
			"messages":     []map[string]interface{}{{"role": "user", "content": "changed"}},
			"tools":        base["tools"],
			"instructions": base["instructions"],
		}},
		{name: "tools", body: map[string]interface{}{
			"messages":     base["messages"],
			"tools":        []map[string]interface{}{{"name": "tool_b"}},
			"instructions": base["instructions"],
		}},
		{name: "instructions", body: map[string]interface{}{
			"messages":     base["messages"],
			"tools":        base["tools"],
			"instructions": "changed system prompt",
		}},
		{name: "prompt_cache_key", body: map[string]interface{}{
			"messages":         base["messages"],
			"tools":            base["tools"],
			"instructions":     base["instructions"],
			"prompt_cache_key": "session-2",
		}},
	}
	for _, change := range changes {
		t.Run(change.name, func(t *testing.T) {
			debug := buildHTTPDebugRequestDiagnostics("openai", "", change.body)
			surface, _ := debug["cache_surface_sha256"].(string)
			if surface == "" || surface == baseSurface {
				t.Fatalf("cache surface fingerprint did not change for %q: %q", change.name, surface)
			}
		})
	}
}

func TestBuildHTTPDebugRequestDiagnosticsRequestFingerprintTracksAllFields(t *testing.T) {
	base := map[string]interface{}{
		"model": "bench-model",
		"messages": []map[string]interface{}{
			{"role": "user", "content": "hello"},
		},
		"tools": []map[string]interface{}{
			{"name": "tool_a"},
		},
		"instructions": "system prompt",
	}
	baseDebug := buildHTTPDebugRequestDiagnostics("openai", "", base)
	baseRequest, _ := baseDebug["request_sha256"].(string)
	if baseRequest == "" {
		t.Fatal("expected request fingerprint")
	}
	again := buildHTTPDebugRequestDiagnostics("openai", "", base)
	if again["request_sha256"] != baseRequest {
		t.Fatal("request fingerprint must be stable for unchanged body")
	}

	changes := []struct {
		name string
		body map[string]interface{}
	}{
		{name: "scalar field", body: map[string]interface{}{
			"model":        "other-model",
			"messages":     base["messages"],
			"tools":        base["tools"],
			"instructions": base["instructions"],
		}},
		{name: "messages field", body: map[string]interface{}{
			"model":        base["model"],
			"messages":     []map[string]interface{}{{"role": "user", "content": "changed"}},
			"tools":        base["tools"],
			"instructions": base["instructions"],
		}},
		{name: "extra field", body: map[string]interface{}{
			"model":        base["model"],
			"messages":     base["messages"],
			"tools":        base["tools"],
			"instructions": base["instructions"],
			"temperature":  0.5,
		}},
	}
	for _, change := range changes {
		t.Run(change.name, func(t *testing.T) {
			debug := buildHTTPDebugRequestDiagnostics("openai", "", change.body)
			got, _ := debug["request_sha256"].(string)
			if got == "" || got == baseRequest {
				t.Fatalf("request fingerprint did not change for %q: %q", change.name, got)
			}
		})
	}
}

// TestBuildHTTPDebugRequestMetadataWithBodyMatchesMapFingerprints 锁定
// 复用序列化 body 的快速路径与旧 map 编码路径产生同一组指纹。字段哈希
// 从"重新 JSON 编码整个 messages/tools/instructions"改为"直接哈希请求体
// 中对应字段的原始字节"，语义必须完全等价。
func TestBuildHTTPDebugRequestMetadataWithBodyMatchesMapFingerprints(t *testing.T) {
	body := map[string]interface{}{
		"model": "gpt-test",
		"messages": []interface{}{
			map[string]interface{}{"role": "user", "content": "1 < 2 & 3"},
			map[string]interface{}{"role": "assistant", "content": "中文回复"},
		},
		"tools": []interface{}{
			map[string]interface{}{"type": "function", "function": map[string]interface{}{"name": "tool_a"}},
		},
		"instructions":     "system <prompt>",
		"prompt_cache_key": "session-1",
		"stream":           true,
	}
	bodyBytes, err := MarshalProviderRequestBody(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	oldMetadata := buildHTTPDebugRequestMetadata(nil, "openai", "bench", body)
	newMetadata := buildHTTPDebugRequestMetadataWithBody(nil, "openai", "bench", body, bodyBytes)
	oldDebug, _ := oldMetadata["_request_debug"].(map[string]interface{})
	newDebug, _ := newMetadata["_request_debug"].(map[string]interface{})
	if oldDebug == nil || newDebug == nil {
		t.Fatalf("missing diagnostics: old=%#v new=%#v", oldMetadata, newMetadata)
	}

	for _, key := range []string{
		"request_sha256",
		"cache_surface_sha256",
		"messages_sha256",
		"tools_sha256",
		"instructions_sha256",
		"message_count",
		"tool_count",
		"prompt_cache_key",
		"protocol",
		"compatibility_profile",
	} {
		if oldDebug[key] != newDebug[key] {
			t.Fatalf("%s differs: old=%#v new=%#v", key, oldDebug[key], newDebug[key])
		}
	}
}

func TestParseHTTPDebugRawFields(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"text":"a }, \" b"}],"tools":[],"instructions":"sys","prompt_cache_key":"k"}`)
	fields, ok := parseHTTPDebugRawFields(body)
	if !ok {
		t.Fatal("expected compact JSON body to parse")
	}
	for _, key := range []string{"messages", "tools", "instructions", "prompt_cache_key"} {
		if len(fields[key]) == 0 {
			t.Fatalf("missing raw field %q: %#v", key, fields)
		}
	}
	if string(fields["messages"]) != `[{"text":"a }, \" b"}]` {
		t.Fatalf("messages raw slice = %q", fields["messages"])
	}
	if string(fields["instructions"]) != `"sys"` {
		t.Fatalf("instructions raw slice = %q", fields["instructions"])
	}
	if _, ok := parseHTTPDebugRawFields([]byte(`not-json`)); ok {
		t.Fatal("invalid body must not parse")
	}
}

func TestBuildHTTPDebugRequestDiagnosticsWithBodyFallback(t *testing.T) {
	body := map[string]interface{}{
		"messages": []interface{}{map[string]interface{}{"role": "user", "content": "hello"}},
	}
	oldDebug := buildHTTPDebugRequestDiagnostics("openai", "", body)
	newDebug := buildHTTPDebugRequestDiagnosticsWithBody("openai", "", body, []byte(`not-json`))
	if oldDebug["messages_sha256"] != newDebug["messages_sha256"] ||
		oldDebug["request_sha256"] != newDebug["request_sha256"] {
		t.Fatalf("fallback diagnostics differ: old=%#v new=%#v", oldDebug, newDebug)
	}
}
