package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

func TestWriteRuntimeHTTPArtifact_PersistsRawBodiesAndTracksLatestPaths(t *testing.T) {
	sessionDir := t.TempDir()
	session := &ChatSession{
		SessionDir:         sessionDir,
		RuntimeSession:     &runtimechat.Session{ID: "session-1", State: runtimechat.StateActive},
		runtimeHTTPCapture: &chatRuntimeHTTPCapture{},
	}

	requestPath, err := writeRuntimeHTTPArtifact(session, runtimellm.HTTPDebugEvent{
		Source:           "gateway_client",
		Phase:            "request",
		Provider:         "nvidia",
		Protocol:         "openai",
		Model:            "z-ai/glm4.7",
		Method:           "POST",
		URL:              "https://example.com/v1/chat/completions",
		RequestBodyBytes: len(`{"messages":[{"role":"user","content":"hello"}]}`),
		RequestBodyRaw:   []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
	})
	if err != nil {
		t.Fatalf("write request artifact: %v", err)
	}

	responseBody := "data: {\"choices\":[{\"delta\":{\"content\":\"<tool_call>ls</tool_call>\"}}]}\n\n"
	responsePath, err := writeRuntimeHTTPArtifact(session, runtimellm.HTTPDebugEvent{
		Source:              "gateway_client",
		Phase:               "response",
		Provider:            "nvidia",
		Protocol:            "openai",
		Model:               "z-ai/glm4.7",
		Method:              "POST",
		URL:                 "https://example.com/v1/chat/completions",
		ResponseStatusCode:  200,
		ResponseBodyBytes:   len(responseBody),
		ResponseBodyPreview: responseBody,
		ResponseBodyRaw:     []byte(responseBody),
	})
	if err != nil {
		t.Fatalf("write response artifact: %v", err)
	}

	expectedDir := filepath.Join(sessionDir, "session-1.artifacts", "runtime-http")
	if requestPath != filepath.Join(expectedDir, "001_request_gateway_client.json") {
		t.Fatalf("unexpected request path: %s", requestPath)
	}
	if responsePath != filepath.Join(expectedDir, "001_response_gateway_client.json") {
		t.Fatalf("unexpected response path: %s", responsePath)
	}

	responseData, err := os.ReadFile(responsePath)
	if err != nil {
		t.Fatalf("read response artifact: %v", err)
	}
	var envelope runtimeHTTPArtifactEnvelope
	if err := json.Unmarshal(responseData, &envelope); err != nil {
		t.Fatalf("unmarshal response artifact: %v", err)
	}
	if envelope.Sequence != 1 || envelope.Phase != "response" {
		t.Fatalf("unexpected response envelope: %+v", envelope)
	}
	if envelope.BodyFormat != "text" || envelope.BodyText != responseBody {
		t.Fatalf("expected full SSE response body, got %+v", envelope)
	}

	snapshot := session.runtimeHTTPCapture.Snapshot()
	if snapshot.ArtifactDir != expectedDir {
		t.Fatalf("unexpected artifact dir snapshot: %+v", snapshot)
	}
	if snapshot.RequestArtifactPath != requestPath || snapshot.ResponseArtifactPath != responsePath {
		t.Fatalf("unexpected artifact paths snapshot: %+v", snapshot)
	}
}
