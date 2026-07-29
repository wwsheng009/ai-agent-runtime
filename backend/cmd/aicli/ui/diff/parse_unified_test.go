package diff

import (
	"strings"
	"testing"
)

// TestParseUnifiedSplitsFilesWithoutGitHeader covers apply_patch and plain
// `diff -u` output, where consecutive file sections carry no "diff --git" line.
func TestParseUnifiedSplitsFilesWithoutGitHeader(t *testing.T) {
	src := strings.Join([]string{
		"--- /dev/null",
		"+++ b/internal/new_file.go",
		"@@ -0,0 +1,2 @@",
		"+package internal",
		"+",
		"--- a/internal/old_file.go",
		"+++ /dev/null",
		"@@ -1,2 +0,0 @@",
		"-package internal",
		"-",
	}, "\n")

	files := ParseUnified(src, DefaultParseOptions())
	if len(files) != 2 {
		t.Fatalf("files=%d, want 2: %+v", len(files), files)
	}
	if files[0].OldPath != "/dev/null" || files[0].NewPath != "internal/new_file.go" {
		t.Fatalf("file 0 paths=%q/%q", files[0].OldPath, files[0].NewPath)
	}
	if files[1].OldPath != "internal/old_file.go" || files[1].NewPath != "/dev/null" {
		t.Fatalf("file 1 paths=%q/%q", files[1].OldPath, files[1].NewPath)
	}
	for index, file := range files {
		if len(file.Hunks) != 1 {
			t.Fatalf("file %d hunks=%d, want 1", index, len(file.Hunks))
		}
		if len(file.Hunks[0].Lines) != 2 {
			t.Fatalf("file %d rows=%d, want 2: %+v", index, len(file.Hunks[0].Lines), file.Hunks[0].Lines)
		}
	}
	if kind := files[0].Hunks[0].Lines[0].Kind; kind != LineAdd {
		t.Fatalf("created file row kind=%v, want LineAdd", kind)
	}
	if kind := files[1].Hunks[0].Lines[0].Kind; kind != LineDelete {
		t.Fatalf("deleted file row kind=%v, want LineDelete", kind)
	}
}

// TestParseUnifiedKeepsHeaderLikeContent guards the header heuristic: rows
// inside a hunk whose own text starts with "++ " or "-- " must stay content.
func TestParseUnifiedKeepsHeaderLikeContent(t *testing.T) {
	src := strings.Join([]string{
		"diff --git a/notes.md b/notes.md",
		"--- a/notes.md",
		"+++ b/notes.md",
		"@@ -1,2 +1,3 @@",
		" intro",
		"+++ nested bullet",
		"---- legacy rule",
	}, "\n")

	files := ParseUnified(src, DefaultParseOptions())
	if len(files) != 1 {
		t.Fatalf("files=%d, want 1: %+v", len(files), files)
	}
	if files[0].NewPath != "notes.md" {
		t.Fatalf("path=%q, want notes.md", files[0].NewPath)
	}
	rows := files[0].Hunks[0].Lines
	if len(rows) != 3 {
		t.Fatalf("rows=%d, want 3: %+v", len(rows), rows)
	}
	if rows[1].Kind != LineAdd || rows[1].Text != "++ nested bullet" {
		t.Fatalf("row 1 = %v %q, want LineAdd %q", rows[1].Kind, rows[1].Text, "++ nested bullet")
	}
	if rows[2].Kind != LineDelete || rows[2].Text != "--- legacy rule" {
		t.Fatalf("row 2 = %v %q, want LineDelete %q", rows[2].Kind, rows[2].Text, "--- legacy rule")
	}
}

// TestParseUnifiedStripsTimestampColumn covers `diff -u` headers, which append
// a tab-separated modification time after the path.
func TestParseUnifiedStripsTimestampColumn(t *testing.T) {
	src := strings.Join([]string{
		"--- a/main.go\t2026-07-29 09:00:00.000000000 +0800",
		"+++ b/main.go\t2026-07-29 09:05:00.000000000 +0800",
		"@@ -1,1 +1,1 @@",
		"-old",
		"+new",
	}, "\n")

	files := ParseUnified(src, DefaultParseOptions())
	if len(files) != 1 {
		t.Fatalf("files=%d, want 1", len(files))
	}
	if files[0].OldPath != "main.go" || files[0].NewPath != "main.go" {
		t.Fatalf("paths=%q/%q, want main.go", files[0].OldPath, files[0].NewPath)
	}
}

func TestParseUnifiedWithLimitReportsTruncation(t *testing.T) {
	var b strings.Builder
	b.WriteString("--- a/big.go\n+++ b/big.go\n@@ -1,6 +1,6 @@\n")
	for i := 0; i < 6; i++ {
		b.WriteString(" row\n")
	}
	src := b.String()

	// Budget counts the @@ header plus content rows, so 4 stops mid-hunk.
	files, truncated := ParseUnifiedWithLimit(src, ParseOptions{MaxLines: 4})
	if !truncated {
		t.Fatal("expected truncated=true when the budget stops mid-diff")
	}
	if len(files) != 1 {
		t.Fatalf("files=%d, want 1", len(files))
	}
	if rows := len(files[0].Hunks[0].Lines); rows != 3 {
		t.Fatalf("rows=%d, want 3 within a budget of 4", rows)
	}

	if _, truncated = ParseUnifiedWithLimit(src, DefaultParseOptions()); truncated {
		t.Fatal("expected truncated=false when the whole diff fits")
	}
}

func TestParseUnifiedWithLimitIgnoresBlankTail(t *testing.T) {
	src := "--- a/x.go\n+++ b/x.go\n@@ -1,1 +1,1 @@\n-old\n+new\n\n\n"

	// The budget is reached exactly at the last content row; the remaining
	// blank lines must not be reported as dropped content.
	if _, truncated := ParseUnifiedWithLimit(src, ParseOptions{MaxLines: 3}); truncated {
		t.Fatal("blank tail must not count as truncation")
	}
}
