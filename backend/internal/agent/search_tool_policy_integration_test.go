package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestVisibleSearchToolExecutesOutsideDerivedAllowlists(t *testing.T) {
	manager := &mockSearchCatalogMCPManager{}
	whitelist := make([]string, 0, len(manager.ListTools()))
	policyAllowlist := make([]string, 0, len(manager.ListTools()))
	for _, info := range manager.ListTools() {
		whitelist = append(whitelist, info.Name)
		if info.Name != "deferred_helper" {
			policyAllowlist = append(policyAllowlist, info.Name)
		}
	}

	agent := &Agent{
		config:     &Config{Name: "search-agent", Model: "test-model"},
		mcpManager: manager,
	}
	agent.SetToolExecutionPolicy(NewToolExecutionPolicy(policyAllowlist, false))
	loop := NewReActLoop(agent, llm.NewLLMRuntime(nil), &LoopReActConfig{EnableToolCalls: true})

	ctx := ensureTurnToolSurfaceSnapshot(context.Background())
	surface, err := loop.getAvailableTools(ctx, "investigate repository architecture and tooling options", whitelist)
	require.NoError(t, err)
	require.Contains(t, toolDefinitionNames(surface), toolSearchName)
	require.NotContains(t, whitelist, toolSearchName)
	require.NotContains(t, policyAllowlist, toolSearchName)

	snapshot, ok := TurnToolSurfaceSnapshotFromContext(ctx)
	require.True(t, ok)
	require.NoError(t, snapshot.SaveTurnToolSurface(ctx, surface))

	results, err := loop.act(ctx, "trace_search", "session_search", 1, 0, nil, []types.ToolCall{{
		ID: "call_search", Name: toolSearchName,
		Args: map[string]interface{}{"query": "filler_00 deferred_helper", "limit": 10},
	}}, whitelist)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Empty(t, results[0].Error)

	var found toolkit.SearchSnapshot
	require.NoError(t, json.Unmarshal([]byte(results[0].Output.(string)), &found))
	require.NotEmpty(t, found.Results)
	resultNames := make([]string, 0, len(found.Results))
	for _, result := range found.Results {
		resultNames = append(resultNames, result.Name)
	}
	require.Contains(t, resultNames, "filler_00")
	require.NotContains(t, resultNames, "deferred_helper", "policy-denied tools must not leak through search")
}
