package admin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
)

func TestConfigDiagnosticsRefreshesFileState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.yaml")
	service := NewService(path, WithConfigDiagnostics(ConfigDiagnosticsFromResolution(aiclipaths.MCPConfigResolution{
		Path:   path,
		Source: "project",
		Candidates: []aiclipaths.MCPConfigCandidate{
			{Path: path, Source: "project"},
			{Path: filepath.Join(t.TempDir(), "missing.yaml"), Source: "user"},
		},
	})))

	diagnostics := service.ConfigDiagnostics()
	if diagnostics == nil {
		t.Fatal("expected diagnostics")
	}
	if diagnostics.Path != path || diagnostics.Source != "project" {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	if diagnostics.Exists || diagnostics.ManagerLoaded {
		t.Fatalf("expected missing file and unloaded manager: %+v", diagnostics)
	}
	if len(diagnostics.Candidates) != 2 {
		t.Fatalf("candidates = %+v", diagnostics.Candidates)
	}

	if err := os.WriteFile(path, []byte("mcpServers: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	diagnostics = service.ConfigDiagnostics()
	if !diagnostics.Exists || diagnostics.SizeBytes <= 0 || diagnostics.ModTime == "" {
		t.Fatalf("expected refreshed file state: %+v", diagnostics)
	}
}

func TestConfigDiagnosticsWithoutInjectedResolution(t *testing.T) {
	service := NewService(filepath.Join(t.TempDir(), "mcp.yaml"))
	diagnostics := service.ConfigDiagnostics()
	if diagnostics == nil {
		t.Fatal("expected diagnostics")
	}
	if diagnostics.Path != service.ConfigPath() || diagnostics.Exists || diagnostics.Source != "" {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}

	var nilService *Service
	if nilService.ConfigDiagnostics() != nil {
		t.Fatal("expected nil diagnostics for nil service")
	}
}
