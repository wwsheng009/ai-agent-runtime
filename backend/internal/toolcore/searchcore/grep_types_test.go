package searchcore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
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
	include, exclude, err := EffectiveGrepFilters(GrepOptions{
		Type:    "go",
		TypeNot: "py",
		Include: []string{"*.txt"},
		Exclude: []string{"*_test.go"},
	})
	if err != nil {
		t.Fatalf("EffectiveGrepFilters: %v", err)
	}
	assertFileList(t, include, []string{"*.go", "*.txt"})
	assertFileList(t, exclude, []string{"*.py", "*_test.go"})
}

// grepTypeDefinitionFixture carries one file per rule the type_add/type_clear
// tests need to observe: a type that only exists because it was added, a glob
// appended to a built-in type, and both halves of a multi-glob built-in.
func grepTypeDefinitionFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range map[string]string{
		"a.go":         "needle go\n",
		"a.gold":       "needle gold\n",
		"a.md":         "needle md\n",
		"a.txt":        "needle text\n",
		"a.doc":        "needle doc\n",
		"nested/b.cpp": "needle cpp\n",
		"nested/c.hpp": "needle hpp\n",
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

// TestGrepTypeAddAndClearAreResolvedByTheSharedCore pins that type definitions
// are table edits in the shared core, not flags handed to one engine: the
// walker (and therefore ripgrep, which consumes the same resolved globs) sees
// exactly the file set the definitions describe.
func TestGrepTypeAddAndClearAreResolvedByTheSharedCore(t *testing.T) {
	root := grepTypeDefinitionFixture(t)
	cases := []struct {
		name string
		opts GrepOptions
		want []string
	}{
		{
			name: "type_add defines a name the table does not have",
			opts: GrepOptions{Pattern: "needle", Type: "doc", TypeAdd: []string{"doc:*.md"}},
			want: []string{"a.md"},
		},
		{
			name: "type_add extends a built-in type",
			opts: GrepOptions{Pattern: "needle", Type: "go", TypeAdd: []string{"go:*.gold"}},
			want: []string{"a.go", "a.gold"},
		},
		{
			name: "one definition may list several globs",
			opts: GrepOptions{Pattern: "needle", Type: "doc", TypeAdd: []string{"doc:*.md,*.txt"}},
			want: []string{"a.md", "a.txt"},
		},
		{
			name: "type_not resolves against the extended table",
			opts: GrepOptions{Pattern: "needle", TypeNot: "go", TypeAdd: []string{"go:*.gold"}},
			want: []string{"a.doc", "a.md", "a.txt", "nested/b.cpp", "nested/c.hpp"},
		},
		{
			name: "type_clear removes a definition",
			opts: GrepOptions{Pattern: "needle", Type: "cpp", TypeClear: []string{"cpp"}},
			want: []string{"nested/b.cpp"},
		},
		{
			name: "clear wins when the same name is added and cleared",
			opts: GrepOptions{Pattern: "needle", Type: "doc", TypeAdd: []string{"doc:*.md"}, TypeClear: []string{"doc"}},
			want: []string{"a.doc"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertFileList(t, grepFilterMatches(t, root, tc.opts), tc.want)
		})
	}
}

func TestGrepTypeAddRejectsMalformedDefinition(t *testing.T) {
	root := grepTypeDefinitionFixture(t)
	for _, spec := range []string{"doc", ":*.md", "doc:", "doc:, "} {
		t.Run(spec, func(t *testing.T) {
			result := runGrepHandler(t, root, map[string]any{
				"pattern":  "needle",
				"type_add": spec,
			})
			if result.Success {
				t.Fatalf("type_add %q was accepted: %s", spec, result.Content)
			}
			if result.ErrorCode != toolcore.ErrInvalidArgument {
				t.Fatalf("type_add %q error code = %q, want %q", spec, result.ErrorCode, toolcore.ErrInvalidArgument)
			}
		})
	}
}

func TestGrepPublishesResolvedTypeDefinitions(t *testing.T) {
	root := grepTypeDefinitionFixture(t)
	result := runGrepHandler(t, root, map[string]any{
		"pattern":    "needle",
		"type":       "doc",
		"type_add":   []any{"doc:*.md", "go:*.gold"},
		"type_clear": []any{"cpp"},
	})
	if !result.Success {
		t.Fatalf("grep failed: %s", result.Content)
	}
	assertFileList(t, result.Metadata["type_add"].([]string), []string{"doc:*.md", "go:*.gold"})
	assertFileList(t, result.Metadata["type_clear"].([]string), []string{"cpp"})
}
