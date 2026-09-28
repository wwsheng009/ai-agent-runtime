package tools

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/lsp"
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

// failingDial counts dial attempts and always fails, modelling a missing
// binary without spawning anything.
func failingDial(calls *int32) lsp.DialFunc {
	return func(context.Context, lsp.ServerSpec, string, lsp.Logger) (*lsp.DialResult, error) {
		atomic.AddInt32(calls, 1)
		return nil, errors.New("missing binary")
	}
}

func waitForLSPCondition(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestNewLSPBridgePrewarmStartsPool(t *testing.T) {
	cfg := lspTestRuntimeConfig(t, false)
	cfg.LSP.Prewarm = true
	cfg.LSP.Servers = []lsp.ServerSpec{{Name: "fake", Command: "fake-lsp", Extensions: []string{".go"}}}
	var calls int32
	bridge := newLSPBridgeWith(cfg, cfg.Workspace.Root, failingDial(&calls), nil)
	if bridge == nil {
		t.Fatalf("bridge must exist while lsp.enabled=true")
	}
	t.Cleanup(func() { bridge.Stop(context.Background()) })

	waitForLSPCondition(t, "prewarm dial", func() bool { return atomic.LoadInt32(&calls) == 1 })
	waitForLSPCondition(t, "unavailable state", func() bool {
		statuses := bridge.Statuses()
		return len(statuses) == 1 && statuses[0].State == lsp.StateUnavailable
	})
	if reason := bridge.Statuses()[0].Reason; !strings.Contains(reason, "missing binary") {
		t.Fatalf("prewarm failure must stay observable, reason = %q", reason)
	}
}

func TestNewLSPBridgeStaysLazyByDefault(t *testing.T) {
	cfg := lspTestRuntimeConfig(t, false)
	cfg.LSP.Servers = []lsp.ServerSpec{{Name: "fake", Command: "fake-lsp", Extensions: []string{".go"}}}
	var calls int32
	bridge := newLSPBridgeWith(cfg, cfg.Workspace.Root, failingDial(&calls), nil)
	if bridge == nil {
		t.Fatalf("bridge must exist while lsp.enabled=true")
	}
	t.Cleanup(func() { bridge.Stop(context.Background()) })

	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("lazy default must not dial at construction, got %d call(s)", got)
	}
	if state := bridge.Statuses()[0].State; state != lsp.StateStarting {
		t.Fatalf("pending member state = %q, want %q (pending first use)", state, lsp.StateStarting)
	}

	if !bridge.Handles(filepath.Join(cfg.Workspace.Root, "main.go")) {
		t.Fatalf("fake spec must claim .go files")
	}
	waitForLSPCondition(t, "lazy dial on first use", func() bool { return atomic.LoadInt32(&calls) == 1 })
}

func TestFormatLSPServerStatusCarriesPoolFacts(t *testing.T) {
	active := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	line := formatLSPServerStatus(lsp.ServerStatus{
		Name:       "gopls",
		State:      lsp.StateReady,
		PID:        4242,
		Restarts:   2,
		LastActive: active,
		Reason:     "ok",
	})
	for _, want := range []string{
		"gopls: ready",
		"pid=4242",
		"restarts=2",
		"last_active=2026-09-28T12:00:00Z",
		"reason=ok",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("lsp_servers line %q must contain %q", line, want)
		}
	}
}
