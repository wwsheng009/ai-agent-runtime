package commands

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	mcpprotocol "github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	mcpregistry "github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
)

type trackingMCPManager struct {
	stopCalled bool
}

func (m *trackingMCPManager) LoadConfig(configPath string) error { return nil }
func (m *trackingMCPManager) Start(ctx context.Context) error    { return nil }
func (m *trackingMCPManager) Stop() error {
	m.stopCalled = true
	return nil
}
func (m *trackingMCPManager) ListTools() []*mcpregistry.ToolInfo { return nil }
func (m *trackingMCPManager) CallTool(ctx context.Context, mcpName, toolName string, args map[string]interface{}) (*mcpprotocol.CallToolResult, error) {
	return nil, nil
}
func (m *trackingMCPManager) FindTool(toolName string) (*mcpregistry.ToolInfo, error) {
	return nil, nil
}
func (m *trackingMCPManager) ListResources(ctx context.Context, mcpName string, cursor *string) (*mcpprotocol.ListResourcesResult, error) {
	return nil, nil
}
func (m *trackingMCPManager) SetMCPEnabled(name string, enabled bool) error { return nil }
func (m *trackingMCPManager) GetMCPStatus(name string) (*mcpconfig.MCPStatus, error) {
	return nil, nil
}
func (m *trackingMCPManager) ListMCPs() []*mcpconfig.MCPStatus { return nil }
func (m *trackingMCPManager) ReloadConfig() error              { return nil }

func TestResolveChatMCPStartupConfigPath_SkipsMissingConfigWhenAutoConnectDisabled(t *testing.T) {
	cfg := &config.Config{
		AICLI: &config.AICLIConfig{
			MCP: &config.AICLIMCPConfig{
				ConfigFile:  filepath.Join(t.TempDir(), "missing.yaml"),
				AutoConnect: false,
			},
		},
	}

	path, shouldInit := resolveChatMCPStartupConfigPath(cfg, nil)
	if shouldInit {
		t.Fatalf("expected missing MCP config to be skipped when auto_connect is false, got path=%q", path)
	}
	if path != "" {
		t.Fatalf("expected empty path when MCP init is skipped, got %q", path)
	}
}

func TestResolveChatMCPStartupConfigPath_WarnsForMissingConfigWhenAutoConnectEnabled(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "missing.yaml")
	cfg := &config.Config{
		AICLI: &config.AICLIConfig{
			MCP: &config.AICLIMCPConfig{
				ConfigFile:  missingPath,
				AutoConnect: true,
			},
		},
	}

	path, shouldInit := resolveChatMCPStartupConfigPath(cfg, nil)
	if !shouldInit {
		t.Fatal("expected missing MCP config to remain actionable when auto_connect is true")
	}
	if path != missingPath {
		t.Fatalf("expected path %q, got %q", missingPath, path)
	}
}

func TestResolveChatMCPStartupConfigPath_UsesExistingConfigEvenWhenAutoConnectDisabled(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "mcp.yaml")
	if err := os.WriteFile(configPath, []byte("mcp_servers: {}\n"), 0o644); err != nil {
		t.Fatalf("write mcp config: %v", err)
	}
	cfg := &config.Config{
		AICLI: &config.AICLIConfig{
			MCP: &config.AICLIMCPConfig{
				ConfigFile:  configPath,
				AutoConnect: false,
			},
		},
	}

	path, shouldInit := resolveChatMCPStartupConfigPath(cfg, nil)
	if !shouldInit {
		t.Fatal("expected existing MCP config to initialize")
	}
	if path != configPath {
		t.Fatalf("expected path %q, got %q", configPath, path)
	}
}

func TestPrepareChatMCPManager_StopsExistingManagerWhenConfigMissingAndAutoConnectDisabled(t *testing.T) {
	originalManager := MCPManagerInstance
	originalPath := mcpManagerConfigPath
	defer func() {
		MCPManagerInstance = originalManager
		mcpManagerConfigPath = originalPath
	}()

	manager := &trackingMCPManager{}
	MCPManagerInstance = manager
	mcpManagerConfigPath = "existing.yaml"

	cfg := &config.Config{
		AICLI: &config.AICLIConfig{
			MCP: &config.AICLIMCPConfig{
				ConfigFile:  filepath.Join(t.TempDir(), "missing.yaml"),
				AutoConnect: false,
			},
		},
	}

	if err := prepareChatMCPManager(cfg, nil); err != nil {
		t.Fatalf("prepareChatMCPManager: %v", err)
	}
	if !manager.stopCalled {
		t.Fatal("expected existing MCP manager to be stopped")
	}
	if MCPManagerInstance != nil {
		t.Fatal("expected MCP manager instance to be cleared")
	}
	if mcpManagerConfigPath != "" {
		t.Fatalf("expected MCP manager config path to be cleared, got %q", mcpManagerConfigPath)
	}
}
