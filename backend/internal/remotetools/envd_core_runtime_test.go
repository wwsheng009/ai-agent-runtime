package remotetools

// The production runtime's path resolver is what renders every label the shared
// cores publish, so its answers are pinned at the unit level as well: the e2e
// render tests would still pass a label that merely *contains* the relative
// path, and this file is the assertion that cannot.

import "testing"

func TestEnvdPathsRenderWorkspaceRelativeLabels(t *testing.T) {
	paths := &envdPaths{backend: &envdBackend{workspace: "/srv/app"}}
	if got := paths.Root(); got != "/srv/app" {
		t.Fatalf("Root = %q, want the target workspace", got)
	}
	cases := []struct{ abs, want string }{
		{"/srv/app", "."},
		{"/srv/app/README.md", "README.md"},
		{"/srv/app/src/deep/x.go", "src/deep/x.go"},
		// A path outside the workspace is not truncated into something that reads
		// as if it were inside: labels only trim their own root.
		{"/etc/passwd", "/etc/passwd"},
	}
	for _, c := range cases {
		if got := paths.Relative(c.abs); got != c.want {
			t.Fatalf("Relative(%q) = %q, want %q", c.abs, got, c.want)
		}
	}
}

// A resolver without a backend is a wiring bug, not a resolver that answers
// relative paths as absolute ones: it must say so.
func TestEnvdPathsWithoutABackendRefuseToResolve(t *testing.T) {
	var paths *envdPaths
	if _, err := paths.Resolve("."); err == nil {
		t.Fatal("an unbound resolver must error, not invent a path")
	}
}
