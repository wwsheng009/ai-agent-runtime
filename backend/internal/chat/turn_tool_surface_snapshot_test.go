package chat

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	runtimeagent "github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestRuntimeTurnToolSurfaceSnapshotReusesStableSurfaceAcrossTurns(t *testing.T) {
	runtimeAgent := runtimeagent.NewAgent(&runtimeagent.Config{Name: "test-agent"}, nil)
	actor := &SessionActor{
		id:    "session-stable-tools",
		agent: runtimeAgent,
		state: &RuntimeState{
			SessionID:     "session-stable-tools",
			Status:        SessionRunning,
			CurrentTurnID: "turn-1",
			CurrentRunMeta: &team.RunMeta{
				PermissionMode: "plan",
			},
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
	require.NotEmpty(t, actor.State().StableToolSurfaceBinding)
}

func TestRuntimeTurnToolSurfaceSnapshotInvalidatesAcrossTurnsWhenBindingChanges(t *testing.T) {
	runtimeAgent := runtimeagent.NewAgent(&runtimeagent.Config{Name: "test-agent"}, nil)
	actor := &SessionActor{
		id:    "session-binding-change",
		agent: runtimeAgent,
		state: &RuntimeState{
			SessionID:     "session-binding-change",
			Status:        SessionRunning,
			CurrentTurnID: "turn-1",
			CurrentRunMeta: &team.RunMeta{
				PermissionMode: "plan",
			},
		},
	}

	first := actor.turnToolSurfaceSnapshot("turn-1")
	require.NoError(t, first.SaveTurnToolSurface(context.Background(), []types.ToolDefinition{{Name: "view"}}))
	state := actor.State()
	require.True(t, state.StableToolSurfaceSet)
	require.NotEmpty(t, state.StableToolSurfaceBinding)
	require.NotEmpty(t, state.StableToolSurfaceFingerprint)

	require.NoError(t, actor.updateState(context.Background(), func(state *RuntimeState) error {
		state.CurrentTurnID = "turn-2"
		state.CurrentRunMeta = &team.RunMeta{PermissionMode: "accept_edits"}
		resetFrozenTurnTools(state)
		return nil
	}))

	tools, cached, err := actor.turnToolSurfaceSnapshot("turn-2").LoadTurnToolSurface(context.Background())
	require.NoError(t, err)
	require.False(t, cached)
	require.Nil(t, tools)
	state = actor.State()
	require.False(t, state.StableToolSurfaceSet)
	require.Empty(t, state.StableToolSurface)
	require.Empty(t, state.StableToolSurfaceBinding)
	require.Empty(t, state.StableToolSurfaceFingerprint)
}

func TestRuntimeTurnToolSurfaceSnapshotKeepsFrozenSurfaceWhenBindingChangesMidTurn(t *testing.T) {
	runtimeAgent := runtimeagent.NewAgent(&runtimeagent.Config{Name: "test-agent"}, nil)
	actor := &SessionActor{
		id:    "session-mid-turn-binding-change",
		agent: runtimeAgent,
		state: &RuntimeState{
			SessionID:     "session-mid-turn-binding-change",
			Status:        SessionRunning,
			CurrentTurnID: "turn-1",
			CurrentRunMeta: &team.RunMeta{
				PermissionMode: "plan",
			},
		},
	}
	snapshot := actor.turnToolSurfaceSnapshot("turn-1")
	require.NoError(t, snapshot.SaveTurnToolSurface(context.Background(), []types.ToolDefinition{{Name: "view"}}))

	require.NoError(t, actor.updateState(context.Background(), func(state *RuntimeState) error {
		state.CurrentRunMeta = &team.RunMeta{PermissionMode: "accept_edits"}
		return nil
	}))

	tools, cached, err := snapshot.LoadTurnToolSurface(context.Background())
	require.NoError(t, err)
	require.True(t, cached)
	require.Equal(t, []string{"view"}, []string{tools[0].Name})
	require.True(t, actor.State().StableToolSurfaceSet, "mid-turn policy changes must not rewrite the active tools prefix")
}

func TestRuntimeTurnToolSurfaceSnapshotSaveIsFreezeOnce(t *testing.T) {
	actor := &SessionActor{
		id: "session-freeze-once",
		state: &RuntimeState{
			SessionID:     "session-freeze-once",
			Status:        SessionRunning,
			CurrentTurnID: "turn-1",
		},
	}
	snapshot := actor.turnToolSurfaceSnapshot("turn-1")
	require.NoError(t, snapshot.SaveTurnToolSurface(context.Background(), []types.ToolDefinition{{Name: "view"}}))
	firstFingerprint := actor.State().StableToolSurfaceFingerprint
	require.NotEmpty(t, firstFingerprint)

	require.NoError(t, snapshot.SaveTurnToolSurface(context.Background(), []types.ToolDefinition{{Name: "write"}}))
	state := actor.State()
	require.Equal(t, "view", state.StableToolSurface[0].Name)
	require.Equal(t, "view", state.FrozenTurnTools[0].Name)
	require.Equal(t, firstFingerprint, state.StableToolSurfaceFingerprint)
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

func TestRuntimeTurnToolSurfaceSnapshotReturnsSingleOwnedProjection(t *testing.T) {
	actor := &SessionActor{
		id: "session-owned-tools",
		state: &RuntimeState{
			SessionID:            "session-owned-tools",
			Status:               SessionRunning,
			CurrentTurnID:        "turn-owned",
			StableToolSurfaceSet: true,
			StableToolSurface: []types.ToolDefinition{{
				Name: "shell",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"command": map[string]interface{}{"type": "string"},
					},
				},
			}},
		},
	}

	tools, cached, err := actor.turnToolSurfaceSnapshot("turn-owned").LoadTurnToolSurface(context.Background())
	require.NoError(t, err)
	require.True(t, cached)
	require.Len(t, tools, 1)
	tools[0].Name = "changed"
	tools[0].Parameters["type"] = "changed"

	actor.mu.RLock()
	defer actor.mu.RUnlock()
	require.Equal(t, "shell", actor.state.StableToolSurface[0].Name)
	require.Equal(t, "object", actor.state.StableToolSurface[0].Parameters["type"])
}

func TestRuntimeTurnToolSurfaceSnapshotSharesIdenticalStoredSurfaces(t *testing.T) {
	actor := &SessionActor{
		id: "session-shared-tools",
		state: &RuntimeState{
			SessionID:     "session-shared-tools",
			Status:        SessionRunning,
			CurrentTurnID: "turn-shared",
		},
	}
	snapshot := actor.turnToolSurfaceSnapshot("turn-shared")
	require.NoError(t, snapshot.SaveTurnToolSurface(context.Background(), []types.ToolDefinition{{Name: "shell"}}))

	state := actor.State()
	require.True(t, runtimeToolDefinitionsShareBacking(state.StableToolSurface, state.FrozenTurnTools))
	state.StableToolSurface[0].Name = "changed"
	require.Equal(t, "changed", state.FrozenTurnTools[0].Name)

	actor.mu.RLock()
	defer actor.mu.RUnlock()
	require.Equal(t, "shell", actor.state.StableToolSurface[0].Name)
	require.True(t, runtimeToolDefinitionsShareBacking(actor.state.StableToolSurface, actor.state.FrozenTurnTools))
}
