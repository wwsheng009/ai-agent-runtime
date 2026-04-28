package imagegen

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveBase64Image_WritesFileAndReturnsMetadata(t *testing.T) {
	dir := t.TempDir()
	payload := base64.StdEncoding.EncodeToString([]byte("image-bytes"))

	saved, err := SaveBase64Image(dir, "img:1", payload, "png")
	if err != nil {
		t.Fatalf("SaveBase64Image failed: %v", err)
	}
	if saved.ID != "img:1" {
		t.Fatalf("unexpected image id: %+v", saved)
	}
	if saved.MimeType != "image/png" {
		t.Fatalf("unexpected mime type: %+v", saved)
	}
	if saved.ByteCount != len("image-bytes") {
		t.Fatalf("unexpected byte count: %+v", saved)
	}
	if _, err := os.Stat(saved.SavedPath); err != nil {
		t.Fatalf("expected saved file to exist: %v", err)
	}
	if !strings.HasSuffix(saved.SavedPath, filepath.Join("", "img_1.png")) {
		t.Fatalf("unexpected saved path: %s", saved.SavedPath)
	}
}

func TestSaveBase64Image_UsesUniqueNameOnCollision(t *testing.T) {
	dir := t.TempDir()
	payload := base64.StdEncoding.EncodeToString([]byte("image-bytes"))

	first, err := SaveBase64Image(dir, "img:1", payload, "png")
	if err != nil {
		t.Fatalf("first save failed: %v", err)
	}
	second, err := SaveBase64Image(dir, "img:1", payload, "png")
	if err != nil {
		t.Fatalf("second save failed: %v", err)
	}
	if first.SavedPath == second.SavedPath {
		t.Fatal("expected unique file name on collision")
	}
}

func TestSaveBase64Image_RejectsInvalidPayloads(t *testing.T) {
	dir := t.TempDir()
	if _, err := SaveBase64Image(dir, "img", "not-base64", "png"); err == nil {
		t.Fatal("expected invalid base64 to fail")
	}
	if _, err := SaveBase64Image(dir, "img", "data:image/png;base64,aW1hZ2U=", "png"); err == nil {
		t.Fatal("expected data URL payload to fail")
	}
}
