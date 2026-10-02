package tools

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

// TestReadToolIsViewAlias pins the alias contract: `read` is the same tool
// under a model/IDE-familiar name (same schema, same execution entry point),
// so a tool call named `read` can never degrade into "tool not found".
func TestReadToolIsViewAlias(t *testing.T) {
	view := NewViewTool()
	read := NewReadTool()

	if view.Name() != "view" {
		t.Fatalf("NewViewTool name = %q, want view", view.Name())
	}
	if read.Name() != "read" {
		t.Fatalf("NewReadTool name = %q, want read", read.Name())
	}
	if !reflect.DeepEqual(view.Parameters(), read.Parameters()) {
		t.Fatal("read alias must expose the same parameter schema as view")
	}
	if read.Description() == "" || read.Description() == view.Description() {
		t.Fatalf("read alias must carry its own alias description, got %q", read.Description())
	}
}

// TestReadAliasRecordsViewLedgerSource keeps the read-before-write guard intact
// when the read arrives under the alias name: the ledger source is a stable
// capability string, never the registered tool name, so a later write is still
// recognized as having read the file.
func TestReadAliasRecordsViewLedgerSource(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	target := filepath.Join(root, "alias.txt")
	if err := os.WriteFile(target, []byte("ledger line\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	ctx := toolctx.WithSessionID(context.Background(), "read-alias-"+t.Name())
	read := NewReadTool()
	read.SetBasePath(root)
	executeViewParams(t, read, ctx, map[string]interface{}{"file_path": target})

	record, ok := ledgerForSession(toolctx.SessionID(ctx)).lookup(target)
	if !ok {
		t.Fatal("expected a read ledger record for the alias read")
	}
	if record.Source != "view" || !record.FullRead {
		t.Fatalf("alias read must record the canonical view source, got %#v", record)
	}
}
