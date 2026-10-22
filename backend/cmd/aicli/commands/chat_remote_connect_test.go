package commands

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

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

func (m *statusProbeManager) LoadConfig(string) error             { return nil }
func (m *statusProbeManager) Start(context.Context) error         { return nil }
func (m *statusProbeManager) Stop() error                         { return nil }
func (m *statusProbeManager) ListTools() []*mcpregistry.ToolInfo  { return nil }
func (m *statusProbeManager) SetMCPEnabled(string, bool) error    { return nil }
func (m *statusProbeManager) ReloadConfig() error                 { return nil }
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
