package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

func seedDocumentStub(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("PK\x03\x04document-container"), 0o644); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	return path
}

// TestWriteRefusesDocumentBinaryOverwrite: write must not replace a document
// container with plain text (analysis §3.9-6).
func TestWriteRefusesDocumentBinaryOverwrite(t *testing.T) {
	root := t.TempDir()
	path := seedDocumentStub(t, root, "report.docx")
	tool := NewWriteTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "report.docx",
		"content":   "plain text",
	})
	if err != nil {
		t.Fatalf("unexpected tool error: %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("expected a document refusal, got %+v", result)
	}
	if result.Metadata["document_refused"] != true || result.Metadata["document_extension"] != ".docx" {
		t.Fatalf("expected document refusal metadata, got %#v", result.Metadata)
	}
	if raw, _ := os.ReadFile(path); string(raw) != "PK\x03\x04document-container" {
		t.Fatalf("document must stay untouched, got %q", raw)
	}
}

func TestAppendWriteRefusesDocumentBinary(t *testing.T) {
	root := t.TempDir()
	path := seedDocumentStub(t, root, "deck.pptx")
	tool := NewAppendWriteTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "deck.pptx",
		"content":   "appended",
	})
	if err != nil || result == nil || result.Success {
		t.Fatalf("expected a document refusal, got %+v err=%v", result, err)
	}
	if result.Metadata["document_extension"] != ".pptx" {
		t.Fatalf("expected the extension in metadata, got %#v", result.Metadata)
	}
	if raw, _ := os.ReadFile(path); string(raw) != "PK\x03\x04document-container" {
		t.Fatalf("document must stay untouched, got %q", raw)
	}
}

func TestEditRefusesDocumentBinary(t *testing.T) {
	root := t.TempDir()
	path := seedDocumentStub(t, root, "data.xlsx")
	tool := NewEditTool()
	tool.SetBasePath(root)
	ctx := toolctx.WithSessionID(context.Background(), "test-doc-guard-edit-"+t.Name())
	result, err := tool.Execute(ctx, map[string]interface{}{
		"file_path":  "data.xlsx",
		"old_string": "document",
		"new_string": "changed",
	})
	if err != nil || result == nil || result.Success {
		t.Fatalf("expected a document refusal, got %+v err=%v", result, err)
	}
	if result.Metadata["document_extension"] != ".xlsx" {
		t.Fatalf("expected the extension in metadata, got %#v", result.Metadata)
	}
	if raw, _ := os.ReadFile(path); string(raw) != "PK\x03\x04document-container" {
		t.Fatalf("document must stay untouched, got %q", raw)
	}
}

func TestMultieditRefusesDocumentBinary(t *testing.T) {
	root := t.TempDir()
	path := seedDocumentStub(t, root, "book.epub")
	tool := NewMultieditTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "book.epub",
		"edits": []interface{}{
			map[string]interface{}{"old_string": "document", "new_string": "changed"},
		},
	})
	if err != nil || result == nil || result.Success {
		t.Fatalf("expected a document refusal, got %+v err=%v", result, err)
	}
	if result.Metadata["document_extension"] != ".epub" {
		t.Fatalf("expected the extension in metadata, got %#v", result.Metadata)
	}
	if raw, _ := os.ReadFile(path); string(raw) != "PK\x03\x04document-container" {
		t.Fatalf("document must stay untouched, got %q", raw)
	}
}

// TestDocumentGuardLeavesTextFilesAlone keeps the guard from becoming a
// blanket extension blacklist surprise: .md/.txt writes still work.
func TestDocumentGuardLeavesTextFilesAlone(t *testing.T) {
	root := t.TempDir()
	tool := NewWriteTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "notes.md",
		"content":   "# hello\n",
	})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("text write must keep working: %+v err=%v", result, err)
	}
}

// TestDocumentGuardNormalizesWindowsTrailingDotsAndSpaces: Windows strips
// trailing dots/spaces, so "report.docx." would otherwise bypass the guard and
// still land on report.docx.
func TestDocumentGuardNormalizesWindowsTrailingDotsAndSpaces(t *testing.T) {
	for _, spelling := range []string{
		"report.docx.", "report.docx ", "sub/deck.pptx. ",
		"REPORT.DOCX", "macro.docm", "slides.pptm", "book.xlsm", "legacy.doc", "old.xls", "old.ppt",
		"report.docx::$DATA",
	} {
		root := t.TempDir()
		tool := NewWriteTool()
		tool.SetBasePath(root)
		result, err := tool.Execute(context.Background(), map[string]interface{}{
			"file_path": spelling,
			"content":   "plain text",
		})
		if err != nil || result == nil || result.Success {
			t.Fatalf("%q must be refused, got %+v err=%v", spelling, result, err)
		}
		if result.Metadata["document_refused"] != true {
			t.Fatalf("%q: expected document refusal metadata, got %#v", spelling, result.Metadata)
		}
	}
	// A trailing-dot text file is not a document and stays writable.
	root := t.TempDir()
	tool := NewWriteTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "notes.md.",
		"content":   "# ok\n",
	})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("trailing-dot markdown must stay writable: %+v err=%v", result, err)
	}
}
