package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteRefusesDocumentReachedThroughSymlinkAlias pins finding H16: the
// document guard checked the spelling the caller passed, so a .txt alias
// pointing at report.rtf let a text write replace the real document.
func TestWriteRefusesDocumentReachedThroughSymlinkAlias(t *testing.T) {
	ctx := dedupSessionContext(t)
	root := t.TempDir()
	document := filepath.Join(root, "report.rtf")
	if err := os.WriteFile(document, []byte(`{\rtf1\ansi hello}`), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias.txt")
	if err := os.Symlink(document, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	writer := NewWriteTool()
	writer.SetBasePath(root)
	result, err := writer.Execute(ctx, map[string]interface{}{"file_path": "alias.txt", "content": "plain text\n"})
	if err != nil || result == nil {
		t.Fatalf("write: %v %+v", err, result)
	}
	if result.Success {
		t.Fatal("writing through a .txt alias into a document must be refused")
	}
	if raw, _ := os.ReadFile(document); !strings.Contains(string(raw), `\rtf1`) {
		t.Fatalf("the real document must stay untouched, got %q", raw)
	}

	// A plain-text link target stays writable.
	plain := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(plain, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plainAlias := filepath.Join(root, "notes-alias.txt")
	if err := os.Symlink(plain, plainAlias); err != nil {
		t.Fatal(err)
	}
	plainResult, err := writer.Execute(ctx, map[string]interface{}{"file_path": "notes-alias.txt", "content": "new\n"})
	if err != nil || plainResult == nil || !plainResult.Success {
		t.Fatalf("a plain-text alias target must stay writable: %v %+v", err, plainResult)
	}
}
