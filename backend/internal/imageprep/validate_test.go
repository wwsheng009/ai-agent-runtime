package imageprep

import (
	"bytes"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func encodedImage(t *testing.T, format string) []byte {
	t.Helper()
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	var err error
	switch format {
	case "png":
		err = png.Encode(&buf, img)
	case "jpeg":
		err = jpeg.Encode(&buf, img, nil)
	case "gif":
		err = gif.Encode(&buf, img, nil)
	default:
		t.Fatalf("unsupported format %q", format)
	}
	if err != nil {
		t.Fatalf("encode %s: %v", format, err)
	}
	return buf.Bytes()
}

// TestValidateImageDataAcceptsCompleteContainers: encoded images of every
// supported type must pass the structural validation.
func TestValidateImageDataAcceptsCompleteContainers(t *testing.T) {
	for _, format := range []string{"png", "jpeg", "gif"} {
		data := encodedImage(t, format)
		if err := ValidateImageData(data, "image/"+format); err != nil {
			t.Fatalf("%s must validate: %v", format, err)
		}
	}
}

// TestValidateImageDataRejectsIncompleteContainers pins finding H10: a header
// is not an image. Both the bare PNG signature and an IHDR+IEND PNG without
// IDAT used to be advertised as attachable.
func TestValidateImageDataRejectsIncompleteContainers(t *testing.T) {
	valid := encodedImage(t, "png")
	cases := map[string][]byte{
		"signature_only": {0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'},
		"header_only":    headerOnlyPNGForTest(t),
		"truncated":      valid[:len(valid)-8],
		"random":         []byte("not an image at all, just text pretending"),
	}
	for name, data := range cases {
		if err := ValidateImageData(data, "image/png"); err == nil {
			t.Fatalf("%s must not validate as PNG", name)
		}
	}
	if err := ValidateImageData(valid, "image/webp"); err == nil {
		t.Fatal("unsupported MIME must be rejected")
	}
}

// TestValidateImageFileReadsTheWholeContainer: the file variant must reject an
// incomplete container on disk and accept a complete one.
func TestValidateImageFileReadsTheWholeContainer(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.png")
	if err := os.WriteFile(good, encodedImage(t, "png"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateImageFile(good, "image/png"); err != nil {
		t.Fatalf("complete PNG file must validate: %v", err)
	}
	bad := filepath.Join(dir, "bad.png")
	if err := os.WriteFile(bad, headerOnlyPNGForTest(t), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateImageFile(bad, "image/png"); err == nil {
		t.Fatal("header-only PNG file must not validate")
	}
	if err := ValidateImageFile(filepath.Join(dir, "missing.png"), "image/png"); err == nil {
		t.Fatal("missing file must be reported")
	}
}

// TestPrepareRejectsHeaderOnlyImage: Prepare's unchanged path must not return a
// broken image for attachment.
func TestPrepareRejectsHeaderOnlyImage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.png")
	if err := os.WriteFile(path, headerOnlyPNGForTest(t), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(path, dir, Options{}); err == nil {
		t.Fatal("a header-only PNG must not be prepared for attachment")
	} else if !strings.Contains(err.Error(), "图片校验失败") {
		t.Fatalf("expected the validation error, got %v", err)
	}
}

func headerOnlyPNGForTest(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	ihdr := make([]byte, 13)
	ihdr[3], ihdr[7] = 1, 1
	ihdr[8], ihdr[9] = 8, 6
	writePNGChunk(&buf, "IHDR", ihdr)
	writePNGChunk(&buf, "IEND", nil)
	return buf.Bytes()
}
