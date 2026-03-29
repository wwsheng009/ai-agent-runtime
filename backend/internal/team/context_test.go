package team

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContextBuilderIncludesTeammateSummary(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:     "mate-idle",
		TeamID: teamID,
		State:  TeammateStateIdle,
	})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:     "mate-busy",
		TeamID: teamID,
		State:  TeammateStateBusy,
	})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:     "mate-offline",
		TeamID: teamID,
		State:  TeammateStateOffline,
	})
	require.NoError(t, err)

	builder := NewContextBuilder(store)
	digest, err := builder.Build(ctx, teamID, "", 6)
	require.NoError(t, err)
	require.NotNil(t, digest)
	require.Contains(t, digest.Summary, "teammates: idle=1 busy=1 blocked=0 offline=1 total=3")
	require.Equal(t, 3, digest.MateCount)
}

func TestContextBuilderIncludesCurrentTaskInputs(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	taskID, err := store.CreateTask(ctx, Task{
		TeamID: teamID,
		Title:  "task-with-inputs",
		Inputs: []string{"spec.md", "notes.txt"},
		Status: TaskStatusRunning,
	})
	require.NoError(t, err)

	builder := NewContextBuilder(store)
	digest, err := builder.Build(ctx, teamID, taskID, 6)
	require.NoError(t, err)
	require.NotNil(t, digest)
	require.Contains(t, digest.Summary, "inputs:")
	require.Contains(t, digest.Summary, "spec.md")
}

func TestContextBuilderDoesNotPrefixTaskIDsWithHash(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	taskID, err := store.CreateTask(ctx, Task{
		ID:     "task_docs_agents",
		TeamID: teamID,
		Title:  "探索 docs/agents",
		Status: TaskStatusRunning,
	})
	require.NoError(t, err)

	builder := NewContextBuilder(store)
	digest, err := builder.Build(ctx, teamID, taskID, 6)
	require.NoError(t, err)
	require.NotNil(t, digest)
	assert.NotContains(t, digest.Summary, "#task_docs_agents")
	assert.Contains(t, digest.Summary, "探索 docs/agents")
}
