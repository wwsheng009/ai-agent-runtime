package agentconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// withWorkspaceCwdForTest points WorkspacePrefsID at a fake cwd via the
// swappable workspaceCwd var (same pattern as userHomeDir in bootstrap.go).
func withWorkspaceCwdForTest(t *testing.T, cwd string) {
	t.Helper()
	prev := workspaceCwd
	t.Cleanup(func() {
		workspaceCwd = prev
	})
	workspaceCwd = func() (string, error) { return cwd, nil }
}

func stringPtrForTest(v string) *string { return &v }

func boolPtrForTest(v bool) *bool { return &v }

func boolPtrPtrForTest(v bool) **bool {
	inner := v
	innerPtr := &inner
	return &innerPtr
}

func sha256SumForTest(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}

func hexForTest(b []byte) string { return hex.EncodeToString(b) }

func TestWorkspacePrefsIDForPath(t *testing.T) {
	if got := WorkspacePrefsIDForCleanedPath(""); got != "" {
		t.Fatalf("empty path should yield empty id, got %q", got)
	}
	a := WorkspacePrefsIDForCleanedPath(`E:\work\proj-a`)
	b := WorkspacePrefsIDForCleanedPath(`E:\work\proj-b`)
	if a == b {
		t.Fatalf("different workspaces should hash differently: %q", a)
	}
	if len(a) != 16 {
		t.Fatalf("expected 16 hex chars (8 bytes), got %d in %q", len(a), a)
	}
	if again := WorkspacePrefsIDForCleanedPath(`E:\work\proj-a`); again != a {
		t.Fatalf("hashing should be deterministic: %q vs %q", a, again)
	}
}

func TestWorkspacePrefsIDMatchesHeaderTemplateProjectIDShape(t *testing.T) {
	cwd := `E:\work\shape-check`
	id := WorkspacePrefsIDForCleanedPath(cwd)
	// Mirrors providerops.HeaderTemplateProjectID: sha256(clean) first 8 bytes.
	sum := sha256SumForTest([]byte(filepath.Clean(cwd)))
	want := hexForTest(sum[:8])
	if id != want {
		t.Fatalf("workspace id %q does not match header-template hashing %q", id, want)
	}
}

func TestSaveAndLoadWorkspaceChatPreferences_RoundTrip(t *testing.T) {
	home := t.TempDir()
	cwd := `E:\work\roundtrip`
	withWorkspaceCwdForTest(t, cwd)
	prevHome := userHomeDir
	t.Cleanup(func() { userHomeDir = prevHome })
	userHomeDir = func() (string, error) { return home, nil }

	path := WorkspacePrefsPath()
	if path == "" {
		t.Fatalf("WorkspacePrefsPath should resolve under fake home")
	}
	if filepath.Base(filepath.Dir(path)) != WorkspacePrefsIDForCleanedPath(cwd) {
		t.Fatalf("preference path %s should live in hash dir for cwd", path)
	}
	err := SaveWorkspaceChatPreferences(AICLIChatPreferenceUpdate{
		DefaultProvider: stringPtrForTest("openrouter"),
		DefaultModel:    stringPtrForTest("anthropic/claude-sonnet-4"),
		ReasoningEffort: stringPtrForTest("high"),
		Stream:          boolPtrPtrForTest(true),
	})
	if err != nil {
		t.Fatalf("SaveWorkspaceChatPreferences: %v", err)
	}

	prefs, err := LoadWorkspaceChatPreferences()
	if err != nil {
		t.Fatalf("LoadWorkspaceChatPreferences: %v", err)
	}
	if prefs == nil {
		t.Fatalf("preferences should be loaded, got nil")
	}
	if prefs.DefaultProvider != "openrouter" {
		t.Fatalf("DefaultProvider = %q, want openrouter", prefs.DefaultProvider)
	}
	if prefs.DefaultModel != "anthropic/claude-sonnet-4" {
		t.Fatalf("DefaultModel = %q, want anthropic/claude-sonnet-4", prefs.DefaultModel)
	}
	if prefs.ReasoningEffort != "high" {
		t.Fatalf("ReasoningEffort = %q, want high", prefs.ReasoningEffort)
	}
	if prefs.Stream == nil || !*prefs.Stream {
		t.Fatalf("Stream = %v, want true", prefs.Stream)
	}

	// Second save merges without clobbering untouched keys.
	if err := SaveWorkspaceChatPreferences(AICLIChatPreferenceUpdate{
		DefaultModel: stringPtrForTest("anthropic/claude-opus-4"),
	}); err != nil {
		t.Fatalf("second SaveWorkspaceChatPreferences: %v", err)
	}
	prefs, err = LoadWorkspaceChatPreferences()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if prefs.DefaultProvider != "openrouter" {
		t.Fatalf("merge lost DefaultProvider: %q", prefs.DefaultProvider)
	}
	if prefs.DefaultModel != "anthropic/claude-opus-4" {
		t.Fatalf("merge did not update DefaultModel: %q", prefs.DefaultModel)
	}
	if prefs.ReasoningEffort != "high" {
		t.Fatalf("merge lost ReasoningEffort: %q", prefs.ReasoningEffort)
	}
}

func TestWorkspacePreferencesAreScopedPerWorkspace(t *testing.T) {
	home := t.TempDir()
	prevHome := userHomeDir
	t.Cleanup(func() { userHomeDir = prevHome })
	userHomeDir = func() (string, error) { return home, nil }

	withWorkspaceCwdForTest(t, `E:\work\alpha`)
	if err := SaveWorkspaceChatPreferences(AICLIChatPreferenceUpdate{
		DefaultProvider: stringPtrForTest("alpha-provider"),
	}); err != nil {
		t.Fatalf("save alpha: %v", err)
	}

	withWorkspaceCwdForTest(t, `E:\work\beta`)
	prefs, err := LoadWorkspaceChatPreferences()
	if err != nil {
		t.Fatalf("load beta: %v", err)
	}
	if prefs != nil && prefs.DefaultProvider != "" {
		t.Fatalf("beta should see no preferences, got %+v", prefs)
	}

	// Beta saves its own; alpha must stay intact.
	if err := SaveWorkspaceChatPreferences(AICLIChatPreferenceUpdate{
		DefaultProvider: stringPtrForTest("beta-provider"),
	}); err != nil {
		t.Fatalf("save beta: %v", err)
	}
	withWorkspaceCwdForTest(t, `E:\work\alpha`)
	prefs, err = LoadWorkspaceChatPreferences()
	if err != nil {
		t.Fatalf("reload alpha: %v", err)
	}
	if prefs == nil || prefs.DefaultProvider != "alpha-provider" {
		t.Fatalf("alpha preferences leaked or lost: %+v", prefs)
	}
}

func TestLoadWorkspaceChatPreferences_MissingFile(t *testing.T) {
	home := t.TempDir()
	prevHome := userHomeDir
	t.Cleanup(func() { userHomeDir = prevHome })
	userHomeDir = func() (string, error) { return home, nil }
	withWorkspaceCwdForTest(t, `E:\work\nothing-here`)

	prefs, err := LoadWorkspaceChatPreferences()
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if prefs != nil && (prefs.DefaultProvider != "" || prefs.DefaultModel != "") {
		t.Fatalf("missing file should yield empty prefs, got %+v", prefs)
	}
}

func TestClearWorkspaceChatPreferences(t *testing.T) {
	home := t.TempDir()
	prevHome := userHomeDir
	t.Cleanup(func() { userHomeDir = prevHome })
	userHomeDir = func() (string, error) { return home, nil }
	withWorkspaceCwdForTest(t, `E:\work\clearable`)

	if err := SaveWorkspaceChatPreferences(AICLIChatPreferenceUpdate{
		DefaultProvider: stringPtrForTest("p"),
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := ClearWorkspaceChatPreferences(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, err := os.Stat(WorkspacePrefsPath()); !os.IsNotExist(err) {
		t.Fatalf("preference file should be removed, stat err = %v", err)
	}
	// Clearing again is a no-op.
	if err := ClearWorkspaceChatPreferences(); err != nil {
		t.Fatalf("second clear should not error: %v", err)
	}
}
