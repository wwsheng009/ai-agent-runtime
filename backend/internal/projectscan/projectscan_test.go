package projectscan

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeTree creates empty files for every slash-separated relative path.
// Empty files double as proof that the scan never depends on file contents.
func writeTree(t *testing.T, root string, rels ...string) {
	t.Helper()
	for _, rel := range rels {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, nil, 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", full, err)
		}
	}
}

// TestScanDetectsEveryBuiltinMarker walks the built-in marker table so every
// marker row is exercised: the file must be detected at the root and map to
// the expected project type.
func TestScanDetectsEveryBuiltinMarker(t *testing.T) {
	for _, mk := range markerTable {
		name := strings.ReplaceAll(mk.name, "*", "sample")
		t.Run(mk.name, func(t *testing.T) {
			root := t.TempDir()
			writeTree(t, root, name)

			got, err := Scan(root, Options{MaxDepth: 1})
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if got.Root != root {
				t.Fatalf("Root = %q, want %q", got.Root, root)
			}
			want := []Match{{Dir: ".", Marker: name, Type: mk.typ}}
			if !reflect.DeepEqual(got.Matches, want) {
				t.Fatalf("Matches = %#v, want %#v", got.Matches, want)
			}
			if wantTypes := []Type{mk.typ}; !reflect.DeepEqual(got.Types, wantTypes) {
				t.Fatalf("Types = %#v, want %#v", got.Types, wantTypes)
			}
			if !got.HasType(mk.typ) {
				t.Fatalf("HasType(%q) = false, Types = %v", mk.typ, got.Types)
			}
			if got.Truncated {
				t.Fatal("Truncated = true, want false")
			}
		})
	}
}

func TestScanScenarios(t *testing.T) {
	tests := []struct {
		name        string
		files       []string
		opts        Options
		wantMatches []Match
		wantTypes   []Type
		wantTrunc   bool
	}{
		{
			name: "empty root",
		},
		{
			name:  "no markers",
			files: []string{"README.md", "src/main.go", "docs/guide.md"},
		},
		{
			name:        "go module at root",
			files:       []string{"go.mod", "main.go"},
			wantMatches: []Match{{Dir: ".", Marker: "go.mod", Type: TypeGo}},
			wantTypes:   []Type{TypeGo},
		},
		{
			name: "polyglot monorepo",
			files: []string{
				"backend/go.mod",
				"backend/cmd/main.go",
				"frontend/package.json",
				"frontend/src/index.ts",
				"README.md",
			},
			wantMatches: []Match{
				{Dir: "backend", Marker: "go.mod", Type: TypeGo},
				{Dir: "frontend", Marker: "package.json", Type: TypeNode},
			},
			wantTypes: []Type{TypeGo, TypeNode},
		},
		{
			name:  "several markers of one type collapse",
			files: []string{"pyproject.toml", "requirements.txt", "setup.py"},
			wantMatches: []Match{
				{Dir: ".", Marker: "pyproject.toml", Type: TypePython},
				{Dir: ".", Marker: "requirements.txt", Type: TypePython},
				{Dir: ".", Marker: "setup.py", Type: TypePython},
			},
			wantTypes: []Type{TypePython},
		},
		{
			name:  "dotnet glob markers",
			files: []string{"src/App.csproj", "src/App.sln", "src/Lib.fsproj"},
			wantMatches: []Match{
				{Dir: "src", Marker: "App.csproj", Type: TypeDotNet},
				{Dir: "src", Marker: "App.sln", Type: TypeDotNet},
				{Dir: "src", Marker: "Lib.fsproj", Type: TypeDotNet},
			},
			wantTypes: []Type{TypeDotNet},
		},
		{
			name: "matches are sorted by directory then marker",
			files: []string{
				"zeta/go.mod",
				"alpha/package.json",
				"Cargo.toml",
			},
			wantMatches: []Match{
				{Dir: ".", Marker: "Cargo.toml", Type: TypeRust},
				{Dir: "alpha", Marker: "package.json", Type: TypeNode},
				{Dir: "zeta", Marker: "go.mod", Type: TypeGo},
			},
			wantTypes: []Type{TypeGo, TypeNode, TypeRust},
		},
		{
			name: "skips dependency and build directories",
			files: []string{
				"node_modules/pkg/package.json",
				"vendor/lib/go.mod",
				".git/package.json",
				"target/debug/Cargo.toml",
				"dist/package.json",
				"build/go.mod",
				"obj/project.csproj",
				"app/package.json",
			},
			wantMatches: []Match{{Dir: "app", Marker: "package.json", Type: TypeNode}},
			wantTypes:   []Type{TypeNode},
		},
		{
			name:  "depth 1 scans only the root",
			files: []string{"nested/go.mod"},
			opts:  Options{MaxDepth: 1},
		},
		{
			name:        "depth 2 reaches immediate children",
			files:       []string{"nested/go.mod", "nested/deeper/package.json"},
			opts:        Options{MaxDepth: 2},
			wantMatches: []Match{{Dir: "nested", Marker: "go.mod", Type: TypeGo}},
			wantTypes:   []Type{TypeGo},
		},
		{
			name:  "marker names are case sensitive",
			files: []string{"GO.MOD", "Package.json"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeTree(t, root, tt.files...)

			got, err := Scan(root, tt.opts)
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if !reflect.DeepEqual(got.Matches, tt.wantMatches) {
				t.Fatalf("Matches = %#v, want %#v", got.Matches, tt.wantMatches)
			}
			if !reflect.DeepEqual(got.Types, tt.wantTypes) {
				t.Fatalf("Types = %#v, want %#v", got.Types, tt.wantTypes)
			}
			if got.Truncated != tt.wantTrunc {
				t.Fatalf("Truncated = %v, want %v", got.Truncated, tt.wantTrunc)
			}
			for _, typ := range tt.wantTypes {
				if !got.HasType(typ) {
					t.Fatalf("HasType(%q) = false, Types = %v", typ, got.Types)
				}
			}
			for _, typ := range allTypes {
				if want := containsType(tt.wantTypes, typ); got.HasType(typ) != want {
					t.Fatalf("HasType(%q) = %v, want %v (Types = %v)", typ, got.HasType(typ), want, got.Types)
				}
			}
		})
	}
}

var allTypes = []Type{TypeGo, TypeNode, TypePython, TypeRust, TypeJava, TypeDotNet, TypeRuby, TypePHP}

func containsType(types []Type, want Type) bool {
	for _, typ := range types {
		if typ == want {
			return true
		}
	}
	return false
}

// TestScanDefaultDepthBoundary pins the level counting of the default depth:
// the root is level 1, so DefaultMaxDepth reaches exactly three levels below
// it.
func TestScanDefaultDepthBoundary(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "a/b/c/go.mod", "x/y/z/w/package.json")

	got, err := Scan(root, Options{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	want := []Match{{Dir: "a/b/c", Marker: "go.mod", Type: TypeGo}}
	if !reflect.DeepEqual(got.Matches, want) {
		t.Fatalf("Matches = %#v, want %#v", got.Matches, want)
	}
	// Visited: root, a, x, a/b, x/y, a/b/c, x/y/z (x/y/z/w is level 5).
	if got.DirsVisited != 7 {
		t.Fatalf("DirsVisited = %d, want 7", got.DirsVisited)
	}
	if got.Truncated {
		t.Fatal("Truncated = true, want false")
	}
}

func TestScanMaxDirsBound(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "d0/go.mod", "d1/go.mod", "d2/go.mod")

	got, err := Scan(root, Options{MaxDepth: 2, MaxDirs: 2, MaxEntries: 1000})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if got.DirsVisited != 2 {
		t.Fatalf("DirsVisited = %d, want 2", got.DirsVisited)
	}
	if !got.Truncated {
		t.Fatal("Truncated = false, want true")
	}
	// BFS visits the root and then d0 (entries are sorted by name).
	want := []Match{{Dir: "d0", Marker: "go.mod", Type: TypeGo}}
	if !reflect.DeepEqual(got.Matches, want) {
		t.Fatalf("Matches = %#v, want %#v", got.Matches, want)
	}
}

func TestScanMaxEntriesBound(t *testing.T) {
	root := t.TempDir()
	files := make([]string, 0, 11)
	for i := 0; i < 10; i++ {
		files = append(files, fmt.Sprintf("f%02d.txt", i))
	}
	files = append(files, "go.mod")
	writeTree(t, root, files...)

	t.Run("budget exhausted before go.mod", func(t *testing.T) {
		got, err := Scan(root, Options{MaxDepth: 1, MaxDirs: 10, MaxEntries: 5})
		if err != nil {
			t.Fatalf("Scan: %v", err)
		}
		if got.EntriesScanned != 5 {
			t.Fatalf("EntriesScanned = %d, want 5", got.EntriesScanned)
		}
		if !got.Truncated {
			t.Fatal("Truncated = false, want true")
		}
		if len(got.Matches) != 0 || got.HasType(TypeGo) {
			t.Fatalf("Matches = %#v, want none", got.Matches)
		}
	})

	t.Run("exact budget is not truncated", func(t *testing.T) {
		got, err := Scan(root, Options{MaxDepth: 1, MaxDirs: 10, MaxEntries: 11})
		if err != nil {
			t.Fatalf("Scan: %v", err)
		}
		if got.EntriesScanned != 11 {
			t.Fatalf("EntriesScanned = %d, want 11", got.EntriesScanned)
		}
		if got.Truncated {
			t.Fatal("Truncated = true, want false")
		}
		want := []Match{{Dir: ".", Marker: "go.mod", Type: TypeGo}}
		if !reflect.DeepEqual(got.Matches, want) {
			t.Fatalf("Matches = %#v, want %#v", got.Matches, want)
		}
	})
}

func TestScanIgnoresDirectoriesNamedLikeMarkers(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"go.mod", "package.json", "App.csproj"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", name, err)
		}
	}

	got, err := Scan(root, Options{MaxDepth: 1})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got.Matches) != 0 {
		t.Fatalf("Matches = %#v, want none", got.Matches)
	}
}

func TestScanDoesNotFollowSymlinkedDirectories(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "real/go.mod")

	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	got, err := Scan(root, Options{MaxDepth: 3})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	want := []Match{{Dir: "real", Marker: "go.mod", Type: TypeGo}}
	if !reflect.DeepEqual(got.Matches, want) {
		t.Fatalf("Matches = %#v, want %#v", got.Matches, want)
	}
}

func TestScanDoesNotModifyTree(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "go.mod", "sub/package.json", "sub/deep/Cargo.toml", "sub/deep/src/main.rs")

	before := snapshotTree(t, root)
	if _, err := Scan(root, Options{}); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	after := snapshotTree(t, root)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("scan changed the tree:\nbefore: %v\nafter:  %v", before, after)
	}
}

func snapshotTree(t *testing.T, root string) []string {
	t.Helper()
	var entries []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		entries = append(entries, fmt.Sprintf("%s|%d|%s", filepath.ToSlash(rel), info.Size(), info.Mode()))
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir: %v", err)
	}
	return entries
}

func TestScanCleansRoot(t *testing.T) {
	root := t.TempDir()
	got, err := Scan(root+string(os.PathSeparator), Options{MaxDepth: 1})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if got.Root != root {
		t.Fatalf("Root = %q, want %q", got.Root, root)
	}
}

func TestScanErrors(t *testing.T) {
	t.Run("blank root", func(t *testing.T) {
		got, err := Scan("   ", Options{})
		if err == nil {
			t.Fatalf("Scan returned nil error, result %#v", got)
		}
		if got != nil {
			t.Fatalf("result = %#v, want nil", got)
		}
	})

	t.Run("missing root", func(t *testing.T) {
		if _, err := Scan(filepath.Join(t.TempDir(), "missing"), Options{}); err == nil {
			t.Fatal("Scan returned nil error")
		}
	})

	t.Run("root is a file", func(t *testing.T) {
		root := t.TempDir()
		writeTree(t, root, "go.mod")
		if _, err := Scan(filepath.Join(root, "go.mod"), Options{}); err == nil {
			t.Fatal("Scan returned nil error")
		}
	})
}

func TestOptionsNormalized(t *testing.T) {
	defaults := Options{MaxDepth: DefaultMaxDepth, MaxDirs: DefaultMaxDirs, MaxEntries: DefaultMaxEntries}
	tests := []struct {
		name string
		in   Options
		want Options
	}{
		{
			name: "zero value uses defaults",
			in:   Options{},
			want: defaults,
		},
		{
			name: "negative values use defaults",
			in:   Options{MaxDepth: -1, MaxDirs: -1, MaxEntries: -1},
			want: defaults,
		},
		{
			name: "explicit values are kept",
			in:   Options{MaxDepth: 2, MaxDirs: 3, MaxEntries: 4},
			want: Options{MaxDepth: 2, MaxDirs: 3, MaxEntries: 4},
		},
		{
			name: "mixed values fill only the gaps",
			in:   Options{MaxDepth: 2},
			want: Options{MaxDepth: 2, MaxDirs: DefaultMaxDirs, MaxEntries: DefaultMaxEntries},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.normalized(); got != tt.want {
				t.Fatalf("normalized() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestResultHasType(t *testing.T) {
	res := Result{Types: []Type{TypeGo, TypePython}}
	tests := []struct {
		name string
		typ  Type
		want bool
	}{
		{name: "go detected", typ: TypeGo, want: true},
		{name: "python detected", typ: TypePython, want: true},
		{name: "node absent", typ: TypeNode, want: false},
		{name: "empty absent", typ: Type(""), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := res.HasType(tt.typ); got != tt.want {
				t.Fatalf("HasType(%q) = %v, want %v", tt.typ, got, tt.want)
			}
		})
	}
}

func TestMatchPath(t *testing.T) {
	tests := []struct {
		name string
		m    Match
		want string
	}{
		{name: "root", m: Match{Dir: ".", Marker: "go.mod"}, want: "go.mod"},
		{name: "empty dir", m: Match{Marker: "go.mod"}, want: "go.mod"},
		{name: "nested", m: Match{Dir: "backend", Marker: "go.mod"}, want: "backend/go.mod"},
		{name: "deep", m: Match{Dir: "a/b", Marker: "package.json"}, want: "a/b/package.json"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.m.Path(); got != tt.want {
				t.Fatalf("Path() = %q, want %q", got, tt.want)
			}
		})
	}
}
