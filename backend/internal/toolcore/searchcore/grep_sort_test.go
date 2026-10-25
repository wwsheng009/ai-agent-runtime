package searchcore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// grepSortFixture has three files with distinct modification times, so path
// order and time order cannot accidentally agree on every case.
func grepSortFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range map[string]string{
		"a.txt":        "needle a\n",
		"b.txt":        "needle b\n",
		"nested/c.txt": "needle c\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stamps := map[string]time.Time{
		"a.txt":        time.Date(2020, 1, 3, 0, 0, 0, 0, time.UTC),
		"b.txt":        time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC),
		"nested/c.txt": time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	for path, stamp := range stamps {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.Chtimes(full, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// grepOrderedFiles returns the files in result order (not sorted), so the test
// observes the page order the caller receives.
func grepOrderedFiles(t *testing.T, root string, opts GrepOptions) []string {
	t.Helper()
	opts.Path = root
	result, err := Grep(context.Background(), opts)
	if err != nil {
		t.Fatalf("grep %+v: %v", opts, err)
	}
	files := make([]string, 0, len(result.Matches))
	for _, match := range result.Matches {
		rel, err := filepath.Rel(root, match.File)
		if err != nil {
			t.Fatalf("rel: %v", err)
		}
		files = append(files, filepath.ToSlash(rel))
	}
	return files
}

func TestGrepSortOrdersCandidatesBeforeScanning(t *testing.T) {
	root := grepSortFixture(t)
	cases := []struct {
		name string
		opts GrepOptions
		want []string
	}{
		{
			name: "path ascending",
			opts: GrepOptions{Pattern: "needle", SortKey: "path", SortSet: true},
			want: []string{"a.txt", "b.txt", "nested/c.txt"},
		},
		{
			name: "path descending",
			opts: GrepOptions{Pattern: "needle", SortKey: "path", SortReverse: true, SortSet: true},
			want: []string{"nested/c.txt", "b.txt", "a.txt"},
		},
		{
			name: "modified ascending",
			opts: GrepOptions{Pattern: "needle", SortKey: "modified", SortSet: true},
			want: []string{"nested/c.txt", "b.txt", "a.txt"},
		},
		{
			name: "modified descending",
			opts: GrepOptions{Pattern: "needle", SortKey: "modified", SortReverse: true, SortSet: true},
			want: []string{"a.txt", "b.txt", "nested/c.txt"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := grepOrderedFiles(t, root, tc.opts); strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("order = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGrepSortAppliesBeforePaging(t *testing.T) {
	root := grepSortFixture(t)
	opts := GrepOptions{Pattern: "needle", Path: root, SortKey: "path", SortSet: true, Limit: 1, Offset: 1}
	result, err := Grep(context.Background(), opts)
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if len(result.Matches) != 1 {
		t.Fatalf("page has %d matches, want 1: %+v", len(result.Matches), result.Matches)
	}
	if got := filepath.Base(result.Matches[0].File); got != "b.txt" {
		t.Fatalf("page entry = %q, want b.txt (the second file in path order)", got)
	}
	if !result.Truncated {
		t.Fatal("a page cut out of a sorted result must report truncated/has_more")
	}
}

func TestGrepSortFromArgsPrecedence(t *testing.T) {
	cases := []struct {
		name        string
		args        map[string]any
		wantKey     string
		wantReverse bool
		wantSet     bool
		wantErr     bool
	}{
		{name: "unset", args: map[string]any{}},
		{name: "sort", args: map[string]any{"sort": "path"}, wantKey: "path", wantSet: true},
		{name: "sortr wins over sort", args: map[string]any{"sort": "modified", "sortr": "path"}, wantKey: "path", wantReverse: true, wantSet: true},
		{name: "sort_reverse alias", args: map[string]any{"sort": "path", "sort_reverse": "modified"}, wantKey: "modified", wantReverse: true, wantSet: true},
		{name: "sort_files true means path", args: map[string]any{"sort": "modified", "sort_files": true}, wantKey: "path", wantSet: true},
		{name: "sort_files false clears", args: map[string]any{"sort": "path", "sort_files": false}, wantSet: true},
		{name: "no_sort_files cancels path", args: map[string]any{"sort_files": true, "no_sort_files": true}, wantSet: true},
		{name: "no_sort_files leaves time sort alone", args: map[string]any{"sort": "modified", "no_sort_files": true}, wantKey: "modified", wantSet: true},
		{name: "no_sort_files alone does not request no-sort", args: map[string]any{"no_sort_files": true}},
		{name: "invalid key", args: map[string]any{"sort": "size"}, wantErr: true},
		{name: "empty sort is unset", args: map[string]any{"sort": "  ", "sortr": "path"}, wantKey: "path", wantReverse: true, wantSet: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, reverse, set, err := grepSortFromArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("grepSortFromArgs(%v) accepted an invalid key", tc.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("grepSortFromArgs(%v): %v", tc.args, err)
			}
			if key != tc.wantKey || reverse != tc.wantReverse || set != tc.wantSet {
				t.Fatalf("grepSortFromArgs(%v) = %q/%v/%v, want %q/%v/%v",
					tc.args, key, reverse, set, tc.wantKey, tc.wantReverse, tc.wantSet)
			}
		})
	}
}

func TestGrepSortMetadata(t *testing.T) {
	root := grepSortFixture(t)
	cases := []struct {
		name          string
		args          map[string]any
		wantSort      string
		wantSortr     string
		wantSortFiles bool
	}{
		{
			name:          "reverse path",
			args:          map[string]any{"pattern": "needle", "sortr": "path"},
			wantSort:      "",
			wantSortr:     "path",
			wantSortFiles: false,
		},
		{
			name:          "sort files",
			args:          map[string]any{"pattern": "needle", "sort_files": true},
			wantSort:      "path",
			wantSortr:     "",
			wantSortFiles: true,
		},
		{
			name:          "reverse modified",
			args:          map[string]any{"pattern": "needle", "sortr": "modified"},
			wantSort:      "",
			wantSortr:     "modified",
			wantSortFiles: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := runGrepHandler(t, root, tc.args)
			if !result.Success {
				t.Fatalf("grep failed: %s", result.Content)
			}
			if got := result.Metadata["sort"]; got != tc.wantSort {
				t.Fatalf("metadata.sort = %#v, want %q", got, tc.wantSort)
			}
			if got := result.Metadata["sortr"]; got != tc.wantSortr {
				t.Fatalf("metadata.sortr = %#v, want %q", got, tc.wantSortr)
			}
			if got := result.Metadata["sort_files"]; got != tc.wantSortFiles {
				t.Fatalf("metadata.sort_files = %#v, want %v", got, tc.wantSortFiles)
			}
		})
	}
}

func TestGrepSortInvalidValueIsAnArgumentError(t *testing.T) {
	root := grepSortFixture(t)
	result := runGrepHandler(t, root, map[string]any{"pattern": "needle", "sort": "size"})
	if result.Success {
		t.Fatalf("invalid sort was accepted: %s", result.Content)
	}
	if result.ErrorCode == "" {
		t.Fatal("invalid sort must carry a classified error code")
	}
}

func TestRipgrepSearchArgsCarriesTheRequestedSort(t *testing.T) {
	countSortFlags := func(args []string) int {
		count := 0
		for _, arg := range args {
			if arg == "--sort" || arg == "--sortr" {
				count++
			}
		}
		return count
	}

	cases := []struct {
		name string
		opts GrepOptions
		want []string
	}{
		{
			name: "default stays path",
			opts: GrepOptions{},
			want: []string{"--sort", "path"},
		},
		{
			name: "ascending modified replaces the default",
			opts: GrepOptions{SortKey: "modified", SortSet: true},
			want: []string{"--sort", "modified"},
		},
		{
			name: "reverse path",
			opts: GrepOptions{SortKey: "path", SortReverse: true, SortSet: true},
			want: []string{"--sortr", "path"},
		},
		{
			name: "none is explicit",
			opts: GrepOptions{SortKey: "none", SortSet: true},
			want: []string{"--sort", "none"},
		},
		{
			name: "explicit no sort reaches the engine",
			opts: GrepOptions{SortSet: true},
			want: []string{"--sort", "none"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := ripgrepSearchArgs(tc.opts)
			if countSortFlags(args) != 1 {
				t.Fatalf("argv has %d sort flags, want exactly one: %v", countSortFlags(args), args)
			}
			if !strings.Contains(strings.Join(args, " "), strings.Join(tc.want, " ")) {
				t.Fatalf("argv = %v, want %v", args, tc.want)
			}
		})
	}
}

func TestGrepFileTimesAreReadable(t *testing.T) {
	if !grepFileTimesSupported() {
		t.Skip("this platform does not expose access/creation time")
	}
	root := t.TempDir()
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := fileAccessTime(info); !ok {
		t.Fatal("access time was not readable on a platform that advertises support")
	}
	if _, ok := fileCreationTime(info); !ok {
		t.Fatal("creation time was not readable on a platform that advertises support")
	}
}
