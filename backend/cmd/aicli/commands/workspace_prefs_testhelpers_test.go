package commands

import (
	"errors"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// errWorkspacePrefsDisabledInTests is returned by the default TestMain
// workspace-cwd resolver so un-isolated tests observe an empty preference
// store instead of resolving a cwd-derived path.
var errWorkspacePrefsDisabledInTests = errors.New("workspace chat preferences disabled in tests")

// isolateWorkspacePrefsForTest redirects both $HOME (for the workspace
// preference directory) and the workspace cwd identity to a deterministic
// per-test location so preference reads/writes never touch the real user home
// nor depend on the process working directory.
func isolateWorkspacePrefsForTest(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	prevHome := agentconfig.UserHomeDirForTest()
	agentconfig.SetUserHomeDirForTest(func() (string, error) { return home, nil })
	t.Cleanup(func() { agentconfig.SetUserHomeDirForTest(prevHome) })

	prevCwd := agentconfig.WorkspaceCwdForTest()
	agentconfig.SetWorkspaceCwdForTest(func() (string, error) { return home, nil })
	t.Cleanup(func() { agentconfig.SetWorkspaceCwdForTest(prevCwd) })
}

// loadWorkspaceChatPrefsForTest loads the workspace preference struct the
// isolated test environment resolves to.
func loadWorkspaceChatPrefsForTest(t *testing.T) *agentconfig.AICLIChatConfig {
	t.Helper()
	prefs, err := agentconfig.LoadWorkspaceChatPreferences()
	if err != nil {
		t.Fatalf("load workspace chat preferences: %v", err)
	}
	return prefs
}
