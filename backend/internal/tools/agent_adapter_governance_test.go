package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	mcpregistry "github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// governanceMCPManager is a minimal MCP manager that reports one remote tool and
// the governance fields the adapter must project.
type governanceMCPManager struct {
	name           string
	toolName       string
	trustLevel     mcpconfig.MCPTrustLevel
	executionMode  string
	executionScope mcpconfig.ExecutionScope
}

func (m *governanceMCPManager) LoadConfig(string) error  { return nil }
func (m *governanceMCPManager) Start(context.Context) error { return nil }
func (m *governanceMCPManager) Stop() error              { return nil }
func (m *governanceMCPManager) ReloadConfig() error      { return nil }

func (m *governanceMCPManager) ListTools() []*mcpregistry.ToolInfo {
	return []*mcpregistry.ToolInfo{{
		MCPName: m.name,
		Enabled: true,
		Tool: &protocol.Tool{
			Name:        m.toolName,
			Description: "remote tool",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{"file_path": map[string]interface{}{"type": "string"}},
			},
		},
	}}
}

func (m *governanceMCPManager) FindTool(name string) (*mcpregistry.ToolInfo, error) {
	tools := m.ListTools()
	for _, info := range tools {
		if info.Tool.Name == name {
			return info, nil
		}
	}
	return nil, errToolNotFound(name)
}

func (m *governanceMCPManager) CallTool(context.Context, string, string, map[string]interface{}) (*protocol.CallToolResult, error) {
	return &protocol.CallToolResult{}, nil
}

func (m *governanceMCPManager) SetMCPEnabled(string, bool) error { return nil }

func (m *governanceMCPManager) GetMCPStatus(name string) (*mcpconfig.MCPStatus, error) {
	return &mcpconfig.MCPStatus{
		Name:           name,
		Type:           "stdio",
		TrustLevel:     m.trustLevel,
		ExecutionMode:  m.executionMode,
		ExecutionScope: m.executionScope,
		Enabled:        true,
	}, nil
}

func (m *governanceMCPManager) ListMCPs() []*mcpconfig.MCPStatus { return nil }

func (m *governanceMCPManager) ListResources(context.Context, string, *string) (*protocol.ListResourcesResult, error) {
	return &protocol.ListResourcesResult{}, nil
}

func (m *governanceMCPManager) ReadResource(context.Context, string, string) (*protocol.ReadResourceResult, error) {
	return &protocol.ReadResourceResult{}, nil
}

func newAdapterWithFakeMCP(t *testing.T, serverName, toolName string) *AgentAdapter {
	t.Helper()
	manager := &governanceMCPManager{
		name:           serverName,
		toolName:       toolName,
		trustLevel:     mcpconfig.MCPTrustLevelTrustedRemote,
		executionMode:  "local_mcp", // SSH-launched stdio reports local_mcp
		executionScope: mcpconfig.ExecutionScopeRemote,
	}
	return NewAgentAdapter(NewDefaultManager(manager))
}

// errToolNotFound keeps the fake manager free of a second error dependency.
func errToolNotFound(name string) error {
	return &toolNotFoundError{name: name}
}

type toolNotFoundError struct{ name string }

func (e *toolNotFoundError) Error() string { return "tool not found: " + e.name }

// TestAgentAdapterStampsMCPGovernanceAndFSOwner locks review B1/B2.
//
// The tools.Manager adapter is the projection used by aicli TUI and
// runtime-server (chat_actor_host.go / runtime-server main.go). It used to drop
// MCPTrustLevel, ExecutionMode and fs_owner, which meant:
//
//   - ToolExecutionPolicy could never see an untrusted/remote MCP tool, so
//     BlockUntrustedMCP / BlockRemoteWrites never fired;
//   - the local preflight stat'ed remote paths and the sandbox validated them
//     against the local workspace.
//
// Both failures are silent, so the contract is pinned here.
func TestAgentAdapterStampsMCPGovernanceAndFSOwner(t *testing.T) {
	adapter := newAdapterWithFakeMCP(t, "remote-dev", "remote_dev__view")

	list := adapter.ListTools()
	var found bool
	for _, info := range list {
		if info.Name != "remote_dev__view" {
			continue
		}
		found = true
		require.Equal(t, "remote-dev", info.MCPName)
		require.Equal(t, "trusted_remote", info.MCPTrustLevel, "MCPTrustLevel must be projected (B1)")
		require.Equal(t, "local_mcp", info.ExecutionMode, "ExecutionMode must be projected (B1)")
		require.Equal(t, "remote", info.ExecutionScope, "ExecutionScope must be projected (B6)")

		owner, ok := info.Metadata[runtimetypes.ToolMetadataFSOwnerKey].(string)
		require.True(t, ok, "fs_owner must be stamped on MCP tools (B2)")
		require.Equal(t, runtimetypes.ToolFSOwnerToolServer, owner)
	}
	require.True(t, found, "remote tool must be visible in the adapter surface")
}

// TestAgentAdapterFindToolStampsFSOwner covers the FindTool path, which the agent
// loop uses for preflight (a different entry point from ListTools).
func TestAgentAdapterFindToolStampsFSOwner(t *testing.T) {
	adapter := newAdapterWithFakeMCP(t, "remote-dev", "remote_dev__view")

	info, err := adapter.FindTool("remote_dev__view")
	require.NoError(t, err)
	require.Equal(t, "remote", info.ExecutionScope)
	require.Equal(t, "trusted_remote", info.MCPTrustLevel)

	owner, ok := info.Metadata[runtimetypes.ToolMetadataFSOwnerKey].(string)
	require.True(t, ok)
	require.Equal(t, runtimetypes.ToolFSOwnerToolServer, owner)
}