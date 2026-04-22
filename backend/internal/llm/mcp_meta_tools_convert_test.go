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
