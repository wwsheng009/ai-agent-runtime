package searchcore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeGrepFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestGrepLineReaderCRLFFinalLineAndEmptyLines(t *testing.T) {
	path := writeGrepFile(t, "lines.txt", []byte("first\r\n\nthird"))

	cases := []struct {
		pattern string
		line    int
		text    string
	}{
		{pattern: "^first$", line: 1, text: "first"},
		{pattern: "^third$", line: 3, text: "third"},
	}
	for _, tc := range cases {
		result, err := Grep(context.Background(), GrepOptions{
			Pattern: tc.pattern,
			Path:    path,
			Limit:   10,
		})
		if err != nil {
			t.Fatalf("%s: %v", tc.pattern, err)
		}
		if len(result.Matches) != 1 {
			t.Fatalf("%s: got %d matches, want 1", tc.pattern, len(result.Matches))
		}
		if got := result.Matches[0]; got.Line != tc.line || got.Text != tc.text {
			t.Fatalf("%s: got line=%d text=%q, want line=%d text=%q",
				tc.pattern, got.Line, got.Text, tc.line, tc.text)
		}
	}
}

func TestGrepLongLineTruncatesButKeepsFollowingLineNumbers(t *testing.T) {
	long := strings.Repeat("a", MaxGrepLineBytes+17)
	path := writeGrepFile(t, "long.txt", []byte(long+"\r\nneedle\n"))

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle",
		Path:    path,
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 1 || result.Matches[0].Line != 2 {
		t.Fatalf("got matches %+v, want needle on line 2", result.Matches)
	}

	result, err = Grep(context.Background(), GrepOptions{
		Pattern: "^a+…$",
		Path:    path,
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 1 {
		t.Fatalf("got %d truncated matches, want 1", len(result.Matches))
	}
	text := result.Matches[0].Text
	if len(text) != MaxGrepLineBytes+len("…") || !strings.HasSuffix(text, "…") {
		t.Fatalf("unexpected truncated text: len=%d suffix=%q", len(text), text[len(text)-3:])
	}
}

func TestGrepExactMaxLineIsNotTruncated(t *testing.T) {
	line := strings.Repeat("b", MaxGrepLineBytes)
	path := writeGrepFile(t, "exact.txt", []byte(line+"\r\n"))

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "^b+$",
		Path:    path,
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 1 || len(result.Matches[0].Text) != MaxGrepLineBytes {
		t.Fatalf("unexpected exact-line result: %+v", result.Matches)
	}
	if strings.HasSuffix(result.Matches[0].Text, "…") {
		t.Fatal("exact max line must not be marked truncated")
	}
}

func TestGrepVeryLongLineDoesNotStopFollowingMatch(t *testing.T) {
	long := strings.Repeat("x", lineReaderBufferBytes*20)
	path := writeGrepFile(t, "very-long.txt", []byte(long+"\nneedle\n"))

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle",
		Path:    path,
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 1 || result.Matches[0].Line != 2 {
		t.Fatalf("got matches %+v, want needle on line 2", result.Matches)
	}
}

func TestGrepTruncatedLineDoesNotSplitUTF8(t *testing.T) {
	line := strings.Repeat("a", MaxGrepLineBytes-1) + "é" + strings.Repeat("z", 32)
	path := writeGrepFile(t, "utf8-cap.txt", []byte(line+"\nneedle\n"))

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle",
		Path:    path,
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 1 || result.Matches[0].Line != 2 {
		t.Fatalf("got matches %+v, want needle on line 2", result.Matches)
	}
}

func TestGrepInvalidUTF8SkipsWholeFile(t *testing.T) {
	path := writeGrepFile(t, "binary.txt", append([]byte{0xff, '\n'}, []byte("needle\n")...))

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle",
		Path:    path,
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 0 {
		t.Fatalf("invalid UTF-8 file should be skipped, got %+v", result.Matches)
	}
}
