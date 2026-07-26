package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/foldertrust"
	"github.com/wwsheng009/ai-agent-runtime/internal/plugins"
)

func TestProjectScopeAllowedFeatureOff(t *testing.T) {
	t.Setenv(foldertrust.EnvFolderTrust, "")
	resetProcessFolderTrust()
	t.Cleanup(resetProcessFolderTrust)

	// Feature off preserves prior behavior: project scope always allowed.
	setProcessFolderTrust(foldertrust.Resolution{
		FeatureEnabled: false,
		Trusted:        false,
		Source:         "feature_off",
		WorkspaceKey:   "test-off",
	})
	if !projectScopeAllowed() {
		t.Fatal("feature off must allow project scope")
	}
}

func TestProjectScopeAllowedFeatureOnUntrusted(t *testing.T) {
	t.Setenv(foldertrust.EnvFolderTrust, "1")
	resetProcessFolderTrust()
	t.Cleanup(resetProcessFolderTrust)

	setProcessFolderTrust(foldertrust.Resolution{
		FeatureEnabled: true,
		Trusted:        false,
		Outcome:        foldertrust.OutcomeUntrusted,
		Source:         "headless_deny",
		WorkspaceKey:   "test-untrusted",
	})
	if projectScopeAllowed() {
		t.Fatal("feature on + untrusted must deny project scope")
	}
}

func TestProjectScopeAllowedFeatureOnTrusted(t *testing.T) {
	t.Setenv(foldertrust.EnvFolderTrust, "1")
	resetProcessFolderTrust()
	t.Cleanup(resetProcessFolderTrust)

	setProcessFolderTrust(foldertrust.Resolution{
		FeatureEnabled: true,
		Trusted:        true,
		Outcome:        foldertrust.OutcomeTrusted,
		Source:         "grant",
		WorkspaceKey:   "test-trusted",
	})
	if !projectScopeAllowed() {
		t.Fatal("feature on + trusted must allow project scope")
	}
}

func TestDiscoverRuntimePluginCatalogSkipsProjectRootWhenUntrusted(t *testing.T) {
	ClearPluginCatalogCache()
	t.Cleanup(ClearPluginCatalogCache)

	t.Setenv(foldertrust.EnvFolderTrust, "1")
	resetProcessFolderTrust()
	t.Cleanup(resetProcessFolderTrust)

	home := filepath.Join(t.TempDir(), "aicli-home")
	t.Setenv("AICLI_HOME", home)

	project := t.TempDir()
	pluginParent := filepath.Join(project, ".aicli", "plugins")
	if err := os.MkdirAll(pluginParent, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = writeCLISamplePlugin(t, pluginParent, "proj-plugin")

	origWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(project); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	// Untrusted: project plugins must be skipped.
	setProcessFolderTrust(foldertrust.Resolution{
		FeatureEnabled: true,
		Trusted:        false,
		Outcome:        foldertrust.OutcomeUntrusted,
		Source:         "headless_deny",
		WorkspaceKey:   project,
		ProjectRoot:    project,
	})
	catalog, err := discoverRuntimePluginCatalog()
	if err != nil {
		t.Fatalf("discover untrusted: %v", err)
	}
	if catalog != nil {
		if _, ok := catalog.Get("proj-plugin"); ok {
			t.Fatal("untrusted must not discover project plugin")
		}
	}
	base := []string{t.TempDir()}
	merged := mergeActivePluginSkillDirs(base)
	if len(merged) != 1 || merged[0] != base[0] {
		t.Fatalf("untrusted must not merge project skills: %#v", merged)
	}

	// Trusted: project plugins are discovered (trust store may still mark untrusted for Active*).
	setProcessFolderTrust(foldertrust.Resolution{
		FeatureEnabled: true,
		Trusted:        true,
		Outcome:        foldertrust.OutcomeTrusted,
		Source:         "grant",
		WorkspaceKey:   project,
		ProjectRoot:    project,
	})
	catalog, err = discoverRuntimePluginCatalog()
	if err != nil {
		t.Fatalf("discover trusted: %v", err)
	}
	if catalog == nil {
		t.Fatal("expected catalog when trusted")
	}
	if _, ok := catalog.Get("proj-plugin"); !ok {
		t.Fatalf("trusted discover missing proj-plugin; order=%v", catalog.Order)
	}
}

func TestResolveChatMCPStartupConfigPathBlocksProjectWhenUntrusted(t *testing.T) {
	t.Setenv(foldertrust.EnvFolderTrust, "1")
	resetProcessFolderTrust()
	t.Cleanup(resetProcessFolderTrust)

	project := t.TempDir()
	projectMCP := filepath.Join(project, ".aicli", "mcp.yaml")
	if err := os.MkdirAll(filepath.Dir(projectMCP), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectMCP, []byte("servers: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	globalMCP := filepath.Join(home, ".aicli", "mcp.yaml")
	if err := os.MkdirAll(filepath.Dir(globalMCP), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(globalMCP, []byte("servers: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	session := &ChatSession{
		MCPConfigPath: projectMCP,
		FolderTrust: foldertrust.Resolution{
			FeatureEnabled: true,
			Trusted:        false,
			Outcome:        foldertrust.OutcomeUntrusted,
			Source:         "headless_deny",
			WorkspaceKey:   project,
			ProjectRoot:    project,
		},
	}
	setProcessFolderTrust(session.FolderTrust)

	if got, ok := resolveChatMCPStartupConfigPath(nil, session); ok || got != "" {
		t.Fatalf("project MCP must be blocked when untrusted, got %q ok=%v", got, ok)
	}

	// Path-level contract: global under ~/.aicli is never project-scoped.
	if foldertrust.IsProjectScopedPath(globalMCP, project) {
		t.Fatalf("global path incorrectly project-scoped: %s under %s", globalMCP, project)
	}
	if !foldertrust.IsProjectScopedPath(projectMCP, project) {
		t.Fatalf("project MCP should be project-scoped: %s", projectMCP)
	}

	// Global path remains allowed through the startup resolver.
	session.MCPConfigPath = globalMCP
	got, ok := resolveChatMCPStartupConfigPath(nil, session)
	if !ok || got != globalMCP {
		t.Fatalf("expected global MCP allowed, got %q ok=%v", got, ok)
	}
}

func TestEnsureProcessFolderTrustIdempotent(t *testing.T) {
	resetProcessFolderTrust()
	t.Cleanup(resetProcessFolderTrust)
	t.Setenv(foldertrust.EnvFolderTrust, "")

	first := ensureProcessFolderTrust(false, false)
	second := ensureProcessFolderTrust(true, true) // grant/interactive ignored after ready
	if first.Source != second.Source || first.Trusted != second.Trusted {
		t.Fatalf("idempotent resolve changed: first=%+v second=%+v", first, second)
	}
	if !processFolderTrustReady {
		t.Fatal("expected process trust ready after ensure")
	}
}

func TestSessionProjectScopeAllowedUsesSession(t *testing.T) {
	resetProcessFolderTrust()
	t.Cleanup(resetProcessFolderTrust)

	// Process says trusted, session says untrusted with feature on.
	setProcessFolderTrust(foldertrust.Resolution{
		FeatureEnabled: true,
		Trusted:        true,
		WorkspaceKey:   "proc",
	})
	session := &ChatSession{
		FolderTrust: foldertrust.Resolution{
			FeatureEnabled: true,
			Trusted:        false,
			WorkspaceKey:   "sess",
			ProjectRoot:    t.TempDir(),
		},
	}
	if sessionProjectScopeAllowed(session) {
		t.Fatal("session untrusted should win over process trusted")
	}
	if !projectScopeAllowed() {
		t.Fatal("process still trusted")
	}
}

func TestHandleTrustCommandFeatureOff(t *testing.T) {
	t.Setenv(foldertrust.EnvFolderTrust, "")
	resetProcessFolderTrust()
	t.Cleanup(resetProcessFolderTrust)

	session := &ChatSession{
		FolderTrust: foldertrust.Resolution{
			FeatureEnabled: false,
			Trusted:        true,
			Source:         "feature_off",
			WorkspaceKey:   "x",
		},
	}
	// Should not error; prints status / disabled hint.
	if handleTrustCommand(session, "/trust grant") {
		t.Fatal("handleTrustCommand should return false (not exit chat)")
	}
}

func TestPluginsStateFileNameStable(t *testing.T) {
	if plugins.StateFileName == "" {
		t.Fatal("plugins.StateFileName empty")
	}
}
