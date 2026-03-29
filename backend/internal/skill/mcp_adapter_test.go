package skill

import (
	"context"
	"testing"

	mcpconfig "github.com/ai-gateway/ai-agent-runtime/internal/mcp/config"
	mcpmanager "github.com/ai-gateway/ai-agent-runtime/internal/mcp/manager"
	"github.com/ai-gateway/ai-agent-runtime/internal/mcp/protocol"
	mcpregistry "github.com/ai-gateway/ai-agent-runtime/internal/mcp/registry"
)

type fakeManager struct {
	result *protocol.CallToolResult
}

func (f *fakeManager) LoadConfig(configPath string) error { return nil }
func (f *fakeManager) Start(ctx context.Context) error    { return nil }
func (f *fakeManager) Stop() error                        { return nil }
func (f *fakeManager) ListTools() []*mcpregistry.ToolInfo { return nil }
func (f *fakeManager) FindTool(toolName string) (*mcpregistry.ToolInfo, error) {
	return &mcpregistry.ToolInfo{
		Tool:    &protocol.Tool{Name: toolName},
		MCPName: "fake-mcp",
		Enabled: true,
	}, nil
}
func (f *fakeManager) CallTool(ctx context.Context, mcpName, toolName string, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return f.result, nil
}
func (f *fakeManager) ListResources(ctx context.Context, mcpName string, cursor *string) (*protocol.ListResourcesResult, error) {
	return nil, nil
}
func (f *fakeManager) SetMCPEnabled(name string, enabled bool) error { return nil }
func (f *fakeManager) GetMCPStatus(name string) (*mcpconfig.MCPStatus, error) {
	return &mcpconfig.MCPStatus{Name: name}, nil
}
func (f *fakeManager) ListMCPs() []*mcpconfig.MCPStatus { return nil }
func (f *fakeManager) ReloadConfig() error              { return nil }

var _ mcpmanager.Manager = (*fakeManager)(nil)

func TestMCPAdapter_CallToolWithMeta_PreservesMetadata(t *testing.T) {
	adapter := NewMCPAdapter(&fakeManager{
		result: &protocol.CallToolResult{
			Content: []protocol.Content{
				{Type: "text", Text: "tool output"},
			},
			Meta: map[string]any{
				"file_path": "workspace/output.txt",
				"action":    "created",
			},
		},
	})

	output, meta, err := adapter.CallToolWithMeta(context.Background(), "fake-mcp", "write_file", map[string]interface{}{"path": "workspace/output.txt"})
	if err != nil {
		t.Fatalf("CallToolWithMeta returned error: %v", err)
	}
	if output.(string) != "tool output" {
		t.Fatalf("expected tool output, got %#v", output)
	}
	if meta["file_path"] != "workspace/output.txt" {
		t.Fatalf("expected file_path metadata, got %#v", meta)
	}
	if meta["action"] != "created" {
		t.Fatalf("expected action metadata, got %#v", meta)
	}
}
