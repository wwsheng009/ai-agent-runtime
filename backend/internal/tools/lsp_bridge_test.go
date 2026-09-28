package tools

import (
	"context"
	"strings"
	"testing"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

func lspTestRuntimeConfig(t *testing.T, toolEnabled bool) *runtimecfg.RuntimeConfig {
	t.Helper()
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Workspace.Root = t.TempDir()
	cfg.LSP.Enabled = true
	cfg.LSP.Diagnostics.ToolEnabled = toolEnabled
	return cfg
}

func descriptorIndex(descriptors []ToolDescriptor) map[string]ToolDescriptor {
	index := make(map[string]ToolDescriptor, len(descriptors))
	for _, descriptor := range descriptors {
		index[descriptor.Name] = descriptor
	}
	return index
}

func TestLSPToolsRegisteredWhileEnabled(t *testing.T) {
	cfg := lspTestRuntimeConfig(t, false)
	manager := NewDefaultManagerWithRuntimeConfig(nil, cfg)
	t.Cleanup(func() { _ = manager.Close() })

	index := descriptorIndex(manager.ListTools())
	if _, ok := index[LSPServersToolName]; !ok {
		t.Fatalf("%s must be registered while the pool is enabled", LSPServersToolName)
	}
	if _, ok := index[LSPDiagnosticsToolName]; ok {
		t.Fatalf("%s is opt-in through diagnostics.tool_enabled and must be absent", LSPDiagnosticsToolName)
	}
	edit, ok := index["edit"]
	if !ok {
		t.Fatalf("edit tool missing from the registry")
	}
	if !strings.Contains(edit.Description, "lsp_diagnostics") {
		t.Fatalf("edit description must carry the inline-diagnostics expectation:\n%s", edit.Description)
	}
}

func TestLSPToolsAbsentWhenDisabled(t *testing.T) {
	cfg := runtimecfg.DefaultRuntimeConfig()
	manager := NewDefaultManagerWithRuntimeConfig(nil, cfg)
	t.Cleanup(func() { _ = manager.Close() })

	index := descriptorIndex(manager.ListTools())
	if _, ok := index[LSPServersToolName]; ok {
		t.Fatalf("%s must not be registered while LSP is disabled (A11)", LSPServersToolName)
	}
	if _, ok := index[LSPDiagnosticsToolName]; ok {
		t.Fatalf("%s must not be registered while LSP is disabled", LSPDiagnosticsToolName)
	}
	if edit, ok := index["edit"]; ok && strings.Contains(edit.Description, "lsp_diagnostics") {
		t.Fatalf("disabled pool must not change edit's description:\n%s", edit.Description)
	}
}

func TestLSPDiagnosticsToolOptIn(t *testing.T) {
	cfg := lspTestRuntimeConfig(t, true)
	manager := NewDefaultManagerWithRuntimeConfig(nil, cfg)
	t.Cleanup(func() { _ = manager.Close() })

	index := descriptorIndex(manager.ListTools())
	if _, ok := index[LSPDiagnosticsToolName]; !ok {
		t.Fatalf("%s must be registered when diagnostics.tool_enabled=true", LSPDiagnosticsToolName)
	}
	output, err := manager.Execute(context.Background(), LSPDiagnosticsToolName, map[string]interface{}{
		"path": "notes.txt",
	})
	if err != nil {
		t.Fatalf("unhandled path must not fail the tool: %v", err)
	}
	if !strings.Contains(output, "没有已配置的语言服务器负责") {
		t.Fatalf("unhandled path must be an explicit silent skip, got:\n%s", output)
	}
}

func TestLSPServersToolReportsPool(t *testing.T) {
	cfg := lspTestRuntimeConfig(t, false)
	manager := NewDefaultManagerWithRuntimeConfig(nil, cfg)
	t.Cleanup(func() { _ = manager.Close() })

	output, err := manager.Execute(context.Background(), LSPServersToolName, map[string]interface{}{})
	if err != nil {
		t.Fatalf("lsp_servers failed: %v", err)
	}
	if !strings.Contains(output, "LSP pool:") || !strings.Contains(output, "gopls") {
		t.Fatalf("lsp_servers must list the preset pool:\n%s", output)
	}
}
