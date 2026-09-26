package tools

import (
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

func writeTestPNG(t *testing.T, path string, width, height int) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer file.Close()
	if err := png.Encode(file, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
		t.Fatalf("encode %s: %v", path, err)
	}
}

// TestViewImageSniffIgnoresExtension: magic bytes decide, so a real PNG with a
// non-image extension still goes through the image path (analysis §3.11).
func TestViewImageSniffIgnoresExtension(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	writeTestPNG(t, filepath.Join(root, "shot.bin"), 8, 8)
	tool := newViewToolAt(t, root)
	result := executeViewParams(t, tool, context.Background(), map[string]interface{}{"file_path": "shot.bin"})
	if result.Metadata[toolresult.MetadataImagePassthroughKey] != true {
		t.Fatalf("expected image passthrough despite the extension, got %#v", result.Metadata)
	}
	if result.Metadata["image_detected_format"] != "png" {
		t.Fatalf("expected the sniffed format in metadata, got %#v", result.Metadata)
	}
}

// TestViewImageUnsupportedFormatReturnsMIMENote: a detectable but unsupported
// format must yield a one-line MIME note plus a recovery route instead of the
// generic binary-file refusal.
func TestViewImageUnsupportedFormatReturnsMIMENote(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	payload := append([]byte("RIFF\x24\x00\x00\x00WEBPVP8 "), make([]byte, 16)...)
	if err := os.WriteFile(filepath.Join(root, "pic.bin"), payload, 0o644); err != nil {
		t.Fatalf("write webp stub: %v", err)
	}
	tool := newViewToolAt(t, root)
	result := executeViewParams(t, tool, context.Background(), map[string]interface{}{"file_path": "pic.bin"})
	if result.Metadata["image_passthrough"] == true {
		t.Fatalf("webp must not be passed through, got %#v", result.Metadata)
	}
	if result.Metadata["image_detected_format"] != "webp" || result.Metadata["image_skip_reason"] != "unsupported_image_format" {
		t.Fatalf("expected an unsupported-format note, got %#v", result.Metadata)
	}
	if !strings.Contains(result.Content, "image/webp") || !strings.Contains(result.Content, "转换为 PNG") {
		t.Fatalf("expected real MIME and a recovery route, got %q", result.Content)
	}
}

// TestViewFakeImageFallsBackToTextPath: a .png that is not an image must not
// reach the image path at all; the regular text/binary handling applies.
func TestViewFakeImageFallsBackToTextPath(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "fake.png"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write fake: %v", err)
	}
	tool := newViewToolAt(t, root)
	result := executeViewParams(t, tool, context.Background(), map[string]interface{}{"file_path": "fake.png"})
	if result.Metadata["image_passthrough"] == true {
		t.Fatalf("a fake .png must not be passed through, got %#v", result.Metadata)
	}
	if !strings.Contains(result.Content, "1: hello") {
		t.Fatalf("expected the text path to read the file, got %q", result.Content)
	}
}
