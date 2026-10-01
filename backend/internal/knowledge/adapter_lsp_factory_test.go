package knowledge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Phase 4 接线（工具面消费）的构造门控：ADR-0002 §4.1 双重门控 + 语言支持判定。
func TestNewSemanticAdapterForWorkspaceGating(t *testing.T) {
	ctx := context.Background()

	// 1) 硬闸：Enabled=false 时任何入口都不起 LSP。
	disabled := DefaultConfig().WithWorkspace(t.TempDir())
	if adapter, reason := NewSemanticAdapterForWorkspace(disabled, disabled.Workspace); adapter != nil || reason != SemanticReasonDisabled {
		t.Fatalf("disabled: adapter=%v reason=%q, want nil/%q", adapter, reason, SemanticReasonDisabled)
	}

	// 2) Mode 仅 self（external 由宿主侧集成）。
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module demo\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	cfg := DefaultConfig().WithWorkspace(root)
	cfg.LSP.Enabled = true
	cfg.LSP.Mode = "external"
	if adapter, reason := NewSemanticAdapterForWorkspace(cfg, root); adapter != nil || reason != SemanticReasonModeNotSelf {
		t.Fatalf("external mode: adapter=%v reason=%q, want nil/%q", adapter, reason, SemanticReasonModeNotSelf)
	}

	// 3) 非 Go workspace（无 go.mod）不支持。
	plain := t.TempDir()
	cfg.LSP.Mode = "self"
	if adapter, reason := NewSemanticAdapterForWorkspace(cfg, plain); adapter != nil || reason != SemanticReasonNoLanguage {
		t.Fatalf("no go.mod: adapter=%v reason=%q, want nil/%q", adapter, reason, SemanticReasonNoLanguage)
	}

	// 4) 门控通过：构造成功且**不启动进程**（Available=false，首查才 Ensure）。
	adapter, reason := NewSemanticAdapterForWorkspace(cfg, root)
	if adapter == nil || reason != "" {
		t.Fatalf("self+go.mod: adapter=%v reason=%q, want constructed", adapter, reason)
	}
	if adapter.Available() {
		t.Fatal("构造阶段不得启动进程（Available 必须为 false）")
	}
	if !strings.HasPrefix(adapter.Version(), "lsp/") {
		t.Fatalf("Version=%q, want lsp/* 前缀", adapter.Version())
	}
	if closer, ok := adapter.(interface{ Close(context.Context) error }); ok {
		if err := closer.Close(ctx); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
}
