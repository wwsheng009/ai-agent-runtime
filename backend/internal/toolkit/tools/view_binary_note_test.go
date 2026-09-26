package tools

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestViewBinaryFileReturnsMIMENote pins the §3.9-2 contract: a binary file
// yields one line of MIME + size + recovery route instead of the old
// "file seems binary, not supported" dead end.
func TestViewBinaryFileReturnsMIMENote(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	payload := append([]byte{0x7F, 'E', 'L', 'F'}, bytes.Repeat([]byte{0x00}, 128)...)
	if err := os.WriteFile(filepath.Join(root, "blob.bin"), payload, 0o644); err != nil {
		t.Fatalf("seed binary: %v", err)
	}
	tool := newViewToolAt(t, root)
	result := executeViewParams(t, tool, context.Background(), map[string]interface{}{"file_path": "blob.bin"})
	if result.Metadata["binary_note"] != true || result.Metadata["is_binary"] != true {
		t.Fatalf("expected binary note metadata, got %#v", result.Metadata)
	}
	if !strings.Contains(result.Content, "MIME:") || !strings.Contains(result.Content, "字节") {
		t.Fatalf("expected MIME and size in the note, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "download") {
		t.Fatalf("expected a recovery route in the note, got %q", result.Content)
	}
}
