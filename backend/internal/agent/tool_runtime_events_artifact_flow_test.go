package agent

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/output"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// O-4（§11.4）：tool.completed 载荷必须携带 artifact-flow 指标字段，让观测
// 侧可以在不新增事件管道的前提下聚合输出体量/归档处置。
func TestToolCompletedEventPayloadPromotesArtifactFlow(t *testing.T) {
	envelope := &output.Envelope{
		ToolName: "view",
		Summary:  "阅读 main.go 第 1-20 行",
		Metadata: map[string]interface{}{
			toolresult.MetadataOKKey: true,
			"raw_bytes":              60000,
			"artifact_id":            "art_abc123",
		},
	}
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call:     types.ToolCall{ID: "call-o4", Name: "view"},
		Envelope: envelope,
	}, 1, "trace-o4", nil)

	if got, _ := payload["output_original_bytes"].(int64); got != 60000 {
		t.Fatalf("expected output_original_bytes=60000, got %#v", payload["output_original_bytes"])
	}
	if got, _ := payload["output_model_visible_bytes"].(int); got <= 0 {
		t.Fatalf("expected positive output_model_visible_bytes, got %#v", payload["output_model_visible_bytes"])
	}
	if got, _ := payload["artifact_archived"].(bool); !got {
		t.Fatalf("expected artifact_archived=true, got %#v", payload["artifact_archived"])
	}
	if got, _ := payload["artifact_id"].(string); got != "art_abc123" {
		t.Fatalf("expected artifact_id=art_abc123, got %#v", payload["artifact_id"])
	}
	if _, exists := payload["artifact_skipped"]; exists {
		t.Fatalf("artifact_skipped must be absent when archived")
	}
}

func TestToolCompletedEventPayloadPromotesArtifactSkipped(t *testing.T) {
	envelope := &output.Envelope{
		ToolName: "grep",
		Summary:  "命中 1 行",
		Metadata: map[string]interface{}{
			toolresult.MetadataOKKey: true,
			"raw_bytes":              42,
			"artifact_skipped":       "below_threshold",
		},
	}
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call:     types.ToolCall{ID: "call-o4-skip", Name: "grep"},
		Envelope: envelope,
	}, 1, "trace-o4-skip", nil)

	if got, _ := payload["output_original_bytes"].(int64); got != 42 {
		t.Fatalf("expected output_original_bytes=42, got %#v", payload["output_original_bytes"])
	}
	if got, _ := payload["artifact_skipped"].(string); got != "below_threshold" {
		t.Fatalf("expected artifact_skipped=below_threshold, got %#v", payload["artifact_skipped"])
	}
	if _, exists := payload["artifact_archived"]; exists {
		t.Fatalf("artifact_archived must be absent when skipped")
	}
	if _, exists := payload["artifact_id"]; exists {
		t.Fatalf("artifact_id must be absent when skipped")
	}
}

func TestToolCompletedEventPayloadOmitsArtifactFlowWithoutEnvelope(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{ID: "call-o4-none", Name: "ls"},
	}, 1, "trace-o4-none", nil)

	for _, key := range []string{"output_original_bytes", "output_model_visible_bytes", "artifact_archived", "artifact_id", "artifact_skipped"} {
		if _, exists := payload[key]; exists {
			t.Fatalf("key %q must be absent without envelope metadata", key)
		}
	}
}
