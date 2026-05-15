package adapter

import (
	"encoding/json"
	"testing"
)

func TestRepairJSONLeavesValidArgumentsWithBracesInsideStrings(t *testing.T) {
	raw := `{"file_path":"E:/projects/ai/ai-agent-runtime/backend/internal/llm/provider_adapter_request.go","old_string":"if hasCapability {\n\treturn adapter.RequestConfig{\n","new_string":"if hasCapability {\n\tmaxTokens := input.MaxTokens\n\treturn adapter.RequestConfig{\n"}`

	repaired := repairJSON(raw)
	if repaired != raw {
		t.Fatalf("repairJSON changed already-valid JSON:\nwant %q\n got %q", raw, repaired)
	}

	var args map[string]interface{}
	if err := json.Unmarshal([]byte(repaired), &args); err != nil {
		t.Fatalf("repaired JSON should unmarshal: %v", err)
	}
	if args["file_path"] == "" || args["old_string"] == "" || args["new_string"] == "" {
		t.Fatalf("expected parsed edit arguments, got %#v", args)
	}
}

func TestRepairJSONClosesObjectOutsideStrings(t *testing.T) {
	raw := `{"command":"if ($true) { Write-Host \"ok\" }"`

	repaired := repairJSON(raw)
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(repaired), &args); err != nil {
		t.Fatalf("repaired JSON should unmarshal: %v; repaired=%q", err, repaired)
	}
	if args["command"] != `if ($true) { Write-Host "ok" }` {
		t.Fatalf("unexpected command: %#v", args["command"])
	}
}
