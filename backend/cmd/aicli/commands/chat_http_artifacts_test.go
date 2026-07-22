package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

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
		Source:   "gateway_client",
		Phase:    "request",
		Provider: "nvidia",
		Protocol: "openai",
		Model:    "z-ai/glm4.7",
		Method:   "POST",
		URL:      "https://example.com/v1/chat/completions",
		RequestMetadata: map[string]interface{}{
			"trace_id": "trace-1",
			"tool_availability": map[string]interface{}{
				"requires_active_team_run": []string{"read_task_spec"},
			},
		},
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

	requestData, err := os.ReadFile(requestPath)
	if err != nil {
		t.Fatalf("read request artifact: %v", err)
	}
	var requestEnvelope runtimeHTTPArtifactEnvelope
	if err := json.Unmarshal(requestData, &requestEnvelope); err != nil {
		t.Fatalf("unmarshal request artifact: %v", err)
	}
	if requestEnvelope.RequestMetadata["trace_id"] != "trace-1" {
		t.Fatalf("expected request trace metadata, got %+v", requestEnvelope.RequestMetadata)
	}
	availability, ok := requestEnvelope.RequestMetadata["tool_availability"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected tool_availability metadata, got %+v", requestEnvelope.RequestMetadata["tool_availability"])
	}
	requires, ok := availability["requires_active_team_run"].([]interface{})
	if !ok || len(requires) != 1 || requires[0] != "read_task_spec" {
		t.Fatalf("unexpected requires_active_team_run metadata: %+v", availability["requires_active_team_run"])
	}

	snapshot := session.runtimeHTTPCapture.Snapshot()
	if snapshot.ArtifactDir != expectedDir {
		t.Fatalf("unexpected artifact dir snapshot: %+v", snapshot)
	}
	if snapshot.RequestArtifactPath != requestPath || snapshot.ResponseArtifactPath != responsePath {
		t.Fatalf("unexpected artifact paths snapshot: %+v", snapshot)
	}
}

func TestBuildRuntimeHTTPArtifactEnvelopeMarksBoundedBody(t *testing.T) {
	envelope := buildRuntimeHTTPArtifactEnvelope(1, runtimellm.HTTPDebugEvent{
		Phase:             "request",
		LogicalTurnID:     "turn-1",
		LLMRequestID:      "request-1",
		RetryAttemptID:    "attempt-1",
		ProviderRequestID: "provider-request-1",
		StreamID:          "stream-1",
		ErrorCode:         "STREAM_INTERRUPTED",
		RequestBodyBytes:  4096,
		RequestBodyRaw:    []byte(`{"bounded":true}`),
	})
	if !envelope.BodyTruncated || envelope.BodyBytes != 4096 {
		t.Fatalf("expected original body size and truncation marker, got %+v", envelope)
	}
	if envelope.BodyCapturedBytes != len(`{"bounded":true}`) {
		t.Fatalf("unexpected captured body size: %+v", envelope)
	}
	if envelope.LogicalTurnID != "turn-1" || envelope.LLMRequestID != "request-1" ||
		envelope.RetryAttemptID != "attempt-1" || envelope.ProviderRequestID != "provider-request-1" ||
		envelope.StreamID != "stream-1" {
		t.Fatalf("expected top-level correlation ids, got %+v", envelope)
	}
	if envelope.ErrorCode != "STREAM_INTERRUPTED" {
		t.Fatalf("expected structured error code, got %+v", envelope)
	}
}

func TestBuildRuntimeHTTPArtifactEnvelopePersistsRequestedAndEffectiveRoute(t *testing.T) {
	envelope := buildRuntimeHTTPArtifactEnvelope(1, runtimellm.HTTPDebugEvent{
		RequestMetadata: map[string]interface{}{
			"route": map[string]interface{}{
				"requested_provider":         "configured-alias",
				"effective_provider":         "canonical-provider",
				"requested_model":            "friendly-model",
				"effective_model":            "canonical-model",
				"requested_reasoning_effort": "xhigh",
				"effective_reasoning_effort": "high",
				"requested_permission_mode":  "plan",
				"effective_permission_mode":  "plan",
				"route_warnings":             []string{"explicit_reasoning_override_denied"},
				"fallback_used":              true,
				"fallback_reason":            "route_policy",
			},
		},
	})
	if envelope.RequestedProvider != "configured-alias" || envelope.EffectiveProvider != "canonical-provider" ||
		envelope.RequestedModel != "friendly-model" || envelope.EffectiveModel != "canonical-model" {
		t.Fatalf("unexpected provider/model route metadata: %+v", envelope)
	}
	if envelope.RequestedReasoningEffort != "xhigh" || envelope.EffectiveReasoningEffort != "high" ||
		envelope.RequestedPermissionMode != "plan" || envelope.EffectivePermissionMode != "plan" {
		t.Fatalf("unexpected reasoning/permission route metadata: %+v", envelope)
	}
	if !envelope.FallbackUsed || envelope.FallbackReason != "route_policy" || len(envelope.RouteWarnings) != 1 {
		t.Fatalf("unexpected route warning metadata: %+v", envelope)
	}
}

func TestWithRuntimeHTTPRouteMetadataUsesCurrentSessionRouteWithoutMutatingInput(t *testing.T) {
	input := map[string]interface{}{"trace_id": "trace-1"}
	session := &ChatSession{
		ProviderName:             "canonical-provider",
		Model:                    "canonical-model",
		ReasoningEffort:          "high",
		RequestedProvider:        "configured-alias",
		EffectiveProvider:        "canonical-provider",
		RequestedModel:           "friendly-model",
		EffectiveModel:           "canonical-model",
		RequestedReasoningEffort: "xhigh",
		EffectiveReasoningEffort: "high",
		RequestedPermissionMode:  "plan",
		EffectivePermissionMode:  "plan",
		RouteWarnings:            []string{"explicit_reasoning_override_denied"},
		FallbackUsed:             true,
		FallbackReason:           "route_policy",
	}

	metadata := withRuntimeHTTPRouteMetadata(input, session)
	if _, exists := input["route"]; exists {
		t.Fatal("expected input metadata to remain unchanged")
	}
	if metadata["trace_id"] != "trace-1" {
		t.Fatalf("expected existing metadata to be retained, got %+v", metadata)
	}
	route, ok := metadata["route"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected route metadata object, got %#v", metadata["route"])
	}
	if route["requested_provider"] != "configured-alias" || route["effective_provider"] != "canonical-provider" ||
		route["requested_model"] != "friendly-model" || route["effective_model"] != "canonical-model" {
		t.Fatalf("unexpected provider/model route metadata: %+v", route)
	}
	if route["requested_reasoning_effort"] != "xhigh" || route["effective_reasoning_effort"] != "high" ||
		route["fallback_used"] != true || route["fallback_reason"] != "route_policy" {
		t.Fatalf("unexpected reasoning/fallback route metadata: %+v", route)
	}
}

func TestPruneRuntimeHTTPArtifactsEnforcesCountAndByteBudgets(t *testing.T) {
	dir := t.TempDir()
	baseTime := time.Now().Add(-time.Hour)
	for index := 1; index <= 5; index++ {
		path := filepath.Join(dir, fmt.Sprintf("%03d_request_runtime.json", index))
		if err := os.WriteFile(path, []byte("12345678"), 0644); err != nil {
			t.Fatalf("write artifact: %v", err)
		}
		stamp := baseTime.Add(time.Duration(index) * time.Second)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatalf("set artifact timestamp: %v", err)
		}
	}
	if err := pruneRuntimeHTTPArtifacts(dir, 3, 20); err != nil {
		t.Fatalf("prune artifacts: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read artifact directory: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	want := []string{"004_request_runtime.json", "005_request_runtime.json"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("expected newest artifacts %v, got %v", want, names)
	}
}
