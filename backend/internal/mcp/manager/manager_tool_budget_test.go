package manager

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
)

// budgetTools builds n well-formed tool definitions, so only the per-server
// budget can reject any of them.
func budgetTools(count int) []*protocol.Tool {
	tools := make([]*protocol.Tool, count)
	for index := range tools {
		tools[index] = &protocol.Tool{
			Name:        fmt.Sprintf("tool_%04d", index),
			Description: "bounded",
			InputSchema: map[string]interface{}{"type": "object"},
		}
	}
	return tools
}

// TestLoadToolsAppliesConfiguredMaxTools proves the §8.7.3 budget is wired from
// configuration through to the registry, not just declared as a struct field:
// without this, an operator raising maxTools would still get a silently
// truncated tool surface.
func TestLoadToolsAppliesConfiguredMaxTools(t *testing.T) {
	cfg := &config.Config{
		MCPServers: map[string]config.MCPConfig{
			"big": {
				Name:     "big",
				Type:     "stdio",
				Enabled:  true,
				MaxTools: 4,
			},
		},
	}
	mgr := newTestManager(cfg)
	mgr.started = true
	mgr.registry.RegisterClient("big", &fakeClient{name: "big"})

	cli := &fakeClient{name: "big", connected: true, tools: budgetTools(10)}
	mgr.loadTools(context.Background(), cli, "big", mgr.currentGeneration())

	require.Len(t, mgr.registry.ListToolsByMCP("big"), 4,
		"configured maxTools must bound the exposed surface")
	quarantined := mgr.registry.ListQuarantinedTools()
	require.Len(t, quarantined, 6, "the rejected remainder must stay diagnosable")
	for _, record := range quarantined {
		require.Contains(t, record.Error, "limit")
	}
}

// TestLoadToolsWithoutMaxToolsKeepsDefaultBudget pins the other direction: an
// unset maxTools means the default, and an ordinary server is unaffected.
func TestLoadToolsWithoutMaxToolsKeepsDefaultBudget(t *testing.T) {
	cfg := &config.Config{
		MCPServers: map[string]config.MCPConfig{
			"plain": {Name: "plain", Type: "stdio", Enabled: true},
		},
	}
	mgr := newTestManager(cfg)
	mgr.started = true
	mgr.registry.RegisterClient("plain", &fakeClient{name: "plain"})

	cli := &fakeClient{name: "plain", connected: true, tools: budgetTools(300)}
	mgr.loadTools(context.Background(), cli, "plain", mgr.currentGeneration())

	// 300 advertised tools against the default 256 budget: the default applies,
	// proving an unset maxTools does not silently remove the bound entirely.
	require.Len(t, mgr.registry.ListToolsByMCP("plain"), registry.MaxToolsPerServer,
		"an unset maxTools must fall back to the registry default")
}

// TestLoadToolsClearsRemovedMaxTools pins the reconnect semantics: a reconnect
// does not reset the registry, so dropping maxTools from the config must clear
// the previously applied override instead of leaving the server truncated.
func TestLoadToolsClearsRemovedMaxTools(t *testing.T) {
	cfg := &config.Config{
		MCPServers: map[string]config.MCPConfig{
			"shrunk": {Name: "shrunk", Type: "stdio", Enabled: true},
		},
	}
	mgr := newTestManager(cfg)
	mgr.started = true
	mgr.registry.RegisterClient("shrunk", &fakeClient{name: "shrunk"})
	// A stale override from an earlier config generation.
	mgr.registry.SetServerToolLimit("shrunk", 4)

	cli := &fakeClient{name: "shrunk", connected: true, tools: budgetTools(10)}
	mgr.loadTools(context.Background(), cli, "shrunk", mgr.currentGeneration())

	require.Len(t, mgr.registry.ListToolsByMCP("shrunk"), 10,
		"removing maxTools from the config must release the stale override")
}
