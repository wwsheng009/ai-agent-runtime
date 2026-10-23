package searchcore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func grepModeFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range map[string]string{
		"a.txt": "hit one\nhit two\nhit three\n",
		"b.txt": "hit again\nmiss\n",
		"c.txt": "nothing here\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func summarizeNames(result GrepResult) []string {
	out := make([]string, 0, len(result.Files))
	for _, entry := range result.Files {
		out = append(out, fmt.Sprintf("%s:%d", filepath.Base(entry.File), entry.Count))
	}
	return out
}

// TestGrepFilesAndCountModes pins the mode contract: files/count paginate by
// file rather than by match, so a file with many matches still counts as one
// entry and its count stays complete.
func TestGrepFilesAndCountModes(t *testing.T) {
	root := grepModeFixture(t)

	files, err := Grep(context.Background(), GrepOptions{
		Pattern: "hit", Path: root, Mode: GrepModeFiles, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileList(t, summarizeNames(files), []string{"a.txt:3", "b.txt:1"})
	if files.Truncated {
		t.Fatal("files mode with room for every file must not report truncation")
	}

	counts, err := Grep(context.Background(), GrepOptions{
		Pattern: "hit", Path: root, Mode: GrepModeCount, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileList(t, summarizeNames(counts), []string{"a.txt:3", "b.txt:1"})
}

// TestGrepFilesModeDoesNotPageByMatch is the regression guard for the obvious
// wrong implementation: limiting by matches would return one file for limit=2
// because a.txt alone contributes three matches.
func TestGrepFilesModeDoesNotPageByMatch(t *testing.T) {
	root := grepModeFixture(t)

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "hit", Path: root, Mode: GrepModeFiles, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileList(t, summarizeNames(result), []string{"a.txt:3", "b.txt:1"})
	if result.Truncated {
		t.Fatalf("two files fill the page exactly; nothing may be reported as truncated: %+v", result.Files)
	}
}

func TestGrepFilesModePagination(t *testing.T) {
	root := grepModeFixture(t)

	first, err := Grep(context.Background(), GrepOptions{
		Pattern: "hit", Path: root, Mode: GrepModeFiles, Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileList(t, summarizeNames(first), []string{"a.txt:3"})
	if !first.Truncated {
		t.Fatal("one file with limit 1 and b.txt waiting must report has_more")
	}

	second, err := Grep(context.Background(), GrepOptions{
		Pattern: "hit", Path: root, Mode: GrepModeFiles, Limit: 1, Offset: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileList(t, summarizeNames(second), []string{"b.txt:1"})
	if second.Truncated {
		t.Fatal("the last page must not report has_more")
	}

	empty, err := Grep(context.Background(), GrepOptions{
		Pattern: "hit", Path: root, Mode: GrepModeCount, Limit: 1, Offset: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Files) != 0 {
		t.Fatalf("offset past the last file must return an empty page, got %+v", empty.Files)
	}
}

func TestGrepCountModeCompleteCountsAcrossPage(t *testing.T) {
	root := grepModeFixture(t)

	// The count for a.txt must be complete even though the raw match list
	// stops as soon as the next file (b.txt) appears.
	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "hit", Path: root, Mode: GrepModeCount, Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 || filepath.Base(result.Files[0].File) != "a.txt" || result.Files[0].Count != 3 {
		t.Fatalf("a.txt count must be complete, got %+v", result.Files)
	}
	if !result.Truncated {
		t.Fatal("b.txt is still waiting, so has_more must be true")
	}
}

func TestNormalizeGrepMode(t *testing.T) {
	cases := map[string]GrepMode{
		"":        GrepModeContent,
		"content": GrepModeContent,
		"files":   GrepModeFiles,
		" FILES ": GrepModeFiles,
		"count":   GrepModeCount,
	}
	for raw, want := range cases {
		got, err := NormalizeGrepMode(raw)
		if err != nil {
			t.Fatalf("NormalizeGrepMode(%q): %v", raw, err)
		}
		if got != want {
			t.Fatalf("NormalizeGrepMode(%q) = %q, want %q", raw, got, want)
		}
	}
	for _, raw := range []string{"files_without", "files_without_match", "bogus"} {
		if _, err := NormalizeGrepMode(raw); err == nil {
			t.Fatalf("NormalizeGrepMode(%q) must fail: the shared core does not implement it", raw)
		}
	}
}
