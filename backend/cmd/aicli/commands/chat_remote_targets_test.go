package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolregistry"
)

// writeRemoteTargetsFile creates the declaration the bridge reads.
func writeRemoteTargetsFile(t *testing.T, workspace, body string) {
	t.Helper()
	dir := filepath.Join(workspace, ".aicli")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "remote-targets.yaml"), []byte(body), 0o644))
}

// remoteToolInfo describes one tool served by a remote target as it reaches the
// execution policy: scope remote, identity from the server's own tool list.
func remoteToolInfo(server, callable, raw string) skill.ToolInfo {
	return skill.ToolInfo{
		Name:           callable,
		MCPName:        server,
		ExecutionScope: "remote",
		ExecutionMode:  "remote_mcp",
		MCPTrustLevel:  "trusted_remote",
		Metadata: map[string]interface{}{
			"mcp_name":          server,
			"mcp_raw_tool_name": raw,
		},
	}
}

// TestRemoteToolsAreGovernedLikeLocalOnes replaces the removed per-target grant
// tests with the rule they were standing in for:
//
//	a remote tool is judged by its own attributes and the session mode,
//	exactly like the local tool it mirrors.
//
// For every interesting verdict, the remote spelling (`remote_wsl__write`, with
// the server-declared raw name in metadata) must agree with the local spelling
// (`write`). If they ever diverge, the mode is no longer the single answer and
// somebody is about to ask for a second permission layer.
func TestRemoteToolsAreGovernedLikeLocalOnes(t *testing.T) {
	resolver := runtimepolicy.DefaultCapabilityResolver{}

	cases := []struct {
		raw      string
		write    bool // classify as write-like
		shell    bool // needs exec_shell
		background bool
	}{
		{raw: "view", write: false},
		{raw: "grep", write: false},
		{raw: "write", write: true},
		{raw: "edit", write: true},
		{raw: "shell", shell: true},
		{raw: "job_start", background: true},
		{raw: "job_input", background: true},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			remote := remoteToolInfo("wsl", "remote_wsl__"+tc.raw, tc.raw)
			remoteCaps := resolver.Resolve(runtimepolicy.EvalRequest{ToolName: remote.Name, ToolInfo: &remote})
			localCaps := resolver.Resolve(runtimepolicy.EvalRequest{ToolName: tc.raw})
			require.ElementsMatch(t, localCaps, remoteCaps,
				"remote %s must classify exactly like local %s", remote.Name, tc.raw)

			if tc.write {
				require.True(t, toolregistry.IsWriteLike(tc.raw), "fixture sanity")
			}
			if tc.background {
				require.Contains(t, remoteCaps, runtimepolicy.CapBackgroundTask,
					"a remote job tool must be gated as a background task, not as a read")
			}
		})
	}
}

// TestReadOnlySessionRefusesRemoteWriteLikeLocal proves the mode-level answer
// covers the remote surface with no extra configuration: a read-only policy
// refuses a remote write exactly like it refuses a local one.
func TestReadOnlySessionRefusesRemoteWriteLikeLocal(t *testing.T) {
	policy := runtimepolicy.NewToolExecutionPolicy(nil, true) // ReadOnly

	require.Error(t, policy.AllowToolInfo(remoteToolInfo("wsl", "remote_wsl__write", "write")),
		"a read-only session must refuse the remote write")
	require.Error(t, policy.AllowToolInfo(remoteToolInfo("local", "write", "write")))

	// Reads stay available on both sides.
	require.NoError(t, policy.AllowToolInfo(remoteToolInfo("wsl", "remote_wsl__view", "view")))
}

// TestDenyToolShutDownsOneRemoteTarget is the documented replacement for the old
// per-target switch: the tool surface itself. `--deny-tool "remote_prod_*"` uses
// the same wildcard matching every other name passes through, so "no prod writes"
// is a surface decision, not a second permission layer.
func TestDenyToolShutDownsOneRemoteTarget(t *testing.T) {
	policy := runtimepolicy.NewToolExecutionPolicy(nil, false)
	policy.AllowlistEnabled = false
	policy.DeniedTools = map[string]bool{"remote_prod_*": true}

	err := policy.AllowToolInfo(remoteToolInfo("prod", "remote_prod__write", "write"))
	require.Error(t, err, "the deny pattern must remove the target's write")
	require.Contains(t, err.Error(), "denied")

	// Another target is untouched by that pattern.
	require.NoError(t, policy.AllowToolInfo(remoteToolInfo("dev", "remote_dev__write", "write")))
}

// TestApplyRemoteTargetsToPolicyIsGone locks the shape of the new world: reading
// the declaration contributes servers only. There is no function left that
// writes a capability map into the session policy, because the policy answers
// that question through capabilities and modes.
func TestRemoteTargetsSessionLookupStillResolvesWarnings(t *testing.T) {
	workspace := t.TempDir()
	writeRemoteTargetsFile(t, workspace, `
remoteTargets:
  wsl:
    transport: http
    remote:
      listen: 127.0.0.1:18765
      workspace: /tmp/aicli-rt/ws
      toolPrefix: remote_wsl
    policy:
      allowWrite: true
`)
	declaration, warnings := loadRemoteTargetsDeclaration(workspace)
	require.NotNil(t, declaration)
	require.Contains(t, declaration.Servers, "wsl")

	var reported bool
	for _, warning := range remoteTargetWarnings(warnings) {
		if warning != "" {
			reported = true
		}
	}
	require.True(t, reported, "the ignored policy block must surface as a warning")
}
