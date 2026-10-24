package remotetools_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// TestRemoteAppendChunkContract pins the chunked-write behavior over MCP. The
// append tool exists for long generations, so the first chunk overwrites, later
// chunks append, and expected_offset makes a retry idempotent instead of
// duplicating content.
func TestRemoteAppendChunkContract(t *testing.T) {
	root := t.TempDir()
	server, err := remotetools.NewServer(newStandardTestConfig(t, root), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	// First chunk creates the file; no separate create call is required.
	callTool(t, ctx, session, "remote_dev__append_write", map[string]interface{}{
		"file_path":      "notes.md",
		"content":        "first",
		"truncate_first": true,
	})
	target := filepath.Join(root, "notes.md")
	if raw, readErr := os.ReadFile(target); readErr != nil || string(raw) != "first" {
		t.Fatalf("first chunk did not land: %q %v", raw, readErr)
	}

	// Second chunk appends at the current offset.
	callTool(t, ctx, session, "remote_dev__append_write", map[string]interface{}{
		"file_path":       "notes.md",
		"content":         "second",
		"expected_offset": 5,
	})
	if raw, _ := os.ReadFile(target); string(raw) != "firstsecond" {
		t.Fatalf("second chunk did not append: %q", raw)
	}

	// Replaying the same chunk is idempotent, not a duplicate append.
	callTool(t, ctx, session, "remote_dev__append_write", map[string]interface{}{
		"file_path":       "notes.md",
		"content":         "second",
		"expected_offset": 5,
	})
	if raw, _ := os.ReadFile(target); string(raw) != "firstsecond" {
		t.Fatalf("a replayed chunk must not duplicate content: %q", raw)
	}

	// A stale offset is a classified failure that leaves the file untouched.
	stale := callToolExpectingError(t, ctx, session, "remote_dev__append_write", map[string]interface{}{
		"file_path":       "notes.md",
		"content":         "third",
		"expected_offset": 5,
	})
	if !strings.Contains(stale, "WRITE_PRECONDITION_FAILED") {
		t.Fatalf("a stale offset must be classified, got %q", stale)
	}
	if raw, _ := os.ReadFile(target); string(raw) != "firstsecond" {
		t.Fatalf("a refused append must not modify the file: %q", raw)
	}
}

// TestRemoteAppendKeepsDocumentGuard pins that the document-container refusal
// covers append too: appending plain text to a docx destroys it just as surely
// as overwriting it does.
func TestRemoteAppendKeepsDocumentGuard(t *testing.T) {
	root := t.TempDir()
	server, err := remotetools.NewServer(newStandardTestConfig(t, root), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	refused := callToolExpectingError(t, ctx, session, "remote_dev__append_write", map[string]interface{}{
		"file_path": "report.docx",
		"content":   "more plain text\n",
	})
	if !strings.Contains(refused, "文档格式") {
		t.Fatalf("append must refuse a document container, got %q", refused)
	}
	if _, statErr := os.Stat(filepath.Join(root, "report.docx")); statErr == nil {
		t.Fatal("a refused append must not create the file")
	}
}
