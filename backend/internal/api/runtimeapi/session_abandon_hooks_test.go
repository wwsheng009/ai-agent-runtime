package runtimeapi

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

func newAPISuspendedTurnAbandonFixture(t *testing.T, name string) (*Handler, *team.SQLiteStore) {
	t.Helper()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), name+".db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	handler.SetTeamStore(store)
	return handler, store
}

// TestAPICancelSuspendedTurnTeamCancelsTasksAndParks pins the API twin of the
// CLI suspend cascade: the abandon path must cancel non-terminal tasks (each
// with a task.cancelled lifecycle event) and idle busy teammates before parking
// the team, not just flip the status.
func TestAPICancelSuspendedTurnTeamCancelsTasksAndParks(t *testing.T) {
	handler, store := newAPISuspendedTurnAbandonFixture(t, "api-abandon-cancel")
	ctx := context.Background()
	now := time.Now().UTC()
	_, err := store.CreateTeam(ctx, team.Team{
		ID:            "team-abandon",
		LeadSessionID: "lead-abandon",
		Status:        team.TeamStatusActive,
		CreatedAt:     now,
		UpdatedAt:     now,
	})
	require.NoError(t, err)

	assignee := "mate-abandon"
	_, err = store.CreateTask(ctx, team.Task{
		ID: "task-running", TeamID: "team-abandon", Title: "running",
		Status: team.TaskStatusRunning, Assignee: &assignee, CreatedAt: now, UpdatedAt: now,
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, team.Task{
		ID: "task-pending", TeamID: "team-abandon", Title: "pending",
		Status: team.TaskStatusPending, CreatedAt: now, UpdatedAt: now,
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, team.Task{
		ID: "task-done", TeamID: "team-abandon", Title: "done",
		Status: team.TaskStatusDone, CreatedAt: now, UpdatedAt: now,
	})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, team.Teammate{
		ID: assignee, TeamID: "team-abandon", Name: "mate",
		State: team.TeammateStateBusy, CreatedAt: now, UpdatedAt: now,
	})
	require.NoError(t, err)

	require.NoError(t, handler.cancelSuspendedTurnTeam(ctx, "team-abandon", "test abandon"))

	record, err := store.GetTeam(ctx, "team-abandon")
	require.NoError(t, err)
	require.NotNil(t, record)
	require.Equal(t, team.TeamStatusPaused, record.Status)

	running, err := store.GetTask(ctx, "task-running")
	require.NoError(t, err)
	require.NotNil(t, running)
	require.Equal(t, team.TaskStatusCancelled, running.Status)
	pending, err := store.GetTask(ctx, "task-pending")
	require.NoError(t, err)
	require.NotNil(t, pending)
	require.Equal(t, team.TaskStatusCancelled, pending.Status)
	done, err := store.GetTask(ctx, "task-done")
	require.NoError(t, err)
	require.NotNil(t, done)
	require.Equal(t, team.TaskStatusDone, done.Status, "terminal tasks keep their history")

	mate, err := store.GetTeammate(ctx, assignee)
	require.NoError(t, err)
	require.NotNil(t, mate)
	require.Equal(t, team.TeammateStateIdle, mate.State)

	events, err := store.ListTeamEvents(ctx, team.TeamEventFilter{TeamID: "team-abandon", EventType: "task.cancelled"})
	require.NoError(t, err)
	require.Len(t, events, 2, "each cancelled task gets one lifecycle event")
	for _, event := range events {
		require.Equal(t, "test abandon", event.Payload["reason"])
	}
}

// TestAPICancelSuspendedTurnTeamIdempotentRetry pins the retry contract: a team
// already paused by a previous partial attempt still gets its lingering tasks
// cancelled, and a fully converged second call stays a harmless no-op.
func TestAPICancelSuspendedTurnTeamIdempotentRetry(t *testing.T) {
	handler, store := newAPISuspendedTurnAbandonFixture(t, "api-abandon-retry")
	ctx := context.Background()
	now := time.Now().UTC()
	_, err := store.CreateTeam(ctx, team.Team{
		ID: "team-abandon-retry", LeadSessionID: "lead", Status: team.TeamStatusPaused,
		CreatedAt: now, UpdatedAt: now,
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, team.Task{
		ID: "task-left", TeamID: "team-abandon-retry", Title: "left",
		Status: team.TaskStatusRunning, CreatedAt: now, UpdatedAt: now,
	})
	require.NoError(t, err)

	require.NoError(t, handler.cancelSuspendedTurnTeam(ctx, "team-abandon-retry", "retry"))
	left, err := store.GetTask(ctx, "task-left")
	require.NoError(t, err)
	require.NotNil(t, left)
	require.Equal(t, team.TaskStatusCancelled, left.Status, "legacy paused team still gets task cleanup")

	require.NoError(t, handler.cancelSuspendedTurnTeam(ctx, "team-abandon-retry", "retry"))
}

// TestAPICancelSuspendedTurnTeamUnwiredStore pins the unwired degradation: no
// team store means nothing to cascade and never an error.
func TestAPICancelSuspendedTurnTeamUnwiredStore(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	require.NoError(t, handler.cancelSuspendedTurnTeam(context.Background(), "team-missing", "unwired"))
}
