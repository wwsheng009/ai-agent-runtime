package llm

import "testing"

func TestBuildToolDefinitionsForProtocol_SortsInputToolsByName(t *testing.T) {
	tools := []map[string]interface{}{
		{
			"name":        "write",
			"description": "write file",
			"parameters":  map[string]interface{}{"type": "object"},
		},
		{
			"name":        "bash",
			"description": "run shell",
			"parameters":  map[string]interface{}{"type": "object"},
		},
	}

	converted := buildToolDefinitionsForProtocol(tools, "openai", false)
	list, ok := converted.([]map[string]interface{})
	if !ok || len(list) != 2 {
		t.Fatalf("expected 2 openai tool definitions, got %T %#v", converted, converted)
	}

	firstFn, _ := list[0]["function"].(map[string]interface{})
	secondFn, _ := list[1]["function"].(map[string]interface{})
	if firstFn["name"] != "bash" || secondFn["name"] != "write" {
		t.Fatalf("expected sorted tool definitions, got %#v", list)
	}
}

func TestBuildToolDefinitionsForProtocol_CodexPromotesFreeformApplyPatch(t *testing.T) {
	tools := []map[string]interface{}{
		{
			"name":        "apply_patch",
			"description": "apply patch freeform",
			"parameters":  map[string]interface{}{"type": "object"},
			"metadata": map[string]interface{}{
				"freeform": map[string]interface{}{
					"type":       "grammar",
					"syntax":     "lark",
					"definition": "start: patch",
				},
			},
		},
	}

	converted := buildToolDefinitionsForProtocol(tools, "codex", false)
	list, ok := converted.([]map[string]interface{})
	if !ok || len(list) != 1 {
		t.Fatalf("expected 1 codex tool definition, got %T %#v", converted, converted)
	}
	if got := list[0]["type"]; got != "custom" {
		t.Fatalf("expected custom tool type, got %#v", got)
	}
	format, ok := list[0]["format"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected format map, got %#v", list[0]["format"])
	}
	if got := format["syntax"]; got != "lark" {
		t.Fatalf("expected lark freeform syntax, got %#v", got)
	}
	if _, exists := list[0]["parameters"]; exists {
		t.Fatalf("did not expect parameters on custom tool: %#v", list[0])
	}
}
