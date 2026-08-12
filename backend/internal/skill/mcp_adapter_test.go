package skill

import (
	"context"
	"strings"
	"testing"

	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	mcpmanager "github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	mcpregistry "github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
)

type fakeManager struct {
	result     *protocol.CallToolResult
	tools      []*mcpregistry.ToolInfo
	calledMCP  string
	calledTool string
}

func (f *fakeManager) LoadConfig(configPath string) error { return nil }
func (f *fakeManager) Start(ctx context.Context) error    { return nil }
func (f *fakeManager) Stop() error                        { return nil }
func (f *fakeManager) ListTools() []*mcpregistry.ToolInfo { return f.tools }
func (f *fakeManager) FindTool(toolName string) (*mcpregistry.ToolInfo, error) {
	return &mcpregistry.ToolInfo{
		Tool: &protocol.Tool{
			Name: toolName,
			Metadata: map[string]interface{}{
				"supports_parallel": true,
			},
		},
		Metadata: map[string]interface{}{
			"supports_parallel": true,
		},
		MCPName: "fake-mcp",
		Enabled: true,
	}, nil
}
func (f *fakeManager) CallTool(ctx context.Context, mcpName, toolName string, args map[string]interface{}) (*protocol.CallToolResult, error) {
	f.calledMCP = mcpName
	f.calledTool = toolName
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

func TestMCPAdapter_CallToolWithMeta_IsErrorBecomesError(t *testing.T) {
	adapter := NewMCPAdapter(&fakeManager{
		result: &protocol.CallToolResult{
			IsError: true,
			Content: []protocol.Content{
				{Type: "text", Text: "path not found: missing.txt"},
			},
			Meta: map[string]any{
				"error_code": "TOOL_PATH_NOT_FOUND",
			},
		},
	})

	output, meta, err := adapter.CallToolWithMeta(context.Background(), "fake-mcp", "view", map[string]interface{}{"file_path": "missing.txt"})
	if err == nil {
		t.Fatal("expected IsError to surface as non-nil error")
	}
	if output.(string) != "path not found: missing.txt" {
		t.Fatalf("expected error text as output, got %#v", output)
	}
	if meta["error_code"] != "TOOL_PATH_NOT_FOUND" {
		t.Fatalf("expected error_code metadata preserved, got %#v", meta)
	}
	if !strings.Contains(err.Error(), "path not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMCPAdapter_ListTools_PreservesDefinitionMetadata(t *testing.T) {
	adapter := NewMCPAdapter(&fakeManager{
		tools: []*mcpregistry.ToolInfo{
			{
				Tool: &protocol.Tool{
					Name:        "read_a",
					Description: "read a",
					InputSchema: map[string]interface{}{"type": "object"},
					Metadata: map[string]interface{}{
						"supports_parallel": true,
					},
				},
				Metadata: map[string]interface{}{
					"supports_parallel": true,
				},
				MCPName: "fake-mcp",
				Enabled: true,
			},
		},
	})

	tools := adapter.ListTools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if got := tools[0].Metadata["supports_parallel"]; got != true {
		t.Fatalf("expected supports_parallel=true, got %#v", got)
	}
}

func TestMCPAdapter_CallToolWithMetaHonorsExplicitServer(t *testing.T) {
	manager := &fakeManager{
		result: &protocol.CallToolResult{Content: []protocol.Content{{Type: "text", Text: "docs result"}}},
	}
	adapter := NewMCPAdapter(manager)

	if _, _, err := adapter.CallToolWithMeta(context.Background(), "docs", "search", nil); err != nil {
		t.Fatalf("explicit server call failed: %v", err)
	}
	if manager.calledMCP != "docs" || manager.calledTool != "search" {
		t.Fatalf("explicit server was ignored: mcp=%q tool=%q", manager.calledMCP, manager.calledTool)
	}
}

func TestMCPAdapter_CallToolWithMetaPrefersCanonicalIdentityOverRawAlias(t *testing.T) {
	manager := &fakeManager{
		result: &protocol.CallToolResult{},
		tools: []*mcpregistry.ToolInfo{
			{MCPName: "docs", Enabled: true, Tool: &protocol.Tool{Name: "mcp__docs__search"}},
			{MCPName: "docs", Enabled: true, Tool: &protocol.Tool{Name: "search"}},
			{MCPName: "issues", Enabled: true, Tool: &protocol.Tool{Name: "search"}},
		},
	}
	adapter := NewMCPAdapter(manager)

	if _, _, err := adapter.CallToolWithMeta(context.Background(), "docs", "mcp__docs__search", nil); err != nil {
		t.Fatalf("canonical call failed: %v", err)
	}
	if manager.calledMCP != "docs" || manager.calledTool != "search" {
		t.Fatalf("canonical identity was shadowed by raw alias: mcp=%q tool=%q", manager.calledMCP, manager.calledTool)
	}
}

func TestMCPAdapter_CallToolWithMetaKeepsCanonicalShapedRawToolAddressable(t *testing.T) {
	manager := &fakeManager{
		result: &protocol.CallToolResult{},
		tools: []*mcpregistry.ToolInfo{
			{MCPName: "docs", Enabled: true, Tool: &protocol.Tool{Name: "mcp__docs__search"}},
			{MCPName: "docs", Enabled: true, Tool: &protocol.Tool{Name: "search"}},
		},
	}
	adapter := NewMCPAdapter(manager)

	shadowCanonicalName := "mcp__docs__mcp__docs__search"
	if _, _, err := adapter.CallToolWithMeta(context.Background(), "docs", shadowCanonicalName, nil); err != nil {
		t.Fatalf("shadowing raw tool call failed: %v", err)
	}
	if manager.calledMCP != "docs" || manager.calledTool != shadowCanonicalName {
		t.Fatalf("shadowing raw tool lost its identity: mcp=%q tool=%q", manager.calledMCP, manager.calledTool)
	}
}
