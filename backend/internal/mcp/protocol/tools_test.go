package protocol

import (
	"encoding/json"
	"testing"
)

func TestTool_JSON(t *testing.T) {
	tool := Tool{
		Name:        "test_tool",
		Description: "A test tool for testing purposes",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"param1": map[string]interface{}{
					"type":        "string",
					"description": "First parameter",
				},
				"param2": map[string]interface{}{
					"type":        "integer",
					"description": "Second parameter",
				},
			},
			"required": []string{"param1"},
		},
	}

	data, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("Failed to marshal tool: %v", err)
	}

	var decoded Tool
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal tool: %v", err)
	}

	if decoded.Name != "test_tool" {
		t.Errorf("Expected tool name 'test_tool', got %s", decoded.Name)
	}

	if decoded.Description != "A test tool for testing purposes" {
		t.Errorf("Expected description 'A test tool for testing purposes', got %s", decoded.Description)
	}

	if decoded.InputSchema == nil {
		t.Fatal("Expected input schema, got nil")
	}

	props, ok := decoded.InputSchema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected properties in input schema")
	}

	if _, exists := props["param1"]; !exists {
		t.Error("Expected param1 to exist in properties")
	}

	if _, exists := props["param2"]; !exists {
		t.Error("Expected param2 to exist in properties")
	}

	required, ok := decoded.InputSchema["required"].([]interface{})
	if !ok {
		t.Fatal("Expected required parameter in input schema")
	}

	if len(required) != 1 || required[0] != "param1" {
		t.Errorf("Expected required param1, got %v", required)
	}
}

func TestTool_Minimal(t *testing.T) {
	tool := Tool{
		Name:        "minimal_tool",
		Description: "A minimal tool",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}

	data, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("Failed to marshal minimal tool: %v", err)
	}

	var decoded Tool
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal minimal tool: %v", err)
	}

	if decoded.Name != "minimal_tool" {
		t.Errorf("Expected tool name 'minimal_tool', got %s", decoded.Name)
	}

	if decoded.InputSchema["type"] != "object" {
		t.Error("Expected input schema type to be 'object'")
	}
}

func TestResource_JSON(t *testing.T) {
	priority := 1.0
	annotations := &ResourceAnnotations{
		Audience: []string{"user", "internal"},
		Priority: &priority,
	}

	resource := Resource{
		URI:         "file:///path/to/file.txt",
		Name:        "Test File",
		Description: "A test resource file",
		MIMEType:    "text/plain",
		Annotations: annotations,
	}

	data, err := json.Marshal(resource)
	if err != nil {
		t.Fatalf("Failed to marshal resource: %v", err)
	}

	var decoded Resource
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal resource: %v", err)
	}

	if decoded.URI != "file:///path/to/file.txt" {
		t.Errorf("Expected URI 'file:///path/to/file.txt', got %s", decoded.URI)
	}

	if decoded.Name != "Test File" {
		t.Errorf("Expected name 'Test File', got %s", decoded.Name)
	}

	if decoded.Description != "A test resource file" {
		t.Errorf("Expected description 'A test resource file', got %s", decoded.Description)
	}

	if decoded.MIMEType != "text/plain" {
		t.Errorf("Expected MIME type 'text/plain', got %s", decoded.MIMEType)
	}

	if decoded.Annotations == nil {
		t.Fatal("Expected annotations, got nil")
	}

	if len(decoded.Annotations.Audience) != 2 {
		t.Fatalf("Expected 2 audience entries, got %d", len(decoded.Annotations.Audience))
	}

	if decoded.Annotations.Priority == nil || *decoded.Annotations.Priority != 1.0 {
		t.Error("Expected priority 1.0")
	}
}

func TestResource_Minimal(t *testing.T) {
	resource := Resource{
		URI:  "file:///path/to/file.txt",
		Name: "Minimal File",
	}

	data, err := json.Marshal(resource)
	if err != nil {
		t.Fatalf("Failed to marshal minimal resource: %v", err)
	}

	var decoded Resource
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal minimal resource: %v", err)
	}

	if decoded.URI != "file:///path/to/file.txt" {
		t.Errorf("Expected URI 'file:///path/to/file.txt', got %s", decoded.URI)
	}

	if decoded.Name != "Minimal File" {
		t.Errorf("Expected name 'Minimal File', got %s", decoded.Name)
	}

	if decoded.Description != "" {
		t.Errorf("Expected empty description, got %s", decoded.Description)
	}

	if decoded.Annotations != nil {
		t.Error("Expected nil annotations for minimal resource")
	}
}

func TestResourceAnnotations_JSON(t *testing.T) {
	priority := 0.5
	annotations := ResourceAnnotations{
		Audience: []string{"admin"},
		Priority: &priority,
	}

	data, err := json.Marshal(annotations)
	if err != nil {
		t.Fatalf("Failed to marshal resource annotations: %v", err)
	}

	var decoded ResourceAnnotations
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal resource annotations: %v", err)
	}

	if len(decoded.Audience) != 1 {
		t.Fatalf("Expected 1 audience entry, got %d", len(decoded.Audience))
	}

	if decoded.Audience[0] != "admin" {
		t.Errorf("Expected audience 'admin', got %s", decoded.Audience[0])
	}

	if decoded.Priority == nil || *decoded.Priority != 0.5 {
		t.Error("Expected priority 0.5")
	}
}

func TestResourceAnnotations_WithoutPriority(t *testing.T) {
	annotations := ResourceAnnotations{
		Audience: []string{"user"},
	}

	data, err := json.Marshal(annotations)
	if err != nil {
		t.Fatalf("Failed to marshal resource annotations without priority: %v", err)
	}

	var decoded ResourceAnnotations
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal resource annotations without priority: %v", err)
	}

	if decoded.Priority != nil {
		t.Error("Expected nil priority")
	}
}

func TestPrompt_JSON(t *testing.T) {
	prompt := Prompt{
		Name:        "test_prompt",
		Description: "A test prompt",
		Arguments: []PromptArgument{
			{
				Name:        "topic",
				Description: "The topic to write about",
				Required:    true,
			},
			{
				Name:        "length",
				Description: "The length of the output",
				Required:    false,
			},
		},
	}

	data, err := json.Marshal(prompt)
	if err != nil {
		t.Fatalf("Failed to marshal prompt: %v", err)
	}

	var decoded Prompt
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal prompt: %v", err)
	}

	if decoded.Name != "test_prompt" {
		t.Errorf("Expected prompt name 'test_prompt', got %s", decoded.Name)
	}

	if decoded.Description != "A test prompt" {
		t.Errorf("Expected description 'A test prompt', got %s", decoded.Description)
	}

	if len(decoded.Arguments) != 2 {
		t.Fatalf("Expected 2 arguments, got %d", len(decoded.Arguments))
	}

	if decoded.Arguments[0].Name != "topic" {
		t.Errorf("Expected first argument name 'topic', got %s", decoded.Arguments[0].Name)
	}

	if !decoded.Arguments[0].Required {
		t.Error("Expected first argument to be required")
	}

	if decoded.Arguments[1].Name != "length" {
		t.Errorf("Expected second argument name 'length', got %s", decoded.Arguments[1].Name)
	}

	if decoded.Arguments[1].Required {
		t.Error("Expected second argument to not be required")
	}
}

func TestPrompt_Minimal(t *testing.T) {
	prompt := Prompt{
		Name: "minimal_prompt",
	}

	data, err := json.Marshal(prompt)
	if err != nil {
		t.Fatalf("Failed to marshal minimal prompt: %v", err)
	}

	var decoded Prompt
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal minimal prompt: %v", err)
	}

	if decoded.Name != "minimal_prompt" {
		t.Errorf("Expected prompt name 'minimal_prompt', got %s", decoded.Name)
	}

	if len(decoded.Arguments) != 0 {
		t.Errorf("Expected no arguments, got %d", len(decoded.Arguments))
	}
}

func TestPromptArgument_JSON(t *testing.T) {
	arg := PromptArgument{
		Name:        "test_arg",
		Description: "A test argument",
		Required:    true,
	}

	data, err := json.Marshal(arg)
	if err != nil {
		t.Fatalf("Failed to marshal prompt argument: %v", err)
	}

	var decoded PromptArgument
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal prompt argument: %v", err)
	}

	if decoded.Name != "test_arg" {
		t.Errorf("Expected argument name 'test_arg', got %s", decoded.Name)
	}

	if decoded.Description != "A test argument" {
		t.Errorf("Expected description 'A test argument', got %s", decoded.Description)
	}

	if !decoded.Required {
		t.Error("Expected argument to be required")
	}
}

func TestPromptArgument_Optional(t *testing.T) {
	arg := PromptArgument{
		Name: "optional_arg",
	}

	data, err := json.Marshal(arg)
	if err != nil {
		t.Fatalf("Failed to marshal optional prompt argument: %v", err)
	}

	var decoded PromptArgument
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal optional prompt argument: %v", err)
	}

	if decoded.Name != "optional_arg" {
		t.Errorf("Expected argument name 'optional_arg', got %s", decoded.Name)
	}

	// Required defaults to false
	if decoded.Required {
		t.Error("Expected argument to not be required")
	}
}
