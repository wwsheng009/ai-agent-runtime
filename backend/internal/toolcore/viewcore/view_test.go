package viewcore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadWindow(t *testing.T) {
	path := writeFile(t, "a.txt", "one\ntwo\nthree\n")
	window, err := Read(path, 2, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(window.Lines) != 1 || window.Lines[0] != "two" {
		t.Fatalf("unexpected lines: %#v", window.Lines)
	}
	if window.TotalLines != 3 || window.EOF {
		t.Fatalf("unexpected window: %+v", window)
	}
	if !strings.Contains(Render("a.txt", window), "offset=3") {
		t.Fatalf("missing continuation: %s", Render("a.txt", window))
	}
}

func TestReadWindowByteBudget(t *testing.T) {
	path := writeFile(t, "a.txt", strings.Repeat("x\n", 100))
	window, err := Read(path, 1, 100, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !window.TruncatedByBytes {
		t.Fatal("expected byte-budget truncation")
	}
	if window.LinesRead == 0 || window.LinesRead >= 100 {
		t.Fatalf("lines_read = %d", window.LinesRead)
	}
}

func TestReadClampsLongLine(t *testing.T) {
	path := writeFile(t, "a.txt", strings.Repeat("x", LineCap+10))
	window, err := Read(path, 1, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(window.Lines) != 1 || !strings.Contains(window.Lines[0], "line truncated") {
		t.Fatalf("line not clamped: %q", window.Lines)
	}
}
