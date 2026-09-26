package tools

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func minimalBMPHeader() []byte {
	head := make([]byte, 32)
	copy(head[0:2], "BM")
	binary.LittleEndian.PutUint32(head[2:6], uint32(len(head)))
	// head[6:10] reserved stays zero
	binary.LittleEndian.PutUint32(head[10:14], 54)
	binary.LittleEndian.PutUint32(head[14:18], 40) // BITMAPINFOHEADER
	return head
}

// TestViewTextStartingWithBMStaysText: "BM" alone is not a BMP magic — a text
// file such as "BM25 ranking notes" must still read as text instead of being
// hijacked into the image skip path.
func TestViewTextStartingWithBMStaysText(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	path := filepath.Join(root, "bm25-notes.txt")
	if err := os.WriteFile(path, []byte("BM25 ranking notes\nsecond line\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tool := newViewToolAt(t, root)
	result := executeViewParams(t, tool, context.Background(), map[string]interface{}{"file_path": "bm25-notes.txt"})
	if !strings.Contains(result.Content, "BM25 ranking notes") {
		t.Fatalf("expected the text content, got %q", result.Content)
	}
	if result.Metadata["image_skipped"] == true || result.Metadata["image_detected_format"] == "bmp" {
		t.Fatalf("text must not be classified as BMP, got %#v", result.Metadata)
	}
}

// TestViewRealBMPStillDetected keeps the strengthened sniff honest: a real BMP
// header (reserved zeros + DIB size 40) is still reported as an image file.
func TestViewRealBMPStillDetected(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "image.bmp"), minimalBMPHeader(), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tool := newViewToolAt(t, root)
	result := executeViewParams(t, tool, context.Background(), map[string]interface{}{"file_path": "image.bmp"})
	if result.Metadata["image_detected_format"] != "bmp" || result.Metadata["image_skipped"] != true {
		t.Fatalf("expected a detected BMP note, got %#v", result.Metadata)
	}
	if !strings.Contains(result.Content, "image/bmp") {
		t.Fatalf("expected the real MIME in the note, got %q", result.Content)
	}
}
