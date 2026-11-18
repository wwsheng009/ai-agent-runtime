package runtimeserver

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/api/runtimeapi"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

func isolateWorkspaceShellHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	previous := agentconfig.UserHomeDirForTest()
	agentconfig.SetUserHomeDirForTest(func() (string, error) { return home, nil })
	t.Cleanup(func() { agentconfig.SetUserHomeDirForTest(previous) })
	return home
}

func writeWorkspaceShellConfig(t *testing.T, workspace, body string) string {
	t.Helper()
	path := filepath.Join(workspace, ".aicli", "runtime.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}

func requireWindowsShells(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("workspace shell fixture uses cmd/powershell")
	}
}

func TestWorkspaceShellResolverUsesWorkspaceConfigAndInvalidates(t *testing.T) {
	requireWindowsShells(t)
	isolateWorkspaceShellHome(t)

	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	pathA := writeWorkspaceShellConfig(t, workspaceA, "shell:\n  prefer: [cmd]\n")
	writeWorkspaceShellConfig(t, workspaceB, "shell:\n  prefer: [powershell]\n")

	resolver := NewWorkspaceShellResolver(nil, "", "", func(string) bool { return true })
	runtimeA, sourceA, err := resolver.Resolve(workspaceA, "")
	require.NoError(t, err)
	require.Equal(t, runtimeexecutor.ShellIDCmd, runtimeA.Shell.ID)
	require.Equal(t, filepath.Clean(pathA), filepath.Clean(sourceA))

	runtimeB, _, err := resolver.Resolve(workspaceB, "")
	require.NoError(t, err)
	require.Equal(t, runtimeexecutor.ShellIDPowerShell, runtimeB.Shell.ID)

	// Same workspace, changed project file -> fingerprint invalidates the cache.
	require.NoError(t, os.WriteFile(pathA, []byte("shell:\n  prefer: [powershell]\n"), 0o644))
	future := time.Now().Add(2 * time.Second)
	require.NoError(t, os.Chtimes(pathA, future, future))
	runtimeA2, _, err := resolver.Resolve(workspaceA, "")
	require.NoError(t, err)
	require.Equal(t, runtimeexecutor.ShellIDPowerShell, runtimeA2.Shell.ID)
}

func TestWorkspaceShellResolverTrustGateAndExplicitOverride(t *testing.T) {
	requireWindowsShells(t)
	home := isolateWorkspaceShellHome(t)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".aicli"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(home, ".aicli", "runtime.yaml"),
		[]byte("shell:\n  prefer: [cmd]\n"),
		0o644,
	))

	workspace := t.TempDir()
	projectPath := writeWorkspaceShellConfig(t, workspace, "shell:\n  prefer: [powershell]\n")

	trustedResolver := NewWorkspaceShellResolver(nil, filepath.Join(home, ".aicli", "runtime.yaml"), "", func(string) bool { return true })
	trusted, _, err := trustedResolver.Resolve(workspace, "")
	require.NoError(t, err)
	require.Equal(t, runtimeexecutor.ShellIDPowerShell, trusted.Shell.ID)

	untrustedResolver := NewWorkspaceShellResolver(nil, filepath.Join(home, ".aicli", "runtime.yaml"), "", func(string) bool { return false })
	untrusted, source, err := untrustedResolver.Resolve(workspace, "")
	require.NoError(t, err)
	require.Equal(t, runtimeexecutor.ShellIDCmd, untrusted.Shell.ID)
	require.NotEqual(t, filepath.Clean(projectPath), filepath.Clean(source))

	explicit := filepath.Join(t.TempDir(), "explicit-runtime.yaml")
	require.NoError(t, os.WriteFile(explicit, []byte("shell:\n  prefer: [powershell]\n"), 0o644))
	explicitResolver := NewWorkspaceShellResolver(
		nil,
		filepath.Join(home, ".aicli", "runtime.yaml"),
		explicit,
		func(string) bool { return true },
	)
	explicitRuntime, _, err := explicitResolver.Resolve(workspace, "")
	require.NoError(t, err)
	require.Equal(t, runtimeexecutor.ShellIDPowerShell, explicitRuntime.Shell.ID)
}

func TestWorkspaceShellResolverProfileConfigWins(t *testing.T) {
	requireWindowsShells(t)
	isolateWorkspaceShellHome(t)

	workspace := t.TempDir()
	writeWorkspaceShellConfig(t, workspace, "shell:\n  prefer: [cmd]\n")
	profileRuntime := filepath.Join(t.TempDir(), "profile-runtime.yaml")
	require.NoError(t, os.WriteFile(profileRuntime, []byte("shell:\n  prefer: [powershell]\n"), 0o644))

	resolver := NewWorkspaceShellResolver(nil, "", "", func(string) bool { return true })
	profileShell, source, err := resolver.Resolve(workspace, profileRuntime)
	require.NoError(t, err)
	require.Equal(t, runtimeexecutor.ShellIDPowerShell, profileShell.Shell.ID)
	require.Equal(t, filepath.Clean(profileRuntime), filepath.Clean(source))
}

func TestWorkspaceShellSettingsProviderAndPersister(t *testing.T) {
	requireWindowsShells(t)
	isolateWorkspaceShellHome(t)
	workspace := t.TempDir()
	resolver := NewWorkspaceShellResolver(nil, "", "", func(string) bool { return true })

	provider := NewWorkspaceRuntimeShellSettingsProvider(nil, resolver)
	cfg, target, err := provider(runtimeapi.ShellSettingsScope{WorkspacePath: workspace, Scope: "workspace"})
	require.NoError(t, err)
	require.Equal(t, runtimeexecutor.ShellConfig{}, cfg)
	require.Equal(t, filepath.Join(workspace, ".aicli", "runtime.yaml"), target)

	persister := NewWorkspaceLayeredRuntimeShellSettingsPersister(nil, resolver)
	written, err := persister(runtimeexecutor.ShellConfig{
		Prefer: []string{runtimeexecutor.ShellIDCmd},
	}, runtimeapi.ShellSettingsScope{WorkspacePath: workspace, Scope: "workspace"})
	require.NoError(t, err)
	require.Equal(t, target, written)
	raw, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Contains(t, string(raw), "- cmd")

	cfg, _, err = provider(runtimeapi.ShellSettingsScope{WorkspacePath: workspace, Scope: "workspace"})
	require.NoError(t, err)
	require.Equal(t, []string{runtimeexecutor.ShellIDCmd}, cfg.NormalizedPrefer())
}
