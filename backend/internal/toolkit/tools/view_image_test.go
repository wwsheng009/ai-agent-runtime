package tools

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeViewTestPNG(t *testing.T, path string, width, height int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := 0; x < width; x++ {
		for y := 0; y < height; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 40), G: uint8(y * 40), B: 120, A: 255})
		}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create png: %v", err)
	}
	defer file.Close()
	if err := png.Encode(file, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
}

func TestViewImagePassthroughReturnsImageMetadata(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "pixel.png")
	writeViewTestPNG(t, path, 3, 5)

	tool := NewViewTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{"file_path": "pixel.png"})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("view image failed: %v %+v", err, result)
	}
	if result.Metadata["image_passthrough"] != true {
		t.Fatalf("expected image_passthrough metadata, got %#v", result.Metadata)
	}
	if got := result.Metadata["image_mime_type"]; got != "image/png" {
		t.Fatalf("expected image/png, got %#v", got)
	}
	if got := result.Metadata["image_width"]; got != 3 {
		t.Fatalf("expected width 3, got %#v", got)
	}
	if got := result.Metadata["image_height"]; got != 5 {
		t.Fatalf("expected height 5, got %#v", got)
	}
	imagePath, ok := result.Metadata["image_path"].(string)
	if !ok || strings.TrimSpace(imagePath) == "" {
		t.Fatalf("expected image_path metadata, got %#v", result.Metadata["image_path"])
	}
	if _, statErr := os.Stat(imagePath); statErr != nil {
		t.Fatalf("image_path must point at a readable file: %v", statErr)
	}
	if !strings.Contains(result.Content, "图片文件") {
		t.Fatalf("expected image summary content, got %q", result.Content)
	}
}

func TestViewImagePassthroughSkipsFakeImageExtension(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "not-really.png")
	if err := os.WriteFile(path, []byte("plain text pretending to be an image\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	tool := NewViewTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{"file_path": "not-really.png"})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("expected text fallback for fake image, got %v %+v", err, result)
	}
	if result.Metadata["image_passthrough"] == true {
		t.Fatalf("fake image must not pass through, got %#v", result.Metadata)
	}
	if !strings.Contains(result.Content, "plain text pretending") {
		t.Fatalf("expected ordinary text read, got %q", result.Content)
	}
}

// A non-image binary is not a failure any more: §3.9-2 turns it into one line
// of MIME + size + recovery route (and it must never pass through as an image).
func TestViewBinaryNonImageReturnsMIMENote(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "blob.dat")
	if err := os.WriteFile(path, []byte{0x00, 0x01, 0x02, 0x00, 0x00, 0x03, 0x00, 0x04}, 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	tool := NewViewTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{"file_path": "blob.dat"})
	if err != nil {
		t.Fatalf("unexpected tool error: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("expected a binary note instead of a failure, got %+v", result)
	}
	if result.Metadata["binary_note"] != true || result.Metadata["image_passthrough"] == true {
		t.Fatalf("expected a plain binary note without image passthrough, got %#v", result.Metadata)
	}
	if !strings.Contains(result.Content, "MIME:") {
		t.Fatalf("expected MIME + size in the note, got %q", result.Content)
	}
}
