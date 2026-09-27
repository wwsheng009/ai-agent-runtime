package imageprep

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOversizePixelNoteBoundary: only the total pixel count decides, and a
// missing/zero header dimension is not treated as oversize.
func TestOversizePixelNoteBoundary(t *testing.T) {
	if note := oversizePixelNote("ok.png", 2000, 1500, 1<<20, "png"); note != "" {
		t.Fatalf("ordinary image must pass the pixel budget, got %q", note)
	}
	if note := oversizePixelNote("huge.png", 32768, 32768, 32<<10, "png"); note == "" || !strings.Contains(note, "像素上限") {
		t.Fatalf("32768x32768 must be refused before decode, got %q", note)
	}
	if note := oversizePixelNote("unknown.png", 0, 0, 0, ""); note != "" {
		t.Fatalf("missing header dimensions must not be refused, got %q", note)
	}
}

// TestPrepareRefusesHugePixelDimensionsBeforeDecode: a tiny file can declare
// enormous dimensions; before the fix Prepare decoded it fully (4 GiB for
// 32768x32768 RGBA) and only then scaled.
func TestPrepareRefusesHugePixelDimensionsBeforeDecode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.png")
	writeHeaderOnlyPNG(t, path, 32768, 32768)
	if info, err := os.Stat(path); err != nil || info.Size() > 4096 {
		t.Fatalf("fixture must stay tiny: size=%v err=%v", info, err)
	}
	result, err := Prepare(path, dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Skipped {
		t.Fatalf("huge dimensions must be skipped, got %#v", result)
	}
	if !strings.Contains(result.Note, "像素上限") {
		t.Fatalf("expected the pixel-budget note, got %q", result.Note)
	}
	if result.Width != 32768 || result.Height != 32768 {
		t.Fatalf("skip result must still report the header dimensions, got %dx%d", result.Width, result.Height)
	}
}

// writeHeaderOnlyPNG writes a valid PNG signature + IHDR/IEND, which is all
// png.DecodeConfig needs to report dimensions without decoding pixel data.
func writeHeaderOnlyPNG(t *testing.T, path string, width, height uint32) {
	t.Helper()
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], width)
	binary.BigEndian.PutUint32(ihdr[4:8], height)
	ihdr[8] = 8 // bit depth
	ihdr[9] = 6 // RGBA
	writePNGChunk(&buf, "IHDR", ihdr)
	writePNGChunk(&buf, "IEND", nil)
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writePNGChunk(buf *bytes.Buffer, kind string, data []byte) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(data)))
	buf.Write(length[:])
	buf.WriteString(kind)
	buf.Write(data)
	crc := crc32.NewIEEE()
	_, _ = crc.Write([]byte(kind))
	_, _ = crc.Write(data)
	var sum [4]byte
	binary.BigEndian.PutUint32(sum[:], crc.Sum32())
	buf.Write(sum[:])
}
