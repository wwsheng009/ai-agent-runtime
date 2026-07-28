package llm

import "testing"

func TestBuildToolDefinitionsForProtocol_PreservesInputToolOrder(t *testing.T) {
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
	if firstFn["name"] != "write" || secondFn["name"] != "bash" {
		t.Fatalf("expected adapter to preserve caller tool order, got %#v", list)
	}
}

func TestBuildToolDefinitionsForProtocol_DoesNotSilentlyDeduplicateCallerTools(t *testing.T) {
	tools := []map[string]interface{}{
		{"name": "duplicate", "description": "first", "parameters": map[string]interface{}{"type": "object"}},
		{"name": "duplicate", "description": "second", "parameters": map[string]interface{}{"type": "object"}},
	}

	converted := buildToolDefinitionsForProtocol(tools, "anthropic", false)
	list, ok := converted.([]map[string]interface{})
	if !ok || len(list) != 2 {
		t.Fatalf("expected adapter to preserve both caller definitions, got %T %#v", converted, converted)
	}
	if list[0]["description"] != "first" || list[1]["description"] != "second" {
		t.Fatalf("expected caller order and definitions to remain intact, got %#v", list)
	}
}

func TestBuildToolDefinitionsForProtocol_DefaultsMissingParametersForAllNamedToolProtocols(t *testing.T) {
	tools := []map[string]interface{}{
		{
			"name":        "get_goal",
			"description": "read current goal",
		},
	}

	for _, protocol := range []string{"openai", "anthropic", "gemini", "codex"} {
		converted := buildToolDefinitionsForProtocol(tools, protocol, false)
		list, ok := converted.([]map[string]interface{})
		if !ok || len(list) != 1 {
			t.Fatalf("%s: expected 1 tool definition, got %T %#v", protocol, converted, converted)
		}

		var params interface{}
		switch protocol {
		case "openai":
			fn, _ := list[0]["function"].(map[string]interface{})
			params = fn["parameters"]
		case "anthropic":
			params = list[0]["input_schema"]
		default:
			params = list[0]["parameters"]
		}

		schema, ok := params.(map[string]interface{})
		if !ok {
			t.Fatalf("%s: expected parameters object, got %#v", protocol, params)
		}
		if schema["type"] != "object" || schema["additionalProperties"] != false {
			t.Fatalf("%s: unexpected default schema %#v", protocol, schema)
		}
	}
}

func TestBuildToolDefinitionsForProtocol_DefaultsInvalidParametersToObjectSchema(t *testing.T) {
	tools := []map[string]interface{}{
		{
			"name":        "bad_schema",
			"description": "bad schema",
			"parameters":  nil,
		},
	}

	converted := buildToolDefinitionsForProtocol(tools, "openai", false)
	list, ok := converted.([]map[string]interface{})
	if !ok || len(list) != 1 {
		t.Fatalf("expected 1 tool definition, got %T %#v", converted, converted)
	}
	fn, _ := list[0]["function"].(map[string]interface{})
	schema, ok := fn["parameters"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected parameters object, got %#v", fn["parameters"])
	}
	if schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Fatalf("unexpected default schema %#v", schema)
	}
}

func TestBuildToolDefinitionsForProtocol_CodexDefaultsToNonStrictAndPreservesExplicitFlags(t *testing.T) {
	tools := []map[string]interface{}{
		{
			"name":        "inspect",
			"description": "inspect a path",
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":  map[string]interface{}{"type": "string"},
					"limit": map[string]interface{}{"type": "integer"},
				},
				"required": []string{"path"},
			},
		},
		{
			"name":        "open_inspect",
			"description": "inspect an open object",
			"parameters":  map[string]interface{}{"type": "object"},
			"strict":      false,
		},
		{
			"name":        "strict_inspect",
			"description": "strictly inspect a path",
			"parameters":  map[string]interface{}{"type": "object"},
			"strict":      true,
		},
	}

	converted := buildToolDefinitionsForProtocol(tools, "codex", false)
	list, ok := converted.([]map[string]interface{})
	if !ok || len(list) != 3 {
		t.Fatalf("expected 3 Codex tools, got %T %#v", converted, converted)
	}
	if list[0]["strict"] != false {
		t.Fatalf("expected Codex-compatible default strict=false, got %#v", list[0]["strict"])
	}
	if list[1]["strict"] != false {
		t.Fatalf("expected explicit strict=false to be preserved, got %#v", list[1]["strict"])
	}
	if list[2]["strict"] != true {
		t.Fatalf("expected explicit strict=true to be preserved, got %#v", list[2]["strict"])
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
