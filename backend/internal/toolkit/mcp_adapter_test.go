package toolkit

import (
	"context"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

type stubTool struct {
	name        string
	description string
	parameters  map[string]interface{}
	result      *ToolResult
}

func (s stubTool) Name() string { return s.name }

func (s stubTool) Description() string { return s.description }

func (s stubTool) Version() string { return "1.0.0" }

func (s stubTool) Parameters() map[string]interface{} { return s.parameters }

func (s stubTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	return s.result, nil
}

func (s stubTool) CanDirectCall() bool { return true }

func TestMCPAdapter_ExecuteAsMCP_PreservesOutputKindMetadata(t *testing.T) {
	adapter := NewMCPAdapter(stubTool{
		name:        "stub_text",
		description: "stub",
		parameters:  map[string]interface{}{"type": "object"},
		result: &ToolResult{
			Success:    true,
			OutputKind: toolresult.KindText,
			Content:    "hello",
			Metadata: map[string]interface{}{
				"custom": "value",
			},
		},
	})

	result, err := adapter.ExecuteAsMCP(context.Background(), nil)
	if err != nil {
		t.Fatalf("execute as mcp: %v", err)
	}
	if len(result.Content) != 1 || result.Content[0].Type != "text" || result.Content[0].Text != "hello" {
		t.Fatalf("unexpected content: %#v", result.Content)
	}
	if got := result.Meta[toolresult.MetadataKey]; got != toolresult.KindText {
		t.Fatalf("expected %s=%q, got %#v", toolresult.MetadataKey, toolresult.KindText, got)
	}
	if got := result.Meta["custom"]; got != "value" {
		t.Fatalf("expected custom metadata, got %#v", got)
	}
}

func TestMCPAdapter_ExecuteAsMCP_MapsBinaryContent(t *testing.T) {
	adapter := NewMCPAdapter(stubTool{
		name:        "stub_image",
		description: "stub",
		parameters:  map[string]interface{}{"type": "object"},
		result: &ToolResult{
			Success:    true,
			OutputKind: toolresult.KindBinary,
			Data:       []byte("img-bytes"),
			MIMEType:   "image/png",
		},
	})

	result, err := adapter.ExecuteAsMCP(context.Background(), nil)
	if err != nil {
		t.Fatalf("execute as mcp: %v", err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected one content item, got %#v", result.Content)
	}
	content := result.Content[0]
	if content.Type != "image" {
		t.Fatalf("expected image content type, got %#v", content.Type)
	}
	if content.MIMEType != "image/png" {
		t.Fatalf("expected image/png mime type, got %#v", content.MIMEType)
	}
	if got := result.Meta[toolresult.MetadataKey]; got != toolresult.KindBinary {
		t.Fatalf("expected %s=%q, got %#v", toolresult.MetadataKey, toolresult.KindBinary, got)
	}
}
