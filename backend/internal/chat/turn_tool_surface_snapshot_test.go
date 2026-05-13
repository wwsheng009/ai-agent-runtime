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
