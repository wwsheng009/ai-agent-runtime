package mesh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clearHomeEnv removes every environment input that path resolution consults,
// so a test can assert the fail-closed branch deterministically.
func clearHomeEnv(t *testing.T) {
	t.Helper()
	t.Setenv(EnvMeshDir, "")
	t.Setenv(EnvHome, "")
	t.Setenv("USERPROFILE", "")
	t.Setenv("HOME", "")
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")
}

func TestResolvePathsPrefersEnvOverride(t *testing.T) {
	clearHomeEnv(t)
	override := filepath.Join(t.TempDir(), "mesh-override")
	t.Setenv(EnvMeshDir, override)
	t.Setenv(EnvHome, filepath.Join(t.TempDir(), "aicli-home"))

	paths := ResolvePaths()
	if paths.Root != filepath.Clean(override) {
		t.Fatalf("Root = %q, want %q", paths.Root, filepath.Clean(override))
	}
	if paths.Source != PathSourceEnv {
		t.Fatalf("Source = %q, want %q", paths.Source, PathSourceEnv)
	}
	if !paths.Enabled() {
		t.Fatal("Enabled() = false, want true")
	}
	for name, dir := range map[string]string{
		"nodes":    paths.Nodes,
		"bindings": paths.Bindings,
		"leases":   paths.Leases,
		"journal":  paths.Journal,
	} {
		if filepath.Dir(dir) != paths.Root {
			t.Fatalf("%s dir = %q, want a direct child of %q", name, dir, paths.Root)
		}
	}
}

func TestResolvePathsHonoursAICLIHome(t *testing.T) {
	clearHomeEnv(t)
	home := filepath.Join(t.TempDir(), "aicli-home")
	t.Setenv(EnvHome, home)
	t.Setenv("USERPROFILE", filepath.Join(t.TempDir(), "user-home"))

	paths := ResolvePaths()
	want := filepath.Join(filepath.Clean(home), "mesh")
	if paths.Root != want {
		t.Fatalf("Root = %q, want %q", paths.Root, want)
	}
	if paths.Source != PathSourceAICLIHome {
		t.Fatalf("Source = %q, want %q", paths.Source, PathSourceAICLIHome)
	}
}

func TestResolvePathsFallsBackToUserHome(t *testing.T) {
	clearHomeEnv(t)
	userHome := filepath.Join(t.TempDir(), "user-home")
	t.Setenv("USERPROFILE", userHome)
	t.Setenv("HOME", userHome)

	paths := ResolvePaths()
	want := filepath.Join(filepath.Clean(userHome), ".aicli", "mesh")
	if paths.Root != want {
		t.Fatalf("Root = %q, want %q", paths.Root, want)
	}
	if paths.Source != PathSourceUserHome {
		t.Fatalf("Source = %q, want %q", paths.Source, PathSourceUserHome)
	}
}

// TestResolvePathsFailClosedWithoutHome is the difference-2 guard: with no home
// at all the mesh must resolve to "nothing", never to a CWD-relative .aicli.
func TestResolvePathsFailClosedWithoutHome(t *testing.T) {
	clearHomeEnv(t)

	paths := ResolvePaths()
	if paths.Root != "" {
		t.Fatalf("Root = %q, want empty (fail-closed)", paths.Root)
	}
	if paths.Enabled() {
		t.Fatal("Enabled() = true, want false")
	}
	if paths.Source != PathSourceUnset {
		t.Fatalf("Source = %q, want %q", paths.Source, PathSourceUnset)
	}
	if err := paths.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs() on fail-closed layout = %v, want nil", err)
	}
	if cwd, err := os.Getwd(); err == nil {
		if _, err := os.Stat(filepath.Join(cwd, ".aicli", "mesh")); err == nil {
			t.Fatalf("fail-closed resolution created %s", filepath.Join(cwd, ".aicli", "mesh"))
		}
	}
}

func TestPathsFailClosedHelpersReturnEmpty(t *testing.T) {
	paths := Paths{Source: PathSourceUnset}
	for name, got := range map[string]string{
		"NodePath":    paths.NodePath("node-1-20260101T000000Z"),
		"BindingPath": paths.BindingPath("session_x"),
		"JournalPath": paths.JournalPath("node-1-20260101T000000Z"),
		"LeasePath":   paths.LeasePath("session", "session_x"),
	} {
		if got != "" {
			t.Fatalf("%s = %q, want empty for a fail-closed layout", name, got)
		}
	}
}

func TestPathsEnsureDirsCreatesLayout(t *testing.T) {
	root := filepath.Join(t.TempDir(), "mesh-root")
	paths := pathsFor(root, PathSourceEnv)

	if err := paths.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs() = %v", err)
	}
	for _, dir := range []string{paths.Nodes, paths.Bindings, paths.Leases, paths.Journal} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a directory", dir)
		}
	}
	// Idempotent.
	if err := paths.EnsureDirs(); err != nil {
		t.Fatalf("second EnsureDirs() = %v", err)
	}
}

func TestSanitizeKey(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "session id unchanged", in: "session_20260924072950_ltYRU9tG", want: "session_20260924072950_ltYRU9tG"},
		{name: "node id unchanged", in: "node-8124-20260924T073012Z", want: "node-8124-20260924T073012Z"},
		{name: "slash replaced", in: "a/b", want: "a_b"},
		{name: "backslash replaced", in: `a\b`, want: "a_b"},
		{name: "space replaced", in: "a b", want: "a_b"},
		{name: "unicode replaced", in: "会话_1", want: "___1"},
		{name: "traversal dotdot rejected", in: "..", wantErr: true},
		{name: "traversal dot rejected", in: ".", wantErr: true},
		{name: "traversal with separator is neutralised", in: `..\..\x`, want: ".._.._x"},
		{name: "empty rejected", in: "   ", wantErr: true},
		{name: "trimmed", in: "  session_x  ", want: "session_x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SanitizeKey(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("SanitizeKey(%q) = %q, want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("SanitizeKey(%q) error = %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("SanitizeKey(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestPathsFileNaming(t *testing.T) {
	root := filepath.Join(t.TempDir(), "mesh-root")
	paths := pathsFor(root, PathSourceEnv)

	if got, want := paths.NodePath("node-8124-20260924T073012Z"), filepath.Join(root, "nodes", "node-8124-20260924T073012Z.json"); got != want {
		t.Fatalf("NodePath = %q, want %q", got, want)
	}
	if got, want := paths.BindingPath("session_20260924072950_ltYRU9tG"), filepath.Join(root, "bindings", "session_20260924072950_ltYRU9tG.json"); got != want {
		t.Fatalf("BindingPath = %q, want %q", got, want)
	}
	if got, want := paths.JournalPath("node-8124-20260924T073012Z"), filepath.Join(root, "journal", "node-8124-20260924T073012Z.ndjson"); got != want {
		t.Fatalf("JournalPath = %q, want %q", got, want)
	}
	if got, want := paths.LeasePath("session", "session_20260924072950_ltYRU9tG"), filepath.Join(root, "leases", "session-session_20260924072950_ltYRU9tG.lock"); got != want {
		t.Fatalf("LeasePath = %q, want %q", got, want)
	}
	// Invalid keys never produce a path.
	if got := paths.NodePath(".."); got != "" {
		t.Fatalf("NodePath(%q) = %q, want empty", "..", got)
	}
	if got := paths.BindingPath(""); got != "" {
		t.Fatalf("BindingPath(%q) = %q, want empty", "", got)
	}
	if got := paths.LeasePath("session", ".."); got != "" {
		t.Fatalf("LeasePath with unsafe key = %q, want empty", got)
	}
	if got := paths.LeasePath("", "session_x"); got != "" {
		t.Fatalf("LeasePath with empty purpose = %q, want empty", got)
	}
}

func TestResolvePathsSourceLabelsAreStable(t *testing.T) {
	// The labels are part of the `doctor` / `self` output contract.
	for label, want := range map[string]string{
		PathSourceEnv:       "env",
		PathSourceAICLIHome: "aicli-home",
		PathSourceUserHome:  "user-home",
		PathSourceUnset:     "unset",
	} {
		if label != want {
			t.Fatalf("source label %q != %q", label, want)
		}
		if strings.TrimSpace(label) == "" {
			t.Fatal("empty source label")
		}
	}
}
