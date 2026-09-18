package main

import (
	"os"
	"path/filepath"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func isolateMCPResolutionHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func TestResolveRuntimeMCPConfigResolutionPrefersProjectFile(t *testing.T) {
	isolateMCPResolutionHome(t)

	projectDir := t.TempDir()
	projectConfig := filepath.Join(projectDir, ".aicli", "mcp.yaml")
	if err := os.MkdirAll(filepath.Dir(projectConfig), 0o755); err != nil {
		t.Fatalf("create project mcp dir: %v", err)
	}
	if err := os.WriteFile(projectConfig, []byte("mcpServers: {}\n"), 0o644); err != nil {
		t.Fatalf("write project mcp config: %v", err)
	}
	t.Chdir(projectDir)

	cfg := &config.Config{AICLI: &config.AICLIConfig{MCP: &config.AICLIMCPConfig{ConfigFile: "configs/mcp.yaml"}}}
	resolution := resolveRuntimeMCPConfigResolution(cfg)
	if resolution.Path != projectConfig || resolution.Source != "project" {
		t.Fatalf("resolution = %+v, want path=%q source=project", resolution, projectConfig)
	}
	if got := resolveRuntimeMCPConfigPath(cfg); got != projectConfig {
		t.Fatalf("resolveRuntimeMCPConfigPath = %q, want %q", got, projectConfig)
	}
}

func TestResolveRuntimeMCPConfigResolutionFallsBackToUserLevel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(t.TempDir())

	cfg := &config.Config{AICLI: &config.AICLIConfig{MCP: &config.AICLIMCPConfig{ConfigFile: "configs/mcp.yaml"}}}
	resolution := resolveRuntimeMCPConfigResolution(cfg)
	want := filepath.Join(home, ".aicli", "mcp.yaml")
	if resolution.Path != want || resolution.Source != "user-fallback" {
		t.Fatalf("resolution = %+v, want path=%q source=user-fallback", resolution, want)
	}
}

func TestResolveRuntimeMCPConfigResolutionEmptyConfig(t *testing.T) {
	if got := resolveRuntimeMCPConfigResolution(nil); got.Path != "" {
		t.Fatalf("nil cfg resolution = %+v, want empty", got)
	}
	cfg := &config.Config{AICLI: &config.AICLIConfig{MCP: &config.AICLIMCPConfig{}}}
	if got := resolveRuntimeMCPConfigResolution(cfg); got.Path != "" {
		t.Fatalf("empty config_file resolution = %+v, want empty", got)
	}
}
