package tools

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	mcpregistry "github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
)

// panickyStatusManager embeds a nil manager.Manager to satisfy the interface
// without implementing GetMCPStatus — exactly the shape a partial wrapper or a
// test double takes. Calling through panics instead of returning an error.
type panickyStatusManager struct {
	manager.Manager
	tools []string
}

func (m *panickyStatusManager) ListTools() []*mcpregistry.ToolInfo {
	infos := make([]*mcpregistry.ToolInfo, 0, len(m.tools))
	for _, name := range m.tools {
		infos = append(infos, &mcpregistry.ToolInfo{
			MCPName: "partial",
			Enabled: true,
			Tool: &protocol.Tool{
				Name:        name,
				Description: "partial " + name,
				InputSchema: map[string]interface{}{"type": "object"},
			},
		})
	}
	return infos
}

// TestCollisionGuardSurvivesManagerPanic locks the nil-embedded-interface fix.
//
// The collision guard calls GetMCPStatus while listing tools. A manager that
// embeds a nil interface (common for partial wrappers and test doubles) panics
// there rather than returning an error. Before the recover, that panic propagated
// out of ListTools and killed the session — a diagnostic guard must never do that.
func TestCollisionGuardSurvivesManagerPanic(t *testing.T) {
	probe := &panickyStatusManager{tools: []string{"write"}}
	m := NewDefaultManager(probe)

	require.NotPanics(t, func() {
		_ = m.ListTools()
	}, "a panicking GetMCPStatus must not take down the tool listing")

	// Unknown scope is treated as "not remote", so the guard stays out of the way
	// and the entry is listed normally.
	require.Contains(t, listingNames(m), "write")
	require.Empty(t, m.RemoteNameCollisions())
}
