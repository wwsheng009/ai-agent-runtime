package imageprep

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPrepareNoteDisclosesCoordinateMultiplier: a scaled screenshot must say
// how to map displayed coordinates back to the original image
// (analysis §3.11: size-before-and-after alone still lets the model do the
// click math confidently wrong).
func TestPrepareNoteDisclosesCoordinateMultiplier(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "shot.png")
	file, err := os.Create(src)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := png.Encode(file, image.NewRGBA(image.Rect(0, 0, 3000, 2000))); err != nil {
		file.Close()
		t.Fatalf("encode: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	result, err := Prepare(src, t.TempDir(), Options{MaxDimension: 200})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if !result.Rewritten {
		t.Fatalf("expected the image to be downscaled, got %#v", result)
	}
	// 3000x2000 → 200x133：纵向取整后比例与横向不同，两个轴都必须披露，
	// 否则模型按单一乘数算纵向坐标会偏（review Q5）。
	if !strings.Contains(result.Note, "显示坐标横向 ×15.00、纵向 ×15.04 得到原图坐标") {
		t.Fatalf("expected both axis multipliers in the note, got %q", result.Note)
	}
}

// TestPrepareNoteUsesSingleMultiplierForExactRatios: an exactly proportional
// downscale keeps the short form.
func TestPrepareNoteUsesSingleMultiplierForExactRatios(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "exact.png")
	file, err := os.Create(src)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := png.Encode(file, image.NewRGBA(image.Rect(0, 0, 2000, 1000))); err != nil {
		file.Close()
		t.Fatalf("encode: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	result, err := Prepare(src, t.TempDir(), Options{MaxDimension: 200})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if !strings.Contains(result.Note, "显示坐标 ×10.00 得到原图坐标") {
		t.Fatalf("expected the single-axis multiplier form, got %q", result.Note)
	}
}
