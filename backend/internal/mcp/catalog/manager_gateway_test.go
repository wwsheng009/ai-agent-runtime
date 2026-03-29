package catalog

import (
	"context"
	"testing"

	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	mcpmanager "github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
)

type fakeManager struct {
	tools []*registry.ToolInfo
}

var _ mcpmanager.Manager = (*fakeManager)(nil)

func (m *fakeManager) LoadConfig(configPath string) error { return nil }
func (m *fakeManager) Start(ctx context.Context) error    { return nil }
func (m *fakeManager) Stop() error                        { return nil }
func (m *fakeManager) ListTools() []*registry.ToolInfo    { return m.tools }
func (m *fakeManager) CallTool(ctx context.Context, mcpName, toolName string, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return nil, nil
}
func (m *fakeManager) FindTool(toolName string) (*registry.ToolInfo, error) { return nil, nil }
func (m *fakeManager) ListResources(ctx context.Context, mcpName string, cursor *string) (*protocol.ListResourcesResult, error) {
	return nil, nil
}
func (m *fakeManager) SetMCPEnabled(name string, enabled bool) error { return nil }
func (m *fakeManager) GetMCPStatus(name string) (*mcpconfig.MCPStatus, error) {
	return &mcpconfig.MCPStatus{
		Name:          name,
		TrustLevel:    mcpconfig.MCPTrustLevelLocal,
		ExecutionMode: "local_mcp",
	}, nil
}
func (m *fakeManager) ListMCPs() []*mcpconfig.MCPStatus { return nil }
func (m *fakeManager) ReloadConfig() error              { return nil }

func TestNewManagerGateway_RefreshesCatalogWithStatus(t *testing.T) {
	manager := &fakeManager{
		tools: []*registry.ToolInfo{
			{
				MCPName: "local-server",
				Enabled: true,
				Tool: &protocol.Tool{
					Name:        "read_logs",
					Description: "Read logs",
				},
			},
		},
	}

	gateway := NewManagerGateway(manager)
	results := gateway.Search("logs", 5)
	if len(results) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(results))
	}
	if results[0].MCPTrustLevel != "local" {
		t.Fatalf("expected local trust level, got %s", results[0].MCPTrustLevel)
	}
	if results[0].ExecutionMode != "local_mcp" {
		t.Fatalf("expected local_mcp execution mode, got %s", results[0].ExecutionMode)
	}
}

func TestManagerGateway_RefreshUpdatesSharedCatalog(t *testing.T) {
	manager := &fakeManager{
		tools: []*registry.ToolInfo{
			{
				MCPName: "local-server",
				Enabled: true,
				Tool: &protocol.Tool{
					Name:        "read_logs",
					Description: "Read logs",
				},
			},
		},
	}

	gateway := NewManagerGateway(manager)
	if count := gateway.Catalog().Count(); count != 1 {
		t.Fatalf("expected 1 tool after initial refresh, got %d", count)
	}

	manager.tools = append(manager.tools, &registry.ToolInfo{
		MCPName: "local-server",
		Enabled: true,
		Tool: &protocol.Tool{
			Name:        "run_tests",
			Description: "Run tests",
		},
	})
	gateway.Refresh()

	results := gateway.Search("tests", 5)
	if len(results) != 1 {
		t.Fatalf("expected refreshed catalog to return new tool, got %d", len(results))
	}
	if results[0].Name != "run_tests" {
		t.Fatalf("expected run_tests after refresh, got %s", results[0].Name)
	}

	stats := gateway.RefreshStats()
	if stats.ToolCount != 2 || stats.Added != 1 || stats.Removed != 0 || stats.Updated != 0 {
		t.Fatalf("unexpected refresh stats after add: %+v", stats)
	}
}
