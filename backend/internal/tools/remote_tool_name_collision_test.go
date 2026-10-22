package tools

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
)

// TestLocalPriorityToolsCoversOnlyReadFamily documents the exact boundary of the
// "local wins" rule.
//
// tools.Manager iterates MCP tools FIRST and skips a remote entry only when
// shouldPreferLocalToolkit says the name belongs to the local read family. Every
// other name is therefore claimed by the remote server. This test pins the set so
// a future change to the protection list is deliberate, not silent drift.
func TestLocalPriorityToolsCoversOnlyReadFamily(t *testing.T) {
	for _, name := range []string{"ls", "glob", "grep", "view"} {
		_, ok := localToolkitPriorityTools[name]
		require.True(t, ok, "%s must stay locally preferred", name)
	}
	for _, name := range []string{
		"write", "edit", "apply_patch", "append_write", "shell",
		"job_start", "job_output", "artifact_read",
	} {
		_, ok := localToolkitPriorityTools[name]
		require.False(t, ok,
			"%s is NOT locally protected: an unprefixed remote tool with this name would win over the local one", name)
	}
}

// TestUnprefixedRemoteWriteClaimsTheName locks the concrete hazard: a remote
// server that forgot its tool prefix publishes `write`, and the local toolkit's
// `write` is dropped from the surfaced list because MCP is visited first.
//
// The model then calls `write` expecting the local filesystem and mutates the
// remote machine instead. Nothing errors — that is what makes it dangerous.
func TestUnprefixedRemoteWriteClaimsTheName(t *testing.T) {
	m := NewDefaultManager(newShadowManager("wsl", "write"))

	descriptors := m.ListTools()
	var writeDesc *ToolDescriptor
	for i := range descriptors {
		if descriptors[i].Name == "write" {
			writeDesc = &descriptors[i]
			break
		}
	}
	require.NotNil(t, writeDesc, "write must be present exactly once")

	// The surviving `write` must be the remote one: its metadata carries the MCP
	// identity, while a local toolkit entry has none.
	require.NotNil(t, writeDesc.Metadata, "the surviving write must carry MCP metadata")
	require.Equal(t, "wsl", writeDesc.Metadata["mcp_name"],
		"unprefixed remote write SHADOWS the local one; prefixing is the only protection")
}

// TestPrefixedRemoteWriteCoexists proves the mitigation: with a prefix both
// tools stay reachable and are distinguishable by name.
func TestPrefixedRemoteWriteCoexists(t *testing.T) {
	m := NewDefaultManager(newShadowManager("wsl", "remote_wsl__write"))

	byName := map[string]*ToolDescriptor{}
	for i, d := range m.ListTools() {
		byName[d.Name] = &m.ListTools()[i]
		_ = d
	}
	require.Contains(t, byName, "write", "local write must survive")
	require.Contains(t, byName, "remote_wsl__write", "remote write must be separately visible")
	// Local tools carry their own metadata (tool_kind etc.) but never an MCP
	// identity; that key is what distinguishes the two entries.
	require.NotContains(t, byName["write"].Metadata, "mcp_name",
		"the surviving local write must not carry a remote identity")
	require.Equal(t, "wsl", byName["remote_wsl__write"].Metadata["mcp_name"])
}

// TestUnprefixedRemoteReadIsInvisible is the mirror image: because ls/glob/grep/
// view are locally protected, an unprefixed remote read tool never reaches the
// model at all. A prefixed one does.
func TestUnprefixedRemoteReadIsInvisible(t *testing.T) {
	m := NewDefaultManager(newShadowManager("wsl", "view"))

	count := 0
	for _, d := range m.ListTools() {
		if d.Name == "view" {
			count++
		}
	}
	require.Equal(t, 1, count)
	require.NotContains(t, listingNames(m), "remote_wsl__view")

	prefixed := NewDefaultManager(newShadowManager("wsl", "remote_wsl__view"))
	names := listingNames(prefixed)
	require.Contains(t, names, "view", "local view stays")
	require.Contains(t, names, "remote_wsl__view", "prefixed remote view is reachable")
}

func listingNames(m *Manager) []string {
	names := make([]string, 0)
	for _, d := range m.ListTools() {
		names = append(names, d.Name)
	}
	return names
}

// shadowManager exposes exactly one MCP tool under a chosen name.
type shadowManager struct {
	stubMCPManager
	mcpName  string
	toolName string
}

func newShadowManager(mcpName, toolName string) *shadowManager {
	return &shadowManager{mcpName: mcpName, toolName: toolName}
}

func (m *shadowManager) ListTools() []*registry.ToolInfo {
	return []*registry.ToolInfo{{
		MCPName: m.mcpName,
		Enabled: true,
		Tool: &protocol.Tool{
			Name:        m.toolName,
			Description: "remote " + m.toolName,
			InputSchema: map[string]interface{}{"type": "object"},
		},
	}}
}

func (m *shadowManager) FindTool(name string) (*registry.ToolInfo, error) {
	for _, tool := range m.ListTools() {
		if tool.Tool != nil && tool.Tool.Name == name {
			return tool, nil
		}
	}
	return nil, fmt.Errorf("tool not found: %s", name)
}

func (m *shadowManager) CallTool(ctx context.Context, mcpName, toolName string, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return nil, fmt.Errorf("shadow manager must not execute %s", toolName)
}
