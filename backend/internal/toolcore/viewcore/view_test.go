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
	window, err := Read(path, 1, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(window.Lines) != 1 || window.Lines[0] != "two" {
		t.Fatalf("unexpected lines: %#v", window.Lines)
	}
	if window.TotalLines != 3 || window.EOF {
		t.Fatalf("unexpected window: %+v", window)
	}
	rendered := Render("a.txt", window)
	if !strings.Contains(rendered, "2: two") {
		t.Fatalf("expected 1-based display line number: %s", rendered)
	}
	if !strings.Contains(rendered, "offset=2") {
		t.Fatalf("missing continuation: %s", Render("a.txt", window))
	}
}

func TestReadWindowByteBudget(t *testing.T) {
	path := writeFile(t, "a.txt", strings.Repeat("x\n", 100))
	window, err := Read(path, 0, 100, 10)
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
	window, err := Read(path, 0, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(window.Lines) != 1 || !strings.Contains(window.Lines[0], "line truncated") {
		t.Fatalf("line not clamped: %q", window.Lines)
	}
}

func TestLineReaderKeepsFollowingLinesAfterHugeLine(t *testing.T) {
	huge := strings.Repeat("x", ReaderLineCapBytes+4096)
	path := writeFile(t, "long.txt", huge+"\r\nnext\n")

	window, err := Read(path, 0, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(window.Lines) != 2 || window.Lines[1] != "next" {
		t.Fatalf("unexpected window: %+v", window)
	}
	if !strings.Contains(window.Lines[0], "line truncated") {
		t.Fatalf("huge line was not marked truncated: %q", window.Lines[0][:64])
	}
	if window.TotalLines != 2 || !window.EOF {
		t.Fatalf("unexpected totals: %+v", window)
	}
}

func TestLineReaderCRLFAndFinalLine(t *testing.T) {
	reader := NewLineReader(strings.NewReader("one\r\n\r\ntwo"))

	var lines []string
	for {
		line, err := reader.Next()
		if err != nil {
			t.Fatal(err)
		}
		if !line.HasBytes {
			break
		}
		lines = append(lines, string(line.Bytes))
	}
	if got, want := strings.Join(lines, "|"), "one||two"; got != want {
		t.Fatalf("lines = %q, want %q", got, want)
	}
}

func TestLineReaderPreservesGenuinelyInvalidBytes(t *testing.T) {
	content := []byte("abc\xff\n")
	reader := NewLineReader(strings.NewReader(string(content)))

	line, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(line.Bytes), "abc\xff"; got != want {
		t.Fatalf("line bytes = %q, want %q", got, want)
	}
	if line.Truncated {
		t.Fatal("short invalid line must not be reported as truncated")
	}
}

func TestLineReaderTrimsOnlyTheCutRuneAtTheCap(t *testing.T) {
	prefix := strings.Repeat("a", ReaderLineCapBytes-1)
	reader := NewLineReader(strings.NewReader(prefix + "é\nnext\n"))

	line, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if !line.Truncated {
		t.Fatal("line crossing the reader cap must be marked truncated")
	}
	if got, want := string(line.Bytes), prefix; got != want {
		t.Fatalf("line bytes length = %d, want %d", len(got), len(want))
	}

	next, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(next.Bytes), "next"; got != want {
		t.Fatalf("following line = %q, want %q", got, want)
	}
}
