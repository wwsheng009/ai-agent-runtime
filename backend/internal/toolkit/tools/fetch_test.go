package tools

import (
	"context"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

func TestFetchTool_Interface(t *testing.T) {
	tool := NewFetchTool()

	var _ toolkit.Tool = tool

	if tool.Name() != "fetch" {
		t.Errorf("expected name 'fetch', got '%s'", tool.Name())
	}

	if tool.Description() == "" {
		t.Error("description should not be empty")
	}

	if !tool.CanDirectCall() {
		t.Error("fetch tool should support direct call")
	}
}

func TestFetchTool_MissingURL(t *testing.T) {
	tool := NewFetchTool()

	result, err := tool.Execute(context.Background(), map[string]interface{}{})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Success {
		t.Error("expected failure for missing URL")
	}
}

func TestFetchTool_InvalidURL(t *testing.T) {
	tool := NewFetchTool()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"url": "not-a-valid-url",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Success {
		t.Error("expected failure for invalid URL")
	}
}
