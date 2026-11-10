package commands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	mcpregistry "github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
)

// statusProbeManager reports a scripted connection outcome for one server.
type statusProbeManager struct {
	serverName string
	connected  bool
	lastError  string
}

func (m *statusProbeManager) LoadConfig(string) error            { return nil }
func (m *statusProbeManager) Start(context.Context) error        { return nil }
func (m *statusProbeManager) Stop() error                        { return nil }
func (m *statusProbeManager) ListTools() []*mcpregistry.ToolInfo { return nil }
func (m *statusProbeManager) SetMCPEnabled(string, bool) error   { return nil }
func (m *statusProbeManager) ReloadConfig() error                { return nil }
func (m *statusProbeManager) ListMCPs() []*mcpconfig.MCPStatus {
	return []*mcpconfig.MCPStatus{{Name: m.serverName, Connected: m.connected}}
}

func (m *statusProbeManager) FindTool(string) (*mcpregistry.ToolInfo, error) {
	return nil, errors.New("not found")
}

func (m *statusProbeManager) CallTool(context.Context, string, string, map[string]interface{}) (*protocol.CallToolResult, error) {
	return nil, errors.New("not implemented")
}

func (m *statusProbeManager) ListResources(context.Context, string, *string) (*protocol.ListResourcesResult, error) {
	return &protocol.ListResourcesResult{}, nil
}

func (m *statusProbeManager) ReadResource(context.Context, string, string) (*protocol.ReadResourceResult, error) {
	return &protocol.ReadResourceResult{}, nil
}

func (m *statusProbeManager) GetMCPStatus(name string) (*mcpconfig.MCPStatus, error) {
	return &mcpconfig.MCPStatus{
		Name:      name,
		Connected: m.connected,
		LastError: m.lastError,
	}, nil
}

// TestVerifyRemoteFlagConnectedFailsFast is the regression test for the silent
// "flag did nothing" failure.
//
// Live repro before the fix: `aicli chat --remote http://127.0.0.1:18765` against a
// closed port printed the "added server" notice, connected to nothing, and the
// model answered normally with exit 0. The user had no signal that the remote
// surface was empty.
func TestVerifyRemoteFlagConnectedFailsFast(t *testing.T) {
	target := &remoteFlagTarget{ServerName: "wsl"}
	mgr := &statusProbeManager{
		serverName: "wsl",
		connected:  false,
		lastError:  "dial tcp 127.0.0.1:18765: connectex: No connection could be made",
	}

	err := verifyRemoteFlagConnected(mgr, target)
	require.Error(t, err, "an unreachable --remote endpoint must fail the command")
	require.Contains(t, err.Error(), "--remote")
	require.Contains(t, err.Error(), "No connection could be made")
}

// TestVerifyRemoteFlagConnectedAcceptsHealthyServer keeps the happy path silent.
func TestVerifyRemoteFlagConnectedAcceptsHealthyServer(t *testing.T) {
	target := &remoteFlagTarget{ServerName: "wsl"}
	mgr := &statusProbeManager{serverName: "wsl", connected: true}

	require.NoError(t, verifyRemoteFlagConnected(mgr, target))
}

// TestVerifyRemoteFlagConnectedWithoutErrorStillFails covers a connect that ends
// without ever reporting success or a reason: the command must still refuse to
// pretend the flag worked.
func TestVerifyRemoteFlagConnectedWithoutErrorStillFails(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the probe budget")
	}
	target := &remoteFlagTarget{ServerName: "wsl"}
	mgr := &statusProbeManager{serverName: "wsl", connected: false, lastError: ""}

	err := verifyRemoteFlagConnected(mgr, target)
	require.Error(t, err)
	require.Contains(t, err.Error(), "did not complete")
}

// TestVerifyRemoteFlagConnectedDefaultsToNil pins that a session WITHOUT the flag
// is never affected: no flag means no probe, so plain mcp.yaml sessions keep their
// existing lenient behaviour.
func TestVerifyRemoteFlagConnectedDefaultsToNil(t *testing.T) {
	require.NoError(t, verifyRemoteFlagConnected(&statusProbeManager{}, nil))
}

// TestChatMCPStartupFailsClosed pins the boundary between an interactive TUI
// and unattended runs: only the TUI may degrade an unreachable explicit target
// to a warning. A headless/JSON run must keep the fail-closed contract.
func TestChatMCPStartupFailsClosed(t *testing.T) {
	cases := []struct {
		name    string
		session *ChatSession
		want    bool
	}{
		{name: "nil-session", session: nil, want: true},
		{name: "headless", session: &ChatSession{NoInteractive: true}, want: true},
		{name: "json", session: &ChatSession{JSONOutput: true}, want: true},
		{name: "non-tui", session: &ChatSession{}, want: true},
		{name: "tui", session: &ChatSession{InteractiveUI: true}, want: false},
		{name: "tui-with-headless-flag", session: &ChatSession{InteractiveUI: true, NoInteractive: true}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, chatMCPStartupFailsClosed(tc.session))
		})
	}
}

// TestChatMCPInitFailureIsFatal locks the exact branch used by capability
// discovery: the same explicit target/declaration that is fatal headless must
// degrade to a warning in the TUI.
func TestChatMCPInitFailureIsFatal(t *testing.T) {
	target := &remoteFlagTarget{ServerName: "dead"}

	require.True(t, chatMCPInitFailureIsFatal(&ChatSession{RemoteFlagTarget: target}),
		"headless --remote failure must stay fatal")
	require.False(t, chatMCPInitFailureIsFatal(&ChatSession{InteractiveUI: true, RemoteFlagTarget: target}),
		"TUI --remote failure must degrade")
	require.False(t, chatMCPInitFailureIsFatal(&ChatSession{InteractiveUI: true}),
		"TUI without an explicit target never fails closed")
	require.False(t, chatMCPInitFailureIsFatal(&ChatSession{}),
		"headless mcp.yaml-only failure stays a warning")

	workspace := t.TempDir()
	writeRemoteTargetsFile(t, workspace, declarationFixture)
	tuiDeclaration := &ChatSession{InteractiveUI: true}
	tuiDeclaration.FolderTrust.ProjectRoot = workspace
	require.False(t, chatMCPInitFailureIsFatal(tuiDeclaration),
		"TUI declaration failure must degrade")

	headlessDeclaration := &ChatSession{}
	headlessDeclaration.FolderTrust.ProjectRoot = workspace
	require.True(t, chatMCPInitFailureIsFatal(headlessDeclaration),
		"headless declaration failure must stay fatal")
}

// TestPrepareChatMCPManager_TUIDoesNotWaitForUnreachableTarget is the regression
// guard for the reported TUI failure: a dead remote target used to make the
// background capability load fail after an 8s probe, which then surfaced as
// "能力面初始化失败" on every turn and blocked the session. In the TUI the
// manager must start asynchronously, report its own per-server failure, and let
// capability discovery finish immediately.
func TestPrepareChatMCPManager_TUIDoesNotWaitForUnreachableTarget(t *testing.T) {
	isolateProcessMCPManager(t)
	t.Cleanup(func() { _ = StopMCPManager() })

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("MCP_CONFIG_FILE", "")
	t.Setenv("AICLI_REMOTE_TOKEN", "test-token")

	dir := t.TempDir()
	configPath := filepath.Join(dir, "mcp.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("mcpServers: {}\n"), 0o600))

	target, err := buildRemoteFlagTarget("http://127.0.0.1:1")
	require.NoError(t, err)
	require.NotNil(t, target)

	session := &ChatSession{
		InteractiveUI:    true,
		RemoteFlagTarget: target,
	}
	cfg := &agentconfig.Config{
		AICLI: &agentconfig.AICLIConfig{
			MCP: &agentconfig.AICLIMCPConfig{ConfigFile: configPath},
		},
	}

	started := time.Now()
	err = prepareChatMCPManager(cfg, session)
	elapsed := time.Since(started)

	require.NoError(t, err, "an unreachable target must not fail TUI capability startup")
	require.Less(t, elapsed, 3*time.Second, "TUI startup must not run the 8s target probe")
	require.NotNil(t, MCPManagerInstance, "the manager should still start its background connects")
}
