package team

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
)

func TestSQLiteStoreTeamEventsFilters(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	base := time.Now().UTC().Add(-2 * time.Hour)
	seq1, err := store.AppendTeamEvent(ctx, TeamEvent{
		TeamID:    teamID,
		Type:      "task.completed",
		Payload:   map[string]interface{}{"task_id": "task-1"},
		Timestamp: base,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, seq1)

	seq2, err := store.AppendTeamEvent(ctx, TeamEvent{
		TeamID:    teamID,
		Type:      "task.failed",
		Payload:   map[string]interface{}{"task_id": "task-2"},
		Timestamp: base.Add(30 * time.Minute),
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, seq2)

	seq3, err := store.AppendTeamEvent(ctx, TeamEvent{
		TeamID:    teamID,
		Type:      "team.summary",
		Payload:   map[string]interface{}{"summary": "all done"},
		Timestamp: base.Add(90 * time.Minute),
	})
	require.NoError(t, err)
	require.EqualValues(t, 3, seq3)

	all, err := store.ListTeamEvents(ctx, TeamEventFilter{TeamID: teamID})
	require.NoError(t, err)
	require.Len(t, all, 3)
	require.Equal(t, int64(1), all[0].Seq)
	require.Equal(t, "task.completed", all[0].Type)
	require.Equal(t, "task-1", all[0].Payload["task_id"])

	afterFirst, err := store.ListTeamEvents(ctx, TeamEventFilter{
		TeamID:   teamID,
		AfterSeq: seq1,
	})
	require.NoError(t, err)
	require.Len(t, afterFirst, 2)
	require.Equal(t, int64(2), afterFirst[0].Seq)

	taskOnly, err := store.ListTeamEvents(ctx, TeamEventFilter{
		TeamID:    teamID,
		EventType: "task.*",
	})
	require.NoError(t, err)
	require.Len(t, taskOnly, 2)

	since := base.Add(30 * time.Minute)
	sinceOnly, err := store.ListTeamEvents(ctx, TeamEventFilter{
		TeamID: teamID,
		Since:  &since,
	})
	require.NoError(t, err)
	require.Len(t, sinceOnly, 2)

	until := base.Add(45 * time.Minute)
	untilOnly, err := store.ListTeamEvents(ctx, TeamEventFilter{
		TeamID: teamID,
		Until:  &until,
	})
	require.NoError(t, err)
	require.Len(t, untilOnly, 2)
	require.Equal(t, int64(1), untilOnly[0].Seq)
	require.Equal(t, int64(2), untilOnly[1].Seq)
}

// TestSQLiteStoreTeamEventsMirrorWakeRegistry verifies P6-1: every durable
// team event is mirrored into the unified AgentControl wake registry
// (kind=team) and wakes registered watchers immediately, so wait_team can be
// event-driven instead of short-interval polling.
func TestSQLiteStoreTeamEventsMirrorWakeRegistry(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	wakeCh, unwatch := store.WatchAgentControlWake(ctx, agentcontrol.WakeFilter{
		Workflow: agentcontrol.WorkflowSpawnTeam,
		Kind:     agentcontrol.WakeKindTeam,
		TeamID:   teamID,
	})
	defer unwatch()

	seq, err := store.AppendTeamEvent(ctx, TeamEvent{
		TeamID:  teamID,
		Type:    "team.completed",
		Payload: map[string]interface{}{"summary": "done"},
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, seq)

	select {
	case wake := <-wakeCh:
		require.Equal(t, agentcontrol.WorkflowSpawnTeam, wake.Workflow)
		require.Equal(t, agentcontrol.WakeKindTeam, wake.Kind)
		require.Equal(t, teamID, wake.TeamID)
		require.EqualValues(t, seq, wake.TeamSeq)
		require.Equal(t, "team.completed", wake.EventKind)
		require.Greater(t, wake.Seq, int64(0))
	case <-time.After(2 * time.Second):
		t.Fatal("team event did not wake the team wake registry watcher")
	}

	last, err := store.LastAgentControlWakeSeq(ctx, agentcontrol.WakeFilter{
		Workflow: agentcontrol.WorkflowSpawnTeam,
		Kind:     agentcontrol.WakeKindTeam,
		TeamID:   teamID,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, last, int64(1), "wake high-water mark must include mirrored team event")

	// A kind-scoped filter for mailbox/task must not see team wake rows.
	mailboxLast, err := store.LastAgentControlWakeSeq(ctx, agentcontrol.WakeFilter{
		Workflow: agentcontrol.WorkflowSpawnTeam,
		Kind:     agentcontrol.WakeKindMailbox,
		TeamID:   teamID,
	})
	require.NoError(t, err)
	require.Zero(t, mailboxLast)
}
