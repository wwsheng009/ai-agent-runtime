package toolkit_test

import (
	"context"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

type countingSchemaTool struct {
	name       string
	parameters map[string]interface{}
	paramCalls int
}

func (t *countingSchemaTool) Name() string        { return t.name }
func (t *countingSchemaTool) Description() string { return "counting schema tool" }
func (t *countingSchemaTool) Version() string     { return "test" }
func (t *countingSchemaTool) CanDirectCall() bool { return true }

func (t *countingSchemaTool) Parameters() map[string]interface{} {
	t.paramCalls++
	return t.parameters
}

func (t *countingSchemaTool) Execute(context.Context, map[string]interface{}) (*toolkit.ToolResult, error) {
	return &toolkit.ToolResult{Success: true}, nil
}

func TestRegistryCachesCanonicalSchemaAndTracksRevision(t *testing.T) {
	registry := toolkit.NewRegistry()
	tool := &countingSchemaTool{
		name: "cached",
		parameters: map[string]interface{}{
			"properties": map[string]interface{}{
				"query": map[string]interface{}{"type": "string"},
			},
		},
	}
	if err := registry.Register(tool); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	if tool.paramCalls != 1 {
		t.Fatalf("Parameters called %d times during registration", tool.paramCalls)
	}
	if registry.SchemaRevision() != 1 {
		t.Fatalf("unexpected revision after register: %d", registry.SchemaRevision())
	}

	first := registry.GetToolSchemas()
	second := registry.GetToolSchemas()
	if tool.paramCalls != 1 {
		t.Fatalf("Parameters was called again while projecting schemas: %d", tool.paramCalls)
	}
	mcpTools := toolkit.RegistryToMCPTools(registry)
	if tool.paramCalls != 1 {
		t.Fatalf("Parameters was called again while projecting MCP tools: %d", tool.paramCalls)
	}
	if len(mcpTools) != 1 || mcpTools[0].InputSchema["type"] != "object" {
		t.Fatalf("unexpected MCP schema projection: %#v", mcpTools)
	}
	firstParams := first[0]["parameters"].(map[string]interface{})
	if firstParams["type"] != "object" || firstParams["additionalProperties"] != false {
		t.Fatalf("schema was not canonicalized: %#v", firstParams)
	}
	firstParams["type"] = "array"
	secondParams := second[0]["parameters"].(map[string]interface{})
	if secondParams["type"] != "object" {
		t.Fatalf("returned schema aliases registry cache: %#v", secondParams)
	}
	mcpTools[0].InputSchema["type"] = "array"
	snapshot, ok := registry.ParameterSchema(tool.Name())
	if !ok || snapshot["type"] != "object" {
		t.Fatalf("MCP projection aliases registry cache: %#v", snapshot)
	}

	if err := registry.Unregister(tool.Name()); err != nil {
		t.Fatal(err)
	}
	if registry.SchemaRevision() != 2 {
		t.Fatalf("unexpected revision after unregister: %d", registry.SchemaRevision())
	}
}

func TestRegistryRejectsInvalidToolSchema(t *testing.T) {
	registry := toolkit.NewRegistry()
	tool := &countingSchemaTool{
		name:       "invalid",
		parameters: map[string]interface{}{"type": "array"},
	}
	if err := registry.Register(tool); err == nil {
		t.Fatal("expected invalid root schema to be rejected")
	}
	if _, ok := registry.Get(tool.Name()); ok {
		t.Fatal("invalid tool was registered")
	}
	if registry.SchemaRevision() != 0 {
		t.Fatalf("invalid registration changed revision: %d", registry.SchemaRevision())
	}
}
