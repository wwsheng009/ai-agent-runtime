package adapter

import "testing"

func TestCodexExtractToolCallsFromRawCallsPreservesCustomInput(t *testing.T) {
	raw := "*** Begin Patch\n*** End Patch"
	calls := (&CodexAdapter{}).ExtractToolCallsFromRawCalls([]map[string]interface{}{{
		"type": "custom_tool_call", "call_id": "call_patch",
		"name": "apply_patch", "input": raw,
	}})
	if len(calls) != 1 {
		t.Fatalf("expected one custom call, got %#v", calls)
	}
	if calls[0].Type != "custom_tool_call" || calls[0].ID != "call_patch" {
		t.Fatalf("expected custom type and call id to survive, got %#v", calls[0])
	}
	if calls[0].Function.Name != "apply_patch" || calls[0].Function.Arguments != raw {
		t.Fatalf("expected raw custom input to survive, got %#v", calls[0].Function)
	}
}
