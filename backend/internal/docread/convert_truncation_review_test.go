package docread

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBoundedBufferReportsTruncation: dropping bytes after the limit must be
// observable, otherwise a prefix is presented as the complete converter output.
func TestBoundedBufferReportsTruncation(t *testing.T) {
	buffer := &boundedBuffer{limit: 8}
	if _, err := buffer.Write([]byte("12345")); err != nil {
		t.Fatal(err)
	}
	if buffer.Truncated() {
		t.Fatal("under-limit writes must not report truncation")
	}
	if _, err := buffer.Write([]byte("67890")); err != nil {
		t.Fatal(err)
	}
	if !buffer.Truncated() {
		t.Fatal("dropped bytes must be reported as truncation")
	}
	if string(buffer.Bytes()) != "12345678" {
		t.Fatalf("retained prefix changed: %q", buffer.Bytes())
	}
}

// TestReadConvertedOutputRejectsOversizeProduct: the 32 MiB ceiling covered only
// the process streams; soffice's file product was read unbounded.
func TestReadConvertedOutputRejectsOversizeProduct(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sheet.csv")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(int64(maxConverterOutputBytes) + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readConvertedOutput(dir, "sheet", ".csv"); err == nil {
		t.Fatal("oversize converter product must fail instead of being read whole")
	} else if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected an explicit limit error, got %v", err)
	}
}

// TestRenderDisclosesTruncatedConverterOutput: a stream that hit the capture
// limit must not report doc_degraded=false with page/EOF numbers derived from
// the retained prefix.
func TestRenderDisclosesTruncatedConverterOutput(t *testing.T) {
	previousLookPath := lookPath
	lookPath = func(string) (string, error) { return "/usr/bin/converter", nil }
	t.Cleanup(func() { lookPath = previousLookPath })
	docreadTestRunCommandWithTruncation(t, func(name string, args []string) ([]byte, []byte, bool, error) {
		return []byte("page one\fpage two\f"), nil, true, nil
	})

	dir := t.TempDir()
	path := filepath.Join(dir, "sample.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.4\n1 0 obj\n<<>>\nendobj\n%%EOF\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	render, err := Render(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if render.Metadata["doc_degraded"] != true {
		t.Fatalf("truncated converter output must be degraded, got %#v", render.Metadata)
	}
	if render.Metadata["doc_reason"] != "output_truncated" {
		t.Fatalf("expected doc_reason=output_truncated, got %#v", render.Metadata["doc_reason"])
	}
	if render.Metadata["doc_output_truncated"] != true {
		t.Fatalf("expected doc_output_truncated=true, got %#v", render.Metadata)
	}
}
