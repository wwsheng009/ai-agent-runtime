package commands

import (
	"os"
	"path/filepath"
	"testing"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestGetMCPConfigPathUsesEffectiveGlobalConfig(t *testing.T) {
	configuredPath := filepath.Join(t.TempDir(), "selected-mcp.yaml")
	if err := os.WriteFile(configuredPath, []byte("mcp:\n  servers: {}\n"), 0o644); err != nil {
		t.Fatalf("write selected MCP config: %v", err)
	}

	previousConfig := agentconfig.GetGlobalConfig()
	previousFlag := mcpConfigFile
	agentconfig.SetGlobalConfig(&agentconfig.Config{
		AICLI: &agentconfig.AICLIConfig{
			MCP: &agentconfig.AICLIMCPConfig{ConfigFile: configuredPath},
		},
	})
	mcpConfigFile = ""
	t.Cleanup(func() {
		agentconfig.SetGlobalConfig(previousConfig)
		mcpConfigFile = previousFlag
	})

	if got := getMCPConfigPath(); got != configuredPath {
		t.Fatalf("getMCPConfigPath() = %q, want effective config path %q", got, configuredPath)
	}
}

func TestGetMCPConfigPathExplicitFlagOverridesEffectiveConfig(t *testing.T) {
	previousConfig := agentconfig.GetGlobalConfig()
	previousFlag := mcpConfigFile
	agentconfig.SetGlobalConfig(&agentconfig.Config{})
	mcpConfigFile = filepath.Join(t.TempDir(), "explicit-mcp.yaml")
	t.Cleanup(func() {
		agentconfig.SetGlobalConfig(previousConfig)
		mcpConfigFile = previousFlag
	})

	if got := getMCPConfigPath(); got != mcpConfigFile {
		t.Fatalf("getMCPConfigPath() = %q, want explicit path %q", got, mcpConfigFile)
	}
}
