package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
)

// shadowProbeManager exposes N remote tools under exactly the given names and
// records which ones were actually invoked.
//
// It deliberately does not embed stubMCPManager: that stub's FindTool only knows
// its own canned tools, so a lookup would fail and the call would silently fall
// through to the local implementation — which is the very behaviour under test.
type shadowProbeManager struct {
	mcpName  string
	tools    []string
	executed []string
}

func newShadowProbeManager(tools ...string) *shadowProbeManager {
	return &shadowProbeManager{mcpName: "wsl", tools: tools}
}

// serverName returns the configured server id, defaulting to wsl.
func (m *shadowProbeManager) serverName() string {
	if m.mcpName == "" {
		return "wsl"
	}
	return m.mcpName
}

func (m *shadowProbeManager) LoadConfig(string) error    { return nil }
func (m *shadowProbeManager) Start(context.Context) error { return nil }
func (m *shadowProbeManager) Stop() error                 { return nil }
func (m *shadowProbeManager) SetMCPEnabled(string, bool) error {
	return nil
}
func (m *shadowProbeManager) ReloadConfig() error { return nil }

func (m *shadowProbeManager) ListResources(context.Context, string, *string) (*protocol.ListResourcesResult, error) {
	return &protocol.ListResourcesResult{}, nil
}

func (m *shadowProbeManager) ReadResource(context.Context, string, string) (*protocol.ReadResourceResult, error) {
	return &protocol.ReadResourceResult{}, nil
}

func (m *shadowProbeManager) GetMCPStatus(name string) (*mcpconfig.MCPStatus, error) {
	return &mcpconfig.MCPStatus{Name: name}, nil
}

func (m *shadowProbeManager) ListMCPs() []*mcpconfig.MCPStatus {
	return []*mcpconfig.MCPStatus{{Name: m.mcpName}}
}

func (m *shadowProbeManager) ListTools() []*registry.ToolInfo {
	infos := make([]*registry.ToolInfo, 0, len(m.tools))
	for _, name := range m.tools {
		infos = append(infos, &registry.ToolInfo{
			MCPName: m.serverName(),
			Enabled: true,
			Tool: &protocol.Tool{
				Name:        name,
				Description: "remote " + name,
				InputSchema: map[string]interface{}{"type": "object"},
			},
		})
	}
	return infos
}

func (m *shadowProbeManager) FindTool(name string) (*registry.ToolInfo, error) {
	for _, info := range m.ListTools() {
		if info.Tool != nil && info.Tool.Name == name {
			return info, nil
		}
	}
	return nil, errors.New("not found: " + name)
}

func (m *shadowProbeManager) CallTool(_ context.Context, _ string, toolName string, _ map[string]interface{}) (*protocol.CallToolResult, error) {
	m.executed = append(m.executed, toolName)
	return &protocol.CallToolResult{
		Content: []protocol.Content{{Type: "text", Text: "REMOTE:" + toolName}},
	}, nil
}

// TestSameNameSurfaceAndExecutionAgree locks the fix for the split brain that
// used to exist between listing and execution.
//
// Before the fix: ListTools let a non-read MCP entry claim the name (so `write`
// was shown with an MCP identity), while executeWithMeta short-circuited to the
// LOCAL implementation because shouldPreferLocalToolkitTool hard-coded the server
// name to "toolkit". The model was told it called a remote tool and the local
// disk was mutated.
//
// After the fix: only the read family short-circuits locally, so a non-read name
// that the MCP server owns is dispatched to that server — matching what the
// listing said.
//
// Note: with a REMOTE-scoped server such a name is now refused at load time by
// validateRemoteToolNameCollision, so this test uses a local-scoped server to
// exercise the dispatch path itself.
func TestSameNameSurfaceAndExecutionAgree(t *testing.T) {
	for _, name := range []string{"write", "shell"} {
		t.Run(name, func(t *testing.T) {
			// Local scope: same-name is allowed, so the dispatch order is what is
			// under test here (the remote-scope refusal has its own test file).
			probe := newScopedProbeManager("filesystem", mcpconfig.ExecutionScopeLocal, name)
			m := NewDefaultManager(probe)

			// What the model sees.
			var listed *ToolDescriptor
			for _, d := range m.ListTools() {
				if d.Name == name {
					copied := d
					listed = &copied
					break
				}
			}
			require.NotNil(t, listed, "%s must be listed exactly once", name)

			_, hasRemoteIdentity := listed.Metadata["mcp_name"]
			require.True(t, hasRemoteIdentity,
				"the listed %q carries an MCP identity, so the model believes it is remote", name)

			// What actually executes.
			_, _, _ = m.ExecuteWithMeta(context.Background(), name, map[string]interface{}{
				"file_path": "same-name-probe.txt",
				"content":   "probe",
				"command":   "echo probe",
			})

			require.Equal(t, []string{name}, probe.executed,
				"%q was listed as MCP-owned, so it must execute on that MCP server", name)
		})
	}
}

// TestReadFamilyIsConsistentlyLocal documents the one case that IS consistent:
// read tools are local in both paths, so a same-named remote read tool is simply
// unreachable.
func TestReadFamilyIsConsistentlyLocal(t *testing.T) {
	probe := newShadowProbeManager("view")
	m := NewDefaultManager(probe)

	var listed *ToolDescriptor
	for i := range m.ListTools() {
		d := m.ListTools()[i]
		if d.Name == "view" {
			listed = &d
			break
		}
	}
	require.NotNil(t, listed)
	_, hasRemoteIdentity := listed.Metadata["mcp_name"]
	require.False(t, hasRemoteIdentity,
		"a same-named remote `view` never reaches the list; the local one owns the name")

	_, _, _ = m.ExecuteWithMeta(context.Background(), "view", map[string]interface{}{
		"file_path": "/definitely/not/here",
	})
	require.Empty(t, probe.executed, "the remote view is unreachable")
}
