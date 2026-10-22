package commands

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
)

// declarationFixture declares two connectable targets, one of them still carrying
// the legacy `policy:` block so the deprecation path is exercised end to end.
//
// The block gates nothing any more: a remote tool is classified by its own
// attributes and gated by the session permission mode like a local one. It stays
// in the fixture to prove the file still loads AND the dead keys are reported.
const declarationFixture = `
remoteTargets:
  wsl:
    transport: http
    remote:
      listen: 127.0.0.1:18765
      workspace: /tmp/aicli-rt/ws
      toolPrefix: remote_wsl
    policy:
      allowRead: true
      allowWrite: true
      allowShell: true
  prod:
    transport: ssh-stdio
    ssh:
      host: 10.0.0.9
      user: ops
    remote:
      workspace: /srv/app
      toolPrefix: remote_prod
`

// TestLoadRemoteTargetsDeclarationExposesServers locks gap G7: the declaration
// must produce connectable MCP entries, not just an authorization table. Before
// the fix `Expanded.Servers` had no production consumer, so every target still
// needed a duplicate mcp.yaml entry.
func TestLoadRemoteTargetsDeclarationExposesServers(t *testing.T) {
	workspace := t.TempDir()
	writeRemoteTargetsFile(t, workspace, declarationFixture)

	declaration, warnings := loadRemoteTargetsDeclaration(workspace)
	require.NotNil(t, declaration)
	for _, warning := range warnings {
		require.NotContains(t, warning, "unusable name")
	}

	require.Contains(t, declaration.Servers, "wsl")
	require.Contains(t, declaration.Servers, "prod")
	require.Equal(t, "streamable", declaration.Servers["wsl"].Type)
	require.Contains(t, declaration.Servers["wsl"].URL, "127.0.0.1:18765")
	require.Equal(t, mcpconfig.ExecutionScopeRemote, declaration.Servers["wsl"].ExecutionScope)

	// The SSH-stdio entry carries its tool prefix in the base64 config payload,
	// because the target-side server is the one that applies it.
	require.Equal(t, "stdio", declaration.Servers["prod"].Type)
	require.Equal(t, "ssh-client", declaration.Servers["prod"].Command)
	require.True(t, declarationServerMentionsPrefix(t, declaration, "prod", "remote_prod"),
		"the ssh-stdio entry must carry the declared tool prefix")
}

// declarationServerMentionsPrefix reports whether the prefix appears in the
// entry's args, decoding the base64 config payload when present.
//
// The prefix is applied by the target-side server, so it travels inside the
// `--config-b64` argument rather than as a top-level field. A plain substring
// search over the args would never see it.
func declarationServerMentionsPrefix(t *testing.T, declaration *remoteTargetsDeclaration, server, prefix string) bool {
	t.Helper()
	if declaration == nil {
		return false
	}
	entry, ok := declaration.Servers[server]
	if !ok {
		return false
	}
	for _, arg := range entry.Args {
		if strings.Contains(arg, prefix) {
			return true
		}
		if decoded, err := base64.StdEncoding.DecodeString(arg); err == nil && strings.Contains(string(decoded), prefix) {
			return true
		}
	}
	return false
}

// TestApplyRemoteTargetsServersAddsServers proves the generated entries reach the
// MCP snapshot, so a declaration alone is enough to connect a target.
func TestApplyRemoteTargetsServersAddsServers(t *testing.T) {
	workspace := t.TempDir()
	writeRemoteTargetsFile(t, workspace, declarationFixture)
	declaration, _ := loadRemoteTargetsDeclaration(workspace)

	cfg := &mcpconfig.Config{MCPServers: map[string]mcpconfig.MCPConfig{
		"existing": {Name: "existing", Type: "stdio"},
	}}
	require.NoError(t, applyRemoteTargetsServers(cfg, declaration))

	require.Contains(t, cfg.MCPServers, "existing", "unrelated servers must survive")
	require.Contains(t, cfg.MCPServers, "wsl")
	require.Contains(t, cfg.MCPServers, "prod")
	require.Equal(t, "streamable", cfg.MCPServers["wsl"].Type)
}

// TestApplyRemoteTargetsServersRejectsShadowing locks the conflict rule: a
// declaration and an mcp.yaml entry with the same name is an error, because
// silently preferring one leaves the other looking effective while being dead.
func TestApplyRemoteTargetsServersRejectsShadowing(t *testing.T) {
	workspace := t.TempDir()
	writeRemoteTargetsFile(t, workspace, declarationFixture)
	declaration, _ := loadRemoteTargetsDeclaration(workspace)

	cfg := &mcpconfig.Config{MCPServers: map[string]mcpconfig.MCPConfig{
		"wsl": {Name: "wsl", Type: "streamable", URL: "http://handwritten/mcp"},
	}}
	err := applyRemoteTargetsServers(cfg, declaration)
	require.Error(t, err)
	require.Contains(t, err.Error(), "wsl")
	require.Contains(t, err.Error(), "冲突")
	// The hand-written entry must not be overwritten by the failed merge.
	require.Equal(t, "http://handwritten/mcp", cfg.MCPServers["wsl"].URL)
}

// TestApplyRemoteTargetsServersNoDeclaration is the common case: no file, no work.
func TestApplyRemoteTargetsServersNoDeclaration(t *testing.T) {
	cfg := &mcpconfig.Config{MCPServers: map[string]mcpconfig.MCPConfig{"a": {Name: "a"}}}
	require.NoError(t, applyRemoteTargetsServers(cfg, nil))
	require.Len(t, cfg.MCPServers, 1)
}

// TestLoadRemoteTargetsDeclarationReportsDeprecatedPolicy proves a file written
// against the removed grant layer still connects AND says out loud that the block
// no longer gates anything. Silently dropping it would leave the reader believing
// `allowWrite: false` was enforcing something.
func TestLoadRemoteTargetsDeclarationReportsDeprecatedPolicy(t *testing.T) {
	workspace := t.TempDir()
	writeRemoteTargetsFile(t, workspace, declarationFixture)

	declaration, warnings := loadRemoteTargetsDeclaration(workspace)
	require.NotNil(t, declaration, "a legacy policy block must not break the declaration")

	var reported bool
	for _, warning := range warnings {
		if strings.Contains(warning, "no longer gates") {
			reported = true
			require.Contains(t, warning, "allowWrite", "the warning must name the ignored key")
		}
	}
	require.True(t, reported, "expected a deprecation warning, got %v", warnings)
}

// TestLoadRemoteTargetsDeclarationWithoutFile pins the no-op default: a workspace
// with no declaration contributes nothing.
func TestLoadRemoteTargetsDeclarationWithoutFile(t *testing.T) {
	declaration, warnings := loadRemoteTargetsDeclaration(t.TempDir())
	require.Nil(t, declaration)
	require.Empty(t, warnings)
}

// TestSessionHasDeclaredRemoteTargets locks the fail-closed trigger used by the
// startup path: a declared target is something the workspace asked for, so an
// unreachable one must abort rather than degrade to a warning.
func TestSessionHasDeclaredRemoteTargets(t *testing.T) {
	workspace := t.TempDir()
	writeRemoteTargetsFile(t, workspace, declarationFixture)

	session := &ChatSession{}
	session.FolderTrust.ProjectRoot = workspace
	require.True(t, sessionHasDeclaredRemoteTargets(session))

	empty := &ChatSession{}
	empty.FolderTrust.ProjectRoot = t.TempDir()
	require.False(t, sessionHasDeclaredRemoteTargets(empty))

	require.False(t, sessionHasDeclaredRemoteTargets(nil), "a nil session declares nothing")
}
