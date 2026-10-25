package searchcore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func grepPassthruFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range map[string]string{
		"a.txt": "one\nneedle here\nthree\n",
		"b.txt": "alpha\nbeta\n",
		"c.txt": "needle one\nmid\nneedle two\n",
	} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// grepEntryLabels formats the ordered page so a test can see match and context
// entries side by side. `:` means a match, `-` means a passthru context line -
// the same distinction the renderer makes.
func grepEntryLabels(t *testing.T, root string, result GrepResult) []string {
	t.Helper()
	labels := make([]string, 0, len(result.Matches))
	for _, match := range result.Matches {
		rel, err := filepath.Rel(root, match.File)
		if err != nil {
			t.Fatalf("rel: %v", err)
		}
		separator := ":"
		if match.IsContext {
			separator = "-"
		}
		labels = append(labels, fmt.Sprintf("%s%s%d:%s", filepath.ToSlash(rel), separator, match.Line, match.Text))
	}
	return labels
}

func grepPassthruResult(t *testing.T, root string, opts GrepOptions) GrepResult {
	t.Helper()
	opts.Path = root
	result, err := Grep(context.Background(), opts)
	if err != nil {
		t.Fatalf("grep %+v: %v", opts, err)
	}
	return result
}

func TestGrepPassthruEmitsEveryLine(t *testing.T) {
	root := grepPassthruFixture(t)
	result := grepPassthruResult(t, root, GrepOptions{Pattern: "needle", Passthru: true, Limit: 50})
	want := []string{
		"a.txt-1:one",
		"a.txt:2:needle here",
		"a.txt-3:three",
		"b.txt-1:alpha",
		"b.txt-2:beta",
		"c.txt:1:needle one",
		"c.txt-2:mid",
		"c.txt:3:needle two",
	}
	if got := grepEntryLabels(t, root, result); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("entries = %v\nwant      %v", got, want)
	}
	if countGrepMatchEntries(result.Matches) != 3 {
		t.Fatalf("match count = %d, want 3 (context lines are not matches)", countGrepMatchEntries(result.Matches))
	}
}

func TestGrepPassthruHonorsReplaceInvertAndMaxCount(t *testing.T) {
	root := grepPassthruFixture(t)
	cases := []struct {
		name string
		opts GrepOptions
		want []string
	}{
		{
			name: "replace only rewrites matching lines",
			opts: GrepOptions{Pattern: "needle", Passthru: true, Replace: "X", Limit: 50},
			want: []string{
				"a.txt-1:one", "a.txt:2:X here", "a.txt-3:three",
				"b.txt-1:alpha", "b.txt-2:beta",
				"c.txt:1:X one", "c.txt-2:mid", "c.txt:3:X two",
			},
		},
		{
			name: "invert swaps the markers",
			opts: GrepOptions{Pattern: "needle", Passthru: true, InvertMatch: true, Limit: 50},
			want: []string{
				"a.txt:1:one", "a.txt-2:needle here", "a.txt:3:three",
				"b.txt:1:alpha", "b.txt:2:beta",
				"c.txt-1:needle one", "c.txt:2:mid", "c.txt-3:needle two",
			},
		},
		{
			name: "max_count turns later matches into context",
			opts: GrepOptions{Pattern: "needle", Passthru: true, MaxCount: 1, MaxCountSet: true, Limit: 50},
			want: []string{
				"a.txt-1:one", "a.txt:2:needle here", "a.txt-3:three",
				"b.txt-1:alpha", "b.txt-2:beta",
				"c.txt:1:needle one", "c.txt-2:mid", "c.txt-3:needle two",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := grepPassthruResult(t, root, tc.opts)
			if got := grepEntryLabels(t, root, result); strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("entries = %v\nwant      %v", got, tc.want)
			}
		})
	}
}

func TestGrepPassthruOnlyMatchingKeepsFragmentsAndWholeContext(t *testing.T) {
	root := grepPassthruFixture(t)
	result := grepPassthruResult(t, root, GrepOptions{Pattern: "needle", Passthru: true, OnlyMatching: true, Limit: 50})
	want := []string{
		"a.txt-1:one",
		"a.txt:2:needle",
		"a.txt-3:three",
		"b.txt-1:alpha",
		"b.txt-2:beta",
		"c.txt:1:needle",
		"c.txt-2:mid",
		"c.txt:3:needle",
	}
	if got := grepEntryLabels(t, root, result); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("entries = %v\nwant      %v", got, want)
	}
}

func TestGrepPassthruIsIgnoredOutsideContentMode(t *testing.T) {
	root := grepPassthruFixture(t)
	for _, mode := range []GrepMode{GrepModeFiles, GrepModeCount, GrepModeFilesWithout} {
		result := grepPassthruResult(t, root, GrepOptions{Pattern: "needle", Passthru: true, Mode: mode, Limit: 50})
		for _, match := range result.Matches {
			if match.IsContext {
				t.Fatalf("mode %s collected a passthru context entry: %+v", mode, match)
			}
		}
	}
}

func TestGrepPassthruHandlerRendersAndReportsMatches(t *testing.T) {
	root := grepPassthruFixture(t)
	result := runGrepHandler(t, root, map[string]any{
		"pattern":  "needle",
		"passthru": true,
		"limit":    50,
	})
	if !result.Success {
		t.Fatalf("grep failed: %s", result.Content)
	}
	for _, want := range []string{
		"a.txt 1: one",
		"a.txt>2: needle here",
		"a.txt 3: three",
		"b.txt 1: alpha",
		"c.txt>1: needle one",
		"c.txt 2: mid",
		"c.txt>3: needle two",
	} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("rendered passthru page is missing %q:\n%s", want, result.Content)
		}
	}
	if got := result.Metadata["match_count"]; got != 3 {
		t.Fatalf("metadata.match_count = %#v, want 3", got)
	}
	if got := result.Metadata["passthru"]; got != true {
		t.Fatalf("metadata.passthru = %#v, want true", got)
	}
}

func TestGrepPassthruPagesEveryLine(t *testing.T) {
	root := grepPassthruFixture(t)
	result := runGrepHandler(t, root, map[string]any{
		"pattern":  "needle",
		"passthru": true,
		"limit":    3,
	})
	if !result.Success {
		t.Fatalf("grep failed: %s", result.Content)
	}
	// First page = a.txt's three lines; the second page starts at b.txt line 1.
	if !strings.Contains(result.Content, "a.txt>2: needle here") {
		t.Fatalf("page one is missing the match line:\n%s", result.Content)
	}
	if strings.Contains(result.Content, "b.txt") {
		t.Fatalf("page one must not include the next file:\n%s", result.Content)
	}
	if got := result.Metadata["next_offset"]; got != 3 {
		t.Fatalf("next_offset = %#v, want 3", got)
	}
	second := runGrepHandler(t, root, map[string]any{
		"pattern":  "needle",
		"passthru": true,
		"limit":    3,
		"offset":   3,
	})
	if !strings.Contains(second.Content, "b.txt 1: alpha") {
		t.Fatalf("page two is missing b.txt:\n%s", second.Content)
	}
}
