package commands

import (
	"context"
	"fmt"
	"testing"

	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	mcpprotocol "github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	mcpregistry "github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
)

type fakeMCPManager struct {
	statuses []*mcpconfig.MCPStatus
	tools    []*mcpregistry.ToolInfo
}

func (f *fakeMCPManager) LoadConfig(configPath string) error { return nil }
func (f *fakeMCPManager) Start(ctx context.Context) error    { return nil }
func (f *fakeMCPManager) Stop() error                        { return nil }
func (f *fakeMCPManager) ListTools() []*mcpregistry.ToolInfo { return f.tools }
func (f *fakeMCPManager) CallTool(ctx context.Context, mcpName, toolName string, args map[string]interface{}) (*mcpprotocol.CallToolResult, error) {
	return &mcpprotocol.CallToolResult{}, nil
}
func (f *fakeMCPManager) FindTool(toolName string) (*mcpregistry.ToolInfo, error) { return nil, nil }
func (f *fakeMCPManager) ListResources(ctx context.Context, mcpName string, cursor *string) (*mcpprotocol.ListResourcesResult, error) {
	return nil, nil
}
func (f *fakeMCPManager) SetMCPEnabled(name string, enabled bool) error { return nil }
func (f *fakeMCPManager) GetMCPStatus(name string) (*mcpconfig.MCPStatus, error) {
	for _, status := range f.statuses {
		if status != nil && status.Name == name {
			return status, nil
		}
	}
	return nil, fmt.Errorf("not found")
}
func (f *fakeMCPManager) ListMCPs() []*mcpconfig.MCPStatus { return f.statuses }
func (f *fakeMCPManager) ReloadConfig() error              { return nil }

func TestRunMCPListCommandSortsStatuses(t *testing.T) {
	original := MCPManager
	MCPManager = &fakeMCPManager{
		statuses: []*mcpconfig.MCPStatus{
			{Name: "toolkit"},
			{Name: "echo-test"},
		},
	}
	defer func() { MCPManager = original }()

	statuses, err := runMCPListCommand()
	if err != nil {
		t.Fatalf("runMCPListCommand: %v", err)
	}
	if len(statuses) != 2 || statuses[0].Name != "echo-test" || statuses[1].Name != "toolkit" {
		t.Fatalf("unexpected statuses: %+v", statuses)
	}
}

func TestRunMCPToolsCommandBuildsSortedOutputs(t *testing.T) {
	original := MCPManager
	MCPManager = &fakeMCPManager{
		statuses: []*mcpconfig.MCPStatus{
			{Name: "toolkit"},
		},
		tools: []*mcpregistry.ToolInfo{
			{
				MCPName: "toolkit",
				Enabled: true,
				Tool: &mcpprotocol.Tool{
					Name:        "beta",
					Description: "beta tool",
				},
			},
			{
				MCPName: "toolkit",
				Enabled: true,
				Tool: &mcpprotocol.Tool{
					Name:        "alpha",
					Description: "alpha tool",
				},
			},
		},
	}
	defer func() { MCPManager = original }()

	tools, err := runMCPToolsCommand("toolkit")
	if err != nil {
		t.Fatalf("runMCPToolsCommand: %v", err)
	}
	if len(tools) != 2 || tools[0].Name != "alpha" || tools[1].Name != "beta" {
		t.Fatalf("unexpected tools: %+v", tools)
	}
}
