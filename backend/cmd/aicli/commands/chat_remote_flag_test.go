package commands

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/remotetargets"
)

// TestBuildRemoteFlagTargetIsOptional proves the flag is opt-in: an empty value
// yields (nil, nil) so every caller can treat "flag absent" without a special case.
func TestBuildRemoteFlagTargetIsOptional(t *testing.T) {
	for _, value := range []string{"", "   "} {
		target, err := buildRemoteFlagTarget(value)
		require.NoError(t, err)
		require.Nil(t, target)
	}
}

// TestBuildRemoteFlagTargetDerivesEverything is the core promise of the flag: the
// user supplies an endpoint and nothing else.
//
// Tool prefix, transport, execution scope and the capability grant are all
// derived, so there is no second file to keep in sync.
func TestBuildRemoteFlagTargetDerivesEverything(t *testing.T) {
	target, err := buildRemoteFlagTarget("http://10.0.0.9:18765#ops")
	require.NoError(t, err)
	require.NotNil(t, target)

	require.Equal(t, "10-0-0-9", target.ServerName)
	require.Equal(t, "remote_10-0-0-9", target.Prefix)
	require.Equal(t, "streamable", target.Config.Type)
	require.Equal(t, "http://10.0.0.9:18765/mcp", target.Config.URL)
	require.Equal(t, mcpconfig.ExecutionScopeRemote, target.Config.ExecutionScope)
	require.Equal(t, mcpconfig.MCPTrustLevelTrustedRemote, target.Config.TrustLevel)
	require.True(t, target.Config.Enabled)

	// `#ops` is carried as the requested depth; it becomes the target's profile,
	// not a local capability grant.
	require.Equal(t, remotetargets.DepthOps, target.Depth)
}

// TestBuildRemoteFlagTargetDepthLadder pins the parsed ladder.
//
// The depth is a PROFILE REQUEST, not a permission: it reaches the server as its
// profile (ssh-stdio) or is reported as not applicable (http). Asserting on the
// parsed value keeps the surface honest about what `#ops` can and cannot do here.
func TestBuildRemoteFlagTargetDepthLadder(t *testing.T) {
	cases := map[string]remotetargets.SpecDepth{
		"":          remotetargets.DepthReadonly,
		"#readonly": remotetargets.DepthReadonly,
		"#standard": remotetargets.DepthStandard,
		"#ops":      remotetargets.DepthOps,
	}
	for suffix, want := range cases {
		t.Run("depth"+suffix, func(t *testing.T) {
			target, err := buildRemoteFlagTarget("http://h:18765" + suffix)
			require.NoError(t, err)
			require.Equal(t, want, target.Depth)
		})
	}
}

// TestBuildRemoteFlagTargetAcceptsSSH covers the ssh:// form and its derived
// stdio command, which is what lets the flag work on a host with no listening
// port.
func TestBuildRemoteFlagTargetAcceptsSSH(t *testing.T) {
	target, err := buildRemoteFlagTarget("ssh://ops@10.0.0.9:2222/srv/app#standard")
	require.NoError(t, err)

	require.Equal(t, "stdio", target.Config.Type)
	require.Equal(t, "ssh-client", target.Config.Command)
	require.Contains(t, target.Config.Args, "--stdio")
	require.Contains(t, target.Config.Args, "ops@10.0.0.9")
	require.Equal(t, remotetargets.DepthStandard, target.Depth)
}

// TestBuildRemoteFlagTargetRejectsBadSpec keeps malformed input failing at startup
// rather than producing a session with no remote tools and no explanation.
func TestBuildRemoteFlagTargetRejectsBadSpec(t *testing.T) {
	for _, bad := range []string{
		"10.0.0.9:18765",     // no scheme
		"ftp://host",         // wrong scheme
		"http://",            // no host
		"ssh://",             // no host
		"http://h#bogus",     // unknown depth
	} {
		_, err := buildRemoteFlagTarget(bad)
		require.Error(t, err, "%q must be rejected", bad)
	}
}

// TestBuildRemoteFlagTargetKeepsDepthWarningOnHTTP locks the honesty rule for
// `#depth` on an already-running target.
//
// The depth is a profile REQUEST. ssh-stdio can honour it (the server starts with
// that profile); an http target is already running, so the local side cannot
// narrow it. That must surface as a warning, not as silence: the previous
// iteration of this feature already shipped one "the flag looks applied but is
// not" bug (the tool-prefix guess), and repeating it here would be the same lie.
func TestBuildRemoteFlagTargetKeepsDepthWarningOnHTTP(t *testing.T) {
	target, err := buildRemoteFlagTarget("http://h:18765#standard")
	require.NoError(t, err)
	require.NotEmpty(t, target.Warnings, "an http depth request must be reported as not applied")
	var found bool
	for _, warning := range target.Warnings {
		if strings.Contains(warning, "not applied to an http target") {
			found = true
		}
	}
	require.True(t, found, "warning must say the profile is not applied, got %v", target.Warnings)

	// ssh-stdio really does apply it, so the same suffix must not warn about it.
	sshTarget, err := buildRemoteFlagTarget("ssh://ops@h#standard")
	require.NoError(t, err)
	for _, warning := range sshTarget.Warnings {
		require.NotContains(t, warning, "not applied to an http target")
	}
}

// TestApplyRemoteFlagTargetIsAdditive is the requirement that separates this
// feature from an exclusive mode: the flag must COMPOSE with the existing MCP
// config, never replace its servers.
func TestApplyRemoteFlagTargetIsAdditive(t *testing.T) {
	cfg := &mcpconfig.Config{MCPServers: map[string]mcpconfig.MCPConfig{
		"github": {Name: "github", Type: "streamable", URL: "https://example/mcp", Enabled: true},
	}}
	target, err := buildRemoteFlagTarget("http://10.0.0.9:18765#standard")
	require.NoError(t, err)

	added, replaced := applyRemoteFlagTarget(cfg, target)
	require.True(t, added)
	require.False(t, replaced)

	require.Contains(t, cfg.MCPServers, "github", "the pre-existing server must survive")
	require.Contains(t, cfg.MCPServers, target.ServerName)
	require.Len(t, cfg.MCPServers, 2)
}

// TestApplyRemoteFlagTargetOverridesSameName documents the one collision case: a
// target of the same host already declared in mcp.yaml. The command-line flag
// wins, because it is the more specific, more recent statement of intent.
func TestApplyRemoteFlagTargetOverridesSameName(t *testing.T) {
	target, err := buildRemoteFlagTarget("http://10.0.0.9:18765#standard")
	require.NoError(t, err)

	cfg := &mcpconfig.Config{MCPServers: map[string]mcpconfig.MCPConfig{
		target.ServerName: {Name: target.ServerName, Type: "streamable", URL: "http://stale/mcp", Enabled: false},
	}}
	added, replaced := applyRemoteFlagTarget(cfg, target)
	require.False(t, added)
	require.True(t, replaced)
	require.Equal(t, target.Config.URL, cfg.MCPServers[target.ServerName].URL,
		"the flag's endpoint must win over the stale file entry")
}

// TestApplyRemoteFlagTargetHandlesNil keeps the no-flag path a true no-op.
func TestApplyRemoteFlagTargetHandlesNil(t *testing.T) {
	cfg := &mcpconfig.Config{MCPServers: map[string]mcpconfig.MCPConfig{"x": {Name: "x"}}}
	added, replaced := applyRemoteFlagTarget(cfg, nil)
	require.False(t, added)
	require.False(t, replaced)
	require.Len(t, cfg.MCPServers, 1)
}

// TestRemoteFlagNeedsNoConfigFile locks the headline requirement: the synthesized
// server works with an EMPTY base config, which is the "no local mcp.yaml" case.
func TestRemoteFlagNeedsNoConfigFile(t *testing.T) {
	target, err := buildRemoteFlagTarget("http://10.0.0.9:18765#readonly")
	require.NoError(t, err)

	base := &mcpconfig.Config{MCPServers: map[string]mcpconfig.MCPConfig{}}
	added, _ := applyRemoteFlagTarget(base, target)
	require.True(t, added)
	require.Len(t, base.MCPServers, 1, "the flag alone is enough to populate the MCP surface")
}
