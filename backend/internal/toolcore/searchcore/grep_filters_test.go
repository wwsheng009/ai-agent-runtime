package searchcore

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// grepFilterFixture builds a small tree with two file types at two depths, so
// every include/exclude rule can be observed on both a bare name and a
// relative path.
func grepFilterFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range map[string]string{
		"a.go":             "needle a\n",
		"b.ts":             "needle b\n",
		"nested/c.go":      "needle c\n",
		"nested/d_test.go": "needle d\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func grepFilterMatches(t *testing.T, root string, opts GrepOptions) []string {
	t.Helper()
	opts.Path = root
	result, err := Grep(context.Background(), opts)
	if err != nil {
		t.Fatalf("grep %+v: %v", opts, err)
	}
	files := make([]string, 0, len(result.Matches))
	seen := map[string]bool{}
	for _, match := range result.Matches {
		rel, err := filepath.Rel(root, match.File)
		if err != nil {
			t.Fatalf("rel: %v", err)
		}
		rel = filepath.ToSlash(rel)
		if !seen[rel] {
			seen[rel] = true
			files = append(files, rel)
		}
	}
	sort.Strings(files)
	return files
}

func assertFileList(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// TestGrepIncludeExcludeFilters pins the shared file-filter semantics: a
// pattern without a slash matches the basename anywhere below the root, a
// pattern with a slash matches the slash path relative to the root, and
// exclude always wins over include.
func TestGrepIncludeExcludeFilters(t *testing.T) {
	root := grepFilterFixture(t)

	cases := []struct {
		name string
		opts GrepOptions
		want []string
	}{
		{
			name: "no filters sees every file",
			opts: GrepOptions{Pattern: "needle"},
			want: []string{"a.go", "b.ts", "nested/c.go", "nested/d_test.go"},
		},
		{
			name: "include bare extension matches nested files",
			opts: GrepOptions{Pattern: "needle", Include: []string{"*.go"}},
			want: []string{"a.go", "nested/c.go", "nested/d_test.go"},
		},
		{
			name: "include with slash is relative to the search root",
			opts: GrepOptions{Pattern: "needle", Include: []string{"nested/*.go"}},
			want: []string{"nested/c.go", "nested/d_test.go"},
		},
		{
			name: "exclude removes matches",
			opts: GrepOptions{Pattern: "needle", Exclude: []string{"*.ts"}},
			want: []string{"a.go", "nested/c.go", "nested/d_test.go"},
		},
		{
			name: "exclude wins over include",
			opts: GrepOptions{Pattern: "needle", Include: []string{"*.go"}, Exclude: []string{"*_test.go"}},
			want: []string{"a.go", "nested/c.go"},
		},
		{
			name: "include list unions",
			opts: GrepOptions{Pattern: "needle", Include: []string{"*.go", "*.ts"}},
			want: []string{"a.go", "b.ts", "nested/c.go", "nested/d_test.go"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertFileList(t, grepFilterMatches(t, root, tc.opts), tc.want)
		})
	}
}

func TestGrepIncludeExcludeFiltersOnSingleFile(t *testing.T) {
	root := grepFilterFixture(t)
	target := filepath.Join(root, "a.go")

	allowed, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle", Path: target, Include: []string{"*.go"},
	})
	if err != nil || len(allowed.Matches) != 1 {
		t.Fatalf("include *.go should keep a.go: err=%v matches=%+v", err, allowed.Matches)
	}
	rejected, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle", Path: target, Exclude: []string{"*.go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rejected.Matches) != 0 {
		t.Fatalf("exclude *.go should drop the single file, got %+v", rejected.Matches)
	}
}

func TestSplitGrepGlobList(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  []string
	}{
		{"single", "*.go", []string{"*.go"}},
		{"comma separated", "*.go,*.ts", []string{"*.go", "*.ts"}},
		{"brace form stays one pattern", "*.{go,ts}", []string{"*.{go,ts}"}},
		{"blanks and duplicates collapse", " a.go , ,a.go, b.ts ", []string{"a.go", "b.ts"}},
		{"empty", "   ", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SplitGrepGlobList(tc.value)
			assertFileList(t, got, tc.want)
		})
	}
}

func TestGrepGlobListFromArgs(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want []string
	}{
		{"string spelling", map[string]any{"include": "*.go,*.ts"}, []string{"*.go", "*.ts"}},
		{"interface array", map[string]any{"include": []any{"*.go", "*.ts"}}, []string{"*.go", "*.ts"}},
		{"string array", map[string]any{"include": []string{"*.go"}}, []string{"*.go"}},
		{"missing", map[string]any{}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertFileList(t, GrepGlobListFromArgs(tc.args, "include"), tc.want)
		})
	}
}
