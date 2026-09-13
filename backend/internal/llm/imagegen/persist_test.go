package imagegen

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestSaveURLImage_UsesContextAndInfersFormat(t *testing.T) {
	dir := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/webp")
		_, _ = w.Write([]byte("image-bytes"))
	}))
	defer server.Close()

	saved, err := SaveURLImage(context.Background(), dir, "img", server.URL+"/image", "", server.Client())
	if err != nil {
		t.Fatalf("SaveURLImage failed: %v", err)
	}
	if saved.MimeType != "image/webp" {
		t.Fatalf("unexpected mime type: %+v", saved)
	}
	if saved.ByteCount != len("image-bytes") {
		t.Fatalf("unexpected byte count: %+v", saved)
	}
	if !strings.HasSuffix(saved.SavedPath, filepath.Join("", "img.webp")) {
		t.Fatalf("unexpected saved path: %s", saved.SavedPath)
	}
}

func TestSaveURLImage_HonorsCanceledContext(t *testing.T) {
	dir := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("image-bytes"))
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := SaveURLImage(ctx, dir, "img", server.URL+"/image", "", server.Client())
	if err == nil {
		t.Fatal("expected canceled context to fail URL image save")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "context canceled") {
		t.Fatalf("expected context canceled error, got %v", err)
	}
}

// TestSaveBase64Image_ConcurrentSavesOwnDistinctPaths guards the coarse-clock
// hazard for generated image files: concurrent saves that share an id hint must
// never be handed the same path. A Stat-then-WriteFile allocator let both
// writers see the base name as missing inside one clock tick, so the later
// write silently replaced the earlier image and both callers reported the same
// SavedPath.
func TestSaveBase64Image_ConcurrentSavesOwnDistinctPaths(t *testing.T) {
	dir := t.TempDir()
	payload := base64.StdEncoding.EncodeToString([]byte("image-bytes"))

	const workers = 16
	paths := make([]string, workers)
	errs := make([]error, workers)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			saved, err := SaveBase64Image(dir, "img:1", payload, "png")
			if err != nil {
				errs[idx] = err
				return
			}
			paths[idx] = saved.SavedPath
		}(i)
	}
	wg.Wait()

	seen := make(map[string]struct{}, workers)
	for i, path := range paths {
		if errs[i] != nil {
			t.Fatalf("save %d failed: %v", i, errs[i])
		}
		if _, duplicate := seen[path]; duplicate {
			t.Fatalf("path %q was handed to more than one save", path)
		}
		seen[path] = struct{}{}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read saved image %s: %v", path, err)
		}
		if string(data) != "image-bytes" {
			t.Fatalf("saved image %s holds %q", path, data)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read output dir: %v", err)
	}
	if len(entries) != workers {
		t.Fatalf("expected %d saved files, got %d", workers, len(entries))
	}
}

// TestSaveURLImage_FailureReleasesReservedPath makes sure a download that dies
// mid-body removes the path reservation. A leftover zero-byte placeholder would
// permanently shadow the base name for later saves of the same id.
func TestSaveURLImage_FailureReleasesReservedPath(t *testing.T) {
	dir := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", "4096")
		_, _ = w.Write([]byte("partial"))
	}))
	defer server.Close()

	if _, err := SaveURLImage(context.Background(), dir, "img", server.URL+"/image.png", "png", server.Client()); err == nil {
		t.Fatal("expected a truncated download to fail")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read output dir: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("expected no leftover files after a failed download, got %v", names)
	}
}
