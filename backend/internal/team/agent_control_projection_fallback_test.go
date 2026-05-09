package team

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
)

type legacyProjectionStore struct {
	Store
}

func TestAgentControlMailboxRegistryFallbackUsesCombinedCursorAcrossTeams(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID1, err := store.CreateTeam(ctx, Team{ID: "team-fallback-mail-1"})
	require.NoError(t, err)
	teamID2, err := store.CreateTeam(ctx, Team{ID: "team-fallback-mail-2"})
	require.NoError(t, err)

	_, err = store.InsertMail(ctx, MailMessage{
		TeamID:    teamID1,
		FromAgent: "lead",
		ToAgent:   "member-1",
		Kind:      "info",
		Body:      "first team message",
		CreatedAt: time.Unix(100, 0).UTC(),
	})
	require.NoError(t, err)

	registry := NewAgentControlMailboxRegistry(legacyProjectionStore{Store: store})
	firstBatch, err := registry.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Workflow: agentcontrol.WorkflowSpawnTeam,
	})
	require.NoError(t, err)
	require.Len(t, firstBatch, 1)
	require.Equal(t, teamID1, firstBatch[0].TeamID)
	require.Equal(t, int64(1), firstBatch[0].TeamSeq)
	require.Greater(t, firstBatch[0].Seq, firstBatch[0].TeamSeq)

	_, err = store.InsertMail(ctx, MailMessage{
		TeamID:    teamID2,
		FromAgent: "lead",
		ToAgent:   "member-2",
		Kind:      "info",
		Body:      "second team message",
		CreatedAt: time.Unix(101, 0).UTC(),
	})
	require.NoError(t, err)

	afterFirst, err := registry.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Workflow: agentcontrol.WorkflowSpawnTeam,
		AfterSeq: firstBatch[0].Seq,
	})
	require.NoError(t, err)
	require.Len(t, afterFirst, 1)
	require.Equal(t, teamID2, afterFirst[0].TeamID)
	require.Equal(t, int64(1), afterFirst[0].TeamSeq)
	require.Greater(t, afterFirst[0].Seq, firstBatch[0].Seq)
}

func TestAgentControlTaskGraphFallbackUsesCombinedCursorAcrossTeams(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID1, err := store.CreateTeam(ctx, Team{ID: "team-fallback-graph-1"})
	require.NoError(t, err)
	teamID2, err := store.CreateTeam(ctx, Team{ID: "team-fallback-graph-2"})
	require.NoError(t, err)

	_, err = store.AppendTeamEvent(ctx, TeamEvent{
		TeamID:    teamID1,
		Type:      "task.completed",
		Timestamp: time.Unix(200, 0).UTC(),
		Payload: map[string]interface{}{
			"task_id": "task-1",
		},
	})
	require.NoError(t, err)

	registry := NewAgentControlTaskRegistry(legacyProjectionStore{Store: store})
	firstBatch, err := registry.ListAgentControlTaskGraphEvents(ctx, agentcontrol.TaskGraphEventFilter{
		Workflow: agentcontrol.WorkflowSpawnTeam,
	})
	require.NoError(t, err)
	require.Len(t, firstBatch, 1)
	require.Equal(t, teamID1, firstBatch[0].TeamID)
	require.Equal(t, int64(1), firstBatch[0].TeamSeq)
	require.Greater(t, firstBatch[0].Seq, firstBatch[0].TeamSeq)

	_, err = store.AppendTeamEvent(ctx, TeamEvent{
		TeamID:    teamID2,
		Type:      "task.completed",
		Timestamp: time.Unix(201, 0).UTC(),
		Payload: map[string]interface{}{
			"task_id": "task-2",
		},
	})
	require.NoError(t, err)

	afterFirst, err := registry.ListAgentControlTaskGraphEvents(ctx, agentcontrol.TaskGraphEventFilter{
		Workflow: agentcontrol.WorkflowSpawnTeam,
		AfterSeq: firstBatch[0].Seq,
	})
	require.NoError(t, err)
	require.Len(t, afterFirst, 1)
	require.Equal(t, teamID2, afterFirst[0].TeamID)
	require.Equal(t, int64(1), afterFirst[0].TeamSeq)
	require.Greater(t, afterFirst[0].Seq, firstBatch[0].Seq)
}
