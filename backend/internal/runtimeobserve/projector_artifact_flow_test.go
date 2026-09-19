package runtimeobserve

import (
	"testing"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// O-4（§11.4）：tool.completed 的 artifact-flow 指标字段必须能穿过 projector
// 的 allowlist 投影到远程观测事件；同时保证未 allowlist 的载荷字段仍被丢弃。
func TestProjectRuntimeEventToolFinishedCarriesArtifactFlow(t *testing.T) {
	p := NewProjector(NewRedactor(nil, "", ""), false, 0)
	proj, ok := p.ProjectRuntimeEvent(runtimeevents.Event{
		Type:      EventToolFinished,
		TraceID:   "trace-o4",
		SessionID: "session-o4",
		Payload: map[string]interface{}{
			"tool_call_id":               "call-1",
			"logical_tool":               "view",
			"step":                       2,
			"output_original_bytes":      60000,
			"output_model_visible_bytes": 512,
			"artifact_archived":          true,
			"artifact_id":                "art_abc123",
			// 不在 allowlist 的字段必须被丢弃。
			"summary":       "正文摘要不得导出",
			"render_output": "## 正文",
			"arg_preview":   "file_path=main.go",
		},
	})
	if !ok {
		t.Fatal("tool.finished must be allowlisted")
	}
	if got, _ := proj.Payload["output_original_bytes"].(int); got != 60000 {
		t.Fatalf("expected output_original_bytes=60000, got %#v", proj.Payload["output_original_bytes"])
	}
	if got, _ := proj.Payload["output_model_visible_bytes"].(int); got != 512 {
		t.Fatalf("expected output_model_visible_bytes=512, got %#v", proj.Payload["output_model_visible_bytes"])
	}
	if got, _ := proj.Payload["artifact_archived"].(bool); !got {
		t.Fatalf("expected artifact_archived=true, got %#v", proj.Payload["artifact_archived"])
	}
	if got, _ := proj.Payload["artifact_id"].(string); got != "art_abc123" {
		t.Fatalf("expected artifact_id passthrough, got %#v", proj.Payload["artifact_id"])
	}
	for _, forbidden := range []string{"summary", "render_output", "arg_preview"} {
		if _, exists := proj.Payload[forbidden]; exists {
			t.Fatalf("field %q must not be exported: %+v", forbidden, proj.Payload)
		}
	}
}

func TestProjectRuntimeEventToolFinishedArtifactSkippedEnum(t *testing.T) {
	p := NewProjector(NewRedactor(nil, "", ""), false, 0)
	proj, ok := p.ProjectRuntimeEvent(runtimeevents.Event{
		Type: EventToolFinished,
		Payload: map[string]interface{}{
			"artifact_skipped": "below_threshold",
		},
	})
	if !ok {
		t.Fatal("tool.finished must be allowlisted")
	}
	if got, _ := proj.Payload["artifact_skipped"].(string); got != "below_threshold" {
		t.Fatalf("expected artifact_skipped enum passthrough, got %#v", proj.Payload["artifact_skipped"])
	}
}
