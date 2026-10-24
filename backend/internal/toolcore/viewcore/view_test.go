package viewcore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/textcodec"
)

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readText(t *testing.T, path string, req Request) (string, Stats) {
	t.Helper()
	content, stats, err := ReadText(path, req, Options{ByteBudget: DefaultByteBudget})
	if err != nil {
		t.Fatal(err)
	}
	return content, stats
}

// TestReadTextWindow pins the window contract every surface publishes: 1-based
// display numbers, no header, and continuation metadata instead of prose.
func TestReadTextWindow(t *testing.T) {
	path := writeFile(t, "a.txt", "one\ntwo\nthree\n")
	req := Request{Offset: 1, Limit: 1}
	content, stats := readText(t, path, req)

	if content != "2: two" {
		t.Fatalf("content = %q, want %q", content, "2: two")
	}
	if stats.LinesRead != 1 || !stats.HasMore || stats.EOF {
		t.Fatalf("unexpected stats: %+v", stats)
	}

	result := BuildResult(path, 12, req, content, stats)
	if result.Metadata["offset"] != 1 || result.Metadata["lines_read"] != 1 {
		t.Fatalf("unexpected metadata: %#v", result.Metadata)
	}
	if result.Metadata["is_truncated"] != true {
		t.Fatalf("expected is_truncated, got %#v", result.Metadata)
	}
	if result.Metadata["suggested_next_offset"] != 2 {
		t.Fatalf("expected suggested_next_offset=2, got %#v", result.Metadata)
	}
}

// TestReadTextEfficiencyAdvisory: a truncated leading window carries the
// non-blocking "prefer offset/limit" guidance so a model does not re-pull a
// whole file.
func TestReadTextEfficiencyAdvisory(t *testing.T) {
	path := writeFile(t, "a.txt", strings.Repeat("x\n", DefaultLimit+10))
	req := Request{Offset: 0, Limit: DefaultLimit}
	content, stats := readText(t, path, req)

	result := BuildResult(path, 0, req, content, stats)
	if result.Metadata["efficiency_advisory"] != "prefer_offset_limit" {
		t.Fatalf("missing advisory: %#v", result.Metadata)
	}
	if !strings.Contains(result.Content, "[efficiency]") {
		t.Fatalf("advisory missing from content: %q", result.Content)
	}
}

// TestReadTextTailWindow covers the negative-offset form: the last N lines,
// reported through a resolved absolute window.
func TestReadTextTailWindow(t *testing.T) {
	path := writeFile(t, "a.txt", "one\ntwo\nthree\nfour\n")
	req := Request{Offset: -2, Limit: DefaultLimit}
	content, stats := readText(t, path, req)

	if content != "3: three\n4: four" {
		t.Fatalf("content = %q", content)
	}
	if !stats.Tail || stats.WindowStart != 2 || stats.LinesRead != 2 || stats.TotalLines != 4 {
		t.Fatalf("unexpected tail stats: %+v", stats)
	}
	result := BuildResult(path, 0, req, content, stats)
	if result.Metadata["tail"] != true || result.Metadata["tail_lines"] != 2 ||
		result.Metadata["offset"] != 2 || result.Metadata["lines_read"] != 2 {
		t.Fatalf("unexpected tail metadata: %#v", result.Metadata)
	}
	if result.Metadata["total_lines"] != 4 {
		t.Fatalf("expected total_lines=4, got %#v", result.Metadata)
	}
}

// TestReadTextTailHonoursTheExplicitLimit: a tail window is bounded by the
// requested limit too, and drops the oldest kept lines first.
func TestReadTextTailHonoursTheExplicitLimit(t *testing.T) {
	path := writeFile(t, "a.txt", "one\ntwo\nthree\nfour\n")
	content, stats := readText(t, path, Request{Offset: -3, Limit: 2})

	if content != "3: three\n4: four" {
		t.Fatalf("content = %q", content)
	}
	if stats.TailDroppedByLimit != 1 || stats.WindowStart != 2 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

// TestReadTextNotes covers the two empty-window shapes: an empty file and an
// offset past the end. Both are successful reads with an actionable note, not
// errors.
func TestReadTextNotes(t *testing.T) {
	empty := writeFile(t, "empty.txt", "")
	content, stats := readText(t, empty, Request{Offset: 0, Limit: 10})
	if content != "Note: file is empty (0 lines)." || !stats.EmptyFile {
		t.Fatalf("empty file: content=%q stats=%+v", content, stats)
	}

	path := writeFile(t, "a.txt", "one\ntwo\nthree\n")
	content, stats = readText(t, path, Request{Offset: 5, Limit: 10})
	if !strings.Contains(content, "beyond the end of the file (3 lines)") {
		t.Fatalf("beyond-EOF note = %q", content)
	}
	if stats.TotalLines != 3 || !stats.TotalLinesKnown {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

// TestReadTextByteBudget: the byte budget stops the window before the declared
// model-visible size, and says so.
func TestReadTextByteBudget(t *testing.T) {
	path := writeFile(t, "a.txt", strings.Repeat("x\n", 100))
	content, stats, err := ReadText(path, Request{Offset: 0, Limit: 100}, Options{ByteBudget: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !stats.ByteBudgetApplied || !stats.HasMore {
		t.Fatalf("expected byte-budget truncation: %+v", stats)
	}
	if stats.LinesRead == 0 || stats.LinesRead >= 100 {
		t.Fatalf("lines_read = %d", stats.LinesRead)
	}

	var reasons []TruncReason
	_, _, err = ReadText(path, Request{Offset: 0, Limit: 100}, Options{
		ByteBudget: 10,
		Truncated:  func(reason TruncReason) { reasons = append(reasons, reason) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(reasons) != 1 || reasons[0] != TruncatedByBytes {
		t.Fatalf("truncation reasons = %#v", reasons)
	}
	_ = content
}

// TestReadTextTruncatesLongLine keeps the honest-truncation contract: an
// over-limit line is cut with a marker that reports how much was hidden.
func TestReadTextTruncatesLongLine(t *testing.T) {
	path := writeFile(t, "a.txt", strings.Repeat("x", MaxLineChars+10)+"\nsecond\n")
	content, stats := readText(t, path, Request{Offset: 0, Limit: 2})

	if !strings.Contains(content, "line truncated") {
		t.Fatalf("line not clamped: %q", content)
	}
	if !strings.Contains(content, "2: second") {
		t.Fatalf("following line lost: %q", content)
	}
	if stats.LongLinesTruncated != 1 {
		t.Fatalf("unexpected accounting: %+v", stats)
	}
	// The marker itself counts against the 2000-rune window, so the hidden byte
	// count is larger than the "10 more chars" the marker claims: 2010 - (2000 -
	// len(marker)) = 42.
	if stats.HiddenBytes != 42 {
		t.Fatalf("hidden bytes = %d, want 42", stats.HiddenBytes)
	}
}

// TestReadTextDecodesUTF16 proves the read path and the write path share one
// codec: a BOM'd UTF-16 file is decoded and its encoding is published.
func TestReadTextDecodesUTF16(t *testing.T) {
	path := filepath.Join(t.TempDir(), "utf16.txt")
	raw := textcodec.Encode("one\ntwo\n", textcodec.UTF16LE)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	content, stats := readText(t, path, Request{Offset: 0, Limit: 10})
	if content != "1: one\n2: two" {
		t.Fatalf("content = %q", content)
	}
	if stats.Encoding != "utf-16le" {
		t.Fatalf("encoding = %q", stats.Encoding)
	}
	result := BuildResult(path, int64(len(raw)), Request{Offset: 0, Limit: 10}, content, stats)
	if result.Metadata["encoding"] != "utf-16le" {
		t.Fatalf("encoding metadata = %#v", result.Metadata)
	}
}

// TestIsBinaryContent pins the binary heuristic the read path uses to degrade a
// non-text file into a MIME note instead of a wall of NULs.
func TestIsBinaryContent(t *testing.T) {
	if IsBinaryContent("") || IsBinaryContent("plain text\n") {
		t.Fatal("text must not be classified as binary")
	}
	if !IsBinaryContent("ab\x00\x00\x00\x00cd") {
		t.Fatal("NUL-heavy content must be classified as binary")
	}
}

func TestDirectoryPreview(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	preview, err := DirectoryPreview(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(preview, "1: b.txt\n2: sub"+string(filepath.Separator)) {
		t.Fatalf("preview = %q", preview)
	}

	empty, err := DirectoryPreview(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if empty != "(空目录)" {
		t.Fatalf("empty preview = %q", empty)
	}
}

func TestLineReaderKeepsFollowingLinesAfterHugeLine(t *testing.T) {
	huge := strings.Repeat("x", ReaderLineCapBytes+4096)
	path := writeFile(t, "long.txt", huge+"\r\nnext\n")

	content, stats := readText(t, path, Request{Offset: 0, Limit: 2})
	if !strings.Contains(content, "next") {
		t.Fatalf("following line lost: %q", content)
	}
	if !strings.Contains(content, "line truncated") {
		t.Fatalf("huge line was not marked truncated: %q", content[:64])
	}
	if stats.TotalLines != 2 || !stats.EOF {
		t.Fatalf("unexpected totals: %+v", stats)
	}
	if stats.ReaderClampedLines != 1 || stats.ReaderClampedBytes <= 0 {
		t.Fatalf("drained remainder not accounted: %+v", stats)
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
