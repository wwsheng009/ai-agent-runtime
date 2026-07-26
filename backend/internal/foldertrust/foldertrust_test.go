package foldertrust

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDecidePrecedence(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		feature bool
		in      DecideInputs
		want    Outcome
	}{
		{
			name:    "feature off always trusted",
			feature: false,
			in:      DecideInputs{RepoConfigsPresent: true, Interactive: false},
			want:    OutcomeTrusted,
		},
		{
			name:    "store trusted",
			feature: true,
			in:      DecideInputs{StoreTrusted: true, RepoConfigsPresent: true},
			want:    OutcomeTrusted,
		},
		{
			name:    "unrecordable key trusted",
			feature: true,
			in:      DecideInputs{KeyRecordable: false, RepoConfigsPresent: true, Interactive: true},
			want:    OutcomeTrusted,
		},
		{
			name:    "no configs trusted",
			feature: true,
			in:      DecideInputs{KeyRecordable: true, RepoConfigsPresent: false},
			want:    OutcomeTrusted,
		},
		{
			name:    "interactive prompt",
			feature: true,
			in:      DecideInputs{KeyRecordable: true, RepoConfigsPresent: true, Interactive: true},
			want:    OutcomePrompt,
		},
		{
			name:    "headless untrusted",
			feature: true,
			in:      DecideInputs{KeyRecordable: true, RepoConfigsPresent: true, Interactive: false},
			want:    OutcomeUntrusted,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := Decide(tc.feature, tc.in)
			if got != tc.want {
				t.Fatalf("Decide() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestProjectScopeAllowed(t *testing.T) {
	t.Parallel()
	if !ProjectScopeAllowed(OutcomeTrusted) {
		t.Fatal("trusted should allow project scope")
	}
	if ProjectScopeAllowed(OutcomeUntrusted) {
		t.Fatal("untrusted must not allow project scope")
	}
	if ProjectScopeAllowed(OutcomePrompt) {
		t.Fatal("prompt must not allow project scope until resolved")
	}
}

func TestFeatureEnabledFromEnv(t *testing.T) {
	t.Parallel()
	if FeatureEnabledFromEnv("") {
		t.Fatal("empty should be off")
	}
	if FeatureEnabledFromEnv("0") || FeatureEnabledFromEnv("false") || FeatureEnabledFromEnv("off") {
		t.Fatal("false-ish should be off")
	}
	if !FeatureEnabledFromEnv("1") || !FeatureEnabledFromEnv("true") || !FeatureEnabledFromEnv("on") {
		t.Fatal("true-ish should be on")
	}
}

func TestStoreGrantAndCascade(t *testing.T) {
	root := t.TempDir()
	storePath := filepath.Join(root, "trusted_folders.yaml")
	store := LoadFrom(storePath)

	parent := filepath.Join(root, "repo")
	child := filepath.Join(parent, "sub")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	// Make absolute/canonical
	parent = Canonicalize(parent)
	child = Canonicalize(child)

	if store.IsTrusted(parent) {
		t.Fatal("expected untrusted initially")
	}
	if err := store.SetTrusted(parent); err != nil {
		t.Fatalf("SetTrusted: %v", err)
	}
	if !store.IsTrusted(parent) {
		t.Fatal("parent should be trusted")
	}
	if !store.IsTrusted(child) {
		t.Fatal("child should cascade from parent trust")
	}

	// Child explicit untrust overrides ancestor.
	if err := store.SetUntrusted(child); err != nil {
		t.Fatalf("SetUntrusted: %v", err)
	}
	if store.IsTrusted(child) {
		t.Fatal("child explicit untrust should win")
	}
	if !store.IsTrusted(parent) {
		t.Fatal("parent should remain trusted")
	}

	// Reload from disk.
	store2 := LoadFrom(storePath)
	if !store2.IsTrusted(parent) {
		t.Fatal("reloaded store should trust parent")
	}
	if store2.IsTrusted(child) {
		t.Fatal("reloaded store should not trust child")
	}
}

func TestStoreRefusesUnsafeRoots(t *testing.T) {
	root := t.TempDir()
	store := LoadFrom(filepath.Join(root, "trusted_folders.yaml"))

	if err := store.SetTrusted("relative/path"); err != nil {
		t.Fatalf("relative should no-op ok: %v", err)
	}
	if store.IsTrusted("relative/path") {
		t.Fatal("relative must not be trusted")
	}

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home")
	}
	if err := store.SetTrusted(home); err != nil {
		t.Fatalf("home should no-op ok: %v", err)
	}
	if store.IsTrusted(home) {
		t.Fatal("home must not be trusted")
	}

	fsRoot := string(filepath.Separator)
	if runtime.GOOS == "windows" {
		fsRoot = filepath.VolumeName(Canonicalize(root)) + string(filepath.Separator)
	}
	if err := store.SetTrusted(fsRoot); err != nil {
		t.Fatalf("fs root should no-op ok: %v", err)
	}
	if store.IsTrusted(fsRoot) {
		t.Fatal("fs root must not be trusted")
	}
}

func TestRepoConfigsPresent(t *testing.T) {
	t.Parallel()

	empty := t.TempDir()
	if RepoConfigsPresent(empty) {
		t.Fatal("empty dir should have no configs")
	}

	// Project plugins
	pluginRoot := t.TempDir()
	pluginDir := filepath.Join(pluginRoot, ".aicli", "plugins", "evil")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.yaml"), []byte("name: evil\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !RepoConfigsPresent(pluginRoot) {
		t.Fatal("project plugin should be detected")
	}
	kinds := RepoConfigKinds(pluginRoot)
	if len(kinds) == 0 || kinds[0] != ConfigKindPlugins {
		t.Fatalf("expected plugins kind, got %v", kinds)
	}

	// Project MCP
	mcpRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(mcpRoot, "mcp.yaml"), []byte("mcp_servers: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !RepoConfigsPresent(mcpRoot) {
		t.Fatal("mcp.yaml should be detected")
	}

	// Project hooks
	hooksRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(hooksRoot, ".aicli"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooksRoot, ".aicli", "hooks.yaml"), []byte("hooks: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !RepoConfigsPresent(hooksRoot) {
		t.Fatal("hooks.yaml should be detected")
	}

	// Project agents
	agentsRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(agentsRoot, ".agents", "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsRoot, ".agents", "agents", "foo.yaml"), []byte("name: foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !RepoConfigsPresent(agentsRoot) {
		t.Fatal("project agents should be detected")
	}
}

func TestResolveGrantAndHeadless(t *testing.T) {
	project := t.TempDir()
	pluginDir := filepath.Join(project, ".aicli", "plugins", "p")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.yaml"), []byte("name: p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(t.TempDir(), "trusted_folders.yaml")
	store := LoadFrom(storePath)
	feature := true
	interactive := false

	// Headless + configs → untrusted
	res := Resolve(ResolveOptions{
		CWD:            project,
		Store:          store,
		FeatureEnabled: &feature,
		Interactive:    &interactive,
	})
	if res.Trusted {
		t.Fatalf("headless should deny project scope, got %+v", res)
	}
	if res.Source != "headless_deny" {
		t.Fatalf("source = %q", res.Source)
	}

	// Grant → trusted
	res = Resolve(ResolveOptions{
		CWD:            project,
		Store:          store,
		FeatureEnabled: &feature,
		Interactive:    &interactive,
		TrustGrant:     true,
	})
	if !res.Trusted {
		t.Fatalf("grant should trust, got %+v", res)
	}
	if !store.IsTrusted(WorkspaceKey(project)) {
		t.Fatal("store should record grant")
	}

	// Feature off → trusted even with configs and no store
	store2 := LoadFrom(filepath.Join(t.TempDir(), "empty.yaml"))
	off := false
	res = Resolve(ResolveOptions{
		CWD:            project,
		Store:          store2,
		FeatureEnabled: &off,
		Interactive:    &interactive,
	})
	if !res.Trusted || res.Source != "feature_off" {
		t.Fatalf("feature off should trust, got %+v", res)
	}
}

func TestResolvePromptYesNo(t *testing.T) {
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".aicli"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "mcp.yaml"), []byte("x: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	feature := true
	interactive := true

	storeYes := LoadFrom(filepath.Join(t.TempDir(), "yes.yaml"))
	res := Resolve(ResolveOptions{
		CWD:            project,
		Store:          storeYes,
		FeatureEnabled: &feature,
		Interactive:    &interactive,
		Stdin:          strings.NewReader("yes\n"),
		Stderr:         ioDiscard{},
	})
	if !res.Trusted || res.Source != "prompt_yes" {
		t.Fatalf("prompt yes failed: %+v", res)
	}

	storeNo := LoadFrom(filepath.Join(t.TempDir(), "no.yaml"))
	res = Resolve(ResolveOptions{
		CWD:            project,
		Store:          storeNo,
		FeatureEnabled: &feature,
		Interactive:    &interactive,
		Stdin:          strings.NewReader("n\n"),
		Stderr:         ioDiscard{},
	})
	if res.Trusted || res.Source != "prompt_no" {
		t.Fatalf("prompt no failed: %+v", res)
	}
	if storeNo.IsTrusted(WorkspaceKey(project)) {
		t.Fatal("decline must not leave store trusted")
	}
}

func TestIsProjectScopedPath(t *testing.T) {
	t.Parallel()
	project := Canonicalize(t.TempDir())
	projectMCP := filepath.Join(project, "mcp.yaml")
	if !IsProjectScopedPath(projectMCP, project) {
		t.Fatal("project mcp should be scoped")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home")
	}
	global := filepath.Join(home, ".aicli", "mcp.yaml")
	if IsProjectScopedPath(global, project) {
		t.Fatal("global ~/.aicli mcp must not be project scoped")
	}
}

// ioDiscard is a tiny writer sink for prompt tests.
type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
