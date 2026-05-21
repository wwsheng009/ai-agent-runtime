package chat

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestRuntimeTurnToolSurfaceSnapshotReusesStableSurfaceAcrossTurns(t *testing.T) {
	actor := &SessionActor{
		id: "session-stable-tools",
		state: &RuntimeState{
			SessionID:     "session-stable-tools",
			Status:        SessionRunning,
			CurrentTurnID: "turn-1",
		},
	}

	first := actor.turnToolSurfaceSnapshot("turn-1")
	require.NotNil(t, first)
	require.NoError(t, first.SaveTurnToolSurface(context.Background(), []types.ToolDefinition{
		{Name: "get_goal"},
		{Name: "update_goal"},
	}))

	require.NoError(t, actor.updateState(context.Background(), func(state *RuntimeState) error {
		state.CurrentTurnID = "turn-2"
		resetFrozenTurnTools(state)
		return nil
	}))

	second := actor.turnToolSurfaceSnapshot("turn-2")
	tools, cached, err := second.LoadTurnToolSurface(context.Background())
	require.NoError(t, err)
	require.True(t, cached)
	require.Len(t, tools, 2)
	require.Equal(t, "get_goal", tools[0].Name)
	require.Equal(t, "update_goal", tools[1].Name)
}

func TestRuntimeTurnToolSurfaceSnapshotPreservesEmptyParameterProperties(t *testing.T) {
	actor := &SessionActor{
		id: "session-stable-tool-schema",
		state: &RuntimeState{
			SessionID:     "session-stable-tool-schema",
			Status:        SessionRunning,
			CurrentTurnID: "turn-1",
		},
	}

	snapshot := actor.turnToolSurfaceSnapshot("turn-1")
	require.NotNil(t, snapshot)
	require.NoError(t, snapshot.SaveTurnToolSurface(context.Background(), []types.ToolDefinition{
		{
			Name:        "get_goal",
			Description: "Read goal",
			Parameters: map[string]interface{}{
				"type":                 "object",
				"properties":           map[string]interface{}{},
				"additionalProperties": false,
			},
		},
		{
			Name:        "get_goal_from_old_state",
			Description: "Read goal from old persisted state",
			Parameters: map[string]interface{}{
				"type":                 "object",
				"properties":           nil,
				"additionalProperties": false,
			},
		},
	}))

	tools, cached, err := snapshot.LoadTurnToolSurface(context.Background())
	require.NoError(t, err)
	require.True(t, cached)
	require.Len(t, tools, 2)
	for _, tool := range tools {
		params := tool.Parameters
		require.Equal(t, "object", params["type"])
		require.Equal(t, false, params["additionalProperties"])
		require.IsType(t, map[string]interface{}{}, params["properties"])
		require.NotNil(t, params["properties"])
	}
}
