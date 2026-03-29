package adapter

import "testing"

func TestBuildMCPMetaTools(t *testing.T) {
	tools := BuildMCPMetaTools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	tool := tools[0]
	if tool["name"] != "list_mcp_resources" {
		t.Fatalf("expected list_mcp_resources, got %v", tool["name"])
	}
	if tool["strict"] != false {
		t.Fatalf("expected strict=false, got %v", tool["strict"])
	}
	params, ok := tool["parameters"].(map[string]interface{})
	if !ok || params == nil {
		t.Fatalf("expected parameters map, got %T", tool["parameters"])
	}
	if params["additionalProperties"] != false {
		t.Fatalf("expected additionalProperties=false, got %v", params["additionalProperties"])
	}
}
