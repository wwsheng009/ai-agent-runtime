package searchcore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// grepBinaryFixture builds the four cases that decide the binary contract:
// a plain file, a file whose match sits before its first NUL, one whose match
// sits after it, and a binary file with no match at all.
func grepBinaryFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"plain.txt":      "needle plain\n",
		"before.dat":     "needle before\x00DATA\n",
		"after.dat":      "DATA\x00needle after\n",
		"nomatch.dat":    "DATA\x00DATA\n",
		"nul_second.dat": "plain first\nneedle\x00tail\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func grepFileNames(result GrepResult) []string {
	seen := make(map[string]bool, len(result.Matches))
	out := make([]string, 0, len(result.Matches))
	for _, match := range result.Matches {
		name := filepath.Base(match.File)
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// TestGrepDefaultSkipsBinaryFiles pins the behaviour the core had lost: a file
// with a NUL byte is skipped outright, exactly like ripgrep's default. Before
// this was restored the core passed --text unconditionally, so a binary blob
// was searched as if it were text on every surface.
func TestGrepDefaultSkipsBinaryFiles(t *testing.T) {
	root := grepBinaryFixture(t)

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle", Path: root, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := grepFileNames(result); len(got) != 1 || got[0] != "plain.txt" {
		t.Fatalf("default search must only see the plain file, got %v", got)
	}
	for _, match := range result.Matches {
		if strings.ContainsRune(match.Text, 0) {
			t.Fatalf("a NUL byte must never reach the caller: %q", match.Text)
		}
	}
}

// TestGrepUnrestrictedThreeSearchesBinaryFiles pins -uuu: the file becomes
// searchable, including text after the NUL, but the NUL itself is rendered as
// its printable symbol so the payload stays readable.
func TestGrepUnrestrictedThreeSearchesBinaryFiles(t *testing.T) {
	root := grepBinaryFixture(t)

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle", Path: root, Limit: 50, Unrestricted: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileList(t, grepFileNames(result), []string{
		"after.dat", "before.dat", "nul_second.dat", "plain.txt",
	})
	joined := ""
	for _, match := range result.Matches {
		joined += match.Text + "\n"
	}
	if strings.ContainsRune(joined, 0) {
		t.Fatalf("even with -uuu a raw NUL must not be emitted: %q", joined)
	}
	if !strings.Contains(joined, "needle"+GrepNulDisplay+"tail") {
		t.Fatalf("the NUL should be shown as %q:\n%s", GrepNulDisplay, joined)
	}
}

// TestGrepUnrestrictedLevelsOneAndTwoKeepBinarySkipped is the guard against
// over-reading the ladder: only the top level changes binary handling, and the
// two lower levels must stay byte-for-byte identical here.
func TestGrepUnrestrictedLevelsOneAndTwoKeepBinarySkipped(t *testing.T) {
	root := grepBinaryFixture(t)
	for _, level := range []int{1, 2} {
		result, err := Grep(context.Background(), GrepOptions{
			Pattern: "needle", Path: root, Limit: 50,
			Unrestricted: level, NoIgnore: true,
		})
		if err != nil {
			t.Fatalf("level %d: %v", level, err)
		}
		if got := grepFileNames(result); len(got) != 1 || got[0] != "plain.txt" {
			t.Fatalf("level %d must still skip binary files, got %v", level, got)
		}
	}
}

// TestGrepFilesWithoutExcludesBinaryFiles: a skipped binary file is neither a
// match nor a proven no-match. Listing it would assert something the search
// never established.
func TestGrepFilesWithoutExcludesBinaryFiles(t *testing.T) {
	root := grepBinaryFixture(t)

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle", Path: root, Mode: GrepModeFilesWithout, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range result.Files {
		name := filepath.Base(entry.File)
		if strings.HasSuffix(name, ".dat") {
			t.Fatalf("a skipped binary file must not be reported as without-match: %v", result.Files)
		}
	}
}

func TestGrepBinaryProbeHead(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "bin.dat")
	if err := os.WriteFile(binary, []byte("text\x00tail"), 0o644); err != nil {
		t.Fatal(err)
	}
	text := filepath.Join(root, "text.txt")
	if err := os.WriteFile(text, []byte("no nul here"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !grepFileIsBinary(binary) {
		t.Fatal("a NUL in the head must be classified as binary")
	}
	if grepFileIsBinary(text) {
		t.Fatal("plain text must not be classified as binary")
	}
	if grepFileIsBinary(filepath.Join(root, "missing")) {
		t.Fatal("an unreadable probe must not be reported as binary")
	}
}
