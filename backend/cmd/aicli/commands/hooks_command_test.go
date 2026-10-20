package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/foldertrust"
)

// writeHookFile drops a declarative hooks file into workspace/.aicli.
func writeHookFile(t *testing.T, workspace, body string) string {
	t.Helper()
	dir := filepath.Join(workspace, ".aicli")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, "hooks.json")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}

const shellHookBody = `{
  "hooks": {
    "PreToolUse": [
      {"matcher": "shell", "hooks": [{"type": "command", "command": "echo ok"}]}
    ]
  }
}`

// isTrustedWorkspace reports whether the trust store currently trusts dir.
func isTrustedWorkspace(t *testing.T, dir string) bool {
	t.Helper()
	return foldertrust.Load().IsTrusted(foldertrust.Canonicalize(foldertrust.WorkspaceKey(dir)))
}

// `--workspace` used to change only the hook file lookup while folder trust was
// still decided from the process cwd. Inspecting a trusted checkout from an
// untrusted cwd therefore reported "no hooks" for a workspace that clearly had
// them — and the reverse could load hooks from an untrusted tree.
func TestResolveHooksWorkspaceTrustUsesTargetWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AICLI_HOME", home)
	t.Setenv(foldertrust.EnvFolderTrust, "1")

	cwdTrusted := t.TempDir()
	targetUntrusted := t.TempDir()
	require.NoError(t, foldertrust.Load().SetTrusted(foldertrust.Canonicalize(foldertrust.WorkspaceKey(cwdTrusted))))
	// A workspace only needs a trust decision when it actually carries a
	// trust-sensitive config; an empty directory is trusted by design.
	writeHookFile(t, targetUntrusted, shellHookBody)

	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(cwdTrusted))
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	trust := resolveHooksWorkspaceTrust(targetUntrusted)
	require.True(t, trust.FeatureEnabled)
	require.False(t, trust.Trusted, "target workspace is untrusted even though cwd is trusted")
}

func TestHooksListSkipsProjectFilesWhenWorkspaceUntrusted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AICLI_HOME", home)
	t.Setenv(foldertrust.EnvFolderTrust, "1")

	cwdTrusted := t.TempDir()
	target := t.TempDir()
	require.NoError(t, foldertrust.Load().SetTrusted(foldertrust.Canonicalize(foldertrust.WorkspaceKey(cwdTrusted))))
	writeHookFile(t, target, shellHookBody)

	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(cwdTrusted))
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	result := collectHooksList(&hooksCommandOptions{Workspace: target})
	require.False(t, result.Trust.Trusted)
	require.Empty(t, result.Sources, "untrusted project scope must not be read")
	require.Zero(t, result.Count)
}

func TestHooksListLoadsProjectFilesWhenWorkspaceTrusted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AICLI_HOME", home)
	t.Setenv(foldertrust.EnvFolderTrust, "1")

	target := t.TempDir()
	require.NoError(t, foldertrust.Load().SetTrusted(foldertrust.Canonicalize(foldertrust.WorkspaceKey(target))))
	writeHookFile(t, target, shellHookBody)

	result := collectHooksList(&hooksCommandOptions{Workspace: target})
	require.True(t, result.Trust.Trusted)
	require.Len(t, result.Sources, 1)
	require.Equal(t, 1, result.Count)
}

// Dry-run must not start a process. The command writes a marker file; if the
// handler ran, the marker exists.
func TestHooksTestDryRunDoesNotExecuteHandlers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AICLI_HOME", home)
	t.Setenv(foldertrust.EnvFolderTrust, "0")

	workspace := t.TempDir()
	marker := filepath.Join(workspace, "executed.txt")
	body := `{
  "hooks": {
    "PreToolUse": [
      {"matcher": "shell", "hooks": [{"type": "command", "command": "echo ran > ` + strings.ReplaceAll(marker, `\`, `\\`) + `"}]}
    ]
  }
}`
	writeHookFile(t, workspace, body)

	result, _, err := runHooksTest(&hooksCommandOptions{Workspace: workspace}, "PreToolUse",
		"", `{"tool_name":"shell"}`, true)
	require.NoError(t, err)
	require.True(t, result.DryRun)
	require.Equal(t, 1, result.Matched)
	require.Empty(t, result.FailedHooks)
	_, statErr := os.Stat(marker)
	require.True(t, os.IsNotExist(statErr), "dry-run must not run the handler")
}

func TestDoctorHooksReportsHealth(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AICLI_HOME", home)
	t.Setenv(foldertrust.EnvFolderTrust, "0")

	workspace := t.TempDir()
	writeHookFile(t, workspace, shellHookBody)

	report := inspectDoctorHooks(&hooksCommandOptions{Workspace: workspace})
	require.Equal(t, "ok", report.Health)
	require.Equal(t, 1, report.HandlerCount)
	require.Equal(t, 1, report.HandlerTypes["shell"])
	require.Equal(t, 1, report.EventCounts["pre_tool_use"])
}

func TestDoctorHooksFlagsSkippedProjectScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AICLI_HOME", home)
	t.Setenv(foldertrust.EnvFolderTrust, "1")

	workspace := t.TempDir()
	writeHookFile(t, workspace, shellHookBody)

	report := inspectDoctorHooks(&hooksCommandOptions{Workspace: workspace})
	require.Equal(t, "warning", report.Health)
	require.Zero(t, report.HandlerCount)
	var found bool
	for _, finding := range report.Findings {
		if strings.Contains(finding.Message, "folder trust") {
			found = true
		}
	}
	require.True(t, found, "skipped project scope must be explained: %+v", report.Findings)
}

// The JSON contract is what scripts consume; a rename here silently breaks CI.
func TestDoctorHooksJSONShape(t *testing.T) {
	report := doctorHooksReport{
		Workspace:    "/tmp/repo",
		ProjectTrust: hooksTrustInfo{FeatureEnabled: false, Trusted: true},
		Health:       "ok",
		HandlerCount: 2,
	}
	raw, err := json.Marshal(report)
	require.NoError(t, err)
	for _, key := range []string{`"workspace"`, `"project_trust"`, `"health"`, `"handler_count"`} {
		require.Contains(t, string(raw), key)
	}
}
