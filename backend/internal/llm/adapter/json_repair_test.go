package adapter

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolargs"
)

func TestRepairJSONLeavesValidArgumentsWithBracesInsideStrings(t *testing.T) {
	raw := `{"file_path":"E:/projects/ai/ai-agent-runtime/backend/internal/llm/provider_adapter_request.go","old_string":"if hasCapability {\n\treturn adapter.RequestConfig{\n","new_string":"if hasCapability {\n\tmaxTokens := input.MaxTokens\n\treturn adapter.RequestConfig{\n"}`

	args := toolargs.DecodeJSON(raw)
	if args["file_path"] == "" || args["old_string"] == "" || args["new_string"] == "" {
		t.Fatalf("expected parsed edit arguments, got %#v", args)
	}
}

func TestRepairJSONClosesObjectOutsideStrings(t *testing.T) {
	raw := `{"command":"if ($true) { Write-Host \"ok\" }"`

	args := toolargs.DecodeJSON(raw)
	if args["command"] != `if ($true) { Write-Host "ok" }` {
		t.Fatalf("unexpected command: %#v", args["command"])
	}
}
