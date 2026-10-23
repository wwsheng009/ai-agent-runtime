package searchcore

import (
	"os"
	"path/filepath"
	"testing"
)

func grepTypeFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range map[string]string{
		"a.go":         "needle go\n",
		"b.py":         "needle py\n",
		"e.txt":        "needle text\n",
		"nested/c.go":  "needle nested go\n",
		"nested/d.hpp": "needle header\n",
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

// TestGrepTypeFilters pins the shared file-type semantics: a type name expands
// through the shared table into glob patterns (a union with any explicit
// include/exclude filters), and an unknown type falls back to `*.<type>`.
func TestGrepTypeFilters(t *testing.T) {
	root := grepTypeFixture(t)

	cases := []struct {
		name string
		opts GrepOptions
		want []string
	}{
		{
			name: "type go matches nested files",
			opts: GrepOptions{Pattern: "needle", Type: "go"},
			want: []string{"a.go", "nested/c.go"},
		},
		{
			name: "type cpp expands to every header and source glob",
			opts: GrepOptions{Pattern: "needle", Type: "cpp"},
			want: []string{"nested/d.hpp"},
		},
		{
			name: "type_not removes the type and keeps the rest",
			opts: GrepOptions{Pattern: "needle", TypeNot: "go"},
			want: []string{"b.py", "e.txt", "nested/d.hpp"},
		},
		{
			name: "type and include are a union",
			opts: GrepOptions{Pattern: "needle", Type: "go", Include: []string{"*.py"}},
			want: []string{"a.go", "b.py", "nested/c.go"},
		},
		{
			name: "exclude still wins over type",
			opts: GrepOptions{Pattern: "needle", Type: "go", Exclude: []string{"nested/*.go"}},
			want: []string{"a.go"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertFileList(t, grepFilterMatches(t, root, tc.opts), tc.want)
		})
	}
}

func TestGrepTypeUnknownFallsBackToExtensionGlob(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "thing.zzz"), []byte("needle zzz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "thing.txt"), []byte("needle text\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := grepFilterMatches(t, root, GrepOptions{Pattern: "needle", Type: "zzz"})
	assertFileList(t, got, []string{"thing.zzz"})
}

func TestGrepTypeGlobsTable(t *testing.T) {
	cases := []struct {
		fileType string
		want     []string
	}{
		{"go", []string{"*.go"}},
		{"cpp", []string{"*.cpp", "*.hpp", "*.cc", "*.cxx"}},
		{"md", []string{"*.md", "*.markdown"}},
		{"", nil},
		{"zzz", []string{"*.zzz"}},
	}
	for _, tc := range cases {
		t.Run(tc.fileType, func(t *testing.T) {
			assertFileList(t, GrepTypeGlobs(tc.fileType), tc.want)
		})
	}
}

func TestEffectiveGrepFiltersFoldsTypeIntoInclude(t *testing.T) {
	include, exclude := EffectiveGrepFilters(GrepOptions{
		Type:    "go",
		TypeNot: "py",
		Include: []string{"*.txt"},
		Exclude: []string{"*_test.go"},
	})
	assertFileList(t, include, []string{"*.go", "*.txt"})
	assertFileList(t, exclude, []string{"*.py", "*_test.go"})
}
