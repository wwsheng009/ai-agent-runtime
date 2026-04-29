package main

import (
	"context"
	"path/filepath"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

func TestResolvePathFromConfigFile(t *testing.T) {
	configFile := filepath.Join("backend", "configs", "runtime.yaml")
	resolved := resolvePathFromConfigFile(configFile, "../data/runtime/sessions")

	expected := filepath.Clean(filepath.Join("backend", "data", "runtime", "sessions"))
	if resolved != expected {
		t.Fatalf("expected %q, got %q", expected, resolved)
	}
}

func TestResolveRuntimeServerSessionDir_UsesAICLIDefaultWhenUnset(t *testing.T) {
	configFile := filepath.Join("backend", "configs", "runtime.yaml")
	resolved := resolveRuntimeServerSessionDir(configFile, "")

	if resolved != aiclipaths.DefaultSessionsDir() {
		t.Fatalf("expected default session dir %q, got %q", aiclipaths.DefaultSessionsDir(), resolved)
	}
}

func TestResolveRuntimeServerSessionDir_ResolvesConfiguredRelativePathFromConfigFile(t *testing.T) {
	configFile := filepath.Join("backend", "configs", "runtime.yaml")
	resolved := resolveRuntimeServerSessionDir(configFile, "../data/runtime/sessions")

	expected := filepath.Clean(filepath.Join("backend", "data", "runtime", "sessions"))
	if resolved != expected {
		t.Fatalf("expected %q, got %q", expected, resolved)
	}
}

func TestBuildSkillsMCPManager_ExposesLocalToolkitWithoutExternalMCP(t *testing.T) {
	runtimeConfig := runtimecfg.DefaultRuntimeConfig()
	runtimeConfig.Workspace.Root = t.TempDir()

	adapter, manager, err := buildSkillsMCPManager(context.Background(), &config.Config{}, runtimeConfig)
	if err != nil {
		t.Fatalf("buildSkillsMCPManager failed: %v", err)
	}
	if manager != nil {
		t.Fatalf("expected nil external MCP manager, got %#v", manager)
	}
	if adapter == nil {
		t.Fatal("expected local runtime tool adapter")
	}

	shellTool, err := adapter.FindTool("execute_shell_command")
	if err != nil {
		t.Fatalf("expected execute_shell_command to be exposed: %v", err)
	}
	if shellTool.MCPName != "" {
		t.Fatalf("expected local shell tool without MCP name, got %+v", shellTool)
	}

	readTool, err := adapter.FindTool("grep")
	if err != nil {
		t.Fatalf("expected grep to be exposed: %v", err)
	}
	if readTool.MCPName != "" {
		t.Fatalf("expected local grep tool without MCP name, got %+v", readTool)
	}
}
