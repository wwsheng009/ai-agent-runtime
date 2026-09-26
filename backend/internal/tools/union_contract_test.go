package tools

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestAdvertisedSchemasHaveNoUnionProperties is the portable contract from
// analysis §3.12: whatever the internal descriptor says, no provider-facing
// payload may advertise a property that mixes anyOf/oneOf with sibling keys
// (Gemini-family models turn that into an opaque whole-request 400).
func TestAdvertisedSchemasHaveNoUnionProperties(t *testing.T) {
	manager := NewDefaultManager(nil)
	descriptors := manager.ListTools()
	if len(descriptors) == 0 {
		t.Fatal("no tools registered; test cannot inspect advertised schemas")
	}

	definitions := make([]types.ToolDefinition, 0, len(descriptors))
	internalUnions := 0
	for _, descriptor := range descriptors {
		if path := findUnionProperty(descriptor.Parameters); path != "" {
			internalUnions++
		}
		definitions = append(definitions, types.ToolDefinition{
			Name:        descriptor.Name,
			Description: descriptor.Description,
			Parameters:  descriptor.Parameters,
			Metadata:    descriptor.Metadata,
		})
	}
	if internalUnions == 0 {
		t.Fatal("no internal union property found; the contract test would pass vacuously")
	}

	for _, protocol := range []string{"openai", "anthropic", "gemini"} {
		payload := llm.BuildToolDefinitionsForRequest(definitions, protocol, "", nil, false, false)
		schemas := protocolSchemaMaps(payload)
		if len(schemas) == 0 {
			t.Fatalf("protocol %s produced no tool schemas", protocol)
		}
		for name, schema := range schemas {
			if path := findUnionProperty(schema); path != "" {
				t.Fatalf("protocol %s advertises union property %s on %s", protocol, path, name)
			}
		}
	}
}

// TestGrepUnionParamsRenderPerProvider checks the concrete ergonomics: the
// internal grep schema keeps both spellings, while every provider payload
// advertises the scalar branch with its description intact.
func TestGrepUnionParamsRenderPerProvider(t *testing.T) {
	manager := NewDefaultManager(nil)
	var grepDefinition *types.ToolDefinition
	for _, descriptor := range manager.ListTools() {
		if descriptor.Name != "grep" {
			continue
		}
		grepDefinition = &types.ToolDefinition{
			Name:        descriptor.Name,
			Description: descriptor.Description,
			Parameters:  descriptor.Parameters,
			Metadata:    descriptor.Metadata,
		}
		break
	}
	if grepDefinition == nil {
		t.Fatal("grep tool not registered")
	}
	if path := findUnionProperty(grepDefinition.Parameters); path == "" {
		t.Fatalf("internal grep schema must keep its union for the runtime, got %#v", grepDefinition.Parameters)
	}

	definitions := []types.ToolDefinition{*grepDefinition}
	for _, protocol := range []string{"openai", "anthropic", "gemini"} {
		payload := llm.BuildToolDefinitionsForRequest(definitions, protocol, "", nil, false, false)
		schemas := protocolSchemaMaps(payload)
		params := schemas["grep"]
		if params == nil {
			t.Fatalf("protocol %s did not render grep", protocol)
		}
		properties, _ := params["properties"].(map[string]interface{})
		for _, key := range []string{"patterns", "paths", "include", "exclude"} {
			property, _ := properties[key].(map[string]interface{})
			if property == nil {
				t.Fatalf("protocol %s lost property %s", protocol, key)
			}
			if _, exists := property["anyOf"]; exists {
				t.Fatalf("protocol %s still advertises anyOf on %s", protocol, key)
			}
			if property["type"] != "string" {
				t.Fatalf("protocol %s must advertise the scalar branch on %s, got %#v", protocol, key, property)
			}
			if description, _ := property["description"].(string); strings.TrimSpace(description) == "" {
				t.Fatalf("protocol %s lost the description on %s", protocol, key)
			}
		}
	}
}

// protocolSchemaMaps normalizes the three provider payload shapes into
// name → parameters.
func protocolSchemaMaps(payload interface{}) map[string]map[string]interface{} {
	out := map[string]map[string]interface{}{}
	switch typed := payload.(type) {
	case []map[string]interface{}:
		for _, tool := range typed {
			name, _ := tool["name"].(string)
			if function, ok := tool["function"].(map[string]interface{}); ok {
				if functionName, _ := function["name"].(string); functionName != "" {
					name = functionName
				}
				if params, ok := function["parameters"].(map[string]interface{}); ok && name != "" {
					out[name] = params
				}
				continue
			}
			params, _ := tool["parameters"].(map[string]interface{})
			if params == nil {
				params, _ = tool["input_schema"].(map[string]interface{})
			}
			if name != "" && params != nil {
				out[name] = params
			}
		}
	}
	return out
}

// findUnionProperty walks a JSON schema and returns the dotted path of the
// first node carrying anyOf/oneOf, or "".
func findUnionProperty(node interface{}) string {
	schema, ok := node.(map[string]interface{})
	if !ok {
		return ""
	}
	for _, key := range []string{"anyOf", "oneOf"} {
		if _, exists := schema[key]; exists {
			return key
		}
	}
	for _, key := range []string{"properties", "$defs", "definitions", "dependencies", "patternProperties", "dependentSchemas"} {
		children, ok := schema[key].(map[string]interface{})
		if !ok {
			continue
		}
		for name, child := range children {
			if path := findUnionProperty(child); path != "" {
				return key + "." + name + "." + path
			}
		}
	}
	for _, key := range []string{"items", "additionalProperties", "contains", "contentSchema", "not", "if", "then", "else"} {
		if path := findUnionProperty(schema[key]); path != "" {
			return key + "." + path
		}
	}
	for _, key := range []string{"allOf", "anyOf", "oneOf", "prefixItems"} {
		children, ok := schema[key].([]interface{})
		if !ok {
			continue
		}
		for index, child := range children {
			if path := findUnionProperty(child); path != "" {
				return key + "[]." + path
			}
			_ = index
		}
	}
	return ""
}
