package skills

import (
	"context"
	"os"
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

func TestResolveProfileRuntimeConfig_AppliesSharedSessionDefaults(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "runtime.yaml")
	data := []byte("sessionRuntime:\n  defaultPersistence: file\nsessions:\n  dir: sessions\n")
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatalf("write runtime config: %v", err)
	}

	handler := &Handler{}
	cfg, resolvedPath, err := handler.resolveProfileRuntimeConfig(UsageScope{}, &profilesys.ResolvedAgent{
		RuntimeConfig: configPath,
	})
	if err != nil {
		t.Fatalf("resolveProfileRuntimeConfig failed: %v", err)
	}
	if resolvedPath != configPath {
		t.Fatalf("unexpected runtime path: %q", resolvedPath)
	}
	sessionDir := filepath.Join(root, "sessions")
	if cfg.Sessions.Dir != sessionDir {
		t.Fatalf("expected session dir %q, got %q", sessionDir, cfg.Sessions.Dir)
	}
	if cfg.SessionRuntime.StorePath != filepath.Join(sessionDir, "runtime", "session_runtime.sqlite") {
		t.Fatalf("unexpected session runtime store path: %q", cfg.SessionRuntime.StorePath)
	}
	if cfg.Background.StorePath != filepath.Join(sessionDir, "runtime", "background.sqlite") {
		t.Fatalf("unexpected background store path: %q", cfg.Background.StorePath)
	}
}
