package commands

import (
	"strings"
	"testing"

	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

func TestFormatRuntimeHTTPDebugEvent_SurfacesPromptLayoutSeparately(t *testing.T) {
	layout := "base: system.md\ndeveloper: tools.md\nuser: AGENTS.md"
	output := formatRuntimeHTTPDebugEvent(runtimellm.HTTPDebugEvent{
		Source:   "gateway_client",
		Phase:    "request",
		Provider: "openai",
		Protocol: "responses",
		Model:    "gpt-5",
		RequestMetadata: map[string]interface{}{
			"prompt_layout": layout,
			"trace_id":      "trace-1",
			"_request_debug": map[string]interface{}{
				"request_sha256":       "req-sha",
				"prompt_layout_sha256": "layout-sha",
				"message_count":        4,
				"prompt_layout_length": len(layout),
			},
		},
	})

	if !strings.Contains(output, "prompt_layout_sha256=layout-sha") {
		t.Fatalf("expected prompt layout fingerprint in output:\n%s", output)
	}
	if !strings.Contains(output, "prompt_layout_length=51") {
		t.Fatalf("expected prompt layout length in output:\n%s", output)
	}
	if !strings.Contains(output, "[http-debug/runtime] prompt_layout="+layout) {
		t.Fatalf("expected prompt layout preview line in output:\n%s", output)
	}
	if !strings.Contains(output, "\"prompt_layout\":\"[omitted:51 chars]\"") {
		t.Fatalf("expected prompt layout placeholder in request metadata:\n%s", output)
	}
}
