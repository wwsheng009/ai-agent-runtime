package searchcore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestGrepFilesWithoutMode pins the mode's page contract on the walker: the
// entry is a file with no matching line, pagination counts files, and a file
// with a single match never appears even though it also has non-matching lines.
func TestGrepFilesWithoutMode(t *testing.T) {
	root := grepModeFixture(t)

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "hit", Path: root, Mode: GrepModeFilesWithout, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileList(t, summarizeNames(result), []string{"c.txt:0"})
	if result.Truncated {
		t.Fatal("a page with room for every file must not report has_more")
	}
	if len(result.Matches) != 0 {
		t.Fatalf("files_without must not collect match lines, got %+v", result.Matches)
	}
	if result.FilesScanned != 3 {
		t.Fatalf("every candidate is scanned, including the ones that matched: %d", result.FilesScanned)
	}
}

// TestGrepFilesWithoutPagination is the regression guard for counting the wrong
// unit: paging by matches would make a file with three matches consume three
// slots of the page.
func TestGrepFilesWithoutPagination(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"a.txt": "nope\n",
		"b.txt": "nope\n",
		"c.txt": "hit hit hit\n",
		"d.txt": "nope\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	first, err := Grep(context.Background(), GrepOptions{
		Pattern: "hit", Path: root, Mode: GrepModeFilesWithout, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileList(t, summarizeNames(first), []string{"a.txt:0", "b.txt:0"})
	if !first.Truncated {
		t.Fatal("d.txt is still waiting, so has_more must be true")
	}

	second, err := Grep(context.Background(), GrepOptions{
		Pattern: "hit", Path: root, Mode: GrepModeFilesWithout, Limit: 2, Offset: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileList(t, summarizeNames(second), []string{"d.txt:0"})
	if second.Truncated {
		t.Fatal("the last page must not report has_more")
	}
}

// TestGrepFilesWithoutEmptyFileCountsAsNoMatch: an empty file cannot contain a
// matching line, so it belongs on the page (rg agrees).
func TestGrepFilesWithoutEmptyFileCountsAsNoMatch(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hit.txt"), []byte("hit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "empty.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "hit", Path: root, Mode: GrepModeFilesWithout, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileList(t, summarizeNames(result), []string{"empty.txt:0"})
}

// TestGrepFilesWithoutMaxCountZero pins the local reading of an explicit zero:
// the file can produce no match at all, so every candidate is a no-match file.
// rg -m 0 prints nothing, and that divergence is exactly why the ripgrep engine
// declines this case.
func TestGrepFilesWithoutMaxCountZero(t *testing.T) {
	root := grepModeFixture(t)

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "hit", Path: root, Mode: GrepModeFilesWithout,
		Limit: 50, MaxCount: 0, MaxCountSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileList(t, summarizeNames(result), []string{"a.txt:0", "b.txt:0", "c.txt:0"})
}

// TestGrepFilesWithoutIgnoresOnlyMatching documents the deliberate no-op: the
// page unit is a file, so fragment expansion cannot change what it contains.
func TestGrepFilesWithoutIgnoresOnlyMatching(t *testing.T) {
	root := grepModeFixture(t)

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "hit", Path: root, Mode: GrepModeFilesWithout,
		Limit: 50, OnlyMatching: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileList(t, summarizeNames(result), []string{"c.txt:0"})
}

// TestGrepFilesWithoutSingleFileRoot: a file that does match yields an empty
// page, not an error.
func TestGrepFilesWithoutSingleFileRoot(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("hit\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "hit", Path: path, Mode: GrepModeFilesWithout, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 0 {
		t.Fatalf("a matching single-file root has no no-match page, got %+v", result.Files)
	}
	if !result.SingleFile {
		t.Fatal("single-file root must keep the single_file flag")
	}
}
