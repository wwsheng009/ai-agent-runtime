package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/lsp"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// bridgeWithUnstartableServer builds a pool whose only member claims the path but
// can never start, so every request degrades to a <lsp_note>. That is the
// deterministic way to exercise "a block really was appended to the receipt"
// without spawning a real language server. The Dial seam also skips the
// executable preflight, so the fixture does not depend on gopls being installed.
func bridgeWithUnstartableServer(t *testing.T, root string, mode lsp.DegradeMode) *lsp.Bridge {
	t.Helper()
	cfg := lsp.Config{
		Enabled: true,
		Servers: []lsp.ServerSpec{{Name: "gopls", Command: "gopls", Extensions: []string{".go"}}},
		Diagnostics: lsp.DiagnosticsConfig{
			Scope:       lsp.ScopeAll,
			WaitMS:      1,
			StartWaitMS: 1,
			DegradeMode: mode,
		},
	}.Normalize()
	bridge := lsp.NewBridgeWithOptions(cfg, root, lsp.BridgeOptions{
		Dial: func(context.Context, lsp.ServerSpec, string, lsp.Logger) (*lsp.DialResult, error) {
			return nil, errors.New("dial refused by test")
		},
	})
	t.Cleanup(func() { bridge.Stop(context.Background()) })
	return bridge
}

func managerWithMutatingStub(t *testing.T, receipt string, target string) *Manager {
	t.Helper()
	manager := &Manager{toolkit: toolkit.NewRegistry()}
	if err := manager.toolkit.Register(toolkitSuccessToolStub{
		name:       "edit",
		content:    receipt,
		outputKind: toolresult.KindText,
		metadata:   map[string]interface{}{"mutated_paths": []string{target}},
	}); err != nil {
		t.Fatalf("register stub tool: %v", err)
	}
	return manager
}

// The declaration is what the render layer uses to keep the diagnostics out of
// the head-only fold, so it must describe the bytes that were actually appended -
// measured, not estimated.
func TestExecuteWithMetaDeclaresAppendedLSPTailBytes(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "main.go")
	if err := os.WriteFile(target, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}

	const receipt = "patch applied to main.go"
	manager := managerWithMutatingStub(t, receipt, target)
	manager.lspBridge = bridgeWithUnstartableServer(t, root, lsp.DegradeHint)

	output, metadata, err := manager.ExecuteWithMeta(context.Background(), "edit", map[string]interface{}{})
	if err != nil {
		t.Fatalf("ExecuteWithMeta failed: %v", err)
	}
	if !strings.HasPrefix(output, receipt) {
		t.Fatalf("the receipt must stay byte-identical (docs/lsp 03 I1), got %q", output)
	}
	if !strings.Contains(output, "<lsp_") {
		t.Fatalf("expected an appended LSP block, got %q", output)
	}
	if want := len(output) - len(receipt); toolresult.ReservedTailBytes(metadata) != want {
		t.Fatalf("reserved tail = %d, want %d (the appended block)", toolresult.ReservedTailBytes(metadata), want)
	}
}

// No pool, no declaration: a result that carries none must not grow a metadata
// key (and must not materialize a map) it never needed.
func TestExecuteWithMetaOmitsReservedBytesWithoutLSPPool(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "main.go")
	if err := os.WriteFile(target, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}

	const receipt = "patch applied to main.go"
	manager := managerWithMutatingStub(t, receipt, target)

	output, metadata, err := manager.ExecuteWithMeta(context.Background(), "edit", map[string]interface{}{})
	if err != nil {
		t.Fatalf("ExecuteWithMeta failed: %v", err)
	}
	if output != receipt {
		t.Fatalf("output must be untouched without a pool, got %q", output)
	}
	if _, declared := metadata[toolresult.MetadataReservedTailBytesKey]; declared {
		t.Fatalf("no block appended must mean no declaration, got %#v", metadata)
	}
}

func TestStampReservedTailBytesOnlyRecordsRealAppends(t *testing.T) {
	if got := stampReservedTailBytes(nil, 0); got != nil {
		t.Fatalf("nothing appended must not materialize metadata, got %#v", got)
	}
	existing := map[string]interface{}{"mutated_paths": []string{"a.go"}}
	got := stampReservedTailBytes(existing, 0)
	if len(got) != 1 || toolresult.ReservedTailBytes(got) != 0 {
		t.Fatalf("a zero append must leave metadata untouched, got %#v", got)
	}
	fresh := stampReservedTailBytes(nil, 128)
	if toolresult.ReservedTailBytes(fresh) != 128 {
		t.Fatalf("declared 128 bytes, read back %d", toolresult.ReservedTailBytes(fresh))
	}
}
