package tools

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
)

type scalingMCPManager struct {
	stubMCPManager
	tools       []*registry.ToolInfo
	statusCalls int
	findCalls   int
}

func newScalingMCPManager(count int) *scalingMCPManager {
	m := &scalingMCPManager{}
	for i := 0; i < count; i++ {
		m.tools = append(m.tools, &registry.ToolInfo{
			MCPName: "remote-dev",
			Enabled: true,
			Tool: &protocol.Tool{
				Name:        fmt.Sprintf("tool_%02d", i),
				Description: "scaling tool",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"value": map[string]interface{}{"type": "string"},
					},
				},
			},
		})
	}
	return m
}

func (m *scalingMCPManager) ListTools() []*registry.ToolInfo {
	return append([]*registry.ToolInfo(nil), m.tools...)
}

func (m *scalingMCPManager) FindTool(name string) (*registry.ToolInfo, error) {
	m.findCalls++
	for _, info := range m.tools {
		if info.Tool != nil && info.Tool.Name == name {
			return info, nil
		}
	}
	return nil, fmt.Errorf("tool not found: %s", name)
}

func (m *scalingMCPManager) GetMCPStatus(name string) (*config.MCPStatus, error) {
	m.statusCalls++
	return &config.MCPStatus{
		Name:             name,
		TrustLevel:       config.MCPTrustLevelTrustedRemote,
		ExecutionMode:    "local_mcp",
		ExecutionScope:   config.ExecutionScopeRemote,
		MaxParallelCalls: 2,
		Enabled:          true,
	}, nil
}

// TestAgentAdapterListToolsDoesNotAddPerToolMCPStatusQuery pins the hot-path
// invariant behind the 45 GB ListTools allocation cluster in production pprof:
// one MCP server must not be status-queried once per tool by either the
// collision guard or the adapter.
func TestAgentAdapterListToolsDoesNotAddPerToolMCPStatusQuery(t *testing.T) {
	const toolCount = 48
	fake := newScalingMCPManager(toolCount)
	adapter := NewAgentAdapter(NewDefaultManager(fake))

	tools := adapter.ListTools()
	remoteTools := make(map[string]int)
	for _, tool := range tools {
		if tool.MCPName != "remote-dev" {
			continue
		}
		remoteTools[tool.Name]++
		require.Equal(t, 2, tool.MaxParallelCalls)
		require.Equal(t, "trusted_remote", tool.MCPTrustLevel)
		require.Equal(t, "remote", tool.ExecutionScope)
	}
	require.Len(t, remoteTools, toolCount)
	for name, count := range remoteTools {
		require.Equal(t, 1, count, "tool %s must not be duplicated", name)
	}
	// One status resolution is allowed for the collision guard's server-scope
	// cache and one for the adapter's governance projection. Anything more is the
	// old per-tool fan-out.
	require.LessOrEqual(t, fake.statusCalls, 2, "one MCP server must cost at most two status calls per list")
	require.Zero(t, fake.findCalls, "ListTools must reuse MCP identity metadata, not scan FindTool per descriptor")
}

func BenchmarkAgentAdapterListTools(b *testing.B) {
	const toolCount = 48
	fake := newScalingMCPManager(toolCount)
	adapter := NewAgentAdapter(NewDefaultManager(fake))

	b.ReportAllocs()
	b.ReportMetric(float64(toolCount), "tools")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tools := adapter.ListTools()
		if len(tools) < toolCount {
			b.Fatalf("tools = %d, want at least %d MCP tools", len(tools), toolCount)
		}
	}
}
