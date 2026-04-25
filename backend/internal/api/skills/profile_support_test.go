package skills

import (
	"context"
	"path/filepath"
	"testing"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

func TestResolveProfileMCPAdapter_ProvidesLocalToolkitWithoutConfiguredMCP(t *testing.T) {
	handler := &Handler{}
	runtimeConfig := runtimecfg.DefaultRuntimeConfig()
	runtimeConfig.Workspace.Root = t.TempDir()

	adapter, manager, err := handler.resolveProfileMCPAdapter(context.Background(), nil, runtimeConfig)
	if err != nil {
		t.Fatalf("resolveProfileMCPAdapter failed: %v", err)
	}
	if manager != nil {
		t.Fatalf("expected nil external MCP manager, got %#v", manager)
	}
	if adapter == nil {
		t.Fatal("expected local runtime tool adapter")
	}
	if _, err := adapter.FindTool("execute_shell_command"); err != nil {
		t.Fatalf("expected execute_shell_command to be available: %v", err)
	}
}

func TestResolveProfileMCPAdapter_KeepsLocalToolkitWhenProfileAutoConnectDisabled(t *testing.T) {
	handler := &Handler{profileMCPAutoConnect: false}
	runtimeConfig := runtimecfg.DefaultRuntimeConfig()
	runtimeConfig.Workspace.Root = t.TempDir()

	adapter, manager, err := handler.resolveProfileMCPAdapter(context.Background(), &profilesys.ResolvedAgent{
		MCPConfig: filepath.Join(t.TempDir(), "mcp.yaml"),
	}, runtimeConfig)
	if err != nil {
		t.Fatalf("resolveProfileMCPAdapter failed: %v", err)
	}
	if manager != nil {
		t.Fatalf("expected nil external MCP manager, got %#v", manager)
	}
	if adapter == nil {
		t.Fatal("expected local runtime tool adapter")
	}
	if _, err := adapter.FindTool("grep"); err != nil {
		t.Fatalf("expected grep to be available: %v", err)
	}
}
